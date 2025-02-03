package config

import (
	"os"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/stretchr/testify/assert"
)

func TestGetImagesFromJSONFile(t *testing.T) {
	tc := []struct {
		name           string
		path           string
		imagesContent  string
		expectedImages ImagesReference
		expectError    string
	}{{
		name: "Unmarshal images from file",
		path: "images_file",
		imagesContent: `{
			"cloudControllerManagerOperator": "registry.ci.openshift.org/openshift:cluster-cloud-controller-manager-operator",
			"cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
			"cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
		}`,
		expectedImages: ImagesReference{
			CloudControllerManagerOperator:  "registry.ci.openshift.org/openshift:cluster-cloud-controller-manager-operator",
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
			"cloudControllerManagerOperator": "registry.ci.openshift.org/openshift:cluster-cloud-controller-manager-operator",
			"cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager"
		}`,
		expectedImages: ImagesReference{
			CloudControllerManagerOperator: "registry.ci.openshift.org/openshift:cluster-cloud-controller-manager-operator",
			CloudControllerManagerAWS:      "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
		},
	}, {
		name: "Duplicate content takes precedence and is accepted",
		path: "images_file",
		imagesContent: `{
			"cloudControllerManagerOperator": "registry.ci.openshift.org/openshift:cluster-cloud-controller-manager-operator",
			"cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
			"cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:different"
		}`,
		expectedImages: ImagesReference{
			CloudControllerManagerOperator: "registry.ci.openshift.org/openshift:cluster-cloud-controller-manager-operator",
			CloudControllerManagerAWS:      "registry.ci.openshift.org/openshift:different",
		},
	}, {
		name: "Unknown image name is ignored",
		path: "images_file",
		imagesContent: `{
			"cloudControllerManagerOperator": "registry.ci.openshift.org/openshift:cluster-cloud-controller-manager-operator",
			"cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
			"cloudControllerManagerUnknown": "registry.ci.openshift.org/openshift:unknown-cloud-controller-manager",
			"cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
		}`,
		expectedImages: ImagesReference{
			CloudControllerManagerOperator:  "registry.ci.openshift.org/openshift:cluster-cloud-controller-manager-operator",
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
				file, err := os.CreateTemp(os.TempDir(), tc.path)
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

func TestCheckInfrastructure(t *testing.T) {
	tc := []struct {
		name      string
		infra     *configv1.Infrastructure
		expectErr string
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
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			err := checkInfrastructureResource(tc.infra)
			if tc.expectErr != "" {
				assert.EqualError(t, err, tc.expectErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestComposeConfig(t *testing.T) {
	defaultManagementNamespace := "test-namespace"

	defaultImagesFileContent := `{
    		"cloudControllerManagerOperator": "registry.ci.openshift.org/openshift:cluster-cloud-controller-manager-operator",
    		"cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
    		"cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager",
			"cloudControllerManagerIBM": "registry.ci.openshift.org/openshift:ibm-cloud-controller-manager",
    		"cloudControllerManagerAzure": "quay.io/openshift/origin-azure-cloud-controller-manager",
    		"cloudNodeManagerAzure": "quay.io/openshift/origin-azure-cloud-node-manager"
		}`

	defaultImagesReference := ImagesReference{
		CloudControllerManagerOperator:  "registry.ci.openshift.org/openshift:cluster-cloud-controller-manager-operator",
		CloudControllerManagerAWS:       "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
		CloudControllerManagerAzure:     "quay.io/openshift/origin-azure-cloud-controller-manager",
		CloudNodeManagerAzure:           "quay.io/openshift/origin-azure-cloud-node-manager",
		CloudControllerManagerIBM:       "registry.ci.openshift.org/openshift:ibm-cloud-controller-manager",
		CloudControllerManagerOpenStack: "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager",
	}

	tc := []struct {
		name          string
		namespace     string
		infra         *configv1.Infrastructure
		clusterProxy  *configv1.Proxy
		imagesContent string
		expectConfig  OperatorConfig
		expectError   string
		featureGates  featuregates.FeatureGateAccess
	}{{
		name:      "Unmarshal images from file",
		namespace: defaultManagementNamespace,
		infra: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.AWSPlatformType,
				},
			},
		},
		imagesContent: defaultImagesFileContent,
		expectConfig: OperatorConfig{
			ImagesReference:  defaultImagesReference,
			ManagedNamespace: defaultManagementNamespace,
			PlatformStatus:   &configv1.PlatformStatus{Type: configv1.AWSPlatformType},
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
		featureGates: featuregates.NewHardcodedFeatureGateAccess(
			[]configv1.FeatureGateName{"CloudControllerManagerWebhook", "ChocobombVanilla", "ChocobombStrawberry"},
			[]configv1.FeatureGateName{"ChocobombBlueberry", "ChocobombBanana"},
		),
		expectConfig: OperatorConfig{
			ManagedNamespace: defaultManagementNamespace,
			ImagesReference:  defaultImagesReference,
			PlatformStatus:   &configv1.PlatformStatus{Type: configv1.OpenStackPlatformType},
			IsSingleReplica:  true,
			// We only see CloudControllerManagerWebhook returned here because kubernetes defines
			// white-listed features that are allowed to be used by cloud providers. Anything that
			// is not defined there won't be passed to the cloud provider.
			// For more details look into k8s.io/controller-manager/pkg/features
			//
			// To the next person doing a k8s version bump where this test case
			// fails: it's possible the FeatureGate used has been promoted, and no
			// longer appears in the features package linked above. You'll need to
			// choose something present in the vendored k8s version.
			FeatureGates: "CloudControllerManagerWebhook=true",
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
	}, {
		name:        "Empty infra",
		namespace:   defaultManagementNamespace,
		infra:       nil,
		expectError: "platform status is not populated on infrastructure",
	}, {
		name:      "Empty Infra Status",
		namespace: defaultManagementNamespace,
		infra: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				PlatformStatus: nil,
			},
		},
		expectError: "platform status is not populated on infrastructure",
	}, {
		name:      "Empty Platform Type",
		namespace: defaultManagementNamespace,
		infra: &configv1.Infrastructure{
			Status: configv1.InfrastructureStatus{
				PlatformStatus: &configv1.PlatformStatus{
					Type: "",
				},
			},
		},
		expectError: "no platform provider found on infrastructure",
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			file, err := os.CreateTemp(os.TempDir(), "images")
			path := file.Name()
			assert.NoError(t, err)
			defer file.Close()

			if tc.imagesContent == "" {
				tc.imagesContent = defaultImagesFileContent
			}
			_, err = file.WriteString(tc.imagesContent)
			assert.NoError(t, err)

			config, err := ComposeConfig(tc.infra, tc.clusterProxy, path, tc.namespace, tc.featureGates)
			if tc.expectError != "" {
				assert.EqualError(t, err, tc.expectError)
			} else {
				assert.NoError(t, err)
			}

			assert.EqualValues(t, config, tc.expectConfig)
		})
	}
}
