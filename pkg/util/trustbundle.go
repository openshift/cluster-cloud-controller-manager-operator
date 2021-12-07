package util

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

const (
	// certPEMBlock is the type taken from the preamble of a PEM-encoded structure.
	certPEMBlock = "CERTIFICATE"
)

// TrustBundleConfigMap validates that ConfigMap contains a
// trust bundle named aa "caBundleKey" argument and that "caBundleKey"
// contains one or more valid PEM encoded certificates, returning
// a byte slice of "caBundleKey" contents upon success.
func TrustBundleConfigMap(cfgMap *corev1.ConfigMap, caBundleKey string) ([]*x509.Certificate, []byte, error) {
	if _, ok := cfgMap.Data[caBundleKey]; !ok {
		return nil, nil, fmt.Errorf("ConfigMap %q is missing %q", cfgMap.Name, caBundleKey)
	}
	trustBundleData := []byte(cfgMap.Data[caBundleKey])
	if len(trustBundleData) == 0 {
		return nil, nil, fmt.Errorf("data key %q is empty from ConfigMap %q", caBundleKey, cfgMap.Name)
	}
	certBundle, err := CertificateData(trustBundleData)
	if err != nil {
		return nil, nil, fmt.Errorf("failed parsing certificate data from ConfigMap %q: %v", cfgMap.Name, err)
	}

	return certBundle, trustBundleData, nil
}

// CertificateData decodes certData, ensuring each PEM block is type
// "CERTIFICATE" and the block can be parsed as an x509 certificate,
// returning slice of parsed certificates
func CertificateData(certData []byte) ([]*x509.Certificate, error) {
	certBundle := []*x509.Certificate{}
	for len(certData) != 0 {
		var block *pem.Block
		block, certData = pem.Decode(certData)
		if block == nil {
			return nil, fmt.Errorf("failed to parse certificate PEM")
		}
		if block.Type != certPEMBlock {
			return nil, fmt.Errorf("invalid certificate PEM, must be of type %q", certPEMBlock)

		}

		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse certificate: %v", err)
		}
		certBundle = append(certBundle, cert)
	}

	return certBundle, nil
}
