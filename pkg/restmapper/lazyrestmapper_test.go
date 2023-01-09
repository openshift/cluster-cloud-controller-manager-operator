package restmapper

import (
	"net/http"
	"testing"

	gmg "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// countingRoundTripper is used to count HTTP requests.
type countingRoundTripper struct {
	roundTripper http.RoundTripper
	requestCount int
}

func newCountingRoundTripper(rt http.RoundTripper) *countingRoundTripper {
	return &countingRoundTripper{roundTripper: rt}
}

// RoundTrip implements http.RoundTripper.RoundTrip that additionally counts requests.
func (crt *countingRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	crt.requestCount++

	return crt.roundTripper.RoundTrip(r)
}

// GetRequestCount returns how many requests have been made.
func (crt *countingRoundTripper) GetRequestCount() int {
	return crt.requestCount
}

// Reset sets the counter to 0.
func (crt *countingRoundTripper) Reset() {
	crt.requestCount = 0
}

func setupEnvtest(t *testing.T) (*rest.Config, func(t *testing.T)) {
	t.Log("Setup envtest")

	g := gmg.NewWithT(t)
	testEnv := &envtest.Environment{
		CRDDirectoryPaths: []string{"testdata"},
	}

	cfg, err := testEnv.Start()
	g.Expect(err).NotTo(gmg.HaveOccurred())
	g.Expect(cfg).NotTo(gmg.BeNil())

	teardownFunc := func(t *testing.T) {
		t.Log("Stop envtest")
		g.Expect(testEnv.Stop()).To(gmg.Succeed())
	}

	return cfg, teardownFunc
}

func TestLazyRestMapperProvider(t *testing.T) {
	restCfg, tearDownFn := setupEnvtest(t)
	defer tearDownFn(t)

	t.Run("LazyRESTMapper should fetch data based on the request", func(t *testing.T) {
		g := gmg.NewWithT(t)

		// To initialize mapper does 2 requests:
		// GET https://host/api
		// GET https://host/apis
		// Then, for each new group it performs just one request to the API server:
		// GET https://host/apis/<group>/<version>

		httpClient, err := rest.HTTPClientFor(restCfg)
		g.Expect(err).NotTo(gmg.HaveOccurred())

		crt := newCountingRoundTripper(httpClient.Transport)
		httpClient.Transport = crt

		discoveryClient, err := discovery.NewDiscoveryClientForConfigAndClient(restCfg, httpClient)
		g.Expect(err).NotTo(gmg.HaveOccurred())

		lazyRestMapper, err := NewLazyRESTMapperWithClient(discoveryClient)
		g.Expect(err).NotTo(gmg.HaveOccurred())

		// There are no requests before any call
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(0))

		mapping, err := lazyRestMapper.RESTMapping(schema.GroupKind{Group: "apps", Kind: "deployment"})
		g.Expect(err).NotTo(gmg.HaveOccurred())
		g.Expect(mapping.GroupVersionKind.Kind).To(gmg.Equal("deployment"))
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(3))

		mappings, err := lazyRestMapper.RESTMappings(schema.GroupKind{Group: "", Kind: "pod"})
		g.Expect(err).NotTo(gmg.HaveOccurred())
		g.Expect(len(mappings)).To(gmg.Equal(1))
		g.Expect(mappings[0].GroupVersionKind.Kind).To(gmg.Equal("pod"))
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(4))

		kind, err := lazyRestMapper.KindFor(schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"})
		g.Expect(err).NotTo(gmg.HaveOccurred())
		g.Expect(kind.Kind).To(gmg.Equal("Ingress"))
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(5))

		kinds, err := lazyRestMapper.KindsFor(schema.GroupVersionResource{Group: "authentication.k8s.io", Version: "v1", Resource: "tokenreviews"})
		g.Expect(err).NotTo(gmg.HaveOccurred())
		g.Expect(len(kinds)).To(gmg.Equal(1))
		g.Expect(kinds[0].Kind).To(gmg.Equal("TokenReview"))
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(6))

		resource, err := lazyRestMapper.ResourceFor(schema.GroupVersionResource{Group: "scheduling.k8s.io", Version: "v1", Resource: "priorityclasses"})
		g.Expect(err).NotTo(gmg.HaveOccurred())
		g.Expect(resource.Resource).To(gmg.Equal("priorityclasses"))
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(7))

		resources, err := lazyRestMapper.ResourcesFor(schema.GroupVersionResource{Group: "policy", Version: "v1", Resource: "poddisruptionbudgets"})
		g.Expect(err).NotTo(gmg.HaveOccurred())
		g.Expect(len(resources)).To(gmg.Equal(1))
		g.Expect(resources[0].Resource).To(gmg.Equal("poddisruptionbudgets"))
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(8))
	})

	t.Run("LazyRESTMapper should cache fetched data and doesn't perform any more requests", func(t *testing.T) {
		g := gmg.NewWithT(t)

		httpClient, err := rest.HTTPClientFor(restCfg)
		g.Expect(err).NotTo(gmg.HaveOccurred())

		crt := newCountingRoundTripper(httpClient.Transport)
		httpClient.Transport = crt

		discoveryClient, err := discovery.NewDiscoveryClientForConfigAndClient(restCfg, httpClient)
		g.Expect(err).NotTo(gmg.HaveOccurred())

		lazyRestMapper, err := NewLazyRESTMapperWithClient(discoveryClient)
		g.Expect(err).NotTo(gmg.HaveOccurred())

		g.Expect(crt.GetRequestCount()).To(gmg.Equal(0))

		mapping, err := lazyRestMapper.RESTMapping(schema.GroupKind{Group: "apps", Kind: "deployment"})
		g.Expect(err).NotTo(gmg.HaveOccurred())
		g.Expect(mapping.GroupVersionKind.Kind).To(gmg.Equal("deployment"))
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(3))

		// Data taken from cache - there are no more additional requests.

		mapping, err = lazyRestMapper.RESTMapping(schema.GroupKind{Group: "apps", Kind: "deployment"})
		g.Expect(err).NotTo(gmg.HaveOccurred())
		g.Expect(mapping.GroupVersionKind.Kind).To(gmg.Equal("deployment"))
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(3))

		kind, err := lazyRestMapper.KindFor((schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployment"}))
		g.Expect(err).NotTo(gmg.HaveOccurred())
		g.Expect(kind.Kind).To(gmg.Equal("Deployment"))
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(3))

		resource, err := lazyRestMapper.ResourceFor((schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployment"}))
		g.Expect(err).NotTo(gmg.HaveOccurred())
		g.Expect(resource.Resource).To(gmg.Equal("deployments"))
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(3))
	})

	t.Run("LazyRESTMapper should work correctly with multiple API group versions", func(t *testing.T) {
		g := gmg.NewWithT(t)

		httpClient, err := rest.HTTPClientFor(restCfg)
		g.Expect(err).NotTo(gmg.HaveOccurred())

		crt := newCountingRoundTripper(httpClient.Transport)
		httpClient.Transport = crt

		discoveryClient, err := discovery.NewDiscoveryClientForConfigAndClient(restCfg, httpClient)
		g.Expect(err).NotTo(gmg.HaveOccurred())

		lazyRestMapper, err := NewLazyRESTMapperWithClient(discoveryClient)
		g.Expect(err).NotTo(gmg.HaveOccurred())

		g.Expect(crt.GetRequestCount()).To(gmg.Equal(0))

		// crew.example.com has 2 versions: v1 and v2

		// If no versions were provided by user, we fetch all of them.
		// Here we expect 4 calls.
		// To initialize:
		// 	#1: GET https://host/api
		// 	#2: GET https://host/apis
		// Then, for each version it performs one request to the API server:
		// 	#3: GET https://host/apis/crew.example.com/v1
		//	#4: GET https://host/apis/crew.example.com/v2
		mapping, err := lazyRestMapper.RESTMapping(schema.GroupKind{Group: "crew.example.com", Kind: "driver"})
		g.Expect(err).NotTo(gmg.HaveOccurred())
		g.Expect(mapping.GroupVersionKind.Kind).To(gmg.Equal("driver"))
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(4))

		// Resetting the mapper
		crt.Reset()
		lazyRestMapper, err = NewLazyRESTMapperWithClient(discoveryClient)
		g.Expect(err).NotTo(gmg.HaveOccurred())
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(0))

		// Now we want resources for crew.example.com/v1 version only.
		// Here we expect 3 calls.
		// To initialize:
		// 	#1: GET https://host/api
		// 	#2: GET https://host/apis
		// To get related resources:
		// 	#3: GET https://host/apis/crew.example.com/v1
		mapping, err = lazyRestMapper.RESTMapping(schema.GroupKind{Group: "crew.example.com", Kind: "driver"}, "v1")
		g.Expect(err).NotTo(gmg.HaveOccurred())
		g.Expect(mapping.GroupVersionKind.Kind).To(gmg.Equal("driver"))
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(3))

		// Get additional resources from v2.
		// Since the mapper had been initialized we don't send requests to /api and /apis anymore,
		// but just call /apis/crew.example.com/v2 directly.
		mapping, err = lazyRestMapper.RESTMapping(schema.GroupKind{Group: "crew.example.com", Kind: "driver"}, "v2")
		g.Expect(err).NotTo(gmg.HaveOccurred())
		g.Expect(mapping.GroupVersionKind.Kind).To(gmg.Equal("driver"))
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(4))

		// No new calls will require additional API requests.
		mapping, err = lazyRestMapper.RESTMapping(schema.GroupKind{Group: "crew.example.com", Kind: "driver"}, "v1")
		g.Expect(err).NotTo(gmg.HaveOccurred())
		g.Expect(mapping.GroupVersionKind.Kind).To(gmg.Equal("driver"))
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(4))
	})

	t.Run("LazyRESTMapper should return an error if the group doesn't exist", func(t *testing.T) {
		g := gmg.NewWithT(t)

		// Once mapper is initialized, it doesn't send any additional requests.

		httpClient, err := rest.HTTPClientFor(restCfg)
		g.Expect(err).NotTo(gmg.HaveOccurred())

		crt := newCountingRoundTripper(httpClient.Transport)
		httpClient.Transport = crt

		discoveryClient, err := discovery.NewDiscoveryClientForConfigAndClient(restCfg, httpClient)
		g.Expect(err).NotTo(gmg.HaveOccurred())

		lazyRestMapper, err := NewLazyRESTMapperWithClient(discoveryClient)
		g.Expect(err).NotTo(gmg.HaveOccurred())

		_, err = lazyRestMapper.RESTMapping(schema.GroupKind{Group: "INVALID1"})
		g.Expect(err).To(gmg.HaveOccurred())
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(2))

		_, err = lazyRestMapper.RESTMappings(schema.GroupKind{Group: "INVALID2"})
		g.Expect(err).To(gmg.HaveOccurred())
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(2))

		_, err = lazyRestMapper.KindFor(schema.GroupVersionResource{Group: "INVALID3"})
		g.Expect(err).To(gmg.HaveOccurred())
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(2))

		_, err = lazyRestMapper.KindsFor(schema.GroupVersionResource{Group: "INVALID4"})
		g.Expect(err).To(gmg.HaveOccurred())
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(2))

		_, err = lazyRestMapper.ResourceFor(schema.GroupVersionResource{Group: "INVALID5"})
		g.Expect(err).To(gmg.HaveOccurred())
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(2))

		_, err = lazyRestMapper.ResourcesFor(schema.GroupVersionResource{Group: "INVALID6"})
		g.Expect(err).To(gmg.HaveOccurred())
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(2))
	})

	t.Run("LazyRESTMapper should return an error if the resource doesn't exist", func(t *testing.T) {
		g := gmg.NewWithT(t)

		// After initialization, for each invalid resource mapper performs 1 requests to the API server.

		httpClient, err := rest.HTTPClientFor(restCfg)
		g.Expect(err).NotTo(gmg.HaveOccurred())

		crt := newCountingRoundTripper(httpClient.Transport)
		httpClient.Transport = crt

		discoveryClient, err := discovery.NewDiscoveryClientForConfigAndClient(restCfg, httpClient)
		g.Expect(err).NotTo(gmg.HaveOccurred())

		lazyRestMapper, err := NewLazyRESTMapperWithClient(discoveryClient)
		g.Expect(err).NotTo(gmg.HaveOccurred())

		_, err = lazyRestMapper.RESTMapping(schema.GroupKind{Group: "apps", Kind: "INVALID"})
		g.Expect(err).To(gmg.HaveOccurred())
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(3))

		_, err = lazyRestMapper.RESTMappings(schema.GroupKind{Group: "", Kind: "INVALID"})
		g.Expect(err).To(gmg.HaveOccurred())
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(4))

		_, err = lazyRestMapper.KindFor(schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "INVALID"})
		g.Expect(err).To(gmg.HaveOccurred())
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(5))

		_, err = lazyRestMapper.KindsFor(schema.GroupVersionResource{Group: "authentication.k8s.io", Version: "v1", Resource: "INVALID"})
		g.Expect(err).To(gmg.HaveOccurred())
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(6))

		_, err = lazyRestMapper.ResourceFor(schema.GroupVersionResource{Group: "scheduling.k8s.io", Version: "v1", Resource: "INVALID"})
		g.Expect(err).To(gmg.HaveOccurred())
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(7))

		_, err = lazyRestMapper.ResourcesFor(schema.GroupVersionResource{Group: "policy", Version: "v1", Resource: "INVALID"})
		g.Expect(err).To(gmg.HaveOccurred())
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(8))
	})

	t.Run("LazyRESTMapper should return an error if the version doesn't exist", func(t *testing.T) {
		g := gmg.NewWithT(t)

		// After initialization, for each invalid resource mapper performs 1 requests to the API server.

		httpClient, err := rest.HTTPClientFor(restCfg)
		g.Expect(err).NotTo(gmg.HaveOccurred())

		crt := newCountingRoundTripper(httpClient.Transport)
		httpClient.Transport = crt

		discoveryClient, err := discovery.NewDiscoveryClientForConfigAndClient(restCfg, httpClient)
		g.Expect(err).NotTo(gmg.HaveOccurred())

		lazyRestMapper, err := NewLazyRESTMapperWithClient(discoveryClient)
		g.Expect(err).NotTo(gmg.HaveOccurred())

		_, err = lazyRestMapper.RESTMapping(schema.GroupKind{Group: "apps", Kind: "deployment"}, "INVALID")
		g.Expect(err).To(gmg.HaveOccurred())
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(3))

		_, err = lazyRestMapper.RESTMappings(schema.GroupKind{Group: "", Kind: "pod"}, "INVALID")
		g.Expect(err).To(gmg.HaveOccurred())
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(4))

		_, err = lazyRestMapper.KindFor(schema.GroupVersionResource{Group: "networking.k8s.io", Version: "INVALID", Resource: "ingresses"})
		g.Expect(err).To(gmg.HaveOccurred())
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(5))

		_, err = lazyRestMapper.KindsFor(schema.GroupVersionResource{Group: "authentication.k8s.io", Version: "INVALID", Resource: "tokenreviews"})
		g.Expect(err).To(gmg.HaveOccurred())
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(6))

		_, err = lazyRestMapper.ResourceFor(schema.GroupVersionResource{Group: "scheduling.k8s.io", Version: "INVALID", Resource: "priorityclasses"})
		g.Expect(err).To(gmg.HaveOccurred())
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(7))

		_, err = lazyRestMapper.ResourcesFor(schema.GroupVersionResource{Group: "policy", Version: "INVALID", Resource: "poddisruptionbudgets"})
		g.Expect(err).To(gmg.HaveOccurred())
		g.Expect(crt.GetRequestCount()).To(gmg.Equal(8))
	})
}
