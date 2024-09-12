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

package capidockerclusterclass

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"github.com/k0sproject/k0s/inttest/common"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"text/template"
	"time"

	"github.com/k0sproject/k0smotron/inttest/util"

	"github.com/stretchr/testify/suite"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type CAPIDockerClusterClassSuite struct {
	common.FootlooseSuite

	client                *kubernetes.Clientset
	restConfig            *rest.Config
	privateKey            []byte
	publicKey             []byte
	ctx                   context.Context
	clusterYamlsPath      string
	clusterClassYamlsPath string
}

func (s *CAPIDockerClusterClassSuite) SetupSuite() {
	s.FootlooseSuite.SetupSuite()
}

//func TestCAPIDockerClusterClassSuite(t *testing.T) {
//	s := CAPIDockerClusterClassSuite{}
//	suite.Run(t, &s)
//}

func TestCAPIDockerClusterClassSuite(t *testing.T) {
	kubeConfigPath := os.Getenv("KUBECONFIG")
	require.NotEmpty(t, kubeConfigPath, "KUBECONFIG env var must be set and point to kind cluster")
	// Get kube client from kubeconfig
	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	require.NoError(t, err)
	require.NotNil(t, restCfg)

	// Get kube client from kubeconfig
	kubeClient, err := kubernetes.NewForConfig(restCfg)
	require.NoError(t, err)
	require.NotNil(t, kubeClient)

	// Create keypair to use with SSH
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// Convert the private key to PEM format
	privateKeyBytes := x509.MarshalPKCS1PrivateKey(privateKey)
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privateKeyBytes,
	})

	// Extract the public key from the private key
	publicKey := &privateKey.PublicKey

	// Convert the public key to the OpenSSH format
	sshPublicKey, err := ssh.NewPublicKey(publicKey)
	require.NoError(t, err)
	sshPublicKeyBytes := ssh.MarshalAuthorizedKey(sshPublicKey)

	tmpDir := t.TempDir()
	t.Log("111cluster objects applied, waiting for cluster to be ready")
	s := CAPIDockerClusterClassSuite{
		FootlooseSuite: common.FootlooseSuite{
			ControllerCount:      0,
			WorkerCount:          0,
			K0smotronWorkerCount: 1,
			K0smotronNetworks:    []string{"kind"},
		},
		client:                kubeClient,
		restConfig:            restCfg,
		privateKey:            privateKeyPEM,
		publicKey:             sshPublicKeyBytes,
		clusterYamlsPath:      tmpDir + "/cluster.yaml",
		clusterClassYamlsPath: tmpDir + "/cluster-class.yaml",
	}

	suite.Run(t, &s)
}

func (s *CAPIDockerClusterClassSuite) TestCAPIDockerClusterClass() {
	s.ctx, _ = util.NewSuiteContext(s.T())

	// Push public key to worker authorized_keys
	workerSSH, err := s.SSH(s.ctx, s.K0smotronNode(0))
	s.Require().NoError(err)
	defer workerSSH.Disconnect()
	s.T().Log("Pushing public key to worker")
	s.Require().NoError(workerSSH.Exec(s.Context(), "cat >>/root/.ssh/authorized_keys", common.SSHStreams{In: bytes.NewReader(s.publicKey)}))

	// Apply the child cluster objects
	s.createCluster()
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
	err = wait.PollImmediateUntilWithContext(s.ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
		localPort, _ = getLBPort("docker-test-cluster-lb")
		return localPort > 0, nil
	})
	s.Require().NoError(err)

	s.T().Log("waiting to see admin kubeconfig secret")
	kmcKC, err := util.GetKMCClientSet(s.ctx, s.client, "docker-test-cluster", "default", localPort)
	s.Require().NoError(err)

	s.T().Log("waiting for control-plane")
	// nolint:staticcheck
	err = wait.PollImmediateUntilWithContext(s.ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
		b, _ := s.client.RESTClient().
			Get().
			AbsPath("/healthz").
			DoRaw(context.Background())

		return string(b) == "ok", nil
	})
	s.Require().NoError(err)

	s.T().Log("waiting for worker nodes")
	// nolint:staticcheck
	err = wait.PollImmediateUntilWithContext(s.ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
		nodes, _ := kmcKC.CoreV1().Nodes().List(s.ctx, metav1.ListOptions{})
		return len(nodes.Items) == 3, nil
	})
	s.Require().NoError(err)
}

func (s *CAPIDockerClusterClassSuite) createCluster() {

	// Get worker IP
	workerIP := s.getWorkerIP()
	s.Require().NotEmpty(workerIP)

	// Get SSH key
	machines, err := s.InspectMachines([]string{s.K0smotronNode(0)})
	s.Require().NoError(err)
	s.Require().NotEmpty(machines)

	// Parse the cluster yaml as template
	t, err := template.New("cluster").Parse(clusterClassYaml)
	s.Require().NoError(err)

	// Execute the template to buffer
	var clusterClassYaml bytes.Buffer

	err = t.Execute(&clusterClassYaml, struct {
		Address string
		SSHKey  string
	}{
		Address: workerIP,
		SSHKey:  base64.StdEncoding.EncodeToString(s.privateKey),
	})
	s.Require().NoError(err)
	bytes := clusterClassYaml.Bytes()

	s.Require().NoError(os.WriteFile(s.clusterClassYamlsPath, bytes, 0644))
	out, err := exec.Command("kubectl", "apply", "-f", s.clusterClassYamlsPath).CombinedOutput()
	s.Require().NoError(err, "failed to update cluster objects: %s", string(out))

	s.Require().NoError(os.WriteFile(s.clusterYamlsPath, []byte(clusterYaml), 0644))
	out, err = exec.Command("kubectl", "apply", "-f", s.clusterYamlsPath).CombinedOutput()
	s.Require().NoError(err, "failed to apply cluster objects: %s", string(out))
}

func (s *CAPIDockerClusterClassSuite) deleteCluster() {
	// Exec via kubectl
	out, err := exec.Command("kubectl", "delete", "-f", s.clusterYamlsPath).CombinedOutput()
	s.Require().NoError(err, "failed to delete cluster objects: %s", string(out))
	out, err = exec.Command("kubectl", "delete", "-f", s.clusterClassYamlsPath).CombinedOutput()
	s.Require().NoError(err, "failed to delete cluster class objects: %s", string(out))
}

func (s *CAPIDockerClusterClassSuite) getWorkerIP() string {
	nodeName := s.K0smotronNode(0)
	ssh, err := s.SSH(s.Context(), nodeName)
	s.Require().NoError(err)
	defer ssh.Disconnect()

	ipAddress, err := ssh.ExecWithOutput(s.Context(), "hostname -i")
	s.Require().NoError(err)
	return ipAddress
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

var clusterYaml = `
---
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: docker-test-cluster
  namespace: default
spec:
  topology:
    class: k0smotron-clusterclass
    version: v1.30.4
    workers:
      machineDeployments:
      - class: docker-test-default-worker
        name: md
        replicas: 1
      - class: remotemachine-test-default-worker
        name: rmd
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
kind: K0sControlPlaneTemplate
metadata:
  name: docker-test
  namespace: default
spec:
  template:
    spec:
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
  name: k0smotron-clusterclass
spec:
  controlPlane:
    ref:
      apiVersion: controlplane.cluster.x-k8s.io/v1beta1
      kind: K0sControlPlaneTemplate
      name: docker-test
      namespace: default
    machineInfrastructure:
      ref:
        kind: DockerMachineTemplate
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        name: docker-test-machine-template
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
    - class: remotemachine-test-default-worker
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
            kind: RemoteMachineTemplate
            name: remote-test-machine-template
            namespace: default
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: RemoteMachineTemplate
metadata:
  name: remote-test-machine-template
  namespace: default
spec:
  template:
    spec: 
      pool: default
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: PooledRemoteMachine
metadata:
  name: remote-test-0
  namespace: default
spec:
  pool: default
  machine:
    address: {{ .Address }}
    port: 22
    user: root
    sshKeyRef:
      name: footloose-key
---
apiVersion: v1
kind: Secret
metadata:
  name:  footloose-key
  namespace: default
data:
   value: {{ .SSHKey }}
type: Opaque
`
