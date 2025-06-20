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

package k0smotronio

import (
	"context"

	km "github.com/k0sproject/k0smotron/api/k0smotron.io/v1beta1"
	kcontrollerutil "github.com/k0sproject/k0smotron/internal/controller/util"
	"github.com/k0sproject/k0smotron/internal/exec"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func (scope *kmcScope) reconcileKubeConfigSecret(ctx context.Context, kmc *km.Cluster) error {
	logger := log.FromContext(ctx)
	pod, err := findStatefulSetPod(ctx, kmc.GetStatefulSetName(), kmc.Namespace, scope.clienSet)

	if err != nil {
		return err
	}

	output, err := exec.PodExecCmdOutput(ctx, scope.clienSet, scope.restConfig, pod.Name, kmc.Namespace, "k0s kubeconfig create admin --groups system:masters")
	if err != nil {
		return err
	}

	logger.Info("Kubeconfig generated, creating the secret")

	secret := v1.Secret{
		// The dynamic r.Client needs TypeMeta to be set
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        kmc.GetAdminConfigSecretName(),
			Namespace:   kmc.Namespace,
			Labels:      kcontrollerutil.LabelsForK0smotronCluster(kmc),
			Annotations: kcontrollerutil.AnnotationsForK0smotronCluster(kmc),
		},
		StringData: map[string]string{"value": output},
		Type:       clusterv1.ClusterSecretType,
	}

	_ = ctrl.SetControllerReference(kmc, &secret, scope.client.Scheme())

	return scope.client.Patch(ctx, &secret, client.Apply, patchOpts...)
}
