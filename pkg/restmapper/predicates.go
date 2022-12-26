package restmapper

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GroupFilterPredicate filters group during discovery process
type GroupFilterPredicate func(*metav1.APIGroup) bool

// AllGroups predicate which permits all groups
func AllGroups(*metav1.APIGroup) bool {
	return true
}

// OpenshiftConfigGroup checks if APIGroup is openshift-specific "config.openshift.io"
func OpenshiftConfigGroup(group *metav1.APIGroup) bool {
	return group.Name == "config.openshift.io"
}

// OpenshiftOperatorGroup checks if APIGroup is openshift-specific "operator.openshift.io"
func OpenshiftOperatorGroup(group *metav1.APIGroup) bool {
	return group.Name == "operator.openshift.io"
}

// KubernetesCoreGroup checks if APIGroup is the Kubernetes' "core" ("legacy") group
// ConfigMaps, Secrets, and other Kube native resources sit here.
// ref: https://kubernetes.io/docs/reference/using-api/#api-groups
func KubernetesCoreGroup(group *metav1.APIGroup) bool {
	return group.Name == ""
}

// KubernetesAppsGroup checks if APIGroup is the Kubernetes' "apps" group
// Deployment and Daemonset resources are sitting here.
func KubernetesAppsGroup(group *metav1.APIGroup) bool {
	return group.Name == "apps"
}

// KubernetesPolicyGroup checks if APIGroup is the Kubernetes' "apps" group
// PodDisruptionBudget is sitting here.
func KubernetesPolicyGroup(group *metav1.APIGroup) bool {
	return group.Name == "policy"
}

// Or combines passed predicate functions in a way to implement logical OR between them.
func Or(predicates ...GroupFilterPredicate) GroupFilterPredicate {
	return func(g *metav1.APIGroup) bool {
		for _, p := range predicates {
			if p(g) {
				return true
			}
		}
		return false
	}
}
