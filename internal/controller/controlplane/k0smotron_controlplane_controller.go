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
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/imdario/mergo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/cluster-api/controllers/remote"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	bootstrapv1 "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1beta1"
	capiutil "sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cpv1beta1 "github.com/k0sproject/k0smotron/api/controlplane/v1beta1"
	kapi "github.com/k0sproject/k0smotron/api/k0smotron.io/v1beta1"
	"github.com/k0sproject/k0smotron/internal/exec"
	"github.com/k0sproject/version"
)

const (
	// AnnotationKeyManagedBy is the annotation key that indicates which controller manages the infrastructure object
	AnnotationKeyManagedBy = "cluster.x-k8s.io/managed-by"

	// AnnotationValueManagedByK0smotron is the value for the managed-by annotation
	AnnotationValueManagedByK0smotron = "k0smotron"
)

type K0smotronController struct {
	client.Client
	SecretCachingClient client.Client
	Scheme              *runtime.Scheme
	ClientSet           *kubernetes.Clientset
	RESTConfig          *rest.Config
}

type Scope struct {
	Config *cpv1beta1.K0smotronControlPlane
	// ConfigOwner *bsutil.ConfigOwner
	Cluster *clusterv1.Cluster
}

// +kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=k0smotroncontrolplanes/status,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=k0smotroncontrolplanes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=*,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;clusters/status,verbs=get;list;watch;update;patch

func (c *K0smotronController) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, err error) {
	log := log.FromContext(ctx).WithValues("controlplane", req.NamespacedName)
	log.Info("Reconciling K0smotronControlPlane")

	kcp := &cpv1beta1.K0smotronControlPlane{}
	if err := c.Get(ctx, req.NamespacedName, kcp); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get K0smotronControlPlane")
		return ctrl.Result{}, err
	}

	if !kcp.ObjectMeta.DeletionTimestamp.IsZero() {
		log.Info("K0smotronControlPlane is being deleted, no action needed")
		return ctrl.Result{}, nil
	}

	cluster, err := capiutil.GetOwnerCluster(ctx, c.Client, kcp.ObjectMeta)
	if err != nil {
		log.Error(err, "Failed to get owner cluster")
		return ctrl.Result{}, err
	}
	if cluster == nil {
		log.Info("Waiting for Cluster Controller to set OwnerRef on K0smotronControlPlane")
		return ctrl.Result{}, nil
	}

	// Configure patch helpers for resources updated during the reconcile loop.
	kcpPatchHelper, err := patch.NewHelper(kcp, c.Client)
	if err != nil {
		log.Error(err, "Failed to configure K0smotronControlPlane patch helper")
		return ctrl.Result{Requeue: true}, nil
	}
	clusterPatchHelper, err := patch.NewHelper(cluster, c.Client)
	if err != nil {
		log.Error(err, "Failed to configure Cluster patch helper")
		return ctrl.Result{Requeue: true}, nil
	}

	log = log.WithValues("cluster", cluster.Name)

	if annotations.IsPaused(cluster, kcp) {
		log.Info("Reconciliation is paused for this object")
		return ctrl.Result{}, nil
	}

	defer func() {
		derr := c.computeStatus(ctx, types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}, kcp)
		if derr != nil {
			if errors.Is(derr, ErrNotReady) {
				res = ctrl.Result{RequeueAfter: 10 * time.Second, Requeue: true}
			} else {
				log.Error(derr, "Failed to update K0smotronContorlPlane status")
				err = derr
				return
			}
		}

		derr = kcpPatchHelper.Patch(ctx, kcp)
		if derr != nil {
			log.Error(derr, "Failed to patch K0smotronContorlPlane")
			err = kerrors.NewAggregate([]error{err, derr})
		}

		derr = clusterPatchHelper.Patch(ctx, cluster)
		if derr != nil {
			log.Error(err, "Failed to update Cluster endpoint")
			err = kerrors.NewAggregate([]error{err, derr})
		}

		if err != nil {
			// We shouldn't proceed with Infrastructure patching
			// if we couldn't update the Cluster, K0smotronContorlPlane object(s)
			return
		}

		derr = c.patchInfrastructureStatus(ctx, cluster, kcp.Status.Ready)
		if derr != nil {
			log.Error(derr, "Failed to patch Infrastructure object status")
			err = kerrors.NewAggregate([]error{err, derr})
		}
	}()

	res, ready, err := c.reconcile(ctx, cluster, kcp)
	if err != nil {
		log.Error(err, "Reconciliation failed")
		return res, err
	}
	// Requeue is needed when the k0smotron Cluster has just been created. k0smotron Cluster controller needs to take action
	// before the ControlPlane reconciliation can continue.
	if !res.IsZero() {
		return res, err
	}

	if !ready {
		err = c.waitExternalAddress(ctx, cluster)
		if err != nil {
			return res, err
		}
	}

	if ready {
		remoteClient, err := remote.NewClusterClient(ctx, "k0smotron", c.Client, capiutil.ObjectKey(cluster))
		if err != nil {
			return res, fmt.Errorf("failed to create remote client: %w", err)
		}

		ns := &corev1.Namespace{}
		err = remoteClient.Get(ctx, types.NamespacedName{Name: "kube-system", Namespace: ""}, ns)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 30}, nil
			}
			return res, fmt.Errorf("failed to get namespace: %w", err)
		}

		annotations.AddAnnotations(cluster, map[string]string{
			cpv1beta1.K0sClusterIDAnnotation: fmt.Sprintf("kube-system:%s", ns.GetUID()),
		})
	}

	kcp.Status.Ready = ready
	kcp.Status.ExternalManagedControlPlane = true
	kcp.Status.Initialized = true

	return res, err
}

// watchExternalAddress watches the external address of the control plane and updates the status accordingly
func (c *K0smotronController) waitExternalAddress(ctx context.Context, cluster *clusterv1.Cluster) error {
	log := log.FromContext(ctx).WithValues("cluster", cluster.Name)
	log.Info("Starting to wait for external address")
	startTime := time.Now()
	err := wait.PollUntilContextTimeout(ctx, 1*time.Second, 3*time.Minute, true, func(ctx context.Context) (done bool, err error) {
		k0smoCluster := &kapi.Cluster{}
		if err := c.Client.Get(ctx, capiutil.ObjectKey(cluster), k0smoCluster); err != nil {
			log.Error(err, "Failed to get k0smotron Cluster")
			return false, err
		}
		if k0smoCluster.Spec.ExternalAddress == "" {
			elapsed := time.Since(startTime).Round(time.Second)
			log.Info("External address not yet available", "elapsed", elapsed)
			return false, nil
		}
		log.Info("External address found", "address", k0smoCluster.Spec.ExternalAddress, "elapsed", time.Since(startTime).Round(time.Second))
		// Get the external address of the control plane
		host := k0smoCluster.Spec.ExternalAddress
		port := k0smoCluster.Spec.Service.APIPort
		// Update the Clusters endpoint if needed
		if cluster.Spec.InfrastructureRef != nil && (cluster.Spec.ControlPlaneEndpoint.Host != host || cluster.Spec.ControlPlaneEndpoint.Port != int32(port)) {

			// Get the infrastructure cluster object
			infraCluster := &unstructured.Unstructured{}
			infraCluster.SetGroupVersionKind(cluster.Spec.InfrastructureRef.GroupVersionKind())
			if err := c.Client.Get(ctx, types.NamespacedName{Namespace: cluster.Namespace, Name: cluster.Spec.InfrastructureRef.Name}, infraCluster); err != nil {
				log.Error(err, "Failed to get infrastructure cluster")
				return false, err
			}
			log.Info("Found infrastructure cluster")
			newEndpoint := map[string]interface{}{
				"host": host,
				"port": int64(port),
			}
			err = unstructured.SetNestedMap(infraCluster.Object, newEndpoint, "spec", "controlPlaneEndpoint")
			if err != nil {
				log.Error(err, "Failed to set controlPlaneEndpoint in infrastructure cluster")
				return false, err
			}
			if err := c.Client.Update(ctx, infraCluster); err != nil {
				log.Error(err, "Failed to update infrastructure cluster")
				return false, err
			}
			log.Info("Updated infrastructure cluster", "host", host, "port", port)
			cluster.Spec.ControlPlaneEndpoint.Host = host
			cluster.Spec.ControlPlaneEndpoint.Port = int32(port)

			return true, nil
		}
		return true, nil
	})
	if err != nil {
		return err
	}

	return nil
}

func (c *K0smotronController) reconcile(ctx context.Context, cluster *clusterv1.Cluster, kcp *cpv1beta1.K0smotronControlPlane) (ctrl.Result, bool, error) {
	logger := log.FromContext(ctx)
	if kcp.Spec.CertificateRefs == nil {
		kcp.Spec.CertificateRefs = []kapi.CertificateRef{
			{
				Type: string(secret.ClusterCA),
				Name: secret.Name(cluster.Name, secret.ClusterCA),
			},
			{
				Type: string(secret.FrontProxyCA),
				Name: secret.Name(cluster.Name, secret.FrontProxyCA),
			},
			{
				Type: string(secret.ServiceAccount),
				Name: secret.Name(cluster.Name, secret.ServiceAccount),
			},
			{
				Type: string(secret.EtcdCA),
				Name: secret.Name(cluster.Name, secret.EtcdCA),
			},
		}

		if err := c.ensureCertificates(ctx, cluster, kcp); err != nil {
			return ctrl.Result{}, false, fmt.Errorf("failed to ensure certificates for K0smotronControlPlane %s/%s", kcp.Namespace, kcp.Name)
		}
	}

	var err error
	kcp.Spec.K0sConfig, err = enrichK0sConfigWithClusterData(cluster, kcp.Spec.K0sConfig)
	if err != nil {
		return ctrl.Result{}, false, fmt.Errorf("failed to enrich k0s config with cluster data for K0smotronControlPlane %s/%s", kcp.Namespace, kcp.Name)
	}

	desiredK0smotronCluster := kapi.Cluster{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kapi.GroupVersion.String(),
			Kind:       "Cluster",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Name,
			Namespace: cluster.Namespace,
			Labels: map[string]string{
				clusterv1.ClusterNameLabel: cluster.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: cpv1beta1.GroupVersion.String(),
					Kind:       "K0smotronControlPlane",
					Name:       kcp.Name,
					UID:        kcp.UID,
				},
			},
		},
		Spec: kcp.Spec,
	}

	var foundCluster kapi.Cluster
	err = c.Client.Get(ctx, types.NamespacedName{Name: desiredK0smotronCluster.Name, Namespace: desiredK0smotronCluster.Namespace}, &foundCluster)
	if err != nil && apierrors.IsNotFound(err) {
		if err := c.Client.Create(ctx, &desiredK0smotronCluster); err != nil {
			return ctrl.Result{}, false, err
		}

		logger.Info("Requeuing because k0smotron Cluster has just been created")
		return ctrl.Result{RequeueAfter: 5 * time.Second, Requeue: true}, false, nil
	}

	if kcp.Spec.ExternalAddress == "" {
		kcp.Spec.ExternalAddress = foundCluster.Spec.ExternalAddress
	}

	isClusterSpecSynced, err := isClusterSpecSynced(foundCluster.Spec, kcp.Spec)
	if err != nil {
		return ctrl.Result{}, false, fmt.Errorf("error comparing cluster spec between k0smotron.Cluster and k0smotronControlPlane: %w", err)
	}
	if !isClusterSpecSynced {
		patchHelper, err := patch.NewHelper(&foundCluster, c.Client)
		if err != nil {
			return ctrl.Result{}, false, err
		}

		// Modidy current Cluster specification with the desired one.
		foundCluster.Spec = kcp.Spec

		return ctrl.Result{}, false, patchHelper.Patch(ctx, &foundCluster)
	}

	return ctrl.Result{}, foundCluster.Status.Ready, nil
}

// isClusterSpecSynced compares ClusterSpecs while accounting for expected changes. The K0smotron Cluster controller may add additional data to the spec,
// so we need to account for that possibility where appropriate.
func isClusterSpecSynced(kmcSpec, kcpSpec kapi.ClusterSpec) (bool, error) {
	overridenKmcSpec := kmcSpec.DeepCopy()
	// The definition in K0smotronControlPlane takes precedence, as the K0smotron Cluster is created based on it. For this reason, mergo.WithOverride is
	// used to ensure those values override existing ones.
	err := mergo.Merge(overridenKmcSpec, kcpSpec, mergo.WithOverride)
	if err != nil {
		return false, err
	}
	// K0smotron Cluster controller can add additional certificate references, such as 'apiserver-etcd-client'. Therefore, we explicitly reset the
	// certificate references based on the original Cluster definition.
	overridenKmcSpec.CertificateRefs = kmcSpec.CertificateRefs

	// K0smotron Cluster controller will add additional SANs, such as those related to the externaladdress or to the service that exposes the controlplanes.
	// Therefore, we explicitly reset the SANs references based on the original Cluster definition.
	if kmcSpec.K0sConfig != nil {
		kmcSans, found, err := unstructured.NestedSlice(kmcSpec.K0sConfig.Object, "spec", "api", "sans")
		if found && err == nil {
			err = unstructured.SetNestedField(overridenKmcSpec.K0sConfig.Object, kmcSans, "spec", "api", "sans")
			if err != nil {
				return false, fmt.Errorf("failed to set api.sans in K0smotronCluster spec: %w", err)
			}
		}
	}

	return reflect.DeepEqual(kmcSpec, *overridenKmcSpec), nil
}

func (c *K0smotronController) ensureCertificates(ctx context.Context, cluster *clusterv1.Cluster, kcp *cpv1beta1.K0smotronControlPlane) error {
	certificates := secret.NewCertificatesForInitialControlPlane(&bootstrapv1.ClusterConfiguration{})
	return certificates.LookupOrGenerateCached(ctx, c.SecretCachingClient, c.Client, capiutil.ObjectKey(cluster), *metav1.NewControllerRef(kcp, cpv1beta1.GroupVersion.WithKind("K0smotronControlPlane")))
}

// FormatStatusVersion formats the status version to match the spec version format.
// If spec.version doesn't contain "-k0s." suffix, it removes the suffix from status.version as well.
func FormatStatusVersion(specVersion, statusVersion string) string {
	specHasK0sSuffix := strings.Contains(specVersion, "-k0s.")

	// Adjust status.version to match the format of spec.version
	if !specHasK0sSuffix && strings.Contains(statusVersion, "-k0s.") {
		// If spec.version doesn't have the -k0s. suffix, remove it from status.version as well
		parts := strings.Split(statusVersion, "-k0s.")
		if len(parts) > 0 {
			return parts[0]
		}
	}

	return statusVersion
}

func (c *K0smotronController) computeStatus(ctx context.Context, cluster types.NamespacedName, kcp *cpv1beta1.K0smotronControlPlane) error {
	var kmc kapi.Cluster
	err := c.Client.Get(ctx, cluster, &kmc)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// The cluster is not yet created.
			return nil
		}

		return err
	}

	contolPlanePods := &corev1.PodList{}
	opts := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{
			"cluster.x-k8s.io/cluster-name":  cluster.Name,
			"cluster.x-k8s.io/control-plane": "true",
		}),
	}
	err = c.List(ctx, contolPlanePods, opts)
	if err != nil {
		return err
	}

	kcp.Status.Replicas = int32(len(contolPlanePods.Items))

	var updatedReplicas, readyReplicas, unavailableReplicas int

	desiredVersionStr := kcp.Spec.Version
	if !strings.Contains(desiredVersionStr, "-") {
		desiredVersionStr = fmt.Sprintf("%s-%s", desiredVersionStr, kapi.DefaultK0SSuffix)
	}
	desiredVersion, err := version.NewVersion(desiredVersionStr)
	if err != nil {
		return err
	}
	minimumVersion := *desiredVersion

	for _, pod := range contolPlanePods.Items {
		isPodReady := false
		for _, c := range pod.Status.Conditions {
			// readiness probe in pod will propagate pod status Ready = True if k0s service is running successfully.
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				isPodReady = true
				break
			}
		}
		if isPodReady {
			readyReplicas++
		} else {
			unavailableReplicas++
			// if pod is unavailable subsequent checks do not apply
			continue
		}

		currentVersion, err := c.getComparableK0sVersionRunningInPod(ctx, &pod)
		if err != nil {
			return err
		}

		if desiredVersion.Equal(currentVersion) {
			updatedReplicas++
		}

		if currentVersion.LessThan(&minimumVersion) {
			minimumVersion = *currentVersion
		}
	}

	kcp.Status.UpdatedReplicas = int32(updatedReplicas)
	kcp.Status.ReadyReplicas = int32(readyReplicas)
	kcp.Status.UnavailableReplicas = int32(unavailableReplicas)

	if kcp.Status.ReadyReplicas > 0 {
		statusVersion := minimumVersion.String()
		// Store the full k0s version
		kcp.Status.K0sVersion = statusVersion
		// Store the formatted version for Cluster API compatibility
		kcp.Status.Version = FormatStatusVersion(kcp.Spec.Version, statusVersion)
	}

	// if no replicas are yet available or the desired version is not in the current state of the
	// control plane, the reconciliation is requeued waiting for the desired replicas to become available.
	if kcp.Status.UnavailableReplicas > 0 || desiredVersion.String() != kcp.Status.Version {
		return ErrNotReady
	}

	return nil
}

func (c *K0smotronController) getComparableK0sVersionRunningInPod(ctx context.Context, pod *corev1.Pod) (*version.Version, error) {
	currentVersionOutput, err := exec.PodExecCmdOutput(ctx, c.ClientSet, c.RESTConfig, pod.GetName(), pod.GetNamespace(), "k0s version")
	if err != nil {
		return nil, err
	}
	currentVersionStr, _ := strings.CutSuffix(currentVersionOutput, "\n")
	// In order to compare the version reported by the 'k0s version' command executed in the pod running
	// the controlplane with the version declared in K0smotronControlPlane.spec this transformation is
	// necessary to match their format.
	currentVersionStr = strings.Replace(currentVersionStr, "+", "-", 1)
	return version.NewVersion(currentVersionStr)
}

// patchInfrastructureStatus updates the ready status of the infrastructure object referenced by the cluster
func (c *K0smotronController) patchInfrastructureStatus(ctx context.Context, cluster *clusterv1.Cluster, ready bool) error {
	log := log.FromContext(ctx).WithValues("cluster", cluster.Name)

	// Skip if no infrastructure reference exists
	if cluster.Spec.InfrastructureRef == nil {
		return nil
	}

	// Get the infrastructure object
	infraObj := &unstructured.Unstructured{}
	infraObj.SetGroupVersionKind(cluster.Spec.InfrastructureRef.GroupVersionKind())
	infraObjKey := types.NamespacedName{
		Namespace: cluster.Spec.InfrastructureRef.Namespace,
		Name:      cluster.Spec.InfrastructureRef.Name,
	}

	if err := c.Client.Get(ctx, infraObjKey, infraObj); err != nil {
		return fmt.Errorf("failed to get Infrastructure object: %w", err)
	}

	// Check if the object has the required annotation
	annotations := infraObj.GetAnnotations()
	if annotations == nil || annotations[AnnotationKeyManagedBy] != AnnotationValueManagedByK0smotron {
		return nil
	}

	// Check current status.ready value
	currentReady, found, err := unstructured.NestedBool(infraObj.Object, "status", "ready")
	if err != nil {
		return fmt.Errorf("failed to get ready status: %w", err)
	}

	// Only patch if status is not set or different from desired value
	if !found || currentReady != ready {
		log.Info("Patching Infrastructure object status", "ready", ready)

		// Apply the patch
		err = c.Client.Status().Patch(
			ctx,
			infraObj,
			client.RawPatch(
				types.MergePatchType,
				fmt.Appendf(nil, `{"status": {"ready": %t}}`, ready),
			),
		)
		if err != nil {
			return fmt.Errorf("failed to patch Infrastructure object: %w", err)
		}

		log.Info("Successfully patched Infrastructure object status")
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (c *K0smotronController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cpv1beta1.K0smotronControlPlane{}).
		Owns(&kapi.Cluster{}, builder.MatchEveryOwner).
		Complete(c)
}
