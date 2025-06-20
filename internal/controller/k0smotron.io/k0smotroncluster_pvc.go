package k0smotronio

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	km "github.com/k0sproject/k0smotron/api/k0smotron.io/v1beta1"
)

func (scope *kmcScope) reconcilePVC(ctx context.Context, kmc *km.Cluster) error {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling PVC")

	// volumeClaimTemplates are immutable, so we need to
	// update PVC, then delete the StatefulSet with --cascade=orphan and recreate it

	err := reconcileControlPlanePVC(ctx, kmc, scope.client)
	if err != nil {
		return fmt.Errorf("failed to reconcile control plane PVC: %w", err)
	}
	err = reconcileEtcdPVC(ctx, kmc, scope.client)
	if err != nil {
		return fmt.Errorf("failed to reconcile etcd PVC: %w", err)
	}

	return nil
}

func reconcileControlPlanePVC(ctx context.Context, kmc *km.Cluster, c client.Client) error {
	// Do nothing if the persistence type is not PVC
	if kmc.Spec.Persistence.Type != "pvc" {
		return nil
	}

	if kmc.Spec.Persistence.PersistentVolumeClaim.Name == "" {
		kmc.Spec.Persistence.PersistentVolumeClaim.Name = kmc.GetVolumeName()
	}

	return resizeStatefulSetAndPVC(ctx, kmc, *kmc.Spec.Persistence.PersistentVolumeClaim.Spec.Resources.Requests.Storage(), kmc.Spec.Replicas, kmc.GetStatefulSetName(), kmc.Spec.Persistence.PersistentVolumeClaim.Name, c)
}

func reconcileEtcdPVC(ctx context.Context, kmc *km.Cluster, c client.Client) error {
	return resizeStatefulSetAndPVC(ctx, kmc, kmc.Spec.Etcd.Persistence.Size, calculateDesiredReplicas(kmc), kmc.GetEtcdStatefulSetName(), "etcd-data", c)
}

func resizeStatefulSetAndPVC(ctx context.Context, kmc *km.Cluster, desiredStorageSize resource.Quantity, replicas int32, stsName, vctName string, c client.Client) error {
	var sts appsv1.StatefulSet
	err := c.Get(ctx, client.ObjectKey{Namespace: kmc.Namespace, Name: stsName}, &sts)
	if err != nil {
		// Do nothing if StatefulSet does not exist yet
		if apierrors.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("failed to get statefulset: %w", err)
	}

	if desiredStorageSize.IsZero() ||
		len(sts.Spec.VolumeClaimTemplates) == 0 ||
		desiredStorageSize.Cmp(*sts.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests.Storage()) == 0 {
		return nil
	}

	var allowExpansion *bool
	for i := 0; i < int(replicas); i++ {
		var pvc corev1.PersistentVolumeClaim

		name := fmt.Sprintf("%s-%s-%d", vctName, stsName, i)
		err := c.Get(ctx, client.ObjectKey{Namespace: kmc.Namespace, Name: name}, &pvc)
		if err != nil {
			if apierrors.IsNotFound(err) {
				// Do nothing if PVC does not exist yet
				return nil
			}
			return fmt.Errorf("failed to get PVC %s: %w", name, err)
		}

		if allowExpansion == nil {
			var sc storagev1.StorageClass
			err = c.Get(ctx, client.ObjectKey{Name: *pvc.Spec.StorageClassName}, &sc)
			if err != nil {
				return fmt.Errorf("failed to get StorageClass %s: %w", *pvc.Spec.StorageClassName, err)
			}
			allowExpansion = sc.AllowVolumeExpansion
		}

		if allowExpansion != nil && *allowExpansion {
			pvc.Spec.Resources.Requests[corev1.ResourceStorage] = desiredStorageSize
			err = c.Update(ctx, &pvc)
			if err != nil {
				return fmt.Errorf("failed to update PVC %s: %w", pvc.Name, err)
			}

			// Remove pod to trigger file system resize
			err = c.Delete(ctx, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-%d", stsName, i),
					Namespace: kmc.Namespace,
				},
			}, &client.DeleteOptions{})

			if err != nil {
				return fmt.Errorf("failed to delete pod '%s' for resizing: %w", fmt.Sprintf("%s-%d", stsName, i), err)
			}
		} else {
			// Do not check other PVCs if expansion is not allowed and just write an event
			//r.Recorder.Eventf(kmc, corev1.EventTypeWarning, "PVCExpansionNotAllowed", "PVC expansion is not allowed for the storage class %s", *pvc.Spec.StorageClassName)

			break
		}
	}

	return c.Delete(ctx, &sts, &client.DeleteOptions{PropagationPolicy: ptr.To(metav1.DeletePropagationOrphan)})
}
