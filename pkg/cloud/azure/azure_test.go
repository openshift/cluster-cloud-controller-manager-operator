package azure

import (
	"encoding/json"
	"fmt"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	azureconsts "sigs.k8s.io/cloud-provider-azure/pkg/consts"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"

	"sigs.k8s.io/cloud-provider-azure/pkg/azclient"
	ratelimitconfig "sigs.k8s.io/cloud-provider-azure/pkg/provider/config"

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
			initErrMsg: "azure: missed images in config: CloudControllerManager: non zero value required;CloudControllerManagerOperator: non zero value required;CloudNodeManager: non zero value required",
		}, {
			name: "No infra config",
			config: config.OperatorConfig{
				ManagedNamespace: "my-cool-namespace",
				ImagesReference: config.ImagesReference{
					CloudControllerManagerAzure:    "CloudControllerManagerAzure",
					CloudNodeManagerAzure:          "CloudNodeManagerAzure",
					CloudControllerManagerOperator: "CloudControllerManagerOperator",
				},
				PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType},
			},
			initErrMsg: "can not construct template values for azure assets: infrastructureName: non zero value required",
		}, {
			name: "No proxy config",
			config: config.OperatorConfig{
				ManagedNamespace: "my-cool-namespace",
				ImagesReference: config.ImagesReference{
					CloudControllerManagerAzure:    "CloudControllerManagerAzure",
					CloudNodeManagerAzure:          "CloudNodeManagerAzure",
					CloudControllerManagerOperator: "CloudControllerManagerOperator",
				},
				PlatformStatus:     &configv1.PlatformStatus{Type: configv1.AzurePlatformType},
				InfrastructureName: "infra",
			},
		}, {
			name: "Config with proxy",
			config: config.OperatorConfig{
				ManagedNamespace: "my-cool-namespace",
				ImagesReference: config.ImagesReference{
					CloudControllerManagerAzure:    "CloudControllerManagerAzure",
					CloudNodeManagerAzure:          "CloudNodeManagerAzure",
					CloudControllerManagerOperator: "CloudControllerManagerOperator",
				},
				PlatformStatus:     &configv1.PlatformStatus{Type: configv1.AzurePlatformType},
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
			assert.Len(t, resources, 4)
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

// makeExpectedConfig sets some repetitive default fields for tests, assuming that they are not already set.
func makeExpectedConfig(config *azure.Config, cloud configv1.AzureCloudEnvironment) azure.Config {
	if config.ClusterServiceLoadBalancerHealthProbeMode == "" {
		config.ClusterServiceLoadBalancerHealthProbeMode = azureconsts.ClusterServiceLoadBalancerHealthProbeModeShared
	}

	if config.VMType == "" {
		config.VMType = "standard"
	}

	config.AzureAuthConfig = ratelimitconfig.AzureAuthConfig{
		ARMClientConfig: azclient.ARMClientConfig{
			Cloud: string(cloud),
		},
	}

	return *config
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
		source   azure.Config
		expected azure.Config
		infra    *configv1.Infrastructure
		errMsg   string
	}{
		{
			name:   "Non Azure returns an error",
			source: azure.Config{},
			infra:  makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzureStackCloud),
			errMsg: fmt.Sprintf("invalid platform, expected CloudName to be %s", configv1.AzurePublicCloud),
		},
		{
			name:     "Azure sets the vmType to standard and cloud to AzurePublicCloud when neither is set",
			source:   azure.Config{},
			expected: makeExpectedConfig(&azure.Config{}, configv1.AzurePublicCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzurePublicCloud),
		},
		{
			name:     "Azure doesn't modify vmType if user set",
			source:   azure.Config{VMType: "vmss"},
			expected: makeExpectedConfig(&azure.Config{VMType: "vmss"}, configv1.AzurePublicCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzurePublicCloud),
		},
		{
			name: "Azure sets the cloud to AzurePublicCloud and keeps existing fields",
			source: azure.Config{
				ResourceGroup: "test-rg",
			},
			expected: makeExpectedConfig(&azure.Config{ResourceGroup: "test-rg"}, configv1.AzurePublicCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzurePublicCloud),
		},
		{
			name:     "Azure keeps the cloud set to AzurePublicCloud",
			source:   azure.Config{AzureAuthConfig: ratelimitconfig.AzureAuthConfig{ARMClientConfig: azclient.ARMClientConfig{Cloud: string(configv1.AzurePublicCloud)}}},
			expected: makeExpectedConfig(&azure.Config{}, configv1.AzurePublicCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzurePublicCloud),
		},
		{
			name:     "Azure keeps the cloud set to US Gov cloud",
			source:   azure.Config{AzureAuthConfig: ratelimitconfig.AzureAuthConfig{ARMClientConfig: azclient.ARMClientConfig{Cloud: string(configv1.AzureUSGovernmentCloud)}}},
			expected: makeExpectedConfig(&azure.Config{}, configv1.AzureUSGovernmentCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzureUSGovernmentCloud),
		},
		{
			name:     "Azure keeps the cloud set to China cloud",
			source:   azure.Config{AzureAuthConfig: ratelimitconfig.AzureAuthConfig{ARMClientConfig: azclient.ARMClientConfig{Cloud: string(configv1.AzureChinaCloud)}}},
			expected: makeExpectedConfig(&azure.Config{}, configv1.AzureChinaCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzureChinaCloud),
		},
		{
			name:     "Azure keeps the cloud set to German cloud",
			source:   azure.Config{AzureAuthConfig: ratelimitconfig.AzureAuthConfig{ARMClientConfig: azclient.ARMClientConfig{Cloud: string(configv1.AzureGermanCloud)}}},
			expected: makeExpectedConfig(&azure.Config{}, configv1.AzureGermanCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzureGermanCloud),
		},
		{
			name:   "Azure throws an error if the infra has an invalid cloud",
			source: azure.Config{},
			infra:  makeInfrastructureResource(configv1.AzurePlatformType, "AzureAnotherCloud"),
			errMsg: "status.platformStatus.azure.cloudName: Unsupported value: \"AzureAnotherCloud\": supported values: \"AzureChinaCloud\", \"AzureGermanCloud\", \"AzurePublicCloud\", \"AzureStackCloud\", \"AzureUSGovernmentCloud\"",
		},
		{
			name:     "Azure keeps the cloud set in the source when there is not one set in infrastructure",
			source:   azure.Config{AzureAuthConfig: ratelimitconfig.AzureAuthConfig{ARMClientConfig: azclient.ARMClientConfig{Cloud: string(configv1.AzurePublicCloud)}}},
			expected: makeExpectedConfig(&azure.Config{}, configv1.AzurePublicCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, ""),
		},
		{
			name:     "Azure sets the cloud to match the infrastructure if an empty string is provided in source",
			source:   azure.Config{AzureAuthConfig: ratelimitconfig.AzureAuthConfig{ARMClientConfig: azclient.ARMClientConfig{Cloud: ""}}},
			expected: makeExpectedConfig(&azure.Config{}, configv1.AzurePublicCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzurePublicCloud),
		},
		{
			name:     "Azure sets the cloud to match the infrastructure if an empty string is provided in source and the infrastructure is non standard",
			source:   azure.Config{AzureAuthConfig: ratelimitconfig.AzureAuthConfig{ARMClientConfig: azclient.ARMClientConfig{Cloud: ""}}},
			expected: makeExpectedConfig(&azure.Config{}, configv1.AzureUSGovernmentCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzureUSGovernmentCloud),
		},
		{
			name:   "Azure returns an error if the source config conflicts with the infrastructure",
			source: azure.Config{AzureAuthConfig: ratelimitconfig.AzureAuthConfig{ARMClientConfig: azclient.ARMClientConfig{Cloud: string(configv1.AzurePublicCloud)}}},
			infra:  makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzureUSGovernmentCloud),
			errMsg: "invalid user-provided cloud.conf: \\\"cloud\\\" field in user-provided\n\t\t\t\tcloud.conf conflicts with infrastructure object",
		},
		{
			name:     "Azure keeps the cloud set to AzurePublicCloud if the source is upper case",
			source:   azure.Config{AzureAuthConfig: ratelimitconfig.AzureAuthConfig{ARMClientConfig: azclient.ARMClientConfig{Cloud: "AZUREPUBLICCLOUD"}}},
			expected: makeExpectedConfig(&azure.Config{}, configv1.AzurePublicCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzurePublicCloud),
		},
	}

	format.CharactersAroundMismatchToInclude = 300
	format.TruncatedDiff = false
	format.MaxLength = 10_000

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
				var observed azure.Config
				g.Expect(json.Unmarshal([]byte(actual), &observed)).To(Succeed(), "Unmarshal of observed data should succeed")
				g.Expect(observed).Should(Equal(tc.expected))
			}
		})
	}
}
