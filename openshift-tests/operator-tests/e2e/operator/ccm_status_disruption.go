package operator

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/client-go/config/clientset/versioned"
	"github.com/openshift/cluster-cloud-controller-manager-operator/openshift-tests/operator-tests/e2e/common"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	cloudControllerManagerOperatorName = "cloud-controller-manager"
	cloudControllerManagerNamespace    = "openshift-cloud-controller-manager"
	openshiftConfigNamespace           = "openshift-config"
	openshiftConfigManagedNamespace    = "openshift-config-managed"
	cloudProviderConfigName            = "cloud-provider-config"
	kubeCloudConfigName                = "kube-cloud-config"
)

var _ = Describe("[Serial][Disruptive][Suite:openshift/ccm/operator/disruptive/serial] cloud-controller-manager operator status", Label("Serial", "Disruptive"), func() {
	var (
		err          error
		kubeClient   *kubernetes.Clientset
		configClient *versioned.Clientset
		platformType configv1.PlatformType
	)

	BeforeEach(func() {
		kubeConfig, configErr := common.NewClientConfigForTest()
		Expect(configErr).NotTo(HaveOccurred(), "failed to load kubeconfig")

		kubeClient = kubernetes.NewForConfigOrDie(rest.AddUserAgent(kubeConfig, clientName))
		configClient = versioned.NewForConfigOrDie(rest.AddUserAgent(kubeConfig, clientName))

		infra, infraErr := configClient.ConfigV1().Infrastructures().Get(context.Background(), "cluster", metav1.GetOptions{})
		Expect(infraErr).NotTo(HaveOccurred(), "failed to get cluster infrastructure")
		platformType = infrastructurePlatformType(infra)

		_, err = configClient.ConfigV1().ClusterOperators().Get(context.Background(), cloudControllerManagerOperatorName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			Skip("cloud-controller-manager clusteroperator is absent on this cluster")
		}
		Expect(err).NotTo(HaveOccurred(), "failed to get cloud-controller-manager clusteroperator")
	})

	It("70621 cloud-controller-manager should be Upgradeable is True when Degraded is False", func(ctx context.Context) {
		skipUnlessSupportedPlatform(platformType,
			configv1.AWSPlatformType,
			configv1.GCPPlatformType,
			configv1.AzurePlatformType,
			configv1.IBMCloudPlatformType,
			configv1.NutanixPlatformType,
			configv1.VSpherePlatformType,
			configv1.OpenStackPlatformType,
		)

		By("Deleting cloud config configmaps while keeping the operator upgradeable during transient recovery")
		originalCloudProviderConfig, err := kubeClient.CoreV1().ConfigMaps(openshiftConfigNamespace).Get(ctx, cloudProviderConfigName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred(), "failed to read %s/%s", openshiftConfigNamespace, cloudProviderConfigName)

		err = kubeClient.CoreV1().ConfigMaps(openshiftConfigNamespace).Delete(ctx, cloudProviderConfigName, metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred(), "failed to delete %s/%s", openshiftConfigNamespace, cloudProviderConfigName)
		DeferCleanup(func(ctx context.Context) {
			By("Restoring cloud-provider-config after the transient disruption")
			restore := restorableConfigMap(originalCloudProviderConfig)
			_, createErr := kubeClient.CoreV1().ConfigMaps(openshiftConfigNamespace).Create(ctx, restore, metav1.CreateOptions{})
			Expect(createErr).NotTo(HaveOccurred(), "failed to restore %s/%s", openshiftConfigNamespace, cloudProviderConfigName)

			err = wait.PollUntilContextTimeout(ctx, 5*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
				co, getErr := configClient.ConfigV1().ClusterOperators().Get(ctx, cloudControllerManagerOperatorName, metav1.GetOptions{})
				if getErr != nil {
					return false, getErr
				}

				degraded := findOperatorConditionStatus(co.Status.Conditions, configv1.OperatorDegraded)
				upgradeable := findOperatorConditionStatus(co.Status.Conditions, configv1.OperatorUpgradeable)
				if degraded == configv1.ConditionFalse && upgradeable == configv1.ConditionTrue {
					return true, nil
				}

				return false, nil
			})
			Expect(err).NotTo(HaveOccurred(), "cloud-controller-manager did not recover to Degraded=False and Upgradeable=True after restoring cloud-provider-config")
		}, ctx)

		err = kubeClient.CoreV1().ConfigMaps(openshiftConfigManagedNamespace).Delete(ctx, kubeCloudConfigName, metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred(), "failed to delete %s/%s", openshiftConfigManagedNamespace, kubeCloudConfigName)

		By("Waiting for kube-cloud-config to be recreated while cloud-controller-manager stays non-degraded and upgradeable")
		err = wait.PollUntilContextTimeout(ctx, 5*time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
			_, getErr := kubeClient.CoreV1().ConfigMaps(openshiftConfigManagedNamespace).Get(ctx, kubeCloudConfigName, metav1.GetOptions{})
			if getErr != nil {
				if apierrors.IsNotFound(getErr) {
					GinkgoWriter.Printf("%s/%s is still absent, retrying\n", openshiftConfigManagedNamespace, kubeCloudConfigName)
					return false, nil
				}
				return false, getErr
			}

			co, getErr := configClient.ConfigV1().ClusterOperators().Get(ctx, cloudControllerManagerOperatorName, metav1.GetOptions{})
			if getErr != nil {
				return false, getErr
			}

			degraded := findOperatorConditionStatus(co.Status.Conditions, configv1.OperatorDegraded)
			upgradeable := findOperatorConditionStatus(co.Status.Conditions, configv1.OperatorUpgradeable)
			if degraded != configv1.ConditionFalse || upgradeable != configv1.ConditionTrue {
				return false, fmt.Errorf("expected cloud-controller-manager to stay Degraded=False and Upgradeable=True after kube-cloud-config recreation, got %q", summarizeOperatorConditions(co.Status.Conditions))
			}

			GinkgoWriter.Printf("%s/%s has been recreated and cloud-controller-manager remains healthy: %s\n", openshiftConfigManagedNamespace, kubeCloudConfigName, summarizeOperatorConditions(co.Status.Conditions))
			return true, nil
		})
		Expect(err).NotTo(HaveOccurred(), "cloud-controller-manager did not remain non-degraded after kube-cloud-config recovery")
	})

	It("70566 Garbage in cloud-controller-manager status", func(ctx context.Context) {
		skipUnlessSupportedPlatform(platformType,
			configv1.AWSPlatformType,
			configv1.AzurePlatformType,
			configv1.GCPPlatformType,
			configv1.AlibabaCloudPlatformType,
			configv1.VSpherePlatformType,
			configv1.IBMCloudPlatformType,
		)

		By("Deleting the cloud-controller-manager namespace to force operator recovery")
		err = kubeClient.CoreV1().Namespaces().Delete(ctx, cloudControllerManagerNamespace, metav1.DeleteOptions{})
		Expect(err).NotTo(HaveOccurred(), "failed to delete namespace %s", cloudControllerManagerNamespace)

		By("Waiting for recovered status to drop stale degraded messages")
		err = wait.PollUntilContextTimeout(ctx, time.Minute, 20*time.Minute, true, func(ctx context.Context) (bool, error) {
			co, getErr := configClient.ConfigV1().ClusterOperators().Get(ctx, cloudControllerManagerOperatorName, metav1.GetOptions{})
			if getErr != nil {
				GinkgoWriter.Printf("retrying while cloud-controller-manager clusteroperator is unavailable: %v\n", getErr)
				return false, nil
			}

			conditionSummary := summarizeOperatorConditions(co.Status.Conditions)
			if strings.Contains(conditionSummary, "TrustedCABundleControllerControllerDegraded condition is set to True") {
				return false, fmt.Errorf("unexpected stale degraded message in recovered cloud-controller-manager status: %s", conditionSummary)
			}

			if strings.Contains(conditionSummary, "Trusted CA Bundle Controller works as expected") {
				GinkgoWriter.Printf("cloud-controller-manager recovered cleanly: %s\n", conditionSummary)
				return true, nil
			}

			GinkgoWriter.Printf("still waiting for a clean recovered status: %s\n", conditionSummary)
			return false, nil
		})
		Expect(err).NotTo(HaveOccurred(), "cloud-controller-manager did not recover with a clean status message")
	})
})

func infrastructurePlatformType(infra *configv1.Infrastructure) configv1.PlatformType {
	if infra.Status.PlatformStatus != nil && infra.Status.PlatformStatus.Type != "" {
		return infra.Status.PlatformStatus.Type
	}

	return infra.Status.Platform
}

func skipUnlessSupportedPlatform(actual configv1.PlatformType, supported ...configv1.PlatformType) {
	for _, platform := range supported {
		if actual == platform {
			return
		}
	}

	Skip(fmt.Sprintf("platform %q is not covered by this disruptive test", actual))
}

func restorableConfigMap(original *corev1.ConfigMap) *corev1.ConfigMap {
	restore := original.DeepCopy()
	restore.ResourceVersion = ""
	restore.UID = ""
	restore.CreationTimestamp = metav1.Time{}
	restore.Generation = 0
	restore.ManagedFields = nil
	restore.OwnerReferences = nil
	restore.Finalizers = nil
	restore.SelfLink = ""
	restore.DeletionTimestamp = nil
	restore.DeletionGracePeriodSeconds = nil
	return restore
}

func findOperatorConditionStatus(conditions []configv1.ClusterOperatorStatusCondition, conditionType configv1.ClusterStatusConditionType) configv1.ConditionStatus {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return condition.Status
		}
	}

	return configv1.ConditionUnknown
}

func summarizeOperatorConditions(conditions []configv1.ClusterOperatorStatusCondition) string {
	parts := make([]string, 0, len(conditions))
	for _, condition := range conditions {
		parts = append(parts, fmt.Sprintf("%s=%s reason=%s message=%s", condition.Type, condition.Status, condition.Reason, condition.Message))
	}

	return strings.Join(parts, "; ")
}
