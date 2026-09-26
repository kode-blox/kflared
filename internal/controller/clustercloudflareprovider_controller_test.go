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
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kflaredv1alpha1 "github.com/kode-blox/kflared/api/v1alpha1"
	cfclient "github.com/kode-blox/kflared/internal/cloudflare"
)

func TestProviderReconcileValidatesCredentials(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kflaredv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	provider := readyClusterProvider()
	provider.Finalizers = nil
	provider.Status = kflaredv1alpha1.ClusterCloudflareProviderStatus{}
	secret := &corev1.Secret{
		Name: testAPITokenSecretName, Namespace: defaultSystemNamespace,
		Data: map[string][]byte{testAPITokenSecretKey: []byte("secret-token")},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(provider).WithObjects(provider, secret).Build()
	reconciler := &ClusterCloudflareProviderReconciler{ControllerClass: testControllerClass, Client: kubeClient, Scheme: scheme, Cloudflare: fakeCloudflareFactory{client: &fakeCloudflareClient{}}}
	request := ctrl.Request{Name: provider.Name}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("add finalizer: %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("validate provider: %v", err)
	}

	actual := &kflaredv1alpha1.ClusterCloudflareProvider{}
	if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
		t.Fatal(err)
	}
	if !conditionTrue(actual.Status.Conditions, actual.Generation, kflaredv1alpha1.ProviderConditionAccepted) {
		t.Fatalf("provider Accepted condition is not true: %#v", actual.Status.Conditions)
	}
	if !conditionTrue(actual.Status.Conditions, actual.Generation, kflaredv1alpha1.ProviderConditionCredentialsValid) {
		t.Fatalf("provider CredentialsValid condition is not true: %#v", actual.Status.Conditions)
	}
}

func TestProviderReconcileIgnoresOtherControllerClassBeforeDeletionEffects(t *testing.T) {
	scheme := providerTestScheme(t)
	provider := readyClusterProvider()
	provider.Spec.Controller = testOtherControllerClass
	provider.Finalizers = []string{clusterProviderFinalizer}
	provider.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(provider).WithObjects(provider).Build()
	cloudflare := &fakeCloudflareClient{}
	reconciler := &ClusterCloudflareProviderReconciler{ControllerClass: testControllerClass, Client: kubeClient, Scheme: scheme, Cloudflare: fakeCloudflareFactory{client: cloudflare}}

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{Name: provider.Name}); err != nil {
		t.Fatalf("ignore foreign provider: %v", err)
	}
	actual := &kflaredv1alpha1.ClusterCloudflareProvider{}
	if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(provider), actual); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(actual.Finalizers, []string{clusterProviderFinalizer}) || len(actual.Status.Conditions) != len(provider.Status.Conditions) {
		t.Fatalf("foreign provider mutated: finalizers=%v status=%#v", actual.Finalizers, actual.Status)
	}
	if cloudflare.validateCalls != 0 {
		t.Fatalf("foreign provider caused %d Cloudflare validations", cloudflare.validateCalls)
	}
}

func TestProviderFinalizationIgnoresBindingsFromOtherControllerClasses(t *testing.T) {
	scheme := providerTestScheme(t)
	provider := readyClusterProvider()
	provider.Finalizers = []string{clusterProviderFinalizer}
	provider.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	foreignBinding := &kflaredv1alpha1.CloudflareTunnelBinding{}
	foreignBinding.Name = "foreign"
	foreignBinding.Namespace = testTenantName
	foreignBinding.Spec = kflaredv1alpha1.CloudflareTunnelBindingSpec{
		Controller:  testOtherControllerClass,
		ProviderRef: kflaredv1alpha1.LocalReference{Name: provider.Name},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(provider).
		WithObjects(provider, foreignBinding).Build()
	reconciler := &ClusterCloudflareProviderReconciler{ControllerClass: testControllerClass, Client: kubeClient, Scheme: scheme}

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{Name: provider.Name}); err != nil {
		t.Fatalf("finalize provider despite foreign binding: %v", err)
	}
	actual := &kflaredv1alpha1.ClusterCloudflareProvider{}
	if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(provider), actual); !apierrors.IsNotFound(err) {
		t.Fatalf("provider remains after class-scoped finalization: err=%v object=%#v", err, actual)
	}
}

func TestProviderFinalizationWaitsForPrivateRoute(t *testing.T) {
	scheme := providerTestScheme(t)
	provider := readyClusterProvider()
	provider.Finalizers = []string{clusterProviderFinalizer}
	provider.DeletionTimestamp = &metav1.Time{Time: time.Now()}
	privateRoute := &kflaredv1alpha1.CloudflareTunnelPrivateRoute{}
	privateRoute.Name = "api"
	privateRoute.Namespace = testTenantName
	privateRoute.Spec.Controller = testControllerClass
	privateRoute.Spec.ProviderRef.Name = provider.Name
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(provider, privateRoute).Build()
	reconciler := &ClusterCloudflareProviderReconciler{ControllerClass: testControllerClass, Client: kubeClient, Scheme: scheme}

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{Name: provider.Name}); err == nil || !strings.Contains(err.Error(), "CloudflareTunnelPrivateRoute") {
		t.Fatalf("expected private route to block provider deletion, got %v", err)
	}
	actual := &kflaredv1alpha1.ClusterCloudflareProvider{}
	if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(provider), actual); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(actual.Finalizers, clusterProviderFinalizer) {
		t.Fatal("provider finalizer removed while private route exists")
	}
}

func TestProviderReconcileClassifiesCredentialValidationFailures(t *testing.T) {
	tests := []struct {
		name       string
		validation func(error) error
		wantStatus metav1.ConditionStatus
		wantReason string
	}{
		{
			name: "confirmed rejection",
			validation: func(cause error) error {
				return errors.Join(cfclient.ErrCredentialsRejected, cause)
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: "CloudflareAPIRejected",
		},
		{
			name:       "transient API failure",
			validation: func(cause error) error { return cause },
			wantStatus: metav1.ConditionUnknown,
			wantReason: "CloudflareAPIUnavailable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := providerTestScheme(t)
			provider := readyClusterProvider()
			provider.Finalizers = []string{clusterProviderFinalizer}
			secret := providerTestSecret()
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(provider).WithObjects(provider, secret).Build()
			validationCause := errors.New("sensitive validation response body")
			validationErr := tt.validation(validationCause)
			reconciler := &ClusterCloudflareProviderReconciler{
				ControllerClass: testControllerClass,
				Client:          kubeClient,
				Scheme:          scheme,
				Cloudflare:      fakeCloudflareFactory{client: &fakeCloudflareClient{validateErr: validationErr}},
			}
			request := ctrl.Request{Name: provider.Name}

			if _, err := reconciler.Reconcile(context.Background(), request); !errors.Is(err, validationCause) {
				t.Fatalf("Reconcile() error = %v, want preserved validation error", err)
			}

			actual := &kflaredv1alpha1.ClusterCloudflareProvider{}
			if err := kubeClient.Get(context.Background(), request.NamespacedName, actual); err != nil {
				t.Fatal(err)
			}
			condition := apiMeta.FindStatusCondition(actual.Status.Conditions, kflaredv1alpha1.ProviderConditionCredentialsValid)
			if condition == nil {
				t.Fatal("CredentialsValid condition is missing")
			}
			if condition.Status != tt.wantStatus || condition.Reason != tt.wantReason {
				t.Errorf("CredentialsValid = %s/%s, want %s/%s", condition.Status, condition.Reason, tt.wantStatus, tt.wantReason)
			}
			if strings.Contains(condition.Message, "sensitive") {
				t.Errorf("CredentialsValid message leaked validation details: %q", condition.Message)
			}
		})
	}
}

func TestProviderReconcilePreservesValidationAndStatusPatchErrors(t *testing.T) {
	scheme := providerTestScheme(t)
	provider := readyClusterProvider()
	provider.Finalizers = []string{clusterProviderFinalizer}
	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(provider).WithObjects(provider, providerTestSecret()).Build()
	validationErr := errors.New("validation failed")
	patchErr := errors.New("status patch failed")
	reconciler := &ClusterCloudflareProviderReconciler{
		ControllerClass: testControllerClass,
		Client:          statusPatchFailingClient{Client: baseClient, err: patchErr},
		Scheme:          scheme,
		Cloudflare:      fakeCloudflareFactory{client: &fakeCloudflareClient{validateErr: validationErr}},
	}

	_, err := reconciler.Reconcile(context.Background(), ctrl.Request{Name: provider.Name})
	if !errors.Is(err, validationErr) || !errors.Is(err, patchErr) {
		t.Fatalf("Reconcile() error = %v, want both validation and patch errors", err)
	}
}

func providerTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kflaredv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func providerTestSecret() *corev1.Secret {
	return &corev1.Secret{
		Name: testAPITokenSecretName, Namespace: defaultSystemNamespace,
		Data: map[string][]byte{testAPITokenSecretKey: []byte("secret-token")},
	}
}

type statusPatchFailingClient struct {
	client.Client
	err error
}

func (c statusPatchFailingClient) Status() client.SubResourceWriter {
	return statusPatchFailingWriter{SubResourceWriter: c.Client.Status(), err: c.err}
}

type statusPatchFailingWriter struct {
	client.SubResourceWriter
	err error
}

func (w statusPatchFailingWriter) Patch(context.Context, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
	return w.err
}
