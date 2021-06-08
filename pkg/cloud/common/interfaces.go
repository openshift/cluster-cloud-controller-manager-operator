package common

import (
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ProviderAssets interface {
	GetResources() ([]client.Object, error)
	GetBootsrapResources() ([]client.Object, error)
	GetOperatorConfig() config.OperatorConfig
	GetPlatformType() configv1.PlatformType
}
