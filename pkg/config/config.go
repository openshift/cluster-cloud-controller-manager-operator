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
	ManagedNamespace   string
	ControllerImage    string
	CloudNodeImage     string
	IsSingleReplica    bool
	InfrastructureName string
	Platform           configv1.PlatformType
	ClusterProxy       *configv1.Proxy
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
func ComposeConfig(infrastructure *configv1.Infrastructure, clusterProxy *configv1.Proxy, imagesFile, managedNamespace string) (OperatorConfig, error) {
	platform, err := GetProviderFromInfrastructure(infrastructure)
	if err != nil {
		klog.Errorf("Unable to get platform from infrastructure: %s", err)
		return OperatorConfig{}, err
	}

	images, err := getImagesFromJSONFile(imagesFile)
	if err != nil {
		klog.Errorf("Unable to decode images file from location %s", imagesFile, err)
		return OperatorConfig{}, err
	}

	config := OperatorConfig{
		Platform:           platform,
		ClusterProxy:       clusterProxy,
		ManagedNamespace:   managedNamespace,
		ControllerImage:    getCloudControllerManagerFromImages(platform, images),
		CloudNodeImage:     getCloudNodeManagerFromImages(platform, images),
		InfrastructureName: infrastructure.Status.InfrastructureName,
		IsSingleReplica:    infrastructure.Status.ControlPlaneTopology == configv1.SingleReplicaTopologyMode,
	}

	return config, nil
}
