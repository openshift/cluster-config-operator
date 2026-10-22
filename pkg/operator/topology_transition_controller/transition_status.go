package topology_transition_controller

import (
	"context"
	"errors"

	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

// refreshDiscovery reads and evaluates inside the retry, so neither a stale
// cache nor a conflict can overwrite newer Infrastructure status.
func (c *TopologyTransitionController) refreshDiscovery(ctx context.Context) (*configv1.Infrastructure, error) {
	var latest *configv1.Infrastructure
	var evaluationErr error

	writeErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := c.infraClient.Get(ctx, "cluster", metav1.GetOptions{})
		if err != nil {
			return err
		}
		latest = current

		updated := current.DeepCopy()
		evaluationErr = c.mergeDiscovery(updated)
		if equality.Semantic.DeepEqual(current.Status, updated.Status) {
			return nil
		}

		_, err = c.infraClient.UpdateStatus(ctx, updated, metav1.UpdateOptions{})
		return err
	})

	return latest, errors.Join(evaluationErr, writeErr)
}

// applyTopologyStatus changes topology and invalidates discovery in one write.
// Each retry checks the request and source against freshly read status.
func (c *TopologyTransitionController) applyTopologyStatus(ctx context.Context, expected *configv1.Infrastructure, transition *TransitionDescriptor) (bool, error) {
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
		canApply = canApply && matchesStatus(transition.From, current.Status)
		if !canApply {
			return nil
		}

		transition.UpdateStatus(updated)
		if topologyState(current.Status) != topologyState(updated.Status) {
			c.invalidateDiscovery(updated)
		}

		if equality.Semantic.DeepEqual(current.Status, updated.Status) {
			return nil
		}

		_, err = c.infraClient.UpdateStatus(ctx, updated, metav1.UpdateOptions{})
		applied = canApply && err == nil
		return err
	})

	return applied, writeErr
}

// invalidateDiscovery clears the old report when topology inputs change.
func (c *TopologyTransitionController) invalidateDiscovery(infra *configv1.Infrastructure) {
	condition := metav1.Condition{
		Type: configv1.TopologyTransitionsEvaluatedConditionType, Status: metav1.ConditionUnknown,
		Reason: "EvaluationPending", Message: "Topology changed; supported transitions await evaluation",
		ObservedGeneration: infra.Generation, LastTransitionTime: metav1.NewTime(c.clock.Now()),
	}
	conditions := &infra.Status.TopologyTransitionStatus.Conditions
	preserveConditionTime(&condition, *conditions)

	if old := meta.FindStatusCondition(*conditions, condition.Type); old != nil {
		*old = condition
	} else {
		*conditions = append(*conditions, condition)
	}

	infra.Status.TopologyTransitionStatus.Transitions = nil
	c.ensureTransitionConditions(infra)
}

// mergeDiscovery owns evaluation status and preserves existing completion.
// Match nested timestamps by transition identity and type.
func (c *TopologyTransitionController) mergeDiscovery(infra *configv1.Infrastructure) error {
	transitions, condition, err := c.evaluateTransitions(infra)

	status := &infra.Status.TopologyTransitionStatus
	preserveConditionTime(&condition, status.Conditions)

	preserveEvaluationTimes(transitions, status.Transitions)

	// Do not use SetStatusCondition: it supplies wall time when a new timestamp
	// is zero. All timestamps here come from the injected clock or old status.
	if old := meta.FindStatusCondition(status.Conditions, condition.Type); old != nil {
		*old = condition
	} else {
		status.Conditions = append(status.Conditions, condition)
	}

	// Semantic.DeepEqual treats nil and empty slices alike. Keep the emitted
	// empty list nil so omitempty removes it on an API round trip.
	status.Transitions = transitions
	c.ensureTransitionConditions(infra)

	return err
}

// preserveConditionTime retains the previous timestamp when the condition status has not changed.
func preserveConditionTime(condition *metav1.Condition, previous []metav1.Condition) {
	if old := meta.FindStatusCondition(previous, condition.Type); old != nil && old.Status == condition.Status {
		condition.LastTransitionTime = old.LastTransitionTime
	}
}

// ensureTransitionConditions repairs older reports without resetting conditions
// already owned by the other status writer.
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

// preserveEvaluationTimes matches source, target, and condition type to retain stable check
// timestamps.
func preserveEvaluationTimes(transitions, previous []configv1.TopologyTransition) {
	for i := range transitions {
		for _, old := range previous {
			if old.Source != transitions[i].Source || old.Target != transitions[i].Target {
				continue
			}

			for j := range transitions[i].Evaluations {
				preserveConditionTime(&transitions[i].Evaluations[j], old.Evaluations)
			}
		}
	}
}
