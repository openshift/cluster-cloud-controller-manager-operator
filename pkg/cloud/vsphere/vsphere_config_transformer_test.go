package vsphere

import (
	"testing"

	gmg "github.com/onsi/gomega"

	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ccm "k8s.io/cloud-provider-vsphere/pkg/cloudprovider/vsphere/config"
)

const (
	infraCloudConfName = "test-config"
	infraCloudConfKey  = "foo"
)

func newVsphereInfraBuilder() infraBuilder {
	return infraBuilder{
		platform: configv1.VSpherePlatformType,
		platformSpec: configv1.PlatformSpec{
			Type:    configv1.VSpherePlatformType,
			VSphere: &configv1.VSpherePlatformSpec{},
		},
	}
}

type infraBuilder struct {
	platform     configv1.PlatformType
	platformSpec configv1.PlatformSpec
}

func (b infraBuilder) Build() *configv1.Infrastructure {
	return &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				Type: b.platform,
			},
		},
		Spec: configv1.InfrastructureSpec{
			CloudConfig: configv1.ConfigMapFileReference{
				Name: infraCloudConfName,
				Key:  infraCloudConfKey,
			},
			PlatformSpec: b.platformSpec,
		},
	}
}

func (b infraBuilder) withPlatform(platform configv1.PlatformType) infraBuilder {
	b.platform = platform
	return b
}

func (b infraBuilder) withVSphereNodeNetworking() infraBuilder {
	vspereSpecRef := b.platformSpec.VSphere

	vspereSpecRef.NodeNetworking.External.Network = "external-network"
	vspereSpecRef.NodeNetworking.Internal.Network = "internal-network"

	vspereSpecRef.NodeNetworking.External.NetworkSubnetCIDR = []string{"198.51.100.0/24", "fe80::3/128"}
	vspereSpecRef.NodeNetworking.External.ExcludeNetworkSubnetCIDR = []string{"192.1.2.0/24", "fe80::2/128"}

	vspereSpecRef.NodeNetworking.Internal.ExcludeNetworkSubnetCIDR = []string{"192.0.2.0/24", "fe80::1/128"}
	vspereSpecRef.NodeNetworking.Internal.NetworkSubnetCIDR = []string{"192.0.3.0/24", "fe80::4/128"}

	return b
}

func (b infraBuilder) withVSphereZones() infraBuilder {
	vcenterSpec := configv1.VSpherePlatformVCenterSpec{
		Server:      "test-server",
		Port:        443,
		Datacenters: []string{"DC1", "DC2"},
	}
	failureDomainSpec := []configv1.VSpherePlatformFailureDomainSpec{
		{
			Name:   "east-1a",
			Region: "east",
			Zone:   "east-1a",
			Server: "test-server",
			Topology: configv1.VSpherePlatformTopology{
				Datacenter:     "DC1",
				Datastore:      "DS1",
				ComputeCluster: "C1",
				Networks:       []string{"N1"},
				ResourcePool:   "RP1",
				Folder:         "F1",
			},
		}, {
			Name:   "east-2a",
			Region: "east",
			Zone:   "east-2a",
			Server: "test-server",
			Topology: configv1.VSpherePlatformTopology{
				Datacenter:     "DC2",
				Datastore:      "DS2",
				ComputeCluster: "C2",
				Networks:       []string{"N2"},
				ResourcePool:   "RP2",
				Folder:         "F2",
			},
		},
		{
			Name:   "west-1a",
			Region: "west",
			Zone:   "west-1a",
			Server: "test-server",
			Topology: configv1.VSpherePlatformTopology{
				Datacenter:     "DC3",
				Datastore:      "DS3",
				ComputeCluster: "C3",
				Networks:       []string{"N3"},
				ResourcePool:   "RP3",
				Folder:         "F3",
			},
		},
	}
	vspereSpecRef := b.platformSpec.VSphere
	vspereSpecRef.FailureDomains = append(vspereSpecRef.FailureDomains, failureDomainSpec...)
	vspereSpecRef.VCenters = append(vspereSpecRef.VCenters, vcenterSpec)
	return b
}

func makeDummyNetworkConfig() *configv1.Network {
	return &configv1.Network{}
}

const iniConfigWithWorkspace = `
[Global]
secret-name = "vsphere-creds"
secret-namespace = "kube-system"
insecure-flag = "1"

[Workspace]
server = "test-server"
datacenter = "DC1"
default-datastore = "Datastore"
folder = "/DC1/vm/F1"

[VirtualCenter "test-server"]
datacenters = "DC1"`

const yamlConfig = `
global:
  insecureFlag: true
  secretName: vsphere-creds
  secretNamespace: kube-system
vcenter:
  test-server:
    server: test-server
    datacenters:
    - DC1`

const iniConfigWithExistingLabels = `
[Global]
secret-name = "vsphere-creds"
secret-namespace = "kube-system"
insecure-flag = "1"

[VirtualCenter "test-server"]
datacenters = "DC1"

[Labels]
region = "openshift-region"
zone = "openshift-zone"`

const iniConfigWithoutWorkspace = `
[Global]
secret-name = "vsphere-creds"
secret-namespace = "kube-system"
insecure-flag = "1"

[VirtualCenter "test-server"]
datacenters = "DC1"`

const iniConfigNodeNetworking = `
[Global]
secret-name = "vsphere-creds"
secret-namespace = "kube-system"
insecure-flag = "1"

[VirtualCenter "test-server"]
datacenters = "DC1"

[Nodes]
exclude-external-network-subnet-cidr = "192.1.2.0/24,fe80::2/128"
exclude-internal-network-subnet-cidr = "192.0.2.0/24,fe80::1/128"
external-network-subnet-cidr = "198.51.100.0/24,fe80::3/128"
external-vm-network-name = "external-network"
internal-network-subnet-cidr = "192.0.3.0/24,fe80::4/128"
internal-vm-network-name = "internal-network"`

const yamlConfigNodeNetworking = `
global:
  insecureFlag: true
  secretName: vsphere-creds
  secretNamespace: kube-system
vcenter:
  test-server:
    server: test-server
    datacenters:
    - DC1
nodes:
  internalNetworkSubnetCidr: 192.0.3.0/24,fe80::4/128
  externalNetworkSubnetCidr: 198.51.100.0/24,fe80::3/128
  internalVmNetworkName: internal-network
  externalVmNetworkName: external-network
  excludeInternalNetworkSubnetCidr: 192.0.2.0/24,fe80::1/128
  excludeExternalNetworkSubnetCidr: 192.1.2.0/24,fe80::2/128`

const iniConfigZonal = `
[Global]
secret-name = "vsphere-creds"
secret-namespace = "kube-system"
insecure-flag = "1"

[VirtualCenter "test-server"]
datacenters = "DC1,DC2,DC3"

[Labels]
region = "openshift-region"
zone = "openshift-zone"`

const yamlConfigZonal = `
global:
  insecureFlag: true
  secretName: vsphere-creds
  secretNamespace: kube-system
vcenter:
  test-server:
    server: test-server
    port: 443
    datacenters:
    - DC1
    - DC2
    - DC3
labels:
  zone: openshift-zone
  region: openshift-region`

func TestCloudConfigTransformer(t *testing.T) {
	testcases := []struct {
		name             string
		infraBuilder     infraBuilder
		inputConfig      string
		equivalentConfig string
		errMsg           string
	}{
		{
			name:             "in-tree to external with empty infra",
			infraBuilder:     newVsphereInfraBuilder(),
			inputConfig:      iniConfigWithWorkspace,
			equivalentConfig: iniConfigWithoutWorkspace,
		},
		{
			name:             "in-tree to external with node networking",
			infraBuilder:     newVsphereInfraBuilder().withVSphereNodeNetworking(),
			inputConfig:      iniConfigWithWorkspace,
			equivalentConfig: iniConfigNodeNetworking,
		},
		{
			name:             "populating labels datacenters from zones config",
			infraBuilder:     newVsphereInfraBuilder().withVSphereZones(),
			inputConfig:      iniConfigWithWorkspace,
			equivalentConfig: iniConfigZonal,
		},
		{
			name:             "replacing existing labels with openshift specific",
			infraBuilder:     newVsphereInfraBuilder().withVSphereZones(),
			inputConfig:      iniConfigWithExistingLabels,
			equivalentConfig: iniConfigZonal,
		},
		{
			name:             "yaml and ini config parsing results should be the same",
			infraBuilder:     newVsphereInfraBuilder(),
			inputConfig:      yamlConfig,
			equivalentConfig: iniConfigWithoutWorkspace,
		},
		{
			name:             "yaml and ini config parsing results should be the same, with zones",
			infraBuilder:     newVsphereInfraBuilder().withVSphereZones(),
			inputConfig:      yamlConfigZonal,
			equivalentConfig: iniConfigZonal,
		},
		{
			name:             "yaml and ini config parsing results should be the same, node networking",
			infraBuilder:     newVsphereInfraBuilder().withVSphereNodeNetworking(),
			inputConfig:      yamlConfigNodeNetworking,
			equivalentConfig: iniConfigNodeNetworking,
		},
		{
			name:             "yaml config should contain node networking if it's specified in infra",
			infraBuilder:     newVsphereInfraBuilder().withVSphereNodeNetworking(),
			inputConfig:      yamlConfig,
			equivalentConfig: yamlConfigNodeNetworking,
		},
		{
			name:             "yaml config should be populated with datacenters and labels if failure domains specified",
			infraBuilder:     newVsphereInfraBuilder().withVSphereZones(),
			inputConfig:      yamlConfig,
			equivalentConfig: yamlConfigZonal,
		},
		{
			name:         "empty input",
			infraBuilder: newVsphereInfraBuilder(),
			errMsg:       "failed to read the cloud.conf: vSphere config is empty",
		},
		{
			name:         "incorrect platform",
			infraBuilder: newVsphereInfraBuilder().withPlatform(configv1.NonePlatformType),
			errMsg:       "invalid platform, expected to be VSphere",
		},
		{
			name:         "invalid ini input",
			infraBuilder: newVsphereInfraBuilder(),
			inputConfig:  ":",
			errMsg:       "failed to read the cloud.conf",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			g := gmg.NewWithT(t)
			infraResouce := tc.infraBuilder.Build()
			transformedConfig, err := CloudConfigTransformer(tc.inputConfig, infraResouce, makeDummyNetworkConfig())
			if tc.errMsg != "" {
				g.Expect(err).To(gmg.MatchError(gmg.ContainSubstring(tc.errMsg)))
				return
			}

			// Using CPI config reader from cloud-provider-vsphere
			// to ensure that config transformation produces valid yaml config which
			// will be readable and usable by the CCM then
			wantConfig, err := ccm.ReadCPIConfig([]byte(tc.equivalentConfig))
			g.Expect(err).ShouldNot(gmg.HaveOccurred())

			gotConfig, err := ccm.ReadCPIConfig([]byte(transformedConfig))
			g.Expect(err).ShouldNot(gmg.HaveOccurred())

			g.Expect(gotConfig).Should(gmg.BeComparableTo(wantConfig))
		})
	}
}
