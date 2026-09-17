/*
Copyright 2026 Sayak Mukhopadhyay.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/recorder"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	kflaredv1alpha1 "github.com/kode-blox/kflared/api/v1alpha1"
	cfclient "github.com/kode-blox/kflared/internal/cloudflare"
	"github.com/kode-blox/kflared/internal/planner"
)

const (
	bindingFinalizer         = "kflared.kodeblox.com/tunnel-cleanup"
	traefikControllerName    = "traefik.io/gateway-controller"
	defaultCloudflaredImage  = "cloudflare/cloudflared:2026.8.3"
	ownerUIDLabel            = "kflared.kodeblox.com/binding-uid"
	managedByLabel           = "app.kubernetes.io/managed-by"
	managedByValue           = "kflared"
	dnsEndpointAPIVersion    = "externaldns.k8s.io/v1alpha1"
	dnsEndpointKind          = "DNSEndpoint"
	tunnelCNAMEZone          = "cfargotunnel.com"
	defaultConnectorReplicas = int32(2)
	maxConnectorReplicas     = int32(10)
)

var errProviderStatusUnknown = errors.New("CloudflareProvider status is not currently known")

// CloudflareTunnelBindingReconciler reconciles a CloudflareTunnelBinding object.
type CloudflareTunnelBindingReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	RESTMapper       apiMeta.RESTMapper
	Recorder         recorder.EventRecorder
	Cloudflare       cfclient.Factory
	SystemNamespace  string
	CloudflaredImage string
}

// +kubebuilder:rbac:groups=kflared.kodeblox.com,resources=cloudflaretunnelbindings,verbs=get;list;watch
// +kubebuilder:rbac:groups=kflared.kodeblox.com,resources=cloudflaretunnelbindings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kflared.kodeblox.com,resources=cloudflaretunnelbindings/finalizers,verbs=update
// +kubebuilder:rbac:groups=kflared.kodeblox.com,resources=cloudflareproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=gatewayclasses;gateways;httproutes;referencegrants,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces;services,verbs=get;list;watch
// +kubebuilder:rbac:groups=externaldns.k8s.io,resources=dnsendpoints,verbs=get;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *CloudflareTunnelBindingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	binding := &kflaredv1alpha1.CloudflareTunnelBinding{}
	if err := r.Get(ctx, req.NamespacedName, binding); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !binding.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, binding)
	}

	provider, hostnames, origin, result, err := r.validateAndPlan(ctx, binding)
	if err != nil || result != nil {
		if result != nil {
			if binding.Status.TunnelID != "" {
				if deprogramErr := r.deprogramTunnel(ctx, binding); deprogramErr != nil {
					return *result, errors.Join(err, deprogramErr)
				}
			}
			return *result, err
		}
		if errors.Is(err, errProviderStatusUnknown) {
			return ctrl.Result{}, r.reportOperationalFailure(ctx, binding, kflaredv1alpha1.BindingConditionReady, "ProviderStatusUnknown", "The referenced CloudflareProvider status is not currently known", err)
		}
		return ctrl.Result{}, r.reportOperationalFailure(ctx, binding, kflaredv1alpha1.BindingConditionProgrammed, "TunnelReconciliationFailed", "Tunnel reconciliation could not complete", err)
	}

	if !controllerutil.ContainsFinalizer(binding, bindingFinalizer) {
		controllerutil.AddFinalizer(binding, bindingFinalizer)
		if err := r.Update(ctx, binding); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	apiToken, err := r.readAPIToken(ctx, provider)
	if err != nil {
		return ctrl.Result{}, r.reportOperationalFailure(ctx, binding, kflaredv1alpha1.BindingConditionConnectorReady, "ConnectorReconciliationFailed", "Connector reconciliation could not complete", err)
	}
	cloudflareClient := r.cloudflare().New(apiToken, provider.Spec.AccountID)

	clusterNamespace := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: metav1.NamespaceSystem}, clusterNamespace); err != nil {
		cause := fmt.Errorf("read cluster identity: %w", err)
		return ctrl.Result{}, r.reportOperationalFailure(ctx, binding, kflaredv1alpha1.BindingConditionProgrammed, "TunnelReconciliationFailed", "Tunnel reconciliation could not complete", cause)
	}
	tunnelName := planner.TunnelName(clusterNamespace.UID, binding)
	tunnel, err := r.ensureTunnel(ctx, cloudflareClient, binding, tunnelName)
	if err != nil {
		return ctrl.Result{}, r.reportOperationalFailure(ctx, binding, kflaredv1alpha1.BindingConditionProgrammed, "TunnelReconciliationFailed", "Tunnel reconciliation could not complete", err)
	}

	desiredIngress := planner.IngressRules(hostnames, origin)
	currentIngress, err := cloudflareClient.GetConfiguration(ctx, tunnel.ID)
	if err != nil {
		cause := fmt.Errorf("get Cloudflare Tunnel configuration: %w", err)
		return ctrl.Result{}, r.reportOperationalFailure(ctx, binding, kflaredv1alpha1.BindingConditionProgrammed, "TunnelReconciliationFailed", "Tunnel reconciliation could not complete", cause)
	}
	if !planner.EqualIngress(currentIngress, desiredIngress) {
		if updateErr := cloudflareClient.UpdateConfiguration(ctx, tunnel.ID, desiredIngress); updateErr != nil {
			cause := fmt.Errorf("update Cloudflare Tunnel configuration: %w", updateErr)
			return ctrl.Result{}, r.reportOperationalFailure(ctx, binding, kflaredv1alpha1.BindingConditionProgrammed, "TunnelReconciliationFailed", "Tunnel reconciliation could not complete", cause)
		}
	}

	token, err := cloudflareClient.GetToken(ctx, tunnel.ID)
	if err != nil {
		cause := fmt.Errorf("retrieve Cloudflare Tunnel token: %w", err)
		return ctrl.Result{}, r.reportOperationalFailure(ctx, binding, kflaredv1alpha1.BindingConditionConnectorReady, "ConnectorReconciliationFailed", "Connector reconciliation could not complete", cause)
	}
	resourceName := connectorResourceName(binding.UID)
	if err := r.reconcileConnectorSecret(ctx, binding, resourceName, token); err != nil {
		return ctrl.Result{}, r.reportOperationalFailure(ctx, binding, kflaredv1alpha1.BindingConditionConnectorReady, "ConnectorReconciliationFailed", "Connector reconciliation could not complete", err)
	}
	deployment, err := r.reconcileConnectorDeployment(ctx, binding, resourceName)
	if err != nil {
		return ctrl.Result{}, r.reportOperationalFailure(ctx, binding, kflaredv1alpha1.BindingConditionConnectorReady, "ConnectorReconciliationFailed", "Connector reconciliation could not complete", err)
	}
	if err := r.reconcilePodDisruptionBudget(ctx, binding, resourceName); err != nil {
		return ctrl.Result{}, r.reportOperationalFailure(ctx, binding, kflaredv1alpha1.BindingConditionConnectorReady, "ConnectorReconciliationFailed", "Connector reconciliation could not complete", err)
	}

	tunnelCNAME := tunnel.ID + "." + tunnelCNAMEZone
	dnsAutomated, err := r.reconcileDNSEndpoint(ctx, binding, resourceName, hostnames, tunnelCNAME)
	if err != nil {
		return ctrl.Result{}, r.reportOperationalFailure(ctx, binding, kflaredv1alpha1.BindingConditionDNSAutomationReady, "DNSReconciliationFailed", "DNS automation reconciliation could not complete", err)
	}
	connectorReady := deployment.Status.AvailableReplicas >= effectiveReplicas(binding.Spec.ConnectorReplicas)
	if err := r.setReadyStatus(ctx, binding, tunnel, tunnelCNAME, resourceName, hostnames, connectorReady, dnsAutomated); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 2 * time.Minute}, nil
}

func (r *CloudflareTunnelBindingReconciler) validateAndPlan(ctx context.Context, binding *kflaredv1alpha1.CloudflareTunnelBinding) (*kflaredv1alpha1.CloudflareProvider, []string, string, *ctrl.Result, error) {
	provider, result, err := r.validateProvider(ctx, binding)
	if result != nil || err != nil {
		return nil, nil, "", result, err
	}
	origin, result, err := r.validateGatewayService(ctx, binding)
	if result != nil || err != nil {
		return nil, nil, "", result, err
	}

	routeHostnames, err := r.acceptedRouteHostnames(ctx, binding)
	if err != nil {
		return nil, nil, "", nil, err
	}
	zones, err := planner.NormalizeZones(provider.Spec.AllowedDNSZones)
	if err != nil {
		return r.rejected(ctx, binding, "ProviderInvalid", err.Error(), nil)
	}
	hostnames, _ := planner.NormalizeHostnames(routeHostnames, zones)
	if len(hostnames) == 0 {
		if binding.Status.TunnelID == "" {
			return r.rejected(ctx, binding, "NoEligibleHostnames", "No accepted same-namespace HTTPRoute has a concrete hostname in an allowed DNS zone", nil)
		}
		return provider, nil, origin, nil, nil
	}
	if winner, hostname, err := r.hostnameWinner(ctx, binding, hostnames); err != nil {
		return nil, nil, "", nil, err
	} else if winner != nil {
		return r.rejected(ctx, binding, "HostnameConflict", fmt.Sprintf("Hostname %s is already owned by %s/%s", hostname, winner.Namespace, winner.Name), nil)
	}
	return provider, hostnames, origin, nil, nil
}

func (r *CloudflareTunnelBindingReconciler) validateProvider(ctx context.Context, binding *kflaredv1alpha1.CloudflareTunnelBinding) (*kflaredv1alpha1.CloudflareProvider, *ctrl.Result, error) {
	provider := &kflaredv1alpha1.CloudflareProvider{}
	if err := r.Get(ctx, types.NamespacedName{Name: binding.Spec.ProviderRef.Name}, provider); err != nil {
		_, _, _, result, rejectErr := r.rejected(ctx, binding, "ProviderNotFound", "The referenced CloudflareProvider was not found", client.IgnoreNotFound(err))
		return nil, result, rejectErr
	}
	accepted := apiMeta.FindStatusCondition(provider.Status.Conditions, kflaredv1alpha1.ProviderConditionAccepted)
	credentialsValid := apiMeta.FindStatusCondition(provider.Status.Conditions, kflaredv1alpha1.ProviderConditionCredentialsValid)
	acceptedCurrent := accepted != nil && accepted.ObservedGeneration == provider.Generation
	credentialsCurrent := credentialsValid != nil && credentialsValid.ObservedGeneration == provider.Generation
	if (acceptedCurrent && accepted.Status == metav1.ConditionFalse) ||
		(credentialsCurrent && credentialsValid.Status == metav1.ConditionFalse) {
		_, _, _, result, rejectErr := r.rejected(ctx, binding, "ProviderNotReady", "The referenced CloudflareProvider is not ready", nil)
		return nil, result, rejectErr
	}
	if !acceptedCurrent || accepted.Status != metav1.ConditionTrue ||
		!credentialsCurrent || credentialsValid.Status != metav1.ConditionTrue {
		return nil, nil, fmt.Errorf("%w: %q", errProviderStatusUnknown, provider.Name)
	}
	namespace := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: binding.Namespace}, namespace); err != nil {
		return nil, nil, err
	}
	selector, err := metav1.LabelSelectorAsSelector(&provider.Spec.BindingNamespaceSelector)
	if err != nil || !selector.Matches(labels.Set(namespace.Labels)) {
		_, _, _, result, rejectErr := r.rejected(ctx, binding, "NamespaceNotAllowed", "The provider does not allow bindings from this namespace", nil)
		return nil, result, rejectErr
	}
	if winner, err := r.gatewayWinner(ctx, binding); err != nil {
		return nil, nil, err
	} else if winner != nil {
		_, _, _, result, rejectErr := r.rejected(ctx, binding, "GatewayConflict", fmt.Sprintf("Gateway is already owned by %s/%s", winner.Namespace, winner.Name), nil)
		return nil, result, rejectErr
	}
	return provider, nil, nil
}

func (r *CloudflareTunnelBindingReconciler) validateGatewayService(ctx context.Context, binding *kflaredv1alpha1.CloudflareTunnelBinding) (string, *ctrl.Result, error) {
	gateway := &gatewayv1.Gateway{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: binding.Namespace, Name: binding.Spec.GatewayRef.Name}, gateway); err != nil {
		_, _, _, result, rejectErr := r.rejected(ctx, binding, "GatewayNotFound", "The referenced Gateway was not found", client.IgnoreNotFound(err))
		return "", result, rejectErr
	}
	gatewayClass := &gatewayv1.GatewayClass{}
	if err := r.Get(ctx, types.NamespacedName{Name: string(gateway.Spec.GatewayClassName)}, gatewayClass); err != nil {
		_, _, _, result, rejectErr := r.rejected(ctx, binding, "GatewayClassNotFound", "The GatewayClass referenced by the Gateway was not found", client.IgnoreNotFound(err))
		return "", result, rejectErr
	}
	if gatewayClass.Spec.ControllerName != gatewayv1.GatewayController(traefikControllerName) {
		_, _, _, result, rejectErr := r.rejected(ctx, binding, "UnsupportedGatewayClass", "The MVP supports only Traefik-managed Gateways", nil)
		return "", result, rejectErr
	}
	if !conditionTrue(gateway.Status.Conditions, gateway.Generation, "Accepted") || !conditionTrue(gateway.Status.Conditions, gateway.Generation, "Programmed") {
		_, _, _, result, rejectErr := r.rejected(ctx, binding, "GatewayNotReady", "The Gateway must report current Accepted and Programmed conditions", nil)
		return "", result, rejectErr
	}
	if !validHTTPListener(gateway, binding.Spec.GatewayRef.SectionName) {
		_, _, _, result, rejectErr := r.rejected(ctx, binding, "ListenerInvalid", "The selected Gateway listener must exist and use HTTP", nil)
		return "", result, rejectErr
	}

	service := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: binding.Namespace, Name: binding.Spec.GatewayServiceRef.Name}, service); err != nil {
		_, _, _, result, rejectErr := r.rejected(ctx, binding, "ServiceNotFound", "The referenced Gateway Service was not found", client.IgnoreNotFound(err))
		return "", result, rejectErr
	}
	if service.Spec.ClusterIP == "" || service.Spec.ClusterIP == corev1.ClusterIPNone {
		_, _, _, result, rejectErr := r.rejected(ctx, binding, "HeadlessServiceUnsupported", "The Gateway Service must have a ClusterIP", nil)
		return "", result, rejectErr
	}
	if service.Spec.Type == corev1.ServiceTypeLoadBalancer || service.Spec.Type == corev1.ServiceTypeNodePort {
		r.event(binding, corev1.EventTypeWarning, "PublicServiceExposure", "The selected Traefik Service may already expose a public endpoint; ClusterIP is recommended")
	}
	port, err := resolveServicePort(service, binding.Spec.GatewayServiceRef.Port)
	if err != nil {
		_, _, _, result, rejectErr := r.rejected(ctx, binding, "ServicePortInvalid", err.Error(), nil)
		return "", result, rejectErr
	}
	return fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", service.Name, service.Namespace, port), nil, nil
}

func (r *CloudflareTunnelBindingReconciler) acceptedRouteHostnames(ctx context.Context, binding *kflaredv1alpha1.CloudflareTunnelBinding) ([]string, error) {
	routes := &gatewayv1.HTTPRouteList{}
	if err := r.List(ctx, routes, client.InNamespace(binding.Namespace)); err != nil {
		return nil, err
	}
	var hostnames []string
	for i := range routes.Items {
		route := &routes.Items[i]
		if !routeTargetsBinding(route, binding) || !routeAccepted(route, binding) {
			continue
		}
		for _, hostname := range route.Spec.Hostnames {
			hostnames = append(hostnames, string(hostname))
		}
	}
	return hostnames, nil
}

func (r *CloudflareTunnelBindingReconciler) ensureTunnel(ctx context.Context, api cfclient.Client, binding *kflaredv1alpha1.CloudflareTunnelBinding, name string) (*cfclient.Tunnel, error) {
	if binding.Status.TunnelID != "" {
		tunnel, err := api.GetTunnel(ctx, binding.Status.TunnelID)
		if err != nil {
			return nil, fmt.Errorf("get owned Cloudflare Tunnel: %w", err)
		}
		if tunnel != nil {
			if tunnel.Name != name || tunnel.ConfigSource != cfclient.ConfigSourceCloudflare {
				return nil, fmt.Errorf("owned Cloudflare Tunnel identity does not match binding")
			}
			return tunnel, nil
		}
	}
	tunnel, err := api.FindTunnelByName(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("find Cloudflare Tunnel: %w", err)
	}
	if tunnel != nil {
		if tunnel.ConfigSource != cfclient.ConfigSourceCloudflare || tunnel.Name != name {
			return nil, fmt.Errorf("refusing to adopt Cloudflare Tunnel with incompatible identity")
		}
	} else {
		tunnel, err = api.CreateTunnel(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("create Cloudflare Tunnel: %w", err)
		}
	}
	base := binding.DeepCopy()
	binding.Status.TunnelID = tunnel.ID
	binding.Status.TunnelName = tunnel.Name
	binding.Status.TunnelCNAME = tunnel.ID + "." + tunnelCNAMEZone
	if err := r.Status().Patch(ctx, binding, client.MergeFrom(base)); err != nil {
		return nil, err
	}
	return tunnel, nil
}

func (r *CloudflareTunnelBindingReconciler) reconcileConnectorSecret(ctx context.Context, binding *kflaredv1alpha1.CloudflareTunnelBinding, name, token string) error {
	secret := &corev1.Secret{Name: name, Namespace: r.systemNamespace()}
	_, err := controllerutil.CreateOrPatch(ctx, r.Client, secret, func() error {
		secret.Labels = managedLabels(binding)
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{"token": []byte(token)}
		return nil
	})
	return err
}

func (r *CloudflareTunnelBindingReconciler) reconcileConnectorDeployment(ctx context.Context, binding *kflaredv1alpha1.CloudflareTunnelBinding, name string) (*appsv1.Deployment, error) {
	replicas := effectiveReplicas(binding.Spec.ConnectorReplicas)
	deployment := &appsv1.Deployment{Name: name, Namespace: r.systemNamespace()}
	_, err := controllerutil.CreateOrPatch(ctx, r.Client, deployment, func() error {
		resourceLabels := managedLabels(binding)
		deployment.Labels = resourceLabels
		deployment.Spec.Replicas = &replicas
		deployment.Spec.Selector = &metav1.LabelSelector{MatchLabels: resourceLabels}
		deployment.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType}
		deployment.Spec.Template.Labels = resourceLabels
		deployment.Spec.Template.Spec = connectorPodSpec(name, r.cloudflaredImage())
		return nil
	})
	return deployment, err
}

func (r *CloudflareTunnelBindingReconciler) reconcilePodDisruptionBudget(ctx context.Context, binding *kflaredv1alpha1.CloudflareTunnelBinding, name string) error {
	pdb := &policyv1.PodDisruptionBudget{Name: name, Namespace: r.systemNamespace()}
	_, err := controllerutil.CreateOrPatch(ctx, r.Client, pdb, func() error {
		resourceLabels := managedLabels(binding)
		pdb.Labels = resourceLabels
		pdb.Spec.Selector = &metav1.LabelSelector{MatchLabels: resourceLabels}
		pdb.Spec.MaxUnavailable = &intstr.IntOrString{Type: intstr.Int, IntVal: 1}
		return nil
	})
	return err
}

func (r *CloudflareTunnelBindingReconciler) reconcileDNSEndpoint(ctx context.Context, binding *kflaredv1alpha1.CloudflareTunnelBinding, name string, hostnames []string, target string) (bool, error) {
	if r.RESTMapper == nil {
		return false, nil
	}
	_, err := r.RESTMapper.RESTMapping(schema.GroupKind{Group: "externaldns.k8s.io", Kind: dnsEndpointKind}, "v1alpha1")
	if apiMeta.IsNoMatchError(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(hostnames) == 0 {
		return false, r.deleteDNSEndpoint(ctx, binding, name)
	}
	endpoint := &unstructured.Unstructured{}
	endpoint.SetAPIVersion(dnsEndpointAPIVersion)
	endpoint.SetKind(dnsEndpointKind)
	endpoint.SetName(name)
	endpoint.SetNamespace(binding.Namespace)
	_, err = controllerutil.CreateOrPatch(ctx, r.Client, endpoint, func() error {
		endpoint.SetLabels(managedLabels(binding))
		if err := controllerutil.SetControllerReference(binding, endpoint, r.Scheme); err != nil {
			return err
		}
		items := make([]any, 0, len(hostnames))
		for _, hostname := range hostnames {
			items = append(items, map[string]any{
				"dnsName":    hostname,
				"recordType": "CNAME",
				"targets":    []any{target},
			})
		}
		return unstructured.SetNestedSlice(endpoint.Object, items, "spec", "endpoints")
	})
	return err == nil, err
}

func (r *CloudflareTunnelBindingReconciler) setReadyStatus(ctx context.Context, binding *kflaredv1alpha1.CloudflareTunnelBinding, tunnel *cfclient.Tunnel, cname, resourceName string, hostnames []string, connectorReady, dnsAutomated bool) error {
	base := binding.DeepCopy()
	binding.Status.ObservedGeneration = binding.Generation
	binding.Status.TunnelID = tunnel.ID
	binding.Status.TunnelName = tunnel.Name
	binding.Status.TunnelCNAME = cname
	binding.Status.PublishedHostnames = slices.Clone(hostnames)
	binding.Status.DNSRecords = make([]kflaredv1alpha1.DNSRecord, 0, len(hostnames))
	for _, hostname := range hostnames {
		binding.Status.DNSRecords = append(binding.Status.DNSRecords, kflaredv1alpha1.DNSRecord{Hostname: hostname, Type: "CNAME", Target: cname})
	}
	binding.Status.Resources = kflaredv1alpha1.ConnectorResourceNames{Deployment: resourceName, PodDisruptionBudget: resourceName, Secret: resourceName}
	setCondition(&binding.Status.Conditions, binding.Generation, kflaredv1alpha1.BindingConditionAccepted, metav1.ConditionTrue, "Accepted", "Binding configuration and ownership are valid")
	if len(hostnames) > 0 {
		setCondition(&binding.Status.Conditions, binding.Generation, kflaredv1alpha1.BindingConditionProgrammed, metav1.ConditionTrue, "Programmed", "Cloudflare Tunnel configuration matches accepted HTTPRoute hostnames")
	} else {
		setCondition(&binding.Status.Conditions, binding.Generation, kflaredv1alpha1.BindingConditionProgrammed, metav1.ConditionFalse, "NoEligibleHostnames", "The tunnel was deprogrammed because no eligible hostname remains")
	}
	if connectorReady {
		setCondition(&binding.Status.Conditions, binding.Generation, kflaredv1alpha1.BindingConditionConnectorReady, metav1.ConditionTrue, "ConnectorReady", "All desired cloudflared connector replicas are available")
	} else {
		setCondition(&binding.Status.Conditions, binding.Generation, kflaredv1alpha1.BindingConditionConnectorReady, metav1.ConditionFalse, "ConnectorProgressing", "Waiting for cloudflared connector replicas")
	}
	if dnsAutomated {
		binding.Status.Resources.DNSEndpoint = resourceName
		setCondition(&binding.Status.Conditions, binding.Generation, kflaredv1alpha1.BindingConditionDNSAutomationReady, metav1.ConditionTrue, "DNSEndpointCreated", "ExternalDNS automation resource is present")
	} else {
		setCondition(&binding.Status.Conditions, binding.Generation, kflaredv1alpha1.BindingConditionDNSAutomationReady, metav1.ConditionFalse, "ManualConfigurationRequired", "Create the CNAME records listed in status.dnsRecords")
	}
	if len(hostnames) > 0 && connectorReady && dnsAutomated {
		setCondition(&binding.Status.Conditions, binding.Generation, kflaredv1alpha1.BindingConditionReady, metav1.ConditionTrue, "Ready", "Tunnel, connectors, and DNS automation are ready")
	} else {
		setCondition(&binding.Status.Conditions, binding.Generation, kflaredv1alpha1.BindingConditionReady, metav1.ConditionFalse, "DependenciesNotReady", "Connector availability or DNS automation is incomplete")
	}
	return r.Status().Patch(ctx, binding, client.MergeFrom(base))
}

func (r *CloudflareTunnelBindingReconciler) reportOperationalFailure(ctx context.Context, binding *kflaredv1alpha1.CloudflareTunnelBinding, failedCondition, reason, message string, cause error) error {
	base := binding.DeepCopy()
	binding.Status.ObservedGeneration = binding.Generation
	setCondition(&binding.Status.Conditions, binding.Generation, failedCondition, metav1.ConditionUnknown, reason, message)
	if failedCondition != kflaredv1alpha1.BindingConditionReady {
		setCondition(&binding.Status.Conditions, binding.Generation, kflaredv1alpha1.BindingConditionReady, metav1.ConditionUnknown, reason, message)
	}
	patchErr := r.Status().Patch(ctx, binding, client.MergeFrom(base))
	return errors.Join(cause, patchErr)
}

func (r *CloudflareTunnelBindingReconciler) rejected(ctx context.Context, binding *kflaredv1alpha1.CloudflareTunnelBinding, reason, message string, cause error) (*kflaredv1alpha1.CloudflareProvider, []string, string, *ctrl.Result, error) {
	base := binding.DeepCopy()
	binding.Status.ObservedGeneration = binding.Generation
	binding.Status.PublishedHostnames = nil
	binding.Status.DNSRecords = nil
	binding.Status.Resources.DNSEndpoint = ""
	setCondition(&binding.Status.Conditions, binding.Generation, kflaredv1alpha1.BindingConditionAccepted, metav1.ConditionFalse, reason, message)
	setCondition(&binding.Status.Conditions, binding.Generation, kflaredv1alpha1.BindingConditionProgrammed, metav1.ConditionFalse, "NotAccepted", "Binding is not accepted")
	setCondition(&binding.Status.Conditions, binding.Generation, kflaredv1alpha1.BindingConditionDNSAutomationReady, metav1.ConditionFalse, "NotAccepted", "Binding is not accepted")
	setCondition(&binding.Status.Conditions, binding.Generation, kflaredv1alpha1.BindingConditionReady, metav1.ConditionFalse, "NotAccepted", "Binding is not accepted")
	patchErr := r.Status().Patch(ctx, binding, client.MergeFrom(base))
	result := ctrl.Result{RequeueAfter: 5 * time.Minute}
	return nil, nil, "", &result, errors.Join(cause, patchErr)
}

func (r *CloudflareTunnelBindingReconciler) deprogramTunnel(ctx context.Context, binding *kflaredv1alpha1.CloudflareTunnelBinding) error {
	provider := &kflaredv1alpha1.CloudflareProvider{}
	if err := r.Get(ctx, types.NamespacedName{Name: binding.Spec.ProviderRef.Name}, provider); err != nil {
		return fmt.Errorf("read provider while deprogramming tunnel: %w", err)
	}
	token, err := r.readAPIToken(ctx, provider)
	if err != nil {
		return fmt.Errorf("read API token while deprogramming tunnel: %w", err)
	}
	api := r.cloudflare().New(token, provider.Spec.AccountID)
	tunnel, err := api.GetTunnel(ctx, binding.Status.TunnelID)
	if err != nil {
		return fmt.Errorf("read tunnel while deprogramming invalid binding: %w", err)
	}
	if tunnel != nil {
		desired := planner.IngressRules(nil, "")
		current, getErr := api.GetConfiguration(ctx, binding.Status.TunnelID)
		if getErr != nil || !planner.EqualIngress(current, desired) {
			if err := api.UpdateConfiguration(ctx, binding.Status.TunnelID, desired); err != nil {
				return fmt.Errorf("deprogram invalid binding tunnel: %w", err)
			}
		}
	}
	if err := r.deleteDNSEndpoint(ctx, binding, connectorResourceName(binding.UID)); err != nil {
		return fmt.Errorf("delete DNS automation for invalid binding: %w", err)
	}
	return nil
}

func (r *CloudflareTunnelBindingReconciler) finalize(ctx context.Context, binding *kflaredv1alpha1.CloudflareTunnelBinding) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(binding, bindingFinalizer) {
		return ctrl.Result{}, nil
	}
	name := connectorResourceName(binding.UID)
	objects := []client.Object{
		&appsv1.Deployment{Name: name, Namespace: r.systemNamespace()},
		&policyv1.PodDisruptionBudget{Name: name, Namespace: r.systemNamespace()},
		&corev1.Secret{Name: name, Namespace: r.systemNamespace()},
	}
	for _, object := range objects {
		if err := r.Delete(ctx, object); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}
	if err := r.deleteDNSEndpoint(ctx, binding, name); err != nil {
		return ctrl.Result{}, err
	}
	if binding.Spec.DeletionPolicy != kflaredv1alpha1.DeletionPolicyRetain && binding.Status.TunnelID != "" {
		provider := &kflaredv1alpha1.CloudflareProvider{}
		if err := r.Get(ctx, types.NamespacedName{Name: binding.Spec.ProviderRef.Name}, provider); err != nil {
			return ctrl.Result{}, err
		}
		token, err := r.readAPIToken(ctx, provider)
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := r.cloudflare().New(token, provider.Spec.AccountID).DeleteTunnel(ctx, binding.Status.TunnelID); err != nil {
			return ctrl.Result{}, fmt.Errorf("delete Cloudflare Tunnel: %w", err)
		}
		r.event(binding, corev1.EventTypeNormal, "TunnelDeleted", "Deleted the owned Cloudflare Tunnel")
	} else if binding.Spec.DeletionPolicy == kflaredv1alpha1.DeletionPolicyRetain {
		r.event(binding, corev1.EventTypeNormal, "TunnelRetained", "Retained the remote Cloudflare Tunnel and removed Kubernetes connector resources")
	}
	if condition := apiMeta.FindStatusCondition(binding.Status.Conditions, kflaredv1alpha1.BindingConditionDNSAutomationReady); condition != nil && condition.Reason == "ManualConfigurationRequired" {
		r.event(binding, corev1.EventTypeWarning, "ManualDNSRetained", "Manual DNS records were not removed and may return Cloudflare error 1016")
	}
	controllerutil.RemoveFinalizer(binding, bindingFinalizer)
	return ctrl.Result{}, r.Update(ctx, binding)
}

func (r *CloudflareTunnelBindingReconciler) deleteDNSEndpoint(ctx context.Context, binding *kflaredv1alpha1.CloudflareTunnelBinding, name string) error {
	if r.RESTMapper == nil {
		return nil
	}
	_, err := r.RESTMapper.RESTMapping(schema.GroupKind{Group: "externaldns.k8s.io", Kind: dnsEndpointKind}, "v1alpha1")
	if apiMeta.IsNoMatchError(err) {
		return nil
	}
	if err != nil {
		return err
	}
	endpoint := &unstructured.Unstructured{}
	endpoint.SetAPIVersion(dnsEndpointAPIVersion)
	endpoint.SetKind(dnsEndpointKind)
	endpoint.SetName(name)
	endpoint.SetNamespace(binding.Namespace)
	err = r.Delete(ctx, endpoint)
	return client.IgnoreNotFound(err)
}

func (r *CloudflareTunnelBindingReconciler) gatewayWinner(ctx context.Context, binding *kflaredv1alpha1.CloudflareTunnelBinding) (*kflaredv1alpha1.CloudflareTunnelBinding, error) {
	bindings := &kflaredv1alpha1.CloudflareTunnelBindingList{}
	if err := r.List(ctx, bindings, client.InNamespace(binding.Namespace)); err != nil {
		return nil, err
	}
	for i := range bindings.Items {
		other := &bindings.Items[i]
		if other.DeletionTimestamp.IsZero() && other.UID != binding.UID && other.Spec.GatewayRef.Name == binding.Spec.GatewayRef.Name && planner.BindingPrecedes(other, binding) {
			return other, nil
		}
	}
	return nil, nil
}

func (r *CloudflareTunnelBindingReconciler) hostnameWinner(ctx context.Context, binding *kflaredv1alpha1.CloudflareTunnelBinding, hostnames []string) (*kflaredv1alpha1.CloudflareTunnelBinding, string, error) {
	bindings := &kflaredv1alpha1.CloudflareTunnelBindingList{}
	if err := r.List(ctx, bindings); err != nil {
		return nil, "", err
	}
	for i := range bindings.Items {
		other := &bindings.Items[i]
		if !other.DeletionTimestamp.IsZero() || other.UID == binding.UID || !planner.BindingPrecedes(other, binding) {
			continue
		}
		otherHostnames := slices.Clone(other.Status.PublishedHostnames)
		provider := &kflaredv1alpha1.CloudflareProvider{}
		if err := r.Get(ctx, types.NamespacedName{Name: other.Spec.ProviderRef.Name}, provider); err == nil &&
			conditionTrue(provider.Status.Conditions, provider.Generation, kflaredv1alpha1.ProviderConditionAccepted) {
			if candidates, listErr := r.acceptedRouteHostnames(ctx, other); listErr == nil {
				if zones, zoneErr := planner.NormalizeZones(provider.Spec.AllowedDNSZones); zoneErr == nil {
					otherHostnames, _ = planner.NormalizeHostnames(candidates, zones)
				}
			}
		}
		for _, hostname := range hostnames {
			if slices.Contains(otherHostnames, hostname) {
				return other, hostname, nil
			}
		}
	}
	return nil, "", nil
}

func (r *CloudflareTunnelBindingReconciler) readAPIToken(ctx context.Context, provider *kflaredv1alpha1.CloudflareProvider) (string, error) {
	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: r.systemNamespace(), Name: provider.Spec.APITokenSecretRef.Name}
	if err := r.Get(ctx, key, secret); err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(secret.Data[provider.Spec.APITokenSecretRef.Key]))
	if token == "" {
		return "", fmt.Errorf("cloudflare API token secret key is missing or empty")
	}
	return token, nil
}

func (r *CloudflareTunnelBindingReconciler) cloudflare() cfclient.Factory {
	if r.Cloudflare == nil {
		return cfclient.SDKFactory{}
	}
	return r.Cloudflare
}

func (r *CloudflareTunnelBindingReconciler) systemNamespace() string {
	if r.SystemNamespace == "" {
		return defaultSystemNamespace
	}
	return r.SystemNamespace
}

func (r *CloudflareTunnelBindingReconciler) cloudflaredImage() string {
	if r.CloudflaredImage == "" {
		return defaultCloudflaredImage
	}
	return r.CloudflaredImage
}

func (r *CloudflareTunnelBindingReconciler) event(binding *kflaredv1alpha1.CloudflareTunnelBinding, eventType, reason, message string) {
	if r.Recorder != nil {
		r.Recorder.Eventf(binding, nil, eventType, reason, "Reconcile", "%s", message)
	}
}

func (r *CloudflareTunnelBindingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kflaredv1alpha1.CloudflareTunnelBinding{}).
		Watches(&kflaredv1alpha1.CloudflareProvider{}, handler.EnqueueRequestsFromMapFunc(r.bindingsForObject)).
		Watches(&gatewayv1.GatewayClass{}, handler.EnqueueRequestsFromMapFunc(r.bindingsForObject)).
		Watches(&gatewayv1.Gateway{}, handler.EnqueueRequestsFromMapFunc(r.bindingsForObject)).
		Watches(&gatewayv1.HTTPRoute{}, handler.EnqueueRequestsFromMapFunc(r.bindingsForObject)).
		Watches(&corev1.Service{}, handler.EnqueueRequestsFromMapFunc(r.bindingsForObject)).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.bindingsForObject)).
		Named("cloudflaretunnelbinding").
		Complete(r)
}

func (r *CloudflareTunnelBindingReconciler) bindingsForObject(ctx context.Context, object client.Object) []reconcile.Request {
	bindings := &kflaredv1alpha1.CloudflareTunnelBindingList{}
	if err := r.List(ctx, bindings); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(bindings.Items))
	for i := range bindings.Items {
		binding := &bindings.Items[i]
		matches := false
		switch changed := object.(type) {
		case *kflaredv1alpha1.CloudflareProvider:
			matches = binding.Spec.ProviderRef.Name == changed.Name
		case *gatewayv1.GatewayClass:
			matches = true
		case *gatewayv1.Gateway:
			matches = binding.Namespace == changed.Namespace && binding.Spec.GatewayRef.Name == changed.Name
		case *gatewayv1.HTTPRoute:
			matches = binding.Namespace == changed.Namespace && routeTargetsBinding(changed, binding)
		case *corev1.Service:
			matches = binding.Namespace == changed.Namespace && binding.Spec.GatewayServiceRef.Name == changed.Name
		case *corev1.Namespace:
			matches = binding.Namespace == changed.Name
		}
		if matches {
			requests = append(requests, reconcile.Request{Namespace: binding.Namespace, Name: binding.Name})
		}
	}
	return requests
}

func validHTTPListener(gateway *gatewayv1.Gateway, sectionName string) bool {
	for _, listener := range gateway.Spec.Listeners {
		if string(listener.Name) == sectionName && listener.Protocol == gatewayv1.HTTPProtocolType {
			return true
		}
	}
	return false
}

func resolveServicePort(service *corev1.Service, requested intstr.IntOrString) (int32, error) {
	for _, port := range service.Spec.Ports {
		if requested.Type == intstr.String && port.Name == requested.StrVal {
			return port.Port, nil
		}
		if requested.Type == intstr.Int && port.Port == requested.IntVal {
			return port.Port, nil
		}
	}
	return 0, fmt.Errorf("gateway Service port %q was not found", requested.String())
}

func routeTargetsBinding(route *gatewayv1.HTTPRoute, binding *kflaredv1alpha1.CloudflareTunnelBinding) bool {
	for _, parent := range route.Spec.ParentRefs {
		if parentReferenceTargetsGateway(parent, route.Namespace, binding) {
			return true
		}
	}
	return false
}

func routeAccepted(route *gatewayv1.HTTPRoute, binding *kflaredv1alpha1.CloudflareTunnelBinding) bool {
	for _, parent := range route.Status.Parents {
		if parent.ControllerName != gatewayv1.GatewayController(traefikControllerName) {
			continue
		}
		if parentReferenceTargetsGateway(parent.ParentRef, route.Namespace, binding) &&
			conditionTrue(parent.Conditions, route.Generation, "Accepted") && conditionTrue(parent.Conditions, route.Generation, "ResolvedRefs") {
			return true
		}
	}
	return false
}

func parentReferenceTargetsGateway(parent gatewayv1.ParentReference, routeNamespace string, binding *kflaredv1alpha1.CloudflareTunnelBinding) bool {
	group := gatewayv1.Group(gatewayv1.GroupName)
	if parent.Group != nil {
		group = *parent.Group
	}
	kind := gatewayv1.Kind("Gateway")
	if parent.Kind != nil {
		kind = *parent.Kind
	}
	namespace := gatewayv1.Namespace(routeNamespace)
	if parent.Namespace != nil {
		namespace = *parent.Namespace
	}

	return group == gatewayv1.Group(gatewayv1.GroupName) &&
		kind == gatewayv1.Kind("Gateway") &&
		string(namespace) == binding.Namespace &&
		string(parent.Name) == binding.Spec.GatewayRef.Name &&
		parent.SectionName != nil && string(*parent.SectionName) == binding.Spec.GatewayRef.SectionName
}

func effectiveReplicas(value int32) int32 {
	if value < defaultConnectorReplicas {
		return defaultConnectorReplicas
	}
	if value > maxConnectorReplicas {
		return maxConnectorReplicas
	}
	return value
}

func connectorResourceName(uid types.UID) string {
	value := strings.ReplaceAll(string(uid), "-", "")
	if len(value) > 12 {
		value = value[:12]
	}
	if value == "" {
		value = "pending"
	}
	return "kflared-" + value
}

func managedLabels(binding *kflaredv1alpha1.CloudflareTunnelBinding) map[string]string {
	return map[string]string{
		managedByLabel: managedByValue,
		ownerUIDLabel:  string(binding.UID),
	}
}

func connectorPodSpec(secretName, image string) corev1.PodSpec {
	falseValue := false
	trueValue := true
	nonRootUID := int64(65532)
	readOnlyMode := int32(0440)
	return corev1.PodSpec{
		AutomountServiceAccountToken: &falseValue,
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot:   &trueValue,
			RunAsUser:      &nonRootUID,
			RunAsGroup:     &nonRootUID,
			FSGroup:        &nonRootUID,
			SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		Containers: []corev1.Container{{
			Name:         "cloudflared",
			Image:        image,
			Args:         []string{"tunnel", "--no-autoupdate", "--metrics", "0.0.0.0:2000", "run", "--token-file", "/etc/cloudflared/token"},
			Ports:        []corev1.ContainerPort{{Name: "metrics", ContainerPort: 2000, Protocol: corev1.ProtocolTCP}},
			VolumeMounts: []corev1.VolumeMount{{Name: "tunnel-token", MountPath: "/etc/cloudflared", ReadOnly: true}},
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: &falseValue,
				ReadOnlyRootFilesystem:   &trueValue,
				RunAsNonRoot:             &trueValue,
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			},
			ReadinessProbe: &corev1.Probe{HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromString("metrics")}, PeriodSeconds: 10, FailureThreshold: 3},
			LivenessProbe:  &corev1.Probe{HTTPGet: &corev1.HTTPGetAction{Path: "/ready", Port: intstr.FromString("metrics")}, InitialDelaySeconds: 10, PeriodSeconds: 20, FailureThreshold: 3},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resourceMustParse("50m"), corev1.ResourceMemory: resourceMustParse("64Mi")},
				Limits:   corev1.ResourceList{corev1.ResourceCPU: resourceMustParse("500m"), corev1.ResourceMemory: resourceMustParse("256Mi")},
			},
		}},
		Volumes: []corev1.Volume{{Name: "tunnel-token", Secret: &corev1.SecretVolumeSource{SecretName: secretName, DefaultMode: &readOnlyMode}}},
	}
}

func resourceMustParse(value string) resource.Quantity {
	return resource.MustParse(value)
}
