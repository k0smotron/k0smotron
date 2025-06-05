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

package capidocker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/cluster-api/api/v1beta1"

	controlplanev1beta1 "github.com/k0sproject/k0smotron/api/controlplane/v1beta1"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/k0sproject/k0smotron/inttest/util"
	"github.com/stretchr/testify/suite"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	bootstrapv1 "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/secret"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type CAPIDockerSuite struct {
	suite.Suite
	client           *kubernetes.Clientset
	restConfig       *rest.Config
	clusterYamlsPath string
	ctx              context.Context
}

func TestCAPIDockerSuite(t *testing.T) {
	s := CAPIDockerSuite{}
	suite.Run(t, &s)
}

func (s *CAPIDockerSuite) SetupSuite() {
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

func (s *CAPIDockerSuite) TestCAPIDocker() {
	s.prepareCerts()
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
	// Apply the child cluster objects
	s.applyClusterObjects()
	s.T().Log("cluster objects applied, waiting for cluster to be ready")

	// Wait for the cluster to be ready
	// Wait to see the CP pods ready
	s.Require().NoError(util.WaitForStatefulSet(s.ctx, s.client, "kmc-docker-test", "default"))

	s.checkControlPlaneStatus(s.ctx, s.restConfig)
	s.checkClusterIDAnnotation(s.ctx)

	s.T().Log("Starting portforward")
	fw, err := util.GetPortForwarder(s.restConfig, "kmc-docker-test-0", "default", 30443)
	s.Require().NoError(err)

	go fw.Start(s.Require().NoError)
	defer fw.Close()

	<-fw.ReadyChan

	localPort, err := fw.LocalPort()
	s.Require().NoError(err)
	s.T().Log("waiting to see admin kubeconfig secret")
	s.Require().NoError(util.WaitForSecret(s.ctx, s.client, "docker-test-kubeconfig", "default"))

	kmcKC, err := util.GetKMCClientSet(s.ctx, s.client, "docker-test", "default", localPort)
	s.Require().NoError(err)

	s.T().Log("waiting for node to be ready")
	s.Require().NoError(util.WaitForNodeReadyStatus(s.ctx, kmcKC, "docker-test-0", corev1.ConditionTrue))
	node, err := kmcKC.CoreV1().Nodes().Get(s.ctx, "docker-test-0", metav1.GetOptions{})
	s.Require().NoError(err)
	s.Require().Equal("v1.31.1+k0s", node.Status.NodeInfo.KubeletVersion)
	fooLabel, ok := node.Labels["k0sproject.io/foo"]
	s.Require().True(ok)
	s.Require().Equal("bar", fooLabel)

	s.T().Log("verifying pod cidrs")
	k0sConfig, err := exec.Command("kubectl", "exec", "kmc-docker-test-0", "--", "cat", "/etc/k0s/k0s.yaml").Output()
	s.Require().NoError(err)
	s.Require().True(strings.Contains(string(k0sConfig), "192.168.0.0/16"))
	s.T().Log("verifying cloud-init extras")
	preStartFile, err := getDockerNodeFile("docker-test-0", "/tmp/pre-start")
	s.Require().NoError(err)
	s.Require().Equal("pre-start", preStartFile)
	postStartFile, err := getDockerNodeFile("docker-test-0", "/tmp/post-start")
	s.Require().NoError(err)
	s.Require().Equal("post-start", postStartFile)
	extraFile, err := getDockerNodeFile("docker-test-0", "/tmp/test-file")
	s.Require().NoError(err)
	s.Require().Equal("test-file", extraFile)

}

func (s *CAPIDockerSuite) prepareCerts() {
	certificates := secret.NewCertificatesForInitialControlPlane(&bootstrapv1.ClusterConfiguration{})
	err := certificates.Generate()
	s.Require().NoError(err, "failed to generate certificates")

	for _, certificate := range certificates {
		certificate.Generated = false
		certSecret := certificate.AsSecret(client.ObjectKey{Namespace: "default", Name: "docker-test"}, metav1.OwnerReference{})
		if _, err := s.client.CoreV1().Secrets("default").Create(s.ctx, certSecret, metav1.CreateOptions{}); err != nil {
			s.Require().NoError(err)
		}
	}
}

func (s *CAPIDockerSuite) applyClusterObjects() {
	// Exec via kubectl
	out, err := exec.Command("kubectl", "apply", "-f", s.clusterYamlsPath).CombinedOutput()
	s.Require().NoError(err, "failed to apply cluster objects: %s", string(out))
}

func (s *CAPIDockerSuite) deleteCluster() {
	// Exec via kubectl
	_, _ = exec.Command("kubectl", "delete", "secret", "docker-test-ca", "docker-test-etcd", "docker-test-proxy", "docker-test-sa").CombinedOutput()

	out, err := exec.Command("kubectl", "delete", "-f", s.clusterYamlsPath).CombinedOutput()
	s.Require().NoError(err, "failed to delete cluster objects: %s", string(out))

	_, _ = exec.Command("kubectl", "delete", "pvc", "etcd-data-kmc-docker-test-etcd-0", "kmc-docker-test-kmc-docker-test-0").CombinedOutput()
}

func (s *CAPIDockerSuite) checkControlPlaneStatus(ctx context.Context, rc *rest.Config) {

	crdConfig := *rc
	crdConfig.ContentConfig.GroupVersion = &controlplanev1beta1.GroupVersion
	crdConfig.APIPath = "/apis"
	crdConfig.NegotiatedSerializer = serializer.NewCodecFactory(scheme.Scheme)
	crdConfig.UserAgent = rest.DefaultKubernetesUserAgent()
	crdRestClient, err := rest.UnversionedRESTClientFor(&crdConfig)
	s.Require().NoError(err)

	var finalKcp controlplanev1beta1.K0smotronControlPlane
	err = wait.PollUntilContextCancel(ctx, 1*time.Second, true, func(ctx context.Context) (bool, error) {
		var kcp controlplanev1beta1.K0smotronControlPlane
		err = crdRestClient.
			Get().
			Resource("k0smotroncontrolplanes").
			Name("docker-test-cp").
			Namespace("default").
			Do(ctx).
			Into(&kcp)

		finalKcp = kcp
		return kcp.Status.Ready, nil
	})

	s.Require().NoError(err)

	// Verify version format in status
	// spec.version is v1.31.2 (without -k0s suffix), so status.version should also be v1.31.2
	s.T().Logf("K0smotronControlPlane status.version: %s", finalKcp.Status.Version)
	s.T().Logf("K0smotronControlPlane status.k0sVersion: %s", finalKcp.Status.K0sVersion)
	s.Require().Equal("v1.31.2", finalKcp.Status.Version, "status.version should match spec.version format without -k0s suffix")
	s.Require().Contains(finalKcp.Status.K0sVersion, "-k0s.", "status.k0sVersion should contain the full k0s version with -k0s suffix")
}

func (s *CAPIDockerSuite) checkClusterIDAnnotation(ctx context.Context) {
	err := wait.PollUntilContextCancel(ctx, 1*time.Second, true, func(ctx context.Context) (bool, error) {
		var cluster v1beta1.Cluster
		_ = s.client.RESTClient().
			Get().
			AbsPath("/apis/cluster.x-k8s.io/v1beta1/namespaces/default/clusters/docker-test").
			Do(ctx).
			Into(&cluster)

		s.T().Log(cluster)
		clusterIDAnnotation, found := cluster.GetAnnotations()[controlplanev1beta1.K0sClusterIDAnnotation]
		return found && strings.Contains(clusterIDAnnotation, "kube-system"), nil
	})

	s.Require().NoError(err)
}

func getDockerNodeFile(nodeName string, path string) (string, error) {
	output, err := exec.Command("docker", "exec", nodeName, "cat", path).Output()
	if err != nil {
		return "", fmt.Errorf("failed to get file %s from node %s: %w", path, nodeName, err)
	}

	return string(string(output)), nil
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
    kind: K0smotronControlPlane
    name: docker-test-cp
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: DockerCluster
    name: docker-test
---
apiVersion: controlplane.cluster.x-k8s.io/v1beta1
kind: K0smotronControlPlane
metadata:
  name: docker-test-cp
spec:
  version: v1.31.2
  certificateRefs:
    - name: docker-test-ca
      type: ca
    - name: docker-test-proxy
      type: proxy
    - name: docker-test-sa
      type: sa
    - name: docker-test-etcd
      type: etcd
  persistence:
    type: pvc
    persistentVolumeClaim:
      spec:
        accessModes:
          - ReadWriteOnce
        resources:
          requests:
            storage: 50Mi
  service:
    type: NodePort
  k0sConfig:
    apiVersion: k0s.k0sproject.io/v1beta1
    kind: ClusterConfig
    spec:
      telemetry:
        enabled: false
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
  name:  docker-test-0
  namespace: default
spec:
  version: v1.31.1
  clusterName: docker-test
  bootstrap:
    configRef:
      apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
      kind: K0sWorkerConfig
      name: docker-test-0
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: DockerMachine
    name: docker-test-0
---
apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
kind: K0sWorkerConfig
metadata:
  name: docker-test-0
  namespace: default
spec:
  # version is deliberately different to be able to verify we actually pick it up :)
  version: v1.31.1+k0s.0
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
  name: docker-test-0
  namespace: default
spec:
  customImage: kindest/node:v1.31.0
`
