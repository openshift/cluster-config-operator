package topology_transition_controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func snoInfra(specTopology configv1.TopologyMode) *configv1.Infrastructure {
	return newTestInfra(
		specTopology,
		configv1.SingleReplicaTopologyMode,
		configv1.SingleReplicaTopologyMode,
		configv1.NonePlatformType,
	)
}

func readyFixture() *testFixture {
	return newTestFixture().
		withNodes(
			newTestControlPlaneNodeWithConditions("master-0", false, readyNodeCondition()),
			newTestControlPlaneNodeWithConditions("master-1", false, readyNodeCondition()),
			newTestControlPlaneNodeWithConditions("master-2", false, readyNodeCondition()),
		).
		withEtcdEndpoints(3).
		withEtcdCR(true, false).
		withClusterOperators(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			newTestClusterOperator("kube-apiserver", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			newTestClusterOperator("monitoring", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
		)
}

func TestSNOToHACompact(t *testing.T) {
	t.Run("all preflights pass", func(t *testing.T) {
		ctrl := readyFixture().newController(snoInfra(configv1.HighlyAvailableTopologyMode), nil)

		if !assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext())) {
			return
		}

		updated, err := ctrl.infraClient.Get(context.TODO(), "cluster", metav1.GetOptions{})
		if !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, configv1.HighlyAvailableTopologyMode, updated.Status.ControlPlaneTopology)
		assert.Equal(t, configv1.HighlyAvailableTopologyMode, updated.Status.InfrastructureTopology)

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, transitionProgressingCondition))
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))
	})

	t.Run("insufficient control plane nodes", func(t *testing.T) {
		fixture := newTestFixture().
			withNodes(newTestControlPlaneNode("master-0", false)).
			withEtcdEndpoints(1).
			withEtcdCR(true, false)

		ctrl := fixture.newController(snoInfra(configv1.HighlyAvailableTopologyMode), nil)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		cond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, operatorv1.ConditionFalse, cond.Status)
		assert.Equal(t, "PreflightCheckFailed", cond.Reason)
		assert.Contains(t, cond.Message, "insufficient control plane nodes: need 3, have 1")
	})

	t.Run("worker nodes present", func(t *testing.T) {
		fixture := newTestFixture().
			withNodes(
				newTestControlPlaneNodeWithConditions("master-0", false, readyNodeCondition()),
				newTestControlPlaneNodeWithConditions("master-1", false, readyNodeCondition()),
				newTestControlPlaneNodeWithConditions("master-2", false, readyNodeCondition()),
				newTestWorkerNode("worker-0"),
			).
			withEtcdEndpoints(3).
			withEtcdCR(true, false)

		ctrl := fixture.newController(snoInfra(configv1.HighlyAvailableTopologyMode), nil)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		cond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, operatorv1.ConditionFalse, cond.Status)
		assert.Equal(t, "PreflightCheckFailed", cond.Reason)
		assert.Contains(t, cond.Message, "unexpected infrastructure node count: expected 0 dedicated workers, have 1")
	})

	t.Run("compact HA with dual-role nodes passes", func(t *testing.T) {
		fixture := newTestFixture().
			withNodes(
				newTestDualRoleNodeWithConditions("master-0", false, readyNodeCondition()),
				newTestDualRoleNodeWithConditions("master-1", false, readyNodeCondition()),
				newTestDualRoleNodeWithConditions("master-2", false, readyNodeCondition()),
			).
			withEtcdEndpoints(3).
			withEtcdCR(true, false)

		ctrl := fixture.newController(snoInfra(configv1.HighlyAvailableTopologyMode), nil)

		if !assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext())) {
			return
		}

		updated, err := ctrl.infraClient.Get(context.TODO(), "cluster", metav1.GetOptions{})
		if !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, configv1.HighlyAvailableTopologyMode, updated.Status.ControlPlaneTopology)
	})

	t.Run("control plane node unschedulable", func(t *testing.T) {
		fixture := newTestFixture().
			withNodes(
				newTestControlPlaneNodeWithConditions("master-0", false, readyNodeCondition()),
				newTestControlPlaneNodeWithConditions("master-1", false, readyNodeCondition()),
				newTestControlPlaneNodeWithConditions("master-2", true, readyNodeCondition()),
			).
			withEtcdEndpoints(3).
			withEtcdCR(true, false)

		ctrl := fixture.newController(snoInfra(configv1.HighlyAvailableTopologyMode), nil)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		cond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, "PreflightCheckFailed", cond.Reason)
		assert.Contains(t, cond.Message, "insufficient schedulable control plane nodes: need 3, have 2")
	})

	t.Run("control plane node not ready", func(t *testing.T) {
		fixture := newTestFixture().
			withNodes(
				newTestControlPlaneNodeWithConditions("master-0", false, readyNodeCondition()),
				newTestControlPlaneNodeWithConditions("master-1", false, readyNodeCondition()),
				newTestControlPlaneNodeWithConditions("master-2", false, notReadyNodeCondition()),
			).
			withEtcdEndpoints(3).
			withEtcdCR(true, false)

		ctrl := fixture.newController(snoInfra(configv1.HighlyAvailableTopologyMode), nil)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		cond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, "PreflightCheckFailed", cond.Reason)
		assert.Contains(t, cond.Message, "insufficient ready control plane nodes: need 3, have 2")
	})

	t.Run("etcd no quorum", func(t *testing.T) {
		fixture := newTestFixture().
			withNodes(
				newTestControlPlaneNodeWithConditions("master-0", false, readyNodeCondition()),
				newTestControlPlaneNodeWithConditions("master-1", false, readyNodeCondition()),
				newTestControlPlaneNodeWithConditions("master-2", false, readyNodeCondition()),
			).
			withEtcdEndpoints(3).
			withEtcdCR(false, false)

		ctrl := fixture.newController(snoInfra(configv1.HighlyAvailableTopologyMode), nil)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		cond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, "PreflightCheckFailed", cond.Reason)
		assert.Contains(t, cond.Message, "etcd does not have quorum")
	})

	t.Run("etcd progressing", func(t *testing.T) {
		fixture := newTestFixture().
			withNodes(
				newTestControlPlaneNodeWithConditions("master-0", false, readyNodeCondition()),
				newTestControlPlaneNodeWithConditions("master-1", false, readyNodeCondition()),
				newTestControlPlaneNodeWithConditions("master-2", false, readyNodeCondition()),
			).
			withEtcdEndpoints(3).
			withEtcdCR(true, true)

		ctrl := fixture.newController(snoInfra(configv1.HighlyAvailableTopologyMode), nil)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		cond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, "PreflightCheckFailed", cond.Reason)
		assert.Contains(t, cond.Message, "etcd is still progressing")
	})

	t.Run("insufficient etcd voting members", func(t *testing.T) {
		fixture := newTestFixture().
			withNodes(
				newTestControlPlaneNodeWithConditions("master-0", false, readyNodeCondition()),
				newTestControlPlaneNodeWithConditions("master-1", false, readyNodeCondition()),
				newTestControlPlaneNodeWithConditions("master-2", false, readyNodeCondition()),
			).
			withEtcdEndpoints(1).
			withEtcdCR(true, false)

		ctrl := fixture.newController(snoInfra(configv1.HighlyAvailableTopologyMode), nil)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		cond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, "PreflightCheckFailed", cond.Reason)
		assert.Contains(t, cond.Message, "insufficient etcd voting members: need 3, have 1")
	})

	t.Run("cluster operators unstable", func(t *testing.T) {
		fixture := newTestFixture().
			withNodes(
				newTestControlPlaneNodeWithConditions("master-0", false, readyNodeCondition()),
				newTestControlPlaneNodeWithConditions("master-1", false, readyNodeCondition()),
				newTestControlPlaneNodeWithConditions("master-2", false, readyNodeCondition()),
			).
			withEtcdEndpoints(3).
			withEtcdCR(true, false).
			withClusterOperators(
				newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
				newTestClusterOperator("kube-apiserver", configv1.ConditionTrue, configv1.ConditionTrue, configv1.ConditionFalse),
			)

		ctrl := fixture.newController(snoInfra(configv1.HighlyAvailableTopologyMode), nil)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		cond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, operatorv1.ConditionFalse, cond.Status)
		assert.Equal(t, "PreflightCheckFailed", cond.Reason)
		assert.Contains(t, cond.Message, "kube-apiserver")
		assert.Contains(t, cond.Message, "Progressing=True")
	})

	t.Run("wrong platform", func(t *testing.T) {
		fixture := readyFixture()
		infra := newTestInfra(
			configv1.HighlyAvailableTopologyMode,
			configv1.SingleReplicaTopologyMode,
			configv1.SingleReplicaTopologyMode,
			configv1.AWSPlatformType,
		)
		ctrl := fixture.newController(infra, nil)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		cond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, operatorv1.ConditionFalse, cond.Status)
		assert.Equal(t, "UnsupportedTransition", cond.Reason)
		assert.Contains(t, cond.Message, "is not supported")
		assert.Contains(t, cond.Message, "platform=AWS")
	})
}
