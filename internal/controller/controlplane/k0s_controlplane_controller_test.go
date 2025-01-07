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

package controlplane

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	bootstrapv1 "github.com/k0sproject/k0smotron/api/bootstrap/v1beta1"
	"github.com/k0sproject/k0smotron/api/controlplane/v1beta1"
)

func TestK0sConfigEnrichment(t *testing.T) {
	var testCases = []struct {
		cluster *clusterv1.Cluster
		kcp     *v1beta1.K0sControlPlane
		want    *unstructured.Unstructured
	}{
		{
			cluster: &clusterv1.Cluster{},
			kcp:     &v1beta1.K0sControlPlane{},
			want:    nil,
		},
		{
			cluster: &clusterv1.Cluster{
				Spec: clusterv1.ClusterSpec{
					ClusterNetwork: &clusterv1.ClusterNetwork{
						Services: &clusterv1.NetworkRanges{
							CIDRBlocks: []string{"10.96.0.0/12"},
						},
						Pods: &clusterv1.NetworkRanges{
							CIDRBlocks: []string{"10.244.0.0/16"},
						},
					},
				},
			},
			kcp: &v1beta1.K0sControlPlane{},
			want: &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "k0s.k0sproject.io/v1beta1",
				"kind":       "ClusterConfig",
				"spec": map[string]interface{}{
					"network": map[string]interface{}{"serviceCIDR": "10.96.0.0/12", "podCIDR": "10.244.0.0/16"},
				},
			}},
		},
		{
			cluster: &clusterv1.Cluster{
				Spec: clusterv1.ClusterSpec{
					ClusterNetwork: &clusterv1.ClusterNetwork{
						Services: &clusterv1.NetworkRanges{
							CIDRBlocks: []string{"10.96.0.0/12"},
						},
						Pods: &clusterv1.NetworkRanges{
							CIDRBlocks: []string{"10.244.0.0/16"},
						},
					},
				},
			},
			kcp: &v1beta1.K0sControlPlane{
				Spec: v1beta1.K0sControlPlaneSpec{
					K0sConfigSpec: bootstrapv1.K0sConfigSpec{
						K0s: &unstructured.Unstructured{Object: map[string]interface{}{
							"spec": map[string]interface{}{
								"network": map[string]interface{}{"serviceCIDR": "10.98.0.0/12"},
							},
						}},
					},
				},
			},
			want: &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "k0s.k0sproject.io/v1beta1",
				"kind":       "ClusterConfig",
				"spec": map[string]interface{}{
					"network": map[string]interface{}{"serviceCIDR": "10.98.0.0/12", "podCIDR": "10.244.0.0/16"},
				},
			}},
		},
		{
			cluster: &clusterv1.Cluster{
				Spec: clusterv1.ClusterSpec{
					ClusterNetwork: &clusterv1.ClusterNetwork{
						ServiceDomain: "cluster.local",
					},
				},
			},
			kcp: &v1beta1.K0sControlPlane{},
			want: &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "k0s.k0sproject.io/v1beta1",
				"kind":       "ClusterConfig",
				"spec": map[string]interface{}{
					"network": map[string]interface{}{"clusterDomain": "cluster.local"},
				},
			}},
		},
	}

	for _, tc := range testCases {
		t.Run("", func(t *testing.T) {
			actual, err := enrichK0sConfigWithClusterData(tc.cluster, tc.kcp.Spec.K0sConfigSpec.K0s)
			require.NoError(t, err)
			require.Equal(t, tc.want, actual)
		})
	}
}

func Test_machineName(t *testing.T) {
	var testCases = []struct {
		replicas        int32
		machineToDelete map[string]bool
		desiredMachines map[string]bool
		want            string
	}{
		{
			replicas:        3,
			machineToDelete: nil,
			desiredMachines: map[string]bool{},
			want:            "test-0",
		},
		{
			replicas:        3,
			machineToDelete: nil,
			desiredMachines: map[string]bool{
				"test-1": true,
			},
			want: "test-0",
		},
		{
			replicas: 3,
			machineToDelete: map[string]bool{
				"test-0": true,
				"test-1": true,
				"test-2": true,
			},
			desiredMachines: map[string]bool{
				"test-3": true,
			},
			want: "test-4",
		},
		{
			replicas: 3,
			machineToDelete: map[string]bool{
				"test-3": true,
				"test-4": true,
				"test-5": true,
			},
			desiredMachines: map[string]bool{},
			want:            "test-0",
		},
		{
			replicas: 3,
			machineToDelete: map[string]bool{
				"test-4": true,
				"test-5": true,
			},
			desiredMachines: map[string]bool{
				"test-0": true,
			},
			want: "test-1",
		},
		{
			replicas: 3,
			machineToDelete: map[string]bool{
				"test-5": true,
			},
			desiredMachines: map[string]bool{
				"test-0": true,
				"test-1": true,
			},
			want: "test-2",
		},
		{
			replicas:        3,
			machineToDelete: nil,
			desiredMachines: map[string]bool{
				"test-1": true,
				"test-2": true,
			},
			want: "test-0",
		},
	}

	for _, tc := range testCases {
		kcp := &v1beta1.K0sControlPlane{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
			Spec: v1beta1.K0sControlPlaneSpec{
				Replicas: tc.replicas,
			},
		}
		t.Run("", func(t *testing.T) {
			actual := machineName(kcp, tc.machineToDelete, tc.desiredMachines)
			require.Equal(t, tc.want, actual)
		})
	}
}
