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

package capicontolplanedocker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k0sproject/k0smotron/inttest/util"

	"github.com/stretchr/testify/suite"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type CAPIControlPlaneDockerSuite struct {
	suite.Suite
	client           *kubernetes.Clientset
	restConfig       *rest.Config
	clusterYamlsPath string
	ctx              context.Context
}

func TestCAPIControlPlaneDockerWorkerWorkerSuite(t *testing.T) {
	s := CAPIControlPlaneDockerSuite{}
	suite.Run(t, &s)
}

func (s *CAPIControlPlaneDockerSuite) SetupSuite() {
	kubeConfigPath := os.Getenv("KUBECONFIG")
	s.Require().NotEmpty(kubeConfigPath, "KUBECONFIG env var must be set and point to kind cluster")
	// Get kube client from kubeconfig
	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	s.Require().NoError(err)
	s.Require().NotNil(restCfg)
	s.restConfig = restCfg

	// Get kube client from kubeconfig
	kubeClient, err := kubernetes.NewForConfig(restCfg)
	s.Require().NoError(err)
	s.Require().NotNil(kubeClient)
	s.client = kubeClient

	tmpDir := s.T().TempDir()
	s.clusterYamlsPath = tmpDir + "/cluster.yaml"
	s.Require().NoError(os.WriteFile(s.clusterYamlsPath, []byte(dockerClusterYaml), 0644))

	s.ctx, _ = util.NewSuiteContext(s.T())
}

func (s *CAPIControlPlaneDockerSuite) TestCAPIControlPlaneDockerWorker() {

	// Apply the child cluster objects
	s.applyClusterObjects()
	defer func() {
		keep := os.Getenv("KEEP_AFTER_TEST")
		if keep == "true" {
			return
		}
		if keep == "on-failure" && s.T().Failed() {
			return
		}
		s.T().Log("Deleting cluster objects")
		s.deleteCluster()
	}()
	s.T().Log("cluster objects applied, waiting for cluster to be ready")

	var localPort int
	// nolint:staticcheck
	err := wait.PollImmediateUntilWithContext(s.ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
		localPort, _ = getLBPort("docker-test-cluster-lb")
		return localPort > 0, nil
	})
	s.Require().NoError(err)

	s.T().Log("waiting to see admin kubeconfig secret")
	kmcKC, err := util.GetKMCClientSet(s.ctx, s.client, "docker-test-cluster", "default", localPort)
	s.Require().NoError(err)

	// nolint:staticcheck
	err = wait.PollImmediateUntilWithContext(s.ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
		b, _ := s.client.RESTClient().
			Get().
			AbsPath("/healthz").
			DoRaw(context.Background())

		return string(b) == "ok", nil
	})
	s.Require().NoError(err)

	// nolint:staticcheck
	err = wait.PollImmediateUntilWithContext(s.ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
		output, err := exec.Command("docker", "exec", "docker-test-cluster-docker-test-0", "k0s", "status").Output()
		if err != nil {
			return false, nil
		}

		return strings.Contains(string(output), "Version:"), nil
	})
	s.Require().NoError(err)

	s.T().Log("waiting for node to be ready")
	s.Require().NoError(util.WaitForNodeReadyStatus(s.ctx, kmcKC, "docker-test-0", corev1.ConditionTrue))
}

func (s *CAPIControlPlaneDockerSuite) applyClusterObjects() {
	// Exec via kubectl
	out, err := exec.Command("kubectl", "apply", "-f", s.clusterYamlsPath).CombinedOutput()
	s.Require().NoError(err, "failed to apply cluster objects: %s", string(out))
}

func (s *CAPIControlPlaneDockerSuite) deleteCluster() {
	// Exec via kubectl
	out, err := exec.Command("kubectl", "delete", "cluster", "docker-test-cluster").CombinedOutput()
	s.Require().NoError(err, "failed to delete cluster objects: %s", string(out))
}

func getLBPort(name string) (int, error) {
	b, err := exec.Command("docker", "inspect", name, "--format", "{{json .NetworkSettings.Ports}}").Output()
	if err != nil {
		return 0, fmt.Errorf("failed to get inspect info from container %s: %w", name, err)
	}

	var ports map[string][]map[string]string
	err = json.Unmarshal(b, &ports)
	if err != nil {
		return 0, fmt.Errorf("failed to unmarshal inspect info from container %s: %w", name, err)
	}

	return strconv.Atoi(ports["6443/tcp"][0]["HostPort"])
}

var dockerClusterYaml = `
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: docker-test-cluster
  namespace: default
spec:
  clusterNetwork:
    pods:
      cidrBlocks:
      - 192.168.0.0/16
    serviceDomain: cluster.local
    services:
      cidrBlocks:
      - 10.128.0.0/12
  controlPlaneRef:
    apiVersion: controlplane.cluster.x-k8s.io/v1beta1
    kind: K0sControlPlane
    name: docker-test
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: DockerCluster
    name: docker-test
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: DockerMachineTemplate
metadata:
  name: docker-test-cp-template
  namespace: default
spec:
  template:
    spec: {}
---
apiVersion: controlplane.cluster.x-k8s.io/v1beta1
kind: K0sControlPlane
metadata:
  name: docker-test
spec:
  replicas: 1
  version: v1.30.4+k0s.0
  k0sConfigSpec:
    k0s:
      apiVersion: k0s.k0sproject.io/v1beta1
      kind: ClusterConfig
      metadata:
        name: k0s
      spec:
        api:
          extraArgs:
            anonymous-auth: "true"
        telemetry:
          enabled: false
    args:
      - --enable-worker
      - --no-taints
  machineTemplate:
    infrastructureRef:
      apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
      kind: DockerMachineTemplate
      name: docker-test-cp-template
      namespace: default
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: DockerCluster
metadata:
  name: docker-test
  namespace: default
spec:
`
