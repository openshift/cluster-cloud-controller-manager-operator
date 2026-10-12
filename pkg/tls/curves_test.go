package tls

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/cloud"
	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/config"
	utiltls "github.com/openshift/controller-runtime-common/pkg/tls"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCurveOverrides(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		version string
		ciphers []string
		curves  []int32
		invalid bool
	}{
		{"groups only", "", nil, []int32{4588, 29, 23, 24}, false},
		{"all seven", "VersionTLS12", []string{"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"}, []int32{29, 23, 24, 25, 4588, 4587, 4589}, false},
		{"TLS13", "VersionTLS13", []string{"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"}, []int32{4588}, false},
		{"duplicate", "", nil, []int32{29, 29}, true},
		{"zero", "", nil, []int32{0}, true},
		{"negative", "", nil, []int32{-1}, true},
		{"overflow", "", nil, []int32{65536}, true},
		{"unknown", "", nil, []int32{1234}, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			require.True(t, HasTLSOverrides(testCase.version, testCase.ciphers, testCase.curves))
			result, err := ResolveTLSConfig(context.Background(), nil, testCase.version, testCase.ciphers, testCase.curves)
			if testCase.invalid {
				require.ErrorContains(t, err, "invalid --tls-curve-preferences")
				return
			}
			require.NoError(t, err)
			resolved := &tls.Config{}
			result.TLSConfig(resolved)
			for index, curve := range testCase.curves {
				require.Equal(t, tls.CurveID(curve), resolved.CurvePreferences[index])
			}
			require.Empty(t, result.TLSAdherencePolicy)
			require.Equal(t, configv1.TLSProfileSpec{}, result.TLSProfileSpec)
			if testCase.version == "VersionTLS13" {
				require.Nil(t, resolved.CipherSuites)
			}
		})
	}
	require.False(t, HasTLSOverrides("", nil, nil))
	require.True(t, HasTLSOverrides("VersionTLS12", nil, nil))
	require.True(t, HasTLSOverrides("", []string{"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"}, nil))
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	curves := flags.Int32Slice("tls-curve-preferences", nil, "")
	require.NoError(t, flags.Parse([]string{"--tls-curve-preferences=4588,29,23,24"}))
	require.Equal(t, []int32{4588, 29, 23, 24}, *curves)
}

func TestProfileCurveConversion(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		groups      []configv1.TLSGroup
		want        []tls.CurveID
		unsupported []string
	}{
		{"absent", nil, nil, nil},
		{"all seven", []configv1.TLSGroup{"X25519", "secp256r1", "secp384r1", "secp521r1", "X25519MLKEM768", "SecP256r1MLKEM768", "SecP384r1MLKEM1024"}, []tls.CurveID{29, 23, 24, 25, 4588, 4587, 4589}, nil},
		{"mixed", []configv1.TLSGroup{"X25519MLKEM768", "future"}, []tls.CurveID{4588}, []string{"future"}},
		{"unsupported", []configv1.TLSGroup{"future"}, nil, []string{"future"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			configure, unsupported := utiltls.NewTLSConfigFromProfile(configv1.TLSProfileSpec{MinTLSVersion: configv1.VersionTLS13, Groups: testCase.groups})
			resolved := &tls.Config{}
			configure(resolved)
			require.Equal(t, testCase.want, resolved.CurvePreferences)
			require.Equal(t, testCase.unsupported, unsupported)
			require.Empty(t, resolved.CipherSuites)
		})
	}
}

func TestGroupsOnlyWatcherChanges(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	require.NoError(t, configv1.AddToScheme(scheme))
	profile := configv1.TLSProfileSpec{MinTLSVersion: configv1.VersionTLS12, Groups: []configv1.TLSGroup{"X25519"}}
	apiServer := &configv1.APIServer{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}, Spec: configv1.APIServerSpec{TLSAdherence: configv1.TLSAdherencePolicyStrictAllComponents, TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType, Custom: &configv1.CustomTLSProfile{TLSProfileSpec: profile}}}}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(apiServer).Build()
	changes := 0
	watcher := &utiltls.SecurityProfileWatcher{Client: kubeClient, InitialTLSProfileSpec: profile, InitialTLSAdherencePolicy: apiServer.Spec.TLSAdherence, OnProfileChange: func(context.Context, configv1.TLSProfileSpec, configv1.TLSProfileSpec) { changes++ }}
	images := config.ImagesReference{CloudControllerManagerOperator: "operator", CloudControllerManagerAWS: "aws", CloudControllerManagerAzure: "azure", CloudNodeManagerAzure: "node-manager", CloudControllerManagerGCP: "gcp", CloudControllerManagerIBM: "ibm", CloudControllerManagerNutanix: "nutanix", CloudControllerManagerOpenStack: "openstack", CloudControllerManagerPowerVS: "powervs", CloudControllerManagerVSphere: "vsphere"}
	imageData, err := json.Marshal(images)
	require.NoError(t, err)
	imagePath := filepath.Join(t.TempDir(), "images.json")
	require.NoError(t, os.WriteFile(imagePath, imageData, 0600))
	previous := map[string][]client.Object{}
	for groupIndex, groups := range [][]configv1.TLSGroup{{"X25519"}, {"X25519MLKEM768"}, {"X25519MLKEM768"}, nil, nil} {
		restartContext, cancel := context.WithCancel(context.Background())
		expectedOld := watcher.InitialTLSProfileSpec
		watcher.OnProfileChange = func(_ context.Context, old, current configv1.TLSProfileSpec) {
			require.Equal(t, expectedOld, old)
			require.Equal(t, groups, current.Groups)
			changes++
			cancel()
		}
		apiServer.Spec.TLSSecurityProfile.Custom.Groups = groups
		require.NoError(t, kubeClient.Update(context.Background(), apiServer))
		_, err := watcher.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: "cluster"}})
		require.NoError(t, err)
		require.Equal(t, []int{0, 1, 1, 2, 2}[groupIndex], changes)
		if groupIndex == 1 || groupIndex == 3 {
			require.ErrorIs(t, restartContext.Err(), context.Canceled)
		} else {
			require.NoError(t, restartContext.Err())
		}
		cancel()
		resolved, err := resolveTLSConfigFromClient(context.Background(), kubeClient)
		require.NoError(t, err)
		resolvedTLS := &tls.Config{}
		resolved.TLSConfig(resolvedTLS)
		wantCurves := [][]tls.CurveID{{29}, {4588}, {4588}, nil, nil}[groupIndex]
		require.Equal(t, wantCurves, resolvedTLS.CurvePreferences)
		wantString := []string{"29", "4588", "4588", "", ""}[groupIndex]
		for _, platform := range []configv1.PlatformType{configv1.AWSPlatformType, configv1.AzurePlatformType, configv1.GCPPlatformType, configv1.IBMCloudPlatformType, configv1.NutanixPlatformType, configv1.OpenStackPlatformType, configv1.PowerVSPlatformType, configv1.VSpherePlatformType} {
			for _, stack := range []bool{false, true} {
				if stack && platform != configv1.AzurePlatformType {
					continue
				}
				status := &configv1.PlatformStatus{Type: platform}
				key := string(platform)
				if stack {
					status.Azure = &configv1.AzurePlatformStatus{CloudName: configv1.AzureStackCloud}
					key += "Stack"
				}
				infra := &configv1.Infrastructure{Status: configv1.InfrastructureStatus{PlatformStatus: status, InfrastructureName: "test-cluster"}}
				cfg, err := config.ComposeConfig(infra, nil, imagePath, "openshift-cloud-controller-manager", nil, resolved.TLSConfig)
				require.NoError(t, err)
				require.Equal(t, wantString, cfg.TLSCurvePreferences)
				objects, err := cloud.GetResources(cfg)
				require.NoError(t, err)
				deferred := platform == configv1.NutanixPlatformType || platform == configv1.PowerVSPlatformType
				templates := map[string]corev1.PodTemplateSpec{}
				oldTemplates := map[string]corev1.PodTemplateSpec{}
				for _, object := range objects {
					switch workload := object.(type) {
					case *appsv1.Deployment:
						templates[workload.Name] = workload.Spec.Template
					case *appsv1.DaemonSet:
						templates[workload.Name] = workload.Spec.Template
					}
				}
				for _, object := range previous[key] {
					switch workload := object.(type) {
					case *appsv1.Deployment:
						oldTemplates[workload.Name] = workload.Spec.Template
					case *appsv1.DaemonSet:
						oldTemplates[workload.Name] = workload.Spec.Template
					}
				}
				for name, template := range templates {
					for _, container := range template.Spec.Containers {
						command := strings.Join(append(container.Command, container.Args...), " ")
						if !strings.Contains(command, "cloud-controller-manager") && !strings.Contains(command, "exec /bin/azure-cloud-node-manager") {
							require.NotContains(t, command, "--tls-curve-preferences=")
							continue
						}
						if wantString == "" || deferred {
							require.NotContains(t, command, "--tls-curve-preferences=")
						} else {
							require.Equal(t, 1, strings.Count(command, "--tls-curve-preferences="))
							require.Contains(t, strings.Fields(command), "--tls-curve-preferences="+wantString)
						}
					}
					if old, exists := oldTemplates[name]; exists {
						if (groupIndex == 1 || groupIndex == 3) && !deferred {
							require.NotEqual(t, old, template, name)
						} else {
							require.Equal(t, old, template, name)
						}
						pattern := regexp.MustCompile(`--tls-curve-preferences=\S+[ \t]*\\?`)
						for _, normalize := range []*corev1.PodTemplateSpec{&old, &template} {
							*normalize = *normalize.DeepCopy()
							for index := range normalize.Spec.Containers {
								for commandIndex, command := range normalize.Spec.Containers[index].Command {
									normalize.Spec.Containers[index].Command[commandIndex] = strings.Join(strings.Fields(pattern.ReplaceAllString(command, "")), " ")
								}
							}
						}
						require.Equal(t, old, template, name)
					}
				}
				if old, exists := previous[key]; exists {
					changed := groupIndex == 1 || groupIndex == 3
					if changed && !deferred {
						require.NotEqual(t, old, objects, key)
					} else {
						require.Equal(t, old, objects, key)
					}
				}
				previous[key] = objects
			}
		}
	}
	require.Equal(t, 2, changes)
	watcher.OnProfileChange = func(context.Context, configv1.TLSProfileSpec, configv1.TLSProfileSpec) { changes++ }
	apiServer.Spec.TLSSecurityProfile.Custom.MinTLSVersion = configv1.VersionTLS13
	require.NoError(t, kubeClient.Update(context.Background(), apiServer))
	_, err = watcher.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: "cluster"}})
	require.NoError(t, err)
	require.Equal(t, 3, changes)
}

func TestResolvedProfileGroups(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	require.NoError(t, configv1.AddToScheme(scheme))
	for _, testCase := range []struct {
		name      string
		adherence configv1.TLSAdherencePolicy
		profile   *configv1.TLSSecurityProfile
		missing   bool
		want      []tls.CurveID
	}{
		{"strict custom", configv1.TLSAdherencePolicyStrictAllComponents, &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType, Custom: &configv1.CustomTLSProfile{TLSProfileSpec: configv1.TLSProfileSpec{MinTLSVersion: configv1.VersionTLS12, Groups: []configv1.TLSGroup{"X25519MLKEM768", "X25519", "secp256r1", "secp384r1"}}}}, false, []tls.CurveID{4588, 29, 23, 24}},
		{"strict empty", configv1.TLSAdherencePolicyStrictAllComponents, &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType, Custom: &configv1.CustomTLSProfile{TLSProfileSpec: configv1.TLSProfileSpec{MinTLSVersion: configv1.VersionTLS12}}}, false, nil},
		{"future adherence", "future", &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType, Custom: &configv1.CustomTLSProfile{TLSProfileSpec: configv1.TLSProfileSpec{MinTLSVersion: configv1.VersionTLS13, Groups: []configv1.TLSGroup{"future", "SecP256r1MLKEM768"}}}}, false, []tls.CurveID{4587}},
		{"all unsupported", configv1.TLSAdherencePolicyStrictAllComponents, &configv1.TLSSecurityProfile{Type: configv1.TLSProfileCustomType, Custom: &configv1.CustomTLSProfile{TLSProfileSpec: configv1.TLSProfileSpec{MinTLSVersion: configv1.VersionTLS12, Groups: []configv1.TLSGroup{"future"}}}}, false, nil},
		{"named", configv1.TLSAdherencePolicyStrictAllComponents, &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType}, false, []tls.CurveID{4588, 29, 23, 24}},
		{"default", configv1.TLSAdherencePolicyStrictAllComponents, nil, false, []tls.CurveID{4588, 29, 23, 24}},
		{"no opinion", "", &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType}, false, []tls.CurveID{4588, 29, 23, 24}},
		{"legacy", configv1.TLSAdherencePolicyLegacyAdheringComponentsOnly, &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType}, false, []tls.CurveID{4588, 29, 23, 24}},
		{"missing", "", nil, true, []tls.CurveID{4588, 29, 23, 24}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if !testCase.missing {
				builder.WithObjects(&configv1.APIServer{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}, Spec: configv1.APIServerSpec{TLSAdherence: testCase.adherence, TLSSecurityProfile: testCase.profile}})
			}
			result, err := resolveTLSConfigFromClient(context.Background(), builder.Build())
			require.NoError(t, err)
			metrics, webhook := &tls.Config{}, &tls.Config{}
			result.TLSConfig(metrics)
			result.TLSConfig(webhook)
			require.Equal(t, testCase.want, metrics.CurvePreferences)
			require.Equal(t, metrics, webhook)
		})
	}
}
