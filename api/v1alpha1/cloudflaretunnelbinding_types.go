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
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	BindingConditionAccepted           = "Accepted"
	BindingConditionProgrammed         = "Programmed"
	BindingConditionConnectorReady     = "ConnectorReady"
	BindingConditionDNSAutomationReady = "DNSAutomationReady"
	BindingConditionReady              = "Ready"

	DeletionPolicyDelete DeletionPolicy = "Delete"
	DeletionPolicyRetain DeletionPolicy = "Retain"
)

// DeletionPolicy controls whether the remote Cloudflare Tunnel is removed with the binding.
// +kubebuilder:validation:Enum=Delete;Retain
type DeletionPolicy string

// LocalReference identifies a resource in the binding namespace.
type LocalReference struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// GatewayReference selects one listener on a Gateway in the binding namespace.
type GatewayReference struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	SectionName string `json:"sectionName"`
}

// OriginServiceReference identifies the internal Traefik Service and port.
type OriginServiceReference struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// namespace defaults to the CloudflareTunnelBinding namespace when omitted.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string             `json:"namespace,omitempty"`
	Port      intstr.IntOrString `json:"port"`
}

// CloudflareTunnelBindingSpec defines the desired tunnel integration.
type CloudflareTunnelBindingSpec struct {
	// controller identifies the KFlared controller class that owns this binding.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="controller is immutable"
	Controller string `json:"controller"`

	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="providerRef is immutable"
	ProviderRef LocalReference `json:"providerRef"`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="gatewayRef is immutable"
	GatewayRef       GatewayReference       `json:"gatewayRef"`
	OriginServiceRef OriginServiceReference `json:"originServiceRef"`

	// connectorReplicas controls the number of official cloudflared connectors.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=2
	// +kubebuilder:validation:Maximum=10
	ConnectorReplicas int32 `json:"connectorReplicas,omitempty"`

	// deletionPolicy controls remote tunnel deletion. Kubernetes connector resources
	// and managed DNSEndpoints are always removed.
	// +kubebuilder:default=Delete
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// DNSRecord describes an exact DNS record required for a published hostname.
type DNSRecord struct {
	Hostname string `json:"hostname"`
	// +kubebuilder:validation:Enum=CNAME
	Type   string `json:"type"`
	Target string `json:"target"`
}

// ConnectorResourceNames records the generated resources without exposing credentials.
type ConnectorResourceNames struct {
	Deployment          string `json:"deployment,omitempty"`
	PodDisruptionBudget string `json:"podDisruptionBudget,omitempty"`
	Secret              string `json:"secret,omitempty"`
	DNSEndpoint         string `json:"dnsEndpoint,omitempty"`
}

// CloudflareTunnelBindingStatus defines the observed binding state.
type CloudflareTunnelBindingStatus struct {
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	TunnelID           string `json:"tunnelID,omitempty"`
	TunnelName         string `json:"tunnelName,omitempty"`
	TunnelCNAME        string `json:"tunnelCNAME,omitempty"`
	// +listType=set
	PublishedHostnames []string `json:"publishedHostnames,omitempty"`
	// +listType=map
	// +listMapKey=hostname
	DNSRecords []DNSRecord            `json:"dnsRecords,omitempty"`
	Resources  ConnectorResourceNames `json:"resources,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=cftb
// +kubebuilder:printcolumn:name="Gateway",type=string,JSONPath=".spec.gatewayRef.name"
// +kubebuilder:printcolumn:name="Tunnel",type=string,JSONPath=".status.tunnelID"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"

// CloudflareTunnelBinding is the Schema for the cloudflaretunnelbindings API.
type CloudflareTunnelBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec CloudflareTunnelBindingSpec `json:"spec"`
	// +optional
	Status CloudflareTunnelBindingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CloudflareTunnelBindingList contains a list of CloudflareTunnelBinding.
type CloudflareTunnelBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareTunnelBinding `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &CloudflareTunnelBinding{}, &CloudflareTunnelBindingList{})
		return nil
	})
}
