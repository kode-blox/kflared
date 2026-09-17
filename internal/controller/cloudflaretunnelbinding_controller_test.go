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
		WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}, &kflaredv1alpha1.CloudflareProvider{}, &gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}, &appsv1.Deployment{}).
		WithObjects(objects...).Build()
	cloudflare := &fakeCloudflareClient{token: testConnectorToken}
	reconciler := &CloudflareTunnelBindingReconciler{Client: kubeClient, Scheme: scheme, Cloudflare: fakeCloudflareFactory{client: cloudflare}}
	request := ctrl.Request{Namespace: binding.Namespace, Name: binding.Name}

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
	if string(secret.Data["token"]) != testConnectorToken {
		t.Fatal("connector token Secret was not reconciled")
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
		WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}, &kflaredv1alpha1.CloudflareProvider{}, &gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}).
		WithObjects(objects...).Build()
	cloudflare := &fakeCloudflareClient{
		token:         testConnectorToken,
		tunnel:        &cfclient.Tunnel{ID: testTunnelID, Name: planner.TunnelName(types.UID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"), binding), ConfigSource: cfclient.ConfigSourceCloudflare},
		configuration: []cfclient.IngressRule{{Hostname: testApplicationHostname, Service: testOldOrigin}, {Service: testNotFoundOrigin}},
	}
	reconciler := &CloudflareTunnelBindingReconciler{Client: kubeClient, Scheme: scheme, Cloudflare: fakeCloudflareFactory{client: cloudflare}}
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
			provider := bindingTestProvider(t, objects)
			condition := apiMeta.FindStatusCondition(provider.Status.Conditions, tt.conditionType)
			if condition == nil {
				t.Fatalf("provider condition %q is missing", tt.conditionType)
			}
			condition.Status = metav1.ConditionFalse
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).
				WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}, &kflaredv1alpha1.CloudflareProvider{}, &gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}).
				WithObjects(objects...).Build()
			cloudflare := &fakeCloudflareClient{
				tunnel:        &cfclient.Tunnel{ID: binding.Status.TunnelID, Name: binding.Status.TunnelName, ConfigSource: cfclient.ConfigSourceCloudflare},
				configuration: []cfclient.IngressRule{{Hostname: testApplicationHostname, Service: testOldOrigin}, {Service: testNotFoundOrigin}},
			}
			reconciler := &CloudflareTunnelBindingReconciler{Client: kubeClient, Scheme: scheme, Cloudflare: fakeCloudflareFactory{client: cloudflare}}
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
		mutate func(*kflaredv1alpha1.CloudflareProvider)
	}{
		{
			name: "unknown",
			mutate: func(provider *kflaredv1alpha1.CloudflareProvider) {
				apiMeta.FindStatusCondition(provider.Status.Conditions, kflaredv1alpha1.ProviderConditionCredentialsValid).Status = metav1.ConditionUnknown
			},
		},
		{
			name: "stale",
			mutate: func(provider *kflaredv1alpha1.CloudflareProvider) {
				apiMeta.FindStatusCondition(provider.Status.Conditions, kflaredv1alpha1.ProviderConditionCredentialsValid).ObservedGeneration = provider.Generation - 1
			},
		},
		{
			name: "missing",
			mutate: func(provider *kflaredv1alpha1.CloudflareProvider) {
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
			tt.mutate(bindingTestProvider(t, objects))
			expectedStatus := binding.DeepCopy().Status
			initialConfiguration := []cfclient.IngressRule{{Hostname: testApplicationHostname, Service: testOldOrigin}, {Service: testNotFoundOrigin}}
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).
				WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}, &kflaredv1alpha1.CloudflareProvider{}, &gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}).
				WithObjects(objects...).Build()
			cloudflare := &fakeCloudflareClient{
				tunnel:        &cfclient.Tunnel{ID: binding.Status.TunnelID, Name: binding.Status.TunnelName, ConfigSource: cfclient.ConfigSourceCloudflare},
				configuration: slices.Clone(initialConfiguration),
			}
			reconciler := &CloudflareTunnelBindingReconciler{Client: kubeClient, Scheme: scheme, Cloudflare: fakeCloudflareFactory{client: cloudflare}}
			request := ctrl.Request{Namespace: binding.Namespace, Name: binding.Name}

			result, err := reconciler.Reconcile(context.Background(), request)
			if !errors.Is(err, errProviderStatusUnknown) {
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

			actual := assertOperationalFailureStatus(t, kubeClient, request.NamespacedName, expectedStatus, kflaredv1alpha1.BindingConditionReady, "ProviderStatusUnknown", "The referenced CloudflareProvider status is not currently known")
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
				WithStatusSubresource(&kflaredv1alpha1.CloudflareTunnelBinding{}, &kflaredv1alpha1.CloudflareProvider{}, &gatewayv1.Gateway{}, &gatewayv1.HTTPRoute{}, &appsv1.Deployment{}).
				WithObjects(objects...).Build()
			var kubeClient client.Client = baseClient
			if tt.objectFailure != nil {
				kubeClient = &failingGetClient{Client: baseClient, objectType: reflect.TypeOf(tt.objectFailure), key: tt.objectKey, err: failure}
			}
			reconciler := &CloudflareTunnelBindingReconciler{
				Client:     kubeClient,
				Scheme:     scheme,
				RESTMapper: bindingTestRESTMapper(),
				Cloudflare: fakeCloudflareFactory{client: cloudflare},
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
	reconciler := &CloudflareTunnelBindingReconciler{Client: kubeClient, Scheme: scheme}
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
				Client:     kubeClient,
				Scheme:     scheme,
				RESTMapper: bindingTestRESTMapper(),
				Cloudflare: fakeCloudflareFactory{client: cloudflare},
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
		Client:     kubeClient,
		Scheme:     scheme,
		RESTMapper: bindingTestRESTMapper(),
		Cloudflare: fakeCloudflareFactory{client: cloudflare},
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

func bindingTestProvider(t *testing.T, objects []client.Object) *kflaredv1alpha1.CloudflareProvider {
	t.Helper()
	for _, object := range objects {
		if provider, ok := object.(*kflaredv1alpha1.CloudflareProvider); ok {
			return provider
		}
	}
	t.Fatal("test objects do not contain a CloudflareProvider")
	return nil
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
	return append(objects,
		&appsv1.Deployment{Name: resourceName, Namespace: defaultSystemNamespace},
		&policyv1.PodDisruptionBudget{Name: resourceName, Namespace: defaultSystemNamespace},
		&corev1.Secret{Name: resourceName, Namespace: defaultSystemNamespace},
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
			ProviderRef:       kflaredv1alpha1.LocalReference{Name: testDefaultName},
			GatewayRef:        kflaredv1alpha1.GatewayReference{Name: testGatewayName, SectionName: testHTTPSectionName},
			GatewayServiceRef: kflaredv1alpha1.GatewayServiceReference{Name: testGatewayName, Port: intstr.FromString("web")},
			ConnectorReplicas: 2,
			DeletionPolicy:    kflaredv1alpha1.DeletionPolicyDelete,
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
		readyProvider(),
		binding,
		&corev1.Namespace{Name: testTenantName, Labels: map[string]string{testTenantName: "allowed"}},
		&corev1.Namespace{Name: metav1.NamespaceSystem, UID: types.UID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")},
		&corev1.Secret{Name: testAPITokenSecretName, Namespace: defaultSystemNamespace, Data: map[string][]byte{testAPITokenSecretKey: []byte(testAPITokenSecretKey)}},
		&gatewayv1.GatewayClass{Name: testGatewayName, Spec: gatewayv1.GatewayClassSpec{ControllerName: gatewayv1.GatewayController(traefikControllerName)}},
		gateway,
		&corev1.Service{Name: testGatewayName, Namespace: testTenantName, Spec: corev1.ServiceSpec{ClusterIP: "10.0.0.10", Ports: []corev1.ServicePort{{Name: "web", Port: 80}}}},
		route,
	}, binding
}
