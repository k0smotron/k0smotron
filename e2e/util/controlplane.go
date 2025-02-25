//go:build e2e

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
	"strings"
	"time"

	"k8s.io/klog/v2"

	cpv1beta1 "github.com/k0smotron/k0smotron/api/controlplane/v1beta1"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	capiframework "sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/util/patch"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func WaitForControlPlaneToBeReady(ctx context.Context, client crclient.Client, cp *cpv1beta1.K0sControlPlane, interval Interval) error {
	fmt.Println("Waiting for the control plane to be ready")

	controlplaneObjectKey := crclient.ObjectKey{
		Name:      cp.Name,
		Namespace: cp.Namespace,
	}
	controlplane := &cpv1beta1.K0sControlPlane{}
	err := wait.PollUntilContextTimeout(ctx, interval.tick, interval.timeout, true, func(ctx context.Context) (done bool, err error) {
		if err := client.Get(ctx, controlplaneObjectKey, controlplane); err != nil {
			return false, errors.Wrapf(err, "failed to get controlplane")
		}

		desiredReplicas := controlplane.Spec.Replicas
		statusReplicas := controlplane.Status.Replicas
		updatedReplicas := controlplane.Status.UpdatedReplicas
		readyReplicas := controlplane.Status.ReadyReplicas
		unavailableReplicas := controlplane.Status.UnavailableReplicas

		if statusReplicas != desiredReplicas ||
			updatedReplicas != desiredReplicas ||
			readyReplicas != desiredReplicas ||
			unavailableReplicas > 0 ||
			controlplane.Spec.Version != controlplane.Status.Version {
			return false, nil
		}

		return true, nil
	})
	if err != nil {
		return fmt.Errorf(capiframework.PrettyPrint(controlplane) + "\n")
	}

	return nil
}

// UpgradeControlPlaneAndWaitForUpgradeInput is the input type for UpgradeControlPlaneAndWaitForUpgrade.
type UpgradeControlPlaneAndWaitForUpgradeInput struct {
	GetLister                        capiframework.GetLister
	ClusterProxy                     capiframework.ClusterProxy
	Cluster                          *clusterv1.Cluster
	ControlPlane                     *cpv1beta1.K0sControlPlane
	KubernetesUpgradeVersion         string
	WaitForKubeProxyUpgradeInterval  Interval
	WaitForControlPlaneReadyInterval Interval
}

// UpgradeControlPlaneAndWaitForUpgrade upgrades a K0sControlPlane and waits for it to be upgraded.
func UpgradeControlPlaneAndWaitForReadyUpgrade(ctx context.Context, input UpgradeControlPlaneAndWaitForUpgradeInput) error {
	mgmtClient := input.ClusterProxy.GetClient()

	fmt.Println("Patching the new kubernetes version to KCP")
	patchHelper, err := patch.NewHelper(input.ControlPlane, mgmtClient)
	if err != nil {
		return err
	}

	input.ControlPlane.Spec.Version = input.KubernetesUpgradeVersion

	err = wait.PollUntilContextTimeout(ctx, time.Second, time.Minute, true, func(ctx context.Context) (done bool, err error) {
		return patchHelper.Patch(ctx, input.ControlPlane) == nil, nil
	})
	if err != nil {
		return fmt.Errorf("failed to patch the new kubernetes version to controlplane %s: %w", klog.KObj(input.ControlPlane), err)
	}

	err = WaitForControlPlaneToBeReady(ctx, input.ClusterProxy.GetClient(), input.ControlPlane, input.WaitForControlPlaneReadyInterval)
	if err != nil {
		return err
	}

	fmt.Println("Waiting for kube-proxy to have the upgraded kubernetes version")
	workloadCluster := input.ClusterProxy.GetWorkloadCluster(ctx, input.Cluster.Namespace, input.Cluster.Name)
	workloadClient := workloadCluster.GetClient()
	return WaitForKubeProxyUpgrade(ctx, WaitForKubeProxyUpgradeInput{
		Getter:            workloadClient,
		KubernetesVersion: input.KubernetesUpgradeVersion,
	}, input.WaitForKubeProxyUpgradeInterval)
}

func DiscoveryAndWaitForControlPlaneInitialized(ctx context.Context, input capiframework.DiscoveryAndWaitForControlPlaneInitializedInput, interval Interval) (*cpv1beta1.K0sControlPlane, error) {
	var controlPlane *cpv1beta1.K0sControlPlane
	err := wait.PollUntilContextTimeout(ctx, time.Second, time.Minute, true, func(ctx context.Context) (done bool, err error) {
		controlPlane, err = getK0sControlPlaneByCluster(ctx, GetK0sControlPlaneByClusterInput{
			Lister:      input.Lister,
			ClusterName: input.Cluster.Name,
			Namespace:   input.Cluster.Namespace,
		})
		if err != nil {
			return false, err
		}

		return controlPlane != nil, nil
	})
	if err != nil {
		return nil, fmt.Errorf("couldn't get the control plane for the cluster %s: %w", klog.KObj(input.Cluster), err)
	}

	fmt.Printf("Waiting for the first control plane machine managed by %s to be provisioned", klog.KObj(controlPlane))
	err = WaitForOneK0sControlPlaneMachineToExist(ctx, WaitForOneK0sControlPlaneMachineToExistInput{
		Lister:       input.Lister,
		Cluster:      input.Cluster,
		ControlPlane: controlPlane,
	}, interval)
	if err != nil {
		return nil, fmt.Errorf("error waiting for the first control machine to be provisioned: %w", err)
	}

	return controlPlane, nil
}

type GetK0sControlPlaneByClusterInput struct {
	Lister      capiframework.Lister
	ClusterName string
	Namespace   string
}

func getK0sControlPlaneByCluster(ctx context.Context, input GetK0sControlPlaneByClusterInput) (*cpv1beta1.K0sControlPlane, error) {
	controlPlaneList := &cpv1beta1.K0sControlPlaneList{}
	err := wait.PollUntilContextTimeout(ctx, time.Second, time.Minute, true, func(ctx context.Context) (done bool, err error) {
		return input.Lister.List(ctx, controlPlaneList, byClusterOptions(input.ClusterName, input.Namespace)...) == nil, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list K0sControlPlane object for Cluster %s", klog.KRef(input.Namespace, input.ClusterName))
	}

	if len(controlPlaneList.Items) > 1 {
		return nil, fmt.Errorf("cluster %s should not have more than 1 K0sControlPlane object", klog.KRef(input.Namespace, input.ClusterName))
	}

	if len(controlPlaneList.Items) == 1 {
		return &controlPlaneList.Items[0], nil
	}

	return nil, nil
}

// byClusterOptions returns a set of ListOptions that allows to identify all the objects belonging to a Cluster.
func byClusterOptions(name, namespace string) []crclient.ListOption {
	return []crclient.ListOption{
		crclient.InNamespace(namespace),
		crclient.MatchingLabels{
			clusterv1.ClusterNameLabel: name,
		},
	}
}

type WaitForOneK0sControlPlaneMachineToExistInput struct {
	Lister       capiframework.Lister
	Cluster      *clusterv1.Cluster
	ControlPlane *cpv1beta1.K0sControlPlane
}

// WaitForOneK0sControlPlaneMachineToExist will wait until all control plane machines have node refs.
func WaitForOneK0sControlPlaneMachineToExist(ctx context.Context, input WaitForOneK0sControlPlaneMachineToExistInput, interval Interval) error {
	fmt.Println("Waiting for one control plane node to exist")
	inClustersNamespaceListOption := crclient.InNamespace(input.Cluster.Namespace)
	// ControlPlane labels
	matchClusterListOption := crclient.MatchingLabels{
		clusterv1.MachineControlPlaneLabel: "true",
		clusterv1.ClusterNameLabel:         input.Cluster.Name,
	}

	err := wait.PollUntilContextTimeout(ctx, interval.tick, interval.timeout, true, func(ctx context.Context) (done bool, err error) {
		machineList := &clusterv1.MachineList{}
		if err := input.Lister.List(ctx, machineList, inClustersNamespaceListOption, matchClusterListOption); err != nil {
			fmt.Printf("failed to list the machines: %+v", err)
			return false, err
		}
		count := 0
		for _, machine := range machineList.Items {
			if machine.Status.NodeRef != nil {
				count++
			}
		}
		return count > 0, nil
	})
	if err != nil {
		return fmt.Errorf("no Control Plane machines came into existence: %w", err)
	}

	return nil
}

type WaitForKubeProxyUpgradeInput struct {
	Getter            capiframework.Getter
	KubernetesVersion string
}

// WaitForKubeProxyUpgrade waits until kube-proxy version matches with the kubernetes version.
func WaitForKubeProxyUpgrade(ctx context.Context, input WaitForKubeProxyUpgradeInput, interval Interval) error {
	fmt.Println("Ensuring kube-proxy has the correct image")

	// this desired version is sticky to the k0s naming on the kube-proxy image
	versionPrefix := strings.Split(input.KubernetesVersion, "+")[0]
	wantKubeProxyImage := fmt.Sprintf("quay.io/k0sproject/kube-proxy:%s", versionPrefix)

	return wait.PollUntilContextTimeout(ctx, interval.tick, interval.timeout, true, func(ctx context.Context) (done bool, err error) {
		ds := &appsv1.DaemonSet{}

		if err := input.Getter.Get(ctx, crclient.ObjectKey{Name: "kube-proxy", Namespace: metav1.NamespaceSystem}, ds); err != nil {
			return false, err
		}

		if strings.HasPrefix(ds.Spec.Template.Spec.Containers[0].Image, wantKubeProxyImage) {
			return true, nil
		}

		return false, nil
	})
}
