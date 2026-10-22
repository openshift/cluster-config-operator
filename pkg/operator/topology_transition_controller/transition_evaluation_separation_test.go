package topology_transition_controller

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestEvaluationKeepsOperatorTransitionProgress checks that discovery leaves active progress on the
// operator.
func TestEvaluationKeepsOperatorTransitionProgress(t *testing.T) {
	c, _, _ := statusTestController(snoInfra(configv1.HighlyAvailableTopologyMode))

	assert.NoError(t, c.syncEvaluation(t.Context()))
	assert.NoError(t, c.sync(t.Context(), newTestSyncContext()))

	_, operatorStatus, _, err := c.operatorClient.GetOperatorState()
	assert.NoError(t, err)
	assert.True(t, v1helpers.IsOperatorConditionTrue(operatorStatus.Conditions, "TopologyTransitionControllerProgressing"))
	assert.True(t, v1helpers.IsOperatorConditionFalse(operatorStatus.Conditions, upgradeableCondition))

	completion := meta.FindStatusCondition(currentInfra(t, c).Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionCompletedConditionType)
	if assert.NotNil(t, completion) {
		assert.Equal(t, metav1.ConditionUnknown, completion.Status)
		assert.Equal(t, "TransitionStatusUnknown", completion.Reason)
	}
}
