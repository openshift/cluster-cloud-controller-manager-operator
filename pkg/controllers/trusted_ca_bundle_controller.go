package controllers

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"reflect"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/cert"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

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
	Scheme          *runtime.Scheme
	trustBundlePath string
}

func (r *TrustedCABundleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	err := r.manageCCMCABundle(ctx)
	if err != nil {
		klog.ErrorS(err, "Error updating Cloud Controller Manager CA bundle.")
	}

	// update status
	if err != nil {
		if r.setDegradedCondition(ctx) != nil {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, nil
	}
	if r.setAvailableCondition(ctx) != nil {
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{}, err
}

func (r *TrustedCABundleReconciler) makeCABundleConfigMap(trustBundle []byte) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      trustedCAConfigMapName,
			Namespace: r.ManagedNamespace,
		},
		Data: map[string]string{
			trustedCABundleConfigMapKey: string(trustBundle),
		},
	}
}

func (r *TrustedCABundleReconciler) createOrUpdateConfigMap(ctx context.Context, cm *corev1.ConfigMap) error {
	// check if target config exists, create if not
	err := r.Get(ctx, client.ObjectKeyFromObject(cm), &corev1.ConfigMap{})
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, cm)
	}
	if err != nil {
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

// SetupWithManager sets up the controller with the Manager.
func (r *TrustedCABundleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	build := ctrl.NewControllerManagedBy(mgr).
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
		Watches(&configv1.Proxy{}, &handler.EnqueueRequestForObject{}).
		Watches(&configv1.ClusterOperator{}, &handler.EnqueueRequestForObject{}, builder.WithPredicates(clusterOperatorPredicates()))

	return build.Complete(r)
}

func (r *TrustedCABundleReconciler) setAvailableCondition(ctx context.Context) error {
	co, err := r.getOrCreateClusterOperator(ctx)
	if err != nil {
		return err
	}
	// TODO this controller should only be setting the xxxDegraded condition to True/False and should not have an xxxAvailable condition.
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
	// TODO this controller should only be setting the xxxDegraded condition to True/False and should not have an xxxAvailable condition.
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
func (r *TrustedCABundleReconciler) manageCCMCABundle(ctx context.Context) error {
	var allCerts []*x509.Certificate

	// certs from "could-conf.Data[ca-bundle.pem]", might not exist, ignore malformed
	certs, err := r.getCloudConfCABundleCerts(ctx)
	if err != nil {
		return err
	}
	allCerts = append(allCerts, certs...)

	// certs from cluster proxy configuration, might not exist, ignore malformed
	certs, err = r.getUserCABundleCerts(ctx)
	if err != nil {
		return err
	}
	allCerts = append(allCerts, certs...)

	// certs from sytem trust bundle certs
	certs, err = r.getSystemCABundleCerts()
	if err != nil {
		return err
	}
	allCerts = append(allCerts, certs...)

	// de-duplicate certs
	allCerts = deduplicateCerts(allCerts)

	// make the configmap
	data, err := cert.EncodeCertificates(allCerts...)
	if err != nil {
		return err
	}
	cm := r.makeCABundleConfigMap(data)

	err = r.createOrUpdateConfigMap(ctx, cm)
	return err

}

func (r *TrustedCABundleReconciler) getSystemCABundleCerts() ([]*x509.Certificate, error) {
	path := r.getTrustBundlePath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("unable to read file %q", path)
	}
	return util.CertificateData(data)
}

func (r *TrustedCABundleReconciler) getCloudConfCABundleCerts(ctx context.Context) ([]*x509.Certificate, error) {
	return r.getCertsFromConfigMap(ctx, r.ManagedNamespace, "cloud-conf", "ca-bundle.pem")
}

func (r *TrustedCABundleReconciler) getCertsFromConfigMap(ctx context.Context, ns, name, key string) ([]*x509.Certificate, error) {
	cm := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, cm)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("configmap/%s in %q could not be read: %v", name, ns, err)
	}
	data := cm.Data[key]
	if len(data) == 0 {
		return nil, nil
	}
	certs, err := cert.ParseCertsPEM([]byte(data))
	if err != nil {
		// this should throw error so operator can go degraded, but previous impl of this controller just ignored it
		klog.Errorf("configmap/%s.Data[%s] in %q is malformed: %v", name, key, ns, err)
		return nil, nil
	}
	return certs, nil
}

func (r *TrustedCABundleReconciler) getUserCABundleCerts(ctx context.Context) ([]*x509.Certificate, error) {
	proxy := &configv1.Proxy{}
	err := r.Get(ctx, types.NamespacedName{Name: "cluster"}, proxy)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("proxy/cluster could not be read: %v", err)
	}
	if len(proxy.Spec.TrustedCA.Name) == 0 {
		return nil, nil
	}
	return r.getCertsFromConfigMap(ctx, "openshift-config", proxy.Spec.TrustedCA.Name, "ca-bundle.crt")
}

func deduplicateCerts(certs []*x509.Certificate) []*x509.Certificate {
	var uniq []*x509.Certificate
	for i := range certs {
		found := false
		for j := range uniq {
			if reflect.DeepEqual(certs[i].Raw, uniq[j].Raw) {
				found = true
				break
			}
		}
		if !found {
			uniq = append(uniq, certs[i])
		}
	}
	return uniq
}
