package common

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
)

const (
	cloudConfigNamespace = "openshift-cloud-controller-manager"
	cloudConfigName      = "cloud-conf"
)

// GetOcClient returns an OpenShift config/v1 API client (FeatureGates, Infrastructures, etc.).
func GetOcClient(ctx context.Context) (*configv1client.ConfigV1Client, error) {
	restConfig, err := framework.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}
	configClient, err := configv1client.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to openshift client: %w", err)
	}

	return configClient, nil
}

// GetKubeClient returns a core Kubernetes client (Pods, ConfigMaps, Services, etc.).
func GetKubeClient(ctx context.Context) (clientset.Interface, error) {
	restConfig, err := framework.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}
	cs, err := clientset.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to kube clientset: %w", err)
	}

	return cs, nil
}

// IsFeatureEnabled checks if an OpenShift feature gate is enabled by querying the
// FeatureGate resource named "cluster" using the typed OpenShift config API.
//
// This function uses the official OpenShift config/v1 API types for type-safe
// access to feature gate information, providing better performance and maintainability
// compared to dynamic client approaches.
//
// Parameters:
//   - ctx: Context for the API call
//   - featureName: Name of the feature gate to check (e.g., "AWSServiceLBNetworkSecurityGroup")
//
// Returns:
//   - bool: true if the feature is enabled, false if disabled or not found
//   - error: error if the API call fails
//
// Note: For HyperShift clusters, this checks the management cluster's feature gates.
// To check hosted cluster feature gates, use the hosted cluster's kubeconfig.
func IsFeatureEnabled(ctx context.Context, featureName string) (bool, error) {
	// Create typed config client (more efficient than dynamic client)
	oclient, err := GetOcClient(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to create config client: %w", err)
	}

	// Get the FeatureGate resource using typed API
	featureGate, err := oclient.FeatureGates().Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to get FeatureGate 'cluster': %w", err)
	}

	// Iterate through the feature gates status (typed structs)
	for _, fg := range featureGate.Status.FeatureGates {
		// Check enabled list
		for _, enabled := range fg.Enabled {
			if string(enabled.Name) == featureName {
				framework.Logf("Feature %s is enabled (version %s)", featureName, fg.Version)
				return true, nil
			}
		}

		// Check disabled list
		for _, disabled := range fg.Disabled {
			if string(disabled.Name) == featureName {
				framework.Logf("Feature %s is disabled (version %s)", featureName, fg.Version)
				return false, nil
			}
		}
	}

	// Feature not found in either list
	framework.Logf("Feature %s not found in FeatureGate status", featureName)
	return false, nil
}

// GetCloudConfig retrieves the CCM cloud-config ConfigMap.
// When cs is nil, a clientset is created from the current kubeconfig.
// This function must not call Ginkgo control-flow helpers (Skip, Fail, etc.)
// because it is also called from main.go outside a spec context.
func GetCloudConfig(ctx context.Context, cs clientset.Interface) (*v1.ConfigMap, error) {
	var err error
	if cs == nil {
		cs, err = GetKubeClient(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get kubernetes client: %w", err)
		}
	}
	cm, err := cs.CoreV1().ConfigMaps(cloudConfigNamespace).Get(ctx, cloudConfigName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get cloud-config ConfigMap: %w", err)
	}
	return cm, nil
}

// IsConfigPresentCloudConfig checks if a specific configuration key is present in the
// cloud-config data stored in the given ConfigMap. It searches all data entries for an
// INI-style key=value match. Values are split by comma to support multi-value configs
// e.g.: "ipFamilies = IPv4,IPv6" returns ["IPv4", "IPv6"], and
// "NLBSecurityGroupMode" = "Managed" returns ["Managed"].
func IsConfigPresentCloudConfig(cm *v1.ConfigMap, configKey string) (bool, []string, error) {
	if cm == nil {
		return false, nil, fmt.Errorf("ConfigMap is nil")
	}
	if configKey == "" {
		return false, nil, fmt.Errorf("configKey is empty")
	}

	pattern, err := regexp.Compile(`(?m)^\s*` + regexp.QuoteMeta(configKey) + `\s*=\s*(.*)$`)
	if err != nil {
		return false, nil, fmt.Errorf("failed to compile regex for key %q: %w", configKey, err)
	}

	for dataKey, content := range cm.Data {
		allMatches := pattern.FindAllStringSubmatch(content, -1)
		if allMatches == nil {
			continue
		}

		var values []string
		for _, matches := range allMatches {
			rawValue := strings.TrimSpace(matches[1])
			if rawValue == "" {
				continue
			}
			for _, p := range strings.Split(rawValue, ",") {
				if v := strings.TrimSpace(p); v != "" {
					values = append(values, v)
				}
			}
		}

		framework.Logf("Found key %q in ConfigMap data key %q with values: %v", configKey, dataKey, values)
		return true, values, nil
	}

	framework.Logf("Key %q not found in ConfigMap %s/%s", configKey, cm.Namespace, cm.Name)
	return false, nil, nil
}

// IsNLBSecurityGroupModeManaged returns true when the cloud-config has
// NLBSecurityGroupMode set to "Managed".
func IsNLBSecurityGroupModeManaged(cm *v1.ConfigMap) (bool, error) {
	found, values, err := IsConfigPresentCloudConfig(cm, "NLBSecurityGroupMode")
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	return len(values) == 1 && values[0] == "Managed", nil
}

// IsDualStack checks the NodeIPFamilies key in the cloud-config ConfigMap.
// It returns (isDualStack, primaryIPv6, error) where isDualStack is true when
// both IPv4 and IPv6 are present, and primaryIPv6 is true when the first
// entry is IPv6 (e.g. NodeIPFamilies=ipv6 then NodeIPFamilies=ipv4).
// When NodeIPFamilies is absent, both booleans are false with no error.
func IsDualStack(cm *v1.ConfigMap) (bool, bool, error) {
	found, values, err := IsConfigPresentCloudConfig(cm, "NodeIPFamilies")
	if err != nil {
		return false, false, fmt.Errorf("failed to lookup up configuration NodeIPFamilies in cloud-config: %w", err)
	}
	if !found {
		return false, false, nil
	}
	var hasIPv4, hasIPv6 bool
	for _, ipFamily := range values {
		switch strings.ToLower(ipFamily) {
		case "ipv6":
			hasIPv6 = true
		case "ipv4":
			hasIPv4 = true
		}
	}
	primaryIPv6 := len(values) > 0 && strings.ToLower(values[0]) == "ipv6"
	return hasIPv4 && hasIPv6, primaryIPv6, nil
}
