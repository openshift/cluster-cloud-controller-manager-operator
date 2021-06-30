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

// imagesReference allows build systems to inject imagesReference for CCCMO components
type imagesReference struct {
	CloudControllerManagerAWS       string `json:"cloudControllerManagerAWS"`
	CloudControllerManagerAzure     string `json:"cloudControllerManagerAzure"`
	CloudNodeManagerAzure           string `json:"cloudNodeManagerAzure"`
	CloudControllerManagerOpenStack string `json:"cloudControllerManagerOpenStack"`
}

// OperatorConfig contains configuration values for templating resources
type OperatorConfig struct {
	ManagedNamespace string
	ControllerImage  string
	CloudNodeImage   string
	Platform         configv1.PlatformType
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

// getImagesFromImagesConfigMap collects the content of provided ConfigMap with images
// via --images-file which is used for rendering bootstrap manifests.
func getImagesFromImagesConfigMap(config *corev1.ConfigMap) (imagesReference, error) {
	if config == nil || config.Data == nil {
		return imagesReference{}, fmt.Errorf("unable to find Data field in provided ConfigMap")
	}

	data, ok := config.Data[configMapImagesKey]
	if !ok {
		return imagesReference{},
			fmt.Errorf("unable to find images key %q in ConfigMap %s", configMapImagesKey, client.ObjectKeyFromObject(config))
	}

	i := imagesReference{}
	if err := json.Unmarshal([]byte(data), &i); err != nil {
		return imagesReference{},
			fmt.Errorf("unable to decode images content from ConfigMap %s: %v", client.ObjectKeyFromObject(config), err)
	}
	return i, nil
}

// getImagesFromJSONFile is used in operator to read the content of mounted ConfigMap
// containing images for substitution in templates
func getImagesFromJSONFile(filePath string) (imagesReference, error) {
	data, err := ioutil.ReadFile(filepath.Clean(filePath))
	if err != nil {
		return imagesReference{}, err
	}

	i := imagesReference{}
	if err := json.Unmarshal(data, &i); err != nil {
		return imagesReference{}, err
	}
	return i, nil
}

// getCloudControllerManagerFromImages returns a CCM binary image later used in substitution
func getCloudControllerManagerFromImages(platform configv1.PlatformType, images imagesReference) string {
	switch platform {
	case configv1.AWSPlatformType:
		return images.CloudControllerManagerAWS
	case configv1.OpenStackPlatformType:
		return images.CloudControllerManagerOpenStack
	case configv1.AzurePlatformType:
		return images.CloudControllerManagerAzure
	default:
		return ""
	}
}

func getCloudNodeManagerFromImages(platform configv1.PlatformType, images imagesReference) string {
	switch platform {
	case configv1.AzurePlatformType:
		return images.CloudNodeManagerAzure
	default:
		return ""
	}
}

// ComposeConfig creates a Config for operator
func ComposeConfig(platform configv1.PlatformType, imagesFile, managedNamespace string) (OperatorConfig, error) {
	images, err := getImagesFromJSONFile(imagesFile)
	if err != nil {
		klog.Errorf("Unable to decode images file from location %s", imagesFile, err)
		return OperatorConfig{}, err
	}

	config := OperatorConfig{
		Platform:         platform,
		ManagedNamespace: managedNamespace,
		ControllerImage:  getCloudControllerManagerFromImages(platform, images),
		CloudNodeImage:   getCloudNodeManagerFromImages(platform, images),
	}

	return config, nil
}

// ComposeBootstrapConfig creates a Config for render
func ComposeBootstrapConfig(infra *configv1.Infrastructure, imagesConfig *corev1.ConfigMap, managedNamespace string) (OperatorConfig, error) {
	platform, err := GetProviderFromInfrastructure(infra)
	if err != nil {
		klog.Errorf("Unable to determine platform from cluster infrastructure file: %s", err)
		return OperatorConfig{}, err
	}

	images, err := getImagesFromImagesConfigMap(imagesConfig)
	if err != nil {
		klog.Errorf("Unable to decode images file from location %s", images, err)
		return OperatorConfig{}, err
	}

	config := OperatorConfig{
		Platform:         platform,
		ManagedNamespace: managedNamespace,
		ControllerImage:  getCloudControllerManagerFromImages(platform, images),
		CloudNodeImage:   getCloudNodeManagerFromImages(platform, images),
	}

	return config, nil
}
