package util

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/cluster-api/controllers/remote"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var ErrClusterConfigNotFound = errors.New("ClusterConfig not available yet")

func ReconcileDynamicConfig(ctx context.Context, cluster metav1.Object, cli client.Client, u unstructured.Unstructured) error {
	u.SetName("k0s")
	u.SetNamespace("kube-system")

	// Remove fields that can be configured only via the local k0s config
	// See: https://docs.k0sproject.io/stable/dynamic-configuration/#cluster-configuration-vs-controller-node-configuration
	//unstructured.RemoveNestedField(u.Object, "spec", "api") // This field is not really should be removed, requires some investigation on the k0s side
	unstructured.RemoveNestedField(u.Object, "spec", "storage")
	unstructured.RemoveNestedField(u.Object, "spec", "network", "controlPlaneLoadBalancing")

	b, err := u.MarshalJSON()
	if err != nil {
		return fmt.Errorf("failed to marshal unstructured config: %w", err)
	}

	chCS, err := remote.NewClusterClient(ctx, "k0smotron", cli, util.ObjectKey(cluster))
	if err != nil {
		return fmt.Errorf("failed to create workload cluster client: %w", err)
	}

	err = retry.OnError(wait.Backoff{
		Steps:    2,
		Duration: 100 * time.Millisecond,
		Factor:   5.0,
		Jitter:   0.5,
	}, func(err error) bool {
		return true
	}, func() error {
		return chCS.Patch(ctx, &u, client.RawPatch(client.Merge.Type(), b), []client.PatchOption{}...)
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to patch k0s config: %w", ErrClusterConfigNotFound)
		}
		return fmt.Errorf("failed to patch k0s config: %w", err)
	}

	return nil
}
