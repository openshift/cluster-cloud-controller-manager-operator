package config

import (
	configv1 "github.com/openshift/api/config/v1"
	"io/ioutil"
	corev1 "k8s.io/api/core/v1"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReadJsonFile(t *testing.T) {
	tc := []struct {
		name                string
		filename            string
		expectedErrMsg      string
		createFile          bool
		fileContent         string
		expectedReturnValue []byte
	}{{
		name:                "File not found",
		filename:            "not_here",
		createFile:          false,
		expectedErrMsg:      "",
		expectedReturnValue: []byte{},
	}, {
		name:                "Successful read",
		filename:            "here",
		expectedErrMsg:      "",
		createFile:          true,
		fileContent:         `{"foo": "bar"}`,
		expectedReturnValue: []byte(`{"foo": "bar"}`),
	}, {
		name:           "Invalid json",
		filename:       "filename",
		expectedErrMsg: "images file is not a valid json",
		createFile:     true,
		fileContent:    `{{`,
	}}
	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			var imagesFilePath string
			if tc.createFile {
				file, err := ioutil.TempFile(os.TempDir(), tc.filename)
				imagesFilePath = file.Name()
				assert.NoError(t, err)
				defer file.Close()

				_, err = file.WriteString(tc.fileContent)
				assert.NoError(t, err)
			}

			content, err := readImagesJSONFile(imagesFilePath)
			if tc.expectedErrMsg != "" {
				assert.EqualError(t, err, tc.expectedErrMsg)
			}
			assert.Equal(t, content, tc.expectedReturnValue)
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

func TestGetImagesFromImagesConfigMap(t *testing.T) {
	validImagesContent := `{
   "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
   "cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
	}`
	tc := []struct {
		name            string
		imagesConfigMap *corev1.ConfigMap
		expectedContent []byte
		expectErr       string
	}{{
		name: "Image collected successfully",
		imagesConfigMap: &corev1.ConfigMap{
			Data: map[string]string{
				configMapImagesKey: validImagesContent,
			},
		},
		expectedContent: []byte(validImagesContent),
	}, {
		name: "Images are not collected if the data key differs",
		imagesConfigMap: &corev1.ConfigMap{
			Data: map[string]string{
				"some-images.json": `{
   "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
   "cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
}`,
			}},
		expectErr: "unable to find images key \"images.json\" in ConfigMap /",
	}, {
		name: "Images are not collected if the file content is corrupted",
		imagesConfigMap: &corev1.ConfigMap{
			Data: map[string]string{
				configMapImagesKey: "",
			}},
		expectErr: "ConfigMap / does not contain a valid images json",
	}, {
		name:      "Empty config map is rejected",
		expectErr: "unable to find Data field in provided ConfigMap",
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			images, err := getImagesContentFromConfigMap(tc.imagesConfigMap)
			if tc.expectErr != "" {
				assert.EqualError(t, err, tc.expectErr)
			} else {
				assert.NoError(t, err)
			}
			assert.EqualValues(t, tc.expectedContent, images)
		})
	}
}

//
//func TestComposeConfig(t *testing.T) {
//	defaultManagementNamespace := "test-namespace"
//
//	tc := []struct {
//		name          string
//		namespace     string
//		platform      configv1.PlatformType
//		imagesContent string
//		expectConfig  OperatorConfig
//		expectError   string
//	}{{
//		name:      "Unmarshal images from file for AWS",
//		namespace: defaultManagementNamespace,
//		platform:  configv1.AWSPlatformType,
//		imagesContent: `{
//    "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
//    "cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
//}`,
//		expectConfig: OperatorConfig{
//			ControllerImage:  "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
//			ManagedNamespace: defaultManagementNamespace,
//			Platform:         configv1.AWSPlatformType,
//		},
//	}, {
//		name:      "Unmarshal images from file for OpenStack",
//		namespace: defaultManagementNamespace,
//		platform:  configv1.OpenStackPlatformType,
//		imagesContent: `{
//    "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
//    "cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
//}`,
//		expectConfig: OperatorConfig{
//			ControllerImage:  "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager",
//			ManagedNamespace: defaultManagementNamespace,
//			Platform:         configv1.OpenStackPlatformType,
//		},
//	}, {
//		name:      "Unmarshal images from file for unknown platform returns nothing",
//		namespace: "otherNamespace",
//		platform:  configv1.NonePlatformType,
//		imagesContent: `{
//    "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
//    "cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
//}`,
//		expectConfig: OperatorConfig{
//			ControllerImage:  "",
//			ManagedNamespace: "otherNamespace",
//			Platform:         configv1.NonePlatformType,
//		},
//	}, {
//		name: "Broken JSON is rejected",
//		imagesContent: `{
//    "cloudControllerManagerAWS": BAD,
//}`,
//		expectError: "invalid character 'B' looking for beginning of value",
//	}}
//
//	for _, tc := range tc {
//		t.Run(tc.name, func(t *testing.T) {
//			file, err := ioutil.TempFile(os.TempDir(), "images")
//			path := file.Name()
//			assert.NoError(t, err)
//			defer file.Close()
//
//			_, err = file.WriteString(tc.imagesContent)
//			assert.NoError(t, err)
//
//			config, err := ComposeConfig(tc.platform, path, tc.namespace)
//			if tc.expectError != "" {
//				assert.EqualError(t, err, tc.expectError)
//			} else {
//				assert.NoError(t, err)
//			}
//
//			assert.EqualValues(t, config, tc.expectConfig)
//		})
//	}
//}
//
//func TestComposeBootstrapConfig(t *testing.T) {
//	defaultManagementNamespace := "test-namespace"
//
//	tc := []struct {
//		name            string
//		namespace       string
//		infra           *configv1.Infrastructure
//		imagesConfigMap *corev1.ConfigMap
//		expectConfig    OperatorConfig
//		expectError     string
//	}{{
//		name:      "Unmarshal images from file for AWS",
//		namespace: defaultManagementNamespace,
//		infra: &configv1.Infrastructure{
//			Status: configv1.InfrastructureStatus{
//				PlatformStatus: &configv1.PlatformStatus{
//					Type: configv1.AWSPlatformType,
//				},
//			},
//		},
//		imagesConfigMap: &corev1.ConfigMap{
//			Data: map[string]string{
//				configMapImagesKey: `{
//    "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
//    "cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
//}`,
//			},
//		},
//		expectConfig: OperatorConfig{
//			ControllerImage:  "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
//			ManagedNamespace: defaultManagementNamespace,
//			Platform:         configv1.AWSPlatformType,
//		},
//	}, {
//		name:      "Unmarshal images from file for OpenStack",
//		namespace: "otherNamespace",
//		infra: &configv1.Infrastructure{
//			Status: configv1.InfrastructureStatus{
//				PlatformStatus: &configv1.PlatformStatus{
//					Type: configv1.OpenStackPlatformType,
//				},
//			},
//		},
//		imagesConfigMap: &corev1.ConfigMap{
//			Data: map[string]string{
//				configMapImagesKey: `{
//    "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
//    "cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
//}`,
//			},
//		},
//		expectConfig: OperatorConfig{
//			ControllerImage:  "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager",
//			ManagedNamespace: "otherNamespace",
//			Platform:         configv1.OpenStackPlatformType,
//		},
//	}, {
//		name:      "Unmarshal images from file for unknown platform returns nothing",
//		namespace: defaultManagementNamespace,
//		infra: &configv1.Infrastructure{
//			Status: configv1.InfrastructureStatus{
//				PlatformStatus: &configv1.PlatformStatus{
//					Type: configv1.NonePlatformType,
//				},
//			},
//		},
//		imagesConfigMap: &corev1.ConfigMap{
//			Data: map[string]string{
//				configMapImagesKey: `{
//    "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
//    "cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
//}`,
//			},
//		},
//		expectConfig: OperatorConfig{
//			ControllerImage:  "",
//			ManagedNamespace: defaultManagementNamespace,
//			Platform:         configv1.NonePlatformType,
//		},
//	}, {
//		name: "Broken JSON is rejected",
//		infra: &configv1.Infrastructure{
//			Status: configv1.InfrastructureStatus{
//				PlatformStatus: &configv1.PlatformStatus{
//					Type: configv1.AWSPlatformType,
//				},
//			},
//		},
//		imagesConfigMap: &corev1.ConfigMap{
//			Data: map[string]string{
//				configMapImagesKey: "",
//			},
//		},
//		expectError: "unable to decode images content from ConfigMap /: unexpected end of JSON input",
//	}, {
//		name:        "Incomplete infrastructure file is rejected",
//		infra:       &configv1.Infrastructure{},
//		expectError: "platform status is not populated on infrastructure",
//	}}
//
//	for _, tc := range tc {
//		t.Run(tc.name, func(t *testing.T) {
//			config, err := ComposeBootstrapConfig(tc.infra, tc.imagesConfigMap, tc.namespace)
//			if tc.expectError != "" {
//				assert.EqualError(t, err, tc.expectError)
//			} else {
//				assert.NoError(t, err)
//			}
//
//			assert.EqualValues(t, config, tc.expectConfig)
//		})
//	}
//}
