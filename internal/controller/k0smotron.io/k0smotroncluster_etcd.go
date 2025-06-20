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
	"fmt"
	"strings"
	"text/template"

	batchv1 "k8s.io/api/batch/v1"

	km "github.com/k0sproject/k0smotron/api/k0smotron.io/v1beta1"

	kcontrollerutil "github.com/k0sproject/k0smotron/internal/controller/util"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var etcdEntrypointScriptTmpl *template.Template

func init() {
	etcdEntrypointScriptTmpl = template.Must(template.New("entrypoint.sh").Parse(etcdEntrypointScriptTemplate))
}

func (scope *kmcScope) reconcileEtcd(ctx context.Context, kmc *km.Cluster) error {
	if kmc.Spec.KineDataSourceURL != "" || kmc.Spec.KineDataSourceSecretName != "" {
		return nil
	}

	if err := scope.reconcileEtcdSvc(ctx, kmc); err != nil {
		return fmt.Errorf("error reconciling etcd service: %w", err)
	}
	if err := scope.reconcileEtcdStatefulSet(ctx, kmc); err != nil {
		return fmt.Errorf("error reconciling etcd statefulset: %w", err)
	}
	if kmc.Spec.Etcd.DefragJob.Enabled {
		if err := scope.reconcileEtcdDefragJob(ctx, kmc); err != nil {
			return fmt.Errorf("error reconciling etcd defrag job: %w", err)
		}
	}

	return nil
}

func (scope *kmcScope) reconcileEtcdSvc(ctx context.Context, kmc *km.Cluster) error {
	labels := kcontrollerutil.LabelsForEtcdK0smotronCluster(kmc)

	svc := v1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        kmc.GetEtcdServiceName(),
			Namespace:   kmc.Namespace,
			Labels:      labels,
			Annotations: kcontrollerutil.AnnotationsForK0smotronCluster(kmc),
		},
		Spec: v1.ServiceSpec{
			Type:                     v1.ServiceTypeClusterIP,
			ClusterIP:                v1.ClusterIPNone,
			Selector:                 labels,
			PublishNotReadyAddresses: true,
			Ports: []v1.ServicePort{
				{
					Name:       "client",
					Port:       2379,
					TargetPort: intstr.FromInt32(2379),
				},
				{
					Name:       "peer",
					Port:       2380,
					TargetPort: intstr.FromInt32(2380),
				},
			},
		},
	}

	_ = ctrl.SetControllerReference(kmc, &svc, scope.client.Scheme())

	return scope.client.Patch(ctx, &svc, client.Apply, patchOpts...)
}

func (scope *kmcScope) reconcileEtcdDefragJob(ctx context.Context, kmc *km.Cluster) error {
	labels := kcontrollerutil.LabelsForEtcdK0smotronCluster(kmc)

	cronJob := batchv1.CronJob{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "batch/v1",
			Kind:       "CronJob",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        kmc.GetEtcdDefragJobName(),
			Namespace:   kmc.Namespace,
			Labels:      labels,
			Annotations: kcontrollerutil.AnnotationsForK0smotronCluster(kmc),
		},
		Spec: batchv1.CronJobSpec{
			Schedule:          kmc.Spec.Etcd.DefragJob.Schedule,
			ConcurrencyPolicy: batchv1.ForbidConcurrent,
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: v1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: labels,
						},
						Spec: v1.PodSpec{
							RestartPolicy: v1.RestartPolicyOnFailure,
							Containers: []v1.Container{
								{
									Name:            "etcd-defrag",
									Image:           kmc.Spec.Etcd.DefragJob.Image,
									ImagePullPolicy: v1.PullIfNotPresent,
									Args: []string{
										fmt.Sprintf("--endpoints=https://%s:2379", kmc.GetEtcdServiceName()),
										"--cacert=/var/lib/k0s/pki/etcd/ca.crt",
										"--cert=/var/lib/k0s/pki/etcd/client.crt",
										"--key=/var/lib/k0s/pki/etcd/client.key",
										"--cluster",
										"--defrag-rule",
										kmc.Spec.Etcd.DefragJob.Rule,
									},
									VolumeMounts: []v1.VolumeMount{
										{Name: "certs", MountPath: "/var/lib/k0s/pki/etcd/"},
									},
								},
							},
							Volumes: []v1.Volume{{
								Name: "certs",
								VolumeSource: v1.VolumeSource{
									Projected: &v1.ProjectedVolumeSource{
										Sources: []v1.VolumeProjection{
											{
												Secret: &v1.SecretProjection{
													LocalObjectReference: v1.LocalObjectReference{Name: secret.Name(kmc.Name, secret.EtcdCA)},
													Items: []v1.KeyToPath{
														{Key: "tls.crt", Path: "ca.crt"},
														{Key: "tls.key", Path: "ca.key"},
													},
												},
											}, {
												Secret: &v1.SecretProjection{
													LocalObjectReference: v1.LocalObjectReference{Name: secret.Name(kmc.Name, "etcd-server")},
													Items: []v1.KeyToPath{
														{Key: "tls.crt", Path: "client.crt"},
														{Key: "tls.key", Path: "client.key"},
													},
												},
											},
										},
									},
								},
							}},
						},
					},
				},
			},
		},
	}

	_ = ctrl.SetControllerReference(kmc, &cronJob, scope.client.Scheme())

	return scope.client.Patch(ctx, &cronJob, client.Apply, patchOpts...)
}

func (scope *kmcScope) reconcileEtcdStatefulSet(ctx context.Context, kmc *km.Cluster) error {
	desiredReplicas := calculateDesiredReplicas(kmc)

	foundStatefulSet, err := scope.clienSet.AppsV1().StatefulSets(kmc.Namespace).Get(ctx, kmc.GetEtcdStatefulSetName(), metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
	} else {
		// If we want to scale up existing etcd statefulset, we always scale up by 1 replica at a time and wait for the previous member to be ready
		// This is to avoid the situation where the new member is not able to join the cluster because the previous member is not ready
		if desiredReplicas > *foundStatefulSet.Spec.Replicas {
			// Scale up by 1 replica
			desiredReplicas = int32(*foundStatefulSet.Spec.Replicas) + 1
		}
		if desiredReplicas > foundStatefulSet.Status.ReadyReplicas+1 {
			// Wait for the previous member to be ready. For example, if the desired replicas is 3, we need to wait for the 2nd member to be ready before adding the 3rd member
			return fmt.Errorf("waiting for previous etcd member to be ready")
		}
	}

	statefulSet := generateEtcdStatefulSet(kmc, foundStatefulSet, desiredReplicas)

	_ = ctrl.SetControllerReference(kmc, &statefulSet, scope.client.Scheme())

	return scope.client.Patch(ctx, &statefulSet, client.Apply, patchOpts...)
}

func generateEtcdStatefulSet(kmc *km.Cluster, existingSts *apps.StatefulSet, replicas int32) apps.StatefulSet {
	labels := kcontrollerutil.LabelsForEtcdK0smotronCluster(kmc)

	size := kmc.Spec.Etcd.Persistence.Size

	if size.IsZero() {
		size = resource.MustParse("1Gi")
	}
	pvc := v1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "etcd-data",
		},
		Spec: v1.PersistentVolumeClaimSpec{
			AccessModes: []v1.PersistentVolumeAccessMode{v1.ReadWriteOnce},
			Resources: v1.VolumeResourceRequirements{
				Requests: v1.ResourceList{
					v1.ResourceStorage: size,
				},
			},
		},
	}
	if kmc.Spec.Etcd.Persistence.StorageClass != "" {
		pvc.Spec.StorageClassName = &kmc.Spec.Etcd.Persistence.StorageClass
	}

	var etcdEntrypointScriptBuf bytes.Buffer
	_ = etcdEntrypointScriptTmpl.Execute(&etcdEntrypointScriptBuf, struct {
		Args []string
	}{
		Args: kmc.Spec.Etcd.Args,
	})

	statefulSet := apps.StatefulSet{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "StatefulSet",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        kmc.GetEtcdStatefulSetName(),
			Namespace:   kmc.Namespace,
			Labels:      labels,
			Annotations: kcontrollerutil.AnnotationsForK0smotronCluster(kmc),
		},
		Spec: apps.StatefulSetSpec{
			ServiceName: kmc.GetEtcdServiceName(),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Replicas:            &replicas,
			PodManagementPolicy: apps.ParallelPodManagement,
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: v1.PodSpec{
					AutomountServiceAccountToken: ptr.To(false),
					Affinity: &v1.Affinity{PodAntiAffinity: &v1.PodAntiAffinity{
						PreferredDuringSchedulingIgnoredDuringExecution: []v1.WeightedPodAffinityTerm{
							{
								Weight: 100,
								PodAffinityTerm: v1.PodAffinityTerm{
									TopologyKey: "topology.kubernetes.io/zone",
									LabelSelector: &metav1.LabelSelector{
										MatchLabels: kcontrollerutil.LabelsForEtcdK0smotronCluster(kmc),
									},
								},
							},
							{
								Weight: 50,
								PodAffinityTerm: v1.PodAffinityTerm{
									TopologyKey: "kubernetes.io/hostname",
									LabelSelector: &metav1.LabelSelector{
										MatchLabels: kcontrollerutil.LabelsForEtcdK0smotronCluster(kmc),
									},
								},
							},
						},
					}},
					Volumes: []v1.Volume{{
						Name: "certs",
						VolumeSource: v1.VolumeSource{
							Projected: &v1.ProjectedVolumeSource{
								Sources: []v1.VolumeProjection{
									{
										Secret: &v1.SecretProjection{
											LocalObjectReference: v1.LocalObjectReference{Name: secret.Name(kmc.Name, secret.EtcdCA)},
											Items: []v1.KeyToPath{
												{Key: "tls.crt", Path: "ca.crt"},
												{Key: "tls.key", Path: "ca.key"},
											},
										},
									}, {
										Secret: &v1.SecretProjection{
											LocalObjectReference: v1.LocalObjectReference{Name: secret.Name(kmc.Name, "etcd-server")},
											Items: []v1.KeyToPath{
												{Key: "tls.crt", Path: "server.crt"},
												{Key: "tls.key", Path: "server.key"},
											},
										},
									}, {
										Secret: &v1.SecretProjection{
											LocalObjectReference: v1.LocalObjectReference{Name: secret.Name(kmc.Name, "etcd-peer")},
											Items: []v1.KeyToPath{
												{Key: "tls.crt", Path: "peer.crt"},
												{Key: "tls.key", Path: "peer.key"},
											},
										},
									},
								},
							},
						},
					}},
					SecurityContext: &v1.PodSecurityContext{
						FSGroup: ptr.To(int64(1001)),
					},
					InitContainers: generateEtcdInitContainers(kmc, existingSts),
					Containers: []v1.Container{{
						Name:            "etcd",
						Image:           kmc.Spec.Etcd.Image,
						ImagePullPolicy: v1.PullIfNotPresent,
						Command:         []string{"/bin/bash"},
						Args:            []string{"-c", etcdEntrypointScriptBuf.String()},
						Env: []v1.EnvVar{
							{Name: "SVC_NAME", Value: kmc.GetEtcdServiceName()},
							{Name: "ETCDCTL_ENDPOINTS", Value: fmt.Sprintf("https://%s:2379", kmc.GetEtcdServiceName())},
							{Name: "ETCDCTL_CACERT", Value: "/var/lib/k0s/pki/etcd/ca.crt"},
							{Name: "ETCDCTL_CERT", Value: "/var/lib/k0s/pki/etcd/server.crt"},
							{Name: "ETCDCTL_KEY", Value: "/var/lib/k0s/pki/etcd/server.key"},
							{Name: "ETCD_INITIAL_CLUSTER", Value: initialCluster(kmc, replicas)},
						},
						Resources: kmc.Spec.Etcd.Resources,
						ReadinessProbe: &v1.Probe{
							ProbeHandler: v1.ProbeHandler{
								Exec: &v1.ExecAction{
									Command: []string{"etcdctl", "endpoint", "health"},
								},
							},
						},
						Ports: []v1.ContainerPort{
							{
								Name:          "client",
								Protocol:      v1.ProtocolTCP,
								ContainerPort: 2379,
							},
							{
								Name:          "peer",
								Protocol:      v1.ProtocolTCP,
								ContainerPort: 2380,
							},
						},
						VolumeMounts: []v1.VolumeMount{
							{Name: "certs", MountPath: "/var/lib/k0s/pki/etcd/"},
							{Name: "etcd-data", MountPath: "/var/lib/k0s/etcd"},
						},
					}},
				},
			},
			VolumeClaimTemplates: []v1.PersistentVolumeClaim{pvc},
		},
	}

	if kmc.Spec.ServiceAccount != "" {
		statefulSet.Spec.Template.Spec.ServiceAccountName = kmc.Spec.ServiceAccount
	}

	if kmc.Spec.TopologySpreadConstraints != nil {
		statefulSet.Spec.Template.Spec.TopologySpreadConstraints = kmc.Spec.TopologySpreadConstraints
	}

	if kmc.Spec.Etcd.AutoDeletePVCs {
		statefulSet.Spec.PersistentVolumeClaimRetentionPolicy = &apps.StatefulSetPersistentVolumeClaimRetentionPolicy{
			WhenDeleted: apps.DeletePersistentVolumeClaimRetentionPolicyType,
		}
	}

	return statefulSet
}

func initialCluster(kmc *km.Cluster, replicas int32) string {
	var members []string
	stsName := kmc.GetEtcdStatefulSetName()
	svcName := kmc.GetEtcdServiceName()
	for i := int32(0); i < replicas; i++ {
		members = append(members, fmt.Sprintf("%s-%d=https://%s-%d.%s:2380", stsName, i, stsName, i, svcName))
	}
	return strings.Join(members, ",")
}

func generateEtcdInitContainers(kmc *km.Cluster, existingSts *apps.StatefulSet) []v1.Container {
	checkImage := kmc.Spec.GetImage()
	if existingSts != nil {
		for _, c := range existingSts.Spec.Template.Spec.InitContainers {
			if c.Name == "dns-check" {
				checkImage = c.Image
				break
			}
		}
	}
	return []v1.Container{
		{
			// Wait for the pods dns name is resolvable, since it takes some time after the pod is created
			// and etcd tries to connect to the other members using the dns names
			Name:            "dns-check",
			Image:           checkImage,
			ImagePullPolicy: v1.PullIfNotPresent,
			Command:         []string{"/bin/sh", "-c"},
			Args:            []string{"getent ahostsv4 ${HOSTNAME}.${SVC_NAME}." + kmc.Namespace + ".svc"},
			Env: []v1.EnvVar{
				{Name: "SVC_NAME", Value: kmc.GetEtcdServiceName()},
			},
		},
		{
			Name:            "init",
			Image:           kmc.Spec.Etcd.Image,
			ImagePullPolicy: v1.PullIfNotPresent,
			Command:         []string{"/bin/bash"},
			Args:            []string{"-c", initEntryScript},
			Env: []v1.EnvVar{
				{Name: "SVC_NAME", Value: kmc.GetEtcdServiceName()},
				{Name: "ETCDCTL_API", Value: "3"},
				{Name: "ETCDCTL_CACERT", Value: "/var/lib/k0s/pki/etcd/ca.crt"},
				{Name: "ETCDCTL_CERT", Value: "/var/lib/k0s/pki/etcd/server.crt"},
				{Name: "ETCDCTL_KEY", Value: "/var/lib/k0s/pki/etcd/server.key"},
			},
			VolumeMounts: []v1.VolumeMount{
				{Name: "certs", MountPath: "/var/lib/k0s/pki/etcd/"},
				{Name: "etcd-data", MountPath: "/var/lib/k0s/etcd"},
			},
		},
	}
}

// calculateDesiredReplicas calculates the desired number of etcd replicas
// We don't allow even number of replicas, so we always round up to the next odd number
func calculateDesiredReplicas(kmc *km.Cluster) int32 {
	var desiredReplicas int32 = 1
	if kmc.Spec.Replicas > 1 {
		desiredReplicas = kmc.Spec.Replicas
		if kmc.Spec.Replicas%2 == 0 {
			desiredReplicas = kmc.Spec.Replicas + 1
		}
	}

	return desiredReplicas
}

const etcdEntrypointScriptTemplate = `

export ETCD_INITIAL_CLUSTER_STATE="new"
if [[ -f /var/lib/k0s/etcd/existing ]]; then
  export ETCD_INITIAL_CLUSTER_STATE="existing"
fi

etcd --name ${HOSTNAME} \
  --listen-peer-urls=https://0.0.0.0:2380 \
  --listen-client-urls=https://0.0.0.0:2379 \
  --advertise-client-urls=https://${HOSTNAME}.${SVC_NAME}:2379 \
  --initial-advertise-peer-urls=https://${HOSTNAME}.${SVC_NAME}:2380 \
  --client-cert-auth=true \
  --tls-min-version=TLS1.2 \
  --trusted-ca-file=/var/lib/k0s/pki/etcd/ca.crt \
  --cert-file=/var/lib/k0s/pki/etcd/server.crt \
  --key-file=/var/lib/k0s/pki/etcd/server.key \
  --peer-trusted-ca-file=/var/lib/k0s/pki/etcd/ca.crt \
  --peer-key-file=/var/lib/k0s/pki/etcd/peer.key \
  --peer-cert-file=/var/lib/k0s/pki/etcd/peer.crt \
  --peer-client-cert-auth=true \
  --enable-pprof=false \
  --auto-compaction-mode=periodic \
  --auto-compaction-retention=5m \
  --snapshot-count=10000 \
{{- range $arg := .Args }}
  {{ $arg }} \
{{- end }}
  --data-dir=/var/lib/k0s/etcd
`

var initEntryScript = `
#!/bin/bash

set -eu

export ETCDCTL_ENDPOINTS=https://${SVC_NAME}:2379

if [[ ! -f /var/lib/k0s/etcd/member/snap/db ]]; then
  echo "Checking if cluster is functional"
  if etcdctl member list; then
    echo "Cluster is functional"
	MEMBER_ID=$(etcdctl member list -w simple | grep "${HOSTNAME}" | awk -F, '{ print $1 }')
	if [[ -n "${MEMBER_ID}" ]]; then
	  echo "A member with this name (${HOSTNAME}) already exists, removing"
	  etcdctl member remove "${MEMBER_ID}"
      echo "Adding new member"
	fi

    etcdctl member add ${HOSTNAME} --peer-urls https://${HOSTNAME}.${SVC_NAME}:2380
    touch /var/lib/k0s/etcd/existing
  else
    echo "Could not list members, assuming this is the first member or the cluster is not up yet"
  fi
else
  echo "Snapshot db exists, the member has data"
fi
`
