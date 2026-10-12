package cloud

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func tlsPodSpecs(objects []client.Object) map[string]corev1.PodSpec {
	pods := map[string]corev1.PodSpec{}
	for _, object := range objects {
		switch workload := object.(type) {
		case *appsv1.Deployment:
			pods["Deployment/"+workload.Name] = workload.Spec.Template.Spec
		case *appsv1.DaemonSet:
			pods["DaemonSet/"+workload.Name] = workload.Spec.Template.Spec
		}
	}
	return pods
}

func withoutTLSFlags(pod corev1.PodSpec) corev1.PodSpec {
	copy := pod.DeepCopy()
	pattern := regexp.MustCompile(`--tls-(?:min-version|cipher-suites|curve-preferences)=\S+[ \t]*\\?`)
	for index := range copy.Containers {
		container := &copy.Containers[index]
		for commandIndex, command := range container.Command {
			container.Command[commandIndex] = strings.Join(strings.Fields(pattern.ReplaceAllString(command, "")), " ")
		}
		for argIndex, arg := range container.Args {
			container.Args[argIndex] = strings.Join(strings.Fields(pattern.ReplaceAllString(arg, "")), " ")
		}
	}
	return *copy
}

func TestTLSGroupsAcrossConsumers(t *testing.T) {
	t.Parallel()
	consumers := []struct {
		provider string
		platform configv1.PlatformType
		stack    bool
		enabled  bool
	}{
		{"aws", configv1.AWSPlatformType, false, true},
		{"azure", configv1.AzurePlatformType, false, true},
		{"azurestack", configv1.AzurePlatformType, true, true},
		{"gcp", configv1.GCPPlatformType, false, true},
		{"ibm", configv1.IBMCloudPlatformType, false, true},
		{"nutanix", configv1.NutanixPlatformType, false, false},
		{"openstack", configv1.OpenStackPlatformType, false, true},
		{"powervs", configv1.PowerVSPlatformType, false, false},
		{"vsphere", configv1.VSpherePlatformType, false, true},
	}
	paths, err := filepath.Glob("*/assets/*.yaml")
	require.NoError(t, err)
	ccmTemplates := map[string]int{}
	nodeTemplates := map[string]int{}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		provider := strings.Split(filepath.ToSlash(path), "/")[0]
		if regexp.MustCompile(`exec /(?:usr/)?bin/\S*cloud-controller-manager`).Match(content) {
			ccmTemplates[provider]++
		}
		if strings.Contains(string(content), "exec /bin/azure-cloud-node-manager") {
			nodeTemplates[provider]++
		}
	}
	require.Len(t, ccmTemplates, len(consumers))
	require.Equal(t, map[string]int{"azure": 1, "azurestack": 1}, nodeTemplates)
	for _, consumer := range consumers {
		require.Equal(t, 1, ccmTemplates[consumer.provider], consumer.provider)
		t.Run(consumer.provider, func(t *testing.T) {
			t.Parallel()
			platform := testPlatform{getDummyPlatformStatus(consumer.platform, consumer.stack)}
			cfg := platform.getOperatorConfig()
			baseline, err := GetResources(cfg)
			require.NoError(t, err)
			baselinePods := tlsPodSpecs(baseline)
			for _, settings := range []struct{ min, ciphers, curves string }{
				{"VersionTLS12", "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256", "4588,29,23,24"},
				{"VersionTLS12", "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256", ""},
				{"VersionTLS13", "", "4587,4589"},
				{"", "", ""},
			} {
				cfg.TLSMinVersion, cfg.TLSCipherSuites, cfg.TLSCurvePreferences = settings.min, settings.ciphers, settings.curves
				rendered, err := GetResources(cfg)
				require.NoError(t, err)
				repeated, err := GetResources(cfg)
				require.NoError(t, err)
				require.Equal(t, rendered, repeated)
				pods := tlsPodSpecs(rendered)
				ccmCount, nodeCount := 0, 0
				for name, pod := range pods {
					require.Equal(t, withoutTLSFlags(baselinePods[name]), withoutTLSFlags(pod), name)
					for _, container := range pod.InitContainers {
						require.NotContains(t, strings.Join(append(container.Command, container.Args...), " "), "--tls-", name)
					}
					for _, container := range pod.Containers {
						command := strings.Join(append(container.Command, container.Args...), " ")
						isCCM := strings.Contains(command, "cloud-controller-manager")
						isNode := strings.Contains(command, "/bin/azure-cloud-node-manager")
						if !isCCM && !isNode {
							require.NotContains(t, command, "--tls-", name)
							continue
						}
						if isCCM {
							ccmCount++
						}
						if isNode {
							nodeCount++
						}
						for flag, value := range map[string]string{"min-version": settings.min, "cipher-suites": settings.ciphers, "curve-preferences": settings.curves} {
							if flag == "curve-preferences" && !consumer.enabled {
								value = ""
							}
							prefix := "--tls-" + flag + "="
							if value == "" {
								require.NotContains(t, command, prefix, name)
							} else {
								require.Equal(t, 1, strings.Count(command, prefix), name)
								require.Contains(t, strings.Fields(command), prefix+value, name)
							}
						}
						if isNode {
							require.Equal(t, cfg.ImagesReference.CloudNodeManagerAzure, container.Image)
							require.Contains(t, container.Ports, corev1.ContainerPort{Name: "https", ContainerPort: 10263, Protocol: corev1.ProtocolTCP})
						}
						if isCCM && consumer.provider != "ibm" && consumer.provider != "powervs" {
							require.Contains(t, command, "--secure-port=0")
						}
					}
				}
				require.Equal(t, 1, ccmCount)
				expectedNodes := 0
				if consumer.platform == configv1.AzurePlatformType {
					expectedNodes = 1
				}
				require.Equal(t, expectedNodes, nodeCount)
			}
			cfg.TLSMinVersion, cfg.TLSCipherSuites = "VersionTLS12", "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"
			var previous map[string]corev1.PodSpec
			for _, curves := range []string{"4588,29,23,24", "4587,4589", ""} {
				cfg.TLSCurvePreferences = curves
				objects, err := GetResources(cfg)
				require.NoError(t, err)
				pods := tlsPodSpecs(objects)
				if previous != nil {
					for name, pod := range pods {
						require.Equal(t, !consumer.enabled, reflect.DeepEqual(previous[name], pod), name)
					}
				}
				previous = pods
			}
			paths, err := filepath.Glob(filepath.Join(consumer.provider, "assets", "*.yaml"))
			require.NoError(t, err)
			stanzas := 0
			for _, path := range paths {
				content, err := os.ReadFile(path)
				require.NoError(t, err)
				if strings.Contains(string(content), "--tls-curve-preferences={{ .tlsCurvePreferences }}") {
					stanzas++
				}
			}
			require.Equal(t, 1, stanzas)
		})
	}
}

func TestNoLocalTLSProxyConsumer(t *testing.T) {
	t.Parallel()
	for _, root := range []string{".", "../../manifests", "../config"} {
		require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			switch filepath.Ext(path) {
			case ".go", ".yaml", ".yml", ".json":
			default:
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			require.NotContains(t, string(content), "kube-rbac-proxy", path)
			return nil
		}))
	}
}
