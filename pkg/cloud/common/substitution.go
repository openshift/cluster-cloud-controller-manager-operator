package common

import (
	configv1 "github.com/openshift/api/config/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
)

// setProxySettings substitutes controller containers in provided pod specs with cluster wide proxy settings
func setProxySettings(config config.OperatorConfig, p corev1.PodSpec) corev1.PodSpec {
	clusterProxyEnvVars := getProxyArgs(config.ClusterProxy)
	if len(clusterProxyEnvVars) == 0 {
		return p
	}

	updatedPod := *p.DeepCopy()
	for i, container := range p.Containers {
		klog.Infof("Substituting proxy settings for container %q", container.Name)
		updatedPod.Containers[i].Env = append(updatedPod.Containers[i].Env, clusterProxyEnvVars...)
	}

	return updatedPod
}

// getProxyArg converts a cluster wide proxy configuration into a list of
// env variable objects for pods.
func getProxyArgs(proxy *configv1.Proxy) []corev1.EnvVar {
	var envVars []corev1.EnvVar

	if proxy == nil {
		return envVars
	}
	if proxy.Status.HTTPProxy != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "HTTP_PROXY",
			Value: proxy.Status.HTTPProxy,
		})
	}
	if proxy.Status.HTTPSProxy != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "HTTPS_PROXY",
			Value: proxy.Status.HTTPSProxy,
		})
	}
	if proxy.Status.NoProxy != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "NO_PROXY",
			Value: proxy.Status.NoProxy,
		})
	}
	return envVars
}

func SubstituteCommonPartsFromConfig(config config.OperatorConfig, renderedObjects []client.Object) []client.Object {
	substitutedObjects := make([]client.Object, len(renderedObjects))
	for i, objectTemplate := range renderedObjects {
		templateCopy := objectTemplate.DeepCopyObject().(client.Object)

		// Set namespaces for all object. Namespace on cluster-wide substitutedObjects is stripped by API server and is not applied
		templateCopy.SetNamespace(config.ManagedNamespace)

		switch obj := templateCopy.(type) {
		case *appsv1.Deployment:
			obj.Spec.Template.Spec = setProxySettings(config, obj.Spec.Template.Spec)
			if config.IsSingleReplica {
				obj.Spec.Replicas = pointer.Int32(1)
			}
		case *appsv1.DaemonSet:
			obj.Spec.Template.Spec = setProxySettings(config, obj.Spec.Template.Spec)
		}
		substitutedObjects[i] = templateCopy
	}
	return substitutedObjects
}
