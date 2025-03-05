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

package hacontroller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/suite"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	bootstrapv1 "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/secret"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/k0smotron/k0smotron/inttest/util"
	"github.com/k0sproject/k0s/inttest/common"
)

type HAControllerSecretSuite struct {
	common.FootlooseSuite
}

func (s *HAControllerSecretSuite) TestK0sGetsUp() {
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

	s.T().Log("deploying postgres")
	s.Require().NoError(util.CreateFromYAML(s.Context(), kc, rc, "postgresql.yaml"))
	s.Require().NoError(common.WaitForDeployment(s.Context(), kc, "postgres", "default"))

	s.T().Log("deploying k0smotron operator")
	s.Require().NoError(util.InstallK0smotronOperator(s.Context(), kc, rc))
	s.Require().NoError(common.WaitForDeployment(s.Context(), kc, "k0smotron-controller-manager", "k0smotron"))

	s.T().Log("deploying k0smotron cluster")
	s.createK0smotronClusterWithSecretRef(s.Context(), kc)
	s.Require().NoError(common.WaitForStatefulSet(s.Context(), kc, "kmc-kmc-test-secret", "kmc-test"))

	s.T().Log("Generating k0smotron join token")
	token, err := util.GetJoinToken(kc, rc, "kmc-kmc-test-secret-0", "kmc-test")
	s.Require().NoError(err)

	s.T().Log("joining worker to k0smotron cluster")
	s.Require().NoError(s.RunWithToken(s.K0smotronNode(0), token))

	s.T().Log("Starting portforward")
	pod := s.getPod(s.Context(), kc)

	fw, err := util.GetPortForwarder(rc, pod.Name, pod.Namespace, 30443)
	s.Require().NoError(err)
	go fw.Start(s.Require().NoError)
	defer fw.Close()

	<-fw.ReadyChan
	s.T().Log("portforward ready")
	s.T().Log("getting child clientset")
	kmcKC, err := util.GetKMCClientSet(s.Context(), kc, "kmc-test-secret", "kmc-test", 30443)
	s.Require().NoError(err)
	s.T().Log("waiting for node to be ready")
	s.Require().NoError(s.WaitForNodeReady(s.K0smotronNode(0), kmcKC))
}

func TestHAControllerSecretSuite(t *testing.T) {
	s := HAControllerSecretSuite{
		common.FootlooseSuite{
			ControllerCount:                 1,
			WorkerCount:                     1,
			K0smotronWorkerCount:            1,
			K0smotronImageBundleMountPoints: []string{"/dist/bundle.tar"},
		},
	}
	suite.Run(t, &s)
}

func (s *HAControllerSecretSuite) prepareCerts(kc *kubernetes.Clientset) {
	certificates := secret.NewCertificatesForInitialControlPlane(&bootstrapv1.ClusterConfiguration{})
	err := certificates.Generate()
	s.Require().NoError(err, "failed to generate certificates")

	for _, certificate := range certificates {
		certificate.Generated = false
		certSecret := certificate.AsSecret(client.ObjectKey{Namespace: "kmc-test", Name: "kmc-test"}, metav1.OwnerReference{})
		if _, err := kc.CoreV1().Secrets("kmc-test").Create(s.Context(), certSecret, metav1.CreateOptions{}); err != nil {
			s.Require().NoError(err)
		}
	}
}

func (s *HAControllerSecretSuite) createK0smotronClusterWithSecretRef(ctx context.Context, kc *kubernetes.Clientset) {
	_, err := kc.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kmc-test",
		},
	}, metav1.CreateOptions{})
	s.Require().NoError(err)

	s.prepareCerts(kc)

	// create K0smotron namespace
	_, err = kc.CoreV1().Secrets("kmc-test").Create(ctx, &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "postgres-dsn",
			Namespace: "kmc-test",
		},
		StringData: map[string]string{
			"K0SMOTRON_KINE_DATASOURCE_URL": "postgres://postgres:postgres@postgres.default:5432/kine?sslmode=disable",
		},
	}, metav1.CreateOptions{})
	s.Require().NoError(err)

	kmc := []byte(`
	{
		"apiVersion": "k0smotron.io/v1beta1",
		"kind": "Cluster",
		"metadata": {
		  "name": "kmc-test-secret",
		  "namespace": "kmc-test"
		},
		"spec": {
			"replicas": 3,
			"version": "v1.31.5-k0s.0",
			"service":{
				"type": "NodePort"
			},
			"kineDataSourceSecretName": "postgres-dsn",
			"certificateRefs": [{"name": "kmc-test-ca","type": "ca"}, {"name": "kmc-test-proxy", "type": "proxy"}, {"name": "kmc-test-sa", "type": "sa"}],
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

func (s *HAControllerSecretSuite) getPod(ctx context.Context, kc *kubernetes.Clientset) corev1.Pod {
	pods, err := kc.CoreV1().Pods("kmc-test").List(
		ctx,
		metav1.ListOptions{FieldSelector: "status.phase=Running"})
	s.Require().NoError(err, "failed to list kmc-test pods")
	s.Require().Equal(3, len(pods.Items), "expected 1 kmc-test pod, got %d", len(pods.Items))

	return pods.Items[0]
}
