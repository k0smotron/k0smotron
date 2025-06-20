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
	"time"

	cpv1beta1 "github.com/k0sproject/k0smotron/api/controlplane/v1beta1"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	capiframework "sigs.k8s.io/cluster-api/test/framework"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type GetK0smotronControlPlaneByClusterInput struct {
	Lister      capiframework.Lister
	ClusterName string
	Namespace   string
}

type WaitForOneK0smotronControlPlaneMachineToExistInput struct {
	Lister       capiframework.Lister
	Cluster      *clusterv1.Cluster
	ControlPlane *cpv1beta1.K0smotronControlPlane
}

type DiscoveryAndWaitForHCPReadyInput struct {
	Lister  capiframework.Lister
	Cluster *clusterv1.Cluster
	Getter  capiframework.Getter
}

func DiscoveryAndWaitForHCPToBeReady(ctx context.Context, input DiscoveryAndWaitForHCPReadyInput, interval Interval) (*cpv1beta1.K0smotronControlPlane, error) {
	var controlPlane *cpv1beta1.K0smotronControlPlane
	err := wait.PollUntilContextTimeout(ctx, time.Second, time.Minute, true, func(ctx context.Context) (done bool, err error) {
		controlPlane, err = getK0smotronControlPlaneByCluster(ctx, GetK0smotronControlPlaneByClusterInput{
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

	fmt.Printf("Waiting for the HCP to be ready")
	err = WaitForHCPToBeReady(ctx, input.Getter, controlPlane, interval)
	if err != nil {
		return nil, fmt.Errorf("error waiting for the first control machine to be provisioned: %w", err)
	}

	return controlPlane, nil
}

func WaitForHCPToBeReady(ctx context.Context, getter capiframework.Getter, cp *cpv1beta1.K0smotronControlPlane, interval Interval) error {
	controlplaneObjectKey := crclient.ObjectKey{
		Name:      cp.Name,
		Namespace: cp.Namespace,
	}
	controlplane := &cpv1beta1.K0smotronControlPlane{}
	err := wait.PollUntilContextTimeout(ctx, interval.tick, interval.timeout, true, func(ctx context.Context) (done bool, err error) {
		if err := getter.Get(ctx, controlplaneObjectKey, controlplane); err != nil {
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

func getK0smotronControlPlaneByCluster(ctx context.Context, input GetK0smotronControlPlaneByClusterInput) (*cpv1beta1.K0smotronControlPlane, error) {
	controlPlaneList := &cpv1beta1.K0smotronControlPlaneList{}
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
