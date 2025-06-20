/*
Copyright 2025.
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
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kapi "github.com/k0sproject/k0smotron/api/k0smotron.io/v1beta1"
)

func GetKmcClientFromClusterPrLubeconfigSecret(ctx context.Context, managementClusterClient client.Client, hostingClusterKubeconfigRef *kapi.HostingClusterKubeconfigRef) (client.Client, *kubernetes.Clientset, *rest.Config, error) {
	kubeconfigSecret := &corev1.Secret{}
	key := client.ObjectKey{
		Namespace: hostingClusterKubeconfigRef.Namespace,
		Name:      hostingClusterKubeconfigRef.Name,
	}
	err := managementClusterClient.Get(ctx, key, kubeconfigSecret)
	if err != nil {
		return nil, nil, nil, err
	}

	kubeconfigData, ok := kubeconfigSecret.Data[hostingClusterKubeconfigRef.Key]
	if !ok {
		return nil, nil, nil, fmt.Errorf("missing %s key in secret %s/%s", hostingClusterKubeconfigRef.Key, kubeconfigSecret.Namespace, kubeconfigSecret.Name)
	}
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	clientSet, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create clientset: %w", err)
	}

	c, err := client.New(restConfig, client.Options{Scheme: managementClusterClient.Scheme()})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create client: %w", err)
	}

	return c, clientSet, restConfig, nil
}
