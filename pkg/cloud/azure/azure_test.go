package azure

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetBootstrapResources(t *testing.T) {
	resources := GetBootstrapResources()
	assert.Len(t, resources, 1)

	var names, kinds []string
	for _, r := range resources {
		names = append(names, r.GetName())
		kinds = append(kinds, r.GetObjectKind().GroupVersionKind().Kind)
	}

	assert.Contains(t, names, "azure-cloud-controller-manager")
	assert.Contains(t, kinds, "Pod")
}
