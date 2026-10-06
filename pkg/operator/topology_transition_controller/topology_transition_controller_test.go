package topology_transition_controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
)

func TestSync(t *testing.T) {
	t.Run("idle no-op when spec equals status", func(t *testing.T) {
		infra := newTestInfra(configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(infra, nil, nil, noopTransitions())

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))
	})

	t.Run("idle no-op when spec is empty", func(t *testing.T) {
		infra := newTestInfra("", configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(infra, nil, nil, noopTransitions())

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))
	})

	t.Run("transition triggered sets conditions and updates status", func(t *testing.T) {
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(infra, nil, nil, noopTransitions())

		if !assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext())) {
			return
		}

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, transitionProgressingCondition))
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))

		updated, err := ctrl.infraClient.Get(context.TODO(), "cluster", metav1.GetOptions{})
		if !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, configv1.HighlyAvailableTopologyMode, updated.Status.ControlPlaneTopology)
		assert.Equal(t, configv1.HighlyAvailableTopologyMode, updated.Status.InfrastructureTopology)
	})

	t.Run("unsupported transition sets conditions and blocks upgrades", func(t *testing.T) {
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.AWSPlatformType)
		ctrl := newTestController(infra, nil, nil, noopTransitions())

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		cond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, operatorv1.ConditionFalse, cond.Status)
		assert.Equal(t, "UnsupportedTransition", cond.Reason)
		assert.Contains(t, cond.Message, "is not supported")
		assert.Contains(t, cond.Message, "platform=AWS")
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))
	})

	t.Run("preflight validation failure sets conditions and blocks upgrades", func(t *testing.T) {
		failingTransitions := []TransitionDescriptor{
			{
				From: configv1.InfrastructureStatus{
					ControlPlaneTopology:   configv1.SingleReplicaTopologyMode,
					InfrastructureTopology: configv1.SingleReplicaTopologyMode,
					PlatformStatus:         &configv1.PlatformStatus{Type: configv1.NonePlatformType},
				},
				To: configv1.InfrastructureSpec{
					ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
				},
				PreflightValidators: []TransitionValidatorFunc{
					func() (string, error) { return "insufficient control plane nodes", nil },
				},
				UpdateStatus: func(infra *configv1.Infrastructure) {
					infra.Status.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
					infra.Status.InfrastructureTopology = configv1.HighlyAvailableTopologyMode
				},
			},
		}

		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(infra, nil, nil, failingTransitions)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		// Check operator conditions
		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		cond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, operatorv1.ConditionFalse, cond.Status)
		assert.Equal(t, "PreflightCheckFailed", cond.Reason)
		assert.Contains(t, cond.Message, "insufficient control plane nodes")
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))

		// Check Infrastructure status
		updated, getErr := ctrl.infraClient.Get(context.TODO(), "cluster", metav1.GetOptions{})
		if !assert.NoError(t, getErr) {
			return
		}
		if !assert.NotNil(t, updated.Status.TopologyTransitionStatus) {
			return
		}
		if !assert.NotNil(t, updated.Status.TopologyTransitionStatus.Conditions) {
			return
		}
		evaluatedCond := findCondition(updated.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionsEvaluatedConditionType)
		if !assert.NotNil(t, evaluatedCond) {
			return
		}
		assert.Equal(t, metav1.ConditionTrue, evaluatedCond.Status)
		assert.Equal(t, "PreflightCheckFailed", evaluatedCond.Reason)
		assert.Contains(t, evaluatedCond.Message, "insufficient control plane nodes")

		// Check Transitions array
		if !assert.Len(t, updated.Status.TopologyTransitionStatus.Transitions, 1) {
			return
		}
		transition := updated.Status.TopologyTransitionStatus.Transitions[0]
		assert.Equal(t, configv1.SingleReplicaTopologyMode, transition.Source.ControlPlaneTopology)
		assert.Equal(t, configv1.SingleReplicaTopologyMode, transition.Source.InfrastructureTopology)
		assert.Equal(t, configv1.HighlyAvailableTopologyMode, transition.Target.ControlPlaneTopology)
		assert.Equal(t, configv1.HighlyAvailableTopologyMode, transition.Target.InfrastructureTopology)
		if !assert.Len(t, transition.Evaluations, 1) {
			return
		}
		availableCond := transition.Evaluations[0]
		assert.Equal(t, configv1.TopologyTransitionAvailableConditionType, availableCond.Type)
		assert.Equal(t, metav1.ConditionFalse, availableCond.Status)
		assert.Equal(t, "PreflightCheckFailed", availableCond.Reason)
		assert.Contains(t, availableCond.Message, "insufficient control plane nodes")
	})

	t.Run("preflight execution error sets Evaluated=False", func(t *testing.T) {
		executionErrorTransitions := []TransitionDescriptor{
			{
				From: configv1.InfrastructureStatus{
					ControlPlaneTopology:   configv1.SingleReplicaTopologyMode,
					InfrastructureTopology: configv1.SingleReplicaTopologyMode,
					PlatformStatus:         &configv1.PlatformStatus{Type: configv1.NonePlatformType},
				},
				To: configv1.InfrastructureSpec{
					ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
				},
				PreflightValidators: []TransitionValidatorFunc{
					func() (string, error) { return "", fmt.Errorf("failed to list nodes") },
				},
				UpdateStatus: func(infra *configv1.Infrastructure) {
					infra.Status.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
					infra.Status.InfrastructureTopology = configv1.HighlyAvailableTopologyMode
				},
			},
		}

		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(infra, nil, nil, executionErrorTransitions)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		// Check Infrastructure status - Evaluated should be False when execution fails
		updated, getErr := ctrl.infraClient.Get(context.TODO(), "cluster", metav1.GetOptions{})
		if !assert.NoError(t, getErr) {
			return
		}
		if !assert.NotNil(t, updated.Status.TopologyTransitionStatus) {
			return
		}
		evaluatedCond := findCondition(updated.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionsEvaluatedConditionType)
		if !assert.NotNil(t, evaluatedCond) {
			return
		}
		assert.Equal(t, metav1.ConditionFalse, evaluatedCond.Status, "Evaluated should be False when validation fails to execute")
		assert.Equal(t, "PreflightCheckFailed", evaluatedCond.Reason)
	})

	t.Run("reconciliation blocked during soak period", func(t *testing.T) {
		now := time.Now()
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
		clk := clocktesting.NewFakePassiveClock(now)
		ctrl := newTestControllerWithClock(infra, transitionInProgressConditionsAt(now), nil, noopTransitions(), clk)

		if !assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext())) {
			return
		}

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionPresentAndEqual(status.Conditions, transitionProgressingCondition, operatorv1.ConditionTrue))
		assert.True(t, v1helpers.IsOperatorConditionPresentAndEqual(status.Conditions, upgradeableCondition, operatorv1.ConditionFalse))
	})

	t.Run("reconciliation complete clears conditions", func(t *testing.T) {
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(infra, transitionInProgressConditions(), nil, noopTransitions())

		if !assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext())) {
			return
		}

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, transitionProgressingCondition))
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition))
	})

	t.Run("reconciliation not complete preserves conditions", func(t *testing.T) {
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(infra, transitionInProgressConditions(), nil, noopTransitionsWithValidators(
			func() (string, error) { return "", fmt.Errorf("not yet reconciled") },
		))

		if !assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext())) {
			return
		}

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, transitionProgressingCondition))
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))
	})

	t.Run("idle path with stale TopologyTransitionInProgress routes through reconciliation", func(t *testing.T) {
		infra := newTestInfra(configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		staleConditions := []operatorv1.OperatorCondition{
			{
				Type:   upgradeableCondition,
				Status: operatorv1.ConditionFalse,
				Reason: reasonTopologyTransitionInProgress,
			},
		}
		ctrl := newTestController(infra, staleConditions, nil, reconciliationTestTransitions())

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		// Reconciliation checks pass, so conditions are cleared
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition))
	})

	t.Run("safety-net path respects soak timer via Upgradeable condition", func(t *testing.T) {
		now := time.Now()
		infra := newTestInfra(configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		clk := clocktesting.NewFakePassiveClock(now)

		staleConditions := []operatorv1.OperatorCondition{
			{
				Type:               upgradeableCondition,
				Status:             operatorv1.ConditionFalse,
				Reason:             reasonTopologyTransitionInProgress,
				LastTransitionTime: metav1.NewTime(now),
			},
		}
		ctrl := newTestControllerWithClock(infra, staleConditions, nil, reconciliationTestTransitions(), clk)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))
	})

	t.Run("safety-net path completes after soak timer elapses", func(t *testing.T) {
		now := time.Now()
		infra := newTestInfra(configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		clk := clocktesting.NewFakePassiveClock(now.Add(10 * time.Minute))

		staleConditions := []operatorv1.OperatorCondition{
			{
				Type:               upgradeableCondition,
				Status:             operatorv1.ConditionFalse,
				Reason:             reasonTopologyTransitionInProgress,
				LastTransitionTime: metav1.NewTime(now),
			},
		}
		ctrl := newTestControllerWithClock(infra, staleConditions, nil, reconciliationTestTransitions(), clk)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition))
	})

	t.Run("idle path with stale TopologyTransitionInProgress blocks when reconciliation incomplete", func(t *testing.T) {
		infra := newTestInfra(configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		staleConditions := []operatorv1.OperatorCondition{
			{
				Type:   upgradeableCondition,
				Status: operatorv1.ConditionFalse,
				Reason: reasonTopologyTransitionInProgress,
			},
		}
		ctrl := newTestController(infra, staleConditions, nil, reconciliationTestTransitions(
			func() (string, error) { return "", fmt.Errorf("not yet reconciled") },
		))

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		// Reconciliation incomplete — Upgradeable stays False
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))
	})

	t.Run("reverting spec to match status clears Upgradeable=False", func(t *testing.T) {
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
		staleConditions := []operatorv1.OperatorCondition{
			{
				Type:   upgradeableCondition,
				Status: operatorv1.ConditionFalse,
				Reason: "PreflightCheckFailed",
			},
			{
				Type:   transitionProgressingCondition,
				Status: operatorv1.ConditionFalse,
				Reason: "PreflightCheckFailed",
			},
		}
		ctrl := newTestController(infra, staleConditions, nil, noopTransitions())

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition))

		// The stale rejection reason on transitionProgressingCondition must not
		// linger once the offending spec change has been withdrawn.
		progressingCond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, progressingCond) {
			return
		}
		assert.Equal(t, operatorv1.ConditionFalse, progressingCond.Status)
		assert.NotEqual(t, "PreflightCheckFailed", progressingCond.Reason)
		assert.Equal(t, "AsExpected", progressingCond.Reason)
	})

	t.Run("reverting spec to match status clears stale UnsupportedTransition reason", func(t *testing.T) {
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
		staleConditions := []operatorv1.OperatorCondition{
			{
				Type:   upgradeableCondition,
				Status: operatorv1.ConditionFalse,
				Reason: "UnsupportedTransition",
			},
			{
				Type:   transitionProgressingCondition,
				Status: operatorv1.ConditionFalse,
				Reason: "UnsupportedTransition",
			},
		}
		ctrl := newTestController(infra, staleConditions, nil, noopTransitions())

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition))

		progressingCond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, progressingCond) {
			return
		}
		assert.Equal(t, operatorv1.ConditionFalse, progressingCond.Status)
		assert.NotEqual(t, "UnsupportedTransition", progressingCond.Reason)
		assert.Equal(t, "AsExpected", progressingCond.Reason)
	})

	t.Run("spec change during status update skips update and returns nil", func(t *testing.T) {
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(infra, nil, nil, noopTransitions())

		reverted := infra.DeepCopy()
		reverted.Spec.ControlPlaneTopology = configv1.SingleReplicaTopologyMode
		_, updateErr := ctrl.infraClient.Update(context.TODO(), reverted, metav1.UpdateOptions{})
		if !assert.NoError(t, updateErr) {
			return
		}

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		current, getErr := ctrl.infraClient.Get(context.TODO(), "cluster", metav1.GetOptions{})
		if !assert.NoError(t, getErr) {
			return
		}
		assert.Equal(t, configv1.SingleReplicaTopologyMode, current.Status.ControlPlaneTopology)
	})

	t.Run("crash recovery retries transition when conditions set but infra status stale", func(t *testing.T) {
		// Simulates a controller restart where conditions were set to
		// Progressing=True but the infra status update never completed,
		// so spec != status still holds.
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(infra, transitionInProgressConditions(), nil, noopTransitions())

		if !assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext())) {
			return
		}

		updated, err := ctrl.infraClient.Get(context.TODO(), "cluster", metav1.GetOptions{})
		if !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, configv1.HighlyAvailableTopologyMode, updated.Status.ControlPlaneTopology)
		assert.Equal(t, configv1.HighlyAvailableTopologyMode, updated.Status.InfrastructureTopology)

		_, status, _, statusErr := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, statusErr) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, transitionProgressingCondition))
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))
	})

	t.Run("reconciliation blocked when one of several transition validators fails", func(t *testing.T) {
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(infra, transitionInProgressConditions(), nil, noopTransitionsWithValidators(
			func() (string, error) { return "", nil },
			func() (string, error) { return "", fmt.Errorf("machine config pool not ready") },
			func() (string, error) { return "", nil },
		))

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, transitionProgressingCondition))
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))
	})

	t.Run("reconciliation completes when all transition validators pass", func(t *testing.T) {
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(infra, transitionInProgressConditions(), nil, noopTransitionsWithValidators(
			func() (string, error) { return "", nil },
			func() (string, error) { return "", nil },
		))

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, transitionProgressingCondition))
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition))
	})

	t.Run("reconciliation with no matching transition treats validators as satisfied", func(t *testing.T) {
		infra := newTestInfra(configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		// noopTransitions only matches a HighlyAvailable spec, so it won't
		// match this SingleReplica infra — there are no validators to run.
		ctrl := newTestController(infra, transitionInProgressConditions(), nil, noopTransitions())

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, transitionProgressingCondition))
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition))
	})
}

func TestMatchesStatus(t *testing.T) {
	tests := []struct {
		name       string
		descriptor configv1.InfrastructureStatus
		actual     configv1.InfrastructureStatus
		expected   bool
	}{
		{
			name: "exact match",
			descriptor: configv1.InfrastructureStatus{
				ControlPlaneTopology:   configv1.SingleReplicaTopologyMode,
				InfrastructureTopology: configv1.SingleReplicaTopologyMode,
				PlatformStatus:         &configv1.PlatformStatus{Type: configv1.NonePlatformType},
			},
			actual: configv1.InfrastructureStatus{
				ControlPlaneTopology:   configv1.SingleReplicaTopologyMode,
				InfrastructureTopology: configv1.SingleReplicaTopologyMode,
				PlatformStatus:         &configv1.PlatformStatus{Type: configv1.NonePlatformType},
			},
			expected: true,
		},
		{
			name: "wildcard platform matches any",
			descriptor: configv1.InfrastructureStatus{
				ControlPlaneTopology: configv1.SingleReplicaTopologyMode,
			},
			actual: configv1.InfrastructureStatus{
				ControlPlaneTopology: configv1.SingleReplicaTopologyMode,
				PlatformStatus:       &configv1.PlatformStatus{Type: configv1.AWSPlatformType},
			},
			expected: true,
		},
		{
			name: "topology mismatch",
			descriptor: configv1.InfrastructureStatus{
				ControlPlaneTopology: configv1.SingleReplicaTopologyMode,
			},
			actual: configv1.InfrastructureStatus{
				ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
			},
			expected: false,
		},
		{
			name: "platform mismatch",
			descriptor: configv1.InfrastructureStatus{
				PlatformStatus: &configv1.PlatformStatus{Type: configv1.NonePlatformType},
			},
			actual: configv1.InfrastructureStatus{
				PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType},
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, matchesStatus(tc.descriptor, tc.actual))
		})
	}
}

func TestMatchesSpec(t *testing.T) {
	tests := []struct {
		name       string
		descriptor configv1.InfrastructureSpec
		actual     configv1.InfrastructureSpec
		expected   bool
	}{
		{
			name: "exact match",
			descriptor: configv1.InfrastructureSpec{
				ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
			},
			actual: configv1.InfrastructureSpec{
				ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
			},
			expected: true,
		},
		{
			name:       "wildcard matches any",
			descriptor: configv1.InfrastructureSpec{},
			actual: configv1.InfrastructureSpec{
				ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
			},
			expected: true,
		},
		{
			name: "mismatch",
			descriptor: configv1.InfrastructureSpec{
				ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
			},
			actual: configv1.InfrastructureSpec{
				ControlPlaneTopology: configv1.SingleReplicaTopologyMode,
			},
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, matchesSpec(tc.descriptor, tc.actual))
		})
	}
}

func TestFindTransition(t *testing.T) {
	transitions := noopTransitions()

	t.Run("matching transition found", func(t *testing.T) {
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		td, err := findTransition(infra, transitions)
		if !assert.NoError(t, err) {
			return
		}
		assert.NotNil(t, td)
	})

	t.Run("no matching transition", func(t *testing.T) {
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.AWSPlatformType)
		td, err := findTransition(infra, transitions)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "platform=AWS")
		assert.Nil(t, td)
	})

	t.Run("nil platform status shows Unknown", func(t *testing.T) {
		infra := &configv1.Infrastructure{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
			Spec: configv1.InfrastructureSpec{
				ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
			},
			Status: configv1.InfrastructureStatus{
				ControlPlaneTopology:   configv1.SingleReplicaTopologyMode,
				InfrastructureTopology: configv1.SingleReplicaTopologyMode,
			},
		}
		_, err := findTransition(infra, transitions)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "platform=Unknown")
	})
}

func TestValidatePreflight(t *testing.T) {
	t.Run("all validators pass", func(t *testing.T) {
		td := &TransitionDescriptor{
			PreflightValidators: []TransitionValidatorFunc{
				func() (string, error) { return "", nil },
				func() (string, error) { return "", nil },
			},
		}
		_, err := validatePreflight(nil, td)
		assert.NoError(t, err)
	})

	t.Run("single validator fails", func(t *testing.T) {
		td := &TransitionDescriptor{
			PreflightValidators: []TransitionValidatorFunc{
				func() (string, error) { return "", nil },
				func() (string, error) { return "", fmt.Errorf("node count too low") },
			},
		}
		_, err := validatePreflight(nil, td)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "node count too low")
	})

	t.Run("accumulates reasons but stops on first error", func(t *testing.T) {
		td := &TransitionDescriptor{
			PreflightValidators: []TransitionValidatorFunc{
				func() (string, error) { return "insufficient nodes", nil },
				func() (string, error) { return "etcd not ready", nil },
				func() (string, error) { return "", fmt.Errorf("api error") },
			},
		}
		reasons, err := validatePreflight(nil, td)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "api error")
		// Reasons from validators before the error should be accumulated
		assert.Contains(t, reasons, "insufficient nodes")
		assert.Contains(t, reasons, "etcd not ready")
	})

	t.Run("stops on first error", func(t *testing.T) {
		td := &TransitionDescriptor{
			PreflightValidators: []TransitionValidatorFunc{
				func() (string, error) { return "", fmt.Errorf("node count too low") },
				func() (string, error) { return "", nil },
				func() (string, error) { return "", fmt.Errorf("etcd not ready") },
			},
		}
		_, err := validatePreflight(nil, td)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "node count too low")
		// Should stop on first error, not accumulate all
		assert.NotContains(t, err.Error(), "etcd not ready")
	})

	t.Run("nil validators", func(t *testing.T) {
		td := &TransitionDescriptor{}
		_, err := validatePreflight(nil, td)
		assert.NoError(t, err)
	})

	t.Run("global preflight checks run before transition validators", func(t *testing.T) {
		globalChecks := []TransitionValidatorFunc{
			func() (string, error) { return "", fmt.Errorf("global check failed") },
		}
		td := &TransitionDescriptor{
			PreflightValidators: []TransitionValidatorFunc{
				func() (string, error) { return "", nil },
			},
		}
		_, err := validatePreflight(globalChecks, td)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "global check failed")
	})

	t.Run("stops on first error from global checks", func(t *testing.T) {
		globalChecks := []TransitionValidatorFunc{
			func() (string, error) { return "", fmt.Errorf("operators unstable") },
		}
		td := &TransitionDescriptor{
			PreflightValidators: []TransitionValidatorFunc{
				func() (string, error) { return "", fmt.Errorf("etcd not ready") },
			},
		}
		_, err := validatePreflight(globalChecks, td)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "operators unstable")
		// Should stop on first error, not run transition validators
		assert.NotContains(t, err.Error(), "etcd not ready")
	})
}

// findCondition searches for a condition with the given type in the slice.
func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}
