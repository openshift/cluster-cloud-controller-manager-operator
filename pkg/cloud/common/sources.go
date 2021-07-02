package common

import (
	"embed"

	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// ObjectSource is a holder for the path of the object to read from
// and typed Object, where the resource content would be unserialized
type ObjectSource struct {
	Object client.Object
	Path   string
}

// ReadResources is responsible for reading resources from embed path in the binary.
// This method is for internal use only, and a <provider> should use this method
// only in init() and later provide a copy of acquired objects. See /pkg/cloud for
// examples
func ReadResources(f embed.FS, sources []ObjectSource) ([]client.Object, error) {
	ret := []client.Object{}
	for _, source := range sources {
		data, err := f.ReadFile(source.Path)
		if err != nil {
			klog.Errorf("Cannot read embedded resource %v: %v", source.Path, err)
			return nil, err
		}

		object := source.Object.DeepCopyObject().(client.Object)
		if err := yaml.UnmarshalStrict(data, object); err != nil {
			klog.Errorf("Cannot decode data from embedded resource %v: %v", source.Path, err)
			return nil, err
		}

		ret = append(ret, object)
	}

	return ret, nil
}
