package azure

import (
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/asaskevich/govalidator"
	configv1 "github.com/openshift/api/config/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	azureconsts "sigs.k8s.io/cloud-provider-azure/pkg/consts"
	azureconfig "sigs.k8s.io/cloud-provider-azure/pkg/provider/config"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
)

const providerName = "azure"

var (
	//go:embed assets/*
	assetsFs  embed.FS
	templates = []common.TemplateSource{
		{ReferenceObject: &appsv1.Deployment{}, EmbedFsPath: "assets/cloud-controller-manager-deployment.yaml"},
		{ReferenceObject: &appsv1.DaemonSet{}, EmbedFsPath: "assets/cloud-node-manager-daemonset.yaml"},
		{ReferenceObject: &rbacv1.Role{}, EmbedFsPath: "assets/azure-cloud-provider-role.yaml"},
		{ReferenceObject: &rbacv1.RoleBinding{}, EmbedFsPath: "assets/azure-cloud-provider-rolebinding.yaml"},
		{ReferenceObject: &rbacv1.ClusterRole{}, EmbedFsPath: "assets/azure-cloud-controller-manager-clusterrole.yaml"},
		{ReferenceObject: &rbacv1.ClusterRoleBinding{}, EmbedFsPath: "assets/azure-cloud-controller-manager-clusterrolebinding.yaml"},
		{ReferenceObject: &admissionregistrationv1.ValidatingAdmissionPolicy{}, EmbedFsPath: "assets/validating-admission-policy.yaml"},
		{ReferenceObject: &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}, EmbedFsPath: "assets/validating-admission-policy-binding.yaml"},
		{ReferenceObject: &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}, EmbedFsPath: "assets/validating-admission-service-annotation-policy-binding.yaml"},
		{ReferenceObject: &admissionregistrationv1.ValidatingAdmissionPolicy{}, EmbedFsPath: "assets/validating-admission-service-annotation-policy.yaml"},
	}
)

var (
	validAzureCloudNames = map[configv1.AzureCloudEnvironment]struct{}{
		configv1.AzurePublicCloud:       struct{}{},
		configv1.AzureUSGovernmentCloud: struct{}{},
		configv1.AzureChinaCloud:        struct{}{},
		configv1.AzureGermanCloud:       struct{}{},
		configv1.AzureStackCloud:        struct{}{},
	}

	validAzureCloudNameValues = func() []string {
		v := make([]string, 0, len(validAzureCloudNames))
		for n := range validAzureCloudNames {
			v = append(v, string(n))
		}
		slices.Sort(v)
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

// IsAzure ensures that the underlying platform is Azure. It will fail if the
// CloudName is AzureStack as we handle it separately with it's own
// CloudConfigTransformer.
func IsAzure(infra *configv1.Infrastructure) bool {
	if infra.Status.PlatformStatus != nil {
		if infra.Status.PlatformStatus.Type == configv1.AzurePlatformType &&
			(infra.Status.PlatformStatus.Azure.CloudName != configv1.AzureStackCloud) {
			return true
		}
	}
	return false
}

func CloudConfigTransformer(source string, infra *configv1.Infrastructure, network *configv1.Network, features featuregates.FeatureGate) (string, error) {
	if !IsAzure(infra) {
		return "", fmt.Errorf("invalid platform, expected CloudName to be %s", configv1.AzurePublicCloud)
	}

	var cfg azureconfig.Config
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
			if _, ok := validAzureCloudNames[c]; !ok {
				return "", field.NotSupported(field.NewPath("status", "platformStatus", "azure", "cloudName"), c, validAzureCloudNameValues)
			}
			cloud = c
		}
	}

	// Ensure cloud set in cloud.conf matches infra
	if cfg.Cloud != "" {
		if !strings.EqualFold(string(cloud), cfg.Cloud) {
			return "",
				fmt.Errorf(`invalid user-provided cloud.conf: \"cloud\" field in user-provided
				cloud.conf conflicts with infrastructure object`)
		}
	}
	cfg.Cloud = string(cloud)

	// If the virtual machine type is not set we need to make sure it uses the
	// "standard" instance type. See OCPBUGS-25483 and OCPBUGS-20213 for more
	// information
	if cfg.VMType == "" {
		cfg.VMType = azureconsts.VMTypeStandard
	}

	// Ensure we are using the shared health probe
	cfg.ClusterServiceLoadBalancerHealthProbeMode = azureconsts.ClusterServiceLoadBalancerHealthProbeModeShared

	cfgbytes, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to marshal the cloud.conf: %w", err)
	}
	return string(cfgbytes), nil
}
