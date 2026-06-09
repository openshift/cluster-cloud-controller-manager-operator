package controllers

import (
	"context"
	"reflect"

	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// errorInjectingClient wraps a real client and injects errors on Get calls
// for a specific object type. This simulates transient API server failures
// (e.g. network blips) without depending on any internal controller code path.
// The getErr pointer allows tests to toggle faults between reconcile calls.
type errorInjectingClient struct {
	client.Client
	getErr   *error
	failType client.Object
}

func (c *errorInjectingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if c.getErr != nil && *c.getErr != nil && reflect.TypeOf(obj) == reflect.TypeOf(c.failType) {
		return *c.getErr
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func deleteClusterOperator(ctx context.Context, cl client.Client) {
	co := &configv1.ClusterOperator{}
	if err := cl.Get(ctx, client.ObjectKey{Name: clusterOperatorName}, co); err == nil {
		Eventually(func() error {
			err := cl.Delete(ctx, co)
			if err == nil || apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}).Should(Succeed())
	}
	Eventually(func() error {
		return cl.Get(ctx, client.ObjectKey{Name: clusterOperatorName}, co)
	}).Should(MatchError(apierrors.IsNotFound, "IsNotFound"))
}
