package config

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	utiltls "github.com/openshift/controller-runtime-common/pkg/tls"
	"github.com/stretchr/testify/require"
)

func TestTLSConsumerCapabilities(t *testing.T) {
	t.Parallel()
	supported := []TLSConsumer{AWSCCM, AzureCCM, AzureStackCCM, GCPCCM, IBMCCM, OpenStackCCM, VSphereCCM, AzureNodeManager, AzureStackNodeManager}
	for _, consumer := range supported {
		t.Run(string(consumer), func(t *testing.T) {
			t.Parallel()
			require.Equal(t, "4588,29,23,24", (OperatorConfig{TLSCurvePreferences: "4588,29,23,24"}).TLSCurvePreferencesFor(consumer))
			require.Empty(t, (OperatorConfig{}).TLSCurvePreferencesFor(consumer))
		})
	}
	for _, consumer := range []TLSConsumer{NutanixCCM, PowerVSCCM, "unknown", ""} {
		t.Run(string(consumer), func(t *testing.T) {
			t.Parallel()
			require.Empty(t, (OperatorConfig{TLSCurvePreferences: "4588,29,23,24"}).TLSCurvePreferencesFor(consumer))
		})
	}
}

func TestComposeConfigCurvePreferences(t *testing.T) {
	t.Parallel()
	images := filepath.Join(t.TempDir(), "images.json")
	require.NoError(t, os.WriteFile(images, []byte(`{}`), 0600))
	infra := &configv1.Infrastructure{Status: configv1.InfrastructureStatus{PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType}}}
	for _, testCase := range []struct {
		name   string
		curves []tls.CurveID
		want   string
	}{
		{"nil", nil, ""},
		{"empty", []tls.CurveID{}, ""},
		{"ordered", []tls.CurveID{4588, 29, 23, 24}, "4588,29,23,24"},
		{"all seven", []tls.CurveID{29, 23, 24, 25, 4588, 4587, 4589}, "29,23,24,25,4588,4587,4589"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
				configure := func(cfg *tls.Config) {
					cfg.MinVersion = version
					cfg.CipherSuites = []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}
					cfg.CurvePreferences = testCase.curves
				}
				first, err := ComposeConfig(infra, nil, images, "test", nil, configure)
				require.NoError(t, err)
				require.Equal(t, testCase.want, first.TLSCurvePreferences)
				second, err := ComposeConfig(infra, nil, images, "test", nil, configure)
				require.NoError(t, err)
				require.Equal(t, first, second)
				if version == tls.VersionTLS13 {
					require.Empty(t, first.TLSCipherSuites)
				}
			}
		})
	}
	configure, _ := utiltls.NewTLSConfigFromProfile(*configv1.TLSProfiles[configv1.TLSProfileIntermediateType])
	resolved, err := ComposeConfig(infra, nil, images, "test", nil, configure)
	require.NoError(t, err)
	require.NotEmpty(t, resolved.TLSCurvePreferences)
	absent, err := ComposeConfig(infra, nil, images, "test", nil, nil)
	require.NoError(t, err)
	require.Empty(t, absent.TLSCurvePreferences)
}
