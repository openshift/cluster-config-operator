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
	var previousTransitions []configv1.TopologyTransition

	writeErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := c.infraClient.Get(ctx, "cluster", metav1.GetOptions{})
		if err != nil {
			return err
		}
		latest = current

		// Keep only nested condition times locally while pending status clears
		// the reported options. Each retry still merges into freshly read status.
		if len(current.Status.TopologyTransitionStatus.Transitions) > 0 {
			previousTransitions = current.Status.TopologyTransitionStatus.Transitions
		}

		if c.discoveryNeedsChecks(current) {
			pending := current.DeepCopy()
			c.mergeTransitionCondition(pending, metav1.Condition{
				Type: configv1.TopologyTransitionsEvaluatedConditionType, Status: metav1.ConditionUnknown,
				Reason: "EvaluationPending", Message: "Supported topology transitions await evaluation",
			})
			if !equality.Semantic.DeepEqual(current.Status, pending.Status) {
				current, err = c.infraClient.UpdateStatus(ctx, pending, metav1.UpdateOptions{})
				if err != nil {
					return err
				}
			}
		}

		updated := current.DeepCopy()
		evaluationErr = c.mergeDiscovery(updated)
		preserveEvaluationTimes(updated.Status.TopologyTransitionStatus.Transitions, previousTransitions)
		if equality.Semantic.DeepEqual(current.Status, updated.Status) {
			return nil
		}

		_, err = c.infraClient.UpdateStatus(ctx, updated, metav1.UpdateOptions{})
		return err
	})

	return latest, errors.Join(evaluationErr, writeErr)
}

// applyTransitionStatus writes the supplied completion condition. A non-nil
// transition also applies topology and invalidates discovery in the same write.
// The upgrade block and events stay outside this conflict retry.
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

// mergeTransitionCondition patches the supplied condition in infra's
// in-memory status and sets ObservedGeneration to infra.Generation. It preserves
// LastTransitionTime when the status is unchanged, except when entering the
// in-progress phase, which restarts the reconciliation soak timer.
// It does not persist the change to the API.
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

// mergeDiscovery owns only evaluation status; transition completion is written
// separately. Match nested timestamps by transition identity and type.
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

func (c *TopologyTransitionController) discoveryNeedsChecks(infra *configv1.Infrastructure) bool {
	for _, transition := range c.transitions {
		if matchesStatus(transition.From, infra.Status) && (len(c.preflightChecks) > 0 || len(transition.PreflightValidators) > 0) {
			return true
		}
	}

	return false
}

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
