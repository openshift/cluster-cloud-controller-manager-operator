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
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"
)

const (
	clusterOperatorName = "cloud-controller-manager"
	reasonAsExpected    = "AsExpected"
)

var relatedObjects = []configv1.ObjectReference{}

// CloudOperatorReconciler reconciles a ClusterOperator object
type CloudOperatorReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=config.openshift.io,resources=clusteroperators,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=config.openshift.io,resources=clusteroperators/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=config.openshift.io,resources=clusteroperators/finalizers,verbs=update

// Reconcile will process the cloud-controller-manager clusterOperator
func (r *CloudOperatorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	if err := r.statusAvailable(ctx); err != nil {
		klog.Errorf("Unable to sync cluster operator status: %s", err)
	}

	return ctrl.Result{}, nil
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
			Message:            fmt.Sprintf("Cluster Cloud Controller Manager Operator is available at 0.0.1"),
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

	co.Status.Versions = []configv1.OperandVersion{{Name: "operator", Version: "0.0.1"}}
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

	if !equality.Semantic.DeepEqual(co.Status.RelatedObjects, relatedObjects) {
		co.Status.RelatedObjects = relatedObjects
	}

	return r.Status().Update(ctx, co)
}

// SetupWithManager sets up the controller with the Manager.
func (r *CloudOperatorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&configv1.ClusterOperator{}, builder.WithPredicates(predicate.Funcs{
			CreateFunc:  func(e event.CreateEvent) bool { return clusterOperatorFilter(e.Object) },
			UpdateFunc:  func(e event.UpdateEvent) bool { return clusterOperatorFilter(e.ObjectNew) },
			GenericFunc: func(e event.GenericEvent) bool { return clusterOperatorFilter(e.Object) },
			DeleteFunc:  func(e event.DeleteEvent) bool { return clusterOperatorFilter(e.Object) },
		})).
		Complete(r)
}

func clusterOperatorFilter(obj runtime.Object) bool {
	clusterOperator, ok := obj.(*configv1.ClusterOperator)
	return ok && clusterOperator.GetName() == clusterOperatorName
}
