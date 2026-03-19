package controllers

import (
	"bytes"
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	"github.com/openshift/api/annotations"
	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/util"
)

const (
	trustedCAConfigMapName      = "ccm-trusted-ca"
	trustedCABundleConfigMapKey = "ca-bundle.crt"
	// key in cloud-provider config is different for some reason.
	// https://github.com/openshift/installer/blob/master/pkg/asset/manifests/cloudproviderconfig.go#L41
	// https://github.com/openshift/installer/blob/master/pkg/asset/manifests/cloudproviderconfig.go#L99
	cloudProviderConfigCABundleConfigMapKey = "ca-bundle.pem"
	systemTrustBundlePath                   = "/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem"

	// Controller conditions for the Cluster Operator resource
	trustedCABundleControllerAvailableCondition = "TrustedCABundleControllerControllerAvailable"
	trustedCABundleControllerDegradedCondition  = "TrustedCABundleControllerControllerDegraded"
)

type TrustedCABundleReconciler struct {
	ClusterOperatorStatusClient
	Scheme                  *runtime.Scheme
	trustBundlePath         string
	consecutiveFailureSince *time.Time // nil when the last reconcile succeeded
	lastTransientFailureAt  *time.Time // when the most recent transient error was observed
}

// isSpecTrustedCASet returns true if spec.trustedCA of proxyConfig is set.
func isSpecTrustedCASet(proxyConfig *configv1.ProxySpec) bool {
	return len(proxyConfig.TrustedCA.Name) > 0
}

func (r *TrustedCABundleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	klog.V(1).Infof("%s emitted event, syncing %s ConfigMap", req, trustedCAConfigMapName)

	// partialRun is set to true on the early-exit path where the event is for
	// an unrelated ConfigMap. That path returns available=true but should NOT
	// reset an ongoing transient failure window from a previous full reconcile.
	partialRun := false

	// Deferred dispatcher: classifies the returned error and calls the right handler.
	// Permanent errors (wrapped with permanent()) degrade immediately without requeue.
	// Transient errors enter the failure window and only degrade after the threshold.
	// Nil-error paths clear the failure window unless partialRun is set.
	defer func() {
		if retErr == nil {
			if !partialRun {
				r.clearFailureWindow()
			}
			return
		}
		if isPermanent(retErr) {
			result, retErr = r.handleDegradeError(ctx, retErr)
		} else {
			result, retErr = r.handleTransientError(ctx, retErr)
		}
	}()

	proxyConfig := &configv1.Proxy{}
	if err := r.Get(ctx, types.NamespacedName{Name: proxyResourceName}, proxyConfig); err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			klog.Infof("proxy not found; reconciliation will be skipped")
			if err := r.setAvailableCondition(ctx); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to set conditions for trusted CA bundle controller: %v", err)
			}
			return reconcile.Result{}, nil // defer clears failure window
		}
		// Non-NotFound: transient API error.
		return ctrl.Result{}, fmt.Errorf("failed to get proxy '%s': %v", req.Name, err) // transient
	}

	// Check if changed config map in 'openshift-config' namespace is proxy trusted ca.
	// If not, return early without resetting the failure window (partialRun=true).
	if req.Namespace == OpenshiftConfigNamespace && proxyConfig.Spec.TrustedCA.Name != req.Name {
		partialRun = true
		klog.V(1).Infof("changed config map %s is not a proxy trusted ca, skipping", req)
		if err := r.setAvailableCondition(ctx); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to set conditions for trusted CA bundle controller: %v", err)
		}
		return reconcile.Result{}, nil
	}

	systemTrustBundle, err := r.getSystemTrustBundle()
	if err != nil {
		// Node cert store may be updating during upgrade: transient.
		return ctrl.Result{}, fmt.Errorf("failed to get system trust bundle: %v", err) // transient
	}

	proxyCABundle, mergedTrustBundle, err := r.addProxyCABundle(ctx, proxyConfig, systemTrustBundle)
	if err != nil {
		// Combined cert bundle is corrupt: permanent.
		return ctrl.Result{}, permanent(fmt.Errorf("can not check and add proxy CA to merged bundle: %v", err))
	}

	_, mergedTrustBundle, err = r.addCloudConfigCABundle(ctx, proxyCABundle, mergedTrustBundle)
	if err != nil {
		// Combined cert bundle is corrupt: permanent.
		return ctrl.Result{}, permanent(fmt.Errorf("can not check and add cloud-config CA to merged bundle: %v", err))
	}

	ccmTrustedConfigMap := r.makeCABundleConfigMap(mergedTrustBundle)
	if err := r.createOrUpdateConfigMap(ctx, ccmTrustedConfigMap); err != nil {
		return ctrl.Result{}, fmt.Errorf("can not update target trust bundle configmap: %v", err) // transient
	}

	if err := r.setAvailableCondition(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set conditions for trusted CA bundle controller: %v", err)
	}

	return ctrl.Result{}, nil // defer clears failure window
}

func (r *TrustedCABundleReconciler) clearFailureWindow() {
	r.consecutiveFailureSince = nil
	r.lastTransientFailureAt = nil
}

// handleTransientError records the start of a failure window and degrades the
// controller only after transientDegradedThreshold has elapsed. It always
// returns a non-nil error so controller-runtime requeues with exponential backoff.
// Called only from the deferred dispatcher in Reconcile.
func (r *TrustedCABundleReconciler) handleTransientError(ctx context.Context, err error) (ctrl.Result, error) {
	now := r.Clock.Now()

	// Start or restart the failure window.
	// Restart if: (a) no window is open, OR (b) the last observed failure was more than
	// transientDegradedThreshold ago (stale window). Case (b) handles the scenario where
	// a partialRun reconcile returned nil (no requeue) after the system recovered, leaving
	// consecutiveFailureSince set but no subsequent successful full reconcile to clear it.
	staleWindow := r.lastTransientFailureAt != nil && now.Sub(*r.lastTransientFailureAt) > transientDegradedThreshold
	if r.consecutiveFailureSince == nil || staleWindow {
		r.consecutiveFailureSince = &now
		r.lastTransientFailureAt = &now
		klog.V(4).Infof("TrustedCABundleReconciler: transient failure started (%v), will degrade after %s", err, transientDegradedThreshold)
		return ctrl.Result{}, err
	}

	r.lastTransientFailureAt = &now
	elapsed := now.Sub(*r.consecutiveFailureSince)
	if elapsed < transientDegradedThreshold {
		klog.V(4).Infof("TrustedCABundleReconciler: transient failure ongoing for %s (threshold %s): %v", elapsed, transientDegradedThreshold, err)
		return ctrl.Result{}, err
	}
	klog.Warningf("TrustedCABundleReconciler: transient failure exceeded threshold (%s), setting degraded: %v", elapsed, err)
	if setErr := r.setDegradedCondition(ctx); setErr != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set degraded condition: %v", setErr)
	}
	return ctrl.Result{}, err
}

// handleDegradeError sets TrustedCABundleControllerControllerDegraded=True immediately and
// returns nil so controller-runtime does NOT requeue. An existing watch on the
// relevant resource will re-trigger reconciliation when the problem is fixed.
// Called only from the deferred dispatcher in Reconcile.
func (r *TrustedCABundleReconciler) handleDegradeError(ctx context.Context, err error) (ctrl.Result, error) {
	klog.Errorf("TrustedCABundleReconciler: persistent error, setting degraded: %v", err)
	if setErr := r.setDegradedCondition(ctx); setErr != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set degraded condition: %v", setErr)
	}
	return ctrl.Result{}, nil
}

// addProxyCABundle checks ca bundle referred by Proxy resource and adds it to passed bundle
// in case if proxy one is valid.
// This function returns added bundle as first value, result as second and an error if it was occurred.
func (r *TrustedCABundleReconciler) addProxyCABundle(ctx context.Context, proxyConfig *configv1.Proxy, originalCABundle []byte) ([]byte, []byte, error) {
	if isSpecTrustedCASet(&proxyConfig.Spec) {
		userProxyCABundle, err := r.getUserProxyCABundle(ctx, proxyConfig.Spec.TrustedCA.Name)
		if err != nil {
			klog.Warningf("failed to get user defined proxy trust bundle, system CA will be used: %v", err)
			return nil, originalCABundle, nil
		}
		resultCABundle, err := r.mergeCABundles(userProxyCABundle, originalCABundle)
		if err != nil {
			return userProxyCABundle, nil, fmt.Errorf("can not merge system and user trust bundles: %v", err)
		}
		return userProxyCABundle, resultCABundle, nil
	}
	return nil, originalCABundle, nil
}

// addCloudConfigCABundle checks cloud-config for additional CA bundle presence and adds it to passed bundle
// in case found one is valid.
// This function returns added bundle as first value, result as second and an error if it was occurred.
// Note: missed cloud-config not considered an error, because no cloud-config is expected on some platforms (AWS)
func (r *TrustedCABundleReconciler) addCloudConfigCABundle(ctx context.Context, proxyCABundle []byte, originalCABundle []byte) ([]byte, []byte, error) {
	// Due to installer implementation nuances, 'additionalTrustBundle' does not always end up in Proxy object.
	// For handling this situation we have to check synced cloud-config for additional CA bundle presence.
	// See https://github.com/openshift/installer/pull/5251#issuecomment-932622321 and
	// https://github.com/openshift/installer/pull/5248 for additional context.
	// However, some platforms might not have cloud-config at all (AWS), so missed cloud config is not an error.
	ccmSyncedCloudConfig := &corev1.ConfigMap{}
	syncedCloudConfigObjectKey := types.NamespacedName{Name: syncedCloudConfigMapName, Namespace: r.ManagedNamespace}
	if err := r.Get(ctx, syncedCloudConfigObjectKey, ccmSyncedCloudConfig); err != nil {
		klog.Infof("cloud-config was not found: %v", err)
		return nil, originalCABundle, nil
	}

	_, found := ccmSyncedCloudConfig.Data[cloudProviderConfigCABundleConfigMapKey]
	if found {
		klog.Infof("additional CA bundle key found in cloud-config")
		_, cloudConfigCABundle, err := r.getCABundleConfigMapData(ccmSyncedCloudConfig, cloudProviderConfigCABundleConfigMapKey)
		if err != nil {
			klog.Warningf("failed to parse additional CA bundle from cloud-config, system and proxy CAs will be used: %v", err)
			return nil, originalCABundle, nil
		}
		if bytes.Equal(proxyCABundle, cloudConfigCABundle) {
			klog.Infof("proxy CA and cloud-config CA bundles are equal, no need to merge")
			return nil, originalCABundle, nil
		}
		klog.Infof("proxy CA and cloud-config CA bundles are not equal, merging")
		mergedCABundle, err := r.mergeCABundles(cloudConfigCABundle, originalCABundle)
		if err != nil {
			return cloudConfigCABundle, nil, fmt.Errorf("can not merge system and user trust bundle from cloud-config: %v", err)
		}
		return cloudConfigCABundle, mergedCABundle, nil
	}
	return nil, originalCABundle, nil
}

func (r *TrustedCABundleReconciler) getUserProxyCABundle(ctx context.Context, trustedCA string) ([]byte, error) {
	cfgMap, err := r.getUserCABundleConfigMap(ctx, trustedCA)
	if err != nil {
		return nil, fmt.Errorf("failed to validate configmap reference for proxy trustedCA '%s': %v",
			trustedCA, err)
	}

	_, bundleData, err := r.getCABundleConfigMapData(cfgMap, trustedCABundleConfigMapKey)
	if err != nil {
		return nil, fmt.Errorf("failed to validate trust bundle for proxy trustedCA '%s': %v",
			trustedCA, err)
	}

	return bundleData, nil
}

func (r *TrustedCABundleReconciler) getUserCABundleConfigMap(ctx context.Context, trustedCA string) (*corev1.ConfigMap, error) {
	cfgMap := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: OpenshiftConfigNamespace, Name: trustedCA}, cfgMap); err != nil {
		return nil, fmt.Errorf("failed to get trustedCA configmap for proxy %s: %v", proxyResourceName, err)
	}

	return cfgMap, nil
}

func (r *TrustedCABundleReconciler) getCABundleConfigMapData(cfgMap *corev1.ConfigMap, caBundleKey string) ([]*x509.Certificate, []byte, error) {
	certBundle, bundleData, err := util.TrustBundleConfigMap(cfgMap, caBundleKey)
	if err != nil {
		return nil, nil, err
	}

	return certBundle, bundleData, nil
}

func (r *TrustedCABundleReconciler) makeCABundleConfigMap(trustBundle []byte) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      trustedCAConfigMapName,
			Namespace: r.ManagedNamespace,
			Annotations: map[string]string{
				annotations.OpenShiftComponent: "Cloud Compute / Cloud Controller Manager",
			},
		},
		Data: map[string]string{
			trustedCABundleConfigMapKey: string(trustBundle),
		},
	}
}

func (r *TrustedCABundleReconciler) createOrUpdateConfigMap(ctx context.Context, cm *corev1.ConfigMap) error {
	// check if target config exists, create if not
	err := r.Get(ctx, client.ObjectKeyFromObject(cm), &corev1.ConfigMap{})
	if err != nil && apierrors.IsNotFound(err) {
		return r.Create(ctx, cm)
	} else if err != nil {
		return err
	}

	return r.Update(ctx, cm)
}

// for test purposes only, normally it returns value from 'trustBundlePath' constant in this module
func (r *TrustedCABundleReconciler) getTrustBundlePath() string {
	if r.trustBundlePath != "" {
		return r.trustBundlePath
	}
	return systemTrustBundlePath
}

func (r *TrustedCABundleReconciler) getSystemTrustBundle() ([]byte, error) {
	bundleData, err := os.ReadFile(r.getTrustBundlePath())
	if err != nil {
		return nil, err
	}
	_, err = util.CertificateData(bundleData)
	if err != nil {
		return nil, err
	}

	return bundleData, nil
}

func (r *TrustedCABundleReconciler) mergeCABundles(additionalData, systemData []byte) ([]byte, error) {
	if len(additionalData) == 0 {
		return nil, fmt.Errorf("failed to merge ca bundles, additional trust bundle is empty")
	}
	if len(systemData) == 0 {
		return nil, fmt.Errorf("failed to merge ca bundles, system trust bundle is empty")
	}

	combinedTrustData := []byte{}
	combinedTrustData = append(combinedTrustData, additionalData...)
	combinedTrustData = append(combinedTrustData, []byte("\n")...)
	combinedTrustData = append(combinedTrustData, systemData...)

	if _, err := util.CertificateData(combinedTrustData); err != nil {
		return nil, err
	}

	return combinedTrustData, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TrustedCABundleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	build := ctrl.NewControllerManagedBy(mgr).
		Named("TrustedCABundleController").
		For(
			&corev1.ConfigMap{},
			builder.WithPredicates(
				predicate.Or(
					openshiftConfigNamespacedPredicate(),
					ccmTrustedCABundleConfigMapPredicates(r.ManagedNamespace),
					ownCloudConfigPredicate(r.ManagedNamespace),
				),
			),
		).
		Watches(
			&configv1.Proxy{},
			&handler.EnqueueRequestForObject{},
		)

	return build.Complete(r)
}

func (r *TrustedCABundleReconciler) setAvailableCondition(ctx context.Context) error {
	co, err := r.getOrCreateClusterOperator(ctx)
	if err != nil {
		return err
	}

	conds := []configv1.ClusterOperatorStatusCondition{
		newClusterOperatorStatusCondition(trustedCABundleControllerAvailableCondition, configv1.ConditionTrue, ReasonAsExpected,
			"Trusted CA Bundle Controller works as expected"),
		newClusterOperatorStatusCondition(trustedCABundleControllerDegradedCondition, configv1.ConditionFalse, ReasonAsExpected,
			"Trusted CA Bundle Controller works as expected"),
	}

	co.Status.Versions = []configv1.OperandVersion{{Name: operatorVersionKey, Version: r.ReleaseVersion}}
	klog.V(1).Info("Trusted CA Bundle Controller is available")
	return r.syncStatus(ctx, co, conds, nil)
}

func (r *TrustedCABundleReconciler) setDegradedCondition(ctx context.Context) error {
	co, err := r.getOrCreateClusterOperator(ctx)
	if err != nil {
		return err
	}

	conds := []configv1.ClusterOperatorStatusCondition{
		newClusterOperatorStatusCondition(trustedCABundleControllerAvailableCondition, configv1.ConditionFalse, ReasonSyncFailed,
			"Trusted CA Bundle Controller failed to sync cloud config"),
		newClusterOperatorStatusCondition(trustedCABundleControllerDegradedCondition, configv1.ConditionTrue, ReasonSyncFailed,
			"Trusted CA Bundle Controller failed to sync cloud config"),
	}

	co.Status.Versions = []configv1.OperandVersion{{Name: operatorVersionKey, Version: r.ReleaseVersion}}
	klog.Info("Trusted CA Bundle Controller is degraded")
	return r.syncStatus(ctx, co, conds, nil)
}
