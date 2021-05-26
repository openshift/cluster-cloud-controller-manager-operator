package common

import (
	"embed"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
)

//go:embed _testdata/*
var badPath embed.FS

//go:embed _testdata/assets/*
var assetPath embed.FS

func TestReadResources(t *testing.T) {

	type resourcemeta struct {
		kind      string
		name      string
		namespace string
	}

	tc := []struct {
		name          string
		fs            embed.FS
		sources       []ObjectSource
		expected      []resourcemeta
		expectedError string
	}{{
		name: "Reading corect sources cause no error",
		fs:   assetPath,
		sources: []ObjectSource{
			{Object: &appsv1.Deployment{}, Path: "_testdata/assets/deployment.yaml"},
		},
		expected: []resourcemeta{
			{"Deployment", "sample", "sample"},
		},
	}, {
		name: "Error opening a non-existent source",
		fs:   badPath,
		sources: []ObjectSource{
			{Object: &appsv1.Deployment{}, Path: "wrong-path"},
		},
		expectedError: "open wrong-path: file does not exist",
	}, {
		name: "Error during unmarshaling into wrong resource type",
		fs:   badPath,
		sources: []ObjectSource{
			{Object: &appsv1.Deployment{}, Path: "_testdata/foo"},
		},
		expectedError: "error unmarshaling JSON: while decoding JSON: json: cannot unmarshal string into Go value of type v1.Deployment",
	}, {
		name: "Incorrect resource type should not be unmarshalled",
		fs:   assetPath,
		sources: []ObjectSource{
			{Object: &appsv1.DaemonSet{}, Path: "_testdata/assets/deployment.yaml"},
		},
		expectedError: "error unmarshaling JSON: while decoding JSON: json: unknown field \"replicas\"",
	}, {
		name: "Incorrect resource type should not be unmarshalled",
		fs:   assetPath,
		sources: []ObjectSource{
			{Object: &v1.PersistentVolume{}, Path: "_testdata/assets/deployment.yaml"},
		},
		expectedError: "error unmarshaling JSON: while decoding JSON: json: unknown field \"replicas\"",
	}}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			initialSources := tc.sources
			resources, err := ReadResources(tc.fs, tc.sources)
			if tc.expectedError != "" {
				assert.EqualError(t, err, tc.expectedError)
			} else {
				assert.NoError(t, err)
			}

			// Check that the returned resources contain a named set of objects
			assert.Equal(t, len(tc.expected), len(resources))

			for i := 0; i < len(resources); i++ {
				assert.Contains(t, tc.expected, resourcemeta{
					resources[i].GetObjectKind().GroupVersionKind().Kind,
					resources[i].GetName(),
					resources[i].GetNamespace()})
			}

			// No modification in place happened
			assert.EqualValues(t, initialSources, tc.sources)
		})
	}
}
