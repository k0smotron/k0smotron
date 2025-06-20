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

package k0smotronio

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	km "github.com/k0sproject/k0smotron/api/k0smotron.io/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestEtcd_calculateDesiredReplicas(t *testing.T) {
	var tests = []struct {
		cluster *km.Cluster
		want    int32
	}{
		{cluster: &km.Cluster{}, want: 1},
		{cluster: &km.Cluster{Spec: km.ClusterSpec{Replicas: 1}}, want: 1},
		{cluster: &km.Cluster{Spec: km.ClusterSpec{Replicas: 2}}, want: 3},
		{cluster: &km.Cluster{Spec: km.ClusterSpec{Replicas: 3}}, want: 3},
		{cluster: &km.Cluster{Spec: km.ClusterSpec{Replicas: 4}}, want: 5},
		{cluster: &km.Cluster{Spec: km.ClusterSpec{Replicas: 5}}, want: 5},
	}

	for _, tc := range tests {
		t.Run("", func(t *testing.T) {
			got := calculateDesiredReplicas(tc.cluster)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestEtcd_resourceRequirements(t *testing.T) {
	tests := []struct {
		name    string
		cluster *km.Cluster
		want    func(t *testing.T, resources corev1.ResourceRequirements)
	}{
		{
			name:    "Default - No resources specified",
			cluster: &km.Cluster{}, // No Resources specified
			want: func(t *testing.T, resources corev1.ResourceRequirements) {
				assert.Empty(t, resources.Requests)
				assert.Empty(t, resources.Limits)
			},
		},
		{
			name: "Resources specified - Requests only",
			cluster: &km.Cluster{
				Spec: km.ClusterSpec{
					Etcd: km.EtcdSpec{
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("128Mi"),
							},
						},
					},
				},
			},
			want: func(t *testing.T, resources corev1.ResourceRequirements) {
				assert.Equal(t, resource.MustParse("100m"), *resources.Requests.Cpu())
				assert.Equal(t, resource.MustParse("128Mi"), *resources.Requests.Memory())
				assert.Empty(t, resources.Limits)
			},
		},
		{
			name: "Resources specified - Requests and limits",
			cluster: &km.Cluster{
				Spec: km.ClusterSpec{
					Etcd: km.EtcdSpec{
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("128Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("200m"),
								corev1.ResourceMemory: resource.MustParse("256Mi"),
							},
						},
					},
				},
			},
			want: func(t *testing.T, resources corev1.ResourceRequirements) {
				assert.Equal(t, resource.MustParse("100m"), *resources.Requests.Cpu())
				assert.Equal(t, resource.MustParse("128Mi"), *resources.Requests.Memory())
				assert.Equal(t, resource.MustParse("200m"), *resources.Limits.Cpu())
				assert.Equal(t, resource.MustParse("256Mi"), *resources.Limits.Memory())
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sts := generateEtcdStatefulSet(tc.cluster, nil, 1)
			resources := sts.Spec.Template.Spec.Containers[0].Resources
			tc.want(t, resources)
		})
	}
}

func TestEtcd_generateEtcdStatefulSet(t *testing.T) {
	var tests = []struct {
		cluster *km.Cluster
		want    []string
	}{
		{
			cluster: &km.Cluster{},
			want: []string{
				"--auto-compaction-mode=periodic",
				"--auto-compaction-retention=5m",
				"--snapshot-count=10000",
			}},
		{
			cluster: &km.Cluster{Spec: km.ClusterSpec{Etcd: km.EtcdSpec{Args: []string{
				"--auto-compaction-mode=periodic",
			}}}},
			want: []string{
				"--auto-compaction-mode=periodic",
				"--auto-compaction-retention=5m",
				"--snapshot-count=10000",
			}},
		{
			cluster: &km.Cluster{Spec: km.ClusterSpec{Etcd: km.EtcdSpec{Args: []string{
				"--auto-compaction-mode=periodic",
				"--auto-compaction-retention=2h",
				"--snapshot-count=50000",
			}}}},
			want: []string{
				"--auto-compaction-mode=periodic",
				"--auto-compaction-retention=2h",
				"--snapshot-count=50000",
			}},
	}

	for _, tc := range tests {
		t.Run("", func(t *testing.T) {
			sts := generateEtcdStatefulSet(tc.cluster, nil, 1)
			for _, w := range tc.want {
				assert.True(t, strings.Contains(sts.Spec.Template.Spec.Containers[0].Args[1], w))
			}
		})
	}
}
