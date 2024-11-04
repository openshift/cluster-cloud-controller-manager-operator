package ibm

import (
	"fmt"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stretchr/testify/assert"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
)

const (
	infraCloudConfName = "test-config"
	infraCloudConfKey  = "conf-key"
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

func TestCloudConfigTransformer(t *testing.T) {

	tc := []struct {
		name                          string
		source                        string
		infra                         *configv1.Infrastructure
		network                       *configv1.Network
		expectedServiceInfraEndpoints []configv1.IBMCloudServiceEndpoint
		expectedErr                   error
		complexErr                    bool // for errors that come from diff package and can't be easily checked via mock, if set validate error contains expected err string
		expectedConfig                string
	}{
		{
			name:           "Unhappy path unsupported platform",
			source:         "",
			infra:          createInfraResource(configv1.OpenStackPlatformType, nil),
			expectedErr:    fmt.Errorf("invalid platform for IBM cloud config transformer"),
			expectedConfig: "",
		},
		{
			name:           "Unhappy path provider section missing",
			source:         "",
			infra:          createInfraResource(configv1.IBMCloudPlatformType, nil),
			expectedErr:    fmt.Errorf("fatal format error, provided source does not have expected provider section"),
			expectedConfig: "",
		},
		{
			name:   "Unhappy path empty URL override",
			source: createConfigNoOverrides(),
			infra: createInfraResource(configv1.IBMCloudPlatformType, []configv1.IBMCloudServiceEndpoint{
				{Name: configv1.IBMCloudServiceIAM, URL: ""}}),
			expectedErr:    fmt.Errorf("failed to validate submitted override, one of URL or Name was empty"),
			expectedConfig: "",
		},
		{
			name:   "Unhappy path empty name for override",
			source: createConfigNoOverrides(),
			infra: createInfraResource(configv1.IBMCloudPlatformType, []configv1.IBMCloudServiceEndpoint{
				{Name: "", URL: "something"}}),
			expectedErr:    fmt.Errorf("failed to validate submitted override, one of URL or Name was empty"),
			expectedConfig: "",
		},
		{
			name:   "Unhappy path duplicate override",
			source: createConfigNoOverrides(),
			infra: createInfraResource(configv1.IBMCloudPlatformType, []configv1.IBMCloudServiceEndpoint{
				{Name: configv1.IBMCloudServiceIAM, URL: "https://private.iam.cloud.ibm.com"},
				{Name: configv1.IBMCloudServiceIAM, URL: "https://private.iam.cloud.ibm.com"}}),
			expectedErr:    fmt.Errorf("error, service endpoint override contained duplicate entries for same name %s", configv1.IBMCloudServiceIAM),
			expectedConfig: "",
		},
		{
			name:   "Unhappy path override doesnt conform to path rules using unsupported path keyword",
			source: createConfigNoOverrides(),
			infra: createInfraResource(configv1.IBMCloudPlatformType, []configv1.IBMCloudServiceEndpoint{
				{Name: configv1.IBMCloudServiceIAM, URL: "https://private.iam.cloud.ibm.com/something/v1"}}),
			expectedErr:    fmt.Errorf("error invalid path present in URI, /something/v1 was provided"),
			expectedConfig: "",
		},
		{
			name:   "Unhappy path override doesnt conform to path rules using only API path keyword with no versioning",
			source: createConfigNoOverrides(),
			infra: createInfraResource(configv1.IBMCloudPlatformType, []configv1.IBMCloudServiceEndpoint{
				{Name: configv1.IBMCloudServiceIAM, URL: "https://private.iam.cloud.ibm.com/api"}}),
			expectedErr:    fmt.Errorf("error invalid path present in URI, /api was provided"),
			expectedConfig: "",
		},
		{
			name:   "Unhappy path override is a DNE endpoint",
			source: createConfigNoOverrides(),
			infra: createInfraResource(configv1.IBMCloudPlatformType, []configv1.IBMCloudServiceEndpoint{
				{Name: configv1.IBMCloudServiceIAM, URL: "https://fakeendpoint.dne.cloud.ibm.com/api"}}),
			complexErr:     true,
			expectedErr:    fmt.Errorf("error validating host exists, with error: lookup fakeendpoint.dne.cloud.ibm.com: no such host"),
			expectedConfig: "",
		},
		{
			name:   "Happy path single IAM override",
			source: createConfigNoOverrides(),
			infra: createInfraResource(configv1.IBMCloudPlatformType, []configv1.IBMCloudServiceEndpoint{
				{Name: configv1.IBMCloudServiceIAM, URL: "https://private.iam.cloud.ibm.com"}}),
			expectedErr: nil,
			expectedServiceInfraEndpoints: []configv1.IBMCloudServiceEndpoint{
				{Name: configv1.IBMCloudServiceIAM, URL: "https://private.iam.cloud.ibm.com"}},
			expectedConfig: createConfigSingleIAMOverrides(),
		},
		{
			name:   "Happy path multi IAM override",
			source: createConfigNoOverrides(),
			infra: createInfraResource(configv1.IBMCloudPlatformType, []configv1.IBMCloudServiceEndpoint{
				{Name: configv1.IBMCloudServiceIAM, URL: "https://private.iam.cloud.ibm.com"},
				{Name: configv1.IBMCloudServiceVPC, URL: "https://us-south.private.iaas.cloud.ibm.com/v4"},
				{Name: configv1.IBMCloudServiceResourceManager, URL: "https://private.iam.cloud.ibm.com/v99"}}),
			expectedErr: nil,
			expectedServiceInfraEndpoints: []configv1.IBMCloudServiceEndpoint{
				{Name: configv1.IBMCloudServiceIAM, URL: "https://private.iam.cloud.ibm.com"},
				{Name: configv1.IBMCloudServiceVPC, URL: "https://us-south.private.iaas.cloud.ibm.com/v4"},
				{Name: configv1.IBMCloudServiceResourceManager, URL: "https://private.iam.cloud.ibm.com/v99"}},
			expectedConfig: createConfigAllOverrides(),
		},
		{
			name:   "Happy path multi IAM override, previous overrides were present",
			source: createConfigAllOverrides(),
			infra: createInfraResource(configv1.IBMCloudPlatformType, []configv1.IBMCloudServiceEndpoint{
				{Name: configv1.IBMCloudServiceIAM, URL: "https://private.iam.cloud.ibm.com/v1"},
				{Name: configv1.IBMCloudServiceVPC, URL: "https://us-south.private.iaas.cloud.ibm.com/v4"},
				{Name: configv1.IBMCloudServiceResourceManager, URL: "https://private.iam.cloud.ibm.com/v99"}}),
			expectedErr: nil,
			expectedServiceInfraEndpoints: []configv1.IBMCloudServiceEndpoint{
				{Name: configv1.IBMCloudServiceIAM, URL: "https://private.iam.cloud.ibm.com/v1"},
				{Name: configv1.IBMCloudServiceVPC, URL: "https://us-south.private.iaas.cloud.ibm.com/v4"},
				{Name: configv1.IBMCloudServiceResourceManager, URL: "https://private.iam.cloud.ibm.com/v99"}},
			expectedConfig: createConfigAllOverridesAlternate(),
		},
		{
			name:                          "Happy path spec has removed overrides, previous overrides were present",
			source:                        createConfigAllOverrides(),
			infra:                         createInfraResource(configv1.IBMCloudPlatformType, []configv1.IBMCloudServiceEndpoint{}),
			expectedErr:                   nil,
			expectedServiceInfraEndpoints: []configv1.IBMCloudServiceEndpoint{},
			expectedConfig:                createConfigNoOverrides(),
		},
		{
			name:   "Happy path single IAM override with diff path supporting /api",
			source: createConfigNoOverrides(),
			infra: createInfraResource(configv1.IBMCloudPlatformType, []configv1.IBMCloudServiceEndpoint{
				{Name: configv1.IBMCloudServiceIAM, URL: "https://private.iam.cloud.ibm.com/api/v2"}}),
			expectedErr: nil,
			expectedServiceInfraEndpoints: []configv1.IBMCloudServiceEndpoint{
				{Name: configv1.IBMCloudServiceIAM, URL: "https://private.iam.cloud.ibm.com/api/v2"}},
			expectedConfig: createConfigSingleIAMOverridesDiffAPIEndpoint(),
		},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			actualConfig, err := CloudConfigTransformer(tc.source, tc.infra, nil)
			if tc.complexErr {
				// contains expected err string
				assert.Contains(t, err.Error(), tc.expectedErr.Error())
			} else {
				assert.Equal(t, tc.expectedErr, err)
			}
			assert.Equal(t, tc.expectedConfig, actualConfig)
			assert.Equal(t, tc.expectedServiceInfraEndpoints, tc.infra.Status.PlatformStatus.IBMCloud.ServiceEndpoints)
		})
	}
}

const multiOverride = `[global]
version = 1.1.0

[kubernetes]
config-file = cf

[provider]
accountID                = 1e1f75646aef447814a6d907cc83fb3c
clusterID                = ocp4-8pxks
cluster-default-provider = g2
region                   = us-east
g2Credentials            = /etc/vpc/ibmcloud_api_key
g2ResourceGroupName      = ocp4-8pxks-rg
g2VpcName                = ocp4-8pxks-vpc
g2workerServiceAccountID = 1e1f75646aef447814a6d907cc83fb3c
g2VpcSubnetNames         = ocp4-8pxks-subnet-compute-us-east-1,ocp4-8pxks-subnet-compute-us-east-2,ocp4-8pxks-subnet-compute-us-east-3,ocp4-8pxks-subnet-control-plane-us-east-1,ocp4-8pxks-subnet-control-plane-us-east-2,ocp4-8pxks-subnet-control-plane-us-east-3
iamEndpointOverride      = https://private.iam.cloud.ibm.com
g2EndpointOverride       = https://us-south.private.iaas.cloud.ibm.com/v4
rmEndpointOverride       = https://private.iam.cloud.ibm.com/v99
`

const multiOverrideAlternate = `[global]
version = 1.1.0

[kubernetes]
config-file = cf

[provider]
accountID                = 1e1f75646aef447814a6d907cc83fb3c
clusterID                = ocp4-8pxks
cluster-default-provider = g2
region                   = us-east
g2Credentials            = /etc/vpc/ibmcloud_api_key
g2ResourceGroupName      = ocp4-8pxks-rg
g2VpcName                = ocp4-8pxks-vpc
g2workerServiceAccountID = 1e1f75646aef447814a6d907cc83fb3c
g2VpcSubnetNames         = ocp4-8pxks-subnet-compute-us-east-1,ocp4-8pxks-subnet-compute-us-east-2,ocp4-8pxks-subnet-compute-us-east-3,ocp4-8pxks-subnet-control-plane-us-east-1,ocp4-8pxks-subnet-control-plane-us-east-2,ocp4-8pxks-subnet-control-plane-us-east-3
iamEndpointOverride      = https://private.iam.cloud.ibm.com/v1
g2EndpointOverride       = https://us-south.private.iaas.cloud.ibm.com/v4
rmEndpointOverride       = https://private.iam.cloud.ibm.com/v99
`

const singleOverride = `[global]
version = 1.1.0

[kubernetes]
config-file = cf

[provider]
accountID                = 1e1f75646aef447814a6d907cc83fb3c
clusterID                = ocp4-8pxks
cluster-default-provider = g2
region                   = us-east
g2Credentials            = /etc/vpc/ibmcloud_api_key
g2ResourceGroupName      = ocp4-8pxks-rg
g2VpcName                = ocp4-8pxks-vpc
g2workerServiceAccountID = 1e1f75646aef447814a6d907cc83fb3c
g2VpcSubnetNames         = ocp4-8pxks-subnet-compute-us-east-1,ocp4-8pxks-subnet-compute-us-east-2,ocp4-8pxks-subnet-compute-us-east-3,ocp4-8pxks-subnet-control-plane-us-east-1,ocp4-8pxks-subnet-control-plane-us-east-2,ocp4-8pxks-subnet-control-plane-us-east-3
iamEndpointOverride      = https://private.iam.cloud.ibm.com
`

const noOverride = `[global]
version = 1.1.0

[kubernetes]
config-file = cf

[provider]
accountID                = 1e1f75646aef447814a6d907cc83fb3c
clusterID                = ocp4-8pxks
cluster-default-provider = g2
region                   = us-east
g2Credentials            = /etc/vpc/ibmcloud_api_key
g2ResourceGroupName      = ocp4-8pxks-rg
g2VpcName                = ocp4-8pxks-vpc
g2workerServiceAccountID = 1e1f75646aef447814a6d907cc83fb3c
g2VpcSubnetNames         = ocp4-8pxks-subnet-compute-us-east-1,ocp4-8pxks-subnet-compute-us-east-2,ocp4-8pxks-subnet-compute-us-east-3,ocp4-8pxks-subnet-control-plane-us-east-1,ocp4-8pxks-subnet-control-plane-us-east-2,ocp4-8pxks-subnet-control-plane-us-east-3
`

const singleOverrideDiffAPIEndpoint = `[global]
version = 1.1.0

[kubernetes]
config-file = cf

[provider]
accountID                = 1e1f75646aef447814a6d907cc83fb3c
clusterID                = ocp4-8pxks
cluster-default-provider = g2
region                   = us-east
g2Credentials            = /etc/vpc/ibmcloud_api_key
g2ResourceGroupName      = ocp4-8pxks-rg
g2VpcName                = ocp4-8pxks-vpc
g2workerServiceAccountID = 1e1f75646aef447814a6d907cc83fb3c
g2VpcSubnetNames         = ocp4-8pxks-subnet-compute-us-east-1,ocp4-8pxks-subnet-compute-us-east-2,ocp4-8pxks-subnet-compute-us-east-3,ocp4-8pxks-subnet-control-plane-us-east-1,ocp4-8pxks-subnet-control-plane-us-east-2,ocp4-8pxks-subnet-control-plane-us-east-3
iamEndpointOverride      = https://private.iam.cloud.ibm.com/api/v2
`

func createConfigAllOverrides() string {
	return multiOverride
}

func createConfigAllOverridesAlternate() string {
	return multiOverrideAlternate
}

func createConfigSingleIAMOverrides() string {
	return singleOverride
}

func createConfigNoOverrides() string {
	return noOverride
}

func createConfigSingleIAMOverridesDiffAPIEndpoint() string {
	return singleOverrideDiffAPIEndpoint
}

func createInfraResource(platform configv1.PlatformType, serviceEndpoints []configv1.IBMCloudServiceEndpoint) *configv1.Infrastructure {
	return &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				Type:     platform,
				IBMCloud: &configv1.IBMCloudPlatformStatus{},
			},
		},
		Spec: configv1.InfrastructureSpec{
			CloudConfig: configv1.ConfigMapFileReference{
				Name: infraCloudConfName,
				Key:  infraCloudConfKey,
			},
			PlatformSpec: configv1.PlatformSpec{
				Type: platform,
				IBMCloud: &configv1.IBMCloudPlatformSpec{
					ServiceEndpoints: serviceEndpoints,
				},
			},
		},
	}
}
