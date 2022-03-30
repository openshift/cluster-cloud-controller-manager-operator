package common

import (
	configv1 "github.com/openshift/api/config/v1"
)

// NoOpTransformer implements the cloudConfigTransformer. It makes no changes
// to the source configuration and simply returns it as-is.
func NoOpTransformer(source string, infra *configv1.Infrastructure) (string, error) {
	return source, nil
}
