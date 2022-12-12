package restmapper

import (
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

type RESTMapperProvider func(c *rest.Config) (meta.RESTMapper, error)

// NewPartialRestMapperProvider returns configured 'partial' rest mapper provider intended to be used with controller-runtime manager.
// Takes GroupFilterPredicate as an argument for filtering out APIGroups during discovery procedure.
func NewPartialRestMapperProvider(groupFilterPredicate GroupFilterPredicate) RESTMapperProvider {
	partialRESTMapperProvider := func(c *rest.Config) (meta.RESTMapper, error) {
		drm, err := apiutil.NewDynamicRESTMapper(c,
			apiutil.WithLazyDiscovery,
			apiutil.WithCustomMapper(
				func() (meta.RESTMapper, error) {
					dc, err := discovery.NewDiscoveryClientForConfig(c)
					if err != nil {
						return nil, err
					}

					groupResources, err := getFilteredAPIGroupResources(dc, groupFilterPredicate)
					if err != nil {
						return nil, err
					}

					return restmapper.NewDiscoveryRESTMapper(groupResources), nil
				},
			),
		)
		if err != nil {
			return nil, err
		}

		return drm, nil
	}
	return partialRESTMapperProvider
}

// fetchGroupVersionResources uses the discovery client to fetch the resources for the specified groups in parallel.
// Mainly replicates the same named function from the client-go internals aside from the changed `apiGroups` argument type (uses slice instead of APIGroupList).
// ref: https://github.com/kubernetes/kubernetes/blob/a84d877310ba5cf9237c8e8e3218229c202d3a1e/staging/src/k8s.io/client-go/discovery/discovery_client.go#L506
func fetchGroupVersionResources(d discovery.DiscoveryInterface, apiGroups []*metav1.APIGroup) (map[schema.GroupVersion]*metav1.APIResourceList, map[schema.GroupVersion]error) {
	groupVersionResources := make(map[schema.GroupVersion]*metav1.APIResourceList)
	failedGroups := make(map[schema.GroupVersion]error)

	wg := &sync.WaitGroup{}
	resultLock := &sync.Mutex{}
	for _, apiGroup := range apiGroups {
		for _, version := range apiGroup.Versions {
			groupVersion := schema.GroupVersion{Group: apiGroup.Name, Version: version.Version}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer utilruntime.HandleCrash()

				apiResourceList, err := d.ServerResourcesForGroupVersion(groupVersion.String())

				// lock to record results
				resultLock.Lock()
				defer resultLock.Unlock()

				if err != nil {
					// TODO: maybe restrict this to NotFound errors
					failedGroups[groupVersion] = err
				}
				if apiResourceList != nil {
					// even in case of error, some fallback might have been returned
					groupVersionResources[groupVersion] = apiResourceList
				}
			}()
		}
	}
	wg.Wait()

	return groupVersionResources, failedGroups
}

// filteredServerGroupsAndResources returns the supported resources for groups filtered by passed predicate and versions.
// Mainly replicate ServerGroupsAndResources function from the client-go. The difference is that this function takes
// a function of the GroupFilterPredicate type as an argument for filtering out unwanted groups.
// ref: https://github.com/kubernetes/kubernetes/blob/a84d877310ba5cf9237c8e8e3218229c202d3a1e/staging/src/k8s.io/client-go/discovery/discovery_client.go#L383
func filteredServerGroupsAndResources(d discovery.DiscoveryInterface, groupFilterPredicate GroupFilterPredicate) ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	sgs, err := d.ServerGroups()
	if sgs == nil {
		return nil, nil, err
	}
	resultGroups := []*metav1.APIGroup{}
	for i := range sgs.Groups {
		if groupFilterPredicate(&sgs.Groups[i]) {
			resultGroups = append(resultGroups, &sgs.Groups[i])
		}
	}

	groupVersionResources, failedGroups := fetchGroupVersionResources(d, resultGroups)

	// order results by group/version discovery order
	result := []*metav1.APIResourceList{}
	for _, apiGroup := range resultGroups {
		for _, version := range apiGroup.Versions {
			gv := schema.GroupVersion{Group: apiGroup.Name, Version: version.Version}
			if resources, ok := groupVersionResources[gv]; ok {
				result = append(result, resources)
			}
		}
	}

	if len(failedGroups) == 0 {
		return resultGroups, result, nil
	}

	return resultGroups, result, &discovery.ErrGroupDiscoveryFailed{Groups: failedGroups}
}

// getFilteredAPIGroupResources uses the provided discovery client to gather
// discovery information and populate a slice of APIGroupResources.
func getFilteredAPIGroupResources(cl discovery.DiscoveryInterface, groupFilterPredicate GroupFilterPredicate) ([]*restmapper.APIGroupResources, error) {
	gs, rs, err := filteredServerGroupsAndResources(cl, groupFilterPredicate)
	if rs == nil || gs == nil {
		return nil, err
		// TODO track the errors and update callers to handle partial errors.
	}
	rsm := map[string]*metav1.APIResourceList{}
	for _, r := range rs {
		rsm[r.GroupVersion] = r
	}

	var result []*restmapper.APIGroupResources
	for _, group := range gs {
		groupResources := &restmapper.APIGroupResources{
			Group:              *group,
			VersionedResources: make(map[string][]metav1.APIResource),
		}
		for _, version := range group.Versions {
			resources, ok := rsm[version.GroupVersion]
			if !ok {
				continue
			}
			groupResources.VersionedResources[version.Version] = resources.APIResources
		}
		result = append(result, groupResources)
	}
	return result, nil
}
