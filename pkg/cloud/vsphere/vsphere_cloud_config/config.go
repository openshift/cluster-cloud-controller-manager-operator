package vsphere_cloud_config

import (
	"errors"
	"fmt"

	"k8s.io/klog/v2"

	"gopkg.in/yaml.v2"
)

func ReadConfig(config []byte) (*CPIConfig, error) {
	if len(config) == 0 {
		return nil, errors.New("vSphere config is empty")
	}

	klog.V(3).Info("Try to parse vSphere config, yaml format first")
	cfg, err := readCPIConfigYAML(config)
	if err != nil {
		klog.Warningf("Parsing yaml config failed, fallback to ini: %v", err)

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

func MarshalConfig(config *CPIConfig) (string, error) {
	yamlBytes, err := yaml.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("can not marshal config into yaml: %w", err)
	}
	return string(yamlBytes), nil
}
