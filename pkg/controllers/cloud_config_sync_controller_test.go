package controllers

import (
	"context"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	infraCloudConfName = "test-config"
	infraCloudConfKey  = "foo"
)

func makeInfrastructureResource() *configv1.Infrastructure {
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
				Type: configv1.AzurePlatformType,
			},
		},
	}
}

func makeInfraCloudConfig() *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      infraCloudConfName,
		Namespace: openshiftConfigNamespace,
	}, Data: map[string]string{infraCloudConfKey: "bar"}}
}

func makeManagedCloudConfig() *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      managedCloudConfigMapName,
		Namespace: openshiftManagedConfigNamespace,
	}, Data: map[string]string{"cloud.conf": "bar"}}
}

var _ = Describe("isCloudConfigEqual reconciler method", func() {
	reconciler := &CloudConfigReconciler{}

	It("should return 'true' if ConfigMaps content are equal", func() {
		Expect(reconciler.isCloudConfigEqual(makeManagedCloudConfig(), makeManagedCloudConfig())).Should(BeTrue())
	})

	It("should return 'false' if ConfigMaps content are not equal", func() {
		changedManagedCloudConfig := makeManagedCloudConfig()
		changedManagedCloudConfig.Immutable = pointer.Bool(true)
		Expect(reconciler.isCloudConfigEqual(changedManagedCloudConfig, makeManagedCloudConfig())).Should(BeFalse())

		changedManagedCloudConfig = makeManagedCloudConfig()
		changedManagedCloudConfig.Data = map[string]string{}
		Expect(reconciler.isCloudConfigEqual(changedManagedCloudConfig, makeManagedCloudConfig())).Should(BeFalse())
	})
})

var _ = Describe("prepareSourceConfigMap reconciler method", func() {
	reconciler := &CloudConfigReconciler{}
	infra := makeInfrastructureResource()
	infraCloudConfig := makeInfraCloudConfig()
	managedCloudConfig := makeManagedCloudConfig()

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
})

var _ = Describe("Cloud config sync controller", func() {
	var rec *record.FakeRecorder

	var mgrCtxCancel context.CancelFunc
	var mgrStopped chan struct{}
	ctx := context.Background()

	targetNamespaceName := testManagedNamespace

	var infraCloudConfig *corev1.ConfigMap
	var managedCloudConfig *corev1.ConfigMap
	var syncedCloudConfigMap *corev1.ConfigMap

	var reconciler *CloudConfigReconciler

	syncedConfigMapKey := client.ObjectKey{Namespace: targetNamespaceName, Name: cloudConfigMapName}

	BeforeEach(func() {
		By("Setting up a new manager")
		mgr, err := manager.New(cfg, manager.Options{MetricsBindAddress: "0"})
		Expect(err).NotTo(HaveOccurred())

		reconciler = &CloudConfigReconciler{
			Client:          cl,
			Scheme:          scheme.Scheme,
			Recorder:        rec,
			TargetNamespace: targetNamespaceName,
		}
		Expect(reconciler.SetupWithManager(mgr)).To(Succeed())

		By("Creating Infra resource")
		Expect(cl.Create(ctx, makeInfrastructureResource())).To(Succeed())

		By("Creating needed ConfigMaps")
		infraCloudConfig = makeInfraCloudConfig()
		managedCloudConfig = makeManagedCloudConfig()
		Expect(cl.Create(ctx, infraCloudConfig)).To(Succeed())
		Expect(cl.Create(ctx, managedCloudConfig)).To(Succeed())

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

		By("Cleanup resources")
		deleteOptions := &client.DeleteOptions{
			GracePeriodSeconds: pointer.Int64(0),
		}

		if infraCloudConfig != nil {
			Expect(cl.Delete(ctx, infraCloudConfig, deleteOptions)).To(Succeed())
			Eventually(
				apierrors.IsNotFound(cl.Get(ctx, client.ObjectKeyFromObject(infraCloudConfig), &corev1.ConfigMap{})),
			).Should(BeTrue())
		}

		if managedCloudConfig != nil {
			Expect(cl.Delete(ctx, managedCloudConfig, deleteOptions)).To(Succeed())
			Eventually(
				apierrors.IsNotFound(cl.Get(ctx, client.ObjectKeyFromObject(managedCloudConfig), &corev1.ConfigMap{})),
			).Should(BeTrue())
		}

		if syncedCloudConfigMap != nil {
			Expect(cl.Delete(ctx, syncedCloudConfigMap, deleteOptions)).To(Succeed())
			Eventually(
				apierrors.IsNotFound(cl.Get(ctx, client.ObjectKeyFromObject(syncedCloudConfigMap), &corev1.Namespace{})),
			).Should(BeTrue())
		}

		infraCloudConfig = nil
		managedCloudConfig = nil
		syncedCloudConfigMap = nil

		infra := makeInfrastructureResource()
		Expect(cl.Delete(ctx, infra)).To(Succeed())
		Eventually(
			apierrors.IsNotFound(cl.Get(ctx, client.ObjectKeyFromObject(infra), infra)),
		).Should(BeTrue())
	})

	It("config should be synced up after first reconcile", func() {
		Eventually(func() (bool, error) {
			syncedCloudConfigMap := &corev1.ConfigMap{}
			err := cl.Get(ctx, syncedConfigMapKey, syncedCloudConfigMap)
			if err != nil {
				return false, err
			}
			return syncedCloudConfigMap.Data[defaultConfigKey] == "bar", nil
		}).Should(BeTrue())
	})

	It("config should be synced up if managed cloud config changed", func() {
		changedManagedConfig := managedCloudConfig.DeepCopy()
		changedManagedConfig.Data = map[string]string{"cloud.conf": "managed one changed"}
		Expect(cl.Update(ctx, changedManagedConfig)).To(Succeed())

		Eventually(func() (bool, error) {
			syncedCloudConfigMap := &corev1.ConfigMap{}
			err := cl.Get(ctx, syncedConfigMapKey, syncedCloudConfigMap)
			if err != nil {
				return false, err
			}
			return syncedCloudConfigMap.Data[defaultConfigKey] == "managed one changed", nil
		}).Should(BeTrue())
	})

	It("config should be synced up if own cloud-config deleted or changed", func() {
		syncedCloudConfigMap := &corev1.ConfigMap{}
		Expect(cl.Get(ctx, syncedConfigMapKey, syncedCloudConfigMap)).Should(Succeed())

		syncedCloudConfigMap.Data = map[string]string{"foo": "baz"}
		Expect(cl.Update(ctx, syncedCloudConfigMap)).To(Succeed())
		Eventually(func() (bool, error) {
			err := cl.Get(ctx, syncedConfigMapKey, syncedCloudConfigMap)
			if err != nil {
				return false, err
			}
			return syncedCloudConfigMap.Data[defaultConfigKey] == "bar", nil
		}).Should(BeTrue())

		Expect(cl.Delete(ctx, syncedCloudConfigMap)).To(Succeed())
		Eventually(func() (bool, error) {
			err := cl.Get(ctx, syncedConfigMapKey, syncedCloudConfigMap)
			if err != nil {
				return false, err
			}
			return syncedCloudConfigMap.Data[defaultConfigKey] == "bar", nil
		}).Should(BeTrue())
	})

	It("config should not be updated if source and target config content are identical", func() {
		syncedCloudConfigMap := &corev1.ConfigMap{}
		Expect(cl.Get(ctx, syncedConfigMapKey, syncedCloudConfigMap)).Should(Succeed())
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

		changedInfraConfig := infraCloudConfig.DeepCopy()
		changedInfraConfig.Data = map[string]string{infraCloudConfKey: "infra one changed"}
		Expect(cl.Update(ctx, changedInfraConfig)).Should(Succeed())

		Eventually(func() (bool, error) {
			syncedCloudConfigMap := &corev1.ConfigMap{}
			err := cl.Get(ctx, syncedConfigMapKey, syncedCloudConfigMap)
			if err != nil {
				return false, err
			}
			return syncedCloudConfigMap.Data[defaultConfigKey] == "infra one changed", nil
		}).Should(BeTrue())
	})
})
