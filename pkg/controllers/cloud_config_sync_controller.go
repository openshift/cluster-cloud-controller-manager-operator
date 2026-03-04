package controllers

import (
	"context"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud"
)

const (
	managedCloudConfigMapName = "kube-cloud-config"

	defaultConfigKey = "cloud.conf"

	// Controller conditions for the Cluster Operator resource
	cloudConfigControllerAvailableCondition = "CloudConfigControllerAvailable"
	cloudConfigControllerDegradedCondition  = "CloudConfigControllerDegraded"

	// transientDegradedThreshold is how long transient errors must persist before
	// the controller sets Degraded=True. This prevents brief
	// API server blips during upgrades from immediately degrading the operator.
	// Applies to both CloudConfigController and TrustedCAController.
	transientDegradedThreshold = 2 * time.Minute
)

type CloudConfigReconciler struct {
	ClusterOperatorStatusClient
	Scheme                  *runtime.Scheme
	FeatureGateAccess       featuregates.FeatureGateAccess
	consecutiveFailureSince *time.Time // nil when the last reconcile succeeded
}

func (r *CloudConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.V(1).Infof("Syncing cloud-conf ConfigMap")

	infra := &configv1.Infrastructure{}
	if err := r.Get(ctx, client.ObjectKey{Name: infrastructureResourceName}, infra); errors.IsNotFound(err) {
		// No cloud platform: mirror the main controller's behaviour of returning Available.
		klog.Infof("Infrastructure cluster does not exist. Skipping...")
		if err := r.setAvailableCondition(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
		}
		// Skip if the infrastructure resource doesn't exist.
		r.clearFailureWindow()
		return ctrl.Result{}, nil
	} else if err != nil {
		return r.handleTransientError(ctx, err)
	}

	network := &configv1.Network{}
	if err := r.Get(ctx, client.ObjectKey{Name: "cluster"}, network); err != nil {
		return r.handleTransientError(ctx, err)
	}

	syncNeeded, err := r.isCloudConfigSyncNeeded(infra.Status.PlatformStatus, infra.Spec.CloudConfig)
	if err != nil {
		// nil platformStatus is a permanent misconfiguration.
		return r.handleDegradeError(ctx, err)
	}
	if !syncNeeded {
		r.clearFailureWindow()
		if err := r.setAvailableCondition(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
		}
		klog.Infof("cloud-config sync is not needed, returning early")
		return ctrl.Result{}, nil
	}

	cloudConfigTransformerFn, needsManagedConfigLookup, err := cloud.GetCloudConfigTransformer(infra.Status.PlatformStatus)
	if err != nil {
		// Unsupported platform won't change without a cluster reconfigure.
		klog.Errorf("unable to get cloud config transformer function; unsupported platform")
		return r.handleDegradeError(ctx, err)
	}

	sourceCM := &corev1.ConfigMap{}
	managedConfigFound := false

	// NOTE: We know that there is some transformation logic in place in the
	// Cluster Config Operator (CCO) for AWS and Azure. We have not implemented
	// this logic here yet so we've intentionally chosen to lookup up config
	// from the (CCO-) managed namespace **only for these cloud platforms**
	// TODO: Drop this once we implement the AWS and Azure transformers here in
	// CCCMO, allowing us to drop this kinda-sorta reliance on CCO stuff. We
	// may also wish to merge the use of cloudConfigTransformerFn into the
	// prepareSourceConfigMap helper function
	if needsManagedConfigLookup {
		defaultSourceCMObjectKey := client.ObjectKey{
			Name:      managedCloudConfigMapName,
			Namespace: OpenshiftManagedConfigNamespace,
		}
		if err := r.Get(ctx, defaultSourceCMObjectKey, sourceCM); err == nil {
			managedConfigFound = true
		} else if errors.IsNotFound(err) {
			klog.Warningf("managed cloud-config is not found, falling back to infrastructure config")
		} else {
			return r.handleTransientError(ctx, err)
		}
	}

	// Only look for an unmanaged config if the managed one isn't found and a name was specified.
	if !managedConfigFound && infra.Spec.CloudConfig.Name != "" {
		openshiftUnmanagedCMKey := client.ObjectKey{
			Name:      infra.Spec.CloudConfig.Name,
			Namespace: OpenshiftConfigNamespace,
		}
		if err := r.Get(ctx, openshiftUnmanagedCMKey, sourceCM); errors.IsNotFound(err) {
			klog.Warningf("managed cloud-config is not found, falling back to default cloud config.")
		} else if err != nil {
			return r.handleTransientError(ctx, err)
		}
	}

	sourceCM, err = r.prepareSourceConfigMap(sourceCM, infra)
	if err != nil {
		// User-supplied key mismatch: permanent until the ConfigMap or Infrastructure changes.
		return r.handleDegradeError(ctx, err)
	}

	if r.FeatureGateAccess == nil {
		// Operator misconfiguration at startup: permanent.
		return r.handleDegradeError(ctx, fmt.Errorf("FeatureGateAccess is not configured"))
	}

	features, err := r.FeatureGateAccess.CurrentFeatureGates()
	if err != nil {
		// The feature-gate informer may not have synced yet: transient.
		klog.Errorf("unable to get feature gates: %v", err)
		return r.handleTransientError(ctx, err)
	}

	if cloudConfigTransformerFn != nil {
		// We ignore stuff in sourceCM.BinaryData. This isn't allowed to
		// contain any key that overlaps with those found in sourceCM.Data and
		// we're not expecting users to put their data in the former.
		output, err := cloudConfigTransformerFn(sourceCM.Data[defaultConfigKey], infra, network, features)
		if err != nil {
			// Platform-specific transform failed on the current config data: permanent.
			return r.handleDegradeError(ctx, err)
		}
		sourceCM.Data[defaultConfigKey] = output
	}

	targetCM := &corev1.ConfigMap{}
	targetConfigMapKey := client.ObjectKey{
		Namespace: r.ManagedNamespace,
		Name:      syncedCloudConfigMapName,
	}

	// If the config does not exist, it will be created later, so we can ignore a Not Found error
	if err := r.Get(ctx, targetConfigMapKey, targetCM); err != nil && !errors.IsNotFound(err) {
		return r.handleTransientError(ctx, err)
	}

	// Note that the source config map is actually a *transformed* source config map
	if r.isCloudConfigEqual(sourceCM, targetCM) {
		klog.V(1).Infof("source and target cloud-config content are equal, no sync needed")
		r.clearFailureWindow()
		if err := r.setAvailableCondition(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
		}
		return ctrl.Result{}, nil
	}

	if err := r.syncCloudConfigData(ctx, sourceCM, targetCM); err != nil {
		klog.Errorf("unable to sync cloud config")
		return r.handleTransientError(ctx, err)
	}

	r.clearFailureWindow()
	if err := r.setAvailableCondition(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
	}

	return ctrl.Result{}, nil
}

// clearFailureWindow resets the transient-error tracking. Call this on every
// successful reconcile so the 2-minute window restarts fresh on the next failure.
func (r *CloudConfigReconciler) clearFailureWindow() {
	r.consecutiveFailureSince = nil
}

// handleTransientError records the start of a failure window and degrades the
// controller only after transientDegradedThreshold has elapsed. It always
// returns a non-nil error so controller-runtime requeues with exponential backoff.
func (r *CloudConfigReconciler) handleTransientError(ctx context.Context, err error) (ctrl.Result, error) {
	now := r.Clock.Now()
	if r.consecutiveFailureSince == nil {
		r.consecutiveFailureSince = &now
		klog.V(4).Infof("CloudConfigReconciler: transient failure started (%v), will degrade after %s", err, transientDegradedThreshold)
		return ctrl.Result{}, err
	}
	elapsed := r.Clock.Now().Sub(*r.consecutiveFailureSince)
	if elapsed < transientDegradedThreshold {
		klog.V(4).Infof("CloudConfigReconciler: transient failure ongoing for %s (threshold %s): %v", elapsed, transientDegradedThreshold, err)
		return ctrl.Result{}, err
	}
	klog.Warningf("CloudConfigReconciler: transient failure exceeded threshold (%s), setting degraded: %v", elapsed, err)
	if setErr := r.setDegradedCondition(ctx); setErr != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set degraded condition: %v", setErr)
	}
	return ctrl.Result{}, err
}

// handleDegradeError sets CloudConfigControllerDegraded=True immediately and
// returns nil so controller-runtime does NOT requeue. An existing watch on the
// relevant resource will re-trigger reconciliation when the problem is fixed.
func (r *CloudConfigReconciler) handleDegradeError(ctx context.Context, err error) (ctrl.Result, error) {
	klog.Errorf("CloudConfigReconciler: permanent error, setting degraded: %v", err)
	if setErr := r.setDegradedCondition(ctx); setErr != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set degraded condition: %v", setErr)
	}
	return ctrl.Result{}, nil
}

func (r *CloudConfigReconciler) isCloudConfigSyncNeeded(platformStatus *configv1.PlatformStatus, infraCloudConfigRef configv1.ConfigMapFileReference) (bool, error) {
	if platformStatus == nil {
		return false, fmt.Errorf("platformStatus is required")
	}
	switch platformStatus.Type {
	case configv1.AWSPlatformType,
		configv1.AzurePlatformType,
		configv1.GCPPlatformType,
		configv1.VSpherePlatformType,
		configv1.IBMCloudPlatformType,
		configv1.PowerVSPlatformType,
		configv1.OpenStackPlatformType,
		configv1.NutanixPlatformType:
		return true, nil
	default:
		return false, nil
	}
}

// prepareSourceConfigMap creates a usable ConfigMap for further processing into a cloud.conf file.
func (r *CloudConfigReconciler) prepareSourceConfigMap(source *corev1.ConfigMap, infra *configv1.Infrastructure) (*corev1.ConfigMap, error) {
	if source == nil {
		return nil, fmt.Errorf("received empty configmap for cloud config")
	}
	cloudConfCm := source.DeepCopy()
	// We might have an empty ConfigMap in clusters created before 4.14.
	if cloudConfCm.Data == nil {
		cloudConfCm.Data = make(map[string]string)
	}

	// Keys might be different between openshift-config/cloud-config and openshift-config-managed/kube-cloud-config
	// Always use "cloud.conf" which is default one across openshift
	if _, ok := cloudConfCm.Data[defaultConfigKey]; ok {
		return cloudConfCm, nil
	} else {
		// Make an entry for the default key even if it didn't exist.
		cloudConfCm.Data[defaultConfigKey] = ""
	}

	// If a user provides their own cloud config...
	infraConfigKey := infra.Spec.CloudConfig.Key
	if infraConfigKey != "" {
		if val, ok := cloudConfCm.Data[infraConfigKey]; ok {
			// ..., copy that over into the default key.
			cloudConfCm.Data[defaultConfigKey] = val
			delete(cloudConfCm.Data, infraConfigKey)
			return cloudConfCm, nil
		} else if !ok {
			// Return an error if they provided a non-existent one and there was a cloud.conf specified.
			return nil, fmt.Errorf("key %s specified in infra resource does not exist in source configmap %s",
				infraConfigKey, client.ObjectKeyFromObject(source),
			)
		}
	}

	return cloudConfCm, nil
}

func (r *CloudConfigReconciler) isCloudConfigEqual(source *corev1.ConfigMap, target *corev1.ConfigMap) bool {
	return source.Immutable == target.Immutable &&
		reflect.DeepEqual(source.Data, target.Data) && reflect.DeepEqual(source.BinaryData, target.BinaryData)
}

func (r *CloudConfigReconciler) syncCloudConfigData(ctx context.Context, source *corev1.ConfigMap, target *corev1.ConfigMap) error {
	target.SetName(syncedCloudConfigMapName)
	target.SetNamespace(r.ManagedNamespace)
	target.Data = source.Data
	target.BinaryData = source.BinaryData
	target.Immutable = source.Immutable

	// check if target config exists, create if not
	err := r.Get(ctx, client.ObjectKeyFromObject(target), &corev1.ConfigMap{})

	if err != nil && errors.IsNotFound(err) {
		return r.Create(ctx, target)
	} else if err != nil {
		return err
	}

	return r.Update(ctx, target)
}

// SetupWithManager sets up the controller with the Manager.
func (r *CloudConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	build := ctrl.NewControllerManagedBy(mgr).
		Named("CloudConfigSyncController").
		For(
			&corev1.ConfigMap{},
			builder.WithPredicates(
				predicate.Or(
					ownCloudConfigPredicate(r.ManagedNamespace),
					openshiftCloudConfigMapPredicates(),
				),
			),
		).
		Watches(
			&configv1.Infrastructure{},
			handler.EnqueueRequestsFromMapFunc(toManagedConfigMap),
			builder.WithPredicates(infrastructurePredicates()),
		).
		Watches(
			&configv1.Network{},
			handler.EnqueueRequestsFromMapFunc(toManagedConfigMap),
		)

	return build.Complete(r)
}

func (r *CloudConfigReconciler) setAvailableCondition(ctx context.Context) error {
	co, err := r.getOrCreateClusterOperator(ctx)
	if err != nil {
		return err
	}

	conds := []configv1.ClusterOperatorStatusCondition{
		newClusterOperatorStatusCondition(cloudConfigControllerAvailableCondition, configv1.ConditionTrue, ReasonAsExpected,
			"Cloud Config Controller works as expected"),
		newClusterOperatorStatusCondition(cloudConfigControllerDegradedCondition, configv1.ConditionFalse, ReasonAsExpected,
			"Cloud Config Controller works as expected"),
	}

	co.Status.Versions = []configv1.OperandVersion{{Name: operatorVersionKey, Version: r.ReleaseVersion}}
	klog.V(1).Info("Cloud Config Controller is available")
	return r.syncStatus(ctx, co, conds, nil)
}

func (r *CloudConfigReconciler) setDegradedCondition(ctx context.Context) error {
	co, err := r.getOrCreateClusterOperator(ctx)
	if err != nil {
		return err
	}

	conds := []configv1.ClusterOperatorStatusCondition{
		newClusterOperatorStatusCondition(cloudConfigControllerAvailableCondition, configv1.ConditionFalse, ReasonSyncFailed,
			"Cloud Config Controller failed to sync cloud config"),
		newClusterOperatorStatusCondition(cloudConfigControllerDegradedCondition, configv1.ConditionTrue, ReasonSyncFailed,
			"Cloud Config Controller failed to sync cloud config"),
	}

	co.Status.Versions = []configv1.OperandVersion{{Name: operatorVersionKey, Version: r.ReleaseVersion}}
	klog.Info("Cloud Config Controller is degraded")
	return r.syncStatus(ctx, co, conds, nil)
}
