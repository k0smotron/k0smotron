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
	"bytes"
	"context"
	"text/template"

	km "github.com/k0sproject/k0smotron/api/k0smotron.io/v1beta1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var entrypointTmpl *template.Template

func init() {
	entrypointTmpl = template.Must(template.New("entrypoint.sh").Parse(entrypointTemplate))
}

func (r *ClusterReconciler) generateEntrypointCM(kmc *km.Cluster) (v1.ConfigMap, error) {
	var entrypointBuf bytes.Buffer
	err := entrypointTmpl.Execute(&entrypointBuf, map[string]string{
		"KineDataSourceURLPlaceholder": kineDataSourceURLPlaceholder,
	})
	if err != nil {
		return v1.ConfigMap{}, err
	}

	cm := v1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmc.GetEntrypointConfigMapName(),
			Namespace: kmc.Namespace,
		},
		Data: map[string]string{
			"k0smotron-entrypoint.sh": entrypointBuf.String(),
		},
	}

	_ = ctrl.SetControllerReference(kmc, &cm, r.Scheme)
	return cm, nil
}

func (r *ClusterReconciler) reconcileEntrypointCM(ctx context.Context, kmc km.Cluster) error {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling entrypoint configmap")

	cm, err := r.generateEntrypointCM(&kmc)
	if err != nil {
		return err
	}

	return r.Client.Patch(ctx, &cm, client.Apply, patchOpts...)
}

const entrypointTemplate = `
#!/bin/sh

# Put the k0s.yaml in place
mkdir /etc/k0s && echo "$K0SMOTRON_K0S_YAML" > /etc/k0s/k0s.yaml

# Substitute the kine datasource URL from the env var
sed -i "s {{ .KineDataSourceURLPlaceholder }} ${K0SMOTRON_KINE_DATASOURCE_URL} g" /etc/k0s/k0s.yaml

# Run the k0s controller
k0s controller --config /etc/k0s/k0s.yaml --enable-dynamic-config
`
