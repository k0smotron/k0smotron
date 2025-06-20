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
	"testing"

	km "github.com/k0sproject/k0smotron/api/k0smotron.io/v1beta1"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestClusterReconciler_serviceLabels(t *testing.T) {
	tests := []struct {
		name string
		kmc  *km.Cluster
		want map[string]string
	}{
		{
			name: "when no labels are set on either Cluster on svc",
			kmc: &km.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: km.ClusterSpec{},
			},
			want: map[string]string{
				"app":       "k0smotron",
				"cluster":   "test",
				"component": "cluster",
			},
		},
		{
			name: "when labels are set on only Cluster",
			kmc: &km.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Labels: map[string]string{
						"test": "test",
					},
				},
				Spec: km.ClusterSpec{},
			},
			want: map[string]string{
				"app":       "k0smotron",
				"cluster":   "test",
				"component": "cluster",
				"test":      "test",
			},
		},
		{
			name: "when labels are set on both Cluster and svc",
			kmc: &km.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Labels: map[string]string{
						"test": "test",
					},
				},
				Spec: km.ClusterSpec{
					Service: km.ServiceSpec{
						Labels: map[string]string{
							"foo": "bar",
						},
					},
				},
			},
			want: map[string]string{
				"app":       "k0smotron",
				"cluster":   "test",
				"component": "cluster",
				"test":      "test",
				"foo":       "bar",
			},
		},
		{
			name: "when same labels is set on both Cluster and svc the svc labels wins",
			kmc: &km.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Labels: map[string]string{
						"test": "test",
					},
				},
				Spec: km.ClusterSpec{
					Service: km.ServiceSpec{
						Labels: map[string]string{
							"test": "foobar",
						},
					},
				},
			},
			want: map[string]string{
				"app":       "k0smotron",
				"cluster":   "test",
				"component": "cluster",
				"test":      "foobar",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := generateService(tt.kmc)
			got := svc.Labels
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestClusterReconciler_serviceAnnotations(t *testing.T) {
	tests := []struct {
		name string
		kmc  *km.Cluster
		want map[string]string
	}{
		{
			name: "when no annotations are set on either Cluster on svc",
			kmc: &km.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: km.ClusterSpec{},
			},
			want: map[string]string{},
		},
		{
			name: "when annotations are set on only Cluster",
			kmc: &km.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"test": "test",
					},
				},
				Spec: km.ClusterSpec{},
			},
			want: map[string]string{
				"test": "test",
			},
		},
		{
			name: "when annotations are set on both Cluster and svc",
			kmc: &km.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"test": "test",
					},
				},
				Spec: km.ClusterSpec{
					Service: km.ServiceSpec{
						Annotations: map[string]string{
							"foo": "bar",
						},
					},
				},
			},
			want: map[string]string{
				"test": "test",
				"foo":  "bar",
			},
		},
		{
			name: "when same annotation is set on both Cluster and svc the svc annotation wins",
			kmc: &km.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"test": "test",
					},
				},
				Spec: km.ClusterSpec{
					Service: km.ServiceSpec{
						Annotations: map[string]string{
							"test": "foobar",
						},
					},
				},
			},
			want: map[string]string{
				"test": "foobar",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := generateService(tt.kmc)
			got := svc.Annotations
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestClusterReconciler_serviceLoadBalancerClass(t *testing.T) {
	tests := []struct {
		name string
		kmc  *km.Cluster
		want *string
	}{
		{
			name: "when no loadBalancerClass is set",
			kmc: &km.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: km.ClusterSpec{},
			},
			want: nil,
		},
		{
			name: "when loadBalancerClass is set on LoadBalancer service",
			kmc: &km.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: km.ClusterSpec{
					Service: km.ServiceSpec{
						Type:              v1.ServiceTypeLoadBalancer,
						LoadBalancerClass: ptr.To("class1"),
					},
				},
			},
			want: ptr.To("class1"),
		},
		{
			name: "when loadBalancerClass is set on non LoadBalancer service",
			kmc: &km.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: km.ClusterSpec{
					Service: km.ServiceSpec{
						Type:              v1.ServiceTypeClusterIP,
						LoadBalancerClass: ptr.To("class1"),
					},
				},
			},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := generateService(tt.kmc)
			got := svc.Spec.LoadBalancerClass
			assert.Equal(t, tt.want, got)
		})
	}
}
