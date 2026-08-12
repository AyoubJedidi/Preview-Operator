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
	"k8s.io/apimachinery/pkg/runtime"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// GitOpsConfig defines the ArgoCD GitOps application parameters
type GitOpsConfig struct {
	// TargetRevision is the branch/tag/sha to sync (usually same as Spec.Branch)
	TargetRevision string `json:"targetRevision"`

	// Path is the directory path within the repo containing manifests
	Path string `json:"path"`

	// HelmValues contains key-value pairs passed as custom parameters to Helm
	// +optional
	HelmValues map[string]string `json:"helmValues,omitempty"`
}

// HealthPolicyConfig defines SRE health limits
type HealthPolicyConfig struct {
	// EvaluationWindow is the duration to evaluate (e.g. "3m")
	// +kubebuilder:default="3m"
	// +optional
	EvaluationWindow string `json:"evaluationWindow,omitempty"`

	// MaxErrorRatePercent is the maximum acceptable HTTP 5xx error rate percentage
	// +kubebuilder:default=1.0
	// +optional
	MaxErrorRatePercent float64 `json:"maxErrorRatePercent,omitempty"`

	// MaxP99LatencyMs is the maximum acceptable P99 latency in milliseconds
	// +kubebuilder:default=300
	// +optional
	MaxP99LatencyMs int `json:"maxP99LatencyMs,omitempty"`

	// MaxPodRestarts is the maximum number of container restarts allowed
	// +kubebuilder:default=0
	// +optional
	MaxPodRestarts int `json:"maxPodRestarts,omitempty"`
}

// FinOpsConfig defines hibernation settings
type FinOpsConfig struct {
	// AutoHibernate enables scaling down to 0 replicas if idle
	// +kubebuilder:default=true
	// +optional
	AutoHibernate bool `json:"autoHibernate,omitempty"`

	// IdleDuration is the inactivity time before hibernation (e.g., "2h")
	// +kubebuilder:default="2h"
	// +optional
	IdleDuration string `json:"idleDuration,omitempty"`
}

// SREMetricsSnapshot is the latest metrics checkpoint
type SREMetricsSnapshot struct {
	// CurrentErrorRatePercent is the calculated 5xx error rate
	CurrentErrorRatePercent float64 `json:"currentErrorRatePercent"`

	// CurrentP99LatencyMs is the P99 latency in milliseconds
	CurrentP99LatencyMs float64 `json:"currentP99LatencyMs"`

	// CurrentPodRestarts is the container restart count
	CurrentPodRestarts int `json:"currentPodRestarts"`

	// LastEvaluatedAt is the timestamp of the last evaluation
	// +optional
	LastEvaluatedAt string `json:"lastEvaluatedAt,omitempty"`
}

// HibernationState details scaled-down deployments
type HibernationState struct {
	// IsHibernating indicates if the resources are currently scaled to 0
	IsHibernating bool `json:"isHibernating"`

	// SavedReplicas maps Deployment names to their original replica counts
	// +optional
	SavedReplicas map[string]int32 `json:"savedReplicas,omitempty"`

	// LastActiveAt is the timestamp of the last active request
	// +optional
	LastActiveAt string `json:"lastActiveAt,omitempty"`
}

// PreviewEnvironmentSpec defines the desired state of PreviewEnvironment
type PreviewEnvironmentSpec struct {
	// PRNumber is the GitHub Pull Request number
	// +kubebuilder:validation:Minimum=1
	PRNumber int `json:"prNumber"`

	// RepoOwner is the GitHub repository owner/organization
	RepoOwner string `json:"repoOwner"`

	// RepoName is the GitHub repository name
	RepoName string `json:"repoName"`

	// Branch is the git branch name for the PR
	Branch string `json:"branch"`

	// CommitSha is the git commit SHA to deploy
	CommitSha string `json:"commitSha"`

	// Domain is the base domain for preview URLs (e.g. preview.company.com)
	Domain string `json:"domain"`

	// GitOps is the GitOps configuration for the application
	GitOps GitOpsConfig `json:"gitops"`

	// HealthPolicy defines the SRE health gating thresholds
	// +optional
	HealthPolicy HealthPolicyConfig `json:"healthPolicy,omitempty"`

	// FinOps defines the auto-hibernation configuration
	// +optional
	FinOps FinOpsConfig `json:"finops,omitempty"`
}

// PreviewEnvironmentStatus defines the observed state of PreviewEnvironment.
type PreviewEnvironmentStatus struct {
	// Phase is the current lifecycle phase of the preview environment
	// +kubebuilder:default=Pending
	Phase string `json:"phase,omitempty"`

	// PreviewURL is the external ingress URL for this environment
	// +optional
	PreviewURL string `json:"previewUrl,omitempty"`

	// ArgoAppStatus is the sync status of the associated ArgoCD Application
	// +optional
	ArgoAppStatus string `json:"argoAppStatus,omitempty"`

	// SREMetrics shows the latest Prometheus evaluation snapshot
	// +optional
	SREMetrics SREMetricsSnapshot `json:"sreMetrics,omitempty"`

	// Hibernation tracks the saved deployment scale state
	// +optional
	Hibernation HibernationState `json:"hibernation,omitempty"`

	// conditions represent the current state of the PreviewEnvironment resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="URL",type="string",JSONPath=".status.previewUrl"

// PreviewEnvironment is the Schema for the previewenvironments API
type PreviewEnvironment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PreviewEnvironmentSpec   `json:"spec,omitempty"`
	Status PreviewEnvironmentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PreviewEnvironmentList contains a list of PreviewEnvironment
type PreviewEnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PreviewEnvironment `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &PreviewEnvironment{}, &PreviewEnvironmentList{})
		return nil
	})
}
