package cloud

import (
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configv1 "github.com/openshift/api/config/v1"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/aws"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/azure"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/azurestack"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/gcp"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/ibm"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/nutanix"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/openstack"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/powervs"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/vsphere"
)

// cloudConfigTransformer function transforms the source config map using the input infrastructure.config.openshift.io
// and network.config.openshift.io objects. Only the data and binaryData field of the output ConfigMap will be respected by
// consumer of the transformer.
type cloudConfigTransformer func(source string, infra *configv1.Infrastructure, network *configv1.Network, features featuregates.FeatureGate) (string, error)

// GetCloudConfigTransformer returns the function that should be used to transform
// the cloud configuration config map, and a boolean to indicate if the config should
// be synced from the CCO namespace before applying the transformation.
// TODO: the boolean return value to indicate if the config should be synced can be
// removed once we migrate the AWS and Azure logic from the CCO to this operator.
// See the FIXME comments below, and the TODO comment in the Reconcile function
// inside cloud_config_sync_controller.go.
func GetCloudConfigTransformer(platformStatus *configv1.PlatformStatus) (cloudConfigTransformer, bool, error) {
	switch platformStatus.Type {
	case configv1.AWSPlatformType:
		// We intentionally return nil rather than NoOpTransformer since we
		// want to handle this differently in the caller.
		// FIXME: We need to implement a transformer for this. Currently we're
		// relying on CCO to do the heavy lifting for us.
		return aws.CloudConfigTransformer, true, nil
	case configv1.AzurePlatformType:
		// We intentionally return nil rather than NoOpTransformer since we
		// want to handle this differently in the caller.
		// Except on Azure Stack Hub, where we need to lookup the cloud config
		// from the managed namespace and also return a config transformer.
		// FIXME: We need to implement a transformer for this. Currently we're
		// relying on CCO to do the heavy lifting for us. The Azure Stack Hub
		// transformer is only to fix OCPBUGS-20213.
		if azurestack.IsAzureStackHub(platformStatus) {
			return azurestack.CloudConfigTransformer, true, nil
		}
		return azure.CloudConfigTransformer, true, nil
	case configv1.GCPPlatformType:
		return common.NoOpTransformer, false, nil
	case configv1.IBMCloudPlatformType:
		return common.NoOpTransformer, false, nil
	case configv1.OpenStackPlatformType:
		return openstack.CloudConfigTransformer, false, nil
	case configv1.PowerVSPlatformType:
		//Power VS platform uses ibm cloud provider
		return common.NoOpTransformer, false, nil
	case configv1.VSpherePlatformType:
		return vsphere.CloudConfigTransformer, false, nil
	case configv1.NutanixPlatformType:
		return common.NoOpTransformer, false, nil
	default:
		return nil, false, newPlatformNotFoundError(platformStatus.Type)
	}
}

// GetResources selectively returns a list of resources required for
// provisioning CCM instance in the cluster for the given OperatorConfig.
//
// These resources will be actively maintained by the operator, preventing
// changes in their spec.
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
	case configv1.AWSPlatformType:
		return aws.NewProviderAssets, nil
	case configv1.AzurePlatformType:
		if azurestack.IsAzureStackHub(platformStatus) {
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
	case configv1.NutanixPlatformType:
		return nutanix.NewProviderAssets, nil
	default:
		return nil, newPlatformNotFoundError(platformStatus.Type)
	}
}
