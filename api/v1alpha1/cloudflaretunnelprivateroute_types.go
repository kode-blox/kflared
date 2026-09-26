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
	PrivateRouteConditionAccepted       = "Accepted"
	PrivateRouteConditionProgrammed     = "Programmed"
	PrivateRouteConditionConnectorReady = "ConnectorReady"
	PrivateRouteConditionReady          = "Ready"
)

// PrivateRouteServiceReference identifies a non-headless ClusterIP Service.
type PrivateRouteServiceReference struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// namespace defaults to the route namespace.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace,omitempty"`
	// port must exist on the Service. The CIDR route itself cannot restrict ports.
	Port intstr.IntOrString `json:"port"`
}

// CloudflareTunnelPrivateRouteSpec defines one exact Service IPv4 route.
type CloudflareTunnelPrivateRouteSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="controller is immutable"
	Controller string `json:"controller"`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="providerRef is immutable"
	ProviderRef LocalReference               `json:"providerRef"`
	ServiceRef  PrivateRouteServiceReference `json:"serviceRef"`
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=10
	ConnectorReplicas int32 `json:"connectorReplicas,omitempty"`
	// +kubebuilder:default=Delete
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// CloudflareTunnelPrivateRouteStatus records Cloudflare IDs needed for cleanup.
type CloudflareTunnelPrivateRouteStatus struct {
	ObservedGeneration int64                  `json:"observedGeneration,omitempty"`
	TunnelID           string                 `json:"tunnelID,omitempty"`
	TunnelName         string                 `json:"tunnelName,omitempty"`
	RouteID            string                 `json:"routeID,omitempty"`
	Network            string                 `json:"network,omitempty"`
	Resources          ConnectorResourceNames `json:"resources,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=cftpr
// +kubebuilder:printcolumn:name="Network",type=string,JSONPath=".status.network"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"

// CloudflareTunnelPrivateRoute is the Schema for the cloudflaretunnelprivateroutes API.
type CloudflareTunnelPrivateRoute struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CloudflareTunnelPrivateRouteSpec `json:"spec"`
	// +optional
	Status CloudflareTunnelPrivateRouteStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CloudflareTunnelPrivateRouteList contains a list of CloudflareTunnelPrivateRoute.
type CloudflareTunnelPrivateRouteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CloudflareTunnelPrivateRoute `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &CloudflareTunnelPrivateRoute{}, &CloudflareTunnelPrivateRouteList{})
		return nil
	})
}
