package aws

import (
	"embed"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	appsv1 "k8s.io/api/apps/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	//go:embed assets/*
	awsFS      embed.FS
	awsSources = []common.ObjectSource{
		{Object: &appsv1.Deployment{}, Path: "assets/deployment.yaml"},
	}
	awsResources []client.Object
)

func init() {
	var err error
	awsResources, err = common.ReadResources(awsFS, awsSources)
	utilruntime.Must(err)
}

func GetResources() []client.Object {
	resources := make([]client.Object, len(awsResources))
	for i := range awsResources {
		resources[i] = awsResources[i].DeepCopyObject().(client.Object)
	}

	return resources
}
