package cloud

import (
	"fmt"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud/common"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/util/testingutils"
)

const (
	cloudControllerManagerDefaultPort = 10258
	cloudNodeManagerDefaultPort       = 10263
)

func getDummyPlatformStatus(platformType configv1.PlatformType, isAzureStack bool) *configv1.PlatformStatus {
	platformStatus := configv1.PlatformStatus{
		Type: platformType,
	}
	if isAzureStack {
		platformStatus.Azure = &configv1.AzurePlatformStatus{
			CloudName: configv1.AzureStackCloud,
		}
	}
	return &platformStatus
}

type testPlatform struct {
	platformStatus *configv1.PlatformStatus
}

func (tp *testPlatform) getOperatorConfig() config.OperatorConfig {
	return config.OperatorConfig{
		ManagedNamespace: "openshift-cloud-controller-manager",
		ImagesReference: config.ImagesReference{
			CloudControllerManagerOperator:  "registry.ci.openshift.org/openshift:cluster-cloud-controller-manager-operator",
			CloudControllerManagerAlibaba:   "quay.io/repository/openshift/origin-alibaba-cloud-controller-manager",
			CloudControllerManagerAWS:       "registry.ci.openshift.org/openshift:aws-cloud-controller-manager",
			CloudControllerManagerAzure:     "quay.io/openshift/origin-azure-cloud-controller-manager",
			CloudNodeManagerAzure:           "quay.io/openshift/origin-azure-cloud-node-manager",
			CloudControllerManagerGCP:       "registry.ci.openshift.org/openshift:gcp-cloud-controller-manager",
			CloudControllerManagerIBM:       "registry.ci.openshift.org/openshift:ibm-cloud-controller-manager",
			CloudControllerManagerOpenStack: "registry.ci.openshift.org/openshift:openstack-cloud-controller-manager",
			CloudControllerManagerVSphere:   "registry.ci.openshift.org/openshift:vsphere-cloud-controller-manager",
			CloudControllerManagerPowerVS:   "quay.io/openshift/origin-powervs-cloud-controller-manager",
			CloudControllerManagerNutanix:   "quay.io/openshift/origin-nutanix-cloud-controller-manager",
		},
		PlatformStatus:     tp.platformStatus,
		InfrastructureName: "my-cool-cluster-777",
	}
}

type testPlatformsMap map[string]testPlatform

func getPlatforms() testPlatformsMap {
	return testPlatformsMap{
		string(configv1.AlibabaCloudPlatformType): {getDummyPlatformStatus(configv1.AlibabaCloudPlatformType, false)},
		string(configv1.AWSPlatformType):          {getDummyPlatformStatus(configv1.AWSPlatformType, false)},
		string(configv1.AzurePlatformType):        {getDummyPlatformStatus(configv1.AzurePlatformType, false)},
		"AzureStackHub":                           {getDummyPlatformStatus(configv1.AzurePlatformType, true)},
		string(configv1.BareMetalPlatformType):    {getDummyPlatformStatus(configv1.BareMetalPlatformType, false)},
		string(configv1.GCPPlatformType):          {getDummyPlatformStatus(configv1.GCPPlatformType, false)},
		string(configv1.IBMCloudPlatformType):     {getDummyPlatformStatus(configv1.IBMCloudPlatformType, false)},
		string(configv1.KubevirtPlatformType):     {getDummyPlatformStatus(configv1.KubevirtPlatformType, false)},
		string(configv1.LibvirtPlatformType):      {getDummyPlatformStatus(configv1.LibvirtPlatformType, false)},
		string(configv1.NonePlatformType):         {getDummyPlatformStatus(configv1.NonePlatformType, false)},
		string(configv1.NutanixPlatformType):      {getDummyPlatformStatus(configv1.NutanixPlatformType, false)},
		string(configv1.OpenStackPlatformType):    {getDummyPlatformStatus(configv1.OpenStackPlatformType, false)},
		string(configv1.OvirtPlatformType):        {getDummyPlatformStatus(configv1.OvirtPlatformType, false)},
		string(configv1.PowerVSPlatformType):      {getDummyPlatformStatus(configv1.PowerVSPlatformType, false)},
		string(configv1.VSpherePlatformType):      {getDummyPlatformStatus(configv1.VSpherePlatformType, false)},
	}
}

func TestGetResources(t *testing.T) {
	platformsMap := getPlatforms()
	getResourcesThresholdMs := 30 * time.Millisecond

	t.Log("disabling klog logging")
	testingutils.TurnOffKlog()
	defer func() {
		t.Log("enabling klog logging")
		testingutils.TurnOnKlog()
	}()

	tc := []struct {
		name                      string
		testPlatform              testPlatform
		expectedResourceCount     int
		singleReplica             bool
		expectedResourcesKindName []string
	}{{
		name:                  "Alibaba resources returned as expected",
		testPlatform:          platformsMap[string(configv1.AlibabaCloudPlatformType)],
		singleReplica:         false,
		expectedResourceCount: 2,
		expectedResourcesKindName: []string{
			"Deployment/alibaba-cloud-controller-manager",
			"PodDisruptionBudget/alibabacloud-cloud-controller-manager",
		},
	}, {
		name:                      "Alibaba resources returned as expected with single node cluster",
		testPlatform:              platformsMap[string(configv1.AlibabaCloudPlatformType)],
		expectedResourceCount:     1,
		singleReplica:             true,
		expectedResourcesKindName: []string{"Deployment/alibaba-cloud-controller-manager"},
	}, {
		name:                  "AWS resources returned as expected",
		testPlatform:          platformsMap[string(configv1.AWSPlatformType)],
		expectedResourceCount: 2,
		expectedResourcesKindName: []string{
			"Deployment/aws-cloud-controller-manager",
			"PodDisruptionBudget/aws-cloud-controller-manager",
		},
	}, {
		name:                      "AWS resources returned as expected with single node cluster",
		testPlatform:              platformsMap[string(configv1.AWSPlatformType)],
		expectedResourceCount:     1,
		singleReplica:             true,
		expectedResourcesKindName: []string{"Deployment/aws-cloud-controller-manager"},
	}, {
		name:                  "OpenStack resources returned as expected",
		testPlatform:          platformsMap[string(configv1.OpenStackPlatformType)],
		expectedResourceCount: 2,
		expectedResourcesKindName: []string{
			"Deployment/openstack-cloud-controller-manager",
			"PodDisruptionBudget/openstack-cloud-controller-manager",
		},
	}, {
		name:                  "OpenStack resources returned as expected with signle node cluster",
		testPlatform:          platformsMap[string(configv1.OpenStackPlatformType)],
		expectedResourceCount: 1,
		singleReplica:         true,
		expectedResourcesKindName: []string{
			"Deployment/openstack-cloud-controller-manager",
		},
	}, {
		name:                  "GCP resources returned as expected",
		testPlatform:          platformsMap[string(configv1.GCPPlatformType)],
		expectedResourceCount: 2,
		expectedResourcesKindName: []string{
			"Deployment/gcp-cloud-controller-manager",
			"PodDisruptionBudget/gcp-cloud-controller-manager",
		},
	}, {
		name:                      "GCP resources returned as expected with single node cluster",
		testPlatform:              platformsMap[string(configv1.GCPPlatformType)],
		expectedResourceCount:     1,
		singleReplica:             true,
		expectedResourcesKindName: []string{"Deployment/gcp-cloud-controller-manager"},
	}, {
		name:                  "Azure resources returned as expected",
		testPlatform:          platformsMap[string(configv1.AzurePlatformType)],
		expectedResourceCount: 3,
		expectedResourcesKindName: []string{
			"Deployment/azure-cloud-controller-manager",
			"DaemonSet/azure-cloud-node-manager",
			"PodDisruptionBudget/azure-cloud-controller-manager",
		},
	}, {
		name:                  "Azure resources returned as expected with single node cluster",
		testPlatform:          platformsMap[string(configv1.AzurePlatformType)],
		expectedResourceCount: 2,
		singleReplica:         true,
		expectedResourcesKindName: []string{
			"Deployment/azure-cloud-controller-manager",
			"DaemonSet/azure-cloud-node-manager",
		},
	}, {
		name:                  "Azure Stack resources returned as expected",
		testPlatform:          platformsMap["AzureStackHub"],
		expectedResourceCount: 3,
		expectedResourcesKindName: []string{
			"Deployment/azure-cloud-controller-manager",
			"DaemonSet/azure-cloud-node-manager",
			"PodDisruptionBudget/azure-cloud-controller-manager",
		},
	}, {
		name:                  "Azure Stack resources returned as expected with single node",
		testPlatform:          platformsMap["AzureStackHub"],
		expectedResourceCount: 2,
		singleReplica:         true,
		expectedResourcesKindName: []string{
			"Deployment/azure-cloud-controller-manager",
			"DaemonSet/azure-cloud-node-manager",
		},
	}, {
		name:                  "VSphere resources returned as expected",
		testPlatform:          platformsMap[string(configv1.VSpherePlatformType)],
		expectedResourceCount: 8,
		expectedResourcesKindName: []string{
			"Deployment/vsphere-cloud-controller-manager",
			"PodDisruptionBudget/vsphere-cloud-controller-manager",
			"Role/vsphere-cloud-controller-manager",
			"RoleBinding/vsphere-cloud-controller-manager:vsphere-cloud-controller-manager",
			"RoleBinding/vsphere-cloud-controller-manager:cloud-controller-manager",
			"ClusterRole/vsphere-cloud-controller-manager",
			"ClusterRoleBinding/vsphere-cloud-controller-manager:vsphere-cloud-controller-manager",
			"ClusterRoleBinding/vsphere-cloud-controller-manager:cloud-controller-manager",
		},
	}, {
		name:                  "VSphere resources returned as expected with single node",
		testPlatform:          platformsMap[string(configv1.VSpherePlatformType)],
		expectedResourceCount: 7,
		singleReplica:         true,
		expectedResourcesKindName: []string{
			"Deployment/vsphere-cloud-controller-manager",
			"Role/vsphere-cloud-controller-manager",
			"RoleBinding/vsphere-cloud-controller-manager:vsphere-cloud-controller-manager",
			"RoleBinding/vsphere-cloud-controller-manager:cloud-controller-manager",
			"ClusterRole/vsphere-cloud-controller-manager",
			"ClusterRoleBinding/vsphere-cloud-controller-manager:vsphere-cloud-controller-manager",
			"ClusterRoleBinding/vsphere-cloud-controller-manager:cloud-controller-manager",
		},
	}, {
		name:         "OVirt resources are empty, as the platform is not yet supported",
		testPlatform: platformsMap[string(configv1.OvirtPlatformType)],
	}, {
		name:                  "IBMCloud resources",
		testPlatform:          platformsMap[string(configv1.IBMCloudPlatformType)],
		expectedResourceCount: 2,
		expectedResourcesKindName: []string{
			"Deployment/ibm-cloud-controller-manager",
			"PodDisruptionBudget/ibmcloud-cloud-controller-manager",
		},
	}, {
		name:                      "IBMCloud resources with single node cluster",
		testPlatform:              platformsMap[string(configv1.IBMCloudPlatformType)],
		expectedResourceCount:     1,
		singleReplica:             true,
		expectedResourcesKindName: []string{"Deployment/ibm-cloud-controller-manager"},
	}, {
		name:                  "PowerVS resources",
		testPlatform:          platformsMap[string(configv1.PowerVSPlatformType)],
		expectedResourceCount: 2,
		singleReplica:         false,
		expectedResourcesKindName: []string{
			"Deployment/powervs-cloud-controller-manager",
			"PodDisruptionBudget/powervs-cloud-controller-manager",
		},
	}, {
		name:                      "PowerVS resources with single node cluster",
		testPlatform:              platformsMap[string(configv1.PowerVSPlatformType)],
		expectedResourceCount:     1,
		singleReplica:             true,
		expectedResourcesKindName: []string{"Deployment/powervs-cloud-controller-manager"},
	}, {
		name:         "Libvirt resources are empty",
		testPlatform: platformsMap[string(configv1.LibvirtPlatformType)],
	}, {
		name:         "Kubevirt resources are empty",
		testPlatform: platformsMap[string(configv1.KubevirtPlatformType)],
	}, {
		name:         "BareMetal resources are empty",
		testPlatform: platformsMap[string(configv1.BareMetalPlatformType)],
	}, {
		name:         "None platform resources are empty",
		testPlatform: platformsMap[string(configv1.NonePlatformType)],
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			operatorConfig := tc.testPlatform.getOperatorConfig()
			operatorConfig.IsSingleReplica = tc.singleReplica
			resources, err := GetResources(operatorConfig)
			assert.NoError(t, err)

			assert.Equal(t, tc.expectedResourceCount, len(resources))

			otherResourcesArray, err := GetResources(operatorConfig)
			assert.NoError(t, err)
			assert.EqualValues(t, otherResourcesArray, resources)

			if tc.expectedResourceCount > 0 {
				assert.NotZero(t, tc.expectedResourcesKindName, "expectedResourcesKindName for this testcase should be specified")

				for _, resource := range resources {
					resourceKind := resource.GetObjectKind().GroupVersionKind().Kind
					resourceKindName := fmt.Sprintf("%s/%s", resourceKind, resource.GetName())
					assert.Contains(t, tc.expectedResourcesKindName, resourceKindName)
				}
			}

			// Edit and repeat procedure to ensure modification in place is not present
			if len(resources) > 0 {
				resources[0].SetName("different")
				newResources, err := GetResources(operatorConfig)
				assert.NoError(t, err)

				assert.Equal(t, len(otherResourcesArray), len(newResources))
				assert.EqualValues(t, otherResourcesArray, newResources)
				assert.NotEqualValues(t, resources, newResources)
			}
		})

		if !testing.Short() {
			t.Run(fmt.Sprintf("Benchmark: %s", tc.name), func(t *testing.T) {
				operatorConfig := tc.testPlatform.getOperatorConfig()
				operatorConfig.IsSingleReplica = tc.singleReplica
				benchResulst := testing.Benchmark(func(b *testing.B) {
					for i := 0; i < b.N; i++ {
						_, err := GetResources(operatorConfig)
						assert.NoError(t, err)
					}
				})
				assert.True(
					t,
					getResourcesThresholdMs.Nanoseconds() > benchResulst.NsPerOp(),
					"Resources rendering took too long, worth to check.",
				)
				fmt.Println(benchResulst)
			})
		}
	}
}

func TestRenderedResources(t *testing.T) {
	/*
		This test runs a number of different checks against the podSpecs produced by
		the different platform resources.
	*/

	platforms := getPlatforms()
	for platformName, platform := range platforms {
		t.Run(platformName, func(t *testing.T) {
			resources, err := GetResources(platform.getOperatorConfig())
			assert.NoError(t, err)

			for _, resource := range resources {
				var podSpec corev1.PodSpec
				switch obj := resource.(type) {
				case *corev1.Pod:
					podSpec = obj.Spec
				case *appsv1.Deployment:
					podSpec = obj.Spec.Template.Spec
				case *appsv1.DaemonSet:
					podSpec = obj.Spec.Template.Spec
				default:
					// Nothing to check for non pod producing types
					continue
				}

				checkResourceRunsBeforeCNI(t, platformName, podSpec)
				checkLeaderElection(t, podSpec)
				checkCloudControllerManagerFlags(t, podSpec)
				checkTrustedCAMounted(t, podSpec)
				checkUseServiceAccountCredentials(t, podSpec)
			}
		})
	}
}

func checkResourceRunsBeforeCNI(t *testing.T, platformName string, podSpec corev1.PodSpec) {
	/*
		As CNI relies on CMM to initialist the Node IP addresses. We must ensure
		that CCM pods can run before the CNO has been deployed and before the CNI
		initialises the Node.

		To achieve this, we must tolerate the not-ready taint, use host
		networking and use the internal API Load Balancer instead of the API Service.
	*/

	checkResourceTolerations(t, podSpec)
	checkHostNetwork(t, podSpec)
	checkPorts(t, podSpec)
	checkVolumes(t, platformName, podSpec)
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
	noScheduleTaint := corev1.Toleration{
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoSchedule,
	}

	tolerations := podSpec.Tolerations

	g := NewWithT(t)
	g.Expect(tolerations).To(SatisfyAny(
		SatisfyAll(
			ContainElement(uninitializedTaint),
			ContainElement(notReadyTaint),
		),
		ContainElement(noScheduleTaint),
	), "PodSpec must either contain the uninitialized and not-ready tolerations, or tolerate any NoSchedule taint")
}

func checkHostNetwork(t *testing.T, podSpec corev1.PodSpec) {
	assert.Equal(t, podSpec.HostNetwork, true, "PodSpec should set HostNetwork true")
}

// This test is to ensure that the guidelines set out in https://github.com/openshift/enhancements/blob/master/dev-guide/host-port-registry.md
// are correctly adhered to.
func checkPorts(t *testing.T, podSpec corev1.PodSpec) {
	var foundValidPort bool
	for _, container := range podSpec.Containers {
		for _, port := range container.Ports {
			switch port.ContainerPort {
			case cloudControllerManagerDefaultPort, cloudNodeManagerDefaultPort:
				foundValidPort = true
			default:
				t.Errorf("Unknown Container Port %d: All ports on Host Network processes must be registered before use", port.ContainerPort)
			}

		}
	}
	if !foundValidPort {
		t.Errorf("Container Ports must specify any used ports. CloudControllerManager should use port %d, CloudNodeManager should use port %d.", cloudControllerManagerDefaultPort, cloudNodeManagerDefaultPort)
	}
}

func checkVolumes(t *testing.T, platformName string, podSpec corev1.PodSpec) {
	directory := corev1.HostPathDirectory
	var (
		hostVolume      corev1.Volume
		hostVolumeMount corev1.VolumeMount
	)
	switch platformName {
	case "Azure":
		// Azure CCM and node manager use an init-container to merge provided credentials
		// with the cloud conf either from host /etc/kubernetes (node-manager) or from
		// the accm configmap (cloud-controller-manager).
		// For this reason, Azure mounts the merged-cloud-config volume where the generated
		// cloud conf has been created.
		hostVolume = corev1.Volume{
			Name: "merged-cloud-config",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: nil,
			},
		}
		assert.Contains(t, podSpec.Volumes, hostVolume, "PodSpec Volumes should contain merged-cloud-config empty dir volume")

		for _, container := range podSpec.Containers {
			hostVolumeMount = corev1.VolumeMount{
				Name:     "merged-cloud-config",
				ReadOnly: true,
			}
			switch container.Name {
			case "cloud-controller-manager":
				hostVolumeMount.MountPath = "/etc/kubernetes-cloud-config"
			case "cloud-node-manager":
				hostVolumeMount.MountPath = "/etc/kubernetes"
			}
			assert.Contains(t, container.VolumeMounts, hostVolumeMount, "Container VolumeMounts should contain merged-cloud-config volume mount")
		}
	default:
		hostVolume = corev1.Volume{
			Name: "host-etc-kube",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/etc/kubernetes",
					Type: &directory,
				},
			},
		}
		hostVolumeMount = corev1.VolumeMount{
			MountPath: "/etc/kubernetes",
			Name:      "host-etc-kube",
			ReadOnly:  true,
		}
		assert.Contains(t, podSpec.Volumes, hostVolume, "PodSpec Volumes should contain host-etc-kube host path volume")

		for _, container := range podSpec.Containers {
			assert.Contains(t, container.VolumeMounts, hostVolumeMount, "Container VolumeMounts should contain host-etc-kube volume mount")
		}
	}
}

func checkContainerCommand(t *testing.T, podSpec corev1.PodSpec) {
	binBash := "/bin/bash"
	dashC := "-c"
	// This script should be present on every node.
	// https://github.com/openshift/machine-config-operator/pull/2232
	// The script sets the API server URL environment variables that
	// the client SDK detects automatically.
	setAPIEnv := `#!/bin/bash
set -o allexport
if [[ -f /etc/kubernetes/apiserver-url.env ]]; then
  source /etc/kubernetes/apiserver-url.env
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

func checkLeaderElection(t *testing.T, podSpec corev1.PodSpec) {
	const (
		leaderElect                  = "--leader-elect=true"
		leaderElectLeaseDuration     = "--leader-elect-lease-duration=137s"
		leaderElectRenewDeadline     = "--leader-elect-renew-deadline=107s"
		leaderElectRetryPeriod       = "--leader-elect-retry-period=26s"
		leaderElectResourceNamesapce = "--leader-elect-resource-namespace=openshift-cloud-controller-manager"
	)

	for _, container := range podSpec.Containers {
		if container.Name != "cloud-controller-manager" {
			// Only the cloud-controller-manager container needs leader election
			continue
		}

		command := container.Command
		assert.Len(t, command, 3, "Container Command should have 3 elements")

		for _, flag := range []string{leaderElect, leaderElectLeaseDuration, leaderElectRenewDeadline, leaderElectRetryPeriod, leaderElectResourceNamesapce} {
			assert.Contains(t, command[2], flag, "Container Command third (%q) element should contain flag %q", command[2], flag)
		}
	}
}

func checkCloudControllerManagerFlags(t *testing.T, podSpec corev1.PodSpec) {
	const (
		// This flag will disable the cloud route controller.
		// The route controller is responsible for setting up inter pod networking
		// using cloud networks, but this isn't required when you have an overlay
		// network as is used within OpenShift.
		configureCloudRoutes = "--configure-cloud-routes=false"
	)

	for _, container := range podSpec.Containers {
		if container.Name != "cloud-controller-manager" {
			// Only the cloud-controller-manager container needs these flags checking
			continue
		}

		command := container.Command
		assert.Len(t, command, 3, "Container Command should have 3 elements")

		for _, flag := range []string{configureCloudRoutes} {
			assert.Contains(t, command[2], flag, "Container Command third (%q) element should contain flag %q", command[2], flag)
		}
	}
}

func TestDeploymentPodAntiAffinity(t *testing.T) {
	platforms := getPlatforms()
	for platformName, platform := range platforms {
		t.Run(platformName, func(t *testing.T) {
			resources, err := GetResources(platform.getOperatorConfig())
			assert.NoError(t, err)

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

func TestDeploymentStrategy(t *testing.T) {
	/*
		This test is designed to check that when a Pod is created by the CCCMO,
		we can update the pod when running on an SNO cluster.
		Because host ports are used by the pods we create, we must release the
		port before creating the new pod
	*/

	platforms := getPlatforms()
	for platformName, platform := range platforms {

		t.Run(platformName, func(t *testing.T) {
			resources, err := GetResources(platform.getOperatorConfig())
			assert.NoError(t, err)

			for _, resource := range resources {
				switch obj := resource.(type) {
				case *appsv1.Deployment:
					checkDeploymentStrategy(t, obj.Spec.Strategy)
				default:
					// Nothing to check for non
				}
			}
		})
	}
}

func checkDeploymentStrategy(t *testing.T, strategy appsv1.DeploymentStrategy) {
	if strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Errorf("Deployment should set strategy type to \"Recreate\"")
	}
}

func checkTrustedCAMounted(t *testing.T, podSpec corev1.PodSpec) {
	trustedCAVolume := corev1.Volume{
		Name: "trusted-ca",
		VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: "ccm-trusted-ca"},
			Items:                []corev1.KeyToPath{{Key: "ca-bundle.crt", Path: "tls-ca-bundle.pem"}},
		}},
	}
	trustedCAVolumeMount := corev1.VolumeMount{
		MountPath: "/etc/pki/ca-trust/extracted/pem",
		Name:      "trusted-ca",
		ReadOnly:  true,
	}
	assert.Contains(t, podSpec.Volumes, trustedCAVolume, "PodSpec %s volumes should contain trusted-ca volume")
	for _, c := range podSpec.Containers {
		assert.Contains(t, c.VolumeMounts, trustedCAVolumeMount, "Container VolumeMounts should contain trusted ca volume mount")
	}
}

func checkUseServiceAccountCredentials(t *testing.T, podSpec corev1.PodSpec) {
	const (
		useServiceAccountCredentials = "--use-service-account-credentials=true"
	)

	for _, container := range podSpec.Containers {
		if container.Name != "cloud-controller-manager" {
			// Only the cloud-controller-manager container needs leader election
			continue
		}

		command := container.Command
		assert.Len(t, command, 3, "Container Command should have 3 elements")

		assert.Contains(t, command[2], useServiceAccountCredentials, "Container Command third (%q) element should contain flag %q", command[2], useServiceAccountCredentials)
	}
}

func TestSelectorLabels(t *testing.T) {
	/*
		This test checks consistency between all Deployment pod template labels and selector labels
	*/

	platforms := getPlatforms()
	for platformName, platform := range platforms {

		t.Run(platformName, func(t *testing.T) {
			resources, err := GetResources(platform.getOperatorConfig())
			assert.NoError(t, err)

			for _, resource := range resources {
				switch obj := resource.(type) {
				case *appsv1.Deployment:
					checkDeployementSelectorLabels(t, obj, platform.platformStatus.Type)
				case *appsv1.DaemonSet:
					checkDaemonSetSelectorLabels(t, obj, platform.platformStatus.Type)
				default:
					// Nothing to check for
				}
			}
		})
	}
}

func checkDeployementSelectorLabels(t *testing.T, deployment *appsv1.Deployment, platformType configv1.PlatformType) {
	assert.Contains(t, deployment.Labels, "k8s-app")
	assert.Contains(t, deployment.Spec.Template.Labels, "k8s-app")
	assert.Contains(t, deployment.Spec.Selector.MatchLabels, "k8s-app")

	assert.Contains(t, deployment.Labels, common.CloudControllerManagerProviderLabel)
	assert.Contains(t, deployment.Spec.Template.Labels, common.CloudControllerManagerProviderLabel)
	assert.Contains(t, deployment.Spec.Selector.MatchLabels, common.CloudControllerManagerProviderLabel)

	assert.Equal(t, string(platformType), deployment.Labels[common.CloudControllerManagerProviderLabel])
	assert.Equal(t, string(platformType), deployment.Spec.Template.Labels[common.CloudControllerManagerProviderLabel])
	assert.Equal(t, string(platformType), deployment.Spec.Selector.MatchLabels[common.CloudControllerManagerProviderLabel])
}

func checkDaemonSetSelectorLabels(t *testing.T, ds *appsv1.DaemonSet, platformType configv1.PlatformType) {
	assert.Contains(t, ds.Labels, "k8s-app")
	assert.Contains(t, ds.Spec.Template.Labels, "k8s-app")
	assert.Contains(t, ds.Spec.Selector.MatchLabels, "k8s-app")

	assert.Contains(t, ds.Labels, common.CloudNodeManagerCloudProviderLabel)
	assert.Contains(t, ds.Spec.Template.Labels, common.CloudNodeManagerCloudProviderLabel)
	assert.Contains(t, ds.Spec.Selector.MatchLabels, common.CloudNodeManagerCloudProviderLabel)

	assert.Equal(t, string(platformType), ds.Labels[common.CloudNodeManagerCloudProviderLabel])
	assert.Equal(t, string(platformType), ds.Spec.Template.Labels[common.CloudNodeManagerCloudProviderLabel])
	assert.Equal(t, string(platformType), ds.Spec.Selector.MatchLabels[common.CloudNodeManagerCloudProviderLabel])
}

func TestReplicas(t *testing.T) {
	platforms := getPlatforms()
	for platformName, platform := range platforms {

		t.Run(platformName, func(t *testing.T) {
			resources, err := GetResources(platform.getOperatorConfig())
			assert.NoError(t, err)

			for _, resource := range resources {
				switch obj := resource.(type) {
				case *appsv1.Deployment:
					assert.Equal(t, derefReplicas(obj.Spec.Replicas), 2)
				default:
					// Nothing to check for
				}
			}
		})

		t.Run(fmt.Sprintf("%s single node", platformName), func(t *testing.T) {
			cfg := platform.getOperatorConfig()
			cfg.IsSingleReplica = true
			resources, err := GetResources(cfg)
			assert.NoError(t, err)

			for _, resource := range resources {
				switch obj := resource.(type) {
				case *appsv1.Deployment:
					assert.Equal(t, derefReplicas(obj.Spec.Replicas), 1)
				default:
					// Nothing to check for
				}
			}
		})
	}
}

func derefReplicas(num *int32) int {
	if num != nil {
		return int(*num)
	}
	return 1
}
