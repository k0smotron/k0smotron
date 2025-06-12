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
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/k0sproject/k0smotron/e2e/mothership"
	e2eutil "github.com/k0sproject/k0smotron/e2e/util"
	"github.com/k0sproject/k0smotron/internal/util"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/test/framework"
	capiframework "sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/test/framework/bootstrap"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestK0smotronUpgrade(t *testing.T) {
	setupAndRun(t, k0smotronUpgradeSpec)
}

// k0smotronMinorVersionsToCheckUpgrades contains the list of k0smotron minor versions to check for upgrades.
// The first version in the list is the one used to create the management cluster. Consequently, the next versions
// in the list are the ones used to upgrade the management cluster, ending with the development version from source.
//
// Important: This version MUST match the ones set in the e2e config file because that guarantees that the clusterctl
// local repository contains the required versions of the providers when running the upgrade tests. We should test
// development version against the latests stable version of k0smotron.
var k0smotronMinorVersionsToCheckUpgrades = []string{"1.4", "1.5"}

func k0smotronUpgradeSpec(t *testing.T) {

	testName := "k0smotron-upgrade"

	initialK0smotronMinorVersion := k0smotronMinorVersionsToCheckUpgrades[0]

	latestK0smotronStableMinor, _ := getStableReleaseOfMinor(context.Background(), initialK0smotronMinorVersion)
	k0smotronVersion := []string{fmt.Sprintf("k0sproject-k0smotron:v%s", latestK0smotronStableMinor)}

	managementClusterName := fmt.Sprintf("%s-management-%s", testName, util.RandomString(6))
	managementClusterLogFolder := filepath.Join(artifactFolder, "clusters", managementClusterName)

	managementClusterProvider := bootstrap.CreateKindBootstrapClusterAndLoadImages(ctx, bootstrap.CreateKindBootstrapClusterAndLoadImagesInput{
		Name:               managementClusterName,
		KubernetesVersion:  e2eConfig.GetVariable(KubernetesVersionManagement),
		RequiresDockerSock: e2eConfig.HasDockerProvider(),
		Images:             e2eConfig.Images,
		IPFamily:           e2eConfig.GetVariable(IPFamily),
		LogFolder:          filepath.Join(managementClusterLogFolder, "logs-kind"),
	})
	require.NotNil(t, managementClusterProvider, "Failed to create cluster to upgrade")

	kubeconfigPath := managementClusterProvider.GetKubeconfigPath()
	require.FileExists(t, kubeconfigPath, "Failed to get the kubeconfig file for the cluster to upgrade")

	scheme, err := initScheme()
	require.NoError(t, err, "Failed to init scheme")
	managementClusterProxy := framework.NewClusterProxy(managementClusterName, kubeconfigPath, scheme)
	require.NotNil(t, managementClusterProxy, "Failed to create a cluster proxy for the cluster to upgrade")

	fmt.Println("Turning the new cluster into a management cluster with older versions of providers")

	err = mothership.InitAndWatchControllerLogs(watchesCtx, clusterctl.InitManagementClusterAndWatchControllerLogsInput{
		ClusterProxy:             managementClusterProxy,
		ClusterctlConfigPath:     clusterctlConfigPath,
		InfrastructureProviders:  e2eConfig.InfrastructureProviders(),
		DisableMetricsCollection: true,
		BootstrapProviders:       k0smotronVersion,
		ControlPlaneProviders:    k0smotronVersion,
		LogFolder:                managementClusterLogFolder,
	}, e2eutil.GetInterval(e2eConfig, "bootstrap", "wait-deployment-available"))
	require.NoError(t, err, "Failed to init management cluster")

	fmt.Println("THE MANAGEMENT CLUSTER WITH THE OLDER VERSION OF K0SMOTRON PROVIDERS IS UP&RUNNING!")

	fmt.Println(fmt.Sprintf("Creating a namespace for hosting the %s test workload cluster", testName))

	testNamespace, testCancelWatches := framework.CreateNamespaceAndWatchEvents(ctx, framework.CreateNamespaceAndWatchEventsInput{
		Creator:   managementClusterProxy.GetClient(),
		ClientSet: managementClusterProxy.GetClientSet(),
		Name:      testName,
		LogFolder: filepath.Join(artifactFolder, "clusters", "bootstrap"),
	})

	fmt.Println("Creating a test workload cluster")

	workloadClusterName := fmt.Sprintf("%s-workload-%s", testName, util.RandomString(6))
	workloadClusterNamespace := testNamespace.Name

	fmt.Println("Getting the cluster template yaml")
	workloadClusterTemplate := clusterctl.ConfigCluster(ctx, clusterctl.ConfigClusterInput{
		ClusterctlConfigPath: clusterctlConfigPath,
		KubeconfigPath:       managementClusterProxy.GetKubeconfigPath(),
		// select cluster templates
		Flavor: "out-of-cluster",

		Namespace:                workloadClusterNamespace,
		ClusterName:              workloadClusterName,
		KubernetesVersion:        e2eConfig.GetVariable(KubernetesVersion),
		ControlPlaneMachineCount: ptr.To[int64](1),
		// TODO: make infra provider configurable
		InfrastructureProvider: "docker",
		LogFolder:              filepath.Join(artifactFolder, "clusters", managementClusterProxy.GetName()),
		ClusterctlVariables: map[string]string{
			"CLUSTER_NAME":    workloadClusterName,
			"NAMESPACE":       workloadClusterNamespace,
			"UPDATE_STRATEGY": "InPlace",
		},
	})
	require.NotNil(t, workloadClusterTemplate)

	require.Eventually(t, func() bool {
		return managementClusterProxy.CreateOrUpdate(ctx, workloadClusterTemplate) == nil
	}, 10*time.Second, 1*time.Second, "Failed to apply the cluster template")

	cluster, err := e2eutil.DiscoveryAndWaitForCluster(ctx, capiframework.DiscoveryAndWaitForClusterInput{
		Getter:    managementClusterProxy.GetClient(),
		Namespace: workloadClusterNamespace,
		Name:      workloadClusterName,
	}, e2eutil.GetInterval(e2eConfig, testName, "wait-cluster"))
	require.NoError(t, err)

	defer func() {
		e2eutil.DumpSpecResourcesAndCleanup(
			ctx,
			testName,
			managementClusterProxy,
			artifactFolder,
			testNamespace,
			cancelWatches,
			cluster,
			e2eutil.GetInterval(e2eConfig, testName, "wait-delete-cluster"),
			skipCleanup,
		)

		testCancelWatches()

		if !skipCleanup {
			managementClusterProxy.Dispose(ctx)
			managementClusterProvider.Dispose(ctx)
		}
	}()

	controlPlane, err := e2eutil.DiscoveryAndWaitForControlPlaneInitialized(ctx, capiframework.DiscoveryAndWaitForControlPlaneInitializedInput{
		Lister:  managementClusterProxy.GetClient(),
		Cluster: cluster,
	}, e2eutil.GetInterval(e2eConfig, testName, "wait-controllers"))
	require.NoError(t, err)
	err = e2eutil.WaitForControlPlaneToBeReady(ctx, managementClusterProxy.GetClient(), controlPlane, e2eutil.GetInterval(e2eConfig, testName, "wait-kube-proxy-upgrade"))
	require.NoError(t, err)

	for _, minor := range k0smotronMinorVersionsToCheckUpgrades[1:] {

		latestK0smotronStableMinor, _ := getStableReleaseOfMinor(context.Background(), minor)
		k0smotronVersion := []string{fmt.Sprintf("k0sproject-k0smotron:v%s", latestK0smotronStableMinor)}

		fmt.Println(fmt.Sprintf("Upgrading the management cluster to k0smotron %s", latestK0smotronStableMinor))

		mothership.UpgradeManagementClusterAndWait(ctx, clusterctl.UpgradeManagementClusterAndWaitInput{
			ClusterctlConfigPath:  clusterctlConfigPath,
			ClusterProxy:          managementClusterProxy,
			BootstrapProviders:    k0smotronVersion,
			ControlPlaneProviders: k0smotronVersion,
			LogFolder:             managementClusterLogFolder,
		}, e2eutil.GetInterval(e2eConfig, "bootstrap", "wait-deployment-available"))

		controlPlane, err := e2eutil.DiscoveryAndWaitForControlPlaneInitialized(ctx, capiframework.DiscoveryAndWaitForControlPlaneInitializedInput{
			Lister:  managementClusterProxy.GetClient(),
			Cluster: cluster,
		}, e2eutil.GetInterval(e2eConfig, testName, "wait-controllers"))
		require.NoError(t, err)
		err = e2eutil.WaitForControlPlaneToBeReady(ctx, managementClusterProxy.GetClient(), controlPlane, e2eutil.GetInterval(e2eConfig, testName, "wait-kube-proxy-upgrade"))
		require.NoError(t, err)

		fmt.Println(fmt.Sprintf("THE MANAGEMENT CLUSTER WITH '%s' VERSION OF K0SMOTRON PROVIDERS WORKS!", latestK0smotronStableMinor))
	}

	// Get the workloadCluster before the management cluster is upgraded with development version to make sure that the upgrade did not trigger
	// any unexpected rollouts.
	preUpgradeMachineList := &clusterv1.MachineList{}
	err = managementClusterProxy.GetClient().List(
		ctx,
		preUpgradeMachineList,
		client.InNamespace(workloadClusterNamespace),
		client.MatchingLabels{clusterv1.ClusterNameLabel: workloadClusterName},
	)
	require.NoError(t, err)

	fmt.Println("Upgrading the management cluster to development version of k0smotron")
	// We apply development version of the providers to the management cluster.
	mothership.UpgradeManagementClusterAndWait(ctx, clusterctl.UpgradeManagementClusterAndWaitInput{
		ClusterctlConfigPath: clusterctlConfigPath,
		ClusterProxy:         managementClusterProxy,
		// TODO: make contract configurable
		Contract:  clusterv1.GroupVersion.Version,
		LogFolder: managementClusterLogFolder,
	}, e2eutil.GetInterval(e2eConfig, "bootstrap", "wait-deployment-available"))

	// Wait a few minutes for any unexpected change in the development version to be applied in the workload cluster.
	time.Sleep(3 * time.Minute)

	controlPlane, err = e2eutil.DiscoveryAndWaitForControlPlaneInitialized(ctx, capiframework.DiscoveryAndWaitForControlPlaneInitializedInput{
		Lister:  managementClusterProxy.GetClient(),
		Cluster: cluster,
	}, e2eutil.GetInterval(e2eConfig, testName, "wait-controllers"))
	require.NoError(t, err)
	err = e2eutil.WaitForControlPlaneToBeReady(ctx, managementClusterProxy.GetClient(), controlPlane, e2eutil.GetInterval(e2eConfig, testName, "wait-kube-proxy-upgrade"))
	require.NoError(t, err)

	postUpgradeMachineList := &clusterv1.MachineList{}
	err = managementClusterProxy.GetClient().List(
		ctx,
		postUpgradeMachineList,
		client.InNamespace(workloadClusterNamespace),
		client.MatchingLabels{clusterv1.ClusterNameLabel: workloadClusterName},
	)
	require.NoError(t, err)

	require.True(t, validateMachineRollout(preUpgradeMachineList, postUpgradeMachineList), "The machines in the workload cluster have been rolled out unexpectedly")

	fmt.Println("UPGRADE TO DEVELOPMENT VERSION OF K0SMOTRON WORKED AS EXPECTED!")
}

// validateMachineRollout checks if the machines in the workload cluster have been rolled out correctly.
// It compares the pre-upgrade and post-upgrade machine lists to ensure that the number of machines is the same
// and that the machines have not been rolled out unexpectedly.
//
// TODO: Add more checks to ensure that the machines have not been rolled out unexpectedly like compare name. Actually, we
// don't care about the machine names because there are pending issues to solve to change the machine naming strategy.
// https://github.com/k0sproject/k0smotron/issues/1022.
func validateMachineRollout(preMachineList, postMachineList *clusterv1.MachineList) bool {
	if preMachineList == nil && postMachineList == nil {
		return true
	}
	if preMachineList == nil || postMachineList == nil {
		return false
	}

	if len(preMachineList.Items) != len(postMachineList.Items) {
		return false
	}

	return true
}

func getStableReleaseOfMinor(ctx context.Context, minorRelease string) (string, error) {
	releaseMarker := fmt.Sprintf("go://github.com/k0sproject/k0smotron@v%s", minorRelease)
	return clusterctl.ResolveRelease(ctx, releaseMarker)
}
