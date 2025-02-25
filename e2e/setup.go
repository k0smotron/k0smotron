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
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	controlplanev1beta1 "github.com/k0smotron/k0smotron/api/controlplane/v1beta1"
	"github.com/k0smotron/k0smotron/e2e/mothership"
	"github.com/k0smotron/k0smotron/e2e/util"
	"sigs.k8s.io/cluster-api/test/framework"
	capiframework "sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/test/framework/bootstrap"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	ctrl "sigs.k8s.io/controller-runtime"
)

// Test suite constants for e2e config variables.
const (
	KubernetesVersion                = "KUBERNETES_VERSION"
	KubernetesVersionManagement      = "KUBERNETES_VERSION_MANAGEMENT"
	KubernetesVersionFirstUpgradeTo  = "KUBERNETES_VERSION_FIRST_UPGRADE_TO"
	KubernetesVersionSecondUpgradeTo = "KUBERNETES_VERSION_SECOND_UPGRADE_TO"
	ControlPlaneMachineCount         = "CONTROL_PLANE_MACHINE_COUNT"
	IPFamily                         = "IP_FAMILY"
)

var (
	ctx = ctrl.SetupSignalHandler()

	// watchesCtx is used in log streaming to be able to get canceled via cancelWatches after ending the test suite.
	watchesCtx, cancelWatches = context.WithCancel(ctx)

	// configPath is the path to the e2e config file.
	configPath string

	// e2eConfig to be used for this test, read from configPath.
	e2eConfig *clusterctl.E2EConfig

	// clusterctlConfig is the file which tests will use as a clusterctl config.
	clusterctlConfig string

	// useExistingCluster instructs the test to use the current cluster instead of creating a new one (default discovery rules apply).
	useExistingCluster bool

	// clusterctlConfigPath to be used for this test, created by generating a clusterctl local repository
	// with the providers specified in the configPath.
	clusterctlConfigPath string

	// artifactFolder is the folder to store e2e test artifacts.
	artifactFolder string

	// skipCleanup prevents cleanup of test resources e.g. for debug purposes.
	skipCleanup bool

	// managementClusterProvider manages provisioning of the bootstrap cluster to be used for the e2e tests.
	// Please note that provisioning will be skipped if e2e.use-existing-cluster is provided.
	managementClusterProvider bootstrap.ClusterProvider

	// managementClusterProxy allows to interact with the management cluster to be used for the e2e tests.
	managementClusterProxy capiframework.ClusterProxy
)

func init() {
	flag.StringVar(&configPath, "config", "", "path to the e2e config file")
	flag.BoolVar(&skipCleanup, "skip-resource-cleanup", false, "if true, the resource cleanup after tests will be skipped")
	flag.StringVar(&artifactFolder, "artifacts-folder", "", "folder where e2e test artifact should be stored")
	flag.BoolVar(&useExistingCluster, "use-existing-cluster", false, "if true, the test uses the current cluster instead of creating a new one (default discovery rules apply)")
}

func setup(t *testing.T, test func(t *testing.T)) {
	ctrl.SetLogger(klog.Background())
	flag.Parse()

	// On the k0smotron side we avoid using Gomega for assertions but since we want to use the
	// cluster-api framework as much as possible, the framework assertions require registering
	// a fail handler beforehand.
	gomega.RegisterFailHandler(func(message string, callerSkip ...int) {
		panic(message)
	})

	err := setupMothership()
	if err != nil {
		panic(err)
	}

	defer func() {
		if !skipCleanup {
			tearDown(managementClusterProvider, managementClusterProxy)
		}
	}()

	test(t)
}

func setupMothership() error {
	var err error
	e2eConfig, err = loadE2EConfig(ctx, configPath)
	if err != nil {
		return fmt.Errorf("failed to load e2e config: %w", err)
	}

	if clusterctlConfig == "" {
		clusterctlConfigPath = clusterctl.CreateRepository(ctx, clusterctl.CreateRepositoryInput{
			E2EConfig:        e2eConfig,
			RepositoryFolder: filepath.Join(artifactFolder, "repository"),
		})
	} else {
		clusterctlConfigPath = clusterctlConfig
	}

	scheme, err := initScheme()
	if err != nil {
		return err
	}

	kubeconfigPath := ""
	if !useExistingCluster {
		managementClusterProvider = bootstrap.CreateKindBootstrapClusterAndLoadImages(ctx, bootstrap.CreateKindBootstrapClusterAndLoadImagesInput{
			Name:               e2eConfig.ManagementClusterName,
			Images:             e2eConfig.Images,
			KubernetesVersion:  e2eConfig.GetVariable(KubernetesVersionManagement),
			RequiresDockerSock: e2eConfig.HasDockerProvider(),
			IPFamily:           e2eConfig.GetVariable(IPFamily),
			LogFolder:          filepath.Join(artifactFolder, "kind"),
		})
		if managementClusterProvider == nil {
			return errors.New("failed to create a management cluster")
		}
		kubeconfigPath = managementClusterProvider.GetKubeconfigPath()
	} else {
		fmt.Println("Using an existing bootstrap cluster")
	}

	managementClusterProxy = capiframework.NewClusterProxy("bootstrap", kubeconfigPath, scheme, framework.WithMachineLogCollector(framework.DockerLogCollector{}))
	if managementClusterProxy == nil {
		return errors.New("failed to get a management cluster proxy")
	}

	err = mothership.InitAndWatchControllerLogs(watchesCtx, clusterctl.InitManagementClusterAndWatchControllerLogsInput{
		ClusterProxy:             managementClusterProxy,
		ClusterctlConfigPath:     clusterctlConfigPath,
		InfrastructureProviders:  e2eConfig.InfrastructureProviders(),
		DisableMetricsCollection: true,
		BootstrapProviders:       []string{"k0sproject-k0smotron"},
		ControlPlaneProviders:    []string{"k0sproject-k0smotron"},
		LogFolder:                filepath.Join(artifactFolder, "capi"),
	}, util.GetInterval(e2eConfig, "bootstrap", "wait-deployment-available"))
	if err != nil {
		return fmt.Errorf("failed to init management cluster: %w", err)
	}

	return nil
}

func tearDown(bootstrapClusterProvider bootstrap.ClusterProvider, bootstrapClusterProxy framework.ClusterProxy) {
	cancelWatches()
	if bootstrapClusterProxy != nil {
		bootstrapClusterProxy.Dispose(ctx)
	}
	if bootstrapClusterProvider != nil {
		bootstrapClusterProvider.Dispose(ctx)
	}
}

func initScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	capiframework.TryAddDefaultSchemes(s)
	err := controlplanev1beta1.AddToScheme(s)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func loadE2EConfig(ctx context.Context, configPath string) (*clusterctl.E2EConfig, error) {
	configData, err := os.ReadFile(configPath)

	if err != nil {
		return nil, fmt.Errorf("failed to read the e2e test config file: %w", err)
	}

	config := &clusterctl.E2EConfig{}
	err = yaml.Unmarshal(configData, config)
	if err != nil {
		return nil, fmt.Errorf("failed to convert the e2e test config file to yaml: %w", err)
	}

	err = config.ResolveReleases(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve release markers in e2e test config file: %w", err)
	}

	config.Defaults()
	config.AbsPaths(filepath.Dir(configPath))

	return config, nil
}
