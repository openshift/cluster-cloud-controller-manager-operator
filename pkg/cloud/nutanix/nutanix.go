package nutanix

import (
	"embed"
	"fmt"

	"github.com/asaskevich/govalidator"
	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
)

const (
	providerName = "nutanix"

	// see manifests/0000_26_cloud-controller-manager-operator_16_credentialsrequest-nutanix.yaml
	globalCredsSecretName = "nutanix-credentials"
)

var (
	//go:embed assets/*
	assetsFs  embed.FS
	templates = []common.TemplateSource{
		{ReferenceObject: &appsv1.Deployment{}, EmbedFsPath: "assets/cloud-controller-manager-deployment.yaml"},
		{ReferenceObject: &rbacv1.Role{}, EmbedFsPath: "assets/nutanix-cloud-controller-manager-role.yaml"},
		{ReferenceObject: &rbacv1.RoleBinding{}, EmbedFsPath: "assets/nutanix-cloud-controller-manager-rolebinding.yaml"},
		{ReferenceObject: &rbacv1.ClusterRole{}, EmbedFsPath: "assets/nutanix-cloud-controller-manager-clusterrole.yaml"},
		{ReferenceObject: &rbacv1.ClusterRoleBinding{}, EmbedFsPath: "assets/nutanix-cloud-controller-manager-clusterrolebinding.yaml"},
	}
)

type imagesReference struct {
	CloudControllerManager string `valid:"required"`
}

var templateValuesValidationMap = map[string]interface{}{
	"images":                     "required",
	"infrastructureName":         "required,type(string)",
	"globalCredsSecretNamespace": "required,type(string)",
	"globalCredsSecretName":      "required,type(string)",
	"cloudproviderName":          "required,type(string)",
}

type nutanixAssets struct {
	operatorConfig    config.OperatorConfig
	renderedResources []client.Object
}

func (assets *nutanixAssets) GetRenderedResources() []client.Object {
	return assets.renderedResources
}

func getTemplateValues(images *imagesReference, operatorConfig config.OperatorConfig) (common.TemplateValues, error) {
	values := common.TemplateValues{
		"images":                     images,
		"infrastructureName":         operatorConfig.InfrastructureName,
		"globalCredsSecretNamespace": operatorConfig.ManagedNamespace,
		"globalCredsSecretName":      globalCredsSecretName,
		"cloudproviderName":          operatorConfig.GetPlatformNameString(),
	}
	_, err := govalidator.ValidateMap(values, templateValuesValidationMap)
	if err != nil {
		return nil, err
	}
	return values, nil
}

func NewProviderAssets(config config.OperatorConfig) (common.CloudProviderAssets, error) {
	images := &imagesReference{
		CloudControllerManager: config.ImagesReference.CloudControllerManagerNutanix,
	}
	_, err := govalidator.ValidateStruct(images)
	if err != nil {
		return nil, fmt.Errorf("%s: missed images in config: %v", providerName, err)
	}
	assets := &nutanixAssets{
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
