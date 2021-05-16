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
	//go:embed assets/*
	openStackFS      embed.FS
	openStackSources = []common.ObjectSource{
		{Object: &v1.ConfigMap{}, Path: "assets/config.yaml"},
		{Object: &appsv1.Deployment{}, Path: "assets/deployment.yaml"},
	}
	openStackResources []client.Object
)

func init() {
	var err error
	openStackResources, err = common.ReadResources(openStackFS, openStackSources)
	utilruntime.Must(err)
}

func GetResources() []client.Object {
	resources := make([]client.Object, len(openStackResources))
	for i := range openStackResources {
		resources[i] = openStackResources[i].DeepCopyObject().(client.Object)
	}

	return resources
}
