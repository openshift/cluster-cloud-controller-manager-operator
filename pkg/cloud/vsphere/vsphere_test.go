package vsphere

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"

	"github.com/stretchr/testify/assert"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
)

func TestResourcesRenderingSmoke(t *testing.T) {
	customFeatureGates := featuregates.NewFeatureGate([]configv1.FeatureGateName{"SomeOtherFeatureGate", features.FeatureGateVSphereMixedNodeEnv}, nil)

	tc := []struct {
		name       string
		config     config.OperatorConfig
		initErrMsg string
	}{
		{
			name:       "Empty config",
			config:     config.OperatorConfig{},
			initErrMsg: "vsphere: missed images in config: CloudControllerManager: non zero value required",
		}, {
			name: "No infra name",
			config: config.OperatorConfig{
				ManagedNamespace: "my-cool-namespace",
				ImagesReference: config.ImagesReference{
					CloudControllerManagerVSphere: "CloudControllerManagerVsphere",
				},
				PlatformStatus: &configv1.PlatformStatus{Type: configv1.VSpherePlatformType},
			},
			initErrMsg: "can not construct template values for vsphere assets: infrastructureName: non zero value required",
		}, {
			name: "Minimal allowed config",
			config: config.OperatorConfig{
				ManagedNamespace: "my-cool-namespace",
				ImagesReference: config.ImagesReference{
					CloudControllerManagerVSphere: "CloudControllerManagerVsphere",
				},
				PlatformStatus:     &configv1.PlatformStatus{Type: configv1.VSpherePlatformType},
				InfrastructureName: "infra",
			},
		}, {
			name: "FeatureGate FeatureGateVSphereMixedNodeEnv=true results in node-labels generated without error",
			config: config.OperatorConfig{
				ManagedNamespace: "my-cool-namespace",
				ImagesReference: config.ImagesReference{
					CloudControllerManagerVSphere: "CloudControllerManagerVsphere",
				},
				PlatformStatus:     &configv1.PlatformStatus{Type: configv1.VSpherePlatformType},
				InfrastructureName: "infra",
				FeatureGates:       "FeatureGateVSphereMixedNodeEnv=true",
				OCPFeatureGates:    customFeatureGates,
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
			assert.Len(t, resources, 7)
		})
	}
}
