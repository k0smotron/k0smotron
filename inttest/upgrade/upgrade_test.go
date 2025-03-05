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

package upgrade

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/k0smotron/k0smotron/internal/exec"
	"github.com/k0smotron/k0smotron/inttest/util"
	"github.com/k0sproject/k0s/inttest/common"
	"github.com/stretchr/testify/suite"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"

	km "github.com/k0smotron/k0smotron/api/k0smotron.io/v1beta1"
)

type UpgradeSuite struct {
	common.FootlooseSuite
}

func TestUpgradeSuite(t *testing.T) {
	s := UpgradeSuite{
		common.FootlooseSuite{
			ControllerCount:                 1,
			WorkerCount:                     1,
			K0smotronWorkerCount:            1,
			K0smotronImageBundleMountPoints: []string{"/dist/bundle.tar"},
		},
	}
	suite.Run(t, &s)
}

// TestK0smotronUpgrade validates a version upgrade of the k0smotron operator. This validation consists of:
// - Check the status persists between upgrades.
// - Check the k0smotron cluster is still accessible between upgrades.
// - Check the status of the k0smotron cluster is Ready and with it its related resources after upgrade.
func (s *UpgradeSuite) TestK0smotronUpgrade() {
	s.T().Log("starting k0s")
	s.Require().NoError(s.InitController(0, "--disable-components=metrics-server"))
	s.Require().NoError(s.RunWorkers())

	kc, err := s.KubeClient(s.ControllerNode(0))
	s.Require().NoError(err)
	rc, err := s.GetKubeConfig(s.ControllerNode(0))
	s.Require().NoError(err)

	err = s.WaitForNodeReady(s.WorkerNode(0), kc)
	s.NoError(err)

	s.T().Log("deploying stable k0smotron operator")
	s.Require().NoError(util.InstallStableK0smotronOperator(s.Context(), kc, rc))
	s.Require().NoError(common.WaitForDeployment(s.Context(), kc, "k0smotron-controller-manager", "k0smotron"))

	s.T().Log("deploying k0smotron cluster")
	s.createK0smotronCluster(s.Context(), kc)
	s.Require().NoError(common.WaitForStatefulSet(s.Context(), kc, "kmc-kmc-test", "kmc-test"))

	pod, err := kc.CoreV1().Pods("kmc-test").Get(s.Context(), "kmc-kmc-test-0", metav1.GetOptions{})
	s.Require().NoError(err)

	// We create state to subsequently validate that it persists between upgrades
	s.addState(s.Context(), pod.Spec.Containers[0].VolumeMounts[1].MountPath, kc, rc)

	s.T().Log("deploying development k0smotron operator")
	s.Require().NoError(s.ImportK0smotronImages(s.Context()))
	s.Require().NoError(util.ApplyFromYAML(s.Context(), kc, rc, os.Getenv("K0SMOTRON_INSTALL_YAML")))
	s.Require().NoError(util.WaitForRolloutCompleted(s.Context(), kc, "k0smotron-controller-manager", "k0smotron"))

	s.forceControllerRecreation(s.Context(), pod.Name, kc)
	s.checkStatePersists(s.Context(), pod.Spec.Containers[0].VolumeMounts[1].MountPath, kc, rc)

	s.Require().NoError(common.WaitForStatefulSet(s.Context(), kc, "kmc-kmc-test", "kmc-test"))

	s.T().Log("Starting portforward")
	fw, err := util.GetPortForwarder(rc, "kmc-kmc-test-0", "kmc-test", 30443)
	s.Require().NoError(err)

	go fw.Start(s.Require().NoError)
	defer fw.Close()

	<-fw.ReadyChan

	localPort, err := fw.LocalPort()
	s.Require().NoError(err)
	kmcKC, err := util.GetKMCClientSet(s.Context(), kc, "kmc-test", "kmc-test", localPort)
	s.Require().NoError(err)

	err = wait.PollUntilContextCancel(s.Context(), 100*time.Millisecond, true, func(ctx context.Context) (done bool, err error) {
		_, err = kmcKC.CoreV1().Namespaces().Get(s.Context(), "test-ns-cm", metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		return true, nil
	})
	s.Require().NoError(err)

	time.Sleep(time.Second)
	_, err = kmcKC.CoreV1().ConfigMaps("default").Get(s.Context(), "test-old-cm", metav1.GetOptions{})
	s.Require().NoError(err)

	result, err := kc.RESTClient().Get().AbsPath("/apis/k0smotron.io/v1beta1/namespaces/kmc-test/clusters/kmc-test").DoRaw(s.Context())
	s.Require().NoError(err)

	// If the status of the Owner is correct, the status of all resources
	// that have OwnerRef to this resource should also be correct.
	var kmc km.Cluster
	err = yaml.Unmarshal(result, &kmc)
	s.Require().NoError(err)
	s.Require().True(kmc.Status.Ready)
}

func (s *UpgradeSuite) checkStatePersists(ctx context.Context, mountedPath string, kc *kubernetes.Clientset, rc *rest.Config) {
	successfulOutput := "File exists"
	output := ""
	cmd := fmt.Sprintf("test -f %s/manifests/mystack/manifest.yaml && echo \"%s\" || echo \"File does not exist\"", mountedPath, successfulOutput)
	err := wait.PollUntilContextCancel(s.Context(), time.Second, true, func(_ context.Context) (done bool, err error) {
		output, err = exec.PodExecCmdOutput(ctx, kc, rc, "kmc-kmc-test-0", "kmc-test", cmd)
		if err != nil {
			return false, nil
		}

		return true, nil
	})
	s.Require().NoError(err)
	s.Require().Equal(strings.ReplaceAll(output, "\n", ""), successfulOutput)
}

func (s *UpgradeSuite) forceControllerRecreation(ctx context.Context, controllerName string, kc *kubernetes.Clientset) {
	err := kc.CoreV1().Pods("kmc-test").Delete(ctx, controllerName, metav1.DeleteOptions{})
	s.Require().NoError(err)
	s.Require().NoError(common.WaitForPod(s.Context(), kc, controllerName, "kmc-test"))
}

func (s *UpgradeSuite) addState(ctx context.Context, mountedPath string, kc *kubernetes.Clientset, rc *rest.Config) {
	cmd := fmt.Sprintf("mkdir -p %s/manifests/mystack && k0s kubectl create ns test-ns --dry-run=client -oyaml > %s/manifests/mystack/manifest.yaml", mountedPath, mountedPath)
	_, err := exec.PodExecCmdOutput(ctx, kc, rc, "kmc-kmc-test-0", "kmc-test", cmd)
	s.Require().NoError(err)

	_, err = exec.PodExecCmdOutput(ctx, kc, rc, "kmc-kmc-test-0", "kmc-test", "k0s kubectl create cm test-old-cm --from-literal=key1=config1")
	s.Require().NoError(err)
}

func (s *UpgradeSuite) createK0smotronCluster(ctx context.Context, kc *kubernetes.Clientset) {
	// create K0smotron namespace
	_, err := kc.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kmc-test",
		},
	}, metav1.CreateOptions{})
	s.Require().NoError(err)

	// create manifests
	_, err = kc.CoreV1().Secrets("kmc-test").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "manifest-secret",
		},
		Data: map[string][]byte{
			"manifest.yaml": []byte(`---
apiVersion: v1
kind: Namespace
metadata:
  name: test-ns-secret
`),
		}}, metav1.CreateOptions{})
	s.Require().NoError(err)

	_, err = kc.CoreV1().ConfigMaps("kmc-test").Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "manifest-cm",
		},
		Data: map[string]string{
			"manifest.yaml": `---
apiVersion: v1
kind: Namespace
metadata:
  name: test-ns-cm
`,
		}}, metav1.CreateOptions{})
	s.Require().NoError(err)

	kmc := []byte(`
	{
		"apiVersion": "k0smotron.io/v1beta1",
		"kind": "Cluster",
		"metadata": {
		  "name": "kmc-test",
		  "namespace": "kmc-test"
		},
		"spec": {
   			"version": "v1.31.5-k0s.0",
			"service":{
				"type": "NodePort"
			},
			"etcd": {
				"defragJob": {
					"enabled": true,
					"schedule": "* * * * *"
				}
			},
			"manifests": [
				{
					"name": "secret",
					"secret": { "secretName": "manifest-secret" }
				},
				{
					"name": "configmap",
					"configMap": { "name": "manifest-cm" }
				}
			],
			"mounts": [
				{
					"path": "/tmp/test",
					"configMap": { "name": "manifest-cm" }
				}
			],
			"resources": {
				"requests": {
					"cpu": "100m",
					"memory": "100Mi"
				}
			},
			"persistence": {
				"type": "pvc",
				"persistentVolumeClaim": {
					"metadata": {
						"name": "kmc-volume-test"
					},
					"spec": {
						"accessModes": ["ReadWriteOnce"],
						"storageClassName": "local-path",
						"resources": {
							"requests": {
								"storage": "200Mi"
							}
						}
					}
				}
			},
			"k0sConfig": {
				"apiVersion": "k0s.k0sproject.io/v1beta1",
				"kind": "ClusterConfig",
				"spec": {
					"telemetry": {"enabled": false}
				}
			}
		}
	  }
`)

	res := kc.RESTClient().Post().AbsPath("/apis/k0smotron.io/v1beta1/namespaces/kmc-test/clusters").Body(kmc).Do(ctx)
	s.Require().NoError(res.Error())
}
