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
	"context"
	"fmt"
	"time"

	kutil "github.com/k0sproject/k0smotron/internal/controller/util"
	apps "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	bootstrapv1 "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	km "github.com/k0sproject/k0smotron/api/k0smotron.io/v1beta1"
)

var patchOpts []client.PatchOption = []client.PatchOption{
	client.FieldOwner("k0smotron-operator"),
	client.ForceOwnership,
}

// ClusterReconciler reconciles a Cluster object
type ClusterReconciler struct {
	client.Client
	SecretCachingClient client.Client
	Scheme              *runtime.Scheme
	ClientSet           *kubernetes.Clientset
	RESTConfig          *rest.Config
	Recorder            record.EventRecorder
}

const (
	clusterUIDLabel  = "k0smotron.io/cluster-uid"
	clusterFinalizer = "k0smotron.io/finalizer"
)

type kmcScope struct {
	// isClusterHCP indicates if the controlplane replicas run in the mothership cluster or in a different cluster
	// (determined by the ClusterProfileRef).
	// If 'true', the controlplane replicas run in the mothership cluster
	inClusterHCP bool
	// client is the client used to interact with the cluster where the controlplane replicas run.
	client client.Client
	// clientSet is the clientset used to interact with the cluster where the controlplane replicas run.
	clienSet *kubernetes.Clientset
	// restConfig is the rest.Config used to interact with the cluster where the controlplane replicas run.
	restConfig *rest.Config
	// secretCachingClient is the client used to cache secrets for certificate generation.
	secretCachingClient client.Client
}

//+kubebuilder:rbac:groups=k0smotron.io,resources=clusters,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=k0smotron.io,resources=clusters/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=k0smotron.io,resources=clusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=persistentvolumes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;delete;watch
// +kubebuilder:rbac:groups=core,resources=pods/exec,verbs=create
// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=list
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=create;update;patch;delete
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Cluster object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	kmc := &km.Cluster{}
	if err := r.Get(ctx, req.NamespacedName, kmc); err != nil {
		logger.Error(err, "unable to fetch Cluster")
		// we'll ignore not-found errors, since they can't be fixed by an immediate
		// requeue (we'll need to wait for a new notification), and we can get them
		// on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	logger.Info("Reconciling")

	if finalizerAdded, err := kutil.EnsureFinalizer(ctx, r.Client, kmc, clusterFinalizer); err != nil || finalizerAdded {
		return ctrl.Result{}, err
	}

	kmcScope, err := r.getKmcScope(ctx, kmc)
	if err != nil {
		logger.Error(err, "Error getting kmc scope")
		return ctrl.Result{}, err
	}

	patchHelper, err := patch.NewHelper(kmc, r.Client)
	if err != nil {
		logger.Error(err, "Failed to configure the patch helper")
		return ctrl.Result{Requeue: true}, nil
	}

	defer func() {
		err = patchHelper.Patch(ctx, kmc)
		if err != nil {
			logger.Error(err, "Unable to update k0smotron Cluster")
		}
	}()

	if !kmc.ObjectMeta.DeletionTimestamp.IsZero() {
		logger.Info("Cluster is being deleted")
		// If controlplanes run in a different cluster, we need to delete the resources associated with
		// the k0smotron.Cluster. This is because the owner references cannot be used in this case.
		if !kmcScope.inClusterHCP {
			var errors []error
			for _, descendant := range kmcScope.getDescendants(ctx, kmc) {
				if !descendant.GetDeletionTimestamp().IsZero() {
					continue
				}

				if err := kmcScope.client.Delete(ctx, descendant); err != nil {
					errors = append(errors, fmt.Errorf("error deleting descendant resource %s: %w", descendant.GetName(), err))
				}
			}
			if len(errors) > 0 {
				return ctrl.Result{}, fmt.Errorf("error deleting descendant resources: %v", errors)
			}
		}
		if controllerutil.ContainsFinalizer(kmc, clusterFinalizer) {
			// Even if there is an error the finalizer must be removed for a complete removal of the
			// cluster resource. In the worst case, the associated JointTokenRequest is not deleted.
			defer func() {
				controllerutil.RemoveFinalizer(kmc, clusterFinalizer)
				if err := r.Update(ctx, kmc); err != nil {
					logger.Error(err, "Error removing cluster finalizer")
				}
			}()

			// Once the cluster enters Terminating state, we ensure that the resources dependent on it
			// are also removed.
			// Note: owner references cannot be used in this case because JoinTokenRequest can be in a
			// different namespace.
			jtrl := &km.JoinTokenRequestList{}
			err := r.List(ctx, jtrl,
				client.MatchingLabels{
					clusterUIDLabel: string(kmc.GetUID()),
				})
			if err != nil {
				logger.Error(err, "Error retrieving JoinTokenRequests resources related to cluster")
				return ctrl.Result{}, nil
			}
			for i := range jtrl.Items {
				err := r.Delete(ctx, &jtrl.Items[i])
				if err != nil {
					logger.Error(err, "Error removing JoinTokenRequests")
					return ctrl.Result{}, nil
				}
			}
		}

		return ctrl.Result{}, nil
	}

	patchHelper, err = patch.NewHelper(kmc, r.Client)
	if err != nil {
		logger.Error(err, "Failed to configure the patch helper")
		return ctrl.Result{Requeue: true}, nil
	}

	defer func() {
		err = patchHelper.Patch(ctx, kmc)
		if err != nil {
			logger.Error(err, "Unable to update k0smotron Cluster")
		}
	}()

	logger.Info("Reconciling services")
	if err := kmcScope.reconcileServices(ctx, kmc); err != nil {
		kmc.Status.ReconciliationStatus = "Failed reconciling services"
		return ctrl.Result{Requeue: true, RequeueAfter: time.Minute}, err
	}

	if err := kmcScope.reconcileK0sConfig(ctx, kmc); err != nil {
		kmc.Status.ReconciliationStatus = "Failed reconciling configmap"
		return ctrl.Result{Requeue: true, RequeueAfter: time.Minute}, err
	}

	if err := kmcScope.reconcileEntrypointCM(ctx, kmc); err != nil {
		kmc.Status.ReconciliationStatus = "Failed reconciling entrypoint configmap"
		return ctrl.Result{Requeue: true, RequeueAfter: time.Minute}, err
	}

	if kmc.Spec.Monitoring.Enabled {
		if err := kmcScope.reconcileMonitoringCM(ctx, kmc); err != nil {
			kmc.Status.ReconciliationStatus = "Failed reconciling prometheus configmap"
			return ctrl.Result{Requeue: true, RequeueAfter: time.Minute}, err
		}
	}

	if kmc.Spec.CertificateRefs == nil {
		if err := kmcScope.ensureCertificates(ctx, kmc); err != nil {
			return ctrl.Result{Requeue: true, RequeueAfter: time.Minute}, err
		}
		kmc.Spec.CertificateRefs = []km.CertificateRef{
			{
				Type: string(secret.ClusterCA),
				Name: secret.Name(kmc.Name, secret.ClusterCA),
			},
			{
				Type: string(secret.FrontProxyCA),
				Name: secret.Name(kmc.Name, secret.FrontProxyCA),
			},
			{
				Type: string(secret.ServiceAccount),
				Name: secret.Name(kmc.Name, secret.ServiceAccount),
			},
			{
				Type: string(secret.EtcdCA),
				Name: secret.Name(kmc.Name, secret.EtcdCA),
			},
		}
	}
	if kmc.Spec.KineDataSourceURL == "" {
		isAPIServerEtcdClientCertRef := false
		for _, cr := range kmc.Spec.CertificateRefs {
			if cr.Type == string(secret.APIServerEtcdClient) {
				isAPIServerEtcdClientCertRef = true
				break
			}
		}
		logger.Info("Reconciling etcd certs")
		err := kmcScope.ensureEtcdCertificates(ctx, kmc)
		if err != nil {
			return ctrl.Result{Requeue: true, RequeueAfter: time.Minute}, fmt.Errorf("error generating etcd certificates: %w", err)
		}

		if !isAPIServerEtcdClientCertRef {
			kmc.Spec.CertificateRefs = append(kmc.Spec.CertificateRefs, km.CertificateRef{
				Type: string(secret.APIServerEtcdClient),
				Name: secret.Name(kmc.Name, secret.APIServerEtcdClient),
			})
		}
	}

	if err := kmcScope.reconcilePVC(ctx, kmc); err != nil {
		kmc.Status.ReconciliationStatus = "Failed reconciling PVCs"
		return ctrl.Result{Requeue: true, RequeueAfter: time.Minute}, err
	}

	logger.Info("Reconciling etcd")
	if err := kmcScope.reconcileEtcd(ctx, kmc); err != nil {
		kmc.Status.ReconciliationStatus = fmt.Sprintf("Failed reconciling etcd, %+v", err)
		return ctrl.Result{Requeue: true, RequeueAfter: time.Minute}, err
	}

	logger.Info("Reconciling statefulset")
	if err := kmcScope.reconcileStatefulSet(ctx, kmc); err != nil {
		kmc.Status.ReconciliationStatus = fmt.Sprintf("Failed reconciling statefulset, %+v", err)
		return ctrl.Result{Requeue: true, RequeueAfter: time.Minute}, err
	}

	if err := kmcScope.reconcileKubeConfigSecret(ctx, kmc); err != nil {
		kmc.Status.ReconciliationStatus = "Failed reconciling secret"
		return ctrl.Result{Requeue: true, RequeueAfter: time.Minute}, err
	}

	kmc.Status.ReconciliationStatus = "Reconciliation successful"
	return ctrl.Result{}, nil
}

func (scope *kmcScope) ensureCertificates(ctx context.Context, kmc *km.Cluster) error {
	certificates := secret.NewCertificatesForInitialControlPlane(&bootstrapv1.ClusterConfiguration{})
	err := certificates.LookupOrGenerateCached(ctx, scope.secretCachingClient, scope.client, util.ObjectKey(kmc), *metav1.NewControllerRef(kmc, km.GroupVersion.WithKind("Cluster")))
	if err != nil {
		return fmt.Errorf("error generating cluster certificates: %w", err)
	}

	return nil
}

func (r *ClusterReconciler) getKmcScope(ctx context.Context, kmc *km.Cluster) (*kmcScope, error) {
	logger := log.FromContext(ctx)

	// By default, the controlplane replicas run in the mothership cluster so we set the
	// clients using the controller's clients.
	kmcScope := &kmcScope{
		inClusterHCP:        true,
		client:              r.Client,
		clienSet:            r.ClientSet,
		restConfig:          r.RESTConfig,
		secretCachingClient: r.SecretCachingClient,
	}

	if kmc.Spec.HostingClusterKubeconfigRef != nil {
		kmcScope.inClusterHCP = false
		var err error
		kmcScope.client, kmcScope.clienSet, kmcScope.restConfig, err = kutil.GetKmcClientFromClusterPrLubeconfigSecret(ctx, r.Client, kmc.Spec.HostingClusterKubeconfigRef)
		if err != nil {
			logger.Error(err, "Error getting client from cluster profile")
			return nil, err
		}
	}

	return kmcScope, nil
}

// getDescendants returns the list of resources that are associated with the k0smotron.Cluster
// and that must be deleted when the k0smotron.Cluster is deleted.
func (scope *kmcScope) getDescendants(ctx context.Context, kmc *km.Cluster) []client.Object {
	logger := log.FromContext(ctx)

	descendants := make([]client.Object, 0)

	lOpt := client.MatchingLabels(kutil.DefaultK0smotronClusterLabels(kmc))

	cmList := &v1.ConfigMapList{}
	err := scope.client.List(ctx, cmList, lOpt)
	if err != nil {
		logger.Error(err, "Error listing configmaps")
	}
	for _, cm := range cmList.Items {
		descendants = append(descendants, &cm)
	}

	svcList := &v1.ServiceList{}
	err = scope.client.List(ctx, svcList, lOpt)
	if err != nil {
		logger.Error(err, "Error listing services")
	}
	for _, svc := range svcList.Items {
		descendants = append(descendants, &svc)
	}

	stsList := &apps.StatefulSetList{}
	err = scope.client.List(ctx, stsList, lOpt)
	if err != nil {
		logger.Error(err, "Error listing statefulsets")
	}
	for _, sts := range stsList.Items {
		descendants = append(descendants, &sts)
	}

	secretList := &v1.SecretList{}
	err = scope.client.List(ctx, secretList, lOpt)
	if err != nil {
		logger.Error(err, "Error listing secrets")
	}
	for _, s := range secretList.Items {
		descendants = append(descendants, &s)
	}

	cjList := &batchv1.CronJobList{}
	err = scope.client.List(ctx, cjList, lOpt)
	if err != nil {
		logger.Error(err, "Error listing cronjobs")
	}
	for _, cj := range cjList.Items {
		descendants = append(descendants, &cj)
	}

	return descendants
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&km.Cluster{}).
		Owns(&apps.StatefulSet{}).
		Complete(r)
}
