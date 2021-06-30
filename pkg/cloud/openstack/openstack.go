package openstack

import (
	"embed"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	//go:embed assets/* bootstrap/*
	openStackFS      embed.FS
	openStackSources = []common.ObjectSource{
		{Object: &v1.ConfigMap{}, Path: "assets/config.yaml"},
		{Object: &appsv1.Deployment{}, Path: "assets/deployment.yaml"},
	}
	openstackBootstrapSources = []common.ObjectSource{
		{Object: &v1.Pod{}, Path: "bootstrap/pod.yaml"},
	}
	openStackResources          []client.Object
	openStackBootstrapResources []client.Object
)

func init() {
	var err error
	openStackResources, err = common.ReadResources(openStackFS, openStackSources)
	utilruntime.Must(err)
	openStackBootstrapResources, err = common.ReadResources(openStackFS, openstackBootstrapSources)
	utilruntime.Must(err)
}

// GetResources returns a list of OpenStack resources for provisioning CCM in running cluster
func GetResources() []client.Object {
	resources := make([]client.Object, len(openStackResources))
	for i := range openStackResources {
		resources[i] = openStackResources[i].DeepCopyObject().(client.Object)
	}

	return resources
}

// GetBootstrapResources returns a list static pods for provisioning CCM on bootstrap node for AWS
func GetBootstrapResources() []client.Object {
	resources := make([]client.Object, len(openStackBootstrapResources))
	for i := range openStackBootstrapResources {
		resources[i] = openStackBootstrapResources[i].DeepCopyObject().(client.Object)
	}

	return resources
}
