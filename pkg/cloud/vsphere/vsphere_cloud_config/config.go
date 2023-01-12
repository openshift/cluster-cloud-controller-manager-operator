package vsphere_cloud_config

import (
	"fmt"

	"k8s.io/klog/v2"

	"gopkg.in/yaml.v2"
)

func ReadConfig(config []byte) (*CPIConfig, error) {
	if len(config) == 0 {
		return nil, fmt.Errorf("vSphere config is empty")
	}

	klog.V(3).Info("Try to parse vSphere config, yaml format first")
	cfg, err := readCPIConfigYAML(config)
	if err != nil {
		klog.Warningf("Parsing yaml config failed, fallback to ini: %s", err)

		cfg, err = readCPIConfigINI(config)
		if err != nil {
			klog.Errorf("ini config parsing failed: %s", err)
			return nil, err
		}

		klog.V(3).Info("ini config parsed successfully")
	}

	return cfg, nil
}

func MarshalConfig(config *CPIConfig) (string, error) {
	yamlBytes, err := yaml.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("can not marshal config into yaml: %w", err)
	}
	return string(yamlBytes), nil
}
