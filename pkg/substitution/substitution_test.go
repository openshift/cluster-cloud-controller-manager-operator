package substitution

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestSetCloudControllerImage(t *testing.T) {
	tc := []struct {
		name               string
		containers         []corev1.Container
		config             config.OperatorConfig
		expectedContainers []corev1.Container
	}{{
		name: "Unknown container name",
		containers: []corev1.Container{{
			Name:  "different_name",
			Image: "no_change",
		}},
		expectedContainers: []corev1.Container{{
			Name:  "different_name",
			Image: "no_change",
		}},
		config: config.OperatorConfig{
			ControllerImage: "correct_image:tag",
		},
	}, {
		name: "Substitute cloud-controller-manager container image",
		containers: []corev1.Container{{
			Name:  cloudControllerManagerName,
			Image: "expect_change",
		}},
		expectedContainers: []corev1.Container{{
			Name:  cloudControllerManagerName,
			Image: "correct_image:tag",
		}},
		config: config.OperatorConfig{
			ControllerImage: "correct_image:tag",
		},
	}, {
		name: "Substitute cloud-node-manager container image",
		containers: []corev1.Container{{
			Name:  cloudNodeManagerName,
			Image: "expect_change",
		}},
		expectedContainers: []corev1.Container{{
			Name:  cloudNodeManagerName,
			Image: "correct_node_image:tag",
		}},
		config: config.OperatorConfig{
			CloudNodeImage: "correct_node_image:tag",
		},
	}, {
		name: "Combination of container image names",
		containers: []corev1.Container{{
			Name:  cloudControllerManagerName,
			Image: "expect_change",
		}, {
			Name:  "node-controller-manager",
			Image: "no_change",
		}},
		expectedContainers: []corev1.Container{{
			Name:  cloudControllerManagerName,
			Image: "correct_image:tag",
		}, {
			Name:  "node-controller-manager",
			Image: "no_change",
		}},
		config: config.OperatorConfig{
			ControllerImage: "correct_image:tag",
		},
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			deploy := &v1.Deployment{
				Spec: v1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: tc.containers,
						},
					},
				},
			}

			ds := &v1.DaemonSet{
				Spec: v1.DaemonSetSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: tc.containers,
						},
					},
				},
			}

			pod := &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: tc.containers,
				},
			}

			spec := setCloudControllerImage(tc.config, deploy.Spec.Template.Spec)
			assert.EqualValues(t, spec.Containers, tc.expectedContainers)

			spec = setCloudControllerImage(tc.config, ds.Spec.Template.Spec)
			assert.EqualValues(t, spec.Containers, tc.expectedContainers)

			spec = setCloudControllerImage(tc.config, pod.Spec)
			assert.EqualValues(t, spec.Containers, tc.expectedContainers)
		})
	}
}

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
		name:    "Substitute object namespace",
		objects: []client.Object{&corev1.ConfigMap{}},
		expectedObjects: []client.Object{&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testManagementNamespace,
			},
		}},
		config: config.OperatorConfig{
			ManagedNamespace: testManagementNamespace,
		},
	}, {
		name: "Substitute cloud-controller-manager container image and namespace",
		objects: []client.Object{&v1.Deployment{
			Spec: v1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:  cloudControllerManagerName,
							Image: "expect_change",
						}},
					},
				},
			},
		}},
		expectedObjects: []client.Object{&v1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testManagementNamespace,
			},
			Spec: v1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:  cloudControllerManagerName,
							Image: "correct_image:tag",
						}},
					},
				},
			},
		}},
		config: config.OperatorConfig{
			ControllerImage:  "correct_image:tag",
			ManagedNamespace: testManagementNamespace,
		},
	}, {
		name: "Substitute image and namespace for more objects at once",
		objects: []client.Object{&corev1.ConfigMap{}, &v1.Deployment{
			Spec: v1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:  cloudControllerManagerName,
							Image: "expect_change",
						}},
					},
				},
			},
		}},
		expectedObjects: []client.Object{
			&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testManagementNamespace,
				},
			}, &v1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testManagementNamespace,
				},
				Spec: v1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:  cloudControllerManagerName,
								Image: "correct_image:tag",
							}},
						},
					},
				},
			}},
		config: config.OperatorConfig{
			ControllerImage:  "correct_image:tag",
			ManagedNamespace: testManagementNamespace,
		},
	}, {
		name: "Substitute image and namespace for deployment, daemonset and pod",
		objects: []client.Object{&v1.DaemonSet{
			Spec: v1.DaemonSetSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:  cloudNodeManagerName,
							Image: "expect_change",
						}},
					},
				},
			},
		}, &corev1.Pod{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  cloudControllerManagerName,
					Image: "expect_change",
				}},
			},
		}, &corev1.Pod{
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  cloudNodeManagerName,
					Image: "expect_change",
				}},
			},
		}, &v1.Deployment{
			Spec: v1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:  cloudControllerManagerName,
							Image: "expect_change",
						}},
					},
				},
			},
		}},
		expectedObjects: []client.Object{
			&v1.DaemonSet{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testManagementNamespace,
				},
				Spec: v1.DaemonSetSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:  cloudNodeManagerName,
								Image: "correct_cloud_node_image:tag",
							}},
						},
					},
				},
			}, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testManagementNamespace,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  cloudControllerManagerName,
						Image: "correct_image:tag",
					}},
				},
			}, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testManagementNamespace,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  cloudNodeManagerName,
						Image: "correct_cloud_node_image:tag",
					}},
				},
			}, &v1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: testManagementNamespace,
				},
				Spec: v1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{
								Name:  cloudControllerManagerName,
								Image: "correct_image:tag",
							}},
						},
					},
				},
			}},
		config: config.OperatorConfig{
			ControllerImage:  "correct_image:tag",
			CloudNodeImage:   "correct_cloud_node_image:tag",
			ManagedNamespace: testManagementNamespace,
		},
	}, {
		name: "Substitute Single Replica for deployment",
		objects: []client.Object{&v1.Deployment{
			Spec: v1.DeploymentSpec{
				Replicas: pointer.Int32(2),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:  cloudControllerManagerName,
							Image: "expect_change",
						}},
					},
				},
			},
		}},
		expectedObjects: []client.Object{&v1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testManagementNamespace,
			},
			Spec: v1.DeploymentSpec{
				Replicas: pointer.Int32(1),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:  cloudControllerManagerName,
							Image: "correct_image:tag",
						}},
					},
				},
			},
		}},
		config: config.OperatorConfig{
			ControllerImage:  "correct_image:tag",
			ManagedNamespace: testManagementNamespace,
			IsSingleReplica:  true,
		},
	}, {
		name: "Substitute cluster env variable name for deployment",
		objects: []client.Object{&v1.Deployment{
			Spec: v1.DeploymentSpec{
				Replicas: pointer.Int32(2),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Env: []corev1.EnvVar{
									{Name: "CLOUD_CONFIG", Value: "foo"},
									{Name: "OCP_INFRASTRUCTURE_NAME", Value: "kubernetes"},
									{Name: "OTHER_VAR", Value: "kubernetes"},
								},
							},
							{
								Env: []corev1.EnvVar{
									{Name: "SOME_RANDOM_VAR", Value: "foo"},
									{Name: "OTHER_RANDOM_VAR", Value: "kubernetes"},
								},
							},
						},
					},
				},
			},
		}},
		config: config.OperatorConfig{
			ManagedNamespace:   testManagementNamespace,
			InfrastructureName: "my-cool-ocp-cluster",
		},
		expectedObjects: []client.Object{&v1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testManagementNamespace,
			},
			Spec: v1.DeploymentSpec{
				Replicas: pointer.Int32(2),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Env: []corev1.EnvVar{
									{Name: "CLOUD_CONFIG", Value: "foo"},
									{Name: "OCP_INFRASTRUCTURE_NAME", Value: "my-cool-ocp-cluster"},
									{Name: "OTHER_VAR", Value: "kubernetes"},
								},
							},
							{
								Env: []corev1.EnvVar{
									{Name: "SOME_RANDOM_VAR", Value: "foo"},
									{Name: "OTHER_RANDOM_VAR", Value: "kubernetes"},
								},
							},
						},
					},
				},
			},
		}},
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			initialObjects := tc.objects
			updatedObjects := FillConfigValues(tc.config, tc.objects)

			assert.EqualValues(t, updatedObjects, tc.expectedObjects)
			// Ensure there is no mutation in place
			assert.EqualValues(t, initialObjects, tc.objects)
		})
	}
}
