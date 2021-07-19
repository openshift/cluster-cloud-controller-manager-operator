package cloud

import (
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/aws"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/azure"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/azurestack"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/openstack"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetResources selectively returns a list resources required for
// provisioning CCM instance in the cluster for the given platform type
//
// These resources will be actively maintained by the operator, preventing
// changes in their spec. However you can extend any resource spec with
// values not specified in the provided source resource. These changes
// would be preserved.
func GetResources(platform configv1.PlatformType, platformStatus *configv1.PlatformStatus) []client.Object {
	switch platform {
	case configv1.AWSPlatformType:
		return aws.GetResources()
	case configv1.OpenStackPlatformType:
		return openstack.GetResources()
	case configv1.AzurePlatformType:
		if isAzureStackHub(platformStatus) {
			return azurestack.GetResources()
		}
		return azure.GetResources()
	default:
		klog.Warningf("Unrecognized platform type %q found in infrastructure", platform)
		return nil
	}
}

func isAzureStackHub(platformStatus *configv1.PlatformStatus) bool {
	return platformStatus.Azure != nil && platformStatus.Azure.CloudName == configv1.AzureStackCloud
}
