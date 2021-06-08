package common

import (
	"bytes"
	"embed"
	"fmt"
	"sigs.k8s.io/yaml"
	"text/template"

	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type TemplateSource struct {
	Object client.Object
	Path   string
}

type ObjectTemplate struct {
	TemplateSource
	Template *template.Template
}

func ReadTemplates(f embed.FS, sources []TemplateSource) ([]ObjectTemplate, error) {
	ret := []ObjectTemplate{}
	for _, source := range sources {

		tmpl, err := template.ParseFS(f, source.Path)
		if err != nil {
			klog.Errorf("Cannot parse template from embedded resource %v: %v", source.Path, err)
			return nil, err
		}
		objectTemplate := ObjectTemplate{
			TemplateSource: source,
			Template:       tmpl,
		}

		ret = append(ret, objectTemplate)
	}

	return ret, nil
}

func (tmpl *ObjectTemplate) render(providerContext ProviderAssets) (client.Object, error) {
	buf := &bytes.Buffer{}
	err := tmpl.Template.Execute(buf, providerContext)
	if err != nil {
		return nil, fmt.Errorf("can not render template: %s", err)
	}
	object := tmpl.TemplateSource.Object.DeepCopyObject()

	err = yaml.UnmarshalStrict(buf.Bytes(), object)
	if err != nil {
		klog.Errorf("Cannot decode data from embedded resource %v: %v", tmpl.TemplateSource.Path, err)
		return nil, err
	}

	object = substituteRenderedObject(object.(client.Object), providerContext)

	return object.(client.Object), nil
}

func substituteRenderedObject(object client.Object, providerContext ProviderAssets) client.Object {
	managedNamespace := providerContext.GetOperatorConfig().ManagedNamespace

	object.SetNamespace(managedNamespace)

	return object
}

func RenderTemplates(templates []ObjectTemplate, providerContext ProviderAssets) ([]client.Object, error) {
	objects := make([]client.Object, len(templates))
	for i, tmpl := range templates {
		object, err := tmpl.render(providerContext)
		if err != nil {
			return nil, err
		}

		objects[i] = object
	}
	return objects, nil
}
