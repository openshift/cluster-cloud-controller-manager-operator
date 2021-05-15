package main

import (
	"flag"
	"fmt"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/render"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

var (
	renderCmd = &cobra.Command{
		Use:   "run",
		Short: "Starts Cluster Cloud Controller Manager in render mode",
		Long:  "",
		RunE:  runRenderCmd,
	}

	renderOpts struct {
		destinationDir string
	}
)

func init() {
	renderCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
	renderCmd.PersistentFlags().StringVar(&renderOpts.destinationDir, "dest-dir", "", "The destination dir where CCCMO writes the generated static pods for CCM.")
}

func runRenderCmd(cmd *cobra.Command, args []string) error {
	flag.Set("logtostderr", "true")
	flag.Parse()

	if renderOpts.destinationDir == "" {
		return fmt.Errorf("--dest-dir is not set")
	}

	if err := render.New().Run(renderOpts.destinationDir); err != nil {
		return err
	}

	return nil
}

func main() {
	if err := renderCmd.Execute(); err != nil {
		klog.Exitf("Error executing render: %v", err)
	}
}
