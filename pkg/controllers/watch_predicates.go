package controllers

import (
	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

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
		return ok && infra.GetName() == infrastructureResourceName
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

func ownCloudConfigPredicate(targetNamespace string) predicate.Funcs {
	isOwnCloudConfigMap := func(obj runtime.Object) bool {
		configMap, ok := obj.(*corev1.ConfigMap)
		return ok && configMap.GetNamespace() == targetNamespace && configMap.GetName() == syncedCloudConfigMapName
	}

	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isOwnCloudConfigMap(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return isOwnCloudConfigMap(e.ObjectNew) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return isOwnCloudConfigMap(e.Object) },
		GenericFunc: func(e event.GenericEvent) bool { return isOwnCloudConfigMap(e.Object) },
	}
}

func openshiftCloudConfigMapPredicates() predicate.Funcs {
	isCloudConfigMap := func(obj runtime.Object) bool {
		configMap, ok := obj.(*corev1.ConfigMap)

		if !ok {
			return false
		}

		isOpenshiftConfigNamespace := configMap.GetName() == OpenshiftConfigNamespace
		isManagedCloudConfig := configMap.GetName() == managedCloudConfigMapName && configMap.GetNamespace() == OpenshiftManagedConfigNamespace

		return isOpenshiftConfigNamespace || isManagedCloudConfig
	}

	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isCloudConfigMap(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return isCloudConfigMap(e.ObjectNew) },
		GenericFunc: func(e event.GenericEvent) bool { return isCloudConfigMap(e.Object) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return isCloudConfigMap(e.Object) },
	}
}

func ccmTrustedCABundleConfigMapPredicates(targetNamespace string) predicate.Funcs {
	isTrustedCaConfigMap := func(obj runtime.Object) bool {
		configMap, ok := obj.(*corev1.ConfigMap)
		return ok && configMap.GetNamespace() == targetNamespace && configMap.GetName() == trustedCAConfigMapName
	}
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isTrustedCaConfigMap(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return isTrustedCaConfigMap(e.ObjectNew) },
		GenericFunc: func(e event.GenericEvent) bool { return isTrustedCaConfigMap(e.Object) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return isTrustedCaConfigMap(e.Object) },
	}
}

// Config maps from 'openshift-config' namespace
func openshiftConfigNamespacedPredicate() predicate.Funcs {
	isTrustedCaConfigMap := func(obj runtime.Object) bool {
		configMap, ok := obj.(*corev1.ConfigMap)
		return ok && configMap.GetNamespace() == OpenshiftConfigNamespace
	}
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isTrustedCaConfigMap(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return isTrustedCaConfigMap(e.ObjectNew) },
		GenericFunc: func(e event.GenericEvent) bool { return isTrustedCaConfigMap(e.Object) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return isTrustedCaConfigMap(e.Object) },
	}
}
