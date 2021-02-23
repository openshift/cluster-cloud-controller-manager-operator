package openstack

import (
	"bytes"
	"embed"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8sYaml "k8s.io/apimachinery/pkg/util/yaml"

	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

//go:embed assets/*
var f embed.FS

func GetResources() []client.Object {
	resources := []struct {
		obj   client.Object
		asset string
	}{
		{&v1.ConfigMap{}, "assets/config.yaml"},
		{&appsv1.Deployment{}, "assets/deployment.yaml"},
		{&rbacv1.Role{}, "assets/rbac/role.yaml"},
		{&rbacv1.RoleBinding{}, "assets/rbac/rolebinding.yaml"},
	}
	ret := make([]client.Object, 0, len(resources))

	for i := 0; i < len(resources); i++ {
		resource := &resources[i]
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
