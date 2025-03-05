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

package capiremotemachinejobprovision

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	infra "github.com/k0smotron/k0smotron/api/infrastructure/v1beta1"
	"github.com/k0smotron/k0smotron/inttest/util"
	"github.com/k0sproject/k0s/inttest/common"
	"os"
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type RemoteMachineSuite struct {
	common.FootlooseSuite

	client     *kubernetes.Clientset
	restConfig *rest.Config
	privateKey []byte
	publicKey  []byte
}

func (s *RemoteMachineSuite) SetupSuite() {
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

	s := RemoteMachineSuite{
		common.FootlooseSuite{
			ControllerCount:      0,
			WorkerCount:          0,
			K0smotronWorkerCount: 1,
			K0smotronNetworks:    []string{"kind"},
		},
		kubeClient,
		restCfg,
		privateKeyPEM,
		sshPublicKeyBytes,
	}
	suite.Run(t, &s)
}

func (s *RemoteMachineSuite) TestCAPIRemoteMachine() {
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

	// Wait for the cluster to be ready
	// Wait to see the CP pods ready
	s.Require().NoError(common.WaitForStatefulSet(ctx, s.client, "kmc-remote-test", "default"))

	s.T().Log("Starting portforward")
	fw, err := util.GetPortForwarder(s.restConfig, "kmc-remote-test-0", "default", 30443)
	s.Require().NoError(err)

	go fw.Start(s.Require().NoError)
	defer fw.Close()

	<-fw.ReadyChan

	localPort, err := fw.LocalPort()
	s.Require().NoError(err)
	s.T().Log("waiting to see admin kubeconfig secret")
	s.Require().NoError(util.WaitForSecret(ctx, s.client, "remote-test-kubeconfig", "default"))
	kmcKC, err := util.GetKMCClientSet(ctx, s.client, "remote-test", "default", localPort)
	s.Require().NoError(err)

	s.T().Log("waiting for node to be ready")
	s.Require().NoError(common.WaitForNodeReadyStatus(ctx, kmcKC, "remote-test-0", corev1.ConditionTrue))
	// Verify the RemoteMachine is at expected state
	rm, err := s.getRemoteMachine("remote-test-0", "default")
	s.Require().NoError(err)
	s.Require().True(rm.Status.Ready)
	expectedProviderID := fmt.Sprintf("remote-machine://%s:22", s.getWorkerIP())
	s.Require().Equal(expectedProviderID, rm.Spec.ProviderID)

	s.T().Log("deleting node from cluster")
	s.Require().NoError(s.deleteRemoteMachine("remote-test-0", "default"))

	nodes, err := kmcKC.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	s.Require().NoError(err)
	s.Require().Equal(corev1.ConditionFalse, nodes.Items[0].Status.Conditions[0].Status)

}

func (s *RemoteMachineSuite) getRemoteMachine(name string, namespace string) (*infra.RemoteMachine, error) {
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

func (s *RemoteMachineSuite) deleteRemoteMachine(name string, namespace string) error {
	apiPath := fmt.Sprintf("/apis/infrastructure.cluster.x-k8s.io/v1beta1/namespaces/%s/remotemachines/%s", namespace, name)
	_, err := s.client.RESTClient().Delete().AbsPath(apiPath).DoRaw(s.Context())
	return err
}

func (s *RemoteMachineSuite) deleteCluster() {
	response := s.client.RESTClient().Delete().AbsPath("/apis/cluster.x-k8s.io/v1beta1/namespaces/default/clusters/remote-test").Do(s.Context())
	s.Require().NoError(response.Error())
	if err := s.client.CoreV1().Secrets("default").Delete(s.Context(), "footloose-key", metav1.DeleteOptions{}); err != nil {
		s.T().Logf("failed to delete footloose SSH key secret: %s", err.Error())
	}
}

func (s *RemoteMachineSuite) createCluster() {

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
	// s.T().Logf("cluster yaml: %s", string(bytes))
	resources, err := util.ParseManifests(bytes)
	s.Require().NoError(err)
	s.Require().NotEmpty(resources)

	dynClient, err := util.GetDynamicClient(s.restConfig)
	s.Require().NoError(err)
	s.Require().NotNil(dynClient)

	err = util.CreateResources(s.Context(), resources, s.client, dynClient)
	s.Require().NoError(err)
}

func (s *RemoteMachineSuite) getWorkerIP() string {
	nodeName := s.K0smotronNode(0)
	ssh, err := s.SSH(s.Context(), nodeName)
	s.Require().NoError(err)
	defer ssh.Disconnect()

	ipAddress, err := ssh.ExecWithOutput(s.Context(), "hostname -i")
	s.Require().NoError(err)
	return ipAddress
}

var clusterYaml = `apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: remote-test
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
    kind: K0smotronControlPlane
    name: remote-test
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: RemoteCluster
    name: remote-test
---
apiVersion: controlplane.cluster.x-k8s.io/v1beta1
kind: K0smotronControlPlane
metadata:
  name: remote-test
  namespace: default
spec:
  version: v1.27.2-k0s.0
  persistence:
    type: emptyDir
  service:
    type: NodePort
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: RemoteCluster
metadata:
  name: remote-test
  namespace: default
spec:
---
apiVersion: cluster.x-k8s.io/v1beta1
kind: Machine
metadata:
  name:  remote-test-0
  namespace: default
spec:
  clusterName: remote-test
  bootstrap:
    configRef:
      apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
      kind: K0sWorkerConfig
      name: remote-test-0
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: RemoteMachine
    name: remote-test-0
---
apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
kind: K0sWorkerConfig
metadata:
  name: remote-test-0
  namespace: default
spec:
  version: v1.27.2+k0s.0
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: RemoteMachine
metadata:
  name: remote-test-0
  namespace: default
spec:
  address: {{ .Address }}
  user: root
  provisionJob:
    sshCommand: "ssh -o \"StrictHostKeyChecking=no\""
    scpCommand: "scp -o \"StrictHostKeyChecking=no\""
    jobSpecTemplate:
      spec:
        template:
          spec:
            containers:
              - name: ssh
                image: makhov/alpine-ssh:latest
                volumeMounts:
                  - name: ssh-key
                    mountPath: /root/.ssh
                    readOnly: true
            volumes:
              - name: ssh-key
                secret: 
                  secretName: footloose-key
                  items: 
                    - key: id_rsa
                      path: id_rsa
                      mode: 0600
            restartPolicy: Never
---
apiVersion: v1
kind: Secret
metadata:
  name:  footloose-key
  namespace: default
data:
   id_rsa: {{ .SSHKey }}
type: Opaque
`
