package openstack

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
)

const (
	infraCloudConfName = "test-config"
	infraCloudConfKey  = "foo"
)

func TestResourcesRenderingSmoke(t *testing.T) {

	tc := []struct {
		name   string
		config config.OperatorConfig
		errMsg string
	}{
		{
			name:   "Empty config",
			config: config.OperatorConfig{},
			errMsg: "openstack: missed images in config: CloudControllerManager: non zero value required",
		}, {
			name: "Minimal allowed config",
			config: config.OperatorConfig{
				InfrastructureName: "infra-name",
				ImagesReference: config.ImagesReference{
					CloudControllerManagerOpenStack: "CloudControllerManagerOpenstack",
				},
				PlatformStatus: &configv1.PlatformStatus{Type: configv1.OpenStackPlatformType},
			},
		},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			assets, err := NewProviderAssets(tc.config)
			if tc.errMsg != "" {
				g.Expect(err).Should(MatchError(tc.errMsg))
				return
			} else {
				g.Expect(err).ToNot(HaveOccurred())
			}

			resources := assets.GetRenderedResources()
			g.Expect(resources).Should(HaveLen(1))
		})
	}
}

func makeInfrastructureResource(platform configv1.PlatformType) *configv1.Infrastructure {
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
			PlatformSpec: configv1.PlatformSpec{
				Type: platform,
			},
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

	tc := []struct {
		name    string
		source  string
		infra   *configv1.Infrastructure
		errMsg  string
		network *configv1.Network
	}{
		{
			name:    "Invalid platform",
			source:  "",
			infra:   makeInfrastructureResource(configv1.AWSPlatformType),
			errMsg:  "invalid platform, expected to be OpenStack",
			network: makeNetworkResource(operatorv1.NetworkTypeOpenShiftSDN),
		}, {
			name: "Config with unsupported secret-namespace override",
			source: `[Global]
secret-namespace = foo
secret-name = openstack-credentials`,
			infra:   makeInfrastructureResource(configv1.OpenStackPlatformType),
			errMsg:  "'[Global] secret-namespace' is set to a non-default value",
			network: makeNetworkResource(operatorv1.NetworkTypeOpenShiftSDN),
		}, {
			name: "Config with unsupported secret-name override",
			source: `[Global]
secret-namespace = kube-system
secret-name = foo`,
			infra:   makeInfrastructureResource(configv1.OpenStackPlatformType),
			errMsg:  "'[Global] secret-name' is set to a non-default value",
			network: makeNetworkResource(operatorv1.NetworkTypeOpenShiftSDN),
		}, {
			name: "Config with unsupported kubeconfig-path override",
			source: `[Global]
secret-namespace = kube-system
secret-name = openstack-credentials
kubeconfig-path = https://foo`,
			infra:   makeInfrastructureResource(configv1.OpenStackPlatformType),
			errMsg:  "'[Global] kubeconfig-path' is set to a non-default value",
			network: makeNetworkResource(operatorv1.NetworkTypeOpenShiftSDN),
		}, {
			name:    "Empty config",
			source:  "",
			infra:   makeInfrastructureResource(configv1.OpenStackPlatformType),
			network: makeNetworkResource(operatorv1.NetworkTypeOpenShiftSDN),
		}, {
			name: "Non-empty config",
			source: `[Global]
secret-name = openstack-credentials
secret-namespace = kube-system

[BlockStorage]
ignore-volume-az = true
`,
			infra:   makeInfrastructureResource(configv1.OpenStackPlatformType),
			network: makeNetworkResource(operatorv1.NetworkTypeOpenShiftSDN),
		},
		{
			name: "Non-empty config",
			source: `[Global]
secret-name = openstack-credentials
secret-namespace = kube-system

[BlockStorage]
ignore-volume-az = true

[LoadBalancer]
max-shared-lb          = 1
manage-security-groups = true
use-octavia            = false
`,
			infra:   makeInfrastructureResource(configv1.OpenStackPlatformType),
			network: makeNetworkResource(operatorv1.NetworkTypeOVNKubernetes),
		},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			actual, err := CloudConfigTransformer(tc.source, tc.infra, tc.network)
			if tc.errMsg != "" {
				g.Expect(err).Should(MatchError(tc.errMsg))
				return
			} else {
				expected := `[Global]
use-clouds  = true
clouds-file = /etc/openstack/secret/clouds.yaml
cloud       = openstack

[LoadBalancer]
max-shared-lb          = 1
manage-security-groups = true`
				actual := strings.TrimSpace(actual)
				g.Expect(actual).Should(Equal(expected))
			}
		})
	}
}
