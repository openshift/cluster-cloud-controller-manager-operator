package main

import (
	"os"
	"regexp"
	"strings"

	"github.com/openshift-eng/openshift-tests-extension/pkg/cmd"
	"github.com/openshift-eng/openshift-tests-extension/pkg/extension"
	"github.com/openshift-eng/openshift-tests-extension/pkg/extension/extensiontests"
	"github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	utilflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/logs"
	"k8s.io/kubernetes/test/e2e/framework"

	// Importing ginkgo tests from the CCM e2e packages
	_ "github.com/openshift/cluster-cloud-controller-manager-operator/openshift-tests/operator-tests/e2e/common"
	_ "github.com/openshift/cluster-cloud-controller-manager-operator/openshift-tests/operator-tests/e2e/operator"
)

var (
	// testContext is the global test context that is used to store the test configuration.
	testContext = &framework.TestContext
)

func main() {
	logs.InitLogs()
	defer logs.FlushLogs()
	pflag.CommandLine.SetNormalizeFunc(utilflag.WordSepNormalizeFunc)

	// Create our registry of openshift-tests extensions
	extensionRegistry := extension.NewRegistry()
	kubeTestsExtension := extension.NewExtension("openshift", "payload", "cluster-cloud-controller-manager-operator")
	extensionRegistry.Register(kubeTestsExtension)

	kubeTestsExtension.AddSuite(extension.Suite{
		Name:       "ccm/operator/conformance/parallel",
		Qualifiers: []string{`!labels.exists(l, l == "Serial") && labels.exists(l, l == "Conformance")`},
	})

	kubeTestsExtension.AddSuite(extension.Suite{
		Name:       "ccm/operator/conformance/serial",
		Qualifiers: []string{`labels.exists(l, l == "Serial") && labels.exists(l, l == "Conformance")`},
	})

	// Build our specs from ginkgo
	specs, err := ginkgo.BuildExtensionTestSpecsFromOpenShiftGinkgoSuite()
	if err != nil {
		panic(err)
	}

	// Let's scan for tests with a platform label and create the rule for them such as [platform:aws]
	foundPlatforms := make(map[string]string)
	for _, test := range specs.Select(extensiontests.NameContains("[platform:")).Names() {
		re := regexp.MustCompile(`\[platform:[a-z]*]`)
		match := re.FindStringSubmatch(test)
		for _, platformDef := range match {
			if _, ok := foundPlatforms[platformDef]; !ok {
				platform := platformDef[strings.Index(platformDef, ":")+1 : len(platformDef)-1]
				foundPlatforms[platformDef] = platform
				specs.Select(extensiontests.NameContains(platformDef)).
					Include(extensiontests.PlatformEquals(platform))
			}
		}

	}

	kubeTestsExtension.AddSpecs(specs)

	// Cobra stuff
	root := &cobra.Command{
		Long: "Cluster Cloud Controller Manager Operator tests extension for OpenShift",
	}

	root.AddCommand(cmd.DefaultExtensionCommands(extensionRegistry)...)

	if err := func() error {
		return root.Execute()
	}(); err != nil {
		os.Exit(1)
	}
}
