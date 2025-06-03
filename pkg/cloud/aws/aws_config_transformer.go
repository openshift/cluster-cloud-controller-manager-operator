package aws

import (
	"bytes"
	"fmt"
	"sort"

	configv1 "github.com/openshift/api/config/v1"

	"gopkg.in/gcfg.v1"
	"gopkg.in/ini.v1"

	awsconfig "k8s.io/cloud-provider-aws/pkg/providers/v1/config"
)

// CloudConfigTransformer is used to inject OpenShift configuration defaults into the Cloud Provider config
// for the AWS Cloud Provider.
func CloudConfigTransformer(source string, infra *configv1.Infrastructure, network *configv1.Network) (string, error) {
	cfg, err := readAWSConfig(source)
	if err != nil {
		return "", fmt.Errorf("failed to read the cloud.conf: %w", err)
	}

	setOpenShiftDefaults(cfg)

	return marshalAWSConfig(cfg)
}

func readAWSConfig(source string) (*awsconfig.CloudConfig, error) {
	if len(source) == 0 {
		return nil, fmt.Errorf("empty INI file")
	}

	// Use the same method the AWS CCM uses to load configuration.
	cfg := &awsconfig.CloudConfig{}
	if err := gcfg.FatalOnly(gcfg.ReadStringInto(cfg, source)); err != nil {
		return nil, fmt.Errorf("failed to parse INI file: %w", err)
	}

	return cfg, nil
}

func marshalAWSConfig(cfg *awsconfig.CloudConfig) (string, error) {
	file := ini.Empty()
	if err := file.Section("Global").ReflectFrom(&cfg.Global); err != nil {
		return "", fmt.Errorf("failed to reflect global config: %w", err)
	}

	for id, override := range cfg.ServiceOverride {
		if err := file.Section(fmt.Sprintf("ServiceOverride %q", id)).ReflectFrom(override); err != nil {
			return "", fmt.Errorf("failed to reflect service override: %w", err)
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
	if cfg.Global.NLBSecurityGroupMode != awsconfig.NLBSecurityGroupModeManaged {
		// OpenShift enforces security group by default when deploying
		// service type loadbalancer NLB.
		cfg.Global.NLBSecurityGroupMode = awsconfig.NLBSecurityGroupModeManaged
	}
}
