package common

import (
	"bytes"
	"embed"

	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ObjectSource struct {
	Object client.Object
	Path   string
}

func ReadResources(f embed.FS, sources []ObjectSource) ([]client.Object, error) {
	ret := []client.Object{}
	for _, source := range sources {
		data, err := f.ReadFile(source.Path)
		if err != nil {
			klog.Errorf("Cannot read embedded resource %v: %v", source.Path, err)
			return nil, err
		}

		object := source.Object.DeepCopyObject().(client.Object)
		dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 1000)
		if err := dec.Decode(object); err != nil {
			klog.Errorf("Cannot decode data from embedded resource %v: %v", source.Path, err)
			return nil, err
		}

		ret = append(ret, object)
	}

	return ret, nil
}
