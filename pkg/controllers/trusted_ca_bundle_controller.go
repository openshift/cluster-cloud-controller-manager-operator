package controllers

import (
	"context"
	"crypto/x509"
	"fmt"
	"io/ioutil"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/util"
)

const (
	trustedCAConfigMapName      = "ccm-trusted-ca"
	trustedCABundleConfigMapKey = "ca-bundle.crt"
	systemTrustBundlePath       = "/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem"
)

type TrustedCABundleReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Recorder        record.EventRecorder
	TargetNamespace string
	trustBundlePath string
}

// isSpecTrustedCASet returns true if spec.trustedCA of proxyConfig is set.
func isSpecTrustedCASet(proxyConfig *configv1.ProxySpec) bool {
	return len(proxyConfig.TrustedCA.Name) > 0
}

func (r *TrustedCABundleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.Infof("%s emitted event, syncing %s ConfigMap", req, trustedCAConfigMapName)

	proxyConfig := &configv1.Proxy{}
	if err := r.Get(ctx, types.NamespacedName{Name: proxyResourceName}, proxyConfig); err != nil {
		if apierrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			klog.Infof("proxy not found; reconciliation will be skipped")
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, fmt.Errorf("failed to get proxy '%s': %v", req.Name, err)
	}

	// check if changed config map in 'openshift-config' namespace is proxy trusted ca
	if req.Namespace == OpenshiftConfigNamespace {
		if proxyConfig.Spec.TrustedCA.Name != req.Name {
			klog.Infof("changed config map %s is not a proxy trusted ca, skipping", req)
			return reconcile.Result{}, nil
		}
	}

	systemTrustBundle, err := r.getSystemTrustBundle()
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("failed to get system trust bundle: %v", err)
	}
	ccmTrustedConfigMap := r.makeCABundleConfigMap(systemTrustBundle)

	if isSpecTrustedCASet(&proxyConfig.Spec) {
		userCABundle, err := r.getUserCABundle(ctx, proxyConfig.Spec.TrustedCA.Name)
		if err != nil {
			klog.Warningf("failed to get user defined trust bundle, system CA will be used: %v", err)
		} else {
			mergedTrustBundle, err := r.mergeCABundles(userCABundle, systemTrustBundle)
			if err != nil {
				return reconcile.Result{}, fmt.Errorf("can not merge system and user trust bundles: %v", err)
			}
			ccmTrustedConfigMap = r.makeCABundleConfigMap(mergedTrustBundle)
		}
	}

	if err := r.createOrUpdateConfigMap(ctx, ccmTrustedConfigMap); err != nil {
		return reconcile.Result{}, fmt.Errorf("can not update target trust bundle configmap: %v", err)
	}

	return ctrl.Result{}, nil
}

func (r *TrustedCABundleReconciler) getUserCABundle(ctx context.Context, trustedCA string) ([]byte, error) {
	cfgMap, err := r.getUserCABundleConfigMap(ctx, trustedCA)
	if err != nil {
		return nil, fmt.Errorf("failed to validate configmap reference for proxy trustedCA '%s': %v",
			trustedCA, err)
	}

	_, bundleData, err := r.getCABundleConfigMapData(cfgMap)
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

func (r *TrustedCABundleReconciler) getCABundleConfigMapData(cfgMap *corev1.ConfigMap) ([]*x509.Certificate, []byte, error) {
	certBundle, bundleData, err := util.TrustBundleConfigMap(cfgMap)
	if err != nil {
		return nil, nil, err
	}

	return certBundle, bundleData, nil
}

func (r *TrustedCABundleReconciler) makeCABundleConfigMap(trustBundle []byte) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      trustedCAConfigMapName,
			Namespace: r.TargetNamespace,
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
	bundleData, err := ioutil.ReadFile(r.getTrustBundlePath())
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
		For(
			&corev1.ConfigMap{},
			builder.WithPredicates(
				predicate.Or(
					openshiftConfigNamespacedPredicate(),
					ccmTrustedCABundleConfigMapPredicates(r.TargetNamespace),
				),
			),
		).
		Watches(
			&source.Kind{Type: &configv1.Proxy{}},
			&handler.EnqueueRequestForObject{},
		)

	return build.Complete(r)
}
