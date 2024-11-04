package ibm

import (
	"bytes"
	"embed"
	"fmt"
	"net"
	"net/url"
	"regexp"

	configv1 "github.com/openshift/api/config/v1"
	"gopkg.in/ini.v1"

	"github.com/asaskevich/govalidator"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
)

const providerName = "ibm"

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
	"images":               "required",
	"enablePublicEndpoint": "required",
	"cloudproviderName":    "required,type(string)",
}

type ibmAssets struct {
	operatorConfig    config.OperatorConfig
	renderedResources []client.Object
}

func (assets *ibmAssets) GetRenderedResources() []client.Object {
	return assets.renderedResources
}

func getTemplateValues(images *imagesReference, operatorConfig config.OperatorConfig) (common.TemplateValues, error) {
	values := common.TemplateValues{
		"images":               images,
		"enablePublicEndpoint": fmt.Sprintf("%t", getEnablePublicEndpointValue(operatorConfig)),
		"cloudproviderName":    operatorConfig.GetPlatformNameString(),
	}
	_, err := govalidator.ValidateMap(values, templateValuesValidationMap)
	if err != nil {
		return nil, err
	}
	return values, nil
}

func getEnablePublicEndpointValue(operatorConfig config.OperatorConfig) bool {
	if operatorConfig.PlatformStatus == nil {
		return false
	}
	switch operatorConfig.PlatformStatus.Type {
	case configv1.PowerVSPlatformType:
		// For Power VS Platform enable public endpoints
		return true
	default:
		return false
	}
}

func NewProviderAssets(config config.OperatorConfig) (common.CloudProviderAssets, error) {
	images := &imagesReference{
		CloudControllerManager: config.ImagesReference.CloudControllerManagerIBM,
	}

	_, err := govalidator.ValidateStruct(images)
	if err != nil {
		return nil, fmt.Errorf("%s: missed images in config: %v", providerName, err)
	}
	assets := &ibmAssets{
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

// CloudConfigTransformer implements the cloudConfigTransformer. Using the provided ConfigMap and
// infra.Spec.PlatformSpec.IBMCloud.ServiceEndpoints it returns an updated configmap with endpoint overrides if present
// An error is returned if platform does not meet the expected format or errors from validating the endpoint overrides
func CloudConfigTransformer(source string, infra *configv1.Infrastructure, network *configv1.Network) (string, error) {
	if infra.Status.PlatformStatus == nil || infra.Status.PlatformStatus.Type != configv1.IBMCloudPlatformType {
		return "", fmt.Errorf("invalid platform for IBM cloud config transformer")
	}

	// undefined IBMCloud platform spec, noop
	if infra.Spec.PlatformSpec.IBMCloud == nil {
		return source, nil
	}

	// validate provided Service Endpoint overrides
	if err := validateOverrides(infra.Spec.PlatformSpec.IBMCloud.ServiceEndpoints); err != nil {
		return "", err
	}

	loadedConfig, err := ini.Load([]byte(source))
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal the ini config: %w", err)
	}

	if !loadedConfig.HasSection("provider") {
		return "", fmt.Errorf("fatal format error, provided source does not have expected provider section")
	}

	providerSection := loadedConfig.Section("provider")

	// delete old keys, covers case where user removes an override from spec and expects the corresponding override to also be deleted
	providerSection.DeleteKey("iamEndpointOverride")
	providerSection.DeleteKey("g2EndpointOverride")
	providerSection.DeleteKey("rmEndpointOverride")

	for _, override := range infra.Spec.PlatformSpec.IBMCloud.ServiceEndpoints {
		switch override.Name {
		case configv1.IBMCloudServiceIAM:
			providerSection.NewKey("iamEndpointOverride", override.URL)
		case configv1.IBMCloudServiceVPC:
			providerSection.NewKey("g2EndpointOverride", override.URL)
		case configv1.IBMCloudServiceResourceManager:
			providerSection.NewKey("rmEndpointOverride", override.URL)
		default:
			klog.Infof("ignoring unsupported override key for cloud provider config: %s", override.Name)
		}
	}

	var bufUpdated bytes.Buffer
	loadedConfig.WriteTo(&bufUpdated)

	// update the platform status to reflect platform spec
	infra.Status.PlatformStatus.IBMCloud.ServiceEndpoints = infra.Spec.PlatformSpec.IBMCloud.ServiceEndpoints
	return bufUpdated.String(), nil
}

// validate endpoint override list does not contain malformed entries
func validateOverrides(overrides []configv1.IBMCloudServiceEndpoint) error {
	overridenNames := make(map[string]int)
	for _, override := range overrides {
		if len(override.Name) == 0 || len(override.URL) == 0 {
			return fmt.Errorf("failed to validate submitted override, one of URL or Name was empty")
		}
		if overridenNames[string(override.Name)] != 0 {
			return fmt.Errorf("error, service endpoint override contained duplicate entries for same name %s", override.Name)
		}
		if err := validateURL(override.URL); err != nil {
			return err
		}
		overridenNames[string(override.Name)] = 1
	}
	return nil
}

// validate URL is not malformed, host exists, and is using https as scheme
func validateURL(uri string) error {
	httpsScheme := "https"
	// regular expression for the final segment of URL path matching api versioning of the resource
	versionPath := regexp.MustCompile(`(^\/(api\/)?v\d+[/]{0,9})$`)

	parsedURL, err := url.Parse(uri)
	if err != nil {
		return err
	}

	// validate the host exists
	_, err = net.LookupIP(parsedURL.Host)
	if err != nil {
		return fmt.Errorf("error validating host exists, with error: %w", err)
	}

	// ensure scheme is https
	if parsedURL.Scheme != httpsScheme {
		return fmt.Errorf("expected https scheme but received unexpected url scheme %s", parsedURL.Scheme)
	}

	// URI must contain hostname
	if parsedURL.Hostname() == "" {
		return fmt.Errorf("empty hostname provided, it cannot be empty")
	}

	// ensure that the resource path is either empty ('/') or follows pattern of /api/v1 or /v1
	if r := parsedURL.RequestURI(); r != "/" && !versionPath.MatchString(r) {
		return fmt.Errorf("error invalid path present in URI, %s was provided", r)
	}

	return nil
}
