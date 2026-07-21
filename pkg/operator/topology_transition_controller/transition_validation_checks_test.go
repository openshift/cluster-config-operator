package topology_transition_controller

import (
	"testing"

	"github.com/stretchr/testify/assert"

	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidateClusterOperatorsStable(t *testing.T) {
	t.Run("passes when all operators stable", func(t *testing.T) {
		fixture := newTestFixture().withClusterOperators(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			newTestClusterOperator("kube-apiserver", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
		)
		v := validateClusterOperatorsStable(fixture.coLister)
		assert.NoError(t, v())
	})

	t.Run("passes when no operators exist", func(t *testing.T) {
		fixture := newTestFixture()
		v := validateClusterOperatorsStable(fixture.coLister)
		assert.NoError(t, v())
	})

	t.Run("fails when operator progressing", func(t *testing.T) {
		fixture := newTestFixture().withClusterOperators(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			newTestClusterOperator("kube-apiserver", configv1.ConditionTrue, configv1.ConditionTrue, configv1.ConditionFalse),
		)
		v := validateClusterOperatorsStable(fixture.coLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "kube-apiserver")
		assert.Contains(t, err.Error(), "Progressing=True")
	})

	t.Run("fails when operator degraded", func(t *testing.T) {
		fixture := newTestFixture().withClusterOperators(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			newTestClusterOperator("kube-apiserver", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionTrue),
		)
		v := validateClusterOperatorsStable(fixture.coLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "kube-apiserver")
		assert.Contains(t, err.Error(), "Degraded=True")
	})

	t.Run("fails when operator unavailable", func(t *testing.T) {
		fixture := newTestFixture().withClusterOperators(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			newTestClusterOperator("kube-apiserver", configv1.ConditionFalse, configv1.ConditionFalse, configv1.ConditionFalse),
		)
		v := validateClusterOperatorsStable(fixture.coLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "kube-apiserver")
		assert.Contains(t, err.Error(), "Available=False")
	})

	t.Run("fails when Available condition missing", func(t *testing.T) {
		fixture := newTestFixture().withClusterOperators(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			&configv1.ClusterOperator{
				ObjectMeta: metav1.ObjectMeta{Name: "new-operator"},
			},
		)
		v := validateClusterOperatorsStable(fixture.coLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "new-operator")
		assert.Contains(t, err.Error(), "Available condition missing")
	})

	t.Run("config-operator is excluded", func(t *testing.T) {
		fixture := newTestFixture().withClusterOperators(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			newTestClusterOperator("config-operator", configv1.ConditionTrue, configv1.ConditionTrue, configv1.ConditionTrue),
		)
		v := validateClusterOperatorsStable(fixture.coLister)
		assert.NoError(t, v())
	})

	t.Run("fails when Progressing condition is Unknown", func(t *testing.T) {
		fixture := newTestFixture().withClusterOperators(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			newTestClusterOperator("kube-apiserver", configv1.ConditionTrue, configv1.ConditionUnknown, configv1.ConditionFalse),
		)
		v := validateClusterOperatorsStable(fixture.coLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "kube-apiserver")
		assert.Contains(t, err.Error(), "Progressing=Unknown")
	})

	t.Run("fails when Degraded condition is Unknown", func(t *testing.T) {
		fixture := newTestFixture().withClusterOperators(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			newTestClusterOperator("kube-apiserver", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionUnknown),
		)
		v := validateClusterOperatorsStable(fixture.coLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "kube-apiserver")
		assert.Contains(t, err.Error(), "Degraded=Unknown")
	})

	t.Run("fails when Progressing condition missing", func(t *testing.T) {
		fixture := newTestFixture().withClusterOperators(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			&configv1.ClusterOperator{
				ObjectMeta: metav1.ObjectMeta{Name: "incomplete-operator"},
				Status: configv1.ClusterOperatorStatus{
					Conditions: []configv1.ClusterOperatorStatusCondition{
						{Type: configv1.OperatorAvailable, Status: configv1.ConditionTrue},
						{Type: configv1.OperatorDegraded, Status: configv1.ConditionFalse},
					},
				},
			},
		)
		v := validateClusterOperatorsStable(fixture.coLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "incomplete-operator")
		assert.Contains(t, err.Error(), "Progressing condition missing")
	})

	t.Run("fails when Degraded condition missing", func(t *testing.T) {
		fixture := newTestFixture().withClusterOperators(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			&configv1.ClusterOperator{
				ObjectMeta: metav1.ObjectMeta{Name: "incomplete-operator"},
				Status: configv1.ClusterOperatorStatus{
					Conditions: []configv1.ClusterOperatorStatusCondition{
						{Type: configv1.OperatorAvailable, Status: configv1.ConditionTrue},
						{Type: configv1.OperatorProgressing, Status: configv1.ConditionFalse},
					},
				},
			},
		)
		v := validateClusterOperatorsStable(fixture.coLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "incomplete-operator")
		assert.Contains(t, err.Error(), "Degraded condition missing")
	})

	t.Run("multiple unstable operators listed", func(t *testing.T) {
		fixture := newTestFixture().withClusterOperators(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionTrue, configv1.ConditionFalse),
			newTestClusterOperator("kube-apiserver", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionTrue),
			newTestClusterOperator("monitoring", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
		)
		v := validateClusterOperatorsStable(fixture.coLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "etcd")
		assert.Contains(t, err.Error(), "kube-apiserver")
		assert.NotContains(t, err.Error(), "monitoring")
	})
}

func TestValidateControlPlaneNodeCount(t *testing.T) {
	t.Run("passes when enough nodes", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestControlPlaneNode("master-0", false),
			newTestControlPlaneNode("master-1", false),
			newTestControlPlaneNode("master-2", false),
		)
		v := validateControlPlaneNodeCount(3, fixture.nodeLister)
		assert.NoError(t, v())
	})

	t.Run("passes when more than required", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestControlPlaneNode("master-0", false),
			newTestControlPlaneNode("master-1", false),
			newTestControlPlaneNode("master-2", false),
			newTestControlPlaneNode("master-3", false),
		)
		v := validateControlPlaneNodeCount(3, fixture.nodeLister)
		assert.NoError(t, v())
	})

	t.Run("fails when insufficient nodes", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestControlPlaneNode("master-0", false),
		)
		v := validateControlPlaneNodeCount(3, fixture.nodeLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "insufficient control plane nodes: need 3, have 1")
	})

	t.Run("fails when no nodes", func(t *testing.T) {
		fixture := newTestFixture()
		v := validateControlPlaneNodeCount(3, fixture.nodeLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "insufficient control plane nodes: need 3, have 0")
	})

	t.Run("counts legacy master-only nodes", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestLegacyMasterNode("master-0", false),
			newTestLegacyMasterNode("master-1", false),
			newTestLegacyMasterNode("master-2", false),
		)
		v := validateControlPlaneNodeCount(3, fixture.nodeLister)
		assert.NoError(t, v())
	})

	t.Run("counts mix of control-plane and legacy master nodes", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestControlPlaneNode("master-0", false),
			newTestLegacyMasterNode("master-1", false),
			newTestControlPlaneNode("master-2", false),
		)
		v := validateControlPlaneNodeCount(3, fixture.nodeLister)
		assert.NoError(t, v())
	})

	t.Run("does not count worker nodes", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestControlPlaneNode("master-0", false),
			newTestWorkerNode("worker-0"),
			newTestWorkerNode("worker-1"),
		)
		v := validateControlPlaneNodeCount(3, fixture.nodeLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "insufficient control plane nodes: need 3, have 1")
	})
}

func TestValidateExactInfrastructureNodeCount(t *testing.T) {
	t.Run("passes when count matches expected", func(t *testing.T) {
		fixture := newTestFixture()
		v := validateExactInfrastructureNodeCount(0, fixture.nodeLister)
		assert.NoError(t, v())
	})

	t.Run("fails when workers present and expected zero", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestWorkerNode("worker-0"),
		)
		v := validateExactInfrastructureNodeCount(0, fixture.nodeLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "unexpected infrastructure node count: expected 0 dedicated workers, have 1")
	})

	t.Run("fails when fewer than expected", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestWorkerNode("worker-0"),
		)
		v := validateExactInfrastructureNodeCount(3, fixture.nodeLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "unexpected infrastructure node count: expected 3 dedicated workers, have 1")
	})

	t.Run("does not count control plane nodes", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestControlPlaneNode("master-0", false),
			newTestControlPlaneNode("master-1", false),
		)
		v := validateExactInfrastructureNodeCount(0, fixture.nodeLister)
		assert.NoError(t, v())
	})

	t.Run("does not count legacy master nodes as dedicated workers", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestLegacyMasterNode("master-0", false),
			newTestLegacyMasterNode("master-1", false),
		)
		v := validateExactInfrastructureNodeCount(0, fixture.nodeLister)
		assert.NoError(t, v())
	})

	t.Run("dual-role nodes not counted as dedicated workers", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestDualRoleNode("master-0", false),
			newTestDualRoleNode("master-1", false),
			newTestDualRoleNode("master-2", false),
		)
		v := validateExactInfrastructureNodeCount(0, fixture.nodeLister)
		assert.NoError(t, v())
	})

	t.Run("dual-role nodes plus dedicated worker fails when expected zero", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestDualRoleNode("master-0", false),
			newTestDualRoleNode("master-1", false),
			newTestDualRoleNode("master-2", false),
			newTestWorkerNode("worker-0"),
		)
		v := validateExactInfrastructureNodeCount(0, fixture.nodeLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "unexpected infrastructure node count: expected 0 dedicated workers, have 1")
	})
}

func TestValidateControlPlaneNodesSchedulable(t *testing.T) {
	t.Run("passes when all schedulable", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestControlPlaneNode("master-0", false),
			newTestControlPlaneNode("master-1", false),
			newTestControlPlaneNode("master-2", false),
		)
		v := validateControlPlaneNodesSchedulable(3, fixture.nodeLister)
		assert.NoError(t, v())
	})

	t.Run("fails when one unschedulable", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestControlPlaneNode("master-0", false),
			newTestControlPlaneNode("master-1", false),
			newTestControlPlaneNode("master-2", true),
		)
		v := validateControlPlaneNodesSchedulable(3, fixture.nodeLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "insufficient schedulable control plane nodes: need 3, have 2")
	})

	t.Run("fails when all unschedulable", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestControlPlaneNode("master-0", true),
			newTestControlPlaneNode("master-1", true),
			newTestControlPlaneNode("master-2", true),
		)
		v := validateControlPlaneNodesSchedulable(3, fixture.nodeLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "insufficient schedulable control plane nodes: need 3, have 0")
	})

	t.Run("counts legacy master nodes as schedulable", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestControlPlaneNode("master-0", false),
			newTestLegacyMasterNode("master-1", false),
			newTestControlPlaneNode("master-2", false),
		)
		v := validateControlPlaneNodesSchedulable(3, fixture.nodeLister)
		assert.NoError(t, v())
	})
}

func TestValidateControlPlaneNodesReady(t *testing.T) {
	t.Run("passes when all nodes ready", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestControlPlaneNodeWithConditions("master-0", false, readyNodeCondition()),
			newTestControlPlaneNodeWithConditions("master-1", false, readyNodeCondition()),
			newTestControlPlaneNodeWithConditions("master-2", false, readyNodeCondition()),
		)
		v := validateControlPlaneNodesReady(3, fixture.nodeLister)
		assert.NoError(t, v())
	})

	t.Run("fails when one node not ready", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestControlPlaneNodeWithConditions("master-0", false, readyNodeCondition()),
			newTestControlPlaneNodeWithConditions("master-1", false, readyNodeCondition()),
			newTestControlPlaneNodeWithConditions("master-2", false, notReadyNodeCondition()),
		)
		v := validateControlPlaneNodesReady(3, fixture.nodeLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "insufficient ready control plane nodes: need 3, have 2")
	})

	t.Run("fails when node has no Ready condition", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestControlPlaneNodeWithConditions("master-0", false, readyNodeCondition()),
			newTestControlPlaneNodeWithConditions("master-1", false, readyNodeCondition()),
			newTestControlPlaneNode("master-2", false),
		)
		v := validateControlPlaneNodesReady(3, fixture.nodeLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "insufficient ready control plane nodes: need 3, have 2")
	})

	t.Run("counts ready legacy master nodes", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestControlPlaneNodeWithConditions("master-0", false, readyNodeCondition()),
			newTestLegacyMasterNodeWithConditions("master-1", false, readyNodeCondition()),
			newTestControlPlaneNodeWithConditions("master-2", false, readyNodeCondition()),
		)
		v := validateControlPlaneNodesReady(3, fixture.nodeLister)
		assert.NoError(t, v())
	})
}

func TestValidateWorkerNodesReady(t *testing.T) {
	t.Run("passes when enough dedicated worker nodes ready", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestWorkerNodeWithConditions("worker-0", readyNodeCondition()),
			newTestWorkerNodeWithConditions("worker-1", readyNodeCondition()),
		)
		v := validateWorkerNodesReady(2, fixture.nodeLister)
		assert.NoError(t, v())
	})

	t.Run("passes when enough dual-role nodes ready", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestDualRoleNodeWithConditions("master-0", false, readyNodeCondition()),
			newTestDualRoleNodeWithConditions("master-1", false, readyNodeCondition()),
			newTestDualRoleNodeWithConditions("master-2", false, readyNodeCondition()),
		)
		v := validateWorkerNodesReady(2, fixture.nodeLister)
		assert.NoError(t, v())
	})

	t.Run("fails when insufficient ready worker nodes", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestWorkerNodeWithConditions("worker-0", readyNodeCondition()),
			newTestWorkerNodeWithConditions("worker-1", notReadyNodeCondition()),
		)
		v := validateWorkerNodesReady(2, fixture.nodeLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "insufficient ready worker nodes: need 2, have 1")
	})

	t.Run("fails when no worker nodes exist", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestControlPlaneNodeWithConditions("master-0", false, readyNodeCondition()),
		)
		v := validateWorkerNodesReady(2, fixture.nodeLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "insufficient ready worker nodes: need 2, have 0")
	})

	t.Run("does not count control-plane-only nodes", func(t *testing.T) {
		fixture := newTestFixture().withNodes(
			newTestControlPlaneNodeWithConditions("master-0", false, readyNodeCondition()),
			newTestControlPlaneNodeWithConditions("master-1", false, readyNodeCondition()),
			newTestWorkerNodeWithConditions("worker-0", readyNodeCondition()),
		)
		v := validateWorkerNodesReady(2, fixture.nodeLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "insufficient ready worker nodes: need 2, have 1")
	})
}

func TestValidateEtcdNotProgressing(t *testing.T) {
	t.Run("passes when not progressing", func(t *testing.T) {
		fixture := newTestFixture().withEtcdCR(true, false)
		v := validateEtcdNotProgressing(fixture.etcdLister)
		assert.NoError(t, v())
	})

	t.Run("fails when progressing", func(t *testing.T) {
		fixture := newTestFixture().withEtcdCR(true, true)
		v := validateEtcdNotProgressing(fixture.etcdLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "etcd is still progressing")
	})

	t.Run("fails when etcd CR not found", func(t *testing.T) {
		fixture := newTestFixture()
		v := validateEtcdNotProgressing(fixture.etcdLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "failed to get etcd operator CR")
	})
}

func TestValidateEtcdVotingMembers(t *testing.T) {
	t.Run("passes when enough voting members", func(t *testing.T) {
		fixture := newTestFixture().withEtcdEndpoints(3)
		v := validateEtcdVotingMembers(3, fixture.cmLister)
		assert.NoError(t, v())
	})

	t.Run("passes when more than required", func(t *testing.T) {
		fixture := newTestFixture().withEtcdEndpoints(5)
		v := validateEtcdVotingMembers(3, fixture.cmLister)
		assert.NoError(t, v())
	})

	t.Run("fails when insufficient voting members", func(t *testing.T) {
		fixture := newTestFixture().withEtcdEndpoints(1)
		v := validateEtcdVotingMembers(3, fixture.cmLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "insufficient etcd voting members: need 3, have 1")
	})

	t.Run("fails when configmap not found", func(t *testing.T) {
		fixture := newTestFixture()
		v := validateEtcdVotingMembers(3, fixture.cmLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "failed to get openshift-etcd/etcd-endpoints ConfigMap")
	})
}

func TestValidateEtcdQuorum(t *testing.T) {
	t.Run("passes when quorum available", func(t *testing.T) {
		fixture := newTestFixture().withEtcdCR(true, false)
		v := validateEtcdQuorum(fixture.etcdLister)
		assert.NoError(t, v())
	})

	t.Run("fails when no quorum", func(t *testing.T) {
		fixture := newTestFixture().withEtcdCR(false, false)
		v := validateEtcdQuorum(fixture.etcdLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "etcd does not have quorum")
	})

	t.Run("fails when etcd CR not found", func(t *testing.T) {
		fixture := newTestFixture()
		v := validateEtcdQuorum(fixture.etcdLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "failed to get etcd operator CR")
	})
}

func TestValidateMachineConfigNotPresent(t *testing.T) {
	t.Run("passes when MachineConfig absent", func(t *testing.T) {
		fixture := newTestFixture()
		v := validateMachineConfigNotPresent("50-master-dnsmasq-configuration", fixture.mcLister)
		assert.NoError(t, v())
	})

	t.Run("fails when MachineConfig present", func(t *testing.T) {
		fixture := newTestFixture().withMachineConfigs(newTestMachineConfig("50-master-dnsmasq-configuration"))
		v := validateMachineConfigNotPresent("50-master-dnsmasq-configuration", fixture.mcLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "50-master-dnsmasq-configuration")
		assert.Contains(t, err.Error(), "still present")
	})

	t.Run("ignores unrelated MachineConfigs", func(t *testing.T) {
		fixture := newTestFixture().withMachineConfigs(newTestMachineConfig("rendered-master-abc123"))
		v := validateMachineConfigNotPresent("50-master-dnsmasq-configuration", fixture.mcLister)
		assert.NoError(t, v())
	})
}

func TestValidateNewRenderedMasterConfig(t *testing.T) {
	t.Run("passes when a rendered master config exists", func(t *testing.T) {
		fixture := newTestFixture().withMachineConfigs(newTestMachineConfig("rendered-master-abc123"))
		v := validateNewRenderedMasterConfig(fixture.mcLister)
		assert.NoError(t, v())
	})

	t.Run("fails when no rendered master config exists", func(t *testing.T) {
		fixture := newTestFixture().withMachineConfigs(newTestMachineConfig("rendered-worker-abc123"))
		v := validateNewRenderedMasterConfig(fixture.mcLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "no rendered master MachineConfig found")
	})

	t.Run("fails when no MachineConfigs exist", func(t *testing.T) {
		fixture := newTestFixture()
		v := validateNewRenderedMasterConfig(fixture.mcLister)
		err := v()
		assert.Error(t, err)
	})
}

func TestValidateNewRenderedWorkerConfig(t *testing.T) {
	t.Run("passes when a rendered worker config exists", func(t *testing.T) {
		fixture := newTestFixture().withMachineConfigs(newTestMachineConfig("rendered-worker-abc123"))
		v := validateNewRenderedWorkerConfig(fixture.mcLister)
		assert.NoError(t, v())
	})

	t.Run("fails when no rendered worker config exists", func(t *testing.T) {
		fixture := newTestFixture().withMachineConfigs(newTestMachineConfig("rendered-master-abc123"))
		v := validateNewRenderedWorkerConfig(fixture.mcLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "no rendered worker MachineConfig found")
	})

	t.Run("fails when no MachineConfigs exist", func(t *testing.T) {
		fixture := newTestFixture()
		v := validateNewRenderedWorkerConfig(fixture.mcLister)
		err := v()
		assert.Error(t, err)
	})
}

func TestValidateMachineConfigPoolReadyCount(t *testing.T) {
	t.Run("passes when enough ready machines", func(t *testing.T) {
		fixture := newTestFixture().withMachineConfigPool(newTestMachineConfigPool("master", 3, 3))
		v := validateMachineConfigPoolReadyCount(3, fixture.mcpLister)
		assert.NoError(t, v())
	})

	t.Run("fails when insufficient ready machines", func(t *testing.T) {
		fixture := newTestFixture().withMachineConfigPool(newTestMachineConfigPool("master", 3, 1))
		v := validateMachineConfigPoolReadyCount(3, fixture.mcpLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "insufficient ready master machines: need 3, have 1")
	})

	t.Run("fails when master pool not found", func(t *testing.T) {
		fixture := newTestFixture()
		v := validateMachineConfigPoolReadyCount(3, fixture.mcpLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "failed to get master MachineConfigPool")
	})
}

func TestValidateIngressRouterCount(t *testing.T) {
	t.Run("passes when enough available replicas", func(t *testing.T) {
		fixture := newTestFixture().withIngressController(newTestIngressController("default", 2))
		v := validateIngressRouterCount(2, fixture.icLister)
		assert.NoError(t, v())
	})

	t.Run("fails when insufficient available replicas", func(t *testing.T) {
		fixture := newTestFixture().withIngressController(newTestIngressController("default", 1))
		v := validateIngressRouterCount(2, fixture.icLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "insufficient available router replicas: need 2, have 1")
	})

	t.Run("fails when default IngressController not found", func(t *testing.T) {
		fixture := newTestFixture()
		v := validateIngressRouterCount(2, fixture.icLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "failed to get default IngressController")
	})
}

func TestValidateKubeAPIServerNodeCount(t *testing.T) {
	t.Run("passes when enough node statuses", func(t *testing.T) {
		fixture := newTestFixture().withKubeAPIServer(newTestKubeAPIServerCR(3))
		v := validateKubeAPIServerNodeCount(3, fixture.kasLister)
		assert.NoError(t, v())
	})

	t.Run("fails when insufficient node statuses", func(t *testing.T) {
		fixture := newTestFixture().withKubeAPIServer(newTestKubeAPIServerCR(1))
		v := validateKubeAPIServerNodeCount(3, fixture.kasLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "insufficient kube-apiserver node statuses: need 3, have 1")
	})

	t.Run("fails when kubeapiservers/cluster not found", func(t *testing.T) {
		fixture := newTestFixture()
		v := validateKubeAPIServerNodeCount(3, fixture.kasLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "failed to get kubeapiservers.operator.openshift.io/cluster")
	})
}

func TestValidateOpenShiftAPIServerReadyReplicas(t *testing.T) {
	t.Run("passes when enough ready replicas", func(t *testing.T) {
		fixture := newTestFixture().withOpenShiftAPIServer(newTestOpenShiftAPIServerCR(3))
		v := validateOpenShiftAPIServerReadyReplicas(3, fixture.oasLister)
		assert.NoError(t, v())
	})

	t.Run("fails when insufficient ready replicas", func(t *testing.T) {
		fixture := newTestFixture().withOpenShiftAPIServer(newTestOpenShiftAPIServerCR(1))
		v := validateOpenShiftAPIServerReadyReplicas(3, fixture.oasLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "insufficient openshift-apiserver ready replicas: need 3, have 1")
	})

	t.Run("fails when openshiftapiservers/cluster not found", func(t *testing.T) {
		fixture := newTestFixture()
		v := validateOpenShiftAPIServerReadyReplicas(3, fixture.oasLister)
		err := v()
		if !assert.Error(t, err) {
			return
		}
		assert.Contains(t, err.Error(), "failed to get openshiftapiservers.operator.openshift.io/cluster")
	})
}
