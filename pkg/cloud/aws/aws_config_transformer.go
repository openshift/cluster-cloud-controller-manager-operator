package aws

import (
	"bytes"
	"fmt"
	"sort"

	configv1 "github.com/openshift/api/config/v1"

	"gopkg.in/gcfg.v1"
	"gopkg.in/ini.v1"

	awsconfig "k8s.io/cloud-provider-aws/pkg/providers/v1/config"

	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
)

// defaultConfig is a string holding the absolute bare minimum INI string that the AWS CCM needs to start.
// The value will be further customized for OCP in the CloudConfigTransformer.
const defaultConfig = `[Global]
`

// CloudConfigTransformer is used to inject OpenShift configuration defaults into the Cloud Provider config
// for the AWS Cloud Provider. If an empty source string is provided, a minimal default configuration will be created.
func CloudConfigTransformer(source string, infra *configv1.Infrastructure, network *configv1.Network, features featuregates.FeatureGate) (string, error) {
	cfg, err := readAWSConfig(source)
	if err != nil {
		return "", fmt.Errorf("failed to read the cloud.conf: %w", err)
	}

	setOpenShiftDefaults(cfg)

	return marshalAWSConfig(cfg)
}

// readAWSConfig will parse a source string into a proper *awsconfig.CloudConfig.
// If an empty source string is provided, a default configuration will be used.
func readAWSConfig(source string) (*awsconfig.CloudConfig, error) {
	cfg := &awsconfig.CloudConfig{}

	// There are cases in which a cloud config was not installed with a cluster, and this is valid.
	// We should, however, populate the configuration so that it doesn't fail on later versions that require
	// a cloud.conf.
	if len(source) == 0 {
		source = defaultConfig
	}

	// Use the same method the AWS CCM uses to load configuration.
	if err := gcfg.FatalOnly(gcfg.ReadStringInto(cfg, source)); err != nil {
		return nil, fmt.Errorf("failed to parse INI file: %w", err)
	}

	return cfg, nil
}

func marshalAWSConfig(cfg *awsconfig.CloudConfig) (string, error) {
	// Configure iniv1 to allow shadow fields to enable multiple entries of NodeIPFamilies.
	file := ini.Empty(ini.LoadOptions{AllowShadows: true})
	if err := file.Section("Global").ReflectFrom(&cfg.Global); err != nil {
		return "", fmt.Errorf("failed to reflect global config: %w", err)
	}

	for id, override := range cfg.ServiceOverride {
		if err := file.Section(fmt.Sprintf("ServiceOverride %q", id)).ReflectFrom(override); err != nil {
			return "", fmt.Errorf("failed to reflect service override: %w", err)
		}
	}

	// In dual-stack environment, the CCM expects NodeIPFamilies to be in the format:
	//
	// NodeIPFamilies=ipv4
	// NodeIPFamilies=ipv6
	//
	// However, iniv1 is serializing go slices as comma-separated list, for example:
	//
	// NodeIPFamilies=ipv4,ipv6
	//
	// Below logic ensures the original NodeIPFamilies field is kept as-is after transforming.
	nodeIPKey := file.Section("Global").Key("NodeIPFamilies")
	for i, ipFamily := range cfg.Global.NodeIPFamilies {
		if i == 0 {
			nodeIPKey.SetValue(ipFamily)
		} else if err := nodeIPKey.AddShadow(ipFamily); err != nil {
			return "", fmt.Errorf("failed to set NodeIPFamilies: %w", err)
		}
	}

	for _, section := range file.Sections() {
		for key, value := range section.KeysHash() {
			// Ignore anything that is the zero value for its type.
			// Everything appears as a string in the INI file, so 0 and false are also considered zero values.
			if value == "" {
				section.DeleteKey(key)
			}
		}
	}

	// Ensure service override sections are last and ordered numerically.
	sort.Slice(file.Sections(), func(i, j int) bool {
		return file.Sections()[i].Name() < file.Sections()[j].Name()
	})

	buf := &bytes.Buffer{}

	if _, err := file.WriteTo(buf); err != nil {
		return "", fmt.Errorf("failed to write INI file: %w", err)
	}

	return buf.String(), nil
}

func setOpenShiftDefaults(cfg *awsconfig.CloudConfig) {
	if cfg.Global.ClusterServiceLoadBalancerHealthProbeMode == "" {
		// OpenShift uses Shared mode by default.
		// This attaches the health check for Cluster scope services to the "kube-proxy"
		// health check endpoint served by OVN.
		cfg.Global.ClusterServiceLoadBalancerHealthProbeMode = "Shared"
	}
}
