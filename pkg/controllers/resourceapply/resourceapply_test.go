package resourceapply

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	appsclientv1 "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func setupEnvtest(t *testing.T) (client.Client, func(t *testing.T)) {
	t.Log("Setup envtest")
	g := NewWithT(t)
	testEnv := &envtest.Environment{}
	cfg, err := testEnv.Start()
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(cfg).NotTo(BeNil())

	cl, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(cl).NotTo(BeNil())

	teardownFunc := func(t *testing.T) {
		t.Log("Stop envtest")
		g.Expect(testEnv.Stop()).To(Succeed())
	}
	return cl, teardownFunc
}

func cleanupResources(t *testing.T, g *WithT, ctx context.Context, cl client.Client, listObject client.ObjectList) {
	g.Expect(cl.List(ctx, listObject)).To(Succeed())
	deleteResouce := func(g *WithT, obj client.Object) {
		key := client.ObjectKeyFromObject(obj)
		obj.SetFinalizers([]string{})
		g.Expect(cl.Update(ctx, obj)).To(Succeed())
		err := cl.Delete(ctx, obj)
		if apierrors.IsNotFound(err) {
			return
		}
		g.Expect(err).ToNot(HaveOccurred())
		g.Eventually(
			apierrors.IsNotFound(cl.Get(ctx, key, obj)),
		).Should(BeTrue(), "Can not cleanup resources")
	}

	switch typedList := listObject.(type) {
	case *appsv1.DeploymentList:
		for _, obj := range typedList.Items {
			deleteResouce(g, &obj)
		}
	case *corev1.ConfigMapList:
		for _, obj := range typedList.Items {
			deleteResouce(g, &obj)
		}
	case *appsv1.DaemonSetList:
		for _, obj := range typedList.Items {
			deleteResouce(g, &obj)
		}
	case *policyv1.PodDisruptionBudgetList:
		for _, obj := range typedList.Items {
			deleteResouce(g, &obj)
		}
	default:
		t.Fatal("can not cast list type for cleanup")
	}
}

func createNamespace(g *WithT, ctx context.Context, cl client.Client) string {
	ns := &corev1.Namespace{}
	ns.SetGenerateName("resource-apply-test-")
	g.Expect(cl.Create(ctx, ns)).To(Succeed())
	return ns.GetName()
}

func TestApplyConfigMap(t *testing.T) {
	cl, tearDownFn := setupEnvtest(t)
	defer tearDownFn(t)
	namespace := createNamespace(NewWithT(t), context.TODO(), cl)

	tests := []struct {
		name     string
		existing *corev1.ConfigMap
		input    *corev1.ConfigMap

		expectedModified bool
	}{
		{
			name: "create",
			input: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "foo"},
			},

			expectedModified: true,
		},
		{
			name: "skip on extra label",
			existing: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
			},
			input: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "foo"},
			},

			expectedModified: false,
		},
		{
			name: "update on missing label",
			existing: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
			},
			input: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "foo", Labels: map[string]string{"new": "merge"}},
			},

			expectedModified: true,
		},
		{
			name: "update on mismatch data",
			existing: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
			},
			input: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "foo"},
				Data: map[string]string{
					"configmap": "value",
				},
			},

			expectedModified: true,
		},
		{
			name: "update on mismatch binary data",
			existing: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "foo", Labels: map[string]string{"extra": "leave-alone"}},
				Data: map[string]string{
					"configmap": "value",
				},
			},
			input: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "foo"},
				Data: map[string]string{
					"configmap": "value",
				},
				BinaryData: map[string][]byte{
					"binconfigmap": []byte("value"),
				},
			},

			expectedModified: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			g := NewWithT(t)
			ctx := context.TODO()
			defer cleanupResources(t, g, ctx, cl, &corev1.ConfigMapList{})

			if test.existing != nil {
				g.Expect(cl.Create(ctx, test.existing)).To(Succeed())
			}
			actualModified, err := applyConfigMap(ctx, cl, record.NewFakeRecorder(1000), test.input)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(test.expectedModified).To(BeEquivalentTo(actualModified), "Resource was modified")
		})
	}
}

func TestApplyDeployment(t *testing.T) {
	cl, tearDownFn := setupEnvtest(t)
	defer tearDownFn(t)
	namespace := createNamespace(NewWithT(t), context.TODO(), cl)

	tests := []struct {
		name              string
		desiredDeployment *appsv1.Deployment
		actualDeployment  *appsv1.Deployment

		expectError        bool
		expectedUpdate     bool
		expectedDeployment *appsv1.Deployment
	}{
		{
			name:               "the deployment is created because it doesn't exist",
			desiredDeployment:  workloadDeployment(namespace),
			expectedDeployment: workloadDeploymentWithDefaultSpecHash(namespace),
			expectedUpdate:     true,
		},

		{
			name:               "the deployment already exists and it's up to date",
			desiredDeployment:  workloadDeployment(namespace),
			actualDeployment:   workloadDeploymentWithDefaultSpecHash(namespace),
			expectedDeployment: workloadDeploymentWithDefaultSpecHash(namespace),
		},

		{
			name: "the deployment is updated due to a change in the spec",
			desiredDeployment: func() *appsv1.Deployment {
				w := workloadDeployment(namespace)
				w.Spec.Template.Finalizers = []string{"newFinalizer"}
				return w
			}(),
			actualDeployment: workloadDeploymentWithDefaultSpecHash(namespace),
			expectedDeployment: func() *appsv1.Deployment {
				w := workloadDeployment(namespace)
				w.Annotations["operator.openshift.io/spec-hash"] = "3595383676891d94b068a1b3cfedc7e1e77f86f49ae53a30757b4f7f5cd4b36a"
				w.Spec.Template.Finalizers = []string{"newFinalizer"}
				return w
			}(),
			expectedUpdate: true,
		},

		{
			name: "the deployment is updated due to a change in Labels field",
			desiredDeployment: func() *appsv1.Deployment {
				w := workloadDeployment(namespace)
				w.Labels["newLabel"] = "newValue"
				return w
			}(),
			actualDeployment: workloadDeploymentWithDefaultSpecHash(namespace),
			expectedDeployment: func() *appsv1.Deployment {
				w := workloadDeploymentWithDefaultSpecHash(namespace)
				w.Labels["newLabel"] = "newValue"
				return w
			}(),
			expectedUpdate: true,
		},

		{
			name: "the deployment is updated due to a change in Annotations field",
			desiredDeployment: func() *appsv1.Deployment {
				w := workloadDeployment(namespace)
				w.Annotations["newAnnotation"] = "newValue"
				return w
			}(),
			actualDeployment: workloadDeploymentWithDefaultSpecHash(namespace),
			expectedDeployment: func() *appsv1.Deployment {
				w := workloadDeploymentWithDefaultSpecHash(namespace)
				w.Annotations["newAnnotation"] = "newValue"
				return w
			}(),
			expectedUpdate: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			eventRecorder := record.NewFakeRecorder(1000)
			ctx := context.TODO()
			defer cleanupResources(t, g, ctx, cl, &appsv1.DeploymentList{})

			if tt.actualDeployment != nil {
				g.Expect(cl.Create(ctx, tt.actualDeployment)).To(Succeed())
			}

			updated, err := applyDeployment(ctx, cl, eventRecorder, tt.desiredDeployment)
			if tt.expectError {
				g.Expect(err).To(HaveOccurred(), "expected error")
			}
			if !tt.expectError {
				g.Expect(err).NotTo(HaveOccurred(), "expected no error")
			}
			if tt.expectedUpdate {
				g.Expect(updated).To(BeTrue(), "expect deployment to be updated")
			}
			if !tt.expectedUpdate {
				g.Expect(updated).To(BeFalse(), "expect deployment not to be updated")
			}

			updatedDeployment := &appsv1.Deployment{}
			deploymentObjectKey := appsclientv1.ObjectKeyFromObject(tt.desiredDeployment)
			g.Expect(cl.Get(ctx, deploymentObjectKey, updatedDeployment)).To(Succeed())

			if !equality.Semantic.DeepDerivative(tt.expectedDeployment.Spec, updatedDeployment.Spec) {
				t.Fatalf("Expected deployment: %+v, got %+v", tt.expectedDeployment, updatedDeployment)
			}
			g.Expect(tt.expectedDeployment.Annotations[specHashAnnotation]).Should(BeEquivalentTo(updatedDeployment.Annotations[specHashAnnotation]))
		})
	}

	updateSelectorTests := []struct {
		name               string
		desiredDeployment  *appsv1.Deployment
		expectedDeployment *appsv1.Deployment
		actualDeployment   *appsv1.Deployment

		expectError      bool
		errorMsg         string
		expectedRecreate bool
	}{
		{
			name:             "the deployment is recreated due to a change in match labels field",
			actualDeployment: workloadDeploymentWithDefaultSpecHash(namespace),

			desiredDeployment: func() *appsv1.Deployment {
				w := workloadDeployment(namespace)
				w.Spec.Selector = &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"bar": "baz",
					},
				}
				w.Spec.Template.Labels = map[string]string{"bar": "baz"}
				return w
			}(),
			expectedDeployment: func() *appsv1.Deployment {
				w := workloadDeploymentWithDefaultSpecHash(namespace)
				w.Spec.Selector = &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"bar": "baz",
					},
				}
				w.Spec.Template.Labels = map[string]string{"bar": "baz"}
				w.Annotations[specHashAnnotation] = "5e54f6f565b4d03edbdf5e129492b54cee18bb3ed84dcd84be02d5dd86e280fa"
				return w
			}(),
			expectedRecreate: true,
		},

		{
			name:             "resourceapply should report an error in case if resource is malformed",
			actualDeployment: workloadDeploymentWithDefaultSpecHash(namespace),

			desiredDeployment: func() *appsv1.Deployment {
				w := workloadDeployment(namespace)
				w.Spec.Selector = &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"bar": "baz",
					},
				}
				w.Spec.Template.Labels = map[string]string{"fiz": "baz"}
				return w
			}(),
			expectedDeployment: workloadDeploymentWithDefaultSpecHash(namespace),
			errorMsg:           "`selector` does not match template `labels`",
			expectError:        true,
		},

		{
			name: "resourceapply should report an error in case if resource deletion stucked",
			actualDeployment: func() *appsv1.Deployment {
				d := workloadDeploymentWithDefaultSpecHash(namespace)
				d.Finalizers = []string{"foo.bar/baz"}
				return d
			}(),

			desiredDeployment: func() *appsv1.Deployment {
				w := workloadDeployment(namespace)
				w.Spec.Selector = &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"bar": "baz",
					},
				}
				w.Spec.Template.Labels = map[string]string{"bar": "baz"}
				return w
			}(),
			expectedDeployment: workloadDeploymentWithDefaultSpecHash(namespace),
			errorMsg:           "object is being deleted: deployments.apps \"apiserver\" already exists",
			expectError:        true,
		},
	}
	for _, tt := range updateSelectorTests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			eventRecorder := record.NewFakeRecorder(1000)
			ctx := context.TODO()
			defer cleanupResources(t, g, ctx, cl, &appsv1.DeploymentList{})

			g.Expect(cl.Create(ctx, tt.actualDeployment)).To(Succeed())
			g.Expect(tt.actualDeployment.UID).NotTo(BeNil())

			_, err := applyDeployment(ctx, cl, eventRecorder, tt.desiredDeployment)
			if tt.expectError {
				g.Expect(err).To(HaveOccurred(), "expected error")
				g.Expect(tt.errorMsg).ToNot(BeEmpty())
				g.Expect(err.Error()).To(ContainSubstring(tt.errorMsg))
			}
			if !tt.expectError {
				g.Expect(err).NotTo(HaveOccurred(), "expected no error")
			}

			updatedDeployment := &appsv1.Deployment{}
			deploymentObjectKey := appsclientv1.ObjectKeyFromObject(tt.desiredDeployment)
			g.Expect(cl.Get(ctx, deploymentObjectKey, updatedDeployment)).To(Succeed())
			if tt.expectedRecreate {
				g.Expect(tt.actualDeployment.UID).ShouldNot(BeEquivalentTo(updatedDeployment.UID))
			}
			if !tt.expectedRecreate {
				g.Expect(tt.actualDeployment.UID).Should(BeEquivalentTo(updatedDeployment.UID))
			}

			if !equality.Semantic.DeepDerivative(tt.expectedDeployment.Spec, updatedDeployment.Spec) {
				t.Fatalf("Expected deployment: %+v, got %+v", tt.expectedDeployment, updatedDeployment)
			}
			g.Expect(tt.expectedDeployment.Annotations[specHashAnnotation]).To(BeEquivalentTo(updatedDeployment.Annotations[specHashAnnotation]))

			deployments := &appsv1.DeploymentList{}
			g.Expect(cl.List(ctx, deployments)).To(Succeed())
			g.Expect(len(deployments.Items)).To(BeEquivalentTo(1))
		})
	}
}

func workloadDeployment(namespace string) *appsv1.Deployment {
	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "apiserver",
			Namespace: namespace,
			Labels:    map[string]string{},
			Annotations: map[string]string{
				generationAnnotation: "1",
			},
			Generation: 1,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: pointer.Int32Ptr(3),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"foo": "bar",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"foo": "bar",
					},
					Annotations: map[string]string{},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "apiserver",
							Image: "docker-registry/img",
						},
					},
				},
			},
		},
	}
}

func workloadDeploymentWithDefaultSpecHash(namespace string) *appsv1.Deployment {
	w := workloadDeployment(namespace)
	w.Annotations[specHashAnnotation] = "259870a8d6f8fca4ded383158594ac91935b0225acabe8e16670b6f6a395f68d"
	return w
}

func TestApplyDaemonSet(t *testing.T) {
	cl, tearDownFn := setupEnvtest(t)
	defer tearDownFn(t)
	namespace := createNamespace(NewWithT(t), context.TODO(), cl)

	tests := []struct {
		name             string
		desiredDaemonSet *appsv1.DaemonSet
		actualDaemonSet  *appsv1.DaemonSet

		expectError       bool
		expectedUpdate    bool
		expectedDaemonSet *appsv1.DaemonSet
	}{
		{
			name:              "the daemonset is created because it doesn't exist",
			desiredDaemonSet:  workloadDaemonSet(namespace),
			expectedDaemonSet: workloadDaemonSetWithDefaultSpecHash(namespace),
			expectedUpdate:    true,
		},

		{
			name:              "the daemonset already exists and it's up to date",
			desiredDaemonSet:  workloadDaemonSet(namespace),
			actualDaemonSet:   workloadDaemonSetWithDefaultSpecHash(namespace),
			expectedDaemonSet: workloadDaemonSetWithDefaultSpecHash(namespace),
		},

		{
			name: "the daemonset is updated due to a change in the spec",
			desiredDaemonSet: func() *appsv1.DaemonSet {
				w := workloadDaemonSet(namespace)
				w.Spec.Template.Finalizers = []string{"newFinalizer"}
				return w
			}(),
			actualDaemonSet: workloadDaemonSetWithDefaultSpecHash(namespace),
			expectedDaemonSet: func() *appsv1.DaemonSet {
				w := workloadDaemonSet(namespace)
				w.Annotations["operator.openshift.io/spec-hash"] = "42ed5653bc5ded7dc099b924ede011e43140c675302d1da42a6b645771d242a0"
				w.Spec.Template.Finalizers = []string{"newFinalizer"}
				return w
			}(),
			expectedUpdate: true,
		},

		{
			name: "the daemonset is updated due to a change in Labels field",
			desiredDaemonSet: func() *appsv1.DaemonSet {
				w := workloadDaemonSet(namespace)
				w.Labels["newLabel"] = "newValue"
				return w
			}(),
			actualDaemonSet: workloadDaemonSetWithDefaultSpecHash(namespace),
			expectedDaemonSet: func() *appsv1.DaemonSet {
				w := workloadDaemonSetWithDefaultSpecHash(namespace)
				w.Labels["newLabel"] = "newValue"
				return w
			}(),
			expectedUpdate: true,
		},

		{
			name: "the daemonset is updated due to a change in Annotations field",
			desiredDaemonSet: func() *appsv1.DaemonSet {
				w := workloadDaemonSet(namespace)
				w.Annotations["newAnnotation"] = "newValue"
				return w
			}(),
			actualDaemonSet: workloadDaemonSetWithDefaultSpecHash(namespace),
			expectedDaemonSet: func() *appsv1.DaemonSet {
				w := workloadDaemonSetWithDefaultSpecHash(namespace)
				w.Annotations["newAnnotation"] = "newValue"
				return w
			}(),
			expectedUpdate: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			eventRecorder := record.NewFakeRecorder(1000)
			ctx := context.TODO()
			defer cleanupResources(t, g, ctx, cl, &appsv1.DaemonSetList{})

			if tt.actualDaemonSet != nil {
				g.Expect(cl.Create(ctx, tt.actualDaemonSet)).To(Succeed())
			}

			updated, err := applyDaemonSet(ctx, cl, eventRecorder, tt.desiredDaemonSet)
			if tt.expectError {
				g.Expect(err).To(HaveOccurred(), "expected error")
			}
			if !tt.expectError {
				g.Expect(err).NotTo(HaveOccurred(), "expected no error")
			}
			if tt.expectedUpdate {
				g.Expect(updated).To(BeTrue(), "expect deployment to be updated")
			}
			if !tt.expectedUpdate {
				g.Expect(updated).To(BeFalse(), "expect deployment not to be updated")
			}

			updatedDaemonSet := &appsv1.DaemonSet{}
			g.Expect(cl.Get(ctx, appsclientv1.ObjectKeyFromObject(tt.desiredDaemonSet), updatedDaemonSet)).To(Succeed())

			if !equality.Semantic.DeepDerivative(tt.expectedDaemonSet.Spec, updatedDaemonSet.Spec) {
				t.Fatalf("Expected DaemonSet: %+v, got %+v", tt.expectedDaemonSet, updatedDaemonSet)
			}
			g.Expect(tt.expectedDaemonSet.Annotations[specHashAnnotation]).Should(BeEquivalentTo(updatedDaemonSet.Annotations[specHashAnnotation]))
		})
	}

	updateSelectorTests := []struct {
		name              string
		actualDaemonSet   *appsv1.DaemonSet
		desiredDaemonSet  *appsv1.DaemonSet
		expectedDaemonSet *appsv1.DaemonSet

		errorMsg         string
		expectError      bool
		expectedRecreate bool
	}{
		{
			name:            "the daemonset is recreated due to a change in match labels field",
			actualDaemonSet: workloadDaemonSetWithDefaultSpecHash(namespace),
			desiredDaemonSet: func() *appsv1.DaemonSet {
				w := workloadDaemonSet(namespace)
				w.Spec.Selector = &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"bar": "baz",
					},
				}
				w.Spec.Template.Labels = map[string]string{"bar": "baz"}
				return w
			}(),
			expectedDaemonSet: func() *appsv1.DaemonSet {
				w := workloadDaemonSet(namespace)
				w.Spec.Selector = &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"bar": "baz",
					},
				}
				w.Spec.Template.Labels = map[string]string{"bar": "baz"}
				w.Annotations[specHashAnnotation] = "ba95dff6a88cc11a6cd80aa8a8d7a5e88793809ad27f9f8c5b7b66c39ce13ee4"
				return w
			}(),
			expectedRecreate: true,
		},

		{
			name:            "resourceapply should report an error in case if resource is malformed",
			actualDaemonSet: workloadDaemonSetWithDefaultSpecHash(namespace),
			desiredDaemonSet: func() *appsv1.DaemonSet {
				w := workloadDaemonSet(namespace)
				w.Spec.Selector = &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"bar": "baz",
					},
				}
				w.Spec.Template.Labels = map[string]string{"fiz": "baz"}
				return w
			}(),
			expectedDaemonSet: workloadDaemonSetWithDefaultSpecHash(namespace),
			errorMsg:          "`selector` does not match template `labels`",
			expectError:       true,
		},

		{
			name: "resourceapply should report an error in case if resource deletion stucked",
			actualDaemonSet: func() *appsv1.DaemonSet {
				ds := workloadDaemonSetWithDefaultSpecHash(namespace)
				ds.Finalizers = []string{"foo.bar/baz"}
				return ds
			}(),
			desiredDaemonSet: func() *appsv1.DaemonSet {
				w := workloadDaemonSet(namespace)
				w.Spec.Selector = &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"bar": "baz",
					},
				}
				w.Spec.Template.Labels = map[string]string{"bar": "baz"}
				return w
			}(),
			expectedDaemonSet: workloadDaemonSetWithDefaultSpecHash(namespace),
			errorMsg:          "object is being deleted: daemonsets.apps \"apiserver\" already exists",
			expectError:       true,
		},
	}
	for _, tt := range updateSelectorTests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			eventRecorder := record.NewFakeRecorder(1000)
			ctx := context.TODO()
			defer cleanupResources(t, g, ctx, cl, &appsv1.DaemonSetList{})

			g.Expect(cl.Create(ctx, tt.actualDaemonSet)).To(Succeed())
			g.Expect(tt.actualDaemonSet.UID).NotTo(BeNil())

			_, err := applyDaemonSet(ctx, cl, eventRecorder, tt.desiredDaemonSet)
			if tt.expectError {
				g.Expect(err).To(HaveOccurred(), "expected error")
				g.Expect(tt.errorMsg).NotTo(BeEmpty())
				g.Expect(err.Error()).To(ContainSubstring(tt.errorMsg))
			}
			if !tt.expectError {
				g.Expect(err).NotTo(HaveOccurred(), "expected no error")
			}

			updatedDaemonSet := &appsv1.DaemonSet{}
			deploymentObjectKey := appsclientv1.ObjectKeyFromObject(tt.desiredDaemonSet)
			g.Expect(cl.Get(ctx, deploymentObjectKey, updatedDaemonSet)).To(Succeed())
			if tt.expectedRecreate {
				g.Expect(tt.actualDaemonSet.UID).ShouldNot(BeEquivalentTo(updatedDaemonSet.UID))
			}
			if !tt.expectedRecreate {
				g.Expect(tt.actualDaemonSet.UID).Should(BeEquivalentTo(updatedDaemonSet.UID))
			}

			if !equality.Semantic.DeepDerivative(tt.expectedDaemonSet.Spec, updatedDaemonSet.Spec) {
				t.Fatalf("Expected deployment: %+v, got %+v", tt.expectedDaemonSet, updatedDaemonSet)
			}
			g.Expect(tt.expectedDaemonSet.Annotations[specHashAnnotation]).To(BeEquivalentTo(updatedDaemonSet.Annotations[specHashAnnotation]))

			dss := &appsv1.DaemonSetList{}
			g.Expect(cl.List(ctx, dss)).To(Succeed())
			g.Expect(len(dss.Items)).To(BeEquivalentTo(1))
		})
	}
}

func workloadDaemonSet(namespace string) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "apiserver",
			Namespace: namespace,
			Labels:    map[string]string{},
			Annotations: map[string]string{
				generationAnnotation: "1",
			},
			Generation: 1,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"foo": "bar",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"foo": "bar"},
					Annotations: map[string]string{},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "apiserver",
							Image: "docker-registry/img",
						},
					},
				},
			},
		},
	}
}

func workloadDaemonSetWithDefaultSpecHash(namespace string) *appsv1.DaemonSet {
	w := workloadDaemonSet(namespace)
	w.Annotations[specHashAnnotation] = "eaeff6ac704fb141d5085803b5b3cc12067ef98c9f2ba8c1052df81faa53299c"
	return w
}

func TestApplyPDB(t *testing.T) {
	cl, tearDownFn := setupEnvtest(t)
	defer tearDownFn(t)
	namespace := createNamespace(NewWithT(t), context.TODO(), cl)

	tests := []struct {
		name     string
		existing *policyv1.PodDisruptionBudget
		input    *policyv1.PodDisruptionBudget

		expectedModified bool
	}{
		{
			name:             "create",
			input:            podDisruptionBudget(namespace),
			expectedModified: true,
		},
		{
			name: "skip on extra label",
			existing: func() *policyv1.PodDisruptionBudget {
				pdb := podDisruptionBudget(namespace)
				pdb.Labels = map[string]string{"bar": "baz"}
				return pdb
			}(),
			input: podDisruptionBudget(namespace),

			expectedModified: false,
		},
		{
			name: "update on missing label",
			existing: func() *policyv1.PodDisruptionBudget {
				pdb := podDisruptionBudget(namespace)
				pdb.Labels = map[string]string{"bar": "baz"}
				return pdb
			}(),
			input: func() *policyv1.PodDisruptionBudget {
				pdb := podDisruptionBudget(namespace)
				pdb.Labels = map[string]string{"new": "merge"}
				return pdb
			}(),

			expectedModified: true,
		},
		{
			name:     "update on mismatch data",
			existing: podDisruptionBudget(namespace),
			input: func() *policyv1.PodDisruptionBudget {
				pdb := podDisruptionBudget(namespace)
				minAvailable := intstr.FromInt(3)
				pdb.Spec.MinAvailable = &minAvailable
				return pdb
			}(),

			expectedModified: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			recorder := record.NewFakeRecorder(1000)
			ctx := context.TODO()
			defer cleanupResources(t, g, ctx, cl, &policyv1.PodDisruptionBudgetList{})

			if tt.existing != nil {
				g.Expect(cl.Create(context.TODO(), tt.existing)).To(Succeed())
			}
			actualModified, err := applyPodDisruptionBudget(context.TODO(), cl, recorder, tt.input)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(tt.expectedModified).To(BeEquivalentTo(actualModified), "Resource was modified")
		})
	}
}

func podDisruptionBudget(namespace string) *policyv1.PodDisruptionBudget {
	minAvailable := intstr.FromInt(1)
	return &policyv1.PodDisruptionBudget{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PodDisruptionBudget",
			APIVersion: "policy/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pdbName",
			Namespace: namespace,
		},
		Spec: policyv1.PodDisruptionBudgetSpec{
			MinAvailable: &minAvailable,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"foo": "bar"},
			},
		},
	}
}
