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
	"strings"
	"text/template"

	km "github.com/k0sproject/k0smotron/api/k0smotron.io/v1beta1"
	kcontrollerutil "github.com/k0sproject/k0smotron/internal/controller/util"
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

func (scope *kmcScope) generateEntrypointCM(kmc *km.Cluster) (v1.ConfigMap, error) {
	var entrypointBuf bytes.Buffer
	err := entrypointTmpl.Execute(&entrypointBuf, map[string]interface{}{
		"KineDataSourceURLPlaceholder": kineDataSourceURLPlaceholder,
		"K0sControllerArgs":            getControllerFlags(kmc),
		"PrivilegedPortIsUsed":         kmc.Spec.Service.APIPort <= 1024,
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
			Name:        kmc.GetEntrypointConfigMapName(),
			Namespace:   kmc.Namespace,
			Labels:      kcontrollerutil.LabelsForK0smotronCluster(kmc),
			Annotations: kcontrollerutil.AnnotationsForK0smotronCluster(kmc),
		},
		Data: map[string]string{
			"k0smotron-entrypoint.sh": entrypointBuf.String(),
		},
	}

	_ = ctrl.SetControllerReference(kmc, &cm, scope.client.Scheme())
	return cm, nil
}

func (scope *kmcScope) reconcileEntrypointCM(ctx context.Context, kmc *km.Cluster) error {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling entrypoint configmap")

	cm, err := scope.generateEntrypointCM(kmc)
	if err != nil {
		return err
	}

	return scope.client.Patch(ctx, &cm, client.Apply, patchOpts...)
}

func getControllerFlags(kmc *km.Cluster) string {
	overrideConfig := false
	overrideDynamicCfg := false
	flags := kmc.Spec.ControlPlaneFlags

	for _, arg := range kmc.Spec.ControlPlaneFlags {
		if strings.HasPrefix(arg, "--config=") || arg == "--config" {
			overrideConfig = true
		}
		if strings.HasPrefix(arg, "--enable-dynamic-config=") || arg == "--enable-dynamic-config" {
			overrideDynamicCfg = true
		}
	}
	if !overrideConfig {
		flags = append(flags, "--config=/etc/k0s/k0s.yaml")
	}
	if !overrideDynamicCfg {
		flags = append(flags, "--enable-dynamic-config")
	}

	return strings.Join(flags, " ")
}

const entrypointTemplate = `
#!/bin/sh

# Put the k0s.yaml in place
mkdir /etc/k0s && echo "$K0SMOTRON_K0S_YAML" > /etc/k0s/k0s.yaml

# Substitute the kine datasource URL from the env var
sed -i "s {{ .KineDataSourceURLPlaceholder }} ${K0SMOTRON_KINE_DATASOURCE_URL} g" /etc/k0s/k0s.yaml

{{if .PrivilegedPortIsUsed}}
apk add --no-cache libcap
{ while ! setcap 'cap_net_bind_service=+ep' /var/lib/k0s/bin/kube-apiserver; do sleep 1 ; done ; } &
{{end}}

# Run the k0s controller
k0s controller {{ .K0sControllerArgs }}
`
