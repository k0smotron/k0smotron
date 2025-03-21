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

package envtest

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	goruntime "runtime"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/pkg/errors"
	"golang.org/x/tools/go/packages"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/klog/v2"
	"sigs.k8s.io/cluster-api/cmd/clusterctl/log"
	"sigs.k8s.io/cluster-api/util/kubeconfig"

	bootstrapv1beta1 "github.com/k0smotron/k0smotron/api/bootstrap/v1beta1"
	controlplanev1beta1 "github.com/k0smotron/k0smotron/api/controlplane/v1beta1"
	infrastructurev1beta1 "github.com/k0smotron/k0smotron/api/infrastructure/v1beta1"
	k0smotronv1beta1 "github.com/k0smotron/k0smotron/api/k0smotron.io/v1beta1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var (
	cacheSyncBackoff = wait.Backoff{
		Duration: 100 * time.Millisecond,
		Factor:   1.5,
		Steps:    8,
		Jitter:   0.4,
	}
)

type setupSecretCachingClientFn func(mgr manager.Manager) error

// Environment encapsulates a Kubernetes local test environment.
type Environment struct {
	manager.Manager
	client.Client
	Config *rest.Config
	env    *envtest.Environment
	cancel context.CancelFunc
}

func init() {
	logger := klog.Background()
	// Use klog as the internal logger for this envtest environment.
	log.SetLogger(logger)
	// Additionally force all controllers to use the Ginkgo logger.
	ctrl.SetLogger(logger)
	// Add logger for ginkgo.
	klog.SetOutput(ginkgo.GinkgoWriter)

	utilruntime.Must(apiextensionsv1.AddToScheme(scheme.Scheme))
	utilruntime.Must(clusterv1.AddToScheme(scheme.Scheme))
	utilruntime.Must(k0smotronv1beta1.AddToScheme(scheme.Scheme))
	utilruntime.Must(bootstrapv1beta1.AddToScheme(scheme.Scheme))
	utilruntime.Must(controlplanev1beta1.AddToScheme(scheme.Scheme))
	utilruntime.Must(infrastructurev1beta1.AddToScheme(scheme.Scheme))
}

func newEnvironment(setupSecretCachingClient setupSecretCachingClientFn) *Environment {
	_, filename, _, _ := goruntime.Caller(0)
	root := path.Join(path.Dir(filename), "..", "..", "..")

	capiCoreCrdsPath := ""
	if capiCoreCrdsPath = getFilePathToCAPICoreCRDs(); capiCoreCrdsPath == "" {
		panic(fmt.Errorf("failed to retrieve cluster-api core crds path"))
	}

	crdPaths := []string{
		capiCoreCrdsPath,
		filepath.Join(root, "config", "clusterapi", "bootstrap", "bases"),
		filepath.Join(root, "config", "clusterapi", "controlplane", "bases"),
		filepath.Join(root, "config", "clusterapi", "infrastructure", "bases"),
		filepath.Join(root, "config", "clusterapi", "k0smotron.io", "bases"),
	}

	env := &envtest.Environment{
		ErrorIfCRDPathMissing: true,
		CRDDirectoryPaths:     crdPaths,
		CRDs: []*apiextensionsv1.CustomResourceDefinition{
			genericInfrastructureMachineCRD,
			genericInfrastructureMachineTemplateCRD,
		},
	}

	if _, err := env.Start(); err != nil {
		panic(err)
	}

	req, _ := labels.NewRequirement(clusterv1.ClusterNameLabel, selection.Exists, nil)
	clusterSecretCacheSelector := labels.NewSelector().Add(*req)

	options := manager.Options{
		Scheme: scheme.Scheme,
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&v1.Secret{}: {
					Label: clusterSecretCacheSelector,
				},
			},
		},
		Client: client.Options{
			Cache: &client.CacheOptions{
				DisableFor: []client.Object{
					&v1.ConfigMap{},
					&v1.Secret{},
				},
				// Use the cache for all Unstructured get/list calls.
				Unstructured: true,
			},
		},
	}

	mgr, err := ctrl.NewManager(env.Config, options)
	if err != nil {
		panic(fmt.Errorf("failed to start testenv manager: %w", err))
	}

	err = setupSecretCachingClient(mgr)
	if err != nil {
		panic(fmt.Errorf("failed to setup secret caching client: %w", err))
	}

	if kubeconfigPath := os.Getenv("TEST_ENV_KUBECONFIG"); kubeconfigPath != "" {
		klog.Infof("Writing test env kubeconfig to %q", kubeconfigPath)
		config := kubeconfig.FromEnvTestConfig(env.Config, &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test"},
		})
		if err := os.WriteFile(kubeconfigPath, config, 0o600); err != nil {
			panic(errors.Wrapf(err, "failed to write the test env kubeconfig"))
		}
	}

	return &Environment{
		Manager: mgr,
		Client:  mgr.GetClient(),
		Config:  mgr.GetConfig(),
		env:     env,
	}
}

func Build(ctx context.Context, setupSecretCachingClient setupSecretCachingClientFn) *Environment {
	testEnv := newEnvironment(setupSecretCachingClient)
	go func() {
		fmt.Println("Starting the manager")
		if err := testEnv.StartManager(ctx); err != nil {
			panic(fmt.Sprintf("Failed to start the envtest manager: %v", err))
		}
	}()

	return testEnv
}

func (e *Environment) Teardown() {
	e.cancel()
	if err := e.Stop(); err != nil {
		panic(fmt.Sprintf("Failed to stop envtest: %v", err))
	}
}

// StartManager starts the test controller against the local API server.
func (e *Environment) StartManager(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	return e.Manager.Start(ctx)
}

// Stop stops the test environment.
func (e *Environment) Stop() error {
	e.cancel()
	return e.env.Stop()
}

func getFilePathToCAPICoreCRDs() string {
	packageName := "sigs.k8s.io/cluster-api"
	packageConfig := &packages.Config{
		Mode: packages.NeedModule,
	}

	pkgs, err := packages.Load(packageConfig, packageName)
	if err != nil {
		return ""
	}

	return filepath.Join(pkgs[0].Module.Dir, "config", "crd", "bases")
}

// CreateNamespace creates a new namespace with a generated name.
func (e *Environment) CreateNamespace(ctx context.Context, generateName string) (*v1.Namespace, error) {
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-", generateName),
			Labels: map[string]string{
				"testenv/original-name": generateName,
			},
		},
	}
	if err := e.Client.Create(ctx, ns); err != nil {
		return nil, err
	}

	return ns, nil
}

// Cleanup removes objects from the Environment.
func (e *Environment) Cleanup(ctx context.Context, objs ...client.Object) error {
	errs := make([]error, 0, len(objs))
	for _, o := range objs {
		err := e.Client.Delete(ctx, o)
		if apierrors.IsNotFound(err) {
			// If the object is not found, it must've been garbage collected
			// already. For example, if we delete namespace first and then
			// objects within it.
			continue
		}
		errs = append(errs, err)
	}

	return kerrors.NewAggregate(errs)
}

// CleanupAndWait deletes all the given objects and waits for the cache to be updated accordingly.
//
// NOTE: Waiting for the cache to be updated helps in preventing test flakes due to the cache sync delays.
func (e *Environment) CleanupAndWait(ctx context.Context, objs ...client.Object) error {
	if err := e.Cleanup(ctx, objs...); err != nil {
		return err
	}

	// Makes sure the cache is updated with the deleted object
	errs := []error{}
	for _, o := range objs {
		// Ignoring namespaces because in testenv the namespace cleaner is not running.
		if o.GetObjectKind().GroupVersionKind().GroupKind() == v1.SchemeGroupVersion.WithKind("Namespace").GroupKind() {
			continue
		}

		oCopy := o.DeepCopyObject().(client.Object)
		key := client.ObjectKeyFromObject(o)
		err := wait.ExponentialBackoff(
			cacheSyncBackoff,
			func() (done bool, err error) {
				if err := e.Get(ctx, key, oCopy); err != nil {
					if apierrors.IsNotFound(err) {
						return true, nil
					}
					return false, err
				}
				return false, nil
			})
		errs = append(errs, errors.Wrapf(err, "key %s, %s is not being deleted from the testenv client cache", o.GetObjectKind().GroupVersionKind().String(), key))
	}
	return kerrors.NewAggregate(errs)
}
