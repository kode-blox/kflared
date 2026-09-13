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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kflaredv1alpha1 "github.com/kode-blox/kflared/api/v1alpha1"
)

func TestProviderReconcileValidatesCredentials(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kflaredv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	provider := readyProvider()
	provider.Finalizers = nil
	provider.Status = kflaredv1alpha1.CloudflareProviderStatus{}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cloudflare-api-token", Namespace: defaultSystemNamespace},
		Data:       map[string][]byte{"api-token": []byte("secret-token")},
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(provider).WithObjects(provider, secret).Build()
	reconciler := &CloudflareProviderReconciler{Client: kubeClient, Scheme: scheme, Cloudflare: fakeCloudflareFactory{client: &fakeCloudflareClient{}}}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: provider.Name}}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("add finalizer: %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("validate provider: %v", err)
	}

	actual := &kflaredv1alpha1.CloudflareProvider{}
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
