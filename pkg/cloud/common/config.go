package common

import (
	configv1 "github.com/openshift/api/config/v1"

	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
)

// NoOpTransformer implements the cloudConfigTransformer. It makes no changes
// to the source configuration and simply returns it as-is.
func NoOpTransformer(source string, infra *configv1.Infrastructure, network *configv1.Network, features featuregates.FeatureGate) (string, error) {
	return source, nil
}
