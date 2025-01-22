package gcp

import (
	"embed"
	"fmt"

	"github.com/asaskevich/govalidator"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
)

const providerName = "gcp"

var (
	//go:embed assets/*.yaml
	assetsFs  embed.FS
	templates = []common.TemplateSource{
		{ReferenceObject: &appsv1.Deployment{}, EmbedFsPath: "assets/cloud-controller-manager.yaml"},
		{ReferenceObject: &rbacv1.ClusterRole{}, EmbedFsPath: "assets/gcp-cloud-controller-manager-clusterrole.yaml"},
		{ReferenceObject: &rbacv1.ClusterRoleBinding{}, EmbedFsPath: "assets/gcp-cloud-controller-manager-clusterrolebinding.yaml"},
		{ReferenceObject: &admissionregistrationv1.ValidatingAdmissionPolicyBinding{}, EmbedFsPath: "assets/validating-admission-policy-binding.yaml"},
		{ReferenceObject: &admissionregistrationv1.ValidatingAdmissionPolicy{}, EmbedFsPath: "assets/validating-admission-policy.yaml"},
	}
)

type imagesReference struct {
	CloudControllerManager string `valid:"required"`
}

var templateValuesValidationMap = map[string]interface{}{
	"images":             "required",
	"infrastructureName": "required,type(string)",
	"cloudproviderName":  "required,type(string)",
}

type GCPAssets struct {
	operatorConfig    config.OperatorConfig
	renderedResources []client.Object
}

func (assets *GCPAssets) GetRenderedResources() []client.Object {
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
		CloudControllerManager: config.ImagesReference.CloudControllerManagerGCP,
	}
	_, err := govalidator.ValidateStruct(images)
	if err != nil {
		return nil, fmt.Errorf("%s: missed images in config: %v", providerName, err)
	}
	assets := &GCPAssets{
		operatorConfig: config,
	}
	objTemplates, err := common.ReadTemplates(assetsFs, templates)
	if err != nil {
		return nil, fmt.Errorf("could not read templates: %v", err)
	}
	templateValues, err := getTemplateValues(images, config)
	if err != nil {
		return nil, fmt.Errorf("can not construct template values for %s assets: %v", providerName, err)
	}

	assets.renderedResources, err = common.RenderTemplates(objTemplates, templateValues)
	if err != nil {
		return nil, fmt.Errorf("could not render templates: %v", err)
	}
	return assets, nil
}
