package aws

import (
	"embed"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	//go:embed assets/*
	awsFS embed.FS
	//go:embed bootstrap/*
	awsBootstrapFS embed.FS

	awsResources, awsBootstrapResoureces []client.Object
	awsSources                           = []common.ObjectSource{
		{Object: &appsv1.Deployment{}, Path: "assets/deployment.yaml"},
	}
	awsBootstrapSources = []common.ObjectSource{
		{Object: &corev1.Pod{}, Path: "bootstrap/pod.yaml"},
	}
)

func init() {
	var err error
	awsResources, err = common.ReadResources(awsFS, awsSources)
	utilruntime.Must(err)
	awsBootstrapResoureces, err = common.ReadResources(awsBootstrapFS, awsBootstrapSources)
	utilruntime.Must(err)
}

// GetResources returns a list of AWS resources for provisioning CCM in running cluster
func GetResources() []client.Object {
	resources := make([]client.Object, len(awsResources))
	for i := range awsResources {
		resources[i] = awsResources[i].DeepCopyObject().(client.Object)
	}

	return resources
}

// GetBootstrapResources returns a list static pods for provisioning CCM on bootstrap node for AWS
func GetBootstrapResources() []client.Object {
	resources := make([]client.Object, len(awsBootstrapResoureces))
	for i := range awsBootstrapResoureces {
		resources[i] = awsBootstrapResoureces[i].DeepCopyObject().(client.Object)
	}

	return resources
}
