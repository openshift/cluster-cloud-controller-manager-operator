package openstack

import (
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
	"testing"

	"github.com/stretchr/testify/assert"
)

var operatorConfig = config.OperatorConfig{
	ManagedNamespace:  "test-namespace",
	Platform:          "OpenStack",
	ImagesFileContent: []byte("{\"cloudControllerManagerOpenStack\": \"registry.ci.openshift.org/openshift:openstack-cloud-controller-manager\"}"),
}

func TestNewOpenstackAssetsObject(t *testing.T) {
	assetsRenderer, err := NewAssets(operatorConfig)
	assert.NoError(t, err)
	assert.Equal(t, assetsRenderer.(*openstackAssets).Images.CloudControllerManager, "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager")
}

func TestGetResources(t *testing.T) {
	assetsRenderer, err := NewAssets(operatorConfig)
	assert.NoError(t, err)
	resources, err := assetsRenderer.GetResources()
	assert.NoError(t, err)
	assert.Len(t, resources, 2)

	var names, kinds []string
	for _, r := range resources {
		names = append(names, r.GetName())
		kinds = append(kinds, r.GetObjectKind().GroupVersionKind().Kind)
	}

	assert.Contains(t, names, "openstack-cloud-controller-manager")
	assert.Contains(t, names, "openstack-cloud-controller-manager-config")

	assert.Contains(t, kinds, "Deployment")
	assert.Contains(t, kinds, "ConfigMap")

}
