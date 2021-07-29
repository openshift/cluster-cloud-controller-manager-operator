package azurestack

import (
	"embed"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	appsv1 "k8s.io/api/apps/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	//go:embed assets/*
	azureFs embed.FS

	azureResources []client.Object

	azureSources = []common.ObjectSource{
		{Object: &appsv1.DaemonSet{}, Path: "assets/cloud-node-manager-daemonset.yaml"},
		{Object: &appsv1.Deployment{}, Path: "assets/cloud-controller-manager-deployment.yaml"},
	}
)

func init() {
	var err error
	azureResources, err = common.ReadResources(azureFs, azureSources)
	utilruntime.Must(err)
}

// GetResources returns a list of Azrue Stack Hub resources for provisioning CCM in running cluster
func GetResources() []client.Object {
	resources := make([]client.Object, len(azureResources))
	for i := range azureResources {
		resources[i] = azureResources[i].DeepCopyObject().(client.Object)
	}

	return resources
}
