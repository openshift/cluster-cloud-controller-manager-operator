package controllers

import (
	"context"
	"fmt"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"
)

func TestPrintOperandVersions(t *testing.T) {
	expectedOutput := "operator: 1.0, controller-manager: 2.0"
	got := printOperandVersions([]configv1.OperandVersion{
		{
			Name:    "operator",
			Version: "1.0",
		},
		{
			Name:    "controller-manager",
			Version: "2.0",
		},
	})
	assert.Equal(t, got, expectedOutput)
}

func TestOperatorSetStatusProgressing(t *testing.T) {
	type tCase struct {
		currentVersion     []configv1.OperandVersion
		desiredVersion     string
		expectedConditions []configv1.ClusterOperatorStatusCondition
	}
	tCases := []tCase{
		{
			desiredVersion: "1.0",
			expectedConditions: []configv1.ClusterOperatorStatusCondition{
				newClusterOperatorStatusCondition(configv1.OperatorProgressing, configv1.ConditionTrue, ReasonSyncing, ""),
				newClusterOperatorStatusCondition(configv1.OperatorUpgradeable, configv1.ConditionTrue, ReasonAsExpected, ""),
			},
		},
		{
			currentVersion: []configv1.OperandVersion{
				{
					Name:    "operator",
					Version: "1.0",
				},
			},
			desiredVersion: "1.0",
			expectedConditions: []configv1.ClusterOperatorStatusCondition{
				newClusterOperatorStatusCondition(configv1.OperatorProgressing, configv1.ConditionTrue, ReasonSyncing, ""),
				newClusterOperatorStatusCondition(configv1.OperatorUpgradeable, configv1.ConditionTrue, ReasonAsExpected, ""),
			},
		},
		{
			currentVersion: []configv1.OperandVersion{
				{
					Name:    "operator",
					Version: "1.0",
				},
			},
			desiredVersion: "2.0",
			expectedConditions: []configv1.ClusterOperatorStatusCondition{
				newClusterOperatorStatusCondition(configv1.OperatorProgressing, configv1.ConditionTrue, ReasonSyncing, ""),
				newClusterOperatorStatusCondition(configv1.OperatorUpgradeable, configv1.ConditionTrue, ReasonAsExpected, ""),
			},
		},
	}

	for i, tc := range tCases {
		startTime := metav1.NewTime(time.Now().Add(-time.Second))

		optr := CloudOperatorReconciler{
			ClusterOperatorStatusClient: ClusterOperatorStatusClient{
				Clock:          clocktesting.NewFakePassiveClock(time.Now()),
				Recorder:       record.NewFakeRecorder(32),
				ReleaseVersion: tc.desiredVersion,
			},
			Scheme: scheme.Scheme,
		}

		builder := fake.NewClientBuilder().WithStatusSubresource(&configv1.ClusterOperator{})
		if tc.currentVersion != nil {
			operator := &configv1.ClusterOperator{}
			operator.SetName(clusterOperatorName)
			operator.Status.Versions = tc.currentVersion
			builder = builder.WithObjects(operator)
		}
		optr.Client = builder.Build()

		err := optr.setStatusProgressing(context.TODO(), nil)
		assert.NoErrorf(t, err, "Failed to set Progressing status on ClusterOperator")

		gotCO, err := optr.getOrCreateClusterOperator(context.TODO())
		assert.NoErrorf(t, err, "Failed to fetch ClusterOperator")

		var condition configv1.ClusterOperatorStatusCondition
		for _, coCondition := range gotCO.Status.Conditions {
			assert.True(t,
				startTime.Before(&coCondition.LastTransitionTime) || startTime.Equal(&coCondition.LastTransitionTime),
				"test-case %v expected LastTransitionTime for the status condition %v to be after %v", i, coCondition, startTime)
			if coCondition.Type == configv1.OperatorProgressing {
				condition = coCondition
				break
			}
		}

		for _, expectedCondition := range tc.expectedConditions {
			ok := v1helpers.IsStatusConditionPresentAndEqual(
				gotCO.Status.Conditions, expectedCondition.Type, expectedCondition.Status,
			)
			if !ok {
				t.Errorf("wrong status for condition. Expected: %v, got: %v",
					expectedCondition,
					v1helpers.FindStatusCondition(gotCO.Status.Conditions, expectedCondition.Type))
			}
		}

		assert.True(t,
			len(tc.expectedConditions) == len(gotCO.Status.Conditions),
			"test-case %v expected equal number of conditions to %v, got %v", i, len(tc.expectedConditions), len(gotCO.Status.Conditions))

		err = optr.setStatusProgressing(context.TODO(), nil)
		assert.NoErrorf(t, err, "Failed to set Progressing status on ClusterOperator")

		err = optr.Client.Get(context.TODO(), client.ObjectKey{Name: clusterOperatorName}, gotCO)
		assert.NoErrorf(t, err, "Failed to fetch ClusterOperator")
		var conditionAfterAnotherSync configv1.ClusterOperatorStatusCondition
		for _, coCondition := range gotCO.Status.Conditions {
			if coCondition.Type == configv1.OperatorProgressing {
				conditionAfterAnotherSync = coCondition
				break
			}
		}
		assert.True(t, condition.LastTransitionTime.Equal(&conditionAfterAnotherSync.LastTransitionTime), "test-case %v expected LastTransitionTime not to be updated if condition state is same", i)

		for _, expectedCondition := range tc.expectedConditions {
			ok := v1helpers.IsStatusConditionPresentAndEqual(
				gotCO.Status.Conditions, expectedCondition.Type, expectedCondition.Status,
			)
			if !ok {
				t.Errorf("wrong status for condition. Expected: %v, got: %v",
					expectedCondition,
					v1helpers.FindStatusCondition(gotCO.Status.Conditions, expectedCondition.Type))
			}
		}

		assert.True(t,
			len(tc.expectedConditions) == len(gotCO.Status.Conditions),
			"test-case %v expected equal number of conditions to %v, got %v", i, len(tc.expectedConditions), len(gotCO.Status.Conditions))
	}
}

func TestOperatorSetStatusDegraded(t *testing.T) {
	type tCase struct {
		currentVersion     []configv1.OperandVersion
		desiredVersion     string
		expectedConditions []configv1.ClusterOperatorStatusCondition
		passErr            error
		expectErrMessage   string
	}
	tCases := []tCase{
		{
			currentVersion: []configv1.OperandVersion{
				{
					Name:    "operator",
					Version: "1.0",
				},
			},
			desiredVersion: "1.0",
			expectedConditions: []configv1.ClusterOperatorStatusCondition{
				newClusterOperatorStatusCondition(configv1.OperatorDegraded, configv1.ConditionTrue, ReasonSyncFailed, ""),
				newClusterOperatorStatusCondition(configv1.OperatorUpgradeable, configv1.ConditionFalse, ReasonAsExpected, ""),
			},
			passErr:          fmt.Errorf("some failure"),
			expectErrMessage: "Failed to resync for operator: 1.0 because &{%!e(string=some failure)}",
		},
		{
			currentVersion: []configv1.OperandVersion{
				{
					Name:    "operator",
					Version: "1.0",
				},
			},
			desiredVersion: "2.0",
			expectedConditions: []configv1.ClusterOperatorStatusCondition{
				newClusterOperatorStatusCondition(configv1.OperatorDegraded, configv1.ConditionTrue, ReasonSyncFailed, ""),
				newClusterOperatorStatusCondition(configv1.OperatorUpgradeable, configv1.ConditionFalse, ReasonAsExpected, ""),
			},
			passErr:          fmt.Errorf("some failure"),
			expectErrMessage: "Failed when progressing towards operator: 2.0 because &{%!e(string=some failure)}",
		},
	}

	for i, tc := range tCases {
		startTime := metav1.NewTime(time.Now().Add(-time.Second))

		optr := CloudOperatorReconciler{
			ClusterOperatorStatusClient: ClusterOperatorStatusClient{
				Clock:          clocktesting.NewFakePassiveClock(time.Now()),
				Recorder:       record.NewFakeRecorder(32),
				ReleaseVersion: tc.desiredVersion,
			},
			Scheme: scheme.Scheme,
		}

		builder := fake.NewClientBuilder().WithStatusSubresource(&configv1.ClusterOperator{})
		if tc.currentVersion != nil {
			operator := &configv1.ClusterOperator{}
			operator.SetName(clusterOperatorName)
			operator.Status.Versions = tc.currentVersion
			builder = builder.WithObjects(operator)
		}
		optr.Client = builder.Build()

		err := optr.setStatusDegraded(context.TODO(), tc.passErr, nil)
		assert.NoErrorf(t, err, "Failed to set Degraded status on ClusterOperator")

		gotCO, err := optr.getOrCreateClusterOperator(context.TODO())
		assert.NoErrorf(t, err, "Failed to fetch ClusterOperator")

		var condition configv1.ClusterOperatorStatusCondition
		for _, coCondition := range gotCO.Status.Conditions {
			assert.True(t,
				startTime.Before(&coCondition.LastTransitionTime) || startTime.Equal(&coCondition.LastTransitionTime),
				"test-case %v expected LastTransitionTime for the status condition %v to be after %v", i, coCondition, startTime)
			if coCondition.Type == configv1.OperatorDegraded {
				condition = coCondition
				break
			}
		}

		assert.True(t,
			tc.expectErrMessage == condition.Message,
			"test-case %v expected error message to be equal %q, got %q", i, tc.expectErrMessage, condition.Message)

		for _, expectedCondition := range tc.expectedConditions {
			ok := v1helpers.IsStatusConditionPresentAndEqual(
				gotCO.Status.Conditions, expectedCondition.Type, expectedCondition.Status,
			)
			if !ok {
				t.Errorf("wrong status for condition. Expected: %v, got: %v",
					expectedCondition,
					v1helpers.FindStatusCondition(gotCO.Status.Conditions, expectedCondition.Type))
			}
		}

		assert.True(t,
			len(tc.expectedConditions) == len(gotCO.Status.Conditions),
			"test-case %v expected equal number of conditions to %v, got %v", i, len(tc.expectedConditions), len(gotCO.Status.Conditions))

		err = optr.setStatusDegraded(context.TODO(), tc.passErr, nil)
		assert.NoErrorf(t, err, "Failed to set Degraded status on ClusterOperator")

		err = optr.Client.Get(context.TODO(), client.ObjectKey{Name: clusterOperatorName}, gotCO)
		assert.NoErrorf(t, err, "Failed to fetch ClusterOperator")

		var conditionAfterAnotherSync configv1.ClusterOperatorStatusCondition
		for _, coCondition := range gotCO.Status.Conditions {
			if coCondition.Type == configv1.OperatorDegraded {
				conditionAfterAnotherSync = coCondition
				break
			}
		}
		assert.True(t, condition.LastTransitionTime.Equal(&conditionAfterAnotherSync.LastTransitionTime), "test-case %v expected LastTransitionTime not to be updated if condition state is same", i)

		for _, expectedCondition := range tc.expectedConditions {
			ok := v1helpers.IsStatusConditionPresentAndEqual(
				gotCO.Status.Conditions, expectedCondition.Type, expectedCondition.Status,
			)
			if !ok {
				t.Errorf("wrong status for condition. Expected: %v, got: %v",
					expectedCondition,
					v1helpers.FindStatusCondition(gotCO.Status.Conditions, expectedCondition.Type))
			}
		}

		assert.True(t,
			len(tc.expectedConditions) == len(gotCO.Status.Conditions),
			"test-case %v expected equal number of conditions to %v, got %v", i, len(tc.expectedConditions), len(gotCO.Status.Conditions))
	}
}

func TestOperatorSetStatusAvailable(t *testing.T) {
	type tCase struct {
		currentVersion     []configv1.OperandVersion
		desiredVersion     string
		expectedConditions []configv1.ClusterOperatorStatusCondition
		overrides          []configv1.ClusterOperatorStatusCondition
	}
	tCases := []tCase{
		{
			desiredVersion: "1.0",
			expectedConditions: []configv1.ClusterOperatorStatusCondition{
				newClusterOperatorStatusCondition(configv1.OperatorAvailable, configv1.ConditionTrue, ReasonAsExpected, ""),
				newClusterOperatorStatusCondition(configv1.OperatorProgressing, configv1.ConditionFalse, ReasonAsExpected, ""),
				newClusterOperatorStatusCondition(configv1.OperatorUpgradeable, configv1.ConditionTrue, ReasonAsExpected, ""),
				newClusterOperatorStatusCondition(configv1.OperatorDegraded, configv1.ConditionFalse, ReasonAsExpected, ""),
			},
		},
		{
			currentVersion: []configv1.OperandVersion{
				{
					Name:    "operator",
					Version: "1.0",
				},
			},
			desiredVersion: "2.0",
			expectedConditions: []configv1.ClusterOperatorStatusCondition{
				newClusterOperatorStatusCondition(configv1.OperatorAvailable, configv1.ConditionTrue, ReasonAsExpected, ""),
				newClusterOperatorStatusCondition(configv1.OperatorProgressing, configv1.ConditionFalse, ReasonAsExpected, ""),
				newClusterOperatorStatusCondition(configv1.OperatorUpgradeable, configv1.ConditionTrue, ReasonAsExpected, ""),
				newClusterOperatorStatusCondition(configv1.OperatorDegraded, configv1.ConditionFalse, ReasonAsExpected, ""),
			},
		},
		{
			currentVersion: []configv1.OperandVersion{
				{
					Name:    "operator",
					Version: "1.0",
				},
			},
			desiredVersion: "2.0",
			// This test is checking that if an override is passed in, it will be used instead of the default for the function.
			overrides: []configv1.ClusterOperatorStatusCondition{
				newClusterOperatorStatusCondition(configv1.OperatorUpgradeable, configv1.ConditionFalse, ReasonPlatformTechPreview, "This platform is tech preview and so shouldn't be upgradable"),
			},
			expectedConditions: []configv1.ClusterOperatorStatusCondition{
				newClusterOperatorStatusCondition(configv1.OperatorAvailable, configv1.ConditionTrue, ReasonAsExpected, ""),
				newClusterOperatorStatusCondition(configv1.OperatorProgressing, configv1.ConditionFalse, ReasonAsExpected, ""),
				newClusterOperatorStatusCondition(configv1.OperatorUpgradeable, configv1.ConditionFalse, ReasonPlatformTechPreview, "This platform is tech preview and so shouldn't be upgradable"),
				newClusterOperatorStatusCondition(configv1.OperatorDegraded, configv1.ConditionFalse, ReasonAsExpected, ""),
			},
		},
	}

	for i, tc := range tCases {
		startTime := metav1.NewTime(time.Now().Add(-time.Second))

		optr := CloudOperatorReconciler{
			ClusterOperatorStatusClient: ClusterOperatorStatusClient{
				Clock:          clocktesting.NewFakePassiveClock(time.Now()),
				Recorder:       record.NewFakeRecorder(32),
				ReleaseVersion: tc.desiredVersion,
			},
			Scheme: scheme.Scheme,
		}

		builder := fake.NewClientBuilder().WithStatusSubresource(&configv1.ClusterOperator{})
		if tc.currentVersion != nil {
			operator := &configv1.ClusterOperator{}
			operator.SetName(clusterOperatorName)
			operator.Status.Versions = tc.currentVersion
			builder = builder.WithObjects(operator)
		}
		optr.Client = builder.Build()

		err := optr.setStatusAvailable(context.TODO(), tc.overrides)
		assert.NoErrorf(t, err, "Failed to set Available status on ClusterOperator")

		gotCO, err := optr.getOrCreateClusterOperator(context.TODO())
		assert.NoErrorf(t, err, "Failed to fetch ClusterOperator")

		var condition configv1.ClusterOperatorStatusCondition
		for _, coCondition := range gotCO.Status.Conditions {
			assert.True(t,
				startTime.Before(&coCondition.LastTransitionTime) || startTime.Equal(&coCondition.LastTransitionTime),
				"test-case %v expected LastTransitionTime for the status condition %v to be after %v", i, coCondition, startTime)
			if coCondition.Type == configv1.OperatorAvailable {
				condition = coCondition
				break
			}
		}

		for _, expectedCondition := range tc.expectedConditions {
			ok := v1helpers.IsStatusConditionPresentAndEqual(
				gotCO.Status.Conditions, expectedCondition.Type, expectedCondition.Status,
			)
			if !ok {
				t.Errorf("wrong status for condition. Expected: %v, got: %v",
					expectedCondition,
					v1helpers.FindStatusCondition(gotCO.Status.Conditions, expectedCondition.Type))
			}
		}

		assert.True(t,
			len(tc.expectedConditions) == len(gotCO.Status.Conditions),
			"test-case %v expected equal number of conditions to %v, got %v", i, len(tc.expectedConditions), len(gotCO.Status.Conditions))

		desiredVersion := []configv1.OperandVersion{
			{
				Name:    "operator",
				Version: tc.desiredVersion,
			},
		}
		assert.True(t, equality.Semantic.DeepEqual(gotCO.Status.Versions, desiredVersion),
			"test-case %v expected equal version for ClusterOperator to %v, got %v", i, desiredVersion, gotCO.Status.Versions)

		err = optr.setStatusAvailable(context.TODO(), tc.overrides)
		assert.NoErrorf(t, err, "Failed to set Available status on ClusterOperator")

		err = optr.Client.Get(context.TODO(), client.ObjectKey{Name: clusterOperatorName}, gotCO)
		assert.NoErrorf(t, err, "Failed to fetch ClusterOperator")

		var conditionAfterAnotherSync configv1.ClusterOperatorStatusCondition
		for _, coCondition := range gotCO.Status.Conditions {
			if coCondition.Type == configv1.OperatorAvailable {
				conditionAfterAnotherSync = coCondition
				break
			}
		}
		assert.True(t, condition.LastTransitionTime.Equal(&conditionAfterAnotherSync.LastTransitionTime), "test-case %v expected LastTransitionTime not to be updated if condition state is same", i)

		for _, expectedCondition := range tc.expectedConditions {
			ok := v1helpers.IsStatusConditionPresentAndEqual(
				gotCO.Status.Conditions, expectedCondition.Type, expectedCondition.Status,
			)
			if !ok {
				t.Errorf("wrong status for condition. Expected: %v, got: %v",
					expectedCondition,
					v1helpers.FindStatusCondition(gotCO.Status.Conditions, expectedCondition.Type))
			}
		}

		assert.True(t,
			len(tc.expectedConditions) == len(gotCO.Status.Conditions),
			"test-case %v expected equal number of conditions to %v, got %v", i, len(tc.expectedConditions), len(gotCO.Status.Conditions))

		assert.True(t, equality.Semantic.DeepEqual(gotCO.Status.Versions, desiredVersion),
			"test-case %v expected equal version for ClusterOperator to %v, got %v", i, desiredVersion, gotCO.Status.Versions)
	}
}
