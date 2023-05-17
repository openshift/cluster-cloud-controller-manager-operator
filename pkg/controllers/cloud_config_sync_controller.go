package controllers

import (
	"context"
	"fmt"
	"reflect"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud"
)

const (
	managedCloudConfigMapName = "kube-cloud-config"

	defaultConfigKey = "cloud.conf"

	// Controller conditions for the Cluster Operator resource
	cloudConfigControllerAvailableCondition = "CloudConfigControllerAvailable"
	cloudConfigControllerDegradedCondition  = "CloudConfigControllerDegraded"

	upgradeAvailableMessage = "Cluster Cloud Controller Manager Operator is working as expected, no concerns about upgrading"
)

type CloudConfigReconciler struct {
	ClusterOperatorStatusClient
	Scheme *runtime.Scheme
}

func (r *CloudConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.Infof("Syncing cloud-conf ConfigMap")

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

	// Due to an upgrade issue from 4.12 -> 4.13 on Nutanix, a user will need to manually create the necessary
	// cloud configuration ConfigMap. We need to check here and set the operator's upgradeable status to
	// false if the configmap does not exist.
	// We are checking here because Nutanix does not require a sync in this version and the controller will return early otherwise.
	// See https://issues.redhat.com/browse/OCPBUGS-7898 for more information.
	if cmNeeded, err := r.isProviderNutanixAndCloudConfigNeeded(ctx, infra.Status.PlatformStatus); cmNeeded {
		// The ConfigMap does not exist, set upgradeable to false and return
		reason := "MissingNutanixConfigMap"
		message := "Cloud Config Controller is not upgradeable due to missing \"cloud-conf\" ConfigMap"
		if err := r.setUpgradeableCondition(ctx, configv1.ConditionFalse, reason, message); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
		}
		return ctrl.Result{}, nil
	} else if err != nil {
		// An error occurred while checking, set degraged and return the error.
		if err := r.setDegradedCondition(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
		}
		return ctrl.Result{}, err
	} else if infra.Status.PlatformStatus != nil && infra.Status.PlatformStatus.Type == configv1.NutanixPlatformType {
		// we are on Nutanix platform and the ConfigMap exists, ensure that the operator is upgradeable
		if err := r.setUpgradeableCondition(ctx, configv1.ConditionTrue, ReasonAsExpected, upgradeAvailableMessage); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for cloud config controller: %v", err)
		}

		// we will always return early on Nutanix, but as this patch is intended for 4.12 only it will be fine
		// because Nutanix does not have a CCM in 4.12 and this operator will be replaced by the incoming 4.13 version.
		return ctrl.Result{}, nil
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

	cloudConfigTransformerFn, err := cloud.GetCloudConfigTransformer(infra.Status.PlatformStatus)
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
	if cloudConfigTransformerFn == nil {
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

	if !managedConfigFound {
		openshiftUnmanagedCMKey := client.ObjectKey{
			Name:      infra.Spec.CloudConfig.Name,
			Namespace: OpenshiftConfigNamespace,
		}
		if err := r.Get(ctx, openshiftUnmanagedCMKey, sourceCM); err != nil {
			klog.Errorf("unable to get cloud-config for sync")
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

	if cloudConfigTransformerFn != nil {
		// We ignore stuff in sourceCM.BinaryData. This isn't allowed to
		// contain any key that overlaps with those found in sourceCM.Data and
		// we're not expecting users to put their data in the former.
		output, err := cloudConfigTransformerFn(sourceCM.Data[defaultConfigKey], infra, network)
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
		klog.Infof("source and target cloud-config content are equal, no sync needed")
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
	case configv1.AzurePlatformType,
		configv1.GCPPlatformType,
		configv1.VSpherePlatformType,
		configv1.AlibabaCloudPlatformType,
		configv1.IBMCloudPlatformType,
		configv1.PowerVSPlatformType,
		configv1.OpenStackPlatformType:
		return true, nil
	case configv1.AWSPlatformType:
		// Some of AWS regions might require to sync a cloud-config, in such case reference in infra resource will be presented
		return infraCloudConfigRef.Name != "", nil
	default:
		return false, nil
	}
}

func (r *CloudConfigReconciler) prepareSourceConfigMap(source *corev1.ConfigMap, infra *configv1.Infrastructure) (*corev1.ConfigMap, error) {
	// Keys might be different between openshift-config/cloud-config and openshift-config-managed/kube-cloud-config
	// Always use "cloud.conf" which is default one across openshift
	cloudConfCm := source.DeepCopy()
	if _, ok := cloudConfCm.Data[defaultConfigKey]; ok {
		return cloudConfCm, nil
	}

	infraConfigKey := infra.Spec.CloudConfig.Key
	if val, ok := cloudConfCm.Data[infraConfigKey]; ok {
		cloudConfCm.Data[defaultConfigKey] = val
		delete(cloudConfCm.Data, infraConfigKey)
		return cloudConfCm, nil
	}
	return nil, fmt.Errorf(
		"key %s specified in infra resource does not found in source configmap %s",
		infraConfigKey, client.ObjectKeyFromObject(source),
	)
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
			&source.Kind{Type: &configv1.Infrastructure{}},
			handler.EnqueueRequestsFromMapFunc(toManagedConfigMap),
			builder.WithPredicates(infrastructurePredicates()),
		).
		Watches(
			&source.Kind{Type: &configv1.Network{}},
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
		newClusterOperatorStatusCondition(configv1.OperatorUpgradeable, configv1.ConditionTrue, ReasonAsExpected,
			upgradeAvailableMessage),
	}

	co.Status.Versions = []configv1.OperandVersion{{Name: operatorVersionKey, Version: r.ReleaseVersion}}
	klog.Info("Cloud Config Controller is available")
	return r.syncStatus(ctx, co, conds)
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
	return r.syncStatus(ctx, co, conds)
}

func (r *CloudConfigReconciler) setUpgradeableCondition(ctx context.Context, condition configv1.ConditionStatus, reason string, message string) error {
	co, err := r.getOrCreateClusterOperator(ctx)
	if err != nil {
		return err
	}

	conds := []configv1.ClusterOperatorStatusCondition{
		newClusterOperatorStatusCondition(configv1.OperatorUpgradeable, condition, reason, message),
	}

	co.Status.Versions = []configv1.OperandVersion{{Name: operatorVersionKey, Version: r.ReleaseVersion}}
	if condition == configv1.ConditionFalse {
		klog.Info("Cloud Config Controller is not upgradeable")
	} else {
		klog.Info("Cloud Config Controller is upgradeable")
	}
	return r.syncStatus(ctx, co, conds)
}

// isProviderNutanixAndCloudConfigNeeded will determine whether the proper cloud configuration ConfigMap is present
// on the Nutanix platform. This function is being added to mitigate an upgrade issue where the user must create
// the ConfigMap before the upgrade can progress.
// See https://issues.redhat.com/browse/OCPBUGS-7898 for more information.
func (r *CloudConfigReconciler) isProviderNutanixAndCloudConfigNeeded(ctx context.Context, platformStatus *configv1.PlatformStatus) (bool, error) {
	if platformStatus == nil || platformStatus.Type != configv1.NutanixPlatformType {
		return false, nil
	}

	cm := &corev1.ConfigMap{}
	// the name of this ConfigMap is sourced from the Nutanix asset for the volume mount
	// See: https://github.com/openshift/cluster-cloud-controller-manager-operator/blob/release-4.13/pkg/cloud/nutanix/assets/cloud-controller-manager-deployment.yaml#L107
	if err := r.Get(ctx, client.ObjectKey{Name: syncedCloudConfigMapName, Namespace: r.ManagedNamespace}, cm); errors.IsNotFound(err) {
		// confirmed that the ConfigMap does not exist.
		klog.Warningf("ConfigMap \"cloud-conf\" is not found, and is required for upgrade on Nutanix platform")
		return true, nil
	} else if err != nil {
		// got an unexpected error, report false and return the error
		return false, err
	}

	return false, nil
}
