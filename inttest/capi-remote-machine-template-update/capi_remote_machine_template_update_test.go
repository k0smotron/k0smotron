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

package capiremotemachinetemplate

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"k8s.io/apimachinery/pkg/util/wait"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"text/template"
	"time"

	infra "github.com/k0smotron/k0smotron/api/infrastructure/v1beta1"
	"github.com/k0smotron/k0smotron/inttest/util"
	"github.com/k0sproject/k0s/inttest/common"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type RemoteMachineTemplateUpdateSuite struct {
	common.FootlooseSuite

	client                  *kubernetes.Clientset
	restConfig              *rest.Config
	clusterYamlsPath        string
	updatedClusterYamlsPath string
	privateKey              []byte
	publicKey               []byte
}

func (s *RemoteMachineTemplateUpdateSuite) SetupSuite() {
	s.FootlooseSuite.SetupSuite()
}

func TestRemoteMachineSuite(t *testing.T) {
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

	s := RemoteMachineTemplateUpdateSuite{
		common.FootlooseSuite{
			ControllerCount:      0,
			WorkerCount:          0,
			K0smotronWorkerCount: 1,
			K0smotronNetworks:    []string{"kind"},
		},
		kubeClient,
		restCfg,
		tmpDir + "/cluster.yaml",
		tmpDir + "/updated-cluster.yaml",
		privateKeyPEM,
		sshPublicKeyBytes,
	}
	suite.Run(t, &s)
}

func (s *RemoteMachineTemplateUpdateSuite) TestCAPIRemoteMachine() {
	ctx := s.Context()
	// Push public key to worker authorized_keys
	workerSSH, err := s.SSH(ctx, s.K0smotronNode(0))
	s.Require().NoError(err)
	defer workerSSH.Disconnect()
	s.T().Log("Pushing public key to worker")
	s.Require().NoError(workerSSH.Exec(s.Context(), "cat >>/root/.ssh/authorized_keys", common.SSHStreams{In: bytes.NewReader(s.publicKey)}))

	s.Require().NoError(err)
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

	s.createCluster()

	s.T().Log("cluster objects applied, waiting for cluster to be ready")
	var localPort int
	// nolint:staticcheck
	err = wait.PollImmediateUntilWithContext(ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
		localPort, _ = getLBPort("TestRemoteMachineSuite-k0smotron0")
		return localPort > 0, nil
	})
	s.Require().NoError(err)
	s.T().Log("waiting to see admin kubeconfig secret")
	s.Require().NoError(util.WaitForSecret(ctx, s.client, "remote-test-cluster-kubeconfig", "default"))
	kmcKC, err := util.GetKMCClientSet(ctx, s.client, "remote-test-cluster", "default", localPort)
	s.Require().NoError(err)

	// Verify the RemoteMachine is at expected state

	var rmName string
	// nolint:staticcheck
	err = wait.PollImmediateUntilWithContext(ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
		rm, err := s.findRemoteMachines("default")
		if err != nil {
			return false, err
		}

		if len(rm) == 0 {
			return true, nil
		}

		rmName = rm[0].GetName()
		return true, nil
	})
	s.Require().NoError(err)

	// nolint:staticcheck
	err = wait.PollImmediateUntilWithContext(ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
		rm, err := s.getRemoteMachine(rmName, "default")
		if err != nil {
			return false, err
		}

		expectedProviderID := fmt.Sprintf("remote-machine://%s:22", s.getWorkerIP())
		return rm.Status.Ready && expectedProviderID == rm.Spec.ProviderID, nil
	})
	s.Require().NoError(err)

	s.T().Log("waiting for node to be ready")
	s.Require().NoError(common.WaitForNodeReadyStatus(ctx, kmcKC, "remote-test-0", corev1.ConditionTrue))

	s.T().Log("update cluster")
	s.updateCluster()
	// nolint:staticcheck
	err = wait.PollImmediateUntilWithContext(ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
		output, err := exec.Command("docker", "exec", "TestRemoteMachineSuite-k0smotron0", "k0s", "status").Output()
		if err != nil {
			return false, nil
		}

		return strings.Contains(string(output), "Version: v1.29"), nil
	})
	s.Require().NoError(err)

	s.T().Log("waiting for node to be ready in updated cluster")
	s.Require().NoError(common.WaitForNodeReadyStatus(ctx, kmcKC, "remote-test-0", corev1.ConditionTrue))
}

func (s *RemoteMachineTemplateUpdateSuite) findRemoteMachines(namespace string) ([]infra.RemoteMachine, error) {
	apiPath := fmt.Sprintf("/apis/infrastructure.cluster.x-k8s.io/v1beta1/namespaces/%s/remotemachines", namespace)
	result, err := s.client.RESTClient().Get().AbsPath(apiPath).DoRaw(s.Context())
	if err != nil {
		return nil, err
	}
	rm := &infra.RemoteMachineList{}
	if err := yaml.Unmarshal(result, rm); err != nil {
		return nil, err
	}
	return rm.Items, nil
}

func (s *RemoteMachineTemplateUpdateSuite) getRemoteMachine(name string, namespace string) (*infra.RemoteMachine, error) {
	apiPath := fmt.Sprintf("/apis/infrastructure.cluster.x-k8s.io/v1beta1/namespaces/%s/remotemachines/%s", namespace, name)
	result, err := s.client.RESTClient().Get().AbsPath(apiPath).DoRaw(s.Context())
	if err != nil {
		return nil, err
	}
	rm := &infra.RemoteMachine{}
	if err := yaml.Unmarshal(result, rm); err != nil {
		return nil, err
	}
	return rm, nil
}

func (s *RemoteMachineTemplateUpdateSuite) deleteCluster() {
	out, err := exec.Command("kubectl", "delete", "-f", s.clusterYamlsPath).CombinedOutput()
	s.Require().NoError(err, "failed to delete cluster objects: %s", string(out))
}

func (s *RemoteMachineTemplateUpdateSuite) updateCluster() {
	out, err := exec.Command("kubectl", "apply", "-f", s.updatedClusterYamlsPath).CombinedOutput()
	s.Require().NoError(err, "failed to update cluster objects: %s", string(out))
}

func (s *RemoteMachineTemplateUpdateSuite) createCluster() {

	// Get worker IP
	workerIP := s.getWorkerIP()
	s.Require().NotEmpty(workerIP)

	// Get SSH key
	machines, err := s.InspectMachines([]string{s.K0smotronNode(0)})
	s.Require().NoError(err)
	s.Require().NotEmpty(machines)

	// Parse the cluster yaml as template
	t, err := template.New("cluster").Parse(clusterYaml)
	s.Require().NoError(err)

	// Execute the template to buffer
	var clusterYaml bytes.Buffer

	err = t.Execute(&clusterYaml, struct {
		Address string
		SSHKey  string
	}{
		Address: workerIP,
		SSHKey:  base64.StdEncoding.EncodeToString(s.privateKey),
	})
	s.Require().NoError(err)
	bytes := clusterYaml.Bytes()

	s.Require().NoError(os.WriteFile(s.clusterYamlsPath, bytes, 0644))
	out, err := exec.Command("kubectl", "apply", "-f", s.clusterYamlsPath).CombinedOutput()
	s.Require().NoError(os.WriteFile(s.updatedClusterYamlsPath, []byte(updatedClusterYaml), 0644))
	s.Require().NoError(err, "failed to update cluster objects: %s", string(out))
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

func (s *RemoteMachineTemplateUpdateSuite) getWorkerIP() string {
	nodeName := s.K0smotronNode(0)
	ssh, err := s.SSH(s.Context(), nodeName)
	s.Require().NoError(err)
	defer ssh.Disconnect()

	ipAddress, err := ssh.ExecWithOutput(s.Context(), "hostname -i")
	s.Require().NoError(err)
	return ipAddress
}

var clusterYaml = `
apiVersion: controlplane.cluster.x-k8s.io/v1beta1
kind: K0sControlPlane
metadata:
  name: remote-test
spec:
  replicas: 1
  version: v1.28.7+k0s.0
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
      kind: RemoteMachineTemplate
      name: remote-test-cp-template
      namespace: default
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: RemoteCluster
metadata:
  name: remote-test
  namespace: default
spec:
---
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: remote-test-cluster
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
  controlPlaneEndpoint:
    host: {{ .Address }}
    port: 6443
  controlPlaneRef:
    apiVersion: controlplane.cluster.x-k8s.io/v1beta1
    kind: K0sControlPlane
    name: remote-test
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: RemoteCluster
    name: remote-test
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: RemoteMachineTemplate
metadata:
  name: remote-test-cp-template
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

var updatedClusterYaml = `
apiVersion: controlplane.cluster.x-k8s.io/v1beta1
kind: K0sControlPlane
metadata:
  name: remote-test
spec:
  replicas: 1
  version: v1.29.2+k0s.0
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
      kind: RemoteMachineTemplate
      name: remote-test-cp-template
      namespace: default
`
