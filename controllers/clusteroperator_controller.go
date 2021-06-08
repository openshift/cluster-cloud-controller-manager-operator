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
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
	"github.com/openshift/library-go/pkg/cloudprovider"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	externalFeatureGateName = "cluster"
	infrastructureName      = "cluster"
)

// CloudOperatorReconciler reconciles a ClusterOperator object
type CloudOperatorReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	Recorder       record.EventRecorder
	watcher        ObjectWatcher
	ReleaseVersion string
	OperatorConfig config.OperatorConfig
}

// +kubebuilder:rbac:groups=config.openshift.io,resources=clusteroperators,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=config.openshift.io,resources=clusteroperators/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=config.openshift.io,resources=clusteroperators/finalizers,verbs=update

// Reconcile will process the cloud-controller-manager clusterOperator
func (r *CloudOperatorReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	featureGate := &configv1.FeatureGate{}
	if err := r.Get(ctx, client.ObjectKey{Name: externalFeatureGateName}, featureGate); errors.IsNotFound(err) {
		klog.Infof("FeatureGate cluster does not exist. Skipping...")

		if err := r.setStatusAvailable(ctx); err != nil {
			klog.Errorf("Unable to sync cluster operator status: %s", err)
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	} else if err != nil {
		klog.Errorf("Unable to retrive FeatureGate object: %v", err)

		if err := r.setStatusDegraded(ctx, err); err != nil {
			klog.Errorf("Error syncing ClusterOperatorStatus: %v", err)
			return ctrl.Result{}, fmt.Errorf("error syncing ClusterOperatorStatus: %v", err)
		}
		return ctrl.Result{}, err
	}

	// Verify FeatureGate ExternalCloudProvider is enabled for operator to work in TP phase
	external, err := cloudprovider.IsCloudProviderExternal(r.OperatorConfig.Platform, featureGate)
	if err != nil {
		klog.Errorf("Could not determine external cloud provider state: %v", err)

		if err := r.setStatusDegraded(ctx, err); err != nil {
			klog.Errorf("Error syncing ClusterOperatorStatus: %v", err)
			return ctrl.Result{}, fmt.Errorf("error syncing ClusterOperatorStatus: %v", err)
		}
		return ctrl.Result{}, err
	} else if !external {
		klog.Infof("FeatureGate cluster is not specifying external cloud provider requirement. Skipping...")

		if err := r.setStatusAvailable(ctx); err != nil {
			klog.Errorf("Unable to sync cluster operator status: %s", err)
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	if err := r.sync(ctx); err != nil {
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

	return ctrl.Result{}, nil
}

func (r *CloudOperatorReconciler) sync(ctx context.Context) error {
	// Deploy resources for platform
	assets, err := cloud.GetAssets(r.OperatorConfig)
	if err != nil {
		return err
	}
	resources, err := assets.GetResources()
	if err != nil {
		return err
	}

	updated, err := r.applyResources(ctx, resources)
	if err != nil {
		return err
	}
	if updated {
		return r.setStatusProgressing(ctx)
	}

	return nil
}

// applyResources will apply all resources as is to the cluster with
// server-side apply patch and will enforce all the conflicts
func (r *CloudOperatorReconciler) applyResources(ctx context.Context, resources []client.Object) (bool, error) {
	updated := false

	for _, resource := range resources {
		resourceExisting := resource.DeepCopyObject().(client.Object)
		err := r.Get(ctx, client.ObjectKeyFromObject(resourceExisting), resourceExisting)
		if errors.IsNotFound(err) {
			klog.Infof("Resource %s %q needs to be created, operator progressing...", resource.GetObjectKind().GroupVersionKind(), client.ObjectKeyFromObject(resource))
			updated = true
		} else if err != nil {
			r.Recorder.Event(resource, corev1.EventTypeWarning, "Update failed", err.Error())
			return false, err
		}

		resourceUpdated := resource.DeepCopyObject().(client.Object)
		if err := r.Patch(ctx, resourceUpdated, client.Apply, client.ForceOwnership, client.FieldOwner(clusterOperatorName)); err != nil {
			klog.Errorf("Unable to apply object %s '%s': %+v", resource.GetObjectKind().GroupVersionKind(), resource.GetName(), err)
			r.Recorder.Event(resourceExisting, corev1.EventTypeWarning, "Update failed", err.Error())
			return false, err
		}
		klog.V(2).Infof("Applied %s %q successfully", resource.GetObjectKind().GroupVersionKind(), client.ObjectKeyFromObject(resource))

		if resourceExisting.GetGeneration() != resourceUpdated.GetGeneration() {
			klog.Infof("Resource %s %q generation increased, resource updated, operator progressing...", resource.GetObjectKind().GroupVersionKind(), client.ObjectKeyFromObject(resource))
			updated = true
			r.Recorder.Event(resourceExisting, corev1.EventTypeNormal, "Updated successfully", "Resource was successfully updated")
		}

		if err := r.watcher.Watch(ctx, resource); err != nil {
			klog.Errorf("Unable to establish watch on object %s '%s': %+v", resource.GetObjectKind().GroupVersionKind(), resource.GetName(), err)
			r.Recorder.Event(resourceExisting, corev1.EventTypeWarning, "Establish watch failed", err.Error())
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
		Watches(&source.Channel{Source: watcher.EventStream()}, handler.EnqueueRequestsFromMapFunc(toClusterOperator))

	return build.Complete(r)
}

func clusterOperatorPredicates() predicate.Funcs {
	isClusterOperator := func(obj runtime.Object) bool {
		clusterOperator, ok := obj.(*configv1.ClusterOperator)
		return ok && clusterOperator.GetName() == clusterOperatorName
	}

	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isClusterOperator(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return isClusterOperator(e.ObjectNew) },
		GenericFunc: func(e event.GenericEvent) bool { return isClusterOperator(e.Object) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return isClusterOperator(e.Object) },
	}
}

func toClusterOperator(client.Object) []reconcile.Request {
	return []reconcile.Request{{
		NamespacedName: client.ObjectKey{Name: clusterOperatorName},
	}}
}

func infrastructurePredicates() predicate.Funcs {
	isInfrastructureCluster := func(obj runtime.Object) bool {
		infra, ok := obj.(*configv1.Infrastructure)
		return ok && infra.GetName() == infrastructureName
	}

	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isInfrastructureCluster(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return isInfrastructureCluster(e.ObjectNew) },
		GenericFunc: func(e event.GenericEvent) bool { return isInfrastructureCluster(e.Object) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return isInfrastructureCluster(e.Object) },
	}
}

func featureGatePredicates() predicate.Funcs {
	isFeatureGateCluster := func(obj runtime.Object) bool {
		featureGate, ok := obj.(*configv1.FeatureGate)
		return ok && featureGate.GetName() == externalFeatureGateName
	}

	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isFeatureGateCluster(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return isFeatureGateCluster(e.ObjectNew) },
		GenericFunc: func(e event.GenericEvent) bool { return isFeatureGateCluster(e.Object) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return isFeatureGateCluster(e.Object) },
	}
}
