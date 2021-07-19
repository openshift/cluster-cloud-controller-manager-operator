package ibm

import (
	"embed"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	appsv1 "k8s.io/api/apps/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	//go:embed assets/*
	ibmFS embed.FS

	ibmResources []client.Object

	ibmSources = []common.ObjectSource{
		{Object: &appsv1.Deployment{}, Path: "assets/deployment.yaml"},
	}
)

func init() {
	var err error
	ibmResources, err = common.ReadResources(ibmFS, ibmSources)
	utilruntime.Must(err)
}

// GetResources returns a list of IBM resources for provisioning CCM in running cluster
func GetResources() []client.Object {
	resources := make([]client.Object, len(ibmResources))
	for i := range ibmResources {
		resources[i] = ibmResources[i].DeepCopyObject().(client.Object)
	}

	return resources
}
