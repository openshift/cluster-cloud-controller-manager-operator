package ibm

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
			initErrMsg: "ibm: missed images in config: CloudControllerManager: non zero value required",
		}, {
			name: "Minimal allowed config",
			config: config.OperatorConfig{
				ImagesReference: config.ImagesReference{
					CloudControllerManagerIBM: "CloudControllerManagerIBM",
				},
				PlatformStatus: &configv1.PlatformStatus{Type: configv1.IBMCloudPlatformType},
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
			assert.Len(t, resources, 1)
		})
	}
}
