package controllers

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

type WatcherOptions struct {
	Cache  cache.Cache
	Scheme *runtime.Scheme
}

type ObjectWatcher interface {
	Watch(ctx context.Context, obj client.Object) error
	EventStream() <-chan event.GenericEvent
}

func NewObjectWatcher(opts WatcherOptions) (ObjectWatcher, error) {
	if opts.Cache == nil {
		return nil, errors.New("Cache is required")
	}

	// Use the default Kubernetes Scheme if unset
	if opts.Scheme == nil {
		opts.Scheme = scheme.Scheme
	}

	return &objectWatcher{
		objectCache:      opts.Cache,
		scheme:           opts.Scheme,
		eventChan:        make(chan event.GenericEvent),
		watchedResources: make(map[string]struct{}),
	}, nil
}

type objectWatcher struct {
	objectCache      cache.Cache
	scheme           *runtime.Scheme
	eventChan        chan event.GenericEvent
	watchedResources map[string]struct{}
}

func (n *objectWatcher) EventStream() <-chan event.GenericEvent {
	return n.eventChan
}

func (n *objectWatcher) Watch(ctx context.Context, obj client.Object) error {
	key, err := n.watchKey(obj)
	if err != nil {
		return err
	}

	if _, ok := n.watchedResources[key]; !ok {
		// watch not set up for this object yet
		return n.watch(ctx, obj)
	}

	return nil
}

func (n *objectWatcher) watch(ctx context.Context, obj client.Object) error {
	informer, err := n.objectCache.GetInformer(ctx, obj)
	if err != nil {
		return nil
	}

	// Get the key before we set up the event to ensure we can mark the key in the watchedResources map
	key, err := n.watchKey(obj)
	if err != nil {
		return err
	}

	// Add an event handler that only allows events through for the correct object name
	// Since the informer is namespace bound, this should limit the events from this event handler to a single resource.
	informer.AddEventHandler(&eventToChannelHandler{
		name:       obj.GetName(),
		eventsChan: n.eventChan,
	})

	n.watchedResources[key] = struct{}{}

	return nil
}

func (n *objectWatcher) watchKey(obj client.Object) (string, error) {
	gvk, err := apiutil.GVKForObject(obj, n.scheme)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s", gvk.GroupKind().String(), obj.GetName()), nil
}

type eventToChannelHandler struct {
	eventsChan chan event.GenericEvent
	name       string
}

func (e *eventToChannelHandler) OnAdd(obj interface{}) {
	e.queueEventForObject(obj)
}

func (e *eventToChannelHandler) OnUpdate(oldobj, obj interface{}) {
	e.queueEventForObject(obj)
}

func (e *eventToChannelHandler) OnDelete(obj interface{}) {
	e.queueEventForObject(obj)
}

// queueEventForObject sends the event onto the channel
func (e *eventToChannelHandler) queueEventForObject(o interface{}) {
	if o == nil {
		// Can't do anything here
		return
	}
	obj, ok := o.(client.Object)
	if !ok {
		return
	}
	if obj.GetName() != e.name {
		// Not the right object, skip
	}

	// Send an event to the events channel
	e.eventsChan <- event.GenericEvent{
		Object: obj,
	}
}
