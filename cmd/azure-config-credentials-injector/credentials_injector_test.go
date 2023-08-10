package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func executeCommand(root *cobra.Command, args ...string) (output string, err error) {
	_, output, err = executeCommandC(root, args...)
	return output, err
}

func executeCommandC(root *cobra.Command, args ...string) (c *cobra.Command, output string, err error) {
	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(args)

	c, err = root.ExecuteC()

	return c, buf.String(), err
}

func Test_mergeCloudConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cccmo-azure-creds-injector")
	require.NoError(t, err)
	defer os.Remove(tmpDir)

	inputFile, err := os.CreateTemp(tmpDir, "dummy-config")
	require.NoError(t, err)
	defer os.Remove(inputFile.Name())

	outputFile, err := os.CreateTemp(tmpDir, "dummy-config-merged")
	require.NoError(t, err)
	defer os.Remove(outputFile.Name())

	cleanupEnv := func(envVars map[string]string) {
		for envVarName := range envVars {
			err := os.Unsetenv(envVarName)
			require.NoError(t, err, "Cannot cleanup environment variables")
		}
	}
	cleanupOpts := func() {
		injectorOpts.disableIdentityExtensionAuth = false
		injectorOpts.cloudConfigFilePath = ""
		injectorOpts.outputFilePath = ""
	}

	cleanupInputFile := func(path string) {
		err := os.WriteFile(inputFile.Name(), []byte(""), 0644)
		require.NoError(t, err, "Cannot cleanup input file")
	}

	testCases := []struct {
		name            string
		args            []string
		envVars         map[string]string
		fileContent     string
		expectedContent string
		expectedErrMsg  string
	}{
		{
			name:           "input file does not exists",
			args:           []string{"--cloud-config-file-path", "foo"},
			expectedErrMsg: "stat foo: no such file or directory",
		},
		{
			name:           "AZURE_CLIENT_ID not set",
			args:           []string{"--cloud-config-file-path", inputFile.Name(), "--output-file-path", outputFile.Name()},
			expectedErrMsg: "AZURE_CLIENT_ID env variable should be set up",
		},
		{
			name:           "AZURE_CLIENT_SECRET not set",
			args:           []string{"--cloud-config-file-path", inputFile.Name(), "--output-file-path", outputFile.Name()},
			envVars:        map[string]string{"AZURE_CLIENT_ID": "foo"},
			expectedErrMsg: "AZURE_CLIENT_SECRET env variable should be set up",
		},
		{
			name:           "input file content is not a valid json",
			args:           []string{"--cloud-config-file-path", inputFile.Name(), "--output-file-path", outputFile.Name()},
			envVars:        map[string]string{"AZURE_CLIENT_ID": "foo", "AZURE_CLIENT_SECRET": "bar"},
			fileContent:    "{*&(&#@!}",
			expectedErrMsg: "couldn't read cloud config from file: invalid character '*' looking for beginning of object key string",
		},
		{
			name:           "input file content is valid json, but format is unexpected",
			args:           []string{"--cloud-config-file-path", inputFile.Name(), "--output-file-path", outputFile.Name()},
			envVars:        map[string]string{"AZURE_CLIENT_ID": "foo", "AZURE_CLIENT_SECRET": "bar"},
			fileContent:    "[1]",
			expectedErrMsg: "couldn't read cloud config from file: json: cannot unmarshal array into Go value of type map[string]interface {}",
		},
		{
			name:            "all ok, file is empty json object",
			args:            []string{"--cloud-config-file-path", inputFile.Name(), "--output-file-path", outputFile.Name()},
			envVars:         map[string]string{"AZURE_CLIENT_ID": "foo", "AZURE_CLIENT_SECRET": "bar"},
			fileContent:     "{}",
			expectedContent: "{\"aadClientId\":\"foo\",\"aadClientSecret\":\"bar\"}",
		},
		{
			name:            "all ok, some content in json",
			args:            []string{"--cloud-config-file-path", inputFile.Name(), "--output-file-path", outputFile.Name()},
			envVars:         map[string]string{"AZURE_CLIENT_ID": "foo", "AZURE_CLIENT_SECRET": "bar"},
			fileContent:     "{\"bar\": \"baz\"}",
			expectedContent: "{\"aadClientId\":\"foo\",\"aadClientSecret\":\"bar\",\"bar\":\"baz\"}",
		},
		{
			name:            "all ok, client_id and client_secret overrides",
			args:            []string{"--cloud-config-file-path", inputFile.Name(), "--output-file-path", outputFile.Name()},
			envVars:         map[string]string{"AZURE_CLIENT_ID": "foo", "AZURE_CLIENT_SECRET": "bar"},
			fileContent:     "{\"aadClientSecret\":\"fizz\",\"aadClientId\":\"baz\"}",
			expectedContent: "{\"aadClientId\":\"foo\",\"aadClientSecret\":\"bar\"}",
		},
		{
			name:           "output file write error",
			args:           []string{"--cloud-config-file-path", inputFile.Name(), "--output-file-path", "/tmp"},
			envVars:        map[string]string{"AZURE_CLIENT_ID": "foo", "AZURE_CLIENT_SECRET": "bar"},
			fileContent:    "{}",
			expectedErrMsg: "couldn't write prepared cloud config to file: open /tmp: is a directory",
		},
		{
			name:            "all ok, useManagedIdentityExtension not disabled",
			args:            []string{"--cloud-config-file-path", inputFile.Name(), "--output-file-path", outputFile.Name()},
			envVars:         map[string]string{"AZURE_CLIENT_ID": "foo", "AZURE_CLIENT_SECRET": "bar"},
			fileContent:     "{\"aadClientSecret\":\"fizz\",\"aadClientId\":\"baz\",\"useManagedIdentityExtension\":true}",
			expectedContent: "{\"aadClientId\":\"foo\",\"aadClientSecret\":\"bar\",\"useManagedIdentityExtension\":true}",
		},
		{
			name:            "all ok, useManagedIdentityExtension disabled",
			args:            []string{"--cloud-config-file-path", inputFile.Name(), "--output-file-path", outputFile.Name(), "--disable-identity-extension-auth"},
			envVars:         map[string]string{"AZURE_CLIENT_ID": "foo", "AZURE_CLIENT_SECRET": "bar"},
			fileContent:     "{\"aadClientSecret\":\"fizz\",\"aadClientId\":\"baz\",\"useManagedIdentityExtension\":true}",
			expectedContent: "{\"aadClientId\":\"foo\",\"aadClientSecret\":\"bar\",\"useManagedIdentityExtension\":false}",
		},
		{
			name:            "all ok, invalid useManagedIdentityExtension value",
			args:            []string{"--cloud-config-file-path", inputFile.Name(), "--output-file-path", outputFile.Name(), "--disable-identity-extension-auth"},
			envVars:         map[string]string{"AZURE_CLIENT_ID": "foo", "AZURE_CLIENT_SECRET": "bar"},
			fileContent:     "{\"aadClientSecret\":\"fizz\",\"aadClientId\":\"baz\",\"useManagedIdentityExtension\":\"true\"}",
			expectedContent: "{\"aadClientId\":\"foo\",\"aadClientSecret\":\"bar\",\"useManagedIdentityExtension\":false}",
		},
		{
			name:            "all ok, use workload identity while client secret is not present",
			args:            []string{"--cloud-config-file-path", inputFile.Name(), "--output-file-path", outputFile.Name(), "--disable-identity-extension-auth", "--enable-azure-workload-identity=true"},
			envVars:         map[string]string{"AZURE_TENANT_ID": "bar", "AZURE_CLIENT_ID": "buzz", "AZURE_FEDERATED_TOKEN_FILE": "baz"},
			fileContent:     "{\"tenantId\":\"foo\",\"aadClientId\":\"fizz\"}",
			expectedContent: "{\"aadClientId\":\"buzz\",\"aadFederatedTokenFile\":\"baz\",\"tenantId\":\"bar\",\"useFederatedWorkloadIdentityExtension\":true}",
		},
		{
			name:            "all ok, use workload identity while managed identity is not explicitly disabled",
			args:            []string{"--cloud-config-file-path", inputFile.Name(), "--output-file-path", outputFile.Name(), "--enable-azure-workload-identity=true"},
			envVars:         map[string]string{"AZURE_TENANT_ID": "bar", "AZURE_CLIENT_ID": "buzz", "AZURE_FEDERATED_TOKEN_FILE": "baz"},
			fileContent:     "{\"tenantId\":\"foo\",\"aadClientId\":\"fizz\"}",
			expectedContent: "{\"aadClientId\":\"buzz\",\"aadFederatedTokenFile\":\"baz\",\"tenantId\":\"bar\",\"useFederatedWorkloadIdentityExtension\":true}",
		},
		{
			name:           "should fail, client secret is present while federated token file is present",
			args:           []string{"--cloud-config-file-path", inputFile.Name(), "--output-file-path", outputFile.Name(), "--disable-identity-extension-auth", "--enable-azure-workload-identity=true"},
			envVars:        map[string]string{"AZURE_TENANT_ID": "baz", "AZURE_CLIENT_ID": "foo", "AZURE_CLIENT_SECRET": "bar", "AZURE_FEDERATED_TOKEN_FILE": "baz"},
			expectedErrMsg: "AZURE_CLIENT_SECRET env variable is set while workload identity is enabled using AZURE_FEDERATED_TOKEN_FILE env variable, this should never happen.\nPlease consider reporting a bug: https://issues.redhat.com",
		},
		{
			name:           "should fail, tenant id missing while federated token file is present",
			args:           []string{"--cloud-config-file-path", inputFile.Name(), "--output-file-path", outputFile.Name(), "--disable-identity-extension-auth", "--enable-azure-workload-identity=true"},
			envVars:        map[string]string{"AZURE_CLIENT_ID": "buzz", "AZURE_FEDERATED_TOKEN_FILE": "baz"},
			expectedErrMsg: "AZURE_TENANT_ID env variable should be set up while workload identity is enabled using AZURE_FEDERATED_TOKEN_FILE env variable, this should never happen.\nPlease consider reporting a bug: https://issues.redhat.com",
		},
		{
			name:           "should fail, workload identity can't be enabled because federated token missing, expect secret provided",
			args:           []string{"--cloud-config-file-path", inputFile.Name(), "--output-file-path", outputFile.Name(), "--disable-identity-extension-auth", "--enable-azure-workload-identity=true"},
			envVars:        map[string]string{"AZURE_TENANT_ID": "bar", "AZURE_CLIENT_ID": "buzz"},
			expectedErrMsg: "AZURE_CLIENT_SECRET env variable should be set up",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {

			for envVarName, envVarValue := range tc.envVars {
				err := os.Setenv(envVarName, envVarValue)
				require.NoError(t, err, "Can not setup environment variable %s: %v", envVarName, err)
			}
			defer cleanupEnv(tc.envVars)

			if tc.fileContent != "" {
				err = os.WriteFile(inputFile.Name(), []byte(tc.fileContent), 0644)
				require.NoError(t, err)
				defer cleanupInputFile(inputFile.Name())
			}
			defer cleanupOpts()

			_, mergeCloudConfError := executeCommand(injectorCmd, tc.args...)

			if tc.expectedErrMsg != "" {
				require.NotNil(t, mergeCloudConfError, "Error was expected but not returned by `mergeCloudConfig` function")
				assert.Equal(t, tc.expectedErrMsg, mergeCloudConfError.Error())
			}

			if tc.expectedContent != "" {
				fileContent, err := os.ReadFile(outputFile.Name())
				require.NoError(t, err, "Cannot read output file")
				stringFileContent := string(fileContent)
				assert.Equal(t, tc.expectedContent, stringFileContent)
			}
		})
	}
}
