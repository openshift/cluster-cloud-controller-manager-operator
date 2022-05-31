package powervs

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configv1 "github.com/openshift/api/config/v1"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"

	. "github.com/onsi/gomega"
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
			initErrMsg: "powervs: missed images in config: CloudControllerManager: non zero value required",
		}, {
			name: "No platform status",
			config: config.OperatorConfig{
				ImagesReference: config.ImagesReference{
					CloudControllerManagerPowerVS: "CloudControllerManagerPowerVS",
				},
			},
			initErrMsg: "can not construct template values for powervs assets: cloudproviderName: non zero value required",
		}, {
			name: "Minimal allowed config",
			config: config.OperatorConfig{
				ImagesReference: config.ImagesReference{
					CloudControllerManagerPowerVS: "CloudControllerManagerPowerVS",
				},
				PlatformStatus: &configv1.PlatformStatus{Type: configv1.PowerVSPlatformType},
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

func makeInfrastructureResource(platform configv1.PlatformType, serviceEndpoints []configv1.PowerVSServiceEndpoint) *configv1.Infrastructure {
	return &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				Type: platform,
				PowerVS: &configv1.PowerVSPlatformStatus{
					ServiceEndpoints: serviceEndpoints,
				},
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
}

func makeCloudConfig() string {
	return `[global]
version = 1.1.0
[kubernetes]
config-file = ""
[provider]
accountID = account-id
clusterID = cluster-id
cluster-default-provider = g2
region = eu-gb
g2Credentials = /etc/vpc/ibmcloud_api_key
g2ResourceGroupName = powervs-ipi-resource-group
g2VpcName = powervs-ipi
g2workerServiceAccountID = account-id
g2VpcSubnetNames = subnet2
powerVSCloudInstanceID = cloud-instance-id
powerVSRegion = lon
powerVSZone = lon4`
}

func TestCloudConfigTransformer(t *testing.T) {

	tc := []struct {
		name     string
		source   string
		infra    *configv1.Infrastructure
		errMsg   string
		expected string
	}{
		{
			name:   "Invalid platform",
			source: "",
			infra:  makeInfrastructureResource(configv1.AWSPlatformType, nil),
			errMsg: "invalid platform, expected to be PowerVS",
		}, {
			name:     "Empty config",
			source:   "",
			infra:    makeInfrastructureResource(configv1.PowerVSPlatformType, nil),
			expected: "",
		}, {
			name:     "with no service endpoints",
			source:   makeCloudConfig(),
			infra:    makeInfrastructureResource(configv1.PowerVSPlatformType, nil),
			expected: makeCloudConfig(),
		},
		{
			name:   "with service endpoints",
			source: makeCloudConfig(),
			infra: makeInfrastructureResource(configv1.PowerVSPlatformType, []configv1.PowerVSServiceEndpoint{
				{
					Name: "iam",
					URL:  "https://iam.test.cloud.ibm.com",
				},
				{
					Name: "rc",
					URL:  "https://resource-controller.test.cloud.ibm.com",
				},
				{
					Name: "pe",
					URL:  "https://dal.power-iaas.test.cloud.ibm.com",
				},
			}),
			expected: `[global]
version = 1.1.0
[kubernetes]
config-file = ""
[provider]
accountID = account-id
clusterID = cluster-id
cluster-default-provider = g2
region = eu-gb
g2Credentials = /etc/vpc/ibmcloud_api_key
g2ResourceGroupName = powervs-ipi-resource-group
g2VpcName = powervs-ipi
g2workerServiceAccountID = account-id
g2VpcSubnetNames = subnet2
powerVSCloudInstanceID = cloud-instance-id
powerVSRegion = lon
powerVSZone = lon4
[ServiceOverride "0"]
	Service = iam
	URL = https://iam.test.cloud.ibm.com

[ServiceOverride "1"]
	Service = rc
	URL = https://resource-controller.test.cloud.ibm.com

[ServiceOverride "2"]
	Service = pe
	URL = https://dal.power-iaas.test.cloud.ibm.com`,
		},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			actual, err := CloudConfigTransformer(tc.source, tc.infra, nil)
			if tc.errMsg != "" {
				g.Expect(err).Should(MatchError(tc.errMsg))
				return
			} else {
				g.Expect(err).ToNot(HaveOccurred())
				actual := strings.TrimSpace(actual)
				g.Expect(actual).Should(Equal(tc.expected))
			}
		})
	}
}
