package azure

import (
	"embed"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	corev1 "k8s.io/api/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	//go:embed bootstrap/*
	azureBootstrapFS embed.FS

	azureBootstrapResources []client.Object

	azureBootstrapSources = []common.ObjectSource{
		{Object: &corev1.Pod{}, Path: "bootstrap/pod.yaml"},
	}
)

func init() {
	var err error
	azureBootstrapResources, err = common.ReadResources(azureBootstrapFS, azureBootstrapSources)
	utilruntime.Must(err)
}

// GetBootstrapResources returns a list static pods for provisioning CCM on bootstrap node for Azure
func GetBootstrapResources() []client.Object {
	resources := make([]client.Object, len(azureBootstrapResources))
	for i := range azureBootstrapResources {
		resources[i] = azureBootstrapResources[i].DeepCopyObject().(client.Object)
	}

	return resources
}
