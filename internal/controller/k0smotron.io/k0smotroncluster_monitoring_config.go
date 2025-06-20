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
	kcontrollerutil "github.com/k0sproject/k0smotron/internal/controller/util"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

var prometheusConfigTmpl *template.Template

func init() {
	prometheusConfigTmpl = template.Must(template.New("prometheus.yml").Parse(prometheusConfigTemplate))
}

func (scope *kmcScope) generateMonitoringCM(kmc *km.Cluster) (v1.ConfigMap, error) {
	var entrypointBuf bytes.Buffer
	err := prometheusConfigTmpl.Execute(&entrypointBuf, struct {
		Kmc         *km.Cluster
		EtcdSvcName string
	}{
		Kmc:         kmc,
		EtcdSvcName: kmc.GetEtcdServiceName(),
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
			Name:        kmc.GetMonitoringConfigMapName(),
			Namespace:   kmc.Namespace,
			Labels:      kcontrollerutil.LabelsForK0smotronCluster(kmc),
			Annotations: kcontrollerutil.AnnotationsForK0smotronCluster(kmc),
		},
		Data: map[string]string{
			"prometheus.yml": entrypointBuf.String(),
			"nginx.conf":     nginxConf,
		},
	}

	_ = ctrl.SetControllerReference(kmc, &cm, scope.client.Scheme())
	return cm, nil
}

func (scope *kmcScope) reconcileMonitoringCM(ctx context.Context, kmc *km.Cluster) error {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling monitoring configmap")

	cm, err := scope.generateMonitoringCM(kmc)
	if err != nil {
		return err
	}

	return scope.client.Patch(ctx, &cm, client.Apply, patchOpts...)
}

const prometheusConfigTemplate = `
global:
  scrape_interval:     10s
  evaluation_interval: 10s
scrape_configs:
  - job_name: "k0smotron_cluster_metrics"
    scheme: https
    tls_config: 
      insecure_skip_verify: true
      cert_file: /var/lib/k0s/pki/admin.crt
      key_file: /var/lib/k0s/pki/admin.key 
    static_configs:
      - targets: ["localhost:{{ .Kmc.Spec.Service.APIPort }}"]
        labels:
          component: kube-apiserver
          k0smotron_cluster: "{{ .Kmc.Name }}"
      - targets: ["localhost:10259"]
        labels:
          component: kube-scheduler
          k0smotron_cluster: "{{ .Kmc.Name }}"
      - targets: ["localhost:10257"]
        labels:
          component: kube-controller-manager
          k0smotron_cluster: "{{ .Kmc.Name }}"
  - job_name: "k0smotron_etcd_metrics"
    scheme: https
    tls_config: 
      insecure_skip_verify: true
      cert_file: /var/lib/k0s/pki/etcd-ca.crt
      key_file: /var/lib/k0s/pki/etcd-ca.key
    static_configs:
      - targets: ["{{ .EtcdSvcName }}:2379"]
        labels:
          component: etcd
          k0smotron_cluster: "{{ .Kmc.Name }}"
`

const nginxConf = `
worker_processes  2;
error_log  /dev/stdout warn;
pid        /var/run/nginx.pid;

events {
  worker_connections  4096;  ## Default: 1024
}

http {
   server {
      access_log /dev/stdout;
      listen 8090;
      location /metrics {
         set $lbr "{";
         set $rbr "}";
         set $q "'";
         rewrite ^(.*)$ /federate?match[]=${lbr}job!=${q}${q}${rbr} break;
         proxy_pass http://localhost:9090/;
      }
   }
}
`
