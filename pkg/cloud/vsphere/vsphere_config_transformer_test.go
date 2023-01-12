package vsphere

import (
	"testing"

	"github.com/onsi/gomega"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ccm "k8s.io/cloud-provider-vsphere/pkg/cloudprovider/vsphere/config"
)

func makeInfrastructureResource(platform configv1.PlatformType, zonal, nodeNetworking, emptyVSpherePlatformSpec bool) *configv1.Infrastructure {
	vspherePlatformSpec := &configv1.VSpherePlatformSpec{}
	var platformSpec configv1.PlatformSpec

	platformSpec.Type = platform

	if nodeNetworking {
		vspherePlatformSpec.NodeNetworking.External.Network = "external-network"
		vspherePlatformSpec.NodeNetworking.Internal.Network = "internal-network"

		vspherePlatformSpec.NodeNetworking.External.NetworkSubnetCIDR = []string{
			"198.51.100.0/24",
			"fe80::3/128",
		}
		vspherePlatformSpec.NodeNetworking.External.ExcludeNetworkSubnetCIDR = []string{
			"192.1.2.0/24",
			"fe80::2/128",
		}

		vspherePlatformSpec.NodeNetworking.Internal.ExcludeNetworkSubnetCIDR = []string{
			"192.0.2.0/24",
			"fe80::1/128",
		}

		vspherePlatformSpec.NodeNetworking.Internal.NetworkSubnetCIDR = []string{
			"192.0.3.0/24",
			"fe80::4/128",
		}
	}

	if zonal {
		vcenterSpec := configv1.VSpherePlatformVCenterSpec{
			Server: "test-server",
			Port:   443,
			Datacenters: []string{
				"DC1",
				"DC2",
			},
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
		vspherePlatformSpec.FailureDomains = append(vspherePlatformSpec.FailureDomains, failureDomainSpec...)
		vspherePlatformSpec.VCenters = append(vspherePlatformSpec.VCenters, vcenterSpec)
	}
	if !emptyVSpherePlatformSpec {
		platformSpec.VSphere = vspherePlatformSpec
	}

	return &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Status: configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				Type: platform,
			},
		},
		Spec: configv1.InfrastructureSpec{
			CloudConfig: configv1.ConfigMapFileReference{
				Name: infraCloudConfName,
				Key:  infraCloudConfKey,
			},
			PlatformSpec: platformSpec,
		},
	}
}

func makeNetworkResource(network operatorv1.NetworkType) *configv1.Network {
	return &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Status: configv1.NetworkStatus{
			NetworkType: string(network),
		},
		Spec: configv1.NetworkSpec{
			NetworkType: string(network),
		},
	}
}

func TestCloudConfigTransformer(t *testing.T) {
	type args struct {
		source  string
		infra   *configv1.Infrastructure
		network *configv1.Network
	}
	type testConfig struct {
		name   string
		args   args
		want   string
		errMsg string
	}

	var tests []testConfig

	tests = append(tests, testConfig{
		name: "in-tree-to-external",
		args: args{
			source: `
[Global]
secret-name = "vsphere-creds"
secret-namespace = "kube-system"
insecure-flag = "1"

[Workspace]
server = "test-server"
datacenter = "DC1"
default-datastore = "Datastore"
folder = "/DC1/vm/folder"

[VirtualCenter "test-server"]
datacenters = "DC1"`,
			infra: makeInfrastructureResource(configv1.VSpherePlatformType,
				false,
				false,
				false),
			network: makeNetworkResource(operatorv1.NetworkTypeOpenShiftSDN),
		},
		want: `
[Global]
secret-name = "vsphere-creds"
secret-namespace = "kube-system"
insecure-flag = "1"

[VirtualCenter "test-server"]
datacenters = "DC1"`,
		errMsg: "",
	})

	tests = append(tests, testConfig{
		name: "labels-already-exist",
		args: args{
			source: `
[Global]
secret-name = "vsphere-creds"
secret-namespace = "kube-system"
insecure-flag = "1"

[Workspace]
server = "vcenter.sddc-44-236-21-251.vmwarevmc.com"
datacenter = "SDDC-Datacenter"
default-datastore = "WorkloadDatastore"
folder = "/SDDC-Datacenter/vm/jcallen"

[VirtualCenter "vcenter.sddc-44-236-21-251.vmwarevmc.com"]
datacenters = "SDDC-Datacenter"

[Labels]
region = "k8s-region"
zone = "k8s-zone"`,
			infra: makeInfrastructureResource(configv1.VSpherePlatformType,
				true,
				false,
				false),
			network: makeNetworkResource(operatorv1.NetworkTypeOpenShiftSDN),
		},
		want: `
[Global]
secret-name = "vsphere-creds"
secret-namespace = "kube-system"
insecure-flag = "1"

[VirtualCenter "test-server"]
datacenters = "DC1,DC2,DC3"

[Labels]
region = "openshift-region"
zone = "openshift-zone"`,
		errMsg: "",
	})

	tests = append(tests, testConfig{
		name: "Node Networking",
		args: args{
			source: `
[Global]
secret-name = "vsphere-creds"
secret-namespace = "kube-system"
insecure-flag = "1"

[Workspace]
server = "vcenter.sddc-44-236-21-251.vmwarevmc.com"
datacenter = "SDDC-Datacenter"
default-datastore = "WorkloadDatastore"
folder = "/SDDC-Datacenter/vm/jcallen"

[VirtualCenter "test-server"]
datacenters = "SDDC-Datacenter"`,
			infra: makeInfrastructureResource(configv1.VSpherePlatformType,
				false,
				true,
				false),
			network: makeNetworkResource(operatorv1.NetworkTypeOpenShiftSDN),
		},
		want: `
[Global]
secret-name = "vsphere-creds"
secret-namespace = "kube-system"
insecure-flag = "1"

[VirtualCenter "test-server"]
datacenters = "SDDC-Datacenter"

[Nodes]
exclude-external-network-subnet-cidr = "192.1.2.0/24,fe80::2/128"
exclude-internal-network-subnet-cidr = "192.0.2.0/24,fe80::1/128"
external-network-subnet-cidr = "198.51.100.0/24,fe80::3/128"
external-vm-network-name = "external-network"
internal-network-subnet-cidr = "192.0.3.0/24,fe80::4/128"
internal-vm-network-name = "internal-network"`,
		errMsg: "",
	})

	tests = append(tests, testConfig{
		name: "Invalid Platform",
		args: args{
			source: "",
			infra: makeInfrastructureResource(configv1.AWSPlatformType,
				false,
				false,
				false),
			network: makeNetworkResource(operatorv1.NetworkTypeOpenShiftSDN),
		},
		want:   "",
		errMsg: "invalid platform, expected to be VSphere",
	})

	tests = append(tests, testConfig{
		name: "Pre 4.13 Cluster, empty vSphere platform spec",
		args: args{
			source: `
[Global]
secret-name = "vsphere-creds"
secret-namespace = "kube-system"
insecure-flag = "1"

[Workspace]
server = "test-server"
datacenter = "DC1"
default-datastore = "Datastore"
folder = "/DC1/vm/folder"

[VirtualCenter "test-server"]
datacenters = "DC1"`,
			infra: makeInfrastructureResource(configv1.VSpherePlatformType,
				false,
				false,
				true),
			network: makeNetworkResource(operatorv1.NetworkTypeOpenShiftSDN),
		},
		want: `
[Global]
secret-name = "vsphere-creds"
secret-namespace = "kube-system"
insecure-flag = "1"

[VirtualCenter "test-server"]
datacenters = "DC1"`,
		errMsg: "",
	})

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)

			got, err := CloudConfigTransformer(tc.args.source, tc.args.infra, tc.args.network)
			if tc.errMsg != "" {
				g.Expect(err).Should(gomega.MatchError(tc.errMsg))
				return
			}

			// String ordering of INI file is problematic
			// Using the CCM INI reader to output CPIConfig struct
			// that can be compared instead of trying to
			// compare a string that might not be equal just
			// do to position within file.

			wantConfig, err := ccm.ReadCPIConfig([]byte(tc.want))
			if err != nil {
				g.Expect(err).Should(gomega.Equal(nil))
				return
			}
			gotConfig, err := ccm.ReadCPIConfig([]byte(got))
			if err != nil {
				g.Expect(err).Should(gomega.Equal(nil))
				return
			}

			g.Expect(gotConfig).Should(gomega.BeComparableTo(wantConfig))
		})
	}
}
