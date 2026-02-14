package config

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/klog/v2"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/util"
)

// ImagesReference allows build systems to inject ImagesReference for CCCMO components
// This structure widely using for construct CloudProviderAssets objects and represents expected structure of
// `cloud-controller-manager-images` config map which manages and populating by `openshift-cluster-version-operator`.
// See manifests/0000_26_cloud-controller-manager-operator_01_images.configmap.yaml
type ImagesReference struct {
	CloudControllerManagerOperator  string `json:"cloudControllerManagerOperator"`
	CloudControllerManagerAWS       string `json:"cloudControllerManagerAWS"`
	CloudControllerManagerAzure     string `json:"cloudControllerManagerAzure"`
	CloudNodeManagerAzure           string `json:"cloudNodeManagerAzure"`
	CloudControllerManagerGCP       string `json:"cloudControllerManagerGCP"`
	CloudControllerManagerIBM       string `json:"cloudControllerManagerIBM"`
	CloudControllerManagerOpenStack string `json:"cloudControllerManagerOpenStack"`
	CloudControllerManagerVSphere   string `json:"cloudControllerManagerVSphere"`
	CloudControllerManagerPowerVS   string `json:"cloudControllerManagerPowerVS"`
	CloudControllerManagerNutanix   string `json:"cloudControllerManagerNutanix"`
}

// OperatorConfig contains configuration values for templating resources
type OperatorConfig struct {
	ManagedNamespace   string
	ImagesReference    ImagesReference
	IsSingleReplica    bool
	InfrastructureName string
	PlatformStatus     *configv1.PlatformStatus
	ClusterProxy       *configv1.Proxy
	FeatureGates       string
	OCPFeatureGates    featuregates.FeatureGate
	// TLSCipherSuites is a comma-separated list of TLS cipher suites for CCM --tls-cipher-suites flag
	TLSCipherSuites string
	// TLSMinVersion is the minimum TLS version for CCM --tls-min-version flag
	TLSMinVersion string
}

func (cfg *OperatorConfig) GetPlatformNameString() string {
	var platformName string
	if cfg.PlatformStatus != nil {
		platformName = string(cfg.PlatformStatus.Type)
	}
	return platformName
}

// checkInfrastructureResource checks Infrastructure resource for platform status presence
func checkInfrastructureResource(infra *configv1.Infrastructure) error {
	if infra == nil || infra.Status.PlatformStatus == nil {
		return fmt.Errorf("platform status is not populated on infrastructure")
	}
	if infra.Status.PlatformStatus.Type == "" {
		return fmt.Errorf("no platform provider found on infrastructure")
	}

	return nil
}

// getImagesFromJSONFile is used in operator to read the content of mounted ConfigMap
// containing images for substitution in templates
func getImagesFromJSONFile(filePath string) (ImagesReference, error) {
	data, err := os.ReadFile(filepath.Clean(filePath))
	if err != nil {
		return ImagesReference{}, err
	}

	i := ImagesReference{}
	if err := json.Unmarshal(data, &i); err != nil {
		return ImagesReference{}, err
	}
	return i, nil
}

// ComposeConfig creates a Config for operator
func ComposeConfig(infrastructure *configv1.Infrastructure, clusterProxy *configv1.Proxy, imagesFile, managedNamespace string, featureGateAccessor featuregates.FeatureGateAccess, tlsConfig *tls.Config) (OperatorConfig, error) {
	err := checkInfrastructureResource(infrastructure)
	if err != nil {
		klog.Errorf("Unable to get platform from infrastructure: %s", err)
		return OperatorConfig{}, err
	}

	images, err := getImagesFromJSONFile(imagesFile)
	if err != nil {
		klog.Errorf("Unable to decode images file from location %s: %v", imagesFile, err)
		return OperatorConfig{}, err
	}

	featureGatesString := ""
	upstreamGates, err := util.GetUpstreamCloudFeatureGates()
	if err != nil {
		klog.Errorf("Unable to get upstream feature gates: %s", err)
		return OperatorConfig{}, fmt.Errorf("unable to get upstream feature gates: %w", err)
	}

	var features featuregates.FeatureGate
	if featureGateAccessor != nil {
		features, _ = featureGateAccessor.CurrentFeatureGates()
		enabled, _ := util.GetEnabledDisabledFeatures(features, upstreamGates)
		featureGatesString = util.BuildFeatureGateString(enabled, nil)
	}

	// Extract TLS cipher suites and min version from the tls.Config
	tlsCipherSuites, tlsMinVersion := extractTLSSettings(tlsConfig)

	config := OperatorConfig{
		PlatformStatus:     infrastructure.Status.PlatformStatus.DeepCopy(),
		ClusterProxy:       clusterProxy,
		ManagedNamespace:   managedNamespace,
		ImagesReference:    images,
		InfrastructureName: infrastructure.Status.InfrastructureName,
		IsSingleReplica:    infrastructure.Status.ControlPlaneTopology == configv1.SingleReplicaTopologyMode,
		FeatureGates:       featureGatesString,
		OCPFeatureGates:    features,
		TLSCipherSuites:    tlsCipherSuites,
		TLSMinVersion:      tlsMinVersion,
	}

	return config, nil
}

// FormatCipherSuitesForCLI converts a slice of cipher suite names to a comma-separated string
// suitable for use with the --tls-cipher-suites CLI flag.
func FormatCipherSuitesForCLI(ciphers []string) string {
	return strings.Join(ciphers, ",")
}

// extractTLSSettings extracts cipher suite names and min TLS version string from a tls.Config.
// Returns comma-separated cipher suite names and the TLS version string suitable for CLI flags.
func extractTLSSettings(tlsConfig *tls.Config) (cipherSuites, minVersion string) {
	if tlsConfig == nil {
		return "", ""
	}

	// Convert cipher suite IDs to names
	var cipherNames []string
	for _, id := range tlsConfig.CipherSuites {
		name := tls.CipherSuiteName(id)
		if name != "" {
			cipherNames = append(cipherNames, name)
		}
	}
	cipherSuites = strings.Join(cipherNames, ",")

	// Convert min version constant to string
	minVersion = tlsVersionToString(tlsConfig.MinVersion)

	return cipherSuites, minVersion
}

// tlsVersionToString converts a TLS version constant to its string representation.
func tlsVersionToString(version uint16) string {
	switch version {
	case tls.VersionTLS10:
		return "VersionTLS10"
	case tls.VersionTLS11:
		return "VersionTLS11"
	case tls.VersionTLS12:
		return "VersionTLS12"
	case tls.VersionTLS13:
		return "VersionTLS13"
	default:
		return ""
	}
}
