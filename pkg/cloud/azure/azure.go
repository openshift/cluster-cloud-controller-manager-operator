package azure

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/asaskevich/govalidator"
	configv1 "github.com/openshift/api/config/v1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	azureconsts "sigs.k8s.io/cloud-provider-azure/pkg/consts"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
)

const providerName = "azure"

var (
	//go:embed assets/*
	assetsFs  embed.FS
	templates = []common.TemplateSource{
		{ReferenceObject: &appsv1.Deployment{}, EmbedFsPath: "assets/cloud-controller-manager-deployment.yaml"},
		{ReferenceObject: &appsv1.DaemonSet{}, EmbedFsPath: "assets/cloud-node-manager-daemonset.yaml"},
	}
)

var (
	validAzureCloudNames = map[configv1.AzureCloudEnvironment]bool{
		configv1.AzurePublicCloud:       true,
		configv1.AzureUSGovernmentCloud: true,
		configv1.AzureChinaCloud:        true,
		configv1.AzureGermanCloud:       true,
		configv1.AzureStackCloud:        true,
	}

	validAzureCloudNameValues = func() []string {
		v := make([]string, 0, len(validAzureCloudNames))
		for n := range validAzureCloudNames {
			v = append(v, string(n))
		}
		return v
	}()
)

type imagesReference struct {
	CloudControllerManager         string `valid:"required"`
	CloudControllerManagerOperator string `valid:"required"`
	CloudNodeManager               string `valid:"required"`
}

var templateValuesValidationMap = map[string]interface{}{
	"images":             "required",
	"infrastructureName": "required,type(string)",
	"cloudproviderName":  "required,notnull,type(string)",
}

type azureAssets struct {
	operatorConfig    config.OperatorConfig
	renderedResources []client.Object
}

func (assets *azureAssets) GetRenderedResources() []client.Object {
	return assets.renderedResources
}

func getTemplateValues(images *imagesReference, operatorConfig config.OperatorConfig) (common.TemplateValues, error) {
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
	images := &imagesReference{
		CloudControllerManager:         config.ImagesReference.CloudControllerManagerAzure,
		CloudControllerManagerOperator: config.ImagesReference.CloudControllerManagerOperator,
		CloudNodeManager:               config.ImagesReference.CloudNodeManagerAzure,
	}
	_, err := govalidator.ValidateStruct(images)
	if err != nil {
		return nil, fmt.Errorf("%s: missed images in config: %v", providerName, err)
	}
	assets := &azureAssets{
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

func IsAzure(infra *configv1.Infrastructure) bool {
	if infra.Status.PlatformStatus != nil &&
		infra.Status.PlatformStatus.Type == configv1.AzurePlatformType {
		return true
	}
	return false
}

func CloudConfigTransformer(source string, infra *configv1.Infrastructure, network *configv1.Network) (string, error) {
	if !IsAzure(infra) {
		return "", fmt.Errorf("invalid platform, expected to be Azure")
	}

	var cfg azure.Config
	if err := json.Unmarshal([]byte(source), &cfg); err != nil {
		return "", fmt.Errorf("failed to unmarshal the cloud.conf: %w", err)
	}

	// We are copying the behaviour from CCO's transformer we need to:
	// 1. Ensure that the Cloud is set in the cloud.conf
	//   i. If it is set, verify that it is valid and does not conflict with the
	//      infrastructure config. If it conflicts, we want to error
	//  ii. If it is not set, default to public cloud (configv1.AzurePublicCloud)
	//
	// 2. Verify the cloud name set in the infra config is valid, if it is not
	// bail with an informative error

	// Verify the cloud name set in the infra config is valid
	cloud := configv1.AzurePublicCloud
	if azurePlatform := infra.Status.PlatformStatus.Azure; azurePlatform != nil {
		if c := azurePlatform.CloudName; c != "" {
			if !validAzureCloudNames[c] {
				return "", field.NotSupported(field.NewPath("status", "platformStatus", "azure", "cloudName"), c, validAzureCloudNameValues)
			}
			cloud = c
		}
	}

	// Ensure Cloud is set in cloud.conf matches what is set in infra
	if cfg.Cloud != "" {
		if !strings.EqualFold(string(cloud), cfg.Cloud) {
			return "",
				fmt.Errorf(`invalid user-provided cloud.conf: \"cloud\" field in user-provided
				cloud.conf conflicts with infrastructure object`)
		}
	}

	// TODO: Remove when you work this out (before merging)
	// Why is cfg.Cloud not typed: type AzureCloudEnvironment string

	// At this point these should always be the same
	// comparrison
	cfg.Cloud = string(cloud)

	// If the virtual machine type is not set we need to make sure it uses the
	// "standard" instance type. See OCPBUGS-25483 and OCPBUGS-20213 for more
	// information
	if cfg.VMType == "" {
		cfg.VMType = azureconsts.VMTypeStandard
	}

	cfgbytes, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to marshal the cloud.conf: %w", err)
	}
	return string(cfgbytes), nil
}
