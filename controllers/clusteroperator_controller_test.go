package controllers

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/ginkgo/extensions/table"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud"
	openstack "github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/openstack"
	"github.com/openshift/library-go/pkg/cloudprovider"
	"github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	timeout                 = time.Second * 10
	testManagementNamespace = "openshift-cloud-controller-manager"
	testImagesFilePath      = "../hack/example-images.json"
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

var _ = Describe("Component sync controller", func() {
	var infra *configv1.Infrastructure
	var fg *configv1.FeatureGate
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

	BeforeEach(func() {
		c, err := cache.New(cfg, cache.Options{})
		Expect(err).To(Succeed())
		w, err := NewObjectWatcher(WatcherOptions{Cache: c})
		Expect(err).To(Succeed())

		infra = &configv1.Infrastructure{}
		infra.SetName(infrastructureName)

		fg = &configv1.FeatureGate{}
		fg.SetName(externalFeatureGateName)

		operands = nil

		operatorController = &CloudOperatorReconciler{
			Client:           cl,
			Scheme:           scheme.Scheme,
			watcher:          w,
			ManagedNamespace: testManagementNamespace,
			ImagesFile:       testImagesFilePath,
			Recorder:         record.NewFakeRecorder(32),
		}
		originalWatcher, _ := w.(*objectWatcher)
		watcher = mockedWatcher{watcher: originalWatcher}

		Expect(cl.Create(context.Background(), infra.DeepCopy())).To(Succeed())
		Expect(cl.Create(context.Background(), fg.DeepCopy())).To(Succeed())
	})

	AfterEach(func() {
		Expect(cl.Delete(context.Background(), infra.DeepCopy())).To(Succeed())
		Expect(cl.Delete(context.Background(), fg.DeepCopy())).To(Succeed())

		Eventually(func() bool {
			return apierrors.IsNotFound(cl.Get(context.Background(), client.ObjectKeyFromObject(infra), infra.DeepCopy())) &&
				apierrors.IsNotFound(cl.Get(context.Background(), client.ObjectKeyFromObject(fg), fg.DeepCopy()))
		}, timeout).Should(BeTrue())

		for _, operand := range operands {
			Expect(cl.Delete(context.Background(), operand)).To(Succeed())

			Eventually(func() bool {
				return apierrors.IsNotFound(cl.Get(context.Background(), client.ObjectKeyFromObject(operand), operand))
			}, timeout).Should(BeTrue())
		}
	})

	type testCase struct {
		status          *configv1.InfrastructureStatus
		featureGateSpec *configv1.FeatureGateSpec
		config          operatorConfig
		expected        []client.Object
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

			_, err := operatorController.Reconcile(context.Background(), reconcile.Request{})
			Expect(err).To(Succeed())

			watchMap := watcher.getWatchedResources()

			operands = fillConfigValues(tc.config, tc.expected)
			for _, obj := range operands {
				Expect(watchMap[obj.GetName()]).ToNot(BeNil())

				original, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj.DeepCopyObject())
				Expect(err).To(Succeed())

				// Purge fields which are only required by SSA
				delete(original, "kind")
				delete(original, "apiVersion")

				Expect(cl.Get(context.Background(), client.ObjectKeyFromObject(obj), obj)).To(Succeed())
				applied, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj.DeepCopyObject())
				Expect(err).To(Succeed())

				// Enforsed fields should be equal
				Expect(equality.Semantic.DeepDerivative(original, applied)).To(BeTrue())
			}
		},
		Entry("Should provision AWS resources", testCase{
			status: &configv1.InfrastructureStatus{
				Platform: configv1.AWSPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.AWSPlatformType,
				},
			},
			featureGateSpec: externalFeatureGateSpec,
			config: operatorConfig{
				ManagedNamespace: testManagementNamespace,
				ControllerImage:  "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
			},
			expected: cloud.GetAWSResources(),
		}),
		Entry("Should provision OpenStack resources", testCase{
			status: &configv1.InfrastructureStatus{
				Platform: configv1.OpenStackPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.OpenStackPlatformType,
				},
			},
			config: operatorConfig{
				ManagedNamespace: testManagementNamespace,
				ControllerImage:  "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager",
			},
			featureGateSpec: externalFeatureGateSpec,
			expected:        openstack.GetResources(),
		}),
		Entry("Should not provision resources for currently unsupported platform", testCase{
			status: &configv1.InfrastructureStatus{
				Platform: configv1.IBMCloudPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.IBMCloudPlatformType,
				},
			},
			featureGateSpec: externalFeatureGateSpec,
		}),
		Entry("Should not provision resources for AWS if external FeatureGate is not present", testCase{
			status: &configv1.InfrastructureStatus{
				Platform: configv1.AWSPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.AWSPlatformType,
				},
			},
		}),
		Entry("Should not provision resources for OpenStack if external FeatureGate is not present", testCase{
			status: &configv1.InfrastructureStatus{
				Platform: configv1.OpenStackPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Type: configv1.OpenStackPlatformType,
				},
			},
		}),
	)
})
