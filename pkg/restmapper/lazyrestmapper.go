package restmapper

import (
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

// LazyRESTMapper is a RESTMapper that will lazily query the provided
// client for discovery information to do REST mappings.
type LazyRESTMapper struct {
	mapper      meta.RESTMapper
	client      *discovery.DiscoveryClient
	knownGroups map[string]*restmapper.APIGroupResources
	apiGroups   *metav1.APIGroupList

	// mutex to provide thread-safe mapper reloading
	mu sync.Mutex
}

// NewLazyRESTMapper initializes a LazyRESTMapper.
func NewLazyRESTMapper(c *rest.Config) (meta.RESTMapper, error) {
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(c)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery client: %w", err)
	}

	return NewLazyRESTMapperWithClient(discoveryClient)
}

// NewLazyRESTMapperWithClient initializes a LazyRESTMapper with a custom discovery client.
func NewLazyRESTMapperWithClient(discoveryClient *discovery.DiscoveryClient) (meta.RESTMapper, error) {
	return &LazyRESTMapper{
		mapper:      restmapper.NewDiscoveryRESTMapper([]*restmapper.APIGroupResources{}),
		client:      discoveryClient,
		knownGroups: map[string]*restmapper.APIGroupResources{},
	}, nil
}

// KindFor implements Mapper.KindFor.
func (m *LazyRESTMapper) KindFor(resource schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	res, err := m.mapper.KindFor(resource)
	if meta.IsNoMatchError(err) {
		if err = m.addKnownGroupAndReload(resource.Group, resource.Version); err != nil {
			return res, err
		}

		res, err = m.mapper.KindFor(resource)
	}

	return res, err
}

// KindsFor implements Mapper.KindsFor.
func (m *LazyRESTMapper) KindsFor(resource schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	res, err := m.mapper.KindsFor(resource)
	if meta.IsNoMatchError(err) {
		if err = m.addKnownGroupAndReload(resource.Group, resource.Version); err != nil {
			return res, err
		}

		res, err = m.mapper.KindsFor(resource)
	}

	return res, err
}

// ResourceFor implements Mapper.ResourceFor.
func (m *LazyRESTMapper) ResourceFor(input schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	res, err := m.mapper.ResourceFor(input)
	if meta.IsNoMatchError(err) {
		if err = m.addKnownGroupAndReload(input.Group, input.Version); err != nil {
			return res, err
		}

		res, err = m.mapper.ResourceFor(input)
	}

	return res, err
}

// ResourcesFor implements Mapper.ResourcesFor.
func (m *LazyRESTMapper) ResourcesFor(input schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	res, err := m.mapper.ResourcesFor(input)
	if meta.IsNoMatchError(err) {
		if err = m.addKnownGroupAndReload(input.Group, input.Version); err != nil {
			return res, err
		}

		res, err = m.mapper.ResourcesFor(input)
	}

	return res, err
}

// RESTMapping implements Mapper.RESTMapping.
func (m *LazyRESTMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	res, err := m.mapper.RESTMapping(gk, versions...)
	if meta.IsNoMatchError(err) {
		if err = m.addKnownGroupAndReload(gk.Group, versions...); err != nil {
			return res, err
		}

		res, err = m.mapper.RESTMapping(gk, versions...)
	}

	return res, err
}

// RESTMappings implements Mapper.RESTMappings.
func (m *LazyRESTMapper) RESTMappings(gk schema.GroupKind, versions ...string) ([]*meta.RESTMapping, error) {
	res, err := m.mapper.RESTMappings(gk, versions...)
	if meta.IsNoMatchError(err) {
		if err = m.addKnownGroupAndReload(gk.Group, versions...); err != nil {
			return res, err
		}

		res, err = m.mapper.RESTMappings(gk, versions...)
	}

	return res, err
}

// ResourceSingularizer implements Mapper.ResourceSingularizer.
func (m *LazyRESTMapper) ResourceSingularizer(resource string) (string, error) {
	return m.mapper.ResourceSingularizer(resource)
}

// addKnownGroupAndReload reloads the mapper with updated information about missing API group.
// versions can be specified for partial updates, for instance for v1beta1 version only.
func (m *LazyRESTMapper) addKnownGroupAndReload(groupName string, versions ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// First, find information about requested group and its versions. Fail immediately if there is no such group.
	apiGroup, err := m.findAPIGroupByName(groupName)
	if err != nil {
		return err
	}

	// If no specific versions are set by user, we will scan all available ones for the API group.
	if len(versions) == 0 {
		for _, version := range apiGroup.Versions {
			versions = append(versions, version.Version)
		}
	}

	// Second, get resources. The number of API calls is equal to the number of versions: /apis/<group>/<version>.
	groupVersionResources, err := m.fetchGroupVersionResources(apiGroup.Name, versions...)
	if err != nil {
		return fmt.Errorf("failed to get API group resources: %w", err)
	}

	groupResources := &restmapper.APIGroupResources{
		Group:              apiGroup,
		VersionedResources: make(map[string][]metav1.APIResource),
	}
	for version, resources := range groupVersionResources {
		groupResources.VersionedResources[version.Version] = resources.APIResources
	}

	// Add new known API group or just append the resources to the existing group.
	if _, ok := m.knownGroups[groupName]; !ok {
		m.knownGroups[groupName] = groupResources
	} else {
		for version, resources := range groupResources.VersionedResources {
			m.knownGroups[groupName].VersionedResources[version] = resources
		}
	}

	// Finally, update the group with received information and regenerate the mapper.
	updatedGroupResources := make([]*restmapper.APIGroupResources, 0, len(m.knownGroups))
	for _, v := range m.knownGroups {
		updatedGroupResources = append(updatedGroupResources, v)
	}

	m.mapper = restmapper.NewDiscoveryRESTMapper(updatedGroupResources)

	return nil
}

// findAPIGroupByName returns API group by its name.
func (m *LazyRESTMapper) findAPIGroupByName(groupName string) (metav1.APIGroup, error) {
	// Ensure that required info about existing API groups is received and stored in the mapper.
	// It will make 2 API calls to /api and /apis, but only once.
	if m.apiGroups == nil {
		apiGroups, err := m.client.ServerGroups()
		if err != nil {
			return metav1.APIGroup{}, fmt.Errorf("failed to get server groups: %w", err)
		}
		if len(apiGroups.Groups) == 0 {
			return metav1.APIGroup{}, fmt.Errorf("received an empty API groups list")
		}

		m.apiGroups = apiGroups
	}

	for i := range m.apiGroups.Groups {
		if groupName == (&m.apiGroups.Groups[i]).Name {
			return m.apiGroups.Groups[i], nil
		}
	}

	return metav1.APIGroup{}, fmt.Errorf("failed to find API group %s", groupName)
}

// fetchGroupVersionResources fetchs the resources for the specified group and its versions.
func (m *LazyRESTMapper) fetchGroupVersionResources(groupName string, versions ...string) (map[schema.GroupVersion]*metav1.APIResourceList, error) {
	groupVersionResources := make(map[schema.GroupVersion]*metav1.APIResourceList)
	failedGroups := make(map[schema.GroupVersion]error)

	for _, version := range versions {
		groupVersion := schema.GroupVersion{Group: groupName, Version: version}

		apiResourceList, err := m.client.ServerResourcesForGroupVersion(groupVersion.String())
		if err != nil {
			failedGroups[groupVersion] = err
		}
		if apiResourceList != nil {
			// even in case of error, some fallback might have been returned
			groupVersionResources[groupVersion] = apiResourceList
		}
	}

	if len(failedGroups) > 0 {
		return nil, &discovery.ErrGroupDiscoveryFailed{Groups: failedGroups}
	}

	return groupVersionResources, nil
}
