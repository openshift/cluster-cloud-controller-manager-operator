package controllers

import (
	"io/ioutil"
	"os"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/apimachinery/pkg/api/equality"
)

func TestGetImagesFromJSONFile(t *testing.T) {
	tc := []struct {
		name           string
		path           string
		imagesContent  string
		expectedImages imagesReference
		expectError    bool
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
		expectError: true,
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
			expectError: true,
		},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			path := "./not_found"
			if tc.path != "" {
				file, err := ioutil.TempFile(os.TempDir(), tc.path)
				path = file.Name()
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()

				_, err = file.WriteString(tc.imagesContent)
				if err != nil {
					t.Fatal(err)
				}
			}

			images, err := getImagesFromJSONFile(path)
			if isErr := err != nil; isErr != tc.expectError {
				t.Fatalf("Unexpected error result: %v", err)
			}

			if !equality.Semantic.DeepEqual(images, tc.expectedImages) {
				t.Errorf("Images are not set correctly:\n%v\nexpected\n%v", images, tc.expectedImages)
			}
		})
	}
}

func TestGetProviderFromInfrastructure(t *testing.T) {
	tc := []struct {
		name           string
		infra          *configv1.Infrastructure
		expectPlatform configv1.PlatformType
		expectErr      bool
	}{{
		name:      "Passing empty infra causes error",
		infra:     nil,
		expectErr: true,
	}, {
		name: "No platform type causes error",
		infra: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				PlatformStatus: &configv1.PlatformStatus{},
			},
		},
		expectErr: true,
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
			platform, err := getProviderFromInfrastructure(tc.infra)
			if isErr := (err != nil); tc.expectErr != isErr {
				t.Fatalf("Unexpected error result: %v", err)
			}
			if platform != tc.expectPlatform {
				t.Errorf("Unexpected platform %s, expected %s", platform, tc.expectPlatform)
			}
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
		name:          "AWS platorm",
		platformType:  configv1.AWSPlatformType,
		expectedImage: "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
	}, {
		name:          "Azure platorm",
		platformType:  configv1.AzurePlatformType,
		expectedImage: "",
	}, {
		name:          "OpenStack platorm",
		platformType:  configv1.OpenStackPlatformType,
		expectedImage: "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager",
	}, {
		name:          "Unknown platorm",
		platformType:  "unknown",
		expectedImage: "",
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			image := getProviderControllerFromImages(tc.platformType, images)
			if image != tc.expectedImage {
				t.Errorf("Unexpected image %s, expected %s", image, tc.expectedImage)
			}
		})
	}
}

func TestComposeConfig(t *testing.T) {
	tc := []struct {
		name          string
		namespace     string
		platform      configv1.PlatformType
		imagesContent string
		expectConfig  operatorConfig
		expectError   bool
	}{{
		name:      "Unmarshal images from file for AWS",
		namespace: defaultManagementNamespace,
		platform:  configv1.AWSPlatformType,
		imagesContent: `{
    "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
    "cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
}`,
		expectConfig: operatorConfig{
			ControllerImage:  "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
			ManagedNamespace: defaultManagementNamespace,
			Platform:         configv1.AWSPlatformType,
		},
	}, {
		name:      "Unmarshal images from file for OpenStack",
		namespace: defaultManagementNamespace,
		platform:  configv1.OpenStackPlatformType,
		imagesContent: `{
    "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
    "cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
}`,
		expectConfig: operatorConfig{
			ControllerImage:  "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager",
			ManagedNamespace: defaultManagementNamespace,
			Platform:         configv1.OpenStackPlatformType,
		},
	}, {
		name:      "Unmarshal images from file for unknown platform returns nothing",
		namespace: "otherNamespace",
		platform:  configv1.NonePlatformType,
		imagesContent: `{
    "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
    "cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
}`,
		expectConfig: operatorConfig{
			ControllerImage:  "",
			ManagedNamespace: "otherNamespace",
			Platform:         configv1.NonePlatformType,
		},
	}, {
		name: "Broken JSON is rejected",
		imagesContent: `{
    "cloudControllerManagerAWS": BAD,
}`,
		expectError: true,
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			file, err := ioutil.TempFile(os.TempDir(), "images")
			path := file.Name()
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()

			_, err = file.WriteString(tc.imagesContent)
			if err != nil {
				t.Fatal(err)
			}

			r := &CloudOperatorReconciler{
				ImagesFile:       path,
				ManagedNamespace: tc.namespace,
			}
			config, err := r.composeConfig(tc.platform)
			if isErr := err != nil; isErr != tc.expectError {
				t.Fatalf("Unexpected error result: %v", err)
			}

			if !equality.Semantic.DeepEqual(config, tc.expectConfig) {
				t.Errorf("Config is not equal:\n%v\nexpected\n%v", config, tc.expectConfig)
			}
		})
	}
}
