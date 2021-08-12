package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"

	"github.com/spf13/cobra"
)

const (
	clientIDEnvKey     = "AZURE_CLIENT_ID"
	clientSecretEnvKey = "AZURE_CLIENT_SECRET"

	clientIDCloudConfigKey     = "aadClientId"
	clientSecretCloudConfigKey = "aadClientSecret"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "azure-config-credentials-injector [cloud-config-file-path] [output-file-path]",
		Short: "Cloud config credentials injection tool for azure cloud platform",
		Args:  cobra.ExactArgs(2),
		RunE:  mergeCloudConfig,
	}

	if err := rootCmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func mergeCloudConfig(_ *cobra.Command, args []string) error {
	cloudConfigPath := args[0]
	if _, err := os.Stat(cloudConfigPath); os.IsNotExist(err) {
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

	cloudConfig, err := readCloudConfig(cloudConfigPath)
	if err != nil {
		return err
	}

	preparedCloudConfig, err := prepareCloudConfig(cloudConfig, azureClientId, azureClientSecret)
	if err != nil {
		return err
	}

	outputPath := args[1]
	if err := writeCloudConfig(outputPath, preparedCloudConfig); err != nil {
		return err
	}

	return nil
}

func readCloudConfig(path string) (map[string]interface{}, error) {
	var data map[string]interface{}

	rawData, err := ioutil.ReadFile(path)
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

	marshalled, err := json.Marshal(cloudConfig)
	if err != nil {
		return nil, err
	}

	return marshalled, nil
}

func writeCloudConfig(path string, preparedConfig []byte) error {
	if err := ioutil.WriteFile(path, preparedConfig, 0644); err != nil {
		return err
	}
	return nil
}
