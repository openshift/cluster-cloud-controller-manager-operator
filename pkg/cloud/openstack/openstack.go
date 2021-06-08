package openstack

import (
	"embed"
	"encoding/json"
	"fmt"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	//go:embed assets/*
	fs embed.FS
)

var (
	templateSources = []common.TemplateSource{
		{Object: &v1.ConfigMap{}, Path: "assets/config.yaml"},
		{Object: &appsv1.Deployment{}, Path: "assets/deployment.yaml"},
	}
)

type imagesReference struct {
	CloudControllerManager string `json:"cloudControllerManagerOpenStack"`
}

type openstackAssets struct {
	platform           configv1.PlatformType
	Images             imagesReference
	operatorConfig     config.OperatorConfig
	templates          []common.ObjectTemplate
	bootstrapTemplates []common.ObjectTemplate
}

func (assets *openstackAssets) GetResources() ([]client.Object, error) {
	return common.RenderTemplates(assets.templates, assets)
}

func (assets *openstackAssets) GetBootsrapResources() ([]client.Object, error) {
	return nil, fmt.Errorf("bootstrap assets are not implemented yet")
}

func (assets *openstackAssets) GetOperatorConfig() config.OperatorConfig {
	return assets.operatorConfig
}

func (assets *openstackAssets) GetPlatformType() configv1.PlatformType {
	return assets.platform
}

func NewAssets(config config.OperatorConfig) (common.ProviderAssets, error) {
	assets := openstackAssets{}
	assets.operatorConfig = config
	assets.platform = config.Platform
	assets.Images = imagesReference{}
	if err := json.Unmarshal(config.ImagesFileContent, &assets.Images); err != nil {
		return nil, err
	}
	var err error

	assets.templates, err = common.ReadTemplates(fs, templateSources)
	if err != nil {
		return nil, err
	}

	return &assets, nil
}
