/*
Copyright 2024.

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
	"testing"
	"time"

	bootstrapv1 "github.com/k0smotron/k0smotron/api/bootstrap/v1beta1"
	cpv1beta1 "github.com/k0smotron/k0smotron/api/controlplane/v1beta1"
	autopilot "github.com/k0sproject/k0s/pkg/apis/autopilot/v1beta2"
	"github.com/k0sproject/k0s/pkg/autopilot/controller/plans/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	restfake "k8s.io/client-go/rest/fake"
	"k8s.io/kubectl/pkg/scheme"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestNewReplicaStatusComputer(t *testing.T) {
	t.Run("inplace strategy returns a plan status computer", func(t *testing.T) {
		ns, err := testEnv.CreateNamespace(ctx, "test-inplace-strategy-returns-plan-status-computer")
		require.NoError(t, err)

		cluster, kcp, _ := createClusterWithControlPlane(ns.Name)
		require.NoError(t, testEnv.Create(ctx, cluster))

		kcp.Spec.UpdateStrategy = cpv1beta1.UpdateInPlace
		require.NoError(t, testEnv.Create(ctx, kcp))

		defer func(do ...client.Object) {
			require.NoError(t, testEnv.Cleanup(ctx, do...))
		}(cluster, kcp, ns)

		plan := &autopilot.Plan{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Plan",
				APIVersion: "autopilot.k0sproject.io/v1beta2",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-plan",
			},
			Spec: autopilot.PlanSpec{
				Commands: []autopilot.PlanCommand{
					{
						K0sUpdate: &autopilot.PlanCommandK0sUpdate{
							Version: "v1.31.0+k0s.0",
						},
					},
				},
			},
		}
		expectedPlanStatusComputer := &planStatus{
			plan: *plan,
		}

		frt := &fakeRoundTripper{plan: plan}
		fakeClient := &restfake.RESTClient{
			Client: restfake.CreateHTTPClient(frt.run),
		}

		restClient, _ := rest.RESTClientFor(&rest.Config{
			ContentConfig: rest.ContentConfig{
				NegotiatedSerializer: scheme.Codecs,
				GroupVersion:         &metav1.SchemeGroupVersion,
			},
		})
		restClient.Client = fakeClient.Client

		controller := &K0sController{
			Client:                    testEnv,
			workloadClusterKubeClient: kubernetes.New(restClient),
		}
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			rc, err := controller.newReplicasStatusComputer(ctx, cluster, kcp)
			assert.NoError(t, err)
			ps, ok := rc.(*planStatus)
			assert.True(t, ok)
			assert.Equal(t, expectedPlanStatusComputer.plan.Name, ps.plan.Name)
		}, 10*time.Second, 100*time.Millisecond)

	})
	t.Run("inplace strategy returns a machine status computer if Plan resource is not found", func(t *testing.T) {
		ns, err := testEnv.CreateNamespace(ctx, "test-inplace-strategy-returns-machine-status-computer")
		require.NoError(t, err)

		cluster, kcp, _ := createClusterWithControlPlane(ns.Name)
		require.NoError(t, testEnv.Create(ctx, cluster))

		kcp.Spec.UpdateStrategy = cpv1beta1.UpdateInPlace
		require.NoError(t, testEnv.Create(ctx, kcp))

		firstMachinesForKCP := &clusterv1.Machine{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Machine",
				APIVersion: "cluster.x-k8s.io/v1beta1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "machine1",
				Namespace: ns.Name,
				Labels: map[string]string{
					clusterv1.ClusterNameLabel:         cluster.Name,
					clusterv1.MachineControlPlaneLabel: "",
				},
			},
			Spec: clusterv1.MachineSpec{
				ClusterName: cluster.Name,
			},
		}
		expectedMachine := firstMachinesForKCP.DeepCopy()
		require.NoError(t, testEnv.Create(ctx, firstMachinesForKCP))

		defer func(do ...client.Object) {
			require.NoError(t, testEnv.Cleanup(ctx, do...))
		}(cluster, kcp, firstMachinesForKCP, ns)

		expectedMachineStatusComputer := &machineStatus{
			machines: collections.Machines{
				expectedMachine.Name: expectedMachine,
			},
		}

		frt := &fakeRoundTripper{}
		fakeClient := &restfake.RESTClient{
			Client: restfake.CreateHTTPClient(frt.run),
		}

		restClient, _ := rest.RESTClientFor(&rest.Config{
			ContentConfig: rest.ContentConfig{
				NegotiatedSerializer: scheme.Codecs,
				GroupVersion:         &metav1.SchemeGroupVersion,
			},
		})
		restClient.Client = fakeClient.Client

		controller := &K0sController{
			Client:                    testEnv,
			workloadClusterKubeClient: kubernetes.New(restClient),
		}

		require.EventuallyWithT(t, func(c *assert.CollectT) {
			rc, err := controller.newReplicasStatusComputer(ctx, cluster, kcp)
			assert.NoError(t, err)
			ms, ok := rc.(*machineStatus)
			assert.True(t, ok)
			assert.Len(t, ms.machines, 1)
			assert.Equal(t, expectedMachineStatusComputer.machines[expectedMachine.Name].Name, ms.machines[expectedMachine.Name].Name)
		}, 10*time.Second, 100*time.Millisecond)

	})
	t.Run("recreate strategy return a machine status computer", func(t *testing.T) {
		ns, err := testEnv.CreateNamespace(ctx, "test-recreate-strategy-returns-machine-status-computer")
		require.NoError(t, err)

		cluster, kcp, _ := createClusterWithControlPlane(ns.Name)
		require.NoError(t, testEnv.Create(ctx, cluster))

		kcp.Spec.UpdateStrategy = cpv1beta1.UpdateRecreate
		require.NoError(t, testEnv.Create(ctx, kcp))

		firstMachinesForKCP := &clusterv1.Machine{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Machine",
				APIVersion: "cluster.x-k8s.io/v1beta1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      "machine1",
				Namespace: ns.Name,
				Labels: map[string]string{
					clusterv1.ClusterNameLabel:         cluster.Name,
					clusterv1.MachineControlPlaneLabel: "",
				},
			},
			Spec: clusterv1.MachineSpec{
				ClusterName: cluster.Name,
			},
		}
		expectedMachine := firstMachinesForKCP.DeepCopy()
		require.NoError(t, testEnv.Create(ctx, firstMachinesForKCP))

		defer func(do ...client.Object) {
			require.NoError(t, testEnv.Cleanup(ctx, do...))
		}(cluster, kcp, firstMachinesForKCP, ns)

		expectedMachineStatusComputer := &machineStatus{
			machines: collections.Machines{
				expectedMachine.Name: expectedMachine,
			},
		}
		controller := &K0sController{
			Client: testEnv,
		}
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			rc, err := controller.newReplicasStatusComputer(ctx, cluster, kcp)
			assert.NoError(t, err)
			ms, ok := rc.(*machineStatus)
			assert.True(t, ok)
			assert.Len(t, ms.machines, 1)
			assert.Equal(t, expectedMachineStatusComputer.machines[expectedMachine.Name].Name, ms.machines[expectedMachine.Name].Name)
		}, 10*time.Second, 100*time.Millisecond)
	})
}

func TestPlanStatusCompute(t *testing.T) {
	t.Run("plan without commands", func(t *testing.T) {

		var kcp *cpv1beta1.K0sControlPlane

		rc := planStatus{
			plan: autopilot.Plan{
				Spec: autopilot.PlanSpec{
					Commands: []autopilot.PlanCommand{},
				},
			},
		}
		require.Error(t, rc.compute(kcp))
		require.Nil(t, kcp)

		rc = planStatus{
			plan: autopilot.Plan{
				Spec: autopilot.PlanSpec{
					Commands: []autopilot.PlanCommand{
						{
							K0sUpdate: &autopilot.PlanCommandK0sUpdate{},
						},
					},
				},
				Status: autopilot.PlanStatus{
					Commands: []autopilot.PlanCommandStatus{},
				},
			},
		}
		require.Error(t, rc.compute(kcp))
		require.Nil(t, kcp)
	})

	t.Run("plan without a K0sUpdate", func(t *testing.T) {
		var kcp *cpv1beta1.K0sControlPlane

		rc := planStatus{
			plan: autopilot.Plan{
				Spec: autopilot.PlanSpec{
					Commands: []autopilot.PlanCommand{
						{
							AirgapUpdate: &autopilot.PlanCommandAirgapUpdate{},
						},
					},
				},
				Status: autopilot.PlanStatus{
					Commands: []autopilot.PlanCommandStatus{
						{
							AirgapUpdate: &autopilot.PlanCommandAirgapUpdateStatus{},
						},
					},
				},
			},
		}
		require.Error(t, rc.compute(kcp))
		require.Nil(t, kcp)

		rc = planStatus{
			plan: autopilot.Plan{
				Spec: autopilot.PlanSpec{
					Commands: []autopilot.PlanCommand{
						{
							K0sUpdate: &autopilot.PlanCommandK0sUpdate{},
						},
					},
				},
				Status: autopilot.PlanStatus{
					Commands: []autopilot.PlanCommandStatus{
						{
							AirgapUpdate: &autopilot.PlanCommandAirgapUpdateStatus{},
						},
					},
				},
			},
		}
		require.Error(t, rc.compute(kcp))
		require.Nil(t, kcp)
	})

	t.Run("plan state unsupported", func(t *testing.T) {
		originalKcp := &cpv1beta1.K0sControlPlane{}
		expectedKcp := &cpv1beta1.K0sControlPlane{}

		rc := planStatus{
			plan: autopilot.Plan{
				Spec: autopilot.PlanSpec{
					Commands: []autopilot.PlanCommand{
						{
							K0sUpdate: &autopilot.PlanCommandK0sUpdate{},
						},
					},
				},
				Status: autopilot.PlanStatus{
					State: core.PlanMissingSignalNode,
					Commands: []autopilot.PlanCommandStatus{
						{
							K0sUpdate: &autopilot.PlanCommandK0sUpdateStatus{},
						},
					},
				},
			},
		}
		require.ErrorIs(t, rc.compute(originalKcp), errUnsupportedPlanState)
		require.Equal(t, expectedKcp, originalKcp)
	})

	t.Run("plan is completed", func(t *testing.T) {
		originalKcp := &cpv1beta1.K0sControlPlane{
			Spec: cpv1beta1.K0sControlPlaneSpec{
				Replicas: 2,
			},
			Status: cpv1beta1.K0sControlPlaneStatus{
				UpdatedReplicas:     0,
				ReadyReplicas:       2,
				UnavailableReplicas: 0,
				Replicas:            2,
				Version:             "v1.31.0+k0s.0",
			},
		}
		expectedKcp := &cpv1beta1.K0sControlPlane{
			Spec: cpv1beta1.K0sControlPlaneSpec{
				Replicas: 2,
			},
			Status: cpv1beta1.K0sControlPlaneStatus{
				UpdatedReplicas:     2,
				ReadyReplicas:       2,
				UnavailableReplicas: 0,
				Replicas:            2,
				Version:             "v1.31.0+k0s.0",
			},
		}

		rc := planStatus{
			plan: autopilot.Plan{
				Spec: autopilot.PlanSpec{
					Commands: []autopilot.PlanCommand{
						{
							K0sUpdate: &autopilot.PlanCommandK0sUpdate{
								Version: "v1.31.0+k0s.0",
							},
						},
					},
				},
				Status: autopilot.PlanStatus{
					State: core.PlanCompleted,
					Commands: []autopilot.PlanCommandStatus{
						{
							K0sUpdate: &autopilot.PlanCommandK0sUpdateStatus{},
						},
					},
				},
			},
		}
		require.NoError(t, rc.compute(originalKcp))
		require.Equal(t, expectedKcp, originalKcp)
	})

	t.Run("plan is mutating controlplane", func(t *testing.T) {
		originalKcp := &cpv1beta1.K0sControlPlane{
			Status: cpv1beta1.K0sControlPlaneStatus{
				UpdatedReplicas:     0,
				ReadyReplicas:       4,
				UnavailableReplicas: 0,
				Replicas:            4,
				Version:             "v1.31.0+k0s.0",
			},
		}

		expectedKcp := &cpv1beta1.K0sControlPlane{
			Status: cpv1beta1.K0sControlPlaneStatus{
				UpdatedReplicas:     1,
				ReadyReplicas:       2,
				UnavailableReplicas: 2,
				Replicas:            4,
				Version:             "v1.31.0+k0s.0",
			},
		}

		rc := planStatus{
			plan: autopilot.Plan{
				Spec: autopilot.PlanSpec{
					Commands: []autopilot.PlanCommand{
						{
							K0sUpdate: &autopilot.PlanCommandK0sUpdate{},
						},
					},
				},
				Status: autopilot.PlanStatus{
					State: core.PlanSchedulableWait,
					Commands: []autopilot.PlanCommandStatus{
						{
							K0sUpdate: &autopilot.PlanCommandK0sUpdateStatus{
								Controllers: []autopilot.PlanCommandTargetStatus{
									{
										Name:  "controller1",
										State: core.SignalSent,
									},
									{
										Name:  "controller2",
										State: core.SignalCompleted,
									},
									{
										Name:  "controller3",
										State: core.SignalSent,
									},
									{
										Name:  "controller4",
										State: core.SignalPending,
									},
								},
							},
						},
					},
				},
			},
		}
		require.ErrorIs(t, rc.compute(originalKcp), errUpgradeNotCompleted)
		require.Equal(t, expectedKcp, originalKcp)
	})
}

func Test_machineStatusCompute(t *testing.T) {
	t.Run("test no machines", func(t *testing.T) {
		kcp := &cpv1beta1.K0sControlPlane{
			Spec: cpv1beta1.K0sControlPlaneSpec{
				Version:  "v1.31.0",
				Replicas: 3,
			},
		}

		rc := &machineStatus{
			machines: collections.Machines{},
		}
		err := rc.compute(kcp)

		require.NoError(t, err)
		require.Zero(t, kcp.Status.Replicas)
		require.Empty(t, kcp.Status.Version)
	})

	t.Run("test all machines are ready", func(t *testing.T) {
		kcp := &cpv1beta1.K0sControlPlane{
			Spec: cpv1beta1.K0sControlPlaneSpec{
				Version:  "v1.31.0",
				Replicas: 2,
			},
		}
		machines := collections.Machines{
			"machine1": &clusterv1.Machine{
				Spec: clusterv1.MachineSpec{
					Version: ptr.To[string]("v1.31.0"),
				},
				Status: clusterv1.MachineStatus{
					Phase: string(clusterv1.MachinePhaseRunning),
				},
			},
			"machine2": &clusterv1.Machine{
				Spec: clusterv1.MachineSpec{
					Version: ptr.To[string]("v1.30.0"),
				},
				Status: clusterv1.MachineStatus{
					Phase: string(clusterv1.MachinePhaseRunning),
				},
			},
		}

		rc := &machineStatus{
			machines: machines,
		}
		err := rc.compute(kcp)

		require.NoError(t, err)
		require.Equal(t, int32(2), kcp.Status.Replicas)
		require.Equal(t, int32(0), kcp.Status.UnavailableReplicas)
		require.Equal(t, int32(1), kcp.Status.UpdatedReplicas)
		require.Equal(t, int32(2), kcp.Status.ReadyReplicas)
		require.Equal(t, "v1.30.0", kcp.Status.Version)
	})

	t.Run("test all machines are ready but not using suffix", func(t *testing.T) {
		kcp := &cpv1beta1.K0sControlPlane{
			Spec: cpv1beta1.K0sControlPlaneSpec{
				Version:  "v1.31.0+k0s.0",
				Replicas: 2,
			},
		}
		machines := collections.Machines{
			"machine1": &clusterv1.Machine{
				Spec: clusterv1.MachineSpec{
					Version: ptr.To[string]("v1.31.0"),
				},
				Status: clusterv1.MachineStatus{
					Phase: string(clusterv1.MachinePhaseRunning),
				},
			},
			"machine2": &clusterv1.Machine{
				Spec: clusterv1.MachineSpec{
					Version: ptr.To[string]("v1.30.0"),
				},
				Status: clusterv1.MachineStatus{
					Phase: string(clusterv1.MachinePhaseRunning),
				},
			},
		}

		rc := &machineStatus{
			machines: machines,
		}
		err := rc.compute(kcp)

		require.NoError(t, err)
		require.Equal(t, int32(2), kcp.Status.Replicas)
		require.Equal(t, int32(0), kcp.Status.UnavailableReplicas)
		require.Equal(t, int32(1), kcp.Status.UpdatedReplicas)
		require.Equal(t, int32(2), kcp.Status.ReadyReplicas)
		require.Equal(t, "v1.30.0+k0s.0", kcp.Status.Version)
	})

	t.Run("test non existent machines are unavailable", func(t *testing.T) {
		kcp := &cpv1beta1.K0sControlPlane{
			Spec: cpv1beta1.K0sControlPlaneSpec{
				Version:  "v1.31.0",
				Replicas: 3,
			},
		}
		machines := collections.Machines{
			"machine1": &clusterv1.Machine{
				Spec: clusterv1.MachineSpec{
					Version: ptr.To[string]("v1.31.0"),
				},
				Status: clusterv1.MachineStatus{
					Phase: string(clusterv1.MachinePhaseRunning),
				},
			},
			"machine2": &clusterv1.Machine{
				Spec: clusterv1.MachineSpec{
					Version: ptr.To[string]("v1.30.0"),
				},
				Status: clusterv1.MachineStatus{
					Phase: string(clusterv1.MachinePhaseRunning),
				},
			},
		}

		rc := &machineStatus{
			machines: machines,
		}
		err := rc.compute(kcp)

		require.NoError(t, err)
		require.Equal(t, int32(2), kcp.Status.Replicas)
		require.Equal(t, int32(1), kcp.Status.UnavailableReplicas)
		require.Equal(t, int32(1), kcp.Status.UpdatedReplicas)
		require.Equal(t, int32(2), kcp.Status.ReadyReplicas)
		require.Equal(t, "v1.30.0", kcp.Status.Version)
	})

	t.Run("test some machines are not ready", func(t *testing.T) {
		kcp := &cpv1beta1.K0sControlPlane{
			Spec: cpv1beta1.K0sControlPlaneSpec{
				Version:  "v1.31.0",
				Replicas: 2,
			},
		}
		machines := collections.Machines{
			"machine1": &clusterv1.Machine{
				Spec: clusterv1.MachineSpec{
					Version: ptr.To[string]("v1.31.0"),
				},
				Status: clusterv1.MachineStatus{
					Phase: string(clusterv1.MachinePhaseRunning),
				},
			},
			"machine2": &clusterv1.Machine{
				Spec: clusterv1.MachineSpec{
					Version: ptr.To[string]("v1.30.0"),
				},
				Status: clusterv1.MachineStatus{
					Phase: string(clusterv1.MachinePhaseProvisioning),
				},
			},
			"machine3": &clusterv1.Machine{
				Spec: clusterv1.MachineSpec{
					Version: ptr.To[string]("v1.30.0"),
				},
				Status: clusterv1.MachineStatus{
					Phase: string(clusterv1.MachinePhaseFailed),
				},
			},
		}

		rc := &machineStatus{
			machines: machines,
		}
		err := rc.compute(kcp)

		require.NoError(t, err)
		require.Equal(t, int32(3), kcp.Status.Replicas)
		require.Equal(t, int32(2), kcp.Status.UnavailableReplicas)
		require.Equal(t, int32(1), kcp.Status.UpdatedReplicas)
		require.Equal(t, int32(1), kcp.Status.ReadyReplicas)
		require.Equal(t, "v1.30.0", kcp.Status.Version)
	})

	t.Run("machines provisioned but kcp not using --enable-worker", func(t *testing.T) {
		kcp := &cpv1beta1.K0sControlPlane{
			Spec: cpv1beta1.K0sControlPlaneSpec{
				Version:  "v1.31.0",
				Replicas: 2,
			},
		}
		machines := collections.Machines{
			"machine1": &clusterv1.Machine{
				Spec: clusterv1.MachineSpec{
					Version: ptr.To[string]("v1.31.0"),
				},
				Status: clusterv1.MachineStatus{
					Phase: string(clusterv1.MachinePhaseProvisioned),
				},
			},
			"machine2": &clusterv1.Machine{
				Spec: clusterv1.MachineSpec{
					Version: ptr.To[string]("v1.30.0"),
				},
				Status: clusterv1.MachineStatus{
					Phase: string(clusterv1.MachinePhaseProvisioning),
				},
			},
			"machine3": &clusterv1.Machine{
				Spec: clusterv1.MachineSpec{
					Version: ptr.To[string]("v1.30.0"),
				},
				Status: clusterv1.MachineStatus{
					Phase: string(clusterv1.MachinePhaseFailed),
				},
			},
		}

		rc := &machineStatus{
			machines: machines,
		}
		err := rc.compute(kcp)

		require.NoError(t, err)
		require.Equal(t, int32(3), kcp.Status.Replicas)
		require.Equal(t, int32(2), kcp.Status.UnavailableReplicas)
		require.Equal(t, int32(1), kcp.Status.UpdatedReplicas)
		require.Equal(t, int32(1), kcp.Status.ReadyReplicas)
		require.Equal(t, "v1.30.0", kcp.Status.Version)

	})

	t.Run("some machines stuck as provisioned but kcp using --enable-worker", func(t *testing.T) {
		kcp := &cpv1beta1.K0sControlPlane{
			Spec: cpv1beta1.K0sControlPlaneSpec{
				Version:  "v1.31.0",
				Replicas: 2,
				K0sConfigSpec: bootstrapv1.K0sConfigSpec{
					Args: []string{"--enable-worker"},
				},
			},
		}
		machines := collections.Machines{
			"machine1": &clusterv1.Machine{
				Spec: clusterv1.MachineSpec{
					Version: ptr.To[string]("v1.31.0"),
				},
				Status: clusterv1.MachineStatus{
					Phase: string(clusterv1.MachinePhaseProvisioned),
				},
			},
			"machine2": &clusterv1.Machine{
				Spec: clusterv1.MachineSpec{
					Version: ptr.To[string]("v1.30.0"),
				},
				Status: clusterv1.MachineStatus{
					Phase: string(clusterv1.MachinePhaseRunning),
				},
			},
		}

		rc := &machineStatus{
			machines: machines,
		}
		err := rc.compute(kcp)

		require.NoError(t, err)
		require.Equal(t, int32(2), kcp.Status.Replicas)
		require.Equal(t, int32(1), kcp.Status.UnavailableReplicas)
		require.Equal(t, int32(1), kcp.Status.UpdatedReplicas)
		require.Equal(t, int32(1), kcp.Status.ReadyReplicas)
		require.Equal(t, "v1.30.0", kcp.Status.Version)

	})
}

func Test_versionMatches(t *testing.T) {
	type args struct {
		machine *clusterv1.Machine
		ver     string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "version matches, both without suffix",
			args: args{
				machine: &clusterv1.Machine{
					Spec: clusterv1.MachineSpec{
						Version: ptr.To("v1.31.0"),
					},
				},
				ver: "v1.31.0",
			},
			want: true,
		},
		{
			name: "version does not match",
			args: args{
				machine: &clusterv1.Machine{
					Spec: clusterv1.MachineSpec{
						Version: ptr.To("v1.31.0"),
					},
				},
				ver: "v1.30.0",
			},
			want: false,
		},
		{
			name: "semver version match, machine version is missing the suffix",
			args: args{
				machine: &clusterv1.Machine{
					Spec: clusterv1.MachineSpec{
						Version: ptr.To("v1.31.0"),
					},
				},
				ver: "v1.31.0+k0s.0",
			},
			want: true,
		},
		{
			name: "semver version match, kcp version is missing the suffix",
			args: args{
				machine: &clusterv1.Machine{
					Spec: clusterv1.MachineSpec{
						Version: ptr.To("v1.31.0+k0s.0"),
					},
				},
				ver: "v1.31.0",
			},
			want: true,
		},
		{
			name: "versions match, both with the suffix",
			args: args{
				machine: &clusterv1.Machine{
					Spec: clusterv1.MachineSpec{
						Version: ptr.To("v1.31.0+k0s.0"),
					},
				},
				ver: "v1.31.0+k0s.0",
			},
			want: true,
		},
		{
			name: "versions do not match, machine version is missing",
			args: args{
				machine: &clusterv1.Machine{
					Spec: clusterv1.MachineSpec{
						Version: nil,
					},
				},
				ver: "v1.31.0+k0s.0",
			},
			want: false,
		},
		{
			name: "versions do not match, machine version is empty",
			args: args{
				machine: &clusterv1.Machine{
					Spec: clusterv1.MachineSpec{
						Version: ptr.To(""),
					},
				},
				ver: "v1.31.0+k0s.0",
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := versionMatches(tt.args.machine, tt.args.ver); got != tt.want {
				t.Errorf("versionMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}
