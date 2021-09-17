package common

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CloudProviderAssets represent cloud provider specific assets object. Such as Azure/Aws/OpenStack/etc.
// Intended to hide provider specific resource management, manifests checks and rendering behind this interface.
type CloudProviderAssets interface {
	// GetRenderedResources intended to return ready to apply list of resources.
	// Expected that resources returned by this method is completely prepared and do not need additional handling.
	GetRenderedResources() []client.Object
}
