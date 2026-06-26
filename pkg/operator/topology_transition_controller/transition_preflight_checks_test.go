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
