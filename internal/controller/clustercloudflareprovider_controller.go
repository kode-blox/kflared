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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kflaredv1alpha1 "github.com/kode-blox/kflared/api/v1alpha1"
	cfclient "github.com/kode-blox/kflared/internal/cloudflare"
	"github.com/kode-blox/kflared/internal/planner"
)

const (
	defaultSystemNamespace   = "kflared"
	clusterProviderFinalizer = "kflared.kodeblox.com/cluster-provider-protection"
)

// ClusterCloudflareProviderReconciler reconciles a ClusterCloudflareProvider object.
type ClusterCloudflareProviderReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Cloudflare      cfclient.Factory
	SystemNamespace string
}

// +kubebuilder:rbac:groups=kflared.kodeblox.com,resources=clustercloudflareproviders,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=kflared.kodeblox.com,resources=clustercloudflareproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kflared.kodeblox.com,resources=clustercloudflareproviders/finalizers,verbs=update
// +kubebuilder:rbac:groups=kflared.kodeblox.com,resources=cloudflaretunnelbindings,verbs=get;list;watch

func (r *ClusterCloudflareProviderReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	provider := &kflaredv1alpha1.ClusterCloudflareProvider{}
	if err := r.Get(ctx, req.NamespacedName, provider); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !provider.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, provider)
	}
	if !controllerutil.ContainsFinalizer(provider, clusterProviderFinalizer) {
		controllerutil.AddFinalizer(provider, clusterProviderFinalizer)
		if err := r.Update(ctx, provider); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	statusBase := provider.DeepCopy()
	provider.Status.ObservedGeneration = provider.Generation
	if _, err := planner.NormalizeZones(provider.Spec.AllowedDNSZones); err != nil {
		setCondition(&provider.Status.Conditions, provider.Generation, kflaredv1alpha1.ProviderConditionAccepted, metav1.ConditionFalse, "InvalidDNSZones", err.Error())
		setCondition(&provider.Status.Conditions, provider.Generation, kflaredv1alpha1.ProviderConditionCredentialsValid, metav1.ConditionUnknown, "ValidationFailed", "Credentials were not checked")
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, r.Status().Patch(ctx, provider, client.MergeFrom(statusBase))
	}
	if _, err := metav1.LabelSelectorAsSelector(&provider.Spec.BindingNamespaceSelector); err != nil {
		setCondition(&provider.Status.Conditions, provider.Generation, kflaredv1alpha1.ProviderConditionAccepted, metav1.ConditionFalse, "InvalidNamespaceSelector", err.Error())
		setCondition(&provider.Status.Conditions, provider.Generation, kflaredv1alpha1.ProviderConditionCredentialsValid, metav1.ConditionUnknown, "ValidationFailed", "Credentials were not checked")
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, r.Status().Patch(ctx, provider, client.MergeFrom(statusBase))
	}
	setCondition(&provider.Status.Conditions, provider.Generation, kflaredv1alpha1.ProviderConditionAccepted, metav1.ConditionTrue, "Accepted", "Provider configuration is valid")

	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{Namespace: r.systemNamespace(), Name: provider.Spec.APITokenSecretRef.Name}
	if err := r.Get(ctx, secretKey, secret); err != nil {
		reason := "SecretReadFailed"
		if apierrors.IsNotFound(err) {
			reason = "SecretNotFound"
		}
		setCondition(&provider.Status.Conditions, provider.Generation, kflaredv1alpha1.ProviderConditionCredentialsValid, metav1.ConditionFalse, reason, fmt.Sprintf("Could not read API token Secret %s", secretKey))
		if patchErr := r.Status().Patch(ctx, provider, client.MergeFrom(statusBase)); patchErr != nil {
			return ctrl.Result{}, patchErr
		}
		return ctrl.Result{RequeueAfter: time.Minute}, client.IgnoreNotFound(err)
	}
	token := strings.TrimSpace(string(secret.Data[provider.Spec.APITokenSecretRef.Key]))
	if token == "" {
		setCondition(&provider.Status.Conditions, provider.Generation, kflaredv1alpha1.ProviderConditionCredentialsValid, metav1.ConditionFalse, "TokenMissing", "The configured Secret key is missing or empty")
		return ctrl.Result{RequeueAfter: time.Minute}, r.Status().Patch(ctx, provider, client.MergeFrom(statusBase))
	}
	if validationErr := r.cloudflare().New(token, provider.Spec.AccountID).Validate(ctx); validationErr != nil {
		status := metav1.ConditionUnknown
		reason := "CloudflareAPIUnavailable"
		message := "Cloudflare credentials could not be checked"
		if cfclient.IsCredentialRejected(validationErr) {
			status = metav1.ConditionFalse
			reason = "CloudflareAPIRejected"
			message = "Cloudflare rejected the configured credentials"
		}
		setCondition(&provider.Status.Conditions, provider.Generation, kflaredv1alpha1.ProviderConditionCredentialsValid, status, reason, message)
		if patchErr := r.Status().Patch(ctx, provider, client.MergeFrom(statusBase)); patchErr != nil {
			return ctrl.Result{}, errors.Join(validationErr, patchErr)
		}
		return ctrl.Result{}, validationErr
	}
	setCondition(&provider.Status.Conditions, provider.Generation, kflaredv1alpha1.ProviderConditionCredentialsValid, metav1.ConditionTrue, "CredentialsValid", "Cloudflare accepted the configured credentials")
	return ctrl.Result{RequeueAfter: 10 * time.Minute}, r.Status().Patch(ctx, provider, client.MergeFrom(statusBase))
}

func (r *ClusterCloudflareProviderReconciler) finalize(ctx context.Context, provider *kflaredv1alpha1.ClusterCloudflareProvider) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(provider, clusterProviderFinalizer) {
		return ctrl.Result{}, nil
	}
	bindings := &kflaredv1alpha1.CloudflareTunnelBindingList{}
	if err := r.List(ctx, bindings); err != nil {
		return ctrl.Result{}, err
	}
	for i := range bindings.Items {
		if bindings.Items[i].Spec.ProviderRef.Name == provider.Name {
			return ctrl.Result{}, fmt.Errorf("provider is still referenced by CloudflareTunnelBinding %s/%s", bindings.Items[i].Namespace, bindings.Items[i].Name)
		}
	}
	controllerutil.RemoveFinalizer(provider, clusterProviderFinalizer)
	return ctrl.Result{}, r.Update(ctx, provider)
}

func (r *ClusterCloudflareProviderReconciler) NamespaceAllowed(provider *kflaredv1alpha1.ClusterCloudflareProvider, namespace *corev1.Namespace) (bool, error) {
	selector, err := metav1.LabelSelectorAsSelector(&provider.Spec.BindingNamespaceSelector)
	if err != nil {
		return false, err
	}
	return selector.Matches(labels.Set(namespace.Labels)), nil
}

func (r *ClusterCloudflareProviderReconciler) cloudflare() cfclient.Factory {
	if r.Cloudflare == nil {
		return cfclient.SDKFactory{}
	}
	return r.Cloudflare
}

func (r *ClusterCloudflareProviderReconciler) systemNamespace() string {
	if r.SystemNamespace == "" {
		return defaultSystemNamespace
	}
	return r.SystemNamespace
}

func (r *ClusterCloudflareProviderReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kflaredv1alpha1.ClusterCloudflareProvider{}).
		Named("clustercloudflareprovider").
		Complete(r)
}
