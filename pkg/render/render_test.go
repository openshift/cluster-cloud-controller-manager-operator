package render

import (
	"io/ioutil"
	"os"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	infra = `apiVersion: config.openshift.io/v1
kind: Infrastructure
metadata:
  name: cluster
spec: {}
status:
  platform: AWS
  platformStatus:
    type: AWS
`
	imagesConfigMap = `apiVersion: v1
kind: ConfigMap
metadata:
  name: cloud-controller-manager-images
  namespace: openshift-cloud-controller-manager-operator
data:
  images.json: >
    {
      "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
      "cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
    }
`
)

var (
	matchingConfigMap = &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cloud-controller-manager-images",
			Namespace: "openshift-cloud-controller-manager-operator",
		},
		Data: map[string]string{
			"images.json": `{
  "cloudControllerManagerAWS": "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
  "cloudControllerManagerOpenStack": "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager"
}
`,
		},
	}
	matchingInfra = &configv1.Infrastructure{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Infrastructure",
			APIVersion: "config.openshift.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Status: configv1.InfrastructureStatus{
			Platform: configv1.AWSPlatformType,
			PlatformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
			},
		},
	}
)

func TestReadAssets(t *testing.T) {
	tc := []struct {
		name            string
		infraContent    string
		infra           *configv1.Infrastructure
		imagesContent   string
		imagesConfigMap *corev1.ConfigMap
		expectError     string
	}{{
		name:            "Unmarshal both infrastructure and images with no issue",
		infraContent:    infra,
		infra:           matchingInfra,
		imagesContent:   imagesConfigMap,
		imagesConfigMap: matchingConfigMap,
	},
		{
			name:        "Infrastructure not located",
			expectError: "open not_found: no such file or directory",
		},
		{
			name:         "ImagesConfigMap not located",
			infraContent: infra,
			expectError:  "open not_found: no such file or directory",
		},
		{
			name:         "Bad images config map file content",
			infraContent: "BAD",
			expectError:  "error unmarshaling JSON: while decoding JSON: json: cannot unmarshal string into Go value of type v1.Infrastructure",
		},
		{
			name:          "Bad infrastructure file content",
			infraContent:  infra,
			imagesContent: "BAD",
			expectError:   "error unmarshaling JSON: while decoding JSON: json: cannot unmarshal string into Go value of type v1.ConfigMap",
		},
	}

	infraPath := "infra.yaml"
	configPath := "imagesConfigMap.yaml"

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			r := Render{
				imagesFile:         "not_found",
				infrastructureFile: "not_found",
			}
			if tc.imagesContent != "" {
				file, err := ioutil.TempFile(os.TempDir(), configPath)
				path := file.Name()
				assert.NoError(t, err)
				defer file.Close()

				_, err = file.WriteString(tc.imagesContent)
				assert.NoError(t, err)
				r.imagesFile = path
			}

			if tc.infraContent != "" {
				file, err := ioutil.TempFile(os.TempDir(), infraPath)
				path := file.Name()
				assert.NoError(t, err)
				defer file.Close()

				_, err = file.WriteString(tc.infraContent)
				assert.NoError(t, err)
				r.infrastructureFile = path
			}

			infra, config, err := r.readAssets()
			if tc.expectError != "" {
				assert.EqualError(t, err, tc.expectError)
			} else {
				assert.NoError(t, err)
			}
			assert.EqualValues(t, config, tc.imagesConfigMap)
			assert.EqualValues(t, infra, tc.infra)
		})
	}
}
