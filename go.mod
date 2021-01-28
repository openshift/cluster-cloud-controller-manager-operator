module github.com/openshift/cluster-cloud-controller-manager-operator

go 1.15

require (
	github.com/onsi/ginkgo v1.14.1
	github.com/onsi/gomega v1.10.2
	github.com/openshift/api v0.0.0-20201216151826-78a19e96f9eb
	github.com/openshift/library-go v0.0.0-20210106214821-c4d0b9c8d55f
	k8s.io/api v0.20.0
	k8s.io/apimachinery v0.21.0-alpha.0.0.20210106165743-6c16abd71758
	k8s.io/client-go v0.20.0
	k8s.io/klog v1.0.0
	k8s.io/klog/v2 v2.4.0
	sigs.k8s.io/controller-runtime v0.7.0
)
