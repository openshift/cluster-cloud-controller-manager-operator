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

package controllers

import (
	"context"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/library-go/pkg/cloudprovider"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/controllers/resourceapply"
)

const (
	externalFeatureGateName = "cluster"
	kcmResourceName         = "cluster"

	// Condition type for Cloud Controller ownership
	cloudControllerOwnershipCondition = "CloudControllerOwner"
)

// CloudOperatorReconciler reconciles a ClusterOperator object
type CloudOperatorReconciler struct {
	ClusterOperatorStatusClient
	Scheme            *runtime.Scheme
	watcher           ObjectWatcher
	ImagesFile        string
	FeatureGateAccess featuregates.FeatureGateAccess
}

// +kubebuilder:rbac:groups=config.openshift.io,resources=clusteroperators,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=config.openshift.io,resources=clusteroperators/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=config.openshift.io,resources=clusteroperators/finalizers,verbs=update
// +kubebuilder:rbac:groups=config.openshift.io,resources=infrastructures,verbs=get;list;watch

// Reconcile will process the cloud-controller-manager clusterOperator
func (r *CloudOperatorReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	conditionOverrides := []configv1.ClusterOperatorStatusCondition{}

	infra := &configv1.Infrastructure{}
	if err := r.Get(ctx, client.ObjectKey{Name: infrastructureResourceName}, infra); errors.IsNotFound(err) {
		klog.Infof("Infrastructure cluster does not exist. Skipping...")

		if err := r.setStatusAvailable(ctx, conditionOverrides); err != nil {
			klog.Errorf("Unable to sync cluster operator status: %s", err)
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	} else if err != nil {
		klog.Errorf("Unable to retrive Infrastructure object: %v", err)

		if err := r.setStatusDegraded(ctx, err, conditionOverrides); err != nil {
			klog.Errorf("Error syncing ClusterOperatorStatus: %v", err)
			return ctrl.Result{}, fmt.Errorf("error syncing ClusterOperatorStatus: %v", err)
		}
		return ctrl.Result{}, err
	}

	if infra.Status.PlatformStatus != nil && infra.Status.PlatformStatus.Type == configv1.AlibabaCloudPlatformType {
		klog.Infof("Alibaba platform type is detected, upgrades are not allowed.")
		conditionOverrides = append(conditionOverrides, newClusterOperatorStatusCondition(configv1.OperatorUpgradeable, configv1.ConditionFalse, ReasonPlatformTechPreview, "Alibaba platform is currently tech preview, upgrades are not allowed."))
	}

	allowedToProvision, err := r.provisioningAllowed(ctx, infra, conditionOverrides)
	if err != nil {
		klog.Errorf("Unable to determine cluster state to check if provision is allowed: %v", err)
		return ctrl.Result{}, err
	} else if !allowedToProvision {
		return ctrl.Result{}, nil
	}

	clusterProxy := &configv1.Proxy{}
	if err := r.Get(ctx, client.ObjectKey{Name: proxyResourceName}, clusterProxy); err != nil && !errors.IsNotFound(err) {
		klog.Errorf("Unable to retrive Proxy object: %v", err)

		if err := r.setStatusDegraded(ctx, err, conditionOverrides); err != nil {
			klog.Errorf("Error syncing ClusterOperatorStatus: %v", err)
			return ctrl.Result{}, fmt.Errorf("error syncing ClusterOperatorStatus: %v", err)
		}
		return ctrl.Result{}, err
	}

	operatorConfig, err := config.ComposeConfig(infra, clusterProxy, r.ImagesFile, r.ManagedNamespace, r.FeatureGateAccess)
	if err != nil {
		klog.Errorf("Unable to build operator config %s", err)
		if err := r.setStatusDegraded(ctx, err, conditionOverrides); err != nil {
			klog.Errorf("Error syncing ClusterOperatorStatus: %v", err)
			return ctrl.Result{}, fmt.Errorf("error syncing ClusterOperatorStatus: %v", err)
		}
		return ctrl.Result{}, err
	}

	if err := r.sync(ctx, operatorConfig, conditionOverrides); err != nil {
		klog.Errorf("Unable to sync operands: %s", err)
		if err := r.setStatusDegraded(ctx, err, conditionOverrides); err != nil {
			klog.Errorf("Error syncing ClusterOperatorStatus: %v", err)
			return ctrl.Result{}, fmt.Errorf("error syncing ClusterOperatorStatus: %v", err)
		}
		return ctrl.Result{}, err
	}

	if err := r.setStatusAvailable(ctx, conditionOverrides); err != nil {
		klog.Errorf("Unable to sync cluster operator status: %s", err)
		return ctrl.Result{}, err
	}

	if err := r.clearCloudControllerOwnerCondition(ctx); err != nil {
		klog.Errorf("Unable to clear CloudControllerOwner condition: %s", err)
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *CloudOperatorReconciler) sync(ctx context.Context, config config.OperatorConfig, conditionOverrides []configv1.ClusterOperatorStatusCondition) error {
	// Deploy resources for platform
	resources, err := cloud.GetResources(config)
	if err != nil {
		return err
	}
	updated, err := r.applyResources(ctx, resources)
	if err != nil {
		return err
	}
	if updated {
		return r.setStatusProgressing(ctx, conditionOverrides)
	}

	return nil
}

// applyResources will apply all resources as-is to the cluster, allowing adding of custom annotations and lables
func (r *CloudOperatorReconciler) applyResources(ctx context.Context, resources []client.Object) (bool, error) {
	updated := false
	var err error

	for _, resource := range resources {
		updated, err = resourceapply.ApplyResource(ctx, r.Client, r.Recorder, resource)
		if err != nil {
			return false, err
		}

		if err := r.watcher.Watch(ctx, resource); err != nil {
			klog.Errorf("Unable to establish watch on object %s '%s': %+v", resource.GetObjectKind().GroupVersionKind(), resource.GetName(), err)
			r.Recorder.Event(resource, corev1.EventTypeWarning, "Establish watch failed", err.Error())
			return false, err
		}
	}

	if len(resources) > 0 {
		klog.V(2).Info("Resources applied successfully.")
	}

	return updated, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CloudOperatorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	watcher, err := NewObjectWatcher(WatcherOptions{
		Cache:  mgr.GetCache(),
		Scheme: mgr.GetScheme(),
	})
	if err != nil {
		return err
	}
	r.watcher = watcher

	build := ctrl.NewControllerManagedBy(mgr).
		For(&configv1.ClusterOperator{}, builder.WithPredicates(clusterOperatorPredicates())).
		Watches(&configv1.Infrastructure{},
			handler.EnqueueRequestsFromMapFunc(toClusterOperator),
			builder.WithPredicates(infrastructurePredicates())).
		Watches(&configv1.FeatureGate{},
			handler.EnqueueRequestsFromMapFunc(toClusterOperator),
			builder.WithPredicates(featureGatePredicates())).
		Watches(&operatorv1.KubeControllerManager{},
			handler.EnqueueRequestsFromMapFunc(toClusterOperator),
			builder.WithPredicates(kcmPredicates())).
		WatchesRawSource(source.Channel(watcher.EventStream(), handler.EnqueueRequestsFromMapFunc(toClusterOperator))).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(toClusterOperator)).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(toClusterOperator))

	return build.Complete(r)
}

func (r *CloudOperatorReconciler) provisioningAllowed(ctx context.Context, infra *configv1.Infrastructure, conditionOverrides []configv1.ClusterOperatorStatusCondition) (bool, error) {
	// Check if dependant controllers are available
	available, err := r.checkControllerConditions(ctx)
	if err != nil {
		if err := r.setStatusDegraded(ctx, err, conditionOverrides); err != nil {
			klog.Errorf("Error syncing ClusterOperatorStatus: %v", err)
			return false, fmt.Errorf("error syncing ClusterOperatorStatus: %v", err)
		}
		return false, err
	}
	if !available {
		return false, nil
	}

	if r.isPlatformExternal(infra.Status.PlatformStatus) {
		klog.V(3).Info("'External' platform type is detected, do nothing.")
		if err := r.setStatusAvailable(ctx, conditionOverrides); err != nil {
			klog.Errorf("Unable to sync cluster operator status: %s", err)
			return false, err
		}
		return false, nil
	}

	// If CCM already owns cloud controllers, then provision is allowed by default
	ownedByCCM, err := r.isCloudControllersOwnedByCCM(ctx, conditionOverrides)
	if err != nil {
		return false, err
	} else if ownedByCCM {
		return true, nil
	}

	// Verify FeatureGate ExternalCloudProvider is enabled for operator to work in TP phase
	external, err := cloudprovider.IsCloudProviderExternal(infra.Status.PlatformStatus)
	if err != nil {
		klog.Errorf("Could not determine external cloud provider state: %v", err)

		if err := r.setStatusDegraded(ctx, err, conditionOverrides); err != nil {
			klog.Errorf("Error syncing ClusterOperatorStatus: %v", err)
			return false, fmt.Errorf("error syncing ClusterOperatorStatus: %v", err)
		}
		return false, err
	} else if !external {
		klog.Infof("Platform does not require an external cloud provider. Skipping...")

		if err := r.setStatusAvailable(ctx, conditionOverrides); err != nil {
			klog.Errorf("Unable to sync cluster operator status: %s", err)
			return false, err
		}

		return false, nil
	}

	return true, nil
}

func (r *CloudOperatorReconciler) isCloudControllersOwnedByCCM(ctx context.Context, conditionOverrides []configv1.ClusterOperatorStatusCondition) (bool, error) {
	co, err := r.getOrCreateClusterOperator(ctx)
	if err != nil {
		klog.Errorf("Unable to retrive ClusterOperator object: %v", err)

		if err := r.setStatusDegraded(ctx, err, conditionOverrides); err != nil {
			klog.Errorf("Error syncing ClusterOperatorStatus: %v", err)
			return false, fmt.Errorf("error syncing ClusterOperatorStatus: %v", err)
		}
		return false, err
	}

	// If there is no condition, we assume that CCM doesn't own the Cloud Controllers
	ownedByCCM := false
	if co.Status.Conditions != nil {
		for _, cond := range co.Status.Conditions {
			if cond.Type == cloudControllerOwnershipCondition {
				ownedByCCM = cond.Status == configv1.ConditionTrue
			}
		}
	}

	return ownedByCCM, nil
}

// checkControllerConditions returns True if all dependant controllers are available, and error if any
// of them is degraded
func (r *CloudOperatorReconciler) checkControllerConditions(ctx context.Context) (bool, error) {
	co, err := r.getOrCreateClusterOperator(ctx)
	if err != nil {
		return false, err
	}

	cloudConfigControllerAvailable := false
	trustedCABundleControllerAvailable := false

	for _, cond := range co.Status.Conditions {
		if cond.Type == cloudConfigControllerDegradedCondition || cond.Type == trustedCABundleControllerDegradedCondition {
			if cond.Status == configv1.ConditionTrue {
				return false, fmt.Errorf("failed to apply resources because %s condition is set to True: %s", cond.Type, cond.Message)
			}
		}

		if cond.Type == cloudConfigControllerAvailableCondition && cond.Status == configv1.ConditionTrue {
			cloudConfigControllerAvailable = true
		}

		if cond.Type == trustedCABundleControllerAvailableCondition && cond.Status == configv1.ConditionTrue {
			trustedCABundleControllerAvailable = true
		}
	}

	return cloudConfigControllerAvailable && trustedCABundleControllerAvailable, nil
}

func (r *CloudOperatorReconciler) isPlatformExternal(platformStatus *configv1.PlatformStatus) bool {
	return platformStatus.Type == configv1.ExternalPlatformType
}
