/*


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

	bootstrapv1 "github.com/k0smotron/k0smotron/api/bootstrap/v1beta1"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	bsutil "sigs.k8s.io/cluster-api/bootstrap/util"
)

func Test_createInstallCmd(t *testing.T) {
	base := "k0s install worker --token-file /etc/k0s.token --labels=k0smotron.io/machine-name=test"
	tests := []struct {
		name  string
		scope *Scope
		want  string
	}{
		{
			name: "with default config",
			scope: &Scope{
				Config: &bootstrapv1.K0sWorkerConfig{},
				ConfigOwner: &bsutil.ConfigOwner{Unstructured: &unstructured.Unstructured{Object: map[string]interface{}{
					"metadata": map[string]interface{}{"name": "test"},
				}}},
			},
			want: base + ` --kubelet-extra-args="--hostname-override=test"`,
		},
		{
			name: "with args",
			scope: &Scope{
				Config: &bootstrapv1.K0sWorkerConfig{
					Spec: bootstrapv1.K0sWorkerConfigSpec{
						Args: []string{"--debug", "--labels=k0sproject.io/foo=bar", `--kubelet-extra-args="--hostname-override=test-from-arg"`},
					},
				},
				ConfigOwner: &bsutil.ConfigOwner{Unstructured: &unstructured.Unstructured{Object: map[string]interface{}{
					"metadata": map[string]interface{}{"name": "test"},
				}}},
			},
			want: base + ` --debug --labels=k0sproject.io/foo=bar --kubelet-extra-args="--hostname-override=test --hostname-override=test-from-arg"`,
		},
		{
			name: "with useSystemHostname set",
			scope: &Scope{
				Config: &bootstrapv1.K0sWorkerConfig{
					Spec: bootstrapv1.K0sWorkerConfigSpec{
						UseSystemHostname: true,
						Args:              []string{"--debug", "--labels=k0sproject.io/foo=bar", `--kubelet-extra-args="--hostname-override=test-from-arg"`},
					},
				},
				ConfigOwner: &bsutil.ConfigOwner{Unstructured: &unstructured.Unstructured{Object: map[string]interface{}{
					"metadata": map[string]interface{}{"name": "test"},
				}}},
			},
			want: base + ` --debug --labels=k0sproject.io/foo=bar --kubelet-extra-args="--hostname-override=test-from-arg"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, createInstallCmd(tt.scope))
		})
	}
}

func Test_createDownloadCommands(t *testing.T) {
	tests := []struct {
		name   string
		config *bootstrapv1.K0sWorkerConfig
		want   []string
	}{
		{
			name:   "with default config",
			config: &bootstrapv1.K0sWorkerConfig{},
			want: []string{
				"curl -sSfL https://get.k0s.sh | sh",
			},
		},
		{
			name: "with pre-installed k0s",
			config: &bootstrapv1.K0sWorkerConfig{
				Spec: bootstrapv1.K0sWorkerConfigSpec{
					PreInstalledK0s: true,
				},
			},
			want: nil,
		},
		{
			name: "with custom version",
			config: &bootstrapv1.K0sWorkerConfig{
				Spec: bootstrapv1.K0sWorkerConfigSpec{
					Version: "v1.2.3",
				},
			},
			want: []string{
				"curl -sSfL https://get.k0s.sh | K0S_VERSION=v1.2.3 sh",
			},
		},
		{
			name: "with custom download URL",
			config: &bootstrapv1.K0sWorkerConfig{
				Spec: bootstrapv1.K0sWorkerConfigSpec{
					DownloadURL: "https://example.com/k0s",
				},
			},
			want: []string{
				"curl -sSfL https://example.com/k0s -o /usr/local/bin/k0s",
				"chmod +x /usr/local/bin/k0s",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, createDownloadCommands(tt.config))
		})
	}
}
