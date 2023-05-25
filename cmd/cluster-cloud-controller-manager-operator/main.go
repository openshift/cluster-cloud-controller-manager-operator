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

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/component-base/config"
	"k8s.io/component-base/config/options"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/klogr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/openshift/library-go/pkg/operator/events"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/controllers"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/restmapper"
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
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(configv1.AddToScheme(scheme))
	utilruntime.Must(operatorv1.AddToScheme(scheme))

	// +kubebuilder:scaffold:scheme
}

func main() {
	flag.Set("logtostderr", "true") //nolint:errcheck
	klog.InitFlags(nil)

	metricsAddr := flag.String(
		"metrics-bind-address",
		":8080",
		"Address for hosting metrics",
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

	ctrl.SetLogger(klogr.New().WithName("CCMOperator"))

	restConfig := ctrl.GetConfigOrDie()
	le := util.GetLeaderElectionDefaults(restConfig, configv1.LeaderElection{
		Disable:       !leaderElectionConfig.LeaderElect,
		RenewDeadline: leaderElectionConfig.RenewDeadline,
		RetryPeriod:   leaderElectionConfig.RetryPeriod,
		LeaseDuration: leaderElectionConfig.LeaseDuration,
	})

	ctx := ctrl.SetupSignalHandler()

	syncPeriod := 10 * time.Minute
	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Namespace:          *managedNamespace,
		Scheme:             scheme,
		SyncPeriod:         &syncPeriod,
		MetricsBindAddress: *metricsAddr,
		Port:               9443,
		MapperProvider: restmapper.NewPartialRestMapperProvider(
			restmapper.Or(
				restmapper.KubernetesCoreGroup, restmapper.KubernetesAppsGroup, restmapper.KubernetesPolicyGroup,
				restmapper.OpenshiftOperatorGroup, restmapper.OpenshiftConfigGroup,
			),
		),
		HealthProbeBindAddress:  *healthAddr,
		LeaderElectionNamespace: leaderElectionConfig.ResourceNamespace,
		LeaderElection:          leaderElectionConfig.LeaderElect,
		LeaderElectionID:        leaderElectionConfig.ResourceName,
		LeaseDuration:           &le.LeaseDuration.Duration,
		RetryPeriod:             &le.RetryPeriod.Duration,
		RenewDeadline:           &le.RenewDeadline.Duration,
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

	recorder := events.NewKubeRecorder(kubeClient.CoreV1().Events(*managedNamespace), "cloud-controller-manager-operator", controllerRef)
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
			Recorder:         mgr.GetEventRecorderFor("cloud-controller-manager-operator"),
			ReleaseVersion:   controllers.GetReleaseVersion(),
			ManagedNamespace: *managedNamespace,
		},
		Scheme:            mgr.GetScheme(),
		ImagesFile:        *imagesFile,
		FeatureGateAccess: featureGateAccessor,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ClusterOperator")
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
