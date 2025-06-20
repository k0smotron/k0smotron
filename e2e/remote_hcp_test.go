//go:build e2e

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

package e2e

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/k0sproject/k0smotron/e2e/util"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/cluster-api/test/framework"
	capiframework "sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/test/framework/bootstrap"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	capiutil "sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/kind/pkg/cluster"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
)

const (
	hostingClusterName = "hosting-cluster"
)

var (
	hostingClusterProxy capiframework.ClusterProxy
)

func TestRemoteHostedControlPlanes(t *testing.T) {
	deployHostingCluster()
	setupAndRun(t, remoteHCPSpec)
}

func remoteHCPSpec(t *testing.T) {
	testName := "remote-hcp"

	encodedHostingClusterKubeconfig, err := getEncodedHostingClusterKubeconfig()
	require.NoError(t, err)

	// Setup a Namespace where to host objects for this spec and create a watcher for the namespace events.
	namespace, _ := util.SetupSpecNamespace(ctx, testName, managementClusterProxy, artifactFolder)
	// Create same namespace in hosting cluster
	nsForHostingCluster := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace.GetName(),
		},
	}
	err = hostingClusterProxy.GetClient().Create(ctx, nsForHostingCluster)
	require.NoError(t, err)

	clusterName := fmt.Sprintf("%s-%s", testName, capiutil.RandomString(6))

	workloadClusterTemplate := clusterctl.ConfigCluster(ctx, clusterctl.ConfigClusterInput{
		ClusterctlConfigPath: clusterctlConfigPath,
		KubeconfigPath:       managementClusterProxy.GetKubeconfigPath(),
		// select cluster templates
		Flavor: "remote-hcp",

		Namespace:                namespace.Name,
		ClusterName:              clusterName,
		KubernetesVersion:        e2eConfig.GetVariable(KubernetesVersion),
		ControlPlaneMachineCount: ptr.To[int64](3),
		// TODO: make infra provider configurable
		InfrastructureProvider: "docker",
		LogFolder:              filepath.Join(artifactFolder, "clusters", managementClusterProxy.GetName()),
		ClusterctlVariables: map[string]string{
			"CLUSTER_NAME":               clusterName,
			"NAMESPACE":                  namespace.Name,
			"HOSTING_CLUSTER_KUBECONFIG": encodedHostingClusterKubeconfig,
		},
	})
	require.NotNil(t, workloadClusterTemplate)

	require.Eventually(t, func() bool {
		return managementClusterProxy.CreateOrUpdate(ctx, workloadClusterTemplate) == nil
	}, 10*time.Second, 1*time.Second, "Failed to apply the cluster template")

	cluster, err := util.DiscoveryAndWaitForCluster(ctx, capiframework.DiscoveryAndWaitForClusterInput{
		Getter:    managementClusterProxy.GetClient(),
		Namespace: namespace.Name,
		Name:      clusterName,
	}, util.GetInterval(e2eConfig, testName, "wait-cluster"))
	require.NoError(t, err)

	defer func() {
		util.DumpSpecResourcesAndCleanup(
			ctx,
			testName,
			managementClusterProxy,
			artifactFolder,
			namespace,
			cancelWatches,
			cluster,
			util.GetInterval(e2eConfig, testName, "wait-delete-cluster"),
			skipCleanup,
		)
	}()

	_, err = util.DiscoveryAndWaitForHCPToBeReady(ctx, util.DiscoveryAndWaitForHCPReadyInput{
		Lister:  managementClusterProxy.GetClient(),
		Cluster: cluster,
		Getter:  managementClusterProxy.GetClient(),
	}, util.GetInterval(e2eConfig, testName, "wait-controllers"))
	require.NoError(t, err)
}

// deployHostingCluster deploys a cluster for hosting control planes. Currently the cluster is based on Kind. An alternative would be to create a workload cluster and convert it into the hosting cluster.
// In that proposed approach, the infra of the hosting cluster is configurable.
func deployHostingCluster() {
	hostingClusterProvider := bootstrap.CreateKindBootstrapClusterAndLoadImages(ctx, bootstrap.CreateKindBootstrapClusterAndLoadImagesInput{
		Name:               hostingClusterName,
		RequiresDockerSock: true,
		IPFamily:           "IPv4",
		LogFolder:          filepath.Join(artifactFolder, "kind"),
	})
	if hostingClusterProvider == nil {
		panic("failed to create a management cluster")
	}

	hostingClusterProxy = capiframework.NewClusterProxy("bootstrap", hostingClusterProvider.GetKubeconfigPath(), getHostingClusterDefaultScheme(), framework.WithMachineLogCollector(framework.DockerLogCollector{}))
	if hostingClusterProxy == nil {
		panic("failed to get a management cluster proxy")
	}
}

func getEncodedHostingClusterKubeconfig() (string, error) {
	kubeconfig, err := cluster.NewProvider().KubeConfig(hostingClusterName, true)
	if err != nil {
		return "", nil
	}

	return base64.StdEncoding.EncodeToString([]byte(kubeconfig)), nil
}

func getHostingClusterDefaultScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	corev1.AddToScheme(s)
	appsv1.AddToScheme(s)
	rbacv1.AddToScheme(s)
	return s
}
