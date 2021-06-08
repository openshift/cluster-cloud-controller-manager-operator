package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"path/filepath"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const configMapImagesKey = "images.json"

// commonImagesReference allows build systems to inject commonImagesReference for CCCMO components (rbac proxy or so)
type commonImagesReference struct {
}

// OperatorConfig contains configuration values for templating resources
type OperatorConfig struct {
	ManagedNamespace  string
	ImagesFileContent []byte
	CommonImages      commonImagesReference
	Platform          configv1.PlatformType
}

// GetProviderFromInfrastructure reads the Infrastructure resource and returns Platform value
func GetProviderFromInfrastructure(infra *configv1.Infrastructure) (configv1.PlatformType, error) {
	if infra == nil || infra.Status.PlatformStatus == nil {
		return "", fmt.Errorf("platform status is not populated on infrastructure")
	}
	if infra.Status.PlatformStatus.Type == "" {
		return "", fmt.Errorf("no platform provider found on infrastructure")
	}

	return infra.Status.PlatformStatus.Type, nil
}

func readImagesJSONFile(filePath string) ([]byte, error) {
	data, err := ioutil.ReadFile(filepath.Clean(filePath))
	if err != nil {
		return data, err
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("images file is not a valid json")
	}
	return data, nil
}

func getCommonImageReferences(imagesFileContent []byte) (commonImagesReference, error) {
	i := commonImagesReference{}
	if err := json.Unmarshal(imagesFileContent, &i); err != nil {
		return i, err
	}
	return i, nil
}

// ComposeConfig creates a Config for operator
func ComposeConfig(platform configv1.PlatformType, imagesFile, managedNamespace string) (OperatorConfig, error) {
	config := OperatorConfig{
		Platform:         platform,
		ManagedNamespace: managedNamespace,
	}

	imagesFileContent, err := readImagesJSONFile(imagesFile)
	if err != nil {
		klog.Errorf("Unable to decode images file from location %s", imagesFile, err)
		return config, err
	}

	config.ImagesFileContent = imagesFileContent

	commonImages, err := getCommonImageReferences(imagesFileContent)
	config.CommonImages = commonImages

	return config, nil
}

// getImagesFromImagesConfigMap collects the content of provided ConfigMap with images
// via --images-file which is used for rendering bootstrap manifests.
func getImagesContentFromConfigMap(config *corev1.ConfigMap) ([]byte, error) {
	if config == nil || config.Data == nil {
		return nil, fmt.Errorf("unable to find Data field in provided ConfigMap")
	}

	data, ok := config.Data[configMapImagesKey]
	if !ok {
		return nil,
			fmt.Errorf("unable to find images key %q in ConfigMap %s", configMapImagesKey, client.ObjectKeyFromObject(config))
	}

	if !json.Valid([]byte(data)) {
		return nil, fmt.Errorf("ConfigMap %s does not contain a valid images json", client.ObjectKeyFromObject(config))
	}

	return []byte(data), nil
}

// ComposeBootstrapConfig creates a Config for render
func ComposeBootstrapConfig(infra *configv1.Infrastructure, imagesConfig *corev1.ConfigMap, managedNamespace string) (OperatorConfig, error) {
	platform, err := GetProviderFromInfrastructure(infra)
	if err != nil {
		klog.Errorf("Unable to determine platform from cluster infrastructure file: %s", err)
		return OperatorConfig{}, err
	}

	config := OperatorConfig{
		Platform:         platform,
		ManagedNamespace: managedNamespace,
	}

	imagesContent, err := getImagesContentFromConfigMap(imagesConfig)
	if err != nil {
		klog.Errorf("Unable to read config map: %s", err)
		return OperatorConfig{}, err
	}
	config.ImagesFileContent = imagesContent

	commonImages, err := getCommonImageReferences(imagesContent)
	config.CommonImages = commonImages

	return config, nil
}
