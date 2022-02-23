package common

import (
	"fmt"
	"strings"

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
	commonResources := make([]client.Object, 0, 1)
	if !config.IsSingleReplica {
		pdb, err := getPDB(config)
		if err != nil {
			return nil, err
		}
		commonResources = append(commonResources, pdb)
	}
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
