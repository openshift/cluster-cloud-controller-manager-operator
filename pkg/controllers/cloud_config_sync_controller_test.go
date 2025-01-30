package controllers

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	infraCloudConfName = "test-config"
	infraCloudConfKey  = "foo"

	defaultAzureConfig = `{"cloud":"AzurePublicCloud","tenantId":"0000000-0000-0000-0000-000000000000","Entries":null,"subscriptionId":"0000000-0000-0000-0000-000000000000","vmType":"standard","putVMSSVMBatchSize":0,"enableMigrateToIPBasedBackendPoolAPI":false,"clusterServiceLoadBalancerHealthProbeMode":"shared"}`
)

func makeInfrastructureResource(platform configv1.PlatformType) *configv1.Infrastructure {
	return &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: infrastructureResourceName,
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

func makeNetworkResource() *configv1.Network {
	return &configv1.Network{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
		Spec: configv1.NetworkSpec{
			NetworkType: string(operatorv1.NetworkTypeOpenShiftSDN),
		},
	}
}

func makeInfraStatus(platform configv1.PlatformType) configv1.InfrastructureStatus {
	if platform == configv1.AzurePlatformType {
		return configv1.InfrastructureStatus{
			PlatformStatus: &configv1.PlatformStatus{
				Type: platform,
				Azure: &configv1.AzurePlatformStatus{
					CloudName: configv1.AzurePublicCloud,
				},
			},
			Platform:               platform,
			InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
			ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
		}
	}

	return configv1.InfrastructureStatus{
		PlatformStatus: &configv1.PlatformStatus{
			Type: platform,
		},
		Platform:               platform,
		InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
		ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
	}
}

func makeInfraCloudConfig(platform configv1.PlatformType) *corev1.ConfigMap {
	defaultConfig := `[Global]
`

	if platform == configv1.AzurePlatformType {
		defaultConfig = defaultAzureConfig
	}

	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      infraCloudConfName,
		Namespace: OpenshiftConfigNamespace,
	}, Data: map[string]string{infraCloudConfKey: defaultConfig}}
}

func makeManagedCloudConfig(platform configv1.PlatformType) *corev1.ConfigMap {
	defaultConfig := `[Global]
`

	if platform == configv1.AzurePlatformType {
		defaultConfig = defaultAzureConfig
	}

	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      managedCloudConfigMapName,
		Namespace: OpenshiftManagedConfigNamespace,
	}, Data: map[string]string{"cloud.conf": defaultConfig}}
}

var _ = Describe("isCloudConfigEqual reconciler method", func() {
	reconciler := &CloudConfigReconciler{}

	It("should return 'true' if ConfigMaps content are equal", func() {
		Expect(reconciler.isCloudConfigEqual(makeManagedCloudConfig(configv1.AzurePlatformType), makeManagedCloudConfig(configv1.AzurePlatformType))).Should(BeTrue())
	})

	It("should return 'false' if ConfigMaps content are not equal", func() {
		changedManagedCloudConfig := makeManagedCloudConfig(configv1.AzurePlatformType)
		changedManagedCloudConfig.Immutable = ptr.To[bool](true)
		Expect(reconciler.isCloudConfigEqual(changedManagedCloudConfig, makeManagedCloudConfig(configv1.AzurePlatformType))).Should(BeFalse())

		changedManagedCloudConfig = makeManagedCloudConfig(configv1.AzurePlatformType)
		changedManagedCloudConfig.Data = map[string]string{}
		Expect(reconciler.isCloudConfigEqual(changedManagedCloudConfig, makeManagedCloudConfig(configv1.AzurePlatformType))).Should(BeFalse())
	})
})

var _ = Describe("prepareSourceConfigMap reconciler method", func() {
	reconciler := &CloudConfigReconciler{}
	infra := makeInfrastructureResource(configv1.AzurePlatformType)
	infraCloudConfig := makeInfraCloudConfig(configv1.AzurePlatformType)
	managedCloudConfig := makeManagedCloudConfig(configv1.AzurePlatformType)

	It("not prepared config should be different with managed one", func() {
		_, ok := infraCloudConfig.Data[infraCloudConfKey]
		Expect(ok).Should(BeTrue())
		Expect(reconciler.isCloudConfigEqual(infraCloudConfig, managedCloudConfig)).Should(BeFalse())
	})

	It("prepared config should be equal with managed one", func() {
		preparedConfig, err := reconciler.prepareSourceConfigMap(infraCloudConfig, infra)
		Expect(err).Should(Succeed())
		_, ok := preparedConfig.Data[infraCloudConfKey]
		Expect(ok).Should(BeFalse())
		_, ok = preparedConfig.Data[defaultConfigKey]
		Expect(ok).Should(BeTrue())
		Expect(reconciler.isCloudConfigEqual(preparedConfig, managedCloudConfig)).Should(BeTrue())
	})

	It("config preparation should fail if key from infra resource does not found", func() {
		brokenInfraConfig := infraCloudConfig.DeepCopy()
		brokenInfraConfig.Data = map[string]string{"hehehehehe": "bar"}
		_, err := reconciler.prepareSourceConfigMap(brokenInfraConfig, infra)
		Expect(err).Should(Not(Succeed()))
		Expect(err.Error()).Should(BeEquivalentTo("key foo specified in infra resource does not found in source configmap openshift-config/test-config"))
	})

	It("config preparation should not touch extra fields in infra ConfigMap", func() {
		extendedInfraConfig := infraCloudConfig.DeepCopy()
		extendedInfraConfig.Data = map[string]string{infraCloudConfKey: "{}", "{}": "{}"}
		preparedConfig, err := reconciler.prepareSourceConfigMap(extendedInfraConfig, infra)
		Expect(err).Should(Succeed())
		_, ok := preparedConfig.Data[defaultConfigKey]
		Expect(ok).Should(BeTrue())
		Expect(len(preparedConfig.Data)).Should(BeEquivalentTo(2))
	})
})

var _ = Describe("Cloud config sync controller", func() {
	var rec *record.FakeRecorder

	var mgr manager.Manager
	var mgrCtxCancel context.CancelFunc
	var mgrStopped chan struct{}
	ctx := context.Background()

	targetNamespaceName := testManagedNamespace

	var infraCloudConfig *corev1.ConfigMap
	var managedCloudConfig *corev1.ConfigMap

	var reconciler *CloudConfigReconciler

	syncedConfigMapKey := client.ObjectKey{Namespace: targetNamespaceName, Name: syncedCloudConfigMapName}

	BeforeEach(func() {
		By("Setting up a new manager")
		var err error
		mgr, err = manager.New(cfg, manager.Options{
			Metrics: metricsserver.Options{
				BindAddress: "0",
			},
			Controller: config.Controller{
				SkipNameValidation: ptr.To(true),
			},
		})
		Expect(err).NotTo(HaveOccurred())

		reconciler = &CloudConfigReconciler{
			ClusterOperatorStatusClient: ClusterOperatorStatusClient{
				Client:           cl,
				Recorder:         rec,
				Clock:            clocktesting.NewFakePassiveClock(time.Now()),
				ManagedNamespace: targetNamespaceName,
			},
			Scheme: scheme.Scheme,
		}
		Expect(reconciler.SetupWithManager(mgr)).To(Succeed())

		By("Creating Infra resource")
		infraResource := makeInfrastructureResource(configv1.AzurePlatformType)
		Expect(cl.Create(ctx, infraResource)).To(Succeed())
		infraResource.Status = makeInfraStatus(infraResource.Spec.PlatformSpec.Type)
		Expect(cl.Status().Update(ctx, infraResource.DeepCopy())).To(Succeed())

		By("Creating network resource")
		networkResource := makeNetworkResource()
		Expect(cl.Create(ctx, networkResource)).To(Succeed())
	})

	JustBeforeEach(func() {
		var mgrCtx context.Context
		mgrCtx, mgrCtxCancel = context.WithCancel(ctx)
		mgrStopped = make(chan struct{})

		By("Starting the manager")
		go func() {
			defer GinkgoRecover()
			defer close(mgrStopped)

			Expect(mgr.Start(mgrCtx)).To(Succeed())
		}()
	})

	AfterEach(func() {
		By("Closing the manager")
		mgrCtxCancel()
		Eventually(mgrStopped, timeout).Should(BeClosed())

		co := &configv1.ClusterOperator{}
		err := cl.Get(context.Background(), client.ObjectKey{Name: clusterOperatorName}, co)
		if err == nil || !apierrors.IsNotFound(err) {
			Eventually(func() error {
				return cl.Delete(context.Background(), co)
			}).Should(SatisfyAny(
				Not(HaveOccurred()),
				MatchError(apierrors.IsNotFound, "IsNotFound"),
			))
		}
		Eventually(func() error {
			return cl.Get(context.Background(), client.ObjectKey{Name: clusterOperatorName}, co)
		}).Should(MatchError(apierrors.IsNotFound, "IsNotFound"))

		By("Cleanup resources")
		deleteOptions := &client.DeleteOptions{
			GracePeriodSeconds: ptr.To[int64](0),
		}

		allCMs := &corev1.ConfigMapList{}
		Expect(cl.List(ctx, allCMs)).To(Succeed())
		for _, cm := range allCMs.Items {
			Expect(cl.Delete(ctx, cm.DeepCopy(), deleteOptions)).To(Succeed())
			Eventually(func() error {
				return cl.Get(ctx, client.ObjectKeyFromObject(cm.DeepCopy()), &corev1.ConfigMap{})
			}).Should(MatchError(apierrors.IsNotFound, "IsNotFound"))
		}

		infraCloudConfig = nil
		managedCloudConfig = nil

		infra := makeInfrastructureResource(configv1.AzurePlatformType)
		Expect(cl.Delete(ctx, infra)).To(Succeed())
		Eventually(func() error {
			return cl.Get(ctx, client.ObjectKeyFromObject(infra), infra)
		}).Should(MatchError(apierrors.IsNotFound, "IsNotFound"))

		networkResource := makeNetworkResource()
		Expect(cl.Delete(ctx, networkResource)).To(Succeed())

		Eventually(func() error {
			return cl.Get(ctx, client.ObjectKeyFromObject(networkResource), networkResource)
		}).Should(MatchError(apierrors.IsNotFound, "IsNotFound"))
	})

	Context("on Azure", func() {
		BeforeEach(func() {

			By("Creating needed ConfigMaps")
			infraCloudConfig = makeInfraCloudConfig(configv1.AzurePlatformType)
			managedCloudConfig = makeManagedCloudConfig(configv1.AzurePlatformType)
			Expect(cl.Create(ctx, infraCloudConfig)).To(Succeed())
			Expect(cl.Create(ctx, managedCloudConfig)).To(Succeed())
		})

		It("config should be synced up after first reconcile", func() {
			Eventually(func(g Gomega) string {
				syncedCloudConfigMap := &corev1.ConfigMap{}
				err := cl.Get(ctx, syncedConfigMapKey, syncedCloudConfigMap)
				g.Expect(err).NotTo(HaveOccurred())
				return syncedCloudConfigMap.Data[defaultConfigKey]
			}).Should(Equal(defaultAzureConfig))
		})

		It("config should be synced up if managed cloud config changed", func() {
			changedConfigString := `{"cloud":"AzurePublicCloud","tenantId":"0000000-1234-1234-0000-000000000000","Entries":null,"subscriptionId":"0000000-0000-0000-0000-000000000000","vmType":"standard","putVMSSVMBatchSize":0,"enableMigrateToIPBasedBackendPoolAPI":false,"clusterServiceLoadBalancerHealthProbeMode":"shared"}`
			changedManagedConfig := managedCloudConfig.DeepCopy()
			changedManagedConfig.Data = map[string]string{"cloud.conf": changedConfigString}
			Expect(cl.Update(ctx, changedManagedConfig)).To(Succeed())

			Eventually(func(g Gomega) string {
				syncedCloudConfigMap := &corev1.ConfigMap{}
				err := cl.Get(ctx, syncedConfigMapKey, syncedCloudConfigMap)
				g.Expect(err).NotTo(HaveOccurred())
				return syncedCloudConfigMap.Data[defaultConfigKey]
			}).Should(Equal(changedConfigString))
		})

		It("config should be synced up if own cloud-config deleted or changed", func() {
			syncedCloudConfigMap := &corev1.ConfigMap{}
			Eventually(func() error {
				return cl.Get(ctx, syncedConfigMapKey, syncedCloudConfigMap)
			}, timeout).Should(Succeed())

			changedConfigString := `{"cloud":"AzurePublicCloud","tenantId":"0000000-1234-1234-0000-000000000000","subscriptionId":"0000000-0000-0000-0000-000000000000","vmType":"standard","putVMSSVMBatchSize":0,"enableMigrateToIPBasedBackendPoolAPI":false,"clusterServiceLoadBalancerHealthProbeMode":"shared"}`
			syncedCloudConfigMap.Data = map[string]string{"foo": changedConfigString}
			Expect(cl.Update(ctx, syncedCloudConfigMap)).To(Succeed())
			Eventually(func(g Gomega) string {
				err := cl.Get(ctx, syncedConfigMapKey, syncedCloudConfigMap)
				g.Expect(err).NotTo(HaveOccurred())
				return syncedCloudConfigMap.Data[defaultConfigKey]
			}).Should(Equal(defaultAzureConfig))

			Expect(cl.Delete(ctx, syncedCloudConfigMap)).To(Succeed())
			Eventually(func(g Gomega) string {
				err := cl.Get(ctx, syncedConfigMapKey, syncedCloudConfigMap)
				g.Expect(err).NotTo(HaveOccurred())
				return syncedCloudConfigMap.Data[defaultConfigKey]
			}).Should(Equal(defaultAzureConfig))
		})

		It("config should not be updated if source and target config content are identical", func() {
			syncedCloudConfigMap := &corev1.ConfigMap{}
			Eventually(func() error {
				return cl.Get(ctx, syncedConfigMapKey, syncedCloudConfigMap)
			}, timeout).Should(Succeed())
			initialCMresourceVersion := syncedCloudConfigMap.ResourceVersion

			request := reconcile.Request{NamespacedName: client.ObjectKey{Name: "foo", Namespace: "bar"}}
			_, err := reconciler.Reconcile(ctx, request)
			Expect(err).Should(Succeed())

			Expect(cl.Get(ctx, syncedConfigMapKey, syncedCloudConfigMap)).Should(Succeed())
			Expect(initialCMresourceVersion).Should(BeEquivalentTo(syncedCloudConfigMap.ResourceVersion))
		})

		It("config should be synced up with infra one if managed config not found", func() {
			Expect(cl.Delete(ctx, managedCloudConfig)).Should(Succeed())
			managedCloudConfig = nil

			changedInfraConfigString := `{"cloud":"AzurePublicCloud","tenantId":"0000000-1234-1234-0000-000000000000","Entries":null,"subscriptionId":"0000000-0000-0000-0000-000000000000","vmType":"standard","putVMSSVMBatchSize":0,"enableMigrateToIPBasedBackendPoolAPI":false,"clusterServiceLoadBalancerHealthProbeMode":"shared"}`
			changedInfraConfig := infraCloudConfig.DeepCopy()
			changedInfraConfig.Data = map[string]string{infraCloudConfKey: changedInfraConfigString}
			Expect(cl.Update(ctx, changedInfraConfig)).Should(Succeed())

			Eventually(func(g Gomega) string {
				syncedCloudConfigMap := &corev1.ConfigMap{}
				err := cl.Get(ctx, syncedConfigMapKey, syncedCloudConfigMap)
				g.Expect(err).NotTo(HaveOccurred())
				return syncedCloudConfigMap.Data[defaultConfigKey]
			}).Should(Equal(changedInfraConfigString))
		})

		It("all keys from cloud-config should be synced", func() {

			changedInfraConfigString := `{"cloud":"AzurePublicCloud","tenantId":"0000000-1234-1234-0000-000000000000","subscriptionId":"0000000-0000-0000-0000-000000000000","vmType":"standard","putVMSSVMBatchSize":0,"enableMigrateToIPBasedBackendPoolAPI":false,"clusterServiceLoadBalancerHealthProbeMode":"shared"}`
			changedManagedConfig := managedCloudConfig.DeepCopy()
			changedManagedConfig.Data = map[string]string{
				infraCloudConfKey: changedInfraConfigString, cloudProviderConfigCABundleConfigMapKey: "some pem there",
				"baz": "fizz",
			}
			Expect(cl.Update(ctx, changedManagedConfig)).Should(Succeed())

			Eventually(func(g Gomega) int {
				syncedCloudConfigMap := &corev1.ConfigMap{}
				err := cl.Get(ctx, syncedConfigMapKey, syncedCloudConfigMap)
				g.Expect(err).NotTo(HaveOccurred())
				return len(syncedCloudConfigMap.Data)
			}).Should(Equal(3))
		})
	})
})

var _ = Describe("Cloud config sync reconciler", func() {
	// Tests which does not involve manager, dedicated to exercise Reconcile method
	var reconciler *CloudConfigReconciler

	ctx := context.Background()
	targetNamespaceName := testManagedNamespace

	BeforeEach(func() {
		reconciler = &CloudConfigReconciler{
			ClusterOperatorStatusClient: ClusterOperatorStatusClient{
				Client:           cl,
				Clock:            clocktesting.NewFakePassiveClock(time.Now()),
				ManagedNamespace: targetNamespaceName,
			},
			Scheme: scheme.Scheme,
		}

		networkResource := makeNetworkResource()
		Expect(cl.Create(ctx, networkResource)).To(Succeed())
	})

	AfterEach(func() {
		deleteOptions := &client.DeleteOptions{
			GracePeriodSeconds: ptr.To[int64](0),
		}

		co := &configv1.ClusterOperator{}
		err := cl.Get(context.Background(), client.ObjectKey{Name: clusterOperatorName}, co)
		if err == nil || !apierrors.IsNotFound(err) {
			Eventually(func() error {
				return cl.Delete(context.Background(), co)
			}).Should(SatisfyAny(
				Not(HaveOccurred()),
				MatchError(apierrors.IsNotFound, "IsNotFound"),
			))
		}
		Eventually(func() error {
			return cl.Get(context.Background(), client.ObjectKey{Name: clusterOperatorName}, co)
		}).Should(MatchError(apierrors.IsNotFound, "IsNotFound"))

		infra := &configv1.Infrastructure{
			ObjectMeta: metav1.ObjectMeta{
				Name: infrastructureResourceName,
			},
		}
		// omitted error intentionally, 404 might be there for some cases
		cl.Delete(ctx, infra) //nolint:errcheck
		Eventually(func() error {
			return cl.Get(ctx, client.ObjectKeyFromObject(infra), infra)
		}).Should(MatchError(apierrors.IsNotFound, "IsNotFound"))

		networkResource := makeNetworkResource()
		Expect(cl.Delete(ctx, networkResource)).To(Succeed())

		Eventually(func() error {
			return cl.Get(ctx, client.ObjectKeyFromObject(networkResource), networkResource)
		}).Should(MatchError(apierrors.IsNotFound, "IsNotFound"))

		allCMs := &corev1.ConfigMapList{}
		Expect(cl.List(ctx, allCMs)).To(Succeed())
		for _, cm := range allCMs.Items {
			Expect(cl.Delete(ctx, cm.DeepCopy(), deleteOptions)).To(Succeed())
			Eventually(func() error {
				return cl.Get(ctx, client.ObjectKeyFromObject(cm.DeepCopy()), &corev1.ConfigMap{})
			}).Should(MatchError(apierrors.IsNotFound, "IsNotFound"))
		}
	})

	Context("On AWS platform", func() {
		BeforeEach(func() {
			Expect(cl.Create(ctx, makeInfraCloudConfig(configv1.AWSPlatformType))).To(Succeed())
		})

		It("should skip config sync for AWS platform if there is no reference in infra resource", func() {
			infraResource := makeInfrastructureResource(configv1.AWSPlatformType)
			infraResource.Spec.CloudConfig.Name = ""
			Expect(cl.Create(ctx, infraResource)).To(Succeed())

			infraResource.Status = makeInfraStatus(infraResource.Spec.PlatformSpec.Type)
			Expect(cl.Status().Update(ctx, infraResource.DeepCopy())).To(Succeed())

			_, err := reconciler.Reconcile(context.TODO(), ctrl.Request{})
			Expect(err).To(BeNil())

			allCMs := &corev1.ConfigMapList{}
			Expect(cl.List(ctx, allCMs, &client.ListOptions{Namespace: targetNamespaceName})).To(Succeed())

			Expect(len(allCMs.Items)).To(BeZero())
		})

		It("should perform config sync for AWS platform if there is a reference in infra resource", func() {
			infraResource := makeInfrastructureResource(configv1.AWSPlatformType)
			Expect(cl.Create(ctx, infraResource)).To(Succeed())

			infraResource.Status = makeInfraStatus(infraResource.Spec.PlatformSpec.Type)
			Expect(cl.Status().Update(ctx, infraResource.DeepCopy())).To(Succeed())

			_, err := reconciler.Reconcile(context.TODO(), ctrl.Request{})
			Expect(err).To(BeNil())

			allCMs := &corev1.ConfigMapList{}
			Expect(cl.List(ctx, allCMs, &client.ListOptions{Namespace: targetNamespaceName})).To(Succeed())

			Expect(len(allCMs.Items)).NotTo(BeZero())
			Expect(len(allCMs.Items)).To(BeEquivalentTo(1))
		})
	})

	Context("On Azure platform", func() {
		BeforeEach(func() {
			Expect(cl.Create(ctx, makeInfraCloudConfig(configv1.AzurePlatformType))).To(Succeed())
		})

		It("should perform config sync for Azure platform", func() {
			infraResource := makeInfrastructureResource(configv1.AzurePlatformType)
			Expect(cl.Create(ctx, infraResource)).To(Succeed())
			infraResource.Status = makeInfraStatus(infraResource.Spec.PlatformSpec.Type)
			Expect(cl.Status().Update(ctx, infraResource.DeepCopy())).To(Succeed())
			_, err := reconciler.Reconcile(context.TODO(), ctrl.Request{})
			Expect(err).To(BeNil())
			allCMs := &corev1.ConfigMapList{}
			Expect(cl.List(ctx, allCMs, &client.ListOptions{Namespace: targetNamespaceName})).To(Succeed())
			Expect(len(allCMs.Items)).NotTo(BeZero())
			Expect(len(allCMs.Items)).To(BeEquivalentTo(1))
		})
	})

	Context("On BareMetal platform", func() {
		BeforeEach(func() {
			Expect(cl.Create(ctx, makeInfraCloudConfig(configv1.BareMetalPlatformType))).To(Succeed())
		})

		It("should skip config sync for BareMetal platform", func() {
			infraResource := makeInfrastructureResource(configv1.BareMetalPlatformType)
			Expect(cl.Create(ctx, infraResource)).To(Succeed())
			infraResource.Status = makeInfraStatus(infraResource.Spec.PlatformSpec.Type)
			Expect(cl.Status().Update(ctx, infraResource.DeepCopy())).To(Succeed())
			_, err := reconciler.Reconcile(context.TODO(), ctrl.Request{})
			Expect(err).To(BeNil())
			allCMs := &corev1.ConfigMapList{}
			Expect(cl.List(ctx, allCMs, &client.ListOptions{Namespace: targetNamespaceName})).To(Succeed())
			Expect(len(allCMs.Items)).To(BeZero())
		})
	})

	It("reconcile should fail if no infra resource found", func() {
		_, err := reconciler.Reconcile(context.TODO(), ctrl.Request{})
		Expect(err.Error()).Should(BeEquivalentTo("infrastructures.config.openshift.io \"cluster\" not found"))
	})

	It("should fail if no PlatformStatus in infra resource presented ", func() {
		infraResource := makeInfrastructureResource(configv1.AWSPlatformType)
		Expect(cl.Create(ctx, infraResource)).To(Succeed())
		_, err := reconciler.Reconcile(context.TODO(), ctrl.Request{})
		Expect(err.Error()).Should(BeEquivalentTo("platformStatus is required"))
	})
})
