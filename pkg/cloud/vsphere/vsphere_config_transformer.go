package vsphere

import (
	"fmt"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/utils/net"

	ccmConfig "github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/vsphere/vsphere_cloud_config"
)

// Well-known OCP-specific vSphere tags. These values are going to the "labels" sections in CCM cloud-config.
// Such tags are meant to be on vSphere resources, such as clusters and datacenters to figure out and properly set up
// K8s topology labels on node objects.
// https://github.com/kubernetes/cloud-provider-vsphere/blob/release-1.25/docs/book/cloud_config.md#labels
// https://github.com/openshift/enhancements/blob/f6b33eb0cd4ba060af71fee6192297cf6bc31e5a/enhancements/installer/vsphere-ipi-zonal.md#cloud-config
const (
	regionLabelValue = "openshift-region"
	zoneLabelValue   = "openshift-zone"
)

// CloudConfigTransformer takes the user-provided, legacy cloud provider-compatible configuration and
// modifies it to be compatible with the external cloud provider.
// Returns an error if the platform is not VSpherePlatformType or if any errors were encountered while attempting
// to transform a configuration.
// Currently, CloudConfigTransformer is responsible to populate vcenters, labels, and node networking parameters from
// the Infrastructure resource.
// Also, this function converts legacy deprecated INI configuration format to a YAML-based one.
func CloudConfigTransformer(source string, infra *configv1.Infrastructure, network *configv1.Network) (string, error) {
	if infra.Status.PlatformStatus == nil ||
		infra.Status.PlatformStatus.Type != configv1.VSpherePlatformType {
		return "", fmt.Errorf("invalid platform, expected to be %s", configv1.VSpherePlatformType)
	}

	cpiCfg, err := ccmConfig.ReadConfig([]byte(source))
	if err != nil {
		return "", fmt.Errorf("failed to read the cloud.conf: %w", err)
	}

	// For Zones support new VSphere PlatformSpec was introduced in the Infrastructure resource
	// If such spec exists need to supplement vsphere-cloud-provider config with values from there.
	// https://github.com/openshift/enhancements/blob/f6b33eb0cd4ba060af71fee6192297cf6bc31e5a/enhancements/installer/vsphere-ipi-zonal.md
	// https://github.com/openshift/api/pull/1278
	if infra.Spec.PlatformSpec.VSphere != nil {
		setDualStack(cpiCfg, infra.Status.PlatformStatus.VSphere, &infra.Spec.PlatformSpec.VSphere.NodeNetworking, network)
		setNodes(cpiCfg, &infra.Spec.PlatformSpec.VSphere.NodeNetworking)
		setVirtualCenters(cpiCfg, infra.Spec.PlatformSpec.VSphere)

		// labels should only be applied if length of failuredomains is
		// greater than one so existing single (or non-zonal) installs function.
		if len(infra.Spec.PlatformSpec.VSphere.FailureDomains) > 1 {
			cpiCfg.Labels.Zone = zoneLabelValue
			cpiCfg.Labels.Region = regionLabelValue
		}
	}

	return ccmConfig.MarshalConfig(cpiCfg)
}

// setNodes sets Nodes section in vsphere-cloud-provider config according passed VSpherePlatformNodeNetworking spec
func setNodes(cfg *ccmConfig.CPIConfig, nodeNetworking *configv1.VSpherePlatformNodeNetworking) {
	cfg.Nodes.ExternalVMNetworkName = nodeNetworking.External.Network
	cfg.Nodes.ExternalNetworkSubnetCIDR = strings.Join(nodeNetworking.External.NetworkSubnetCIDR, ",")
	cfg.Nodes.ExcludeExternalNetworkSubnetCIDR = strings.Join(nodeNetworking.External.ExcludeNetworkSubnetCIDR, ",")

	cfg.Nodes.InternalVMNetworkName = nodeNetworking.Internal.Network
	cfg.Nodes.InternalNetworkSubnetCIDR = strings.Join(nodeNetworking.Internal.NetworkSubnetCIDR, ",")
	cfg.Nodes.ExcludeInternalNetworkSubnetCIDR = strings.Join(nodeNetworking.Internal.ExcludeNetworkSubnetCIDR, ",")
}

// setVirtualCenters sets vcenter server sections according passed VSpherePlatformSpec
func setVirtualCenters(cfg *ccmConfig.CPIConfig, vSphereSpec *configv1.VSpherePlatformSpec) {
	for _, vcenter := range vSphereSpec.VCenters {
		cfg.Vcenter[vcenter.Server] = &ccmConfig.VirtualCenterConfig{
			VCenterIP:   vcenter.Server,
			VCenterPort: uint(vcenter.Port),
			Datacenters: vcenter.Datacenters,
		}
	}

	for _, fd := range vSphereSpec.FailureDomains {
		vcenterCfg, ok := cfg.Vcenter[fd.Server]
		if !ok {
			cfg.Vcenter[fd.Server] = &ccmConfig.VirtualCenterConfig{
				VCenterIP:   fd.Server,
				Datacenters: []string{fd.Topology.Datacenter},
			}
		}

		dcSeen := false
		for _, dc := range vcenterCfg.Datacenters {
			if dc == fd.Topology.Datacenter {
				dcSeen = true
				break
			}
		}

		if !dcSeen {
			vcenterCfg.Datacenters = append(vcenterCfg.Datacenters, fd.Topology.Datacenter)
		}
	}
}

// setDualStack updates the configuration required by the cloud-provider-vsphere to explicitly set
// value of IPFamilyPriority instead of using the default which is IPv4. This is needed by the
// cloud provider in order to properly filter IP addresses that feed the instance metadata.
//
// We rely on the Service Networks configuration that initially comes from o/installer and later
// from the Cluster Network Operator as those two components take care of validating that clusters
// with dual-stack configuration have exactly 2 of them and that they match the required order.
//
// We are mangling with the ExcludeNetworkSubnetCIDR param here because VM agent by default detects
// also IP addresses that are used by us internally and which should never be exposed as node IPs
// (i.e. API VIP and Ingress VIP for IPI installations and fd69::2 which is internal to OVN-K8s).
//
// Ref.: https://issues.redhat.com/browse/OCPBUGS-18641
func setDualStack(cfg *ccmConfig.CPIConfig, status *configv1.VSpherePlatformStatus, nodeNetworking *configv1.VSpherePlatformNodeNetworking, network *configv1.Network) {
	if network != nil && len(network.Spec.ServiceNetwork) == 2 {
		// Extensive validations are performed by o/installer so that here we already know that
		// if the configuration is dual-stack, we will have exactly 2 service networks and if
		// single-stack then 1 service network. Simplified logic here is applied to avoid code
		// duplication.
		//
		// Ref.: https://github.com/openshift/installer/blob/6471b31/pkg/types/validation/installconfig.go#L241
		if net.IsIPv4CIDRString(network.Spec.ServiceNetwork[0]) {
			cfg.Global.IPFamilyPriority = []string{"ipv4", "ipv6"}
		} else {
			cfg.Global.IPFamilyPriority = []string{"ipv6", "ipv4"}
		}

		if status != nil {
			for _, addr := range append(status.APIServerInternalIPs, status.IngressIPs...) {
				if net.IsIPv4String(addr) {
					addr = addr + "/32"
				} else {
					addr = addr + "/128"
				}
				nodeNetworking.External.ExcludeNetworkSubnetCIDR = append(nodeNetworking.External.ExcludeNetworkSubnetCIDR, addr)
				nodeNetworking.Internal.ExcludeNetworkSubnetCIDR = append(nodeNetworking.Internal.ExcludeNetworkSubnetCIDR, addr)
			}
		}

		nodeNetworking.External.ExcludeNetworkSubnetCIDR = append(nodeNetworking.External.ExcludeNetworkSubnetCIDR, "fd69::2/128")
		nodeNetworking.Internal.ExcludeNetworkSubnetCIDR = append(nodeNetworking.Internal.ExcludeNetworkSubnetCIDR, "fd69::2/128")
	}
}
