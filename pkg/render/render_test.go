package render

import (
	"fmt"
	"io/fs"
	"io/ioutil"
	"os"
	"path"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/aws"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/azure"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
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
	infraWithCloudConfig = `apiVersion: config.openshift.io/v1
kind: Infrastructure
metadata:
  name: cluster
spec:
  cloudConfig:
    name: test
    key: test
status:
  platform: Azure
  platformStatus:
    type: Azure
`
	infraMissingPlatform = `apiVersion: config.openshift.io/v1
kind: Infrastructure
metadata:
  name: cluster
spec:
  cloudConfig:
    name: test
    key: cloud.conf
status: {}
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
	cloudConfigMap = `apiVersion: v1
data:
  test: "{test:test}"
kind: ConfigMap
metadata:
  name: kube-cloud-config
  namespace: kube-system
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
		cloudConfig     string
		cloudConfigMap  *corev1.ConfigMap
		expectError     string
	}{{
		name:            "Unmarshal both infrastructure and images with no issue",
		infraContent:    infra,
		infra:           matchingInfra,
		imagesContent:   imagesConfigMap,
		imagesConfigMap: matchingConfigMap,
	}, {
		name:        "Infrastructure not located",
		expectError: "open not_found: no such file or directory",
	}, {
		name:         "ImagesConfigMap not located",
		infraContent: infra,
		expectError:  "open not_found: no such file or directory",
	}, {
		name:         "Bad images config map file content",
		infraContent: "BAD",
		expectError:  "error unmarshaling JSON: while decoding JSON: json: cannot unmarshal string into Go value of type v1.Infrastructure",
	}, {
		name:          "Bad infrastructure file content",
		infraContent:  infra,
		imagesContent: "BAD",
		expectError:   "error unmarshaling JSON: while decoding JSON: json: cannot unmarshal string into Go value of type v1.ConfigMap",
	}}

	infraPath := "infra.yaml"
	configPath := "imagesConfigMap.yaml"
	cloudConfigPath := "cloudConfigMap.yaml"

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			r := Render{
				imagesFile:         "not_found",
				infrastructureFile: "not_found",
				cloudConfigFile:    "not_found",
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

			if tc.cloudConfig != "" {
				file, err := ioutil.TempFile(os.TempDir(), cloudConfigPath)
				path := file.Name()
				assert.NoError(t, err)
				defer file.Close()

				_, err = file.WriteString(tc.cloudConfig)
				assert.NoError(t, err)
				r.cloudConfigFile = path
			}

			infra, config, cloudConfig, err := r.readAssets()
			if tc.expectError != "" {
				assert.EqualError(t, err, tc.expectError)
			} else {
				assert.NoError(t, err)
			}
			assert.EqualValues(t, config, tc.imagesConfigMap)
			assert.EqualValues(t, infra, tc.infra)
			assert.EqualValues(t, cloudConfig, tc.cloudConfig)
		})
	}
}

func TestRenderRun(t *testing.T) {
	tc := []struct {
		name               string
		infraContent       string
		imagesContent      string
		cloudConfigContent string
		expectObjects      []client.Object
		expectCloudConfig  bool
		badDestination     bool
		expectError        string
	}{{
		name:          "Unmarshal both infrastructure and images with no issue",
		infraContent:  infra,
		imagesContent: imagesConfigMap,
		expectObjects: aws.GetBootstrapResources(),
	}, {
		name:               "Unmarshal both infrastructure and images with no issue",
		infraContent:       infraWithCloudConfig,
		imagesContent:      imagesConfigMap,
		cloudConfigContent: cloudConfigMap,
		expectObjects:      azure.GetBootstrapResources(),
		expectCloudConfig:  true,
	}, {
		name:               "Infrastructure not populated",
		infraContent:       infraMissingPlatform,
		imagesContent:      imagesConfigMap,
		cloudConfigContent: cloudConfigMap,
		expectError:        "platform status is not populated on infrastructure",
	}, {
		name:          "Cloud config missing",
		infraContent:  infraWithCloudConfig,
		imagesContent: imagesConfigMap,
		expectError:   "open not_found: no such file or directory",
	}, {
		name:          "ImagesConfigMap not located",
		infraContent:  infra,
		imagesContent: "BAD",
		expectError:   "error unmarshaling JSON: while decoding JSON: json: cannot unmarshal string into Go value of type v1.ConfigMap",
	}, {
		name:               "CloudConfigMap is not populated correctly",
		infraContent:       infraWithCloudConfig,
		imagesContent:      imagesConfigMap,
		cloudConfigContent: "BAD",
		expectError:        "error unmarshaling JSON: while decoding JSON: json: cannot unmarshal string into Go value of type v1.ConfigMap",
	}, {
		name:           "Destination directory is broken",
		infraContent:   infra,
		imagesContent:  imagesConfigMap,
		expectObjects:  aws.GetBootstrapResources(),
		badDestination: true,
		expectError:    "mkdir /dev/null: not a directory",
	}}

	infraPath := "infra.yaml"
	configPath := "imagesConfigMap.yaml"
	cloudConfigPath := "cloudConfigMap.yaml"

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			imagesFile := "not_found"
			infrastructureFile := "not_found"
			cloudConfigFile := "not_found"
			destination, err := ioutil.TempDir("", "test")
			assert.NoError(t, err)

			if tc.badDestination {
				destination = "/dev/null"
			}

			if tc.imagesContent != "" {
				file, err := ioutil.TempFile(os.TempDir(), configPath)
				path := file.Name()
				assert.NoError(t, err)
				defer file.Close()

				_, err = file.WriteString(tc.imagesContent)
				assert.NoError(t, err)
				imagesFile = path
			}

			if tc.cloudConfigContent != "" {
				file, err := ioutil.TempFile(os.TempDir(), cloudConfigPath)
				path := file.Name()
				assert.NoError(t, err)
				defer file.Close()

				_, err = file.WriteString(tc.cloudConfigContent)
				assert.NoError(t, err)
				cloudConfigFile = path
			}

			if tc.infraContent != "" {
				file, err := ioutil.TempFile(os.TempDir(), infraPath)
				path := file.Name()
				assert.NoError(t, err)
				defer file.Close()

				_, err = file.WriteString(tc.infraContent)
				assert.NoError(t, err)
				infrastructureFile = path
			}

			r := New(infrastructureFile, imagesFile, cloudConfigFile)
			err = r.Run(destination)
			if tc.expectError != "" {
				assert.EqualError(t, err, tc.expectError)
				return
			}
			assert.NoError(t, err)

			// Assert all files were written to bootstrap dir
			files, err := ioutil.ReadDir(path.Join(destination, bootstrapPrefix))
			assert.NoError(t, err)
			assert.Len(t, files, len(tc.expectObjects))

			// Assert config dir is present
			_, err = ioutil.ReadDir(path.Join(destination, configPrefix))
			assert.NoError(t, err)

			if tc.expectCloudConfig {
				_, err := os.ReadFile(path.Join(destination, configPrefix, configDataKey))
				assert.NoError(t, err)
			}
		})
	}
}

func TestWriteAssets(t *testing.T) {
	tc := []struct {
		name          string
		destination   string
		preCreateMode fs.FileMode
		objects       []client.Object
		expectErr     string
	}{{
		name:        "Writing file finished with success",
		destination: "test",
		objects:     []client.Object{matchingInfra, matchingConfigMap},
	}, {
		name:      "Fail to write into /dev/null",
		objects:   []client.Object{matchingInfra, matchingConfigMap},
		expectErr: "mkdir /dev/null: not a directory",
	}, {
		name:          "Fail to write into /dev/null",
		objects:       []client.Object{matchingInfra, matchingConfigMap},
		destination:   "bad_permissions",
		preCreateMode: 0444,
		expectErr:     "permission denied",
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			destination := "/dev/null"

			if tc.destination != "" {
				dirPath, err := ioutil.TempDir("", tc.destination)
				assert.NoError(t, err)
				destination = dirPath
				if tc.preCreateMode != 0 {
					os.MkdirAll(path.Join(destination, bootstrapPrefix), tc.preCreateMode)
					assert.NoError(t, err)
				}
			}

			err := writeAssets(destination, tc.objects)
			if tc.expectErr != "" {
				assert.Contains(t, err.Error(), tc.expectErr)
				return
			}
			assert.NoError(t, err)

			// Assert all files were written to bootstrap dir
			files, err := ioutil.ReadDir(path.Join(destination, bootstrapPrefix))
			assert.NoError(t, err)
			assert.Len(t, files, len(tc.objects))

			names := []string{}
			for _, file := range files {
				names = append(names, file.Name())
			}
			for _, res := range tc.objects {
				filename := fmt.Sprintf("%s-%s.yaml", res.GetName(), strings.ToLower(res.GetObjectKind().GroupVersionKind().Kind))
				assert.Contains(t, names, filename)

				// Object copy with some required fields emptied
				collectedObject := res.DeepCopyObject().(client.Object)
				collectedObject.SetName("")
				collectedObject.SetNamespace("")
				data, err := os.ReadFile(path.Join(destination, bootstrapPrefix, filename))
				assert.NoError(t, err)

				// Fill object with data from disc
				err = yaml.UnmarshalStrict(data, collectedObject)
				assert.NoError(t, err)

				assert.Equal(t, collectedObject, res)
			}
		})
	}
}

func TestWriteCloudConfig(t *testing.T) {
	tc := []struct {
		name          string
		destination   string
		preCreateMode fs.FileMode
		cloudConfig   string
		expectErr     string
	}{{
		name:        "Writing file finished with success",
		destination: "test",
		cloudConfig: "some_data",
	}, {
		name:        "No operation for emplty cloud config",
		destination: "test",
	}, {
		name:        "Fail to write into /dev/null",
		cloudConfig: "some_data",
		expectErr:   "mkdir /dev/null: not a directory",
	}, {
		name:          "Fail to write into folder with bad permissions",
		cloudConfig:   "some_data",
		destination:   "bad_permissions",
		preCreateMode: 0444,
		expectErr:     "permission denied",
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			destination := "/dev/null"

			if tc.destination != "" {
				dirPath, err := ioutil.TempDir("", tc.destination)
				assert.NoError(t, err)
				destination = dirPath
				if tc.preCreateMode != 0 {
					os.MkdirAll(path.Join(destination, configPrefix), tc.preCreateMode)
					assert.NoError(t, err)
				}
			}

			err := writeCloudConfig(destination, tc.cloudConfig)
			if tc.expectErr != "" {
				assert.Contains(t, err.Error(), tc.expectErr)
				return
			}
			assert.NoError(t, err)

			// Assert all files were written to bootstrap dir
			files, err := ioutil.ReadDir(path.Join(destination, configPrefix))
			assert.NoError(t, err)

			data, err := os.ReadFile(path.Join(destination, configPrefix, configDataKey))
			if tc.cloudConfig != "" {
				assert.NoError(t, err)
				assert.Equal(t, string(data), tc.cloudConfig)
				assert.Len(t, files, 1)
			} else {
				assert.Len(t, files, 0)
				assert.Error(t, err)
			}
		})
	}
}
