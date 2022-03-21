package openstack

import (
	"testing"

	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	ini "gopkg.in/ini.v1"
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
				ImagesReference: config.ImagesReference{
					CloudControllerManagerOpenStack: "CloudControllerManagerOpenstack",
				},
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
			g.Expect(resources).Should(HaveLen(2))
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

func TestCloudConfigTransformer(t *testing.T) {

	tc := []struct {
		name   string
		source string
		infra  *configv1.Infrastructure
		errMsg string
	}{
		{
			name:   "Invalid platform",
			source: "",
			infra:  makeInfrastructureResource(configv1.AWSPlatformType),
			errMsg: "invalid platform, expected to be OpenStack",
		}, {
			name: "Config with unsupported secret-namespace override",
			source: `[Global]
secret-namespace = foo
secret-name = openstack-credentials`,
			infra:  makeInfrastructureResource(configv1.OpenStackPlatformType),
			errMsg: "'[Global] secret-namespace' is set to a non-default value",
		}, {
			name: "Config with unsupported secret-name override",
			source: `[Global]
secret-namespace = kube-system
secret-name = foo`,
			infra:  makeInfrastructureResource(configv1.OpenStackPlatformType),
			errMsg: "'[Global] secret-name' is set to a non-default value",
		}, {
			name: "Config with unsupported kubeconfig-path override",
			source: `[Global]
secret-namespace = kube-system
secret-name = openstack-credentials
kubeconfig-path = https://foo`,
			infra:  makeInfrastructureResource(configv1.OpenStackPlatformType),
			errMsg: "'[Global] kubeconfig-path' is set to a non-default value",
		}, {
			name:   "Empty config",
			source: "",
			infra:  makeInfrastructureResource(configv1.OpenStackPlatformType),
		}, {
			name: "Non-empty config",
			source: `[Global]
secret-name = openstack-credentials
secret-namespace = kube-system

[BlockStorage]
ignore-volume-az = true
`,
			infra: makeInfrastructureResource(configv1.OpenStackPlatformType),
		},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			output, err := CloudConfigTransformer(tc.source, tc.infra)
			if tc.errMsg != "" {
				g.Expect(err).Should(MatchError(tc.errMsg))
				return
			} else {
				g.Expect(err).ToNot(HaveOccurred())
				// The output is unsorted so we must reload and reparse the
				// strings
				expected, err := ini.Load([]byte(`[Global]
use-clouds  = true
clouds-file = /etc/openstack/secret/clouds.yaml
cloud       = openstack`))
				g.Expect(err).ToNot(HaveOccurred())
				actual, err := ini.Load([]byte(output))
				g.Expect(err).ToNot(HaveOccurred())

				// Because things aren't sorted, we need to manually iterate
				// over sections and keys
				g.Expect(expected.SectionStrings()).Should(ConsistOf(actual.SectionStrings()))
				for _, sectionName := range expected.SectionStrings() {
					expectedSection, err := expected.GetSection(sectionName)
					g.Expect(err).ToNot(HaveOccurred())
					actualSection, err := actual.GetSection(sectionName)
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(expectedSection.KeyStrings()).Should(ConsistOf(actualSection.KeyStrings()))
					for _, keyName := range expectedSection.KeyStrings() {
						expectedKey, err := expectedSection.GetKey(keyName)
						g.Expect(err).ToNot(HaveOccurred())
						actualKey, err := actualSection.GetKey(keyName)
						g.Expect(err).ToNot(HaveOccurred())
						g.Expect(expectedKey.String()).Should(Equal(actualKey.String()))
					}
				}
			}
		})
	}
}
