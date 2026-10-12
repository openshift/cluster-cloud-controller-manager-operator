package tls

import (
	"strings"

	"github.com/spf13/pflag"
	cliflag "k8s.io/component-base/cli/flag"
)

type TLSFlags struct {
	MinVersion       string
	CipherSuites     []string
	CurvePreferences []int32
}

func RegisterTLSFlags(flags *pflag.FlagSet) *TLSFlags {
	options := &TLSFlags{}
	flags.StringVar(&options.MinVersion, "tls-min-version", "", "Minimum TLS version supported. When set, overrides the cluster-wide TLS profile. Possible values: "+strings.Join(cliflag.TLSPossibleVersions(), ", "))
	flags.StringSliceVar(&options.CipherSuites, "tls-cipher-suites", nil, "Comma-separated list of cipher suites for the server. When set, overrides the cluster-wide TLS profile. Possible values: "+strings.Join(cliflag.TLSCipherPossibleValues(), ", "))
	flags.Int32SliceVar(&options.CurvePreferences, "tls-curve-preferences", nil, "Comma-separated decimal TLS group IDs. When set, overrides the entire cluster-wide TLS profile.")
	return options
}

func (options TLSFlags) OverridesProfile() bool {
	return HasTLSOverrides(options.MinVersion, options.CipherSuites, options.CurvePreferences)
}
