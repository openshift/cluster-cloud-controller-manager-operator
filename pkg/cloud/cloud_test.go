package cloud

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/aws"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/azure"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/openstack"
	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestGetResources(t *testing.T) {
	tc := []struct {
		name     string
		platform configv1.PlatformType
		expected []client.Object
	}{{
		name:     "AWS resources returned as expected",
		platform: configv1.AWSPlatformType,
		expected: aws.GetResources(),
	}, {
		name:     "OpenStack resources returned as expected",
		platform: configv1.OpenStackPlatformType,
		expected: openstack.GetResources(),
	}, {
		name:     "GCP resources are empty, as the platform is not yet supported",
		platform: configv1.GCPPlatformType,
	}, {
		name:     "Azure resources are empty, as the platform is not yet supported",
		platform: configv1.AzurePlatformType,
	}, {
		name:     "VSphere resources are empty, as the platform is not yet supported",
		platform: configv1.VSpherePlatformType,
	}, {
		name:     "OVirt resources are empty, as the platform is not yet supported",
		platform: configv1.OvirtPlatformType,
	}, {
		name:     "IBMCloud resources are empty, as the platform is not yet supported",
		platform: configv1.IBMCloudPlatformType,
	}, {
		name:     "Libvirt resources are empty",
		platform: configv1.LibvirtPlatformType,
	}, {
		name:     "Kubevirt resources are empty",
		platform: configv1.KubevirtPlatformType,
	}, {
		name:     "BareMetal resources are empty",
		platform: configv1.BareMetalPlatformType,
	}, {
		name:     "None platform resources are empty",
		platform: configv1.NonePlatformType,
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			resources := GetResources(tc.platform)

			assert.Equal(t, len(tc.expected), len(resources))
			assert.EqualValues(t, tc.expected, resources)

			// Edit and repeat procedure to ensure modification in place is not present
			if len(resources) > 0 {
				resources[0].SetName("different")
				newResources := GetResources(tc.platform)

				assert.Equal(t, len(tc.expected), len(newResources))
				assert.EqualValues(t, tc.expected, newResources)
				assert.NotEqualValues(t, resources, newResources)
			}
		})
	}
}

func TestGetBootstrapResources(t *testing.T) {
	tc := []struct {
		name     string
		platform configv1.PlatformType
		expected []client.Object
	}{{
		name:     "AWS resources returned as expected",
		platform: configv1.AWSPlatformType,
		expected: aws.GetBootstrapResources(),
	}, {
		name:     "OpenStack resources returned as expected",
		platform: configv1.OpenStackPlatformType,
		expected: openstack.GetBootstrapResources(),
	}, {
		name:     "GCP resources are empty, as the platform is not yet supported",
		platform: configv1.GCPPlatformType,
	}, {
		name:     "Azure resources returned as expected",
		platform: configv1.AzurePlatformType,
		expected: azure.GetBootstrapResources(),
	}, {
		name:     "VSphere resources are empty, as the platform is not yet supported",
		platform: configv1.VSpherePlatformType,
	}, {
		name:     "OVirt resources are empty, as the platform is not yet supported",
		platform: configv1.OvirtPlatformType,
	}, {
		name:     "IBMCloud resources are empty, as the platform is not yet supported",
		platform: configv1.IBMCloudPlatformType,
	}, {
		name:     "Libvirt resources are empty",
		platform: configv1.LibvirtPlatformType,
	}, {
		name:     "Kubevirt resources are empty",
		platform: configv1.KubevirtPlatformType,
	}, {
		name:     "BareMetal resources are empty",
		platform: configv1.BareMetalPlatformType,
	}, {
		name:     "None platform resources are empty",
		platform: configv1.NonePlatformType,
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			resources := GetBootstrapResources(tc.platform)

			assert.Equal(t, len(tc.expected), len(resources))
			assert.EqualValues(t, tc.expected, resources)

			if len(resources) > 0 {
				// Edit and repeat procedure to ensure modification in place is not present
				for _, resource := range resources {
					resource.SetName("different")
				}
				newResources := GetBootstrapResources(tc.platform)

				assert.Equal(t, len(tc.expected), len(newResources))
				assert.EqualValues(t, tc.expected, newResources)
				assert.NotEqualValues(t, resources, newResources)
			}
		})
	}
}
