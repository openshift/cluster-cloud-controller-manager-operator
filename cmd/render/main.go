package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/openshift/cluster-cloud-controller-manager-operator/pkg/render"
	"github.com/spf13/cobra"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

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
		destinationDir        string
		clusterInfrastructure string
	}
)

func init() {
	renderCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
	renderCmd.PersistentFlags().StringVar(&renderOpts.destinationDir, "dest-dir", "", "The destination dir where CCCMO writes the generated static pods for CCM.")
	renderCmd.PersistentFlags().StringVar(&renderOpts.clusterInfrastructure, "cluster-infrastructure-file", "", "Input path for the cluster infrastructure file.")
	renderCmd.MarkFlagRequired("dest-dir")
	renderCmd.MarkFlagRequired("cluster-infrastructure-file")
}

func runRenderCmd(cmd *cobra.Command, args []string) error {
	flag.Set("logtostderr", "true")
	flag.Parse()

	if err := validate(renderOpts.destinationDir, renderOpts.clusterInfrastructure); err != nil {
		return err
	}

	if err := render.New().Run(renderOpts.destinationDir); err != nil {
		return err
	}

	return nil
}

// validate verifies all file and dirs exist
func validate(destinationDir, clusterInfrastructure string) error {
	errs := []error{}
	if err := isDir(destinationDir); err != nil {
		errs = append(errs, fmt.Errorf("error reading --dest-dir: %s", err))
	}
	if err := isFile(clusterInfrastructure); err != nil {
		errs = append(errs, fmt.Errorf("error reading --cluster-infrastructure-file: %s", err))
	}

	if len(errs) > 0 {
		return utilerrors.NewAggregate(errs)
	}

	return nil
}

func isFile(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%q is not a regular file", path)
	}
	if st.Size() <= 0 {
		return fmt.Errorf("%q is empty", path)
	}

	return nil
}

func isDir(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsDir() {
		return fmt.Errorf("%q is not a regular file", path)
	}

	return nil
}

func main() {
	if err := renderCmd.Execute(); err != nil {
		klog.Exitf("Error executing render: %v", err)
	}
}
