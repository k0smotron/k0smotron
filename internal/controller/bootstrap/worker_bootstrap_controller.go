/*


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

package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	bsutil "sigs.k8s.io/cluster-api/bootstrap/util"
	"sigs.k8s.io/cluster-api/controllers/remote"
	capiutil "sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"
	bootstrapv1 "github.com/k0smotron/k0smotron/api/bootstrap/v1beta1"
	"github.com/k0smotron/k0smotron/internal/cloudinit"
	kutil "github.com/k0smotron/k0smotron/internal/util"
)

const (
	defaultK0sSuffix = "k0s.0"

	machineNameNodeLabel = "k0smotron.io/machine-name"
)

type Controller struct {
	client.Client
	Scheme     *runtime.Scheme
	ClientSet  *kubernetes.Clientset
	RESTConfig *rest.Config
	// workloadClusterClient is used during testing to inject a fake client
	workloadClusterClient client.Client
}

type Scope struct {
	Config      *bootstrapv1.K0sWorkerConfig
	ConfigOwner *bsutil.ConfigOwner
	Cluster     *clusterv1.Cluster
}

// +kubebuilder:rbac:groups=bootstrap.cluster.x-k8s.io,resources=k0sworkerconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bootstrap.cluster.x-k8s.io,resources=k0sworkerconfigs/status,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;clusters/status;machines;machines/status,verbs=get;list;watch
// +kubebuilder:rbac:groups=exp.cluster.x-k8s.io,resources=machinepools;machinepools/status,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets;events;configmaps,verbs=get;list;watch;create;update;patch;delete

func (r *Controller) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, err error) {
	log := log.FromContext(ctx).WithValues("k0sconfig", req.NamespacedName)
	log.Info("Reconciling K0sConfig")

	// Lookup the config object
	config := &bootstrapv1.K0sWorkerConfig{}
	if err := r.Get(ctx, req.NamespacedName, config); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("K0sConfig not found")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get config")
		return ctrl.Result{}, err
	}

	// Look up the owner of this config if there is one
	configOwner, err := bsutil.GetConfigOwner(ctx, r.Client, config)
	if apierrors.IsNotFound(err) {
		// Could not find the owner yet, this is not an error and will rereconcile when the owner gets set.
		log.Info("Owner not found yet, waiting until it is set")
		return ctrl.Result{}, nil
	}
	if err != nil {
		log.Error(err, "Failed to get owner")
		return ctrl.Result{}, err
	}

	if configOwner == nil {
		log.Info("Owner is nil, waiting until it is set")
		return ctrl.Result{}, nil
	}

	log = log.WithValues("kind", configOwner.GetKind(), "version", configOwner.GetResourceVersion(), "name", configOwner.GetName())

	machine := &clusterv1.Machine{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(configOwner.Object, machine); err != nil {
		return ctrl.Result{}, fmt.Errorf("error converting %s to Machine: %w", configOwner.GetKind(), err)
	}
	if config.Spec.Version == "" && machine.Spec.Version != nil {
		config.Spec.Version = fmt.Sprintf("%s+%s", *machine.Spec.Version, defaultK0sSuffix)
	}

	// Lookup the cluster the config owner is associated with
	cluster, err := capiutil.GetClusterByName(ctx, r.Client, configOwner.GetNamespace(), configOwner.ClusterName())
	if err != nil {
		if errors.Is(err, capiutil.ErrNoCluster) {
			log.Info(fmt.Sprintf("%s does not belong to a cluster yet, waiting until it's part of a cluster", configOwner.GetKind()))
			return ctrl.Result{}, nil
		}

		if apierrors.IsNotFound(err) {
			log.Info("Cluster does not exist yet, waiting until it is created")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Could not get cluster with metadata")
		return ctrl.Result{}, err
	}

	if annotations.IsPaused(cluster, config) {
		log.Info("Reconciliation is paused for this object")
		return ctrl.Result{}, nil
	}

	scope := &Scope{
		Config:      config,
		ConfigOwner: configOwner,
		Cluster:     cluster,
	}

	if scope.Config.Status.Ready {
		// Bootstrapdata field is ready to be consumed, skipping the generation of the bootstrap data secret
		log.Info("Bootstrapdata already created, reconciled succesfully")
		return ctrl.Result{}, nil
	}

	defer func() {
		// Always report the status of the bootsrap data secret generation.
		conditions.SetSummary(config,
			conditions.WithConditions(
				bootstrapv1.DataSecretAvailableCondition,
			),
		)

		if err := r.Status().Update(ctx, config); err != nil {
			log.Error(err, "Failed to patch K0sWorkerConfig status")
		}
	}()

	// Control plane needs to be ready because worker needs to use controlplane API to retrieve a join token.
	if scope.Cluster.Spec.ControlPlaneEndpoint.IsZero() || !scope.Cluster.Status.ControlPlaneReady {
		conditions.MarkFalse(config, bootstrapv1.DataSecretAvailableCondition, bootstrapv1.WaitingForControlPlaneInitializationReason, clusterv1.ConditionSeverityInfo, "")
		return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 5}, nil
	}

	log.Info("Generating bootstrap data")
	bootstrapData, err := r.generateBootstrapDataForWorker(ctx, log, scope)
	if err != nil {
		conditions.MarkFalse(config, bootstrapv1.DataSecretAvailableCondition, bootstrapv1.DataSecretGenerationFailedReason, clusterv1.ConditionSeverityError, err.Error())
		return ctrl.Result{}, err
	}

	// Create the secret containing the bootstrap data
	bootstrapSecret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.Name,
			Namespace: config.Namespace,
			Labels: map[string]string{
				clusterv1.ClusterNameLabel: scope.Cluster.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: bootstrapv1.GroupVersion.String(),
					Kind:       scope.Config.Kind,
					Name:       scope.Config.Name,
					UID:        scope.Config.UID,
					Controller: ptr.To(true),
				},
			},
		},
		Data: map[string][]byte{
			"value": bootstrapData,
		},
		Type: clusterv1.ClusterSecretType,
	}

	if err := r.Client.Patch(ctx, bootstrapSecret, client.Apply, &client.PatchOptions{FieldManager: "k0s-bootstrap"}); err != nil {
		log.Error(err, "Failed to patch bootstrap secret")
		conditions.MarkFalse(config, bootstrapv1.DataSecretAvailableCondition, bootstrapv1.DataSecretGenerationFailedReason, clusterv1.ConditionSeverityError, err.Error())
		return ctrl.Result{}, err
	}
	conditions.MarkTrue(config, bootstrapv1.DataSecretAvailableCondition)
	log.Info("Bootstrap secret created", "secret", bootstrapSecret.Name)

	// Set the status to ready
	scope.Config.Status.Ready = true
	scope.Config.Status.DataSecretName = ptr.To(bootstrapSecret.Name)
	if err := r.Status().Update(ctx, scope.Config); err != nil {
		log.Error(err, "Failed to patch config status")
		return ctrl.Result{}, err
	}

	log.Info("Reconciled succesfully")

	return ctrl.Result{}, nil
}

func (r *Controller) generateBootstrapDataForWorker(ctx context.Context, log logr.Logger, scope *Scope) ([]byte, error) {
	log.Info("Finding the token secret")
	token, err := r.getK0sToken(ctx, scope)
	if err != nil {
		log.Error(err, "Failed to get token")
		return nil, err
	}

	files := []cloudinit.File{
		{
			Path:        "/etc/k0s.token",
			Permissions: "0644",
			Content:     token,
		},
	}

	resolvedFiles, err := resolveFiles(ctx, r.Client, scope.Cluster, scope.Config.Spec.Files)
	if err != nil {
		return nil, err
	}
	files = append(files, resolvedFiles...)

	downloadCommands := createDownloadCommands(scope.Config)
	installCmd := createInstallCmd(scope)

	startCmd := `(command -v systemctl > /dev/null 2>&1 && systemctl start k0sworker) || ` + // systemd
		`(command -v rc-service > /dev/null 2>&1 && rc-service k0sworker start) || ` + // OpenRC
		`(command -v service > /dev/null 2>&1 && service k0sworker start) || ` + // SysV
		`(echo "Not a supported init system"; false)`

	commands := scope.Config.Spec.PreStartCommands
	commands = append(commands, downloadCommands...)
	commands = append(commands, installCmd, startCmd)
	commands = append(commands, scope.Config.Spec.PostStartCommands...)
	// Create the sentinel file as the last step so we know all previous _stuff_ has completed
	// https://cluster-api.sigs.k8s.io/developer/providers/contracts/bootstrap-config#sentinel-file
	commands = append(commands, "mkdir -p /run/cluster-api && touch /run/cluster-api/bootstrap-success.complete")

	ci := &cloudinit.CloudInit{
		Files:   files,
		RunCmds: commands,
	}

	return ci.AsBytes()
}

func (r *Controller) getK0sToken(ctx context.Context, scope *Scope) (string, error) {
	if r.workloadClusterClient == nil {
		var err error
		r.workloadClusterClient, err = remote.NewClusterClient(ctx, "k0smotron", r.Client, capiutil.ObjectKey(scope.Cluster))
		if err != nil {
			return "", fmt.Errorf("failed to create child cluster client: %w", err)
		}
	}

	// Create the token using the child cluster client
	tokenID := kutil.RandomString(6)
	tokenSecret := kutil.RandomString(16)
	token := fmt.Sprintf("%s.%s", tokenID, tokenSecret)
	if err := r.workloadClusterClient.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("bootstrap-token-%s", tokenID),
			Namespace: "kube-system",
		},
		Type: corev1.SecretTypeBootstrapToken,
		StringData: map[string]string{
			"token-id":     tokenID,
			"token-secret": tokenSecret,
			// TODO We need bit shorter time for the token
			"expiration":                       time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"usage-bootstrap-api-auth":         "true",
			"description":                      "Worker bootstrap token generated by k0smotron",
			"usage-bootstrap-authentication":   "true",
			"usage-bootstrap-api-worker-calls": "true",
		},
	}); err != nil {
		return "", fmt.Errorf("failed to create token secret: %w", err)
	}

	certificates := secret.NewCertificatesForWorker("")
	if err := certificates.Lookup(ctx, r.Client, capiutil.ObjectKey(scope.Cluster)); err != nil {
		return "", fmt.Errorf("failed to lookup CA certificates: %w", err)
	}
	ca := certificates.GetByPurpose(secret.ClusterCA)
	if ca.KeyPair == nil {
		return "", errors.New("failed to get CA certificate key pair")
	}

	joinToken, err := kutil.CreateK0sJoinToken(ca.KeyPair.Cert, token, fmt.Sprintf("https://%s:%d", scope.Cluster.Spec.ControlPlaneEndpoint.Host, scope.Cluster.Spec.ControlPlaneEndpoint.Port), "kubelet-bootstrap")
	if err != nil {
		return "", fmt.Errorf("failed to create join token: %w", err)
	}
	return joinToken, nil
}

func createInstallCmd(scope *Scope) string {
	installCmd := []string{
		"k0s install worker --token-file /etc/k0s.token",
	}
	installCmd = append(installCmd, mergeExtraArgs(scope.Config.Spec.Args, scope.ConfigOwner, true, scope.Config.Spec.UseSystemHostname)...)
	return strings.Join(installCmd, " ")
}

func createDownloadCommands(config *bootstrapv1.K0sWorkerConfig) []string {
	if config.Spec.PreInstalledK0s {
		return nil
	}

	if config.Spec.DownloadURL != "" {
		return []string{
			fmt.Sprintf("curl -sSfL %s -o /usr/local/bin/k0s", config.Spec.DownloadURL),
			"chmod +x /usr/local/bin/k0s",
		}
	}

	// Figure out version to download if download URL is not set
	if config.Spec.Version != "" {
		return []string{fmt.Sprintf("curl -sSfL https://get.k0s.sh | K0S_VERSION=%s sh", config.Spec.Version)}
	}

	return []string{"curl -sSfL https://get.k0s.sh | sh"}
}

func (r *Controller) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&bootstrapv1.K0sWorkerConfig{}).
		Complete(r)
}
