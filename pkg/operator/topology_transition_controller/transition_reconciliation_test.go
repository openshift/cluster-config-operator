package topology_transition_controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCheckAllClusterOperatorsStable(t *testing.T) {
	t.Run("all operators stable", func(t *testing.T) {
		lister := newTestClusterOperatorLister(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			newTestClusterOperator("kube-apiserver", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			newTestClusterOperator("monitoring", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
		)
		check := reconcileClusterOperatorsStable(lister)
		reconciled, err := check(context.TODO())
		assert.NoError(t, err)
		assert.True(t, reconciled)
	})

	t.Run("one operator progressing", func(t *testing.T) {
		lister := newTestClusterOperatorLister(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			newTestClusterOperator("kube-apiserver", configv1.ConditionTrue, configv1.ConditionTrue, configv1.ConditionFalse),
			newTestClusterOperator("monitoring", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
		)
		check := reconcileClusterOperatorsStable(lister)
		reconciled, err := check(context.TODO())
		assert.NoError(t, err)
		assert.False(t, reconciled)
	})

	t.Run("one operator degraded", func(t *testing.T) {
		lister := newTestClusterOperatorLister(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			newTestClusterOperator("kube-apiserver", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionTrue),
			newTestClusterOperator("monitoring", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
		)
		check := reconcileClusterOperatorsStable(lister)
		reconciled, err := check(context.TODO())
		assert.NoError(t, err)
		assert.False(t, reconciled)
	})

	t.Run("one operator unavailable", func(t *testing.T) {
		lister := newTestClusterOperatorLister(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			newTestClusterOperator("kube-apiserver", configv1.ConditionFalse, configv1.ConditionFalse, configv1.ConditionFalse),
			newTestClusterOperator("monitoring", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
		)
		check := reconcileClusterOperatorsStable(lister)
		reconciled, err := check(context.TODO())
		assert.NoError(t, err)
		assert.False(t, reconciled)
	})

	t.Run("no cluster operators", func(t *testing.T) {
		lister := newTestClusterOperatorLister()
		check := reconcileClusterOperatorsStable(lister)
		reconciled, err := check(context.TODO())
		assert.NoError(t, err)
		assert.True(t, reconciled)
	})

	t.Run("multiple issues", func(t *testing.T) {
		lister := newTestClusterOperatorLister(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionTrue, configv1.ConditionFalse),
			newTestClusterOperator("kube-apiserver", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionTrue),
			newTestClusterOperator("monitoring", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
		)
		check := reconcileClusterOperatorsStable(lister)
		reconciled, err := check(context.TODO())
		assert.NoError(t, err)
		assert.False(t, reconciled)
	})

	t.Run("operator with no conditions treated as not stable", func(t *testing.T) {
		lister := newTestClusterOperatorLister(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			&configv1.ClusterOperator{
				ObjectMeta: metav1.ObjectMeta{Name: "new-operator"},
			},
		)
		check := reconcileClusterOperatorsStable(lister)
		reconciled, err := check(context.TODO())
		assert.NoError(t, err)
		assert.False(t, reconciled)
	})

	t.Run("operator missing Available condition treated as not stable", func(t *testing.T) {
		lister := newTestClusterOperatorLister(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			&configv1.ClusterOperator{
				ObjectMeta: metav1.ObjectMeta{Name: "partial-operator"},
				Status: configv1.ClusterOperatorStatus{
					Conditions: []configv1.ClusterOperatorStatusCondition{
						{Type: configv1.OperatorProgressing, Status: configv1.ConditionFalse},
						{Type: configv1.OperatorDegraded, Status: configv1.ConditionFalse},
					},
				},
			},
		)
		check := reconcileClusterOperatorsStable(lister)
		reconciled, err := check(context.TODO())
		assert.NoError(t, err)
		assert.False(t, reconciled)
	})

	t.Run("config-operator is excluded from stability check", func(t *testing.T) {
		lister := newTestClusterOperatorLister(
			newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse),
			newTestClusterOperator("config-operator", configv1.ConditionTrue, configv1.ConditionTrue, configv1.ConditionTrue),
		)
		check := reconcileClusterOperatorsStable(lister)
		reconciled, err := check(context.TODO())
		assert.NoError(t, err)
		assert.True(t, reconciled)
	})
}
