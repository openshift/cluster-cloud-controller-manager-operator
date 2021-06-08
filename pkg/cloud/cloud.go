package cloud

import (
	"fmt"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/aws"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/openstack"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
)

type ProviderAssets common.ProviderAssets

func GetAssets(operatorConfig config.OperatorConfig) (ProviderAssets, error) {

	switch operatorConfig.Platform {
	case configv1.AWSPlatformType:
		return aws.NewAssets(operatorConfig)
	case configv1.OpenStackPlatformType:
		return openstack.NewAssets(operatorConfig)
	default:
		return nil, fmt.Errorf("platform type %q not yet supported", operatorConfig.Platform)
	}
}
