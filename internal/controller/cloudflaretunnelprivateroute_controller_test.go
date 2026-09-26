package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	kflaredv1alpha1 "github.com/kode-blox/kflared/api/v1alpha1"
	cfclient "github.com/kode-blox/kflared/internal/cloudflare"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const testKubernetesServiceName = "kubernetes"

func TestPrivateGrantPermits(t *testing.T) {
	route := &kflaredv1alpha1.CloudflareTunnelPrivateRoute{Namespace: testTenantName, Spec: kflaredv1alpha1.CloudflareTunnelPrivateRouteSpec{ServiceRef: kflaredv1alpha1.PrivateRouteServiceReference{Name: testKubernetesServiceName, Namespace: testDefaultName}}}
	service := gatewayv1.ObjectName(testKubernetesServiceName)
	grant := &gatewayv1.ReferenceGrant{Spec: gatewayv1.ReferenceGrantSpec{From: []gatewayv1.ReferenceGrantFrom{{Group: gatewayv1.Group(kflaredv1alpha1.GroupVersion.Group), Kind: testPrivateRouteKind, Namespace: testTenantName}}, To: []gatewayv1.ReferenceGrantTo{{Group: "", Kind: "Service", Name: &service}}}}
	if !privateGrantPermits(grant, route) {
		t.Fatal("expected exact grant")
	}
	grant.Spec.From[0].Kind = "CloudflareTunnelBinding"
	if privateGrantPermits(grant, route) {
		t.Fatal("binding grant must not authorize a private route")
	}
	grant.Spec.From[0].Kind = testPrivateRouteKind
	grant.Spec.From[0].Namespace = "other"
	if privateGrantPermits(grant, route) {
		t.Fatal("different source namespace must not be authorized")
	}
}

func TestPrivateRouteIdentity(t *testing.T) {
	uid := types.UID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	route := &kflaredv1alpha1.CloudflareTunnelPrivateRoute{UID: uid}
	route.Status.TunnelID = "tunnel-1"
	current := &cfclient.PrivateRoute{ID: testPrivateRouteID, TunnelID: "tunnel-1", Comment: privateComment(route)}
	if !privateOwned(current, route) {
		t.Fatal("expected UID-tagged route ownership")
	}
	current.Comment = "someone else"
	if privateOwned(current, route) {
		t.Fatal("must not adopt an untagged route")
	}
	current.Comment = privateComment(route)
	current.VirtualNetworkID = "other-vnet"
	if !privateOwned(current, route) {
		t.Fatal("Cloudflare may return a default virtual network ID")
	}
	current.VirtualNetworkID = ""
	current.TunnelID = "another-tunnel"
	if privateOwned(current, route) {
		t.Fatal("must not adopt route for another tunnel")
	}
}

func TestPrivateRoutePrecedence(t *testing.T) {
	now := metav1.NewTime(time.Now())
	older := &kflaredv1alpha1.CloudflareTunnelPrivateRoute{Name: "z", Namespace: testTenantName, UID: "first", CreationTimestamp: now}
	later := older.DeepCopy()
	later.Name = "a"
	later.UID = testSecondName
	later.CreationTimestamp = metav1.NewTime(now.Add(time.Minute))
	if !privatePrecedes(older, later) || privatePrecedes(later, older) {
		t.Fatal("older object must win regardless of name")
	}
	later.CreationTimestamp = now
	if !privatePrecedes(later, older) {
		t.Fatal("name must break identical timestamp ties")
	}
}

func TestPrivateTunnelNameUsesFullUIDs(t *testing.T) {
	name := privateTunnelName("11111111-2222-3333-4444-555555555555", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	if len(name) > 63 || !strings.HasSuffix(name, "-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee") {
		t.Fatalf("unexpected tunnel name %q", name)
	}
}

func TestPrivateRouteReconcileServiceIPAndGrantRemoval(t *testing.T) {
	ctx := context.Background()
	scheme := bindingTestScheme(t)
	provider := readyClusterProvider()
	route := &kflaredv1alpha1.CloudflareTunnelPrivateRoute{Name: testPrivateRouteName, Namespace: testTenantName, UID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Generation: 1, Spec: kflaredv1alpha1.CloudflareTunnelPrivateRouteSpec{Controller: testControllerClass, ProviderRef: kflaredv1alpha1.LocalReference{Name: provider.Name}, ServiceRef: kflaredv1alpha1.PrivateRouteServiceReference{Name: testKubernetesServiceName, Namespace: testDefaultName, Port: intstr.FromInt32(443)}}}
	name := gatewayv1.ObjectName(testKubernetesServiceName)
	grant := &gatewayv1.ReferenceGrant{Name: "allow-api", Namespace: testDefaultName, Spec: gatewayv1.ReferenceGrantSpec{From: []gatewayv1.ReferenceGrantFrom{{Group: gatewayv1.Group(kflaredv1alpha1.GroupVersion.Group), Kind: testPrivateRouteKind, Namespace: gatewayv1.Namespace(testTenantName)}}, To: []gatewayv1.ReferenceGrantTo{{Group: "", Kind: "Service", Name: &name}}}}
	service := &corev1.Service{Name: testKubernetesServiceName, Namespace: testDefaultName, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.43.0.1", Ports: []corev1.ServicePort{{Port: 443}}}}
	objects := []client.Object{provider, route, grant, service, &corev1.Namespace{Name: testTenantName, Labels: map[string]string{testTenantName: testAllowedLabelValue}}, &corev1.Namespace{Name: metav1.NamespaceSystem, UID: "11111111-2222-3333-4444-555555555555"}, &corev1.Namespace{Name: defaultSystemNamespace}, &corev1.Secret{Name: testAPITokenSecretName, Namespace: defaultSystemNamespace, Data: map[string][]byte{testAPITokenSecretKey: []byte("api-token")}}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelPrivateRoute{}, &kflaredv1alpha1.ClusterCloudflareProvider{}, &appsv1.Deployment{}).WithObjects(objects...).Build()
	cloudflare := &fakeCloudflareClient{token: testConnectorToken}
	r := &CloudflareTunnelPrivateRouteReconciler{Client: kube, Scheme: scheme, Cloudflare: fakeCloudflareFactory{client: cloudflare}, ControllerClass: testControllerClass}
	req := ctrl.Request{Namespace: route.Namespace, Name: route.Name}
	for i := range 3 {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("initial reconcile %d: %v", i, err)
		}
	}
	if cloudflare.createRouteCalls != 1 || len(cloudflare.privateRoutes) != 1 || cloudflare.privateRoutes[0].Network != testPrivateRouteNetwork {
		t.Fatalf("initial routes: %#v", cloudflare.privateRoutes)
	}
	loser := route.DeepCopy()
	loser.Name = "zzz-api"
	loser.UID = "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
	loser.ResourceVersion = ""
	loser.Finalizers = nil
	loser.Status = kflaredv1alpha1.CloudflareTunnelPrivateRouteStatus{}
	if err := kube.Create(ctx, loser); err != nil {
		t.Fatal(err)
	}
	loserRequest := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(loser)}
	if _, err := r.Reconcile(ctx, loserRequest); err != nil {
		t.Fatalf("add losing route finalizer: %v", err)
	}
	result, err := r.Reconcile(ctx, loserRequest)
	if err != nil || result.RequeueAfter == 0 || cloudflare.createRouteCalls != 1 {
		t.Fatalf("losing route should wait for conflict resolution: result=%#v err=%v creates=%d", result, err, cloudflare.createRouteCalls)
	}
	connector := &appsv1.Deployment{}
	if err := kube.Get(ctx, types.NamespacedName{Namespace: defaultSystemNamespace, Name: privateResourceName(route.UID)}, connector); err != nil {
		t.Fatal(err)
	}
	if connector.Spec.Template.Spec.AutomountServiceAccountToken == nil || *connector.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatal("connector must not mount a Kubernetes ServiceAccount token")
	}
	connector.Generation = 2
	if err := kube.Update(ctx, connector); err != nil {
		t.Fatal(err)
	}
	connector.Status.AvailableReplicas = 2
	connector.Status.ObservedGeneration = 1
	if err := kube.Status().Update(ctx, connector); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile connector rollout: %v", err)
	}
	actualRoute := &kflaredv1alpha1.CloudflareTunnelPrivateRoute{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(route), actualRoute); err != nil {
		t.Fatal(err)
	}
	if conditionTrue(actualRoute.Status.Conditions, actualRoute.Generation, kflaredv1alpha1.PrivateRouteConditionConnectorReady) {
		t.Fatal("stale available replicas must not mark the new connector rollout ready")
	}
	verifyPrivateRouteTransitions(t, privateRouteTestState{
		ctx: ctx, kube: kube, reconciler: r, route: route, service: service,
		grant: grant, cloudflare: cloudflare, loser: loser,
		request: req, loserRequest: loserRequest,
	})
}

type privateRouteTestState struct {
	ctx          context.Context
	kube         client.Client
	reconciler   *CloudflareTunnelPrivateRouteReconciler
	route        *kflaredv1alpha1.CloudflareTunnelPrivateRoute
	service      *corev1.Service
	grant        *gatewayv1.ReferenceGrant
	cloudflare   *fakeCloudflareClient
	loser        *kflaredv1alpha1.CloudflareTunnelPrivateRoute
	request      ctrl.Request
	loserRequest ctrl.Request
}

func verifyPrivateRouteTransitions(t *testing.T, state privateRouteTestState) {
	t.Helper()
	ctx, kube, r := state.ctx, state.kube, state.reconciler
	route, service, grant := state.route, state.service, state.grant
	cloudflare, loser := state.cloudflare, state.loser
	req, loserRequest := state.request, state.loserRequest
	if err := kube.Get(ctx, client.ObjectKeyFromObject(service), service); err != nil {
		t.Fatal(err)
	}
	service.Spec.ClusterIP = "10.43.0.2"
	if err := kube.Update(ctx, service); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("Service IP transition: %v", err)
	}
	if cloudflare.deleteRouteCalls != 1 || cloudflare.createRouteCalls != 2 || len(cloudflare.privateRoutes) != 1 || cloudflare.privateRoutes[0].Network != "10.43.0.2/32" {
		t.Fatalf("routes after IP change: %#v", cloudflare.privateRoutes)
	}
	if err := kube.Delete(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("grant removal: %v", err)
	}
	if len(cloudflare.privateRoutes) != 0 {
		t.Fatalf("route remained after grant removal: %#v", cloudflare.privateRoutes)
	}
	grant.ResourceVersion = ""
	grant.UID = ""
	if err := kube.Create(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("restore route after grant: %v", err)
	}
	cloudflare.tunnel = nil // Simulate deletion outside KFlared while its route remains.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("recover remotely deleted tunnel: %v", err)
	}
	if cloudflare.createCalls != 2 || cloudflare.createRouteCalls != 4 || cloudflare.deleteRouteCalls != 3 || len(cloudflare.privateRoutes) != 1 {
		t.Fatalf("remote tunnel recovery did not replace the stale route: tunnels=%d createdRoutes=%d deletedRoutes=%d routes=%#v", cloudflare.createCalls, cloudflare.createRouteCalls, cloudflare.deleteRouteCalls, cloudflare.privateRoutes)
	}
	actualRoute := &kflaredv1alpha1.CloudflareTunnelPrivateRoute{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(route), actualRoute); err != nil {
		t.Fatal(err)
	}
	if err := kube.Delete(ctx, actualRoute); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("finalize private route: %v", err)
	}
	if len(cloudflare.privateRoutes) != 0 || cloudflare.tunnel != nil {
		t.Fatalf("Delete policy left Cloudflare state: tunnel=%#v routes=%#v", cloudflare.tunnel, cloudflare.privateRoutes)
	}
	if _, err := r.Reconcile(ctx, loserRequest); err != nil {
		t.Fatalf("losing route did not recover after winner deletion: %v", err)
	}
	if len(cloudflare.privateRoutes) != 1 || cloudflare.privateRoutes[0].Comment != privateComment(loser) {
		t.Fatalf("winner deletion did not release route: %#v", cloudflare.privateRoutes)
	}
}

func TestPrivateRouteRetainRemovesConnectorButPreservesCloudflareState(t *testing.T) {
	ctx := context.Background()
	scheme := bindingTestScheme(t)
	route := &kflaredv1alpha1.CloudflareTunnelPrivateRoute{
		Name: testPrivateRouteName, Namespace: testTenantName, UID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Finalizers: []string{privateFinalizer}, DeletionTimestamp: &metav1.Time{Time: time.Now()},
		Spec:   kflaredv1alpha1.CloudflareTunnelPrivateRouteSpec{Controller: testControllerClass, DeletionPolicy: kflaredv1alpha1.DeletionPolicyRetain},
		Status: kflaredv1alpha1.CloudflareTunnelPrivateRouteStatus{TunnelID: testTunnelID, RouteID: testPrivateRouteID, Network: testPrivateRouteNetwork},
	}
	child := &corev1.Secret{Name: privateResourceName(route.UID), Namespace: defaultSystemNamespace, UID: "child-uid", Labels: privateLabels(route)}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(route, child).Build()
	cloudflare := &fakeCloudflareClient{tunnel: &cfclient.Tunnel{ID: testTunnelID, Name: "existing"}, privateRoutes: []cfclient.PrivateRoute{{ID: testPrivateRouteID, Network: testPrivateRouteNetwork, TunnelID: testTunnelID, Comment: privateComment(route)}}}
	r := &CloudflareTunnelPrivateRouteReconciler{Client: kube, Scheme: scheme, Cloudflare: fakeCloudflareFactory{client: cloudflare}, ControllerClass: testControllerClass}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(route)}); err != nil {
		t.Fatalf("finalize retained private route: %v", err)
	}
	if cloudflare.deleteCalls != 0 || cloudflare.deleteRouteCalls != 0 || len(cloudflare.privateRoutes) != 1 {
		t.Fatalf("Retain policy changed Cloudflare state: %#v", cloudflare)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(child), &corev1.Secret{}); err == nil {
		t.Fatal("Retain policy left the connector token Secret")
	}
}

func TestPrivateRouteSingleConnector(t *testing.T) {
	ctx := context.Background()
	scheme := bindingTestScheme(t)
	route := &kflaredv1alpha1.CloudflareTunnelPrivateRoute{
		ObjectMeta: metav1.ObjectMeta{Name: testPrivateRouteName, Namespace: testTenantName, UID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"},
		Spec:       kflaredv1alpha1.CloudflareTunnelPrivateRouteSpec{ConnectorReplicas: 1},
	}
	name := privateResourceName(route.UID)
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(route).WithObjects(route).Build()
	r := &CloudflareTunnelPrivateRouteReconciler{Client: kube, Scheme: scheme}
	deployment, err := r.ensureConnector(ctx, route, testConnectorToken)
	if err != nil {
		t.Fatalf("ensure single connector: %v", err)
	}
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 {
		t.Fatalf("deployment replicas = %v, want 1", deployment.Spec.Replicas)
	}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: defaultSystemNamespace, Name: name}, &policyv1.PodDisruptionBudget{}); !apierrors.IsNotFound(err) {
		t.Fatalf("unexpected disruption budget: %v", err)
	}
	actual := &kflaredv1alpha1.CloudflareTunnelPrivateRoute{}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(route), actual); err != nil {
		t.Fatal(err)
	}
	if actual.Status.Resources.Deployment != name || actual.Status.Resources.Secret != name {
		t.Fatalf("connector resource status = %#v", actual.Status.Resources)
	}
	if _, err := r.ensureConnector(ctx, actual, testConnectorToken); err != nil {
		t.Fatalf("repeat reconcile: %v", err)
	}
}
