/*
Copyright 2026.

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
)

// AgentRuntimeSpec defines the desired state of AgentRuntime.
type AgentRuntimeSpec struct {
	// +kubebuilder:validation:Enum=agent;tool
	// +kubebuilder:default=agent
	Type string `json:"type"`

	// +required
	TargetRef TargetRef `json:"targetRef"`

	// +optional
	Identity *IdentityConfig `json:"identity,omitempty"`

	// +optional
	Trace *TraceConfig `json:"trace,omitempty"`

	// +optional
	Policy *PolicyConfig `json:"policy,omitempty"`
}

// TargetRef identifies the workload this AgentRuntime manages.
type TargetRef struct {
	// +kubebuilder:default="apps/v1"
	APIVersion string `json:"apiVersion"`

	// +kubebuilder:validation:Enum=Deployment;StatefulSet;Sandbox
	Kind string `json:"kind"`

	// +required
	Name string `json:"name"`
}

// IdentityConfig configures workload identity.
type IdentityConfig struct {
	// +optional
	SPIFFE *SPIFFEConfig `json:"spiffe,omitempty"`
}

// SPIFFEConfig configures SPIFFE-based identity.
type SPIFFEConfig struct {
	// +required
	TrustDomain string `json:"trustDomain"`
}

// TraceConfig configures OpenTelemetry tracing.
type TraceConfig struct {
	// +required
	Endpoint string `json:"endpoint"`

	// +kubebuilder:validation:Enum=grpc;http
	// +kubebuilder:default=grpc
	// +optional
	Protocol string `json:"protocol,omitempty"`

	// +optional
	SamplingRate *int32 `json:"samplingRate,omitempty"`
}

// PolicyConfig defines network access control and authorization policy.
type PolicyConfig struct {
	// +optional
	AllowedIngressNamespaces []string `json:"allowedIngressNamespaces,omitempty"`

	// +optional
	Dependencies []Dependency `json:"dependencies,omitempty"`

	// +optional
	ExternalEgress []ExternalEgressRule `json:"externalEgress,omitempty"`
}

// Dependency declares a target this agent is allowed to call.
type Dependency struct {
	// +required
	Name string `json:"name"`

	// +optional
	Namespace string `json:"namespace,omitempty"`

	// +optional
	AllowedTools []string `json:"allowedTools,omitempty"`

	// +optional
	AllowedSkills []string `json:"allowedSkills,omitempty"`
}

// ExternalEgressRule allows egress to an external endpoint.
type ExternalEgressRule struct {
	// +optional
	Host string `json:"host,omitempty"`

	// +optional
	CIDR string `json:"cidr,omitempty"`

	// +required
	Port int32 `json:"port"`
}

// AgentRuntimeStatus defines the observed state of AgentRuntime.
type AgentRuntimeStatus struct {
	// +kubebuilder:validation:Enum=Pending;Active;Error
	// +kubebuilder:default=Pending
	// +optional
	Phase string `json:"phase,omitempty"`

	// +optional
	ConfiguredPods int32 `json:"configuredPods,omitempty"`

	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	Gateway *GatewayStatus `json:"gateway,omitempty"`

	// +optional
	Identity *IdentityStatus `json:"identity,omitempty"`

	// +optional
	Card *CardStatus `json:"card,omitempty"`

	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// GatewayStatus reports the state of generated gateway resources.
type GatewayStatus struct {
	// +optional
	HTTPRouteName string `json:"httpRouteName,omitempty"`

	// +optional
	AuthPolicyName string `json:"authPolicyName,omitempty"`

	// +optional
	NetworkPolicyName string `json:"networkPolicyName,omitempty"`

	// +optional
	GatewayEndpoint string `json:"gatewayEndpoint,omitempty"`
}

// IdentityStatus reports the agent's identity state.
type IdentityStatus struct {
	// +optional
	SpiffeID string `json:"spiffeId,omitempty"`

	// +optional
	MTLSEnabled bool `json:"mtlsEnabled,omitempty"`

	// +optional
	CertificateSource string `json:"certificateSource,omitempty"`
}

// CardStatus caches the agent's A2A card fetched on rollout.
type CardStatus struct {
	// +optional
	Name string `json:"name,omitempty"`

	// +optional
	Version string `json:"version,omitempty"`

	// +optional
	Skills []string `json:"skills,omitempty"`

	// +optional
	FetchedAt *metav1.Time `json:"fetchedAt,omitempty"`

	// +optional
	FetchedOverMTLS bool `json:"fetchedOverMTLS,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.gateway.gatewayEndpoint`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AgentRuntime is the Schema for the agentruntimes API.
type AgentRuntime struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec AgentRuntimeSpec `json:"spec"`

	// +optional
	Status AgentRuntimeStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// AgentRuntimeList contains a list of AgentRuntime.
type AgentRuntimeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []AgentRuntime `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentRuntime{}, &AgentRuntimeList{})
}
