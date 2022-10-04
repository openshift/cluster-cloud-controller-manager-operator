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
