package restmapper

import (
	"testing"

	gmg "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func setupEnvtest(t *testing.T) (*rest.Config, func(t *testing.T)) {
	t.Log("Setup envtest")
	g := gmg.NewWithT(t)
	testEnv := &envtest.Environment{}
	cfg, err := testEnv.Start()
	g.Expect(err).NotTo(gmg.HaveOccurred())
	g.Expect(cfg).NotTo(gmg.BeNil())

	teardownFunc := func(t *testing.T) {
		t.Log("Stop envtest")
		g.Expect(testEnv.Stop()).To(gmg.Succeed())
	}
	return cfg, teardownFunc
}

func TestPartialRestMapperProvider(t *testing.T) {
	restCfg, tearDownFn := setupEnvtest(t)
	defer tearDownFn(t)

	t.Run("getFilteredAPIGroupResources should return APIGroupResources based on passed predicates", func(t *testing.T) {
		g := gmg.NewWithT(t)

		discoveryClient, err := discovery.NewDiscoveryClientForConfig(restCfg)
		g.Expect(err).NotTo(gmg.HaveOccurred())

		// Get GroupResources for all groups
		apiGroupResources, err := getFilteredAPIGroupResources(discoveryClient, AllGroups)
		g.Expect(err).NotTo(gmg.HaveOccurred())
		g.Expect(len(apiGroupResources)).Should(gmg.BeNumerically(">", 0))
		// One of a built-in k8s api groups
		g.Expect(apiGroupResources).Should(gmg.ContainElement(gmg.HaveField("Group.Name", "authorization.k8s.io")))

		// Get GroupResources for kubernetes core and kubernetes apps groups
		appsAndCoreGroupsPredicate := Or(KubernetesCoreGroup, KubernetesAppsGroup)
		filteredApiGroupResources, err := getFilteredAPIGroupResources(discoveryClient, appsAndCoreGroupsPredicate)
		g.Expect(err).NotTo(gmg.HaveOccurred())
		g.Expect(filteredApiGroupResources).Should(gmg.HaveLen(2))
		group0, group1 := filteredApiGroupResources[0], filteredApiGroupResources[1]
		g.Expect(group1).ShouldNot(gmg.BeEquivalentTo(group0))
		g.Expect(filteredApiGroupResources).Should(gmg.ContainElement(gmg.HaveField("Group.Name", "")))
		g.Expect(filteredApiGroupResources).Should(gmg.ContainElement(gmg.HaveField("Group.Name", "apps")))
		g.Expect(filteredApiGroupResources).ShouldNot(gmg.ContainElement(gmg.HaveField("Group.Name", "authorization.k8s.io")))
	})

	t.Run("NewPartialRestMapperProvider should return different RESTMapperProviders based on passed predicates", func(t *testing.T) {
		g := gmg.NewWithT(t)

		// Create two different REST mappers with different passed group filter predicates
		allGroupsRestMapperProvider := NewPartialRestMapperProvider(AllGroups)
		allGroupsRestMapper, err := allGroupsRestMapperProvider(restCfg)
		g.Expect(err).To(gmg.Succeed())

		filteredGroupsRestMapperProvider := NewPartialRestMapperProvider(KubernetesAppsGroup)
		filteredGroupsMapper, err := filteredGroupsRestMapperProvider(restCfg)
		g.Expect(err).To(gmg.Succeed())

		// mapping for event expected to be found in allGroupsMapper
		_, err = allGroupsRestMapper.RESTMapping(schema.GroupKind{Group: "events.k8s.io", Kind: "event"})
		g.Expect(err).To(gmg.Succeed())
		_, err = allGroupsRestMapper.RESTMapping(schema.GroupKind{Group: "apps", Kind: "deployment"})
		g.Expect(err).To(gmg.Succeed())

		// mapping for the event expected not to be found in filteredGroupsMapper,
		// since the predicate did not pass the events.k8s.io api group
		_, err = filteredGroupsMapper.RESTMapping(schema.GroupKind{Group: "events.k8s.io", Kind: "event"})
		g.Expect(err).To(gmg.HaveOccurred())
		_, err = filteredGroupsMapper.RESTMapping(schema.GroupKind{Group: "apps", Kind: "deployment"})
		g.Expect(err).To(gmg.Succeed())
	})
}
