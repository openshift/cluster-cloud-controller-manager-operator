package openstack

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOpenStackResources(t *testing.T) {
	resources := GetResources()
	assert.NotNil(t, resources)

	type resourcemeta struct {
		kind      string
		name      string
		namespace string
	}

	const ccmo_ns = "openshift-cloud-controller-manager"

	// Check that the returned resources contain a named set of objects
	expected := []resourcemeta{
		{"ConfigMap", "openstack-cloud-controller-manager-config", ccmo_ns},
		{"Deployment", "openstack-cloud-controller-manager", ccmo_ns},
	}
	assert.Equal(t, len(expected), len(resources))

	for i := 0; i < len(resources); i++ {
		assert.Contains(t, expected, resourcemeta{
			resources[i].GetObjectKind().GroupVersionKind().Kind,
			resources[i].GetName(),
			resources[i].GetNamespace()})
	}
}
