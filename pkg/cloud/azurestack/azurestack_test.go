package azurestack

import (
	"testing"

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
			initErrMsg: "azurestack: missed images in config: CloudControllerManager: non zero value required;CloudNodeManager: non zero value required;Operator: non zero value required",
		}, {
			name: "No infra config",
			config: config.OperatorConfig{
				ManagedNamespace: "my-cool-namespace",
				ImagesReference: config.ImagesReference{
					CloudControllerManagerOperator: "Operator",
					CloudControllerManagerAzure:    "CloudControllerManagerAzure",
					CloudNodeManagerAzure:          "CloudNodeManagerAzure",
				},
			},
			initErrMsg: "can not construct template values for azurestack assets: infrastructureName: non zero value required",
		}, {
			name: "Minimal allowed config",
			config: config.OperatorConfig{
				ImagesReference: config.ImagesReference{
					CloudControllerManagerOperator: "Operator",
					CloudControllerManagerAzure:    "CloudControllerManagerAzure",
					CloudNodeManagerAzure:          "CloudNodeManagerAzure",
				},
				InfrastructureName: "infra",
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
