package tls

import (
	"context"
	"crypto/tls"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

func TestTLSFlagRegistrationAndOverride(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		nil,
		{"--tls-min-version=VersionTLS12"},
		{"--tls-cipher-suites=TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"},
		{"--tls-curve-preferences=4588,29,23,24"},
		{"--tls-min-version=VersionTLS13", "--tls-cipher-suites=TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256", "--tls-curve-preferences=4587,4589"},
	} {
		t.Run(stringName(args), func(t *testing.T) {
			t.Parallel()
			flags := pflag.NewFlagSet("operator", pflag.ContinueOnError)
			options := RegisterTLSFlags(flags)
			require.NoError(t, flags.Parse(args))
			require.Equal(t, len(args) > 0, options.OverridesProfile())
			if !options.OverridesProfile() {
				return
			}
			result, err := ResolveTLSConfig(context.Background(), nil, options.MinVersion, options.CipherSuites, options.CurvePreferences)
			require.NoError(t, err)
			resolved := &tls.Config{}
			result.TLSConfig(resolved)
			require.Empty(t, result.TLSProfileSpec)
			require.Empty(t, result.TLSAdherencePolicy)
			if len(options.CurvePreferences) == 0 {
				require.Nil(t, resolved.CurvePreferences)
			}
		})
	}
}

func stringName(args []string) string {
	if len(args) == 0 {
		return "no overrides"
	}
	return args[0]
}
