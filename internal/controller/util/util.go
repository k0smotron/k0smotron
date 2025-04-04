package util

import (
	"sort"

	km "github.com/k0smotron/k0smotron/api/k0smotron.io/v1beta1"
)

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

// AddToExistingSans merges original sans list with a new sans slice avoiding duplicated values.
func AddToExistingSans(existing []string, new []string) []string {
	uniques := make(map[string]struct{})
	for _, val := range existing {
		uniques[val] = struct{}{}
	}
	for _, val := range new {
		uniques[val] = struct{}{}
	}
	finalSans := make([]string, 0, len(uniques))
	for key := range uniques {
		finalSans = append(finalSans, key)
	}

	// Sort the sans to ensure stable output order
	sort.Strings(finalSans)

	return finalSans
}
