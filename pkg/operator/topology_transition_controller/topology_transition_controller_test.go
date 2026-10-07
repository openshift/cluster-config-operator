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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clocktesting "k8s.io/utils/clock/testing"
)

func TestSync(t *testing.T) {
	t.Run("transition progress is recorded on Infrastructure, not the operator", func(t *testing.T) {
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(infra, nil, nil, noopTransitions())

		assert.NoError(t, ctrl.sync(context.Background(), newTestSyncContext()))

		updated := currentInfra(t, ctrl)
		if !assert.NotEmpty(t, updated.Status.TopologyTransitionStatus.Conditions) {
			return
		}

		condition := meta.FindStatusCondition(updated.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionCompletedConditionType)
		if assert.NotNil(t, condition) {
			assert.Equal(t, metav1.ConditionFalse, condition.Status)
			assert.Equal(t, reasonTopologyTransitionInProgress, condition.Reason)
		}

		_, operatorStatus, _, err := ctrl.operatorClient.GetOperatorState()
		assert.NoError(t, err)
		assert.Nil(t, v1helpers.FindOperatorCondition(operatorStatus.Conditions, "TopologyTransitionControllerProgressing"))
	})

	t.Run("standalone evaluation publishes discovery when spec equals status", func(t *testing.T) {
		infra := newTestInfra(configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(infra, nil, nil, noopTransitions())

		assert.NoError(t, ctrl.syncEvaluation(t.Context()))

		updated := currentInfra(t, ctrl)
		if assert.NotEmpty(t, updated.Status.TopologyTransitionStatus.Conditions) {
			assert.Len(t, updated.Status.TopologyTransitionStatus.Transitions, 1)
		}
	})

	t.Run("standalone evaluation publishes discovery when spec is empty", func(t *testing.T) {
		infra := newTestInfra("", configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(infra, nil, nil, noopTransitions())

		assert.NoError(t, ctrl.syncEvaluation(t.Context()))

		updated := currentInfra(t, ctrl)
		if assert.NotEmpty(t, updated.Status.TopologyTransitionStatus.Conditions) {
			assert.Len(t, updated.Status.TopologyTransitionStatus.Transitions, 1)
		}
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
		assert.Equal(t, reasonTopologyTransitionInProgress, completionCondition(t, ctrl).Reason)
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

		cond := completionCondition(t, ctrl)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, "UnsupportedTransition", cond.Reason)
		assert.Contains(t, cond.Message, "is not supported")
		assert.Contains(t, cond.Message, "platform=AWS")
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))
		assert.Nil(t, v1helpers.FindOperatorCondition(status.Conditions, "TopologyTransitionControllerProgressing"))
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
				PreflightValidators: []PreflightCheck{
					{Type: "ControlPlaneNodeCountSatisfied", Validate: func() error { return fmt.Errorf("insufficient control plane nodes") }},
				},
				UpdateStatus: func(infra *configv1.Infrastructure) {},
			},
		}

		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(infra, nil, nil, failingTransitions)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}

		cond := completionCondition(t, ctrl)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, "PreflightCheckFailed", cond.Reason)
		assert.Contains(t, cond.Message, "insufficient control plane nodes")
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))
	})

	t.Run("reconciliation blocked during soak period", func(t *testing.T) {
		now := time.Now()
		infra := withTransitionInProgress(newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType), now)
		clk := clocktesting.NewFakePassiveClock(now)
		ctrl := newTestControllerWithClock(infra, transitionInProgressConditionsAt(now), nil, noopTransitions(), clk)

		if !assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext())) {
			return
		}

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, reasonTopologyTransitionInProgress, completionCondition(t, ctrl).Reason)
		assert.True(t, v1helpers.IsOperatorConditionPresentAndEqual(status.Conditions, upgradeableCondition, operatorv1.ConditionFalse))
	})

	t.Run("reconciliation complete clears conditions", func(t *testing.T) {
		infra := withTransitionInProgress(newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType), time.Now().Add(-10*time.Minute))
		ctrl := newTestController(infra, transitionInProgressConditions(), nil, noopTransitions())

		if !assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext())) {
			return
		}

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, metav1.ConditionTrue, completionCondition(t, ctrl).Status)
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition))
		assert.Nil(t, v1helpers.FindOperatorCondition(status.Conditions, "TopologyTransitionControllerProgressing"))
	})

	t.Run("reconciliation not complete preserves conditions", func(t *testing.T) {
		infra := withTransitionInProgress(newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType), time.Now().Add(-10*time.Minute))
		ctrl := newTestController(infra, transitionInProgressConditions(), nil, noopTransitionsWithValidators(
			func() error { return fmt.Errorf("not yet reconciled") },
		))

		if !assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext())) {
			return
		}

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, reasonTopologyTransitionInProgress, completionCondition(t, ctrl).Reason)
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
			func() error { return fmt.Errorf("not yet reconciled") },
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
		}
		infra.Status.TopologyTransitionStatus = configv1.TopologyTransitionStatus{Conditions: []metav1.Condition{{Type: configv1.TopologyTransitionCompletedConditionType, Status: metav1.ConditionFalse, Reason: "PreflightCheckFailed"}}}
		ctrl := newTestController(infra, staleConditions, nil, noopTransitions())

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition))

		// The stale API rejection reason must not
		// linger once the offending spec change has been withdrawn.
		progressingCond := completionCondition(t, ctrl)
		if !assert.NotNil(t, progressingCond) {
			return
		}
		assert.Equal(t, metav1.ConditionUnknown, progressingCond.Status)
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
		}
		infra.Status.TopologyTransitionStatus = configv1.TopologyTransitionStatus{Conditions: []metav1.Condition{{Type: configv1.TopologyTransitionCompletedConditionType, Status: metav1.ConditionFalse, Reason: "UnsupportedTransition"}}}
		ctrl := newTestController(infra, staleConditions, nil, noopTransitions())

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition))

		progressingCond := completionCondition(t, ctrl)
		if !assert.NotNil(t, progressingCond) {
			return
		}
		assert.Equal(t, metav1.ConditionUnknown, progressingCond.Status)
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
		infra = withTransitionInProgress(infra, time.Now().Add(-10*time.Minute))
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
		assert.Equal(t, reasonTopologyTransitionInProgress, completionCondition(t, ctrl).Reason)
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))
	})

	t.Run("reconciliation blocked when one of several transition validators fails", func(t *testing.T) {
		infra := withTransitionInProgress(newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType), time.Now().Add(-10*time.Minute))
		ctrl := newTestController(infra, transitionInProgressConditions(), nil, noopTransitionsWithValidators(
			func() error { return nil },
			func() error { return fmt.Errorf("machine config pool not ready") },
			func() error { return nil },
		))

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, reasonTopologyTransitionInProgress, completionCondition(t, ctrl).Reason)
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))
	})

	t.Run("reconciliation completes when all transition validators pass", func(t *testing.T) {
		infra := withTransitionInProgress(newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType), time.Now().Add(-10*time.Minute))
		ctrl := newTestController(infra, transitionInProgressConditions(), nil, noopTransitionsWithValidators(
			func() error { return nil },
			func() error { return nil },
		))

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, metav1.ConditionTrue, completionCondition(t, ctrl).Status)
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition))
	})

	t.Run("reconciliation with no matching transition keeps upgrades blocked", func(t *testing.T) {
		infra := withTransitionInProgress(newTestInfra(configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType), time.Now().Add(-10*time.Minute))
		// noopTransitions only matches a HighlyAvailable spec, so it won't
		// match this SingleReplica infra; the target cannot be verified.
		ctrl := newTestController(infra, transitionInProgressConditions(), nil, noopTransitions())

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, metav1.ConditionFalse, completionCondition(t, ctrl).Status)
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))
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
			PreflightValidators: []PreflightCheck{
				{Type: "FirstCheck", Validate: func() error { return nil }},
				{Type: "SecondCheck", Validate: func() error { return nil }},
			},
		}

		assert.NoError(t, validatePreflight(nil, td))
	})

	t.Run("single validator fails", func(t *testing.T) {
		td := &TransitionDescriptor{
			PreflightValidators: []PreflightCheck{
				{Type: "FirstCheck", Validate: func() error { return nil }},
				{Type: "SecondCheck", Validate: func() error { return fmt.Errorf("node count too low") }},
			},
		}

		err := validatePreflight(nil, td)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "node count too low")
	})

	t.Run("multiple failures are accumulated", func(t *testing.T) {
		td := &TransitionDescriptor{
			PreflightValidators: []PreflightCheck{
				{Type: "FirstCheck", Validate: func() error { return fmt.Errorf("node count too low") }},
				{Type: "SecondCheck", Validate: func() error { return nil }},
				{Type: "ThirdCheck", Validate: func() error { return fmt.Errorf("etcd not ready") }},
			},
		}

		err := validatePreflight(nil, td)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "node count too low")
		assert.Contains(t, err.Error(), "etcd not ready")
	})

	t.Run("nil validators", func(t *testing.T) {
		td := &TransitionDescriptor{}
		assert.NoError(t, validatePreflight(nil, td))
	})

	t.Run("global preflight checks run before transition validators", func(t *testing.T) {
		globalChecks := []PreflightCheck{
			{Type: "GlobalCheck", Validate: func() error { return fmt.Errorf("global check failed") }},
		}
		td := &TransitionDescriptor{
			PreflightValidators: []PreflightCheck{
				{Type: "TransitionCheck", Validate: func() error { return nil }},
			},
		}

		err := validatePreflight(globalChecks, td)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "global check failed")
	})

	t.Run("global and transition failures are accumulated", func(t *testing.T) {
		globalChecks := []PreflightCheck{
			{Type: "GlobalCheck", Validate: func() error { return fmt.Errorf("operators unstable") }},
		}
		td := &TransitionDescriptor{
			PreflightValidators: []PreflightCheck{
				{Type: "TransitionCheck", Validate: func() error { return fmt.Errorf("etcd not ready") }},
			},
		}

		err := validatePreflight(globalChecks, td)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "operators unstable")
		assert.Contains(t, err.Error(), "etcd not ready")
	})
}
