package topology_transition_controller

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1validation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation/field"
	corev1listers "k8s.io/client-go/listers/core/v1"
	clocktesting "k8s.io/utils/clock/testing"
)

var evaluationTime = time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)

func TestEvaluateTransitionsHealthyWithoutRequest(t *testing.T) {
	for _, spec := range []configv1.TopologyMode{"", configv1.SingleReplicaTopologyMode} {
		t.Run(string(spec), func(t *testing.T) {
			infra := snoInfra(spec)
			infra.Generation = 7
			before := infra.DeepCopy()
			fixture := readyFixture()
			ctrl := newTestControllerWithClock(infra, nil, fixture.buildPreflightChecks(), fixture.buildTransitions(), clocktesting.NewFakePassiveClock(evaluationTime))

			transitions, condition, err := ctrl.evaluateTransitions(infra)
			if !assert.NoError(t, err) || !assert.Len(t, transitions, 1) {
				return
			}

			assert.Equal(t, before, infra, "evaluation must not change Infrastructure")
			assert.Equal(t, metav1.Condition{
				Type: configv1.TopologyTransitionsEvaluatedConditionType, Status: metav1.ConditionTrue,
				Reason: "EvaluationComplete", Message: "Supported topology transitions evaluated",
				ObservedGeneration: 7, LastTransitionTime: metav1.NewTime(evaluationTime),
			}, condition)
			assert.Equal(t, configv1.TopologyState{ControlPlaneTopology: configv1.SingleReplicaTopologyMode, InfrastructureTopology: configv1.SingleReplicaTopologyMode}, transitions[0].Source)
			assert.Equal(t, configv1.TopologyState{ControlPlaneTopology: configv1.HighlyAvailableTopologyMode, InfrastructureTopology: configv1.HighlyAvailableTopologyMode}, transitions[0].Target)

			expectedTypes := []string{
				configv1.TopologyTransitionAvailableConditionType,
				"ClusterOperatorsStable", "NoClusterVersionUpgradeInProgress",
				"ControlPlaneNodeCountSatisfied", "InfrastructureNodeCountSatisfied",
				"ControlPlaneNodesSchedulable", "ControlPlaneNodesReady", "ControlPlaneNodesAreWorkers",
				"EtcdQuorumAvailable", "EtcdNotProgressing", "EtcdVotingMembersSatisfied",
			}
			var types []string
			for _, check := range transitions[0].Evaluations {
				types = append(types, check.Type)
				assert.Equal(t, metav1.ConditionTrue, check.Status, check.Type)
				assert.Equal(t, int64(7), check.ObservedGeneration)
				assert.Equal(t, metav1.NewTime(evaluationTime), check.LastTransitionTime)
				assert.NotEmpty(t, check.Reason)
				assert.NotEmpty(t, check.Message)
			}
			assert.Equal(t, expectedTypes, types)
			assert.Equal(t, "PreflightChecksPassed", transitions[0].Evaluations[0].Reason)
		})
	}
}

func TestEvaluateTransitionsEmittedConditionsValid(t *testing.T) {
	for _, tc := range []struct {
		name        string
		change      func(*testFixture, *configv1.Infrastructure)
		transitions int
		readFailure bool
	}{
		{name: "healthy", transitions: 1},
		{name: "multiple blockers", transitions: 1, change: func(f *testFixture, _ *configv1.Infrastructure) {
			f.withNodes(newTestDualRoleNodeWithConditions("master-2", true, notReadyNodeCondition())).
				withEtcdCR(false, true).withEtcdEndpoints(1).withClusterVersion(true)
		}},
		{name: "unsupported platform", change: func(_ *testFixture, i *configv1.Infrastructure) {
			i.Status.PlatformStatus.Type = configv1.AWSPlatformType
		}},
		{name: "typed read failure", readFailure: true, change: func(f *testFixture, _ *configv1.Infrastructure) {
			f.nodeLister = failingNodeLister{NodeLister: f.nodeLister, err: errors.New("node cache unavailable")}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := readyFixture()
			infra := snoInfra("")
			infra.Generation = 7
			if tc.change != nil {
				tc.change(fixture, infra)
			}
			ctrl := newTestControllerWithClock(infra, nil, fixture.buildPreflightChecks(), fixture.buildTransitions(), clocktesting.NewFakePassiveClock(evaluationTime))

			transitions, condition, err := ctrl.evaluateTransitions(infra)

			if tc.readFailure {
				var readErr *clusterStateReadError
				assert.ErrorAs(t, err, &readErr)
			} else {
				assert.NoError(t, err)
			}
			assert.Len(t, transitions, tc.transitions)
			assert.LessOrEqual(t, len(transitions), 1)
			assert.Equal(t, configv1.TopologyTransitionsEvaluatedConditionType, condition.Type)
			assert.Equal(t, infra.Generation, condition.ObservedGeneration)

			path := field.NewPath("status", "topologyTransitionStatus")
			// ValidateConditions also rejects duplicate condition map keys. These
			// ordinary messages fit its byte cap; Unicode bounds are tested below.
			assert.Empty(t, metav1validation.ValidateConditions([]metav1.Condition{condition}, path.Child("conditions")))
			for i, transition := range transitions {
				for _, state := range []configv1.TopologyState{transition.Source, transition.Target} {
					validModes := []configv1.TopologyMode{configv1.SingleReplicaTopologyMode, configv1.HighlyAvailableTopologyMode}
					assert.Contains(t, validModes, state.ControlPlaneTopology)
					assert.Contains(t, validModes, state.InfrastructureTopology)
				}
				assert.GreaterOrEqual(t, len(transition.Evaluations), 1)
				assert.LessOrEqual(t, len(transition.Evaluations), 32)
				assert.NotNil(t, meta.FindStatusCondition(transition.Evaluations, configv1.TopologyTransitionAvailableConditionType))
				assert.Empty(t, metav1validation.ValidateConditions(transition.Evaluations, path.Child("transitions").Index(i).Child("evaluations")))
				for _, check := range transition.Evaluations {
					assert.Equal(t, infra.Generation, check.ObservedGeneration, check.Type)
				}
			}
		})
	}
}

func TestEvaluateTransitionsMessageBounds(t *testing.T) {
	// The CRD limits characters, not the bytes counted by ValidateConditions.
	for _, readFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("read failure %t", readFailure), func(t *testing.T) {
			message := strings.Repeat("a", 32767) + strings.Repeat("界", 100)
			infra := snoInfra("")
			ctrl := &TopologyTransitionController{
				transitions: noopTransitions(), clock: clocktesting.NewFakePassiveClock(evaluationTime),
				preflightChecks: []PreflightCheck{{Type: "LongMessage", Validate: func() error {
					err := errors.New(message)
					if readFailure {
						return &clusterStateReadError{err: err}
					}
					return err
				}}},
			}

			transitions, condition, err := ctrl.evaluateTransitions(infra)

			conditions := []metav1.Condition{condition}
			if readFailure {
				assert.Error(t, err)
				assert.Empty(t, transitions)
				assert.Equal(t, metav1.ConditionFalse, condition.Status)
				assert.Equal(t, 32768, utf8.RuneCountInString(condition.Message))
			} else {
				if !assert.NoError(t, err) || !assert.Len(t, transitions, 1) {
					return
				}
				conditions = append(conditions, transitions[0].Evaluations...)
				check := meta.FindStatusCondition(transitions[0].Evaluations, "LongMessage")
				if assert.NotNil(t, check) {
					assert.Equal(t, strings.Repeat("a", 32767)+"界", check.Message, "limit characters, not bytes")
				}
			}

			for _, c := range conditions {
				assert.True(t, utf8.ValidString(c.Message), c.Type)
				assert.LessOrEqual(t, utf8.RuneCountInString(c.Message), 32768, c.Type)
				assert.NotEmpty(t, c.Reason)
				assert.LessOrEqual(t, utf8.RuneCountInString(c.Reason), 1024)
			}
		})
	}

	t.Run("etcd progressing message", func(t *testing.T) {
		fixture := readyFixture()
		etcd := newTestEtcdCR(true, true)
		message := strings.Repeat("界", 32768)
		for i := range etcd.Status.Conditions {
			if etcd.Status.Conditions[i].Type == etcdMembersProgressingCondition {
				etcd.Status.Conditions[i].Message = message
			}
		}
		if !assert.NoError(t, fixture.etcdIndexer.Update(etcd)) {
			return
		}
		infra := snoInfra("")
		ctrl := newTestControllerWithClock(infra, nil, fixture.buildPreflightChecks(), fixture.buildTransitions(), clocktesting.NewFakePassiveClock(evaluationTime))

		transitions, condition, err := ctrl.evaluateTransitions(infra)
		if !assert.NoError(t, err) || !assert.Len(t, transitions, 1) {
			return
		}
		assert.Equal(t, metav1.ConditionTrue, condition.Status)
		check := meta.FindStatusCondition(transitions[0].Evaluations, "EtcdNotProgressing")
		if assert.NotNil(t, check) {
			assert.Equal(t, metav1.ConditionFalse, check.Status)
			assert.Equal(t, string([]rune("etcd is still progressing: " + message)[:32768]), check.Message)
			assert.Equal(t, 32768, utf8.RuneCountInString(check.Message))
			assert.True(t, utf8.ValidString(check.Message))
		}

		available := meta.FindStatusCondition(transitions[0].Evaluations, configv1.TopologyTransitionAvailableConditionType)
		if assert.NotNil(t, available) {
			assert.Equal(t, metav1.ConditionFalse, available.Status)
			assert.Equal(t, "One or more preflight checks failed. See the other evaluation conditions with status False for details.", available.Message)
		}
	})
}

func TestEvaluateTransitionsDescriptorBounds(t *testing.T) {
	checks := func(count int) []PreflightCheck {
		var result []PreflightCheck
		for i := 0; i < count; i++ {
			result = append(result, PreflightCheck{Type: fmt.Sprintf("Check%d", i), Validate: func() error { return nil }})
		}
		return result
	}

	infra := snoInfra("")
	for _, tc := range []struct {
		name      string
		checks    []PreflightCheck
		change    func([]TransitionDescriptor) []TransitionDescriptor
		wantError string
	}{
		{name: "32 conditions including availability", checks: checks(31)},
		{name: "too many checks", checks: checks(32), wantError: "at most 31 preflight checks"},
		{name: "duplicate name", checks: []PreflightCheck{{Type: "Repeated", Validate: func() error { return nil }}, {Type: "Repeated", Validate: func() error { return nil }}}, wantError: "duplicate preflight condition type"},
		{name: "reserved name", checks: []PreflightCheck{{Type: configv1.TopologyTransitionAvailableConditionType, Validate: func() error { return nil }}}, wantError: "duplicate preflight condition type"},
		{name: "empty name", checks: []PreflightCheck{{Validate: func() error { return nil }}}, wantError: "invalid preflight condition type"},
		{name: "invalid name", checks: []PreflightCheck{{Type: "bad type", Validate: func() error { return nil }}}, wantError: "invalid preflight condition type"},
		{name: "missing validator", checks: []PreflightCheck{{Type: "Missing"}}, wantError: "has no validator"},
		{name: "multiple matches", change: func(d []TransitionDescriptor) []TransitionDescriptor { return append(d, d[0]) }, wantError: "more than one supported transition matches"},
		{name: "missing updater", change: func(d []TransitionDescriptor) []TransitionDescriptor { d[0].UpdateStatus = nil; return d }, wantError: "has no status updater"},
		{name: "invalid target", change: func(d []TransitionDescriptor) []TransitionDescriptor {
			d[0].UpdateStatus = func(i *configv1.Infrastructure) { i.Status.InfrastructureTopology = "Invalid" }
			return d
		}, wantError: "invalid source or target topology"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			descriptors := noopTransitions()
			if tc.change != nil {
				descriptors = tc.change(descriptors)
			}
			ctrl := &TopologyTransitionController{transitions: descriptors, preflightChecks: tc.checks, clock: clocktesting.NewFakePassiveClock(evaluationTime)}

			transitions, condition, err := ctrl.evaluateTransitions(infra)

			if tc.wantError != "" {
				if assert.Error(t, err) {
					assert.Contains(t, err.Error(), tc.wantError)
				}
				assert.Empty(t, transitions)
				assert.Equal(t, metav1.ConditionFalse, condition.Status)
				assert.Equal(t, "TransitionEvaluationFailed", condition.Reason)
				return
			}

			if !assert.NoError(t, err) || !assert.Len(t, transitions, 1) {
				return
			}
			assert.Len(t, transitions[0].Evaluations, 32)
			assert.NotNil(t, meta.FindStatusCondition(transitions[0].Evaluations, configv1.TopologyTransitionAvailableConditionType))
		})
	}
}

type failingNodeLister struct {
	corev1listers.NodeLister
	err error
}

func (l failingNodeLister) List(labels.Selector) ([]*corev1.Node, error) {
	return nil, l.err
}

func TestEvaluateTransitionsReadFailureAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		name  string
		prior bool
		mixed bool
	}{
		{name: "first evaluation"},
		{name: "after successful evaluation", prior: true},
		{name: "mixed blockers and read failure", prior: true, mixed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			infra := snoInfra("")
			fixture := readyFixture()
			ctrl := fixture.newController(infra, nil)
			ctrl.clock = clocktesting.NewFakePassiveClock(evaluationTime)

			if tc.prior {
				transitions, condition, err := ctrl.evaluateTransitions(infra)
				if !assert.NoError(t, err) || !assert.Len(t, transitions, 1) {
					return
				}
				infra.Status.TopologyTransitionStatus = configv1.TopologyTransitionStatus{Transitions: transitions, Conditions: []metav1.Condition{condition}}
			}
			if tc.mixed {
				fixture.withEtcdCR(false, true).withClusterVersion(true)
			}

			readErr := errors.New("node cache unavailable")
			fixture.nodeLister = failingNodeLister{NodeLister: fixture.nodeLister, err: readErr}
			ctrl.transitions = fixture.buildTransitions()
			calls := 0
			ctrl.transitions[0].PreflightValidators = append(ctrl.transitions[0].PreflightValidators, PreflightCheck{
				Type: "LastCheck", Validate: func() error { calls++; return nil },
			})
			before := infra.DeepCopy()

			transitions, condition, err := ctrl.evaluateTransitions(infra)

			assert.Empty(t, transitions, "never report partial results or old entries")
			assert.ErrorIs(t, err, readErr)
			var typed *clusterStateReadError
			assert.ErrorAs(t, err, &typed)
			assert.Equal(t, configv1.TopologyTransitionsEvaluatedConditionType, condition.Type)
			assert.Equal(t, metav1.ConditionFalse, condition.Status)
			assert.Equal(t, "TransitionEvaluationFailed", condition.Reason)
			assert.Contains(t, condition.Message, "node cache unavailable")
			assert.Equal(t, 1, calls, "all checks must run even after read errors")
			assert.Equal(t, before, infra, "evaluation only returns a result; status writes are separate")

			requestInfra := snoInfra(configv1.HighlyAvailableTopologyMode)
			descriptor, matchErr := findTransition(requestInfra, ctrl.transitions)
			if !assert.NoError(t, matchErr) {
				return
			}
			requestErr := validatePreflight(ctrl.preflightChecks, descriptor)
			assert.ErrorIs(t, requestErr, readErr)
			if tc.mixed {
				assert.Contains(t, requestErr.Error(), "cluster upgrade is in progress")
				assert.Contains(t, requestErr.Error(), "etcd does not have quorum")
			}

			fixture.nodeLister = fixture.nodeLister.(failingNodeLister).NodeLister
			fixture.withEtcdCR(true, false).withClusterVersion(false)
			ctrl.transitions = fixture.buildTransitions()

			transitions, condition, err = ctrl.evaluateTransitions(infra)
			if !assert.NoError(t, err) || !assert.Len(t, transitions, 1) {
				return
			}
			assert.Equal(t, metav1.ConditionTrue, condition.Status)
			assert.Equal(t, "EvaluationComplete", condition.Reason)
			for _, check := range transitions[0].Evaluations {
				assert.Equal(t, metav1.ConditionTrue, check.Status, check.Type)
			}
		})
	}
}

func TestEvaluateTransitionsMissingRequiredState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		remove  func(*testFixture) error
		message string
	}{
		{name: "etcd CR", remove: func(f *testFixture) error { return f.etcdIndexer.Delete(newTestEtcdCR(true, false)) }, message: "failed to get etcd operator CR"},
		{name: "etcd endpoints", remove: func(f *testFixture) error { return f.cmIndexer.Delete(newTestEtcdEndpointsConfigMap(3)) }, message: "failed to get openshift-etcd/etcd-endpoints ConfigMap"},
		{name: "cluster version", remove: func(f *testFixture) error { return f.cvIndexer.Delete(newTestClusterVersion(false)) }, message: "failed to get clusterversions"},
		{name: "cluster operators", remove: func(f *testFixture) error {
			f.coLister = testClusterOperatorLister{err: errors.New("cluster operator cache unavailable")}
			return nil
		}, message: "failed to check cluster operator stability"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := readyFixture()
			if !assert.NoError(t, tc.remove(fixture)) {
				return
			}
			infra := snoInfra("")
			ctrl := fixture.newController(infra, nil)

			transitions, condition, err := ctrl.evaluateTransitions(infra)

			var readErr *clusterStateReadError
			assert.ErrorAs(t, err, &readErr)
			assert.Empty(t, transitions)
			assert.Equal(t, metav1.ConditionFalse, condition.Status)
			assert.Equal(t, "TransitionEvaluationFailed", condition.Reason)
			assert.Contains(t, condition.Message, tc.message)
		})
	}
}

func TestEvaluateTransitionsNoMatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*configv1.Infrastructure)
	}{
		{name: "unsupported platform", change: func(i *configv1.Infrastructure) { i.Status.PlatformStatus.Type = configv1.AWSPlatformType }},
		{name: "missing platform", change: func(i *configv1.Infrastructure) { i.Status.PlatformStatus = nil }},
		{name: "unsupported control plane", change: func(i *configv1.Infrastructure) { i.Status.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode }},
		{name: "unsupported infrastructure", change: func(i *configv1.Infrastructure) {
			i.Status.InfrastructureTopology = configv1.HighlyAvailableTopologyMode
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			infra := snoInfra(configv1.HighlyAvailableTopologyMode)
			tc.change(infra)
			infra.Generation = 9
			ctrl := &TopologyTransitionController{
				transitions: noopTransitions(), clock: clocktesting.NewFakePassiveClock(evaluationTime),
				preflightChecks: []PreflightCheck{{Type: "MustNotRun", Validate: func() error {
					t.Error("checks should not run without a supported transition")
					return nil
				}}},
			}

			transitions, condition, err := ctrl.evaluateTransitions(infra)

			assert.NoError(t, err)
			assert.Empty(t, transitions)
			assert.Equal(t, configv1.TopologyTransitionsEvaluatedConditionType, condition.Type)
			assert.Equal(t, metav1.ConditionTrue, condition.Status)
			assert.Equal(t, "NoSupportedTransitions", condition.Reason)
			assert.Contains(t, condition.Message, "No supported topology transition matches")
			assert.Contains(t, condition.Message, string(infra.Status.ControlPlaneTopology))
			assert.Equal(t, int64(9), condition.ObservedGeneration)
			assert.Equal(t, metav1.NewTime(evaluationTime), condition.LastTransitionTime)
		})
	}
}

func TestEvaluateTransitionsUsesActualSourceAndUpdaterCopy(t *testing.T) {
	infra := snoInfra("")
	infra.Status.InfrastructureTopology = configv1.HighlyAvailableTopologyMode
	infra.Annotations = map[string]string{"keep": "original"}
	before := infra.DeepCopy()
	descriptors := []TransitionDescriptor{{
		From: configv1.InfrastructureStatus{ControlPlaneTopology: configv1.SingleReplicaTopologyMode},
		To:   configv1.InfrastructureSpec{ControlPlaneTopology: configv1.HighlyAvailableTopologyMode},
		UpdateStatus: func(target *configv1.Infrastructure) {
			target.Status.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
			target.Status.PlatformStatus.Type = configv1.AWSPlatformType
			target.Annotations["keep"] = "changed"
		},
	}}
	ctrl := &TopologyTransitionController{transitions: descriptors, clock: clocktesting.NewFakePassiveClock(evaluationTime)}

	transitions, _, err := ctrl.evaluateTransitions(infra)
	if !assert.NoError(t, err) || !assert.Len(t, transitions, 1) {
		return
	}
	assert.Equal(t, before, infra)
	assert.Equal(t, configv1.TopologyState{ControlPlaneTopology: configv1.SingleReplicaTopologyMode, InfrastructureTopology: configv1.HighlyAvailableTopologyMode}, transitions[0].Source)
	assert.Equal(t, configv1.TopologyState{ControlPlaneTopology: configv1.HighlyAvailableTopologyMode, InfrastructureTopology: configv1.HighlyAvailableTopologyMode}, transitions[0].Target)
	assert.Len(t, transitions[0].Evaluations, 1, "availability is required even with no checks")
	assert.Equal(t, configv1.TopologyTransitionAvailableConditionType, transitions[0].Evaluations[0].Type)
}

func TestEvaluateTransitionsBlockersAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		block   func(*testFixture)
		blocked map[string]string
	}{
		{
			name: "node not ready",
			block: func(f *testFixture) {
				f.withNodes(newTestDualRoleNodeWithConditions("master-2", false, notReadyNodeCondition()))
			},
			blocked: map[string]string{"ControlPlaneNodesReady": "insufficient ready control plane nodes: need 3, have 2"},
		},
		{
			name:    "etcd quorum lost",
			block:   func(f *testFixture) { f.withEtcdCR(false, false) },
			blocked: map[string]string{"EtcdQuorumAvailable": "etcd does not have quorum: EtcdMembersAvailable condition is not True"},
		},
		{
			name: "multiple blockers",
			block: func(f *testFixture) {
				f.withNodes(newTestDualRoleNodeWithConditions("master-2", true, notReadyNodeCondition())).
					withEtcdCR(false, true).withEtcdEndpoints(1).withClusterVersion(true)
			},
			blocked: map[string]string{
				"ControlPlaneNodesReady":            "insufficient ready control plane nodes: need 3, have 2",
				"ControlPlaneNodesSchedulable":      "insufficient schedulable control plane nodes: need 3, have 2",
				"EtcdQuorumAvailable":               "etcd does not have quorum: EtcdMembersAvailable condition is not True",
				"EtcdNotProgressing":                "etcd is still progressing: 1 member has not started",
				"EtcdVotingMembersSatisfied":        "insufficient etcd voting members: need 3, have 1",
				"NoClusterVersionUpgradeInProgress": "cluster upgrade is in progress: clusterversions.config.openshift.io/version has Progressing=True",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := readyFixture()
			tc.block(fixture)
			infra := snoInfra("")
			ctrl := fixture.newController(infra, nil)
			ctrl.clock = clocktesting.NewFakePassiveClock(evaluationTime)

			transitions, condition, err := ctrl.evaluateTransitions(infra)
			if !assert.NoError(t, err) || !assert.Len(t, transitions, 1) {
				return
			}
			assert.Equal(t, metav1.ConditionTrue, condition.Status)
			assert.Equal(t, "EvaluationComplete", condition.Reason)

			evaluations := transitions[0].Evaluations
			assert.Len(t, evaluations, 11, "all checks must run, including after a blocker")
			available := meta.FindStatusCondition(evaluations, configv1.TopologyTransitionAvailableConditionType)
			if !assert.NotNil(t, available) {
				return
			}
			assert.Equal(t, metav1.ConditionFalse, available.Status)
			assert.Equal(t, "PreflightCheckFailed", available.Reason)
			assert.Equal(t, "One or more preflight checks failed. See the other evaluation conditions with status False for details.", available.Message)

			seen := map[string]bool{}
			for _, check := range evaluations {
				assert.False(t, seen[check.Type], "duplicate condition %s", check.Type)
				seen[check.Type] = true
				if check.Type == configv1.TopologyTransitionAvailableConditionType {
					continue
				}

				if message, blocked := tc.blocked[check.Type]; blocked {
					assert.Equal(t, metav1.ConditionFalse, check.Status, check.Type)
					assert.Equal(t, "PreflightCheckFailed", check.Reason)
					assert.Equal(t, message, check.Message)
					assert.NotContains(t, available.Message, message)
				} else {
					assert.Equal(t, metav1.ConditionTrue, check.Status, check.Type)
				}
			}
			for checkType := range tc.blocked {
				assert.True(t, seen[checkType], "missing check %s", checkType)
			}

			again, againCondition, err := ctrl.evaluateTransitions(infra)
			assert.NoError(t, err)
			assert.Equal(t, transitions, again)
			assert.Equal(t, condition, againCondition)

			fixture.withNodes(newTestDualRoleNodeWithConditions("master-2", false, readyNodeCondition())).
				withEtcdCR(true, false).withEtcdEndpoints(3).withClusterVersion(false)

			transitions, condition, err = ctrl.evaluateTransitions(infra)
			if !assert.NoError(t, err) || !assert.Len(t, transitions, 1) {
				return
			}
			assert.Equal(t, metav1.ConditionTrue, condition.Status)
			for _, check := range transitions[0].Evaluations {
				assert.Equal(t, metav1.ConditionTrue, check.Status, check.Type)
			}
		})
	}
}
