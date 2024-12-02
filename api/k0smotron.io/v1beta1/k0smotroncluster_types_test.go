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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClusterSpec_GetImage(t *testing.T) {

	tests := []struct {
		name string
		spec *ClusterSpec
		want string
	}{
		{
			name: "Nothing given",
			spec: &ClusterSpec{},
			want: "k0sproject/k0s:v1.30.4-k0s.0",
		},
		{
			name: "Only version given with suffix",
			spec: &ClusterSpec{
				Version: "v1.29.4-k0s.0",
			},
			want: "k0sproject/k0s:v1.29.4-k0s.0",
		},
		{
			name: "Version given without suffix",
			spec: &ClusterSpec{
				Version: "v1.29.4",
			},
			want: "k0sproject/k0s:v1.29.4-k0s.0",
		},
		{
			name: "Image given without version should use default version",
			spec: &ClusterSpec{
				Image: "foobar/k0s",
			},
			want: "foobar/k0s:v1.30.4-k0s.0",
		},
		{
			name: "Image and version given",
			spec: &ClusterSpec{
				Image:   "foobar/k0s",
				Version: "v1.29.4",
			},
			want: "foobar/k0s:v1.29.4-k0s.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.spec.GetImage(); got != tt.want {
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestShortName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{
			"kmc-cluster",
			"kmc-cluster",
		},
		{
			"kmc-prometheus-dk-enc-nodes-basic-sep17-v5-lg5hz-njttw-config",
			"kmc-prometheus-dk-enc-nodes-basic-sep17-v5-lg5hz-njttw-config",
		},
		{
			"kmc-prometheus-dk-enc-nodes-basic-sep17-v5-lg5hz-njttw-config-nginx",
			"kmc-prometheus-dk-enc-nodes-basic-sep17-v5-lg5hz-njttw-co-c79c4",
		},
		{
			"kmc-prometheus-dk-enc-nodes-basic-sep17-v5-lg5hz-njttw-config-nginx2",
			"kmc-prometheus-dk-enc-nodes-basic-sep17-v5-lg5hz-njttw-co-a85e8",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shortName(tt.name)
			require.Equal(t, tt.want, got)
			require.LessOrEqual(t, len(got), 63)
		})
	}
}
