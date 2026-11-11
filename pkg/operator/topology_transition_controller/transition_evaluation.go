package topology_transition_controller

import (
	"errors"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const maxEvaluationMessageCharacters = 32768

// evaluateTransitions reports each applicable transition, independently of any
// spec request. It returns only the evaluation condition; callers merge it with
// the completion condition when writing status. No input is changed.
func (c *TopologyTransitionController) evaluateTransitions(infra *configv1.Infrastructure) ([]configv1.TopologyTransition, metav1.Condition, error) {
	now := metav1.NewTime(c.clock.Now())
	condition := metav1.Condition{
		Type: configv1.TopologyTransitionsEvaluatedConditionType, Status: metav1.ConditionTrue,
		Reason: "EvaluationComplete", Message: "Supported topology transitions evaluated",
		ObservedGeneration: infra.Generation, LastTransitionTime: now,
	}

	if len(c.transitions) == 0 {
		condition.Reason = "NoSupportedTransitions"
		condition.Message = "No supported topology transitions are registered"
		return nil, condition, nil
	}
	if len(c.transitions) > 8 {
		return failedTransitionEvaluation(condition, fmt.Errorf("at most 8 supported transitions may be reported"))
	}

	var transitions []configv1.TopologyTransition
	var readErrors []error
	for i := range c.transitions {
		descriptor := &c.transitions[i]
		if descriptor.UpdateStatus == nil {
			return failedTransitionEvaluation(condition, fmt.Errorf("supported transition has no status updater"))
		}

		checks := append(append([]PreflightCheck{}, c.preflightChecks...), descriptor.PreflightValidators...)
		if err := validateEvaluationChecks(checks); err != nil {
			return failedTransitionEvaluation(condition, err)
		}
		if !matchesStatus(descriptor.From, infra.Status) {
			continue
		}

		// Resolve wildcard fields from current status.
		source := infra.DeepCopy()
		if descriptor.From.ControlPlaneTopology != "" {
			source.Status.ControlPlaneTopology = descriptor.From.ControlPlaneTopology
		}
		if descriptor.From.InfrastructureTopology != "" {
			source.Status.InfrastructureTopology = descriptor.From.InfrastructureTopology
		}
		if descriptor.From.PlatformStatus != nil && descriptor.From.PlatformStatus.Type != "" {
			source.Status.PlatformStatus = descriptor.From.PlatformStatus.DeepCopy()
		}
		target := source.DeepCopy()
		descriptor.UpdateStatus(target)
		transition := configv1.TopologyTransition{Source: topologyState(source.Status), Target: topologyState(target.Status)}
		if !validTopologyState(transition.Source) || !validTopologyState(transition.Target) {
			return failedTransitionEvaluation(condition, fmt.Errorf("supported transition has invalid source or target topology"))
		}
		for _, previous := range transitions {
			if previous.Source == transition.Source && previous.Target == transition.Target {
				return failedTransitionEvaluation(condition, fmt.Errorf("duplicate supported transition source and target"))
			}
		}

		available := metav1.Condition{
			Type: configv1.TopologyTransitionAvailableConditionType, Status: metav1.ConditionTrue,
			Reason: "PreflightChecksPassed", Message: "All preflight checks passed",
			ObservedGeneration: infra.Generation, LastTransitionTime: now,
		}

		var hasBlockers bool
		var evaluations []metav1.Condition
		for _, check := range checks {
			result := metav1.Condition{
				Type: check.Type, Status: metav1.ConditionTrue,
				Reason: "PreflightCheckPassed", Message: "Preflight check passed",
				ObservedGeneration: infra.Generation, LastTransitionTime: now,
			}

			if err := check.Validate(); err != nil {
				result.Status = metav1.ConditionFalse
				result.Reason = reasonPreflightCheckFailed
				result.Message = limitEvaluationMessage(err.Error())

				if _, ok := errors.AsType[*clusterStateReadError](err); ok {
					readErrors = append(readErrors, fmt.Errorf("transition %d %s: %w", i, check.Type, err))
				} else {
					hasBlockers = true
				}
			}
			evaluations = append(evaluations, result)
		}

		if hasBlockers {
			available.Status = metav1.ConditionFalse
			available.Reason = reasonPreflightCheckFailed
			available.Message = "One or more preflight checks failed. See the other evaluation conditions with status False for details."
		}

		transition.Evaluations = append([]metav1.Condition{available}, evaluations...)
		transitions = append(transitions, transition)
	}

	// Required read failures invalidate the whole report. Run every transition's
	// checks first, then clear all options rather than publish partial results.
	if err := errors.Join(readErrors...); err != nil {
		return failedTransitionEvaluation(condition, err)
	}

	return transitions, condition, nil
}

// failedTransitionEvaluation marks the report failed, omits all transitions, and returns the error.
func failedTransitionEvaluation(condition metav1.Condition, err error) ([]configv1.TopologyTransition, metav1.Condition, error) {
	condition.Status = metav1.ConditionFalse
	condition.Reason = "TransitionEvaluationFailed"
	condition.Message = limitEvaluationMessage(err.Error())

	return nil, condition, err
}

// Check descriptors are controller-owned. Reject invalid registration rather
// than emit conditions that violate the API's map keys or count limit.
func validateEvaluationChecks(checks []PreflightCheck) error {
	if len(checks) > 31 {
		return fmt.Errorf("a transition may have at most 31 preflight checks plus availability")
	}

	seen := map[string]bool{configv1.TopologyTransitionAvailableConditionType: true}
	for _, check := range checks {
		if len(validation.IsQualifiedName(check.Type)) > 0 {
			return fmt.Errorf("invalid preflight condition type %q", check.Type)
		}
		if seen[check.Type] {
			return fmt.Errorf("duplicate preflight condition type %q", check.Type)
		}
		if check.Validate == nil {
			return fmt.Errorf("preflight check %q has no validator", check.Type)
		}

		seen[check.Type] = true
	}

	return nil
}

// limitEvaluationMessage caps messages at the API character limit without splitting UTF-8
// characters.
func limitEvaluationMessage(message string) string {
	characters := []rune(message)
	if len(characters) > maxEvaluationMessageCharacters {
		characters = characters[:maxEvaluationMessageCharacters]
	}

	return string(characters)
}

// topologyState extracts the control plane and infrastructure modes used to identify transitions.
func topologyState(status configv1.InfrastructureStatus) configv1.TopologyState {
	return configv1.TopologyState{
		ControlPlaneTopology: status.ControlPlaneTopology, InfrastructureTopology: status.InfrastructureTopology,
	}
}

// validTopologyState checks that both modes are valid, allowing arbiter mode only for the control
// plane.
func validTopologyState(state configv1.TopologyState) bool {
	valid := func(mode configv1.TopologyMode) bool {
		return mode == configv1.SingleReplicaTopologyMode || mode == configv1.HighlyAvailableTopologyMode
	}

	return (valid(state.ControlPlaneTopology) || state.ControlPlaneTopology == configv1.HighlyAvailableArbiterMode) && valid(state.InfrastructureTopology)
}
