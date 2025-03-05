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

package main

import (
	"crypto/md5"
	"crypto/tls"
	"flag"
	"fmt"
	"os"

	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/tools/clientcmd"

	bootstrapv1beta1 "github.com/k0smotron/k0smotron/api/bootstrap/v1beta1"
	controlplanev1beta1 "github.com/k0smotron/k0smotron/api/controlplane/v1beta1"
	infrastructurev1beta1 "github.com/k0smotron/k0smotron/api/infrastructure/v1beta1"
	k0smotronv1beta1 "github.com/k0smotron/k0smotron/api/k0smotron.io/v1beta1"
	"github.com/k0smotron/k0smotron/internal/controller/bootstrap"
	"github.com/k0smotron/k0smotron/internal/controller/controlplane"
	"github.com/k0smotron/k0smotron/internal/controller/infrastructure"
	controller "github.com/k0smotron/k0smotron/internal/controller/k0smotron.io"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	//+kubebuilder:scaffold:imports
)

var (
	scheme             = runtime.NewScheme()
	setupLog           = ctrl.Log.WithName("setup")
	enabledControllers = map[string]bool{
		bootstrapController:      true,
		controlPlaneController:   true,
		infrastructureController: true,
	}
)

const (
	allControllers           = "all"
	bootstrapController      = "bootstrap"
	controlPlaneController   = "control-plane"
	infrastructureController = "infrastructure"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(k0smotronv1beta1.AddToScheme(scheme))
	utilruntime.Must(bootstrapv1beta1.AddToScheme(scheme))

	// Register cluster-api types
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	utilruntime.Must(controlplanev1beta1.AddToScheme(scheme))
	utilruntime.Must(infrastructurev1beta1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var secureMetrics bool
	var enableHTTP2 bool
	var probeAddr string
	var enabledController string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8443", "The address the metric endpoint binds to. "+
		"Use :8080 for http and :8443 for https. Setting to 0 will disable the metrics endpoint.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")

	flag.StringVar(&enabledController, "enable-controller", "", "The controller to enable. Default: all")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	if enabledController != "" && enabledController != allControllers {
		enabledControllers = map[string]bool{
			enabledController: true,
		}
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	var tlsOpts []func(*tls.Config)
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	metricsOpts := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		metricsOpts.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsOpts,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       fmt.Sprintf("%x.k0smotron.io", md5.Sum([]byte(enabledController))),
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	restConfig, err := loadRestConfig()
	if err != nil {
		setupLog.Error(err, "unable to get cluster config")
		os.Exit(1)
	}
	clientSet, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		setupLog.Error(err, "unable to get kubernetes clientset")
		os.Exit(1)
	}

	//+kubebuilder:scaffold:builder

	if isControllerEnabled(bootstrapController) {
		if err = (&bootstrap.Controller{
			Client:     mgr.GetClient(),
			Scheme:     mgr.GetScheme(),
			ClientSet:  clientSet,
			RESTConfig: restConfig,
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Bootstrap")
			os.Exit(1)
		}
		if err = (&bootstrap.ControlPlaneController{
			Client:     mgr.GetClient(),
			Scheme:     mgr.GetScheme(),
			ClientSet:  clientSet,
			RESTConfig: restConfig,
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Bootstrap")
			os.Exit(1)
		}
		if err = (&bootstrap.ProviderIDController{
			Client:    mgr.GetClient(),
			Scheme:    mgr.GetScheme(),
			ClientSet: clientSet,
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Bootstrap")
			os.Exit(1)
		}
	}

	if isControllerEnabled(controlPlaneController) {
		if err = (&controller.ClusterReconciler{
			Client:     mgr.GetClient(),
			Scheme:     mgr.GetScheme(),
			ClientSet:  clientSet,
			RESTConfig: restConfig,
			Recorder:   mgr.GetEventRecorderFor("cluster-reconciler"),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "K0smotronCluster")
			os.Exit(1)
		}

		if err = (&controller.JoinTokenRequestReconciler{
			Client:     mgr.GetClient(),
			Scheme:     mgr.GetScheme(),
			ClientSet:  clientSet,
			RESTConfig: restConfig,
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "JoinTokenRequest")
			os.Exit(1)
		}

		if err = (&controlplane.K0smotronController{
			Client:     mgr.GetClient(),
			Scheme:     mgr.GetScheme(),
			ClientSet:  clientSet,
			RESTConfig: restConfig,
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "K0smotronControlPlane")
			os.Exit(1)
		}

		if err = (&controlplane.K0sController{
			Client:     mgr.GetClient(),
			ClientSet:  clientSet,
			RESTConfig: restConfig,
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "K0sController")
			os.Exit(1)
		}

		if err = (&controlplane.K0sControlPlaneValidator{}).SetupK0sControlPlaneWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create validation webhook", "webhook", "K0sControlPlaneValidator")
			os.Exit(1)
		}
	}

	if isControllerEnabled(infrastructureController) {
		if err = (&infrastructure.RemoteMachineController{
			Client:     mgr.GetClient(),
			Scheme:     mgr.GetScheme(),
			ClientSet:  clientSet,
			RESTConfig: restConfig,
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "RemoteMachine")
			os.Exit(1)
		}

		if err = (&infrastructure.ClusterController{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "RemoteCluster")
			os.Exit(1)
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// loadRestConfig loads the rest config from the KUBECONFIG env var or from the in-cluster config
func loadRestConfig() (*rest.Config, error) {
	if os.Getenv("KUBECONFIG") != "" {
		return clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	}
	return rest.InClusterConfig()
}

func isControllerEnabled(controllerName string) bool {
	return enabledControllers[controllerName]
}
