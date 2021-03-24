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
	"os"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/openstack"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/openshift/library-go/pkg/cloudprovider"
	"github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"
)

const (
	clusterOperatorName           = "cloud-controller-manager"
	reasonAsExpected              = "AsExpected"
	unknownVersionValue           = "unknown"
	operatorVersionKey            = "operator"
	releaseVersionEnvVariableName = "RELEASE_VERSION"
	infrastructureName            = "cluster"
	externalFeatureGateName       = "cluster"
	defaultManagementNamespace    = "openshift-cloud-controller-manager-operator"
)

// CloudOperatorReconciler reconciles a ClusterOperator object
type CloudOperatorReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	watcher          ObjectWatcher
	ManagedNamespace string
	ImagesFile       string
}

// +kubebuilder:rbac:groups=config.openshift.io,resources=clusteroperators,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=config.openshift.io,resources=clusteroperators/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=config.openshift.io,resources=clusteroperators/finalizers,verbs=update

// Reconcile will process the cloud-controller-manager clusterOperator
func (r *CloudOperatorReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	featureGate := &configv1.FeatureGate{}
	if err := r.Get(ctx, client.ObjectKey{Name: externalFeatureGateName}, featureGate); errors.IsNotFound(err) {
		klog.Infof("FeatureGate cluster does not exist. Skipping...")
		if err := r.statusAvailable(ctx); err != nil {
			klog.Errorf("Unable to sync cluster operator status: %s", err)
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	} else if err != nil {
		klog.Errorf("Unable to retrive FeatureGate object: %v", err)
		return ctrl.Result{}, err
	}

	infra := &configv1.Infrastructure{}
	if err := r.Get(ctx, client.ObjectKey{Name: infrastructureName}, infra); errors.IsNotFound(err) {
		klog.Infof("Infrastructure cluster does not exist. Skipping...")

		if err := r.statusAvailable(ctx); err != nil {
			klog.Errorf("Unable to sync cluster operator status: %s", err)
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	} else if err != nil {
		klog.Errorf("Unable to retrive Infrastructure object: %v", err)
		return ctrl.Result{}, err
	}

	// Verify FeatureGate ExternalCloudProvider is enabled for operator to work in TP phase
	external, err := cloudprovider.IsCloudProviderExternal(infra.Status.Platform, featureGate)
	if err != nil {
		klog.Errorf("Could not determine external cloud provider state: %v", err)
		return ctrl.Result{}, err
	} else if !external {
		klog.Infof("FeatureGate cluster is not specifying external cloud provider requirement. Skipping...")

		if err := r.statusAvailable(ctx); err != nil {
			klog.Errorf("Unable to sync cluster operator status: %s", err)
			return ctrl.Result{}, err
		}

		return ctrl.Result{}, nil
	}

	// Deploy resources for platform
	resources := getResources(infra)
	if err := r.sync(ctx, resources); err != nil {
		klog.Errorf("Unable to sync operands: %s", err)
		return ctrl.Result{}, err
	}

	if err := r.statusAvailable(ctx); err != nil {
		klog.Errorf("Unable to sync cluster operator status: %s", err)
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *CloudOperatorReconciler) relatedObjects() []configv1.ObjectReference {
	return []configv1.ObjectReference{
		{Resource: "namespaces", Name: defaultManagementNamespace},
		{Resource: "namespaces", Name: r.ManagedNamespace},
	}
}

func (r *CloudOperatorReconciler) sync(ctx context.Context, resources []client.Object) error {
	for _, resource := range resources {
		if err := applyServerSide(ctx, r.Client, clusterOperatorName, resource); err != nil {
			klog.Errorf("Unable to apply object %T '%s': %+v", resource, resource.GetName(), err)
			return err
		}

		if err := r.watcher.Watch(ctx, resource); err != nil {
			klog.Errorf("Unable to establish watch on object %T '%s': %+v", resource, resource.GetName(), err)
			return err
		}
	}

	if len(resources) > 0 {
		klog.Info("Resources applied successfully.")
	}

	return nil
}

func applyServerSide(ctx context.Context, c client.Client, owner client.FieldOwner, obj client.Object, opts ...client.PatchOption) error {
	opts = append([]client.PatchOption{client.ForceOwnership, owner}, opts...)
	return c.Patch(ctx, obj, client.Apply, opts...)
}

func getResources(infra *configv1.Infrastructure) []client.Object {
	if infra.Status == (configv1.InfrastructureStatus{}) {
		klog.Warning("No platform value found in infrastructure")
		return nil
	}

	switch infra.Status.Platform {
	case configv1.AWSPlatformType:
		return cloud.GetAWSResources()
	case configv1.OpenStackPlatformType:
		return openstack.GetResources()
	default:
		klog.Warning("No recognized cloud provider platform found in infrastructure")
	}

	return nil
}

// statusAvailable sets the Available condition to True, with the given reason
// and message, and sets both the Progressing and Degraded conditions to False.
func (r *CloudOperatorReconciler) statusAvailable(ctx context.Context) error {
	co, err := r.getOrCreateClusterOperator(ctx)
	if err != nil {
		return err
	}

	conds := []configv1.ClusterOperatorStatusCondition{
		{
			Type:               configv1.OperatorAvailable,
			Status:             configv1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             reasonAsExpected,
			Message:            fmt.Sprintf("Cluster Cloud Controller Manager Operator is available at %s", getReleaseVersion()),
		},
		{
			Type:               configv1.OperatorDegraded,
			Status:             configv1.ConditionFalse,
			LastTransitionTime: metav1.Now(),
			Reason:             reasonAsExpected,
			Message:            "",
		},
		{
			Type:               configv1.OperatorProgressing,
			Status:             configv1.ConditionFalse,
			LastTransitionTime: metav1.Now(),
			Reason:             reasonAsExpected,
			Message:            "",
		},
		{
			Type:               configv1.OperatorUpgradeable,
			Status:             configv1.ConditionTrue,
			LastTransitionTime: metav1.Now(),
			Reason:             reasonAsExpected,
			Message:            "",
		},
	}

	co.Status.Versions = []configv1.OperandVersion{{Name: operatorVersionKey, Version: getReleaseVersion()}}
	return r.syncStatus(ctx, co, conds)
}

func (r *CloudOperatorReconciler) getOrCreateClusterOperator(ctx context.Context) (*configv1.ClusterOperator, error) {
	co := &configv1.ClusterOperator{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterOperatorName,
		},
		Status: configv1.ClusterOperatorStatus{},
	}
	err := r.Get(ctx, client.ObjectKey{Name: clusterOperatorName}, co)
	if errors.IsNotFound(err) {
		klog.Infof("ClusterOperator does not exist, creating a new one.")

		err = r.Create(ctx, co)
		if err != nil {
			return nil, fmt.Errorf("failed to create cluster operator: %v", err)
		}
		return co, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get clusterOperator %q: %v", clusterOperatorName, err)
	}
	return co, nil
}

//syncStatus applies the new condition to the ClusterOperator object.
func (r *CloudOperatorReconciler) syncStatus(ctx context.Context, co *configv1.ClusterOperator, conds []configv1.ClusterOperatorStatusCondition) error {
	for _, c := range conds {
		v1helpers.SetStatusCondition(&co.Status.Conditions, c)
	}

	if !equality.Semantic.DeepEqual(co.Status.RelatedObjects, r.relatedObjects()) {
		co.Status.RelatedObjects = r.relatedObjects()
	}

	return r.Status().Update(ctx, co)
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

func getReleaseVersion() string {
	releaseVersion := os.Getenv(releaseVersionEnvVariableName)
	if len(releaseVersion) == 0 {
		releaseVersion = unknownVersionValue
		klog.Infof("%s environment variable is missing, defaulting to %q", releaseVersionEnvVariableName, unknownVersionValue)
	}
	return releaseVersion
}
