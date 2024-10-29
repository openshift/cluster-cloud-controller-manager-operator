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
	"errors"
	"flag"
	"os"
	"time"

	"github.com/spf13/pflag"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	configv1client "github.com/openshift/client-go/config/clientset/versioned"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/openshift/library-go/pkg/operator/events"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/component-base/config"
	"k8s.io/component-base/config/options"
	"k8s.io/klog/v2/textlogger"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	configv1 "github.com/openshift/api/config/v1"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/controllers"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/restmapper"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/util"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	leaderElectionConfig = config.LeaderElectionConfiguration{
		LeaderElect:  true,
		ResourceName: "cluster-cloud-config-sync-leader",
	}
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(configv1.AddToScheme(scheme))

	// +kubebuilder:scaffold:scheme
}

func main() {
	textLoggerCfg := textlogger.NewConfig()
	textLoggerCfg.AddFlags(flag.CommandLine)

	healthAddr := flag.String(
		"health-addr",
		":9440",
		"The address for health checking.",
	)

	managedNamespace := flag.String(
		"namespace",
		controllers.DefaultManagedNamespace,
		"The namespace for managed objects, target cloud-conf in particular.",
	)

	// Once all the flags are regitered, switch to pflag
	// to allow leader lection flags to be bound
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	options.BindLeaderElectionFlags(&leaderElectionConfig, pflag.CommandLine)
	pflag.Parse()

	ctrl.SetLogger(textlogger.NewLogger(textLoggerCfg).WithName("CCCMOConfigSyncControllers"))

	restConfig := ctrl.GetConfigOrDie()
	le := util.GetLeaderElectionDefaults(restConfig, configv1.LeaderElection{
		Disable:       !leaderElectionConfig.LeaderElect,
		RenewDeadline: leaderElectionConfig.RenewDeadline,
		RetryPeriod:   leaderElectionConfig.RetryPeriod,
		LeaseDuration: leaderElectionConfig.LeaseDuration,
	})

	syncPeriod := 10 * time.Minute

	cacheOptions := cache.Options{
		SyncPeriod: &syncPeriod,
		DefaultNamespaces: map[string]cache.Config{
			*managedNamespace:                           {},
			controllers.OpenshiftConfigNamespace:        {},
			controllers.OpenshiftManagedConfigNamespace: {}},
	}

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{ // we do not expose any metric at this point
			BindAddress: "0",
		},
		HealthProbeBindAddress: *healthAddr,
		MapperProvider: restmapper.NewPartialRestMapperProvider(
			restmapper.Or(
				restmapper.KubernetesCoreGroup,
				restmapper.OpenshiftOperatorGroup,
				restmapper.OpenshiftConfigGroup,
			),
		),
		LeaderElectionNamespace: leaderElectionConfig.ResourceNamespace,
		LeaderElection:          leaderElectionConfig.LeaderElect,
		LeaderElectionID:        leaderElectionConfig.ResourceName,
		LeaseDuration:           &le.LeaseDuration.Duration,
		RetryPeriod:             &le.RetryPeriod.Duration,
		RenewDeadline:           &le.RenewDeadline.Duration,
		Cache:                   cacheOptions,
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

	ctx := ctrl.SetupSignalHandler()
	controllerRef, err := events.GetControllerReferenceForCurrentPod(ctx, kubeClient, *managedNamespace, nil)
	if err != nil {
		setupLog.Error(err, "unable to get owner reference (falling back to namespace")
	}

	recorder := events.NewKubeRecorder(kubeClient.CoreV1().Events(*managedNamespace), "config-sync-controller", controllerRef)
	featureGateAccessor := featuregates.NewFeatureGateAccess(
		desiredVersion, missingVersion,
		configInformers.Config().V1().ClusterVersions(), configInformers.Config().V1().FeatureGates(),
		recorder,
	)

	featureGateAccessor.SetChangeHandler(func(featureChange featuregates.FeatureChange) {
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

	if err = (&controllers.CloudConfigReconciler{
		ClusterOperatorStatusClient: controllers.ClusterOperatorStatusClient{
			Client:           mgr.GetClient(),
			Recorder:         mgr.GetEventRecorderFor("cloud-controller-manager-operator-cloud-config-sync-controller"),
			ReleaseVersion:   controllers.GetReleaseVersion(),
			ManagedNamespace: *managedNamespace,
		},
		FeatureGateAccess: featureGateAccessor,
		Scheme:            mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create cloud-config sync controller", "controller", "ClusterOperator")
		os.Exit(1)
	}

	if err = (&controllers.TrustedCABundleReconciler{
		ClusterOperatorStatusClient: controllers.ClusterOperatorStatusClient{
			Client:           mgr.GetClient(),
			Recorder:         mgr.GetEventRecorderFor("cloud-controller-manager-operator-ca-sync-controller"),
			ReleaseVersion:   controllers.GetReleaseVersion(),
			ManagedNamespace: *managedNamespace,
		},
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create Trusted CA sync controller", "controller", "ClusterOperator")
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
