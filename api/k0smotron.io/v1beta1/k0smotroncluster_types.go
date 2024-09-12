/*
Copyright 2023.

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
	"fmt"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ClusterSpec defines the desired state of K0smotronCluster
type ClusterSpec struct {
	// Replicas is the desired number of replicas of the k0s control planes.
	// If unspecified, defaults to 1. If the value is above 1, k0smotron requires kine datasource URL to be set.
	// Recommended value is 3.
	//+kubebuilder:validation:Optional
	//+kubebuilder:default=1
	Replicas int32 `json:"replicas,omitempty"`
	// Image defines the k0s image to be deployed. If empty k0smotron
	// will pick it automatically. Must not include the image tag.
	//+kubebuilder:default=k0sproject/k0s
	Image string `json:"image,omitempty"`
	// Version defines the k0s version to be deployed. If empty k0smotron
	// will pick it automatically.
	//+kubebuilder:validation:Optional
	Version string `json:"version,omitempty"`
	// ExternalAddress defines k0s external address. See https://docs.k0sproject.io/stable/configuration/#specapi
	// Will be detected automatically for service type LoadBalancer.
	//+kubebuilder:validation:Optional
	ExternalAddress string `json:"externalAddress,omitempty"`
	// Service defines the service configuration.
	//+kubebuilder:validation:Optional
	//+kubebuilder:default={"type":"ClusterIP","apiPort":30443,"konnectivityPort":30132}
	Service ServiceSpec `json:"service,omitempty"`
	// Persistence defines the persistence configuration. If empty k0smotron
	// will use emptyDir as a volume.
	//+kubebuilder:validation:Optional
	Persistence PersistenceSpec `json:"persistence,omitempty"`
	// KineDataSourceURL defines the kine datasource URL.
	// KineDataSourceURL or KineDataSourceSecretName are required for HA controlplane setup
	// and one of them must be set if replicas > 1.
	//+kubebuilder:validation:Optional
	KineDataSourceURL string `json:"kineDataSourceURL,omitempty"`
	// KineDataSourceSecretName defines the name of kine datasource URL secret.
	// KineDataSourceURL or KineDataSourceSecretName are required for HA controlplane setup
	// and one of them must be set if replicas > 1.
	//+kubebuilder:validation:Optional
	KineDataSourceSecretName string `json:"kineDataSourceSecretName,omitempty"`
	// k0sConfig defines the k0s configuration. Note, that some fields will be overwritten by k0smotron.
	// If empty, will be used default configuration. @see https://docs.k0sproject.io/stable/configuration/
	//+kubebuilder:validation:Optional
	//+kubebuilder:pruning:PreserveUnknownFields
	K0sConfig *unstructured.Unstructured `json:"k0sConfig,omitempty"`
	// CertificateRefs defines the certificate references.
	CertificateRefs []CertificateRef `json:"certificateRefs,omitempty"`
	// Manifests allows to specify list of volumes with manifests to be
	// deployed in the cluster. The volumes will be mounted
	// in /var/lib/k0s/manifests/<manifests.name>, for this reason each
	// manifest is a stack. K0smotron allows any kind of volume, but the
	// recommendation is to use secrets and configmaps.
	// For more information check:
	// https://docs.k0sproject.io/stable/manifests/ and
	// https://kubernetes.io/docs/concepts/storage/volumes
	//+kubebuilder:validation:Optional
	Manifests []v1.Volume `json:"manifests,omitempty"`
	// Mounts allows to specify list of volumes with any files to be
	// mounted in the controlplane pod. K0smotron allows any kind of volume, but the
	// recommendation is to use secrets and configmaps.
	// For more information check:
	// https://kubernetes.io/docs/concepts/storage/volumes
	//+kubebuilder:validation:Optional
	Mounts []Mount `json:"mounts,omitempty"`
	// ControlPlaneFlags allows to configure additional flags for k0s
	// control plane and to override existing ones. The default flags are
	// kept unless they are overriden explicitly. Flags with arguments must
	// be specified as a single string, e.g. --some-flag=argument
	//+kubebuilder:validation:Optional
	ControlPlaneFlags []string `json:"controllerPlaneFlags,omitempty"`
	// Monitoring defines the monitoring configuration.
	//+kubebuilder:validation:Optional
	Monitoring MonitoringSpec `json:"monitoring,omitempty"`
	// Etcd defines the etcd configuration.
	//+kubebuilder:default={"image":"quay.io/k0sproject/etcd:v3.5.13","persistence":{}}
	Etcd EtcdSpec `json:"etcd,omitempty"`

	// Resources describes the compute resource requirements for the control plane pods.
	Resources v1.ResourceRequirements `json:"resources,omitempty"`
}

type Mount struct {
	Path            string `json:"path"`
	v1.VolumeSource `json:",inline"`
}

const (
	defaultK0SImage   = "k0sproject/k0s"
	defaultK0SVersion = "v1.30.4-k0s.0"
	defaultK0SSuffix  = "k0s.0"
)

func (c *ClusterSpec) GetImage() string {
	k0sVersion := c.Version
	if k0sVersion == "" {
		k0sVersion = defaultK0SVersion
	}

	if !strings.Contains(k0sVersion, "-k0s.") {
		k0sVersion = fmt.Sprintf("%s-%s", k0sVersion, defaultK0SSuffix)
	}

	if c.Image == "" {
		return fmt.Sprintf("%s:%s", defaultK0SImage, k0sVersion)
	}

	return fmt.Sprintf("%s:%s", c.Image, k0sVersion)
}

// ClusterStatus defines the observed state of K0smotronCluster
type ClusterStatus struct {
	ReconciliationStatus string `json:"reconciliationStatus"`
	Ready                bool   `json:"ready,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=kmc

// Cluster is the Schema for the k0smotronclusters API
type Cluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	//+kubebuilder:validation:Optional
	//+kubebuilder:default={service:{type:NodePort}}
	Spec   ClusterSpec   `json:"spec,omitempty"`
	Status ClusterStatus `json:"status,omitempty"`
}

type ServiceSpec struct {
	//+kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	//+kubebuilder:default=ClusterIP
	Type v1.ServiceType `json:"type"`
	// APIPort defines the kubernetes API port. If empty k0smotron
	// will pick it automatically.
	//+kubebuilder:validation:Optional
	//+kubebuilder:default=30443
	APIPort int `json:"apiPort,omitempty"`
	// KonnectivityPort defines the konnectivity port. If empty k0smotron
	// will pick it automatically.
	//+kubebuilder:validation:Optional
	//+kubebuilder:default=30132
	KonnectivityPort int `json:"konnectivityPort,omitempty"`

	// Annotations defines extra annotations to be added to the service.
	//+kubebuilder:validation:Optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

//+kubebuilder:object:root=true

// ClusterList contains a list of K0smotronCluster
type ClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Cluster `json:"items"`
}

type PersistenceSpec struct {
	//+kubebuilder:validation:Enum:emptyDir;hostPath;pvc
	//+kubebuilder:default=emptyDir
	Type string `json:"type"`
	// PersistentVolumeClaim defines the PVC configuration. Will be used as is in case of .spec.persistence.type is pvc.
	//+kubebuilder:validation:Optional
	PersistentVolumeClaim *PersistentVolumeClaim `json:"persistentVolumeClaim,omitempty"`
	// HostPath defines the host path configuration. Will be used as is in case of .spec.persistence.type is hostPath.
	//+kubebuilder:validation:Optional
	HostPath string `json:"hostPath,omitempty"`
}

// PersistentVolumeClaim is a user's request for and claim to a persistent volume
type PersistentVolumeClaim struct {
	metav1.TypeMeta `json:",inline"`
	// Standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	// +optional
	ObjectMeta `json:"metadata,omitempty" protobuf:"bytes,1,opt,name=metadata"`

	// spec defines the desired characteristics of a volume requested by a pod author.
	// More info: https://kubernetes.io/docs/concepts/storage/persistent-volumes#persistentvolumeclaims
	// +optional
	Spec v1.PersistentVolumeClaimSpec `json:"spec,omitempty" protobuf:"bytes,2,opt,name=spec"`

	// status represents the current information/status of a persistent volume claim.
	// Read-only.
	// More info: https://kubernetes.io/docs/concepts/storage/persistent-volumes#persistentvolumeclaims
	// +optional
	Status v1.PersistentVolumeClaimStatus `json:"status,omitempty" protobuf:"bytes,3,opt,name=status"`
}

type ObjectMeta struct {
	// +optional
	Name string `json:"name,omitempty" protobuf:"bytes,1,opt,name=name"`

	// +optional
	Namespace string `json:"namespace,omitempty" protobuf:"bytes,3,opt,name=namespace"`

	// +optional
	Labels map[string]string `json:"labels,omitempty" protobuf:"bytes,11,rep,name=labels"`

	// +optional
	Annotations map[string]string `json:"annotations,omitempty" protobuf:"bytes,12,rep,name=annotations"`

	// +optional
	// +patchStrategy=merge
	Finalizers []string `json:"finalizers,omitempty" patchStrategy:"merge" protobuf:"bytes,14,rep,name=finalizers"`
}

type MonitoringSpec struct {
	// Enabled enables prometheus sidecar that scrapes metrics from the child cluster system components and expose
	// them as usual kubernetes pod metrics.
	Enabled bool `json:"enabled"`
	// PrometheusImage defines the image used for the prometheus sidecar.
	//+kubebuilder:default="quay.io/k0sproject/prometheus:v2.44.0"
	PrometheusImage string `json:"prometheusImage"`
	// ProxyImage defines the image used for the nginx proxy sidecar.
	//+kubebuilder:default="nginx:1.19.10"
	ProxyImage string `json:"proxyImage"`
}

type EtcdSpec struct {
	// Image defines the etcd image to be deployed.
	//+kubebuilder:default="quay.io/k0sproject/etcd:v3.5.13"
	Image string `json:"image"`
	// Args defines the etcd arguments.
	//+kubebuilder:validation:Optional
	Args []string `json:"args,omitempty"`
	// Persistence defines the persistence configuration.
	//+kubebuilder:validation:Optional
	Persistence EtcdPersistenceSpec `json:"persistence"`
}

type EtcdPersistenceSpec struct {
	// StorageClass defines the storage class to be used for etcd persistence. If empty, will be used the default storage class.
	//+kubebuilder:validation:Optional
	StorageClass string `json:"storageClass"`
	// Size defines the size of the etcd volume. Default: 1Gi
	//+kubebuilder:default="1Gi"
	//+kubebuilder:validation:Optional
	Size resource.Quantity `json:"size"`
}

type CertificateRef struct {
	//+kubebuilder:validation:Enum=ca;sa;proxy;etcd;apiserver-etcd-client;etcd-peer;etcd-server
	Type string `json:"type"`
	//+kubebuilder:validation:Optional
	Name string `json:"name,omitempty"`
}

func init() {
	SchemeBuilder.Register(&Cluster{}, &ClusterList{})
}

func GetStatefulSetName(clusterName string) string {
	return fmt.Sprintf("kmc-%s", clusterName)
}

func (kmc *Cluster) GetStatefulSetName() string {
	return GetStatefulSetName(kmc.Name)
}

func (kmc *Cluster) GetEtcdStatefulSetName() string {
	return fmt.Sprintf("kmc-%s-etcd", kmc.Name)
}

func (kmc *Cluster) GetAdminConfigSecretName() string {
	return fmt.Sprintf("%s-kubeconfig", kmc.Name)
}

func (kmc *Cluster) GetEntrypointConfigMapName() string {
	return fmt.Sprintf("kmc-entrypoint-%s-config", kmc.Name)
}

func (kmc *Cluster) GetMonitoringConfigMapName() string {
	return fmt.Sprintf("kmc-prometheus-%s-config", kmc.Name)
}

func (kmc *Cluster) GetConfigMapName() string {
	return fmt.Sprintf("kmc-%s-config", kmc.Name)
}

func (kmc *Cluster) GetServiceName() string {
	switch kmc.Spec.Service.Type {
	case v1.ServiceTypeNodePort:
		return kmc.GetNodePortServiceName()
	case v1.ServiceTypeLoadBalancer:
		return kmc.GetLoadBalancerServiceName()
	case v1.ServiceTypeClusterIP:
		return kmc.GetClusterIPServiceName()
	default:
		// The list of service types is limited and defined as enum in the CRD, so the default case should never be reached
		panic("unknown service type")
	}
}

func (kmc *Cluster) GetClusterIPServiceName() string {
	return fmt.Sprintf("kmc-%s", kmc.Name)
}

func (kmc *Cluster) GetEtcdServiceName() string {
	return fmt.Sprintf("kmc-%s-etcd", kmc.Name)
}

func (kmc *Cluster) GetLoadBalancerServiceName() string {
	return fmt.Sprintf("kmc-%s-lb", kmc.Name)
}

func (kmc *Cluster) GetNodePortServiceName() string {
	return fmt.Sprintf("kmc-%s-nodeport", kmc.Name)
}

func (kmc *Cluster) GetVolumeName() string {
	return fmt.Sprintf("kmc-%s", kmc.Name)
}
