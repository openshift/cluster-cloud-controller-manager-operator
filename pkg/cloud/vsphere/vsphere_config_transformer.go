package vsphere

import (
	"fmt"
	"strings"

	configv1 "github.com/openshift/api/config/v1"

	ccmConfig "github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/vsphere/vsphere_cloud_config"
)

const (
	regionLabelValue = "openshift-region"
	zoneLabelValue   = "openshift-zone"
)

func CloudConfigTransformer(source string, infra *configv1.Infrastructure, _ *configv1.Network) (string, error) {
	if infra.Status.PlatformStatus == nil ||
		infra.Status.PlatformStatus.Type != configv1.VSpherePlatformType {
		return "", fmt.Errorf("invalid platform, expected to be %s", configv1.VSpherePlatformType)
	}

	cpiCfg, err := ccmConfig.ReadConfig([]byte(source))

	if err != nil {
		return "", fmt.Errorf("failed to read the cloud.conf: %w", err)
	}

	if infra.Spec.PlatformSpec.VSphere != nil {
		setNodes(cpiCfg, &infra.Spec.PlatformSpec.VSphere.NodeNetworking)
		if err := setVirtualCenters(cpiCfg, infra); err != nil {
			return "", fmt.Errorf("could not set VirtualCenter section: %w", err)
		}

		if len(infra.Spec.PlatformSpec.VSphere.FailureDomains) != 0 {
			cpiCfg.Labels.Zone = zoneLabelValue
			cpiCfg.Labels.Region = regionLabelValue
		}
	}

	return ccmConfig.MarshalConfig(cpiCfg)
}

func setNodes(cfg *ccmConfig.CPIConfig, nodeNetworking *configv1.VSpherePlatformNodeNetworking) {
	cfg.Nodes.ExternalVMNetworkName = nodeNetworking.External.Network
	cfg.Nodes.ExternalNetworkSubnetCIDR = strings.Join(nodeNetworking.External.NetworkSubnetCIDR[:], ",")
	cfg.Nodes.ExcludeExternalNetworkSubnetCIDR = strings.Join(nodeNetworking.External.ExcludeNetworkSubnetCIDR[:], ",")

	cfg.Nodes.InternalVMNetworkName = nodeNetworking.Internal.Network
	cfg.Nodes.InternalNetworkSubnetCIDR = strings.Join(nodeNetworking.Internal.NetworkSubnetCIDR[:], ",")
	cfg.Nodes.ExcludeInternalNetworkSubnetCIDR = strings.Join(nodeNetworking.Internal.ExcludeNetworkSubnetCIDR[:], ",")
}

func setVirtualCenters(cfg *ccmConfig.CPIConfig, infra *configv1.Infrastructure) error {
	for _, vcenter := range infra.Spec.PlatformSpec.VSphere.VCenters {
		cfg.Vcenter[vcenter.Server] = &ccmConfig.VirtualCenterConfig{
			VCenterIP:   vcenter.Server,
			VCenterPort: uint(vcenter.Port),
			Datacenters: vcenter.Datacenters,
		}
	}

	for _, fd := range infra.Spec.PlatformSpec.VSphere.FailureDomains {
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
	return nil
}
