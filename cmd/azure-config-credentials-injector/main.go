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
	clientIDCloudConfigKey               = "aadClientId"
	clientSecretCloudConfigKey           = "aadClientSecret"
	useManagedIdentityExtensionConfigKey = "useManagedIdentityExtension"

	tenantIdConfigKey                              = "tenantId"
	aadFederatedTokenFileConfigKey                 = "aadFederatedTokenFile"
	useFederatedWorkloadIdentityExtensionConfigKey = "useFederatedWorkloadIdentityExtension"
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
		enableWorkloadIdentity       string
		disableIdentityExtensionAuth bool
		credentialsPath              string
	}
)

func init() {
	klog.InitFlags(flag.CommandLine)
	injectorCmd.PersistentFlags().AddGoFlagSet(flag.CommandLine)
	injectorCmd.PersistentFlags().StringVar(&injectorOpts.cloudConfigFilePath, "cloud-config-file-path", "/tmp/cloud-config/cloud.conf", "Location of the original cloud config file.")
	injectorCmd.PersistentFlags().StringVar(&injectorOpts.outputFilePath, "output-file-path", "/tmp/merged-cloud-config/cloud.conf", "Location of the generated cloud config file with injected credentials.")
	injectorCmd.PersistentFlags().BoolVar(&injectorOpts.disableIdentityExtensionAuth, "disable-identity-extension-auth", false, "Disable managed identity authentication, if it's set in cloudConfig.")
	injectorCmd.PersistentFlags().StringVar(&injectorOpts.enableWorkloadIdentity, "enable-azure-workload-identity", "false", "Enable workload identity authentication.")
	injectorCmd.PersistentFlags().StringVar(&injectorOpts.credentialsPath, "creds-path", "/etc/azure/credentials", "Path of the credential file.")
}

func main() {
	if err := injectorCmd.Execute(); err != nil {
		klog.Fatal(err)
	}
}

func mergeCloudConfig(_ *cobra.Command, args []string) error {
	var (
		azureClientId           string
		tenantId                string
		tenantIdFound           bool
		federatedTokenFile      string
		federatedTokenFileFound bool
		azureClientSecret       string
		secretFound             bool
		err                     error
	)

	if _, err := os.Stat(injectorOpts.cloudConfigFilePath); os.IsNotExist(err) {
		return err
	}

	// Read credentials from mounted Secret files
	azureClientId, err = readSecretFile(fmt.Sprintf("%s/azure_client_id", injectorOpts.credentialsPath))
	if err != nil {
		return fmt.Errorf("failed to read azure_client_id from secret: %w", err)
	}

	azureClientSecret, err = readSecretFile(fmt.Sprintf("%s/azure_client_secret", injectorOpts.credentialsPath))
	if err == nil {
		secretFound = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to read azure_client_secret from secret: %w", err)
	}

	federatedTokenFile, err = readSecretFile(fmt.Sprintf("%s/azure_federated_token_file", injectorOpts.credentialsPath))
	if err == nil {
		federatedTokenFileFound = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to read azure_federated_token_file from secret: %w", err)
	}

	tenantId, err = readSecretFile(fmt.Sprintf("%s/azure_tenant_id", injectorOpts.credentialsPath))
	if err == nil {
		tenantIdFound = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to read azure_tenant_id from secret: %w", err)
	}

	// If federatedTokenFile found, workload identity should be used
	if federatedTokenFileFound {
		// azureClientSecret should not be set for workload identity auth, report error when secretFound
		if secretFound {
			return fmt.Errorf("azure_client_secret is set while workload identity is enabled using azure_federated_token_file, this should never happen.\nPlease consider reporting a bug: https://issues.redhat.com")
		}
		// tenantId is required for workload identity auth, report error when !tenantIdFound
		if !tenantIdFound {
			return fmt.Errorf("azure_tenant_id should be set up while workload identity is enabled using azure_federated_token_file, this should never happen.\nPlease consider reporting a bug: https://issues.redhat.com")
		}
	} else {
		// federatedTokenFile not found, secret will be required
		if !secretFound {
			return fmt.Errorf("azure_client_secret should be set up")
		}
	}

	cloudConfig, err := readCloudConfig(injectorOpts.cloudConfigFilePath)
	if err != nil {
		return fmt.Errorf("couldn't read cloud config from file: %w", err)
	}

	preparedCloudConfig, err := prepareCloudConfig(cloudConfig, azureClientId, azureClientSecret, tenantId, federatedTokenFile)
	if err != nil {
		return fmt.Errorf("couldn't prepare cloud config: %w", err)
	}

	if err := writeCloudConfig(injectorOpts.outputFilePath, preparedCloudConfig); err != nil {
		return fmt.Errorf("couldn't write prepared cloud config to file: %w", err)
	}

	return nil
}

// Helper function to read a file and return its content as a string
func readSecretFile(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return string(data), nil
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

func prepareCloudConfig(cloudConfig map[string]interface{}, clientId, clientSecret, tenantId, federatedTokenFile string) ([]byte, error) {
	cloudConfig[clientIDCloudConfigKey] = clientId

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

	if len(tenantId) != 0 && len(federatedTokenFile) != 0 {
		cloudConfig[tenantIdConfigKey] = tenantId
		cloudConfig[aadFederatedTokenFileConfigKey] = federatedTokenFile
		cloudConfig[useFederatedWorkloadIdentityExtensionConfigKey] = true
	} else {
		klog.V(4).Infof("azure_client_secret is set, client secret authentication will be used")
		cloudConfig[clientSecretCloudConfigKey] = clientSecret
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
