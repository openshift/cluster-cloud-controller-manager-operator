package resourceapply

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/openshift/cluster-api-actuator-pkg/testutils"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	appsclientv1 "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	namespaceNamePrefix = "resource-apply-test-"
)

type applyConfigMapArguments struct {
	existing       *corev1.ConfigMap
	input          *corev1.ConfigMap
	expectModified bool
}

var _ = Describe("applyConfigMap", func() {
	var namespaceName string

	BeforeEach(func() {
		By("Setting up a namespace for the test")
		ns := &corev1.Namespace{}
		ns.SetGenerateName(namespaceNamePrefix)
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		namespaceName = ns.GetName()
	})

	AfterEach(func() {
		testutils.CleanupResources(Default, ctx, cfg, k8sClient, namespaceName,
			&corev1.ConfigMap{},
		)
	})

	DescribeTable("Updates configuration when expected",
		func(args applyConfigMapArguments) {
			// we need to set the namespace name in the test because ginkgo does not know it when the Entry calls are defined
			if args.existing != nil {
				args.existing.Namespace = namespaceName
				Expect(k8sClient.Create(ctx, args.existing)).To(Succeed())
			}
			args.input.Namespace = namespaceName
			actualModified, err := applyConfigMap(ctx, k8sClient, record.NewFakeRecorder(1000), args.input)
			Expect(err).NotTo(HaveOccurred())
			Expect(args.expectModified).To(BeEquivalentTo(actualModified), "Resource was modified")
		},
		Entry("When created it is updated",
			applyConfigMapArguments{
				existing: nil,
				input: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
					},
				},
				expectModified: true,
			},
		),
		Entry("When an extra label is present it is not updated",
			applyConfigMapArguments{
				existing: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "foo",
						Labels: map[string]string{"extra": "leave-alone"},
					},
				},
				input: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
					},
				},
				expectModified: false,
			},
		),
		Entry("When a label is missing it is updated",
			applyConfigMapArguments{
				existing: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "foo",
						Labels: map[string]string{"extra": "leave-alone"},
					},
				},
				input: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "foo",
						Labels: map[string]string{"new": "merge"},
					},
				},
				expectModified: true,
			},
		),
		Entry("When there is a data mismatch it is updated",
			applyConfigMapArguments{
				existing: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "foo",
						Labels: map[string]string{"extra": "leave-alone"},
					},
				},
				input: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
					},
					Data: map[string]string{
						"configmap": "value",
					},
				},
				expectModified: true,
			},
		),
		Entry("When there is a binary data mismatch it is updated",
			applyConfigMapArguments{
				existing: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "foo",
						Labels: map[string]string{"extra": "leave-alone"},
					},
					Data: map[string]string{
						"configmap": "value",
					},
				},
				input: &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name: "foo",
					},
					Data: map[string]string{
						"configmap": "value",
					},
					BinaryData: map[string][]byte{
						"binconfigmap": []byte("value"),
					},
				},
				expectModified: true,
			},
		),
	)
})

type deploymentSupplier func(context.Context, appsclientv1.Client, string) *appsv1.Deployment

type applyDeploymentArguments struct {
	desiredFn    deploymentSupplier
	actualFn     deploymentSupplier
	expectedFn   deploymentSupplier
	expectError  bool
	expectUpdate bool
	errorMsg     string
	updConfigsFn func(*corev1.Secret, *corev1.ConfigMap)
}

var _ = Describe("applyDeployment", func() {
	var namespaceName string

	BeforeEach(func() {
		By("Setting up a namespace for the test")
		ns := &corev1.Namespace{}
		ns.SetGenerateName(namespaceNamePrefix)
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		namespaceName = ns.GetName()
	})

	AfterEach(func() {
		testutils.CleanupResources(Default, ctx, cfg, k8sClient, namespaceName,
			&appsv1.Deployment{},
			&corev1.ConfigMap{},
			&corev1.Secret{},
		)
	})

	DescribeTable("Updates deployment when expected",
		func(args applyDeploymentArguments) {
			eventRecorder := record.NewFakeRecorder(1000)

			desiredDeployment := args.desiredFn(ctx, k8sClient, namespaceName)

			if args.actualFn != nil {
				actualDeployment := args.actualFn(ctx, k8sClient, namespaceName)
				Expect(k8sClient.Create(ctx, actualDeployment)).To(Succeed())
			}

			updated, err := applyDeployment(ctx, k8sClient, eventRecorder, desiredDeployment)
			// TODO (elmiko) add some test cases to exercise the error failure modes
			if args.expectError {
				Expect(err).To(HaveOccurred(), "expected error")
			} else {
				Expect(err).NotTo(HaveOccurred(), "expected no error")
			}
			if args.expectUpdate {
				Expect(updated).To(BeTrue(), "expect deployment to be updated")
			} else {
				Expect(updated).To(BeFalse(), "expect deployment not to be updated")
			}

			updatedDeployment := &appsv1.Deployment{}
			deploymentObjectKey := appsclientv1.ObjectKeyFromObject(desiredDeployment)
			Expect(k8sClient.Get(ctx, deploymentObjectKey, updatedDeployment)).To(Succeed())

			expectedDeployment := args.expectedFn(ctx, k8sClient, namespaceName)
			Expect(equality.Semantic.DeepDerivative(expectedDeployment.Spec, updatedDeployment.Spec)).To(BeTrue(), fmt.Sprintf("Expected deployment: %+v, got %+v", expectedDeployment, updatedDeployment))
			Expect(expectedDeployment.Annotations[specHashAnnotation]).Should(BeEquivalentTo(updatedDeployment.Annotations[specHashAnnotation]))
		},
		Entry("When the deployment is created because it doesn't exist it is updated",
			applyDeploymentArguments{
				desiredFn:    workloadDeployment,
				actualFn:     nil,
				expectedFn:   workloadDeploymentWithDefaultSpecHash,
				expectError:  false,
				expectUpdate: true,
			},
		),
		Entry("When the deployment already exists and it is up to date it is not updated",
			applyDeploymentArguments{
				desiredFn:    workloadDeployment,
				actualFn:     workloadDeploymentWithDefaultSpecHash,
				expectedFn:   workloadDeploymentWithDefaultSpecHash,
				expectError:  false,
				expectUpdate: false,
			},
		),
		Entry("When the deployment is updated due to a change in the spec it is updated",
			applyDeploymentArguments{
				desiredFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.Deployment {
					w := workloadDeployment(ctx, client, namespace)
					w.Spec.Template.Finalizers = []string{"newFinalizer"}
					return w
				},
				actualFn: workloadDeploymentWithDefaultSpecHash,
				expectedFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.Deployment {
					w := workloadDeployment(ctx, client, namespace)
					w.Spec.Template.Finalizers = []string{"newFinalizer"}
					_ = annotatePodSpecWithRelatedConfigsHash(ctx, client, w.Namespace, &w.Spec.Template)
					_ = setSpecHashAnnotation(&w.ObjectMeta, w.Spec)
					return w
				},
				expectError:  false,
				expectUpdate: true,
			},
		),
		Entry("When the deployment is updated due to a change in the labels field it is updated",
			applyDeploymentArguments{
				desiredFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.Deployment {
					w := workloadDeployment(ctx, client, namespace)
					w.Labels["newLabel"] = "newValue"
					return w
				},
				actualFn: workloadDeploymentWithDefaultSpecHash,
				expectedFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.Deployment {
					w := workloadDeploymentWithDefaultSpecHash(ctx, client, namespace)
					w.Labels["newLabel"] = "newValue"
					return w
				},
				expectError:  false,
				expectUpdate: true,
			},
		),
		Entry("When the deployment is updated due to a change in the Annotations field",
			applyDeploymentArguments{
				desiredFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.Deployment {
					w := workloadDeployment(ctx, client, namespace)
					w.Annotations["newAnnotation"] = "newValue"
					return w
				},
				actualFn: workloadDeploymentWithDefaultSpecHash,
				expectedFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.Deployment {
					w := workloadDeploymentWithDefaultSpecHash(ctx, client, namespace)
					w.Annotations["newAnnotation"] = "newValue"
					return w
				},
				expectError:  false,
				expectUpdate: true,
			},
		),
	)

	DescribeTable("Recreates deployment after selector change when expected",
		func(args applyDeploymentArguments) {
			eventRecorder := record.NewFakeRecorder(1000)

			actualDeployment := args.actualFn(ctx, k8sClient, namespaceName)
			Expect(k8sClient.Create(ctx, actualDeployment)).To(Succeed())
			Expect(actualDeployment.UID).NotTo(BeNil())

			desiredDeployment := args.desiredFn(ctx, k8sClient, namespaceName)
			_, err := applyDeployment(ctx, k8sClient, eventRecorder, desiredDeployment)
			if args.expectError {
				Expect(err).To(HaveOccurred(), "expected error")
				Expect(args.errorMsg).ToNot(BeEmpty(), "expected error string is empty")
				Expect(err).To(MatchError(ContainSubstring(args.errorMsg)))
			} else {
				Expect(err).NotTo(HaveOccurred(), "expected no error")
			}

			updatedDeployment := &appsv1.Deployment{}
			deploymentObjectKey := appsclientv1.ObjectKeyFromObject(desiredDeployment)
			Expect(k8sClient.Get(ctx, deploymentObjectKey, updatedDeployment)).To(Succeed())
			if args.expectUpdate {
				Expect(actualDeployment.UID).ShouldNot(BeEquivalentTo(updatedDeployment.UID))
			} else {
				Expect(actualDeployment.UID).Should(BeEquivalentTo(updatedDeployment.UID))
			}

			expectedDeployment := args.expectedFn(ctx, k8sClient, namespaceName)
			Expect(equality.Semantic.DeepDerivative(expectedDeployment.Spec, updatedDeployment.Spec)).To(BeTrue(), fmt.Sprintf("Expected deployment: %+v, got %+v", expectedDeployment, updatedDeployment))

			Expect(expectedDeployment.Annotations[specHashAnnotation]).To(BeEquivalentTo(updatedDeployment.Annotations[specHashAnnotation]))

			deployments := &appsv1.DeploymentList{}
			Expect(k8sClient.List(ctx, deployments)).To(Succeed())
			Expect(len(deployments.Items)).To(BeEquivalentTo(1))
		},
		Entry("When the deployment is recreated due to a change in the match labels field",
			applyDeploymentArguments{
				desiredFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.Deployment {
					w := workloadDeployment(ctx, client, namespace)
					w.Spec.Selector = &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"bar": "baz",
						},
					}
					w.Spec.Template.Labels = map[string]string{"bar": "baz"}
					return w
				},
				actualFn: workloadDeploymentWithDefaultSpecHash,
				expectedFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.Deployment {
					w := workloadDeployment(ctx, client, namespace)
					w.Spec.Selector = &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"bar": "baz",
						},
					}
					w.Spec.Template.Labels = map[string]string{"bar": "baz"}
					_ = annotatePodSpecWithRelatedConfigsHash(ctx, client, w.Namespace, &w.Spec.Template)
					_ = setSpecHashAnnotation(&w.ObjectMeta, w.Spec)
					return w
				},
				expectError:  false,
				expectUpdate: true,
			},
		),
		Entry("When a resource is malformed an error is reported",
			applyDeploymentArguments{
				desiredFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.Deployment {
					w := workloadDeployment(ctx, client, namespace)
					w.Spec.Selector = &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"bar": "baz",
						},
					}
					w.Spec.Template.Labels = map[string]string{"fiz": "baz"}
					return w
				},
				actualFn:     workloadDeploymentWithDefaultSpecHash,
				expectedFn:   workloadDeploymentWithDefaultSpecHash,
				expectError:  true,
				errorMsg:     "`selector` does not match template `labels`",
				expectUpdate: false,
			},
		),
		Entry("When resource deletion is stuck an error is reported",
			applyDeploymentArguments{
				desiredFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.Deployment {
					w := workloadDeployment(ctx, client, namespace)
					w.Spec.Selector = &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"bar": "baz",
						},
					}
					w.Spec.Template.Labels = map[string]string{"bar": "baz"}
					return w
				},
				actualFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.Deployment {
					d := workloadDeploymentWithDefaultSpecHash(ctx, client, namespace)
					d.Finalizers = []string{"foo.bar/baz"}
					return d
				},
				expectedFn:   workloadDeploymentWithDefaultSpecHash,
				expectError:  true,
				errorMsg:     "object is being deleted: deployments.apps \"apiserver\" already exists",
				expectUpdate: false,
			},
		),
	)

	DescribeTable("Updates deployment after configuration change when expected",
		func(args applyDeploymentArguments) {
			eventRecorder := record.NewFakeRecorder(1000)

			initialSecret := simpleSecret(namespaceName, "secret")
			Expect(k8sClient.Create(ctx, initialSecret)).To(Succeed())
			Expect(initialSecret.UID).NotTo(BeNil())

			initialConfigMap := simpleConfigMap(namespaceName, "configmap")
			Expect(k8sClient.Create(ctx, initialConfigMap)).To(Succeed())
			Expect(initialSecret.UID).NotTo(BeNil())

			deployment := args.desiredFn(ctx, k8sClient, namespaceName)
			_, err := applyDeployment(ctx, k8sClient, eventRecorder, deployment)
			Expect(err).ToNot(HaveOccurred())

			if args.updConfigsFn != nil {
				args.updConfigsFn(initialSecret, initialConfigMap)
				Expect(k8sClient.Update(ctx, initialSecret)).To(Succeed())
				Expect(k8sClient.Update(ctx, initialConfigMap)).To(Succeed())
			}

			updated, err := applyDeployment(ctx, k8sClient, eventRecorder, deployment)
			Expect(err).ToNot(HaveOccurred())
			Expect(updated).To(Equal(args.expectUpdate), "resource update expectation mismatch")
		},
		Entry("When related config specified in volumes did not change it is not updated",
			applyDeploymentArguments{
				desiredFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.Deployment {
					d := workloadDeployment(ctx, client, namespace)
					addSecretVolumeToPodSpec(&d.Spec.Template.Spec, "secret", "secret")
					addConfigMapVolumeToPodSpec(&d.Spec.Template.Spec, "configmap", "configmap")
					return d
				},
				updConfigsFn: nil,
				expectUpdate: false,
			},
		),
		Entry("When related configs are changed it is updated",
			applyDeploymentArguments{
				desiredFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.Deployment {
					d := workloadDeployment(ctx, client, namespace)
					addSecretVolumeToPodSpec(&d.Spec.Template.Spec, "secret", "secret")
					addConfigMapVolumeToPodSpec(&d.Spec.Template.Spec, "configmap", "configmap")
					return d
				},
				updConfigsFn: func(secret *corev1.Secret, configMap *corev1.ConfigMap) {
					secret.Data = map[string][]byte{"bar": []byte("bazz")}
				},
				expectUpdate: true,
			},
		),
		Entry("When non-existent config is specified it is not updated",
			applyDeploymentArguments{
				desiredFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.Deployment {
					d := workloadDeployment(ctx, client, namespace)
					addSecretVolumeToPodSpec(&d.Spec.Template.Spec, "non-existed", "non-existed")
					return d
				},
				updConfigsFn: nil,
				expectUpdate: false,
			},
		),
	)

})

type daemonSetSupplier func(context.Context, appsclientv1.Client, string) *appsv1.DaemonSet

type applyDaemonSetArguments struct {
	desiredFn    daemonSetSupplier
	actualFn     daemonSetSupplier
	expectedFn   daemonSetSupplier
	expectError  bool
	expectUpdate bool
	errorMsg     string
	updConfigsFn func(*corev1.Secret, *corev1.ConfigMap)
}

var _ = Describe("applyDaemonSet", func() {
	var namespaceName string

	BeforeEach(func() {
		By("Setting up a namespace for the test")
		ns := &corev1.Namespace{}
		ns.SetGenerateName(namespaceNamePrefix)
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		namespaceName = ns.GetName()
	})

	AfterEach(func() {
		testutils.CleanupResources(Default, ctx, cfg, k8sClient, namespaceName,
			&appsv1.DaemonSet{},
			&corev1.ConfigMap{},
			&corev1.Secret{},
		)
	})

	DescribeTable("Updates daemonset when expected",
		func(args applyDaemonSetArguments) {
			eventRecorder := record.NewFakeRecorder(1000)

			if args.actualFn != nil {
				actualDaemonSet := args.actualFn(ctx, k8sClient, namespaceName)
				Expect(k8sClient.Create(ctx, actualDaemonSet)).To(Succeed())
			}

			desiredDaemonSet := args.desiredFn(ctx, k8sClient, namespaceName)
			updated, err := applyDaemonSet(ctx, k8sClient, eventRecorder, desiredDaemonSet)
			// TODO (elmiko) add some test cases to exercise the error failure modes
			if args.expectError {
				Expect(err).To(HaveOccurred(), "expected error")
			} else {
				Expect(err).NotTo(HaveOccurred(), "expected no error")
			}
			if args.expectUpdate {
				Expect(updated).To(BeTrue(), "expect deployment to be updated")
			} else {
				Expect(updated).To(BeFalse(), "expect deployment not to be updated")
			}

			updatedDaemonSet := &appsv1.DaemonSet{}
			Expect(k8sClient.Get(ctx, appsclientv1.ObjectKeyFromObject(desiredDaemonSet), updatedDaemonSet)).To(Succeed())

			expectedDaemonSet := args.expectedFn(ctx, k8sClient, namespaceName)
			Expect(equality.Semantic.DeepDerivative(expectedDaemonSet.Spec, updatedDaemonSet.Spec)).To(BeTrue(), fmt.Sprintf("Expected DaemonSet: %+v, got %+v", expectedDaemonSet, updatedDaemonSet))

			Expect(expectedDaemonSet.Annotations).Should(HaveKeyWithValue(specHashAnnotation, updatedDaemonSet.Annotations[specHashAnnotation]))
		},
		Entry("When it does not exist it is created",
			applyDaemonSetArguments{
				desiredFn:    workloadDaemonSet,
				actualFn:     nil,
				expectedFn:   workloadDaemonSetWithDefaultSpecHash,
				expectError:  false,
				expectUpdate: true,
			},
		),
		Entry("When it exists and is up to date it is not updated",
			applyDaemonSetArguments{
				desiredFn:    workloadDaemonSet,
				actualFn:     workloadDaemonSetWithDefaultSpecHash,
				expectedFn:   workloadDaemonSetWithDefaultSpecHash,
				expectError:  false,
				expectUpdate: false,
			},
		),
		Entry("When there is a change in the spec it is updated",
			applyDaemonSetArguments{
				desiredFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.DaemonSet {
					w := workloadDaemonSet(ctx, client, namespace)
					w.Spec.Template.Finalizers = []string{"newFinalizer"}
					return w
				},
				actualFn: workloadDaemonSetWithDefaultSpecHash,
				expectedFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.DaemonSet {
					w := workloadDaemonSet(ctx, client, namespace)
					w.Spec.Template.Finalizers = []string{"newFinalizer"}
					_ = annotatePodSpecWithRelatedConfigsHash(ctx, client, w.Namespace, &w.Spec.Template)
					_ = setSpecHashAnnotation(&w.ObjectMeta, w.Spec)
					return w
				},
				expectError:  false,
				expectUpdate: true,
			},
		),
		Entry("When there is a change in the labels field it is updated",
			applyDaemonSetArguments{
				desiredFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.DaemonSet {
					w := workloadDaemonSet(ctx, client, namespace)
					w.Labels["newLabel"] = "newValue"
					return w
				},
				actualFn: workloadDaemonSetWithDefaultSpecHash,
				expectedFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.DaemonSet {
					w := workloadDaemonSetWithDefaultSpecHash(ctx, client, namespace)
					w.Labels["newLabel"] = "newValue"
					return w
				},
				expectError:  false,
				expectUpdate: true,
			},
		),
		Entry("When there is a change in the annotations field it is updated",
			applyDaemonSetArguments{
				desiredFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.DaemonSet {
					w := workloadDaemonSet(ctx, client, namespace)
					w.Annotations["newAnnotation"] = "newValue"
					return w
				},
				actualFn: workloadDaemonSetWithDefaultSpecHash,
				expectedFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.DaemonSet {
					w := workloadDaemonSetWithDefaultSpecHash(ctx, client, namespace)
					w.Annotations["newAnnotation"] = "newValue"
					return w
				},
				expectError:  false,
				expectUpdate: true,
			},
		),
	)

	DescribeTable("Recreates daemonset after selector change when expected",
		func(args applyDaemonSetArguments) {
			eventRecorder := record.NewFakeRecorder(1000)

			actualDaemonSet := args.actualFn(ctx, k8sClient, namespaceName)
			Expect(k8sClient.Create(ctx, actualDaemonSet)).To(Succeed())
			Expect(actualDaemonSet.UID).NotTo(BeNil())

			desiredDaemonSet := args.desiredFn(ctx, k8sClient, namespaceName)
			_, err := applyDaemonSet(ctx, k8sClient, eventRecorder, desiredDaemonSet)
			if args.expectError {
				Expect(err).To(HaveOccurred(), "expected error")
				Expect(args.errorMsg).ToNot(BeEmpty(), "expected error string is empty")
				Expect(err).To(MatchError(ContainSubstring(args.errorMsg)))
			} else {
				Expect(err).NotTo(HaveOccurred(), "expected no error")
			}

			updatedDaemonSet := &appsv1.DaemonSet{}
			deploymentObjectKey := appsclientv1.ObjectKeyFromObject(desiredDaemonSet)
			Expect(k8sClient.Get(ctx, deploymentObjectKey, updatedDaemonSet)).To(Succeed())
			if args.expectUpdate {
				Expect(actualDaemonSet.UID).ShouldNot(BeEquivalentTo(updatedDaemonSet.UID))
			} else {
				Expect(actualDaemonSet.UID).Should(BeEquivalentTo(updatedDaemonSet.UID))
			}

			expectedDaemonSet := args.expectedFn(ctx, k8sClient, namespaceName)
			Expect(equality.Semantic.DeepDerivative(expectedDaemonSet.Spec, updatedDaemonSet.Spec)).To(BeTrue(), fmt.Sprintf("Expected deployment: %+v, got %+v", expectedDaemonSet, updatedDaemonSet))

			Expect(expectedDaemonSet.Annotations).Should(HaveKeyWithValue(specHashAnnotation, updatedDaemonSet.Annotations[specHashAnnotation]))

			dss := &appsv1.DaemonSetList{}
			Expect(k8sClient.List(ctx, dss)).To(Succeed())
			Expect(len(dss.Items)).To(BeEquivalentTo(1))
		},
		Entry("When there is a change in the match labels field it is recreated",
			applyDaemonSetArguments{
				desiredFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.DaemonSet {
					w := workloadDaemonSet(ctx, client, namespace)
					w.Spec.Selector = &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"bar": "baz",
						},
					}
					w.Spec.Template.Labels = map[string]string{"bar": "baz"}
					return w
				},
				actualFn: workloadDaemonSetWithDefaultSpecHash,
				expectedFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.DaemonSet {
					w := workloadDaemonSet(ctx, client, namespace)
					w.Spec.Selector = &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"bar": "baz",
						},
					}
					w.Spec.Template.Labels = map[string]string{"bar": "baz"}
					_ = annotatePodSpecWithRelatedConfigsHash(ctx, client, w.Namespace, &w.Spec.Template)
					_ = setSpecHashAnnotation(&w.ObjectMeta, w.Spec)
					return w
				},
				expectError:  false,
				expectUpdate: true,
			},
		),
		Entry("When the resource is malformed an error is reported",
			applyDaemonSetArguments{
				desiredFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.DaemonSet {
					w := workloadDaemonSet(ctx, client, namespace)
					w.Spec.Selector = &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"bar": "baz",
						},
					}
					w.Spec.Template.Labels = map[string]string{"fiz": "baz"}
					return w
				},
				actualFn:     workloadDaemonSetWithDefaultSpecHash,
				expectedFn:   workloadDaemonSetWithDefaultSpecHash,
				expectError:  true,
				errorMsg:     "`selector` does not match template `labels`",
				expectUpdate: false,
			},
		),
		Entry("When the resource deletion is stuck it should report an error",
			applyDaemonSetArguments{
				desiredFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.DaemonSet {
					w := workloadDaemonSet(ctx, client, namespace)
					w.Spec.Selector = &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"bar": "baz",
						},
					}
					w.Spec.Template.Labels = map[string]string{"bar": "baz"}
					return w
				},
				actualFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.DaemonSet {
					ds := workloadDaemonSetWithDefaultSpecHash(ctx, client, namespace)
					ds.Finalizers = []string{"foo.bar/baz"}
					return ds
				},
				expectedFn:   workloadDaemonSetWithDefaultSpecHash,
				expectError:  true,
				errorMsg:     "object is being deleted: daemonsets.apps \"apiserver\" already exists",
				expectUpdate: false,
			},
		),
	)

	DescribeTable("Updates daemonset after configuration change when expected",
		func(args applyDaemonSetArguments) {
			eventRecorder := record.NewFakeRecorder(1000)

			initialSecret := simpleSecret(namespaceName, "secret")
			Expect(k8sClient.Create(ctx, initialSecret)).To(Succeed())
			Expect(initialSecret.UID).NotTo(BeNil())

			initialConfigMap := simpleConfigMap(namespaceName, "configmap")
			Expect(k8sClient.Create(ctx, initialConfigMap)).To(Succeed())
			Expect(initialSecret.UID).NotTo(BeNil())

			daemonSet := args.desiredFn(ctx, k8sClient, namespaceName)
			_, err := applyDaemonSet(ctx, k8sClient, eventRecorder, daemonSet)
			Expect(err).ToNot(HaveOccurred())

			if args.updConfigsFn != nil {
				args.updConfigsFn(initialSecret, initialConfigMap)
				Expect(k8sClient.Update(ctx, initialSecret)).To(Succeed())
				Expect(k8sClient.Update(ctx, initialConfigMap)).To(Succeed())
			}

			updated, err := applyDaemonSet(ctx, k8sClient, eventRecorder, daemonSet)
			Expect(err).ToNot(HaveOccurred())
			Expect(updated).To(Equal(args.expectUpdate), "resource update expectation mismatch")
		},
		Entry("When related config specified in volumes did not change it is not updated",
			applyDaemonSetArguments{
				desiredFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.DaemonSet {
					ds := workloadDaemonSet(ctx, client, namespace)
					addSecretVolumeToPodSpec(&ds.Spec.Template.Spec, "secret", "secret")
					addConfigMapVolumeToPodSpec(&ds.Spec.Template.Spec, "configmap", "configmap")
					return ds
				},
				updConfigsFn: nil,
				expectUpdate: false,
			},
		),
		Entry("When related config is changed it is updated",
			applyDaemonSetArguments{
				desiredFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.DaemonSet {
					ds := workloadDaemonSet(ctx, client, namespace)
					addSecretVolumeToPodSpec(&ds.Spec.Template.Spec, "secret", "secret")
					addConfigMapVolumeToPodSpec(&ds.Spec.Template.Spec, "configmap", "configmap")
					return ds
				},
				updConfigsFn: func(secret *corev1.Secret, configMap *corev1.ConfigMap) {
					secret.Data = map[string][]byte{"bar": []byte("bazz")}
				},
				expectUpdate: true,
			},
		),
		Entry("When non-existent config is specified it is not updated",
			applyDaemonSetArguments{
				desiredFn: func(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.DaemonSet {
					ds := workloadDaemonSet(ctx, client, namespace)
					addSecretVolumeToPodSpec(&ds.Spec.Template.Spec, "non-existed", "non-existed")
					return ds
				},
				updConfigsFn: nil,
				expectUpdate: false,
			},
		),
	)

})

type podDisruptionBudgetSupplier func(string) *policyv1.PodDisruptionBudget

type applyPodDisruptionBudgetArguments struct {
	inputFn        podDisruptionBudgetSupplier
	existingFn     podDisruptionBudgetSupplier
	expectModified bool
}

var _ = Describe("applyPodDisruptionBudget", func() {
	var namespaceName string

	BeforeEach(func() {
		By("Setting up a namespace for the test")
		ns := &corev1.Namespace{}
		ns.SetGenerateName(namespaceNamePrefix)
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		namespaceName = ns.GetName()
	})

	AfterEach(func() {
		testutils.CleanupResources(Default, ctx, cfg, k8sClient, namespaceName,
			&policyv1.PodDisruptionBudget{},
		)
	})

	DescribeTable("Updates pod disrutpion budget when expected",
		func(args applyPodDisruptionBudgetArguments) {
			recorder := record.NewFakeRecorder(1000)

			if args.existingFn != nil {
				existing := args.existingFn(namespaceName)
				Expect(k8sClient.Create(ctx, existing)).To(Succeed())
			}

			input := args.inputFn(namespaceName)
			actualModified, err := applyPodDisruptionBudget(ctx, k8sClient, recorder, input)
			Expect(err).NotTo(HaveOccurred())
			Expect(args.expectModified).To(BeEquivalentTo(actualModified), "Resource was modified")
		},
		Entry("When it does not exist it is created",
			applyPodDisruptionBudgetArguments{
				inputFn:        podDisruptionBudget,
				existingFn:     nil,
				expectModified: true,
			},
		),
		Entry("When there is an extra label on the existing pdb it does not update",
			applyPodDisruptionBudgetArguments{
				inputFn: podDisruptionBudget,
				existingFn: func(namespace string) *policyv1.PodDisruptionBudget {
					pdb := podDisruptionBudget(namespace)
					pdb.Labels = map[string]string{"bar": "baz"}
					return pdb
				},
				expectModified: false,
			},
		),
		Entry("When there is a missing label on the existing pdb it is updated",
			applyPodDisruptionBudgetArguments{
				inputFn: func(namespace string) *policyv1.PodDisruptionBudget {
					pdb := podDisruptionBudget(namespace)
					pdb.Labels = map[string]string{"new": "merge"}
					return pdb
				},
				existingFn: func(namespace string) *policyv1.PodDisruptionBudget {
					pdb := podDisruptionBudget(namespace)
					pdb.Labels = map[string]string{"bar": "baz"}
					return pdb
				},
				expectModified: true,
			},
		),
		Entry("When there is a mismatch of data it is updated",
			applyPodDisruptionBudgetArguments{
				inputFn: func(namespace string) *policyv1.PodDisruptionBudget {
					pdb := podDisruptionBudget(namespace)
					minAvailable := intstr.FromInt(3)
					pdb.Spec.MinAvailable = &minAvailable
					return pdb
				},
				existingFn:     podDisruptionBudget,
				expectModified: true,
			},
		),
	)
})

func workloadDeployment(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.Deployment {
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
			Replicas: ptr.To[int32](3),
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

func workloadDeploymentWithDefaultSpecHash(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.Deployment {
	w := workloadDeployment(ctx, client, namespace)
	// Apply the same hash calculation logic used in production code
	_ = annotatePodSpecWithRelatedConfigsHash(ctx, client, w.Namespace, &w.Spec.Template)
	_ = setSpecHashAnnotation(&w.ObjectMeta, w.Spec)
	return w
}

func simpleSecret(namespace string, name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Data: map[string][]byte{"foo": []byte("bar")},
	}
}

func simpleConfigMap(namespace string, name string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Data: map[string]string{"foo": "bar"},
	}
}

func addSecretVolumeToPodSpec(spec *corev1.PodSpec, secretName string, volumeName string) {
	volume := corev1.Volume{
		Name: volumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: secretName},
		},
	}

	spec.Volumes = append(spec.Volumes, volume)
}

func addConfigMapVolumeToPodSpec(spec *corev1.PodSpec, cmName string, volumeName string) {
	volume := corev1.Volume{
		Name: volumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
			},
		},
	}

	spec.Volumes = append(spec.Volumes, volume)
}

func workloadDaemonSet(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.DaemonSet {
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

func workloadDaemonSetWithDefaultSpecHash(ctx context.Context, client appsclientv1.Client, namespace string) *appsv1.DaemonSet {
	w := workloadDaemonSet(ctx, client, namespace)
	// Apply the same hash calculation logic used in production code
	_ = annotatePodSpecWithRelatedConfigsHash(ctx, client, w.Namespace, &w.Spec.Template)
	_ = setSpecHashAnnotation(&w.ObjectMeta, w.Spec)
	return w
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
