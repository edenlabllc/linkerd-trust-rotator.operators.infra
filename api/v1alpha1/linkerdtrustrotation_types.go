/*
Copyright 2025 Edenlab.

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

// RotationTrigger defines the conditions that initiate a trust rotation.
// Rotation can be triggered when the trust-roots ConfigMap changes and/or
// when the current and previous trust anchor secrets diverge. Both conditions
// can be combined to ensure that rotation happens only when a real change
// in trust anchors has been detected.
type RotationTrigger struct {
	// Start rotation when ConfigMap with trust roots is changed
	OnTrustRootsConfigMapChange bool `json:"onTrustRootsConfigMapChange"`

	// OnTrustAnchorSecretsDiff, if true, triggers rotation only when the current
	// trust-anchor Secret(s) differ from the previously observed state (current != previous)ю
	OnTrustAnchorSecretsDiff bool `json:"onTrustAnchorSecretsDiff"`
}

// RolloutSpec defines how workloads should be restarted during trust rotation.
// It controls the order of restart (control-plane first or data-plane first),
// the restart method (native rollout restart or annotation bump),
// an optional whitelist of namespaces, and a target selector that defines
// which workloads (by pod-template annotation and kind) are subject to restart.
type RolloutSpec struct {
	// Workload selection by pod-template annotation and per-kind scoping.
	TargetAnnotationSelector TargetAnnotationSelector `json:"targetAnnotationSelector"`
}

// ProtectionSpec defines validation and guard settings for the rotation process.
// It controls when rollouts can start, what checks are performed during rollout,
// the readiness threshold required for data-plane convergence, how long to wait
// before cleaning up the previous trust anchor, and how many failures are tolerated
// before the rotation is aborted. These fields ensure the cluster remains healthy
// and Linkerd stays operational throughout the trust rotation workflow.
type ProtectionSpec struct {
	// Run `linkerd check --proxy` during rollout
	RunLinkerdCheckProxy bool `json:"runLinkerdCheckProxy"`

	// +optional
	LinkerdCheckProxyImage string `json:"linkerdCheckProxyImage,omitempty"`

	// Delay before starting rollouts after detecting change (e.g. "30s")
	// +optional
	BeforeRolloutDelay *metav1.Duration `json:"beforeRolloutDelay,omitempty"`

	// RetriggerRolloutAfterCleanup runs an additional restart after trust cleanup,
	// ensuring proxies reload only the new trust anchor.
	// +optional
	RetriggerRolloutAfterCleanup bool `json:"retriggerRolloutAfterCleanup,omitempty"`

	// Hold time after reaching readiness threshold after cleanup previous trust secret (e.g. "5m").
	// Relevant only if retriggerRollout is enabled.
	// +optional
	HoldAfterCleanup *metav1.Duration `json:"holdAfterCleanup,omitempty"`

	// Maximum number of allowed failures before aborting rotation
	MaxRolloutFailures int `json:"maxRolloutFailures"`
}

// TargetAnnotationSelector defines how to select workloads that should be restarted.
// Only pod-template annotations are supported (Linkerd-specific).
type TargetAnnotationSelector struct {
	// Annotation key to match (e.g., "linkerd.io/inject")
	Key string `json:"key"`

	// Expected value (e.g., "enabled")
	Value string `json:"value"`

	// Per-kind map: key is Kind (e.g., "Deployment", "StatefulSet", "DaemonSet", or custom kinds).
	Targets []TargetScope `json:"targets"`
}

// TargetScope defines scope for a particular Kind.
type TargetScope struct {
	// Type of Kubernetes resources (e.g. "Deployment", "StatefulSet", "DaemonSet", "CustomResource")
	// +kubebuilder:validation:Enum=Deployment;StatefulSet;DaemonSet;CustomResource
	KindType string `json:"kindType"`

	// Whitelist of namespaces for this Kind.
	AllowedNamespaces []string `json:"allowedNamespaces"`

	// Rollout strategy (e.g. "rolloutRestart", "rolloutDelete")
	// +kubebuilder:validation:Enum=rolloutRestart;rolloutDelete
	// +optional
	RolloutStrategy string `json:"rolloutStrategy,omitempty"`

	// Optional G/V for custom kinds. Built-ins default to apps/v1.
	// +optional
	APIGroup string `json:"apiGroup,omitempty"`

	// +optional
	Kind string `json:"kind,omitempty"`

	// +optional
	Version string `json:"version,omitempty"`

	// Options for the rolloutRestart.
	// +optional
	AnnotationBump *AnnotationBumpOptions `json:"annotationBump,omitempty"`
}

// AnnotationBumpOptions customizes how the annotation bump is applied.
// Ignored unless method=annotationBump.
type AnnotationBumpOptions struct {
	// Annotation key to bump (default: "operators.infra/rotation")
	// +optional
	BumpAnnotationKey string `json:"key,omitempty"`

	// Annotation value to bump (default: "")
	// +optional
	BumpAnnotationValue string `json:"value,omitempty"`
}

type LinkerdSpec struct {
	// Namespace where Linkerd control-plane is installed
	Namespace string `json:"namespace"`

	// Names of ConfigMap and Secrets managed by the operator
	TrustRootsConfigMap       string `json:"trustRootsConfigMap"`
	TrustAnchorSecret         string `json:"trustAnchorSecret"`
	PreviousTrustAnchorSecret string `json:"previousTrustAnchorSecret"`

	// Whether the operator should create the previous trust secret
	// during the first bootstrap if it does not exist.
	// If false, the operator assumes it is already provisioned.
	BootstrapPreviousSecret bool `json:"bootstrapPreviousSecret"`
}

// LinkerdTrustRotationSpec defines the desired state of LinkerdTrustRotation
type LinkerdTrustRotationSpec struct {
	// Linkerd settings
	Linkerd LinkerdSpec `json:"linkerd"`

	// Trigger settings
	Trigger RotationTrigger `json:"trigger"`

	// Rollout settings
	Rollout RolloutSpec `json:"rollout"`

	// Protection and validation settings
	Protection ProtectionSpec `json:"protection"`

	// Dry-run mode
	// +optional
	DryRun bool `json:"dryRun,omitempty"`
}

// ProgressStatus Status
type ProgressStatus struct {
	// Whether control-plane workloads are rolled out and ready
	ControlPlaneReady bool `json:"controlPlaneReady"`

	// Percentage of data-plane workloads updated and ready
	DataPlanePercent int `json:"dataPlanePercent"`
}

// TrustStatus Status
type TrustStatus struct {
	// Bundle state: single | overlap
	BundleState *BundleState `json:"bundleState"`

	// Current trust anchor fingerprint (short SHA256)
	// +optional
	CurrentFP string `json:"currentFP,omitempty"`

	// Previous trust anchor fingerprint (short SHA256)
	// +optional
	PreviousFP string `json:"previousFP,omitempty"`
}

// WorkRef is a stable reference to a workload in the plan.
type WorkRef struct {
	Kind string `json:"kind"` // "Deployment" | "StatefulSet" | ...

	Namespace string `json:"namespace"`

	Name string `json:"name"`
}

// RolloutCursor tracks progress through the ordered Queue.
type RolloutCursor struct {
	// Hash of the current plan (Queue) to detect spec/selection changes.
	// +optional
	PlanHash string `json:"planHash,omitempty"`

	// Index of the next item to process (0..Total). Increments on success.
	Next int `json:"next"`

	// Total number of items in the plan.
	Total int `json:"total"`

	// Last successfully processed item (for logs/diagnostics).
	// +optional
	LastDone *WorkRef `json:"lastDone,omitempty"`
}

// RetryStatus Status
type RetryStatus struct {
	// Number of performed retries
	Count int `json:"count"`

	// Last error message if any
	// +optional
	LastError string `json:"lastError,omitempty"`

	// Reference to the work item that caused the last failure
	// +optional
	LastFailed *WorkRef `json:"lastErrorObject,omitempty"`

	// Timestamp of the last error
	// +optional
	LastErrorTime *metav1.Time `json:"lastErrorTime,omitempty"`
}

// LinkerdTrustRotationStatus defines the observed state of LinkerdTrustRotation.
type LinkerdTrustRotationStatus struct {
	// Current phase of the rotation process
	// +optional
	Phase *Phase `json:"phase,omitempty"`

	// Reason for the current phase (short machine-readable string)
	// +optional
	Reason *Reason `json:"reason,omitempty"`

	// Human-readable message with details
	// +optional
	Message *string `json:"message,omitempty"`

	// Timestamp when rotation started
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// Timestamp of the last update
	// +optional
	LastUpdated *metav1.Time `json:"lastUpdated,omitempty"`

	// Timestamp of completion (if succeeded or failed)
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Progress information
	// +optional
	Progress *ProgressStatus `json:"progress,omitempty"`

	// Trust anchor information
	// +optional
	Trust *TrustStatus `json:"trust,omitempty"`

	// Number of retries and last error
	// +optional
	Retries *RetryStatus `json:"retries,omitempty"`

	// Cursor tracks rollout position for resume on failure.
	// +optional
	Cursor *RolloutCursor `json:"cursor,omitempty"`

	// DryRunPlan is a human-readable summary of the last dry-run (no changes applied).
	// +optional
	DryRunPlan string `json:"dryRunPlan,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="ControlPlaneReady",type=boolean,JSONPath=`.status.progress.controlPlaneReady`
// +kubebuilder:printcolumn:name="DataPlaneProgress%",type=integer,JSONPath=`.status.progress.dataPlanePercent`
// +kubebuilder:printcolumn:name="BundleState",type=string,JSONPath=`.status.trust.bundleState`
// +kubebuilder:printcolumn:name="LastUpdated",type=date,JSONPath=`.status.lastUpdated`

// LinkerdTrustRotation is the Schema for the linkerdtrustrotations API
type LinkerdTrustRotation struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of LinkerdTrustRotation
	// +required
	Spec LinkerdTrustRotationSpec `json:"spec"`

	// status defines the observed state of LinkerdTrustRotation
	// +optional
	Status LinkerdTrustRotationStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// LinkerdTrustRotationList contains a list of LinkerdTrustRotation
type LinkerdTrustRotationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LinkerdTrustRotation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LinkerdTrustRotation{}, &LinkerdTrustRotationList{})
}
