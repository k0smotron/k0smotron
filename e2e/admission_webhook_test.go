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
	"fmt"
	"github.com/k0smotron/k0smotron/e2e/util"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
	"path/filepath"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	capiutil "sigs.k8s.io/cluster-api/util"
	"testing"
)

func TestAdmissionWebhookT1(t *testing.T) {
	setupAndRun(t, admissionWebhookSpecT1)
}

func TestAdmissionWebhookT2(t *testing.T) {
	setupAndRun(t, admissionWebhookSpecT2)
}

func admissionWebhookSpecT1(t *testing.T) {
	testName := "admission-webhook-t1"

	// Setup a Namespace where to host objects for this spec and create a watcher for the namespace events.
	namespace, _ := util.SetupSpecNamespace(ctx, testName, managementClusterProxy, artifactFolder)

	clusterName := fmt.Sprintf("%s-%s", testName, capiutil.RandomString(6))

	workloadClusterTemplate := clusterctl.ConfigCluster(ctx, clusterctl.ConfigClusterInput{
		ClusterctlConfigPath: clusterctlConfigPath,
		KubeconfigPath:       managementClusterProxy.GetKubeconfigPath(),
		// select cluster templates
		Flavor: "admission-webhook-t1",

		Namespace:                namespace.Name,
		ClusterName:              clusterName,
		KubernetesVersion:        e2eConfig.GetVariable(KubernetesVersion),
		ControlPlaneMachineCount: ptr.To[int64](3),
		// TODO: make infra provider configurable
		InfrastructureProvider: "docker",
		LogFolder:              filepath.Join(artifactFolder, "clusters", managementClusterProxy.GetName()),
		ClusterctlVariables: map[string]string{
			"CLUSTER_NAME":    clusterName,
			"NAMESPACE":       namespace.Name,
			"UPDATE_STRATEGY": "Recreate",
		},
	})
	require.NotNil(t, workloadClusterTemplate)

	err := managementClusterProxy.CreateOrUpdate(ctx, workloadClusterTemplate)
	require.Error(t, err)
	require.Contains(t, err.Error(), "UpdateStrategy Recreate strategy is not allowed when the cluster is running in single mode")
}

func admissionWebhookSpecT2(t *testing.T) {
	testName := "admission-webhook-t2"

	// Setup a Namespace where to host objects for this spec and create a watcher for the namespace events.
	namespace, _ := util.SetupSpecNamespace(ctx, testName, managementClusterProxy, artifactFolder)

	clusterName := fmt.Sprintf("%s-%s", testName, capiutil.RandomString(6))

	workloadClusterTemplate := clusterctl.ConfigCluster(ctx, clusterctl.ConfigClusterInput{
		ClusterctlConfigPath: clusterctlConfigPath,
		KubeconfigPath:       managementClusterProxy.GetKubeconfigPath(),
		// select cluster templates
		Flavor: "admission-webhook-t2",

		Namespace:                namespace.Name,
		ClusterName:              clusterName,
		KubernetesVersion:        e2eConfig.GetVariable(KubernetesVersion),
		ControlPlaneMachineCount: ptr.To[int64](3),
		// TODO: make infra provider configurable
		InfrastructureProvider: "docker",
		LogFolder:              filepath.Join(artifactFolder, "clusters", managementClusterProxy.GetName()),
		ClusterctlVariables: map[string]string{
			"CLUSTER_NAME":    clusterName,
			"NAMESPACE":       namespace.Name,
			"UPDATE_STRATEGY": "Recreate",
		},
	})
	require.NotNil(t, workloadClusterTemplate)

	err := managementClusterProxy.CreateOrUpdate(ctx, workloadClusterTemplate)
	require.Error(t, err)
	require.Contains(t, err.Error(), "version v1.31.1+k0s.0 is not compatible with K0sControlPlane, use v1.31.2+")
}
