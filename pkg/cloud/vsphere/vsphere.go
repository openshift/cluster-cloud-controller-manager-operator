package vsphere

import (
	"bytes"
	"embed"
	"fmt"
	"strconv"
	"strings"

	"github.com/asaskevich/govalidator"
	configv1 "github.com/openshift/api/config/v1"
	"gopkg.in/ini.v1"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
)

const (
	providerName = "vsphere"

	// see manifests/0000_26_cloud-controller-manager-operator_16_credentialsrequest-vsphere.yaml
	globalCredsSecretName = "vsphere-cloud-credentials"
)

var (
	//go:embed assets/*
	assetsFs  embed.FS
	templates = []common.TemplateSource{
		{ReferenceObject: &appsv1.Deployment{}, EmbedFsPath: "assets/cloud-controller-manager-deployment.yaml"},
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

type vsphereAssets struct {
	operatorConfig    config.OperatorConfig
	renderedResources []client.Object
}

func (assets *vsphereAssets) GetRenderedResources() []client.Object {
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
		CloudControllerManager: config.ImagesReference.CloudControllerManagerVSphere,
	}
	_, err := govalidator.ValidateStruct(images)
	if err != nil {
		return nil, fmt.Errorf("%s: missed images in config: %v", providerName, err)
	}
	assets := &vsphereAssets{
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

func CloudConfigTransformer(source string, infra *configv1.Infrastructure, network *configv1.Network) (string, error) {
	if infra.Status.PlatformStatus == nil ||
		infra.Status.PlatformStatus.Type != configv1.VSpherePlatformType {
		return "", fmt.Errorf("invalid platform, expected to be %s", configv1.VSpherePlatformType)
	}

	cfg, err := ini.Load([]byte(source))
	if err != nil {
		return "", fmt.Errorf("failed to read the cloud.conf: %w", err)
	}

	// Workspace no longer exists in CCM
	if cfg.HasSection("Workspace") {
		klog.V(3).Infof("Section: Workspace is no longer used, removing.")
		cfg.DeleteSection("Workspace")
	}

	if infra.Spec.PlatformSpec.VSphere != nil {
		if err := setNodesSection(cfg, &infra.Spec.PlatformSpec.VSphere.NodeNetworking); err != nil {
			return "", err
		}
		if err := setVirtualCentersFromInfra(cfg, infra); err != nil {
			return "", fmt.Errorf("could not set VirtualCenter section: %w", err)
		}

		if len(infra.Spec.PlatformSpec.VSphere.FailureDomains) != 0 {
			if err = setDatacentersFromFailureDomains(cfg, infra); err != nil {
				return "", fmt.Errorf("could not set Failure Domain datacenters to VirtualCenter section: %w", err)
			}
		}
	}

	var buf bytes.Buffer

	_, err = cfg.WriteTo(&buf)
	if err != nil {
		return "", fmt.Errorf("failed to modify the provided configuration: %w", err)
	}

	return buf.String(), nil
}
func setVirtualCentersFromInfra(cfg *ini.File, infra *configv1.Infrastructure) error {
	if len(infra.Spec.PlatformSpec.VSphere.VCenters) != 0 {
		// delete all existing [VirtualCenter] sections
		// they will be recreated below
		for _, section := range cfg.Sections() {
			if strings.Contains(section.Name(), "VirtualCenter") {
				cfg.DeleteSection(section.Name())
			}
		}
	}
	for _, vcenter := range infra.Spec.PlatformSpec.VSphere.VCenters {
		vcenterSectionName := fmt.Sprintf("VirtualCenter \"%s\"", vcenter.Server)
		vCenterSection, err := setOrGetSection(cfg, vcenterSectionName)
		if err != nil {
			return fmt.Errorf("could not get or set VirtualCenter section: %w", err)
		}

		if err := setVCenterPortKey(vCenterSection, vcenter.Port); err != nil {
			return fmt.Errorf("could not set VirtualCenters port value: %w", err)
		}

		datacenters := strings.Join(vcenter.Datacenters[:], ",")
		_, err = setOrGetKeyValue(vCenterSection, "datacenters", datacenters, true)
		if err != nil {
			return fmt.Errorf("could not get or set the datacenters key value: %w", err)
		}
	}
	return nil
}

func setDatacentersFromFailureDomains(cfg *ini.File, infra *configv1.Infrastructure) error {
	if err := setLabelsSection(cfg); err != nil {
		return fmt.Errorf("could not set Labels section: %w", err)
	}

	for _, fd := range infra.Spec.PlatformSpec.VSphere.FailureDomains {
		vcenterSectionName := fmt.Sprintf("VirtualCenter \"%s\"", fd.Server)
		vCenterSection, err := setOrGetSection(cfg, vcenterSectionName)
		if err != nil {
			return err
		}

		err = setVCenterDatacentersKey(vCenterSection, fd.Topology.Datacenter)
		if err != nil {
			return err
		}
	}
	return nil
}

func setLabelsSection(cfg *ini.File) error {
	labels := map[string]string{"region": "openshift-region", "zone": "openshift-zone"}

	labelsSection, err := setOrGetSection(cfg, "Labels")
	if err != nil {
		return err
	}

	for k, v := range labels {
		_, err = setOrGetKeyValue(labelsSection, k, v, true)
		if err != nil {
			return err
		}
	}

	// Check to make sure the [Labels] section doesn't have
	// any incorrect keys
	for _, k := range labelsSection.Keys() {
		if _, ok := labels[k.Name()]; !ok {
			klog.Warningf("Key: %s should not be in section: %s, removing.", k.Name(), labelsSection.Name())
			labelsSection.DeleteKey(k.Name())
		}
	}

	return nil
}

func setVCenterPortKey(vCenterSection *ini.Section, port int32) error {
	_, err := setOrGetKeyValue(vCenterSection, "port", strconv.FormatInt(int64(port), 10), true)
	return err
}

func setVCenterDatacentersKey(vCenterSection *ini.Section, datacenter string) error {
	// Get existing key (don't overwrite) or get new key
	key, err := setOrGetKeyValue(vCenterSection, "datacenters", datacenter, false)
	if err != nil {
		return err
	}

	datacenters := key.String()

	if !strings.Contains(datacenters, datacenter) {
		klog.V(3).Infof("Appending %s to existing datacenters key value %s", datacenter, datacenters)
		datacenters = fmt.Sprintf("%s,%s", datacenters, datacenter)
		key.SetValue(datacenters)
	}

	return nil
}

func setNodesSection(cfg *ini.File, nodeNetworking *configv1.VSpherePlatformNodeNetworking) error {
	keyValues := map[string]string{
		"external-vm-network-name":             nodeNetworking.External.Network,
		"internal-vm-network-name":             nodeNetworking.Internal.Network,
		"external-network-subnet-cidr":         strings.Join(nodeNetworking.External.NetworkSubnetCIDR[:], ","),
		"exclude-external-network-subnet-cidr": strings.Join(nodeNetworking.External.ExcludeNetworkSubnetCIDR[:], ","),
		"internal-network-subnet-cidr":         strings.Join(nodeNetworking.Internal.NetworkSubnetCIDR[:], ","),
		"exclude-internal-network-subnet-cidr": strings.Join(nodeNetworking.Internal.ExcludeNetworkSubnetCIDR[:], ","),
	}

	nodesSection, err := setOrGetSection(cfg, "Nodes")
	if err != nil {
		return err
	}

	for k, v := range keyValues {
		if v != "" {
			_, err = setOrGetKeyValue(nodesSection, k, v, true)
			if err != nil {
				return err
			}
		}
	}

	// if there are no new or existing keys on [Nodes] section
	// [Nodes] should not exist, delete section
	if len(nodesSection.Keys()) == 0 {
		cfg.DeleteSection("Nodes")
	}

	return nil
}

func setOrGetSection(cfg *ini.File, name string) (*ini.Section, error) {
	if cfg.HasSection(name) {
		klog.V(5).Infof("Section found: %s", name)
		return cfg.GetSection(name)
	} else {
		klog.V(5).Infof("Creating new section: %s", name)
		return cfg.NewSection(name)
	}
}

func setOrGetKeyValue(section *ini.Section, name, value string, overwrite bool) (*ini.Key, error) {
	var err error
	var key *ini.Key
	if !section.HasKey(name) {
		klog.V(5).Infof("Creating key: %s with value: %s", name, value)
		key, err = section.NewKey(name, value)
	} else {
		key, err = section.GetKey(name)
		if err != nil {
			return nil, err
		}
		if overwrite {
			klog.V(5).Infof("Setting key: %s with value: %s", name, value)
			key.SetValue(value)
		}
	}
	return key, err
}
