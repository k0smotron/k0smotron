package v1beta1

import (
	kmapi "github.com/k0sproject/k0smotron/api/k0smotron.io/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func init() {
	SchemeBuilder.Register(&K0smotronControlPlaneTemplate{}, &K0smotronControlPlaneTemplateList{})
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:metadata:labels="cluster.x-k8s.io/v1beta1=v1beta1"

type K0smotronControlPlaneTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec K0smotronControlPlaneTemplateSpec `json:"spec,omitempty"`
}

type K0smotronControlPlaneTemplateSpec struct {
	Template K0smotronControlPlaneTemplateResource `json:"template,omitempty"`
}

type K0smotronControlPlaneTemplateResource struct {
	// +kubebuilder:validation:Optional
	ObjectMeta metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec       kmapi.ClusterSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

type K0smotronControlPlaneTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []K0smotronControlPlaneTemplate `json:"items"`
}
