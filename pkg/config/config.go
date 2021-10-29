package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"path/filepath"

	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/klog/v2"
)

// ImagesReference allows build systems to inject ImagesReference for CCCMO components
// This structure widely using for construct CloudProviderAssets objects and represents expected structure of
// `cloud-controller-manager-images` config map which manages and populating by `openshift-cluster-version-operator`.
// See manifests/0000_26_cloud-controller-manager-operator_01_images.configmap.yaml
type ImagesReference struct {
	CloudControllerManagerOperator  string `json:"cloudControllerManagerOperator"`
	CloudControllerManagerAlibaba   string `json:"cloudControllerManagerAlibaba"`
	CloudControllerManagerAWS       string `json:"cloudControllerManagerAWS"`
	CloudControllerManagerAzure     string `json:"cloudControllerManagerAzure"`
	CloudNodeManagerAzure           string `json:"cloudNodeManagerAzure"`
	CloudControllerManagerIBM       string `json:"cloudControllerManagerIBM"`
	CloudControllerManagerOpenStack string `json:"cloudControllerManagerOpenStack"`
}

// OperatorConfig contains configuration values for templating resources
type OperatorConfig struct {
	ManagedNamespace   string
	ImagesReference    ImagesReference
	IsSingleReplica    bool
	InfrastructureName string
	PlatformStatus     *configv1.PlatformStatus
	ClusterProxy       *configv1.Proxy
}

// checkInfrastructureResource checks Infrastructure resource for platform status presence
func checkInfrastructureResource(infra *configv1.Infrastructure) error {
	if infra == nil || infra.Status.PlatformStatus == nil {
		return fmt.Errorf("platform status is not populated on infrastructure")
	}
	if infra.Status.PlatformStatus.Type == "" {
		return fmt.Errorf("no platform provider found on infrastructure")
	}

	return nil
}

// getImagesFromJSONFile is used in operator to read the content of mounted ConfigMap
// containing images for substitution in templates
func getImagesFromJSONFile(filePath string) (ImagesReference, error) {
	data, err := ioutil.ReadFile(filepath.Clean(filePath))
	if err != nil {
		return ImagesReference{}, err
	}

	i := ImagesReference{}
	if err := json.Unmarshal(data, &i); err != nil {
		return ImagesReference{}, err
	}
	return i, nil
}

// ComposeConfig creates a Config for operator
func ComposeConfig(infrastructure *configv1.Infrastructure, clusterProxy *configv1.Proxy, imagesFile, managedNamespace string) (OperatorConfig, error) {
	err := checkInfrastructureResource(infrastructure)
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
		PlatformStatus:     infrastructure.Status.PlatformStatus.DeepCopy(),
		ClusterProxy:       clusterProxy,
		ManagedNamespace:   managedNamespace,
		ImagesReference:    images,
		InfrastructureName: infrastructure.Status.InfrastructureName,
		IsSingleReplica:    infrastructure.Status.ControlPlaneTopology == configv1.SingleReplicaTopologyMode,
	}

	return config, nil
}
