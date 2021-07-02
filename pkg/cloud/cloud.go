package cloud

import (
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/aws"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/azure"
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
func GetResources(platform configv1.PlatformType) []client.Object {
	switch platform {
	case configv1.AWSPlatformType:
		return aws.GetResources()
	case configv1.OpenStackPlatformType:
		return openstack.GetResources()
	default:
		klog.Warningf("Unrecognized platform type %q found in infrastructure", platform)
		return nil
	}
}

// GetBootstrapResources selectively returns a list static pods required for
// provisioning CCM on bootstrap node for the given platform type
//
// This pod is required for platforms that allow multiple Node initialization from
// a single CCM instance which is not bound to link-local VM IP address and node name.
// Allows to initialize master Nodes immediately after they are created by the installer.
func GetBootstrapResources(platform configv1.PlatformType) []client.Object {
	switch platform {
	case configv1.AWSPlatformType:
		return aws.GetBootstrapResources()
	case configv1.AzurePlatformType:
		return azure.GetBootstrapResources()
	default:
		klog.Warning("No recognized cloud provider platform found in infrastructure")
		return nil
	}
}
