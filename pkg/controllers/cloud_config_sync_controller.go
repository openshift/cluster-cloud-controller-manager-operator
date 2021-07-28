package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	managedCloudConfigMapName = "kube-cloud-config"

	cloudConfigMapName = "cloud-conf"
	defaultConfigKey   = "cloud.conf"
)

type CloudConfigReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Recorder        record.EventRecorder
	TargetNamespace string
}

func (r *CloudConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.Infof("%s emitted event, syncing cloud-conf ConfigMap", req)

	// Use kube-cloud-config from openshift-config-managed namespace as default source.
	// If it is not exists try to use cloud-config reference from infra resource.
	// https://github.com/openshift/library-go/blob/master/pkg/operator/configobserver/cloudprovider/observe_cloudprovider.go#L82
	defaultSourceCMObjectKey := client.ObjectKey{
		Name: managedCloudConfigMapName, Namespace: OpenshiftManagedConfigNamespace,
	}
	sourceCM := &corev1.ConfigMap{}

	infra := &configv1.Infrastructure{}
	if err := r.Get(ctx, client.ObjectKey{Name: infrastructureResourceName}, infra); err != nil {
		klog.Errorf("infrastructure resource not found")
		return ctrl.Result{}, err
	}

	if err := r.Get(ctx, defaultSourceCMObjectKey, sourceCM); errors.IsNotFound(err) {
		klog.Warningf("managed cloud-config is not found, falling back to infrastructure config")
		openshiftUnmanagedCMKey := client.ObjectKey{Name: infra.Spec.CloudConfig.Name, Namespace: OpenshiftConfigNamespace}
		if err := r.Get(ctx, openshiftUnmanagedCMKey, sourceCM); err != nil {
			klog.Errorf("unable to get cloud-config for sync")
			return ctrl.Result{}, err
		}
	} else if err != nil {
		klog.Errorf("unable to get managed cloud-config for sync")
		return ctrl.Result{}, err
	}

	sourceCM, err := r.prepareSourceConfigMap(sourceCM, infra)
	if err != nil {
		return ctrl.Result{}, err
	}

	targetCM := &corev1.ConfigMap{}
	targetConfigMapKey := client.ObjectKey{
		Namespace: r.TargetNamespace,
		Name:      cloudConfigMapName,
	}

	// If the config does not exist, it will be created later, so we can ignore a Not Found error
	if err := r.Get(ctx, targetConfigMapKey, targetCM); err != nil && !errors.IsNotFound(err) {
		klog.Errorf("unable to get target cloud-config for sync")
		return ctrl.Result{}, err
	}

	if r.isCloudConfigEqual(sourceCM, targetCM) {
		klog.Infof("source and target cloud-config content are equal, no sync needed")
		return ctrl.Result{}, nil
	}

	if err := r.syncCloudConfigData(ctx, sourceCM, targetCM); err != nil {
		klog.Errorf("unable to sync cloud config")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *CloudConfigReconciler) prepareSourceConfigMap(source *corev1.ConfigMap, infra *configv1.Infrastructure) (*corev1.ConfigMap, error) {
	// Keys might be different between openshift-config/cloud-config and openshift-config-managed/kube-cloud-config
	// Always use "cloud.conf" which is default one across openshift
	cloudConfCm := source.DeepCopy()

	if _, ok := cloudConfCm.Data[defaultConfigKey]; !ok {
		infraConfigKey := infra.Spec.CloudConfig.Key
		if val, ok := cloudConfCm.Data[infraConfigKey]; ok {
			cloudConfCm.Data[defaultConfigKey] = val
			delete(cloudConfCm.Data, infraConfigKey)
		} else {
			return nil, fmt.Errorf(
				"key %s specified in infra resource does not found in source configmap %s",
				infraConfigKey, client.ObjectKeyFromObject(source),
			)
		}
	}

	provider, err := config.GetProviderFromInfrastructure(infra)
	if err != nil {
		return nil, err
	}
	if provider == configv1.AzurePlatformType {
		changedCloudConfigData, err := r.prepareAzureCloudConfigData(cloudConfCm.Data[defaultConfigKey])
		if err != nil {
			return nil, err
		}
		cloudConfCm.Data[defaultConfigKey] = changedCloudConfigData
	}

	return cloudConfCm, nil
}

func (r *CloudConfigReconciler) prepareAzureCloudConfigData(cloudConfigContent string) (string, error) {
	// Hack for add excludeMasterFromStandardLB parameter if it is not presented in cloud config for azure platform
	const excludeMasterFromStandartLBParamKey = "excludeMasterFromStandardLB"

	bytesContent := []byte(cloudConfigContent)
	if !json.Valid(bytesContent) {
		return "", fmt.Errorf("cloudConfigContent is not a valid json")
	}

	var cfg map[string]interface{}

	err := json.Unmarshal(bytesContent, &cfg)
	if err != nil {
		return "", err
	}

	if val, ok := cfg[excludeMasterFromStandartLBParamKey]; !ok || val == nil {
		cfg[excludeMasterFromStandartLBParamKey] = false
	}

	marshalledConfig, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return string(marshalledConfig), nil
}

func (r *CloudConfigReconciler) isCloudConfigEqual(source *corev1.ConfigMap, target *corev1.ConfigMap) bool {
	return source.Immutable == target.Immutable &&
		reflect.DeepEqual(source.Data, target.Data) && reflect.DeepEqual(source.BinaryData, target.BinaryData)
}

func (r *CloudConfigReconciler) syncCloudConfigData(ctx context.Context, source *corev1.ConfigMap, target *corev1.ConfigMap) error {
	target.SetName(cloudConfigMapName)
	target.SetNamespace(r.TargetNamespace)
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
					ownCloudConfigPredicate(r.TargetNamespace),
					openshiftCloudConfigMapPredicates(),
				),
			),
		).
		Watches(
			&source.Kind{Type: &configv1.Infrastructure{}},
			&handler.EnqueueRequestForObject{},
			builder.WithPredicates(infrastructurePredicates()),
		)

	return build.Complete(r)
}
