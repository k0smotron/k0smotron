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
	bootstrapv1 "github.com/k0sproject/k0smotron/api/bootstrap/v1beta1"
	"github.com/k0sproject/k0smotron/internal/cloudinit"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

func init() {
	SchemeBuilder.Register(&K0sControlPlane{}, &K0sControlPlaneList{})
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:labels="cluster.x-k8s.io/v1beta1=v1beta1"
// +kubebuilder:metadata:labels="cluster.x-k8s.io/provider=control-plane-k0smotron"

type K0sControlPlane struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   K0sControlPlaneSpec   `json:"spec,omitempty"`
	Status K0sControlPlaneStatus `json:"status,omitempty"`
}

type K0sControlPlaneSpec struct {
	K0sConfigSpec   bootstrapv1.K0sConfigSpec       `json:"k0sConfigSpec"`
	MachineTemplate *K0sControlPlaneMachineTemplate `json:"machineTemplate"`
	//+kubebuilder:validation:Optional
	//+kubebuilder:default=1
	Replicas int32 `json:"replicas,omitempty"`
	// Version defines the k0s version to be deployed. You can use a specific k0s version (e.g. v1.27.1+k0s.0) or
	// just the Kubernetes version (e.g. v1.27.1). If left empty, k0smotron will select one automatically.
	//+kubebuilder:validation:Optional
	Version string `json:"version,omitempty"`
}

type K0sBootstrapConfigSpec struct {
	// Files specifies extra files to be passed to user_data upon creation.
	// +kubebuilder:validation:Optional
	Files []cloudinit.File `json:"files,omitempty"`

	// Args specifies extra arguments to be passed to k0s worker.
	// See: https://docs.k0sproject.io/stable/advanced/worker-configuration/
	Args []string `json:"args,omitempty"`

	// PreStartCommands specifies commands to be run before starting k0s worker.
	// +kubebuilder:validation:Optional
	PreStartCommands []string `json:"preStartCommands,omitempty"`

	// PostStartCommands specifies commands to be run after starting k0s worker.
	// +kubebuilder:validation:Optional
	PostStartCommands []string `json:"postStartCommands,omitempty"`

	// PreInstallK0s specifies whether k0s binary is pre-installed on the node.
	// +kubebuilder:validation:Optional
	PreInstalledK0s bool `json:"preInstalledK0s,omitempty"`

	// DownloadURL specifies the URL from which to download the k0s binary.
	// If the version field is specified, it is ignored, and whatever version is downloaded from the URL is used.
	// +kubebuilder:validation:Optional
	DownloadURL string `json:"downloadURL,omitempty"`
}

type K0sControlPlaneMachineTemplate struct {
	// Standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	// +optional
	ObjectMeta clusterv1.ObjectMeta `json:"metadata,omitempty"`

	// InfrastructureRef is a required reference to a custom resource
	// offered by an infrastructure provider.
	InfrastructureRef corev1.ObjectReference `json:"infrastructureRef"`
}

// +kubebuilder:object:root=true

type K0sControlPlaneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []K0sControlPlane `json:"items"`
}

type K0sControlPlaneStatus struct {
	// Ready denotes that the control plane is ready
	Ready                       bool   `json:"ready"`
	ControlPlaneReady           bool   `json:"controlPlaneReady"`
	Inititalized                bool   `json:"initialized"`
	ExternalManagedControlPlane bool   `json:"externalManagedControlPlane"`
	Replicas                    int32  `json:"replicas"`
	Version                     string `json:"version"`
}
