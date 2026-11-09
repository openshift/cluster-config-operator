package topology_transition_controller

import (
	"errors"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

// TestEvaluationKeepsOperatorTransitionProgress checks that discovery leaves active progress on the
// operator.
func TestEvaluationKeepsOperatorTransitionProgress(t *testing.T) {
	infra := snoInfra(configv1.HighlyAvailableTopologyMode)
	c, _, clk := statusTestController(infra)
	ready := false
	validatorCalls := 0
	c.transitions[0].TransitionValidators = []TransitionValidatorFunc{func() error {
		validatorCalls++
		if !ready {
			return errors.New("downstream reconciliation pending")
		}
		return nil
	}}
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	assert.NoError(t, indexer.Add(infra))
	c.infraLister = configlistersv1.NewInfrastructureLister(indexer)

	assert.NoError(t, c.syncEvaluation(t.Context()))
	assert.NoError(t, c.sync(t.Context(), newTestSyncContext()))

	_, operatorStatus, _, err := c.operatorClient.GetOperatorState()
	assert.NoError(t, err)
	assert.True(t, v1helpers.IsOperatorConditionTrue(operatorStatus.Conditions, "TopologyTransitionControllerProgressing"))
	assert.True(t, v1helpers.IsOperatorConditionFalse(operatorStatus.Conditions, upgradeableCondition))
	activeConditions := append([]operatorv1.OperatorCondition(nil), operatorStatus.Conditions...)
	assert.NoError(t, c.syncEvaluation(t.Context()))
	updated := currentInfra(t, c)
	assert.Nil(t, updated.Status.TopologyTransitionStatus.Transitions)
	assert.Len(t, c.transitions, 1)
	assert.NoError(t, indexer.Update(updated))
	_, operatorStatus, _, err = c.operatorClient.GetOperatorState()
	assert.NoError(t, err)
	assert.Equal(t, activeConditions, operatorStatus.Conditions)
	assert.NoError(t, c.sync(t.Context(), newTestSyncContext()))
	assert.Zero(t, validatorCalls)

	progressing := v1helpers.FindOperatorCondition(operatorStatus.Conditions, transitionProgressingCondition)
	clk.SetTime(progressing.LastTransitionTime.Add(minReconciliationSoakTime))
	assert.NoError(t, c.sync(t.Context(), newTestSyncContext()))
	assert.Equal(t, 1, validatorCalls)
	_, operatorStatus, _, err = c.operatorClient.GetOperatorState()
	assert.NoError(t, err)
	assert.Equal(t, activeConditions, operatorStatus.Conditions)

	// Before validators pass, completion should be False (transition in progress)
	preCompletion := meta.FindStatusCondition(currentInfra(t, c).Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionCompletedConditionType)
	if assert.NotNil(t, preCompletion) {
		assert.Equal(t, metav1.ConditionFalse, preCompletion.Status, "completion is False while transition in progress")
	}

	ready = true
	assert.NoError(t, c.sync(t.Context(), newTestSyncContext()))
	assert.Equal(t, 2, validatorCalls)
	_, operatorStatus, _, err = c.operatorClient.GetOperatorState()
	assert.NoError(t, err)
	assert.True(t, v1helpers.IsOperatorConditionFalse(operatorStatus.Conditions, transitionProgressingCondition))
	assert.True(t, v1helpers.IsOperatorConditionTrue(operatorStatus.Conditions, upgradeableCondition))
	assert.Equal(t, "TopologyTransitionComplete", v1helpers.FindOperatorCondition(operatorStatus.Conditions, transitionProgressingCondition).Reason)
	completedConditions := append([]operatorv1.OperatorCondition(nil), operatorStatus.Conditions...)
	// Refresh the cached Infrastructure so the idle-path sync below observes the
	// completed transition instead of replaying it from a stale cache.
	assert.NoError(t, indexer.Update(currentInfra(t, c)))
	assert.NoError(t, c.syncEvaluation(t.Context()))
	assert.NoError(t, c.sync(t.Context(), newTestSyncContext()))
	_, operatorStatus, _, err = c.operatorClient.GetOperatorState()
	assert.NoError(t, err)
	assert.Equal(t, completedConditions, operatorStatus.Conditions)
	assert.Equal(t, 2, validatorCalls)
	assert.Nil(t, currentInfra(t, c).Status.TopologyTransitionStatus.Transitions)

	completion := meta.FindStatusCondition(currentInfra(t, c).Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionCompletedConditionType)
	if assert.NotNil(t, completion) {
		// Evaluation preserves completion state written by the main sync; after the
		// transition completed above, the main sync wrote True and the evaluation
		// worker carries it forward without modification.
		assert.Equal(t, metav1.ConditionTrue, completion.Status, "evaluation preserves completion state from main sync")
		assert.Equal(t, reasonTopologyTransitionComplete, completion.Reason)
	}
}
