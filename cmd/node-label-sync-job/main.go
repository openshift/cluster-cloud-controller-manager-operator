/*
Copyright 2026.

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

// Command node-label-sync-job runs to completion once, backfilling the
// node.openshift.io/platform-type=vsphere label onto vSphere nodes that are missing it. The
// operator's own NodeLabelSyncJobReconciler (see
// pkg/controllers/node_label_sync_job_controller.go) creates this as a Kubernetes Job on demand,
// rather than running it as an ongoing in-process controller.
package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	klog "k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	configv1client "github.com/openshift/client-go/config/clientset/versioned"
	configinformers "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/openshift/library-go/pkg/operator/events"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/controllers"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

const (
	recorderName           = "cloud-controller-manager-operator-node-label-sync-job"
	missingVersion         = "0.0.1-snapshot"
	managedNamespace       = controllers.DefaultManagedNamespace
	featureGateWaitTimeout = 1 * time.Minute
	// jobTimeout bounds the process's overall runtime safely below the Job manifest's
	// activeDeadlineSeconds (5m), so it can log a clear error and exit cleanly instead of
	// being killed by the kubelet once the deadline is hit.
	jobTimeout = 4 * time.Minute
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(configv1.AddToScheme(scheme))
}

func main() {
	klog.InitFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(klog.NewKlogr().WithName("VSphereNodeLabelSyncJob"))

	ctx, cancel := context.WithTimeout(ctrl.SetupSignalHandler(), jobTimeout)
	defer cancel()

	restConfig := ctrl.GetConfigOrDie()

	k8sClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to create client")
		os.Exit(1)
	}

	configClient, err := configv1client.NewForConfig(restConfig)
	if err != nil {
		setupLog.Error(err, "unable to create config client")
		os.Exit(1)
	}

	kubeClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		setupLog.Error(err, "unable to create kube client")
		os.Exit(1)
	}

	configInformers := configinformers.NewSharedInformerFactory(configClient, 10*time.Minute)
	controllerRef, err := events.GetControllerReferenceForCurrentPod(ctx, kubeClient, managedNamespace, nil)
	if err != nil {
		klog.Warningf("unable to get owner reference (falling back to namespace): %v", err)
	}

	featureGateAccessor := featuregates.NewFeatureGateAccess(
		controllers.GetReleaseVersion(), missingVersion,
		configInformers.Config().V1().ClusterVersions(), configInformers.Config().V1().FeatureGates(),
		events.NewKubeRecorder(kubeClient.CoreV1().Events(managedNamespace), recorderName, controllerRef, clock.RealClock{}),
	)
	featureGateAccessor.SetChangeHandler(func(featuregates.FeatureChange) {})
	go featureGateAccessor.Run(ctx)
	go configInformers.Start(ctx.Done())

	select {
	case <-featureGateAccessor.InitialFeatureGatesObserved():
	case <-ctx.Done():
		setupLog.Error(ctx.Err(), "context canceled while waiting for FeatureGate detection")
		os.Exit(1)
	case <-time.After(featureGateWaitTimeout):
		setupLog.Error(errors.New("timed out waiting for FeatureGate detection"), "unable to determine feature gate state")
		os.Exit(1)
	}

	currentFeatureGates, err := featureGateAccessor.CurrentFeatureGates()
	if err != nil {
		setupLog.Error(err, "failed to get current feature gates")
		os.Exit(1)
	}
	if !currentFeatureGates.Enabled(features.FeatureGateVSphereMixedNodeEnv) {
		setupLog.Info("VSphereMixedNodeEnv feature gate is disabled, nothing to do")
		return
	}

	if err := controllers.SyncVSphereNodeLabels(ctx, k8sClient); err != nil {
		setupLog.Error(err, "failed to sync vSphere node labels")
		os.Exit(1)
	}

	setupLog.Info("vSphere node label sync completed successfully")
}
