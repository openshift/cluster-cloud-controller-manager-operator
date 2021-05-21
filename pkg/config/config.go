package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"path/filepath"

	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/klog/v2"
)

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

func GetProviderFromInfrastructure(infra *configv1.Infrastructure) (configv1.PlatformType, error) {
	if infra == nil || infra.Status.PlatformStatus == nil {
		return "", fmt.Errorf("platform status is not populated on infrastructure")
	}
	if infra.Status.PlatformStatus.Type == "" {
		return "", fmt.Errorf("no platform provider found on infrastructure")
	}

	return infra.Status.PlatformStatus.Type, nil
}

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

func ComposeConfig(platform configv1.PlatformType, imagesFile, managedNamespace string) (OperatorConfig, error) {
	config := OperatorConfig{
		Platform:         platform,
		ManagedNamespace: managedNamespace,
	}

	images, err := getImagesFromJSONFile(imagesFile)
	if err != nil {
		klog.Errorf("Unable to decode images file from location %s", imagesFile, err)
		return config, err
	}

	config.ControllerImage = getCloudControllerManagerFromImages(platform, images)
	config.CloudNodeImage = getCloudNodeManagerFromImages(platform, images)

	return config, nil
}
