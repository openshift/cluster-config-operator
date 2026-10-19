package topology_transition_controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	corev1 "k8s.io/api/core/v1"
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
			newTestDualRoleNodeWithConditions("master-0", false, readyNodeCondition()),
			newTestDualRoleNodeWithConditions("master-1", false, readyNodeCondition()),
			newTestDualRoleNodeWithConditions("master-2", false, readyNodeCondition()),
		).
		withEtcdEndpoints(3).
		withEtcdCR(true, false).
		withClusterOperators(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			newTestClusterOperator("kube-apiserver", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			newTestClusterOperator("monitoring", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
		)
}

func infraSRToHAInfra(specInfraTopology configv1.TopologyMode) *configv1.Infrastructure {
	return newTestInfraWithInfraSpec(
		specInfraTopology,
		configv1.HighlyAvailableTopologyMode,
		configv1.SingleReplicaTopologyMode,
		"",
	)
}

func infraReadyFixture() *testFixture {
	return newTestFixture().
		withNodes(
			newTestWorkerNodeWithConditions("worker-0", readyNodeCondition()),
			newTestWorkerNodeWithConditions("worker-1", readyNodeCondition()),
		).
		withClusterOperators(
			newTestClusterOperator("ingress", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			newTestClusterOperator("monitoring", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
		).
		withIngressController(newTestIngressController("default", 2)).
		withImageRegistryDeployment(2).
		withConsoleDeployment(2).
		withOAuthDeployment(2).
		withMonitoringStatefulSet("prometheus-k8s", 2).
		withMonitoringStatefulSet("alertmanager-main", 2)
}

func TestInfrastructureSRToHA(t *testing.T) {
	t.Run("all preflights pass", func(t *testing.T) {
		ctrl := infraReadyFixture().newController(infraSRToHAInfra(configv1.HighlyAvailableTopologyMode), nil)

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

	t.Run("does not modify control plane topology", func(t *testing.T) {
		ctrl := infraReadyFixture().newController(infraSRToHAInfra(configv1.HighlyAvailableTopologyMode), nil)

		if !assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext())) {
			return
		}

		updated, err := ctrl.infraClient.Get(context.TODO(), "cluster", metav1.GetOptions{})
		if !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, configv1.HighlyAvailableTopologyMode, updated.Status.ControlPlaneTopology)
	})

	t.Run("insufficient worker-capable nodes", func(t *testing.T) {
		fixture := newTestFixture().
			withNodes(
				newTestWorkerNodeWithConditions("worker-0", readyNodeCondition()),
			)

		ctrl := fixture.newController(infraSRToHAInfra(configv1.HighlyAvailableTopologyMode), nil)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		cond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, operatorv1.ConditionFalse, cond.Status)
		assert.Equal(t, "PreflightCheckFailed", cond.Reason)
		assert.Contains(t, cond.Message, "insufficient worker-capable nodes")
	})

	t.Run("worker node not ready", func(t *testing.T) {
		fixture := newTestFixture().
			withNodes(
				newTestWorkerNodeWithConditions("worker-0", readyNodeCondition()),
				newTestWorkerNodeWithConditions("worker-1", notReadyNodeCondition()),
			)

		ctrl := fixture.newController(infraSRToHAInfra(configv1.HighlyAvailableTopologyMode), nil)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		cond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, operatorv1.ConditionFalse, cond.Status)
		assert.Equal(t, "PreflightCheckFailed", cond.Reason)
		assert.Contains(t, cond.Message, "insufficient ready worker-capable nodes")
	})

	t.Run("worker node unschedulable", func(t *testing.T) {
		fixture := newTestFixture().
			withNodes(
				newTestWorkerNodeWithConditions("worker-0", readyNodeCondition()),
				newWorkerNodeUnschedulable("worker-1"),
			)

		ctrl := fixture.newController(infraSRToHAInfra(configv1.HighlyAvailableTopologyMode), nil)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		cond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, operatorv1.ConditionFalse, cond.Status)
		assert.Equal(t, "PreflightCheckFailed", cond.Reason)
		assert.Contains(t, cond.Message, "insufficient schedulable worker-capable nodes")
	})

	t.Run("rejected when control plane is SingleReplica", func(t *testing.T) {
		infra := newTestInfraWithInfraSpec(
			configv1.HighlyAvailableTopologyMode,
			configv1.SingleReplicaTopologyMode,
			configv1.SingleReplicaTopologyMode,
			"",
		)

		ctrl := infraReadyFixture().newController(infra, nil)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		cond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, operatorv1.ConditionFalse, cond.Status)
		assert.Equal(t, "UnsupportedTransition", cond.Reason)
	})

	t.Run("dual-role nodes count as worker-capable", func(t *testing.T) {
		fixture := newTestFixture().
			withNodes(
				newTestDualRoleNodeWithConditions("master-0", false, readyNodeCondition()),
				newTestDualRoleNodeWithConditions("master-1", false, readyNodeCondition()),
				newTestDualRoleNodeWithConditions("master-2", false, readyNodeCondition()),
			)

		ctrl := fixture.newController(infraSRToHAInfra(configv1.HighlyAvailableTopologyMode), nil)

		if !assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext())) {
			return
		}

		updated, err := ctrl.infraClient.Get(context.TODO(), "cluster", metav1.GetOptions{})
		if !assert.NoError(t, err) {
			return
		}
		assert.Equal(t, configv1.HighlyAvailableTopologyMode, updated.Status.InfrastructureTopology)
	})

	t.Run("cluster operators unstable", func(t *testing.T) {
		fixture := newTestFixture().
			withNodes(
				newTestWorkerNodeWithConditions("worker-0", readyNodeCondition()),
				newTestWorkerNodeWithConditions("worker-1", readyNodeCondition()),
			).
			withClusterOperators(
				newTestClusterOperator("ingress", configv1.ConditionTrue, configv1.ConditionTrue, configv1.ConditionFalse),
			)

		ctrl := fixture.newController(infraSRToHAInfra(configv1.HighlyAvailableTopologyMode), nil)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		cond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, operatorv1.ConditionFalse, cond.Status)
		assert.Equal(t, "PreflightCheckFailed", cond.Reason)
		assert.Contains(t, cond.Message, "ingress")
	})

	t.Run("worker node with blocking taint", func(t *testing.T) {
		taintedNode := newTestWorkerNodeWithConditions("worker-1", readyNodeCondition())
		taintedNode.Spec.Taints = []corev1.Taint{
			{Key: "custom/dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
		}
		fixture := newTestFixture().
			withNodes(
				newTestWorkerNodeWithConditions("worker-0", readyNodeCondition()),
				taintedNode,
			)

		ctrl := fixture.newController(infraSRToHAInfra(configv1.HighlyAvailableTopologyMode), nil)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		cond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, operatorv1.ConditionFalse, cond.Status)
		assert.Equal(t, "PreflightCheckFailed", cond.Reason)
		assert.Contains(t, cond.Message, "insufficient untainted worker-capable nodes")
	})

	t.Run("cluster version not upgradeable", func(t *testing.T) {
		fixture := newTestFixture().
			withNodes(
				newTestWorkerNodeWithConditions("worker-0", readyNodeCondition()),
				newTestWorkerNodeWithConditions("worker-1", readyNodeCondition()),
			)
		cv := newTestClusterVersionWithUpgradeable(false, configv1.ConditionFalse)
		if err := fixture.cvIndexer.Update(cv); err != nil {
			t.Fatal(err)
		}

		ctrl := fixture.newController(infraSRToHAInfra(configv1.HighlyAvailableTopologyMode), nil)

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		cond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
		if !assert.NotNil(t, cond) {
			return
		}
		assert.Equal(t, operatorv1.ConditionFalse, cond.Status)
		assert.Equal(t, "PreflightCheckFailed", cond.Reason)
		assert.Contains(t, cond.Message, "cluster is not upgradeable")
	})

	t.Run("post-transition blocked by insufficient image-registry replicas", func(t *testing.T) {
		fixture := infraReadyFixture().withImageRegistryDeployment(1)
		infra := newTestInfraWithInfraSpec(
			configv1.HighlyAvailableTopologyMode,
			configv1.HighlyAvailableTopologyMode,
			configv1.HighlyAvailableTopologyMode,
			"",
		)
		ctrl := fixture.newController(infra, transitionInProgressConditions())

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, transitionProgressingCondition))
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))
	})

	t.Run("post-transition blocked by insufficient prometheus replicas", func(t *testing.T) {
		fixture := infraReadyFixture().withMonitoringStatefulSet("prometheus-k8s", 1)
		infra := newTestInfraWithInfraSpec(
			configv1.HighlyAvailableTopologyMode,
			configv1.HighlyAvailableTopologyMode,
			configv1.HighlyAvailableTopologyMode,
			"",
		)
		ctrl := fixture.newController(infra, transitionInProgressConditions())

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, transitionProgressingCondition))
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, upgradeableCondition))
	})

	t.Run("post-transition completes when all operators converged", func(t *testing.T) {
		fixture := infraReadyFixture()
		infra := newTestInfraWithInfraSpec(
			configv1.HighlyAvailableTopologyMode,
			configv1.HighlyAvailableTopologyMode,
			configv1.HighlyAvailableTopologyMode,
			"",
		)
		ctrl := fixture.newController(infra, transitionInProgressConditions())

		assert.NoError(t, ctrl.sync(context.TODO(), newTestSyncContext()))

		_, status, _, _ := ctrl.operatorClient.GetOperatorState()
		assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, transitionProgressingCondition))
		assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition))
	})
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
		assert.Equal(t, operatorv1.ConditionFalse, cond.Status)
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
		assert.Equal(t, operatorv1.ConditionFalse, cond.Status)
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
		assert.Equal(t, operatorv1.ConditionFalse, cond.Status)
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
		assert.Equal(t, operatorv1.ConditionFalse, cond.Status)
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
		assert.Equal(t, operatorv1.ConditionFalse, cond.Status)
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
