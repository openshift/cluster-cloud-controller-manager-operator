package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd"
	e "github.com/openshift-eng/openshift-tests-extension/pkg/extension"
	"github.com/openshift-eng/openshift-tests-extension/pkg/extension/extensiontests"
	g "github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	kclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/kubernetes/test/e2e/framework"

	log "github.com/sirupsen/logrus"

	// Importing ginkgo tests from the CCM e2e packages
	_ "github.com/openshift/cluster-cloud-controller-manager-operator/openshift-tests/ccm-aws-tests/e2e/aws"
	_ "github.com/openshift/cluster-cloud-controller-manager-operator/openshift-tests/ccm-aws-tests/e2e/common"
	_ "k8s.io/cloud-provider-aws/tests/e2e"
)

var (
	// testContext is the global test context that is used to store the test configuration.
	testContext = &framework.TestContext
)

func main() {
	registry := e.NewRegistry()
	ext := e.NewExtension("openshift", "payload", "aws-cloud-controller-manager")

	ext.AddSuite(e.Suite{
		Name:       "ccm/aws/conformance/parallel",
		Qualifiers: []string{`!labels.exists(l, l == "Serial") && labels.exists(l, l == "Conformance")`},
	})

	// Initialize framework with cluster-independent settings. Cluster-dependent
	// initialization (kubeconfig, host) is deferred to AddBeforeAll so that
	// "info" and "list tests" commands work without cluster access.
	initFrameworkDefaults()

	// Build the extension test specs
	specs, err := g.BuildExtensionTestSpecsFromOpenShiftGinkgoSuite()
	if err != nil {
		panic(fmt.Errorf("failed to build extension test specs: %w", err))
	}

	// Select any of the loadbalancer and nodes specs.
	// We need to filter to prevent adding ECR tests.
	// All upstream tests must be runnable on OpenShift, if issues are found, let's try to
	// fix in upstream to work well with OpenShift and cloud-provider-aws CI.
	specs, err = specs.MustSelectAny([]extensiontests.SelectFunction{
		extensiontests.NameContains("[cloud-provider-aws-e2e] loadbalancer"),
		extensiontests.NameContains("[cloud-provider-aws-e2e] nodes"),
		extensiontests.NameContains("[cloud-provider-aws-e2e-openshift]"),
	})
	if err != nil {
		panic(fmt.Errorf("failed to select specs: %w", err))
	}

	// Skip set of tests when topology is SingleReplica.
	singleReplicaSkips := []string{
		"nodes should label nodes with topology network info if instance is supported",
		"nodes should set zone-id topology label",
	}

	// Add the suite name to the spec name and apply topology-based exclusions.
	specs.Walk(func(spec *extensiontests.ExtensionTestSpec) {
		spec.Name = spec.Name + " [Suite:openshift/conformance/parallel]"

		// Exclude specific tests when topology is SingleReplica.
		for _, skip := range singleReplicaSkips {
			if strings.Contains(spec.Name, skip) {
				spec.Exclude(extensiontests.TopologyEquals("SingleReplica"))
			}
		}

	}).Include(extensiontests.PlatformEquals("aws"))
	specs.AddBeforeAll(func() {
		if err := initFrameworkCluster(); err != nil {
			panic(fmt.Errorf("failed to initialize cluster config: %w", err))
		}
		if err := initFrameworkForTest(); err != nil {
			panic(fmt.Errorf("failed to initialize test framework: %w", err))
		}
	})

	ext.AddSpecs(specs)
	registry.Register(ext)

	root := &cobra.Command{
		Long: "AWS Cloud Controller Manager tests extension for OpenShift",
	}
	root.AddCommand(cmd.DefaultExtensionCommands(registry)...)
	if err := func() error {
		return root.Execute()
	}(); err != nil {
		log.Errorf("Failed to execute root command: %v", err)
		os.Exit(1)
	}
}

// getRegionFromEnv gets the region from the environment variables.
func getRegionFromEnv() string {
	region := os.Getenv("LEASED_RESOURCE")
	if len(region) > 0 {
		log.Debugf("Using region from LEASED_RESOURCE: %s", region)
		os.Setenv("AWS_REGION", region)
		return region
	}
	region = os.Getenv("AWS_REGION")
	if len(region) > 0 {
		log.Debugf("Using region from AWS_REGION: %s", region)
		return region
	}
	region = os.Getenv("AWS_DEFAULT_REGION")
	if len(region) > 0 {
		log.Debugf("Using region from AWS_DEFAULT_REGION: %s", region)
		os.Setenv("AWS_REGION", region)
		return region
	}
	return ""
}

// initFrameworkDefaults sets cluster-independent framework defaults needed
// for test discovery. This runs at startup and does not require KUBECONFIG.
// TODO:
// 1. Fix the provider getting from env (when ote supports aws)
// 2. Build the config from the env, and set the testContext.CloudConfig (if required by the test)
func initFrameworkDefaults() {
	testContext.Provider = "local" // TODO: OTE supports local or skeleton

	// Set up AWS cloud configuration when environment variables are set.
	region := getRegionFromEnv()
	if len(region) > 0 {
		testContext.CloudConfig = framework.CloudConfig{Region: region}
	}

	// General flags
	testContext.KubectlPath = "kubectl"
	testContext.DeleteNamespace = os.Getenv("DELETE_NAMESPACE") != "false"
	testContext.AllowedNotReadyNodes = -1
	testContext.MinStartupPods = -1
	testContext.MaxNodesToGather = 0
	testContext.VerifyServiceAccount = true
	testContext.DumpLogsOnFailure = true

	// "debian" is used when not set. At least GlusterFS tests need "custom".
	// (There is no option for "rhel" or "centos".)
	testContext.NodeOSDistro = "custom"
	testContext.MasterOSDistro = "custom"

	// Must be called during startup (before the Ginkgo tree is built) because it
	// internally calls ginkgo.PreviewSpecs which clones the suite.
	framework.AfterReadingAllFlags(testContext)
}

// initFrameworkCluster loads kubeconfig and sets the host for kubectl.
// This is called in AddBeforeAll so that "info" and "list tests" work without cluster access.
func initFrameworkCluster() error {
	if len(os.Getenv("KUBECONFIG")) == 0 {
		return fmt.Errorf("KUBECONFIG is empty. Set the KUBECONFIG environment variable")
	}

	testContext.KubeConfig = os.Getenv("KUBECONFIG")
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{
			ExplicitPath: testContext.KubeConfig,
		},
		&clientcmd.ConfigOverrides{},
	)
	cfg, err := clientConfig.ClientConfig()
	if err != nil {
		return fmt.Errorf("failed to get client config: %w", err)
	}
	testContext.Host = cfg.Host

	return nil
}

// initFrameworkForTest initializes the framework for the test instance.
func initFrameworkForTest() error {
	if ad := os.Getenv("ARTIFACT_DIR"); len(strings.TrimSpace(ad)) == 0 {
		if err := os.Setenv("ARTIFACT_DIR", filepath.Join(os.TempDir(), "artifacts")); err != nil {
			return fmt.Errorf("unable to set ARTIFACT_DIR: %v", err)
		}
	}

	// Output logs on failure when junit dir is explicitly set.
	if testDir := strings.TrimSpace(os.Getenv("TEST_JUNIT_DIR")); testDir != "" {
		testContext.ReportDir = testDir
	}

	testContext.CreateTestingNS = func(ctx context.Context, baseName string, c kclientset.Interface, labels map[string]string) (*corev1.Namespace, error) {
		return framework.CreateTestingNS(ctx, baseName, c, labels)
	}

	return nil
}
