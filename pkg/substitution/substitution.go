package substitution

import (
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// Names in this list are unique and will be substituted with an image from config
	// cloudControllerManagerName is a name for default CCM controller container any provider may have
	cloudControllerManagerName = "cloud-controller-manager"
	cloudNodeManagerName       = "cloud-node-manager"
)

// setCloudControllerImage substitutes controller containers in provided pod specs with correct image
func setCloudControllerImage(config config.OperatorConfig, p corev1.PodSpec) corev1.PodSpec {
	updatedPod := *p.DeepCopy()
	for i, container := range p.Containers {
		substituteName := ""
		switch container.Name {
		case cloudControllerManagerName:
			substituteName = config.ControllerImage
		case cloudNodeManagerName:
			substituteName = config.CloudNodeImage
		default:
			continue
		}

		if substituteName != "" {
			klog.Infof("Substituting container image for container %q with %q", container.Name, substituteName)
			updatedPod.Containers[i].Image = substituteName
		}
	}

	return updatedPod
}

func FillConfigValues(config config.OperatorConfig, templates []client.Object) []client.Object {
	objects := make([]client.Object, len(templates))
	for i, objectTemplate := range templates {
		templateCopy := objectTemplate.DeepCopyObject().(client.Object)

		// Set namespaces for all object. Namespace on cluster-wide objects is stripped by API server and is not applied
		templateCopy.SetNamespace(config.ManagedNamespace)

		switch obj := templateCopy.(type) {
		case *appsv1.Deployment:
			obj.Spec.Template.Spec = setCloudControllerImage(config, obj.Spec.Template.Spec)
		case *appsv1.DaemonSet:
			obj.Spec.Template.Spec = setCloudControllerImage(config, obj.Spec.Template.Spec)
		case *corev1.Pod:
			obj.Spec = setCloudControllerImage(config, obj.Spec)
		}
		objects[i] = templateCopy
	}
	return objects
}
