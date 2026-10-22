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

// statusTestController builds a controller with fake status storage, upgrades allowed, and a fixed
// evaluation clock.
func statusTestController(infra *configv1.Infrastructure) (*TopologyTransitionController, *configfakeclient.Clientset, *clocktesting.FakePassiveClock) {
	clk := clocktesting.NewFakePassiveClock(evaluationTime)
	c := newTestControllerWithClock(infra, []operatorv1.OperatorCondition{{Type: upgradeableCondition, Status: operatorv1.ConditionTrue}}, nil, noopTransitions(), clk)
	client := configfakeclient.NewSimpleClientset(infra)
	c.infraClient = client.ConfigV1().Infrastructures()

	return c, client, clk
}

// TestDiscoveryFailureOwnsSeparateDegradedConditionAndRecovers checks that evaluation health
// changes leave transition health untouched.
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

// TestDiscoveryChecksKeepPreviousReportAndSkipUnchangedWrites checks that evaluation retains the
// old report and avoids writes for stable results.
func TestDiscoveryChecksKeepPreviousReportAndSkipUnchangedWrites(t *testing.T) {
	c, client, clk := statusTestController(snoInfra(""))
	c.preflightChecks = []PreflightCheck{{Type: "StableCheck", Validate: func() error { return nil }}}

	if !assert.NoError(t, c.syncEvaluation(t.Context())) {
		return
	}

	previous := currentInfra(t, c).Status.TopologyTransitionStatus
	if !assert.Len(t, previous.Transitions, 1) {
		return
	}

	client.ClearActions()
	clk.SetTime(evaluationTime.Add(time.Minute))
	c.preflightChecks[0].Validate = func() error {
		assert.Zero(t, statusWrites(client), "checks must run before any status write")
		assert.Equal(t, previous, currentInfra(t, c).Status.TopologyTransitionStatus)

		return nil
	}

	assert.NoError(t, c.syncEvaluation(t.Context()))

	assert.Zero(t, statusWrites(client), "unchanged results must not write status")
	assert.Equal(t, previous, currentInfra(t, c).Status.TopologyTransitionStatus)
}

// TestDiscoveryConditionTimesAndRecovery checks timestamp stability across blockers, read failures,
// and recovery.
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
	if !assert.NotEmpty(t, first.Status.TopologyTransitionStatus.Conditions) {
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
	assert.Equal(t, first.Status.TopologyTransitionStatus.Conditions[0].LastTransitionTime, blocked.Conditions[0].LastTransitionTime)
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
	assert.Equal(t, writes, statusWrites(client), "unchanged failures must not write status")
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

// TestDiscoveryNormalizesEmptyTransitions checks that omitted and empty transition lists cause no
// repeated writes.
func TestDiscoveryNormalizesEmptyTransitions(t *testing.T) {
	infra := snoInfra("")
	c, client, clk := statusTestController(infra)
	c.transitions = nil

	assert.NoError(t, c.syncEvaluation(t.Context()))

	first := currentInfra(t, c)
	data, err := json.Marshal(first.Status.TopologyTransitionStatus)
	assert.NoError(t, err)
	assert.NotContains(t, string(data), `"transitions"`)
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

// TestDiscoveryConflictReevaluatesAndPreservesOtherFields checks fresh evaluation after a conflict
// without overwriting unrelated status.
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
					newer.Status.TopologyTransitionStatus = configv1.TopologyTransitionStatus{Conditions: []metav1.Condition{completion}}
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
			if !assert.NotEmpty(t, status.Conditions) {
				return
			}
			assert.Equal(t, &completion, meta.FindStatusCondition(status.Conditions, completion.Type))
			assert.Len(t, status.Conditions, 2)
			condition := meta.FindStatusCondition(status.Conditions, configv1.TopologyTransitionsEvaluatedConditionType)
			if assert.NotNil(t, condition) {
				assert.Equal(t, int64(5), condition.ObservedGeneration)
			}
			if assert.Len(t, status.Transitions, 1) {
				available := status.Transitions[0].Evaluations[0]
				if changeSource {
					assert.Equal(t, metav1.ConditionFalse, available.Status)
					assert.Equal(t, "SourceTopologyMismatch", available.Reason)
				} else {
					assert.Equal(t, metav1.ConditionTrue, available.Status)
				}
			}
		})
	}
}

// TestDiscoveryReadFailurePreservesCompletionAndOtherFields checks that failed discovery and
// recovery preserve concurrent completion and config updates.
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
			*meta.FindStatusCondition(seed.Status.TopologyTransitionStatus.Conditions, completion.Type) = completion
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
			if !assert.NotEmpty(t, failed.Status.TopologyTransitionStatus.Conditions) {
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
			if !assert.NotEmpty(t, recovered.Status.TopologyTransitionStatus.Conditions) || !assert.Len(t, recovered.Status.TopologyTransitionStatus.Transitions, 1) {
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

// currentInfra reads live Infrastructure from the fake client and fails the test on a read error.
func currentInfra(t *testing.T, c *TopologyTransitionController) *configv1.Infrastructure {
	t.Helper()

	infra, err := c.infraClient.Get(context.Background(), "cluster", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}

	return infra
}

// statusWrites counts recorded status updates while excluding reads and spec updates.
func statusWrites(client *configfakeclient.Clientset) int {
	count := 0
	for _, action := range client.Actions() {
		if action.GetVerb() == "update" && action.GetSubresource() == "status" {
			count++
		}
	}

	return count
}

// TestEvaluationPublishesDiscoveryIdle checks that idle clusters receive one report and stable
// refreshes do not write again.
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
			if !assert.NotEmpty(t, first.Status.TopologyTransitionStatus.Conditions) {
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

// TestDiscoveryRealBlockersRemainStableAndRecover checks that real preflight blockers update only
// affected check conditions.
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
			if !assert.NotEmpty(t, first.Conditions) || !assert.Len(t, first.Transitions, 1) {
				return
			}
			assert.Equal(t, metav1.ConditionTrue, first.Conditions[0].Status)

			// Change the real indexers read by the production checks.
			tc.block(fixture)
			clk.SetTime(evaluationTime.Add(time.Minute))

			assert.NoError(t, c.syncEvaluation(t.Context()))

			blocked := currentInfra(t, c).Status.TopologyTransitionStatus
			assert.Equal(t, first.Conditions[1], blocked.Conditions[1])
			assert.Equal(t, first.Conditions[0].LastTransitionTime, blocked.Conditions[0].LastTransitionTime)
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
			assert.Equal(t, writes, statusWrites(client), "unchanged blockers must not write status")
			assert.Equal(t, blocked, currentInfra(t, c).Status.TopologyTransitionStatus)

			fixture.withNodes(newTestDualRoleNodeWithConditions("master-2", false, readyNodeCondition())).
				withEtcdCR(true, false).withEtcdEndpoints(3)
			clk.SetTime(evaluationTime.Add(3 * time.Minute))

			assert.NoError(t, c.syncEvaluation(t.Context()))

			recovered := currentInfra(t, c).Status.TopologyTransitionStatus
			assert.Equal(t, first.Conditions[1], recovered.Conditions[1])
			assert.Equal(t, first.Conditions[0].LastTransitionTime, recovered.Conditions[0].LastTransitionTime)
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

// TestDiscoveryNewTransitionAndConditionGetNewTimes checks that new checks and targets receive new
// timestamps.
func TestDiscoveryNewTransitionAndConditionGetNewTimes(t *testing.T) {
	c, _, clk := statusTestController(snoInfra(""))
	assert.NoError(t, c.syncEvaluation(t.Context()))

	clk.SetTime(evaluationTime.Add(time.Minute))
	c.preflightChecks = []PreflightCheck{{Type: "NewCheck", Validate: func() error { return nil }}}

	assert.NoError(t, c.syncEvaluation(t.Context()))

	status := currentInfra(t, c).Status.TopologyTransitionStatus
	if !assert.NotEmpty(t, status.Conditions) || !assert.Len(t, status.Transitions, 1) {
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

// TestDiscoveryWriteFailureRecovers checks bounded conflict retries and recovery using current
// cluster state.
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
			if !assert.NotEmpty(t, status.Conditions) || !assert.Len(t, status.Transitions, 1) {
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

// TestTopologyWriteGuardsConcurrentSpecAndSource checks that changed requests or source inputs stop
// a conflicted topology write.
func TestTopologyWriteGuardsConcurrentSpecAndSource(t *testing.T) {
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
			if assert.NotEmpty(t, updated.Status.TopologyTransitionStatus.Conditions) {
				if change == "spec" {
					assert.Equal(t, configv1.SingleReplicaTopologyMode, updated.Spec.ControlPlaneTopology)
					assert.Len(t, updated.Status.TopologyTransitionStatus.Transitions, 1)
				} else {
					if assert.Len(t, updated.Status.TopologyTransitionStatus.Transitions, 1) {
						available := updated.Status.TopologyTransitionStatus.Transitions[0].Evaluations[0]
						assert.Equal(t, metav1.ConditionFalse, available.Status)
						assert.Equal(t, "SourceTopologyMismatch", available.Reason)
					}
				}
			}
		})
	}
}
