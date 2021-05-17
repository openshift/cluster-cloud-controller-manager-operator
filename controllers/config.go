package controllers

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
	CloudControllerManagerOpenStack string `json:"cloudControllerManagerOpenStack"`
}

// operatorConfig contains configuration values for templating resources
type operatorConfig struct {
	ManagedNamespace string
	ControllerImage  string
	Platform         configv1.PlatformType
}

func getProviderFromInfrastructure(infra *configv1.Infrastructure) (configv1.PlatformType, error) {
	if infra == nil || infra.Status.PlatformStatus == nil {
		return "", fmt.Errorf("platform status is not pupulated on infrastructure")
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

func getProviderControllerFromImages(platform configv1.PlatformType, images imagesReference) string {
	switch platform {
	case configv1.AWSPlatformType:
		return images.CloudControllerManagerAWS
	case configv1.OpenStackPlatformType:
		return images.CloudControllerManagerOpenStack
	default:
		return ""
	}
}

func (r *CloudOperatorReconciler) composeConfig(platform configv1.PlatformType) (operatorConfig, error) {
	config := operatorConfig{
		Platform:         platform,
		ManagedNamespace: r.ManagedNamespace,
	}

	images, err := getImagesFromJSONFile(r.ImagesFile)
	if err != nil {
		klog.Errorf("Unable to decode images file from location %s", r.ImagesFile, err)
		return config, err
	}

	config.ControllerImage = getProviderControllerFromImages(platform, images)

	return config, nil
}
