/*
Copyright 2025.

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

package bootstrap

import (
	"testing"

	bootstrapv1 "github.com/k0sproject/k0smotron/api/bootstrap/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

func Test_createBootstrapSecret(t *testing.T) {
	tests := []struct {
		name                string
		scope               *Scope
		bootstrapData       []byte
		expectedLabels      map[string]string
		expectedAnnotations map[string]string
	}{
		{
			name: "with custom metadata",
			scope: &Scope{
				Config: &bootstrapv1.K0sWorkerConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-config",
						Namespace: "test-ns",
						UID:       "test-uid",
					},
					TypeMeta: metav1.TypeMeta{
						Kind: "K0sWorkerConfig",
					},
					Spec: bootstrapv1.K0sWorkerConfigSpec{
						SecretMetadata: &bootstrapv1.SecretMetadata{
							Labels: map[string]string{
								"custom-label": "foo",
							},
							Annotations: map[string]string{
								"custom-anno": "bar",
							},
						},
					},
				},
				Cluster: &clusterv1.Cluster{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster",
					},
				},
			},
			bootstrapData: []byte("test-bootstrap-data"),
			expectedLabels: map[string]string{
				"custom-label":             "foo",
				clusterv1.ClusterNameLabel: "test-cluster",
			},
			expectedAnnotations: map[string]string{
				"custom-anno": "bar",
			},
		},
		{
			name: "without custom metadata",
			scope: &Scope{
				Config: &bootstrapv1.K0sWorkerConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-config",
						Namespace: "test-ns",
						UID:       "test-uid",
					},
					TypeMeta: metav1.TypeMeta{
						Kind: "K0sWorkerConfig",
					},
				},
				Cluster: &clusterv1.Cluster{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster",
					},
				},
			},
			bootstrapData: []byte("test-bootstrap-data"),
			expectedLabels: map[string]string{
				clusterv1.ClusterNameLabel: "test-cluster",
			},
			expectedAnnotations: map[string]string{},
		},
		{
			name: "with nil secret metadata",
			scope: &Scope{
				Config: &bootstrapv1.K0sWorkerConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-config",
						Namespace: "test-ns",
						UID:       "test-uid",
					},
					TypeMeta: metav1.TypeMeta{
						Kind: "K0sWorkerConfig",
					},
					Spec: bootstrapv1.K0sWorkerConfigSpec{
						SecretMetadata: nil,
					},
				},
				Cluster: &clusterv1.Cluster{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-cluster",
					},
				},
			},
			bootstrapData: []byte("test-bootstrap-data"),
			expectedLabels: map[string]string{
				clusterv1.ClusterNameLabel: "test-cluster",
			},
			expectedAnnotations: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := createBootstrapSecret(tt.scope, tt.bootstrapData)

			// Check basic properties
			assert.Equal(t, tt.scope.Config.Name, secret.Name)
			assert.Equal(t, tt.scope.Config.Namespace, secret.Namespace)
			assert.Equal(t, clusterv1.ClusterSecretType, secret.Type)
			assert.Equal(t, tt.bootstrapData, secret.Data["value"])

			// Check labels
			assert.Equal(t, tt.expectedLabels, secret.Labels)

			// Check annotations
			assert.Equal(t, tt.expectedAnnotations, secret.Annotations)

			// Check owner references
			require.Len(t, secret.OwnerReferences, 1)
			assert.Equal(t, bootstrapv1.GroupVersion.String(), secret.OwnerReferences[0].APIVersion)
			assert.Equal(t, tt.scope.Config.Kind, secret.OwnerReferences[0].Kind)
			assert.Equal(t, tt.scope.Config.Name, secret.OwnerReferences[0].Name)
			assert.Equal(t, tt.scope.Config.UID, secret.OwnerReferences[0].UID)
			assert.True(t, *secret.OwnerReferences[0].Controller)
		})
	}
}
