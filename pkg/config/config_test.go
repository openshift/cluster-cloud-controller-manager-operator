package config

import (
	"io/ioutil"
	"os"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
)

func TestGetImagesFromJSONFile(t *testing.T) {
	tc := []struct {
		name           string
		path           string
		imagesContent  string
		expectedImages imagesReference
		expectError    string
	}{{
		name: "Unmarshal images from file",
		path: "images_file",
		imagesContent: `{
    "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
    "cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
}`,
		expectedImages: imagesReference{
			CloudControllerManagerAWS:       "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
			CloudControllerManagerOpenStack: "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager",
		},
	}, {
		name:        "Error on non present file",
		expectError: "open not_found: no such file or directory",
	}, {
		name: "Partial content is accepted",
		path: "images_file",
		imagesContent: `{
    "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager"
}`,
		expectedImages: imagesReference{
			CloudControllerManagerAWS: "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
		},
	}, {
		name: "Duplicate content takes precedence and is accepted",
		path: "images_file",
		imagesContent: `{
    "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
    "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:different"
}`,
		expectedImages: imagesReference{
			CloudControllerManagerAWS: "registry.ci.openshift.org/openshift:different",
		},
	}, {
		name: "Unknown image name is ignored",
		path: "images_file",
		imagesContent: `{
    "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
    "cloudControllerManagerUnknown": "registry.ci.openshift.org/openshift:unknown-cloud-controller-manager",
    "cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
}`,
		expectedImages: imagesReference{
			CloudControllerManagerAWS:       "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
			CloudControllerManagerOpenStack: "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager",
		},
	},
		{
			name: "Broken JSON is rejected",
			path: "images_file",
			imagesContent: `{
    "cloudControllerManagerAWS": BAD,
}`,
			expectError: "invalid character 'B' looking for beginning of value",
		},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			path := "./not_found"
			if tc.path != "" {
				file, err := ioutil.TempFile(os.TempDir(), tc.path)
				path = file.Name()
				assert.NoError(t, err)
				defer file.Close()

				_, err = file.WriteString(tc.imagesContent)
				assert.NoError(t, err)
			}

			images, err := getImagesFromJSONFile(path)
			if tc.expectError != "" {
				assert.EqualError(t, err, tc.expectError)
			} else {
				assert.NoError(t, err)
			}

			assert.EqualValues(t, tc.expectedImages, images)
		})
	}
}

func TestGetProviderFromInfrastructure(t *testing.T) {
	tc := []struct {
		name           string
		infra          *configv1.Infrastructure
		expectPlatform configv1.PlatformType
		expectErr      string
	}{{
		name:      "Passing empty infra causes error",
		infra:     nil,
		expectErr: "platform status is not populated on infrastructure",
	}, {
		name: "No platform type causes error",
		infra: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				PlatformStatus: &configv1.PlatformStatus{},
			},
		},
		expectErr: "no platform provider found on infrastructure",
	}, {
		name: "All good",
		infra: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				PlatformStatus: &configv1.PlatformStatus{
					Type: "some_platform",
				},
			},
		},
		expectPlatform: "some_platform",
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			platform, err := GetProviderFromInfrastructure(tc.infra)
			if tc.expectErr != "" {
				assert.EqualError(t, err, tc.expectErr)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, platform, tc.expectPlatform)
		})
	}
}

func TestGetProviderControllerFromImages(t *testing.T) {
	images := imagesReference{
		CloudControllerManagerAWS:       "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
		CloudControllerManagerOpenStack: "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager",
	}

	tc := []struct {
		name          string
		platformType  configv1.PlatformType
		expectedImage string
	}{{
		name:          "AWS platform",
		platformType:  configv1.AWSPlatformType,
		expectedImage: "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
	}, {
		name:          "Azure platform",
		platformType:  configv1.AzurePlatformType,
		expectedImage: "",
	}, {
		name:          "OpenStack platform",
		platformType:  configv1.OpenStackPlatformType,
		expectedImage: "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager",
	}, {
		name:          "Unknown platform",
		platformType:  "unknown",
		expectedImage: "",
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			image := getCloudControllerManagerFromImages(tc.platformType, images)
			assert.Equal(t, tc.expectedImage, image)
		})
	}
}

func TestGetNodeControllerFromImages(t *testing.T) {
	images := imagesReference{
		CloudControllerManagerAWS:       "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
		CloudControllerManagerAzure:     "registry.ci.openshift.org/openshift:azure-cloud-controller-manager",
		CloudNodeManagerAzure:           "registry.ci.openshift.org/openshift:azure-cloud-node-manager",
		CloudControllerManagerOpenStack: "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager",
	}

	tc := []struct {
		name          string
		platformType  configv1.PlatformType
		expectedImage string
	}{{
		name:          "AWS platform",
		platformType:  configv1.AWSPlatformType,
		expectedImage: "",
	}, {
		name:          "Azure platform",
		platformType:  configv1.AzurePlatformType,
		expectedImage: "registry.ci.openshift.org/openshift:azure-cloud-node-manager",
	}, {
		name:          "OpenStack platform",
		platformType:  configv1.OpenStackPlatformType,
		expectedImage: "",
	}, {
		name:          "Unknown platform",
		platformType:  "unknown",
		expectedImage: "",
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			image := getCloudNodeManagerFromImages(tc.platformType, images)
			assert.Equal(t, tc.expectedImage, image)
		})
	}
}

func TestComposeConfig(t *testing.T) {
	defaultManagementNamespace := "test-namespace"

	tc := []struct {
		name          string
		namespace     string
		infra         *configv1.Infrastructure
		imagesContent string
		expectConfig  OperatorConfig
		expectError   string
	}{{
		name:      "Unmarshal images from file for AWS",
		namespace: defaultManagementNamespace,
		infra: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.AWSPlatformType,
				},
			},
		},
		imagesContent: `{
    "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
    "cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
}`,
		expectConfig: OperatorConfig{
			ControllerImage:  "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
			ManagedNamespace: defaultManagementNamespace,
			Platform:         configv1.AWSPlatformType,
		},
	}, {
		name:      "Unmarshal images from file for OpenStack",
		namespace: defaultManagementNamespace,
		infra: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.OpenStackPlatformType,
				},
			},
		},
		imagesContent: `{
    "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
    "cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
}`,
		expectConfig: OperatorConfig{
			ControllerImage:  "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager",
			ManagedNamespace: defaultManagementNamespace,
			Platform:         configv1.OpenStackPlatformType,
		},
	}, {
		name:      "Unmarshal images from file for unknown platform returns nothing",
		namespace: "otherNamespace",
		infra: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.NonePlatformType,
				},
			},
		},
		imagesContent: `{
    "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
    "cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
}`,
		expectConfig: OperatorConfig{
			ControllerImage:  "",
			ManagedNamespace: "otherNamespace",
			Platform:         configv1.NonePlatformType,
		},
	}, {
		name: "Broken JSON is rejected",
		infra: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				ControlPlaneTopology: configv1.SingleReplicaTopologyMode,
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.AWSPlatformType,
				},
			},
		},
		imagesContent: `{
    "cloudControllerManagerAWS": BAD,
}`,
		expectError: "invalid character 'B' looking for beginning of value",
	}, {
		name:      "Single Replica",
		namespace: defaultManagementNamespace,
		infra: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				ControlPlaneTopology: configv1.SingleReplicaTopologyMode,
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.OpenStackPlatformType,
				},
			},
		},
		imagesContent: `{
    "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
    "cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
}`,
		expectConfig: OperatorConfig{
			ControllerImage:  "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager",
			ManagedNamespace: defaultManagementNamespace,
			Platform:         configv1.OpenStackPlatformType,
			IsSingleReplica:  true,
		},
	}, {
		name:        "Empty infrastructure should return error",
		expectError: "platform status is not populated on infrastructure",
	}, {
		name:        "Unpopulated infrastructure should return error",
		infra:       &configv1.Infrastructure{},
		expectError: "platform status is not populated on infrastructure",
	}, {
		name: "Unpopulated infrastructure status should return error",
		infra: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				PlatformStatus: nil,
			},
		},
		expectError: "platform status is not populated on infrastructure",
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			file, err := ioutil.TempFile(os.TempDir(), "images")
			path := file.Name()
			assert.NoError(t, err)
			defer file.Close()

			_, err = file.WriteString(tc.imagesContent)
			assert.NoError(t, err)

			config, err := ComposeConfig(tc.infra, path, tc.namespace)
			if tc.expectError != "" {
				assert.EqualError(t, err, tc.expectError)
			} else {
				assert.NoError(t, err)
			}

			assert.EqualValues(t, config, tc.expectConfig)
		})
	}
}
