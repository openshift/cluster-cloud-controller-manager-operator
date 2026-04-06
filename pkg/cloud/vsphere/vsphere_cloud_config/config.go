package vsphere_cloud_config

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/klog/v2"

	"gopkg.in/yaml.v2"
)

// ReadConfig parses vSphere cloud-config file and returns CPIConfig structure
// Accepts both YAML and INI formats as input.
// YAML format takes precedence, in case parsing YAML is not successful function falls back to the legacy INI format.
// Unlike 'cloud-provider-vsphere' version of a similar function, this does ignore environment variables.
func ReadConfig(config []byte) (*CPIConfig, error) {
	if len(config) == 0 {
		return nil, errors.New("vSphere config is empty")
	}

	klog.V(3).Info("Try to parse vSphere config, yaml format first")
	cfg, err := readCPIConfigYAML(config)
	if err != nil {
		klog.V(3).Info("Parsing yaml config failed, fallback to ini")
		klog.V(4).Infof("Yaml config parsing error:\n %s", err.Error())

		cfg, err = readCPIConfigINI(config)
		if err != nil {
			return nil, fmt.Errorf("ini config parsing failed: %w", err)
		}

		klog.V(3).Info("ini config parsed successfully")
	} else {
		klog.V(3).Info("yaml config parsed successfully")
	}

	return cfg, nil
}

// MarshalConfig serializes CPIConfig instance into a YAML document
func MarshalConfig(config *CPIConfig) (string, error) {
	yamlBytes, err := yaml.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("can not marshal config into yaml: %w", err)
	}
	return string(yamlBytes), nil
}

// IsINIFormat checks if the provided configuration is in INI format.
// It returns true if the config appears to be INI format, false otherwise.
// INI format is detected by looking for section headers like [Global], [VirtualCenter ...], etc.
func IsINIFormat(config []byte) bool {
	if len(config) == 0 {
		return false
	}

	configStr := strings.TrimSpace(string(config))
	lines := strings.Split(configStr, "\n")

	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)
		// Skip empty lines and comments
		if trimmedLine == "" || strings.HasPrefix(trimmedLine, "#") || strings.HasPrefix(trimmedLine, ";") {
			continue
		}
		// Check for INI section headers like [Global], [VirtualCenter "..."], [Labels], [Nodes]
		if strings.HasPrefix(trimmedLine, "[") && strings.Contains(trimmedLine, "]") {
			return true
		}
		// If we encounter a line that doesn't look like a section header and isn't a comment,
		// it's likely YAML (like "global:" or "vcenter:")
		break
	}

	return false
}
