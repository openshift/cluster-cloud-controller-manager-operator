package util

import (
	"fmt"
	"strings"

	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	upstreamfeature "k8s.io/component-base/featuregate"
	cloudfeatures "k8s.io/controller-manager/pkg/features"
)

// GetEnabledDisabledFeatures returns two slices that contain all the known feature gates
// and separates them by their enabled/disabled state. It has ability to filter results
// by using a provided list of whitelisted names. It is useful when not all the features
// are allowed to be used in a specific context.
func GetEnabledDisabledFeatures(features featuregates.FeatureGate, filter []string) ([]string, []string) {
	var enabled []string
	var disabled []string

	for _, feature := range features.KnownFeatures() {
		if features.Enabled(feature) {
			enabled = append(enabled, string(feature))
		} else {
			disabled = append(disabled, string(feature))
		}
	}

	if filter != nil {
		enabled = filterStringsByNames(enabled, filter)
		disabled = filterStringsByNames(disabled, filter)
	}

	return enabled, disabled
}

// BuildFeatureGateString takes slices of enabled and disabled feature gates and returns a string
// that can be passed as a cmd param "--feature-gates=" to the Cloud Provider. Returned string
// will be formated in a way that it can be passed as-is, i.e. enabled features will get "=true"
// suffix and disabled "=false". E.g. "ChocobombStrawberry=true,ChocobombBanana=false".
func BuildFeatureGateString(enabled, disabled []string) string {
	for i, x := range enabled {
		enabled[i] = x + "=true"
	}
	for i, x := range disabled {
		disabled[i] = x + "=false"
	}

	return strings.Join(append(enabled, disabled...), ",")
}

// GetUpstreamCloudFeatureGates returns a list of feature gates that are allowed to be used in the
// context of cloud provider.
func GetUpstreamCloudFeatureGates() ([]string, error) {
	var result []string
	upstreamFeatureGate := upstreamfeature.NewFeatureGate()
	err := cloudfeatures.SetupCurrentKubernetesSpecificFeatureGates(upstreamFeatureGate)
	if err != nil {
		return nil, fmt.Errorf("unable to get upstream feature gates: %w", err)

	}

	for feature := range upstreamFeatureGate.GetAll() {
		result = append(result, string(feature))
	}
	return result, nil
}

func filterStringsByNames(features []string, filter []string) []string {
	var result []string
	for _, feature := range features {
		for _, allowed := range filter {
			if feature == allowed {
				result = append(result, feature)
			}
		}
	}
	return result
}
