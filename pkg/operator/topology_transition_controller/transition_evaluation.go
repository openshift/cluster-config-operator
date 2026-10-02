package topology_transition_controller

import (
	"errors"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const maxEvaluationMessageCharacters = 32768

// evaluateTransitions discovers transitions from current status, independently of
// any spec request. It returns only the evaluation condition; callers must merge
// it with any completion condition when writing status. No input is changed.
func (c *TopologyTransitionController) evaluateTransitions(infra *configv1.Infrastructure) ([]configv1.TopologyTransition, metav1.Condition, error) {
	now := metav1.NewTime(c.clock.Now())
	condition := metav1.Condition{
		Type: configv1.TopologyTransitionsEvaluatedConditionType, Status: metav1.ConditionTrue,
		Reason: "EvaluationComplete", Message: "Supported topology transitions evaluated",
		ObservedGeneration: infra.Generation, LastTransitionTime: now,
	}

	var descriptor *TransitionDescriptor
	for i := range c.transitions {
		if !matchesStatus(c.transitions[i].From, infra.Status) {
			continue
		}

		if descriptor != nil {
			return failedTransitionEvaluation(condition, fmt.Errorf("more than one supported transition matches the current topology"))
		}

		descriptor = &c.transitions[i]
	}

	if descriptor == nil {
		platformType := configv1.PlatformType("Unknown")
		if infra.Status.PlatformStatus != nil {
			platformType = infra.Status.PlatformStatus.Type
		}

		condition.Reason = "NoSupportedTransitions"
		condition.Message = limitEvaluationMessage(fmt.Sprintf("No supported topology transition matches {controlPlane=%s, infrastructure=%s, platform=%s}",
			infra.Status.ControlPlaneTopology, infra.Status.InfrastructureTopology, platformType))
		return nil, condition, nil
	}

	if descriptor.UpdateStatus == nil {
		return failedTransitionEvaluation(condition, fmt.Errorf("supported transition has no status updater"))
	}

	checks := append(append([]PreflightCheck{}, c.preflightChecks...), descriptor.PreflightValidators...)
	if err := validateEvaluationChecks(checks); err != nil {
		return failedTransitionEvaluation(condition, err)
	}

	target := infra.DeepCopy()
	descriptor.UpdateStatus(target)
	transition := configv1.TopologyTransition{Source: topologyState(infra.Status), Target: topologyState(target.Status)}
	if !validTopologyState(transition.Source) || !validTopologyState(transition.Target) {
		return failedTransitionEvaluation(condition, fmt.Errorf("supported transition has invalid source or target topology"))
	}

	available := metav1.Condition{
		Type: configv1.TopologyTransitionAvailableConditionType, Status: metav1.ConditionTrue,
		Reason: "PreflightChecksPassed", Message: "All preflight checks passed",
		ObservedGeneration: infra.Generation, LastTransitionTime: now,
	}

	var hasBlockers bool
	var readErrors []error
	var evaluations []metav1.Condition
	for _, check := range checks {
		result := metav1.Condition{
			Type: check.Type, Status: metav1.ConditionTrue,
			Reason: "PreflightCheckPassed", Message: "Preflight check passed",
			ObservedGeneration: infra.Generation, LastTransitionTime: now,
		}

		if err := check.Validate(); err != nil {
			result.Status = metav1.ConditionFalse
			result.Reason = "PreflightCheckFailed"
			result.Message = limitEvaluationMessage(err.Error())

			if _, ok := errors.AsType[*clusterStateReadError](err); ok {
				readErrors = append(readErrors, fmt.Errorf("%s: %w", check.Type, err))
			} else {
				hasBlockers = true
			}
		}

		evaluations = append(evaluations, result)
	}

	// Required read failures take precedence over known blockers. Never publish
	// a partial evaluation, but run all checks so every read failure is returned.
	if err := errors.Join(readErrors...); err != nil {
		return failedTransitionEvaluation(condition, err)
	}

	if hasBlockers {
		available.Status = metav1.ConditionFalse
		available.Reason = "PreflightCheckFailed"
		available.Message = "One or more preflight checks failed. See the other evaluation conditions with status False for details."
	}

	transition.Evaluations = append([]metav1.Condition{available}, evaluations...)

	return []configv1.TopologyTransition{transition}, condition, nil
}

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

func limitEvaluationMessage(message string) string {
	characters := []rune(message)
	if len(characters) > maxEvaluationMessageCharacters {
		characters = characters[:maxEvaluationMessageCharacters]
	}

	return string(characters)
}

func topologyState(status configv1.InfrastructureStatus) configv1.TopologyState {
	return configv1.TopologyState{
		ControlPlaneTopology: status.ControlPlaneTopology, InfrastructureTopology: status.InfrastructureTopology,
	}
}

func validTopologyState(state configv1.TopologyState) bool {
	valid := func(mode configv1.TopologyMode) bool {
		return mode == configv1.SingleReplicaTopologyMode || mode == configv1.HighlyAvailableTopologyMode
	}

	return valid(state.ControlPlaneTopology) && valid(state.InfrastructureTopology)
}
