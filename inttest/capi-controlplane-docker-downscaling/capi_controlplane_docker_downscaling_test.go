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

package capicontolplanedockerdownscaling

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	k0stestutil "github.com/k0sproject/k0s/inttest/common"
	autopilot "github.com/k0sproject/k0s/pkg/apis/autopilot/v1beta2"
	"github.com/k0sproject/k0smotron/inttest/util"

	"github.com/stretchr/testify/suite"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type CAPIControlPlaneDockerDownScalingSuite struct {
	suite.Suite
	client                 *kubernetes.Clientset
	restConfig             *rest.Config
	clusterYamlsPath       string
	clusterYamlsUpdatePath string
	ctx                    context.Context
}

func TestCAPIControlPlaneDockerDownScalingSuite(t *testing.T) {
	s := CAPIControlPlaneDockerDownScalingSuite{}
	suite.Run(t, &s)
}

func (s *CAPIControlPlaneDockerDownScalingSuite) SetupSuite() {
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
	s.clusterYamlsUpdatePath = tmpDir + "/update.yaml"
	s.Require().NoError(os.WriteFile(s.clusterYamlsUpdatePath, []byte(controlPlaneUpdate), 0644))

	s.ctx, _ = util.NewSuiteContext(s.T())
}

func (s *CAPIControlPlaneDockerDownScalingSuite) TestCAPIControlPlaneDockerDownScaling() {

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
		localPort, _ = getLBPort("docker-test-lb")
		return localPort > 0, nil
	})
	s.Require().NoError(err)

	s.T().Log("waiting to see admin kubeconfig secret")
	kmcKC, err := util.GetKMCClientSet(s.ctx, s.client, "docker-test", "default", localPort)
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

	for i := 0; i < 3; i++ {
		// nolint:staticcheck
		err = wait.PollImmediateUntilWithContext(s.ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
			nodeName := fmt.Sprintf("docker-test-%d", i)
			output, err := exec.Command("docker", "exec", nodeName, "k0s", "status").Output()
			if err != nil {
				return false, nil
			}

			return strings.Contains(string(output), "Version:"), nil
		})
		s.Require().NoError(err)
	}

	s.T().Log("waiting for node to be ready")
	s.Require().NoError(k0stestutil.WaitForNodeReadyStatus(s.ctx, kmcKC, "docker-test-worker-0", corev1.ConditionTrue))

	// nolint:staticcheck
	err = wait.PollImmediateUntilWithContext(s.ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
		result, err := kmcKC.RESTClient().Get().AbsPath("/apis/autopilot.k0sproject.io/v1beta2/controlnodes").DoRaw(ctx)
		if err != nil {
			return false, err
		}
		var controlNodeList autopilot.ControlNodeList
		if err := yaml.Unmarshal(result, &controlNodeList); err != nil {
			return false, err
		}

		return len(controlNodeList.Items) == 3, nil
	})
	s.Require().NoError(err)

	s.T().Log("scaling down control plane")

	out, err := exec.Command("docker", "exec", "docker-test-1", "k0s", "etcd", "leave").CombinedOutput()
	s.T().Log(string(out))
	s.Require().NoError(err)

	out, err = exec.Command("docker", "exec", "docker-test-2", "k0s", "etcd", "leave").CombinedOutput()
	s.T().Log(string(out))
	s.Require().NoError(err)

	s.updateClusterObjects()
	// nolint:staticcheck
	err = wait.PollImmediateUntilWithContext(s.ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
		output, err := exec.Command("docker", "exec", "docker-test-0", "k0s", "etcd", "member-list").CombinedOutput()
		if err != nil {
			return false, nil
		}

		return !strings.Contains(string(output), "docker-test-1"), nil
	})
	s.Require().NoError(err)

	s.Require().NoError(k0stestutil.WaitForNodeReadyStatus(s.ctx, kmcKC, "docker-test-worker-0", corev1.ConditionTrue))
}

func (s *CAPIControlPlaneDockerDownScalingSuite) applyClusterObjects() {
	// Exec via kubectl
	out, err := exec.Command("kubectl", "apply", "-f", s.clusterYamlsPath).CombinedOutput()
	s.Require().NoError(err, "failed to apply cluster objects: %s", string(out))
}

func (s *CAPIControlPlaneDockerDownScalingSuite) updateClusterObjects() {
	// Exec via kubectl
	out, err := exec.Command("kubectl", "apply", "-f", s.clusterYamlsUpdatePath).CombinedOutput()
	s.Require().NoError(err, "failed to update cluster objects: %s", string(out))
}

func (s *CAPIControlPlaneDockerDownScalingSuite) deleteCluster() {
	// Exec via kubectl
	out, err := exec.Command("kubectl", "delete", "-f", s.clusterYamlsPath).CombinedOutput()
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
  name: docker-test
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
  replicas: 3
  version: v1.27.1+k0s.0
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
---
apiVersion: cluster.x-k8s.io/v1beta1
kind: Machine
metadata:
  name:  docker-test-worker-0
  namespace: default
spec:
  version: v1.27.1
  clusterName: docker-test
  bootstrap:
    configRef:
      apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
      kind: K0sWorkerConfig
      name: docker-test-worker-0
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: DockerMachine
    name: docker-test-worker-0
---
apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
kind: K0sWorkerConfig
metadata:
  name: docker-test-worker-0
  namespace: default
spec:
  # version is deliberately different to be able to verify we actually pick it up :)
  version: v1.27.1+k0s.0
  args:
    - --labels=k0sproject.io/foo=bar
  preStartCommands:
    - echo -n "pre-start" > /tmp/pre-start
  postStartCommands:
    - echo -n "post-start" > /tmp/post-start
  files:
    - path: /tmp/test-file
      content: test-file
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: DockerMachine
metadata:
  name: docker-test-worker-0
  namespace: default
spec:
`

var controlPlaneUpdate = `
apiVersion: controlplane.cluster.x-k8s.io/v1beta1
kind: K0sControlPlane
metadata:
  name: docker-test
spec:
  replicas: 1
  version: v1.27.1+k0s.0
  k0sConfigSpec:
    args:
      - --enable-worker
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
  machineTemplate:
    infrastructureRef:
      apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
      kind: DockerMachineTemplate
      name: docker-test-cp-template
      namespace: default
`
