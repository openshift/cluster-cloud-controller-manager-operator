package azure

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
)

func TestResourcesRenderingSmoke(t *testing.T) {

	tc := []struct {
		name       string
		config     config.OperatorConfig
		initErrMsg string
	}{
		{
			name:       "Empty config",
			config:     config.OperatorConfig{},
			initErrMsg: "azure: missed images in config: CloudControllerManager: non zero value required;CloudNodeManager: non zero value required",
		}, {
			name: "No infra config",
			config: config.OperatorConfig{
				ManagedNamespace: "my-cool-namespace",
				ImagesReference: config.ImagesReference{
					CloudControllerManagerAzure: "CloudControllerManagerAzure",
					CloudNodeManagerAzure:       "CloudNodeManagerAzure",
				},
				PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType},
			},
			initErrMsg: "can not construct template values for azure assets: infrastructureName: non zero value required",
		}, {
			name: "No proxy config",
			config: config.OperatorConfig{
				ManagedNamespace: "my-cool-namespace",
				ImagesReference: config.ImagesReference{
					CloudControllerManagerAzure: "CloudControllerManagerAzure",
					CloudNodeManagerAzure:       "CloudNodeManagerAzure",
				},
				PlatformStatus:     &configv1.PlatformStatus{Type: configv1.AzurePlatformType},
				InfrastructureName: "infra",
			},
		}, {
			name: "Config with proxy",
			config: config.OperatorConfig{
				ManagedNamespace: "my-cool-namespace",
				ImagesReference: config.ImagesReference{
					CloudControllerManagerAzure: "CloudControllerManagerAzure",
					CloudNodeManagerAzure:       "CloudNodeManagerAzure",
				},
				PlatformStatus:     &configv1.PlatformStatus{Type: configv1.AlibabaCloudPlatformType},
				InfrastructureName: "infra",
				ClusterProxy: &configv1.Proxy{
					Status: configv1.ProxyStatus{
						HTTPSProxy: "https://squid.corp.acme.com:3128",
					},
				},
			},
		},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			assets, err := NewProviderAssets(tc.config)
			if tc.initErrMsg != "" {
				assert.EqualError(t, err, tc.initErrMsg)
				return
			} else {
				assert.NoError(t, err)
			}

			resources := assets.GetRenderedResources()
			assert.Len(t, resources, 2)
		})
	}
}
