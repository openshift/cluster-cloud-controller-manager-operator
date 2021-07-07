package cloud

import (
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/aws"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/azure"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/openstack"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestGetResources(t *testing.T) {
	tc := []struct {
		name     string
		platform configv1.PlatformType
		expected []client.Object
	}{{
		name:     "AWS resources returned as expected",
		platform: configv1.AWSPlatformType,
		expected: aws.GetResources(),
	}, {
		name:     "OpenStack resources returned as expected",
		platform: configv1.OpenStackPlatformType,
		expected: openstack.GetResources(),
	}, {
		name:     "GCP resources are empty, as the platform is not yet supported",
		platform: configv1.GCPPlatformType,
	}, {
		name:     "Azure resources are empty, as the platform is not yet supported",
		platform: configv1.AzurePlatformType,
	}, {
		name:     "VSphere resources are empty, as the platform is not yet supported",
		platform: configv1.VSpherePlatformType,
	}, {
		name:     "OVirt resources are empty, as the platform is not yet supported",
		platform: configv1.OvirtPlatformType,
	}, {
		name:     "IBMCloud resources are empty, as the platform is not yet supported",
		platform: configv1.IBMCloudPlatformType,
	}, {
		name:     "Libvirt resources are empty",
		platform: configv1.LibvirtPlatformType,
	}, {
		name:     "Kubevirt resources are empty",
		platform: configv1.KubevirtPlatformType,
	}, {
		name:     "BareMetal resources are empty",
		platform: configv1.BareMetalPlatformType,
	}, {
		name:     "None platform resources are empty",
		platform: configv1.NonePlatformType,
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			resources := GetResources(tc.platform)

			assert.Equal(t, len(tc.expected), len(resources))
			assert.EqualValues(t, tc.expected, resources)

			// Edit and repeat procedure to ensure modification in place is not present
			if len(resources) > 0 {
				resources[0].SetName("different")
				newResources := GetResources(tc.platform)

				assert.Equal(t, len(tc.expected), len(newResources))
				assert.EqualValues(t, tc.expected, newResources)
				assert.NotEqualValues(t, resources, newResources)
			}
		})
	}
}

func TestGetBootstrapResources(t *testing.T) {
	tc := []struct {
		name     string
		platform configv1.PlatformType
		expected []client.Object
	}{{
		name:     "AWS resources returned as expected",
		platform: configv1.AWSPlatformType,
		expected: aws.GetBootstrapResources(),
	}, {
		name:     "OpenStack resources are empty, as the platform is not yet supported",
		platform: configv1.OpenStackPlatformType,
	}, {
		name:     "GCP resources are empty, as the platform is not yet supported",
		platform: configv1.GCPPlatformType,
	}, {
		name:     "Azure resources returned as expected",
		platform: configv1.AzurePlatformType,
		expected: azure.GetBootstrapResources(),
	}, {
		name:     "VSphere resources are empty, as the platform is not yet supported",
		platform: configv1.VSpherePlatformType,
	}, {
		name:     "OVirt resources are empty, as the platform is not yet supported",
		platform: configv1.OvirtPlatformType,
	}, {
		name:     "IBMCloud resources are empty, as the platform is not yet supported",
		platform: configv1.IBMCloudPlatformType,
	}, {
		name:     "Libvirt resources are empty",
		platform: configv1.LibvirtPlatformType,
	}, {
		name:     "Kubevirt resources are empty",
		platform: configv1.KubevirtPlatformType,
	}, {
		name:     "BareMetal resources are empty",
		platform: configv1.BareMetalPlatformType,
	}, {
		name:     "None platform resources are empty",
		platform: configv1.NonePlatformType,
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			resources := GetBootstrapResources(tc.platform)

			assert.Equal(t, len(tc.expected), len(resources))
			assert.EqualValues(t, tc.expected, resources)

			if len(resources) > 0 {
				// Edit and repeat procedure to ensure modification in place is not present
				for _, resource := range resources {
					resource.SetName("different")
				}
				newResources := GetBootstrapResources(tc.platform)

				assert.Equal(t, len(tc.expected), len(newResources))
				assert.EqualValues(t, tc.expected, newResources)
				assert.NotEqualValues(t, resources, newResources)
			}
		})
	}
}

func TestResourcesRunBeforeCNI(t *testing.T) {
	/*
		As CNI relies on CMM to initialist the Node IP addresses. We must ensure
		that CCM pods can run before the CNO has been deployed and before the CNI
		initialises the Node.

		To achieve this, we must tolerate the not-ready taint, use host
		networking and use the internal API Load Balancer instead of the API Service.
	*/

	platforms := []configv1.PlatformType{
		configv1.AWSPlatformType,
		configv1.OpenStackPlatformType,
		configv1.GCPPlatformType,
		configv1.AzurePlatformType,
		configv1.VSpherePlatformType,
		configv1.OvirtPlatformType,
		configv1.IBMCloudPlatformType,
		configv1.LibvirtPlatformType,
		configv1.KubevirtPlatformType,
		configv1.BareMetalPlatformType,
		configv1.NonePlatformType,
	}
	for _, platform := range platforms {
		t.Run(string(platform), func(t *testing.T) {
			resources := GetResources(platform)
			resources = append(resources, GetBootstrapResources(platform)...)

			for _, resource := range resources {
				switch obj := resource.(type) {
				case *corev1.Pod:
					checkResourceRunsBeforeCNI(t, obj.Spec)
				case *appsv1.Deployment:
					checkResourceRunsBeforeCNI(t, obj.Spec.Template.Spec)
				case *appsv1.DaemonSet:
					checkResourceRunsBeforeCNI(t, obj.Spec.Template.Spec)
				default:
					// Nothing to check for non
				}
			}
		})
	}
}

func checkResourceRunsBeforeCNI(t *testing.T, podSpec corev1.PodSpec) {
	checkResourceTolerations(t, podSpec)
	checkHostNetwork(t, podSpec)
	checkVolumes(t, podSpec)
	checkContainerCommand(t, podSpec)
}

func checkResourceTolerations(t *testing.T, podSpec corev1.PodSpec) {
	uninitializedTaint := corev1.Toleration{
		Key:      "node.cloudprovider.kubernetes.io/uninitialized",
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoSchedule,
	}
	notReadyTaint := corev1.Toleration{
		Key:      "node.kubernetes.io/not-ready",
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoSchedule,
	}

	tolerations := podSpec.Tolerations
	assert.Contains(t, tolerations, uninitializedTaint, "PodSpec should tolerate the uninitialized taint")
	assert.Contains(t, tolerations, notReadyTaint, "PodSpec should tolerate the not-ready taint")
}

func checkHostNetwork(t *testing.T, podSpec corev1.PodSpec) {
	assert.Equal(t, podSpec.HostNetwork, true, "PodSpec should set HostNetwork true")
}

func checkVolumes(t *testing.T, podSpec corev1.PodSpec) {
	directory := corev1.HostPathDirectory
	hostVolume := corev1.Volume{
		Name: "host-etc-kube",
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: "/etc/kubernetes",
				Type: &directory,
			},
		},
	}
	hostVolumeMount := corev1.VolumeMount{
		MountPath: "/etc/kubernetes",
		Name:      "host-etc-kube",
		ReadOnly:  true,
	}

	assert.Contains(t, podSpec.Volumes, hostVolume, "PodSpec Volumes should contain host-etc-kube host path volume")

	for _, container := range podSpec.Containers {
		assert.Contains(t, container.VolumeMounts, hostVolumeMount, "Container VolumeMounts should contain host-etc-kube volume mount")
	}
}

func checkContainerCommand(t *testing.T, podSpec corev1.PodSpec) {
	binBash := "/bin/bash"
	dashC := "-c"
	setAPIEnv := `#!/bin/bash
set -o allexport
if [[ -f /etc/kubernetes/apiserver-url.env ]]; then
  source /etc/kubernetes/apiserver-url.env
else
  URL_ONLY_KUBECONFIG=/etc/kubernetes/kubeconfig
fi
exec `

	for _, container := range podSpec.Containers {
		command := container.Command
		assert.Len(t, command, 3, "Container Command should have 3 elements")
		assert.Len(t, container.Args, 0, "Container Args should have no elements, inline the args into the Container Command")

		assert.Equal(t, command[0], binBash, "Container Command first element should equal %q", binBash)
		assert.Equal(t, command[1], dashC, "Container Command second element should equal %q", dashC)
		assert.True(t, strings.HasPrefix(command[2], setAPIEnv), "Container Command third (%q) element should start with %q", command[2], setAPIEnv)
	}
}

func TestDeploymentPodAntiAffinity(t *testing.T) {
	platforms := []configv1.PlatformType{
		configv1.AWSPlatformType,
		configv1.OpenStackPlatformType,
		configv1.GCPPlatformType,
		configv1.AzurePlatformType,
		configv1.VSpherePlatformType,
		configv1.OvirtPlatformType,
		configv1.IBMCloudPlatformType,
		configv1.LibvirtPlatformType,
		configv1.KubevirtPlatformType,
		configv1.BareMetalPlatformType,
		configv1.NonePlatformType,
	}
	for _, platform := range platforms {
		t.Run(string(platform), func(t *testing.T) {
			resources := GetResources(platform)
			resources = append(resources, GetBootstrapResources(platform)...)

			for _, resource := range resources {
				switch obj := resource.(type) {
				case *appsv1.Deployment:
					checkPodAntiAffinity(t, obj.Spec.Template.Spec, obj.ObjectMeta)
				default:
					// Nothing to check for non
				}
			}
		})
	}
}

func checkPodAntiAffinity(t *testing.T, podSpec corev1.PodSpec, podMeta metav1.ObjectMeta) {
	assert.NotNil(t, podSpec.Affinity)

	podAntiAffinity := &corev1.PodAntiAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
			{
				TopologyKey: "kubernetes.io/hostname",
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: podMeta.Labels,
				},
			},
		},
	}

	assert.EqualValues(t, podAntiAffinity, podSpec.Affinity.PodAntiAffinity)
}
