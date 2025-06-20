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

package util

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"

	kexec "github.com/k0sproject/k0smotron/internal/exec"

	cpv1beta1 "github.com/k0sproject/k0smotron/api/controlplane/v1beta1"
	"github.com/k0sproject/k0smotron/inttest/util/watch"
	"github.com/sirupsen/logrus"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

func InstallK0smotronOperator(ctx context.Context, kc *kubernetes.Clientset, rc *rest.Config) error {
	err := InstallLocalPathStorage(ctx, kc, rc)
	if err != nil {
		return err
	}

	err = InstallCertManager(ctx, kc, rc)
	if err != nil {
		return err
	}

	err = WaitForDeployment(ctx, kc, "cert-manager-webhook", "cert-manager")
	if err != nil {
		return err
	}

	err = CreateFromYAML(ctx, kc, rc, os.Getenv("K0SMOTRON_INSTALL_YAML"))
	if err != nil {
		return fmt.Errorf("failed to install k0smotron operator: %w", err)
	}

	err = InstallWebhookChecker(ctx, kc, rc)
	if err != nil {
		return fmt.Errorf("failed to install webhook checker: %w", err)
	}

	err = WaitForPod(ctx, kc, "webhook-checker", "k0smotron")
	if err != nil {
		return fmt.Errorf("failed to wait for k0smotron webhook: %w", err)
	}

	return nil
}

func InstallStableK0smotronOperator(ctx context.Context, kc *kubernetes.Clientset, rc *rest.Config) error {
	err := InstallLocalPathStorage(ctx, kc, rc)
	if err != nil {
		return err
	}

	err = InstallCertManager(ctx, kc, rc)
	if err != nil {
		return err
	}

	installFileName, err := dowloadStableK0smotronOperator()
	if err != nil {
		return err
	}

	return CreateFromYAML(ctx, kc, rc, installFileName)
}

func InstallWebhookChecker(ctx context.Context, kc *kubernetes.Clientset, rc *rest.Config) error {
	return CreateFromYAML(ctx, kc, rc, os.Getenv("WEBHOOK_CHECKER_INSTALL_YAML"))
}

func InstallCertManager(ctx context.Context, kc *kubernetes.Clientset, rc *rest.Config) error {
	return CreateFromYAML(ctx, kc, rc, os.Getenv("CERT_MANAGER_INSTALL_YAML"))
}

func InstallLocalPathStorage(ctx context.Context, kc *kubernetes.Clientset, rc *rest.Config) error {
	return CreateFromYAML(ctx, kc, rc, os.Getenv("LOCAL_STORAGE_INSTALL_YAML"))
}

func CreateFromYAML(ctx context.Context, kc *kubernetes.Clientset, rc *rest.Config, filename string) error {
	data, err := os.ReadFile(filename)
	if err != nil {
		return err
	}

	resources, err := ParseManifests(data)
	if err != nil {
		return err
	}

	dc, err := GetDynamicClient(rc)
	if err != nil {
		return err
	}

	return CreateResources(ctx, resources, kc, dc)
}

func ApplyFromYAML(ctx context.Context, kc *kubernetes.Clientset, rc *rest.Config, filename string) error {
	data, err := os.ReadFile(filename)
	if err != nil {
		return err
	}

	resources, err := ParseManifests(data)
	if err != nil {
		return err
	}

	dc, err := GetDynamicClient(rc)
	if err != nil {
		return err
	}

	return applyResources(ctx, resources, kc, dc)
}

func ParseManifests(data []byte) ([]*unstructured.Unstructured, error) {
	var resources []*unstructured.Unstructured

	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var resource map[string]interface{}
	for {
		err := decoder.Decode(&resource)
		if err == io.EOF {
			return resources, nil
		} else if err != nil {
			return nil, err
		}

		item := &unstructured.Unstructured{
			Object: resource,
		}
		if item.GetAPIVersion() != "" && item.GetKind() != "" {
			resources = append(resources, item)
			resource = nil
		}
	}
}

func GetDynamicClient(rc *rest.Config) (*dynamic.DynamicClient, error) {
	client, err := dynamic.NewForConfig(rc)
	if err != nil {
		return nil, err
	}
	return client, nil
}

func getMapper(kc *kubernetes.Clientset) *restmapper.DeferredDiscoveryRESTMapper {
	disc := memory.NewMemCacheClient(discovery.NewDiscoveryClient(kc.RESTClient()))
	return restmapper.NewDeferredDiscoveryRESTMapper(disc)
}

func CreateResources(ctx context.Context, resources []*unstructured.Unstructured, kc *kubernetes.Clientset, client *dynamic.DynamicClient) error {
	mapper := getMapper(kc)
	for _, res := range resources {
		err := retry.OnError(wait.Backoff{
			Steps:    10,
			Duration: 1 * time.Second,
			Factor:   1.0,
			Jitter:   0.1,
		}, func(err error) bool {
			return true
		}, func() error {
			mapping, err := mapper.RESTMapping(
				res.GroupVersionKind().GroupKind(),
				res.GroupVersionKind().Version)

			if err != nil {
				mapper.Reset()
				return fmt.Errorf("getting mapping error: %w", err)
			}

			var drClient dynamic.ResourceInterface
			if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
				drClient = client.Resource(mapping.Resource).Namespace(res.GetNamespace())
			} else {
				drClient = client.Resource(mapping.Resource)
			}

			_, err = drClient.Create(ctx, res, metav1.CreateOptions{})

			return err
		})
		if err != nil {
			return fmt.Errorf("creating %s/%s objects error: %w", res.GroupVersionKind(), res.GetName(), err)
		}
	}
	return nil
}

func applyResources(ctx context.Context, resources []*unstructured.Unstructured, kc *kubernetes.Clientset, client *dynamic.DynamicClient) error {
	mapper := getMapper(kc)
	for _, res := range resources {
		err := retry.OnError(wait.Backoff{
			Steps:    10,
			Duration: 1 * time.Second,
			Factor:   1.0,
			Jitter:   0.1,
		}, func(err error) bool {
			return true
		}, func() error {
			mapping, err := mapper.RESTMapping(
				res.GroupVersionKind().GroupKind(),
				res.GroupVersionKind().Version)

			if err != nil {
				mapper.Reset()
				return fmt.Errorf("getting mapping error: %w", err)
			}

			var drClient dynamic.ResourceInterface
			if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
				drClient = client.Resource(mapping.Resource).Namespace(res.GetNamespace())
			} else {
				drClient = client.Resource(mapping.Resource)
			}

			_, err = drClient.Apply(ctx, res.GetName(), res, metav1.ApplyOptions{Force: true, FieldManager: "application/apply-patch"})

			return err
		})
		if err != nil {
			return fmt.Errorf("applying %s/%s objects error: %w", res.GroupVersionKind(), res.GetName(), err)
		}
	}
	return nil
}

func GetJoinToken(kc *kubernetes.Clientset, rc *rest.Config, name string, namespace string) (string, error) {
	output, err := kexec.PodExecCmdOutput(context.TODO(), kc, rc, name, namespace, "k0s token create --role=worker")
	if err != nil {
		return "", fmt.Errorf("failed to get join token: %s", err)
	}

	return output, nil
}

// GetKMCClientSet returns a kubernetes clientset for the cluster given
// the name and the namespace of the cluster.k0smotron.io
func GetKMCClientSet(ctx context.Context, kc *kubernetes.Clientset, name string, namespace string, port int) (*kubernetes.Clientset, error) {
	secretName := fmt.Sprintf("%s-kubeconfig", name)
	// Wait first to see the secret exists
	if err := WaitForSecret(ctx, kc, secretName, namespace); err != nil {
		return nil, err
	}
	kubeConf, err := kc.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	kmcCfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeConf.Data["value"]))
	if err != nil {
		return nil, err
	}

	// Override the host to point to the port forwarded API server
	kmcCfg.Host = fmt.Sprintf("localhost:%d", port)

	return kubernetes.NewForConfig(kmcCfg)
}

func GetNodeAddress(ctx context.Context, kc *kubernetes.Clientset, node string) (string, error) {
	n, err := kc.CoreV1().Nodes().Get(ctx, node, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	for _, addr := range n.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address, nil
		}
	}

	return "", fmt.Errorf("Node doesn't have an Address of type InternalIP")
}

func WaitForSecret(ctx context.Context, kc *kubernetes.Clientset, name string, namespace string) error {
	// Use apimachinery wait directly as the k0s common polls bit too much and sometimes it results into client side throttling
	// Since it's marked deprecated in a wrong way, there's no replacement for it yet, we'll disable the linter for now
	// nolint:staticcheck
	return wait.PollImmediateUntilWithContext(ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
		secret, err := kc.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})

		if err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
		if err != nil && apierrors.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if secret.Data["value"] != nil && len(secret.Data["value"]) > 0 {
			return true, nil
		}
		return false, nil
	})
}

func GetCluster(ctx context.Context, kc *kubernetes.Clientset, name string, namespace string) (*clusterv1.Cluster, error) {
	url := fmt.Sprintf("apis/cluster.x-k8s.io/v1beta1/namespaces/%s/clusters/%s", namespace, name)

	cluster := &clusterv1.Cluster{}

	err := kc.RESTClient().
		Get().
		AbsPath(url).
		Do(ctx).
		Into(cluster)

	return cluster, err
}

func UpdateCluster(ctx context.Context, kc *kubernetes.Clientset, cluster *clusterv1.Cluster) error {
	url := fmt.Sprintf("apis/cluster.x-k8s.io/v1beta1/namespaces/%s/clusters/%s", cluster.Namespace, cluster.Name)

	clusterJSON, err := json.Marshal(cluster)
	if err != nil {
		return err
	}

	return kc.RESTClient().
		Put().
		AbsPath(url).
		Body(bytes.NewReader(clusterJSON)).
		Do(ctx).
		Into(cluster)

}

func GetK0sControlPlane(ctx context.Context, kc *kubernetes.Clientset, name string, namespace string) (*cpv1beta1.K0sControlPlane, error) {

	url := fmt.Sprintf("apis/controlplane.cluster.x-k8s.io/v1beta1/namespaces/%s/k0scontrolplanes/%s", namespace, name)

	cp := &cpv1beta1.K0sControlPlane{}

	err := kc.RESTClient().
		Get().
		AbsPath(url).
		Do(ctx).
		Into(cp)

	return cp, err
}

func WaitForRolloutCompleted(ctx context.Context, kc *kubernetes.Clientset, name string, namespace string) error {
	newReplicaSetCreated := false
	return watch.Deployments(kc.AppsV1().Deployments(namespace)).
		WithObjectName(name).
		WithErrorCallback(RetryWatchErrors(logrus.Infof)).
		Until(ctx, func(deployment *appsv1.Deployment) (bool, error) {
			if newReplicaSetCreated {
				for _, c := range deployment.Status.Conditions {
					if c.Type == appsv1.DeploymentProgressing {
						newReplicaSetCreated = true
						break
					}
				}
			}

			allReplicasAvailable := deployment.Status.UnavailableReplicas == 0
			rolloutApplied := deployment.Status.ObservedGeneration >= deployment.Generation

			return allReplicasAvailable && rolloutApplied, nil
		})
}

func DeleteCluster(clusterName string) error {
	out, err := exec.Command("kubectl", "delete", "cluster", clusterName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to delete cluster objects: %s", string(out))
	}

	return nil
}

func dowloadStableK0smotronOperator() (string, error) {
	url := "https://docs.k0smotron.io/stable/install.yaml"

	response, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("failed to download k0smotron install file: %v", err)
	}
	defer response.Body.Close()

	installFile, err := os.Create(filepath.Join(os.TempDir(), "install.yaml"))
	if err != nil {
		return "", fmt.Errorf("failed to create k0smotron install file: %v", err)
	}
	defer installFile.Close()

	_, err = io.Copy(installFile, response.Body)
	if err != nil {
		return "", fmt.Errorf("failed to save k0smotron install file: %v", err)
	}
	return installFile.Name(), nil
}

// GetK0smotronControlPlaneStatus retrieves the K0smotronControlPlane status
func GetK0smotronControlPlaneStatus(ctx context.Context, client *kubernetes.Clientset, name, namespace string) (*cpv1beta1.K0smotronControlPlaneStatus, error) {
	kcp := &cpv1beta1.K0smotronControlPlane{}
	err := client.RESTClient().
		Get().
		AbsPath("/apis/controlplane.cluster.x-k8s.io/v1beta1").
		Resource("k0smotroncontrolplanes").
		Namespace(namespace).
		Name(name).
		Do(ctx).Into(kcp)
	if err != nil {
		return nil, err
	}
	return &kcp.Status, nil
}

// WaitForK0smotronControlPlaneStatus waits for the K0smotronControlPlane status to be populated
func WaitForK0smotronControlPlaneStatus(ctx context.Context, client *kubernetes.Clientset, clusterName string, timeout time.Duration) (*cpv1beta1.K0smotronControlPlaneStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var status *cpv1beta1.K0smotronControlPlaneStatus
	err := wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (done bool, err error) {
		status, err = GetK0smotronControlPlaneStatus(ctx, client, clusterName, "default")
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

// VerifyK0smotronControlPlaneVersionFormat verifies that the version format is consistent
func VerifyK0smotronControlPlaneVersionFormat(ctx context.Context, t *testing.T, client *kubernetes.Clientset, clusterName, expectedSpecVersion string, expectK0sSuffix bool) {
	// Wait for status
	status, err := WaitForK0smotronControlPlaneStatus(ctx, client, clusterName, 2*time.Minute)
	if err != nil {
		t.Fatalf("Failed to wait for K0smotronControlPlane status: %v", err)
	}

	// Verify version format based on expectK0sSuffix
	if expectK0sSuffix {
		if !strings.Contains(status.Version, "-k0s.") {
			t.Errorf("Expected version to contain '-k0s.' suffix, but got: %s", status.Version)
		}
	} else {
		if strings.Contains(status.Version, "-k0s.") {
			t.Errorf("Expected version to NOT contain '-k0s.' suffix, but got: %s", status.Version)
		}
	}

	// Verify version matches expected
	if status.Version != expectedSpecVersion {
		t.Errorf("Expected version %s, but got: %s", expectedSpecVersion, status.Version)
	}

	// Verify k0sVersion always has suffix
	if status.K0sVersion == "" {
		t.Error("K0sVersion should not be empty")
	}
	if !strings.Contains(status.K0sVersion, "-k0s.") {
		t.Errorf("K0sVersion should always contain '-k0s.' suffix, but got: %s", status.K0sVersion)
	}
}
