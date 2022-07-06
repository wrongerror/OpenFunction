/*
Copyright 2022 The OpenFunction Authors.

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
	k8sgatewayapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

const (
	ClusterDomain              = "cluster.local"
	HostTemplate               = "{{.Name}}.{{.Namespace}}.{{.Domain}}"
	pathTemplate               = "{{.Namespace}}/{{.Name}}"
	HttpRouteLabelKey          = "app.kubernetes.io/managed-by"
	DefaultHttpListenerName    = "ofn-http-internal"
	DefaultListenersCount      = 1
	GatewayListenersAnnotation = "networking.openfunction.io/inject-listeners"
)

const (
	GatewayReasonNotFound           k8sgatewayapiv1alpha2.GatewayConditionReason = "NotFound"
	GatewayReasonExists             k8sgatewayapiv1alpha2.GatewayConditionReason = "GatewayExists"
	GatewayReasonCreationFailure    k8sgatewayapiv1alpha2.GatewayConditionReason = "CreationFailure"
	GatewayReasonResourcesAvailable k8sgatewayapiv1alpha2.GatewayConditionReason = "ResourcesAvailable"
)

var GatewayCompatibilities = map[string]map[string]bool{
	"contour": {
		"allowMultipleGateway": false,
		"implementedAddresses": false,
	},
	"istio": {
		"allowMultipleGateway": true,
		"implementedAddresses": true,
	},
}

type GatewayRef struct {
	// Name is the name of the referent.
	// It refers to the name of a k8s Gateway resource.
	//
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// Namespace is the namespace of the referent.
	// It refers to a k8s namespace.
	//
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace"`
}

type GatewayDef struct {
	// Name is the name of the referent.
	// It refers to the name of a k8s Gateway resource.
	//
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name,omitempty"`
	// Namespace is the namespace of the referent.
	//
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Namespace string `json:"namespace"`
	// GatewayClassName used for this Gateway.
	// This is the name of a GatewayClass resource.
	GatewayClassName k8sgatewayapiv1alpha2.ObjectName `json:"gatewayClassName"`
}

type K8sGatewaySpec struct {
	// Listeners associated with this Gateway. Listeners define
	// logical endpoints that are bound on this Gateway's addresses.
	// At least one Listener MUST be specified.
	//
	// Each listener in a Gateway must have a unique combination of Hostname,
	// Port, and Protocol.
	//
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	Listeners []k8sgatewayapiv1alpha2.Listener `json:"listeners"`
}

// GatewaySpec defines the desired state of Gateway
type GatewaySpec struct {
	// Used to generate the hostname field of gatewaySpec.listeners.openfunction.hostname
	Domain string `json:"domain"`
	// Used to generate the hostname field of gatewaySpec.listeners.openfunction.hostname
	//
	// +kubebuilder:default="cluster.local"
	ClusterDomain string `json:"clusterDomain"`
	// Used to generate the hostname of attaching HTTPRoute
	//
	// +optional
	HostTemplate string `json:"hostTemplate,omitempty"`
	// Used to generate the path of attaching HTTPRoute
	//
	// +optional
	PathTemplate string `json:"pathTemplate,omitempty"`
	// Label key to add to the HTTPRoute generated by function
	// The value will be the `gateway.openfunction.openfunction.io` CR's namespaced name
	//
	// +kubebuilder:default="app.kubernetes.io/managed-by"
	HttpRouteLabelKey string `json:"httpRouteLabelKey,omitempty"`
	// Reference to an existing K8s gateway
	//
	// +optional
	GatewayRef *GatewayRef `json:"gatewayRef,omitempty"`
	// Definition to a new K8s gateway
	//
	// +optional
	GatewayDef *GatewayDef `json:"gatewayDef,omitempty"`
	// GatewaySpec defines the desired state of k8s Gateway.
	GatewaySpec K8sGatewaySpec `json:"gatewaySpec"`
}

// GatewayStatus defines the observed state of Gateway
type GatewayStatus struct {
	// Addresses lists the IP addresses that have actually been
	// bound to the Gateway. These addresses may differ from the
	// addresses in the Spec, e.g. if the Gateway automatically
	// assigns an address from a reserved pool.
	//
	// +optional
	// +kubebuilder:validation:MaxItems=16

	Addresses []k8sgatewayapiv1alpha2.GatewayAddress `json:"addresses,omitempty"`
	// Conditions describe the current conditions of the Gateway.
	//
	// Known condition types are:
	//
	// * "Scheduled"
	// * "Ready"
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:default={{type: "Scheduled", status: "Unknown", reason:"NotReconciled", message:"Waiting for controller", lastTransitionTime: "1970-01-01T00:00:00Z"}}
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Listeners provide status for each unique listener port defined in the Spec.
	//
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	Listeners []k8sgatewayapiv1alpha2.ListenerStatus `json:"listeners,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:storageversion
//+kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=`.status.addresses[*].value`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Gateway is the Schema for the gateways API
type Gateway struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GatewaySpec   `json:"spec,omitempty"`
	Status GatewayStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// GatewayList contains a list of Gateway
type GatewayList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Gateway `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Gateway{}, &GatewayList{})
}
