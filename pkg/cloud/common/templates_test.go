package common

import (
	"embed"
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
)

var (
	//go:embed _testdata/*
	testDataRootFs embed.FS
)

func TestReadTemplateSources(t *testing.T) {
	tc := []struct {
		name        string
		sources     []TemplateSource
		expectedErr string
	}{
		{
			name: "Correct template sources",
			sources: []TemplateSource{
				{ReferenceObject: &appsv1.Deployment{}, EmbedFsPath: "_testdata/assets/deployment.yaml"},
				{ReferenceObject: &appsv1.Deployment{}, EmbedFsPath: "_testdata/foo"},
			},
		}, {
			name: "Non existing template source",
			sources: []TemplateSource{
				{ReferenceObject: &appsv1.Deployment{}, EmbedFsPath: "_testdata/assets/deployment.yaml"},
				{ReferenceObject: &appsv1.Deployment{}, EmbedFsPath: "kekeke"},
			},
			expectedErr: "template: pattern matches no files: `kekeke`",
		},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			objectTemplates, err := ReadTemplates(testDataRootFs, tc.sources)
			if tc.expectedErr != "" {
				assert.NotNil(t, err)
				assert.Equal(t, tc.expectedErr, err.Error())
				assert.Zero(t, len(objectTemplates))
			} else {
				assert.NoError(t, err)
				assert.Equal(t, len(objectTemplates), len(tc.sources))
			}
		})
	}
}

func TestTemplateRendering(t *testing.T) {
	tc := []struct {
		name           string
		templateSource TemplateSource
		templateValues TemplateValues
		expectedErr    string
	}{
		{
			name:           "template renders successfully",
			templateSource: TemplateSource{ReferenceObject: &appsv1.Deployment{}, EmbedFsPath: "_testdata/assets/deployment.yaml"},
			templateValues: TemplateValues{"name": "foo", "someLabel": "bar", "images": map[string]string{"Foo": "baz"}},
		}, {
			name:           "render fails if some template values missing",
			templateSource: TemplateSource{ReferenceObject: &appsv1.Deployment{}, EmbedFsPath: "_testdata/assets/deployment.yaml"},
			templateValues: TemplateValues{"name": "foo", "images": map[string]string{"Foo": "baz"}},
			expectedErr:    "can not render template: template: deployment.yaml:10:18: executing \"deployment.yaml\" at <.someLabel>: map has no entry for key \"someLabel\"",
		}, {
			name:           "render fails if manifest can not be unmarshalled to reference object",
			templateSource: TemplateSource{ReferenceObject: &v1.ConfigMap{}, EmbedFsPath: "_testdata/assets/deployment.yaml"},
			templateValues: TemplateValues{"someLabel": "bar", "name": "foo", "images": map[string]string{"Foo": "baz"}},
			expectedErr:    "error unmarshaling JSON: while decoding JSON: json: unknown field \"spec\"",
		}, {
			name:           "render fails if manifest is not a valid yaml",
			templateSource: TemplateSource{ReferenceObject: &v1.ConfigMap{}, EmbedFsPath: "_testdata/foo"},
			templateValues: TemplateValues{},
			expectedErr:    "error unmarshaling JSON: while decoding JSON: json: cannot unmarshal string into Go value of type v1.ConfigMap",
		},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			objectTemplates, err := ReadTemplates(testDataRootFs, []TemplateSource{tc.templateSource})
			assert.NoError(t, err)
			_, err = RenderTemplates(objectTemplates, tc.templateValues)
			if tc.expectedErr != "" {
				assert.NotNil(t, err)
				assert.Equal(t, tc.expectedErr, err.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
