package main

import (
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"io/ioutil"
	"os"
	"testing"
)

func Test_mergeCloudConfig(t *testing.T) {

	tmpDir, err := ioutil.TempDir("", "cccmo-azure-creds-injector")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpDir)

	inputFile, err := ioutil.TempFile(tmpDir, "dummy-config")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(inputFile.Name())

	outputFile, err := ioutil.TempFile(tmpDir, "dummy-config-merged")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(outputFile.Name())

	cleanupEnv := func(envVars map[string]string) {
		for envVarName := range envVars {
			if err := os.Unsetenv(envVarName); err != nil {
				t.Fatalf("Can not cleanup environment variabeles: %v", err)
			}
		}
	}

	cleanupInputFile := func(path string) {
		if err := ioutil.WriteFile(inputFile.Name(), []byte(""), 0644); err != nil {
			t.Fatalf("Can not cleanup input file: %v", err)
		}
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
			args:           []string{"foo"},
			expectedErrMsg: "stat foo: no such file or directory",
		},
		{
			name:           "AZURE_CLIENT_ID not set",
			args:           []string{inputFile.Name(), outputFile.Name()},
			expectedErrMsg: "AZURE_CLIENT_ID env variable should be set up",
		},
		{
			name:           "AZURE_CLIENT_SECRET not set",
			args:           []string{inputFile.Name(), outputFile.Name()},
			envVars:        map[string]string{"AZURE_CLIENT_ID": "foo"},
			expectedErrMsg: "AZURE_CLIENT_SECRET env variable should be set up",
		},
		{
			name:           "input file content is not a valid json",
			args:           []string{inputFile.Name(), outputFile.Name()},
			envVars:        map[string]string{"AZURE_CLIENT_ID": "foo", "AZURE_CLIENT_SECRET": "bar"},
			fileContent:    "{*&(&#@!}",
			expectedErrMsg: "invalid character '*' looking for beginning of object key string",
		},
		{
			name:           "input file content is valid json, but format is unexpected",
			args:           []string{inputFile.Name(), outputFile.Name()},
			envVars:        map[string]string{"AZURE_CLIENT_ID": "foo", "AZURE_CLIENT_SECRET": "bar"},
			fileContent:    "[1]",
			expectedErrMsg: "json: cannot unmarshal array into Go value of type map[string]interface {}",
		},
		{
			name:            "all ok, file is empty json object",
			args:            []string{inputFile.Name(), outputFile.Name()},
			envVars:         map[string]string{"AZURE_CLIENT_ID": "foo", "AZURE_CLIENT_SECRET": "bar"},
			fileContent:     "{}",
			expectedContent: "{\"aadClientId\":\"foo\",\"aadClientSecret\":\"bar\"}",
		},
		{
			name:            "all ok, some content in json",
			args:            []string{inputFile.Name(), outputFile.Name()},
			envVars:         map[string]string{"AZURE_CLIENT_ID": "foo", "AZURE_CLIENT_SECRET": "bar"},
			fileContent:     "{\"bar\": \"baz\"}",
			expectedContent: "{\"aadClientId\":\"foo\",\"aadClientSecret\":\"bar\",\"bar\":\"baz\"}",
		},
		{
			name:            "all ok, client_id and client_secret overrides",
			args:            []string{inputFile.Name(), outputFile.Name()},
			envVars:         map[string]string{"AZURE_CLIENT_ID": "foo", "AZURE_CLIENT_SECRET": "bar"},
			fileContent:     "{\"aadClientSecret\":\"fizz\",\"aadClientId\":\"baz\"}",
			expectedContent: "{\"aadClientId\":\"foo\",\"aadClientSecret\":\"bar\"}",
		},
		{
			name:           "output file write error",
			args:           []string{inputFile.Name(), "/tmp"},
			envVars:        map[string]string{"AZURE_CLIENT_ID": "foo", "AZURE_CLIENT_SECRET": "bar"},
			fileContent:    "{}",
			expectedErrMsg: "open /tmp: is a directory",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {

			for envVarName, envVarValue := range tc.envVars {
				if err := os.Setenv(envVarName, envVarValue); err != nil {
					t.Fatalf("Can not setup environment variable %s: %v", envVarName, err)
				}
			}
			defer cleanupEnv(tc.envVars)

			if tc.fileContent != "" {
				if err := ioutil.WriteFile(inputFile.Name(), []byte(tc.fileContent), 0644); err != nil {
					t.Fatal(err)
				}
				defer cleanupInputFile(inputFile.Name())
			}

			mergeCloudConfError := mergeCloudConfig(&cobra.Command{}, tc.args)

			if tc.expectedErrMsg != "" {

				if mergeCloudConfError == nil {
					t.Fatal("Error was expected but not returned by `mergeCloudConfig` function")
				}
				assert.Equal(t, mergeCloudConfError.Error(), tc.expectedErrMsg)
			}

			if tc.expectedContent != "" {
				fileContent, err := ioutil.ReadFile(outputFile.Name())
				if err != nil {
					t.Fatal("Can not read output file")
				}
				stringFileContent := string(fileContent)
				assert.Equal(t, tc.expectedContent, stringFileContent)
			}
		})
	}
}
