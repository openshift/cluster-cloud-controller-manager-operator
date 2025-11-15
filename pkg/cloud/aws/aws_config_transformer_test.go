package aws

import (
	"testing"

	. "github.com/onsi/gomega"

	configv1 "github.com/openshift/api/config/v1"

	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
)

var mockEmptyFeatureGates = featuregates.NewFeatureGate([]configv1.FeatureGateName{}, []configv1.FeatureGateName{})
var mockEnabledFeatureGates = featuregates.NewFeatureGate([]configv1.FeatureGateName{"AWSServiceLBNetworkSecurityGroup"}, []configv1.FeatureGateName{})
var mockDisabledFeatureGates = featuregates.NewFeatureGate([]configv1.FeatureGateName{}, []configv1.FeatureGateName{"AWSServiceLBNetworkSecurityGroup"})

func TestIsFeatureGateEnabled(t *testing.T) {
	testCases := []struct {
		name        string
		features    featuregates.FeatureGate
		featureName string
		expected    bool
	}{
		{
			name:        "returns false when features is nil",
			features:    nil,
			featureName: "AWSServiceLBNetworkSecurityGroup",
			expected:    false,
		},
		{
			name:        "returns false when feature name is empty string",
			features:    mockEnabledFeatureGates,
			featureName: "",
			expected:    false,
		},
		{
			name:        "returns false when feature is not registered (empty feature gate)",
			features:    mockEmptyFeatureGates,
			featureName: "AWSServiceLBNetworkSecurityGroup",
			expected:    false,
		},
		{
			name:        "returns true when feature is enabled",
			features:    mockEnabledFeatureGates,
			featureName: "AWSServiceLBNetworkSecurityGroup",
			expected:    true,
		},
		{
			name:        "returns false when feature is explicitly disabled",
			features:    mockDisabledFeatureGates,
			featureName: "AWSServiceLBNetworkSecurityGroup",
			expected:    false,
		},
		{
			name:        "returns false when feature is not in known features list",
			features:    featuregates.NewFeatureGate([]configv1.FeatureGateName{"SomeOtherFeature"}, []configv1.FeatureGateName{}),
			featureName: "AWSServiceLBNetworkSecurityGroup",
			expected:    false,
		},
		{
			name:        "returns false for unknown feature with disabled features registered",
			features:    featuregates.NewFeatureGate([]configv1.FeatureGateName{}, []configv1.FeatureGateName{"SomeOtherFeature"}),
			featureName: "AWSServiceLBNetworkSecurityGroup",
			expected:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			result := isFeatureGateEnabled(tc.features, tc.featureName)
			g.Expect(result).To(Equal(tc.expected), "Expected isFeatureGateEnabled to return %v for feature '%s'", tc.expected, tc.featureName)
		})
	}
}

func TestCloudConfigTransformer(t *testing.T) {
	testCases := []struct {
		name     string
		source   string
		expected string
		features featuregates.FeatureGate
	}{
		{
			name: "default source",
			source: `[Global]
			`, // This is the default that gets created for any OpenShift Cluster.
			expected: `[Global]
DisableSecurityGroupIngress                     = false
ClusterServiceLoadBalancerHealthProbeMode       = Shared
ClusterServiceSharedLoadBalancerHealthProbePort = 0
`,
			features: mockEmptyFeatureGates,
		},
		{
			name:   "completely empty source",
			source: "", // This could happen in cases where the cluster was born prior to a cloud.conf being required.
			expected: `[Global]
DisableSecurityGroupIngress                     = false
ClusterServiceLoadBalancerHealthProbeMode       = Shared
ClusterServiceSharedLoadBalancerHealthProbePort = 0
`,
			features: mockEmptyFeatureGates,
		},
		{
			name: "with existing configuration",
			source: `[Global]
DisableSecurityGroupIngress = true
Zone                        = Foo
`, // This is the default that gets created for any OpenShift Cluster.
			expected: `[Global]
Zone                                            = Foo
DisableSecurityGroupIngress                     = true
ClusterServiceLoadBalancerHealthProbeMode       = Shared
ClusterServiceSharedLoadBalancerHealthProbePort = 0
`, // Ordered based on the order of fields in the AWS CloudConfig struct.
			features: mockEmptyFeatureGates,
		},
		{
			name: "with existing configuration and overrides",
			source: `[Global]
DisableSecurityGroupIngress = true
Zone                        = Foo

[ServiceOverride "1"]
Service         = ec2
Region          = us-west-2
URL             = https://ec2.foo.bar
SigningRegion   = signing_region

[ServiceOverride "2"]
Service         = s3
Region          = us-west-1
URL             = https://s3.foo.bar
SigningRegion   = signing_region
`, // Cluster Config Operator currently writes service overrides into the managed configuration for us.
			expected: `[Global]
Zone                                            = Foo
DisableSecurityGroupIngress                     = true
ClusterServiceLoadBalancerHealthProbeMode       = Shared
ClusterServiceSharedLoadBalancerHealthProbePort = 0

[ServiceOverride "1"]
Service       = ec2
Region        = us-west-2
URL           = https://ec2.foo.bar
SigningRegion = signing_region

[ServiceOverride "2"]
Service       = s3
Region        = us-west-1
URL           = https://s3.foo.bar
SigningRegion = signing_region
`, // Ordered based on the order of fields in the AWS CloudConfig struct.
			features: mockEmptyFeatureGates,
		},
		{
			name: "with AWSServiceLBNetworkSecurityGroup feature gate enabled",
			source: `[Global]
DisableSecurityGroupIngress = true
Zone                        = Foo
`,
			expected: `[Global]
Zone                                            = Foo
DisableSecurityGroupIngress                     = true
ClusterServiceLoadBalancerHealthProbeMode       = Shared
ClusterServiceSharedLoadBalancerHealthProbePort = 0
NLBSecurityGroupMode                            = Managed
`,
			features: mockEnabledFeatureGates,
		},
		{
			name: "with AWSServiceLBNetworkSecurityGroup feature gate disabled",
			source: `[Global]
DisableSecurityGroupIngress = true
Zone                        = Foo
`,
			expected: `[Global]
Zone                                            = Foo
DisableSecurityGroupIngress                     = true
ClusterServiceLoadBalancerHealthProbeMode       = Shared
ClusterServiceSharedLoadBalancerHealthProbePort = 0
`,
			features: mockDisabledFeatureGates,
		},
		{
			name: "with NodeIPFamilies",
			source: `[Global]
NodeIPFamilies = ipv4
NodeIPFamilies = ipv6
			`,
			expected: `[Global]
DisableSecurityGroupIngress                     = false
NodeIPFamilies                                  = ipv4
NodeIPFamilies                                  = ipv6
ClusterServiceLoadBalancerHealthProbeMode       = Shared
ClusterServiceSharedLoadBalancerHealthProbePort = 0
`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			gotConfig, err := CloudConfigTransformer(tc.source, nil, nil, tc.features) // No Infra or Network are required for the current functionality.
			g.Expect(err).ToNot(HaveOccurred())

			g.Expect(gotConfig).To(Equal(tc.expected))
		})
	}
}
