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

package capidockerclusterclassk0smotron

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/k0sproject/k0smotron/inttest/util"
	"github.com/stretchr/testify/suite"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type CAPIDockerClusterClassK0smotronSuite struct {
	suite.Suite
	client                *kubernetes.Clientset
	restConfig            *rest.Config
	clusterYamlsPath      string
	clusterClassYamlsPath string
	ctx                   context.Context
}

func TestCAPIDockerClusterClassK0smotronSuite(t *testing.T) {
	s := CAPIDockerClusterClassK0smotronSuite{}
	suite.Run(t, &s)
}

func (s *CAPIDockerClusterClassK0smotronSuite) SetupSuite() {
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
	s.Require().NoError(os.WriteFile(s.clusterYamlsPath, []byte(clusterYaml), 0644))
	s.clusterClassYamlsPath = tmpDir + "/cluster-class.yaml"
	s.Require().NoError(os.WriteFile(s.clusterClassYamlsPath, []byte(clusterClassYaml), 0644))

	s.ctx, _ = util.NewSuiteContext(s.T())
}

func (s *CAPIDockerClusterClassK0smotronSuite) TestCAPIDocker() {

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

	// Wait for the cluster to be ready
	// Wait to see the CP pods ready
	s.Require().NoError(util.WaitForStatefulSet(s.ctx, s.client, "kmc-docker-test-cluster", "default"))
}

func (s *CAPIDockerClusterClassK0smotronSuite) applyClusterObjects() {
	// Exec via kubectl
	out, err := exec.Command("kubectl", "apply", "-f", s.clusterClassYamlsPath).CombinedOutput()
	s.Require().NoError(err, "failed to apply cluster class objects: %s", string(out))
	out, err = exec.Command("kubectl", "apply", "-f", s.clusterYamlsPath).CombinedOutput()
	s.Require().NoError(err, "failed to apply cluster objects: %s", string(out))
}

func (s *CAPIDockerClusterClassK0smotronSuite) deleteCluster() {
	// Exec via kubectl
	out, err := exec.Command("kubectl", "delete", "-f", s.clusterYamlsPath).CombinedOutput()
	s.Require().NoError(err, "failed to delete cluster objects: %s", string(out))
	out, err = exec.Command("kubectl", "delete", "-f", s.clusterClassYamlsPath).CombinedOutput()
	s.Require().NoError(err, "failed to delete cluster class objects: %s", string(out))
}

var clusterYaml = `
---
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: docker-test-cluster
  namespace: default
spec:
  topology:
    class: k0smotron-cluster-class
    version: v1.30.4
    workers:
      machineDeployments:
      - class: docker-test-default-worker
        name: md
        replicas: 1
`

var clusterClassYaml = `
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: DockerClusterTemplate
metadata:
  name: k0smotron-docker-cluster-tmpl
spec:
  template:
    spec: {}
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: DockerMachineTemplate
metadata:
  name: docker-test-machine-template
  namespace: default
spec:
  template:
    spec: {}
---
apiVersion: controlplane.cluster.x-k8s.io/v1beta1
kind: K0smotronControlPlaneTemplate
metadata:
  name: docker-test
  namespace: default
spec:
  template:
    spec:
      version: v1.30.4-k0s.0
      persistence:
        type: emptyDir
      service:
        type: NodePort
---
apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
kind: K0sWorkerConfigTemplate
metadata:
  name: docker-test-worker-template
  namespace: default
spec:
  template:
    spec:
      version: v1.30.4+k0s.0
---
apiVersion: cluster.x-k8s.io/v1beta1
kind: ClusterClass
metadata:
  name: k0smotron-cluster-class
spec:
  controlPlane:
    ref:
      apiVersion: controlplane.cluster.x-k8s.io/v1beta1
      kind: K0smotronControlPlaneTemplate
      name: docker-test
      namespace: default
  infrastructure:
    ref:
      apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
      kind: DockerClusterTemplate
      name: k0smotron-docker-cluster-tmpl
      namespace: default
  workers:
    machineDeployments:
    - class: docker-test-default-worker
      template:
        bootstrap:
          ref:
            apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
            kind: K0sWorkerConfigTemplate
            name: docker-test-worker-template
            namespace: default
        infrastructure:
          ref:
            apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
            kind: DockerMachineTemplate
            name: docker-test-machine-template
            namespace: default
`
