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

package capidockermachinedeployment

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/k0sproject/k0smotron/inttest/util"
	"github.com/stretchr/testify/suite"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"

	cpv1beta1 "github.com/k0sproject/k0smotron/api/controlplane/v1beta1"
)

type CAPIDockerSuite struct {
	suite.Suite
	client     *kubernetes.Clientset
	restConfig *rest.Config
	ctx        context.Context
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

	s.ctx, _ = util.NewSuiteContext(s.T())
}

func (s *CAPIDockerSuite) TestCAPIDocker() {
	// Test with k0s suffix (default/backward compatible)
	s.Run("WithK0sSuffix", func() {
		s.testCAPIDockerWithVersion("docker-md-test", "v1.27.2-k0s.0", "../../config/samples/capi/docker/cluster-with-machinedeployment.yaml", true)
	})

	// Test without k0s suffix
	s.Run("WithoutK0sSuffix", func() {
		s.testCAPIDockerWithVersion("docker-md-test-no-suffix", "v1.27.2", "../../config/samples/capi/docker/cluster-with-machinedeployment-no-suffix.yaml", false)
	})
}

func (s *CAPIDockerSuite) testCAPIDockerWithVersion(clusterName, expectedVersion, yamlPath string, expectK0sSuffix bool) {
	// Apply the child cluster objects
	s.applyClusterObjects(yamlPath)
	defer func() {
		keep := os.Getenv("KEEP_AFTER_TEST")
		if keep == "true" {
			return
		}
		if keep == "on-failure" && s.T().Failed() {
			return
		}
		s.T().Log("Deleting cluster objects")
		s.Require().NoError(util.DeleteCluster(clusterName))
	}()
	s.T().Log("cluster objects applied, waiting for cluster to be ready")

	// Wait for the cluster to be ready
	// Wait to see the CP pods ready
	s.Require().NoError(util.WaitForStatefulSet(s.ctx, s.client, fmt.Sprintf("kmc-%s", clusterName), "default"))

	// Verify K0smotronControlPlane version format
	s.T().Log("Verifying K0smotronControlPlane version format")
	s.verifyK0smotronControlPlaneVersionFormat(clusterName, expectedVersion, expectK0sSuffix)

	s.T().Log("Starting portforward")
	fw, err := util.GetPortForwarder(s.restConfig, fmt.Sprintf("kmc-%s-0", clusterName), "default", 30443)
	s.Require().NoError(err)

	go fw.Start(s.Require().NoError)
	defer fw.Close()

	<-fw.ReadyChan

	localPort, err := fw.LocalPort()
	s.Require().NoError(err)
	s.T().Log("waiting to see admin kubeconfig secret")
	s.Require().NoError(util.WaitForSecret(s.ctx, s.client, fmt.Sprintf("%s-kubeconfig", clusterName), "default"))
	kmcKC, err := util.GetKMCClientSet(s.ctx, s.client, clusterName, "default", localPort)
	s.Require().NoError(err)

	s.T().Log("waiting for 2 nodes to be ready")
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Minute)
	defer cancel()
	err = util.Poll(ctx, func(ctx context.Context) (done bool, err error) {
		nodes, err := kmcKC.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		readyCount := 0
		for _, node := range nodes.Items {
			for _, cond := range node.Status.Conditions {
				if cond.Type == corev1.NodeReady {
					if cond.Status == corev1.ConditionTrue {
						readyCount++
					}
					break
				}
			}
		}
		if readyCount != 2 {
			return false, nil
		}
		return true, nil
	})
	s.Require().NoError(err)
	// Check that the MD gets to ready state
	s.T().Log("waiting for machinedeployment to be ready")
	s.Require().NoError(wait.PollUntilContextCancel(ctx, 1*time.Second, true, func(ctx context.Context) (done bool, err error) {
		// Get the MachineDeployment
		md := &clusterv1.MachineDeployment{}
		err = s.client.RESTClient().
			Get().
			AbsPath("/apis/cluster.x-k8s.io/v1beta1").
			Resource("machinedeployments").
			Namespace("default").
			Name(clusterName).
			Do(ctx).Into(md)
		if err != nil {
			return false, err
		}
		if md.Status.ReadyReplicas != 2 {
			return false, nil
		}
		return true, nil
	}))
}

func (s *CAPIDockerSuite) applyClusterObjects(yamlPath string) {
	// Exec via kubectl
	out, err := exec.Command("kubectl", "apply", "-f", yamlPath).CombinedOutput()
	s.Require().NoError(err, "failed to apply cluster objects: %s", string(out))
}

// getK0smotronControlPlaneStatus retrieves the K0smotronControlPlane status
func (s *CAPIDockerSuite) getK0smotronControlPlaneStatus(name, namespace string) (*cpv1beta1.K0smotronControlPlaneStatus, error) {
	kcp := &cpv1beta1.K0smotronControlPlane{}
	err := s.client.RESTClient().
		Get().
		AbsPath("/apis/controlplane.cluster.x-k8s.io/v1beta1").
		Resource("k0smotroncontrolplanes").
		Namespace(namespace).
		Name(name).
		Do(s.ctx).Into(kcp)
	if err != nil {
		return nil, err
	}
	return &kcp.Status, nil
}

// waitForK0smotronControlPlaneStatus waits for the K0smotronControlPlane status to be populated
func (s *CAPIDockerSuite) waitForK0smotronControlPlaneStatus(clusterName string, timeout time.Duration) (*cpv1beta1.K0smotronControlPlaneStatus, error) {
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()

	var status *cpv1beta1.K0smotronControlPlaneStatus
	err := wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (done bool, err error) {
		status, err = s.getK0smotronControlPlaneStatus(clusterName, "default")
		if err != nil {
			return false, nil // Retry on error
		}
		return status.Version != "", nil
	})

	if err != nil {
		return nil, fmt.Errorf("timeout waiting for K0smotronControlPlane status: %w", err)
	}
	return status, nil
}

// verifyK0smotronControlPlaneVersionFormat verifies that the version format is consistent
func (s *CAPIDockerSuite) verifyK0smotronControlPlaneVersionFormat(clusterName, expectedSpecVersion string, expectK0sSuffix bool) {
	// Wait for status
	status, err := s.waitForK0smotronControlPlaneStatus(clusterName, 2*time.Minute)
	s.Require().NoError(err)

	// Verify version format based on expectK0sSuffix
	if expectK0sSuffix {
		s.Require().Contains(status.Version, "-k0s.")
	} else {
		s.Require().NotContains(status.Version, "-k0s.")
	}

	// Verify version matches expected
	s.Require().Equal(expectedSpecVersion, status.Version)

	// Verify k0sVersion always has suffix
	s.Require().NotEmpty(status.K0sVersion)
	s.Require().Contains(status.K0sVersion, "-k0s.")
}
