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

package configupdate

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/k0sproject/k0s/inttest/common"
	km "github.com/k0sproject/k0smotron/api/k0smotron.io/v1beta1"
	"github.com/k0sproject/k0smotron/inttest/util"

	"github.com/stretchr/testify/suite"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

type ConfigUpdateSuite struct {
	common.FootlooseSuite
}

func (s *ConfigUpdateSuite) TestK0sGetsUp() {
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

	s.checkClusterStatus(s.Context(), rc)

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

	err = common.WaitForDaemonSet(s.Context(), kmcKC, "kube-router")
	s.Require().NoError(err)

	s.T().Log("updating k0smotron cluster")
	s.updateK0smotronCluster(s.Context(), rc)

	// nolint:staticcheck
	err = wait.PollImmediateUntilWithContext(s.Context(), 1*time.Second, func(ctx context.Context) (bool, error) {
		cm, err := kmcKC.CoreV1().ConfigMaps("kube-system").Get(s.Context(), "kube-router-cfg", metav1.GetOptions{})
		if err != nil {
			return false, nil
		}

		return strings.Contains(cm.Data["cni-conf.json"], `"mtu": 1300`), nil
	})
	s.Require().NoError(err)
}

func TestConfigUpdateSuite(t *testing.T) {
	s := ConfigUpdateSuite{
		common.FootlooseSuite{
			ControllerCount:                 1,
			WorkerCount:                     1,
			K0smotronWorkerCount:            1,
			K0smotronImageBundleMountPoints: []string{"/dist/bundle.tar"},
		},
	}
	suite.Run(t, &s)
}

func (s *ConfigUpdateSuite) checkClusterStatus(ctx context.Context, rc *rest.Config) {

	crdConfig := *rc
	crdConfig.ContentConfig.GroupVersion = &km.GroupVersion
	crdConfig.APIPath = "/apis"
	crdConfig.NegotiatedSerializer = serializer.NewCodecFactory(scheme.Scheme)
	crdConfig.UserAgent = rest.DefaultKubernetesUserAgent()
	crdRestClient, err := rest.UnversionedRESTClientFor(&crdConfig)
	s.Require().NoError(err)

	// nolint:staticcheck
	err = wait.PollImmediateUntilWithContext(ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
		var kmc km.Cluster
		err = crdRestClient.
			Get().
			Resource("clusters").
			Name("kmc-test").
			Namespace("kmc-test").
			Do(ctx).
			Into(&kmc)

		return kmc.Status.Ready, nil
	})

	s.Require().NoError(err)
}

func (s *ConfigUpdateSuite) updateK0smotronCluster(ctx context.Context, rc *rest.Config) {
	crdConfig := *rc
	crdConfig.ContentConfig.GroupVersion = &km.GroupVersion
	crdConfig.APIPath = "/apis"
	crdConfig.NegotiatedSerializer = serializer.NewCodecFactory(scheme.Scheme)
	crdConfig.UserAgent = rest.DefaultKubernetesUserAgent()
	crdRestClient, err := rest.UnversionedRESTClientFor(&crdConfig)
	s.Require().NoError(err)

	patch := `[{"op": "replace", "path": "/spec/k0sConfig/spec/network/kuberouter/mtu", "value": 1300}]`
	res := crdRestClient.
		Patch(types.JSONPatchType).
		Resource("clusters").
		Name("kmc-test").
		Namespace("kmc-test").
		Body([]byte(patch)).
		Do(ctx)
	s.Require().NoError(res.Error())
}

func (s *ConfigUpdateSuite) createK0smotronCluster(ctx context.Context, kc *kubernetes.Clientset) {
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
			"resources": {
				"requests": {
					"cpu": "100m",
					"memory": "100Mi"
				}
			},
			"k0sConfig": {
				"apiVersion": "k0s.k0sproject.io/v1beta1",
				"kind": "ClusterConfig",
				"spec": {
					"telemetry": {"enabled": false},
					"network": {
						"kuberouter": {
							"autoMTU": false,
							"mtu": 1200
						}
					}
				}
			}
		}
	  }
`)

	res := kc.RESTClient().Post().AbsPath("/apis/k0smotron.io/v1beta1/namespaces/kmc-test/clusters").Body(kmc).Do(ctx)
	s.Require().NoError(res.Error())
}
