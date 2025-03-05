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

package jointoken

import (
	"context"
	"testing"
	"time"

	"github.com/k0sproject/k0s/inttest/common"
	"github.com/k0sproject/k0s/pkg/kubernetes/watch"
	"github.com/stretchr/testify/suite"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/k0smotron/k0smotron/inttest/util"
)

type JoinTokenSuite struct {
	common.FootlooseSuite
}

func (s *JoinTokenSuite) TestK0sGetsUp() {
	s.T().Log("starting k0s")
	s.Require().NoError(s.InitController(0, "--disable-components=metrics-server"))
	s.Require().NoError(s.RunWorkers())

	kc, err := s.KubeClient(s.ControllerNode(0))
	s.Require().NoError(err)
	rc, err := s.GetKubeConfig(s.ControllerNode(0))
	s.Require().NoError(err)

	err = s.WaitForNodeReady(s.WorkerNode(0), kc)
	s.NoError(err)

	s.Require().NoError(s.ImportK0smotronImages(s.Context()))

	s.T().Log("deploying k0smotron operator")
	s.Require().NoError(util.InstallK0smotronOperator(s.Context(), kc, rc))
	s.Require().NoError(common.WaitForDeployment(s.Context(), kc, "k0smotron-controller-manager", "k0smotron"))

	s.T().Log("deploying k0smotron cluster")
	s.createK0smotronCluster(s.Context(), kc)
	s.Require().NoError(common.WaitForStatefulSet(s.Context(), kc, "kmc-kmc-test", "kmc-test"))

	s.T().Log("deploying JoinTokenRequest")
	s.createJoinTokenRequest(s.Context(), kc)

	s.Require().NoError(s.WaitForSecret(s.Context(), kc, "jtr-test", "jtr-test"))

	s.T().Log("removing cluster")
	s.deleteK0smotronCluster(s.Context(), kc)

	s.T().Log("checking if JoinTokenRequest is deleted")
	err = wait.PollUntilContextCancel(s.Context(), 100*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		res := kc.RESTClient().Get().AbsPath("/apis/k0smotron.io/v1beta1/namespaces/jtr-test/jointokenrequests/jtr-test").Do(s.Context())

		var statusCode int
		res.StatusCode(&statusCode)

		return statusCode == 404, nil
	})
	s.Require().NoError(err)
}

func TestJoinTokenSuite(t *testing.T) {
	s := JoinTokenSuite{
		common.FootlooseSuite{
			ControllerCount:                 1,
			WorkerCount:                     1,
			K0smotronWorkerCount:            1,
			K0smotronImageBundleMountPoints: []string{"/dist/bundle.tar"},
		},
	}
	suite.Run(t, &s)
}

func (s *JoinTokenSuite) createK0smotronCluster(ctx context.Context, kc *kubernetes.Clientset) {
	// create K0smotron namespace
	_, err := kc.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kmc-test",
		},
	}, metav1.CreateOptions{})
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

func (s *JoinTokenSuite) createJoinTokenRequest(ctx context.Context, kc *kubernetes.Clientset) {
	_, err := kc.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "jtr-test",
		},
	}, metav1.CreateOptions{})
	s.Require().NoError(err)

	jtr := []byte(`
	{
		"apiVersion": "k0smotron.io/v1beta1",
		"kind": "JoinTokenRequest",
		"metadata": {
		  "name": "jtr-test",
		  "namespace": "jtr-test"
		},
		"spec": {
			"clusterRef":{
				"name": "kmc-test",
				"namespace": "kmc-test"
			}
		}
	}`)

	res := kc.RESTClient().Post().AbsPath("/apis/k0smotron.io/v1beta1/namespaces/jtr-test/jointokenrequests").Body(jtr).Do(ctx)
	s.Require().NoError(res.Error())
}

func (s *JoinTokenSuite) deleteK0smotronCluster(ctx context.Context, kc *kubernetes.Clientset) {
	res := kc.RESTClient().Delete().AbsPath("/apis/k0smotron.io/v1beta1/namespaces/kmc-test/clusters/kmc-test").Do(ctx)
	s.Require().NoError(res.Error())
}

func (s *JoinTokenSuite) WaitForSecret(ctx context.Context, clients kubernetes.Interface, name, namespace string) error {
	return watch.FromClient[*corev1.SecretList, corev1.Secret](clients.CoreV1().Secrets(namespace)).
		WithObjectName(name).
		Until(ctx, func(secret *corev1.Secret) (done bool, err error) {
			if secret.Data["token"] != nil && secret.Labels["k0smotron.io/cluster-uid"] != "" {
				return true, nil
			}

			return false, nil
		})
}
