package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetResources(t *testing.T) {
	resources := GetResources()
	assert.Len(t, resources, 2)

	var names, kinds []string
	for _, r := range resources {
		names = append(names, r.GetName())
		kinds = append(kinds, r.GetObjectKind().GroupVersionKind().Kind)
	}

	assert.Contains(t, names, "azure-cloud-controller-manager")
	assert.Contains(t, kinds, "Deployment")

	assert.Contains(t, names, "azure-cloud-node-manager")
	assert.Contains(t, kinds, "DaemonSet")
}
