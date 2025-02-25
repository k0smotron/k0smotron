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
	"bytes"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	restfake "k8s.io/client-go/rest/fake"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
	clientcmdlatest "k8s.io/client-go/tools/clientcmd/api/latest"
	"k8s.io/kubectl/pkg/scheme"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	kubeadmConfig "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1beta1"
	"sigs.k8s.io/cluster-api/controllers/external"
	"sigs.k8s.io/cluster-api/util"
	capiutil "sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/certs"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/kubeconfig"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bootstrapv1 "github.com/k0smotron/k0smotron/api/bootstrap/v1beta1"
	"github.com/k0smotron/k0smotron/api/controlplane/v1beta1"
	cpv1beta1 "github.com/k0smotron/k0smotron/api/controlplane/v1beta1"
	autopilot "github.com/k0sproject/k0s/pkg/apis/autopilot/v1beta2"
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

func TestReconcileReturnErrorWhenOwnerClusterIsMissing(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-reconcile-return-error-cluster-owner-missing")
	require.NoError(t, err)

	cluster, kcp, gmt := createClusterWithControlPlane(ns.Name)
	require.NoError(t, testEnv.Create(ctx, cluster))
	require.NoError(t, testEnv.Create(ctx, kcp))
	require.NoError(t, testEnv.Create(ctx, gmt))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(kcp, gmt, cluster, ns)

	r := &K0sController{
		Client: testEnv,
	}

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: util.ObjectKey(kcp)})
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{RequeueAfter: 20 * time.Second, Requeue: true}, result)
	require.NoError(t, testEnv.CleanupAndWait(ctx, cluster))

	require.Eventually(t, func() bool {
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: util.ObjectKey(kcp)})
		return err != nil
	}, 5*time.Second, 100*time.Millisecond)
}

func TestReconcileNoK0sControlPlane(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-reconcile-no-control-plane")
	require.NoError(t, err)

	cluster, kcp, _ := createClusterWithControlPlane(ns.Name)
	require.NoError(t, testEnv.Create(ctx, cluster))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(cluster, ns)

	r := &K0sController{
		Client: testEnv,
	}

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: util.ObjectKey(kcp)})
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)
}

func TestReconcilePausedCluster(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-reconcile-paused-cluster")
	require.NoError(t, err)

	cluster, kcp, _ := createClusterWithControlPlane(ns.Name)

	// Cluster 'paused'.
	cluster.Spec.Paused = true

	require.NoError(t, testEnv.Create(ctx, cluster))
	require.NoError(t, testEnv.Create(ctx, kcp))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(kcp, cluster, ns)

	r := &K0sController{
		Client: testEnv,
	}

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: util.ObjectKey(kcp)})
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)
}

func TestReconcilePausedK0sControlPlane(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-reconcile-paused-k0scontrolplane")
	require.NoError(t, err)

	cluster, kcp, _ := createClusterWithControlPlane(ns.Name)
	require.NoError(t, testEnv.Create(ctx, cluster))

	// K0sControlPlane with 'paused' annotation.
	kcp.Annotations = map[string]string{"cluster.x-k8s.io/paused": "true"}

	require.NoError(t, testEnv.Create(ctx, kcp))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(kcp, cluster, ns)

	r := &K0sController{
		Client: testEnv,
	}

	result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: util.ObjectKey(kcp)})
	require.NoError(t, err)
	require.Equal(t, ctrl.Result{}, result)
}

func TestReconcileTunneling(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-reconcile-tunneling")
	require.NoError(t, err)

	node := createNode()
	require.NoError(t, testEnv.Create(ctx, node))

	cluster, kcp, _ := createClusterWithControlPlane(ns.Name)
	require.NoError(t, testEnv.Create(ctx, cluster))

	kcp.Spec.K0sConfigSpec = bootstrapv1.K0sConfigSpec{
		Tunneling: bootstrapv1.TunnelingSpec{
			Enabled: true,
		},
	}
	require.NoError(t, testEnv.Create(ctx, kcp))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(kcp, cluster, ns)

	clientSet, err := kubernetes.NewForConfig(testEnv.Config)
	require.NoError(t, err)

	r := &K0sController{
		Client:    testEnv,
		ClientSet: clientSet,
	}
	err = r.reconcileTunneling(ctx, cluster, kcp)
	require.NoError(t, err)

	frpToken, err := clientSet.CoreV1().Secrets(ns.Name).Get(ctx, fmt.Sprintf(FRPTokenNameTemplate, cluster.Name), metav1.GetOptions{})
	require.NoError(t, err)
	require.True(t, metav1.IsControlledBy(frpToken, kcp))

	frpCM, err := clientSet.CoreV1().ConfigMaps(ns.Name).Get(ctx, fmt.Sprintf(FRPConfigMapNameTemplate, kcp.GetName()), metav1.GetOptions{})
	require.NoError(t, err)
	require.True(t, metav1.IsControlledBy(frpCM, kcp))

	frpDeploy, err := clientSet.AppsV1().Deployments(ns.Name).Get(ctx, fmt.Sprintf(FRPDeploymentNameTemplate, kcp.GetName()), metav1.GetOptions{})
	require.NoError(t, err)
	require.True(t, metav1.IsControlledBy(frpDeploy, kcp))

	frpService, err := clientSet.CoreV1().Services(ns.Name).Get(ctx, fmt.Sprintf(FRPServiceNameTemplate, kcp.GetName()), metav1.GetOptions{})
	require.NoError(t, err)
	require.True(t, metav1.IsControlledBy(frpService, kcp))
}

func TestReconcileKubeconfigEmptyAPIEndpoints(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-reconcile-kubeconfig-empty-api-endpoints")
	require.NoError(t, err)

	cluster, kcp, _ := createClusterWithControlPlane(ns.Name)

	// Host and Port with zero values.
	cluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{}

	require.NoError(t, testEnv.Create(ctx, cluster))
	require.NoError(t, testEnv.Create(ctx, kcp))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(kcp, cluster, ns)

	r := &K0sController{
		Client: testEnv,
	}
	err = r.reconcileKubeconfig(ctx, cluster, kcp)
	require.Error(t, err)

	kubeconfigSecret := &corev1.Secret{}
	secretKey := client.ObjectKey{
		Namespace: cluster.Namespace,
		Name:      secret.Name(cluster.Name, secret.Kubeconfig),
	}
	require.ErrorContains(t, testEnv.GetAPIReader().Get(ctx, secretKey, kubeconfigSecret), "not found")
}

func TestReconcileKubeconfigMissingCACertificate(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-reconcile-kubeconfig-missing-ca-certificates")
	require.NoError(t, err)

	cluster, kcp, _ := createClusterWithControlPlane(ns.Name)
	require.NoError(t, testEnv.Create(ctx, cluster))
	require.NoError(t, testEnv.Create(ctx, kcp))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(kcp, cluster, ns)

	r := &K0sController{
		Client: testEnv,
	}

	err = r.reconcileKubeconfig(ctx, cluster, kcp)
	require.Error(t, err)

	kubeconfigSecret := &corev1.Secret{}
	secretKey := client.ObjectKey{
		Namespace: cluster.Namespace,
		Name:      secret.Name(cluster.Name, secret.Kubeconfig),
	}
	require.ErrorContains(t, testEnv.GetAPIReader().Get(ctx, secretKey, kubeconfigSecret), "not found")
}

func TestReconcileKubeconfigTunnelingModeNotEnabled(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-reconcile-kubeconfig-tunneling-mode-not-enabled")
	require.NoError(t, err)

	cluster, kcp, _ := createClusterWithControlPlane(ns.Name)
	require.NoError(t, testEnv.Create(ctx, cluster))

	// Tunneling not enabled.
	kcp.Spec.K0sConfigSpec.Tunneling.Enabled = false

	require.NoError(t, testEnv.Create(ctx, kcp))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(kcp, cluster, ns)

	kubeconfigSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secret.Name(cluster.Name, secret.Kubeconfig),
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				clusterv1.ClusterNameLabel: cluster.Name,
			},
			OwnerReferences: []metav1.OwnerReference{},
		},
		Data: map[string][]byte{
			secret.KubeconfigDataName: {},
		},
	}
	require.NoError(t, testEnv.Create(ctx, kubeconfigSecret))

	r := &K0sController{
		Client: testEnv,
	}

	err = r.reconcileKubeconfig(ctx, cluster, kcp)
	require.NoError(t, err)
}

func TestReconcileKubeconfigTunnelingModeProxy(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-reconcile-kubeconfig-tunneling-mode-proxy")
	require.NoError(t, err)

	cluster, kcp, _ := createClusterWithControlPlane(ns.Name)
	require.NoError(t, testEnv.Create(ctx, cluster))

	// Tunneling mode = 'proxy'
	kcp.Spec.K0sConfigSpec.Tunneling = bootstrapv1.TunnelingSpec{
		Enabled:           true,
		Mode:              "proxy",
		ServerAddress:     "test.com",
		TunnelingNodePort: 9999,
	}

	require.NoError(t, testEnv.Create(ctx, kcp))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(kcp, cluster, ns)

	kubeconfigSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secret.Name(cluster.Name, secret.Kubeconfig),
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				clusterv1.ClusterNameLabel: cluster.Name,
			},
			OwnerReferences: []metav1.OwnerReference{},
		},
		Data: map[string][]byte{
			secret.KubeconfigDataName: {},
		},
	}
	require.NoError(t, testEnv.Create(ctx, kubeconfigSecret))

	clusterCerts := secret.NewCertificatesForInitialControlPlane(&kubeadmConfig.ClusterConfiguration{})
	require.NoError(t, clusterCerts.Generate())
	caCert := clusterCerts.GetByPurpose(secret.ClusterCA)
	caCertSecret := caCert.AsSecret(
		client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name},
		*metav1.NewControllerRef(kcp, cpv1beta1.GroupVersion.WithKind("K0sControlPlane")),
	)
	require.NoError(t, testEnv.Create(ctx, caCertSecret))

	r := &K0sController{
		Client: testEnv,
	}

	err = r.reconcileKubeconfig(ctx, cluster, kcp)
	require.Error(t, err)

	secretKey := client.ObjectKey{
		Namespace: cluster.Namespace,
		Name:      secret.Name(cluster.Name+"-proxied", secret.Kubeconfig),
	}

	kubeconfigProxiedSecret := &corev1.Secret{}
	require.NoError(t, testEnv.Get(ctx, secretKey, kubeconfigProxiedSecret))

	kubeconfigProxiedSecretCrt, _ := runtime.Decode(clientcmdlatest.Codec, kubeconfigProxiedSecret.Data["value"])
	for _, v := range kubeconfigProxiedSecretCrt.(*api.Config).Clusters {
		require.Equal(t, "https://test.endpoint:6443", v.Server)
		require.Equal(t, "http://test.com:9999", v.ProxyURL)
	}
}

func TestReconcileKubeconfigTunnelingModeTunnel(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-reconcile-kubeconfig-tunneling-mode-tunnel")
	require.NoError(t, err)

	cluster, kcp, _ := createClusterWithControlPlane(ns.Name)
	require.NoError(t, testEnv.Create(ctx, cluster))

	// Tunneling mode = 'tunnel'
	kcp.Spec.K0sConfigSpec.Tunneling = bootstrapv1.TunnelingSpec{
		Enabled:           true,
		Mode:              "tunnel",
		ServerAddress:     "test.com",
		TunnelingNodePort: 9999,
	}

	require.NoError(t, testEnv.Create(ctx, kcp))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(kcp, cluster, ns)

	kubeconfigSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secret.Name(cluster.Name, secret.Kubeconfig),
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				clusterv1.ClusterNameLabel: cluster.Name,
			},
			OwnerReferences: []metav1.OwnerReference{},
		},
		Data: map[string][]byte{
			secret.KubeconfigDataName: {},
		},
	}
	require.NoError(t, testEnv.Create(ctx, kubeconfigSecret))

	clusterCerts := secret.NewCertificatesForInitialControlPlane(&kubeadmConfig.ClusterConfiguration{})
	require.NoError(t, clusterCerts.Generate())
	caCert := clusterCerts.GetByPurpose(secret.ClusterCA)
	caCertSecret := caCert.AsSecret(
		client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name},
		*metav1.NewControllerRef(kcp, cpv1beta1.GroupVersion.WithKind("K0sControlPlane")),
	)
	require.NoError(t, testEnv.Create(ctx, caCertSecret))

	r := &K0sController{
		Client: testEnv,
	}

	err = r.reconcileKubeconfig(ctx, cluster, kcp)
	require.Error(t, err)

	secretKey := client.ObjectKey{
		Namespace: cluster.Namespace,
		Name:      secret.Name(cluster.Name+"-tunneled", secret.Kubeconfig),
	}
	kubeconfigProxiedSecret := &corev1.Secret{}
	require.NoError(t, testEnv.Get(ctx, secretKey, kubeconfigProxiedSecret))

	kubeconfigProxiedSecretCrt, _ := runtime.Decode(clientcmdlatest.Codec, kubeconfigProxiedSecret.Data["value"])
	for _, v := range kubeconfigProxiedSecretCrt.(*api.Config).Clusters {
		require.Equal(t, "https://test.com:9999", v.Server)
	}
}

func TestReconcileKubeconfigCertsRotation(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-reconcile-config-k0sconfig-certs-rotation")
	require.NoError(t, err)

	cluster, kcp, _ := createClusterWithControlPlane(ns.Name)
	require.NoError(t, testEnv.Create(ctx, cluster))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(kcp, cluster, ns)

	type tunneligSpec struct {
		bootstrapv1.TunnelingSpec
		secretName string
	}
	tunnelingSpecs := []tunneligSpec{
		{
			TunnelingSpec: bootstrapv1.TunnelingSpec{
				Enabled:           true,
				Mode:              "proxy",
				ServerAddress:     "test.com",
				TunnelingNodePort: 9999,
			},
			secretName: secret.Name(cluster.Name+"-proxied", secret.Kubeconfig),
		},
		{
			TunnelingSpec: bootstrapv1.TunnelingSpec{
				Enabled:           true,
				Mode:              "tunnel",
				ServerAddress:     "test.com",
				TunnelingNodePort: 9999,
			},
			secretName: secret.Name(cluster.Name+"-tunneled", secret.Kubeconfig),
		},
	}

	for i := range tunnelingSpecs {
		kcp.Spec.K0sConfigSpec.Tunneling = tunnelingSpecs[i].TunnelingSpec

		outdatedKubeconfigData, err := generateKubeconfigRequiringRotation(cluster.Name)
		require.NoError(t, err)

		secretsWithCertsToRotate := []*corev1.Secret{}
		kubeconfigSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secret.Name(cluster.Name, secret.Kubeconfig),
				Namespace: cluster.Namespace,
				Labels: map[string]string{
					clusterv1.ClusterNameLabel: cluster.Name,
				},
				OwnerReferences: []metav1.OwnerReference{},
			},
			Data: map[string][]byte{
				secret.KubeconfigDataName: outdatedKubeconfigData,
			},
		}
		require.NoError(t, testEnv.Create(ctx, kubeconfigSecret))
		secretsWithCertsToRotate = append(secretsWithCertsToRotate, kubeconfigSecret)

		tunneledKubeconfigSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tunnelingSpecs[i].secretName,
				Namespace: cluster.Namespace,
				Labels: map[string]string{
					clusterv1.ClusterNameLabel: cluster.Name,
				},
				OwnerReferences: []metav1.OwnerReference{},
			},
			Data: map[string][]byte{
				secret.KubeconfigDataName: outdatedKubeconfigData,
			},
		}
		require.NoError(t, testEnv.Create(ctx, tunneledKubeconfigSecret))
		secretsWithCertsToRotate = append(secretsWithCertsToRotate, tunneledKubeconfigSecret)

		cc := secret.Certificates{
			&secret.Certificate{
				Purpose:  secret.ClusterCA,
				CertFile: "ca.crt",
				KeyFile:  "ca.key",
			},
		}
		require.NoError(t, cc.LookupOrGenerate(ctx, testEnv, capiutil.ObjectKey(cluster), *metav1.NewControllerRef(kcp, cpv1beta1.GroupVersion.WithKind("K0sControlPlane"))))

		r := &K0sController{
			Client: testEnv,
		}
		err = r.reconcileKubeconfig(ctx, cluster, kcp)
		require.NoError(t, err)

		for _, s := range secretsWithCertsToRotate {
			secretKey := client.ObjectKey{
				Namespace: s.Namespace,
				Name:      s.Name,
			}
			kubeconfigSecret := &corev1.Secret{}
			require.NoError(t, testEnv.Get(ctx, secretKey, kubeconfigSecret))

			needsRotation, err := kubeconfig.NeedsClientCertRotation(kubeconfigSecret, certs.ClientCertificateRenewalDuration)
			require.NoError(t, err)
			require.False(t, needsRotation)
		}

		require.NoError(t, testEnv.Delete(ctx, kubeconfigSecret))
		require.NoError(t, testEnv.Delete(ctx, tunneledKubeconfigSecret))
	}
}

func TestReconcileK0sConfigNotProvided(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-reconcile-config-k0sconfig-not-provided")
	require.NoError(t, err)

	cluster, kcp, _ := createClusterWithControlPlane(ns.Name)
	require.NoError(t, testEnv.Create(ctx, cluster))

	kcp.Spec.K0sConfigSpec.K0s = nil
	require.NoError(t, testEnv.Create(ctx, kcp))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(kcp, cluster, ns)

	r := &K0sController{
		Client: testEnv,
	}
	err = r.reconcileConfig(ctx, cluster, kcp)
	require.NoError(t, err)
	require.Nil(t, kcp.Spec.K0sConfigSpec.K0s)
}

func TestReconcileK0sConfigWithNLLBEnabled(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-reconcile-config-nllb-enabled")
	require.NoError(t, err)

	cluster, kcp, _ := createClusterWithControlPlane(ns.Name)
	require.NoError(t, testEnv.Create(ctx, cluster))

	// Enable '.spec.network.nodeLocalLoadBalancing'
	kcp.Spec.K0sConfigSpec = bootstrapv1.K0sConfigSpec{
		K0s: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "k0s.k0sproject.io/v1beta1",
				"kind":       "ClusterConfig",
				"spec": map[string]interface{}{
					"api": map[string]interface{}{
						"sans": []interface{}{
							"test.com",
						},
					},
					"network": map[string]interface{}{
						"nodeLocalLoadBalancing": map[string]interface{}{
							"enabled": true,
						},
					},
				},
			},
		},
	}

	require.NoError(t, testEnv.Create(ctx, kcp))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(kcp, cluster, ns)

	r := &K0sController{
		Client: testEnv,
	}
	err = r.reconcileConfig(ctx, cluster, kcp)
	require.NoError(t, err)

	expectedk0sConfig := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "k0s.k0sproject.io/v1beta1",
			"kind":       "ClusterConfig",
			"spec": map[string]interface{}{
				"api": map[string]interface{}{
					"sans": []interface{}{
						"test.endpoint",
						"test.com",
					},
				},
				"network": map[string]interface{}{
					"nodeLocalLoadBalancing": map[string]interface{}{
						"enabled": true,
					},
				},
			},
		},
	}
	require.Equal(t, expectedk0sConfig, kcp.Spec.K0sConfigSpec.K0s)
}

func TestReconcileK0sConfigWithNLLBDisabled(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-reconcile-config-nllb-disabled")
	require.NoError(t, err)

	cluster, kcp, _ := createClusterWithControlPlane(ns.Name)
	require.NoError(t, testEnv.Create(ctx, cluster))

	// Disable '.spec.network.nodeLocalLoadBalancing'
	kcp.Spec.K0sConfigSpec = bootstrapv1.K0sConfigSpec{
		K0s: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "k0s.k0sproject.io/v1beta1",
				"kind":       "ClusterConfig",
				"spec": map[string]interface{}{
					"api": map[string]interface{}{
						"sans": []interface{}{
							"test.com",
						},
					},
				},
			},
		},
	}

	require.NoError(t, testEnv.Create(ctx, kcp))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(kcp, cluster, ns)

	r := &K0sController{
		Client: testEnv,
	}
	err = r.reconcileConfig(ctx, cluster, kcp)
	require.NoError(t, err)

	expectedk0sConfig := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "k0s.k0sproject.io/v1beta1",
			"kind":       "ClusterConfig",
			"spec": map[string]interface{}{
				"api": map[string]interface{}{
					"sans": []interface{}{
						"test.com",
					},
					"externalAddress": "test.endpoint",
				},
			},
		},
	}
	require.Equal(t, expectedk0sConfig, kcp.Spec.K0sConfigSpec.K0s)
}

func TestReconcileK0sConfigTunnelingServerAddressToApiSans(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-reconcile-config-tunneling-serveraddress-to-api-sans")
	require.NoError(t, err)

	cluster, kcp, _ := createClusterWithControlPlane(ns.Name)
	require.NoError(t, testEnv.Create(ctx, cluster))

	// With '.spec.k0sConfigSpec.Tunneling.ServerAddress'
	kcp.Spec.K0sConfigSpec = bootstrapv1.K0sConfigSpec{
		K0s: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "k0s.k0sproject.io/v1beta1",
				"kind":       "ClusterConfig",
				"spec": map[string]interface{}{
					"api": map[string]interface{}{
						"sans": []interface{}{
							"test.com",
						},
					},
				},
			},
		},
		Tunneling: bootstrapv1.TunnelingSpec{
			ServerAddress: "my-tunneling-server-address.com",
		},
	}

	require.NoError(t, testEnv.Create(ctx, kcp))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(kcp, cluster, ns)

	r := &K0sController{
		Client: testEnv,
	}
	err = r.reconcileConfig(ctx, cluster, kcp)
	require.NoError(t, err)

	expectedk0sConfig := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "k0s.k0sproject.io/v1beta1",
			"kind":       "ClusterConfig",
			"spec": map[string]interface{}{
				"api": map[string]interface{}{
					"sans": []interface{}{
						"test.com",
						"my-tunneling-server-address.com",
					},
					"externalAddress": "test.endpoint",
				},
			},
		},
	}
	require.Equal(t, expectedk0sConfig, kcp.Spec.K0sConfigSpec.K0s)
}

func TestReconcileMachinesScaleUp(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-reconcile-machine-scale-up")
	require.NoError(t, err)

	cluster, kcp, gmt := createClusterWithControlPlane(ns.Name)
	require.NoError(t, testEnv.Create(ctx, cluster))
	require.NoError(t, testEnv.Create(ctx, gmt))

	desiredReplicas := 5
	kcp.Spec.Replicas = int32(desiredReplicas)
	require.NoError(t, testEnv.Create(ctx, kcp))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(kcp, gmt, cluster, ns)

	kcpOwnerRef := *metav1.NewControllerRef(kcp, cpv1beta1.GroupVersion.WithKind("K0sControlPlane"))

	r := &K0sController{
		Client: testEnv,
	}

	firstMachineRelatedToControlPlane := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", kcp.Name, 0),
			Namespace: ns.Name,
			Labels: map[string]string{
				clusterv1.ClusterNameLabel:             cluster.Name,
				clusterv1.MachineControlPlaneLabel:     "true",
				clusterv1.MachineControlPlaneNameLabel: kcp.GetName(),
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: cluster.Name,
			Version:     ptr.To("v1.30.0"),
			InfrastructureRef: v1.ObjectReference{
				Kind:       "GenericInfrastructureMachineTemplate",
				Namespace:  ns.Name,
				Name:       gmt.GetName(),
				APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
			},
		},
	}
	firstMachineRelatedToControlPlane.SetOwnerReferences([]metav1.OwnerReference{kcpOwnerRef})
	require.NoError(t, testEnv.Create(ctx, firstMachineRelatedToControlPlane))

	secondMachineRelatedToControlPlane := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", kcp.Name, 1),
			Namespace: ns.Name,
			Labels: map[string]string{
				clusterv1.ClusterNameLabel:             cluster.Name,
				clusterv1.MachineControlPlaneLabel:     "true",
				clusterv1.MachineControlPlaneNameLabel: kcp.GetName(),
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: cluster.Name,
			Version:     ptr.To("v1.30.0"),
			InfrastructureRef: v1.ObjectReference{
				Kind:       "GenericInfrastructureMachineTemplate",
				Namespace:  ns.Name,
				Name:       gmt.GetName(),
				APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
			},
		},
	}
	secondMachineRelatedToControlPlane.SetOwnerReferences([]metav1.OwnerReference{kcpOwnerRef})
	require.NoError(t, testEnv.Create(ctx, secondMachineRelatedToControlPlane))

	machineNotRelatedToControlPlane := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "external-machine",
			Namespace: ns.Name,
			Labels: map[string]string{
				clusterv1.ClusterNameLabel: cluster.Name,
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: cluster.Name,
			Version:     ptr.To("v1.30.0"),
		},
	}
	require.NoError(t, testEnv.Create(ctx, machineNotRelatedToControlPlane))

	require.Eventually(t, func() bool {
		return r.reconcileMachines(ctx, cluster, kcp) == nil
	}, 5*time.Second, 100*time.Millisecond)

	machines, err := collections.GetFilteredMachinesForCluster(ctx, testEnv, cluster, collections.ControlPlaneMachines(cluster.Name), collections.ActiveMachines)
	require.NoError(t, err)
	require.Len(t, machines, desiredReplicas)
	for _, m := range machines {
		expectedLabels := map[string]string{
			clusterv1.ClusterNameLabel:             cluster.GetName(),
			clusterv1.MachineControlPlaneLabel:     "true",
			clusterv1.MachineControlPlaneNameLabel: kcp.GetName(),
		}
		require.Equal(t, expectedLabels, m.Labels)
		require.True(t, metav1.IsControlledBy(m, kcp))
		require.Equal(t, kcp.Spec.Version, *m.Spec.Version)
	}
}

func TestReconcileMachinesScaleDown(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-reconcile-machines-scale-down")
	require.NoError(t, err)

	cluster, kcp, gmt := createClusterWithControlPlane(ns.Name)
	require.NoError(t, testEnv.Create(ctx, cluster))
	require.NoError(t, testEnv.Create(ctx, gmt))

	desiredReplicas := 1
	kcp.Spec.Replicas = int32(desiredReplicas)
	require.NoError(t, testEnv.Create(ctx, kcp))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(kcp, gmt, cluster, ns)

	kcpOwnerRef := *metav1.NewControllerRef(kcp, cpv1beta1.GroupVersion.WithKind("K0sControlPlane"))

	frt := &fakeRoundTripper{}
	fakeClient := &restfake.RESTClient{
		Client: restfake.CreateHTTPClient(frt.run),
	}

	restClient, _ := rest.RESTClientFor(&rest.Config{
		ContentConfig: rest.ContentConfig{
			NegotiatedSerializer: scheme.Codecs,
			GroupVersion:         &metav1.SchemeGroupVersion,
		},
	})
	restClient.Client = fakeClient.Client

	r := &K0sController{
		Client:                    testEnv,
		workloadClusterKubeClient: kubernetes.New(restClient),
	}

	firstMachineRelatedToControlPlane := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", kcp.Name, 0),
			Namespace: ns.Name,
			Labels: map[string]string{
				clusterv1.ClusterNameLabel:             cluster.Name,
				clusterv1.MachineControlPlaneLabel:     "true",
				clusterv1.MachineControlPlaneNameLabel: kcp.GetName(),
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: cluster.Name,
			Version:     ptr.To("v1.30.0"),
			InfrastructureRef: v1.ObjectReference{
				Kind:       "GenericInfrastructureMachineTemplate",
				Namespace:  ns.Name,
				Name:       gmt.GetName(),
				APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
			},
		},
	}
	firstMachineRelatedToControlPlane.SetOwnerReferences([]metav1.OwnerReference{kcpOwnerRef})
	require.NoError(t, testEnv.Create(ctx, firstMachineRelatedToControlPlane))
	firstControllerConfig := &bootstrapv1.K0sControllerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", kcp.Name, 0),
			Namespace: ns.Name,
			Labels:    controlPlaneCommonLabelsForCluster(kcp, cluster.Name),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "cluster.x-k8s.io/v1beta1",
				Kind:               "Machine",
				Name:               firstMachineRelatedToControlPlane.GetName(),
				UID:                firstMachineRelatedToControlPlane.GetUID(),
				BlockOwnerDeletion: ptr.To(true),
				Controller:         ptr.To(true),
			}},
		},
	}
	require.NoError(t, testEnv.Create(ctx, firstControllerConfig))

	secondMachineRelatedToControlPlane := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", kcp.Name, 1),
			Namespace: ns.Name,
			Labels: map[string]string{
				clusterv1.ClusterNameLabel:             cluster.Name,
				clusterv1.MachineControlPlaneLabel:     "true",
				clusterv1.MachineControlPlaneNameLabel: kcp.GetName(),
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: cluster.Name,
			Version:     ptr.To("v1.30.0"),
			InfrastructureRef: v1.ObjectReference{
				Kind:       "GenericInfrastructureMachineTemplate",
				Namespace:  ns.Name,
				Name:       gmt.GetName(),
				APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
			},
		},
	}
	secondMachineRelatedToControlPlane.SetOwnerReferences([]metav1.OwnerReference{kcpOwnerRef})
	require.NoError(t, testEnv.Create(ctx, secondMachineRelatedToControlPlane))
	secondControllerConfig := &bootstrapv1.K0sControllerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", kcp.Name, 1),
			Namespace: ns.Name,
			Labels:    controlPlaneCommonLabelsForCluster(kcp, cluster.Name),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "cluster.x-k8s.io/v1beta1",
				Kind:               "Machine",
				Name:               secondMachineRelatedToControlPlane.GetName(),
				UID:                secondMachineRelatedToControlPlane.GetUID(),
				BlockOwnerDeletion: ptr.To(true),
				Controller:         ptr.To(true),
			}},
		},
	}
	require.NoError(t, testEnv.Create(ctx, secondControllerConfig))

	thirdMachineRelatedToControlPlane := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", kcp.Name, 2),
			Namespace: ns.Name,
			Labels: map[string]string{
				clusterv1.ClusterNameLabel:             cluster.Name,
				clusterv1.MachineControlPlaneLabel:     "true",
				clusterv1.MachineControlPlaneNameLabel: kcp.GetName(),
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: cluster.Name,
			Version:     ptr.To("v1.30.0"),
			InfrastructureRef: v1.ObjectReference{
				Kind:       "GenericInfrastructureMachineTemplate",
				Namespace:  ns.Name,
				Name:       gmt.GetName(),
				APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
			},
		},
	}
	thirdMachineRelatedToControlPlane.SetOwnerReferences([]metav1.OwnerReference{kcpOwnerRef})
	require.NoError(t, testEnv.Create(ctx, thirdMachineRelatedToControlPlane))
	thirdControllerConfig := &bootstrapv1.K0sControllerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", kcp.Name, 2),
			Namespace: ns.Name,
			Labels:    controlPlaneCommonLabelsForCluster(kcp, cluster.Name),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "cluster.x-k8s.io/v1beta1",
				Kind:               "Machine",
				Name:               thirdMachineRelatedToControlPlane.GetName(),
				UID:                thirdMachineRelatedToControlPlane.GetUID(),
				BlockOwnerDeletion: ptr.To(true),
				Controller:         ptr.To(true),
			}},
		},
	}
	require.NoError(t, testEnv.Create(ctx, thirdControllerConfig))

	machineNotRelatedToControlPlane := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "external-machine",
			Namespace: ns.Name,
			Labels: map[string]string{
				clusterv1.ClusterNameLabel: cluster.Name,
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: cluster.Name,
			Version:     ptr.To("v1.30.0"),
		},
	}
	require.NoError(t, testEnv.Create(ctx, machineNotRelatedToControlPlane))
	notRelatedControllerConfig := &bootstrapv1.K0sControllerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "external-machine",
			Namespace: ns.Name,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "cluster.x-k8s.io/v1beta1",
				Kind:               "Machine",
				Name:               machineNotRelatedToControlPlane.GetName(),
				UID:                machineNotRelatedToControlPlane.GetUID(),
				BlockOwnerDeletion: ptr.To(true),
				Controller:         ptr.To(true),
			}},
		},
	}
	require.NoError(t, testEnv.Create(ctx, notRelatedControllerConfig))

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		err = r.reconcileMachines(ctx, cluster, kcp)
		assert.NoError(c, err)

		machines, err := collections.GetFilteredMachinesForCluster(ctx, testEnv, cluster, collections.ControlPlaneMachines(cluster.Name), collections.ActiveMachines)
		assert.NoError(c, err)
		assert.Len(c, machines, desiredReplicas)

		k0sBootstrapConfigList := &bootstrapv1.K0sControllerConfigList{}
		assert.NoError(c, testEnv.GetAPIReader().List(ctx, k0sBootstrapConfigList, client.InNamespace(cluster.Namespace), client.MatchingLabels{clusterv1.MachineControlPlaneLabel: "true"}))
		assert.Len(c, k0sBootstrapConfigList.Items, desiredReplicas)

		for _, m := range machines {
			expectedLabels := map[string]string{
				clusterv1.ClusterNameLabel:             cluster.GetName(),
				clusterv1.MachineControlPlaneLabel:     "true",
				clusterv1.MachineControlPlaneNameLabel: kcp.GetName(),
			}
			assert.Equal(c, expectedLabels, m.Labels)
			assert.True(c, metav1.IsControlledBy(m, kcp))
			assert.Equal(c, kcp.Spec.Version, *m.Spec.Version)

			// verify that the bootrap config related to the existing machines is present.
			bootstrapObjectKey := client.ObjectKey{
				Namespace: m.Namespace,
				Name:      m.Name,
			}
			kc := &bootstrapv1.K0sControllerConfig{}
			assert.NoError(c, testEnv.GetAPIReader().Get(ctx, bootstrapObjectKey, kc))
			assert.True(c, metav1.IsControlledBy(kc, m))

		}
	}, 10*time.Second, 100*time.Millisecond)
}

func TestReconcileMachinesSyncOldMachines(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-reconcile-machines-sync-old-machines")
	require.NoError(t, err)

	cluster, kcp, gmt := createClusterWithControlPlane(ns.Name)
	require.NoError(t, testEnv.Create(ctx, cluster))
	require.NoError(t, testEnv.Create(ctx, gmt))

	desiredReplicas := 3
	kcp.Spec.Replicas = int32(desiredReplicas)
	require.NoError(t, testEnv.Create(ctx, kcp))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(kcp, gmt, cluster, ns)

	kcpOwnerRef := *metav1.NewControllerRef(kcp, cpv1beta1.GroupVersion.WithKind("K0sControlPlane"))

	frt := &fakeRoundTripper{}
	fakeClient := &restfake.RESTClient{
		Client: restfake.CreateHTTPClient(frt.run),
	}

	restClient, _ := rest.RESTClientFor(&rest.Config{
		ContentConfig: rest.ContentConfig{
			NegotiatedSerializer: scheme.Codecs,
			GroupVersion:         &metav1.SchemeGroupVersion,
		},
	})
	restClient.Client = fakeClient.Client

	clientSet, err := kubernetes.NewForConfig(testEnv.Config)
	require.NoError(t, err)

	r := &K0sController{
		Client:                    testEnv,
		workloadClusterKubeClient: kubernetes.New(restClient),
		ClientSet:                 clientSet,
	}

	firstMachineRelatedToControlPlane := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", kcp.Name, 0),
			Namespace: ns.Name,
			Labels: map[string]string{
				clusterv1.ClusterNameLabel:             cluster.Name,
				clusterv1.MachineControlPlaneLabel:     "true",
				clusterv1.MachineControlPlaneNameLabel: kcp.GetName(),
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: cluster.Name,
			Version:     ptr.To("v1.29.0"),
			InfrastructureRef: v1.ObjectReference{
				Kind:       "GenericInfrastructureMachineTemplate",
				Namespace:  ns.Name,
				Name:       gmt.GetName(),
				APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
			},
		},
	}
	firstMachineRelatedToControlPlane.SetOwnerReferences([]metav1.OwnerReference{kcpOwnerRef})
	require.NoError(t, testEnv.Create(ctx, firstMachineRelatedToControlPlane))
	firstControllerConfig := &bootstrapv1.K0sControllerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", kcp.Name, 0),
			Namespace: ns.Name,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "cluster.x-k8s.io/v1beta1",
				Kind:               "Machine",
				Name:               firstMachineRelatedToControlPlane.GetName(),
				UID:                firstMachineRelatedToControlPlane.GetUID(),
				BlockOwnerDeletion: ptr.To(true),
				Controller:         ptr.To(true),
			}},
		},
	}
	require.NoError(t, testEnv.Create(ctx, firstControllerConfig))

	secondMachineRelatedToControlPlane := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", kcp.Name, 1),
			Namespace: ns.Name,
			Labels: map[string]string{
				clusterv1.ClusterNameLabel:             cluster.Name,
				clusterv1.MachineControlPlaneLabel:     "true",
				clusterv1.MachineControlPlaneNameLabel: kcp.GetName(),
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: cluster.Name,
			Version:     ptr.To("v1.30.0"),
			InfrastructureRef: v1.ObjectReference{
				Kind:       "GenericInfrastructureMachineTemplate",
				Namespace:  ns.Name,
				Name:       gmt.GetName(),
				APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
			},
		},
	}
	secondMachineRelatedToControlPlane.SetOwnerReferences([]metav1.OwnerReference{kcpOwnerRef})
	require.NoError(t, testEnv.Create(ctx, secondMachineRelatedToControlPlane))
	secondControllerConfig := &bootstrapv1.K0sControllerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", kcp.Name, 1),
			Namespace: ns.Name,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "cluster.x-k8s.io/v1beta1",
				Kind:               "Machine",
				Name:               secondMachineRelatedToControlPlane.GetName(),
				UID:                secondMachineRelatedToControlPlane.GetUID(),
				BlockOwnerDeletion: ptr.To(true),
				Controller:         ptr.To(true),
			}},
		},
	}
	require.NoError(t, testEnv.Create(ctx, secondControllerConfig))
	thirdMachineRelatedToControlPlane := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", kcp.Name, 2),
			Namespace: ns.Name,
			Labels: map[string]string{
				clusterv1.ClusterNameLabel:             cluster.Name,
				clusterv1.MachineControlPlaneLabel:     "true",
				clusterv1.MachineControlPlaneNameLabel: kcp.GetName(),
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName: cluster.Name,
			Version:     ptr.To("v1.29.0"),
			InfrastructureRef: v1.ObjectReference{
				Kind:       "GenericInfrastructureMachineTemplate",
				Namespace:  ns.Name,
				Name:       gmt.GetName(),
				APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
			},
		},
	}
	thirdMachineRelatedToControlPlane.SetOwnerReferences([]metav1.OwnerReference{kcpOwnerRef})
	require.NoError(t, testEnv.Create(ctx, thirdMachineRelatedToControlPlane))
	thirdControllerConfig := &bootstrapv1.K0sControllerConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", kcp.Name, 2),
			Namespace: ns.Name,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         "cluster.x-k8s.io/v1beta1",
				Kind:               "Machine",
				Name:               thirdMachineRelatedToControlPlane.GetName(),
				UID:                thirdMachineRelatedToControlPlane.GetUID(),
				BlockOwnerDeletion: ptr.To(true),
				Controller:         ptr.To(true),
			}},
		},
	}
	require.NoError(t, testEnv.Create(ctx, thirdControllerConfig))

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		err = r.reconcileMachines(ctx, cluster, kcp)
		assert.NoError(c, err)

		machines, err := collections.GetFilteredMachinesForCluster(ctx, testEnv, cluster, collections.ControlPlaneMachines(cluster.Name), collections.ActiveMachines)
		assert.NoError(c, err)
		assert.Len(c, machines, desiredReplicas)

		k0sBootstrapConfigList := &bootstrapv1.K0sControllerConfigList{}
		assert.NoError(c, testEnv.GetAPIReader().List(ctx, k0sBootstrapConfigList, client.InNamespace(cluster.Namespace)))
		assert.Len(c, k0sBootstrapConfigList.Items, desiredReplicas)

		for _, m := range machines {
			expectedLabels := map[string]string{
				clusterv1.ClusterNameLabel:             cluster.GetName(),
				clusterv1.MachineControlPlaneLabel:     "true",
				clusterv1.MachineControlPlaneNameLabel: kcp.GetName(),
			}
			assert.Equal(c, expectedLabels, m.Labels)
			assert.True(c, metav1.IsControlledBy(m, kcp))
			assert.Equal(c, kcp.Spec.Version, *m.Spec.Version)

			// verify that the bootrap config related to the existing machines is present.
			bootstrapObjectKey := client.ObjectKey{
				Namespace: m.Namespace,
				Name:      m.Name,
			}
			kc := &bootstrapv1.K0sControllerConfig{}
			assert.NoError(c, testEnv.GetAPIReader().Get(ctx, bootstrapObjectKey, kc))
			assert.True(c, metav1.IsControlledBy(kc, m))
		}
	}, 5*time.Second, 100*time.Millisecond)
}

func TestReconcileInitializeControlPlanes(t *testing.T) {
	ns, err := testEnv.CreateNamespace(ctx, "test-reconcile-initialize-controlplanes")
	require.NoError(t, err)

	cluster, kcp, gmt := createClusterWithControlPlane(ns.Name)
	require.NoError(t, testEnv.Create(ctx, cluster))
	kcp.Spec.Replicas = 1
	require.NoError(t, testEnv.Create(ctx, kcp))
	require.NoError(t, testEnv.Create(ctx, gmt))

	defer func(do ...client.Object) {
		require.NoError(t, testEnv.Cleanup(ctx, do...))
	}(kcp, gmt, cluster, ns)

	expectedLabels := map[string]string{clusterv1.ClusterNameLabel: cluster.Name}

	frt := &fakeRoundTripper{}
	fakeClient := &restfake.RESTClient{
		Client: restfake.CreateHTTPClient(frt.run),
	}

	restClient, _ := rest.RESTClientFor(&rest.Config{
		ContentConfig: rest.ContentConfig{
			NegotiatedSerializer: scheme.Codecs,
			GroupVersion:         &metav1.SchemeGroupVersion,
		},
	})
	restClient.Client = fakeClient.Client

	r := &K0sController{
		Client:                    testEnv,
		workloadClusterKubeClient: kubernetes.New(restClient),
	}

	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: util.ObjectKey(kcp)})
	require.NoError(t, err)
	require.NoError(t, testEnv.GetAPIReader().Get(ctx, client.ObjectKey{Name: kcp.Name, Namespace: kcp.Namespace}, kcp))
	require.NotEmpty(t, kcp.Status.Selector)
	require.Equal(t, fmt.Sprintf("%s+%s", kcp.Spec.Version, defaultK0sSuffix), kcp.Status.Version)
	require.Equal(t, kcp.Status.Replicas, int32(1))
	require.NoError(t, testEnv.GetAPIReader().Get(ctx, util.ObjectKey(gmt), gmt))
	require.Contains(t, gmt.GetOwnerReferences(), metav1.OwnerReference{
		APIVersion:         clusterv1.GroupVersion.String(),
		Kind:               "Cluster",
		Name:               cluster.Name,
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
		UID:                cluster.UID,
	})
	require.True(t, conditions.IsFalse(kcp, cpv1beta1.ControlPlaneReadyCondition))

	// Expected secrets are created
	caSecret, err := secret.GetFromNamespacedName(ctx, testEnv, client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}, secret.ClusterCA)
	require.NoError(t, err)
	require.NotNil(t, caSecret)
	require.NotEmpty(t, caSecret.Data)
	require.Equal(t, expectedLabels, caSecret.Labels)

	etcdSecret, err := secret.GetFromNamespacedName(ctx, testEnv, client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}, secret.EtcdCA)
	require.NoError(t, err)
	require.NotNil(t, etcdSecret)
	require.NotEmpty(t, etcdSecret.Data)
	require.Equal(t, expectedLabels, etcdSecret.Labels)

	kubeconfigSecret, err := secret.GetFromNamespacedName(ctx, testEnv, client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}, secret.Kubeconfig)
	require.NoError(t, err)
	require.NotNil(t, kubeconfigSecret)
	require.NotEmpty(t, kubeconfigSecret.Data)
	require.Equal(t, expectedLabels, kubeconfigSecret.Labels)
	k, err := kubeconfig.FromSecret(ctx, testEnv, util.ObjectKey(cluster))
	require.NoError(t, err)
	require.NotEmpty(t, k)

	proxySecret, err := secret.GetFromNamespacedName(ctx, testEnv, client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}, secret.FrontProxyCA)
	require.NoError(t, err)
	require.NotNil(t, proxySecret)
	require.NotEmpty(t, proxySecret.Data)
	require.Equal(t, expectedLabels, proxySecret.Labels)

	saSecret, err := secret.GetFromNamespacedName(ctx, testEnv, client.ObjectKey{Namespace: cluster.Namespace, Name: cluster.Name}, secret.ServiceAccount)
	require.NoError(t, err)
	require.NotNil(t, saSecret)
	require.NotEmpty(t, saSecret.Data)
	require.Equal(t, expectedLabels, saSecret.Labels)

	machineList := &clusterv1.MachineList{}
	require.NoError(t, testEnv.GetAPIReader().List(ctx, machineList, client.InNamespace(cluster.Namespace)))
	require.Len(t, machineList.Items, 1)
	machine := machineList.Items[0]
	require.True(t, strings.HasPrefix(machine.Name, kcp.Name))
	require.Equal(t, fmt.Sprintf("%s+%s", kcp.Spec.Version, defaultK0sSuffix), *machine.Spec.Version)
	// Newly cloned infra objects should have the infraref annotation.
	infraObj, err := external.Get(ctx, r.Client, &machine.Spec.InfrastructureRef, machine.Spec.InfrastructureRef.Namespace)
	require.NoError(t, err)
	require.Equal(t, gmt.GetName(), infraObj.GetAnnotations()[clusterv1.TemplateClonedFromNameAnnotation])
	require.Equal(t, gmt.GroupVersionKind().GroupKind().String(), infraObj.GetAnnotations()[clusterv1.TemplateClonedFromGroupKindAnnotation])

	k0sBootstrapConfigList := &bootstrapv1.K0sControllerConfigList{}
	require.NoError(t, testEnv.GetAPIReader().List(ctx, k0sBootstrapConfigList, client.InNamespace(cluster.Namespace)))
	require.Len(t, k0sBootstrapConfigList.Items, 1)

	require.True(t, metav1.IsControlledBy(&k0sBootstrapConfigList.Items[0], &machine))
}

func generateKubeconfigRequiringRotation(clusterName string) ([]byte, error) {
	caKey, err := certs.NewPrivateKey()
	if err != nil {
		return nil, err
	}

	cfg := certs.Config{
		CommonName: "kubernetes",
	}

	now := time.Now().UTC()

	tmpl := x509.Certificate{
		SerialNumber: new(big.Int).SetInt64(0),
		Subject: pkix.Name{
			CommonName:   cfg.CommonName,
			Organization: cfg.Organization,
		},
		NotBefore:             now.Add(-730 * 24 * time.Hour), // 2 year ago
		NotAfter:              now.Add(-365 * 24 * time.Hour), // 1 year ago
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		MaxPathLenZero:        true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		IsCA:                  true,
	}

	b, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, caKey.Public(), caKey)
	if err != nil {
		return nil, err
	}

	caCert, err := x509.ParseCertificate(b)
	if err != nil {
		return nil, err
	}

	userName := "foo"
	contextName := "foo"
	config := &api.Config{
		Clusters: map[string]*api.Cluster{
			clusterName: {
				Server:                   "https://127:0.0.1:4003",
				CertificateAuthorityData: certs.EncodeCertPEM(caCert),
			},
		},
		Contexts: map[string]*api.Context{
			contextName: {
				Cluster:  clusterName,
				AuthInfo: userName,
			},
		},
		AuthInfos: map[string]*api.AuthInfo{
			userName: {
				ClientKeyData:         certs.EncodePrivateKeyPEM(caKey),
				ClientCertificateData: certs.EncodeCertPEM(caCert),
			},
		},
		CurrentContext: contextName,
	}

	return clientcmd.Write(*config)
}

type fakeRoundTripper struct {
	plan *autopilot.Plan
}

func (f *fakeRoundTripper) run(req *http.Request) (*http.Response, error) {
	header := http.Header{}
	header.Set("Content-Type", runtime.ContentTypeJSON)

	switch req.Method {
	case "GET":
		if strings.HasPrefix(req.URL.Path, "/apis/autopilot.k0sproject.io/v1beta2/controlnodes/") {
			res, err := json.Marshal(autopilot.ControlNode{})
			if err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(bytes.NewReader(res))}, nil

		}
		if strings.HasPrefix(req.URL.Path, "/apis/autopilot.k0sproject.io/v1beta2/plans/autopilot") {
			if f.plan != nil {
				res, err := yaml.Marshal(f.plan)
				if err != nil {
					return nil, err
				}
				return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(bytes.NewReader(res))}, nil
			}

			return &http.Response{StatusCode: http.StatusNotFound, Header: header, Body: nil}, nil
		}
	case "DELETE":
		if strings.HasPrefix(req.URL.Path, "/apis/autopilot.k0sproject.io/v1beta2/controlnodes/") {
			return &http.Response{StatusCode: http.StatusOK, Header: header, Body: nil}, nil
		}
	case "PATCH":
		switch {
		case strings.HasPrefix(req.URL.Path, "/apis/etcd.k0sproject.io/v1beta1/etcdmembers/"):
			{
				return &http.Response{StatusCode: http.StatusOK, Header: header, Body: nil}, nil
			}
		case strings.HasPrefix(req.URL.Path, "/apis/autopilot.k0sproject.io/v1beta2/controlnodes/"):
			{
				return &http.Response{StatusCode: http.StatusOK, Header: header, Body: nil}, nil
			}
		case strings.HasPrefix(req.URL.Path, "/apis/infrastructure.cluster.x-k8s.io/v1beta1/namespaces/"):
			{
				return &http.Response{StatusCode: http.StatusOK, Header: header, Body: nil}, nil
			}
		}
	}

	return &http.Response{StatusCode: http.StatusNotFound, Header: header, Body: nil}, nil
}

func newCluster(namespacedName *types.NamespacedName) *clusterv1.Cluster {
	return &clusterv1.Cluster{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Cluster",
			APIVersion: clusterv1.GroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespacedName.Namespace,
			Name:      namespacedName.Name,
		},
	}
}

func createNode() *v1.Node {
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "foo",
			Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""},
		},
		Status: v1.NodeStatus{
			Addresses: []v1.NodeAddress{
				{
					Type:    v1.NodeExternalIP,
					Address: "1.1.1.1",
				},
			},
		},
	}
}

func createClusterWithControlPlane(namespace string) (*clusterv1.Cluster, *cpv1beta1.K0sControlPlane, *unstructured.Unstructured) {
	kcpName := fmt.Sprintf("kcp-foo-%s", util.RandomString(6))

	cluster := newCluster(&types.NamespacedName{Name: kcpName, Namespace: namespace})
	cluster.Spec = clusterv1.ClusterSpec{
		ControlPlaneRef: &v1.ObjectReference{
			Kind:       "K0sControlPlane",
			Namespace:  namespace,
			Name:       kcpName,
			APIVersion: cpv1beta1.GroupVersion.String(),
		},
		ControlPlaneEndpoint: clusterv1.APIEndpoint{
			Host: "test.endpoint",
			Port: 6443,
		},
	}

	kcp := &cpv1beta1.K0sControlPlane{
		TypeMeta: metav1.TypeMeta{
			APIVersion: cpv1beta1.GroupVersion.String(),
			Kind:       "K0sControlPlane",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      kcpName,
			Namespace: namespace,
			UID:       "1",
			OwnerReferences: []metav1.OwnerReference{
				{
					Kind:       "Cluster",
					APIVersion: clusterv1.GroupVersion.String(),
					Name:       kcpName,
					UID:        "1",
				},
			},
		},
		Spec: v1beta1.K0sControlPlaneSpec{
			MachineTemplate: &v1beta1.K0sControlPlaneMachineTemplate{
				InfrastructureRef: v1.ObjectReference{
					Kind:       "GenericInfrastructureMachineTemplate",
					Namespace:  namespace,
					Name:       "infra-foo",
					APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
				},
			},
			UpdateStrategy: cpv1beta1.UpdateRecreate,
			Replicas:       int32(1),
			Version:        "v1.30.0",
		},
	}

	genericMachineTemplate := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"kind":       "GenericInfrastructureMachineTemplate",
			"apiVersion": "infrastructure.cluster.x-k8s.io/v1beta1",
			"metadata": map[string]interface{}{
				"name":      "infra-foo",
				"namespace": namespace,
				"annotations": map[string]interface{}{
					clusterv1.TemplateClonedFromNameAnnotation:      kcp.Spec.MachineTemplate.InfrastructureRef.Name,
					clusterv1.TemplateClonedFromGroupKindAnnotation: kcp.Spec.MachineTemplate.InfrastructureRef.GroupVersionKind().GroupKind().String(),
				},
			},
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"hello": "world",
					},
				},
			},
		},
	}
	return cluster, kcp, genericMachineTemplate
}
