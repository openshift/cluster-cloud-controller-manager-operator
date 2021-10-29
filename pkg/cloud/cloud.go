package cloud

import (
	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/alibaba"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/aws"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/azure"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/azurestack"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/ibm"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/openstack"
)

// GetResources selectively returns a list of resources required for
// provisioning CCM instance in the cluster for the given OperatorConfig.
//
// These resources will be actively maintained by the operator, preventing
// changes in their spec. However you can extend any resource spec with
// values not specified in the provided source resource. These changes
// would be preserved.
func GetResources(operatorConfig config.OperatorConfig) ([]client.Object, error) {
	assets, err := getAssets(operatorConfig)
	if err != nil {
		if _, isPlatformNotFoundError := err.(*platformNotFoundError); isPlatformNotFoundError {
			klog.Infof("platform not supported: %v", err)
			return nil, nil
		}
		klog.Errorf("can not get assets: %v", err)
		return nil, err
	}
	renderedObjects := assets.GetRenderedResources()
	substitutedObjects := common.SubstituteCommonPartsFromConfig(operatorConfig, renderedObjects)
	return substitutedObjects, nil
}

// getAssets internal function which returns fully initialized CloudProviderAssets object.
func getAssets(operatorConfig config.OperatorConfig) (common.CloudProviderAssets, error) {
	constructor, err := getAssetsConstructor(operatorConfig.PlatformStatus)
	if err != nil {
		return nil, err
	}
	return constructor(operatorConfig)
}

type assetsConstructor func(config config.OperatorConfig) (common.CloudProviderAssets, error)

// getAssetsConstructor internal function which selectively returns CloudProviderAssets constructor function
// for given PlatformStatus. Intended to be a single place across operator logic where platform dependent choice happen.
func getAssetsConstructor(platformStatus *configv1.PlatformStatus) (assetsConstructor, error) {
	switch platformStatus.Type {
	case configv1.AlibabaCloudPlatformType:
		return alibaba.NewProviderAssets, nil
	case configv1.AWSPlatformType:
		return aws.NewProviderAssets, nil
	case configv1.OpenStackPlatformType:
		return openstack.NewProviderAssets, nil
	case configv1.AzurePlatformType:
		if isAzureStackHub(platformStatus) {
			return azurestack.NewProviderAssets, nil
		}
		return azure.NewProviderAssets, nil
	case configv1.IBMCloudPlatformType:
		return ibm.NewProviderAssets, nil
	default:
		return nil, newPlatformNotFoundError(platformStatus.Type)
	}
}

func isAzureStackHub(platformStatus *configv1.PlatformStatus) bool {
	return platformStatus.Azure != nil && platformStatus.Azure.CloudName == configv1.AzureStackCloud
}
