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
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/kubernetes/cmd/kubeadm/app/constants"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	kubeadmbootstrapv1 "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1beta1"
	bsutil "sigs.k8s.io/cluster-api/bootstrap/util"
	"sigs.k8s.io/cluster-api/controllers/remote"
	capiutil "sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/collections"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"
	bootstrapv1 "github.com/k0smotron/k0smotron/api/bootstrap/v1beta1"
	"github.com/k0smotron/k0smotron/internal/cloudinit"
	kutil "github.com/k0smotron/k0smotron/internal/util"
	"github.com/k0sproject/version"
)

type ControlPlaneController struct {
	client.Client
	SecretCachingClient client.Client
	Scheme              *runtime.Scheme
	ClientSet           *kubernetes.Clientset
	RESTConfig          *rest.Config
}

const joinTokenFilePath = "/etc/k0s.token"

var minVersionForETCDName = version.MustParse("v1.31.1+k0s.0")
var errInitialControllerMachineNotInitialize = errors.New("initial controller machine has not completed its initialization")

type ControllerScope struct {
	Config        *bootstrapv1.K0sControllerConfig
	ConfigOwner   *bsutil.ConfigOwner
	Cluster       *clusterv1.Cluster
	WorkerEnabled bool
	machines      collections.Machines
}

// +kubebuilder:rbac:groups=bootstrap.cluster.x-k8s.io,resources=k0scontrollerconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bootstrap.cluster.x-k8s.io,resources=k0scontrollerconfigs/status,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;clusters/status;machines;machines/status,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=exp.cluster.x-k8s.io,resources=machinepools;machinepools/status,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machinepools;machinepools/status,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets;events;configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete

func (c *ControlPlaneController) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, err error) {
	log := log.FromContext(ctx).WithValues("K0sControllerConfig", req.NamespacedName)
	log.Info("Reconciling K0sControllerConfig")

	// Lookup the config object
	config := &bootstrapv1.K0sControllerConfig{}
	if err := c.Get(ctx, req.NamespacedName, config); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("K0sControllerConfig not found")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get config")
		return ctrl.Result{}, err
	}

	// Look up the owner of this config if there is one
	configOwner, err := bsutil.GetConfigOwner(ctx, c.Client, config)
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
	cluster, err := capiutil.GetClusterByName(ctx, c.Client, configOwner.GetNamespace(), configOwner.ClusterName())
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

	scope := &ControllerScope{
		Config:        config,
		ConfigOwner:   configOwner,
		Cluster:       cluster,
		WorkerEnabled: false,
	}

	for _, arg := range config.Spec.Args {
		if arg == "--enable-worker" || arg == "--enable-worker=true" || arg == "--single" {
			scope.WorkerEnabled = true
			break
		}
	}

	if scope.Config.Status.Ready {
		// Bootstrapdata field is ready to be consumed, skipping the generation of the bootstrap data secret
		log.Info("Bootstrapdata already created, reconciled succesfully")
		return ctrl.Result{}, nil
	}

	patchHelper, err := patch.NewHelper(config, c.Client)
	if err != nil {
		return ctrl.Result{}, err
	}

	oldConfig := config.DeepCopy()
	defer func() {
		// Always report the status of the bootsrap data secret generation.
		conditions.SetSummary(config,
			conditions.WithConditions(
				bootstrapv1.DataSecretAvailableCondition,
			),
		)
		config.Spec = oldConfig.Spec
		err := patchHelper.Patch(ctx, config)
		if err != nil {
			log.Error(err, "Failed to patch K0sControllerConfig status")
		}
	}()

	if scope.Cluster.Spec.ControlPlaneEndpoint.IsZero() {
		log.Info("control plane endpoint is not set")
		conditions.MarkFalse(config, bootstrapv1.DataSecretAvailableCondition, bootstrapv1.WaitingForControlPlaneInitializationReason, clusterv1.ConditionSeverityInfo, "")
		return ctrl.Result{Requeue: true, RequeueAfter: time.Second * 30}, nil
	}

	machines, err := collections.GetFilteredMachinesForCluster(ctx, c.Client, cluster, collections.ControlPlaneMachines(cluster.Name), collections.ActiveMachines)
	if err != nil {
		err = fmt.Errorf("error collecting machines: %w", err)
		conditions.MarkFalse(config, bootstrapv1.DataSecretAvailableCondition, bootstrapv1.DataSecretGenerationFailedReason, clusterv1.ConditionSeverityError, err.Error())
		return ctrl.Result{}, err
	}

	if machines.Len() == 0 {
		log.Info("No control plane machines found, waiting for machines to be created")
		conditions.MarkFalse(config, bootstrapv1.DataSecretAvailableCondition, bootstrapv1.WaitingForInfrastructureInitializationReason, clusterv1.ConditionSeverityInfo, "")
		return ctrl.Result{Requeue: true}, nil
	}
	scope.machines = machines

	bootstrapData, err := c.generateBootstrapDataForController(ctx, log, scope)
	if err != nil {
		// if the bootstrap data generation corresponds to a controller that is not the initial one, it is common to try to obtain
		// the IP of the first controller when has not yet been surfaced. This is required to create a join token. It is needed to
		// wait for the addresses to be set.
		if errors.Is(err, errInitialControllerMachineNotInitialize) {
			conditions.MarkFalse(config, bootstrapv1.DataSecretAvailableCondition, bootstrapv1.WaitingForInfrastructureInitializationReason, clusterv1.ConditionSeverityInfo, "")
			return ctrl.Result{RequeueAfter: time.Second * 30}, nil
		}

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
					Kind:       config.Kind,
					Name:       config.Name,
					UID:        config.UID,
					Controller: ptr.To(true),
				},
			},
		},
		Data: map[string][]byte{
			"value": bootstrapData,
		},
		Type: clusterv1.ClusterSecretType,
	}

	if err := c.Client.Patch(ctx, bootstrapSecret, client.Apply, &client.PatchOptions{FieldManager: "k0s-bootstrap"}); err != nil {
		log.Error(err, "Failed to patch bootstrap secret")
		conditions.MarkFalse(config, bootstrapv1.DataSecretAvailableCondition, bootstrapv1.DataSecretGenerationFailedReason, clusterv1.ConditionSeverityError, err.Error())
		return ctrl.Result{}, err
	}
	conditions.MarkTrue(config, bootstrapv1.DataSecretAvailableCondition)
	log.Info("Bootstrap secret created", "secret", bootstrapSecret.Name)

	// Set the status to ready
	config.Status.Ready = true
	config.Status.DataSecretName = ptr.To(bootstrapSecret.Name)

	log.Info("Reconciled succesfully")

	return ctrl.Result{}, nil
}

func (c *ControlPlaneController) generateBootstrapDataForController(ctx context.Context, log logr.Logger, scope *ControllerScope) ([]byte, error) {
	var (
		files      []cloudinit.File
		installCmd string
		err        error
	)

	currentKCPVersion, err := version.NewVersion(scope.Config.Spec.Version)
	if err != nil {
		return nil, fmt.Errorf("error parsing k0s version: %w", err)
	}
	if currentKCPVersion.GreaterThanOrEqual(minVersionForETCDName) {
		if scope.Config.Spec.K0s == nil {
			scope.Config.Spec.K0s = &unstructured.Unstructured{
				Object: make(map[string]interface{}),
			}
		}
		// If it is not explicitly indicated to use Kine storage, we use the machine name to name the ETCD member.
		kineStorage, found, err := unstructured.NestedString(scope.Config.Spec.K0s.Object, "spec", "storage", "kine", "dataSource")
		if err != nil {
			return nil, fmt.Errorf("error retrieving storage.kine.datasource: %w", err)
		}
		if !found || kineStorage == "" {
			err = unstructured.SetNestedMap(scope.Config.Spec.K0s.Object, map[string]interface{}{}, "spec", "storage", "etcd", "extraArgs")
			if err != nil {
				return nil, fmt.Errorf("error ensuring intermediate maps spec.storage.etcd.extraArgs: %w", err)
			}
			err = unstructured.SetNestedField(scope.Config.Spec.K0s.Object, scope.ConfigOwner.GetName(), "spec", "storage", "etcd", "extraArgs", "name")
			if err != nil {
				return nil, fmt.Errorf("error setting storage.etcd.extraArgs.name: %w", err)
			}
		}
	}

	if scope.Config.Spec.K0s != nil {
		k0sConfigBytes, err := scope.Config.Spec.K0s.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("error marshalling k0s config: %v", err)
		}
		files = append(files, cloudinit.File{
			Path:        "/etc/k0s.yaml",
			Permissions: "0644",
			Content:     string(k0sConfigBytes),
		})
		scope.Config.Spec.Args = append(scope.Config.Spec.Args, "--config", "/etc/k0s.yaml")
	}

	if scope.machines.Oldest().Name == scope.Config.Name {
		files, err = c.genInitialControlPlaneFiles(ctx, scope, files)
		if err != nil {
			return nil, fmt.Errorf("error generating initial control plane files: %v", err)
		}
		installCmd = createCPInstallCmd(scope)
	} else {
		oldest := getFirstRunningMachineWithLatestVersion(scope.machines)
		if oldest == nil {
			log.Info("wait for initial control plane provisioning")
			return nil, err
		}
		files, err = c.genControlPlaneJoinFiles(ctx, scope, files, oldest)
		if err != nil {
			return nil, err
		}
		installCmd = createCPInstallCmdWithJoinToken(scope, joinTokenFilePath)
	}

	if scope.Config.Spec.Tunneling.Enabled {
		tunnelingFiles, err := c.genTunnelingFiles(ctx, scope)
		if err != nil {
			return nil, fmt.Errorf("error generating tunneling files: %v", err)
		}
		files = append(files, tunnelingFiles...)
	}

	resolvedFiles, err := resolveFiles(ctx, c.Client, scope.Cluster, scope.Config.Spec.Files)
	if err != nil {
		return nil, fmt.Errorf("error extracting the contents of the provided extra files: %w", err)
	}
	files = append(files, resolvedFiles...)
	files = append(files, genShutdownServiceFiles()...)

	downloadCommands := createCPDownloadCommands(scope.Config)

	commands := scope.Config.Spec.PreStartCommands
	commands = append(commands, downloadCommands...)
	commands = append(commands, "(command -v systemctl > /dev/null 2>&1 && (cp /k0s/k0sleave.service /etc/systemd/system/k0sleave.service && systemctl daemon-reload && systemctl enable k0sleave.service && systemctl start k0sleave.service) || true)")
	commands = append(commands, "(command -v rc-service > /dev/null 2>&1 && (cp /k0s/k0sleave-openrc /etc/init.d/k0sleave && rc-update add k0sleave shutdown) || true)")
	commands = append(commands, "(command -v service > /dev/null 2>&1 && (cp /k0s/k0sleave-sysv /etc/init.d/k0sleave && update-rc.d k0sleave defaults && service k0sleave start) || true)")
	commands = append(commands, installCmd, "k0s start")
	commands = append(commands, scope.Config.Spec.PostStartCommands...)
	// Create the sentinel file as the last step so we know all previous _stuff_ has completed
	// https://cluster-api.sigs.k8s.io/developer/providers/contracts/bootstrap-config#sentinel-file
	commands = append(commands, "mkdir -p /run/cluster-api && touch /run/cluster-api/bootstrap-success.complete")

	ci := &cloudinit.CloudInit{
		Files:   files,
		RunCmds: commands,
	}
	if scope.Config.Spec.CustomUserDataRef != nil {
		customCloudInit, err := resolveContentFromFile(ctx, c.Client, scope.Cluster, scope.Config.Spec.CustomUserDataRef)
		if err != nil {
			return nil, fmt.Errorf("error extracting the contents of the provided custom controller user data: %w", err)
		}
		ci.CustomCloudInit = customCloudInit
	}

	return ci.AsBytes()
}

func (c *ControlPlaneController) genInitialControlPlaneFiles(ctx context.Context, scope *ControllerScope, files []cloudinit.File) ([]cloudinit.File, error) {
	log := log.FromContext(ctx).WithValues("K0sControllerConfig cluster", scope.Cluster.Name)

	certs, _, err := c.getCerts(ctx, scope)
	if err != nil {
		log.Error(err, "Failed to get certs")
		return nil, err
	}
	files = append(files, certs...)

	return files, nil
}

func (c *ControlPlaneController) genControlPlaneJoinFiles(ctx context.Context, scope *ControllerScope, files []cloudinit.File, firstControllerMachine *clusterv1.Machine) ([]cloudinit.File, error) {
	log := log.FromContext(ctx).WithValues("K0sControllerConfig cluster", scope.Cluster.Name)

	_, ca, err := c.getCerts(ctx, scope)
	if err != nil {
		log.Error(err, "Failed to create certs")
		return nil, err
	}

	// Create the token using the child cluster client
	tokenID := kutil.RandomString(6)
	tokenSecret := kutil.RandomString(16)
	token := fmt.Sprintf("%s.%s", tokenID, tokenSecret)
	tokenKubeSecret := createTokenSecret(tokenID, tokenSecret)

	chCS, err := remote.NewClusterClient(ctx, "k0smotron", c.Client, capiutil.ObjectKey(scope.Cluster))
	if err != nil {
		log.Error(err, "Failed to getting child cluster client set")
		return nil, err
	}

	err = chCS.Create(ctx, tokenKubeSecret)
	if err != nil {
		log.Error(err, "Failed to create token secret in the child cluster")
		return nil, err
	}

	host, err := c.detectJoinHost(ctx, scope, firstControllerMachine)
	if err != nil {
		log.Error(err, "Failed to detect join controller host")
		return nil, err
	}

	joinToken, err := kutil.CreateK0sJoinToken(ca.KeyPair.Cert, token, host, "controller-bootstrap")

	files = append(files, cloudinit.File{
		Path:        joinTokenFilePath,
		Permissions: "0644",
		Content:     joinToken,
	})

	return files, err
}

func (c *ControlPlaneController) genTunnelingFiles(ctx context.Context, scope *ControllerScope) ([]cloudinit.File, error) {
	secretName := scope.Cluster.Name + "-frp-token"
	frpSecret := corev1.Secret{}
	err := c.Client.Get(ctx, client.ObjectKey{Namespace: scope.Cluster.Namespace, Name: secretName}, &frpSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to get frp secret: %w", err)
	}
	frpToken := string(frpSecret.Data["value"])

	localIP := "10.96.0.1"
	if scope.Cluster.Spec.ClusterNetwork != nil && scope.Cluster.Spec.ClusterNetwork.Services != nil {
		kubeSvcIP, err := constants.GetAPIServerVirtualIP(scope.Cluster.Spec.ClusterNetwork.Services.String())
		if err != nil {
			return nil, err
		}
		localIP = kubeSvcIP.String()
	}

	var modeConfig string
	if scope.Config.Spec.Tunneling.Mode == "proxy" {
		modeConfig = fmt.Sprintf(`
    type = tcpmux
    custom_domains = %s
    multiplexer = httpconnect
`, scope.Cluster.Spec.ControlPlaneEndpoint.Host)
	} else {
		modeConfig = `
    remote_port = 6443
`
	}

	tunnelingResources := `
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: frpc-config
  namespace: kube-system
data:
  frpc.ini: |
    [common]
    authentication_method = token
    server_addr = %s
    server_port = %d
    token = %s

    [kube-apiserver]
    type = tcp
    local_ip = %s
    local_port = 443
    %s
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: frpc
  namespace: kube-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: frpc
  template:
    metadata:
      labels:
        app: frpc
    spec:
      containers:
        - name: frpc
          image: snowdreamtech/frpc:0.51.3
          imagePullPolicy: "IfNotPresent"
          volumeMounts:
            - name: frpc-config
              mountPath: /etc/frp/frpc.ini
              subPath: frpc.ini
      volumes:
        - name: frpc-config
          configMap:
            name: frpc-config
            items:
              - key: frpc.ini
                path: frpc.ini

`
	return []cloudinit.File{{
		Path:        "/var/lib/k0s/manifests/k0smotron-tunneling/manifest.yaml",
		Permissions: "0644",
		Content:     fmt.Sprintf(tunnelingResources, scope.Config.Spec.Tunneling.ServerAddress, scope.Config.Spec.Tunneling.ServerNodePort, frpToken, localIP, modeConfig),
	}}, nil
}

func (c *ControlPlaneController) getCerts(ctx context.Context, scope *ControllerScope) ([]cloudinit.File, *secret.Certificate, error) {
	var files []cloudinit.File
	certificates := secret.NewCertificatesForInitialControlPlane(&kubeadmbootstrapv1.ClusterConfiguration{
		CertificatesDir: "/var/lib/k0s/pki",
	})

	s := &corev1.Secret{}
	err := c.SecretCachingClient.Get(ctx, client.ObjectKey{Namespace: scope.Cluster.Namespace, Name: secret.Name(scope.Cluster.Name, secret.Kubeconfig)}, s)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, fmt.Errorf("cluster's kubeconfig secret not found, waiting for secret")
		}
		return nil, nil, err
	}

	err = certificates.LookupCached(ctx, c.SecretCachingClient, c.Client, capiutil.ObjectKey(scope.Cluster))
	if err != nil {
		return nil, nil, err
	}
	ca := certificates.GetByPurpose(secret.ClusterCA)
	for _, cert := range certificates.AsFiles() {
		files = append(files, cloudinit.File{
			Path:        cert.Path,
			Permissions: "0644",
			Content:     cert.Content,
		})
	}

	return files, ca, nil
}

func createTokenSecret(tokenID, tokenSecret string) *corev1.Secret {
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("bootstrap-token-%s", tokenID),
			Namespace: "kube-system",
		},
		Type: corev1.SecretTypeBootstrapToken,
		StringData: map[string]string{
			"token-id":     tokenID,
			"token-secret": tokenSecret,
			// TODO We need bit shorter time for the token
			"expiration":                     time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"description":                    "Controller bootstrap token generated by k0smotron",
			"usage-bootstrap-api-auth":       "true",
			"usage-bootstrap-authentication": "false",
			"usage-bootstrap-signing":        "false",
			"usage-controller-join":          "true",
		},
	}
}

func (c *ControlPlaneController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&bootstrapv1.K0sControllerConfig{}).
		Complete(c)
}

func createCPDownloadCommands(config *bootstrapv1.K0sControllerConfig) []string {
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

func createCPInstallCmd(scope *ControllerScope) string {
	installCmd := []string{
		"k0s install controller",
		"--force",
		"--enable-dynamic-config",
		"--env AUTOPILOT_HOSTNAME=" + scope.Config.Name,
	}

	installCmd = append(installCmd, mergeControllerExtraArgs(scope)...)

	return strings.Join(installCmd, " ")
}

func createCPInstallCmdWithJoinToken(scope *ControllerScope, tokenPath string) string {
	installCmd := []string{
		"k0s install controller",
		"--force",
		"--enable-dynamic-config",
		"--env AUTOPILOT_HOSTNAME=" + scope.Config.Name,
	}

	installCmd = append(installCmd, mergeControllerExtraArgs(scope)...)
	installCmd = append(installCmd, "--token-file", tokenPath)

	return strings.Join(installCmd, " ")
}

func mergeControllerExtraArgs(scope *ControllerScope) []string {
	return mergeExtraArgs(scope.Config.Spec.Args, scope.ConfigOwner, scope.WorkerEnabled, scope.Config.Spec.UseSystemHostname)
}

func (c *ControlPlaneController) detectJoinHost(ctx context.Context, scope *ControllerScope, firstControllerMachine *clusterv1.Machine) (string, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		// Since we are using self-signed certificates, we need to skip the verification
		InsecureSkipVerify: true,
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   time.Second,
	}

	// TODO: fix hardcoded port
	port := "9443"
	host := fmt.Sprintf("https://%s:%s", scope.Cluster.Spec.ControlPlaneEndpoint.Host, port)

	_, err := httpClient.Get(fmt.Sprintf("%s/v1beta1/ca", host))
	if err == nil {
		return host, nil
	}

	firstControllerIP, err := c.findFirstControllerIP(ctx, firstControllerMachine)
	if err != nil {
		return "", fmt.Errorf("failed to get first controller IP: %w", err)
	}

	return fmt.Sprintf("https://%s:%s", firstControllerIP, port), nil
}

func (c *ControlPlaneController) findFirstControllerIP(ctx context.Context, firstControllerMachine *clusterv1.Machine) (string, error) {
	extAddr, intIPv4Addr, intAddr := "", "", ""
	for _, addr := range firstControllerMachine.Status.Addresses {
		if addr.Type == clusterv1.MachineExternalIP {
			extAddr = addr.Address
			break
		}
		if addr.Type == clusterv1.MachineInternalIP {
			ip, err := netip.ParseAddr(addr.Address)
			if err != nil {
				continue
			}
			if ip.Is4() {
				intIPv4Addr = ip.String()
				break
			}
			if ip.Is6() {
				intAddr = fmt.Sprintf("[%s]", ip.WithZone("").String())
			}
		}
	}

	name := firstControllerMachine.Name

	if extAddr == "" && intAddr == "" && intIPv4Addr == "" {
		machineImpl, err := c.getMachineImplementation(ctx, firstControllerMachine)
		if err != nil {
			return "", fmt.Errorf("error getting machine implementation: %w", err)
		}
		addresses, found, err := unstructured.NestedSlice(machineImpl.UnstructuredContent(), "status", "addresses")
		if err != nil {
			return "", err
		}

		if found {
			for _, addr := range addresses {
				addrMap, _ := addr.(map[string]interface{})
				if addrMap["type"] == string(v1.NodeExternalIP) {
					extAddr = addrMap["address"].(string)
					break
				}
				if addrMap["type"] == string(v1.NodeInternalIP) {
					ip, err := netip.ParseAddr(addrMap["address"].(string))
					if err != nil {
						continue
					}
					if ip.Is4() {
						intIPv4Addr = ip.String()
						break
					}
					if ip.Is6() {
						intAddr = fmt.Sprintf("[%s]", ip.WithZone("").String())
					}
				}
			}
		}
	}

	if extAddr != "" {
		return extAddr, nil
	}

	if intIPv4Addr != "" {
		return intIPv4Addr, nil
	}

	if intAddr != "" {
		return intAddr, nil
	}

	return "", fmt.Errorf("no address found for machine %s: %w", name, errInitialControllerMachineNotInitialize)
}

func (c *ControlPlaneController) getMachineImplementation(ctx context.Context, machine *clusterv1.Machine) (*unstructured.Unstructured, error) {
	infRef := machine.Spec.InfrastructureRef

	machineImpl := new(unstructured.Unstructured)
	machineImpl.SetAPIVersion(infRef.APIVersion)
	machineImpl.SetKind(infRef.Kind)
	machineImpl.SetName(infRef.Name)

	key := client.ObjectKey{Name: infRef.Name, Namespace: infRef.Namespace}

	err := c.Get(ctx, key, machineImpl)
	if err != nil {
		return nil, fmt.Errorf("error getting machine implementation object: %w", err)
	}
	return machineImpl, nil
}

func genShutdownServiceFiles() []cloudinit.File {
	return []cloudinit.File{
		{
			Path:        "/etc/bin/k0sleave.sh",
			Permissions: "0777",
			Content: `#!/bin/sh

PID=$(k0s status | grep "Process ID" | awk '{print $3}')
AUTOPILOT_HOSTNAME=$(tr '\0' '\n' < /proc/$PID/environ | grep AUTOPILOT_HOSTNAME)
MACHINE_NAME=${AUTOPILOT_HOSTNAME#"AUTOPILOT_HOSTNAME="}

IS_LEAVING=$(/usr/local/bin/k0s kc get controlnodes $MACHINE_NAME -o jsonpath='{.metadata.annotations.k0smotron\.io/leave}')

if [ $IS_LEAVING = "true" ]; then
    until /usr/local/bin/k0s etcd leave; do
        sleep 1
    done
fi
`,
		}, {
			Path:        "/k0s/k0sleave.service",
			Permissions: "0644",
			Content: `[Unit]
Description=k0s etcd leave service
After=multi-user.target

[Service]
Type=simple
ExecStart=/bin/true
ExecStop=/etc/bin/k0sleave.sh
TimeoutStartSec=0
TimeoutStopSec=180
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
`,
		},
		{
			Path:        "/k0s/k0sleave-openrc",
			Permissions: "0644",
			Content: `#!/sbin/openrc-run

name="k0sleave"
description="k0s etcd leave service"
command="/etc/bin/k0sleave.sh"
		`,
		},
		{
			Path:        "/k0s/k0sleave-sysv",
			Permissions: "0644",
			Content: `#!/bin/sh
# For RedHat and cousins:
# chkconfig: - 99 01
# description: k0s etcd leave service
# processname: k0sleave

### BEGIN INIT INFO
# Provides:          k0sleave
# Required-Start:
# Required-Stop:
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Short-Description: k0s etcd leave service
# Description:       Handles etcd leave operations for k0s.
### END INIT INFO

cmd="/etc/bin/k0sleave.sh"

name="k0sleave"
pid_file="/var/run/$name.pid"
stdout_log="/var/log/$name.log"
stderr_log="/var/log/$name.err"

get_pid() {
    [ -f "$pid_file" ] && cat "$pid_file"
}

is_running() {
    [ -f "$pid_file" ] && ps $(get_pid) > /dev/null 2>&1
}

case "$1" in
    start)
        if is_running; then
            echo "Already started"
        else
            echo "Starting $name"
            $cmd >> "$stdout_log" 2>> "$stderr_log" &
            echo $! > "$pid_file"
            if ! is_running; then
                echo "Unable to start, see $stdout_log and $stderr_log"
                exit 1
            fi
        fi
    ;;
    stop)
        if is_running; then
            echo -n "Stopping $name.."
            kill $(get_pid)
            for i in $(seq 1 10)
            do
                if ! is_running; then
                    break
                fi
                echo -n "."
                sleep 1
            done
            echo
            if is_running; then
                echo "Not stopped; may still be shutting down or shutdown may have failed"
                exit 1
            else
                echo "Stopped"
                if [ -f "$pid_file" ]; then
                    rm "$pid_file"
                fi
            fi
        else
            echo "Not running"
        fi
    ;;
    restart)
        $0 stop
        if is_running; then
            echo "Unable to stop, will not attempt to start"
            exit 1
        fi
        $0 start
    ;;
    status)
        if is_running; then
            echo "Running"
        else
            echo "Stopped"
            exit 1
        fi
    ;;
    *)
        echo "Usage: $0 {start|stop|restart|status}"
        exit 1
    ;;
esac
exit 0`,
		},
	}
}

func getFirstRunningMachineWithLatestVersion(machines collections.Machines) *clusterv1.Machine {
	res := make(machinesByVersionAndCreationTimestamp, 0, len(machines))
	for _, value := range machines {
		if value.Status.Phase == string(clusterv1.MachinePhasePending) {
			continue
		}
		res = append(res, value)
	}
	if len(res) == 0 {
		return nil
	}
	sort.Sort(res)
	return res[0]
}

// machinesByCreationTimestamp sorts a list of Machine by creation timestamp, using their names as a tie breaker.
type machinesByVersionAndCreationTimestamp []*clusterv1.Machine

func (o machinesByVersionAndCreationTimestamp) Len() int      { return len(o) }
func (o machinesByVersionAndCreationTimestamp) Swap(i, j int) { o[i], o[j] = o[j], o[i] }
func (o machinesByVersionAndCreationTimestamp) Less(i, j int) bool {

	if o[i].CreationTimestamp.Equal(&o[j].CreationTimestamp) {
		return o[i].Name < o[j].Name
	}
	return *o[i].Spec.Version < *o[j].Spec.Version && o[i].CreationTimestamp.Before(&o[j].CreationTimestamp)
}
