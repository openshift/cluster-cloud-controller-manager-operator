package controllers

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/controllers/resourceapply"
)

const (
	timeout            = time.Second * 10
	testImagesFilePath = "./fixtures/images.json"
)

var _ = Describe("Cluster Operator status controller", func() {
	var operator *configv1.ClusterOperator
	var operatorController *CloudOperatorReconciler

	BeforeEach(func() {
		operatorController = &CloudOperatorReconciler{
			ClusterOperatorStatusClient: ClusterOperatorStatusClient{
				Client:           cl,
				Clock:            clocktesting.NewFakePassiveClock(time.Now()),
				ManagedNamespace: defaultManagementNamespace,
				Recorder:         record.NewFakeRecorder(32),
			},
			Scheme: scheme.Scheme,
		}
		operator = &configv1.ClusterOperator{}
		operator.SetName(clusterOperatorName)
	})

	AfterEach(func() {
		deleteClusterOperator(context.Background(), cl)
	})

	type testCase struct {
		releaseVersionEnvVariableValue string
		namespace                      string
		existingCO                     *configv1.ClusterOperator
	}

	DescribeTable("should ensure Cluster Operator status is present",
		func(tc testCase) {
			expectedVersion := "unknown"

			if tc.releaseVersionEnvVariableValue != "" {
				expectedVersion = tc.releaseVersionEnvVariableValue
				operatorController.ReleaseVersion = tc.releaseVersionEnvVariableValue
			}

			if tc.namespace != "" {
				operatorController.ManagedNamespace = tc.namespace
			}

			if tc.existingCO != nil {
				err := cl.Create(context.Background(), tc.existingCO)
				Expect(err).To(Succeed())
			}

			_, err := operatorController.Reconcile(context.Background(), reconcile.Request{})
			Expect(err).To(Succeed())

			getOp := &configv1.ClusterOperator{}
			Eventually(func() error {
				err := cl.Get(context.Background(), client.ObjectKey{Name: clusterOperatorName}, getOp)
				if err != nil {
					return err
				}
				// Successful sync means CO exists and the status is not empty
				if getOp == nil || len(getOp.Status.Versions) == 0 {
					return fmt.Errorf("ClusterOperator status versions not yet populated")
				}
				return nil
			}, timeout).Should(Succeed())

			// check version.
			Expect(getOp.Status.Versions).To(HaveLen(1))
			Expect(getOp.Status.Versions[0].Name).To(Equal(operatorVersionKey))
			Expect(getOp.Status.Versions[0].Version).To(Equal(expectedVersion))

			// check conditions.
			Expect(v1helpers.FindStatusCondition(getOp.Status.Conditions, configv1.OperatorAvailable).Status).To(Equal(configv1.ConditionTrue))
			Expect(v1helpers.FindStatusCondition(getOp.Status.Conditions, configv1.OperatorAvailable).Reason).To(Equal(ReasonAsExpected))
			Expect(v1helpers.FindStatusCondition(getOp.Status.Conditions, configv1.OperatorUpgradeable).Status).To(Equal(configv1.ConditionTrue))
			Expect(v1helpers.FindStatusCondition(getOp.Status.Conditions, configv1.OperatorUpgradeable).Reason).To(Equal(ReasonAsExpected))
			Expect(v1helpers.FindStatusCondition(getOp.Status.Conditions, configv1.OperatorDegraded).Status).To(Equal(configv1.ConditionFalse))
			Expect(v1helpers.FindStatusCondition(getOp.Status.Conditions, configv1.OperatorDegraded).Reason).To(Equal(ReasonAsExpected))
			Expect(v1helpers.FindStatusCondition(getOp.Status.Conditions, configv1.OperatorProgressing).Status).To(Equal(configv1.ConditionFalse))
			Expect(v1helpers.FindStatusCondition(getOp.Status.Conditions, configv1.OperatorProgressing).Reason).To(Equal(ReasonAsExpected))
			Expect(v1helpers.FindStatusCondition(getOp.Status.Conditions, cloudControllerOwnershipCondition)).To(BeNil())

			// check related objects.
			Expect(getOp.Status.RelatedObjects).To(Equal(operatorController.relatedObjects(context.Background())))
		},
		Entry("when there's no existing cluster operator nor release version", testCase{
			releaseVersionEnvVariableValue: "unknown",
			existingCO:                     nil,
		}),
		Entry("when there's no existing cluster operator but there's release version", testCase{
			releaseVersionEnvVariableValue: "a_cvo_given_version",
			existingCO:                     nil,
		}),
		Entry("when there's no existing cluster operator but there's release version", testCase{
			releaseVersionEnvVariableValue: "a_cvo_given_version",
			existingCO:                     nil,
			namespace:                      "different-ccm-namespace",
		}),
		Entry("when there's an existing cluster operator and a release version", testCase{
			releaseVersionEnvVariableValue: "another_cvo_given_version",
			existingCO: &configv1.ClusterOperator{
				ObjectMeta: metav1.ObjectMeta{
					Name: clusterOperatorName,
				},
				Status: configv1.ClusterOperatorStatus{
					Conditions: []configv1.ClusterOperatorStatusCondition{
						{
							Type:               configv1.OperatorAvailable,
							Status:             configv1.ConditionFalse,
							LastTransitionTime: metav1.Now(),
							Reason:             "",
							Message:            "",
						},
						{
							Type:               configv1.OperatorDegraded,
							Status:             configv1.ConditionTrue,
							LastTransitionTime: metav1.Now(),
							Reason:             "",
							Message:            "",
						},
						{
							Type:               configv1.OperatorProgressing,
							Status:             configv1.ConditionTrue,
							LastTransitionTime: metav1.Now(),
							Reason:             "",
							Message:            "",
						},
						{
							Type:               configv1.OperatorUpgradeable,
							Status:             configv1.ConditionFalse,
							LastTransitionTime: metav1.Now(),
							Reason:             "",
							Message:            "",
						},
					},
					Versions: []configv1.OperandVersion{
						{
							Name:    "anything",
							Version: "anything",
						},
					},
					RelatedObjects: []configv1.ObjectReference{
						{
							Group:    "",
							Resource: "anything",
							Name:     "anything",
						},
					},
				},
			},
		}),
		Entry("when there's a CloudControllerOwner condition with True status", testCase{
			releaseVersionEnvVariableValue: "another_cvo_given_version",
			existingCO: &configv1.ClusterOperator{
				ObjectMeta: metav1.ObjectMeta{
					Name: clusterOperatorName,
				},
				Status: configv1.ClusterOperatorStatus{
					Conditions: []configv1.ClusterOperatorStatusCondition{
						{
							Type:               cloudControllerOwnershipCondition,
							Status:             configv1.ConditionTrue,
							LastTransitionTime: metav1.Now(),
							Reason:             "",
							Message:            "",
						},
					},
				},
			},
		}),
		Entry("when there's a CloudControllerOwner condition with False status", testCase{
			releaseVersionEnvVariableValue: "another_cvo_given_version",
			existingCO: &configv1.ClusterOperator{
				ObjectMeta: metav1.ObjectMeta{
					Name: clusterOperatorName,
				},
				Status: configv1.ClusterOperatorStatus{
					Conditions: []configv1.ClusterOperatorStatusCondition{
						{
							Type:               cloudControllerOwnershipCondition,
							Status:             configv1.ConditionFalse,
							LastTransitionTime: metav1.Now(),
							Reason:             "",
							Message:            "",
						},
					},
				},
			},
		}),
	)
})

var _ = Describe("toClusterOperator mapping is targeting requests to 'cloud-controller-manager' clusterOperator", func() {
	It("Should map reconciles to 'cloud-controller-manager' clusterOperator", func() {
		object := &configv1.Infrastructure{}
		requests := []reconcile.Request{{
			NamespacedName: client.ObjectKey{
				Name: clusterOperatorName,
			},
		}}
		Expect(toClusterOperator(ctx, object)).To(Equal(requests))
	})
})

var _ = Describe("Apply resources should", func() {
	var resources []client.Object
	var reconciler *CloudOperatorReconciler
	var recorder *record.FakeRecorder
	var getConfigForPlatform func(status *configv1.PlatformStatus) config.OperatorConfig

	BeforeEach(func() {
		c, err := cache.New(cfg, cache.Options{})
		Expect(err).To(Succeed())
		w, err := NewObjectWatcher(WatcherOptions{Cache: c})
		Expect(err).To(Succeed())

		ns := &corev1.Namespace{}
		ns.SetName("cluster-cloud-controller-manager")

		resources = []client.Object{}
		if !apierrors.IsNotFound(cl.Get(context.TODO(), client.ObjectKeyFromObject(ns), ns.DeepCopy())) {
			Expect(cl.Create(context.TODO(), ns.DeepCopy())).ShouldNot(HaveOccurred())
		}

		recorder = record.NewFakeRecorder(32)
		recorder.IncludeObject = true
		reconciler = &CloudOperatorReconciler{
			ClusterOperatorStatusClient: ClusterOperatorStatusClient{
				Clock:    clocktesting.NewFakePassiveClock(time.Now()),
				Client:   cl,
				Recorder: recorder,
			},
			Scheme:  scheme.Scheme,
			watcher: w,
		}

		getConfigForPlatform = func(status *configv1.PlatformStatus) config.OperatorConfig {
			return config.OperatorConfig{
				ManagedNamespace: DefaultManagedNamespace,
				ImagesReference: config.ImagesReference{
					CloudControllerManagerOperator:  "registry.ci.openshift.org/openshift:cluster-cloud-controller-manager-operator",
					CloudControllerManagerAWS:       "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
					CloudControllerManagerAzure:     "quay.io/openshift/origin-azure-cloud-controller-manager",
					CloudNodeManagerAzure:           "quay.io/openshift/origin-azure-cloud-node-manager",
					CloudControllerManagerOpenStack: "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager",
				},
				PlatformStatus: status,
			}
		}
	})

	It("Expect update when resources are not found", func() {
		operatorConfig := getConfigForPlatform(&configv1.PlatformStatus{Type: configv1.AWSPlatformType})
		awsResources, err := cloud.GetResources(operatorConfig)
		Expect(err).To(Succeed())

		resources = append(resources, awsResources...)

		updated, err := reconciler.applyResources(context.TODO(), resources)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(updated).To(BeTrue(), "expected applyResources to report that at least one resource was created or updated")
		// two resources should report successful update, deployment and pdb
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Resource was successfully created")))
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Resource was successfully created")))
	})

	It("Expect update when deployment generation have changed", func() {
		var dep *appsv1.Deployment
		operatorConfig := getConfigForPlatform(&configv1.PlatformStatus{Type: configv1.AWSPlatformType})

		freshResources, err := cloud.GetResources(operatorConfig)
		Expect(err).To(Succeed())

		for _, res := range freshResources {
			if deployment, ok := res.(*appsv1.Deployment); ok {
				dep = deployment
				break
			}
		}
		resources = append(resources, dep)

		updated, err := reconciler.applyResources(context.TODO(), resources)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(updated).To(BeTrue(), "expected applyResources to report that at least one resource was created or updated")
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Resource was successfully created")))

		dep.Spec.Replicas = ptr.To[int32](20)

		updated, err = reconciler.applyResources(context.TODO(), resources)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(updated).To(BeTrue(), "expected applyResources to report that at least one resource was created or updated")
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Resource was successfully updated")))

		// No update as resource didn't change
		updated, err = reconciler.applyResources(context.TODO(), resources)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(updated).To(BeFalse())
	})

	It("Expect error when object requested is incorrect", func() {
		operatorConfig := getConfigForPlatform(&configv1.PlatformStatus{Type: configv1.AWSPlatformType})
		objects, err := cloud.GetResources(operatorConfig)
		Expect(err).To(Succeed())

		objects[0].SetNamespace("non-existent")

		updated, err := reconciler.applyResources(context.TODO(), objects)
		Expect(err).Should(HaveOccurred())
		Expect(updated).To(BeFalse())
		Eventually(recorder.Events).Should(Receive(ContainSubstring(resourceapply.ResourceCreateFailedEvent)))
	})

	It("Expect no update when resources are applied twice", func() {
		operatorConfig := getConfigForPlatform(&configv1.PlatformStatus{Type: configv1.AWSPlatformType})
		awsResources, err := cloud.GetResources(operatorConfig)
		Expect(err).To(Succeed())

		resources = append(resources, awsResources...)

		updated, err := reconciler.applyResources(context.TODO(), resources)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(updated).To(BeTrue(), "expected applyResources to report that at least one resource was created or updated")
		// three resources should report successful update, deployment, pdb and service
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Resource was successfully created")))
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Resource was successfully created")))
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Resource was successfully created")))

		updated, err = reconciler.applyResources(context.TODO(), resources)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(updated).To(BeFalse())
	})

	It("Expect to have just one item in the port list after it's been updated", func() {
		var dep *appsv1.Deployment
		operatorConfig := getConfigForPlatform(&configv1.PlatformStatus{Type: configv1.AWSPlatformType})

		freshResources, err := cloud.GetResources(operatorConfig)
		Expect(err).ShouldNot(HaveOccurred())

		for _, res := range freshResources {
			if deployment, ok := res.(*appsv1.Deployment); ok {
				dep = deployment
				break
			}
		}

		resources = append(resources, dep)

		updated, err := reconciler.applyResources(context.TODO(), resources)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(updated).To(BeTrue(), "expected applyResources to report that at least one resource was created or updated")
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Resource was successfully created")))

		// Manually changing the port number
		ports := []corev1.ContainerPort{
			{
				ContainerPort: 11258,
				Name:          "https",
				Protocol:      corev1.ProtocolTCP,
			},
		}
		dep.Spec.Template.Spec.Containers[0].Ports = ports
		err = reconciler.Update(context.TODO(), dep)
		Expect(err).ShouldNot(HaveOccurred())

		// Checking that the port has been updated and there is only one item in the list
		Expect(cl.Get(context.Background(), client.ObjectKeyFromObject(dep), dep)).To(Succeed())
		Expect(len(dep.Spec.Template.Spec.Containers[0].Ports)).To(Equal(1))
		Expect(dep.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort).To(Equal(int32(11258)))

		// Apply resources again
		freshResources, err = cloud.GetResources(operatorConfig)
		Expect(err).ShouldNot(HaveOccurred())

		for _, res := range freshResources {
			if deployment, ok := res.(*appsv1.Deployment); ok {
				resources = []client.Object{deployment}
				break
			}
		}

		updated, err = reconciler.applyResources(context.TODO(), resources)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(updated).To(BeTrue(), "expected applyResources to report that at least one resource was created or updated")
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Resource was successfully updated")))

		// Checking that the port has been reverted back and there is only one item in the list
		Expect(cl.Get(context.Background(), client.ObjectKeyFromObject(dep), dep)).To(Succeed())
		Expect(len(dep.Spec.Template.Spec.Containers[0].Ports)).To(Equal(1))
		Expect(dep.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort).To(Equal(int32(10258)))
	})

	It("Expect to have just one item in the port list after user added another one", func() {
		var dep *appsv1.Deployment
		operatorConfig := getConfigForPlatform(&configv1.PlatformStatus{Type: configv1.AWSPlatformType})

		freshResources, err := cloud.GetResources(operatorConfig)
		Expect(err).ShouldNot(HaveOccurred())

		for _, res := range freshResources {
			if deployment, ok := res.(*appsv1.Deployment); ok {
				dep = deployment
				break
			}
		}

		resources = append(resources, dep)

		updated, err := reconciler.applyResources(context.TODO(), resources)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(updated).To(BeTrue(), "expected applyResources to report that at least one resource was created or updated")
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Resource was successfully created")))

		// Manually adding another port
		newPort := corev1.ContainerPort{
			ContainerPort: 11258,
			Name:          "http",
			Protocol:      corev1.ProtocolTCP,
		}
		dep.Spec.Template.Spec.Containers[0].Ports = append(dep.Spec.Template.Spec.Containers[0].Ports, newPort)
		err = reconciler.Update(context.TODO(), dep)
		Expect(err).ShouldNot(HaveOccurred())

		// Checking that the port has been added and there are two items in the list
		Expect(cl.Get(context.Background(), client.ObjectKeyFromObject(dep), dep)).To(Succeed())
		Expect(len(dep.Spec.Template.Spec.Containers[0].Ports)).To(Equal(2))
		Expect(dep.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort).To(Equal(int32(10258)))
		Expect(dep.Spec.Template.Spec.Containers[0].Ports[1].ContainerPort).To(Equal(int32(11258)))

		// Apply resources again
		freshResources, err = cloud.GetResources(operatorConfig)
		Expect(err).ShouldNot(HaveOccurred())

		for _, res := range freshResources {
			if deployment, ok := res.(*appsv1.Deployment); ok {
				resources = []client.Object{deployment}
				break
			}
		}

		updated, err = reconciler.applyResources(context.TODO(), resources)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(updated).To(BeTrue(), "expected applyResources to report that at least one resource was created or updated")
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Resource was successfully updated")))

		// Checking that the port list has been reverted back and there is only one item in the list
		Expect(cl.Get(context.Background(), client.ObjectKeyFromObject(dep), dep)).To(Succeed())
		Expect(len(dep.Spec.Template.Spec.Containers[0].Ports)).To(Equal(1))
		Expect(dep.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort).To(Equal(int32(10258)))
	})

	It("Expect to have deployment labels merged with user ones", func() {
		var dep *appsv1.Deployment

		labelName := "my-label"
		labelValue := "someValue"

		operatorConfig := getConfigForPlatform(&configv1.PlatformStatus{Type: configv1.AWSPlatformType})

		freshResources, err := cloud.GetResources(operatorConfig)
		Expect(err).ShouldNot(HaveOccurred())

		for _, res := range freshResources {
			if deployment, ok := res.(*appsv1.Deployment); ok {
				dep = deployment
				break
			}
		}

		resources = append(resources, dep)

		updated, err := reconciler.applyResources(context.TODO(), resources)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(updated).To(BeTrue(), "expected applyResources to report that at least one resource was created or updated")
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Resource was successfully created")))

		// Manually inserting a new label
		dep.Labels[labelName] = labelValue
		err = reconciler.Update(context.TODO(), dep)
		Expect(err).ShouldNot(HaveOccurred())

		// Checking that the label has been added and there are two items in the map
		Expect(cl.Get(context.Background(), client.ObjectKeyFromObject(dep), dep)).To(Succeed())
		Expect(len(dep.Labels)).To(Equal(3))
		Expect(dep.Labels[labelName]).To(Equal(labelValue))

		// Apply resources again
		freshResources, err = cloud.GetResources(operatorConfig)
		Expect(err).ShouldNot(HaveOccurred())

		for _, res := range freshResources {
			if deployment, ok := res.(*appsv1.Deployment); ok {
				resources = []client.Object{deployment}
				break
			}
		}

		updated, err = reconciler.applyResources(context.TODO(), resources)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(updated).To(BeTrue(), "expected applyResources to report that at least one resource was created or updated")
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Resource was successfully updated")))

		// Checking that the new label is still there
		Expect(cl.Get(context.Background(), client.ObjectKeyFromObject(dep), dep)).To(Succeed())
		Expect(len(dep.Labels)).To(Equal(3))
		Expect(dep.Labels[labelName]).To(Equal(labelValue))
	})

	It("Expect to have modified system label reverted back", func() {
		var dep *appsv1.Deployment
		operatorConfig := getConfigForPlatform(&configv1.PlatformStatus{Type: configv1.AWSPlatformType})

		freshResources, err := cloud.GetResources(operatorConfig)
		Expect(err).ShouldNot(HaveOccurred())

		for _, res := range freshResources {
			if deployment, ok := res.(*appsv1.Deployment); ok {
				dep = deployment
				break
			}
		}

		resources = append(resources, dep)

		updated, err := reconciler.applyResources(context.TODO(), resources)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(updated).To(BeTrue(), "expected applyResources to report that at least one resource was created or updated")
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Resource was successfully created")))

		// Now the deployment has just one label "k8s-app: aws-cloud-controller-manager"
		// Manually modifying the value
		dep.Labels["k8s-app"] = "someValue"
		dep.Labels[common.CloudControllerManagerProviderLabel] = "FOO"
		err = reconciler.Update(context.TODO(), dep)
		Expect(err).ShouldNot(HaveOccurred())

		// Checking that the label has been updated
		Expect(cl.Get(context.Background(), client.ObjectKeyFromObject(dep), dep)).To(Succeed())
		Expect(len(dep.Labels)).To(Equal(2))
		Expect(dep.Labels["k8s-app"]).To(Equal("someValue"))
		Expect(dep.Labels[common.CloudControllerManagerProviderLabel]).To(Equal("FOO"))

		// Apply resources again
		freshResources, err = cloud.GetResources(operatorConfig)
		Expect(err).ShouldNot(HaveOccurred())

		for _, res := range freshResources {
			if deployment, ok := res.(*appsv1.Deployment); ok {
				resources = []client.Object{deployment}
				break
			}
		}

		updated, err = reconciler.applyResources(context.TODO(), resources)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(updated).To(BeTrue(), "expected applyResources to report that at least one resource was created or updated")
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Resource was successfully updated")))

		// Checking that the label value has been reverted and there is only one item in the map
		Expect(cl.Get(context.Background(), client.ObjectKeyFromObject(dep), dep)).To(Succeed())
		Expect(len(dep.Labels)).To(Equal(2))
		Expect(dep.Labels["k8s-app"]).To(Equal("aws-cloud-controller-manager"))
		Expect(dep.Labels[common.CloudControllerManagerProviderLabel]).To(Equal("AWS"))
	})

	It("reports updated=true when only a non-final resource changed", func() {
		operatorConfig := getConfigForPlatform(&configv1.PlatformStatus{Type: configv1.AWSPlatformType})
		awsResources, err := cloud.GetResources(operatorConfig)
		Expect(err).To(Succeed())
		Expect(len(awsResources)).To(BeNumerically(">=", 2))

		resources = append(resources, awsResources...)

		updated, err := reconciler.applyResources(context.TODO(), resources)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(updated).To(BeTrue())

		// Drain recorder events from initial creation
		for len(recorder.Events) > 0 {
			<-recorder.Events
		}

		// Modify only the deployment so it gets updated, while later resources remain unchanged
		for i, res := range resources {
			if dep, ok := res.(*appsv1.Deployment); ok {
				dep.Spec.Replicas = ptr.To[int32](99)
				resources[i] = dep
				break
			}
		}

		updated, err = reconciler.applyResources(context.Background(), resources)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(updated).To(BeTrue(), "should report updated even when only a non-final resource changed")
	})

	Context("operandsConverged", func() {
		It("returns false when deployment rollout is not complete, true after patching status", func() {
			operatorConfig := getConfigForPlatform(&configv1.PlatformStatus{Type: configv1.AWSPlatformType})
			awsResources, err := cloud.GetResources(operatorConfig)
			Expect(err).To(Succeed())
			resources = append(resources, awsResources...)

			updated, err := reconciler.applyResources(context.Background(), resources)
			Expect(err).ShouldNot(HaveOccurred())
			Expect(updated).To(BeTrue())

			// envtest has no deployment controller, so no Progressing condition exists.
			converged, err := reconciler.operandsConverged(context.Background(), resources)
			Expect(err).To(Succeed())
			Expect(converged).To(BeFalse(), "should not be converged when deployment has no Progressing condition")

			// Patch the deployment status to mark rollout complete.
			for _, res := range resources {
				if dep, ok := res.(*appsv1.Deployment); ok {
					live := &appsv1.Deployment{}
					Expect(cl.Get(context.Background(), client.ObjectKeyFromObject(dep), live)).To(Succeed())
					live.Status.ObservedGeneration = live.Generation
					live.Status.Conditions = []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentProgressing,
							Status: corev1.ConditionTrue,
							Reason: "NewReplicaSetAvailable",
						},
					}
					Expect(cl.Status().Update(context.Background(), live)).To(Succeed())
				}
			}

			converged, err = reconciler.operandsConverged(context.Background(), resources)
			Expect(err).To(Succeed())
			Expect(converged).To(BeTrue(), "should be converged after deployment rollout completes")
		})
	})

	It("should set Progressing=True on first sync, remain progressing until rollout completes, then stop", func() {
		reconciler.ManagedNamespace = DefaultManagedNamespace

		co := &configv1.ClusterOperator{}
		co.SetName(clusterOperatorName)
		Expect(cl.Create(context.Background(), co)).To(Succeed())

		operatorConfig := getConfigForPlatform(&configv1.PlatformStatus{Type: configv1.AWSPlatformType})
		awsResources, err := cloud.GetResources(operatorConfig)
		Expect(err).To(Succeed())
		resources = append(resources, awsResources...)

		// First sync: resources do not yet exist, so applyResources reports updated=true.
		progressing, err := reconciler.sync(context.Background(), operatorConfig, nil)
		Expect(err).To(Succeed())
		Expect(progressing).To(BeTrue(), "sync should report progressing when resources are newly applied")

		Expect(cl.Get(context.Background(), client.ObjectKey{Name: clusterOperatorName}, co)).To(Succeed())
		progressingCond := v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorProgressing)
		Expect(progressingCond).NotTo(BeNil(), "Progressing condition should exist after first sync")
		Expect(progressingCond.Status).To(
			Equal(configv1.ConditionTrue), "Progressing should be True after resources are first applied",
		)

		// Second sync: resources exist and are unchanged, but deployment rollout is
		// not complete (envtest has no deployment controller). sync should still
		// report progressing.
		progressing, err = reconciler.sync(context.Background(), operatorConfig, nil)
		Expect(err).To(Succeed())
		Expect(progressing).To(BeTrue(), "sync should report progressing while deployment rollout is incomplete")

		// Patch deployment status to mark rollout complete.
		for _, res := range resources {
			if dep, ok := res.(*appsv1.Deployment); ok {
				live := &appsv1.Deployment{}
				Expect(cl.Get(context.Background(), client.ObjectKeyFromObject(dep), live)).To(Succeed())
				live.Status.ObservedGeneration = live.Generation
				live.Status.Conditions = []appsv1.DeploymentCondition{
					{
						Type:   appsv1.DeploymentProgressing,
						Status: corev1.ConditionTrue,
						Reason: "NewReplicaSetAvailable",
					},
				}
				Expect(cl.Status().Update(context.Background(), live)).To(Succeed())
			}
		}

		// Third sync: deployment rollout is complete, sync should report not progressing.
		progressing, err = reconciler.sync(context.Background(), operatorConfig, nil)
		Expect(err).To(Succeed())
		Expect(progressing).To(BeFalse(), "sync should not report progressing once deployment rollout completes")
	})

	AfterEach(func() {
		deleteClusterOperator(context.Background(), cl)

		for _, operand := range resources {
			Expect(cl.Delete(context.Background(), operand)).To(Succeed())

			Eventually(func() error {
				err := cl.Get(context.Background(), client.ObjectKeyFromObject(operand), operand)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("expected operand %s to be deleted", operand.GetName())
			}, timeout).Should(Succeed())
		}
	})
})

var _ = Describe("CloudOperatorReconciler error handling", func() {
	ctx := context.Background()

	AfterEach(func() {
		deleteClusterOperator(ctx, cl)

		infra := &configv1.Infrastructure{ObjectMeta: metav1.ObjectMeta{Name: infrastructureResourceName}}
		cl.Delete(ctx, infra) //nolint:errcheck
	})

	It("terminal error via finalizeReconcile should set OperatorDegraded=True immediately and return nil error", func() {
		reconciler := &CloudOperatorReconciler{
			ClusterOperatorStatusClient: ClusterOperatorStatusClient{
				Client:           cl,
				Clock:            clocktesting.NewFakePassiveClock(time.Now()),
				ManagedNamespace: defaultManagementNamespace,
				Recorder:         record.NewFakeRecorder(32),
			},
			Scheme:     scheme.Scheme,
			ImagesFile: "/nonexistent/images.json",
		}

		infra := &configv1.Infrastructure{
			ObjectMeta: metav1.ObjectMeta{Name: infrastructureResourceName},
		}
		Expect(cl.Create(ctx, infra)).To(Succeed())
		infra.Status = configv1.InfrastructureStatus{
			PlatformStatus:         &configv1.PlatformStatus{Type: configv1.AWSPlatformType},
			Platform:               configv1.AWSPlatformType,
			InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
			ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
		}
		Expect(cl.Status().Update(ctx, infra)).To(Succeed())

		co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: clusterOperatorName}}
		Expect(cl.Create(ctx, co)).To(Succeed())
		co.Status.Conditions = []configv1.ClusterOperatorStatusCondition{
			{Type: cloudConfigControllerAvailableCondition, Status: configv1.ConditionTrue, LastTransitionTime: metav1.Now()},
			{Type: cloudConfigControllerDegradedCondition, Status: configv1.ConditionFalse, LastTransitionTime: metav1.Now()},
			{Type: trustedCABundleControllerAvailableCondition, Status: configv1.ConditionTrue, LastTransitionTime: metav1.Now()},
			{Type: trustedCABundleControllerDegradedCondition, Status: configv1.ConditionFalse, LastTransitionTime: metav1.Now()},
		}
		Expect(cl.Status().Update(ctx, co)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{})
		Expect(err).NotTo(HaveOccurred(), "terminal errors should return nil so controller-runtime does not requeue")

		Expect(cl.Get(ctx, client.ObjectKey{Name: clusterOperatorName}, co)).To(Succeed())
		Expect(v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorDegraded).Status).To(Equal(configv1.ConditionTrue))
	})

	It("transient error via finalizeReconcile should not degrade before threshold, but degrade after threshold", func() {
		fakeClock := clocktesting.NewFakeClock(time.Now())
		reconciler := &CloudOperatorReconciler{
			ClusterOperatorStatusClient: ClusterOperatorStatusClient{
				Client:           cl,
				Clock:            fakeClock,
				ManagedNamespace: defaultManagementNamespace,
				Recorder:         record.NewFakeRecorder(32),
			},
			Scheme: scheme.Scheme,
		}

		co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: clusterOperatorName}}
		Expect(cl.Create(ctx, co)).To(Succeed())

		transientErr := fmt.Errorf("test transient error")
		conditionOverrides := []configv1.ClusterOperatorStatusCondition{}

		// Simulate what Reconcile's defer does: call finalizeReconcile with
		// the same closure that Reconcile constructs.
		callFinalizeReconcile := func(retErr error) (ctrl.Result, error) {
			result := ctrl.Result{}
			finalizeReconcile(
				&reconciler.failures, reconciler.Clock,
				noStalenessWindow, aggregatedTransientDegradedThreshold,
				"CloudOperatorReconciler",
				reconciler.clearFailureWindow,
				func() error { return reconciler.setStatusDegraded(ctx, retErr, conditionOverrides) },
				&result, &retErr,
			)
			return result, retErr
		}

		// First call: transient failure starts; error returned but no OperatorDegraded set.
		_, err := callFinalizeReconcile(transientErr)
		Expect(err).To(HaveOccurred())
		Expect(cl.Get(ctx, client.ObjectKey{Name: clusterOperatorName}, co)).To(Succeed())
		Expect(v1helpers.IsStatusConditionTrue(co.Status.Conditions, configv1.OperatorDegraded)).To(BeFalse(),
			"should not be degraded before threshold")

		// Advance clock past the degraded threshold.
		fakeClock.Step(aggregatedTransientDegradedThreshold + time.Second)

		// Second call: threshold exceeded, controller sets OperatorDegraded.
		_, err = callFinalizeReconcile(transientErr)
		Expect(err).To(HaveOccurred())
		Expect(cl.Get(ctx, client.ObjectKey{Name: clusterOperatorName}, co)).To(Succeed())
		Expect(v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorDegraded).Status).To(Equal(configv1.ConditionTrue))
	})

	It("transient error should not set OperatorDegraded before threshold", func() {
		fakeClock := clocktesting.NewFakeClock(time.Now())
		getErr := fmt.Errorf("connection refused")
		faultyClient := errorInjectingClient{Client: cl, getErr: &getErr, failType: &configv1.Infrastructure{}}

		co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: clusterOperatorName}}
		Expect(cl.Create(ctx, co)).To(Succeed())

		reconciler := &CloudOperatorReconciler{
			ClusterOperatorStatusClient: ClusterOperatorStatusClient{
				Client:           &faultyClient,
				Clock:            fakeClock,
				ManagedNamespace: defaultManagementNamespace,
				Recorder:         record.NewFakeRecorder(32),
			},
			Scheme: scheme.Scheme,
		}

		// Reconcile several times, advancing the clock each iteration but
		// staying under the threshold. OperatorDegraded must never be set.
		stepSize := aggregatedTransientDegradedThreshold / 5
		for range 4 {
			_, err := reconciler.Reconcile(ctx, reconcile.Request{})
			Expect(err).To(HaveOccurred(), "transient error should be returned for controller-runtime to requeue")
			fakeClock.Step(stepSize)
		}

		Expect(cl.Get(ctx, client.ObjectKey{Name: clusterOperatorName}, co)).To(Succeed())
		Expect(v1helpers.IsStatusConditionTrue(co.Status.Conditions, configv1.OperatorDegraded)).To(BeFalse(),
			"OperatorDegraded should not be True before the transient threshold elapses")
	})

	It("transient error should set OperatorDegraded after threshold is crossed", func() {
		fakeClock := clocktesting.NewFakeClock(time.Now())
		getErr := fmt.Errorf("connection refused")
		faultyClient := errorInjectingClient{Client: cl, getErr: &getErr, failType: &configv1.Infrastructure{}}

		co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: clusterOperatorName}}
		Expect(cl.Create(ctx, co)).To(Succeed())

		reconciler := &CloudOperatorReconciler{
			ClusterOperatorStatusClient: ClusterOperatorStatusClient{
				Client:           &faultyClient,
				Clock:            fakeClock,
				ManagedNamespace: defaultManagementNamespace,
				Recorder:         record.NewFakeRecorder(32),
			},
			Scheme: scheme.Scheme,
		}

		// First Reconcile opens the failure window.
		_, err := reconciler.Reconcile(ctx, reconcile.Request{})
		Expect(err).To(HaveOccurred())

		// Advance past the threshold.
		fakeClock.Step(aggregatedTransientDegradedThreshold + time.Second)

		// Next Reconcile crosses the threshold and sets OperatorDegraded.
		_, err = reconciler.Reconcile(ctx, reconcile.Request{})
		Expect(err).To(HaveOccurred())

		Expect(cl.Get(ctx, client.ObjectKey{Name: clusterOperatorName}, co)).To(Succeed())
		degradedCond := v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorDegraded)
		Expect(degradedCond).NotTo(BeNil(), "OperatorDegraded condition should exist after threshold is crossed")
		Expect(degradedCond.Status).To(Equal(configv1.ConditionTrue),
			"OperatorDegraded should be True after threshold is crossed")
	})

	It("successful reconcile resets the failure window so transient errors must start fresh", func() {
		fakeClock := clocktesting.NewFakeClock(time.Now())
		getErr := fmt.Errorf("connection refused")
		faultyClient := errorInjectingClient{Client: cl, getErr: &getErr, failType: &configv1.Infrastructure{}}

		co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: clusterOperatorName}}
		Expect(cl.Create(ctx, co)).To(Succeed())

		reconciler := &CloudOperatorReconciler{
			ClusterOperatorStatusClient: ClusterOperatorStatusClient{
				Client:           &faultyClient,
				Clock:            fakeClock,
				ManagedNamespace: defaultManagementNamespace,
				Recorder:         record.NewFakeRecorder(32),
			},
			Scheme: scheme.Scheme,
		}

		By("opening the failure window with a transient error")
		_, err := reconciler.Reconcile(ctx, reconcile.Request{})
		Expect(err).To(HaveOccurred())

		By("clearing the fault so Reconcile succeeds via the Infrastructure-not-found path")
		getErr = nil
		_, err = reconciler.Reconcile(ctx, reconcile.Request{})
		Expect(err).NotTo(HaveOccurred())

		By("advancing past the original threshold and re-injecting the fault")
		fakeClock.Step(aggregatedTransientDegradedThreshold + time.Second)
		getErr = fmt.Errorf("connection refused")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{})
		Expect(err).To(HaveOccurred())

		Expect(cl.Get(ctx, client.ObjectKey{Name: clusterOperatorName}, co)).To(Succeed())
		Expect(v1helpers.IsStatusConditionTrue(co.Status.Conditions, configv1.OperatorDegraded)).To(BeFalse(),
			"OperatorDegraded should not be True because the failure window was reset by the successful reconcile")
	})

	It("successful finalizeReconcile clears the failure window so subsequent transient errors start fresh", func() {
		fakeClock := clocktesting.NewFakeClock(time.Now())
		reconciler := &CloudOperatorReconciler{
			ClusterOperatorStatusClient: ClusterOperatorStatusClient{
				Client:           cl,
				Clock:            fakeClock,
				ManagedNamespace: defaultManagementNamespace,
				Recorder:         record.NewFakeRecorder(32),
			},
			Scheme: scheme.Scheme,
		}

		co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: clusterOperatorName}}
		Expect(cl.Create(ctx, co)).To(Succeed())

		transientErr := fmt.Errorf("test transient error")
		conditionOverrides := []configv1.ClusterOperatorStatusCondition{}

		callFinalizeReconcile := func(retErr error) (ctrl.Result, error) {
			result := ctrl.Result{}
			finalizeReconcile(
				&reconciler.failures, reconciler.Clock,
				noStalenessWindow, aggregatedTransientDegradedThreshold,
				"CloudOperatorReconciler",
				reconciler.clearFailureWindow,
				func() error { return reconciler.setStatusDegraded(ctx, retErr, conditionOverrides) },
				&result, &retErr,
			)
			return result, retErr
		}

		// Open the failure window with a transient error.
		_, err := callFinalizeReconcile(transientErr)
		Expect(err).To(HaveOccurred())

		// Simulate a successful reconcile: nil error clears the window.
		_, err = callFinalizeReconcile(nil)
		Expect(err).NotTo(HaveOccurred())

		// Advance clock past the original threshold.
		fakeClock.Step(aggregatedTransientDegradedThreshold + time.Second)

		// Despite the clock being past threshold, the window was cleared
		// by the successful reconcile — this starts a fresh window.
		_, err = callFinalizeReconcile(transientErr)
		Expect(err).To(HaveOccurred())
		Expect(cl.Get(ctx, client.ObjectKey{Name: clusterOperatorName}, co)).To(Succeed())
		Expect(v1helpers.IsStatusConditionTrue(co.Status.Conditions, configv1.OperatorDegraded)).To(BeFalse(),
			"should not be degraded because the failure window was reset by the successful reconcile")
	})
})

var _ = Describe("Reconcile progressing flow", func() {
	ctx := context.Background()
	var reconciler *CloudOperatorReconciler
	var operandResources []client.Object

	BeforeEach(func() {
		c, err := cache.New(cfg, cache.Options{})
		Expect(err).To(Succeed())

		w, err := NewObjectWatcher(WatcherOptions{Cache: c})
		Expect(err).To(Succeed())

		reconciler = &CloudOperatorReconciler{
			ClusterOperatorStatusClient: ClusterOperatorStatusClient{
				Client:           cl,
				Clock:            clocktesting.NewFakePassiveClock(time.Now()),
				ManagedNamespace: DefaultManagedNamespace,
				Recorder:         record.NewFakeRecorder(32),
			},
			Scheme:     scheme.Scheme,
			watcher:    w,
			ImagesFile: testImagesFilePath,
		}

		infra := &configv1.Infrastructure{
			ObjectMeta: metav1.ObjectMeta{Name: infrastructureResourceName},
		}
		Expect(cl.Create(ctx, infra)).To(Succeed())

		infra.Status = configv1.InfrastructureStatus{
			PlatformStatus:         &configv1.PlatformStatus{Type: configv1.AWSPlatformType},
			Platform:               configv1.AWSPlatformType,
			InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
			ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
		}
		Expect(cl.Status().Update(ctx, infra)).To(Succeed())

		co := &configv1.ClusterOperator{ObjectMeta: metav1.ObjectMeta{Name: clusterOperatorName}}
		Expect(cl.Create(ctx, co)).To(Succeed())

		co.Status.Conditions = []configv1.ClusterOperatorStatusCondition{
			{Type: cloudConfigControllerAvailableCondition, Status: configv1.ConditionTrue, LastTransitionTime: metav1.Now()},
			{Type: cloudConfigControllerDegradedCondition, Status: configv1.ConditionFalse, LastTransitionTime: metav1.Now()},
			{Type: trustedCABundleControllerAvailableCondition, Status: configv1.ConditionTrue, LastTransitionTime: metav1.Now()},
			{Type: trustedCABundleControllerDegradedCondition, Status: configv1.ConditionFalse, LastTransitionTime: metav1.Now()},
		}
		Expect(cl.Status().Update(ctx, co)).To(Succeed())
	})

	AfterEach(func() {
		deleteClusterOperator(ctx, cl)

		infra := &configv1.Infrastructure{ObjectMeta: metav1.ObjectMeta{Name: infrastructureResourceName}}
		cl.Delete(ctx, infra) //nolint:errcheck

		for _, res := range operandResources {
			cl.Delete(ctx, res) //nolint:errcheck
			Eventually(func() error {
				err := cl.Get(ctx, client.ObjectKeyFromObject(res), res)
				if apierrors.IsNotFound(err) {
					return nil
				}
				if err != nil {
					return err
				}
				return fmt.Errorf("expected operand %s to be deleted", res.GetName())
			}, timeout).Should(Succeed())
		}
	})

	It("sets Progressing=True on first reconcile, stays progressing until rollout completes, then Available", func() {
		// First Reconcile: resources are created, Progressing=True, Available not set.
		_, err := reconciler.Reconcile(ctx, reconcile.Request{})
		Expect(err).NotTo(HaveOccurred())

		// Track created resources for cleanup
		operatorConfig := config.OperatorConfig{
			ManagedNamespace: DefaultManagedNamespace,
			ImagesReference: config.ImagesReference{
				CloudControllerManagerOperator: "registry.ci.openshift.org/openshift:cluster-cloud-controller-manager-operator",
				CloudControllerManagerAWS:      "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
			},
			PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType},
		}
		operandResources, err = cloud.GetResources(operatorConfig)
		Expect(err).NotTo(HaveOccurred())

		co := &configv1.ClusterOperator{}
		Expect(cl.Get(ctx, client.ObjectKey{Name: clusterOperatorName}, co)).To(Succeed())
		progressingCond := v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorProgressing)
		Expect(progressingCond).NotTo(BeNil(), "Progressing condition should exist after first reconcile")
		Expect(progressingCond.Status).To(
			Equal(configv1.ConditionTrue), "Progressing should be True after first reconcile creates resources",
		)
		availCond := v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorAvailable)
		if availCond != nil {
			Expect(availCond.Status).NotTo(Equal(configv1.ConditionTrue),
				"Available should not be True while progressing")
		}

		// Second Reconcile: nothing changed, but deployment rollout is not complete
		// (envtest has no deployment controller). Progressing should remain True.
		_, err = reconciler.Reconcile(ctx, reconcile.Request{})
		Expect(err).NotTo(HaveOccurred())

		Expect(cl.Get(ctx, client.ObjectKey{Name: clusterOperatorName}, co)).To(Succeed())
		progressingCond = v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorProgressing)
		Expect(progressingCond).NotTo(BeNil(), "Progressing condition should exist after second reconcile")
		Expect(progressingCond.Status).To(
			Equal(configv1.ConditionTrue), "Progressing should remain True while deployment rollout is incomplete",
		)

		// Patch deployment status to mark rollout complete.
		for _, res := range operandResources {
			if dep, ok := res.(*appsv1.Deployment); ok {
				live := &appsv1.Deployment{}
				Expect(cl.Get(ctx, client.ObjectKeyFromObject(dep), live)).To(Succeed())
				live.Status.ObservedGeneration = live.Generation
				live.Status.Conditions = []appsv1.DeploymentCondition{
					{
						Type:   appsv1.DeploymentProgressing,
						Status: corev1.ConditionTrue,
						Reason: "NewReplicaSetAvailable",
					},
				}
				Expect(cl.Status().Update(ctx, live)).To(Succeed())
			}
		}

		// Third Reconcile: deployment rollout is complete, Progressing=False, Available=True.
		_, err = reconciler.Reconcile(ctx, reconcile.Request{})
		Expect(err).NotTo(HaveOccurred())

		Expect(cl.Get(ctx, client.ObjectKey{Name: clusterOperatorName}, co)).To(Succeed())
		progressingCond = v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorProgressing)
		Expect(progressingCond).NotTo(BeNil(), "Progressing condition should exist after third reconcile")
		Expect(progressingCond.Status).To(
			Equal(configv1.ConditionFalse), "Progressing should be False after deployment rollout completes",
		)
		availCond = v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorAvailable)
		Expect(availCond).NotTo(BeNil(), "Available condition should exist after third reconcile")
		Expect(availCond.Status).To(
			Equal(configv1.ConditionTrue), "Available should be True after deployment rollout completes",
		)
	})
})

var _ = Describe("Rollout completion checks", func() {
	Context("isDeploymentRolloutComplete", func() {
		It("is not complete when no conditions exist", func() {
			deploy := &appsv1.Deployment{}
			Expect(isDeploymentRolloutComplete(deploy)).To(BeFalse())
		})

		It("is complete when ObservedGeneration matches and Reason=NewReplicaSetAvailable", func() {
			deploy := &appsv1.Deployment{}
			deploy.Generation = 2
			deploy.Status = appsv1.DeploymentStatus{
				ObservedGeneration: 2,
				Conditions: []appsv1.DeploymentCondition{
					{
						Type:   appsv1.DeploymentProgressing,
						Status: corev1.ConditionTrue,
						Reason: "NewReplicaSetAvailable",
					},
				},
			}
			Expect(isDeploymentRolloutComplete(deploy)).To(BeTrue())
		})

		It("is not complete when ObservedGeneration lags behind Generation", func() {
			deploy := &appsv1.Deployment{}
			deploy.Generation = 3
			deploy.Status = appsv1.DeploymentStatus{
				ObservedGeneration: 2,
				Conditions: []appsv1.DeploymentCondition{
					{
						Type:   appsv1.DeploymentProgressing,
						Status: corev1.ConditionTrue,
						Reason: "NewReplicaSetAvailable",
					},
				},
			}
			Expect(isDeploymentRolloutComplete(deploy)).To(BeFalse())
		})

		It("is not complete when Progressing=True with Reason=ReplicaSetUpdated", func() {
			deploy := &appsv1.Deployment{}
			deploy.Generation = 1
			deploy.Status = appsv1.DeploymentStatus{
				ObservedGeneration: 1,
				Conditions: []appsv1.DeploymentCondition{
					{
						Type:   appsv1.DeploymentProgressing,
						Status: corev1.ConditionTrue,
						Reason: "ReplicaSetUpdated",
					},
				},
			}
			Expect(isDeploymentRolloutComplete(deploy)).To(BeFalse())
		})

		It("is not complete when Progressing=False due to deadline exceeded", func() {
			deploy := &appsv1.Deployment{}
			deploy.Generation = 1
			deploy.Status = appsv1.DeploymentStatus{
				ObservedGeneration: 1,
				Conditions: []appsv1.DeploymentCondition{
					{
						Type:   appsv1.DeploymentProgressing,
						Status: corev1.ConditionFalse,
						Reason: "ProgressDeadlineExceeded",
					},
				},
			}
			Expect(isDeploymentRolloutComplete(deploy)).To(BeFalse())
		})

		It("is not complete when Progressing=True with Reason=FoundNewReplicaSet", func() {
			deploy := &appsv1.Deployment{}
			deploy.Generation = 1
			deploy.Status = appsv1.DeploymentStatus{
				ObservedGeneration: 1,
				Conditions: []appsv1.DeploymentCondition{
					{
						Type:   appsv1.DeploymentProgressing,
						Status: corev1.ConditionTrue,
						Reason: "FoundNewReplicaSet",
					},
				},
			}
			Expect(isDeploymentRolloutComplete(deploy)).To(BeFalse())
		})

		It("keys off Progressing condition even when other conditions are present", func() {
			deploy := &appsv1.Deployment{}
			deploy.Generation = 1
			deploy.Status = appsv1.DeploymentStatus{
				ObservedGeneration: 1,
				Conditions: []appsv1.DeploymentCondition{
					{
						Type:   appsv1.DeploymentAvailable,
						Status: corev1.ConditionTrue,
						Reason: "MinimumReplicasAvailable",
					},
					{
						Type:   appsv1.DeploymentProgressing,
						Status: corev1.ConditionTrue,
						Reason: "NewReplicaSetAvailable",
					},
				},
			}
			Expect(isDeploymentRolloutComplete(deploy)).To(BeTrue())
		})
	})

	Context("isDeploymentRolloutStalled", func() {
		It("is stalled when ProgressDeadlineExceeded", func() {
			deploy := &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentProgressing,
							Status: corev1.ConditionFalse,
							Reason: "ProgressDeadlineExceeded",
						},
					},
				},
			}
			Expect(isDeploymentRolloutStalled(deploy)).To(BeTrue())
		})

		It("is not stalled during normal rollout", func() {
			deploy := &appsv1.Deployment{
				Status: appsv1.DeploymentStatus{
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentProgressing,
							Status: corev1.ConditionTrue,
							Reason: "ReplicaSetUpdated",
						},
					},
				},
			}
			Expect(isDeploymentRolloutStalled(deploy)).To(BeFalse())
		})

		It("is not stalled when no conditions exist", func() {
			deploy := &appsv1.Deployment{}
			Expect(isDeploymentRolloutStalled(deploy)).To(BeFalse())
		})

		It("ignores stale ProgressDeadlineExceeded from a previous generation", func() {
			deploy := &appsv1.Deployment{}
			deploy.Generation = 2
			deploy.Status = appsv1.DeploymentStatus{
				ObservedGeneration: 1,
				Conditions: []appsv1.DeploymentCondition{
					{
						Type:   appsv1.DeploymentProgressing,
						Status: corev1.ConditionFalse,
						Reason: "ProgressDeadlineExceeded",
					},
				},
			}
			Expect(isDeploymentRolloutStalled(deploy)).To(BeFalse())
		})
	})

	Context("isDaemonSetRolloutComplete", func() {
		It("is not complete when UpdatedNumberScheduled < CurrentNumberScheduled", func() {
			ds := &appsv1.DaemonSet{}
			ds.Generation = 1
			ds.Status = appsv1.DaemonSetStatus{
				ObservedGeneration:     1,
				DesiredNumberScheduled: 3,
				CurrentNumberScheduled: 3,
				UpdatedNumberScheduled: 1,
			}
			Expect(isDaemonSetRolloutComplete(ds)).To(BeFalse())
		})

		It("is not complete when ObservedGeneration lags behind Generation", func() {
			ds := &appsv1.DaemonSet{}
			ds.Generation = 2
			ds.Status = appsv1.DaemonSetStatus{
				ObservedGeneration:     1,
				DesiredNumberScheduled: 3,
				CurrentNumberScheduled: 3,
				UpdatedNumberScheduled: 3,
			}
			Expect(isDaemonSetRolloutComplete(ds)).To(BeFalse())
		})

		It("is complete when generation and updated counts match", func() {
			ds := &appsv1.DaemonSet{}
			ds.Generation = 2
			ds.Status = appsv1.DaemonSetStatus{
				ObservedGeneration:     2,
				DesiredNumberScheduled: 3,
				CurrentNumberScheduled: 3,
				UpdatedNumberScheduled: 3,
			}
			Expect(isDaemonSetRolloutComplete(ds)).To(BeTrue())
		})

		// NumberUnavailable and NumberMisscheduled are deliberately not checked.
		// During MCO-driven node reboots, DaemonSet pods are evicted and
		// rescheduled without a spec change. The pod hash is unchanged so
		// UpdatedNumberScheduled stays at CurrentNumberScheduled and
		// ObservedGeneration == Generation. Only NumberUnavailable bumps
		// transiently. Treating that as "not complete" would cause
		// Progressing=True flapping during upgrades (run level ~29 CCCMO
		// re-entering Progressing while run level ~90 MCO rolls nodes).
		It("is complete despite NumberUnavailable > 0 when generation and updated counts match (node reboot)", func() {
			ds := &appsv1.DaemonSet{}
			ds.Generation = 1
			ds.Status = appsv1.DaemonSetStatus{
				ObservedGeneration:     1,
				DesiredNumberScheduled: 3,
				CurrentNumberScheduled: 3,
				UpdatedNumberScheduled: 3,
				NumberUnavailable:      1,
			}
			Expect(isDaemonSetRolloutComplete(ds)).To(BeTrue(), "node reboot should not prevent rollout from being considered complete")
		})

		It("is complete despite NumberMisscheduled > 0 when generation and updated counts match", func() {
			ds := &appsv1.DaemonSet{}
			ds.Generation = 1
			ds.Status = appsv1.DaemonSetStatus{
				ObservedGeneration:     1,
				DesiredNumberScheduled: 3,
				CurrentNumberScheduled: 3,
				UpdatedNumberScheduled: 3,
				NumberMisscheduled:     1,
			}
			Expect(isDaemonSetRolloutComplete(ds)).To(BeTrue(), "misscheduled pods are a scheduling concern, not a rollout concern")
		})

		// OCPBUGS-98617: MachineSet scale-up increases DesiredNumberScheduled
		// before pods are created on new nodes. Comparing against
		// DesiredNumberScheduled caused Progressing=True during scaling.
		// Per API convention, operators must not report Progressing due to
		// DaemonSets adjusting to new nodes from cluster scale-up.
		It("is complete during node scale-up when all existing pods are updated (OCPBUGS-98617)", func() {
			ds := &appsv1.DaemonSet{}
			ds.Generation = 1
			ds.Status = appsv1.DaemonSetStatus{
				ObservedGeneration:     1,
				DesiredNumberScheduled: 5,
				CurrentNumberScheduled: 3,
				UpdatedNumberScheduled: 3,
			}
			Expect(isDaemonSetRolloutComplete(ds)).To(BeTrue(),
				"new nodes without pods yet should not cause Progressing=True")
		})

		It("is not complete when no pods have been scheduled yet (initial deploy)", func() {
			ds := &appsv1.DaemonSet{}
			ds.Generation = 1
			ds.Status = appsv1.DaemonSetStatus{
				ObservedGeneration:     1,
				DesiredNumberScheduled: 3,
				CurrentNumberScheduled: 0,
				UpdatedNumberScheduled: 0,
			}
			Expect(isDaemonSetRolloutComplete(ds)).To(BeFalse(),
				"zero pods scheduled means rollout has not started")
		})
	})
})
