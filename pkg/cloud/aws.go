package cloud

import (
	"bytes"
	"embed"

	appsv1 "k8s.io/api/apps/v1"
	k8sYaml "k8s.io/apimachinery/pkg/util/yaml"

	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

//go:embed aws/assets/*
var f embed.FS

func GetAWSResources() []client.Object {
	resources := []struct {
		obj   client.Object
		asset string
	}{
		{&appsv1.Deployment{}, "aws/assets/deployment.yaml"},
	}
	ret := make([]client.Object, 0, len(resources))

	for _, resource := range resources {
		data, err := f.ReadFile(resource.asset)
		if err != nil {
			klog.Errorf("Cannot read embedded resource %v: %v", resource.asset, err)
			return nil
		}

		dec := k8sYaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 1000)
		if err := dec.Decode(resource.obj); err != nil {
			klog.Errorf("Cannot decode data from embedded resource %v: %v", resource.asset, err)
			return nil
		}

		ret = append(ret, resource.obj)
	}

	return ret
}
