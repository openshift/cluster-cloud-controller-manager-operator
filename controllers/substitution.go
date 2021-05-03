package controllers

import (
	v1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// Names in this list are unique and will be substituted with an image from config
	// cloudControllerManagerName is a name for default CCM controller container any provider may have
	cloudControllerManagerName = "cloud-controller-manager"
)

// setDeploymentImages substitutes controller containers in Deployment with correct image
func setDeploymentImages(config operatorConfig, d *v1.Deployment) {
	for i, container := range d.Spec.Template.Spec.Containers {
		if container.Name != cloudControllerManagerName {
			continue
		}

		d.Spec.Template.Spec.Containers[i].Image = config.ControllerImage
	}
}

func fillConfigValues(config operatorConfig, templates []client.Object) []client.Object {
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
