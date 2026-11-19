package azure

import (
	"encoding/json"
	"fmt"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/stretchr/testify/assert"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/cloud-provider-azure/pkg/azclient"
	azureconsts "sigs.k8s.io/cloud-provider-azure/pkg/consts"
	azconfig "sigs.k8s.io/cloud-provider-azure/pkg/provider/config"
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
			assert.Len(t, resources, 10)
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
func makeExpectedConfig(config *azconfig.Config, cloud configv1.AzureCloudEnvironment) azconfig.Config {
	if config.ClusterServiceLoadBalancerHealthProbeMode == "" {
		config.ClusterServiceLoadBalancerHealthProbeMode = azureconsts.ClusterServiceLoadBalancerHealthProbeModeShared
	}

	if config.VMType == "" {
		config.VMType = "standard"
	}

	config.AzureClientConfig = azconfig.AzureClientConfig{
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
		source   azconfig.Config
		expected azconfig.Config
		infra    *configv1.Infrastructure
		errMsg   string
	}{
		{
			name:   "Non Azure returns an error",
			source: azconfig.Config{},
			infra:  makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzureStackCloud),
			errMsg: fmt.Sprintf("invalid platform, expected CloudName to be %s", configv1.AzurePublicCloud),
		},
		{
			name:     "Azure sets the vmType to standard and cloud to AzurePublicCloud when neither is set",
			source:   azconfig.Config{},
			expected: makeExpectedConfig(&azconfig.Config{}, configv1.AzurePublicCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzurePublicCloud),
		},
		{
			name:     "Azure doesn't modify vmType if user set",
			source:   azconfig.Config{VMType: "vmss"},
			expected: makeExpectedConfig(&azconfig.Config{VMType: "vmss"}, configv1.AzurePublicCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzurePublicCloud),
		},
		{
			name: "Azure sets the cloud to AzurePublicCloud and keeps existing fields",
			source: azconfig.Config{
				ResourceGroup: "test-rg",
			},
			expected: makeExpectedConfig(&azconfig.Config{ResourceGroup: "test-rg"}, configv1.AzurePublicCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzurePublicCloud),
		},
		{
			name:     "Azure keeps the cloud set to AzurePublicCloud",
			source:   azconfig.Config{AzureClientConfig: azconfig.AzureClientConfig{ARMClientConfig: azclient.ARMClientConfig{Cloud: string(configv1.AzurePublicCloud)}}},
			expected: makeExpectedConfig(&azconfig.Config{}, configv1.AzurePublicCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzurePublicCloud),
		},
		{
			name:     "Azure keeps the cloud set to US Gov cloud",
			source:   azconfig.Config{AzureClientConfig: azconfig.AzureClientConfig{ARMClientConfig: azclient.ARMClientConfig{Cloud: string(configv1.AzureUSGovernmentCloud)}}},
			expected: makeExpectedConfig(&azconfig.Config{}, configv1.AzureUSGovernmentCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzureUSGovernmentCloud),
		},
		{
			name:     "Azure keeps the cloud set to China cloud",
			source:   azconfig.Config{AzureClientConfig: azconfig.AzureClientConfig{ARMClientConfig: azclient.ARMClientConfig{Cloud: string(configv1.AzureChinaCloud)}}},
			expected: makeExpectedConfig(&azconfig.Config{}, configv1.AzureChinaCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzureChinaCloud),
		},
		{
			name:     "Azure keeps the cloud set to German cloud",
			source:   azconfig.Config{AzureClientConfig: azconfig.AzureClientConfig{ARMClientConfig: azclient.ARMClientConfig{Cloud: string(configv1.AzureGermanCloud)}}},
			expected: makeExpectedConfig(&azconfig.Config{}, configv1.AzureGermanCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzureGermanCloud),
		},
		{
			name:   "Azure throws an error if the infra has an invalid cloud",
			source: azconfig.Config{},
			infra:  makeInfrastructureResource(configv1.AzurePlatformType, "AzureAnotherCloud"),
			errMsg: "status.platformStatus.azure.cloudName: Unsupported value: \"AzureAnotherCloud\": supported values: \"AzureChinaCloud\", \"AzureGermanCloud\", \"AzurePublicCloud\", \"AzureStackCloud\", \"AzureUSGovernmentCloud\"",
		},
		{
			name:     "Azure keeps the cloud set in the source when there is not one set in infrastructure",
			source:   azconfig.Config{AzureClientConfig: azconfig.AzureClientConfig{ARMClientConfig: azclient.ARMClientConfig{Cloud: string(configv1.AzurePublicCloud)}}},
			expected: makeExpectedConfig(&azconfig.Config{}, configv1.AzurePublicCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, ""),
		},
		{
			name:     "Azure sets the cloud to match the infrastructure if an empty string is provided in source",
			source:   azconfig.Config{AzureClientConfig: azconfig.AzureClientConfig{ARMClientConfig: azclient.ARMClientConfig{Cloud: ""}}},
			expected: makeExpectedConfig(&azconfig.Config{}, configv1.AzurePublicCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzurePublicCloud),
		},
		{
			name:     "Azure sets the cloud to match the infrastructure if an empty string is provided in source and the infrastructure is non standard",
			source:   azconfig.Config{AzureClientConfig: azconfig.AzureClientConfig{ARMClientConfig: azclient.ARMClientConfig{Cloud: ""}}},
			expected: makeExpectedConfig(&azconfig.Config{}, configv1.AzureUSGovernmentCloud),
			infra:    makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzureUSGovernmentCloud),
		},
		{
			name: "Azure returns an error if the source config conflicts with the infrastructure",
			source: azconfig.Config{AzureClientConfig: azconfig.AzureClientConfig{ARMClientConfig: azclient.ARMClientConfig{
				Cloud: string(configv1.AzurePublicCloud)}},
			},
			infra:  makeInfrastructureResource(configv1.AzurePlatformType, configv1.AzureUSGovernmentCloud),
			errMsg: "invalid user-provided cloud.conf: \\\"cloud\\\" field in user-provided\n\t\t\t\tcloud.conf conflicts with infrastructure object",
		},
		{
			name: "Azure keeps the cloud set to AzurePublicCloud if the source is upper case",
			source: azconfig.Config{AzureClientConfig: azconfig.AzureClientConfig{ARMClientConfig: azclient.ARMClientConfig{
				Cloud: "AZUREPUBLICCLOUD"}},
			},
			expected: makeExpectedConfig(&azconfig.Config{}, configv1.AzurePublicCloud),
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

			actual, err := CloudConfigTransformer(string(src), tc.infra, nil, featuregates.NewFeatureGate(nil, nil))
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

func TestNodeIPFamilies(t *testing.T) {
	tc := []struct {
		name           string
		platformStatus *configv1.PlatformStatus
		expected       string
	}{{
		name:           "nil platform status",
		platformStatus: nil,
		expected:       "",
	}, {
		name:           "nil azure platform status",
		platformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType},
		expected:       "",
	}, {
		name: "unset ipFamily leaves the default in place",
		platformStatus: &configv1.PlatformStatus{
			Type:  configv1.AzurePlatformType,
			Azure: &configv1.AzurePlatformStatus{},
		},
		expected: "",
	}, {
		name: "single-stack IPv4 leaves the default in place",
		platformStatus: &configv1.PlatformStatus{
			Type:  configv1.AzurePlatformType,
			Azure: &configv1.AzurePlatformStatus{IPFamily: configv1.IPv4},
		},
		expected: "",
	}, {
		name: "dual-stack IPv6 primary",
		platformStatus: &configv1.PlatformStatus{
			Type:  configv1.AzurePlatformType,
			Azure: &configv1.AzurePlatformStatus{IPFamily: configv1.DualStackIPv6Primary},
		},
		expected: "ipv6,ipv4",
	}, {
		name: "dual-stack IPv4 primary",
		platformStatus: &configv1.PlatformStatus{
			Type:  configv1.AzurePlatformType,
			Azure: &configv1.AzurePlatformStatus{IPFamily: configv1.DualStackIPv4Primary},
		},
		expected: "ipv4,ipv6",
	}}

	for _, tt := range tc {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, nodeIPFamilies(tt.platformStatus))
		})
	}
}

// TestNodeIPFamiliesRendering asserts that the computed value actually reaches
// the cloud node manager's command line, and that the argument is omitted
// entirely when the cluster is not dual-stack.
func TestNodeIPFamiliesRendering(t *testing.T) {
	tc := []struct {
		name     string
		ipFamily configv1.IPFamilyType
		expected string
	}{{
		name:     "dual-stack IPv6 primary passes the argument",
		ipFamily: configv1.DualStackIPv6Primary,
		expected: "--node-ip-families=ipv6,ipv4",
	}, {
		name:     "single-stack omits the argument",
		ipFamily: configv1.IPv4,
		expected: "",
	}}

	for _, tt := range tc {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			assets, err := NewProviderAssets(config.OperatorConfig{
				ManagedNamespace: "my-cool-namespace",
				ImagesReference: config.ImagesReference{
					CloudControllerManagerAzure:    "CloudControllerManagerAzure",
					CloudNodeManagerAzure:          "CloudNodeManagerAzure",
					CloudControllerManagerOperator: "CloudControllerManagerOperator",
				},
				InfrastructureName: "infra",
				PlatformStatus: &configv1.PlatformStatus{
					Type:  configv1.AzurePlatformType,
					Azure: &configv1.AzurePlatformStatus{IPFamily: tt.ipFamily},
				},
			})
			g.Expect(err).NotTo(HaveOccurred())

			var found bool
			for _, obj := range assets.GetRenderedResources() {
				ds, ok := obj.(*appsv1.DaemonSet)
				if !ok || ds.Name != "azure-cloud-node-manager" {
					continue
				}
				found = true
				cmd := fmt.Sprintf("%v", ds.Spec.Template.Spec.Containers[0].Command)
				if tt.expected == "" {
					g.Expect(cmd).NotTo(ContainSubstring("--node-ip-families"))
				} else {
					g.Expect(cmd).To(ContainSubstring(tt.expected))
				}
			}
			g.Expect(found).To(BeTrue(), "azure-cloud-node-manager DaemonSet not found in rendered resources")
		})
	}
}
