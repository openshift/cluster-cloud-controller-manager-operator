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
	"flag"
	"os"
	"time"

	"github.com/spf13/pflag"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/component-base/config"
	"k8s.io/component-base/config/options"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/klogr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	configv1 "github.com/openshift/api/config/v1"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/controllers"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/util"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	leaderElectionConfig = config.LeaderElectionConfiguration{
		LeaderElect:   true,
		LeaseDuration: util.LeaseDuration,
		RenewDeadline: util.RenewDeadline,
		RetryPeriod:   util.RetryPeriod,
		ResourceName:  "cluster-cloud-config-sync-leader",
	}
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(configv1.AddToScheme(scheme))

	// +kubebuilder:scaffold:scheme
}

func main() {
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
		"The namespace for managed objects, target cloud-conf in particular.",
	)

	// Once all the flags are regitered, switch to pflag
	// to allow leader lection flags to be bound
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	options.BindLeaderElectionFlags(&leaderElectionConfig, pflag.CommandLine)
	pflag.Parse()

	ctrl.SetLogger(klogr.New().WithName("CCCMOConfigSyncControllers"))

	syncPeriod := 10 * time.Minute
	cacheBuilder := cache.MultiNamespacedCacheBuilder([]string{
		*managedNamespace, controllers.OpenshiftConfigNamespace, controllers.OpenshiftManagedConfigNamespace,
	})
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Namespace:               *managedNamespace,
		Scheme:                  scheme,
		SyncPeriod:              &syncPeriod,
		MetricsBindAddress:      *metricsAddr,
		HealthProbeBindAddress:  *healthAddr,
		LeaderElectionNamespace: leaderElectionConfig.ResourceNamespace,
		LeaderElection:          leaderElectionConfig.LeaderElect,
		LeaseDuration:           &leaderElectionConfig.LeaseDuration.Duration,
		LeaderElectionID:        leaderElectionConfig.ResourceName,
		RetryPeriod:             &leaderElectionConfig.RetryPeriod.Duration,
		RenewDeadline:           &leaderElectionConfig.RenewDeadline.Duration,
		NewCache:                cacheBuilder,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controllers.CloudConfigReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		Recorder:        mgr.GetEventRecorderFor("cloud-controller-manager-operator-cloud-config-sync-controller"),
		TargetNamespace: *managedNamespace,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create cloud-config sync controller", "controller", "ClusterOperator")
		os.Exit(1)
	}

	if err = (&controllers.TrustedCABundleReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		Recorder:        mgr.GetEventRecorderFor("cloud-controller-manager-operator-ca-sync-controller"),
		TargetNamespace: *managedNamespace,
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
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
