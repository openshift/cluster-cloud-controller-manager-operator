package controllers

import (
	"context"
	"fmt"
	"reflect"

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
	"github.com/openshift/api/features"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud"
)

const (
	managedCloudConfigMapName = "kube-cloud-config"

	defaultConfigKey = "cloud.conf"

	// Controller conditions for the Cluster Operator resource
	cloudConfigControllerAvailableCondition = "CloudConfigControllerAvailable"
	cloudConfigControllerDegradedCondition  = "CloudConfigControllerDegraded"
)

// isFeatureGateEnabled checks if a feature gate is enabled.
// Returns false if the feature gate is nil.
// This provides a nil-safe way to check feature gates.
func isFeatureGateEnabled(featureGates featuregates.FeatureGate, featureName configv1.FeatureGateName) bool {
	if featureGates == nil {
		return false
	}
	return featureGates.Enabled(featureName)
}

// shouldManageManagedConfigMap returns true if CCCMO should manage the
// openshift-config-managed/kube-cloud-config ConfigMap for the given platform.
// This indicates ownership has been migrated from CCO to CCCMO.
//
// For vSphere, this requires the VSphereMultiVCenterDay2 feature gate to be enabled.
func shouldManageManagedConfigMap(platformType configv1.PlatformType, featureGates featuregates.FeatureGate) bool {
	switch platformType {
	case configv1.VSpherePlatformType:
		// Only manage the configmap if the feature gate is enabled
		return isFeatureGateEnabled(featureGates, features.FeatureGateVSphereMultiVCenterDay2)
	// Future: Add other platforms as they migrate from CCO
	// case configv1.AWSPlatformType:
	//     return true
	// case configv1.AzurePlatformType:
	//     return true
	default:
		return false
	}
}

type CloudConfigReconciler struct {
	ClusterOperatorStatusClient
	Scheme            *runtime.Scheme
	FeatureGateAccess featuregates.FeatureGateAccess
}

func (r *CloudConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.V(1).Infof("Syncing cloud-conf ConfigMap")

	infra := &configv1.Infrastructure{}
	if err := r.Get(ctx, client.ObjectKey{Name: infrastructureResourceName}, infra); err != nil {
		klog.Errorf("infrastructure resource not found")
		if err := r.setDegradedCondition(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
		}
		return ctrl.Result{}, err
	}

	network := &configv1.Network{}
	if err := r.Get(ctx, client.ObjectKey{Name: "cluster"}, network); err != nil {
		if err := r.setDegradedCondition(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller when getting cluster Network object: %v", err)
		}
		return ctrl.Result{}, err
	}

	syncNeeded, err := r.isCloudConfigSyncNeeded(infra.Status.PlatformStatus, infra.Spec.CloudConfig)
	if err != nil {
		if err := r.setDegradedCondition(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
		}
		return ctrl.Result{}, err
	}
	if !syncNeeded {
		if err := r.setAvailableCondition(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
		}
		klog.Infof("cloud-config sync is not needed, returning early")
		return ctrl.Result{}, nil
	}

	// Check if FeatureGateAccess is configured (needed early for transformer)
	if r.FeatureGateAccess == nil {
		klog.Errorf("FeatureGateAccess is not configured")
		if err := r.setDegradedCondition(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
		}
		return ctrl.Result{}, fmt.Errorf("FeatureGateAccess is not configured")
	}

	features, err := r.FeatureGateAccess.CurrentFeatureGates()
	if err != nil {
		klog.Errorf("unable to get feature gates: %v", err)
		if errD := r.setDegradedCondition(ctx); errD != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", errD)
		}
		return ctrl.Result{}, err
	}

	cloudConfigTransformerFn, needsManagedConfigLookup, err := cloud.GetCloudConfigTransformer(infra.Status.PlatformStatus)
	if err != nil {
		klog.Errorf("unable to get cloud config transformer function; unsupported platform")
		if err := r.setDegradedCondition(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
		}
		return ctrl.Result{}, err
	}

	platformType := infra.Status.PlatformStatus.Type
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
		} else if err != nil {
			klog.Errorf("unable to get managed cloud-config for sync")
			if err := r.setDegradedCondition(ctx); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
			}
			return ctrl.Result{}, err
		}
	}

	// Fallback: Look for config in openshift-config namespace if not found in managed namespace
	// For platforms we manage (e.g., vSphere), we'll use this as the source to populate openshift-config-managed
	if !managedConfigFound && infra.Spec.CloudConfig.Name != "" {
		openshiftUnmanagedCMKey := client.ObjectKey{
			Name:      infra.Spec.CloudConfig.Name,
			Namespace: OpenshiftConfigNamespace,
		}
		if err := r.Get(ctx, openshiftUnmanagedCMKey, sourceCM); errors.IsNotFound(err) {
			klog.Warningf("cloud-config not found in either openshift-config-managed or openshift-config namespace")
			// For platforms we manage, create an empty source that will be populated by the transformer
			if shouldManageManagedConfigMap(platformType, features) {
				klog.Infof("Initializing empty config for platform %s", platformType)
				sourceCM.Data = map[string]string{defaultConfigKey: ""}
			}
		} else if err != nil {
			klog.Errorf("unable to get cloud-config for sync: %v", err)
			if err := r.setDegradedCondition(ctx); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
			}
			return ctrl.Result{}, err
		} else {
			klog.V(3).Infof("Found config in openshift-config namespace for platform %s", platformType)
		}
	}

	sourceCM, err = r.prepareSourceConfigMap(sourceCM, infra)
	if err != nil {
		if err := r.setDegradedCondition(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
		}
		return ctrl.Result{}, err
	}

	// Apply transformer if needed
	if cloudConfigTransformerFn != nil {
		// We ignore stuff in sourceCM.BinaryData. This isn't allowed to
		// contain any key that overlaps with those found in sourceCM.Data and
		// we're not expecting users to put their data in the former.
		output, err := cloudConfigTransformerFn(sourceCM.Data[defaultConfigKey], infra, network, features)
		if err != nil {
			if err := r.setDegradedCondition(ctx); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
			}
			return ctrl.Result{}, err
		}
		sourceCM.Data[defaultConfigKey] = output
	}

	// For platforms managed by CCCMO, update openshift-config-managed/kube-cloud-config
	// with the transformed config so other operators can read from a consistent location
	if shouldManageManagedConfigMap(platformType, features) {
		if err := r.syncManagedCloudConfig(ctx, sourceCM); err != nil {
			klog.Errorf("failed to sync managed cloud config: %v", err)
			if err := r.setDegradedCondition(ctx); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
			}
			return ctrl.Result{}, err
		}
	}

	// Sync the transformed config to the target configmap for CCM consumption
	if err := r.syncCloudConfigData(ctx, sourceCM); err != nil {
		klog.Errorf("unable to sync cloud config")
		if err := r.setDegradedCondition(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
		}
		return ctrl.Result{}, err
	}

	if err := r.setAvailableCondition(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
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

// isCloudConfigEqual compares two ConfigMaps to determine if their content is equal.
// It performs a deep comparison of the Data, BinaryData, and Immutable fields.
//
// This function is used to avoid unnecessary updates when the cloud configuration
// content hasn't changed. Metadata fields (labels, annotations, resourceVersion, etc.)
// are intentionally ignored as they don't affect the actual configuration data.
//
// Returns true if both ConfigMaps have identical Data, BinaryData, and Immutable values.
func (r *CloudConfigReconciler) isCloudConfigEqual(source *corev1.ConfigMap, target *corev1.ConfigMap) bool {
	return source.Immutable == target.Immutable &&
		reflect.DeepEqual(source.Data, target.Data) && reflect.DeepEqual(source.BinaryData, target.BinaryData)
}

// syncConfigMapToTarget is a generic helper that syncs a source ConfigMap to a target namespace/name.
// It handles create-or-update logic with optional equality checking to avoid unnecessary updates.
func (r *CloudConfigReconciler) syncConfigMapToTarget(ctx context.Context, source *corev1.ConfigMap, targetName, targetNamespace string, checkEquality bool) error {
	if source == nil || source.Data == nil {
		return fmt.Errorf("source configmap is nil or has no data")
	}

	targetCM := &corev1.ConfigMap{}
	targetKey := client.ObjectKey{
		Name:      targetName,
		Namespace: targetNamespace,
	}

	// Check if target exists
	err := r.Get(ctx, targetKey, targetCM)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to get target configmap %s/%s: %w", targetNamespace, targetName, err)
	}

	targetExists := err == nil

	// Check if update is needed (if requested)
	if targetExists && checkEquality {
		if r.isCloudConfigEqual(source, targetCM) {
			klog.V(3).Infof("Target configmap %s/%s is already up to date", targetNamespace, targetName)
			return nil
		}
	}

	// Prepare the target configmap
	targetCM.SetName(targetName)
	targetCM.SetNamespace(targetNamespace)
	targetCM.Data = source.Data
	targetCM.BinaryData = source.BinaryData
	targetCM.Immutable = source.Immutable

	// Create or update
	if !targetExists {
		klog.Infof("Creating configmap %s/%s", targetNamespace, targetName)
		if err := r.Create(ctx, targetCM); err != nil {
			return fmt.Errorf("failed to create configmap %s/%s: %w", targetNamespace, targetName, err)
		}
		return nil
	}

	klog.V(3).Infof("Updating configmap %s/%s", targetNamespace, targetName)
	if err := r.Update(ctx, targetCM); err != nil {
		return fmt.Errorf("failed to update configmap %s/%s: %w", targetNamespace, targetName, err)
	}
	return nil
}

func (r *CloudConfigReconciler) syncCloudConfigData(ctx context.Context, source *corev1.ConfigMap) error {
	// Use the generic helper, no equality check (always update for target CM)
	return r.syncConfigMapToTarget(ctx, source, syncedCloudConfigMapName, r.ManagedNamespace, false)
}

// syncManagedCloudConfig updates openshift-config-managed/kube-cloud-config with the
// transformed cloud config. This makes the transformed config available to other operators
// while maintaining CCCMO as the owner of this ConfigMap (migrated from CCO).
//
// This function handles the migration of ownership from CCO to CCCMO by:
//   - Creating the ConfigMap if it doesn't exist (initial migration)
//   - Updating it with transformed config from user source (openshift-config)
//   - Making it the single source of truth for other operators
func (r *CloudConfigReconciler) syncManagedCloudConfig(ctx context.Context, source *corev1.ConfigMap) error {
	// Validate source has required key
	if source != nil && source.Data != nil {
		if _, ok := source.Data[defaultConfigKey]; !ok {
			return fmt.Errorf("source configmap missing required key: %s", defaultConfigKey)
		}
	}

	// Use the generic helper with equality check (avoid unnecessary updates)
	// For managed config, we want to check equality to reduce churn
	return r.syncConfigMapToTarget(ctx, source, managedCloudConfigMapName, OpenshiftManagedConfigNamespace, true)
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
