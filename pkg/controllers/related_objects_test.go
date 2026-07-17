package controllers

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	"sigs.k8s.io/yaml"
)

var _ = Describe("relatedObjects", func() {
	It("static core should stay in sync with the ClusterOperator manifest", func() {
		manifestPath := filepath.Join("..", "..", "manifests", "0000_26_cloud-controller-manager-operator_60_clusteroperator.yaml")
		data, err := os.ReadFile(manifestPath)
		Expect(err).ToNot(HaveOccurred(), "should be able to read ClusterOperator manifest")

		co := &configv1.ClusterOperator{}
		Expect(yaml.Unmarshal(data, co)).To(Succeed(), "should be able to unmarshal ClusterOperator manifest")

		statusClient := &ClusterOperatorStatusClient{
			ManagedNamespace: DefaultManagedNamespace,
		}
		Expect(co.Status.RelatedObjects).To(Equal(statusClient.relatedObjectsStatic()), "Go relatedObjectsStatic() must match the static ClusterOperator manifest")
	})
})
