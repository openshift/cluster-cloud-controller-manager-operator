package azurestack

import (
	"encoding/json"
	"fmt"
	"testing"

	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	azconfig "sigs.k8s.io/cloud-provider-azure/pkg/provider/config"

	"github.com/stretchr/testify/assert"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
)

const (
	infraCloudConfName = "test-config"
	infraCloudConfKey  = "foo"
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
				PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType},
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
				PlatformStatus:     &configv1.PlatformStatus{Type: configv1.AzurePlatformType},
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

func makeInfrastructureResource(platform configv1.PlatformType, cloudName configv1.AzureCloudEnvironment) *configv1.Infrastructure {
	cfg := configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				Type: platform,
			},
		},
		Spec: configv1.InfrastructureSpec{
			CloudConfig: configv1.ConfigMapFileReference{
				Name: infraCloudConfName,
				Key:  infraCloudConfKey,
			},
			PlatformSpec: configv1.PlatformSpec{
				Type: platform,
			},
		},
	}

	if platform == configv1.AzurePlatformType {
		cfg.Status.PlatformStatus.Azure = &configv1.AzurePlatformStatus{
			CloudName: cloudName,
		}
	}

	return &cfg
}

// This test is a little complicated with all the JSON marshalling and
// unmarshalling, but it is necessary due to the nature of how this data
// is stored in Kuberenetes. The ConfigMaps containing the cloud config
// will have string encoded JSON objects in them, due to the non-deterministic
// natue of map object in Go we will need to examine the data instead of
// comparing strings.
func TestCloudConfigTransformer(t *testing.T) {
	tc := []struct {
		name     string
		source   azconfig.Config
		expected azconfig.Config
		infra    *configv1.Infrastructure
		errMsg   string
	}{
		{
			name:     "Azure Stack Hub sets the vmType to standard",
			source:   azconfig.Config{},
			expected: azconfig.Config{VMType: "standard"},
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzureStackCloud),
		},
		{
			name:     "Azure Stack Hub doesn't modify vmType if user set",
			source:   azconfig.Config{VMType: "vmss"},
			expected: azconfig.Config{VMType: "vmss"},
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzureStackCloud),
		},
		{
			name:   "Non Azure Stack Hub returns an error",
			source: azconfig.Config{},
			infra:  makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzurePublicCloud),
			errMsg: fmt.Sprintf("invalid platform, expected CloudName to be %s", configv1.AzureStackCloud),
		},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			src, err := json.Marshal(tc.source)
			g.Expect(err).NotTo(HaveOccurred(), "Marshal of source data should succeed")

			actual, err := CloudConfigTransformer(string(src), tc.infra, nil)
			if tc.errMsg != "" {
				g.Expect(err).Should(MatchError(tc.errMsg))
				g.Expect(actual).Should(Equal(""))
			} else {
				var observed azconfig.Config
				g.Expect(json.Unmarshal([]byte(actual), &observed)).To(Succeed(), "Unmarshal of observed data should succeed")

				g.Expect(observed).Should(Equal(tc.expected))
			}
		})
	}
}
