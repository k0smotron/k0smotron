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

package e2e

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/k0sproject/k0smotron/e2e/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/test/framework"
	capiframework "sigs.k8s.io/cluster-api/test/framework"
	"sigs.k8s.io/cluster-api/test/framework/clusterctl"
	capiutil "sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	configMapName    = "mhc-test"
	configMapDataKey = "signal"
	failLabelValue   = "fail"
)

func TestControlplaneRemediation(t *testing.T) {
	setupAndRun(t, controlplaneRemediationSpec)
}

// Verifies that K0sControlPlane controller properly remediates the Machines.
func controlplaneRemediationSpec(t *testing.T) {
	testName := "kcp-remediation"

	// Setup a Namespace where to host objects for this spec and create a watcher for the namespace events.
	namespace, _ := util.SetupSpecNamespace(ctx, testName, managementClusterProxy, artifactFolder)

	// creates the mhc-test ConfigMap that will be used to control machines bootstrap during the remediation tests.
	createConfigMapForMachinesBootstrapSignal(ctx, managementClusterProxy.GetClient(), namespace.Name)

	clusterName := fmt.Sprintf("%s-%s", testName, capiutil.RandomString(6))

	serverAddr, err := getServerAddr(ctx, managementClusterProxy)
	require.NoError(t, err)

	authToken, err := getAuthenticationToken(ctx, managementClusterProxy, namespace.GetName())
	require.NoError(t, err)

	workloadClusterTemplate := clusterctl.ConfigCluster(ctx, clusterctl.ConfigClusterInput{
		ClusterctlConfigPath: clusterctlConfigPath,
		KubeconfigPath:       managementClusterProxy.GetKubeconfigPath(),
		// select cluster templates
		Flavor: "kcp-remediation",

		Namespace:                namespace.Name,
		ClusterName:              clusterName,
		KubernetesVersion:        e2eConfig.GetVariable(KubernetesVersion),
		ControlPlaneMachineCount: ptr.To[int64](3),
		// TODO: make infra provider configurable
		InfrastructureProvider: "docker",
		LogFolder:              filepath.Join(artifactFolder, "clusters", managementClusterProxy.GetName()),
		ClusterctlVariables: map[string]string{
			"CLUSTER_NAME": clusterName,
			"NAMESPACE":    namespace.Name,
			"TOKEN":        authToken,
			"SERVER":       serverAddr,
		},
	})
	require.NotNil(t, workloadClusterTemplate)

	require.Eventually(t, func() bool {
		return managementClusterProxy.CreateOrUpdate(ctx, workloadClusterTemplate) == nil
	}, 10*time.Second, 1*time.Second, "Failed to apply the cluster template")

	cluster, err := util.DiscoveryAndWaitForCluster(ctx, capiframework.DiscoveryAndWaitForClusterInput{
		Getter:    managementClusterProxy.GetClient(),
		Namespace: namespace.Name,
		Name:      clusterName,
	}, util.GetInterval(e2eConfig, testName, "wait-cluster"))
	require.NoError(t, err)

	// The first CP machine comes up but it does not complete bootstrap

	fmt.Println("FIRST CONTROL PLANE MACHINE")

	fmt.Println("Wait for the cluster to get stuck with the first CP machine not completing the bootstrap")

	waitMachineInterval := util.GetInterval(e2eConfig, testName, "wait-machines")

	allMachines, newMachines, err := util.WaitForMachines(ctx, util.WaitForMachinesInput{
		Lister:                   managementClusterProxy.GetClient(),
		Namespace:                namespace.Name,
		ClusterName:              clusterName,
		ExpectedReplicas:         1,
		WaitForMachinesIntervals: waitMachineInterval,
	})
	require.NoError(t, err)
	require.Len(t, allMachines, 1)
	require.Len(t, newMachines, 1)
	firstMachineName := newMachines[0]
	firstMachine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      firstMachineName,
			Namespace: namespace.Name,
		},
	}

	require.NoError(t, managementClusterProxy.GetClient().Get(ctx, client.ObjectKeyFromObject(firstMachine), firstMachine))
	require.Nil(t, firstMachine.Status.NodeRef)
	fmt.Printf("Machine %s is up but still bootstrapping\n", firstMachineName)

	// Intentionally trigger remediation on the first CP, and validate the first machine is deleted and a replacement should come up.

	fmt.Println("REMEDIATING FIRST CONTROL PLANE MACHINE")

	fmt.Printf("Add mhc-test:fail label to machine %s so it will be immediately remediated\n", firstMachineName)
	firstMachineWithLabel := firstMachine.DeepCopy()
	firstMachineWithLabel.Labels["mhc-test"] = failLabelValue
	require.NoError(t, managementClusterProxy.GetClient().Patch(ctx, firstMachineWithLabel, client.MergeFrom(firstMachine)), "Failed to patch machine %d", firstMachineName)

	fmt.Printf("Wait for the first CP machine to be remediated, and the replacement machine to come up, but again get stuck with the Machine not completing the bootstrap\n")
	allMachines, newMachines, err = util.WaitForMachines(ctx, util.WaitForMachinesInput{
		Lister:                   managementClusterProxy.GetClient(),
		Namespace:                namespace.Name,
		ClusterName:              clusterName,
		ExpectedReplicas:         1,
		ExpectedDeletedMachines:  map[string]string{string(firstMachine.GetUID()): firstMachineName},
		WaitForMachinesIntervals: waitMachineInterval,
	})
	require.NoError(t, err)
	require.Len(t, allMachines, 1)
	require.Len(t, newMachines, 1)
	firstMachineReplacementName := newMachines[0]
	firstMachineReplacement := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      firstMachineReplacementName,
			Namespace: namespace.Name,
		},
	}
	require.NoError(t, managementClusterProxy.GetClient().Get(ctx, client.ObjectKeyFromObject(firstMachineReplacement), firstMachineReplacement), "Failed to get machine %d", firstMachineReplacementName)
	require.Nil(t, firstMachineReplacement.Status.NodeRef)
	fmt.Printf("Machine %s is up but still bootstrapping \n", firstMachineReplacementName)

	// The firstMachine replacement is up, meaning that the test validated that remediation of the first CP machine works (note: first CP is a special case because the cluster is not initialized yet).
	// In order to test remediation of other machine while provisioning we unblock bootstrap of the first CP replacement
	// and wait for the second cp machine to come up.

	fmt.Println("FIRST CONTROL PLANE MACHINE SUCCESSFULLY REMEDIATED!")

	fmt.Printf("Unblock bootstrap for Machine %s and wait for it to be provisioned\n", firstMachineReplacementName)
	sendSignalToBootstrappingMachine(ctx, t, sendSignalToBootstrappingMachineInput{
		Client:    managementClusterProxy.GetClient(),
		Namespace: namespace.Name,
		Machine:   firstMachineReplacementName,
		Signal:    "pass",
	})
	fmt.Printf("Waiting for Machine %s to be provisioned\n", firstMachineReplacementName)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.NoError(c, managementClusterProxy.GetClient().Get(ctx, client.ObjectKeyFromObject(firstMachineReplacement), firstMachineReplacement))
		assert.NotNil(c, firstMachineReplacement.Status.NodeRef)
	}, 3*time.Minute, 10*time.Second, "Machine %s failed to be provisioned", firstMachineReplacementName)

	fmt.Println("FIRST CONTROL PLANE MACHINE UP AND RUNNING!")
	fmt.Println("START PROVISIONING OF SECOND CONTROL PLANE MACHINE!")

	fmt.Println("Wait for the cluster to get stuck with the second CP machine not completing the bootstrap")
	allMachines, newMachines, err = util.WaitForMachines(ctx, util.WaitForMachinesInput{
		Lister:                   managementClusterProxy.GetClient(),
		Namespace:                namespace.Name,
		ClusterName:              clusterName,
		ExpectedReplicas:         2,
		ExpectedDeletedMachines:  map[string]string{},
		ExpectedOldMachines:      map[string]string{string(firstMachineReplacement.GetUID()): firstMachineReplacementName},
		WaitForMachinesIntervals: waitMachineInterval,
	})
	require.NoError(t, err)
	require.Len(t, allMachines, 2)
	require.Len(t, newMachines, 1)
	secondMachineName := newMachines[0]
	secondMachine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secondMachineName,
			Namespace: namespace.Name,
		},
	}
	require.NoError(t, managementClusterProxy.GetClient().Get(ctx, client.ObjectKeyFromObject(secondMachine), secondMachine), "Failed to get machine %d", secondMachineName)
	require.Nil(t, secondMachine.Status.NodeRef)
	fmt.Printf("Machine %s is up but still bootstrapping\n", secondMachineName)

	// Intentionally trigger remediation on the second CP and validate that also this one is deleted and a replacement should come up.

	fmt.Println("REMEDIATING SECOND CONTROL PLANE MACHINE")

	fmt.Printf("Add mhc-test:fail label to machine %s so it will be immediately remediated\n", firstMachineName)
	secondMachineWithLabel := secondMachine.DeepCopy()
	secondMachineWithLabel.Labels["mhc-test"] = failLabelValue
	require.NoError(t, managementClusterProxy.GetClient().Patch(ctx, secondMachineWithLabel, client.MergeFrom(secondMachine)), "Failed to patch machine %d", secondMachineName)

	fmt.Printf("Wait for the second CP machine to be remediated, and the replacement machine to come up, but again get stuck with the Machine not completing the bootstrap\n")
	allMachines, newMachines, err = util.WaitForMachines(ctx, util.WaitForMachinesInput{
		Lister:                   managementClusterProxy.GetClient(),
		Namespace:                namespace.Name,
		ClusterName:              clusterName,
		ExpectedReplicas:         2,
		ExpectedDeletedMachines:  map[string]string{string(secondMachine.GetUID()): secondMachineName},
		ExpectedOldMachines:      map[string]string{string(firstMachineReplacement.GetUID()): firstMachineName},
		WaitForMachinesIntervals: waitMachineInterval,
	})
	require.NoError(t, err)
	require.Len(t, allMachines, 2)
	require.Len(t, newMachines, 1)
	secondMachineReplacementName := newMachines[0]
	secondMachineReplacement := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secondMachineReplacementName,
			Namespace: namespace.Name,
		},
	}
	require.NoError(t, managementClusterProxy.GetClient().Get(ctx, client.ObjectKeyFromObject(secondMachineReplacement), secondMachineReplacement), "Failed to get machine %d", secondMachineReplacementName)
	require.Nil(t, secondMachineReplacement.Status.NodeRef)
	fmt.Printf("Machine %s is up but still bootstrapping\n", secondMachineReplacementName)

	// The secondMachine replacement is up, meaning that the test validated that remediation of the second CP machine works (note: this test remediation after the cluster is initialized, but not yet fully provisioned).
	// In order to test remediation after provisioning we unblock bootstrap of the second CP replacement as well as for the third CP machine.
	// and wait for the second cp machine to come up.

	fmt.Println("SECOND CONTROL PLANE MACHINE SUCCESSFULLY REMEDIATED!")

	fmt.Printf("Unblock bootstrap for Machine %s and wait for it to be provisioned\n", secondMachineReplacementName)
	sendSignalToBootstrappingMachine(ctx, t, sendSignalToBootstrappingMachineInput{
		Client:    managementClusterProxy.GetClient(),
		Namespace: namespace.Name,
		Machine:   secondMachineReplacementName,
		Signal:    "pass",
	})
	fmt.Printf("Waiting for Machine %s to be provisioned\n", secondMachineReplacementName)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.NoError(c, managementClusterProxy.GetClient().Get(ctx, client.ObjectKeyFromObject(secondMachineReplacement), secondMachineReplacement))
		assert.NotNil(c, secondMachineReplacement.Status.NodeRef)
	}, 3*time.Minute, 10*time.Second, "Machine %s failed to be provisioned", secondMachineReplacementName)

	fmt.Println("SECOND CONTROL PLANE MACHINE UP AND RUNNING!")
	fmt.Println("START PROVISIONING OF THIRD CONTROL PLANE MACHINE!")

	fmt.Println("Wait for the cluster to get stuck with the third CP machine not completing the bootstrap")
	allMachines, newMachines, err = util.WaitForMachines(ctx, util.WaitForMachinesInput{
		Lister:                   managementClusterProxy.GetClient(),
		Namespace:                namespace.Name,
		ClusterName:              clusterName,
		ExpectedReplicas:         3,
		ExpectedDeletedMachines:  map[string]string{},
		ExpectedOldMachines:      map[string]string{string(firstMachineReplacement.GetUID()): firstMachineReplacementName, string(secondMachineReplacement.GetUID()): secondMachineReplacementName},
		WaitForMachinesIntervals: waitMachineInterval,
	})
	require.NoError(t, err)
	require.Len(t, allMachines, 3)
	require.Len(t, newMachines, 1)
	thirdMachineName := newMachines[0]
	thirdMachine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      thirdMachineName,
			Namespace: namespace.Name,
		},
	}
	require.NoError(t, managementClusterProxy.GetClient().Get(ctx, client.ObjectKeyFromObject(thirdMachine), thirdMachine), "Failed to get machine %d", thirdMachineName)
	require.Nil(t, thirdMachine.Status.NodeRef)
	fmt.Printf("Machine %s is up but still bootstrapping\n", thirdMachineName)

	fmt.Printf("Unblock bootstrap for Machine %s and wait for it to be provisioned\n", thirdMachineName)
	sendSignalToBootstrappingMachine(ctx, t, sendSignalToBootstrappingMachineInput{
		Client:    managementClusterProxy.GetClient(),
		Namespace: namespace.Name,
		Machine:   thirdMachineName,
		Signal:    "pass",
	})
	fmt.Printf("Waiting for Machine %s to be provisioned\n", thirdMachineName)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.NoError(c, managementClusterProxy.GetClient().Get(ctx, client.ObjectKeyFromObject(thirdMachine), thirdMachine))
		assert.NotNil(c, thirdMachine.Status.NodeRef)
	}, 3*time.Minute, 10*time.Second, "Machine %s failed to be provisioned", thirdMachineName)

	fmt.Println("ALL THE CONTROL PLANE MACHINES SUCCESSFULLY PROVISIONED!")

	// We now want to test remediation of a CP machine already provisioned.
	// In order to do so we need to apply both mhc-test:fail as well as setting an unhealthy condition in order to trigger remediation

	fmt.Println("REMEDIATING THIRD CP")

	fmt.Printf("Add mhc-test:fail label to machine %s and set an unhealthy condition on the node so it will be immediately remediated\n", thirdMachineName)
	thirdMachineWithLabel := thirdMachine.DeepCopy()
	thirdMachineWithLabel.Labels["mhc-test"] = failLabelValue
	require.NoError(t, managementClusterProxy.GetClient().Patch(ctx, thirdMachineWithLabel, client.MergeFrom(thirdMachine)), "Failed to patch machine %d", thirdMachineName)

	unhealthyNodeCondition := corev1.NodeCondition{
		Type:               "e2e.remediation.condition",
		Status:             "False",
		LastTransitionTime: metav1.Time{Time: time.Now()},
	}
	framework.PatchNodeCondition(ctx, framework.PatchNodeConditionInput{
		ClusterProxy:  managementClusterProxy,
		Cluster:       cluster,
		NodeCondition: unhealthyNodeCondition,
		Machine:       *thirdMachine, // TODO: make this a pointer.
	})

	fmt.Printf("Wait for the third CP machine to be remediated, and the replacement machine to come up, but again get stuck with the Machine not completing the bootstrap\n")
	allMachines, newMachines, err = util.WaitForMachines(ctx, util.WaitForMachinesInput{
		Lister:                   managementClusterProxy.GetClient(),
		Namespace:                namespace.Name,
		ClusterName:              clusterName,
		ExpectedReplicas:         3,
		ExpectedDeletedMachines:  map[string]string{string(thirdMachine.GetUID()): thirdMachineName},
		ExpectedOldMachines:      map[string]string{string(firstMachineReplacement.GetUID()): firstMachineReplacementName, string(secondMachineReplacement.GetUID()): secondMachineReplacementName},
		WaitForMachinesIntervals: waitMachineInterval,
	})
	require.NoError(t, err)
	require.Len(t, allMachines, 3)
	require.Len(t, newMachines, 1)
	thirdMachineReplacementName := newMachines[0]
	thirdMachineReplacement := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      thirdMachineReplacementName,
			Namespace: namespace.Name,
		},
	}
	require.NoError(t, managementClusterProxy.GetClient().Get(ctx, client.ObjectKeyFromObject(thirdMachineReplacement), thirdMachineReplacement), "Failed to get machine %d", thirdMachineReplacement)
	require.Nil(t, thirdMachineReplacement.Status.NodeRef)
	fmt.Printf("Machine %s is up but still bootstrapping\n", thirdMachineReplacementName)

	// The thirdMachine replacement is up, meaning that the test validated that remediation of the third CP machine works (note: this test remediation after the cluster is fully provisioned).

	fmt.Println("THIRD CP SUCCESSFULLY REMEDIATED!")

	fmt.Printf("Unblock bootstrap for Machine %s and wait for it to be provisioned\n", thirdMachineReplacementName)
	sendSignalToBootstrappingMachine(ctx, t, sendSignalToBootstrappingMachineInput{
		Client:    managementClusterProxy.GetClient(),
		Namespace: namespace.Name,
		Machine:   thirdMachineReplacementName,
		Signal:    "pass",
	})
	fmt.Printf("Waiting for Machine %s to be provisioned\n", thirdMachineReplacementName)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.NoError(c, managementClusterProxy.GetClient().Get(ctx, client.ObjectKeyFromObject(thirdMachineReplacement), thirdMachineReplacement))
		assert.NotNil(c, thirdMachineReplacement.Status.NodeRef)
	}, 3*time.Minute, 10*time.Second, "Machine %s failed to be provisioned", thirdMachineReplacementName)

	fmt.Println("ALL THE CONTROL PLANE MACHINES SUCCESSFULLY REMEDIATED AND PROVISIONED!")
}

// getServerAddr returns the address to be used for accessing the management cluster from a workload cluster.
func getServerAddr(ctx context.Context, clusterProxy capiframework.ClusterProxy) (string, error) {
	// With CAPD, we can't just access the bootstrap cluster via 127.0.0.1:<port> from the
	// workload cluster. Instead we retrieve the server name from the cluster-info ConfigMap in the bootstrap
	// cluster (e.g. "https://test-z45p9k-control-plane:6443")
	clusterInfoCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-info",
			Namespace: metav1.NamespacePublic,
		},
	}

	err := clusterProxy.GetClient().Get(ctx, client.ObjectKeyFromObject(clusterInfoCM), clusterInfoCM)
	if err != nil {
		return "", err
	}

	kubeConfigString, ok := clusterInfoCM.Data["kubeconfig"]
	if !ok {
		return "", errors.New("cluster-info configmap data does not contain kubedonfig key")
	}

	kubeConfig, err := clientcmd.Load([]byte(kubeConfigString))
	if err != nil {
		return "", err
	}

	return kubeConfig.Clusters[""].Server, nil
}

// getAuthenticationToken returns a bearer authenticationToken with minimal RBAC permissions to access the mhc-test ConfigMap that will be used
// to control machines bootstrap during the remediation tests.
func getAuthenticationToken(ctx context.Context, managementClusterProxy framework.ClusterProxy, namespace string) (string, error) {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mhc-test",
			Namespace: namespace,
		},
	}
	err := managementClusterProxy.GetClient().Create(ctx, sa)
	if err != nil {
		return "", fmt.Errorf("failed to create mhc-test service account: %w", err)
	}

	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mhc-test",
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:         []string{"get", "list", "patch"},
				APIGroups:     []string{""},
				Resources:     []string{"configmaps"},
				ResourceNames: []string{"mhc-test"},
			},
		},
	}
	err = managementClusterProxy.GetClient().Create(ctx, role)
	if err != nil {
		return "", fmt.Errorf("failed to create mhc-test role: %w", err)
	}

	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mhc-test",
			Namespace: namespace,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				APIGroup:  "",
				Name:      "mhc-test",
				Namespace: namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "Role",
			Name:     "mhc-test",
		},
	}
	err = managementClusterProxy.GetClient().Create(ctx, roleBinding)
	if err != nil {
		return "", fmt.Errorf("failed to create mhc-test role binding: %w", err)
	}

	tokenRequest := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			ExpirationSeconds: ptr.To[int64](2 * 60 * 60), // 2 hours.
		},
	}
	tokenRequest, err = managementClusterProxy.GetClientSet().CoreV1().ServiceAccounts(namespace).CreateToken(ctx, "mhc-test", tokenRequest, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}

	return tokenRequest.Status.Token, nil
}

func createConfigMapForMachinesBootstrapSignal(ctx context.Context, writer client.Writer, namespace string) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
		},
		Data: map[string]string{
			configMapDataKey: "hold",
		},
	}
	return writer.Create(ctx, cm)
}

type sendSignalToBootstrappingMachineInput struct {
	Client    client.Client
	Namespace string
	Machine   string
	Signal    string
}

// sendSignalToBootstrappingMachine sends a signal to a machine stuck during bootstrap.
func sendSignalToBootstrappingMachine(ctx context.Context, t *testing.T, input sendSignalToBootstrappingMachineInput) {
	fmt.Printf("Sending bootstrap signal %s to Machine %s\n", input.Signal, input.Machine)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: input.Namespace,
		},
	}
	require.NoError(t, input.Client.Get(ctx, client.ObjectKeyFromObject(cm), cm), "failed to get mhc-test config map")

	cmWithSignal := cm.DeepCopy()
	cmWithSignal.Data[configMapDataKey] = input.Signal
	require.NoError(t, input.Client.Patch(ctx, cmWithSignal, client.MergeFrom(cm)), "failed to patch mhc-test config map")

	fmt.Printf("Waiting for Machine %s to acknowledge signal %s has been received\n", input.Machine, input.Signal)

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.NoError(c, input.Client.Get(ctx, client.ObjectKeyFromObject(cmWithSignal), cmWithSignal))
		assert.Equal(c, cmWithSignal.Data[configMapDataKey], fmt.Sprintf("ack-%s", input.Signal))
	}, time.Minute, 10*time.Second, "Failed to get ack signal from machine %s", input.Machine)

	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      input.Machine,
			Namespace: input.Namespace,
		},
	}
	require.NoError(t, input.Client.Get(ctx, client.ObjectKeyFromObject(machine), machine))

	// Resetting the signal in the config map
	cmWithSignal.Data[configMapDataKey] = "hold"
	require.NoError(t, input.Client.Patch(ctx, cmWithSignal, client.MergeFrom(cm)), "failed to patch mhc-test config map")
}
