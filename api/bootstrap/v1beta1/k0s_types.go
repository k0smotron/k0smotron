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
	"github.com/k0sproject/k0smotron/internal/cloudinit"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

func init() {
	SchemeBuilder.Register(&K0sWorkerConfig{}, &K0sWorkerConfigList{})
	SchemeBuilder.Register(&K0sControllerConfig{}, &K0sControllerConfigList{})
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:labels="cluster.x-k8s.io/v1beta1=v1beta1"
// +kubebuilder:metadata:labels="cluster.x-k8s.io/provider=bootstrap-k0smotron"

type K0sWorkerConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   K0sWorkerConfigSpec   `json:"spec,omitempty"`
	Status K0sWorkerConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type K0sWorkerConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []K0sWorkerConfig `json:"items"`
}

type K0sWorkerConfigSpec struct {
	// The secret that contains the join token.
	// Specify the secret only when using a pre-generated join token.
	// +kubebuilder:validation:Optional
	JoinTokenSecretRef *JoinTokenSecretRef `json:"joinTokenSecretRef,omitempty"`

	// The version of k0s to be deployed. If the parameter is not set, k0smotron uses
	// the Version field of the Machine object. If the Version field is empty, the latest version of k0s is used.
	// Make sure the version is compatible with the k0s version running on the control plane.
	// For reference, see the Kubernetes version skew policy: https://kubernetes.io/docs/setup/release/version-skew-policy/.
	// +kubebuilder:validation:Optional
	Version string `json:"version,omitempty"`

	// Additional files to be passed to user_data upon creation.
	// +kubebuilder:validation:Optional
	Files []cloudinit.File `json:"files,omitempty"`

	// Additional arguments to be passed to the k0s worker node.
	// See: https://docs.k0sproject.io/stable/advanced/worker-configuration/
	Args []string `json:"args,omitempty"`

	// Commands that should be executed before the k0s worker node start.
	// +kubebuilder:validation:Optional
	PreStartCommands []string `json:"preStartCommands,omitempty"`

	// Commands that should be executed after the k0s worker node start.
	// +kubebuilder:validation:Optional
	PostStartCommands []string `json:"postStartCommands,omitempty"`

	// Specifies whether k0s binary is pre-installed on the node.
	// +kubebuilder:validation:Optional
	PreInstalledK0s bool `json:"preInstalledK0s,omitempty"`

	// The URL from which the k0s binary should be downloaded.
	// If the Version field is empty, the downloaded version of k0s is used.
	// +kubebuilder:validation:Optional
	DownloadURL string `json:"downloadURL,omitempty"`
}

type JoinTokenSecretRef struct {
	// The name of the secret with the join token.
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// The key of the secret that contains the join token.
	// +kubebuilder:validation:Required
	Key string `json:"key"`
}

type K0sWorkerConfigStatus struct {
	// Indicates whether the Bootstrapdata field is ready to be consumed.
	Ready bool `json:"ready,omitempty"`

	// The name of the secret that stores the bootstrap data script.
	// +optional
	DataSecretName *string `json:"dataSecretName,omitempty"`
	// TODO Conditions etc
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:labels="cluster.x-k8s.io/v1beta1=v1beta1"
// +kubebuilder:metadata:labels="cluster.x-k8s.io/provider=bootstrap-k0smotron"

type K0sControllerConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   K0sControllerConfigSpec   `json:"spec,omitempty"`
	Status K0sControllerConfigStatus `json:"status,omitempty"`
}

type K0sControllerConfigStatus struct {
	// Indicates whether the Bootstrapdata field is ready to be consumed.
	Ready bool `json:"ready,omitempty"`

	// The name of the secret that stores the bootstrap data script.
	// +optional
	DataSecretName *string `json:"dataSecretName,omitempty"`
}

// +kubebuilder:object:root=true

type K0sControllerConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []K0sControllerConfig `json:"items"`
}

type K0sControllerConfigSpec struct {
	// The version of k0s to be deployed. If the parameter is not set, k0smotron uses
	// the Version field of the Machine object. If the Version field is empty, the latest version of k0s is used.
	// Make sure the version is compatible with the k0s version running on the control plane.
	// For reference, see the Kubernetes version skew policy: https://kubernetes.io/docs/setup/release/version-skew-policy/.
	// +kubebuilder:validation:Optional
	Version string `json:"version,omitempty"`

	*K0sConfigSpec `json:",inline"`
}

type K0sConfigSpec struct {
	// Defines the k0s configuration. Note, that some fields will be overwritten by k0smotron.
	// If empty, the default k0s configuration is used. For details, see https://docs.k0sproject.io/stable/configuration/.
	//+kubebuilder:validation:Optional
	//+kubebuilder:pruning:PreserveUnknownFields
	K0s *unstructured.Unstructured `json:"k0s,omitempty"`

	// Additinal files to be passed to user_data upon creation.
	// +kubebuilder:validation:Optional
	Files []cloudinit.File `json:"files,omitempty"`

	// Additional extra arguments to be passed to the k0s worker node.
	// See: https://docs.k0sproject.io/stable/advanced/worker-configuration/
	Args []string `json:"args,omitempty"`

	// Commands that should be executed before the k0s worker node start.
	// +kubebuilder:validation:Optional
	PreStartCommands []string `json:"preStartCommands,omitempty"`

	// Commands that should be executed after the k0s worker node start.
	// +kubebuilder:validation:Optional
	PostStartCommands []string `json:"postStartCommands,omitempty"`

	// Specifies whether k0s binary is pre-installed on the node.
	// +kubebuilder:validation:Optional
	PreInstalledK0s bool `json:"preInstalledK0s,omitempty"`

	// The URL from which the k0s binary should be downloaded.
	// If the Version field is empty, the downloaded version of k0s is used.
	// +kubebuilder:validation:Optional
	DownloadURL string `json:"downloadURL,omitempty"`

	// The tunneling configuration for the cluster.
	//+kubebuilder:validation:Optional
	Tunneling TunnelingSpec `json:"tunneling,omitempty"`
}

type TunnelingSpec struct {
	// Specifies whether tunneling is enabled.
	//+kubebuilder:validation:Optional
	//+kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`
	// The address of the tunneling server.
	// If empty, k0smotron tries to detect the address of the worker node.
	//+kubebuilder:validation:Optional
	ServerAddress string `json:"serverAddress,omitempty"`
	// Defines the NodePort to be used as the server port of the tunneling server.
	// If empty, k0smotron uses the default port.
	//+kubebuilder:validation:Optional
	//+kubebuilder:default=31700
	ServerNodePort int32 `json:"serverNodePort,omitempty"`
	// Defines the NodePort to be used as the tunneling port.
	// If empty, k0smotron uses the default port.
	//+kubebuilder:validation:Optional
	//+kubebuilder:default=31443
	TunnelingNodePort int32 `json:"tunnelingNodePort,omitempty"`
	// Defines the tunneling mode.
	// If empty, k0smotron uses the default mode.
	//+kubebuilder:validation:Enum=tunnel;proxy
	//+kubebuilder:default=tunnel
	Mode string `json:"mode,omitempty"`
}
