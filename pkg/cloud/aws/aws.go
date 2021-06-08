package aws

import (
	"embed"
	"encoding/json"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	//go:embed assets/*
	fs embed.FS
	//go:embed bootstrap/*
	bootstrapFs embed.FS
)

var (
	templateSources = []common.TemplateSource{
		{Object: &appsv1.Deployment{}, Path: "assets/deployment.yaml"},
	}

	bootstrapTemplateSources = []common.TemplateSource{
		{Object: &corev1.Pod{}, Path: "bootstrap/pod.yaml"},
	}
)

type imagesReference struct {
	CloudControllerManager string `json:"cloudControllerManagerAWS"`
}

type awsAssets struct {
	platform           configv1.PlatformType
	Images             imagesReference
	operatorConfig     config.OperatorConfig
	templates          []common.ObjectTemplate
	bootstrapTemplates []common.ObjectTemplate
}

func (assets *awsAssets) GetResources() ([]client.Object, error) {
	return common.RenderTemplates(assets.templates, assets)

}

func (assets *awsAssets) GetBootsrapResources() ([]client.Object, error) {
	return common.RenderTemplates(assets.bootstrapTemplates, assets)

}

func (assets *awsAssets) GetOperatorConfig() config.OperatorConfig {
	return assets.operatorConfig
}

func (assets *awsAssets) GetPlatformType() configv1.PlatformType {
	return assets.platform
}

func NewAssets(config config.OperatorConfig) (common.ProviderAssets, error) {
	assets := awsAssets{}
	assets.operatorConfig = config
	assets.platform = config.Platform
	assets.Images = imagesReference{}
	if err := json.Unmarshal(config.ImagesFileContent, &assets.Images); err != nil {
		return nil, err
	}

	var err error

	assets.bootstrapTemplates, err = common.ReadTemplates(bootstrapFs, bootstrapTemplateSources)
	if err != nil {
		return nil, err
	}

	assets.templates, err = common.ReadTemplates(fs, templateSources)
	if err != nil {
		return nil, err
	}

	return &assets, nil
}
