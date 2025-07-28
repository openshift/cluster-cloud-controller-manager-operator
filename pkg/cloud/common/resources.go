package common

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
)

const (
	CloudControllerManagerProviderLabel = "infrastructure.openshift.io/cloud-controller-manager"
	CloudNodeManagerCloudProviderLabel  = "infrastructure.openshift.io/cloud-node-manager"
)

func GetCommonResources(config config.OperatorConfig) ([]client.Object, error) {
	commonResources := []client.Object{}
	if !config.IsSingleReplica {
		pdb, err := getPDB(config)
		if err != nil {
			return nil, err
		}
		commonResources = append(commonResources, pdb)
	}

	commonResources = append(commonResources, getService(config))

	return commonResources, nil
}

func getPDB(config config.OperatorConfig) (*policyv1.PodDisruptionBudget, error) {
	minAvailable := intstr.FromInt(1)
	matchLabels := map[string]string{
		CloudControllerManagerProviderLabel: config.GetPlatformNameString(),
	}
	pdbNamePrefix := strings.ToLower(config.GetPlatformNameString())
	pdbName := fmt.Sprintf("%s-cloud-controller-manager", pdbNamePrefix)
	return &policyv1.PodDisruptionBudget{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PodDisruptionBudget",
			APIVersion: "policy/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      pdbName,
			Namespace: config.ManagedNamespace,
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &minAvailable,
			Selector: &metav1.LabelSelector{
				MatchLabels: matchLabels,
			},
		},
	}, nil
}

// getService returns a common service for the cloud-controller-manager on port 10258,
// for a given platform.
func getService(config config.OperatorConfig) *corev1.Service {
	matchLabels := map[string]string{
		CloudControllerManagerProviderLabel: config.GetPlatformNameString(),
	}
	name := fmt.Sprintf("%s-cloud-controller-manager", strings.ToLower(config.GetPlatformNameString()))

	return &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Service",
			APIVersion: "core/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: config.ManagedNamespace,
			Labels: map[string]string{
				"k8s-app": name,
			},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{
					Name: "https",
					Port: 10258,
				},
				{
					Name:       "webhooks",
					Port:       10260,
					TargetPort: intstr.FromInt(10260),
				},
			},
			Selector:        matchLabels,
			SessionAffinity: corev1.ServiceAffinityNone,
		},
	}
}
