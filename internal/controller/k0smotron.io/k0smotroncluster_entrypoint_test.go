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
)

func TestGetControllerFlags(t *testing.T) {
	var tests = []struct {
		name   string
		kmc    km.Cluster
		result string
	}{
		{
			"Undefined flags must not panic",
			km.Cluster{},
			"--config=/etc/k0s/k0s.yaml --enable-dynamic-config",
		},
		{
			"Empty flags",
			km.Cluster{Spec: km.ClusterSpec{ControlPlaneFlags: []string{}}},
			"--config=/etc/k0s/k0s.yaml --enable-dynamic-config",
		},
		{
			"Multiple flags",
			km.Cluster{Spec: km.ClusterSpec{ControlPlaneFlags: []string{"--foo=bar", "--bar=baz"}}},
			"--foo=bar --bar=baz --config=/etc/k0s/k0s.yaml --enable-dynamic-config",
		},
		{
			"Override dynamic config",
			km.Cluster{Spec: km.ClusterSpec{ControlPlaneFlags: []string{"--enable-dynamic-config=false"}}},
			"--enable-dynamic-config=false --config=/etc/k0s/k0s.yaml",
		},
		{
			"Override config path",
			km.Cluster{Spec: km.ClusterSpec{ControlPlaneFlags: []string{"--config=/custom/path"}}},
			"--config=/custom/path --enable-dynamic-config",
		},
	}

	for _, test := range tests {
		assert.Equal(t, test.result, getControllerFlags(&test.kmc), test.name)
	}
}
