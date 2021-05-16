package cloud

import (
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/aws"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/openstack"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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
