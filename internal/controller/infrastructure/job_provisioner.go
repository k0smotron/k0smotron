package infrastructure

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"path/filepath"

	"github.com/go-logr/logr"
	"github.com/k0smotron/k0smotron/internal/cloudinit"
	"gopkg.in/yaml.v3"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrastructure "github.com/k0smotron/k0smotron/api/infrastructure/v1beta1"
)

var patchOpts []client.PatchOption = []client.PatchOption{
	client.FieldOwner("k0smotron-operator"),
	client.ForceOwnership,
}

type JobProvisioner struct {
	client    client.Client
	clientSet *kubernetes.Clientset

	bootstrapData []byte
	machine       *v1beta1.Machine
	remoteMachine *infrastructure.RemoteMachine
	provisionJob  *infrastructure.ProvisionJob
	log           logr.Logger
}

func (p *JobProvisioner) Provision(ctx context.Context) error {
	// Parse the bootstrap data
	cloudInit := &cloudinit.CloudInit{}
	err := yaml.Unmarshal(p.bootstrapData, cloudInit)
	if err != nil {
		return fmt.Errorf("failed to parse bootstrap data: %w", err)
	}

	jb := p.provisionJob.JobTemplate
	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "batch/v1",
			Kind:       "Job",
		},
		ObjectMeta: jb.ObjectMeta,
		Spec:       jb.Spec,
	}
	if job.ObjectMeta.Name == "" {
		job.ObjectMeta.Name = fmt.Sprintf("%s-%s", p.machine.Spec.ClusterName, p.remoteMachine.Name)
	}
	if job.ObjectMeta.Namespace == "" {
		job.ObjectMeta.Namespace = p.remoteMachine.Namespace
	}
	job.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: p.remoteMachine.APIVersion,
		Kind:       p.remoteMachine.Kind,
		Name:       p.remoteMachine.GetName(),
		UID:        p.remoteMachine.GetUID(),
	}}

	secret := &v1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-job-bootstrap-data", job.Name),
			Namespace: job.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: p.remoteMachine.APIVersion,
				Kind:       p.remoteMachine.Kind,
				Name:       p.remoteMachine.GetName(),
				UID:        p.remoteMachine.GetUID(),
			}},
		},
	}

	volume, volumeMounts, secretData := p.extractCloudInit(cloudInit)
	job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, volume)
	job.Spec.Template.Spec.Containers[0].VolumeMounts = append(job.Spec.Template.Spec.Containers[0].VolumeMounts, volumeMounts...)
	volume.Secret.SecretName = secret.Name
	secret.Data = secretData

	job.Spec.Template.Spec.Containers[0].Args = []string{"/var/lib/bootstrap-data/k0smotron-entrypoint.sh"}

	if err := p.client.Patch(context.Background(), secret, client.Apply, patchOpts...); err != nil {
		return fmt.Errorf("failed to create a secret: %w", err)
	}

	if err := p.client.Create(context.Background(), job, &client.CreateOptions{}); err != nil {
		return fmt.Errorf("failed to create job: %w", err)
	}

	watch, err := p.clientSet.BatchV1().Jobs(job.Namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("metadata.name=%s", job.Name),
	})
	if err != nil {
		return fmt.Errorf("failed to watch job: %w", err)
	}

	go func() {
		for event := range watch.ResultChan() {
			j, ok := event.Object.(*batchv1.Job)
			if !ok {
				continue
			}
			for _, c := range j.Status.Conditions {
				if c.Type == batchv1.JobComplete {
					// TODO: update machine status
					break
				}
			}
		}
	}()

	return nil
}

func (p *JobProvisioner) extractCloudInit(cloudInit *cloudinit.CloudInit) (volume v1.Volume, volumeMounts []v1.VolumeMount, secretData map[string][]byte) {
	machineDSN := p.machineDSN()

	var sshCommand, scpCommand string
	if p.remoteMachine.Spec.Port != 0 {
		sshCommand = fmt.Sprintf("%s -p %d %s", p.provisionJob.SSHCommand, p.remoteMachine.Spec.Port, machineDSN)
		scpCommand = fmt.Sprintf("%s -P %d", p.provisionJob.SCPCommand, p.remoteMachine.Spec.Port)
	} else {
		sshCommand = fmt.Sprintf("%s %s", p.provisionJob.SSHCommand, machineDSN)
		scpCommand = p.provisionJob.SCPCommand
	}

	volume = v1.Volume{
		Name: "bootstrap-data",
		VolumeSource: v1.VolumeSource{
			Secret: &v1.SecretVolumeSource{
				SecretName: fmt.Sprintf("%s-job-bootstrap-data", p.provisionJob.JobTemplate.Name),
			},
		},
	}

	var buf bytes.Buffer
	buf.WriteString("#!/bin/sh\n")

	secretData = make(map[string][]byte)

	for _, file := range cloudInit.Files {
		fileName := genFileName(file.Path)
		secretData[fileName] = []byte(file.Content)

		volume.VolumeSource.Secret.Items = append(volume.VolumeSource.Secret.Items, v1.KeyToPath{
			Key:  fileName,
			Path: fileName,
		})

		buf.WriteString(fmt.Sprintf("%s /var/lib/bootstrap-data/%s %s:%s\n", scpCommand, fileName, machineDSN, file.Path))
	}
	volumeMounts = append(volumeMounts, v1.VolumeMount{
		Name:      "bootstrap-data",
		MountPath: "/var/lib/bootstrap-data",
	})

	for _, cmd := range cloudInit.RunCmds {
		if p.remoteMachine.Spec.UseSudo {
			cmd = fmt.Sprintf("sudo su -c '%s'", cmd)
		}
		buf.WriteString(fmt.Sprintf("%s \"%s\"\n", sshCommand, cmd))
	}
	secretData["k0smotron-entrypoint.sh"] = buf.Bytes()

	volume.VolumeSource.Secret.Items = append(volume.VolumeSource.Secret.Items, v1.KeyToPath{
		Key:  "k0smotron-entrypoint.sh",
		Path: "k0smotron-entrypoint.sh",
		Mode: ptr.To[int32](0755),
	})

	return volume, volumeMounts, secretData
}

func (p *JobProvisioner) machineDSN() (dsn string) {
	dsn = p.remoteMachine.Spec.Address
	if p.remoteMachine.Spec.User != "" {
		dsn = fmt.Sprintf("%s@%s", p.remoteMachine.Spec.User, dsn)
	}

	return dsn
}

func (p *JobProvisioner) Cleanup(_ context.Context, mode RemoteMachineMode) error {
	if mode == ModeNonK0s {
		return nil
	}

	return nil
}

func genFileName(filePath string) string {
	return fmt.Sprintf("%s-%x", filepath.Base(filePath), md5.Sum([]byte(filePath)))
}
