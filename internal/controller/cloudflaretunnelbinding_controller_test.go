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
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	kflaredv1alpha1 "github.com/kode-blox/kflared/api/v1alpha1"
	cfclient "github.com/kode-blox/kflared/internal/cloudflare"
	"github.com/kode-blox/kflared/internal/planner"
)

const (
	routeParentTestNamespace   = testTenantName
	routeParentTestGatewayName = testGatewayName
	routeParentTestSectionName = testHTTPSectionName
)

type failingGetClient struct {
	client.Client
	objectType reflect.Type
	key        types.NamespacedName
	err        error
}

func (c *failingGetClient) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	if reflect.TypeOf(object) == c.objectType && key == c.key {
		return c.err
	}
	return c.Client.Get(ctx, key, object, opts...)
}

type failingStatusClient struct {
	client.Client
	patchErr   error
	patchCalls int
}

func (c *failingStatusClient) Status() client.SubResourceWriter {
	return &failingStatusWriter{SubResourceWriter: c.Client.Status(), client: c}
}

type failingStatusWriter struct {
	client.SubResourceWriter
	client *failingStatusClient
}

func (w *failingStatusWriter) Patch(_ context.Context, _ client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
	w.client.patchCalls++
	return w.client.patchErr
}

func TestBindingReconcileCreatesIdempotentTunnelAndHardenedConnectors(t *testing.T) {
	scheme := bindingTestScheme(t)
	objects, binding := validBindingObjects()
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}, &kflaredv1alpha1.ClusterCloudflareProvider{}, &gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}, &appsv1.Deployment{}).
		WithObjects(objects...).Build()
	cloudflare := &fakeCloudflareClient{token: testConnectorToken}
	reconciler := &CloudflareTunnelBindingReconciler{ControllerClass: testControllerClass, Client: kubeClient, Scheme: scheme, RESTMapper: bindingTestRESTMapper(), Cloudflare: fakeCloudflareFactory{client: cloudflare}}
	request := ctrl.Request{Namespace: binding.Namespace, Name: binding.Name}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("add finalizer: %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("create tunnel and connectors: %v", err)
	}
	resourceName := connectorResourceName(binding.UID)
	endpointKey := types.NamespacedName{Namespace: binding.Namespace, Name: resourceName}
	setTestDNSEndpointProxyValue(t, kubeClient, endpointKey, "false")
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("repair DNSEndpoint proxy metadata: %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("idempotent reconcile: %v", err)
	}
	if cloudflare.createCalls != 1 || cloudflare.updateCalls != 1 {
		t.Fatalf("Cloudflare calls create=%d update=%d, want 1 each", cloudflare.createCalls, cloudflare.updateCalls)
	}
	if len(cloudflare.configuration) != 2 || cloudflare.configuration[0].Hostname != testApplicationHostname || cloudflare.configuration[1].Service != testNotFoundOrigin {
		t.Fatalf("unexpected ingress: %#v", cloudflare.configuration)
	}

	actualBinding := &kflaredv1alpha1.CloudflareTunnelBinding{}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, actualBinding); err != nil {
		t.Fatal(err)
	}
	if actualBinding.Status.TunnelID != testTunnelID || len(actualBinding.Status.DNSRecords) != 1 {
		t.Fatalf("unexpected binding status: %#v", actualBinding.Status)
	}
	deployment := &appsv1.Deployment{}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Namespace: defaultSystemNamespace, Name: resourceName}, deployment); err != nil {
		t.Fatal(err)
	}
	pod := deployment.Spec.Template.Spec
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken || pod.SecurityContext == nil || pod.SecurityContext.SeccompProfile == nil {
		t.Fatalf("connector pod is not hardened: %#v", pod)
	}
	connector := pod.Containers[0]
	containerSecurity := connector.SecurityContext
	if containerSecurity == nil || containerSecurity.ReadOnlyRootFilesystem == nil || !*containerSecurity.ReadOnlyRootFilesystem {
		t.Fatalf("connector container is not hardened: %#v", pod.Containers)
	}
	if connector.ReadinessProbe == nil || connector.ReadinessProbe.HTTPGet == nil || connector.ReadinessProbe.HTTPGet.Path != "/ready" {
		t.Fatalf("connector readiness does not require an active tunnel connection: %#v", connector.ReadinessProbe)
	}
	if connector.LivenessProbe == nil || connector.LivenessProbe.HTTPGet == nil || connector.LivenessProbe.HTTPGet.Path != "/healthcheck" {
		t.Fatalf("connector liveness depends on tunnel connectivity: %#v", connector.LivenessProbe)
	}
	secret := &corev1.Secret{}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Namespace: defaultSystemNamespace, Name: resourceName}, secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["token"]) != testConnectorToken {
		t.Fatal("connector token Secret was not reconciled")
	}
	expectedEndpoints := []any{map[string]any{
		"dnsName":    testApplicationHostname,
		"recordType": "CNAME",
		"targets":    []any{testTunnelID + "." + tunnelCNAMEZone},
		"providerSpecific": []any{map[string]any{
			"name":  "cloudflare/proxied",
			"value": "true",
		}},
	}}
	assertTestDNSEndpointEndpoints(t, kubeClient, endpointKey, expectedEndpoints)
}

func TestBindingReconcileDisablesDNSAutomationAndDeletesOwnedDNSEndpoint(t *testing.T) {
	scheme := bindingTestScheme(t)
	objects, binding := validBindingObjects()
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}, &kflaredv1alpha1.ClusterCloudflareProvider{}, &gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}, &appsv1.Deployment{}).
		WithObjects(objects...).Build()
	cloudflare := &fakeCloudflareClient{token: testConnectorToken}
	reconciler := &CloudflareTunnelBindingReconciler{ControllerClass: testControllerClass, Client: kubeClient, Scheme: scheme, RESTMapper: bindingTestRESTMapper(), Cloudflare: fakeCloudflareFactory{client: cloudflare}}
	request := ctrl.Request{Namespace: binding.Namespace, Name: binding.Name}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("add finalizer: %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("create DNS automation: %v", err)
	}
	resourceName := connectorResourceName(binding.UID)
	endpointKey := types.NamespacedName{Namespace: binding.Namespace, Name: resourceName}
	getTestDNSEndpoint(t, kubeClient, endpointKey)

	deployment := &appsv1.Deployment{}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Namespace: defaultSystemNamespace, Name: resourceName}, deployment); err != nil {
		t.Fatal(err)
	}
	deployment.Status.AvailableReplicas = 2
	if err := kubeClient.Status().Update(context.Background(), deployment); err != nil {
		t.Fatalf("mark connectors available: %v", err)
	}
	storedBinding := &kflaredv1alpha1.CloudflareTunnelBinding{}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, storedBinding); err != nil {
		t.Fatal(err)
	}
	storedBinding.Spec.DNSAutomationEnabled = new(false)
	if err := kubeClient.Update(context.Background(), storedBinding); err != nil {
		t.Fatalf("disable DNS automation: %v", err)
	}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("reconcile DNS automation opt-out: %v", err)
	}
	endpoint := &unstructured.Unstructured{}
	endpoint.SetAPIVersion(dnsEndpointAPIVersion)
	endpoint.SetKind(dnsEndpointKind)
	if err := kubeClient.Get(context.Background(), endpointKey, endpoint); !apierrors.IsNotFound(err) {
		t.Fatalf("DNSEndpoint after opt-out = %v, want not found", err)
	}
	if cloudflare.tunnel == nil || cloudflare.deleteCalls != 0 || len(cloudflare.configuration) != 2 {
		t.Fatalf("DNS opt-out changed tunnel: tunnel=%#v deleteCalls=%d ingressRules=%d", cloudflare.tunnel, cloudflare.deleteCalls, len(cloudflare.configuration))
	}
	connectorKey := types.NamespacedName{Namespace: defaultSystemNamespace, Name: resourceName}
	if err := kubeClient.Get(context.Background(), connectorKey, &appsv1.Deployment{}); err != nil {
		t.Fatalf("get connector Deployment after DNS opt-out: %v", err)
	}
	if err := kubeClient.Get(context.Background(), connectorKey, &policyv1.PodDisruptionBudget{}); err != nil {
		t.Fatalf("get connector PodDisruptionBudget after DNS opt-out: %v", err)
	}
	if err := kubeClient.Get(context.Background(), connectorKey, &corev1.Secret{}); err != nil {
		t.Fatalf("get connector Secret after DNS opt-out: %v", err)
	}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, storedBinding); err != nil {
		t.Fatal(err)
	}
	if len(storedBinding.Status.DNSRecords) != 0 || storedBinding.Status.Resources.DNSEndpoint != "" {
		t.Fatalf("disabled DNS automation retained DNS status: %#v", storedBinding.Status)
	}
	dnsReady := apiMeta.FindStatusCondition(storedBinding.Status.Conditions, kflaredv1alpha1.BindingConditionDNSAutomationReady)
	if dnsReady == nil || dnsReady.Status != metav1.ConditionTrue || dnsReady.Reason != "DNSAutomationDisabled" {
		t.Fatalf("DNSAutomationReady = %#v, want True/DNSAutomationDisabled", dnsReady)
	}
	ready := apiMeta.FindStatusCondition(storedBinding.Status.Conditions, kflaredv1alpha1.BindingConditionReady)
	if ready == nil || ready.Status != metav1.ConditionTrue {
		t.Fatalf("Ready = %#v, want True when connectors are available", ready)
	}
}

func TestBindingReconcileDoesNotCreateDNSEndpointWhenDNSAutomationIsDisabled(t *testing.T) {
	scheme := bindingTestScheme(t)
	objects, binding := validBindingObjects()
	binding.Spec.DNSAutomationEnabled = new(false)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}, &kflaredv1alpha1.ClusterCloudflareProvider{}, &gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}, &appsv1.Deployment{}).
		WithObjects(objects...).Build()
	cloudflare := &fakeCloudflareClient{token: testConnectorToken}
	reconciler := &CloudflareTunnelBindingReconciler{ControllerClass: testControllerClass, Client: kubeClient, Scheme: scheme, RESTMapper: bindingTestRESTMapper(), Cloudflare: fakeCloudflareFactory{client: cloudflare}}
	request := ctrl.Request{Namespace: binding.Namespace, Name: binding.Name}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("add finalizer: %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("reconcile disabled DNS automation: %v", err)
	}
	endpoint := &unstructured.Unstructured{}
	endpoint.SetAPIVersion(dnsEndpointAPIVersion)
	endpoint.SetKind(dnsEndpointKind)
	endpointKey := types.NamespacedName{Namespace: binding.Namespace, Name: connectorResourceName(binding.UID)}
	if err := kubeClient.Get(context.Background(), endpointKey, endpoint); !apierrors.IsNotFound(err) {
		t.Fatalf("DNSEndpoint for a disabled binding = %v, want not found", err)
	}
}

func TestBindingReconcileDefaultsNilDNSAutomationToEnabled(t *testing.T) {
	scheme := bindingTestScheme(t)
	objects, binding := validBindingObjects()
	binding.Spec.DNSAutomationEnabled = nil
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}, &kflaredv1alpha1.ClusterCloudflareProvider{}, &gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}, &appsv1.Deployment{}).
		WithObjects(objects...).Build()
	cloudflare := &fakeCloudflareClient{token: testConnectorToken}
	reconciler := &CloudflareTunnelBindingReconciler{ControllerClass: testControllerClass, Client: kubeClient, Scheme: scheme, RESTMapper: bindingTestRESTMapper(), Cloudflare: fakeCloudflareFactory{client: cloudflare}}
	request := ctrl.Request{Namespace: binding.Namespace, Name: binding.Name}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("add finalizer: %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("reconcile nil DNS automation setting: %v", err)
	}
	endpointKey := types.NamespacedName{Namespace: binding.Namespace, Name: connectorResourceName(binding.UID)}
	getTestDNSEndpoint(t, kubeClient, endpointKey)
}

func TestBindingReconcileRefusesToDeleteUnownedDNSEndpointWhenDNSAutomationIsDisabled(t *testing.T) {
	scheme := bindingTestScheme(t)
	objects, binding := validBindingObjects()
	binding.Spec.DNSAutomationEnabled = new(false)
	endpointKey := types.NamespacedName{Namespace: binding.Namespace, Name: connectorResourceName(binding.UID)}
	endpoint := &unstructured.Unstructured{}
	endpoint.SetAPIVersion(dnsEndpointAPIVersion)
	endpoint.SetKind(dnsEndpointKind)
	endpoint.SetNamespace(endpointKey.Namespace)
	endpoint.SetName(endpointKey.Name)
	endpoint.Object["spec"] = map[string]any{"endpoints": []any{}}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}, &kflaredv1alpha1.ClusterCloudflareProvider{}, &gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}, &appsv1.Deployment{}).
		WithObjects(append(objects, endpoint)...).Build()
	cloudflare := &fakeCloudflareClient{token: testConnectorToken}
	reconciler := &CloudflareTunnelBindingReconciler{ControllerClass: testControllerClass, Client: kubeClient, Scheme: scheme, RESTMapper: bindingTestRESTMapper(), Cloudflare: fakeCloudflareFactory{client: cloudflare}}
	request := ctrl.Request{Namespace: binding.Namespace, Name: binding.Name}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("add finalizer: %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err == nil || !strings.Contains(err.Error(), "refusing to delete DNSEndpoint") {
		t.Fatalf("reconcile disabled DNS automation error = %v, want ownership refusal", err)
	}
	getTestDNSEndpoint(t, kubeClient, endpointKey)
}

func TestBindingReconcileIgnoresOtherControllerClassBeforeDeletionEffects(t *testing.T) {
	scheme := bindingTestScheme(t)
	objects, binding := validBindingObjects()
	binding.Spec.Controller = testOtherControllerClass
	binding.Finalizers = []string{bindingFinalizer}
	binding.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	binding.Status.TunnelID = testTunnelID
	child := &appsv1.Deployment{}
	child.Name = connectorResourceName(binding.UID)
	child.Namespace = defaultSystemNamespace
	child.Labels = managedLabels(binding)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}, &kflaredv1alpha1.ClusterCloudflareProvider{}).
		WithObjects(append(objects, child)...).Build()
	cloudflare := &fakeCloudflareClient{}
	reconciler := &CloudflareTunnelBindingReconciler{ControllerClass: testControllerClass, Client: kubeClient, Scheme: scheme, Cloudflare: fakeCloudflareFactory{client: cloudflare}}

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{Namespace: binding.Namespace, Name: binding.Name}); err != nil {
		t.Fatalf("ignore foreign binding: %v", err)
	}
	actual := &kflaredv1alpha1.CloudflareTunnelBinding{}
	if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(binding), actual); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(actual.Finalizers, []string{bindingFinalizer}) || actual.Status.TunnelID != testTunnelID || len(actual.Status.Conditions) != 0 {
		t.Fatalf("foreign binding mutated: finalizers=%v status=%#v", actual.Finalizers, actual.Status)
	}
	if cloudflare.validateCalls+cloudflare.createCalls+cloudflare.updateCalls+cloudflare.deleteCalls != 0 {
		t.Fatalf("foreign binding caused Cloudflare calls: %#v", cloudflare)
	}
	actualChild := &appsv1.Deployment{}
	if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(child), actualChild); err != nil {
		t.Fatalf("foreign binding deleted its child Deployment: %v", err)
	}
}

func TestBindingProviderControllerClassMismatchOnlyUpdatesBindingStatus(t *testing.T) {
	scheme := bindingTestScheme(t)
	objects, binding := validBindingObjects()
	binding.Finalizers = []string{bindingFinalizer}
	binding.Status.TunnelID = testTunnelID
	provider := bindingTestClusterProvider(t, objects)
	provider.Spec.Controller = testOtherControllerClass
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}, &kflaredv1alpha1.ClusterCloudflareProvider{}).
		WithObjects(objects...).Build()
	cloudflare := &fakeCloudflareClient{}
	reconciler := &CloudflareTunnelBindingReconciler{ControllerClass: testControllerClass, Client: kubeClient, Scheme: scheme, Cloudflare: fakeCloudflareFactory{client: cloudflare}}

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{Namespace: binding.Namespace, Name: binding.Name}); err != nil {
		t.Fatalf("report provider class mismatch: %v", err)
	}
	actual := &kflaredv1alpha1.CloudflareTunnelBinding{}
	if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(binding), actual); err != nil {
		t.Fatal(err)
	}
	accepted := apiMeta.FindStatusCondition(actual.Status.Conditions, kflaredv1alpha1.BindingConditionAccepted)
	if accepted == nil || accepted.Status != metav1.ConditionFalse || accepted.Reason != "ProviderControllerClassMismatch" {
		t.Fatalf("Accepted condition = %#v, want False/ProviderControllerClassMismatch", accepted)
	}
	if cloudflare.validateCalls+cloudflare.createCalls+cloudflare.updateCalls+cloudflare.deleteCalls != 0 {
		t.Fatalf("class mismatch caused Cloudflare calls: %#v", cloudflare)
	}
}

func TestBindingEventMapperQueuesOnlyOwnedControllerClass(t *testing.T) {
	scheme := bindingTestScheme(t)
	objects, binding := validBindingObjects()
	otherClass := binding.DeepCopy()
	otherClass.Name = "foreign"
	otherClass.UID = types.UID("99999999-2222-3333-4444-555555555555")
	otherClass.Spec.Controller = testOtherControllerClass
	objects = append(objects, otherClass)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	reconciler := &CloudflareTunnelBindingReconciler{ControllerClass: testControllerClass, Client: kubeClient}
	changedService := &corev1.Service{}
	changedService.Name = testGatewayName
	changedService.Namespace = testTenantName
	requests := reconciler.bindingsForObject(context.Background(), changedService)
	if len(requests) != 1 || requests[0].Namespace != binding.Namespace || requests[0].Name != binding.Name {
		t.Fatalf("event requests = %#v, want only %s/%s", requests, binding.Namespace, binding.Name)
	}
}

func TestOriginReferenceGrantMatching(t *testing.T) {
	binding := &kflaredv1alpha1.CloudflareTunnelBinding{
		Namespace: testTenantName,
		Spec: kflaredv1alpha1.CloudflareTunnelBindingSpec{
			OriginServiceRef: kflaredv1alpha1.OriginServiceReference{Name: testGatewayName},
		},
	}
	serviceName := gatewayv1.ObjectName(testGatewayName)
	otherServiceName := gatewayv1.ObjectName("other")
	validFrom := gatewayv1.ReferenceGrantFrom{
		Group:     gatewayv1.Group(kflaredv1alpha1.GroupVersion.Group),
		Kind:      gatewayv1.Kind("CloudflareTunnelBinding"),
		Namespace: gatewayv1.Namespace(testTenantName),
	}
	validTo := gatewayv1.ReferenceGrantTo{Group: gatewayv1.Group(""), Kind: gatewayv1.Kind("Service"), Name: &serviceName}
	tests := []struct {
		name  string
		grant gatewayv1.ReferenceGrant
		want  bool
	}{
		{name: "exact service", grant: gatewayv1.ReferenceGrant{Spec: gatewayv1.ReferenceGrantSpec{From: []gatewayv1.ReferenceGrantFrom{validFrom}, To: []gatewayv1.ReferenceGrantTo{validTo}}}, want: true},
		{name: "all services when name omitted", grant: gatewayv1.ReferenceGrant{Spec: gatewayv1.ReferenceGrantSpec{From: []gatewayv1.ReferenceGrantFrom{validFrom}, To: []gatewayv1.ReferenceGrantTo{{Group: gatewayv1.Group(""), Kind: gatewayv1.Kind("Service")}}}}, want: true},
		{name: "wrong source namespace", grant: gatewayv1.ReferenceGrant{Spec: gatewayv1.ReferenceGrantSpec{From: []gatewayv1.ReferenceGrantFrom{{Group: validFrom.Group, Kind: validFrom.Kind, Namespace: gatewayv1.Namespace("other")}}, To: []gatewayv1.ReferenceGrantTo{validTo}}}},
		{name: "wrong source group", grant: gatewayv1.ReferenceGrant{Spec: gatewayv1.ReferenceGrantSpec{From: []gatewayv1.ReferenceGrantFrom{{Group: gatewayv1.Group("other.example.com"), Kind: validFrom.Kind, Namespace: validFrom.Namespace}}, To: []gatewayv1.ReferenceGrantTo{validTo}}}},
		{name: "wrong source kind", grant: gatewayv1.ReferenceGrant{Spec: gatewayv1.ReferenceGrantSpec{From: []gatewayv1.ReferenceGrantFrom{{Group: validFrom.Group, Kind: gatewayv1.Kind("HTTPRoute"), Namespace: validFrom.Namespace}}, To: []gatewayv1.ReferenceGrantTo{validTo}}}},
		{name: "wrong target group", grant: gatewayv1.ReferenceGrant{Spec: gatewayv1.ReferenceGrantSpec{From: []gatewayv1.ReferenceGrantFrom{validFrom}, To: []gatewayv1.ReferenceGrantTo{{Group: gatewayv1.Group("apps"), Kind: gatewayv1.Kind("Service"), Name: &serviceName}}}}},
		{name: "wrong target kind", grant: gatewayv1.ReferenceGrant{Spec: gatewayv1.ReferenceGrantSpec{From: []gatewayv1.ReferenceGrantFrom{validFrom}, To: []gatewayv1.ReferenceGrantTo{{Group: gatewayv1.Group(""), Kind: gatewayv1.Kind("Secret"), Name: &serviceName}}}}},
		{name: "another service", grant: gatewayv1.ReferenceGrant{Spec: gatewayv1.ReferenceGrantSpec{From: []gatewayv1.ReferenceGrantFrom{validFrom}, To: []gatewayv1.ReferenceGrantTo{{Group: gatewayv1.Group(""), Kind: gatewayv1.Kind("Service"), Name: &otherServiceName}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := referenceGrantPermitsOrigin(&tt.grant, binding); got != tt.want {
				t.Fatalf("referenceGrantPermitsOrigin() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestOriginWatchMappingUsesEffectiveNamespace(t *testing.T) {
	scheme := bindingTestScheme(t)
	objects, first := validBindingObjects()
	const originNamespace = testSharedOriginNamespace
	first.Spec.OriginServiceRef.Namespace = originNamespace
	second := first.DeepCopy()
	second.Name = "second"
	second.Namespace = "second-tenant"
	second.UID = types.UID("22222222-3333-4444-5555-666666666666")
	objects = append(objects, second)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	reconciler := &CloudflareTunnelBindingReconciler{ControllerClass: testControllerClass, Client: kubeClient}

	grantRequests := reconciler.bindingsForObject(context.Background(), &gatewayv1.ReferenceGrant{Namespace: originNamespace})
	if len(grantRequests) != 2 {
		t.Fatalf("ReferenceGrant event requests = %#v, want both bindings", grantRequests)
	}
	serviceRequests := reconciler.bindingsForObject(context.Background(), &corev1.Service{Name: testGatewayName, Namespace: originNamespace})
	if len(serviceRequests) != 2 {
		t.Fatalf("Service event requests = %#v, want both bindings", serviceRequests)
	}

	storedSecond := &kflaredv1alpha1.CloudflareTunnelBinding{}
	if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(second), storedSecond); err != nil {
		t.Fatal(err)
	}
	storedSecond.Spec.OriginServiceRef.Name = "other-origin"
	if err := kubeClient.Update(context.Background(), storedSecond); err != nil {
		t.Fatal(err)
	}
	serviceRequests = reconciler.bindingsForObject(context.Background(), &corev1.Service{Name: testGatewayName, Namespace: originNamespace})
	if len(serviceRequests) != 1 || serviceRequests[0].Namespace != first.Namespace || serviceRequests[0].Name != first.Name {
		t.Fatalf("Service event after changing one binding = %#v, want only %s/%s", serviceRequests, first.Namespace, first.Name)
	}
}

func TestCrossNamespaceOriginGrantLifecycle(t *testing.T) {
	scheme := bindingTestScheme(t)
	objects, binding := validBindingObjects()
	const originNamespace = testSharedOriginNamespace
	binding.Spec.OriginServiceRef.Namespace = originNamespace
	service := bindingTestService(t, objects)
	service.Namespace = originNamespace
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}, &kflaredv1alpha1.ClusterCloudflareProvider{}, &gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}, &appsv1.Deployment{}).
		WithObjects(objects...).Build()
	cloudflare := &fakeCloudflareClient{token: testConnectorToken}
	reconciler := &CloudflareTunnelBindingReconciler{ControllerClass: testControllerClass, Client: kubeClient, Scheme: scheme, Cloudflare: fakeCloudflareFactory{client: cloudflare}}
	request := ctrl.Request{Namespace: binding.Namespace, Name: binding.Name}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("reject origin before grant: %v", err)
	}
	assertBindingAcceptedReason(t, kubeClient, request.NamespacedName, "RefNotPermitted")
	if cloudflare.createCalls != 0 || cloudflare.updateCalls != 0 {
		t.Fatalf("unauthorized origin caused Cloudflare calls: %#v", cloudflare)
	}

	grant := originReferenceGrant(binding)
	if err := kubeClient.Create(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("add finalizer after grant: %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("program cross-namespace origin after grant: %v", err)
	}
	if cloudflare.createCalls != 1 || cloudflare.updateCalls != 1 || len(cloudflare.configuration) != 2 || cloudflare.configuration[0].Service != "http://traefik.traefik.svc.cluster.local:80" {
		t.Fatalf("unexpected authorized origin programming: %#v", cloudflare)
	}

	if err := kubeClient.Delete(context.Background(), grant); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("deprogram origin after grant removal: %v", err)
	}
	if cloudflare.updateCalls != 2 || len(cloudflare.configuration) != 1 || cloudflare.configuration[0].Service != testNotFoundOrigin {
		t.Fatalf("grant removal did not deprogram tunnel: %#v", cloudflare.configuration)
	}
	assertBindingAcceptedReason(t, kubeClient, request.NamespacedName, "RefNotPermitted")
}

func TestOriginAuthorizationPrecedesServiceLookup(t *testing.T) {
	scheme := bindingTestScheme(t)
	objects, binding := validBindingObjects()
	binding.Spec.OriginServiceRef.Namespace = testSharedOriginNamespace
	objects = withoutBindingTestService(objects)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}).
		WithObjects(objects...).Build()
	reconciler := &CloudflareTunnelBindingReconciler{ControllerClass: testControllerClass, Client: kubeClient}

	if _, result, err := reconciler.validateOriginService(context.Background(), binding); err != nil || result == nil {
		t.Fatalf("validate unauthorized missing Service: result=%v err=%v", result, err)
	}
	assertBindingAcceptedReason(t, kubeClient, client.ObjectKeyFromObject(binding), "RefNotPermitted")
}

func TestOriginServiceValidation(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func([]client.Object, *kflaredv1alpha1.CloudflareTunnelBinding) []client.Object
		wantReason string
	}{
		{name: "same namespace with omitted namespace succeeds"},
		{name: "explicit same namespace needs no grant", mutate: func(objects []client.Object, binding *kflaredv1alpha1.CloudflareTunnelBinding) []client.Object {
			binding.Spec.OriginServiceRef.Namespace = binding.Namespace
			return objects
		}},
		{name: "numeric port succeeds", mutate: func(objects []client.Object, binding *kflaredv1alpha1.CloudflareTunnelBinding) []client.Object {
			binding.Spec.OriginServiceRef.Port = intstr.FromInt32(80)
			return objects
		}},
		{name: "missing Service", wantReason: "ServiceNotFound", mutate: func(objects []client.Object, _ *kflaredv1alpha1.CloudflareTunnelBinding) []client.Object {
			return withoutBindingTestService(objects)
		}},
		{name: "authorized cross-namespace missing Service", wantReason: "ServiceNotFound", mutate: func(objects []client.Object, binding *kflaredv1alpha1.CloudflareTunnelBinding) []client.Object {
			binding.Spec.OriginServiceRef.Namespace = testSharedOriginNamespace
			return append(withoutBindingTestService(objects), originReferenceGrant(binding))
		}},
		{name: "missing port", wantReason: "ServicePortNotFound", mutate: func(objects []client.Object, binding *kflaredv1alpha1.CloudflareTunnelBinding) []client.Object {
			binding.Spec.OriginServiceRef.Port = intstr.FromString("missing")
			return objects
		}},
		{name: "authorized cross-namespace missing port", wantReason: "ServicePortNotFound", mutate: func(objects []client.Object, binding *kflaredv1alpha1.CloudflareTunnelBinding) []client.Object {
			binding.Spec.OriginServiceRef.Namespace = testSharedOriginNamespace
			binding.Spec.OriginServiceRef.Port = intstr.FromString("missing")
			bindingTestService(t, objects).Namespace = testSharedOriginNamespace
			return append(objects, originReferenceGrant(binding))
		}},
		{name: "headless Service", wantReason: testInvalidServiceReason, mutate: func(objects []client.Object, _ *kflaredv1alpha1.CloudflareTunnelBinding) []client.Object {
			bindingTestService(t, objects).Spec.ClusterIP = corev1.ClusterIPNone
			return objects
		}},
		{name: "ExternalName Service", wantReason: testInvalidServiceReason, mutate: func(objects []client.Object, _ *kflaredv1alpha1.CloudflareTunnelBinding) []client.Object {
			bindingTestService(t, objects).Spec.Type = corev1.ServiceTypeExternalName
			return objects
		}},
		{name: "NodePort Service", wantReason: testInvalidServiceReason, mutate: func(objects []client.Object, _ *kflaredv1alpha1.CloudflareTunnelBinding) []client.Object {
			bindingTestService(t, objects).Spec.Type = corev1.ServiceTypeNodePort
			return objects
		}},
		{name: "LoadBalancer Service", wantReason: testInvalidServiceReason, mutate: func(objects []client.Object, _ *kflaredv1alpha1.CloudflareTunnelBinding) []client.Object {
			bindingTestService(t, objects).Spec.Type = corev1.ServiceTypeLoadBalancer
			return objects
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := bindingTestScheme(t)
			objects, binding := validBindingObjects()
			if tt.mutate != nil {
				objects = tt.mutate(objects, binding)
			}
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).
				WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}).
				WithObjects(objects...).Build()
			reconciler := &CloudflareTunnelBindingReconciler{ControllerClass: testControllerClass, Client: kubeClient}
			origin, result, err := reconciler.validateOriginService(context.Background(), binding)
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantReason == "" {
				if result != nil || origin != "http://traefik.tenant.svc.cluster.local:80" {
					t.Fatalf("origin=%q result=%v, want same-namespace origin", origin, result)
				}
				return
			}
			if result == nil {
				t.Fatalf("validation unexpectedly succeeded with origin %q", origin)
			}
			assertBindingAcceptedReason(t, kubeClient, client.ObjectKeyFromObject(binding), tt.wantReason)
		})
	}
}

func TestBindingsInDifferentNamespacesCanShareAuthorizedOrigin(t *testing.T) {
	scheme := bindingTestScheme(t)
	objects, first := validBindingObjects()
	const originNamespace = testSharedOriginNamespace
	first.Spec.OriginServiceRef.Namespace = originNamespace
	service := bindingTestService(t, objects)
	service.Namespace = originNamespace
	firstGrant := originReferenceGrant(first)

	second := first.DeepCopy()
	second.Name = "second"
	second.Namespace = "second-tenant"
	second.UID = types.UID("22222222-3333-4444-5555-666666666666")
	secondGateway := &gatewayv1.Gateway{}
	for _, object := range objects {
		if gateway, ok := object.(*gatewayv1.Gateway); ok {
			secondGateway = gateway.DeepCopy()
			break
		}
	}
	secondGateway.Namespace = second.Namespace
	secondGrant := originReferenceGrant(second)
	secondGrant.Name = "allow-second-kflared-origin"
	objects = append(objects, firstGrant, second, secondGateway, secondGrant)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	reconciler := &CloudflareTunnelBindingReconciler{ControllerClass: testControllerClass, Client: kubeClient}

	for _, binding := range []*kflaredv1alpha1.CloudflareTunnelBinding{first, second} {
		origin, result, err := reconciler.validateOriginService(context.Background(), binding)
		if err != nil || result != nil || origin != "http://traefik.traefik.svc.cluster.local:80" {
			t.Fatalf("validate shared origin for %s/%s: origin=%q result=%v err=%v", binding.Namespace, binding.Name, origin, result, err)
		}
	}
}

func TestPrimaryWatchPredicatesFilterControllerClass(t *testing.T) {
	providerPredicate := clusterProviderClassPredicate(testControllerClass)
	matchingProvider := readyClusterProvider()
	foreignProvider := matchingProvider.DeepCopy()
	foreignProvider.Spec.Controller = testOtherControllerClass
	if !providerPredicate.Create(event.CreateEvent{Object: matchingProvider}) || providerPredicate.Create(event.CreateEvent{Object: foreignProvider}) {
		t.Fatal("provider primary watch predicate did not filter by controller class")
	}

	bindingPredicate := bindingClassPredicate(testControllerClass)
	matchingBinding := &kflaredv1alpha1.CloudflareTunnelBinding{Spec: kflaredv1alpha1.CloudflareTunnelBindingSpec{Controller: testControllerClass}}
	foreignBinding := matchingBinding.DeepCopy()
	foreignBinding.Spec.Controller = testOtherControllerClass
	if !bindingPredicate.Create(event.CreateEvent{Object: matchingBinding}) || bindingPredicate.Create(event.CreateEvent{Object: foreignBinding}) {
		t.Fatal("binding primary watch predicate did not filter by controller class")
	}
}

func TestBindingFinalizationPreservesChildOwnedByOtherBinding(t *testing.T) {
	scheme := bindingTestScheme(t)
	objects, binding := validBindingObjects()
	binding.Finalizers = []string{bindingFinalizer}
	binding.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	name := connectorResourceName(binding.UID)
	foreignDeployment := testDeployment(name, defaultSystemNamespace, map[string]string{ownerUIDLabel: "different-binding-uid"})
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objects, foreignDeployment)...).Build()
	reconciler := &CloudflareTunnelBindingReconciler{ControllerClass: testControllerClass, Client: kubeClient, Scheme: scheme}

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{Namespace: binding.Namespace, Name: binding.Name}); err == nil || !strings.Contains(err.Error(), "not owned by this binding") {
		t.Fatalf("Reconcile() error = %v, want ownership refusal", err)
	}
	actual := &appsv1.Deployment{}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Namespace: defaultSystemNamespace, Name: name}, actual); err != nil {
		t.Fatalf("foreign Deployment was deleted: %v", err)
	}
}

func TestBindingFinalizationWithForeignProviderLeavesChildrenAndTunnelAlone(t *testing.T) {
	scheme := bindingTestScheme(t)
	objects, binding := validBindingObjects()
	binding.Finalizers = []string{bindingFinalizer}
	binding.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	binding.Status.TunnelID = testTunnelID
	provider := bindingTestClusterProvider(t, objects)
	provider.Spec.Controller = testOtherControllerClass
	child := testDeployment(connectorResourceName(binding.UID), defaultSystemNamespace, managedLabels(binding))
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(append(objects, child)...).Build()
	cloudflare := &fakeCloudflareClient{}
	reconciler := &CloudflareTunnelBindingReconciler{ControllerClass: testControllerClass, Client: kubeClient, Scheme: scheme, Cloudflare: fakeCloudflareFactory{client: cloudflare}}

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{Namespace: binding.Namespace, Name: binding.Name}); err == nil || !strings.Contains(err.Error(), "belongs to controller class") {
		t.Fatalf("Reconcile() error = %v, want provider class refusal", err)
	}
	actualChild := &appsv1.Deployment{}
	if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(child), actualChild); err != nil {
		t.Fatalf("child Deployment was deleted before provider class check: %v", err)
	}
	actualBinding := &kflaredv1alpha1.CloudflareTunnelBinding{}
	if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(binding), actualBinding); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(actualBinding.Finalizers, bindingFinalizer) {
		t.Fatalf("binding finalizer was removed: %v", actualBinding.Finalizers)
	}
	if cloudflare.validateCalls+cloudflare.createCalls+cloudflare.updateCalls+cloudflare.deleteCalls != 0 {
		t.Fatalf("foreign provider caused Cloudflare calls: %#v", cloudflare)
	}
}

func TestBindingDeprogramsTunnelWhenItLosesEligibility(t *testing.T) {
	scheme := bindingTestScheme(t)
	objects, binding := validBindingObjects()
	binding.Finalizers = []string{bindingFinalizer}
	binding.Status.TunnelID = testTunnelID
	for _, object := range objects {
		if route, ok := object.(*gatewayv1.HTTPRoute); ok {
			route.Status.Parents = nil
		}
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}, &kflaredv1alpha1.ClusterCloudflareProvider{}, &gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}).
		WithObjects(objects...).Build()
	cloudflare := &fakeCloudflareClient{
		token:         testConnectorToken,
		tunnel:        &cfclient.Tunnel{ID: testTunnelID, Name: planner.TunnelName(types.UID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"), binding), ConfigSource: cfclient.ConfigSourceCloudflare},
		configuration: []cfclient.IngressRule{{Hostname: testApplicationHostname, Service: testOldOrigin}, {Service: testNotFoundOrigin}},
	}
	reconciler := &CloudflareTunnelBindingReconciler{ControllerClass: testControllerClass, Client: kubeClient, Scheme: scheme, Cloudflare: fakeCloudflareFactory{client: cloudflare}}
	request := ctrl.Request{Namespace: binding.Namespace, Name: binding.Name}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("deprogram invalid binding: %v", err)
	}
	if cloudflare.updateCalls != 1 || len(cloudflare.configuration) != 1 || cloudflare.configuration[0].Service != testNotFoundOrigin {
		t.Fatalf("tunnel was not safely deprogrammed: %#v", cloudflare.configuration)
	}
	actual := &kflaredv1alpha1.CloudflareTunnelBinding{}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
		t.Fatal(err)
	}
	programmed := apiMeta.FindStatusCondition(actual.Status.Conditions, kflaredv1alpha1.BindingConditionProgrammed)
	if programmed == nil || programmed.Status != metav1.ConditionFalse || programmed.Reason != "NoEligibleHostnames" {
		t.Fatalf("Programmed condition = %#v, want False/NoEligibleHostnames", programmed)
	}
}

func TestBindingRejectsCurrentProviderFailure(t *testing.T) {
	tests := []struct {
		name          string
		conditionType string
	}{
		{name: "invalid provider configuration", conditionType: kflaredv1alpha1.ProviderConditionAccepted},
		{name: "rejected provider credentials", conditionType: kflaredv1alpha1.ProviderConditionCredentialsValid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := bindingTestScheme(t)
			objects, binding := validBindingObjects()
			setPublishedBindingStatus(binding)
			provider := bindingTestClusterProvider(t, objects)
			condition := apiMeta.FindStatusCondition(provider.Status.Conditions, tt.conditionType)
			if condition == nil {
				t.Fatalf("provider condition %q is missing", tt.conditionType)
			}
			condition.Status = metav1.ConditionFalse
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).
				WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}, &kflaredv1alpha1.ClusterCloudflareProvider{}, &gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}).
				WithObjects(objects...).Build()
			cloudflare := &fakeCloudflareClient{
				tunnel:        &cfclient.Tunnel{ID: binding.Status.TunnelID, Name: binding.Status.TunnelName, ConfigSource: cfclient.ConfigSourceCloudflare},
				configuration: []cfclient.IngressRule{{Hostname: testApplicationHostname, Service: testOldOrigin}, {Service: testNotFoundOrigin}},
			}
			reconciler := &CloudflareTunnelBindingReconciler{ControllerClass: testControllerClass, Client: kubeClient, Scheme: scheme, Cloudflare: fakeCloudflareFactory{client: cloudflare}}
			request := ctrl.Request{Namespace: binding.Namespace, Name: binding.Name}

			if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
				t.Fatalf("reject binding: %v", err)
			}
			if cloudflare.updateCalls != 1 || len(cloudflare.configuration) != 1 || cloudflare.configuration[0].Service != testNotFoundOrigin {
				t.Fatalf("confirmed provider failure did not deprogram the tunnel: %#v", cloudflare.configuration)
			}
			actual := &kflaredv1alpha1.CloudflareTunnelBinding{}
			if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
				t.Fatal(err)
			}
			accepted := apiMeta.FindStatusCondition(actual.Status.Conditions, kflaredv1alpha1.BindingConditionAccepted)
			ready := apiMeta.FindStatusCondition(actual.Status.Conditions, kflaredv1alpha1.BindingConditionReady)
			if accepted == nil || accepted.Status != metav1.ConditionFalse || ready == nil || ready.Status != metav1.ConditionFalse {
				t.Fatalf("deterministic rejection conditions Accepted=%#v Ready=%#v, want False", accepted, ready)
			}
		})
	}
}

func TestBindingPreservesWorkingTunnelWhileProviderCredentialsAreIndeterminate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*kflaredv1alpha1.ClusterCloudflareProvider)
	}{
		{
			name: "unknown",
			mutate: func(provider *kflaredv1alpha1.ClusterCloudflareProvider) {
				apiMeta.FindStatusCondition(provider.Status.Conditions, kflaredv1alpha1.ProviderConditionCredentialsValid).Status = metav1.ConditionUnknown
			},
		},
		{
			name: "stale",
			mutate: func(provider *kflaredv1alpha1.ClusterCloudflareProvider) {
				apiMeta.FindStatusCondition(provider.Status.Conditions, kflaredv1alpha1.ProviderConditionCredentialsValid).ObservedGeneration = provider.Generation - 1
			},
		},
		{
			name: "missing",
			mutate: func(provider *kflaredv1alpha1.ClusterCloudflareProvider) {
				provider.Status.Conditions = slices.DeleteFunc(provider.Status.Conditions, func(condition metav1.Condition) bool {
					return condition.Type == kflaredv1alpha1.ProviderConditionCredentialsValid
				})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := bindingTestScheme(t)
			objects, binding := validBindingObjects()
			setPublishedBindingStatus(binding)
			tt.mutate(bindingTestClusterProvider(t, objects))
			expectedStatus := binding.DeepCopy().Status
			initialConfiguration := []cfclient.IngressRule{{Hostname: testApplicationHostname, Service: testOldOrigin}, {Service: testNotFoundOrigin}}
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).
				WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}, &kflaredv1alpha1.ClusterCloudflareProvider{}, &gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}).
				WithObjects(objects...).Build()
			cloudflare := &fakeCloudflareClient{
				tunnel:        &cfclient.Tunnel{ID: binding.Status.TunnelID, Name: binding.Status.TunnelName, ConfigSource: cfclient.ConfigSourceCloudflare},
				configuration: slices.Clone(initialConfiguration),
			}
			reconciler := &CloudflareTunnelBindingReconciler{ControllerClass: testControllerClass, Client: kubeClient, Scheme: scheme, Cloudflare: fakeCloudflareFactory{client: cloudflare}}
			request := ctrl.Request{Namespace: binding.Namespace, Name: binding.Name}

			result, err := reconciler.Reconcile(context.Background(), request)
			if !errors.Is(err, errClusterProviderStatusUnknown) {
				t.Fatalf("Reconcile() error = %v, want provider status error", err)
			}
			if result != (ctrl.Result{}) {
				t.Errorf("Reconcile() result = %#v, want zero result", result)
			}
			if cloudflare.updateCalls != 0 {
				t.Fatalf("UpdateConfiguration calls = %d, want 0", cloudflare.updateCalls)
			}
			if !reflect.DeepEqual(cloudflare.configuration, initialConfiguration) {
				t.Fatalf("Cloudflare configuration changed: %#v", cloudflare.configuration)
			}

			actual := assertOperationalFailureStatus(t, kubeClient, request.NamespacedName, expectedStatus, kflaredv1alpha1.BindingConditionReady, "ProviderStatusUnknown", "The referenced ClusterCloudflareProvider status is not currently known")
			programmed := apiMeta.FindStatusCondition(actual.Status.Conditions, kflaredv1alpha1.BindingConditionProgrammed)
			if programmed == nil || programmed.Status != metav1.ConditionTrue {
				t.Fatalf("Programmed condition = %#v, want unchanged True", programmed)
			}
		})
	}
}

func TestBindingReportsOperationalReconciliationFailures(t *testing.T) {
	tests := []struct {
		name              string
		failedCondition   string
		reason            string
		message           string
		cloudflareFailure func(*fakeCloudflareClient, error)
		objectFailure     client.Object
		objectKey         types.NamespacedName
	}{
		{
			name:            "cluster identity",
			failedCondition: kflaredv1alpha1.BindingConditionProgrammed,
			reason:          testTunnelReconciliationReason,
			message:         testTunnelReconciliationMessage,
			objectFailure:   &corev1.Namespace{},
			objectKey:       types.NamespacedName{Name: metav1.NamespaceSystem},
		},
		{
			name:            "tunnel",
			failedCondition: kflaredv1alpha1.BindingConditionProgrammed,
			reason:          testTunnelReconciliationReason,
			message:         testTunnelReconciliationMessage,
			cloudflareFailure: func(cloudflare *fakeCloudflareClient, failure error) {
				cloudflare.getTunnelErr = failure
			},
		},
		{
			name:            "configuration",
			failedCondition: kflaredv1alpha1.BindingConditionProgrammed,
			reason:          testTunnelReconciliationReason,
			message:         testTunnelReconciliationMessage,
			cloudflareFailure: func(cloudflare *fakeCloudflareClient, failure error) {
				cloudflare.getConfigErr = failure
			},
		},
		{
			name:            "token",
			failedCondition: kflaredv1alpha1.BindingConditionConnectorReady,
			reason:          testConnectorReconciliationReason,
			message:         testConnectorReconciliationMessage,
			cloudflareFailure: func(cloudflare *fakeCloudflareClient, failure error) {
				cloudflare.getTokenErr = failure
			},
		},
		{
			name:            "API token Secret",
			failedCondition: kflaredv1alpha1.BindingConditionConnectorReady,
			reason:          testConnectorReconciliationReason,
			message:         testConnectorReconciliationMessage,
			objectFailure:   &corev1.Secret{},
			objectKey:       types.NamespacedName{Namespace: defaultSystemNamespace, Name: testAPITokenSecretName},
		},
		{
			name:            "connector Secret",
			failedCondition: kflaredv1alpha1.BindingConditionConnectorReady,
			reason:          testConnectorReconciliationReason,
			message:         testConnectorReconciliationMessage,
			objectFailure:   &corev1.Secret{},
			objectKey:       types.NamespacedName{Namespace: defaultSystemNamespace, Name: testManagedResourceName},
		},
		{
			name:            "Deployment",
			failedCondition: kflaredv1alpha1.BindingConditionConnectorReady,
			reason:          testConnectorReconciliationReason,
			message:         testConnectorReconciliationMessage,
			objectFailure:   &appsv1.Deployment{},
			objectKey:       types.NamespacedName{Namespace: defaultSystemNamespace, Name: testManagedResourceName},
		},
		{
			name:            "PodDisruptionBudget",
			failedCondition: kflaredv1alpha1.BindingConditionConnectorReady,
			reason:          testConnectorReconciliationReason,
			message:         testConnectorReconciliationMessage,
			objectFailure:   &policyv1.PodDisruptionBudget{},
			objectKey:       types.NamespacedName{Namespace: defaultSystemNamespace, Name: testManagedResourceName},
		},
		{
			name:            "DNS",
			failedCondition: kflaredv1alpha1.BindingConditionDNSAutomationReady,
			reason:          "DNSReconciliationFailed",
			message:         "DNS automation reconciliation could not complete",
			objectFailure:   &unstructured.Unstructured{},
			objectKey:       types.NamespacedName{Namespace: testTenantName, Name: testManagedResourceName},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := bindingTestScheme(t)
			objects, binding := validBindingObjects()
			setPublishedBindingStatus(binding)
			expectedStatus := binding.DeepCopy().Status
			failure := errors.New("injected operational failure")
			desiredConfiguration := planner.IngressRules([]string{testApplicationHostname}, "http://traefik.tenant.svc.cluster.local:80")
			cloudflare := &fakeCloudflareClient{
				tunnel:        &cfclient.Tunnel{ID: binding.Status.TunnelID, Name: planner.TunnelName(types.UID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"), binding), ConfigSource: cfclient.ConfigSourceCloudflare},
				configuration: slices.Clone(desiredConfiguration),
				token:         testConnectorToken,
			}
			if tt.cloudflareFailure != nil {
				tt.cloudflareFailure(cloudflare, failure)
			}
			baseClient := fake.NewClientBuilder().WithScheme(scheme).
				WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}, &kflaredv1alpha1.ClusterCloudflareProvider{}, &gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}, &appsv1.Deployment{}).
				WithObjects(objects...).Build()
			var kubeClient client.Client = baseClient
			if tt.objectFailure != nil {
				kubeClient = &failingGetClient{Client: baseClient, objectType: reflect.TypeOf(tt.objectFailure), key: tt.objectKey, err: failure}
			}
			reconciler := &CloudflareTunnelBindingReconciler{
				ControllerClass: testControllerClass,
				Client:          kubeClient,
				Scheme:          scheme,
				RESTMapper:      bindingTestRESTMapper(),
				Cloudflare:      fakeCloudflareFactory{client: cloudflare},
			}
			request := ctrl.Request{Namespace: binding.Namespace, Name: binding.Name}

			result, err := reconciler.Reconcile(context.Background(), request)
			if !errors.Is(err, failure) {
				t.Fatalf("Reconcile() error = %v, want injected failure", err)
			}
			if result != (ctrl.Result{}) {
				t.Errorf("Reconcile() result = %#v, want zero result", result)
			}
			if cloudflare.updateCalls != 0 || cloudflare.deleteCalls != 0 {
				t.Fatalf("destructive Cloudflare calls update=%d delete=%d, want 0", cloudflare.updateCalls, cloudflare.deleteCalls)
			}
			if !reflect.DeepEqual(cloudflare.configuration, desiredConfiguration) {
				t.Fatalf("Cloudflare configuration changed: %#v", cloudflare.configuration)
			}
			if cloudflare.tunnel == nil {
				t.Fatal("Cloudflare tunnel was removed")
			}
			assertOperationalFailureStatus(t, kubeClient, request.NamespacedName, expectedStatus, tt.failedCondition, tt.reason, tt.message)
		})
	}
}

func TestBindingOperationalFailureReturnsCauseAndSinglePatchFailure(t *testing.T) {
	scheme := bindingTestScheme(t)
	patchFailure := errors.New("status patch failed")
	kubeClient := &failingStatusClient{
		Client:   fake.NewClientBuilder().WithScheme(scheme).Build(),
		patchErr: patchFailure,
	}
	reconciler := &CloudflareTunnelBindingReconciler{ControllerClass: testControllerClass, Client: kubeClient, Scheme: scheme}
	binding := &kflaredv1alpha1.CloudflareTunnelBinding{Name: "public", Namespace: testTenantName, Generation: 1}
	cause := errors.New("tunnel operation failed")

	err := reconciler.reportOperationalFailure(context.Background(), binding, kflaredv1alpha1.BindingConditionProgrammed, testTunnelReconciliationReason, testTunnelReconciliationMessage, cause)

	if !errors.Is(err, cause) || !errors.Is(err, patchFailure) {
		t.Fatalf("reportOperationalFailure() error = %v, want cause and patch failure", err)
	}
	if kubeClient.patchCalls != 1 {
		t.Fatalf("status patch calls = %d, want 1", kubeClient.patchCalls)
	}
}

func TestBindingFinalizationHonorsDeletionPolicy(t *testing.T) {
	tests := []struct {
		name            string
		deletionPolicy  kflaredv1alpha1.DeletionPolicy
		wantDeleteCalls int
		wantTunnel      bool
	}{
		{name: "delete", deletionPolicy: kflaredv1alpha1.DeletionPolicyDelete, wantDeleteCalls: 1},
		{name: "retain", deletionPolicy: kflaredv1alpha1.DeletionPolicyRetain, wantTunnel: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := bindingTestScheme(t)
			objects, binding := finalizingBindingObjects(tt.deletionPolicy)
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).
				WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}).
				WithObjects(objects...).Build()
			cloudflare := &fakeCloudflareClient{tunnel: &cfclient.Tunnel{ID: binding.Status.TunnelID, Name: testBindingTunnelName}}
			reconciler := &CloudflareTunnelBindingReconciler{
				ControllerClass: testControllerClass,
				Client:          kubeClient,
				Scheme:          scheme,
				RESTMapper:      bindingTestRESTMapper(),
				Cloudflare:      fakeCloudflareFactory{client: cloudflare},
			}
			request := ctrl.Request{Namespace: binding.Namespace, Name: binding.Name}

			if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
				t.Fatalf("finalize binding: %v", err)
			}
			if cloudflare.deleteCalls != tt.wantDeleteCalls {
				t.Fatalf("DeleteTunnel calls = %d, want %d", cloudflare.deleteCalls, tt.wantDeleteCalls)
			}
			if got := cloudflare.tunnel != nil; got != tt.wantTunnel {
				t.Fatalf("remote tunnel present = %t, want %t", got, tt.wantTunnel)
			}
			assertFinalizationResourcesDeleted(t, kubeClient, binding)
			assertBindingFinalized(t, kubeClient, request.NamespacedName)
		})
	}
}

func TestBindingFinalizationRetriesRemoteTunnelDeletion(t *testing.T) {
	scheme := bindingTestScheme(t)
	objects, binding := finalizingBindingObjects(kflaredv1alpha1.DeletionPolicyDelete)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}).
		WithObjects(objects...).Build()
	deleteErr := errors.New("remote deletion failed")
	cloudflare := &fakeCloudflareClient{
		deleteErr: deleteErr,
		tunnel:    &cfclient.Tunnel{ID: binding.Status.TunnelID, Name: testBindingTunnelName},
	}
	reconciler := &CloudflareTunnelBindingReconciler{
		ControllerClass: testControllerClass,
		Client:          kubeClient,
		Scheme:          scheme,
		RESTMapper:      bindingTestRESTMapper(),
		Cloudflare:      fakeCloudflareFactory{client: cloudflare},
	}
	request := ctrl.Request{Namespace: binding.Namespace, Name: binding.Name}

	if _, err := reconciler.Reconcile(context.Background(), request); !errors.Is(err, deleteErr) {
		t.Fatalf("first finalization error = %v, want %v", err, deleteErr)
	}
	if cloudflare.deleteCalls != 1 {
		t.Fatalf("DeleteTunnel calls after failure = %d, want 1", cloudflare.deleteCalls)
	}
	if cloudflare.tunnel == nil {
		t.Fatal("failed remote deletion removed the fake tunnel")
	}
	assertFinalizationResourcesDeleted(t, kubeClient, binding)
	assertBindingFinalizerPresent(t, kubeClient, request.NamespacedName)

	cloudflare.deleteErr = nil
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("retry finalization: %v", err)
	}
	if cloudflare.deleteCalls != 2 {
		t.Fatalf("DeleteTunnel calls after retry = %d, want 2", cloudflare.deleteCalls)
	}
	if cloudflare.tunnel != nil {
		t.Fatal("successful remote deletion retained the fake tunnel")
	}
	assertFinalizationResourcesDeleted(t, kubeClient, binding)
	assertBindingFinalized(t, kubeClient, request.NamespacedName)
}

func TestConnectorReplicasClampedToSupportedRange(t *testing.T) {
	tests := []struct {
		value int32
		want  int32
	}{
		{value: 0, want: 2},
		{value: 5, want: 5},
		{value: 11, want: 10},
	}
	for _, tt := range tests {
		if got := effectiveReplicas(tt.value); got != tt.want {
			t.Errorf("effectiveReplicas(%d) = %d, want %d", tt.value, got, tt.want)
		}
	}
}

func TestParentReferenceTargetsGateway(t *testing.T) {
	section := gatewayv1.SectionName(routeParentTestSectionName)
	otherNamespace := gatewayv1.Namespace("other")
	wrongGroup := gatewayv1.Group("example.com")
	wrongKind := gatewayv1.Kind("Service")
	binding := &kflaredv1alpha1.CloudflareTunnelBinding{
		Namespace: routeParentTestNamespace,
		Spec: kflaredv1alpha1.CloudflareTunnelBindingSpec{
			GatewayRef: kflaredv1alpha1.GatewayReference{Name: routeParentTestGatewayName, SectionName: routeParentTestSectionName},
		},
	}
	tests := []struct {
		name   string
		parent gatewayv1.ParentReference
		want   bool
	}{
		{
			name:   "exact local parent with defaults",
			parent: gatewayv1.ParentReference{Name: routeParentTestGatewayName, SectionName: &section},
			want:   true,
		},
		{
			name:   "same name and section in another namespace",
			parent: gatewayv1.ParentReference{Name: routeParentTestGatewayName, Namespace: &otherNamespace, SectionName: &section},
		},
		{
			name:   "wrong group",
			parent: gatewayv1.ParentReference{Group: &wrongGroup, Name: routeParentTestGatewayName, SectionName: &section},
		},
		{
			name:   "wrong kind",
			parent: gatewayv1.ParentReference{Kind: &wrongKind, Name: routeParentTestGatewayName, SectionName: &section},
		},
		{
			name:   "missing section",
			parent: gatewayv1.ParentReference{Name: routeParentTestGatewayName},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parentReferenceTargetsGateway(tt.parent, routeParentTestNamespace, binding); got != tt.want {
				t.Errorf("parentReferenceTargetsGateway() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestRouteAcceptedDoesNotBorrowStatusFromAnotherGatewayParent(t *testing.T) {
	section := gatewayv1.SectionName(routeParentTestSectionName)
	otherNamespace := gatewayv1.Namespace("other")
	binding := &kflaredv1alpha1.CloudflareTunnelBinding{
		Namespace: routeParentTestNamespace,
		Spec: kflaredv1alpha1.CloudflareTunnelBindingSpec{
			GatewayRef: kflaredv1alpha1.GatewayReference{Name: routeParentTestGatewayName, SectionName: routeParentTestSectionName},
		},
	}
	route := &gatewayv1.HTTPRoute{
		Namespace: routeParentTestNamespace, Generation: 1,
		Spec: gatewayv1.HTTPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{{
			Name: routeParentTestGatewayName, SectionName: &section,
		}}}},
		Status: gatewayv1.HTTPRouteStatus{RouteStatus: gatewayv1.RouteStatus{Parents: []gatewayv1.RouteParentStatus{{
			ParentRef:      gatewayv1.ParentReference{Name: routeParentTestGatewayName, Namespace: &otherNamespace, SectionName: &section},
			ControllerName: gatewayv1.GatewayController(traefikControllerName),
			Conditions:     []metav1.Condition{currentCondition("Accepted"), currentCondition("ResolvedRefs")},
		}}}},
	}

	if !routeTargetsBinding(route, binding) {
		t.Fatal("route should target the selected local Gateway")
	}
	if routeAccepted(route, binding) {
		t.Fatal("route should not borrow accepted status from a Gateway parent in another namespace")
	}
}

func bindingTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, kflaredv1alpha1.AddToScheme, gatewayv1.Install} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

func bindingTestRESTMapper() apiMeta.RESTMapper {
	groupVersion := schema.GroupVersion{Group: "externaldns.k8s.io", Version: "v1alpha1"}
	mapper := apiMeta.NewDefaultRESTMapper([]schema.GroupVersion{groupVersion})
	mapper.Add(groupVersion.WithKind(dnsEndpointKind), apiMeta.RESTScopeNamespace)
	return mapper
}

func setTestDNSEndpointProxyValue(t *testing.T, kubeClient client.Client, key types.NamespacedName, value string) {
	t.Helper()
	endpoint := getTestDNSEndpoint(t, kubeClient, key)
	endpoints, found, err := unstructured.NestedSlice(endpoint.Object, "spec", "endpoints")
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(endpoints) != 1 {
		t.Fatalf("DNSEndpoint spec.endpoints = %#v, want one endpoint", endpoints)
	}
	item, ok := endpoints[0].(map[string]any)
	if !ok {
		t.Fatalf("DNSEndpoint endpoint = %#v, want an object", endpoints[0])
	}
	item["providerSpecific"] = []any{map[string]any{
		"name":  "cloudflare/proxied",
		"value": value,
	}}
	if err := unstructured.SetNestedSlice(endpoint.Object, endpoints, "spec", "endpoints"); err != nil {
		t.Fatal(err)
	}
	if err := kubeClient.Update(context.Background(), endpoint); err != nil {
		t.Fatal(err)
	}
}

func assertTestDNSEndpointEndpoints(t *testing.T, kubeClient client.Client, key types.NamespacedName, expected []any) {
	t.Helper()
	endpoint := getTestDNSEndpoint(t, kubeClient, key)
	actual, found, err := unstructured.NestedSlice(endpoint.Object, "spec", "endpoints")
	if err != nil {
		t.Fatal(err)
	}
	if !found || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("DNSEndpoint spec.endpoints = %#v, want %#v", actual, expected)
	}
}

func getTestDNSEndpoint(t *testing.T, kubeClient client.Client, key types.NamespacedName) *unstructured.Unstructured {
	t.Helper()
	endpoint := &unstructured.Unstructured{}
	endpoint.SetAPIVersion(dnsEndpointAPIVersion)
	endpoint.SetKind(dnsEndpointKind)
	if err := kubeClient.Get(context.Background(), key, endpoint); err != nil {
		t.Fatal(err)
	}
	return endpoint
}

func bindingTestClusterProvider(t *testing.T, objects []client.Object) *kflaredv1alpha1.ClusterCloudflareProvider {
	t.Helper()
	for _, object := range objects {
		if provider, ok := object.(*kflaredv1alpha1.ClusterCloudflareProvider); ok {
			return provider
		}
	}
	t.Fatal("test objects do not contain a ClusterCloudflareProvider")
	return nil
}

func bindingTestService(t *testing.T, objects []client.Object) *corev1.Service {
	t.Helper()
	for _, object := range objects {
		if service, ok := object.(*corev1.Service); ok && service.Name == testGatewayName && service.Namespace == testTenantName {
			return service
		}
	}
	t.Fatal("test objects do not contain the origin Service")
	return nil
}

func withoutBindingTestService(objects []client.Object) []client.Object {
	filtered := make([]client.Object, 0, len(objects))
	for _, object := range objects {
		service, ok := object.(*corev1.Service)
		if ok && service.Name == testGatewayName && service.Namespace == testTenantName {
			continue
		}
		filtered = append(filtered, object)
	}
	return filtered
}

func originReferenceGrant(binding *kflaredv1alpha1.CloudflareTunnelBinding) *gatewayv1.ReferenceGrant {
	name := gatewayv1.ObjectName(binding.Spec.OriginServiceRef.Name)
	grant := &gatewayv1.ReferenceGrant{
		Name: "allow-kflared-origin", Namespace: testSharedOriginNamespace,
		Spec: gatewayv1.ReferenceGrantSpec{
			From: []gatewayv1.ReferenceGrantFrom{{
				Group:     gatewayv1.Group(kflaredv1alpha1.GroupVersion.Group),
				Kind:      gatewayv1.Kind("CloudflareTunnelBinding"),
				Namespace: gatewayv1.Namespace(binding.Namespace),
			}},
			To: []gatewayv1.ReferenceGrantTo{{Group: gatewayv1.Group(""), Kind: gatewayv1.Kind("Service"), Name: &name}},
		},
	}
	return grant
}

func assertBindingAcceptedReason(t *testing.T, kubeClient client.Client, key types.NamespacedName, reason string) {
	t.Helper()
	binding := &kflaredv1alpha1.CloudflareTunnelBinding{}
	if err := kubeClient.Get(context.Background(), key, binding); err != nil {
		t.Fatal(err)
	}
	accepted := apiMeta.FindStatusCondition(binding.Status.Conditions, kflaredv1alpha1.BindingConditionAccepted)
	if accepted == nil || accepted.Status != metav1.ConditionFalse || accepted.Reason != reason {
		t.Fatalf("Accepted condition = %#v, want False/%s", accepted, reason)
	}
}

func setPublishedBindingStatus(binding *kflaredv1alpha1.CloudflareTunnelBinding) {
	binding.Finalizers = []string{bindingFinalizer}
	binding.Status = kflaredv1alpha1.CloudflareTunnelBindingStatus{
		ObservedGeneration: binding.Generation,
		TunnelID:           testTunnelID,
		TunnelName:         testBindingTunnelName,
		TunnelCNAME:        "tunnel-id.cfargotunnel.com",
		PublishedHostnames: []string{testApplicationHostname},
		DNSRecords: []kflaredv1alpha1.DNSRecord{{
			Hostname: testApplicationHostname,
			Type:     "CNAME",
			Target:   "tunnel-id.cfargotunnel.com",
		}},
		Resources: kflaredv1alpha1.ConnectorResourceNames{
			Deployment:          testManagedResourceName,
			PodDisruptionBudget: testManagedResourceName,
			Secret:              testManagedResourceName,
			DNSEndpoint:         testManagedResourceName,
		},
		Conditions: []metav1.Condition{
			currentCondition(kflaredv1alpha1.BindingConditionAccepted),
			currentCondition(kflaredv1alpha1.BindingConditionProgrammed),
			currentCondition(kflaredv1alpha1.BindingConditionReady),
		},
	}
}

func assertOperationalFailureStatus(t *testing.T, kubeClient client.Client, key types.NamespacedName, expected kflaredv1alpha1.CloudflareTunnelBindingStatus, failedCondition, reason, message string) *kflaredv1alpha1.CloudflareTunnelBinding {
	t.Helper()
	actual := &kflaredv1alpha1.CloudflareTunnelBinding{}
	if err := kubeClient.Get(context.Background(), key, actual); err != nil {
		t.Fatal(err)
	}
	expected.Conditions = actual.Status.Conditions
	if !reflect.DeepEqual(actual.Status, expected) {
		t.Fatalf("operational failure changed recovery status:\n got: %#v\nwant: %#v", actual.Status, expected)
	}
	accepted := apiMeta.FindStatusCondition(actual.Status.Conditions, kflaredv1alpha1.BindingConditionAccepted)
	if accepted == nil || accepted.Status != metav1.ConditionTrue || accepted.ObservedGeneration != actual.Generation {
		t.Fatalf("Accepted condition = %#v, want current True", accepted)
	}
	failed := apiMeta.FindStatusCondition(actual.Status.Conditions, failedCondition)
	if failed == nil || failed.Status != metav1.ConditionUnknown || failed.Reason != reason || failed.Message != message || failed.ObservedGeneration != actual.Generation {
		t.Fatalf("%s condition = %#v, want current Unknown/%s", failedCondition, failed, reason)
	}
	ready := apiMeta.FindStatusCondition(actual.Status.Conditions, kflaredv1alpha1.BindingConditionReady)
	if ready == nil || ready.Status != metav1.ConditionUnknown || ready.Reason != reason || ready.Message != message || ready.ObservedGeneration != actual.Generation {
		t.Fatalf("Ready condition = %#v, want current Unknown/%s", ready, reason)
	}
	return actual
}

func finalizingBindingObjects(deletionPolicy kflaredv1alpha1.DeletionPolicy) ([]client.Object, *kflaredv1alpha1.CloudflareTunnelBinding) {
	objects, binding := validBindingObjects()
	deletionTimestamp := metav1.NewTime(time.Unix(200, 0))
	binding.DeletionTimestamp = &deletionTimestamp
	binding.Finalizers = []string{bindingFinalizer}
	binding.Spec.DeletionPolicy = deletionPolicy
	binding.Status.TunnelID = testTunnelID

	resourceName := connectorResourceName(binding.UID)
	endpoint := &unstructured.Unstructured{}
	endpoint.SetAPIVersion(dnsEndpointAPIVersion)
	endpoint.SetKind(dnsEndpointKind)
	endpoint.SetName(resourceName)
	endpoint.SetNamespace(binding.Namespace)
	endpoint.SetLabels(managedLabels(binding))
	pdb := &policyv1.PodDisruptionBudget{}
	pdb.Name = resourceName
	pdb.Namespace = defaultSystemNamespace
	pdb.Labels = managedLabels(binding)
	secret := &corev1.Secret{}
	secret.Name = resourceName
	secret.Namespace = defaultSystemNamespace
	secret.Labels = managedLabels(binding)
	return append(objects,
		testDeployment(resourceName, defaultSystemNamespace, managedLabels(binding)),
		pdb,
		secret,
		endpoint,
	), binding
}

func assertFinalizationResourcesDeleted(t *testing.T, kubeClient client.Client, binding *kflaredv1alpha1.CloudflareTunnelBinding) {
	t.Helper()
	resourceName := connectorResourceName(binding.UID)
	endpoint := &unstructured.Unstructured{}
	endpoint.SetAPIVersion(dnsEndpointAPIVersion)
	endpoint.SetKind(dnsEndpointKind)
	objects := []client.Object{
		&appsv1.Deployment{Name: resourceName, Namespace: defaultSystemNamespace},
		&policyv1.PodDisruptionBudget{Name: resourceName, Namespace: defaultSystemNamespace},
		&corev1.Secret{Name: resourceName, Namespace: defaultSystemNamespace},
		endpoint,
	}
	objects[3].SetName(resourceName)
	objects[3].SetNamespace(binding.Namespace)
	for _, object := range objects {
		err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(object), object)
		if !apierrors.IsNotFound(err) {
			t.Errorf("get deleted %T %s/%s: got error %v, want NotFound", object, object.GetNamespace(), object.GetName(), err)
		}
	}
}

func assertBindingFinalizerPresent(t *testing.T, kubeClient client.Client, key types.NamespacedName) {
	t.Helper()
	binding := &kflaredv1alpha1.CloudflareTunnelBinding{}
	if err := kubeClient.Get(context.Background(), key, binding); err != nil {
		t.Fatalf("get binding after failed finalization: %v", err)
	}
	if !slices.Contains(binding.Finalizers, bindingFinalizer) {
		t.Fatalf("binding finalizers after failed finalization = %v, want %q", binding.Finalizers, bindingFinalizer)
	}
}

func assertBindingFinalized(t *testing.T, kubeClient client.Client, key types.NamespacedName) {
	t.Helper()
	binding := &kflaredv1alpha1.CloudflareTunnelBinding{}
	err := kubeClient.Get(context.Background(), key, binding)
	if apierrors.IsNotFound(err) {
		return
	}
	if err != nil {
		t.Fatalf("get finalized binding: %v", err)
	}
	if slices.Contains(binding.Finalizers, bindingFinalizer) {
		t.Fatalf("binding finalizers after successful finalization = %v, do not want %q", binding.Finalizers, bindingFinalizer)
	}
}

func validBindingObjects() ([]client.Object, *kflaredv1alpha1.CloudflareTunnelBinding) {
	section := gatewayv1.SectionName(testHTTPSectionName)
	binding := &kflaredv1alpha1.CloudflareTunnelBinding{
		Name: "public", Namespace: testTenantName, UID: types.UID("11111111-2222-3333-4444-555555555555"), Generation: 1, CreationTimestamp: metav1.NewTime(time.Unix(100, 0)),
		Spec: kflaredv1alpha1.CloudflareTunnelBindingSpec{
			Controller:           testControllerClass,
			ProviderRef:          kflaredv1alpha1.LocalReference{Name: testDefaultName},
			GatewayRef:           kflaredv1alpha1.GatewayReference{Name: testGatewayName, SectionName: testHTTPSectionName},
			OriginServiceRef:     kflaredv1alpha1.OriginServiceReference{Name: testGatewayName, Port: intstr.FromString(testOriginPortName)},
			ConnectorReplicas:    2,
			DeletionPolicy:       kflaredv1alpha1.DeletionPolicyDelete,
			DNSAutomationEnabled: new(true),
		},
	}
	gateway := &gatewayv1.Gateway{
		Name: testGatewayName, Namespace: testTenantName, Generation: 1,
		Spec: gatewayv1.GatewaySpec{GatewayClassName: testGatewayName, Listeners: []gatewayv1.Listener{{Name: testHTTPSectionName, Protocol: gatewayv1.HTTPProtocolType, Port: 80}}},
		Status: gatewayv1.GatewayStatus{Conditions: []metav1.Condition{
			currentCondition("Accepted"),
			currentCondition("Programmed"),
		}},
	}
	parent := gatewayv1.ParentReference{Name: testGatewayName, SectionName: &section}
	route := &gatewayv1.HTTPRoute{
		Name: "application", Namespace: testTenantName, Generation: 1,
		Spec: gatewayv1.HTTPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{parent}}, Hostnames: []gatewayv1.Hostname{testApplicationHostname}},
		Status: gatewayv1.HTTPRouteStatus{RouteStatus: gatewayv1.RouteStatus{Parents: []gatewayv1.RouteParentStatus{{
			ParentRef: parent, ControllerName: gatewayv1.GatewayController(traefikControllerName),
			Conditions: []metav1.Condition{currentCondition("Accepted"), currentCondition("ResolvedRefs")},
		}}}},
	}
	return []client.Object{
		readyClusterProvider(),
		binding,
		&corev1.Namespace{Name: testTenantName, Labels: map[string]string{testTenantName: "allowed"}},
		&corev1.Namespace{Name: metav1.NamespaceSystem, UID: types.UID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")},
		&corev1.Secret{Name: testAPITokenSecretName, Namespace: defaultSystemNamespace, Data: map[string][]byte{testAPITokenSecretKey: []byte(testAPITokenSecretKey)}},
		&gatewayv1.GatewayClass{Name: testGatewayName, Spec: gatewayv1.GatewayClassSpec{ControllerName: gatewayv1.GatewayController(traefikControllerName)}},
		gateway,
		&corev1.Service{Name: testGatewayName, Namespace: testTenantName, Spec: corev1.ServiceSpec{ClusterIP: "10.0.0.10", Ports: []corev1.ServicePort{{Name: testOriginPortName, Port: 80}}}},
		route,
	}, binding
}
