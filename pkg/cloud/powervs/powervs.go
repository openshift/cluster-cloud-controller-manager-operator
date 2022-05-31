package powervs

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"

	configv1 "github.com/openshift/api/config/v1"

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

// CloudConfigTransformer implements the cloudConfigTransformer. It uses the input ConfigMap and
// infra.status.platformStatus.PowerVS.ServiceEndpoints
// to create a new config that include the ServiceOverrides sections
// It returns an error if the platform is not PowerVSPlatformType or if any errors are
// encountered while attempting to rework the configuration.
func CloudConfigTransformer(source string, infra *configv1.Infrastructure, network *configv1.Network) (string, error) {
	if infra.Status.PlatformStatus == nil ||
		infra.Status.PlatformStatus.Type != configv1.PowerVSPlatformType {
		return "", fmt.Errorf("invalid platform, expected to be %s", configv1.PowerVSPlatformType)
	}

	if infra.Status.PlatformStatus.PowerVS == nil || len(infra.Status.PlatformStatus.PowerVS.ServiceEndpoints) == 0 {
		return source, nil
	}
	overrides, err := serviceOverrides(infra.Status.PlatformStatus.PowerVS.ServiceEndpoints)
	if err != nil {
		return "", fmt.Errorf("failed to create service overrides section for cloud.conf: %w", err)
	}

	cloudCfg := bytes.NewBufferString(source)
	_, err = cloudCfg.WriteString(overrides)
	if err != nil {
		return "", fmt.Errorf("failed to append service overrides section for cloud.conf: %w", err)
	}
	return cloudCfg.String(), nil
}

// serviceOverrides returns a section of configuration with custom service endpoints
func serviceOverrides(overrides []configv1.PowerVSServiceEndpoint) (string, error) {
	input := struct {
		ServiceOverrides []configv1.PowerVSServiceEndpoint
	}{ServiceOverrides: overrides}

	buf := &bytes.Buffer{}
	err := template.Must(template.New("service_overrides").Parse(serviceOverrideTmpl)).Execute(buf, input)
	return buf.String(), err
}

// serviceOverrideTmpl can be used to generate a list of serviceOverride sections given,
// input: {ServiceOverrides (list configv1.PowerVSServiceEndpoint)}
var serviceOverrideTmpl = `
{{- range $idx, $service := .ServiceOverrides }}
[ServiceOverride "{{ $idx }}"]
	Service = {{ $service.Name }}
	URL = {{ $service.URL }}
{{ end }}`
