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
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	kflaredv1alpha1 "github.com/kode-blox/kflared/api/v1alpha1"
	cfclient "github.com/kode-blox/kflared/internal/cloudflare"
	"github.com/kode-blox/kflared/internal/planner"
)

func TestBindingReconcileCreatesIdempotentTunnelAndHardenedConnectors(t *testing.T) {
	scheme := bindingTestScheme(t)
	objects, binding := validBindingObjects()
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}, &kflaredv1alpha1.CloudflareProvider{}, &gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}, &appsv1.Deployment{}).
		WithObjects(objects...).Build()
	cloudflare := &fakeCloudflareClient{token: "connector-token"}
	reconciler := &CloudflareTunnelBindingReconciler{Client: kubeClient, Scheme: scheme, Cloudflare: fakeCloudflareFactory{client: cloudflare}}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: binding.Namespace, Name: binding.Name}}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("add finalizer: %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("create tunnel and connectors: %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("idempotent reconcile: %v", err)
	}
	if cloudflare.createCalls != 1 || cloudflare.updateCalls != 1 {
		t.Fatalf("Cloudflare calls create=%d update=%d, want 1 each", cloudflare.createCalls, cloudflare.updateCalls)
	}
	if len(cloudflare.configuration) != 2 || cloudflare.configuration[0].Hostname != "app.example.com" || cloudflare.configuration[1].Service != "http_status:404" {
		t.Fatalf("unexpected ingress: %#v", cloudflare.configuration)
	}

	actualBinding := &kflaredv1alpha1.CloudflareTunnelBinding{}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, actualBinding); err != nil {
		t.Fatal(err)
	}
	if actualBinding.Status.TunnelID != "tunnel-id" || len(actualBinding.Status.DNSRecords) != 1 {
		t.Fatalf("unexpected binding status: %#v", actualBinding.Status)
	}
	resourceName := connectorResourceName(binding.UID)
	deployment := &appsv1.Deployment{}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Namespace: defaultSystemNamespace, Name: resourceName}, deployment); err != nil {
		t.Fatal(err)
	}
	pod := deployment.Spec.Template.Spec
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken || pod.SecurityContext == nil || pod.SecurityContext.SeccompProfile == nil {
		t.Fatalf("connector pod is not hardened: %#v", pod)
	}
	containerSecurity := pod.Containers[0].SecurityContext
	if containerSecurity == nil || containerSecurity.ReadOnlyRootFilesystem == nil || !*containerSecurity.ReadOnlyRootFilesystem {
		t.Fatalf("connector container is not hardened: %#v", pod.Containers)
	}
	secret := &corev1.Secret{}
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Namespace: defaultSystemNamespace, Name: resourceName}, secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["token"]) != "connector-token" {
		t.Fatal("connector token Secret was not reconciled")
	}
}

func TestBindingDeprogramsTunnelWhenItLosesEligibility(t *testing.T) {
	scheme := bindingTestScheme(t)
	objects, binding := validBindingObjects()
	binding.Finalizers = []string{bindingFinalizer}
	binding.Status.TunnelID = "tunnel-id"
	for _, object := range objects {
		if route, ok := object.(*gatewayv1.HTTPRoute); ok {
			route.Status.Parents = nil
		}
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}, &kflaredv1alpha1.CloudflareProvider{}, &gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}).
		WithObjects(objects...).Build()
	cloudflare := &fakeCloudflareClient{
		token:         "connector-token",
		tunnel:        &cfclient.Tunnel{ID: "tunnel-id", Name: planner.TunnelName(types.UID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"), binding), ConfigSource: cfclient.ConfigSourceCloudflare},
		configuration: []cfclient.IngressRule{{Hostname: "app.example.com", Service: "http://old"}, {Service: "http_status:404"}},
	}
	reconciler := &CloudflareTunnelBindingReconciler{Client: kubeClient, Scheme: scheme, Cloudflare: fakeCloudflareFactory{client: cloudflare}}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: binding.Namespace, Name: binding.Name}}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("deprogram invalid binding: %v", err)
	}
	if cloudflare.updateCalls != 1 || len(cloudflare.configuration) != 1 || cloudflare.configuration[0].Service != "http_status:404" {
		t.Fatalf("tunnel was not safely deprogrammed: %#v", cloudflare.configuration)
	}
}

func TestConnectorReplicasNeverDropBelowTwo(t *testing.T) {
	if got := effectiveReplicas(0); got != 2 {
		t.Fatalf("effectiveReplicas(0) = %d", got)
	}
	if got := effectiveReplicas(5); got != 5 {
		t.Fatalf("effectiveReplicas(5) = %d", got)
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

func validBindingObjects() ([]client.Object, *kflaredv1alpha1.CloudflareTunnelBinding) {
	section := gatewayv1.SectionName("http")
	binding := &kflaredv1alpha1.CloudflareTunnelBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "public", Namespace: "tenant", UID: types.UID("11111111-2222-3333-4444-555555555555"), Generation: 1, CreationTimestamp: metav1.NewTime(time.Unix(100, 0))},
		Spec: kflaredv1alpha1.CloudflareTunnelBindingSpec{
			ProviderRef:       kflaredv1alpha1.LocalReference{Name: "default"},
			GatewayRef:        kflaredv1alpha1.GatewayReference{Name: "traefik", SectionName: "http"},
			GatewayServiceRef: kflaredv1alpha1.GatewayServiceReference{Name: "traefik", Port: intstr.FromString("web")},
			ConnectorReplicas: 2,
			DeletionPolicy:    kflaredv1alpha1.DeletionPolicyDelete,
		},
	}
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: "traefik", Namespace: "tenant", Generation: 1},
		Spec:       gatewayv1.GatewaySpec{GatewayClassName: "traefik", Listeners: []gatewayv1.Listener{{Name: "http", Protocol: gatewayv1.HTTPProtocolType, Port: 80}}},
		Status: gatewayv1.GatewayStatus{Conditions: []metav1.Condition{
			currentCondition("Accepted", metav1.ConditionTrue),
			currentCondition("Programmed", metav1.ConditionTrue),
		}},
	}
	parent := gatewayv1.ParentReference{Name: "traefik", SectionName: &section}
	route := &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: "application", Namespace: "tenant", Generation: 1},
		Spec:       gatewayv1.HTTPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{parent}}, Hostnames: []gatewayv1.Hostname{"app.example.com"}},
		Status: gatewayv1.HTTPRouteStatus{RouteStatus: gatewayv1.RouteStatus{Parents: []gatewayv1.RouteParentStatus{{
			ParentRef: parent, ControllerName: gatewayv1.GatewayController(traefikControllerName),
			Conditions: []metav1.Condition{currentCondition("Accepted", metav1.ConditionTrue), currentCondition("ResolvedRefs", metav1.ConditionTrue)},
		}}}},
	}
	return []client.Object{
		readyProvider(),
		binding,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant", Labels: map[string]string{"tenant": "allowed"}}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: metav1.NamespaceSystem, UID: types.UID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "cloudflare-api-token", Namespace: defaultSystemNamespace}, Data: map[string][]byte{"api-token": []byte("api-token")}},
		&gatewayv1.GatewayClass{ObjectMeta: metav1.ObjectMeta{Name: "traefik"}, Spec: gatewayv1.GatewayClassSpec{ControllerName: gatewayv1.GatewayController(traefikControllerName)}},
		gateway,
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "traefik", Namespace: "tenant"}, Spec: corev1.ServiceSpec{ClusterIP: "10.0.0.10", Ports: []corev1.ServicePort{{Name: "web", Port: 80}}}},
		route,
	}, binding
}
