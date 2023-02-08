package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

const (
	clientIDEnvKey     = "AZURE_CLIENT_ID"
	clientSecretEnvKey = "AZURE_CLIENT_SECRET"

	clientIDCloudConfigKey               = "aadClientId"
	clientSecretCloudConfigKey           = "aadClientSecret"
	useManagedIdentityExtensionConfigKey = "useManagedIdentityExtension"
)

var (
	injectorCmd = &cobra.Command{
		Use:   "azure-config-credentials-injector [OPTIONS]",
		Short: "Cloud config credentials injection tool for azure cloud platform",
		RunE:  mergeCloudConfig,
	}

	injectorOpts struct {
		cloudConfigFilePath          string
		outputFilePath               string
		disableIdentityExtensionAuth bool
	}
)

func init() {
	klog.InitFlags(flag.CommandLine)
	injectorCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
	injectorCmd.PersistentFlags().StringVar(&injectorOpts.cloudConfigFilePath, "cloud-config-file-path", "/tmp/cloud-config/cloud.conf", "Location of the original cloud config file.")
	injectorCmd.PersistentFlags().StringVar(&injectorOpts.outputFilePath, "output-file-path", "/tmp/merged-cloud-config/cloud.conf", "Location of the generated cloud config file with injected credentials.")
	injectorCmd.PersistentFlags().BoolVar(&injectorOpts.disableIdentityExtensionAuth, "disable-identity-extension-auth", false, "Disable managed identity authentication, if it's set in cloudConfig.")
}

func main() {
	if err := injectorCmd.Execute(); err != nil {
		klog.Fatal(err)
	}
}

func mergeCloudConfig(_ *cobra.Command, args []string) error {
	if _, err := os.Stat(injectorOpts.cloudConfigFilePath); os.IsNotExist(err) {
		return err
	}

	azureClientId, found := os.LookupEnv(clientIDEnvKey)
	if !found {
		return fmt.Errorf("%s env variable should be set up", clientIDEnvKey)
	}

	azureClientSecret, found := os.LookupEnv(clientSecretEnvKey)
	if !found {
		return fmt.Errorf("%s env variable should be set up", clientSecretEnvKey)
	}

	cloudConfig, err := readCloudConfig(injectorOpts.cloudConfigFilePath)
	if err != nil {
		return fmt.Errorf("couldn't read cloud config from file: %w", err)
	}

	preparedCloudConfig, err := prepareCloudConfig(cloudConfig, azureClientId, azureClientSecret)
	if err != nil {
		return fmt.Errorf("couldn't prepare cloud config: %w", err)
	}

	if err := writeCloudConfig(injectorOpts.outputFilePath, preparedCloudConfig); err != nil {
		return fmt.Errorf("couldn't write prepared cloud config to file: %w", err)
	}

	return nil
}

func readCloudConfig(path string) (map[string]interface{}, error) {
	var data map[string]interface{}

	rawData, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(rawData, &data); err != nil {
		return nil, err
	}
	return data, nil
}

func prepareCloudConfig(cloudConfig map[string]interface{}, clientId string, clientSecret string) ([]byte, error) {
	cloudConfig[clientIDCloudConfigKey] = clientId
	cloudConfig[clientSecretCloudConfigKey] = clientSecret
	if value, found := cloudConfig[useManagedIdentityExtensionConfigKey]; found {
		if injectorOpts.disableIdentityExtensionAuth {
			klog.Infof("%s cleared\n", useManagedIdentityExtensionConfigKey)
			cloudConfig[useManagedIdentityExtensionConfigKey] = false
		} else {
			if value == true {
				klog.Warningf("Warning: %s is set to \"true\", injected credentials may not be used\n", useManagedIdentityExtensionConfigKey)
			}
		}
	}

	marshalled, err := json.Marshal(cloudConfig)
	if err != nil {
		return nil, err
	}

	return marshalled, nil
}

func writeCloudConfig(path string, preparedConfig []byte) error {
	if err := os.WriteFile(path, preparedConfig, 0644); err != nil {
		return err
	}
	return nil
}
