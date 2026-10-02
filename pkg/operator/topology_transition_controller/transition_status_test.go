package topology_transition_controller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	configfakeclient "github.com/openshift/client-go/config/clientset/versioned/fake"
	applyoperatorv1 "github.com/openshift/client-go/operator/applyconfigurations/operator/v1"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	"github.com/stretchr/testify/assert"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/util/retry"
	clocktesting "k8s.io/utils/clock/testing"
)

func statusTestController(infra *configv1.Infrastructure) (*TopologyTransitionController, *configfakeclient.Clientset, *clocktesting.FakePassiveClock) {
	clk := clocktesting.NewFakePassiveClock(evaluationTime)
	c := newTestControllerWithClock(infra, []operatorv1.OperatorCondition{{Type: upgradeableCondition, Status: operatorv1.ConditionTrue}}, nil, noopTransitions(), clk)
	client := configfakeclient.NewSimpleClientset(infra)
	c.infraClient = client.ConfigV1().Infrastructures()

	return c, client, clk
}

type failingOperatorStatusClient struct {
	v1helpers.OperatorClient
	err error
}

func (c failingOperatorStatusClient) UpdateOperatorStatus(context.Context, string, *operatorv1.OperatorStatus) (*operatorv1.OperatorStatus, error) {
	return nil, c.err
}

func TestDiscoveryFailureDoesNotStopReconciliation(t *testing.T) {
	for _, writeFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "read error", true: "read and write errors"}[writeFails], func(t *testing.T) {
			c, client, _ := statusTestController(snoInfra(configv1.SingleReplicaTopologyMode))
			c.operatorClient = v1helpers.NewFakeOperatorClient(&operatorv1.OperatorSpec{}, &operatorv1.OperatorStatus{Conditions: transitionInProgressConditionsAt(evaluationTime.Add(-time.Hour))}, nil)
			readErr := errors.New("node API unavailable")
			writeErr := errors.New("infrastructure status unavailable")
			c.preflightChecks = []PreflightCheck{{Type: "NodesReadable", Validate: validateControlPlaneNodeCount(3, failingNodeLister{err: readErr})}}
			checked := 0
			c.transitions[0].To = configv1.InfrastructureSpec{}
			c.transitions[0].TransitionValidators = []TransitionValidatorFunc{func() error { checked++; return nil }}
			if writeFails {
				client.PrependReactor("update", "infrastructures", func(clienttesting.Action) (bool, runtime.Object, error) {
					return true, nil, writeErr
				})
			}

			evaluationErr := c.syncEvaluation(t.Context())
			err := c.sync(t.Context(), newTestSyncContext())

			assert.ErrorIs(t, evaluationErr, readErr)
			assert.Equal(t, 1, checked, "discovery failure must not skip active checks")
			if writeFails {
				assert.ErrorIs(t, err, writeErr)
			} else {
				status := currentInfra(t, c).Status.TopologyTransitionStatus
				if assert.NotNil(t, status) {
					assert.Equal(t, metav1.ConditionFalse, status.Conditions[0].Status)
					assert.Empty(t, status.Transitions)
				}
				assert.NoError(t, err)
				assert.Equal(t, metav1.ConditionTrue, completionCondition(t, c).Status)
			}
		})
	}
}

func TestDiscoveryFailureOwnsSeparateDegradedConditionAndRecovers(t *testing.T) {
	c, _, _ := statusTestController(snoInfra(""))
	readErr := errors.New("node API unavailable")
	var failure error = &clusterStateReadError{err: readErr}
	c.preflightChecks = []PreflightCheck{{Type: "NodesReadable", Validate: func() error { return failure }}}
	c.operatorClient = v1helpers.NewFakeOperatorClient(&operatorv1.OperatorSpec{}, &operatorv1.OperatorStatus{Conditions: []operatorv1.OperatorCondition{{
		Type: "TopologyTransitionControllerDegraded", Status: operatorv1.ConditionTrue, Reason: "SyncError",
	}}}, nil)

	assert.ErrorIs(t, c.syncEvaluation(t.Context()), readErr)
	_, status, _, err := c.operatorClient.GetOperatorState()
	assert.NoError(t, err)
	assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, evaluationDegradedCondition))
	assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, "TopologyTransitionControllerDegraded"))

	failure = nil
	assert.NoError(t, c.syncEvaluation(t.Context()))
	_, status, _, err = c.operatorClient.GetOperatorState()
	assert.NoError(t, err)
	assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, evaluationDegradedCondition))
	assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, "TopologyTransitionControllerDegraded"))
}

type reportingOperatorClient struct {
	v1helpers.OperatorClient
	reports chan<- *operatorv1.OperatorStatus
}

func (c reportingOperatorClient) ApplyOperatorStatus(ctx context.Context, manager string, configuration *applyoperatorv1.OperatorStatusApplyConfiguration) error {
	if err := c.OperatorClient.ApplyOperatorStatus(ctx, manager, configuration); err != nil {
		return err
	}

	_, status, _, err := c.OperatorClient.GetOperatorState()
	if err == nil {
		c.reports <- status.DeepCopy()
	}

	return err
}

func TestFreshInfrastructureReadFailureStillRunsActiveChecks(t *testing.T) {
	infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
	c, client, _ := statusTestController(infra)
	c.operatorClient = v1helpers.NewFakeOperatorClient(&operatorv1.OperatorSpec{}, &operatorv1.OperatorStatus{Conditions: transitionInProgressConditionsAt(evaluationTime.Add(-time.Hour))}, nil)
	checked := 0
	c.transitions[0].TransitionValidators = []TransitionValidatorFunc{func() error { checked++; return nil }}
	readErr := errors.New("infrastructure API unavailable")
	client.PrependReactor("get", "infrastructures", func(clienttesting.Action) (bool, runtime.Object, error) { return true, nil, readErr })

	assert.ErrorIs(t, c.sync(context.Background(), newTestSyncContext()), readErr)
	assert.Equal(t, 1, checked)
	assert.Equal(t, 0, statusWrites(client))
}

func TestDiscoveryConditionTimesAndRecovery(t *testing.T) {
	infra := snoInfra("")
	c, client, clk := statusTestController(infra)
	var failure error
	c.preflightChecks = []PreflightCheck{
		{Type: "ChangingCheck", Validate: func() error { return failure }},
		{Type: "StableCheck", Validate: func() error { return nil }},
	}

	assert.NoError(t, c.syncEvaluation(t.Context()))

	first := currentInfra(t, c)
	if !assert.NotNil(t, first.Status.TopologyTransitionStatus) {
		return
	}

	clk.SetTime(evaluationTime.Add(time.Minute))
	failure = errors.New("node is not ready")

	assert.NoError(t, c.syncEvaluation(t.Context()), "known blocker is not an evaluation error")

	blocked := currentInfra(t, c).Status.TopologyTransitionStatus
	if !assert.Len(t, blocked.Transitions, 1) {
		return
	}
	assert.Equal(t, metav1.ConditionTrue, blocked.Conditions[0].Status)
	assert.Equal(t, metav1.NewTime(evaluationTime), blocked.Conditions[0].LastTransitionTime)
	for _, condition := range blocked.Transitions[0].Evaluations {
		if condition.Type == "StableCheck" {
			assert.Equal(t, metav1.NewTime(evaluationTime), condition.LastTransitionTime)
		} else {
			assert.Equal(t, metav1.ConditionFalse, condition.Status)
			assert.Equal(t, metav1.NewTime(clk.Now()), condition.LastTransitionTime)
		}
	}

	clk.SetTime(evaluationTime.Add(2 * time.Minute))
	failure = errors.New("node is still not ready")
	changed := currentInfra(t, c)
	changed.Generation = 9
	_, err := c.infraClient.Update(context.Background(), changed, metav1.UpdateOptions{})
	assert.NoError(t, err)

	assert.NoError(t, c.syncEvaluation(t.Context()))

	reasonOnly := currentInfra(t, c).Status.TopologyTransitionStatus
	for i, condition := range reasonOnly.Transitions[0].Evaluations {
		assert.Equal(t, blocked.Transitions[0].Evaluations[i].LastTransitionTime, condition.LastTransitionTime)
		assert.Equal(t, int64(9), condition.ObservedGeneration)
	}
	assert.Equal(t, blocked.Conditions[0].LastTransitionTime, reasonOnly.Conditions[0].LastTransitionTime)

	clk.SetTime(evaluationTime.Add(3 * time.Minute))
	readErr := errors.New("cannot read nodes")
	failure = &clusterStateReadError{err: readErr}

	assert.ErrorIs(t, c.syncEvaluation(t.Context()), readErr)

	failed := currentInfra(t, c).Status.TopologyTransitionStatus
	assert.Empty(t, failed.Transitions)
	assert.Equal(t, metav1.ConditionFalse, failed.Conditions[0].Status)
	assert.Equal(t, metav1.NewTime(clk.Now()), failed.Conditions[0].LastTransitionTime)

	writes := statusWrites(client)
	clk.SetTime(evaluationTime.Add(4 * time.Minute))
	assert.ErrorIs(t, c.syncEvaluation(t.Context()), readErr)
	assert.Equal(t, writes, statusWrites(client), "unchanged read failures must not churn")
	assert.Equal(t, failed, currentInfra(t, c).Status.TopologyTransitionStatus)

	failure = &clusterStateReadError{err: errors.New("another read error")}
	assert.Error(t, c.syncEvaluation(t.Context()))
	assert.Equal(t, failed.Conditions[0].LastTransitionTime, currentInfra(t, c).Status.TopologyTransitionStatus.Conditions[0].LastTransitionTime)

	failure = nil
	assert.NoError(t, c.syncEvaluation(t.Context()))

	recovered := currentInfra(t, c).Status.TopologyTransitionStatus
	assert.Equal(t, metav1.ConditionTrue, recovered.Conditions[0].Status)
	assert.Equal(t, metav1.NewTime(clk.Now()), recovered.Conditions[0].LastTransitionTime)
	if assert.Len(t, recovered.Transitions, 1) {
		for _, condition := range recovered.Transitions[0].Evaluations {
			assert.Equal(t, metav1.NewTime(clk.Now()), condition.LastTransitionTime, "cleared entries get new timestamps")
		}
	}
}

func TestDiscoveryNormalizesEmptyTransitions(t *testing.T) {
	infra := snoInfra("")
	infra.Status.PlatformStatus.Type = configv1.AWSPlatformType
	c, client, clk := statusTestController(infra)

	assert.NoError(t, c.syncEvaluation(t.Context()))

	first := currentInfra(t, c)
	data, err := json.Marshal(first.Status.TopologyTransitionStatus)
	assert.NoError(t, err)
	assert.NotContains(t, string(data), "transitions")
	assert.NotEqual(t, "{}", string(data))
	assert.Contains(t, string(data), configv1.TopologyTransitionsEvaluatedConditionType)

	var roundTrip configv1.TopologyTransitionStatus
	assert.NoError(t, json.Unmarshal(data, &roundTrip))
	assert.Nil(t, roundTrip.Transitions)

	// An empty in-memory list and an omitted list must compare alike.
	first.Status.TopologyTransitionStatus.Transitions = []configv1.TopologyTransition{}
	_, err = c.infraClient.UpdateStatus(context.Background(), first, metav1.UpdateOptions{})
	assert.NoError(t, err)
	client.ClearActions()
	clk.SetTime(evaluationTime.Add(time.Hour))

	assert.NoError(t, c.syncEvaluation(t.Context()))
	assert.Equal(t, 0, statusWrites(client))
}

func TestDiscoveryConflictReevaluatesAndPreservesOtherFields(t *testing.T) {
	for _, changeSource := range []bool{false, true} {
		t.Run(map[bool]string{false: "same source", true: "new source"}[changeSource], func(t *testing.T) {
			infra := snoInfra("")
			infra.ResourceVersion = "1"
			c, client, _ := statusTestController(infra)
			completion := metav1.Condition{Type: configv1.TopologyTransitionCompletedConditionType, Status: metav1.ConditionFalse, Reason: "OtherOwner", Message: "keep me", LastTransitionTime: metav1.NewTime(evaluationTime.Add(-time.Hour))}
			attempts := 0
			client.PrependReactor("update", "infrastructures", func(action clienttesting.Action) (bool, runtime.Object, error) {
				attempts++
				update := action.(clienttesting.UpdateAction).GetObject().(*configv1.Infrastructure)
				assert.Equal(t, "status", action.GetSubresource())
				if attempts == 1 {
					newer := infra.DeepCopy()
					newer.ResourceVersion = "2"
					newer.Generation = 5
					newer.Spec.CloudConfig.Name = "concurrent-config"
					newer.Status.APIServerURL = "https://concurrent.example"
					newer.Status.TopologyTransitionStatus = &configv1.TopologyTransitionStatus{Conditions: []metav1.Condition{completion}}
					if changeSource {
						newer.Status.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
						newer.Status.InfrastructureTopology = configv1.HighlyAvailableTopologyMode
					}
					assert.NoError(t, client.Tracker().Update(configv1.SchemeGroupVersion.WithResource("infrastructures"), newer, ""))
					return true, nil, apierrors.NewConflict(schema.GroupResource{Group: configv1.GroupName, Resource: "infrastructures"}, "cluster", errors.New("concurrent update"))
				}
				assert.Equal(t, "2", update.ResourceVersion)
				return false, nil, nil
			})

			assert.NoError(t, c.syncEvaluation(t.Context()))

			updated := currentInfra(t, c)
			assert.Equal(t, 2, attempts)
			assert.Equal(t, "concurrent-config", updated.Spec.CloudConfig.Name)
			assert.Equal(t, "https://concurrent.example", updated.Status.APIServerURL)

			status := updated.Status.TopologyTransitionStatus
			if !assert.NotNil(t, status) {
				return
			}
			assert.Equal(t, &completion, meta.FindStatusCondition(status.Conditions, completion.Type))
			assert.Len(t, status.Conditions, 2)
			condition := meta.FindStatusCondition(status.Conditions, configv1.TopologyTransitionsEvaluatedConditionType)
			if assert.NotNil(t, condition) {
				assert.Equal(t, int64(5), condition.ObservedGeneration)
			}
			if changeSource {
				assert.Empty(t, status.Transitions)
				assert.Equal(t, "NoSupportedTransitions", condition.Reason)
			} else {
				assert.Len(t, status.Transitions, 1)
			}
		})
	}
}

func TestDiscoveryReadFailurePreservesCompletionAndOtherFields(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "no conflict", true: "conflict with newer completion"}[conflict], func(t *testing.T) {
			fixture := readyFixture()
			infra := snoInfra("")
			cloudConfig := "original-config"
			infra.Spec.CloudConfig.Name = cloudConfig
			c, client, clk := statusTestController(infra)
			c.preflightChecks = fixture.buildPreflightChecks()
			c.transitions = fixture.buildTransitions()

			if !assert.NoError(t, c.syncEvaluation(t.Context())) {
				return
			}

			seed := currentInfra(t, c)
			completion := metav1.Condition{
				Type: configv1.TopologyTransitionCompletedConditionType, Status: metav1.ConditionFalse,
				Reason: "OtherOwner", Message: "original completion", ObservedGeneration: seed.Generation,
				LastTransitionTime: metav1.NewTime(evaluationTime.Add(-time.Hour)),
			}
			serverURL := "https://original.example"
			seed.Status.APIServerURL = serverURL
			seed.Status.TopologyTransitionStatus.Conditions = append(seed.Status.TopologyTransitionStatus.Conditions, completion)
			if _, err := c.infraClient.UpdateStatus(context.Background(), seed, metav1.UpdateOptions{}); !assert.NoError(t, err) {
				return
			}

			readErr := errors.New("node cache unavailable")
			originalLister := fixture.nodeLister
			fixture.nodeLister = failingNodeLister{NodeLister: originalLister, err: readErr}
			c.transitions = fixture.buildTransitions()
			attempts := 0
			client.PrependReactor("update", "infrastructures", func(action clienttesting.Action) (bool, runtime.Object, error) {
				attempts++
				if !conflict || attempts != 1 {
					return false, nil, nil
				}
				assert.Equal(t, "status", action.GetSubresource())
				newer := seed.DeepCopy()
				newer.ResourceVersion = "2"
				newer.Generation = 9
				serverURL = "https://newer.example"
				cloudConfig = "newer-config"
				newer.Status.APIServerURL = serverURL
				newer.Spec.CloudConfig.Name = cloudConfig
				completion.Status = metav1.ConditionTrue
				completion.Message = "newer completion"
				completion.ObservedGeneration = newer.Generation
				completion.LastTransitionTime = metav1.NewTime(evaluationTime)
				*meta.FindStatusCondition(newer.Status.TopologyTransitionStatus.Conditions, completion.Type) = completion
				assert.NoError(t, client.Tracker().Update(configv1.SchemeGroupVersion.WithResource("infrastructures"), newer, ""))
				return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "infrastructures"}, "cluster", errors.New("completion changed"))
			})
			clk.SetTime(evaluationTime.Add(time.Minute))

			assert.ErrorIs(t, c.syncEvaluation(t.Context()), readErr)
			assert.Equal(t, map[bool]int{false: 1, true: 2}[conflict], attempts)

			failed := currentInfra(t, c)
			if !assert.NotNil(t, failed.Status.TopologyTransitionStatus) {
				return
			}
			assert.Empty(t, failed.Status.TopologyTransitionStatus.Transitions)
			evaluated := meta.FindStatusCondition(failed.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionsEvaluatedConditionType)
			if assert.NotNil(t, evaluated) {
				assert.Equal(t, metav1.ConditionFalse, evaluated.Status)
				assert.Equal(t, "TransitionEvaluationFailed", evaluated.Reason)
				assert.Equal(t, failed.Generation, evaluated.ObservedGeneration)
			}

			fixture.nodeLister = originalLister
			c.transitions = fixture.buildTransitions()
			clk.SetTime(evaluationTime.Add(2 * time.Minute))

			assert.NoError(t, c.syncEvaluation(t.Context()))

			recovered := currentInfra(t, c)
			if !assert.NotNil(t, recovered.Status.TopologyTransitionStatus) || !assert.Len(t, recovered.Status.TopologyTransitionStatus.Transitions, 1) {
				return
			}
			evaluated = meta.FindStatusCondition(recovered.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionsEvaluatedConditionType)
			if assert.NotNil(t, evaluated) {
				assert.Equal(t, metav1.ConditionTrue, evaluated.Status)
				assert.Equal(t, "EvaluationComplete", evaluated.Reason)
			}

			for _, stored := range []*configv1.Infrastructure{failed, recovered} {
				assert.Equal(t, serverURL, stored.Status.APIServerURL)
				assert.Equal(t, cloudConfig, stored.Spec.CloudConfig.Name)
				conditions := stored.Status.TopologyTransitionStatus.Conditions
				assert.Equal(t, &completion, meta.FindStatusCondition(conditions, completion.Type))
				var types []string
				for _, condition := range conditions {
					types = append(types, condition.Type)
				}
				assert.ElementsMatch(t, []string{configv1.TopologyTransitionsEvaluatedConditionType, configv1.TopologyTransitionCompletedConditionType}, types, "only the two allowed top-level types may be stored")
			}
		})
	}
}

func currentInfra(t *testing.T, c *TopologyTransitionController) *configv1.Infrastructure {
	t.Helper()

	infra, err := c.infraClient.Get(context.Background(), "cluster", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}

	return infra
}

func statusWrites(client *configfakeclient.Clientset) int {
	count := 0
	for _, action := range client.Actions() {
		if action.GetVerb() == "update" && action.GetSubresource() == "status" {
			count++
		}
	}

	return count
}

func TestApplyTransitionStatusConditionOnly(t *testing.T) {
	for _, tc := range []struct {
		name        string
		unchanged   bool
		conflict    bool
		change      func(*configv1.Infrastructure)
		wantApplied bool
		wantWrites  int
	}{
		{name: "writes only completion", wantApplied: true, wantWrites: 1},
		{name: "already-set completion succeeds without a write", unchanged: true, wantApplied: true},
		{name: "conflict preserves newer status and discovery", conflict: true, wantApplied: true, wantWrites: 2},
		{name: "conflict skips changed spec", conflict: true, wantWrites: 1, change: func(infra *configv1.Infrastructure) {
			infra.Spec.CloudConfig.Name = "new-config"
		}},
		{name: "conflict skips changed control plane source", conflict: true, wantWrites: 1, change: func(infra *configv1.Infrastructure) {
			infra.Status.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
		}},
		{name: "conflict skips changed infrastructure source", conflict: true, wantWrites: 1, change: func(infra *configv1.Infrastructure) {
			infra.Status.InfrastructureTopology = configv1.HighlyAvailableTopologyMode
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			infra := snoInfra(configv1.HighlyAvailableTopologyMode)
			infra.ResourceVersion = "1"
			infra.Generation = 3
			c, client, clk := statusTestController(infra)
			assert.NoError(t, c.mergeDiscovery(infra))
			condition := metav1.Condition{
				Type: configv1.TopologyTransitionCompletedConditionType, Status: metav1.ConditionTrue,
				Reason: "TopologyTransitionComplete", Message: "Topology transition reconciliation complete",
			}
			initial := condition
			if !tc.unchanged {
				initial.Status = metav1.ConditionFalse
				initial.Reason = reasonTopologyTransitionInProgress
			}
			c.mergeTransitionCondition(infra, initial)
			resource := configv1.SchemeGroupVersion.WithResource("infrastructures")
			assert.NoError(t, client.Tracker().Update(resource, infra, ""))

			before := infra.DeepCopy()
			live := infra.DeepCopy()
			clk.SetTime(evaluationTime.Add(time.Hour))
			c.preflightChecks = []PreflightCheck{{Type: "MustNotEvaluate", Validate: func() error {
				t.Error("condition-only writes must not evaluate discovery")
				return errors.New("unexpected discovery evaluation")
			}}}
			c.transitions[0].UpdateStatus = func(*configv1.Infrastructure) {
				t.Error("condition-only writes must not run a topology updater")
			}
			attempts := 0
			client.PrependReactor("update", "infrastructures", func(action clienttesting.Action) (bool, runtime.Object, error) {
				assert.Equal(t, "status", action.GetSubresource())
				attempts++
				if !tc.conflict || attempts != 1 {
					update := action.(clienttesting.UpdateAction).GetObject().(*configv1.Infrastructure)
					assert.Equal(t, live.ResourceVersion, update.ResourceVersion)
					return false, nil, nil
				}
				live.ResourceVersion = "2"
				live.Generation = 9
				live.Status.APIServerURL = "https://newer.example"
				live.Status.PlatformStatus.Type = configv1.AWSPlatformType
				live.Status.TopologyTransitionStatus.Conditions[0].Message = "newer discovery"
				live.Status.TopologyTransitionStatus.Transitions[0].Evaluations[0].Message = "newer evaluation"
				if tc.change != nil {
					tc.change(live)
				}
				assert.NoError(t, client.Tracker().Update(resource, live, ""))
				return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "infrastructures"}, "cluster", errors.New("condition conflict"))
			})

			applied, err := c.applyTransitionStatus(context.Background(), infra, nil, condition)
			assert.NoError(t, err)
			assert.Equal(t, tc.wantApplied, applied)
			assert.Equal(t, tc.wantWrites, statusWrites(client))

			want := live.DeepCopy()
			if tc.wantApplied {
				condition.ObservedGeneration = live.Generation
				condition.LastTransitionTime = metav1.NewTime(clk.Now())
				if tc.unchanged {
					condition.LastTransitionTime = metav1.NewTime(evaluationTime)
				}
				*meta.FindStatusCondition(want.Status.TopologyTransitionStatus.Conditions, condition.Type) = condition
			}

			stored := currentInfra(t, c)
			assert.Equal(t, want.Status, stored.Status, "only completion may change; keep live platform, discovery and other status fields")
			assert.Equal(t, live.Spec, stored.Spec)
			assert.Equal(t, before, infra, "expected Infrastructure must not change")
		})
	}
}

func TestEvaluationPublishesDiscoveryIdle(t *testing.T) {
	for _, spec := range []configv1.TopologyMode{"", configv1.SingleReplicaTopologyMode} {
		t.Run(string(spec), func(t *testing.T) {
			infra := snoInfra(spec)
			before := infra.DeepCopy()
			c, client, clk := statusTestController(infra)
			fixture := readyFixture()
			c.preflightChecks = fixture.buildPreflightChecks()
			c.transitions = fixture.buildTransitions()

			assert.NoError(t, c.syncEvaluation(t.Context()))

			first := currentInfra(t, c)
			if !assert.NotNil(t, first.Status.TopologyTransitionStatus) {
				return
			}
			assert.Len(t, first.Status.TopologyTransitionStatus.Transitions, 1)
			assert.Len(t, first.Status.TopologyTransitionStatus.Transitions[0].Evaluations, 11)
			condition := meta.FindStatusCondition(first.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionsEvaluatedConditionType)
			if !assert.NotNil(t, condition) {
				return
			}
			assert.Equal(t, metav1.ConditionTrue, condition.Status)
			assert.Equal(t, metav1.NewTime(evaluationTime), condition.LastTransitionTime)
			assert.Equal(t, before, infra, "cached Infrastructure must not change")
			assert.Equal(t, before.Spec, first.Spec)
			assert.Equal(t, before.Status.ControlPlaneTopology, first.Status.ControlPlaneTopology)
			assert.Equal(t, before.Status.InfrastructureTopology, first.Status.InfrastructureTopology)
			assert.Equal(t, 1, statusWrites(client))

			clk.SetTime(evaluationTime.Add(minReconciliationSoakTime))
			// Fake clients do not update the lister cache. Evaluation must use fresh status.
			assert.NoError(t, c.syncEvaluation(t.Context()))
			assert.Equal(t, first.Status, currentInfra(t, c).Status)
			assert.Equal(t, 1, statusWrites(client))
		})
	}
}

func TestDiscoveryRealBlockersRemainStableAndRecover(t *testing.T) {
	for _, tc := range []struct {
		name      string
		checkType string
		block     func(*testFixture)
	}{
		{name: "node not ready", checkType: "ControlPlaneNodesReady", block: func(f *testFixture) {
			f.withNodes(newTestDualRoleNodeWithConditions("master-2", false, notReadyNodeCondition()))
		}},
		{name: "etcd quorum lost", checkType: "EtcdQuorumAvailable", block: func(f *testFixture) { f.withEtcdCR(false, false) }},
		{name: "etcd progressing", checkType: "EtcdNotProgressing", block: func(f *testFixture) { f.withEtcdCR(true, true) }},
		{name: "too few voting members", checkType: "EtcdVotingMembersSatisfied", block: func(f *testFixture) { f.withEtcdEndpoints(1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := readyFixture()
			c, client, clk := statusTestController(snoInfra(""))
			c.preflightChecks = fixture.buildPreflightChecks()
			c.transitions = fixture.buildTransitions()

			assert.NoError(t, c.syncEvaluation(t.Context()))

			first := currentInfra(t, c).Status.TopologyTransitionStatus
			if !assert.NotNil(t, first) || !assert.Len(t, first.Transitions, 1) {
				return
			}
			assert.Equal(t, metav1.ConditionTrue, first.Conditions[0].Status)

			// Change the real indexers read by the production checks.
			tc.block(fixture)
			clk.SetTime(evaluationTime.Add(time.Minute))

			assert.NoError(t, c.syncEvaluation(t.Context()))

			blocked := currentInfra(t, c).Status.TopologyTransitionStatus
			assert.Equal(t, first.Conditions, blocked.Conditions)
			if !assert.Len(t, blocked.Transitions, 1) {
				return
			}
			assert.Len(t, blocked.Transitions[0].Evaluations, len(first.Transitions[0].Evaluations))
			assert.NotNil(t, meta.FindStatusCondition(blocked.Transitions[0].Evaluations, tc.checkType))
			assert.NotNil(t, meta.FindStatusCondition(blocked.Transitions[0].Evaluations, configv1.TopologyTransitionAvailableConditionType))
			for _, condition := range blocked.Transitions[0].Evaluations {
				if condition.Type == configv1.TopologyTransitionAvailableConditionType || condition.Type == tc.checkType {
					previous := meta.FindStatusCondition(first.Transitions[0].Evaluations, condition.Type)
					if assert.NotNil(t, previous) {
						assert.Equal(t, metav1.ConditionTrue, previous.Status)
					}
					assert.Equal(t, metav1.ConditionFalse, condition.Status)
					assert.Equal(t, metav1.NewTime(clk.Now()), condition.LastTransitionTime)
				} else {
					assert.Equal(t, meta.FindStatusCondition(first.Transitions[0].Evaluations, condition.Type), &condition)
				}
			}

			writes := statusWrites(client)
			assert.Equal(t, 2, writes)
			clk.SetTime(evaluationTime.Add(2 * time.Minute))
			assert.NoError(t, c.syncEvaluation(t.Context()))
			assert.Equal(t, writes, statusWrites(client), "unchanged blockers must not churn")
			assert.Equal(t, blocked, currentInfra(t, c).Status.TopologyTransitionStatus)

			fixture.withNodes(newTestDualRoleNodeWithConditions("master-2", false, readyNodeCondition())).
				withEtcdCR(true, false).withEtcdEndpoints(3)
			clk.SetTime(evaluationTime.Add(3 * time.Minute))

			assert.NoError(t, c.syncEvaluation(t.Context()))

			recovered := currentInfra(t, c).Status.TopologyTransitionStatus
			assert.Equal(t, first.Conditions, recovered.Conditions)
			if !assert.Len(t, recovered.Transitions, 1) {
				return
			}
			assert.Len(t, recovered.Transitions[0].Evaluations, len(first.Transitions[0].Evaluations))
			assert.NotNil(t, meta.FindStatusCondition(recovered.Transitions[0].Evaluations, tc.checkType))
			assert.NotNil(t, meta.FindStatusCondition(recovered.Transitions[0].Evaluations, configv1.TopologyTransitionAvailableConditionType))
			assert.Equal(t, writes+1, statusWrites(client))
			for _, condition := range recovered.Transitions[0].Evaluations {
				assert.Equal(t, metav1.ConditionTrue, condition.Status)
				if condition.Type == configv1.TopologyTransitionAvailableConditionType || condition.Type == tc.checkType {
					assert.Equal(t, metav1.NewTime(clk.Now()), condition.LastTransitionTime)
				} else {
					assert.Equal(t, meta.FindStatusCondition(first.Transitions[0].Evaluations, condition.Type), &condition)
				}
			}
		})
	}
}

func TestDiscoveryNewTransitionAndConditionGetNewTimes(t *testing.T) {
	c, _, clk := statusTestController(snoInfra(""))
	assert.NoError(t, c.syncEvaluation(t.Context()))

	clk.SetTime(evaluationTime.Add(time.Minute))
	c.preflightChecks = []PreflightCheck{{Type: "NewCheck", Validate: func() error { return nil }}}

	assert.NoError(t, c.syncEvaluation(t.Context()))

	status := currentInfra(t, c).Status.TopologyTransitionStatus
	if !assert.NotNil(t, status) || !assert.Len(t, status.Transitions, 1) {
		return
	}
	assert.Equal(t, metav1.NewTime(evaluationTime), status.Transitions[0].Evaluations[0].LastTransitionTime)
	assert.Equal(t, metav1.NewTime(clk.Now()), status.Transitions[0].Evaluations[1].LastTransitionTime)

	clk.SetTime(evaluationTime.Add(2 * time.Minute))
	c.transitions[0].UpdateStatus = func(infra *configv1.Infrastructure) {
		infra.Status.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
	}

	assert.NoError(t, c.syncEvaluation(t.Context()))

	status = currentInfra(t, c).Status.TopologyTransitionStatus
	assert.Equal(t, metav1.NewTime(evaluationTime), status.Conditions[0].LastTransitionTime)
	for _, condition := range status.Transitions[0].Evaluations {
		assert.Equal(t, metav1.NewTime(clk.Now()), condition.LastTransitionTime, "same condition type under a new target is a new condition")
	}
}

func TestDiscoveryNotFoundHandling(t *testing.T) {
	t.Run("Infrastructure NotFound preserves warning", func(t *testing.T) {
		c, client, _ := statusTestController(snoInfra(""))
		assert.NoError(t, c.infraClient.Delete(context.Background(), "cluster", metav1.DeleteOptions{}))
		client.ClearActions()
		syncCtx := newTestSyncContext()

		assert.NoError(t, c.sync(context.Background(), syncCtx))

		assert.Equal(t, 0, statusWrites(client))
		recorded := syncCtx.Recorder().(events.InMemoryRecorder).Events()
		if assert.Len(t, recorded, 1) {
			assert.Equal(t, "Warning", recorded[0].Type)
			assert.Contains(t, recorded[0].Message, "Required infrastructures.config.openshift.io/cluster not found")
		}
	})

	t.Run("required resource NotFound is a read error", func(t *testing.T) {
		c, _, _ := statusTestController(snoInfra(""))
		readErr := apierrors.NewNotFound(schema.GroupResource{Resource: "nodes"}, "required-node")
		c.preflightChecks = []PreflightCheck{{Type: "NodesReadable", Validate: func() error { return &clusterStateReadError{err: readErr} }}}

		assert.ErrorIs(t, c.syncEvaluation(t.Context()), readErr)

		status := currentInfra(t, c).Status.TopologyTransitionStatus
		if assert.NotNil(t, status) {
			assert.Equal(t, metav1.ConditionFalse, status.Conditions[0].Status)
			assert.Empty(t, status.Transitions)
		}
	})
}

func TestTransitionWriteInvalidatesDiscoveryAndRetries(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "no conflict", true: "conflict"}[conflict], func(t *testing.T) {
			infra := snoInfra(configv1.HighlyAvailableTopologyMode)
			c, client, _ := statusTestController(infra)
			completion := metav1.Condition{Type: configv1.TopologyTransitionCompletedConditionType, Status: metav1.ConditionTrue, Reason: "OtherOwner", LastTransitionTime: metav1.NewTime(evaluationTime.Add(-time.Hour))}
			attempts := 0
			client.PrependReactor("update", "infrastructures", func(action clienttesting.Action) (bool, runtime.Object, error) {
				update := action.(clienttesting.UpdateAction).GetObject().(*configv1.Infrastructure)
				if update.Status.ControlPlaneTopology != configv1.HighlyAvailableTopologyMode {
					return false, nil, nil
				}
				attempts++
				assert.Empty(t, update.Status.TopologyTransitionStatus.Transitions, "topology and invalidated discovery must be written together")
				if conflict && attempts == 1 {
					object, err := client.Tracker().Get(configv1.SchemeGroupVersion.WithResource("infrastructures"), "", "cluster")
					assert.NoError(t, err)
					newer := object.(*configv1.Infrastructure).DeepCopy()
					newer.ResourceVersion = "2"
					newer.Status.APIServerInternalURL = "https://preserved.example"
					newer.Status.TopologyTransitionStatus = &configv1.TopologyTransitionStatus{Conditions: []metav1.Condition{completion}}
					assert.NoError(t, client.Tracker().Update(configv1.SchemeGroupVersion.WithResource("infrastructures"), newer, ""))
					return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "infrastructures"}, "cluster", errors.New("transition conflict"))
				}
				return false, nil, nil
			})
			syncCtx := newTestSyncContext()

			assert.NoError(t, c.sync(context.Background(), syncCtx))

			recorded := syncCtx.Recorder().(events.InMemoryRecorder).Events()
			assert.Len(t, recorded, 1, "a successful write emits one event, not one per attempt")
			updated := currentInfra(t, c)
			assert.Equal(t, configv1.HighlyAvailableTopologyMode, updated.Status.ControlPlaneTopology)
			assert.Equal(t, configv1.HighlyAvailableTopologyMode, updated.Status.InfrastructureTopology)
			if assert.NotNil(t, updated.Status.TopologyTransitionStatus) {
				assert.Empty(t, updated.Status.TopologyTransitionStatus.Transitions)
				condition := meta.FindStatusCondition(updated.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionsEvaluatedConditionType)
				if assert.NotNil(t, condition) {
					assert.Equal(t, "EvaluationPending", condition.Reason)
					assert.Equal(t, metav1.ConditionUnknown, condition.Status)
					assert.Equal(t, metav1.NewTime(evaluationTime), condition.LastTransitionTime)
				}
				if conflict {
					assert.Equal(t, "https://preserved.example", updated.Status.APIServerInternalURL)
					assert.Equal(t, reasonTopologyTransitionInProgress, completionCondition(t, c).Reason, "starting a transition owns the completion condition")
					assert.Equal(t, 2, attempts)
				}
			}

			client.ClearActions()
			// Cache still has the old request/source. It must not skip active recovery.
			checked := 0
			c.transitions[0].TransitionValidators = []TransitionValidatorFunc{func() error { checked++; return nil }}
			c.clock.(*clocktesting.FakePassiveClock).SetTime(time.Now().Add(time.Hour))

			assert.NoError(t, c.sync(context.Background(), newTestSyncContext()))
			assert.Equal(t, 1, checked)
			assert.Equal(t, 1, statusWrites(client), "reconciliation writes Completed=True")
			assert.Equal(t, metav1.ConditionTrue, completionCondition(t, c).Status)
		})
	}
}

func TestBlockedRequestStartsNewSoakPeriodWhenItBecomesAvailable(t *testing.T) {
	infra := snoInfra(configv1.HighlyAvailableTopologyMode)
	oldTime := metav1.NewTime(evaluationTime.Add(-time.Hour))
	infra.Status.TopologyTransitionStatus = &configv1.TopologyTransitionStatus{Conditions: []metav1.Condition{{
		Type: configv1.TopologyTransitionCompletedConditionType, Status: metav1.ConditionFalse,
		Reason: "PreflightCheckFailed", LastTransitionTime: oldTime,
	}}}
	c, client, clk := statusTestController(infra)
	clk.SetTime(evaluationTime)

	assert.NoError(t, c.sync(context.Background(), newTestSyncContext()))

	condition := completionCondition(t, c)
	assert.Equal(t, metav1.ConditionFalse, condition.Status)
	assert.Equal(t, reasonTopologyTransitionInProgress, condition.Reason)
	assert.Equal(t, metav1.NewTime(evaluationTime), condition.LastTransitionTime)

	client.ClearActions()
	assert.NoError(t, c.sync(context.Background(), newTestSyncContext()))
	assert.Equal(t, 0, statusWrites(client), "the soak period must not move on repeated syncs")
	assert.Equal(t, metav1.NewTime(evaluationTime), completionCondition(t, c).LastTransitionTime)
	_, operatorStatus, _, err := c.operatorClient.GetOperatorState()
	assert.NoError(t, err)
	assert.True(t, v1helpers.IsOperatorConditionFalse(operatorStatus.Conditions, upgradeableCondition))
}

func TestDiscoveryWriteFailureRecovers(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonconflict write error", true: "persistent conflict"}[conflict], func(t *testing.T) {
			fixture := readyFixture()
			c, client, clk := statusTestController(snoInfra(""))
			c.preflightChecks = fixture.buildPreflightChecks()
			c.transitions = fixture.buildTransitions()

			if !assert.NoError(t, c.syncEvaluation(t.Context())) {
				return
			}

			baseline := currentInfra(t, c)
			writeErr := errors.New("status write unavailable")
			wantAttempts := 1
			if conflict {
				writeErr = apierrors.NewConflict(schema.GroupResource{Resource: "infrastructures"}, "cluster", errors.New("status conflict"))
				wantAttempts = retry.DefaultRetry.Steps
			}
			fault := true
			attempts := 0
			client.PrependReactor("update", "infrastructures", func(action clienttesting.Action) (bool, runtime.Object, error) {
				assert.Equal(t, "status", action.GetSubresource())
				attempts++
				if fault {
					return true, nil, writeErr
				}
				return false, nil, nil
			})
			fixture.withEtcdCR(true, true)
			clk.SetTime(evaluationTime.Add(time.Minute))

			assert.ErrorIs(t, c.syncEvaluation(t.Context()), writeErr)
			assert.Equal(t, wantAttempts, attempts, "only conflicts are retried, up to the configured limit")
			assert.Equal(t, baseline.Status, currentInfra(t, c).Status, "failed writes must not publish their proposed status")

			fault = false
			// The next evaluation must use current state, not replay the failed write.
			fixture.withEtcdCR(true, false).withEtcdEndpoints(1)
			clk.SetTime(evaluationTime.Add(2 * time.Minute))

			assert.NoError(t, c.syncEvaluation(t.Context()))
			assert.Equal(t, wantAttempts+1, attempts)

			recovered := currentInfra(t, c)
			assert.Equal(t, baseline.Status.ControlPlaneTopology, recovered.Status.ControlPlaneTopology)
			assert.Equal(t, baseline.Status.InfrastructureTopology, recovered.Status.InfrastructureTopology)
			status := recovered.Status.TopologyTransitionStatus
			if !assert.NotNil(t, status) || !assert.Len(t, status.Transitions, 1) {
				return
			}
			evaluated := meta.FindStatusCondition(status.Conditions, configv1.TopologyTransitionsEvaluatedConditionType)
			if assert.NotNil(t, evaluated) {
				assert.Equal(t, metav1.ConditionTrue, evaluated.Status)
			}
			for checkType, wantStatus := range map[string]metav1.ConditionStatus{
				configv1.TopologyTransitionAvailableConditionType: metav1.ConditionFalse,
				"EtcdVotingMembersSatisfied":                      metav1.ConditionFalse,
				"EtcdNotProgressing":                              metav1.ConditionTrue,
			} {
				check := meta.FindStatusCondition(status.Transitions[0].Evaluations, checkType)
				if assert.NotNil(t, check) {
					assert.Equal(t, wantStatus, check.Status, checkType)
				}
			}
		})
	}
}

func TestTransitionWriteFailureRecovers(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonconflict write error", true: "persistent conflict"}[conflict], func(t *testing.T) {
			infra := snoInfra(configv1.HighlyAvailableTopologyMode)
			c, client, _ := statusTestController(infra)
			fixture := readyFixture()
			c.preflightChecks = fixture.buildPreflightChecks()
			c.transitions = fixture.buildTransitions()
			assert.NoError(t, c.syncEvaluation(t.Context()))
			client.ClearActions()

			writeErr := errors.New("topology write unavailable")
			wantAttempts := 1
			if conflict {
				writeErr = apierrors.NewConflict(schema.GroupResource{Resource: "infrastructures"}, "cluster", errors.New("topology conflict"))
				wantAttempts = retry.DefaultRetry.Steps
			}
			fault := true
			attempts := 0
			client.PrependReactor("update", "infrastructures", func(action clienttesting.Action) (bool, runtime.Object, error) {
				update := action.(clienttesting.UpdateAction).GetObject().(*configv1.Infrastructure)
				if update.Status.ControlPlaneTopology != configv1.HighlyAvailableTopologyMode {
					return false, nil, nil // allow discovery writes
				}
				assert.Equal(t, "status", action.GetSubresource())
				attempts++
				if fault {
					return true, nil, writeErr
				}
				return false, nil, nil
			})
			syncCtx := newTestSyncContext()

			assert.ErrorIs(t, c.sync(context.Background(), syncCtx), writeErr)
			assert.Equal(t, wantAttempts, attempts, "only conflicts are retried, up to the configured limit")
			assert.Equal(t, wantAttempts, statusWrites(client), "transition sync must not write discovery inline")
			assert.Empty(t, syncCtx.Recorder().(events.InMemoryRecorder).Events(), "failed topology writes must not emit success")

			failed := currentInfra(t, c)
			assert.Equal(t, infra.Status.ControlPlaneTopology, failed.Status.ControlPlaneTopology)
			assert.Equal(t, infra.Status.InfrastructureTopology, failed.Status.InfrastructureTopology)
			if !assert.NotNil(t, failed.Status.TopologyTransitionStatus) {
				return
			}
			assert.Len(t, failed.Status.TopologyTransitionStatus.Transitions, 1)
			_, operatorStatus, _, err := c.operatorClient.GetOperatorState()
			assert.NoError(t, err)
			assert.Nil(t, meta.FindStatusCondition(failed.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionCompletedConditionType), "failed topology write must not report a started transition")
			assert.True(t, v1helpers.IsOperatorConditionFalse(operatorStatus.Conditions, upgradeableCondition))

			fault = false
			assert.NoError(t, c.sync(context.Background(), syncCtx), "spec still differs from status despite the in-progress operator conditions")
			assert.Equal(t, wantAttempts+1, attempts)

			recovered := currentInfra(t, c)
			assert.Equal(t, configv1.HighlyAvailableTopologyMode, recovered.Status.ControlPlaneTopology)
			assert.Equal(t, configv1.HighlyAvailableTopologyMode, recovered.Status.InfrastructureTopology)
			assert.Equal(t, reasonTopologyTransitionInProgress, completionCondition(t, c).Reason)
			status := recovered.Status.TopologyTransitionStatus
			if !assert.NotNil(t, status) {
				return
			}
			assert.Empty(t, status.Transitions, "do not advertise the old source after recovery applies the transition")
			evaluated := meta.FindStatusCondition(status.Conditions, configv1.TopologyTransitionsEvaluatedConditionType)
			if assert.NotNil(t, evaluated) {
				assert.Equal(t, metav1.ConditionUnknown, evaluated.Status)
				assert.Equal(t, "EvaluationPending", evaluated.Reason)
			}

			recorded := syncCtx.Recorder().(events.InMemoryRecorder).Events()
			if assert.Len(t, recorded, 1) {
				assert.Equal(t, "Normal", recorded[0].Type)
				assert.Equal(t, "Control plane topology updated from SingleReplica to HighlyAvailable", recorded[0].Message)
			}
		})
	}
}

func TestTransitionWriteGuardsConcurrentSpecAndSource(t *testing.T) {
	for _, change := range []string{"spec", "source", "platform"} {
		t.Run(change, func(t *testing.T) {
			infra := snoInfra(configv1.HighlyAvailableTopologyMode)
			c, client, _ := statusTestController(infra)
			assert.NoError(t, c.syncEvaluation(t.Context()))
			attempts := 0
			client.PrependReactor("update", "infrastructures", func(action clienttesting.Action) (bool, runtime.Object, error) {
				update := action.(clienttesting.UpdateAction).GetObject().(*configv1.Infrastructure)
				if update.Status.ControlPlaneTopology != configv1.HighlyAvailableTopologyMode {
					return false, nil, nil
				}
				attempts++
				if attempts > 1 {
					return false, nil, nil
				}

				object, err := client.Tracker().Get(configv1.SchemeGroupVersion.WithResource("infrastructures"), "", "cluster")
				assert.NoError(t, err)
				newer := object.(*configv1.Infrastructure).DeepCopy()
				newer.ResourceVersion = "2"
				switch change {
				case "spec":
					newer.Spec.ControlPlaneTopology = configv1.SingleReplicaTopologyMode
				case "source":
					newer.Status.InfrastructureTopology = configv1.HighlyAvailableTopologyMode
				case "platform":
					newer.Status.PlatformStatus.Type = configv1.AWSPlatformType
				}
				assert.NoError(t, client.Tracker().Update(configv1.SchemeGroupVersion.WithResource("infrastructures"), newer, ""))
				return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "infrastructures"}, "cluster", errors.New("request changed"))
			})

			assert.NoError(t, c.sync(context.Background(), newTestSyncContext()))

			updated := currentInfra(t, c)
			assert.Equal(t, configv1.SingleReplicaTopologyMode, updated.Status.ControlPlaneTopology, "do not apply an obsolete request")
			assert.Len(t, updated.Status.TopologyTransitionStatus.Transitions, 1, "skipped starts do not evaluate inline")
			assert.NoError(t, c.syncEvaluation(t.Context()))
			updated = currentInfra(t, c)
			if assert.NotNil(t, updated.Status.TopologyTransitionStatus) {
				if change == "spec" {
					assert.Equal(t, configv1.SingleReplicaTopologyMode, updated.Spec.ControlPlaneTopology)
					assert.Len(t, updated.Status.TopologyTransitionStatus.Transitions, 1)
				} else {
					assert.Empty(t, updated.Status.TopologyTransitionStatus.Transitions, "standalone evaluation refreshes the changed source")
				}
			}
		})
	}
}

func TestRequestValidationRemainsAuthoritative(t *testing.T) {
	for _, readFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "known blocker", true: "request read failure"}[readFailure], func(t *testing.T) {
			c, _, _ := statusTestController(snoInfra(""))
			assert.NoError(t, c.syncEvaluation(t.Context()))
			requested := currentInfra(t, c)
			requested.Spec.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
			_, err := c.infraClient.Update(context.Background(), requested, metav1.UpdateOptions{})
			assert.NoError(t, err)

			calls := 0
			readErr := errors.New("request node read failed")
			c.preflightChecks = []PreflightCheck{{Type: "RequestCheck", Validate: func() error {
				calls++
				if readFailure {
					return &clusterStateReadError{err: readErr}
				}
				return errors.New("node became unready after discovery")
			}}}

			err = c.sync(context.Background(), newTestSyncContext())

			if readFailure {
				assert.ErrorIs(t, err, readErr)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, 1, calls, "request validation must run despite the stale available report")
			assert.Equal(t, metav1.ConditionTrue, currentInfra(t, c).Status.TopologyTransitionStatus.Transitions[0].Evaluations[0].Status)
			assert.Equal(t, configv1.SingleReplicaTopologyMode, currentInfra(t, c).Status.ControlPlaneTopology)
			condition := completionCondition(t, c)
			if assert.NotNil(t, condition) {
				assert.Equal(t, "PreflightCheckFailed", condition.Reason)
			}
		})
	}
}

func TestDiscoveryReadFailureDoesNotPreventRecoveredRequestChecks(t *testing.T) {
	c, _, _ := statusTestController(snoInfra(configv1.HighlyAvailableTopologyMode))
	calls := 0
	readErr := errors.New("discovery node read failed")
	c.preflightChecks = []PreflightCheck{{Type: "NodesReadable", Validate: func() error {
		calls++
		if calls == 1 {
			return &clusterStateReadError{err: readErr}
		}
		return nil
	}}}

	assert.ErrorIs(t, c.syncEvaluation(t.Context()), readErr)
	assert.NoError(t, c.sync(t.Context(), newTestSyncContext()))
	assert.Equal(t, 2, calls, "request checks run independently of the failed report")
	assert.Equal(t, configv1.HighlyAvailableTopologyMode, currentInfra(t, c).Status.ControlPlaneTopology)
}

func TestDiscoveryFailureDoesNotMaskRequestOperatorFailure(t *testing.T) {
	c, client, _ := statusTestController(snoInfra(configv1.HighlyAvailableTopologyMode))
	readErr := errors.New("required node read failed")
	writeErr := errors.New("failed discovery status write")
	operatorErr := errors.New("failed operator rejection write")
	c.preflightChecks = []PreflightCheck{{Type: "NodesReadable", Validate: func() error { return &clusterStateReadError{err: readErr} }}}
	client.PrependReactor("update", "infrastructures", func(clienttesting.Action) (bool, runtime.Object, error) { return true, nil, writeErr })
	c.operatorClient = failingOperatorStatusClient{OperatorClient: c.operatorClient, err: operatorErr}

	assert.ErrorIs(t, c.syncEvaluation(t.Context()), writeErr)
	err := c.sync(t.Context(), newTestSyncContext())

	assert.ErrorIs(t, err, readErr)
	assert.NotErrorIs(t, err, writeErr, "background write errors must not join transition errors")
	assert.ErrorIs(t, err, operatorErr)
	assert.Equal(t, configv1.SingleReplicaTopologyMode, currentInfra(t, c).Status.ControlPlaneTopology)
}

func TestRequestReadAndOperatorErrorsAreJoined(t *testing.T) {
	c, _, _ := statusTestController(snoInfra(configv1.HighlyAvailableTopologyMode))
	calls := 0
	readErr := errors.New("request read failed")
	operatorErr := errors.New("request rejection status failed")
	c.preflightChecks = []PreflightCheck{{Type: "RequestCheck", Validate: func() error {
		calls++
		return &clusterStateReadError{err: readErr}
	}}}
	c.operatorClient = failingOperatorStatusClient{OperatorClient: c.operatorClient, err: operatorErr}

	err := c.sync(context.Background(), newTestSyncContext())

	assert.ErrorIs(t, err, readErr)
	assert.ErrorIs(t, err, operatorErr)
	assert.Equal(t, configv1.SingleReplicaTopologyMode, currentInfra(t, c).Status.ControlPlaneTopology)
}
