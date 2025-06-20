/*


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

package v1beta1

import (
	kmapi "github.com/k0sproject/k0smotron/api/k0smotron.io/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const K0smotronControlPlaneFinalizer = "k0smotron.controlplane.cluster.x-k8s.io"

func init() {
	SchemeBuilder.Register(&K0smotronControlPlane{}, &K0smotronControlPlaneList{})
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:metadata:labels="cluster.x-k8s.io/v1beta1=v1beta1"
// +kubebuilder:metadata:labels="cluster.x-k8s.io/provider=control-plane-k0smotron"
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".metadata.labels['cluster\\.x-k8s\\.io/cluster-name']",description="Cluster"
// +kubebuilder:printcolumn:name="API Server Available",type=boolean,JSONPath=".status.ready",description="This denotes that the target API Server is ready to receive requests"
// +kubebuilder:printcolumn:name="Desired",type=integer,JSONPath=".spec.replicas",description="Total number of machines desired by this control plane",priority=10
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=".status.replicas",description="Total number of non-terminated machines targeted by this control plane"
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=".status.readyReplicas",description="Total number of fully running and ready control plane instances"
// +kubebuilder:printcolumn:name="Updated",type=integer,JSONPath=".status.updatedReplicas",description="Total number of non-terminated machines targeted by this control plane that have the desired template spec"
// +kubebuilder:printcolumn:name="Unavailable",type=integer,JSONPath=".status.unavailableReplicas",description="Total number of unavailable control plane instances targeted by this control plane"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Time duration since creation of K0sControlPlane"
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=".spec.version",description="Kubernetes version associated with this control plane"

type K0smotronControlPlane struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              kmapi.ClusterSpec `json:"spec,omitempty"`

	Status K0smotronControlPlaneStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type K0smotronControlPlaneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []K0smotronControlPlane `json:"items"`
}

type K0smotronControlPlaneStatus struct {
	// Ready denotes that the control plane is ready
	// +optional
	Ready bool `json:"ready"`
	// initialized denotes that the K0smotronControlPlane API Server is initialized and thus
	// it can accept requests.
	// NOTE: this field is part of the Cluster API contract and it is used to orchestrate provisioning.
	// The value of this field is never updated after provisioning is completed. Please use conditions
	// to check the operational state of the control plane.
	// +optional
	Initialized bool `json:"initialized"`
	// externalManagedControlPlane is a bool that should be set to true if the Node objects do not exist in the cluster.
	// +optional
	ExternalManagedControlPlane bool `json:"externalManagedControlPlane"`
	// version represents the minimum Kubernetes version for the control plane pods
	// in the cluster.
	// +optional
	Version string `json:"version"`
	// replicas is the total number of pods targeted by this control plane
	// +optional
	Replicas int32 `json:"replicas"`
	// updatedReplicas is the total number of pods targeted by this control plane
	// that have the desired version.
	// +optional
	UpdatedReplicas int32 `json:"updatedReplicas"`
	// readyReplicas is the total number of fully running and ready control plane pods.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas"`
	// unavailableReplicas is the total number of unavailable pods targeted by this control plane.
	// This is the total number of pods with Condition Ready = false.
	// They may either be pods that are running but not yet ready.
	// +optional
	UnavailableReplicas int32 `json:"unavailableReplicas"`
	// K0sVersion represents the full k0s version string including build metadata
	// +optional
	K0sVersion string `json:"k0sVersion,omitempty"`
}
