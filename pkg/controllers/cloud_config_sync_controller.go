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

	cloudConfigTransformerFn, needsManagedConfigLookup, err := cloud.GetCloudConfigTransformer(infra.Status.PlatformStatus)
	if err != nil {
		klog.Errorf("unable to get cloud config transformer function; unsupported platform")
		if err := r.setDegradedCondition(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
		}
		return ctrl.Result{}, err
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
		} else if err != nil {
			klog.Errorf("unable to get managed cloud-config for sync")
			if err := r.setDegradedCondition(ctx); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
			}
			return ctrl.Result{}, err
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
			klog.Errorf("unable to get cloud-config for sync: %v", err)
			if err := r.setDegradedCondition(ctx); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
			}
			return ctrl.Result{}, err
		}
	}

	sourceCM, err = r.prepareSourceConfigMap(sourceCM, infra)
	if err != nil {
		if err := r.setDegradedCondition(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
		}
		return ctrl.Result{}, err
	}

	// Check if FeatureGateAccess is configured
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

	targetCM := &corev1.ConfigMap{}
	targetConfigMapKey := client.ObjectKey{
		Namespace: r.ManagedNamespace,
		Name:      syncedCloudConfigMapName,
	}

	// If the config does not exist, it will be created later, so we can ignore a Not Found error
	if err := r.Get(ctx, targetConfigMapKey, targetCM); err != nil && !errors.IsNotFound(err) {
		klog.Errorf("unable to get target cloud-config for sync")
		if err := r.setDegradedCondition(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
		}
		return ctrl.Result{}, err
	}

	// Note that the source config map is actually a *transformed* source config map
	if r.isCloudConfigEqual(sourceCM, targetCM) {
		klog.V(1).Infof("source and target cloud-config content are equal, no sync needed")
		if err := r.setAvailableCondition(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
		}
		return ctrl.Result{}, nil
	}

	if err := r.syncCloudConfigData(ctx, sourceCM, targetCM); err != nil {
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
