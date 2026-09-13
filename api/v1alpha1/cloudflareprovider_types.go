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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	ProviderConditionAccepted         = "Accepted"
	ProviderConditionCredentialsValid = "CredentialsValid"
)

// SecretKeyReference identifies one key in a Secret in the controller namespace.
type SecretKeyReference struct {
	// name is the Secret name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// key is the Secret data key containing the API token.
	// +kubebuilder:validation:MinLength=1
	Key string `json:"key"`
}

// CloudflareProviderSpec defines a Cloudflare account and the tenants allowed to use it.
type CloudflareProviderSpec struct {
	// accountID is the Cloudflare account identifier.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="accountID is immutable"
	AccountID string `json:"accountID"`

	// apiTokenSecretRef refers to a Secret in kflared-system.
	APITokenSecretRef SecretKeyReference `json:"apiTokenSecretRef"`

	// allowedDNSZones is the explicit set of DNS suffixes this provider may publish.
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	AllowedDNSZones []string `json:"allowedDNSZones"`

	// bindingNamespaceSelector selects namespaces permitted to reference this provider.
	// An empty selector intentionally permits all namespaces.
	BindingNamespaceSelector metav1.LabelSelector `json:"bindingNamespaceSelector"`
}

// CloudflareProviderStatus defines the observed provider state.
type CloudflareProviderStatus struct {
	// observedGeneration is the most recent generation processed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions report provider validation and credential health.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=cfp
// +kubebuilder:printcolumn:name="Accepted",type=string,JSONPath=".status.conditions[?(@.type=='Accepted')].status"
// +kubebuilder:printcolumn:name="Credentials",type=string,JSONPath=".status.conditions[?(@.type=='CredentialsValid')].status"

// CloudflareProvider is the Schema for the cloudflareproviders API.
type CloudflareProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec CloudflareProviderSpec `json:"spec"`
	// +optional
	Status CloudflareProviderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CloudflareProviderList contains a list of CloudflareProvider.
type CloudflareProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &CloudflareProvider{}, &CloudflareProviderList{})
		return nil
	})
}
