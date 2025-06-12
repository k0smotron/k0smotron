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

package mothership

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/k0sproject/k0smotron/e2e/util"
	"sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
)

// UpgradeManagementClusterAndWait upgrades providers in a management cluster using clusterctl, and waits for the cluster to be ready.
func UpgradeManagementClusterAndWait(ctx context.Context, input clusterctl.UpgradeManagementClusterAndWaitInput, interval util.Interval) error {
	upgradeInput := clusterctl.UpgradeInput{
		ClusterctlConfigPath:      input.ClusterctlConfigPath,
		ClusterctlVariables:       input.ClusterctlVariables,
		ClusterName:               input.ClusterProxy.GetName(),
		KubeconfigPath:            input.ClusterProxy.GetKubeconfigPath(),
		Contract:                  input.Contract,
		CoreProvider:              input.CoreProvider,
		BootstrapProviders:        input.BootstrapProviders,
		ControlPlaneProviders:     input.ControlPlaneProviders,
		InfrastructureProviders:   input.InfrastructureProviders,
		IPAMProviders:             input.IPAMProviders,
		RuntimeExtensionProviders: input.RuntimeExtensionProviders,
		AddonProviders:            input.AddonProviders,
		LogFolder:                 input.LogFolder,
	}

	client := input.ClusterProxy.GetClient()

	clusterctl.Upgrade(ctx, upgradeInput)

	fmt.Println("Waiting for provider controllers to be running")
	controllersDeployments := framework.GetControllerDeployments(ctx, framework.GetControllerDeploymentsInput{
		Lister: client,
		// This namespace has been dropped in v0.4.x.
		// We have to exclude this namespace here as after an upgrade from v0.3x there won't
		// be a controller in this namespace anymore and if we wait for it to come up the test would fail.
		// Note: We can drop this as soon as we don't have a test upgrading from v0.3.x anymore.
		ExcludeNamespaces: []string{"capi-webhook-system"},
	})
	if len(controllersDeployments) == 0 {
		return errors.New("the list of controller deployments should not be empty")
	}

	for _, deployment := range controllersDeployments {
		err := util.WaitForDeploymentsAvailable(ctx, framework.WaitForDeploymentsAvailableInput{
			Getter:     client,
			Deployment: deployment,
		}, interval)
		if err != nil {
			return err
		}

		framework.WatchPodMetrics(ctx, framework.WatchPodMetricsInput{
			GetLister:   client,
			ClientSet:   input.ClusterProxy.GetClientSet(),
			Deployment:  deployment,
			MetricsPath: filepath.Join(input.LogFolder, "metrics", deployment.GetNamespace()),
		})
	}

	return nil
}
