package controllers

import (
	"testing"

	v1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestSetDeploymentImages(t *testing.T) {
	tc := []struct {
		name               string
		containers         []corev1.Container
		config             operatorConfig
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
		config: operatorConfig{
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
		config: operatorConfig{
			ControllerImage: "correct_image:tag",
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
		config: operatorConfig{
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

			setDeploymentImages(tc.config, deploy)

			if !equality.Semantic.DeepEqual(deploy.Spec.Template.Spec.Containers, tc.expectedContainers) {
				t.Errorf("Container images are not set correctly:\n%v\nexpected\n%v", deploy.Spec.Template.Spec.Containers, tc.expectedContainers)
			}
		})
	}

}

func TestFillConfigValues(t *testing.T) {
	tc := []struct {
		name            string
		objects         []client.Object
		config          operatorConfig
		expectedObjects []client.Object
	}{{
		name:    "Substitute object namespace",
		objects: []client.Object{&corev1.ConfigMap{}},
		expectedObjects: []client.Object{&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: testManagementNamespace,
			},
		}},
		config: operatorConfig{
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
		config: operatorConfig{
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
		config: operatorConfig{
			ControllerImage:  "correct_image:tag",
			ManagedNamespace: testManagementNamespace,
		},
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {

			initialObjects := tc.objects
			updatedObjects := fillConfigValues(tc.config, tc.objects)

			if !equality.Semantic.DeepEqual(updatedObjects, tc.expectedObjects) {
				t.Errorf("Objects are not equal expected: \n%v, \n\nexpected %v", updatedObjects, tc.expectedObjects)
			}
			if !equality.Semantic.DeepEqual(initialObjects, tc.objects) {
				t.Errorf("Objects were mutated in place unexpectingly")
			}
		})
	}
}
