package aws

import (
	"testing"

	. "github.com/onsi/gomega"

	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
)

func TestCloudConfigTransformer(t *testing.T) {
	testCases := []struct {
		name     string
		source   string
		expected string
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
		},
		{
			name:   "completely empty source",
			source: "", // This could happen in cases where the cluster was born prior to a cloud.conf being required.
			expected: `[Global]
DisableSecurityGroupIngress                     = false
ClusterServiceLoadBalancerHealthProbeMode       = Shared
ClusterServiceSharedLoadBalancerHealthProbePort = 0
`,
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
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			gotConfig, err := CloudConfigTransformer(tc.source, nil, nil, featuregates.NewFeatureGate(nil, nil)) // No Infra or Network are required for the current functionality.
			g.Expect(err).ToNot(HaveOccurred())

			g.Expect(gotConfig).To(Equal(tc.expected))
		})
	}
}
