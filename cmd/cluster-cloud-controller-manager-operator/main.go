/*
Copyright 2021.

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
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"os"
	"time"

	"github.com/spf13/pflag"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/component-base/config"
	"k8s.io/component-base/config/options"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	utiltls "github.com/openshift/controller-runtime-common/pkg/tls"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/openshift/library-go/pkg/operator/events"
	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/controllers"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/util"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	leaderElectionConfig = config.LeaderElectionConfiguration{
		LeaderElect:  true,
		ResourceName: "cluster-cloud-controller-manager-leader",
	}
)

const (
	defaultImagesLocation = "/etc/cloud-controller-manager-config/images.json"
	kubeSystemNamespace   = "kube-system"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(configv1.AddToScheme(scheme))
	utilruntime.Must(operatorv1.AddToScheme(scheme))

	// +kubebuilder:scaffold:scheme
}

func main() {
	klog.InitFlags(flag.CommandLine)

	metricsAddr := flag.String(
		"metrics-bind-address",
		":9258",
		"Address for hosting metrics",
	)

	webhookPort := flag.Int(
		"webhook-port",
		9443,
		"Webhook Server port",
	)

	healthAddr := flag.String(
		"health-addr",
		":9440",
		"The address for health checking.",
	)
	managedNamespace := flag.String(
		"namespace",
		controllers.DefaultManagedNamespace,
		"The namespace for managed objects, where out-of-tree CCM binaries will run.",
	)

	imagesFile := flag.String(
		"images-json",
		defaultImagesLocation,
		"The location of images file to use by operator for managed CCM binaries.",
	)

	// Once all the flags are regitered, switch to pflag
	// to allow leader lection flags to be bound
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	options.BindLeaderElectionFlags(&leaderElectionConfig, pflag.CommandLine)
	pflag.Parse()

	ctrl.SetLogger(klog.NewKlogr().WithName("CCMOperator"))

	restConfig := ctrl.GetConfigOrDie()
	le := util.GetLeaderElectionDefaults(restConfig, configv1.LeaderElection{
		Disable:       !leaderElectionConfig.LeaderElect,
		RenewDeadline: leaderElectionConfig.RenewDeadline,
		RetryPeriod:   leaderElectionConfig.RetryPeriod,
		LeaseDuration: leaderElectionConfig.LeaseDuration,
	})

	// Create a cancellable context so the TLS controller can trigger a shutdown
	ctx, cancel := context.WithCancel(ctrl.SetupSignalHandler())
	// Ensure the context is cancelled when the program exits.
	defer cancel()

	k8sClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to create Kubernetes client")
		os.Exit(1)
	}

	// Fetch the TLS profile from the APIServer resource.
	tlsProfileSpec, err := utiltls.FetchAPIServerTLSProfile(ctx, k8sClient)
	if err != nil {
		setupLog.Error(err, "unable to get TLS profile from API server")
		os.Exit(1)
	}

	// Create the TLS configuration function for the server endpoints.
	tlsConfigFunc, unsupportedCiphers := utiltls.NewTLSConfigFromProfile(tlsProfileSpec)
	if len(unsupportedCiphers) > 0 {
		setupLog.Info("Some ciphers from TLS profile are not supported", "unsupportedCiphers", unsupportedCiphers)
	}
	tlsOpts := []func(*tls.Config){tlsConfigFunc}

	syncPeriod := 10 * time.Minute
	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:    *metricsAddr,
			FilterProvider: filters.WithAuthenticationAndAuthorization,
			SecureServing:  true,
			TLSOpts:        tlsOpts,
		},
		Cache: cache.Options{
			// For roles/rolebindings specifically, we need to also watch kube-system.
			ByObject: map[client.Object]cache.ByObject{
				&rbacv1.Role{}: {
					Namespaces: map[string]cache.Config{
						kubeSystemNamespace: {},
						*managedNamespace:   {},
					},
				},
				&rbacv1.RoleBinding{}: {
					Namespaces: map[string]cache.Config{
						kubeSystemNamespace: {},
						*managedNamespace:   {},
					},
				},
			},
			SyncPeriod: &syncPeriod,
			DefaultNamespaces: map[string]cache.Config{
				*managedNamespace: {},
			},
		},
		WebhookServer: &webhook.DefaultServer{
			Options: webhook.Options{
				Port:    *webhookPort,
				TLSOpts: tlsOpts,
			},
		},
		HealthProbeBindAddress:        *healthAddr,
		LeaderElectionReleaseOnCancel: true,
		LeaderElectionNamespace:       leaderElectionConfig.ResourceNamespace,
		LeaderElection:                leaderElectionConfig.LeaderElect,
		LeaderElectionID:              leaderElectionConfig.ResourceName,
		LeaseDuration:                 &le.LeaseDuration.Duration,
		RetryPeriod:                   &le.RetryPeriod.Duration,
		RenewDeadline:                 &le.RenewDeadline.Duration,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Setup for the feature gate accessor. This reads and monitors feature gates
	// from the FeatureGate object status for the given version.
	desiredVersion := controllers.GetReleaseVersion()
	missingVersion := "0.0.1-snapshot"

	configClient, err := configv1client.NewForConfig(mgr.GetConfig())
	if err != nil {
		setupLog.Error(err, "unable to create config client")
		os.Exit(1)
	}
	configInformers := configinformers.NewSharedInformerFactory(configClient, 10*time.Minute)

	kubeClient, err := kubernetes.NewForConfig(mgr.GetConfig())
	if err != nil {
		setupLog.Error(err, "unable to create kube client")
		os.Exit(1)
	}

	controllerRef, err := events.GetControllerReferenceForCurrentPod(ctx, kubeClient, *managedNamespace, nil)
	if err != nil {
		klog.Warningf("unable to get owner reference (falling back to namespace): %v", err)
	}
	mgrClock := clock.RealClock{}
	recorder := events.NewKubeRecorder(kubeClient.CoreV1().Events(*managedNamespace), "cloud-controller-manager-operator", controllerRef, mgrClock)
	featureGateAccessor := featuregates.NewFeatureGateAccess(
		desiredVersion, missingVersion,
		configInformers.Config().V1().ClusterVersions(), configInformers.Config().V1().FeatureGates(),
		recorder,
	)

	featureGateAccessor.SetChangeHandler(func(featureChange featuregates.FeatureChange) {
		// Do nothing here. The controller watches feature gate changes and will react to them.
		klog.InfoS("FeatureGates changed", "enabled", featureChange.New.Enabled, "disabled", featureChange.New.Disabled)
	})

	go featureGateAccessor.Run(ctx)
	go configInformers.Start(ctx.Done())

	select {
	case <-featureGateAccessor.InitialFeatureGatesObserved():
		features, _ := featureGateAccessor.CurrentFeatureGates()

		enabled, disabled := util.GetEnabledDisabledFeatures(features, nil)
		setupLog.Info("FeatureGates initialized", "enabled", enabled, "disabled", disabled)
	case <-time.After(1 * time.Minute):
		setupLog.Error(errors.New("timed out waiting for FeatureGate detection"), "unable to start manager")
	}

	if err = (&controllers.CloudOperatorReconciler{
		ClusterOperatorStatusClient: controllers.ClusterOperatorStatusClient{
			Client:           mgr.GetClient(),
			Recorder:         mgr.GetEventRecorderFor("cloud-controller-manager-operator"), //nolint:staticcheck // manager expects legacy recorder interface here
			Clock:            mgrClock,
			ReleaseVersion:   controllers.GetReleaseVersion(),
			ManagedNamespace: *managedNamespace,
		},
		Scheme:            mgr.GetScheme(),
		ImagesFile:        *imagesFile,
		FeatureGateAccess: featureGateAccessor,
		TLSProfileSpec:    tlsProfileSpec,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ClusterOperator")
		os.Exit(1)
	}

	// Set up the TLS security profile watcher to watch for TLS config changes
	if err = (&utiltls.SecurityProfileWatcher{
		Client:                mgr.GetClient(),
		InitialTLSProfileSpec: tlsProfileSpec,
		OnProfileChange: func(ctx context.Context, oldTLSProfileSpec, newTLSProfileSpec configv1.TLSProfileSpec) {
			klog.Infof("TLS profile has changed, initiating a shutdown to reload it. %q: %+v, %q: %+v",
				"old profile", oldTLSProfileSpec,
				"new profile", newTLSProfileSpec,
			)
			cancel()
		},
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "TLSSecurityProfileWatcher")
		os.Exit(1)
	}

	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("health", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("check", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
