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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	kflaredv1alpha1 "github.com/kode-blox/kflared/api/v1alpha1"
	cfclient "github.com/kode-blox/kflared/internal/cloudflare"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"net/netip"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	"strings"
	"time"
)

// CloudflareTunnelPrivateRouteReconciler reconciles a CloudflareTunnelPrivateRoute object
type CloudflareTunnelPrivateRouteReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Cloudflare       cfclient.Factory
	SystemNamespace  string
	CloudflaredImage string
	ControllerClass  string
}

// +kubebuilder:rbac:groups=kflared.kodeblox.com,resources=cloudflaretunnelprivateroutes,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=kflared.kodeblox.com,resources=cloudflaretunnelprivateroutes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kflared.kodeblox.com,resources=cloudflaretunnelprivateroutes/finalizers,verbs=update

const privateFinalizer = "kflared.kodeblox.com/private-route-cleanup"
const privateUIDLabel = "kflared.kodeblox.com/private-route-uid"

func (r *CloudflareTunnelPrivateRouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	result, err := r.reconcile(ctx, req)
	if err == nil {
		return result, nil
	}
	route := &kflaredv1alpha1.CloudflareTunnelPrivateRoute{}
	if getErr := r.Get(ctx, req.NamespacedName, route); getErr == nil && route.Spec.Controller == r.ControllerClass && route.DeletionTimestamp.IsZero() {
		statusErr := r.patchStatus(ctx, route, func() {
			setCondition(&route.Status.Conditions, route.Generation, kflaredv1alpha1.PrivateRouteConditionProgrammed, metav1.ConditionUnknown, "ReconciliationFailed", "Cloudflare private route reconciliation could not complete")
			setCondition(&route.Status.Conditions, route.Generation, kflaredv1alpha1.PrivateRouteConditionConnectorReady, metav1.ConditionUnknown, "ReconciliationFailed", "Cloudflare connector reconciliation could not complete")
			setCondition(&route.Status.Conditions, route.Generation, kflaredv1alpha1.PrivateRouteConditionReady, metav1.ConditionFalse, "ReconciliationFailed", "Private route reconciliation could not complete")
		})
		if statusErr != nil {
			return result, fmt.Errorf("reconcile: %w; update failure status: %v", err, statusErr)
		}
	}
	return result, err
}

func (r *CloudflareTunnelPrivateRouteReconciler) reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	route := &kflaredv1alpha1.CloudflareTunnelPrivateRoute{}
	if err := r.Get(ctx, req.NamespacedName, route); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if r.ControllerClass == "" || route.Spec.Controller != r.ControllerClass {
		return ctrl.Result{}, nil
	}
	if !route.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, route)
	}
	if !controllerutil.ContainsFinalizer(route, privateFinalizer) {
		controllerutil.AddFinalizer(route, privateFinalizer)
		return ctrl.Result{}, r.Update(ctx, route)
	}
	provider, network, reason, err := r.validate(ctx, route)
	if err != nil {
		return ctrl.Result{}, err
	}
	if reason != "" {
		if route.Status.Network != "" && reason != "ProviderControllerClassMismatch" {
			if err := r.removeRoute(ctx, route); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, r.status(ctx, route, false, reason, false)
	}
	api, err := r.api(ctx, provider)
	if err != nil {
		return ctrl.Result{}, err
	}
	system := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: metav1.NamespaceSystem}, system); err != nil {
		return ctrl.Result{}, err
	}
	tunnel, err := r.ensureTunnel(ctx, api, route, privateTunnelName(system.UID, route.UID))
	if err != nil {
		return ctrl.Result{}, err
	}
	if route.Status.Network != "" && route.Status.Network != network {
		if err := r.deleteRoute(ctx, api, route); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.patchStatus(ctx, route, func() { route.Status.RouteID = ""; route.Status.Network = "" }); err != nil {
			return ctrl.Result{}, err
		}
	}
	if err := r.ensureRoute(ctx, api, route, tunnel, network); err != nil {
		return ctrl.Result{}, err
	}
	token, err := api.GetToken(ctx, tunnel.ID)
	if err != nil {
		return ctrl.Result{}, err
	}
	deployment, err := r.ensureConnector(ctx, route, token)
	if err != nil {
		return ctrl.Result{}, err
	}
	ready := deployment.Status.ObservedGeneration >= deployment.Generation &&
		deployment.Status.AvailableReplicas >= effectiveReplicas(route.Spec.ConnectorReplicas)
	if err := r.status(ctx, route, true, "Accepted", ready); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 2 * time.Minute}, nil
}

func (r *CloudflareTunnelPrivateRouteReconciler) validate(ctx context.Context, route *kflaredv1alpha1.CloudflareTunnelPrivateRoute) (*kflaredv1alpha1.ClusterCloudflareProvider, string, string, error) {
	provider := &kflaredv1alpha1.ClusterCloudflareProvider{}
	if err := r.Get(ctx, types.NamespacedName{Name: route.Spec.ProviderRef.Name}, provider); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, "", "ProviderNotFound", nil
		}
		return nil, "", "", err
	}
	if provider.Spec.Controller != route.Spec.Controller {
		return nil, "", "ProviderControllerClassMismatch", nil
	}
	if !conditionTrue(provider.Status.Conditions, provider.Generation, kflaredv1alpha1.ProviderConditionAccepted) || !conditionTrue(provider.Status.Conditions, provider.Generation, kflaredv1alpha1.ProviderConditionCredentialsValid) {
		return nil, "", "ProviderNotReady", nil
	}
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: route.Namespace}, ns); err != nil {
		return nil, "", "", err
	}
	selector, err := metav1.LabelSelectorAsSelector(&provider.Spec.BindingNamespaceSelector)
	if err != nil || !selector.Matches(labels.Set(ns.Labels)) {
		return nil, "", "NamespaceNotAllowed", nil
	}
	targetNS := privateServiceNamespace(route)
	if targetNS != route.Namespace {
		grants := &gatewayv1.ReferenceGrantList{}
		if err := r.List(ctx, grants, client.InNamespace(targetNS)); err != nil {
			return nil, "", "", err
		}
		permitted := false
		for i := range grants.Items {
			if privateGrantPermits(&grants.Items[i], route) {
				permitted = true
				break
			}
		}
		if !permitted {
			return nil, "", "RefNotPermitted", nil
		}
	}
	service := &corev1.Service{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: targetNS, Name: route.Spec.ServiceRef.Name}, service); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, "", "ServiceNotFound", nil
		}
		return nil, "", "", err
	}
	if service.Spec.Type != "" && service.Spec.Type != corev1.ServiceTypeClusterIP {
		return nil, "", "InvalidService", nil
	}
	ip, err := netip.ParseAddr(service.Spec.ClusterIP)
	if err != nil || !ip.Is4() || ip.IsUnspecified() {
		return nil, "", "InvalidService", nil
	}
	if _, err := resolveServicePort(service, route.Spec.ServiceRef.Port); err != nil {
		return nil, "", "ServicePortNotFound", nil
	}
	network := netip.PrefixFrom(ip, 32).String()
	routes := &kflaredv1alpha1.CloudflareTunnelPrivateRouteList{}
	if err := r.List(ctx, routes); err != nil {
		return nil, "", "", err
	}
	for i := range routes.Items {
		other := &routes.Items[i]
		if other.UID == route.UID || !other.DeletionTimestamp.IsZero() || other.Spec.ProviderRef.Name != route.Spec.ProviderRef.Name || other.Spec.Controller != route.Spec.Controller {
			continue
		}
		otherNetwork := other.Status.Network
		if otherNetwork == "" && privateServiceNamespace(other) == targetNS && other.Spec.ServiceRef.Name == service.Name {
			otherNetwork = network
		}
		if otherNetwork == network && privatePrecedes(other, route) {
			return nil, "", "RouteConflict", nil
		}
	}
	return provider, network, "", nil
}

func privatePrecedes(a, b *kflaredv1alpha1.CloudflareTunnelPrivateRoute) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	if a.Namespace != b.Namespace {
		return a.Namespace < b.Namespace
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return string(a.UID) < string(b.UID)
}
func privateServiceNamespace(route *kflaredv1alpha1.CloudflareTunnelPrivateRoute) string {
	if route.Spec.ServiceRef.Namespace != "" {
		return route.Spec.ServiceRef.Namespace
	}
	return route.Namespace
}
func privateGrantPermits(grant *gatewayv1.ReferenceGrant, route *kflaredv1alpha1.CloudflareTunnelPrivateRoute) bool {
	fromOK := false
	for _, from := range grant.Spec.From {
		if from.Group == gatewayv1.Group(kflaredv1alpha1.GroupVersion.Group) && from.Kind == "CloudflareTunnelPrivateRoute" && string(from.Namespace) == route.Namespace {
			fromOK = true
		}
	}
	if !fromOK {
		return false
	}
	for _, to := range grant.Spec.To {
		if to.Group == "" && to.Kind == "Service" && (to.Name == nil || string(*to.Name) == route.Spec.ServiceRef.Name) {
			return true
		}
	}
	return false
}
func privateTunnelName(clusterUID, routeUID types.UID) string {
	digest := sha256.Sum256([]byte(clusterUID))
	return "kflared-private-" + hex.EncodeToString(digest[:4]) + "-" + string(routeUID)
}
func privateComment(route *kflaredv1alpha1.CloudflareTunnelPrivateRoute) string {
	return "kflared private route UID=" + string(route.UID)
}
func privateResourceName(uid types.UID) string {
	return "kflared-private-" + strings.TrimPrefix(connectorResourceName(uid), "kflared-")
}
func privateLabels(route *kflaredv1alpha1.CloudflareTunnelPrivateRoute) map[string]string {
	return map[string]string{managedByLabel: managedByValue, privateUIDLabel: string(route.UID)}
}

func (r *CloudflareTunnelPrivateRouteReconciler) api(ctx context.Context, provider *kflaredv1alpha1.ClusterCloudflareProvider) (cfclient.Client, error) {
	token, err := r.bindingHelper().readAPIToken(ctx, provider)
	if err != nil {
		return nil, err
	}
	return r.cloudflare().New(token, provider.Spec.AccountID), nil
}
func (r *CloudflareTunnelPrivateRouteReconciler) bindingHelper() *CloudflareTunnelBindingReconciler {
	return &CloudflareTunnelBindingReconciler{Client: r.Client, SystemNamespace: r.SystemNamespace}
}
func (r *CloudflareTunnelPrivateRouteReconciler) cloudflare() cfclient.Factory {
	if r.Cloudflare != nil {
		return r.Cloudflare
	}
	return cfclient.SDKFactory{}
}
func (r *CloudflareTunnelPrivateRouteReconciler) systemNamespace() string {
	if r.SystemNamespace != "" {
		return r.SystemNamespace
	}
	return defaultSystemNamespace
}
func (r *CloudflareTunnelPrivateRouteReconciler) cloudflaredImage() string {
	if r.CloudflaredImage != "" {
		return r.CloudflaredImage
	}
	return defaultCloudflaredImage
}

func (r *CloudflareTunnelPrivateRouteReconciler) ensureTunnel(ctx context.Context, api cfclient.Client, route *kflaredv1alpha1.CloudflareTunnelPrivateRoute, name string) (*cfclient.Tunnel, error) {
	var tunnel *cfclient.Tunnel
	var err error
	if route.Status.TunnelID != "" {
		tunnel, err = api.GetTunnel(ctx, route.Status.TunnelID)
		if err != nil {
			return nil, err
		}
		if tunnel == nil {
			// Remove any UID-owned route left behind by a remotely deleted tunnel.
			if route.Status.Network != "" {
				if err := r.deleteRoute(ctx, api, route); err != nil {
					return nil, err
				}
			}
			if err := r.patchStatus(ctx, route, func() {
				route.Status.TunnelID = ""
				route.Status.TunnelName = ""
				route.Status.RouteID = ""
				route.Status.Network = ""
			}); err != nil {
				return nil, err
			}
		}
	}
	if tunnel == nil {
		tunnel, err = api.FindTunnelByName(ctx, name)
		if err != nil {
			return nil, err
		}
		if tunnel == nil {
			tunnel, err = api.CreateTunnel(ctx, name)
			if err != nil {
				return nil, err
			}
		}
	}
	if tunnel.Name != name || tunnel.ConfigSource != cfclient.ConfigSourceCloudflare {
		return nil, fmt.Errorf("refusing tunnel with incompatible identity")
	}
	if err := r.patchStatus(ctx, route, func() { route.Status.TunnelID = tunnel.ID; route.Status.TunnelName = tunnel.Name }); err != nil {
		return nil, err
	}
	return tunnel, nil
}

func (r *CloudflareTunnelPrivateRouteReconciler) ensureRoute(ctx context.Context, api cfclient.Client, route *kflaredv1alpha1.CloudflareTunnelPrivateRoute, tunnel *cfclient.Tunnel, network string) error {
	if route.Status.RouteID != "" {
		current, err := api.GetPrivateRoute(ctx, route.Status.RouteID)
		if err != nil {
			return err
		}
		if current == nil {
			if err := r.patchStatus(ctx, route, func() { route.Status.RouteID = ""; route.Status.Network = "" }); err != nil {
				return err
			}
		} else if !privateOwned(current, route) || current.Network != network || current.TunnelID != tunnel.ID {
			return fmt.Errorf("owned route identity does not match status")
		} else {
			return nil
		}
	}
	existing, err := api.ListPrivateRoutes(ctx, network)
	if err != nil {
		return err
	}
	for i := range existing {
		current := &existing[i]
		if current.Network != network {
			continue
		}
		if privateOwned(current, route) && current.TunnelID == tunnel.ID {
			return r.patchStatus(ctx, route, func() { route.Status.RouteID = current.ID; route.Status.Network = network })
		}
		return fmt.Errorf("Cloudflare network %s is already routed by route %s", network, current.ID)
	}
	// Persist the scope before creation. If Cloudflare creates the route but the
	// following status patch fails, cleanup can still find it by network and UID.
	if route.Status.Network != network {
		if err := r.patchStatus(ctx, route, func() { route.Status.Network = network }); err != nil {
			return err
		}
	}
	created, err := api.CreatePrivateRoute(ctx, network, tunnel.ID, privateComment(route))
	if err != nil {
		return err
	}
	if created == nil || !privateOwned(created, route) || created.Network != network || created.TunnelID != tunnel.ID {
		return fmt.Errorf("Cloudflare returned unexpected route identity")
	}
	return r.patchStatus(ctx, route, func() { route.Status.RouteID = created.ID; route.Status.Network = network })
}
func privateOwned(current *cfclient.PrivateRoute, route *kflaredv1alpha1.CloudflareTunnelPrivateRoute) bool {
	return current != nil && current.ID != "" && current.Comment == privateComment(route) && (route.Status.TunnelID == "" || current.TunnelID == route.Status.TunnelID)
}
func (r *CloudflareTunnelPrivateRouteReconciler) deleteRoute(ctx context.Context, api cfclient.Client, route *kflaredv1alpha1.CloudflareTunnelPrivateRoute) error {
	if route.Status.RouteID == "" {
		existing, err := api.ListPrivateRoutes(ctx, route.Status.Network)
		if err != nil {
			return err
		}
		for i := range existing {
			current := &existing[i]
			if current.Network == route.Status.Network && privateOwned(current, route) {
				if err := api.DeletePrivateRoute(ctx, current.ID); err != nil {
					return err
				}
			}
		}
		return nil
	}
	current, err := api.GetPrivateRoute(ctx, route.Status.RouteID)
	if err != nil {
		return err
	}
	if current == nil {
		return nil
	}
	if !privateOwned(current, route) || current.Network != route.Status.Network {
		return fmt.Errorf("refusing to delete route %s with mismatched identity", route.Status.RouteID)
	}
	return api.DeletePrivateRoute(ctx, current.ID)
}
func (r *CloudflareTunnelPrivateRouteReconciler) removeRoute(ctx context.Context, route *kflaredv1alpha1.CloudflareTunnelPrivateRoute) error {
	provider := &kflaredv1alpha1.ClusterCloudflareProvider{}
	if err := r.Get(ctx, types.NamespacedName{Name: route.Spec.ProviderRef.Name}, provider); err != nil {
		return err
	}
	if provider.Spec.Controller != route.Spec.Controller {
		return fmt.Errorf("provider controller class changed")
	}
	api, err := r.api(ctx, provider)
	if err != nil {
		return err
	}
	if err := r.deleteRoute(ctx, api, route); err != nil {
		return err
	}
	return r.patchStatus(ctx, route, func() { route.Status.RouteID = ""; route.Status.Network = "" })
}

func (r *CloudflareTunnelPrivateRouteReconciler) ensureConnector(ctx context.Context, route *kflaredv1alpha1.CloudflareTunnelPrivateRoute, token string) (*appsv1.Deployment, error) {
	name := privateResourceName(route.UID)
	namespace := r.systemNamespace()
	labels := privateLabels(route)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if _, err := controllerutil.CreateOrPatch(ctx, r.Client, secret, func() error {
		if err := verifyPrivateChild(route, secret); err != nil {
			return err
		}
		secret.Labels = labels
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{"token": []byte(token)}
		return nil
	}); err != nil {
		return nil, err
	}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if _, err := controllerutil.CreateOrPatch(ctx, r.Client, deployment, func() error {
		if err := verifyPrivateChild(route, deployment); err != nil {
			return err
		}
		replicas := effectiveReplicas(route.Spec.ConnectorReplicas)
		deployment.Labels = labels
		deployment.Spec.Replicas = &replicas
		deployment.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deployment.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType}
		deployment.Spec.Template.Labels = labels
		deployment.Spec.Template.Spec = connectorPodSpec(name, r.cloudflaredImage())
		return nil
	}); err != nil {
		return nil, err
	}
	pdb := &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	if _, err := controllerutil.CreateOrPatch(ctx, r.Client, pdb, func() error {
		if err := verifyPrivateChild(route, pdb); err != nil {
			return err
		}
		pdb.Labels = labels
		pdb.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		pdb.Spec.MaxUnavailable = &intstr.IntOrString{Type: intstr.Int, IntVal: 1}
		return nil
	}); err != nil {
		return nil, err
	}
	return deployment, r.patchStatus(ctx, route, func() {
		route.Status.Resources = kflaredv1alpha1.ConnectorResourceNames{Deployment: name, PodDisruptionBudget: name, Secret: name}
	})
}
func verifyPrivateChild(route *kflaredv1alpha1.CloudflareTunnelPrivateRoute, object client.Object) error {
	if object.GetUID() != "" && object.GetLabels()[privateUIDLabel] != string(route.UID) {
		return fmt.Errorf("refusing to adopt unowned child %s/%s", object.GetNamespace(), object.GetName())
	}
	return nil
}

func (r *CloudflareTunnelPrivateRouteReconciler) finalize(ctx context.Context, route *kflaredv1alpha1.CloudflareTunnelPrivateRoute) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(route, privateFinalizer) {
		return ctrl.Result{}, nil
	}
	if route.Spec.DeletionPolicy != kflaredv1alpha1.DeletionPolicyRetain && route.Status.Network != "" {
		if err := r.removeRoute(ctx, route); err != nil {
			return ctrl.Result{}, err
		}
	}
	name := privateResourceName(route.UID)
	for _, object := range []client.Object{&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: r.systemNamespace()}}, &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: r.systemNamespace()}}, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: r.systemNamespace()}}} {
		current := object.DeepCopyObject().(client.Object)
		err := r.Get(ctx, types.NamespacedName{Namespace: object.GetNamespace(), Name: object.GetName()}, current)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := verifyPrivateChild(route, current); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Delete(ctx, current); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}
	if route.Spec.DeletionPolicy != kflaredv1alpha1.DeletionPolicyRetain && route.Status.TunnelID != "" {
		provider := &kflaredv1alpha1.ClusterCloudflareProvider{}
		if err := r.Get(ctx, types.NamespacedName{Name: route.Spec.ProviderRef.Name}, provider); err != nil {
			return ctrl.Result{}, err
		}
		if provider.Spec.Controller != route.Spec.Controller {
			return ctrl.Result{}, fmt.Errorf("provider class mismatch")
		}
		api, err := r.api(ctx, provider)
		if err != nil {
			return ctrl.Result{}, err
		}
		tunnel, err := api.GetTunnel(ctx, route.Status.TunnelID)
		if err != nil {
			return ctrl.Result{}, err
		}
		if tunnel != nil {
			if tunnel.Name != route.Status.TunnelName || tunnel.ConfigSource != cfclient.ConfigSourceCloudflare {
				return ctrl.Result{}, fmt.Errorf("refusing to delete tunnel with mismatched identity")
			}
			if err := api.DeleteTunnel(ctx, tunnel.ID); err != nil {
				return ctrl.Result{}, err
			}
		}
	}
	controllerutil.RemoveFinalizer(route, privateFinalizer)
	return ctrl.Result{}, r.Update(ctx, route)
}
func (r *CloudflareTunnelPrivateRouteReconciler) status(ctx context.Context, route *kflaredv1alpha1.CloudflareTunnelPrivateRoute, accepted bool, reason string, connectorReady bool) error {
	return r.patchStatus(ctx, route, func() {
		route.Status.ObservedGeneration = route.Generation
		s := metav1.ConditionFalse
		if accepted {
			s = metav1.ConditionTrue
		}
		setCondition(&route.Status.Conditions, route.Generation, kflaredv1alpha1.PrivateRouteConditionAccepted, s, reason, reason)
		programmed := metav1.ConditionFalse
		if route.Status.RouteID != "" {
			programmed = metav1.ConditionTrue
		}
		setCondition(&route.Status.Conditions, route.Generation, kflaredv1alpha1.PrivateRouteConditionProgrammed, programmed, reason, reason)
		ready := metav1.ConditionFalse
		if connectorReady {
			ready = metav1.ConditionTrue
		}
		setCondition(&route.Status.Conditions, route.Generation, kflaredv1alpha1.PrivateRouteConditionConnectorReady, ready, reason, reason)
		if !accepted || programmed != metav1.ConditionTrue {
			ready = metav1.ConditionFalse
		}
		setCondition(&route.Status.Conditions, route.Generation, kflaredv1alpha1.PrivateRouteConditionReady, ready, reason, reason)
	})
}
func (r *CloudflareTunnelPrivateRouteReconciler) patchStatus(ctx context.Context, route *kflaredv1alpha1.CloudflareTunnelPrivateRoute, change func()) error {
	before := route.DeepCopy()
	change()
	return r.Status().Patch(ctx, route, client.MergeFrom(before))
}

// SetupWithManager sets up the controller with the Manager.
func (r *CloudflareTunnelPrivateRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kflaredv1alpha1.CloudflareTunnelPrivateRoute{}, builder.WithPredicates(predicate.NewPredicateFuncs(func(object client.Object) bool {
			route, ok := object.(*kflaredv1alpha1.CloudflareTunnelPrivateRoute)
			return ok && r.ControllerClass != "" && route.Spec.Controller == r.ControllerClass
		}))).
		Watches(&kflaredv1alpha1.ClusterCloudflareProvider{}, handler.EnqueueRequestsFromMapFunc(r.routesForObject)).
		Watches(&corev1.Service{}, handler.EnqueueRequestsFromMapFunc(r.routesForObject)).
		Watches(&gatewayv1.ReferenceGrant{}, handler.EnqueueRequestsFromMapFunc(r.routesForObject)).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.routesForObject)).
		Named("cloudflaretunnelprivateroute").
		Complete(r)
}
func (r *CloudflareTunnelPrivateRouteReconciler) routesForObject(ctx context.Context, object client.Object) []reconcile.Request {
	routes := &kflaredv1alpha1.CloudflareTunnelPrivateRouteList{}
	if err := r.List(ctx, routes); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for i := range routes.Items {
		route := &routes.Items[i]
		if route.Spec.Controller != r.ControllerClass {
			continue
		}
		match := false
		switch changed := object.(type) {
		case *kflaredv1alpha1.ClusterCloudflareProvider:
			match = route.Spec.ProviderRef.Name == changed.Name
		case *corev1.Service:
			match = privateServiceNamespace(route) == changed.Namespace && route.Spec.ServiceRef.Name == changed.Name
		case *gatewayv1.ReferenceGrant:
			match = privateServiceNamespace(route) == changed.Namespace
		case *corev1.Namespace:
			match = route.Namespace == changed.Name
		}
		if match {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: route.Namespace, Name: route.Name}})
		}
	}
	return requests
}
