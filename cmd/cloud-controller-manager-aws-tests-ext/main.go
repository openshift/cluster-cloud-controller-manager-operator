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

	// Importing ginkgo tests from the CCM e2e package
	_ "github.com/openshift/cluster-cloud-controller-manager-operator/cmd/cloud-controller-manager-aws-tests-ext/e2e"
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

	// Initialize framework for the tests.
	if err := initFrameworkForTests(); err != nil {
		panic(fmt.Errorf("failed to initialize test framework: %w", err))
	}

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
	// TODO: Exclude specs when the environment variable is set.

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

// initFrameworkForTests initializes the framework for the tests globally.
func initFrameworkForTests() error {
	if len(os.Getenv("KUBECONFIG")) == 0 {
		return fmt.Errorf("KUBECONFIG is empty. Set the KUBECONFIG environment variable")
	}

	// Initialize framework - required for test discovery
	// TODO:
	// 1. Fix the provider getting from env (when ote supports aws)
	// 2. Build the config from the env, and set the testContext.CloudConfig (if required by the test)
	// 3. Move this init to a dedicated function
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

	// Load kube client config and set the host variable for kubectl
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

	// After reading all flags, this will configure the test context, and need to be
	// called once by framework to avoid re-configuring the test context, and leding
	// to issues in Ginkgo phases (PhaseBuildTopLevel, PhaseBuildTree, PhaseRun),
	// such as:'cannot clone suite after tree has been built'
	framework.AfterReadingAllFlags(testContext)

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
