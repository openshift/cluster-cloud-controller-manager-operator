package aws

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
)

const (
	healthserverTLSConfigMap = "healthserver-tls"
	healthserverTLSCertFile  = "tls.crt"
	healthserverTLSKeyFile   = "tls.key"
	healthserverTLSMountPath = "/etc/healthserver-tls"
)

// generateSelfSignedTLSCert creates a self-signed cert/key pair for test use.
// The cert includes a wildcard DNS SAN so NLB HC and clients are not blocked.
func generateSelfSignedTLSCert() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("generate serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "healthserver-e2e"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"*"},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create certificate: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	return certPEM, keyPEM, nil
}

// ensureHealthserverTLSConfigMap creates or updates a ConfigMap with a
// self-signed TLS cert/key for the healthserver DaemonSet.
func ensureHealthserverTLSConfigMap(ctx context.Context, cs clientset.Interface, namespace string) error {
	certPEM, keyPEM, err := generateSelfSignedTLSCert()
	if err != nil {
		return err
	}

	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      healthserverTLSConfigMap,
			Namespace: namespace,
		},
		Data: map[string]string{
			healthserverTLSCertFile: string(certPEM),
			healthserverTLSKeyFile:  string(keyPEM),
		},
	}

	existing, getErr := cs.CoreV1().ConfigMaps(namespace).Get(ctx, healthserverTLSConfigMap, metav1.GetOptions{})
	if getErr != nil {
		_, err = cs.CoreV1().ConfigMaps(namespace).Create(ctx, cm, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create TLS configmap: %w", err)
		}
		framework.Logf("created TLS configmap %s/%s", namespace, healthserverTLSConfigMap)
		return nil
	}

	existing.Data = cm.Data
	_, err = cs.CoreV1().ConfigMaps(namespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update TLS configmap: %w", err)
	}
	framework.Logf("updated TLS configmap %s/%s", namespace, healthserverTLSConfigMap)
	return nil
}
