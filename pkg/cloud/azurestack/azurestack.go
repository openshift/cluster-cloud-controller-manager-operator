package azurestack

import (
	"embed"
	"encoding/json"
	"fmt"

	"github.com/asaskevich/govalidator"
	configv1 "github.com/openshift/api/config/v1"
	appsv1 "k8s.io/api/apps/v1"
	azureconsts "sigs.k8s.io/cloud-provider-azure/pkg/consts"
	azureconfig "sigs.k8s.io/cloud-provider-azure/pkg/provider/config"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
)

const providerName = "azurestack"

var (
	//go:embed assets/*
	assetsFs  embed.FS
	templates = []common.TemplateSource{
		{ReferenceObject: &appsv1.Deployment{}, EmbedFsPath: "assets/cloud-controller-manager-deployment.yaml"},
		{ReferenceObject: &appsv1.DaemonSet{}, EmbedFsPath: "assets/cloud-node-manager-daemonset.yaml"},
	}
)

type imagesReference struct {
	Operator               string `valid:"required"`
	CloudControllerManager string `valid:"required"`
	CloudNodeManager       string `valid:"required"`
}

var templateValuesValidationMap = map[string]interface{}{
	"images":             "required",
	"infrastructureName": "required,type(string)",
	"cloudproviderName":  "required,type(string)",
}

type azurestackAssets struct {
	operatorConfig    config.OperatorConfig
	renderedResources []client.Object
}

func (assets *azurestackAssets) GetRenderedResources() []client.Object {
	return assets.renderedResources
}

func getTemplateValues(images imagesReference, operatorConfig config.OperatorConfig) (common.TemplateValues, error) {
	values := common.TemplateValues{
		"images":             images,
		"infrastructureName": operatorConfig.InfrastructureName,
		"cloudproviderName":  operatorConfig.GetPlatformNameString(),
	}
	_, err := govalidator.ValidateMap(values, templateValuesValidationMap)
	if err != nil {
		return nil, err
	}
	return values, nil
}

func NewProviderAssets(config config.OperatorConfig) (common.CloudProviderAssets, error) {
	images := imagesReference{
		Operator:               config.ImagesReference.CloudControllerManagerOperator,
		CloudControllerManager: config.ImagesReference.CloudControllerManagerAzure,
		CloudNodeManager:       config.ImagesReference.CloudNodeManagerAzure,
	}
	_, err := govalidator.ValidateStruct(images)
	if err != nil {
		return nil, fmt.Errorf("%s: missed images in config: %v", providerName, err)
	}

	assets := &azurestackAssets{
		operatorConfig: config,
	}
	objTemplates, err := common.ReadTemplates(assetsFs, templates)
	if err != nil {
		return nil, err
	}
	templateValues, err := getTemplateValues(images, config)
	if err != nil {
		return nil, fmt.Errorf("can not construct template values for %s assets: %v", providerName, err)
	}

	assets.renderedResources, err = common.RenderTemplates(objTemplates, templateValues)
	if err != nil {
		return nil, err
	}
	return assets, nil
}

// IsAzureStackHub inspects the infrastructure config returns true if it is configured for ASH.
func IsAzureStackHub(platformStatus *configv1.PlatformStatus) bool {
	return platformStatus.Azure != nil && platformStatus.Azure.CloudName == configv1.AzureStackCloud
}

// CloudConfigTransformer implements the cloudConfigTransformer. It takes
// the user-provided, legacy cloud provider-compatible configuration and
// modifies it to be compatible with the external cloud provider. It returns
// an error if the platform is not OpenStackPlatformType or if any errors are
// encountered while attempting to rework the configuration.
func CloudConfigTransformer(source string, infra *configv1.Infrastructure, network *configv1.Network) (string, error) {
	if !IsAzureStackHub(infra.Status.PlatformStatus) {
		return "", fmt.Errorf("invalid platform, expected CloudName to be %s", configv1.AzureStackCloud)
	}

	var cfg azureconfig.Config
	if err := json.Unmarshal([]byte(source), &cfg); err != nil {
		return "", fmt.Errorf("failed to unmarshal the cloud.conf: %w", err)
	}

	// If the virtual machine type is not set we need to make sure it uses
	// the "standard" instance type. This is to mitigate an issue in the 1.27
	// release where the default instance type was changed to VMSS.
	// see OCPBUGS-20213 for more information.
	if cfg.VMType == "" {
		cfg.VMType = azureconsts.VMTypeStandard
	}

	cfgbytes, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to marshal the cloud.conf: %w", err)
	}
	return string(cfgbytes), nil
}
