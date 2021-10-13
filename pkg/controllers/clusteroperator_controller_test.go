package controllers

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/ginkgo/extensions/table"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/library-go/pkg/cloudprovider"
	"github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
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
			Client:           cl,
			Scheme:           scheme.Scheme,
			ManagedNamespace: defaultManagementNamespace,
			Recorder:         record.NewFakeRecorder(32),
		}
		operator = &configv1.ClusterOperator{}
		operator.SetName(clusterOperatorName)
	})

	AfterEach(func() {
		co := &configv1.ClusterOperator{}
		err := cl.Get(context.Background(), client.ObjectKey{Name: clusterOperatorName}, co)
		if err == nil || !apierrors.IsNotFound(err) {
			Eventually(func() bool {
				err := cl.Delete(context.Background(), operator)
				return err == nil || apierrors.IsNotFound(err)
			}).Should(BeTrue())
		}
		Eventually(apierrors.IsNotFound(cl.Get(context.Background(), client.ObjectKey{Name: clusterOperatorName}, co))).Should(BeTrue())
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
			Eventually(func() (bool, error) {
				err := cl.Get(context.Background(), client.ObjectKey{Name: clusterOperatorName}, getOp)
				if err != nil {
					return false, err
				}
				// Successful sync means CO exists and the status is not empty
				return getOp != nil && len(getOp.Status.Versions) > 0, nil
			}, timeout).Should(BeTrue())

			// check version.
			Expect(getOp.Status.Versions).To(HaveLen(1))
			Expect(getOp.Status.Versions[0].Name).To(Equal(operatorVersionKey))
			Expect(getOp.Status.Versions[0].Version).To(Equal(expectedVersion))

			// check conditions.
			Expect(v1helpers.IsStatusConditionTrue(getOp.Status.Conditions, configv1.OperatorAvailable)).To(BeTrue())
			Expect(v1helpers.FindStatusCondition(getOp.Status.Conditions, configv1.OperatorAvailable).Reason).To(Equal(ReasonAsExpected))
			Expect(v1helpers.IsStatusConditionTrue(getOp.Status.Conditions, configv1.OperatorUpgradeable)).To(BeTrue())
			Expect(v1helpers.FindStatusCondition(getOp.Status.Conditions, configv1.OperatorUpgradeable).Reason).To(Equal(ReasonAsExpected))
			Expect(v1helpers.IsStatusConditionFalse(getOp.Status.Conditions, configv1.OperatorDegraded)).To(BeTrue())
			Expect(v1helpers.FindStatusCondition(getOp.Status.Conditions, configv1.OperatorDegraded).Reason).To(Equal(ReasonAsExpected))
			Expect(v1helpers.IsStatusConditionFalse(getOp.Status.Conditions, configv1.OperatorProgressing)).To(BeTrue())
			Expect(v1helpers.FindStatusCondition(getOp.Status.Conditions, configv1.OperatorProgressing).Reason).To(Equal(ReasonAsExpected))

			// check related objects.
			Expect(getOp.Status.RelatedObjects).To(Equal(operatorController.relatedObjects()))
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
		Expect(toClusterOperator(object)).To(Equal(requests))
	})
})

type mockedWatcher struct {
	watcher *objectWatcher
}

func (m *mockedWatcher) getWatchedResources() map[string]struct{} {
	return m.watcher.watchedResources
}

// pkg/controllers/cache.go:94
func constructKeyForWatchedObject(object client.Object, scheme *runtime.Scheme) (string, error) {
	gvk, err := apiutil.GVKForObject(object, scheme)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s", gvk.GroupKind().String(), object.GetName()), nil
}

var _ = Describe("Component sync controller", func() {
	var infra *configv1.Infrastructure
	var fg *configv1.FeatureGate
	var kcm *operatorv1.KubeControllerManager
	var co *configv1.ClusterOperator
	var operatorController *CloudOperatorReconciler
	var operands []client.Object
	var watcher mockedWatcher

	externalFeatureGateSpec := &configv1.FeatureGateSpec{
		FeatureGateSelection: configv1.FeatureGateSelection{
			FeatureSet: configv1.CustomNoUpgrade,
			CustomNoUpgrade: &configv1.CustomFeatureGates{
				Enabled: []string{cloudprovider.ExternalCloudProviderFeature},
			},
		},
	}

	kcmStatus := &operatorv1.KubeControllerManagerStatus{
		StaticPodOperatorStatus: operatorv1.StaticPodOperatorStatus{
			OperatorStatus: operatorv1.OperatorStatus{
				Conditions: []operatorv1.OperatorCondition{
					{
						Type:               cloudControllerOwnershipCondition,
						Status:             operatorv1.ConditionFalse,
						LastTransitionTime: metav1.Now(),
					},
				},
			},
		},
	}

	getOperatorConfigForPlatform := func(status *configv1.PlatformStatus) config.OperatorConfig {
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

	BeforeEach(func() {
		c, err := cache.New(cfg, cache.Options{})
		Expect(err).To(Succeed())
		w, err := NewObjectWatcher(WatcherOptions{Cache: c})
		Expect(err).To(Succeed())

		infra = &configv1.Infrastructure{}
		infra.SetName(infrastructureResourceName)

		fg = &configv1.FeatureGate{}
		fg.SetName(externalFeatureGateName)

		kcm = &operatorv1.KubeControllerManager{
			ObjectMeta: metav1.ObjectMeta{
				Name: kcmResourceName,
			},
			Spec: operatorv1.KubeControllerManagerSpec{
				StaticPodOperatorSpec: operatorv1.StaticPodOperatorSpec{
					OperatorSpec: operatorv1.OperatorSpec{
						ManagementState: operatorv1.Managed,
					},
				},
			},
		}

		co = &configv1.ClusterOperator{}
		co.SetName(clusterOperatorName)

		operands = nil

		operatorController = &CloudOperatorReconciler{
			Client:           cl,
			Scheme:           scheme.Scheme,
			watcher:          w,
			ManagedNamespace: testManagedNamespace,
			ImagesFile:       testImagesFilePath,
			Recorder:         record.NewFakeRecorder(32),
		}
		originalWatcher, _ := w.(*objectWatcher)
		watcher = mockedWatcher{watcher: originalWatcher}

		Expect(cl.Create(context.Background(), infra.DeepCopy())).To(Succeed())
		Expect(cl.Create(context.Background(), fg.DeepCopy())).To(Succeed())
		Expect(cl.Create(context.Background(), kcm.DeepCopy())).To(Succeed())
		Expect(cl.Create(context.Background(), co.DeepCopy())).To(Succeed())
	})

	AfterEach(func() {
		Expect(cl.Delete(context.Background(), infra.DeepCopy())).To(Succeed())
		Expect(cl.Delete(context.Background(), fg.DeepCopy())).To(Succeed())
		Expect(cl.Delete(context.Background(), kcm.DeepCopy())).To(Succeed())
		Expect(cl.Delete(context.Background(), co.DeepCopy())).To(Succeed())

		Eventually(func() bool {
			return apierrors.IsNotFound(cl.Get(context.Background(), client.ObjectKeyFromObject(infra), infra.DeepCopy())) &&
				apierrors.IsNotFound(cl.Get(context.Background(), client.ObjectKeyFromObject(fg), fg.DeepCopy())) &&
				apierrors.IsNotFound(cl.Get(context.Background(), client.ObjectKeyFromObject(co), fg.DeepCopy())) &&
				apierrors.IsNotFound(cl.Get(context.Background(), client.ObjectKeyFromObject(kcm), kcm.DeepCopy()))
		}, timeout).Should(BeTrue())

		for _, operand := range operands {
			Expect(cl.Delete(context.Background(), operand)).To(Succeed())

			Eventually(func() bool {
				return apierrors.IsNotFound(cl.Get(context.Background(), client.ObjectKeyFromObject(operand), operand))
			}, timeout).Should(BeTrue())
		}
	})

	type testCase struct {
		status            *configv1.InfrastructureStatus
		featureGateSpec   *configv1.FeatureGateSpec
		kcmStatus         *operatorv1.KubeControllerManagerStatus
		expectProvisioned bool
	}

	DescribeTable("should ensure resources are provisioned",
		func(tc testCase) {
			Expect(cl.Get(context.Background(), client.ObjectKeyFromObject(infra), infra)).To(Succeed())
			infra.Status = *tc.status
			Expect(cl.Status().Update(context.Background(), infra.DeepCopy())).To(Succeed())

			if tc.featureGateSpec != nil {
				Expect(cl.Get(context.Background(), client.ObjectKeyFromObject(fg), fg)).To(Succeed())
				fg.Spec = *tc.featureGateSpec
				Expect(cl.Update(context.Background(), fg.DeepCopy())).To(Succeed())
			}

			if tc.kcmStatus != nil {
				Expect(cl.Get(context.Background(), client.ObjectKeyFromObject(kcm), kcm)).To(Succeed())
				kcm.Status = *tc.kcmStatus
				Expect(cl.Status().Update(context.Background(), kcm.DeepCopy())).To(Succeed())
			}

			_, err := operatorController.Reconcile(context.Background(), reconcile.Request{})
			Expect(err).To(Succeed())

			watchMap := watcher.getWatchedResources()

			operatorConfig := getOperatorConfigForPlatform(tc.status.PlatformStatus)

			clusterOperator, err := operatorController.getOrCreateClusterOperator(context.Background())
			Expect(err).To(Succeed())
			if tc.expectProvisioned == true {
				ownedByCCM := false
				for _, cond := range clusterOperator.Status.Conditions {
					if cond.Type == cloudControllerOwnershipCondition {
						ownedByCCM = cond.Status == configv1.ConditionTrue
					}
				}
				Expect(ownedByCCM).To(BeTrue())
			}

			if tc.expectProvisioned == false {
				Expect(len(watchMap)).To(BeZero())
				return
			}

			operands, err = cloud.GetResources(operatorConfig)
			Expect(err).To(Succeed())
			Expect(len(watchMap)).To(BeEquivalentTo(len(operands)))
			for _, obj := range operands {
				watchKey, err := constructKeyForWatchedObject(obj, operatorController.Scheme)
				Expect(err).To(Succeed())
				_, watchExists := watchMap[watchKey]
				Expect(watchExists).To(BeTrue())

				original, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj.DeepCopyObject())
				Expect(err).To(Succeed())

				// Purge fields which are only required by SSA
				delete(original, "kind")
				delete(original, "apiVersion")

				Expect(cl.Get(context.Background(), client.ObjectKeyFromObject(obj), obj)).To(Succeed())
				applied, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj.DeepCopyObject())
				Expect(err).To(Succeed())

				// Enforced fields should be equal
				Expect(equality.Semantic.DeepDerivative(original, applied)).To(BeTrue())
			}
		},
		Entry("Should provision AWS resources", testCase{
			status: &configv1.InfrastructureStatus{
				InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
				ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
				Platform:               configv1.AWSPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.AWSPlatformType,
				},
			},
			featureGateSpec:   externalFeatureGateSpec,
			kcmStatus:         kcmStatus,
			expectProvisioned: true,
		}),
		Entry("Should provision OpenStack resources", testCase{
			status: &configv1.InfrastructureStatus{
				InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
				ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
				Platform:               configv1.OpenStackPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.OpenStackPlatformType,
				},
			},
			featureGateSpec:   externalFeatureGateSpec,
			kcmStatus:         kcmStatus,
			expectProvisioned: true,
		}),
		Entry("Should provision resources if FG is set and KCM object doesn't exist", testCase{
			status: &configv1.InfrastructureStatus{
				InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
				ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
				Platform:               configv1.AWSPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.AWSPlatformType,
				},
			},
			featureGateSpec:   externalFeatureGateSpec,
			kcmStatus:         &operatorv1.KubeControllerManagerStatus{},
			expectProvisioned: true,
		}),
		Entry("Should not provision resources for currently unsupported platform", testCase{
			status: &configv1.InfrastructureStatus{
				InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
				ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
				Platform:               configv1.KubevirtPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.KubevirtPlatformType,
				},
			},
			featureGateSpec:   externalFeatureGateSpec,
			kcmStatus:         kcmStatus,
			expectProvisioned: false,
		}),
		Entry("Should not provision resources for AWS if external FeatureGate is not present", testCase{
			status: &configv1.InfrastructureStatus{
				InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
				ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
				Platform:               configv1.AWSPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.AWSPlatformType,
				},
			},
			kcmStatus:         kcmStatus,
			expectProvisioned: false,
		}),
		Entry("Should not provision resources for OpenStack if external FeatureGate is not present", testCase{
			status: &configv1.InfrastructureStatus{
				InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
				ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
				Platform:               configv1.OpenStackPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.OpenStackPlatformType,
				},
			},
			expectProvisioned: false,
		}),
		Entry("Should not provision resources because KCM still owns the controllers", testCase{
			status: &configv1.InfrastructureStatus{
				InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
				ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
				Platform:               configv1.AWSPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.AWSPlatformType,
				},
			},
			featureGateSpec: externalFeatureGateSpec,
			kcmStatus: &operatorv1.KubeControllerManagerStatus{
				StaticPodOperatorStatus: operatorv1.StaticPodOperatorStatus{
					OperatorStatus: operatorv1.OperatorStatus{
						Conditions: []operatorv1.OperatorCondition{
							{
								Type:               cloudControllerOwnershipCondition,
								Status:             operatorv1.ConditionTrue,
								LastTransitionTime: metav1.Now(),
							},
						},
					},
				},
			},
			expectProvisioned: false,
		}),
		Entry("Should not provision resources because KCMO hasn't set CloudControllerOwner condition", testCase{
			status: &configv1.InfrastructureStatus{
				InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
				ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
				Platform:               configv1.AWSPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.AWSPlatformType,
				},
			},
			featureGateSpec: externalFeatureGateSpec,
			kcmStatus: &operatorv1.KubeControllerManagerStatus{
				StaticPodOperatorStatus: operatorv1.StaticPodOperatorStatus{
					OperatorStatus: operatorv1.OperatorStatus{
						Conditions: []operatorv1.OperatorCondition{
							{
								Type:               "StaticPodsAvailable",
								Status:             operatorv1.ConditionTrue,
								LastTransitionTime: metav1.Now(),
							},
						},
					},
				},
			},
			expectProvisioned: false,
		}),
	)
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
		reconciler = &CloudOperatorReconciler{
			Client:   cl,
			Scheme:   scheme.Scheme,
			Recorder: recorder,
			watcher:  w,
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
		Expect(updated).To(BeTrue())
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Resource was successfully updated")))
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
		Expect(updated).To(BeTrue())
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Resource was successfully updated")))

		dep.Spec.Replicas = pointer.Int32Ptr(20)

		updated, err = reconciler.applyResources(context.TODO(), resources)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(updated).To(BeTrue())
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
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Update failed")))
	})

	It("Expect no update when resources are applied twice", func() {
		operatorConfig := getConfigForPlatform(&configv1.PlatformStatus{Type: configv1.AWSPlatformType})
		awsResources, err := cloud.GetResources(operatorConfig)
		Expect(err).To(Succeed())

		resources = append(resources, awsResources...)

		updated, err := reconciler.applyResources(context.TODO(), resources)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(updated).To(BeTrue())
		Eventually(recorder.Events).Should(Receive(ContainSubstring("Resource was successfully updated")))

		updated, err = reconciler.applyResources(context.TODO(), resources)
		Expect(err).ShouldNot(HaveOccurred())
		Expect(updated).To(BeFalse())
	})

	AfterEach(func() {
		for _, operand := range resources {
			Expect(cl.Delete(context.Background(), operand)).To(Succeed())

			Eventually(func() bool {
				return apierrors.IsNotFound(cl.Get(context.Background(), client.ObjectKeyFromObject(operand), operand))
			}, timeout).Should(BeTrue())
		}
		Consistently(recorder.Events).ShouldNot(Receive())
	})

})
