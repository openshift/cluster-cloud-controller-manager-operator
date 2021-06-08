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
	"flag"
	"fmt"
	"k8s.io/apimachinery/pkg/api/errors"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.

	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/klog/klogr"
	"k8s.io/klog/v2"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-cloud-controller-manager-operator/controllers"
	// +kubebuilder:scaffold:imports

	operatorconfig "github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	// The default durations for the leader electrion operations.
	leaseDuration = 120 * time.Second
	renewDealine  = 110 * time.Second
	retryPeriod   = 90 * time.Second
)

const (
	defaultManagedNamespace       = "openshift-cloud-controller-manager"
	defaultImagesLocation         = "/etc/cloud-controller-manager-config/images.json"
	releaseVersionEnvVariableName = "RELEASE_VERSION"
	unknownVersionValue           = "unknown"
	infrastructureName            = "cluster"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(configv1.AddToScheme(scheme))

	// +kubebuilder:scaffold:scheme
}

func main() {
	flag.Set("logtostderr", "true")
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

	leaderElectResourceNamespace := flag.String(
		"leader-elect-resource-namespace",
		"",
		"The namespace of resource object that is used for locking during leader election. If unspecified and running in cluster, defaults to the service account namespace for the controller. Required for leader-election outside of a cluster.",
	)

	leaderElect := flag.Bool(
		"leader-elect",
		false,
		"Start a leader election client and gain leadership before executing the main loop. Enable this when running replicated components for high availability.",
	)

	leaderElectLeaseDuration := flag.Duration(
		"leader-elect-lease-duration",
		leaseDuration,
		"The duration that non-leader candidates will wait after observing a leadership renewal until attempting to acquire leadership of a led but unrenewed leader slot. This is effectively the maximum duration that a leader can be stopped before it is replaced by another candidate. This is only applicable if leader election is enabled.",
	)

	managedNamespace := flag.String(
		"namespace",
		defaultManagedNamespace,
		"The namespace for managed objects, where out-of-tree CCM binaries will run.",
	)

	imagesFile := flag.String(
		"images-json",
		defaultImagesLocation,
		"The location of images file to use by operator for managed CCM binaries.",
	)

	flag.Parse()

	ctrl.SetLogger(klogr.New().WithName("CCMOperator"))

	syncPeriod := 10 * time.Minute
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Namespace:               *managedNamespace,
		Scheme:                  scheme,
		SyncPeriod:              &syncPeriod,
		MetricsBindAddress:      *metricsAddr,
		Port:                    9443,
		HealthProbeBindAddress:  *healthAddr,
		LeaderElectionNamespace: *leaderElectResourceNamespace,
		LeaderElection:          *leaderElect,
		LeaseDuration:           leaderElectLeaseDuration,
		LeaderElectionID:        "cluster-cloud-controller-manager-leader",
		RetryPeriod:             &retryPeriod,
		RenewDeadline:           &renewDealine,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	platformType, err := getPlatformType(mgr.GetAPIReader())
	if err != nil {
		setupLog.Error(err, "unable to get platform type from infrastructure resource")
		os.Exit(1)
	}

	operatorConfig, err := operatorconfig.ComposeConfig(platformType, *imagesFile, *managedNamespace)
	if err != nil {
		setupLog.Error(err, "can not compose operator config")
		os.Exit(1)
	}

	if err = (&controllers.CloudOperatorReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		Recorder:       mgr.GetEventRecorderFor("cloud-controller-manager-operator"),
		ReleaseVersion: getReleaseVersion(),
		OperatorConfig: operatorConfig,
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
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func getPlatformType(cl client.Reader) (configv1.PlatformType, error) {
	infra := &configv1.Infrastructure{}
	ctx := context.Background()

	if err := cl.Get(ctx, client.ObjectKey{Name: infrastructureName}, infra); errors.IsNotFound(err) {
		return "", fmt.Errorf("Infrastructure resources does not exist. Can not obtain platform type.")
	} else if err != nil {
		return "", err
	}
	return operatorconfig.GetProviderFromInfrastructure(infra)
}

func getReleaseVersion() string {
	releaseVersion := os.Getenv(releaseVersionEnvVariableName)
	if len(releaseVersion) == 0 {
		releaseVersion = unknownVersionValue
		klog.Infof("%s environment variable is missing, defaulting to %q", releaseVersionEnvVariableName, unknownVersionValue)
	}
	return releaseVersion
}
