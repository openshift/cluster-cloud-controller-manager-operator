module github.com/openshift/cluster-cloud-controller-manager-operator

go 1.16

require (
	github.com/google/go-cmp v0.5.6 // indirect
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/onsi/ginkgo v1.16.4
	github.com/onsi/gomega v1.13.0
	github.com/openshift/api v0.0.0-20210720160326-96bb0f993a66
	github.com/openshift/library-go v0.0.0-20210708191609-4b9033d00d37
	github.com/stretchr/testify v1.7.0
	golang.org/x/net v0.0.0-20210716203947-853a461950ff // indirect
	k8s.io/api v0.21.3
	k8s.io/apiextensions-apiserver v0.21.1
	k8s.io/apimachinery v0.21.3
	k8s.io/client-go v0.21.1
	k8s.io/klog/v2 v2.10.0
	k8s.io/utils v0.0.0-20210527160623-6fdb442a123b
	sigs.k8s.io/controller-runtime v0.9.0
	sigs.k8s.io/yaml v1.2.0
)

// Workaround to deal with https://github.com/kubernetes/klog/issues/253
// Should be deleted when https://github.com/kubernetes/klog/pull/242 is merged
exclude github.com/go-logr/logr v1.0.0
