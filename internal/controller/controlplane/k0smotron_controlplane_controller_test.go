package controlplane

import (
	"testing"

	kapi "github.com/k0smotron/k0smotron/api/k0smotron.io/v1beta1"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestIsClusterSpecSynced(t *testing.T) {
	testCases := []struct {
		name     string
		kmcSpec  kapi.ClusterSpec
		kcpSpec  kapi.ClusterSpec
		expected bool
	}{
		{
			name: "cluster spec is synced",
			kmcSpec: kapi.ClusterSpec{
				CertificateRefs: []kapi.CertificateRef{
					{
						Type: "cert-type-1",
						Name: "cert-name-1",
					},
					{
						Type: "cert-type-2",
						Name: "cert-name-2",
					},
					{
						Type: "cert-type-3",
						Name: "cert-name-3",
					},
				},
				K0sConfig: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "k0s.k0sproject.io/v1beta1",
						"kind":       "ClusterConfig",
						"metadata": map[string]interface{}{
							"name":      "k0s",
							"namespace": "kube-system",
						},
						"spec": map[string]any{
							"api": map[string]interface{}{
								"externalAddress": "172.18.0.2",
								"port":            int64(30443),
								"sans": []interface{}{
									"10.96.138.152",
									"172.18.0.2",
									"kmc-docker-test-nodeport",
									"kmc-docker-test-nodeport.default",
									"kmc-docker-test-nodeport.default.svc",
									"kmc-docker-test-nodeport.default.svc.cluster.local",
								},
							},
							"konnectivity": map[string]interface{}{
								"agentPort": int64(30132),
							},
							"network": map[string]interface{}{
								"clusterDomain": "cluster.local",
								"podCIDR":       "192.168.0.0/16",
								"serviceCIDR":   "10.128.0.0/12",
							},
						},
					},
				},
			},
			kcpSpec: kapi.ClusterSpec{
				CertificateRefs: []kapi.CertificateRef{
					{
						Type: "cert-type-1",
						Name: "cert-name-1",
					},
					{
						Type: "cert-type-2",
						Name: "cert-name-2",
					},
				},
				K0sConfig: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "k0s.k0sproject.io/v1beta1",
						"kind":       "ClusterConfig",
						"metadata": map[string]interface{}{
							"name":      "k0s",
							"namespace": "kube-system",
						},
						"spec": map[string]interface{}{
							"network": map[string]interface{}{
								"clusterDomain": "cluster.local",
								"podCIDR":       "192.168.0.0/16",
								"serviceCIDR":   "10.128.0.0/12",
							},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "cluster spec is not synced",
			kmcSpec: kapi.ClusterSpec{
				CertificateRefs: []kapi.CertificateRef{
					{
						Type: "cert-type-1",
						Name: "cert-name-1",
					},
					{
						Type: "cert-type-2",
						Name: "cert-name-2",
					},
					{
						Type: "cert-type-3",
						Name: "cert-name-3",
					},
				},
				K0sConfig: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "k0s.k0sproject.io/v1beta1",
						"kind":       "ClusterConfig",
						"metadata": map[string]interface{}{
							"name":      "k0s",
							"namespace": "kube-system",
						},
						"spec": map[string]any{
							"api": map[string]interface{}{
								"externalAddress": "172.18.0.2",
								"port":            int64(30443),
								"sans": []interface{}{
									"10.96.138.152",
									"172.18.0.2",
									"kmc-docker-test-nodeport",
									"kmc-docker-test-nodeport.default",
									"kmc-docker-test-nodeport.default.svc",
									"kmc-docker-test-nodeport.default.svc.cluster.local",
								},
							},
							"konnectivity": map[string]interface{}{
								"agentPort": int64(30132),
							},
							"network": map[string]interface{}{
								"clusterDomain": "cluster.local",
								"podCIDR":       "192.168.0.0/16",
								"serviceCIDR":   "10.128.0.0/12",
							},
						},
					},
				},
			},
			kcpSpec: kapi.ClusterSpec{
				CertificateRefs: []kapi.CertificateRef{
					{
						Type: "cert-type-1",
						Name: "cert-name-1",
					},
					{
						Type: "cert-type-2",
						Name: "cert-name-2",
					},
				},
				K0sConfig: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "k0s.k0sproject.io/v1beta1",
						"kind":       "ClusterConfig",
						"metadata": map[string]interface{}{
							"name":      "k0s",
							"namespace": "kube-system",
						},
						"spec": map[string]interface{}{
							"network": map[string]interface{}{
								"clusterDomain": "cluster.local",
								"podCIDR":       "192.168.0.0/16",
								"serviceCIDR":   "10.128.0.0/12",
							},
							// kmcspec does not have same value for api.externalAddress
							"api": map[string]interface{}{
								"externalAddress": "172.18.0.3",
							},
						},
					},
				},
			},
			expected: false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := isClusterSpecSynced(tc.kmcSpec, tc.kcpSpec)
			require.NoError(t, err)
			require.EqualValues(t, tc.expected, result)
		})
	}
}
