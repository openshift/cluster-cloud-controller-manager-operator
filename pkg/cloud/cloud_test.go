package cloud

import (
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
)

func getConfigForPlatform(platform configv1.PlatformType) config.OperatorConfig {
	return config.OperatorConfig{
		ManagedNamespace:  "test",
		Platform:          platform,
		ImagesFileContent: []byte("{}"),
	}
}

func TestGetResources(t *testing.T) {
	tc := []struct {
		name                 string
		config               config.OperatorConfig
		expectedGetAssetsErr string
	}{{
		name:   "AWS resources returned as expected",
		config: getConfigForPlatform(configv1.AWSPlatformType),
	}, {
		name:   "OpenStack resources returned as expected",
		config: getConfigForPlatform(configv1.OpenStackPlatformType),
	}, {
		name:                 "GCP not yet supported",
		config:               getConfigForPlatform(configv1.GCPPlatformType),
		expectedGetAssetsErr: `platform type "GCP" not yet supported`,
	}, {
		name:                 "Azure not yet supported",
		config:               getConfigForPlatform(configv1.AzurePlatformType),
		expectedGetAssetsErr: `platform type "Azure" not yet supported`,
	}, {
		name:                 "VSphere not yet supported",
		config:               getConfigForPlatform(configv1.VSpherePlatformType),
		expectedGetAssetsErr: `platform type "VSphere" not yet supported`,
	}, {
		name:                 "OVirt not yet supported",
		config:               getConfigForPlatform(configv1.OvirtPlatformType),
		expectedGetAssetsErr: `platform type "oVirt" not yet supported`,
	}, {
		name:                 "IBMCloud not yet supported",
		config:               getConfigForPlatform(configv1.IBMCloudPlatformType),
		expectedGetAssetsErr: `platform type "IBMCloud" not yet supported`,
	}, {
		name:                 "Libvirt not yet supported",
		config:               getConfigForPlatform(configv1.LibvirtPlatformType),
		expectedGetAssetsErr: `platform type "Libvirt" not yet supported`,
	}, {
		name:                 "Kubevirt not yet supported",
		config:               getConfigForPlatform(configv1.KubevirtPlatformType),
		expectedGetAssetsErr: `platform type "KubeVirt" not yet supported`,
	}, {
		name:                 "BareMetal not yet supported",
		config:               getConfigForPlatform(configv1.BareMetalPlatformType),
		expectedGetAssetsErr: `platform type "BareMetal" not yet supported`,
	}, {
		name:                 "None not yet supported",
		config:               getConfigForPlatform(configv1.NonePlatformType),
		expectedGetAssetsErr: `platform type "None" not yet supported`,
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			assets, err := GetAssets(tc.config)

			if tc.expectedGetAssetsErr != "" {
				assert.EqualError(t, err, tc.expectedGetAssetsErr)
			} else {
				assert.NoError(t, err)

				_, ok := assets.(ProviderAssets)
				assert.True(t, ok)

				resources, err := assets.GetResources()
				assert.NoError(t, err)

				// Edit and repeat procedure to ensure modification in place is not present
				resources[0].SetName("different")
				newResources, err := assets.GetResources()
				assert.NoError(t, err)
				assert.NotEqualValues(t, resources, newResources)
			}
		})
	}
}

func TestGetBootstrapResources(t *testing.T) {
	tc := []struct {
		name                    string
		config                  config.OperatorConfig
		expectedGetAssetsErr    string
		expectedGetBootstrapErr string
	}{{
		name:   "AWS resources returned as expected",
		config: getConfigForPlatform(configv1.AWSPlatformType),
	}, {
		name:                    "OpenStack bootstrap does not yet supported",
		expectedGetBootstrapErr: "bootstrap assets are not implemented yet",
		config:                  getConfigForPlatform(configv1.OpenStackPlatformType),
	}, {
		name:                 "GCP resources are empty, as the platform is not yet supported",
		config:               getConfigForPlatform(configv1.GCPPlatformType),
		expectedGetAssetsErr: `platform type "GCP" not yet supported`,
	}, {
		name:                 "Azure resources are empty, as the platform is not yet supported",
		config:               getConfigForPlatform(configv1.AzurePlatformType),
		expectedGetAssetsErr: `platform type "Azure" not yet supported`,
	}, {
		name:                 "VSphere resources are empty, as the platform is not yet supported",
		config:               getConfigForPlatform(configv1.VSpherePlatformType),
		expectedGetAssetsErr: `platform type "VSphere" not yet supported`,
	}, {
		name:                 "OVirt resources are empty, as the platform is not yet supported",
		config:               getConfigForPlatform(configv1.OvirtPlatformType),
		expectedGetAssetsErr: `platform type "oVirt" not yet supported`,
	}, {
		name:                 "IBMCloud resources are empty, as the platform is not yet supported",
		config:               getConfigForPlatform(configv1.IBMCloudPlatformType),
		expectedGetAssetsErr: `platform type "IBMCloud" not yet supported`,
	}, {
		name:                 "Libvirt resources are empty",
		config:               getConfigForPlatform(configv1.LibvirtPlatformType),
		expectedGetAssetsErr: `platform type "Libvirt" not yet supported`,
	}, {
		name:                 "Kubevirt resources are empty",
		config:               getConfigForPlatform(configv1.KubevirtPlatformType),
		expectedGetAssetsErr: `platform type "KubeVirt" not yet supported`,
	}, {
		name:                 "BareMetal resources are empty",
		config:               getConfigForPlatform(configv1.BareMetalPlatformType),
		expectedGetAssetsErr: `platform type "BareMetal" not yet supported`,
	}, {
		name:                 "None platform resources are empty",
		config:               getConfigForPlatform(configv1.NonePlatformType),
		expectedGetAssetsErr: `platform type "None" not yet supported`,
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			assets, err := GetAssets(tc.config)

			if tc.expectedGetAssetsErr != "" {
				assert.EqualError(t, err, tc.expectedGetAssetsErr)
			} else {
				assert.NoError(t, err)

				_, ok := assets.(ProviderAssets)
				assert.True(t, ok)

				resources, err := assets.GetBootsrapResources()

				if tc.expectedGetBootstrapErr != "" {
					assert.EqualError(t, err, tc.expectedGetBootstrapErr)
				} else {
					assert.NoError(t, err)
					// Edit and repeat procedure to ensure modification in place is not present
					resources[0].SetName("different")
					newResources, err := assets.GetBootsrapResources()
					assert.NoError(t, err)
					assert.NotEqualValues(t, resources, newResources)
				}
			}
		})
	}
}
