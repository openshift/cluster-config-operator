package topology_transition_controller

import (
	"context"

	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

// applyTransitionStatus writes completion and any topology change together.
// Each retry reads live status and guards against changed request inputs.
func (c *TopologyTransitionController) applyTransitionStatus(ctx context.Context, expected *configv1.Infrastructure, transition *TransitionDescriptor, completionCondition metav1.Condition) (bool, error) {
	applied := false

	writeErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		applied = false
		current, err := c.infraClient.Get(ctx, "cluster", metav1.GetOptions{})
		if err != nil {
			return err
		}

		updated := current.DeepCopy()

		// Guard both the requested spec and the source used by checks.
		canApply := equality.Semantic.DeepEqual(expected.Spec, current.Spec) &&
			topologyState(expected.Status) == topologyState(current.Status)
		if transition != nil {
			canApply = canApply && matchesStatus(transition.From, current.Status)
		} else if !canApply {
			return nil
		}

		if canApply {
			if transition != nil {
				transition.UpdateStatus(updated)
				if topologyState(current.Status) != topologyState(updated.Status) {
					c.mergeTransitionCondition(updated, metav1.Condition{
						Type: configv1.TopologyTransitionsEvaluatedConditionType, Status: metav1.ConditionUnknown,
						Reason: "EvaluationPending", Message: "Topology changed; supported transitions await evaluation",
					})
					updated.Status.TopologyTransitionStatus.Transitions = nil
				}
			}
			c.mergeTransitionCondition(updated, completionCondition)
		}

		if equality.Semantic.DeepEqual(current.Status, updated.Status) {
			// An already-set condition is enough to allow upgrade recovery, but
			// starting a transition reports success only after a topology write.
			applied = canApply && transition == nil
			return nil
		}

		_, err = c.infraClient.UpdateStatus(ctx, updated, metav1.UpdateOptions{})
		applied = canApply && err == nil
		return err
	})

	return applied, writeErr
}

// mergeTransitionCondition sets the generation and preserves the condition time
// unless a blocked request starts a new transition and needs a new soak timer.
func (c *TopologyTransitionController) mergeTransitionCondition(infra *configv1.Infrastructure, condition metav1.Condition) {
	conditions := &infra.Status.TopologyTransitionStatus.Conditions
	old := meta.FindStatusCondition(*conditions, condition.Type)

	condition.ObservedGeneration = infra.Generation
	condition.LastTransitionTime = metav1.NewTime(c.clock.Now())

	if old != nil {
		// Entering a new request must restart the soak timer, even when both
		// the blocked and the active phases report Completed=False.
		newRequest := condition.Reason == reasonTopologyTransitionInProgress && old.Reason != condition.Reason
		if old.Status == condition.Status && !newRequest {
			condition.LastTransitionTime = old.LastTransitionTime
		}

		*old = condition
	} else {
		*conditions = append(*conditions, condition)
	}

	c.ensureTransitionConditions(infra)
}

// ensureTransitionConditions supplies both API conditions without resetting an
// existing report from another writer. Unevaluated transitions are not listed.
func (c *TopologyTransitionController) ensureTransitionConditions(infra *configv1.Infrastructure) {
	status := &infra.Status.TopologyTransitionStatus
	for _, condition := range []metav1.Condition{
		{Type: configv1.TopologyTransitionsEvaluatedConditionType, Reason: "EvaluationPending", Message: "Supported topology transitions await evaluation"},
		{Type: configv1.TopologyTransitionCompletedConditionType, Reason: "TransitionStatusUnknown", Message: "Topology transition completion has not been evaluated"},
	} {
		if meta.FindStatusCondition(status.Conditions, condition.Type) != nil {
			continue
		}

		condition.Status = metav1.ConditionUnknown
		condition.ObservedGeneration = infra.Generation
		condition.LastTransitionTime = metav1.NewTime(c.clock.Now())
		status.Conditions = append(status.Conditions, condition)
	}

	if !meta.IsStatusConditionTrue(status.Conditions, configv1.TopologyTransitionsEvaluatedConditionType) {
		status.Transitions = nil
	}
}

func topologyState(status configv1.InfrastructureStatus) configv1.TopologyState {
	return configv1.TopologyState{
		ControlPlaneTopology: status.ControlPlaneTopology, InfrastructureTopology: status.InfrastructureTopology,
	}
}
