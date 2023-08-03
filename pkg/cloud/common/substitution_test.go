package common

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
)

func TestSetProxySettings(t *testing.T) {
	tc := []struct {
		name               string
		containers         []corev1.Container
		config             config.OperatorConfig
		expectedContainers []corev1.Container
	}{{
		name: "No proxy",
		containers: []corev1.Container{{
			Name: "no_proxy",
			Env: []corev1.EnvVar{{
				Name:  "SomeVar",
				Value: "SomeValue",
			}},
		}},
		expectedContainers: []corev1.Container{{
			Name: "no_proxy",
			Env: []corev1.EnvVar{{
				Name:  "SomeVar",
				Value: "SomeValue",
			}},
		}},
		config: config.OperatorConfig{},
	}, {
		name: "Empty proxy",
		containers: []corev1.Container{{
			Name: "empty_proxy",
			Env: []corev1.EnvVar{{
				Name:  "SomeVar",
				Value: "SomeValue",
			}},
		}},
		expectedContainers: []corev1.Container{{
			Name: "empty_proxy",
			Env: []corev1.EnvVar{{
				Name:  "SomeVar",
				Value: "SomeValue",
			}},
		}},
		config: config.OperatorConfig{
			ClusterProxy: &configv1.Proxy{},
		},
	}, {
		name: "Substitute http proxy",
		containers: []corev1.Container{{
			Name: "http_proxy",
			Env: []corev1.EnvVar{{
				Name:  "SomeVar",
				Value: "SomeValue",
			}},
		}},
		expectedContainers: []corev1.Container{{
			Name: "http_proxy",
			Env: []corev1.EnvVar{
				{
					Name:  "SomeVar",
					Value: "SomeValue",
				}, {
					Name:  "HTTP_PROXY",
					Value: "http://squid.corp.acme.com:3128",
				}},
		}},
		config: config.OperatorConfig{
			ClusterProxy: &configv1.Proxy{
				Status: configv1.ProxyStatus{
					HTTPProxy: "http://squid.corp.acme.com:3128",
				},
			},
		},
	}, {
		name: "Substitute https proxy",
		containers: []corev1.Container{{
			Name: "https_proxy",
			Env: []corev1.EnvVar{{
				Name:  "SomeVar",
				Value: "SomeValue",
			}},
		}},
		expectedContainers: []corev1.Container{{
			Name: "https_proxy",
			Env: []corev1.EnvVar{
				{
					Name:  "SomeVar",
					Value: "SomeValue",
				}, {
					Name:  "HTTPS_PROXY",
					Value: "https://squid.corp.acme.com:3128",
				}},
		}},
		config: config.OperatorConfig{
			ClusterProxy: &configv1.Proxy{
				Status: configv1.ProxyStatus{
					HTTPSProxy: "https://squid.corp.acme.com:3128",
				},
			},
		},
	}, {
		name: "Substitute no proxy",
		containers: []corev1.Container{{
			Name: "no_proxy",
			Env: []corev1.EnvVar{{
				Name:  "SomeVar",
				Value: "SomeValue",
			}},
		}},
		expectedContainers: []corev1.Container{{
			Name: "no_proxy",
			Env: []corev1.EnvVar{
				{
					Name:  "SomeVar",
					Value: "SomeValue",
				}, {
					Name:  "NO_PROXY",
					Value: "https://internal.acme.com",
				}},
		}},
		config: config.OperatorConfig{
			ClusterProxy: &configv1.Proxy{
				Status: configv1.ProxyStatus{
					NoProxy: "https://internal.acme.com",
				},
			},
		},
	}, {
		name: "Combination of proxy settings",
		containers: []corev1.Container{{
			Name: "all_proxy",
			Env: []corev1.EnvVar{{
				Name:  "SomeVar",
				Value: "SomeValue",
			}},
		}},
		expectedContainers: []corev1.Container{{
			Name: "all_proxy",
			Env: []corev1.EnvVar{{
				Name:  "SomeVar",
				Value: "SomeValue",
			}, {
				Name:  "HTTP_PROXY",
				Value: "http://squid.corp.acme.com:3128",
			}, {
				Name:  "HTTPS_PROXY",
				Value: "https://squid.corp.acme.com:3128",
			}, {
				Name:  "NO_PROXY",
				Value: "https://internal.acme.com",
			}},
		}},
		config: config.OperatorConfig{
			ClusterProxy: &configv1.Proxy{
				Status: configv1.ProxyStatus{
					HTTPProxy:  "http://squid.corp.acme.com:3128",
					HTTPSProxy: "https://squid.corp.acme.com:3128",
					NoProxy:    "https://internal.acme.com",
				},
			},
		},
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			podSpec := corev1.PodSpec{
				Containers: tc.containers,
			}

			spec := setProxySettings(tc.config, podSpec)
			assert.EqualValues(t, spec.Containers, tc.expectedContainers)
		})
	}
}

func TestFillConfigValues(t *testing.T) {
	testManagementNamespace := "test-namespace"

	tc := []struct {
		name            string
		objects         []client.Object
		config          config.OperatorConfig
		expectedObjects []client.Object
	}{{
		name: "Substitute Single Replica for deployment",
		objects: []client.Object{&v1.Deployment{
			Spec: v1.DeploymentSpec{
				Replicas: pointer.Int32(2),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{},
					},
				},
			},
		}},
		expectedObjects: []client.Object{&v1.Deployment{
			Spec: v1.DeploymentSpec{
				Replicas: pointer.Int32(1),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{},
					},
				},
			},
		}},
		config: config.OperatorConfig{
			ManagedNamespace: testManagementNamespace,
			IsSingleReplica:  true,
		},
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			initialObjects := tc.objects
			updatedObjects := SubstituteCommonPartsFromConfig(tc.config, tc.objects)

			assert.EqualValues(t, updatedObjects, tc.expectedObjects)
			// Ensure there is no mutation in place
			assert.EqualValues(t, initialObjects, tc.objects)
		})
	}
}
