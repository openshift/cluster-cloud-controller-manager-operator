package cloud

import (
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
)

type platformNotFoundError struct {
	platform configv1.PlatformType
}

func (p *platformNotFoundError) Error() string {
	return fmt.Sprintf("unrecognized platform type %q found in infrastructure", p.platform)
}

func newPlatformNotFoundError(platform configv1.PlatformType) *platformNotFoundError {
	return &platformNotFoundError{platform: platform}
}
