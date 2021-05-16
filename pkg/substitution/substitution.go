package substitution

import (
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
	v1 "k8s.io/api/apps/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// Names in this list are unique and will be substituted with an image from config
	// cloudControllerManagerName is a name for default CCM controller container any provider may have
	cloudControllerManagerName = "cloud-controller-manager"
)

// setDeploymentImages substitutes controller containers in Deployment with correct image
func setDeploymentImages(config config.OperatorConfig, d *v1.Deployment) {
	for i, container := range d.Spec.Template.Spec.Containers {
		if container.Name != cloudControllerManagerName {
			continue
		}

		klog.Infof("Substituting %q: %s", container.Name, config.ControllerImage)
		d.Spec.Template.Spec.Containers[i].Image = config.ControllerImage
	}
}

func FillConfigValues(config config.OperatorConfig, templates []client.Object) []client.Object {
	objects := make([]client.Object, len(templates))
	for i, objectTemplate := range templates {
		templateCopy := objectTemplate.DeepCopyObject().(client.Object)

		// Set namespaces for all object. Namespace on cluster-wide objects is stripped by API server and is not applied
		templateCopy.SetNamespace(config.ManagedNamespace)

		dep, ok := templateCopy.(*v1.Deployment)
		if ok {
			setDeploymentImages(config, dep)
			// TODO: add cloud-config calculated hash to annotations to account for redeployment on content change
		}

		objects[i] = templateCopy
	}
	return objects
}
