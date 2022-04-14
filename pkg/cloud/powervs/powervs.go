package powervs

import (
	"embed"
	"fmt"

	"github.com/asaskevich/govalidator"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
)

const providerName = "powervs"

var (
	//go:embed assets/*
	assetsFs  embed.FS
	templates = []common.TemplateSource{
		{ReferenceObject: &appsv1.Deployment{}, EmbedFsPath: "assets/deployment.yaml"},
	}
)

type imagesReference struct {
	CloudControllerManager string `valid:"required"`
}

var templateValuesValidationMap = map[string]interface{}{
	"images":            "required",
	"cloudproviderName": "required,type(string)",
}

type powerVSAssets struct {
	operatorConfig    config.OperatorConfig
	renderedResources []client.Object
}

func (assets *powerVSAssets) GetRenderedResources() []client.Object {
	return assets.renderedResources
}

func getTemplateValues(images *imagesReference, operatorConfig config.OperatorConfig) (common.TemplateValues, error) {
	values := common.TemplateValues{
		"images":            images,
		"cloudproviderName": operatorConfig.GetPlatformNameString(),
	}
	_, err := govalidator.ValidateMap(values, templateValuesValidationMap)
	if err != nil {
		return nil, err
	}
	return values, nil
}

func NewProviderAssets(config config.OperatorConfig) (common.CloudProviderAssets, error) {
	images := &imagesReference{
		CloudControllerManager: config.ImagesReference.CloudControllerManagerPowerVS,
	}

	_, err := govalidator.ValidateStruct(images)
	if err != nil {
		return nil, fmt.Errorf("%s: missed images in config: %v", providerName, err)
	}
	assets := &powerVSAssets{
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
