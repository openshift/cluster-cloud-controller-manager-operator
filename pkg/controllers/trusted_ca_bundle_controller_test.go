package controllers

import (
	"context"
	"fmt"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/util"
)

const (
	systemCAValid   = "./fixtures/trust_bundle_valid.pem"
	systemCAInvalid = "./fixtures/trust_bundle_invalid.pem"

	additionalAmazonCAPemPath = "./fixtures/additional_ca_amazon.pem"
	additionalMsCAPemPath     = "./fixtures/additional_ca_ms.pem"

	// https://docs.openshift.com/container-platform/4.8/networking/configuring-a-custom-pki.html#nw-proxy-configure-object_configuring-a-custom-pki
	additionalCAConfigMapName = "user-ca-bundle"
	additionalCAConfigMapKey  = trustedCABundleConfigMapKey
)

func makeValidUserCAConfigMap(pemPath string) (*corev1.ConfigMap, error) {
	testTrustBundle, err := os.ReadFile(pemPath)
	if err != nil {
		return nil, err
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      additionalCAConfigMapName,
			Namespace: OpenshiftConfigNamespace,
		},
		Data: map[string]string{
			additionalCAConfigMapKey: string(testTrustBundle),
		},
	}, nil
}

func makeProxyResource() *v1.Proxy {
	return &v1.Proxy{
		ObjectMeta: metav1.ObjectMeta{Name: proxyResourceName},
		Spec: v1.ProxySpec{
			TrustedCA: v1.ConfigMapNameReference{Name: additionalCAConfigMapName},
		},
	}
}

func makeSyncedCloudConfig(namespace string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name:      syncedCloudConfigMapName,
		Namespace: namespace,
	}, Data: data}
}

var _ = Describe("Trusted CA bundle sync controller", func() {
	var rec *record.FakeRecorder

	var mgrCtxCancel context.CancelFunc
	var mgrStopped chan struct{}
	ctx := context.Background()

	targetNamespaceName := testManagedNamespace

	var reconciler *TrustedCABundleReconciler

	var proxyResource *v1.Proxy
	var additionalCAConfigMap *corev1.ConfigMap
	var syncedCloudConfigConfigMap *corev1.ConfigMap

	mergedCAObjectKey := client.ObjectKey{Namespace: targetNamespaceName, Name: trustedCAConfigMapName}

	var checkMergedTrustedCAConfig = func(certificateCount int, issuer string) func() error {
		return func() error {
			mergedTrustedCA := &corev1.ConfigMap{}
			if err := cl.Get(ctx, mergedCAObjectKey, mergedTrustedCA); err != nil {
				return err
			}
			certs, err := util.CertificateData([]byte(mergedTrustedCA.Data[additionalCAConfigMapKey]))
			if err != nil {
				return err
			}
			if len(certs) != certificateCount {
				return fmt.Errorf("Expected %d certs but got %d certs", certificateCount, len(certs))
			}
			if certs[0].Issuer.Organization[0] != issuer {
				return fmt.Errorf("Expected Issuer %q, but got %q", issuer, certs[0].Issuer.Organization[0])
			}
			return nil
		}
	}

	BeforeEach(func() {
		By("Setting up a new manager")
		mgr, err := manager.New(cfg, manager.Options{
			Metrics: metricsserver.Options{
				BindAddress: "0",
			},
			Controller: config.Controller{
				SkipNameValidation: ptr.To(true),
			},
		})
		Expect(err).NotTo(HaveOccurred())

		reconciler = &TrustedCABundleReconciler{
			ClusterOperatorStatusClient: ClusterOperatorStatusClient{
				Client:           cl,
				Recorder:         rec,
				Clock:            clocktesting.NewFakePassiveClock(time.Now()),
				ManagedNamespace: targetNamespaceName,
			},
			Scheme:          scheme.Scheme,
			trustBundlePath: systemCAValid,
		}
		Expect(reconciler.SetupWithManager(mgr)).To(Succeed())

		By("Creating needed ConfigMaps and Update Proxy")
		proxyResource = makeProxyResource()
		additionalCAConfigMap, err = makeValidUserCAConfigMap(additionalAmazonCAPemPath)
		syncedCloudConfigConfigMap = makeSyncedCloudConfig(targetNamespaceName, map[string]string{})
		Expect(err).NotTo(HaveOccurred())
		Expect(cl.Create(ctx, proxyResource)).To(Succeed())
		Expect(cl.Create(ctx, additionalCAConfigMap)).To(Succeed())
		Expect(cl.Create(ctx, syncedCloudConfigConfigMap)).To(Succeed())

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
			GracePeriodSeconds: ptr.To[int64](0),
		}

		co := &v1.ClusterOperator{}
		err := cl.Get(context.Background(), client.ObjectKey{Name: clusterOperatorName}, co)
		if err == nil || !apierrors.IsNotFound(err) {
			Eventually(func() bool {
				err := cl.Delete(context.Background(), co)
				return err == nil || apierrors.IsNotFound(err)
			}).Should(BeTrue())
		}
		Eventually(apierrors.IsNotFound(cl.Get(context.Background(), client.ObjectKey{Name: clusterOperatorName}, co))).Should(BeTrue())

		if proxyResource != nil {
			Expect(cl.Delete(ctx, proxyResource, deleteOptions)).To(Succeed())
			Eventually(
				apierrors.IsNotFound(cl.Get(ctx, client.ObjectKeyFromObject(proxyResource), &v1.Proxy{})),
			).Should(BeTrue())
		}

		allCMs := &corev1.ConfigMapList{}
		Expect(cl.List(ctx, allCMs)).To(Succeed())
		for _, cm := range allCMs.Items {
			Expect(cl.Delete(ctx, cm.DeepCopy(), deleteOptions)).To(Succeed())
			Eventually(
				apierrors.IsNotFound(cl.Get(ctx, client.ObjectKeyFromObject(cm.DeepCopy()), &corev1.ConfigMap{})),
			).Should(BeTrue())
		}

		proxyResource = nil
		additionalCAConfigMap = nil
		syncedCloudConfigConfigMap = nil
	})

	It("CA should be synced and merged up after first reconcile", func() {
		Eventually(checkMergedTrustedCAConfig(3, "Amazon")).Should(Succeed())
	})

	It("ca bundle should be synced up if own one was deleted or changed", func() {
		mergedTrustedCA := &corev1.ConfigMap{}
		Eventually(func() error {
			return cl.Get(ctx, mergedCAObjectKey, mergedTrustedCA)
		}, timeout).Should(Succeed())
		certs, err := util.CertificateData([]byte(mergedTrustedCA.Data[additionalCAConfigMapKey]))
		Expect(err).NotTo(HaveOccurred())
		Expect(len(certs)).Should(BeEquivalentTo(3))
		Expect(certs[0].Issuer.Organization[0]).Should(BeEquivalentTo("Amazon"))

		mergedTrustedCA.Data = map[string]string{additionalCAConfigMapKey: "KEKEKE"}
		Expect(cl.Update(ctx, mergedTrustedCA)).To(Succeed())
		Eventually(checkMergedTrustedCAConfig(3, "Amazon")).Should(Succeed())

		Expect(cl.Delete(ctx, mergedTrustedCA)).To(Succeed())
		Eventually(checkMergedTrustedCAConfig(3, "Amazon")).Should(Succeed())
	})

	It("ca bundle should be synced up if user one in openshift-config was changed", func() {
		mergedTrustedCA := &corev1.ConfigMap{}
		Eventually(func() error {
			return cl.Get(ctx, mergedCAObjectKey, mergedTrustedCA)
		}, timeout).Should(Succeed())
		certs, err := util.CertificateData([]byte(mergedTrustedCA.Data[additionalCAConfigMapKey]))
		Expect(err).NotTo(HaveOccurred())
		Expect(len(certs)).Should(BeEquivalentTo(3))
		Expect(certs[0].Issuer.Organization[0]).Should(BeEquivalentTo("Amazon"))

		msCA, err := os.ReadFile(additionalMsCAPemPath)
		Expect(err).To(Succeed())
		additionalCAConfigMap.Data = map[string]string{additionalCAConfigMapKey: string(msCA)}
		Expect(cl.Update(ctx, additionalCAConfigMap)).To(Succeed())
		Eventually(checkMergedTrustedCAConfig(3, "Microsoft Corporation")).Should(Succeed())
	})

	It("ca bundle should be set to system one if additional ca bundle is invalid PEM", func() {
		additionalCAConfigMap.Data = map[string]string{additionalCAConfigMapKey: "kekekeke"}
		Expect(cl.Update(ctx, additionalCAConfigMap)).To(Succeed())
		Eventually(checkMergedTrustedCAConfig(2, "GlobalSign")).Should(Succeed())
	})

	It("ca bundle should be set to system one if additional ca bundle has invalid key", func() {
		additionalCAConfigMap.Data = map[string]string{"foo": "bar"}
		Expect(cl.Update(ctx, additionalCAConfigMap)).To(Succeed())
		Eventually(checkMergedTrustedCAConfig(2, "GlobalSign")).Should(Succeed())
	})

	It("ca bundle should be set to system one if proxy points nowhere", func() {
		proxyResource.Spec.TrustedCA.Name = "SomewhereNowhere"
		Expect(cl.Update(ctx, proxyResource)).To(Succeed())
		Eventually(checkMergedTrustedCAConfig(2, "GlobalSign")).Should(Succeed())
	})

	It("ca bundle from cloud config should be added if it differs from proxy one", func() {
		Eventually(checkMergedTrustedCAConfig(3, "Amazon")).Should(Succeed())

		msCA, err := os.ReadFile(additionalMsCAPemPath)
		Expect(err).To(Succeed())
		syncedCloudConfigConfigMap.Data = map[string]string{cloudProviderConfigCABundleConfigMapKey: string(msCA)}
		Expect(cl.Update(ctx, syncedCloudConfigConfigMap)).To(Succeed())

		Eventually(checkMergedTrustedCAConfig(4, "Microsoft Corporation")).Should(Succeed())
	})

	It("ca bundle from cloud config should not be added if it is the same as proxy one", func() {
		Eventually(checkMergedTrustedCAConfig(3, "Amazon")).Should(Succeed())

		awsCA, err := os.ReadFile(additionalAmazonCAPemPath)
		Expect(err).To(Succeed())
		syncedCloudConfigConfigMap.Data = map[string]string{cloudProviderConfigCABundleConfigMapKey: string(awsCA)}
		Expect(cl.Update(ctx, syncedCloudConfigConfigMap)).To(Succeed())

		Eventually(checkMergedTrustedCAConfig(3, "Amazon")).Should(Succeed())
	})

	It("proxy ca should still be added to merged bundle in case if cloud-config contains broken one", func() {
		awsCA, err := os.ReadFile(systemCAInvalid)
		Expect(err).To(Succeed())
		syncedCloudConfigConfigMap.Data = map[string]string{cloudProviderConfigCABundleConfigMapKey: string(awsCA)}
		Expect(cl.Update(ctx, syncedCloudConfigConfigMap)).To(Succeed())

		Eventually(checkMergedTrustedCAConfig(3, "Amazon")).Should(Succeed())
	})

	It("cloud-config ca should still be added to merged bundle in case if proxy one contains broken CA", func() {
		additionalCAConfigMap.Data = map[string]string{additionalCAConfigMapKey: "kekekeke"}
		Expect(cl.Update(ctx, additionalCAConfigMap)).To(Succeed())

		msCA, err := os.ReadFile(additionalMsCAPemPath)
		Expect(err).To(Succeed())
		syncedCloudConfigConfigMap.Data = map[string]string{cloudProviderConfigCABundleConfigMapKey: string(msCA)}
		Expect(cl.Update(ctx, syncedCloudConfigConfigMap)).To(Succeed())

		Eventually(checkMergedTrustedCAConfig(3, "Microsoft Corporation")).Should(Succeed())
	})

	It("merged bundle should be generated without cloud-config at all", func() {
		Expect(cl.Delete(ctx, syncedCloudConfigConfigMap)).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(cl.Get(ctx, client.ObjectKeyFromObject(syncedCloudConfigConfigMap), &corev1.ConfigMap{}))
		}).Should(BeTrue())
		syncedCloudConfigConfigMap = nil

		mergedTrustedCA := &corev1.ConfigMap{}
		Eventually(func() error {
			return cl.Get(ctx, mergedCAObjectKey, mergedTrustedCA)
		}).Should(Succeed())
		Expect(cl.Delete(ctx, mergedTrustedCA)).To(Succeed())

		Eventually(checkMergedTrustedCAConfig(3, "Amazon")).Should(Succeed())
	})
})

var _ = Describe("Trusted CA reconciler methods", func() {
	It("Get system CA should be fine if bundle is valid", func() {
		reconciler := &TrustedCABundleReconciler{
			trustBundlePath: systemCAValid,
		}
		_, err := reconciler.getSystemTrustBundle()
		Expect(err).NotTo(HaveOccurred())
	})

	It("Get system CA should return err if bundle is not valid", func() {
		reconciler := &TrustedCABundleReconciler{
			trustBundlePath: systemCAInvalid,
		}
		_, err := reconciler.getSystemTrustBundle()
		Expect(err.Error()).Should(BeEquivalentTo("failed to parse certificate PEM"))
	})

	It("Get system CA should return err if bundle not found", func() {
		reconciler := &TrustedCABundleReconciler{
			trustBundlePath: "/broken/ca/path.pem",
		}
		_, err := reconciler.getSystemTrustBundle()
		Expect(err.Error()).Should(BeEquivalentTo("open /broken/ca/path.pem: no such file or directory"))
	})
})
