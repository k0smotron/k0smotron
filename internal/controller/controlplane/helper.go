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

package controlplane

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/imdario/mergo"
	"github.com/k0sproject/version"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/controllers/external"
	"sigs.k8s.io/cluster-api/util/collections"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	bootstrapv1 "github.com/k0smotron/k0smotron/api/bootstrap/v1beta1"
	cpv1beta1 "github.com/k0smotron/k0smotron/api/controlplane/v1beta1"
)

const (
	etcdMemberConditionTypeJoined = "Joined"
)

func (c *K0sController) createMachine(ctx context.Context, name string, cluster *clusterv1.Cluster, kcp *cpv1beta1.K0sControlPlane, infraRef corev1.ObjectReference, failureDomain *string) (*clusterv1.Machine, error) {
	machine, err := c.generateMachine(ctx, name, cluster, kcp, infraRef, failureDomain)
	if err != nil {
		return nil, fmt.Errorf("error generating machine: %w", err)
	}
	_ = ctrl.SetControllerReference(kcp, machine, c.Client.Scheme())

	err = c.Client.Patch(ctx, machine, client.Apply, &client.PatchOptions{
		FieldManager: "k0smotron",
	})
	if err != nil {
		return machine, err
	}

	// Remove the annotation tracking that a remediation is in progress.
	// A remediation is completed when the replacement machine has been created above.
	delete(kcp.Annotations, cpv1beta1.RemediationInProgressAnnotation)

	return machine, nil
}

func (c *K0sController) deleteMachine(ctx context.Context, name string, kcp *cpv1beta1.K0sControlPlane) error {
	machine := &clusterv1.Machine{

		TypeMeta: metav1.TypeMeta{
			APIVersion: clusterv1.GroupVersion.String(),
			Kind:       "Machine",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: kcp.Namespace,
		},
	}

	err := c.Client.Delete(ctx, machine)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("error deleting machine: %w", err)
	}
	return nil
}

func (c *K0sController) generateMachine(_ context.Context, name string, cluster *clusterv1.Cluster, kcp *cpv1beta1.K0sControlPlane, infraRef corev1.ObjectReference, failureDomain *string) (*clusterv1.Machine, error) {
	v := kcp.Spec.Version

	labels := controlPlaneCommonLabelsForCluster(kcp, cluster.Name)

	for _, arg := range kcp.Spec.K0sConfigSpec.Args {
		if arg == "--enable-worker" || arg == "--enable-worker=true" {
			labels["k0smotron.io/control-plane-worker-enabled"] = "true"
			break
		}
	}

	annotations := map[string]string{
		cpv1beta1.K0ControlPlanePreTerminateHookCleanupAnnotation: "",
	}
	// Add the annotations from the MachineTemplate.
	// Note: we intentionally don't use the map directly to ensure we don't modify the map in KCP.
	for k, v := range kcp.Spec.MachineTemplate.ObjectMeta.Annotations {
		annotations[k] = v
	}

	machine := &clusterv1.Machine{
		TypeMeta: metav1.TypeMeta{
			APIVersion: clusterv1.GroupVersion.String(),
			Kind:       "Machine",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   kcp.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: clusterv1.MachineSpec{
			Version:       &v,
			ClusterName:   cluster.Name,
			FailureDomain: failureDomain,
			Bootstrap: clusterv1.Bootstrap{
				ConfigRef: &corev1.ObjectReference{
					APIVersion: "bootstrap.cluster.x-k8s.io/v1beta1",
					Kind:       "K0sControllerConfig",
					Name:       name,
				},
			},
			InfrastructureRef:       infraRef,
			NodeDrainTimeout:        kcp.Spec.MachineTemplate.NodeDrainTimeout,
			NodeDeletionTimeout:     kcp.Spec.MachineTemplate.NodeDeletionTimeout,
			NodeVolumeDetachTimeout: kcp.Spec.MachineTemplate.NodeVolumeDetachTimeout,
		},
	}

	return machine, nil
}

func (c *K0sController) getInfraMachines(ctx context.Context, machines collections.Machines) (map[string]*unstructured.Unstructured, error) {
	result := map[string]*unstructured.Unstructured{}
	for _, m := range machines {
		infraMachine, err := external.Get(ctx, c.Client, &m.Spec.InfrastructureRef, m.Namespace)
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("failed to retrieve infra machine for machine object %s: %w", m.Name, err)
		}
		result[m.Name] = infraMachine
	}
	return result, nil
}

func (c *K0sController) getBootstrapConfigs(ctx context.Context, machines collections.Machines) (map[string]bootstrapv1.K0sControllerConfig, error) {
	result := map[string]bootstrapv1.K0sControllerConfig{}
	for _, m := range machines {
		var b bootstrapv1.K0sControllerConfig
		err := c.Client.Get(ctx, client.ObjectKey{Namespace: m.Namespace, Name: m.Name}, &b)
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("failed to retrieve bootstrap data for machine object %s: %w", m.Name, err)
		}
		result[m.Name] = b
	}
	return result, nil
}

func (c *K0sController) createMachineFromTemplate(ctx context.Context, name string, cluster *clusterv1.Cluster, kcp *cpv1beta1.K0sControlPlane) (*unstructured.Unstructured, error) {
	infraMachine, err := c.generateMachineFromTemplate(ctx, name, cluster, kcp)
	if err != nil {
		return nil, err
	}

	existingInfraMachine := &unstructured.Unstructured{}
	existingInfraMachine.SetAPIVersion(infraMachine.GetAPIVersion())
	existingInfraMachine.SetKind(infraMachine.GetKind())
	err = c.Get(ctx, client.ObjectKey{Namespace: infraMachine.GetNamespace(), Name: infraMachine.GetName()}, existingInfraMachine)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if err = c.Client.Patch(ctx, infraMachine, client.Apply, &client.PatchOptions{
				FieldManager: "k0smotron",
			}); err != nil {
				return nil, fmt.Errorf("error apply patching: %w", err)
			}
			return infraMachine, nil
		}

		return nil, fmt.Errorf("error getting machine implementation: %w", err)
	}

	err = mergo.Merge(existingInfraMachine, infraMachine, mergo.WithSliceDeepCopy)
	if err != nil {
		return nil, err
	}

	spec, _, _ := unstructured.NestedMap(existingInfraMachine.Object, "spec")
	patch := unstructured.Unstructured{Object: map[string]interface{}{
		"spec": spec,
	}}
	data, err := patch.MarshalJSON()
	if err != nil {
		return nil, err
	}

	pluralName := ""
	resList, _ := c.ClientSet.Discovery().ServerResourcesForGroupVersion(existingInfraMachine.GetAPIVersion())
	for _, apiRes := range resList.APIResources {
		if apiRes.Kind == existingInfraMachine.GetKind() && !strings.Contains(apiRes.Name, "/") {
			pluralName = apiRes.Name
			break
		}
	}
	req := c.ClientSet.RESTClient().Patch(types.MergePatchType).
		Body(data).
		AbsPath("apis", infraMachine.GetAPIVersion(), "namespaces", infraMachine.GetNamespace(), pluralName, infraMachine.GetName())
	_, err = req.DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("error patching: %w", err)
	}
	return infraMachine, nil
}

func (c *K0sController) deleteMachineFromTemplate(ctx context.Context, name string, cluster *clusterv1.Cluster, kcp *cpv1beta1.K0sControlPlane) error {
	infraMachine, err := c.generateMachineFromTemplate(ctx, name, cluster, kcp)
	if err != nil {
		return err
	}

	err = c.Client.Delete(ctx, infraMachine)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("error deleting machine implementation: %w", err)
	}
	return nil
}

func (c *K0sController) generateMachineFromTemplate(ctx context.Context, name string, cluster *clusterv1.Cluster, kcp *cpv1beta1.K0sControlPlane) (*unstructured.Unstructured, error) {
	infraMachineTemplate, err := c.getMachineTemplate(ctx, kcp)
	if err != nil {
		return nil, err
	}

	_ = ctrl.SetControllerReference(cluster, infraMachineTemplate, c.Client.Scheme())
	err = c.Client.Patch(ctx, infraMachineTemplate, client.Merge, &client.PatchOptions{FieldManager: "k0smotron"})
	if err != nil {
		return nil, err
	}

	template, found, err := unstructured.NestedMap(infraMachineTemplate.UnstructuredContent(), "spec", "template")
	if !found {
		return nil, fmt.Errorf("missing spec.template on %v %q", infraMachineTemplate.GroupVersionKind(), infraMachineTemplate.GetName())
	} else if err != nil {
		return nil, fmt.Errorf("error getting spec.template map on %v %q: %w", infraMachineTemplate.GroupVersionKind(), infraMachineTemplate.GetName(), err)
	}

	infraMachine := &unstructured.Unstructured{Object: template}
	infraMachine.SetName(name)
	infraMachine.SetNamespace(kcp.Namespace)

	annotations := map[string]string{}
	for key, value := range kcp.Annotations {
		annotations[key] = value
	}

	for k, v := range kcp.Spec.MachineTemplate.ObjectMeta.Annotations {
		annotations[k] = v
	}

	annotations[clusterv1.TemplateClonedFromNameAnnotation] = kcp.Spec.MachineTemplate.InfrastructureRef.Name
	annotations[clusterv1.TemplateClonedFromGroupKindAnnotation] = kcp.Spec.MachineTemplate.InfrastructureRef.GroupVersionKind().GroupKind().String()
	infraMachine.SetAnnotations(annotations)

	infraMachine.SetLabels(controlPlaneCommonLabelsForCluster(kcp, cluster.GetName()))

	infraMachine.SetAPIVersion(infraMachineTemplate.GetAPIVersion())
	infraMachine.SetKind(strings.TrimSuffix(infraMachineTemplate.GetKind(), clusterv1.TemplateSuffix))

	return infraMachine, nil
}

func (c *K0sController) needsRecreation(bootstrapConfigs map[string]bootstrapv1.K0sControllerConfig, kcp *cpv1beta1.K0sControlPlane, machine *clusterv1.Machine) bool {
	// Skip the check if the K0sControlPlane is not ready
	if !kcp.Status.Ready || kcp.Spec.Replicas != kcp.Status.Replicas {
		return false
	}

	if machine == nil {
		return false
	}

	if machine.Status.Phase != string(clusterv1.MachinePhaseRunning) &&
		machine.Status.Phase != string(clusterv1.MachinePhaseProvisioned) &&
		machine.Status.Phase != string(clusterv1.MachinePhaseProvisioning) {
		return false
	}

	bootstrapConfig, found := bootstrapConfigs[machine.Name]
	if !found {
		return false
	}

	// k0s config will be reconciled using dynamic config, so leave it out of the comparison
	bootstrapConfig.Spec.K0sConfigSpec.K0s = kcp.Spec.K0sConfigSpec.K0s
	// leave out the tunneling spec for the bootstrap config
	bootstrapConfig.Spec.K0sConfigSpec.Tunneling = kcp.Spec.K0sConfigSpec.Tunneling
	return !reflect.DeepEqual(kcp.Spec.K0sConfigSpec, *bootstrapConfig.Spec.K0sConfigSpec)
}

func matchesTemplateClonedFrom(infraMachines map[string]*unstructured.Unstructured, kcp *cpv1beta1.K0sControlPlane, machine *clusterv1.Machine) bool {
	if machine == nil {
		return false
	}
	infraMachine, found := infraMachines[machine.Name]
	if !found {
		return false
	}

	clonedFromName := infraMachine.GetAnnotations()[clusterv1.TemplateClonedFromNameAnnotation]
	clonedFromGroupKind := infraMachine.GetAnnotations()[clusterv1.TemplateClonedFromGroupKindAnnotation]

	return clonedFromName == kcp.Spec.MachineTemplate.InfrastructureRef.Name &&
		clonedFromGroupKind == kcp.Spec.MachineTemplate.InfrastructureRef.GroupVersionKind().GroupKind().String()
}

func (c *K0sController) checkMachineLeft(ctx context.Context, name string, clientset *kubernetes.Clientset) (bool, error) {
	var etcdMember unstructured.Unstructured
	err := clientset.RESTClient().
		Get().
		AbsPath("/apis/etcd.k0sproject.io/v1beta1/etcdmembers/" + name).
		Do(ctx).
		Into(&etcdMember)

	if err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("error getting etcd member: %w", err)
	}

	conditions, _, err := unstructured.NestedSlice(etcdMember.Object, "status", "conditions")
	if err != nil {
		return false, fmt.Errorf("error getting etcd member conditions: %w", err)
	}

	for _, condition := range conditions {
		conditionMap := condition.(map[string]interface{})
		if conditionMap["type"] == etcdMemberConditionTypeJoined && conditionMap["status"] == "False" {
			err = clientset.RESTClient().
				Delete().
				AbsPath("/apis/etcd.k0sproject.io/v1beta1/etcdmembers/" + name).
				Do(ctx).
				Into(&etcdMember)
			if err != nil && !apierrors.IsNotFound(err) {
				return false, fmt.Errorf("error deleting etcd member %s: %w", name, err)
			}

			return true, nil
		}
	}
	return false, nil
}

func (c *K0sController) markChildControlNodeToLeave(ctx context.Context, name string, clientset *kubernetes.Clientset) error {
	if clientset == nil {
		return nil
	}

	logger := log.FromContext(ctx).WithValues("controlNode", name)

	err := clientset.RESTClient().
		Patch(types.MergePatchType).
		AbsPath("/apis/etcd.k0sproject.io/v1beta1/etcdmembers/" + name).
		Body([]byte(`{"spec":{"leave":true}, "metadata": {"annotations": {"k0smotron.io/marked-to-leave-at": "` + time.Now().String() + `"}}}`)).
		Do(ctx).
		Error()
	if err != nil {
		logger.Error(err, "error marking etcd member to leave. Trying to mark control node to leave")
		err := clientset.RESTClient().
			Patch(types.MergePatchType).
			AbsPath("/apis/autopilot.k0sproject.io/v1beta2/controlnodes/" + name).
			Body([]byte(`{"metadata":{"annotations":{"k0smotron.io/leave":"true"}}}`)).
			Do(ctx).
			Error()
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("error marking control node to leave: %w", err)
		}
	}
	logger.Info("marked etcd to leave")

	return nil
}

func (c *K0sController) deleteOldControlNodes(ctx context.Context, cluster *clusterv1.Cluster) error {
	kubeClient, err := c.getKubeClient(ctx, cluster)
	if err != nil {
		return fmt.Errorf("error getting kube client: %w", err)
	}
	machines, err := collections.GetFilteredMachinesForCluster(ctx, c, cluster, collections.ControlPlaneMachines(cluster.Name))
	if err != nil {
		return fmt.Errorf("error getting all machines: %w", err)
	}

	var controlNodeList unstructured.UnstructuredList
	err = kubeClient.RESTClient().
		Get().
		AbsPath("/apis/autopilot.k0sproject.io/v1beta2/controlnodes").
		Do(ctx).
		Into(&controlNodeList)

	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	existingMachineNames := make(map[string]struct{})
	for _, n := range machines.Names() {
		existingMachineNames[n] = struct{}{}
	}

	for _, controlNode := range controlNodeList.Items {
		if _, ok := existingMachineNames[controlNode.GetName()]; !ok {
			err := c.deleteControlNode(ctx, controlNode.GetName(), kubeClient)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (c *K0sController) deleteControlNode(ctx context.Context, name string, clientset *kubernetes.Clientset) error {
	if clientset == nil {
		return nil
	}

	err := clientset.RESTClient().
		Delete().
		AbsPath("/apis/autopilot.k0sproject.io/v1beta2/controlnodes/" + name).
		Do(ctx).
		Error()
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	return nil
}

func (c *K0sController) createAutopilotPlan(ctx context.Context, kcp *cpv1beta1.K0sControlPlane, cluster *clusterv1.Cluster, clientset *kubernetes.Clientset) error {
	if clientset == nil {
		return nil
	}

	var existingPlan unstructured.Unstructured
	err := clientset.RESTClient().Get().AbsPath("/apis/autopilot.k0sproject.io/v1beta2/plans/autopilot").Do(ctx).Into(&existingPlan)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("error getting autopilot plan: %w", err)
	}

	state, found, err := unstructured.NestedString(existingPlan.Object, "status", "state")
	if err != nil {
		return fmt.Errorf("error getting autopilot plan's state: %w", err)
	}
	if found {
		commands, found, err := unstructured.NestedSlice(existingPlan.Object, "spec", "commands")
		if err != nil || !found || len(commands) == 0 {
			return fmt.Errorf("error getting current autopilot plan's commands: %w", err)
		}

		version, found, err := unstructured.NestedString(commands[0].(map[string]interface{}), "k0supdate", "version")
		if err != nil || !found {
			return fmt.Errorf("error getting current autopilot plan's version: %w", err)
		}
		if state == "Schedulable" || state == "SchedulableWait" {
			// it is necessary to check if the current autopilot process corresponds to a previous update by comparing the current
			// version of the resource with the desired one. If that is the case, the state is not yet ready to proceed with a new plan.
			if version != kcp.Spec.Version {
				return fmt.Errorf("previous autopilot is not finished: %w", ErrNotReady)
			}

			return nil
		}

		if state == "Completed" {
			// If the state is completed, it is necessary to check if the current version of the resource corresponds to the desired one.
			// If that is the case, it is not necessary to proceed with a new plan.
			if version == kcp.Spec.Version {
				return nil
			}
		}
	}

	err = clientset.RESTClient().Delete().AbsPath("/apis/autopilot.k0sproject.io/v1beta2/plans/autopilot").Do(ctx).Error()
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("error deleting autopilot plan: %w", err)
	}

	machines, err := collections.GetFilteredMachinesForCluster(ctx, c, cluster, collections.ControlPlaneMachines(cluster.Name), collections.ActiveMachines)
	if err != nil {
		return fmt.Errorf("error getting control plane machines: %w", err)
	}

	amd64DownloadURL := `https://get.k0sproject.io/` + kcp.Spec.Version + `/k0s-` + kcp.Spec.Version + `-amd64`
	arm64DownloadURL := `https://get.k0sproject.io/` + kcp.Spec.Version + `/k0s-` + kcp.Spec.Version + `-arm64`
	armDownloadURL := `https://get.k0sproject.io/` + kcp.Spec.Version + `/k0s-` + kcp.Spec.Version + `-arm`
	if kcp.Spec.K0sConfigSpec.DownloadURL != "" {
		amd64DownloadURL = kcp.Spec.K0sConfigSpec.DownloadURL
		arm64DownloadURL = kcp.Spec.K0sConfigSpec.DownloadURL
		armDownloadURL = kcp.Spec.K0sConfigSpec.DownloadURL
	}

	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	plan := []byte(`
	{
		"apiVersion": "autopilot.k0sproject.io/v1beta2",
		"kind": "Plan",
		"metadata": {
		  "name": "autopilot"
		},
		"spec": {
			"id": "id-` + kcp.Name + `-` + timestamp + `",
			"timestamp": "` + timestamp + `",
			"commands": [{
				"k0supdate": {
					"version": "` + kcp.Spec.Version + `",
					"platforms": {
						"linux-amd64": {
							"url": "` + amd64DownloadURL + `"
						},
						"linux-arm64": {
							"url": "` + arm64DownloadURL + `"
						},
						"linux-arm": {
							"url": "` + armDownloadURL + `"
						}
					},
					"targets": {
						"controllers": {
							"discovery": {
							    "static": {
									"nodes": ["` + strings.Join(machines.Names(), `","`) + `"]
								}
							}
						}
					}
				}
			}]
		}
	}`)

	return clientset.RESTClient().Post().
		AbsPath("/apis/autopilot.k0sproject.io/v1beta2/plans").
		Body(plan).
		Do(ctx).
		Error()
}

// minVersion returns the minimum version from a list of machines
func minVersion(machines collections.Machines) (string, error) {
	if machines == nil || machines.Len() == 0 {
		return "", nil
	}

	versions := make([]*version.Version, 0, len(machines))
	for _, m := range machines {
		v, err := version.NewVersion(*m.Spec.Version)
		if err != nil {
			return "", fmt.Errorf("failed to parse version %s: %w", *m.Spec.Version, err)
		}

		versions = append(versions, v)
	}

	sort.Sort(version.Collection(versions))

	return versions[0].String(), nil
}
