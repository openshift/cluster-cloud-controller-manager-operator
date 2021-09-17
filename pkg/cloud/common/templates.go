package common

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"

	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// TemplateValues represents values map which would be used in ObjectTemplate rendering
type TemplateValues map[string]interface{}

// TemplateSource structure which intended to keep path to template
// and reference kubernetes object for further unmarshalling (such as Deployment, DaemonSet, ConfigMap, etc)
type TemplateSource struct {
	ReferenceObject client.Object
	EmbedFsPath     string
}

// ObjectTemplate extension of TemplateSource,
// contains Template object from 'text/template' for further rendering with TemplateValues
type ObjectTemplate struct {
	TemplateSource
	templateContent *template.Template
}

// Internal method for rendering ObjectTemplate with given TemplateValues map.
func (tmpl *ObjectTemplate) render(templateValues TemplateValues) (client.Object, error) {
	buf := &bytes.Buffer{}
	if err := tmpl.templateContent.Execute(buf, templateValues); err != nil {
		return nil, fmt.Errorf("can not render template: %s", err)
	}
	object := tmpl.ReferenceObject.DeepCopyObject()

	if err := yaml.UnmarshalStrict(buf.Bytes(), object); err != nil {
		klog.Errorf("Cannot decode data from embedded resource %v: %v", tmpl.EmbedFsPath, err)
		return nil, err
	}
	return object.(client.Object), nil
}

// RenderTemplates renders array of templates with given TemplateValues map.
// Returns list of controller-runtime's client.Objects which are ready to further substitution
// or for passing it directly to kubernetes cluster.
func RenderTemplates(templates []ObjectTemplate, values TemplateValues) ([]client.Object, error) {
	objects := make([]client.Object, len(templates))
	for i, tmpl := range templates {
		object, err := tmpl.render(values)
		if err != nil {
			return nil, err
		}

		objects[i] = object
	}
	return objects, nil
}

// ReadTemplates reads templates content from given embed.FS instance by paths passed in each TemplateSource.
// Basically this function transforms TemplateSource to ObjectTemplate by populating each object
// with a bit configured 'text/template' Templates.
func ReadTemplates(f embed.FS, sources []TemplateSource) ([]ObjectTemplate, error) {
	ret := *new([]ObjectTemplate)
	for _, source := range sources {

		tmpl, err := template.ParseFS(f, source.EmbedFsPath)
		if err != nil {
			klog.Errorf("Cannot parse template from embedded resource %v: %v", source.EmbedFsPath, err)
			return nil, err
		}
		tmpl.Option("missingkey=error") // throw error if no key in TemplateValues map found during rendering
		objectTemplate := ObjectTemplate{
			TemplateSource:  source,
			templateContent: tmpl,
		}

		ret = append(ret, objectTemplate)
	}

	return ret, nil
}
