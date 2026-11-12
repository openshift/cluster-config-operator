package topology_transition_controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	configfakeclient "github.com/openshift/client-go/config/clientset/versioned/fake"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	"k8s.io/apimachinery/pkg/api/meta"
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

	t.Run("conflicting infrastructure target is rejected without mutating status", func(t *testing.T) {
		// SNO with controlPlane=HighlyAvailable requested but an explicit
		// infrastructure=SingleReplica target: SNOToHACompact matches via its
		// wildcard infrastructure target, but its updater sets infrastructure
		// to HighlyAvailable, which would leave the explicit request diverging
		// from status in a state no descriptor matches.
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		infra.Spec.InfrastructureTopology = configv1.SingleReplicaTopologyMode
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
		assert.Contains(t, cond.Message, "spec.infrastructureTopology=SingleReplica conflicts with the transition target HighlyAvailable")
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))

		// The transition must not be applied: status still matches the SNO state.
		updated, err := ctrl.infraClient.Get(context.TODO(), "cluster", metav1.GetOptions{})
		if !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, configv1.SingleReplicaTopologyMode, updated.Status.ControlPlaneTopology)
		assert.Equal(t, configv1.SingleReplicaTopologyMode, updated.Status.InfrastructureTopology)
	})

	t.Run("preflight validation failure sets conditions and blocks upgrades", func(t *testing.T) {
		failingTransitions := []TransitionDescriptor{
			{
				Name: "SNOToHACompact",
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
				UpdateStatus: func(infra *configv1.Infrastructure) {
					infra.Status.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
					infra.Status.InfrastructureTopology = configv1.HighlyAvailableTopologyMode
				},
			},
		}

		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(infra, nil, nil, failingTransitions)

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
		assert.Equal(t, "PreflightCheckFailed", cond.Reason)
		assert.Contains(t, cond.Message, "insufficient control plane nodes")
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))
	})

	t.Run("reconciliation blocked during soak period", func(t *testing.T) {
		now := time.Now()
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
		clk := clocktesting.NewFakePassiveClock(now)
		ctrl := newTestControllerWithClock(markTransitionCommitted(infra, "SNOToHACompact"), transitionInProgressConditionsAt(now, "SNOToHACompact"), nil, noopTransitions(), clk)

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
		ctrl := newTestController(markTransitionCommitted(infra, "SNOToHACompact"), transitionInProgressConditions("SNOToHACompact"), nil, noopTransitions())

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
		ctrl := newTestController(markTransitionCommitted(infra, "SNOToHACompact"), transitionInProgressConditions("SNOToHACompact"), nil, noopTransitionsWithValidators(
			func() error { return fmt.Errorf("not yet reconciled") },
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

	t.Run("idle path with stale transition routes through reconciliation", func(t *testing.T) {
		infra := newTestInfra(configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		staleConditions := []operatorv1.OperatorCondition{
			{
				Type:   upgradeableCondition,
				Status: operatorv1.ConditionFalse,
				Reason: "TestTransition",
			},
		}
		ctrl := newTestController(markTransitionCommitted(infra, "TestTransition"), staleConditions, nil, reconciliationTestTransitions())

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
				Reason:             "TestTransition",
				LastTransitionTime: metav1.NewTime(now),
			},
		}
		ctrl := newTestControllerWithClock(markTransitionCommitted(infra, "TestTransition"), staleConditions, nil, reconciliationTestTransitions(), clk)

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
				Reason:             "TestTransition",
				LastTransitionTime: metav1.NewTime(now),
			},
		}
		ctrl := newTestControllerWithClock(markTransitionCommitted(infra, "TestTransition"), staleConditions, nil, reconciliationTestTransitions(), clk)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition))
	})

	t.Run("idle path with stale transition blocks when reconciliation incomplete", func(t *testing.T) {
		infra := newTestInfra(configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		staleConditions := []operatorv1.OperatorCondition{
			{
				Type:   upgradeableCondition,
				Status: operatorv1.ConditionFalse,
				Reason: "TestTransition",
			},
		}
		ctrl := newTestController(markTransitionCommitted(infra, "TestTransition"), staleConditions, nil, reconciliationTestTransitions(
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
		ctrl := newTestController(infra, transitionInProgressConditions("SNOToHACompact"), nil, noopTransitions())

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
		ctrl := newTestController(markTransitionCommitted(infra, "SNOToHACompact"), transitionInProgressConditions("SNOToHACompact"), nil, noopTransitionsWithValidators(
			func() error { return nil },
			func() error { return fmt.Errorf("machine config pool not ready") },
			func() error { return nil },
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
		ctrl := newTestController(markTransitionCommitted(infra, "SNOToHACompact"), transitionInProgressConditions("SNOToHACompact"), nil, noopTransitionsWithValidators(
			func() error { return nil },
			func() error { return nil },
		))

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, transitionProgressingCondition))
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition))
	})

	t.Run("infrastructure transition triggered sets conditions and updates status", func(t *testing.T) {
		infra := newTestInfraWithInfraSpec(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(infra, nil, nil, noopInfraTransitions())

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
		assert.Equal(t, configv1.HighlyAvailableTopologyMode, updated.Status.InfrastructureTopology)
		assert.Equal(t, configv1.HighlyAvailableTopologyMode, updated.Status.ControlPlaneTopology)
	})

	t.Run("infrastructure transition idle when spec matches status", func(t *testing.T) {
		infra := newTestInfraWithInfraSpec(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(infra, nil, nil, noopInfraTransitions())

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))
	})

	t.Run("infrastructure transition reconciliation complete clears conditions", func(t *testing.T) {
		infra := newTestInfraWithInfraSpec(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(markTransitionCommitted(infra, "InfrastructureSingleToHA"), transitionInProgressConditions("InfrastructureSingleToHA"), nil, noopInfraTransitions())

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

	t.Run("infrastructure transition reconciliation blocked when validators fail", func(t *testing.T) {
		infra := newTestInfraWithInfraSpec(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(markTransitionCommitted(infra, "InfrastructureSingleToHA"), transitionInProgressConditions("InfrastructureSingleToHA"), nil, noopInfraTransitionsWithValidators(
			func() error { return fmt.Errorf("ingress not scaled up") },
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

	t.Run("reconciliation uses correct validators when both transitions are registered", func(t *testing.T) {
		// Both SNO->HA and infra SR->HA transitions share target HA topology.
		// After the infra SR->HA transition is applied, spec is {CP:HA, Infra:HA}.
		// Without the transition name in the condition, matchesSpec picks
		// the SNO->HA descriptor first (wrong validators). The condition's
		// Reason must route to the correct descriptor.
		snoValidatorCalled := false
		infraValidatorCalled := false
		bothTransitions := []TransitionDescriptor{
			{
				Name: "SNOToHACompact",
				From: configv1.InfrastructureStatus{
					ControlPlaneTopology:   configv1.SingleReplicaTopologyMode,
					InfrastructureTopology: configv1.SingleReplicaTopologyMode,
					PlatformStatus:         &configv1.PlatformStatus{Type: configv1.NonePlatformType},
				},
				To: configv1.InfrastructureSpec{
					ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
				},
				UpdateStatus: func(infra *configv1.Infrastructure) {
					infra.Status.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
					infra.Status.InfrastructureTopology = configv1.HighlyAvailableTopologyMode
				},
				TransitionValidators: []PostTransitionCheck{
					{Name: "SNOCheck", Validate: func() error { snoValidatorCalled = true; return nil }},
				},
			},
			{
				Name: "InfrastructureSingleToHA",
				From: configv1.InfrastructureStatus{
					ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
					InfrastructureTopology: configv1.SingleReplicaTopologyMode,
				},
				To: configv1.InfrastructureSpec{
					InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
				},
				UpdateStatus: func(infra *configv1.Infrastructure) {
					infra.Status.InfrastructureTopology = configv1.HighlyAvailableTopologyMode
				},
				TransitionValidators: []PostTransitionCheck{
					{Name: "InfraCheck", Validate: func() error { infraValidatorCalled = true; return nil }},
				},
			},
		}

		infra := newTestInfraWithInfraSpec(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
		infra.Spec.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
		ctrl := newTestController(markTransitionCommitted(infra, "InfrastructureSingleToHA"), transitionInProgressConditions("InfrastructureSingleToHA"), nil, bothTransitions)

		if !assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext())) {
			return
		}

		assert.False(t, snoValidatorCalled, "SNO->HA validators should not run during infra transition")
		assert.True(t, infraValidatorCalled, "infra SR->HA validators should run")

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, transitionProgressingCondition))
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition))
	})

	t.Run("reconciliation with unidentifiable committed transition stays blocked", func(t *testing.T) {
		infra := newTestInfra(configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		// The committed marker names a transition that matches no registered
		// descriptor. Completing without identifying the committed transition
		// would re-enable upgrades without checking workload reconciliation,
		// so the controller must withhold completion.
		ctrl := newTestController(markTransitionCommitted(infra, "UnknownTransition"), transitionInProgressConditions("UnknownTransition"), nil, noopTransitions())

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, transitionProgressingCondition), "unidentifiable transition must not complete")
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))

		completion := meta.FindStatusCondition(currentInfra(t, ctrl).Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionCompletedConditionType)
		if assert.NotNil(t, completion) {
			assert.Equal(t, metav1.ConditionFalse, completion.Status)
		}
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
		{
			name: "infrastructure topology exact match",
			descriptor: configv1.InfrastructureSpec{
				InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
			},
			actual: configv1.InfrastructureSpec{
				InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
			},
			expected: true,
		},
		{
			name: "infrastructure topology mismatch",
			descriptor: configv1.InfrastructureSpec{
				InfrastructureTopology: configv1.HighlyAvailableTopologyMode,
			},
			actual: configv1.InfrastructureSpec{
				InfrastructureTopology: configv1.SingleReplicaTopologyMode,
			},
			expected: false,
		},
		{
			name: "infrastructure topology wildcard matches any",
			descriptor: configv1.InfrastructureSpec{
				ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
			},
			actual: configv1.InfrastructureSpec{
				ControlPlaneTopology:   configv1.HighlyAvailableTopologyMode,
				InfrastructureTopology: configv1.SingleReplicaTopologyMode,
			},
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, matchesSpec(tc.descriptor, tc.actual))
		})
	}
}

func TestHasTopologyDivergence(t *testing.T) {
	tests := []struct {
		name     string
		infra    *configv1.Infrastructure
		expected bool
	}{
		{
			name:     "no divergence when spec empty",
			infra:    newTestInfra("", configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, ""),
			expected: false,
		},
		{
			name:     "no divergence when spec matches status",
			infra:    newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, ""),
			expected: false,
		},
		{
			name:     "control plane divergence",
			infra:    newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, ""),
			expected: true,
		},
		{
			name:     "infrastructure divergence",
			infra:    newTestInfraWithInfraSpec(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType),
			expected: true,
		},
		{
			name: "infrastructure divergence with matching control plane",
			infra: func() *configv1.Infrastructure {
				infra := newTestInfraWithInfraSpec(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
				infra.Spec.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
				return infra
			}(),
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, hasTopologyDivergence(tc.infra))
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

// TestValidatePreflight checks that global and transition checks run and their errors are combined.
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

// TestTopologyTransitionCompletedLifecycle pins the TopologyTransitionCompleted
// contract from the enhancement proposal: Completed=False is committed
// atomically with the topology status change, drives crash recovery when
// operator conditions are lost, and flips to True only after post-transition
// reconciliation converges.
func TestTopologyTransitionCompletedLifecycle(t *testing.T) {
	t.Run("commit marks the transition in progress in the same status write", func(t *testing.T) {
		c, client, _ := statusTestController(snoInfra(configv1.HighlyAvailableTopologyMode))

		assert.NoError(t, c.sync(context.TODO(), newTestSyncContext()))

		infra := currentInfra(t, c)
		assert.Equal(t, configv1.HighlyAvailableTopologyMode, infra.Status.ControlPlaneTopology)
		completion := meta.FindStatusCondition(infra.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionCompletedConditionType)
		if assert.NotNil(t, completion) {
			assert.Equal(t, metav1.ConditionFalse, completion.Status)
			assert.Equal(t, "SNOToHACompact", completion.Reason, "the marker must carry the committed transition identity")
		}
		// The topology change and the lifecycle condition must land in a
		// single status write: there is no intermediate persisted state where
		// the topology is HA but Completed is unset.
		assert.Equal(t, 1, statusWrites(client))
	})

	t.Run("cancelled commit leaves no completed condition behind", func(t *testing.T) {
		c, client, _ := statusTestController(snoInfra(configv1.HighlyAvailableTopologyMode))

		// The administrator reverts the spec request before the commit: the
		// lister still sees the divergence, but the fresh read inside
		// applyTopologyStatus must abort without writing.
		reverted := snoInfra("")
		resource := configv1.SchemeGroupVersion.WithResource("infrastructures")
		assert.NoError(t, client.Tracker().Update(resource, reverted, ""))

		assert.NoError(t, c.sync(context.TODO(), newTestSyncContext()))

		infra := currentInfra(t, c)
		assert.Equal(t, configv1.SingleReplicaTopologyMode, infra.Status.ControlPlaneTopology)
		assert.Nil(t, meta.FindStatusCondition(infra.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionCompletedConditionType))
		assert.Equal(t, 0, statusWrites(client))
	})

	t.Run("completion marks the transition complete and preserves discovery", func(t *testing.T) {
		started := evaluationTime.Add(-2 * minReconciliationSoakTime)
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
		discovered := []configv1.TopologyTransition{{
			Source: configv1.TopologyState{ControlPlaneTopology: configv1.SingleReplicaTopologyMode, InfrastructureTopology: configv1.SingleReplicaTopologyMode},
			Target: configv1.TopologyState{ControlPlaneTopology: configv1.HighlyAvailableTopologyMode, InfrastructureTopology: configv1.HighlyAvailableTopologyMode},
		}}
		infra.Status.TopologyTransitionStatus = configv1.TopologyTransitionStatus{
			Conditions: []metav1.Condition{
				{Type: configv1.TopologyTransitionsEvaluatedConditionType, Status: metav1.ConditionTrue, Reason: "EvaluationComplete", LastTransitionTime: metav1.NewTime(started)},
				{Type: configv1.TopologyTransitionCompletedConditionType, Status: metav1.ConditionFalse, Reason: "TransitionInProgress", LastTransitionTime: metav1.NewTime(started)},
			},
			Transitions: discovered,
		}
		c, _, _ := statusTestController(infra)
		c.operatorClient = v1helpers.NewFakeOperatorClient(&operatorv1.OperatorSpec{}, &operatorv1.OperatorStatus{
			Conditions: transitionInProgressConditionsAt(started, "SNOToHACompact"),
		}, nil)

		assert.NoError(t, c.sync(context.TODO(), newTestSyncContext()))

		updated := currentInfra(t, c)
		completion := meta.FindStatusCondition(updated.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionCompletedConditionType)
		if assert.NotNil(t, completion) {
			assert.Equal(t, metav1.ConditionTrue, completion.Status)
			assert.Equal(t, "TopologyTransitionComplete", completion.Reason)
		}
		// The completion write must not clobber the discovery report.
		evaluated := meta.FindStatusCondition(updated.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionsEvaluatedConditionType)
		if assert.NotNil(t, evaluated) {
			assert.Equal(t, metav1.ConditionTrue, evaluated.Status)
			assert.Equal(t, metav1.NewTime(started), evaluated.LastTransitionTime)
		}
		assert.Equal(t, discovered, updated.Status.TopologyTransitionStatus.Transitions)

		_, operatorStatus, _, err := c.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionTrue(operatorStatus.Conditions, upgradeableCondition))
		assert.True(t, v1helpers.IsOperatorConditionFalse(operatorStatus.Conditions, transitionProgressingCondition))
	})

	t.Run("recovery with unsatisfied validators stays blocked", func(t *testing.T) {
		// The committed marker recovers the descriptor, but its validators
		// have not converged: completion must be withheld even though the
		// operator conditions were lost.
		validatorCalls := 0
		transitions := noopTransitionsWithValidators(func() error { validatorCalls++; return fmt.Errorf("ingress not scaled up") })
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
		infra.Status.TopologyTransitionStatus = configv1.TopologyTransitionStatus{
			Conditions: []metav1.Condition{
				{Type: configv1.TopologyTransitionCompletedConditionType, Status: metav1.ConditionFalse, Reason: "SNOToHACompact", LastTransitionTime: metav1.NewTime(evaluationTime.Add(-2 * minReconciliationSoakTime))},
			},
		}
		c, _, _ := statusTestController(infra)
		c.transitions = transitions

		assert.NoError(t, c.sync(context.TODO(), newTestSyncContext()))

		assert.Equal(t, 1, validatorCalls, "recovery must run the committed transition's validators")
		completion := meta.FindStatusCondition(currentInfra(t, c).Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionCompletedConditionType)
		if assert.NotNil(t, completion) {
			assert.Equal(t, metav1.ConditionFalse, completion.Status, "unsatisfied validators must block completion")
		}
		_, operatorStatus, _, err := c.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.False(t, v1helpers.IsOperatorConditionTrue(operatorStatus.Conditions, transitionProgressingCondition), "no completion condition may be written while validators fail")
	})

	t.Run("completed=false resumes reconciliation after operator conditions are lost", func(t *testing.T) {
		// Crash recovery: the topology status was committed (spec equals
		// status) and the operator conditions were lost, but
		// TopologyTransitionCompleted=False remains on the Infrastructure CR.
		// Per the contract, spec/status equality must not be treated as
		// completion: sync must resume post-transition reconciliation, and
		// the committed marker's transition name must recover the descriptor
		// so its validators actually run.
		validatorCalls := 0
		transitions := noopTransitionsWithValidators(func() error { validatorCalls++; return nil })
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
		infra.Status.TopologyTransitionStatus = configv1.TopologyTransitionStatus{
			Conditions: []metav1.Condition{
				{Type: configv1.TopologyTransitionCompletedConditionType, Status: metav1.ConditionFalse, Reason: "SNOToHACompact", LastTransitionTime: metav1.NewTime(evaluationTime.Add(-2 * minReconciliationSoakTime))},
			},
		}
		c, _, _ := statusTestController(infra)
		c.transitions = transitions

		assert.NoError(t, c.sync(context.TODO(), newTestSyncContext()))

		assert.Equal(t, 1, validatorCalls, "recovery must run the committed transition's validators")
		updated := currentInfra(t, c)
		completion := meta.FindStatusCondition(updated.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionCompletedConditionType)
		if assert.NotNil(t, completion) {
			assert.Equal(t, metav1.ConditionTrue, completion.Status, "recovery must complete the transition")
		}
		_, operatorStatus, _, err := c.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionTrue(operatorStatus.Conditions, upgradeableCondition))
	})
}

// TestWithdrawnTransitionUnwindsGating pins the cancellation contract: when
// the gating conditions were set but the status commit never landed (the
// request was withdrawn before commit), sync must unwind the conditions to
// idle instead of running the never-applied transition's validators, which
// would wedge Upgradeable=False forever.
func TestWithdrawnTransitionUnwindsGating(t *testing.T) {
	t.Run("withdrawn before commit clears conditions without running validators", func(t *testing.T) {
		validatorCalled := false
		// The validator would never pass on a cluster where the transition
		// did not happen (e.g. ingress still at one replica): if it ran, the
		// conditions would stay blocked.
		transitions := noopTransitionsWithValidators(
			func() error { validatorCalled = true; return fmt.Errorf("ingress not scaled up") },
		)

		infra := newTestInfra(configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.SingleReplicaTopologyMode, configv1.NonePlatformType)
		ctrl := newTestController(infra, transitionInProgressConditions("SNOToHACompact"), nil, transitions)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		assert.False(t, validatorCalled, "validators must not run for a withdrawn transition")

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		progressingCond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, progressingCond) {
			return
		}
		assert.Equal(t, operatorv1.ConditionFalse, progressingCond.Status)
		assert.Equal(t, "AsExpected", progressingCond.Reason)
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition))
	})

	t.Run("already-completed transition only redoes the condition clear", func(t *testing.T) {
		validatorCalled := false
		transitions := noopTransitionsWithValidators(
			func() error { validatorCalled = true; return nil },
		)

		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
		setTopologyTransitionCompleted(infra, metav1.ConditionTrue, "TopologyTransitionComplete", "Topology transition complete", time.Now())

		ctrl := newTestController(infra, transitionInProgressConditions("SNOToHACompact"), nil, transitions)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		assert.False(t, validatorCalled, "validators must not re-run after completion is recorded")

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		progressingCond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, progressingCond) {
			return
		}
		assert.Equal(t, operatorv1.ConditionFalse, progressingCond.Status)
		assert.Equal(t, "TopologyTransitionComplete", progressingCond.Reason)
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition))
	})
}

// TestReconciliationSoakAnchorsOnCommitTimestamp pins the proposal's soak
// contract: the 5-minute soak is anchored on the status commit timestamp —
// the TopologyTransitionCompleted=False condition written atomically with the
// commit — not on the operator conditions, which are set before the commit
// and can predate it by failed commit attempts.
func TestReconciliationSoakAnchorsOnCommitTimestamp(t *testing.T) {
	t.Run("stale conditions do not shortcut the soak of a fresh commit", func(t *testing.T) {
		// The gating conditions were set 10 minutes ago, but the commit only
		// landed now (e.g. the status write failed and was retried). The soak
		// must run from the commit, not from the conditions.
		now := time.Now()
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
		clk := clocktesting.NewFakePassiveClock(now)
		ctrl := newTestControllerWithClock(
			markTransitionCommittedAt(infra, "SNOToHACompact", now),
			transitionInProgressConditionsAt(now.Add(-10*time.Minute), "SNOToHACompact"),
			nil, noopTransitions(), clk,
		)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, err := ctrl.operatorClient.GetOperatorState()
		if !assert.NoError(t, err) {
			return
		}
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, transitionProgressingCondition), "within soak: conditions must be preserved")
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))
	})

	t.Run("commit timestamp older than the conditions still satisfies the soak", func(t *testing.T) {
		// The conditions were re-set more recently than the commit (e.g. a
		// condition flap); the commit timestamp is authoritative, so the
		// elapsed soak is measured from the commit.
		now := time.Now()
		infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
		clk := clocktesting.NewFakePassiveClock(now)
		ctrl := newTestControllerWithClock(
			markTransitionCommittedAt(infra, "SNOToHACompact", now.Add(-2*minReconciliationSoakTime)),
			transitionInProgressConditionsAt(now, "SNOToHACompact"),
			nil, noopTransitions(), clk,
		)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		updated, err := ctrl.infraClient.Get(context.TODO(), "cluster", metav1.GetOptions{})
		if !assert.NoError(t, err) {
			return
		}
		completion := meta.FindStatusCondition(updated.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionCompletedConditionType)
		if assert.NotNil(t, completion) {
			assert.Equal(t, metav1.ConditionTrue, completion.Status, "past the commit-anchored soak: transition must complete")
		}
	})
}

// committedMessage is the TopologyTransitionCompleted=False message written
// atomically with the topology status commit.
const committedMessage = "Topology transition committed; post-transition reconciliation in progress"

// committedTransitionController returns a controller whose SNOToHACompact
// transition was committed at committedAt and is reconciled with the given
// post-transition checks. Operator conditions gate upgrades, and the
// Infrastructure carries a discovery condition that writes to the completion
// condition must preserve.
func committedTransitionController(committedAt time.Time, checks ...PostTransitionCheck) (*TopologyTransitionController, *configfakeclient.Clientset) {
	return committedTransitionControllerWithReason(committedAt, "SNOToHACompact", checks...)
}

// committedTransitionControllerWithReason is committedTransitionController
// with an explicit TopologyTransitionCompleted Reason, for markers whose
// Reason does not name the transition and that are resolved through the
// operator conditions instead.
func committedTransitionControllerWithReason(committedAt time.Time, reason string, checks ...PostTransitionCheck) (*TopologyTransitionController, *configfakeclient.Clientset) {
	infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
	infra.Status.TopologyTransitionStatus = configv1.TopologyTransitionStatus{
		Conditions: []metav1.Condition{
			{Type: configv1.TopologyTransitionsEvaluatedConditionType, Status: metav1.ConditionTrue, Reason: "EvaluationComplete", Message: "Supported topology transitions evaluated", LastTransitionTime: metav1.NewTime(committedAt)},
			{Type: configv1.TopologyTransitionCompletedConditionType, Status: metav1.ConditionFalse, Reason: reason, Message: committedMessage, LastTransitionTime: metav1.NewTime(committedAt)},
		},
	}
	c, client, _ := statusTestController(infra)
	c.transitions = noopTransitionsWithChecks(checks...)
	c.operatorClient = v1helpers.NewFakeOperatorClient(&operatorv1.OperatorSpec{}, &operatorv1.OperatorStatus{
		Conditions: transitionInProgressConditionsAt(committedAt, "SNOToHACompact"),
	}, nil)
	return c, client
}

// completedCondition returns the live TopologyTransitionCompleted condition
// and fails the test if it is missing.
func completedCondition(t *testing.T, c *TopologyTransitionController) *metav1.Condition {
	t.Helper()
	condition := meta.FindStatusCondition(currentInfra(t, c).Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionCompletedConditionType)
	if condition == nil {
		t.Fatal("TopologyTransitionCompleted condition is missing")
	}
	return condition
}

// TestPostTransitionFailuresPublishedInCompletedMessage pins how failing
// post-transition checks are reported on the Infrastructure CR: every failing
// check is named in the TopologyTransitionCompleted=False message, unchanged
// failures are not rewritten, the message follows the current failures, and
// completion replaces it. Throughout, the Reason keeps identifying the
// committed transition, the soak anchor does not move, and the upgrade gating
// conditions are left alone until every check passes.
func TestPostTransitionFailuresPublishedInCompletedMessage(t *testing.T) {
	committedAt := evaluationTime.Add(-2 * minReconciliationSoakTime)
	bravoErr := errors.New("insufficient bravo replicas: need 2, have 1")
	charlieErr := errors.New("no rendered charlie config found")
	var deltaErr error
	deltaCalls := 0
	c, client := committedTransitionController(committedAt,
		PostTransitionCheck{Name: "AlphaReady", Validate: func() error { return nil }},
		PostTransitionCheck{Name: "BravoReady", Validate: func() error { return bravoErr }},
		PostTransitionCheck{Name: "CharlieReady", Validate: func() error { return charlieErr }},
		PostTransitionCheck{Name: "DeltaReady", Validate: func() error { deltaCalls++; return deltaErr }},
	)
	_, operatorStatus, _, err := c.operatorClient.GetOperatorState()
	if !assert.NoError(t, err) {
		return
	}
	gating := append([]operatorv1.OperatorCondition(nil), operatorStatus.Conditions...)
	evaluated := *meta.FindStatusCondition(currentInfra(t, c).Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionsEvaluatedConditionType)

	assertStillGated := func(t *testing.T) {
		t.Helper()
		completion := completedCondition(t, c)
		assert.Equal(t, metav1.ConditionFalse, completion.Status, "failing checks must block completion")
		assert.Equal(t, "SNOToHACompact", completion.Reason, "the Reason must keep identifying the committed transition")
		assert.Equal(t, metav1.NewTime(committedAt), completion.LastTransitionTime, "message updates must not move the soak anchor")
		assert.Equal(t, &evaluated, meta.FindStatusCondition(currentInfra(t, c).Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionsEvaluatedConditionType), "the discovery report must be preserved")
		_, operatorStatus, _, err := c.operatorClient.GetOperatorState()
		if assert.NoError(t, err) {
			assert.Equal(t, gating, operatorStatus.Conditions, "failing checks must leave the upgrade gating conditions untouched")
		}
	}

	// The steps below build on each other's state, so they run in sequence
	// rather than as independent subtests. The informer cache keeps the
	// commit-time message throughout, so every step also exercises the fresh
	// read behind the cache.

	// Every failing check is named, in registration order, and checks after a
	// failing check still run.
	assert.NoError(t, c.sync(context.TODO(), newTestSyncContext()))
	assert.Equal(t,
		"Topology transition committed; waiting for 2 of 4 post-transition checks: BravoReady: insufficient bravo replicas: need 2, have 1; CharlieReady: no rendered charlie config found. The transition completes automatically when all checks pass.",
		completedCondition(t, c).Message)
	assert.Equal(t, 1, deltaCalls, "checks after a failing check must still run")
	assert.Equal(t, 1, statusWrites(client))
	assertStillGated(t)

	// Unchanged failures do not rewrite status.
	assert.NoError(t, c.sync(context.TODO(), newTestSyncContext()))
	assert.Equal(t, 1, statusWrites(client), "unchanged failures must not rewrite status")
	assertStillGated(t)

	// The message follows the current failures: a recovered check drops out,
	// a changed error is updated, and a new failure is added.
	bravoErr = nil
	charlieErr = errors.New("rendered charlie config predates the transition")
	deltaErr = errors.New("insufficient delta replicas: need 3, have 2")
	assert.NoError(t, c.sync(context.TODO(), newTestSyncContext()))
	message := completedCondition(t, c).Message
	assert.Equal(t,
		"Topology transition committed; waiting for 2 of 4 post-transition checks: CharlieReady: rendered charlie config predates the transition; DeltaReady: insufficient delta replicas: need 3, have 2. The transition completes automatically when all checks pass.",
		message)
	assert.NotContains(t, message, "BravoReady", "a recovered check must not linger in the message")
	assert.Equal(t, 2, statusWrites(client))
	assertStillGated(t)

	// Completion replaces the published failures and releases the gating.
	charlieErr = nil
	deltaErr = nil
	assert.NoError(t, c.sync(context.TODO(), newTestSyncContext()))
	completion := completedCondition(t, c)
	assert.Equal(t, metav1.ConditionTrue, completion.Status)
	assert.Equal(t, reasonTopologyTransitionComplete, completion.Reason)
	assert.Equal(t, "Topology transition complete", completion.Message)
	assert.Equal(t, &evaluated, meta.FindStatusCondition(currentInfra(t, c).Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionsEvaluatedConditionType))
	_, operatorStatus, _, err = c.operatorClient.GetOperatorState()
	if assert.NoError(t, err) {
		assert.True(t, v1helpers.IsOperatorConditionFalse(operatorStatus.Conditions, transitionProgressingCondition))
		assert.True(t, v1helpers.IsOperatorConditionTrue(operatorStatus.Conditions, upgradeableCondition))
	}
}

// TestPostTransitionFailuresKeepUnnamedCompletedReason checks that the
// Completed Reason is preserved verbatim when it does not name the
// transition and the descriptor was resolved through the operator
// conditions.
func TestPostTransitionFailuresKeepUnnamedCompletedReason(t *testing.T) {
	committedAt := evaluationTime.Add(-2 * minReconciliationSoakTime)
	c, _ := committedTransitionControllerWithReason(committedAt, "TransitionInProgress",
		PostTransitionCheck{Name: "AlphaReady", Validate: func() error { return errors.New("alpha not ready") }},
	)

	assert.NoError(t, c.sync(context.TODO(), newTestSyncContext()))

	completion := completedCondition(t, c)
	assert.Equal(t, metav1.ConditionFalse, completion.Status)
	assert.Equal(t, "TransitionInProgress", completion.Reason)
	assert.Equal(t, metav1.NewTime(committedAt), completion.LastTransitionTime)
	assert.Equal(t,
		"Topology transition committed; waiting for 1 of 1 post-transition checks: AlphaReady: alpha not ready. The transition completes automatically when all checks pass.",
		completion.Message)
}

// TestPostTransitionMessageUnchangedDuringSoak checks that no check runs and
// no failure is published before the reconciliation soak has elapsed.
func TestPostTransitionMessageUnchangedDuringSoak(t *testing.T) {
	calls := 0
	c, client := committedTransitionController(evaluationTime.Add(-minReconciliationSoakTime/2),
		PostTransitionCheck{Name: "AlphaReady", Validate: func() error { calls++; return errors.New("not ready") }},
	)

	assert.NoError(t, c.sync(context.TODO(), newTestSyncContext()))

	assert.Zero(t, calls)
	assert.Equal(t, committedMessage, completedCondition(t, c).Message)
	assert.Zero(t, statusWrites(client))
}

// TestPostTransitionMessageIsCappedAtConditionLimit checks that an oversized
// check error is truncated so the message fits the API condition message
// limit, without splitting UTF-8 characters and without hiding the other
// failing checks or the closing hint.
func TestPostTransitionMessageIsCappedAtConditionLimit(t *testing.T) {
	verbose := strings.Repeat("é", maxEvaluationMessageCharacters)
	c, _ := committedTransitionController(evaluationTime.Add(-2*minReconciliationSoakTime),
		PostTransitionCheck{Name: "VerboseCheck", Validate: func() error { return errors.New(verbose) }},
		PostTransitionCheck{Name: "LaterCheck", Validate: func() error { return errors.New("later failure") }},
	)

	assert.NoError(t, c.sync(context.TODO(), newTestSyncContext()))

	message := completedCondition(t, c).Message
	assert.LessOrEqual(t, utf8.RuneCountInString(message), maxEvaluationMessageCharacters)
	assert.True(t, utf8.ValidString(message))
	assert.True(t, strings.HasPrefix(message, "Topology transition committed; waiting for 2 of 2 post-transition checks: VerboseCheck: éé"))
	assert.Contains(t, message, "é…; LaterCheck: later failure", "the oversized error is cut, and the next failing check is still named")
	assert.True(t, strings.HasSuffix(message, postTransitionWaitingHint), "the closing hint must survive truncation")
}

// TestTruncateWithEllipsis covers the character-safe truncation used to fit
// failing check entries into the condition message.
func TestTruncateWithEllipsis(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		limit int
		want  string
	}{
		{name: "shorter than the limit is unchanged", input: "abc", limit: 5, want: "abc"},
		{name: "exactly the limit is unchanged", input: "abcde", limit: 5, want: "abcde"},
		{name: "longer than the limit ends with an ellipsis", input: "abcdef", limit: 5, want: "abcd…"},
		{name: "multi-byte characters are not split", input: "éééééé", limit: 3, want: "éé…"},
		{name: "a limit of one leaves only the ellipsis", input: "abc", limit: 1, want: "…"},
		{name: "a non-positive limit leaves nothing", input: "abc", limit: 0, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateWithEllipsis(tc.input, tc.limit)
			assert.Equal(t, tc.want, got)
			assert.LessOrEqual(t, utf8.RuneCountInString(got), max(tc.limit, 0))
		})
	}
}
