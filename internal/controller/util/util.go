package util

import km "github.com/k0smotron/k0smotron/api/k0smotron.io/v1beta1"

func DefaultK0smotronClusterLabels(kmc *km.Cluster) map[string]string {
	return map[string]string{
		"app":     "k0smotron",
		"cluster": kmc.Name,
	}
}

func LabelsForK0smotronCluster(kmc *km.Cluster) map[string]string {
	labels := DefaultK0smotronClusterLabels(kmc)
	for k, v := range kmc.Labels {
		labels[k] = v
	}
	labels["component"] = "cluster"
	return labels
}

func LabelsForK0smotronControlPlane(kmc *km.Cluster) map[string]string {
	labels := LabelsForK0smotronCluster(kmc)
	labels["cluster.x-k8s.io/control-plane"] = "true"
	return labels
}

func LabelsForEtcdK0smotronCluster(kmc *km.Cluster) map[string]string {
	labels := LabelsForK0smotronCluster(kmc)
	labels["component"] = "etcd"
	return labels
}

func AnnotationsForK0smotronCluster(kmc *km.Cluster) map[string]string {
	return kmc.Annotations
}
