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
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/gcp"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/ibm"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/openstack"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/powervs"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/vsphere"
)

// cloudConfigTransformer function transforms the source config map using the input infrastructure.config.openshift.io object.
// only the data and binaryData field of the output ConfigMap will be respected by consumer of the transformer.
type cloudConfigTransformer func(source string, infra *configv1.Infrastructure) (string, error)

// GetCloudConfigTransformer returns the function that should be used to transform
// the cloud configuration config map
func GetCloudConfigTransformer(platformStatus *configv1.PlatformStatus) (cloudConfigTransformer, error) {
	switch platformStatus.Type {
	case configv1.AlibabaCloudPlatformType:
		return common.NoOpTransformer, nil
	case configv1.AWSPlatformType:
		// We intentionally return nil rather than NoOpTransformer since we
		// want to handle this differently in the caller.
		// FIXME: We need to implement a transformer for this. Currently we're
		// relying on CCO to do the heavy lifting for us.
		return nil, nil
	case configv1.AzurePlatformType:
		// We intentionally return nil rather than NoOpTransformer since we
		// want to handle this differently in the caller.
		// FIXME: We need to implement a transformer for this. Currently we're
		// relying on CCO to do the heavy lifting for us.
		return nil, nil
	case configv1.GCPPlatformType:
		return common.NoOpTransformer, nil
	case configv1.IBMCloudPlatformType:
		return common.NoOpTransformer, nil
	case configv1.OpenStackPlatformType:
		return openstack.CloudConfigTransformer, nil
	case configv1.PowerVSPlatformType:
		//Power VS platform uses ibm cloud provider
		return common.NoOpTransformer, nil
	case configv1.VSpherePlatformType:
		return common.NoOpTransformer, nil
	default:
		return nil, newPlatformNotFoundError(platformStatus.Type)
	}
}

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
	commonResources, err := common.GetCommonResources(operatorConfig)
	if err != nil {
		klog.Errorf("can not create common resources %v", err)
		return nil, err
	}
	substitutedObjects = append(substitutedObjects, commonResources...)
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
	case configv1.AzurePlatformType:
		if isAzureStackHub(platformStatus) {
			return azurestack.NewProviderAssets, nil
		}
		return azure.NewProviderAssets, nil
	case configv1.GCPPlatformType:
		return gcp.NewProviderAssets, nil
	case configv1.IBMCloudPlatformType:
		return ibm.NewProviderAssets, nil
	case configv1.OpenStackPlatformType:
		return openstack.NewProviderAssets, nil
	case configv1.PowerVSPlatformType:
		return powervs.NewProviderAssets, nil
	case configv1.VSpherePlatformType:
		return vsphere.NewProviderAssets, nil
	default:
		return nil, newPlatformNotFoundError(platformStatus.Type)
	}
}

func isAzureStackHub(platformStatus *configv1.PlatformStatus) bool {
	return platformStatus.Azure != nil && platformStatus.Azure.CloudName == configv1.AzureStackCloud
}
