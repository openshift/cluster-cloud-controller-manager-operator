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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	errutils "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
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
	Scheme     *runtime.Scheme
	watcher    ObjectWatcher
	ImagesFile string
}

// +kubebuilder:rbac:groups=config.openshift.io,resources=clusteroperators,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=config.openshift.io,resources=clusteroperators/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=config.openshift.io,resources=clusteroperators/finalizers,verbs=update
// +kubebuilder:rbac:groups=config.openshift.io,resources=infrastructures,verbs=get;list;watch

// Reconcile will process the cloud-controller-manager clusterOperator
func (r *CloudOperatorReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	infra := &configv1.Infrastructure{}
	if err := r.Get(ctx, client.ObjectKey{Name: infrastructureResourceName}, infra); errors.IsNotFound(err) {
		klog.Infof("Infrastructure cluster does not exist. Skipping...")

		if err := r.setStatusAvailable(ctx); err != nil {
			klog.Errorf("Unable to sync cluster operator status: %s", err)
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	} else if err != nil {
		klog.Errorf("Unable to retrive Infrastructure object: %v", err)

		if err := r.setStatusDegraded(ctx, err); err != nil {
			klog.Errorf("Error syncing ClusterOperatorStatus: %v", err)
			return ctrl.Result{}, fmt.Errorf("error syncing ClusterOperatorStatus: %v", err)
		}
		return ctrl.Result{}, err
	}

	clusterProxy := &configv1.Proxy{}
	if err := r.Get(ctx, client.ObjectKey{Name: proxyResourceName}, clusterProxy); err != nil && !errors.IsNotFound(err) {
		klog.Errorf("Unable to retrive Proxy object: %v", err)

		if err := r.setStatusDegraded(ctx, err); err != nil {
			klog.Errorf("Error syncing ClusterOperatorStatus: %v", err)
			return ctrl.Result{}, fmt.Errorf("error syncing ClusterOperatorStatus: %v", err)
		}
		return ctrl.Result{}, err
	}

	operatorConfig, err := config.ComposeConfig(infra, clusterProxy, r.ImagesFile, r.ManagedNamespace)
	if err != nil {
		klog.Errorf("Unable to build operator config %s", err)
		if err := r.setStatusDegraded(ctx, err); err != nil {
			klog.Errorf("Error syncing ClusterOperatorStatus: %v", err)
			return ctrl.Result{}, fmt.Errorf("error syncing ClusterOperatorStatus: %v", err)
		}
		return ctrl.Result{}, err
	}

	// Get resources for platform the specific platform
	resources, err := cloud.GetResources(operatorConfig)
	if err != nil {
		klog.Errorf("Unable to render operands manifests: %s", err)
		if err := r.setStatusDegraded(ctx, err); err != nil {
			klog.Errorf("Error syncing ClusterOperatorStatus: %v", err)
			return ctrl.Result{}, fmt.Errorf("error syncing ClusterOperatorStatus: %v", err)
		}
	}

	allowedToProvision, err := r.provisioningAllowed(ctx, infra)
	if err != nil {
		klog.Errorf("Unable to determine cluster state to check if provision is allowed: %v", err)
		return ctrl.Result{}, err
	} else if !allowedToProvision {
		operandsProvisioned, err := r.operandsProvisioned(ctx, resources)
		if err != nil {
			klog.Errorf("Unable to determine if operands are already provisioned: %v", err)
		}
		if operandsProvisioned {
			if err := r.deleteOperands(ctx, resources); err != nil {
				if err := r.setStatusDegraded(ctx, err); err != nil {
					klog.Errorf("Error syncing ClusterOperatorStatus: %v", err)
					return ctrl.Result{}, fmt.Errorf("error syncing ClusterOperatorStatus: %v", err)
				}
				return ctrl.Result{}, fmt.Errorf("Unable to delete previously deployed operands: %v", err)
			}
		}
		return ctrl.Result{}, nil
	}

	if err := r.sync(ctx, resources); err != nil {
		klog.Errorf("Unable to sync operands: %s", err)
		if err := r.setStatusDegraded(ctx, err); err != nil {
			klog.Errorf("Error syncing ClusterOperatorStatus: %v", err)
			return ctrl.Result{}, fmt.Errorf("error syncing ClusterOperatorStatus: %v", err)
		}
		return ctrl.Result{}, err
	}

	if err := r.setStatusAvailable(ctx); err != nil {
		klog.Errorf("Unable to sync cluster operator status: %s", err)
		return ctrl.Result{}, err
	}

	if err := r.setCloudControllerOwnerCondition(ctx); err != nil {
		klog.Errorf("Unable to sync cluster operator status: %s", err)
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *CloudOperatorReconciler) sync(ctx context.Context, resources []client.Object) error {
	updated, err := r.applyResources(ctx, resources)
	if err != nil {
		return err
	}
	if updated {
		return r.setStatusProgressing(ctx)
	}

	return nil
}

// operandsProvisioned checks if resources from the passed slice are provisioned
func (r *CloudOperatorReconciler) operandsProvisioned(ctx context.Context, resources []client.Object) (bool, error) {
	var errList []error
	// use delete with dry run for avoid occasional resource mutation in the passed slice
	opts := &client.DeleteOptions{
		DryRun: []string{metav1.DryRunAll},
	}
	for _, resource := range resources {
		err := r.Client.Delete(ctx, resource, opts)
		if err == nil {
			return true, nil
		}
		if err != nil && !errors.IsNotFound(err) {
			errList = append(errList, err)
		}
	}
	if len(errList) > 0 {
		return false, errutils.NewAggregate(errList)
	}
	return false, nil
}

// deleteOperands deletes resources from the passed slice from the cluster
func (r *CloudOperatorReconciler) deleteOperands(ctx context.Context, resources []client.Object) error {
	var errList []error
	propagation := metav1.DeletePropagationForeground
	opts := &client.DeleteOptions{
		GracePeriodSeconds: pointer.Int64(20),
		PropagationPolicy:  &propagation,
	}
	for _, resource := range resources {
		if err := r.Client.Delete(ctx, resource, opts); err != nil {
			errList = append(errList, err)
		}
	}
	if len(errList) > 0 {
		return errutils.NewAggregate(errList)
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
		Watches(&source.Kind{Type: &configv1.Infrastructure{}},
			handler.EnqueueRequestsFromMapFunc(toClusterOperator),
			builder.WithPredicates(infrastructurePredicates())).
		Watches(&source.Kind{Type: &configv1.FeatureGate{}},
			handler.EnqueueRequestsFromMapFunc(toClusterOperator),
			builder.WithPredicates(featureGatePredicates())).
		Watches(&source.Kind{Type: &operatorv1.KubeControllerManager{}},
			handler.EnqueueRequestsFromMapFunc(toClusterOperator),
			builder.WithPredicates(kcmPredicates())).
		Watches(&source.Channel{Source: watcher.EventStream()}, handler.EnqueueRequestsFromMapFunc(toClusterOperator)).
		Watches(&source.Kind{Type: &corev1.ConfigMap{}}, handler.EnqueueRequestsFromMapFunc(toClusterOperator)).
		Watches(&source.Kind{Type: &corev1.Secret{}}, handler.EnqueueRequestsFromMapFunc(toClusterOperator))

	return build.Complete(r)
}

func (r *CloudOperatorReconciler) provisioningAllowed(ctx context.Context, infra *configv1.Infrastructure) (bool, error) {
	// Check if dependant controllers are available
	available, err := r.checkControllerConditions(ctx)
	if err != nil {
		if err := r.setStatusDegraded(ctx, err); err != nil {
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
		return false, nil
	}

	// If CCM already owns cloud controllers, then provision is allowed by default
	ownedByCCM, err := r.isCloudControllersOwnedByCCM(ctx)
	if err != nil {
		return false, err
	} else if ownedByCCM {
		return true, nil
	}

	featureGate := &configv1.FeatureGate{}
	if err := r.Get(ctx, client.ObjectKey{Name: externalFeatureGateName}, featureGate); errors.IsNotFound(err) {
		klog.Infof("FeatureGate cluster does not exist. Skipping...")

		if err := r.setStatusAvailable(ctx); err != nil {
			klog.Errorf("Unable to sync cluster operator status: %s", err)
			return false, err
		}
		return false, nil
	} else if err != nil {
		klog.Errorf("Unable to retrive FeatureGate object: %v", err)

		if err := r.setStatusDegraded(ctx, err); err != nil {
			klog.Errorf("Error syncing ClusterOperatorStatus: %v", err)
			return false, fmt.Errorf("error syncing ClusterOperatorStatus: %v", err)
		}
		return false, err
	}

	// Verify FeatureGate ExternalCloudProvider is enabled for operator to work in TP phase
	external, err := cloudprovider.IsCloudProviderExternal(infra.Status.PlatformStatus, featureGate)
	if err != nil {
		klog.Errorf("Could not determine external cloud provider state: %v", err)

		if err := r.setStatusDegraded(ctx, err); err != nil {
			klog.Errorf("Error syncing ClusterOperatorStatus: %v", err)
			return false, fmt.Errorf("error syncing ClusterOperatorStatus: %v", err)
		}
		return false, err
	} else if !external {
		klog.Infof("FeatureGate cluster is not specifying external cloud provider requirement. Skipping...")

		if err := r.setStatusAvailable(ctx); err != nil {
			klog.Errorf("Unable to sync cluster operator status: %s", err)
			return false, err
		}

		return false, nil
	}

	ownedByKCM, err := r.isCloudControllersOwnedByKCM(ctx)
	if err != nil {
		return false, err
	}
	if ownedByKCM {
		// KCM resource found and it owns Cloud provider
		klog.Infof("KubeControllerManager still owns Cloud Controllers. Skipping...")

		if err := r.setStatusAvailable(ctx); err != nil {
			klog.Errorf("Unable to sync cluster operator status: %s", err)
			return false, err
		}

		return false, nil
	}

	return true, nil
}

func (r *CloudOperatorReconciler) isCloudControllersOwnedByKCM(ctx context.Context) (bool, error) {
	kcm := &operatorv1.KubeControllerManager{}
	err := r.Get(ctx, client.ObjectKey{Name: kcmResourceName}, kcm)
	if err != nil {
		klog.Errorf("Unable to retrive KubeControllerManager object: %v", err)

		if err := r.setStatusDegraded(ctx, err); err != nil {
			klog.Errorf("Error syncing ClusterOperatorStatus: %v", err)
			return false, fmt.Errorf("error syncing ClusterOperatorStatus: %v", err)
		}
		return false, err
	}

	if len(kcm.Status.Conditions) == 0 {
		// If KCM Conditions object doesn't exist, we assume that we are in bootstrapping process and
		// the controllers are not owned by KCM.
		klog.Info("KubeControllerManager status not found, cluster is bootstrapping with external cloud providers")
		return false, nil
	}

	// If there is no condition, we assume that KCM owns the Cloud Controllers
	ownedByKCM := true
	for _, cond := range kcm.Status.Conditions {
		if cond.Type == cloudControllerOwnershipCondition {
			ownedByKCM = cond.Status == operatorv1.ConditionTrue
		}
	}

	return ownedByKCM, nil
}

func (r *CloudOperatorReconciler) isCloudControllersOwnedByCCM(ctx context.Context) (bool, error) {
	co, err := r.getOrCreateClusterOperator(ctx)
	if err != nil {
		klog.Errorf("Unable to retrive ClusterOperator object: %v", err)

		if err := r.setStatusDegraded(ctx, err); err != nil {
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
				return false, fmt.Errorf("failed to apply resources because %s condition is set to True", cond.Type)
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
