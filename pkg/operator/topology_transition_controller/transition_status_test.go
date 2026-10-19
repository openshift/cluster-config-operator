package topology_transition_controller

import (
	"context"
	"errors"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	configfakeclient "github.com/openshift/client-go/config/clientset/versioned/fake"
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

// statusTestController creates a controller with a fixed clock and tracked Infrastructure writes.
func statusTestController(infra *configv1.Infrastructure) (*TopologyTransitionController, *configfakeclient.Clientset, *clocktesting.FakePassiveClock) {
	clk := clocktesting.NewFakePassiveClock(evaluationTime)
	c := newTestControllerWithClock(infra, []operatorv1.OperatorCondition{{Type: upgradeableCondition, Status: operatorv1.ConditionTrue}}, nil, noopTransitions(), clk)
	client := configfakeclient.NewSimpleClientset(infra)
	c.infraClient = client.ConfigV1().Infrastructures()

	return c, client, clk
}

// TestFreshInfrastructureReadFailureStillRunsActiveChecks checks that cached recovery
// runs active validators while a failed live read prevents status writes.
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

// currentInfra reads live Infrastructure and fails the test on read errors.
func currentInfra(t *testing.T, c *TopologyTransitionController) *configv1.Infrastructure {
	t.Helper()

	infra, err := c.infraClient.Get(context.Background(), "cluster", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}

	return infra
}

// statusWrites counts Infrastructure status updates recorded by the fake client.
func statusWrites(client *configfakeclient.Clientset) int {
	count := 0
	for _, action := range client.Actions() {
		if action.GetVerb() == "update" && action.GetSubresource() == "status" {
			count++
		}
	}

	return count
}

// TestApplyTransitionStatusConditionOnly checks completion updates, stable timestamps,
// and conflict retries that preserve live discovery and unrelated status.
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
			seedExistingReport(c, client, infra)
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
			c.preflightChecks = []TransitionValidatorFunc{func() error {
				t.Error("condition-only writes must not evaluate discovery")
				return errors.New("unexpected discovery evaluation")
			}}
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

// TestBlockedRequestStartsNewSoakPeriodWhenItBecomesAvailable checks that starting
// a formerly blocked request resets the soak timer exactly once.
func TestBlockedRequestStartsNewSoakPeriodWhenItBecomesAvailable(t *testing.T) {
	infra := snoInfra(configv1.HighlyAvailableTopologyMode)
	oldTime := metav1.NewTime(evaluationTime.Add(-time.Hour))
	infra.Status.TopologyTransitionStatus = configv1.TopologyTransitionStatus{Conditions: []metav1.Condition{{
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

// TestTransitionWriteFailureRecovers checks bounded conflict retries, retained
// upgrade blocking, and a single success event after the topology write recovers.
func TestTransitionWriteFailureRecovers(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonconflict write error", true: "persistent conflict"}[conflict], func(t *testing.T) {
			infra := snoInfra(configv1.HighlyAvailableTopologyMode)
			c, client, _ := statusTestController(infra)
			seedExistingReport(c, client, currentInfra(t, c))
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
			if !assert.NotEmpty(t, failed.Status.TopologyTransitionStatus.Conditions) {
				return
			}
			assert.Len(t, failed.Status.TopologyTransitionStatus.Transitions, 1)
			_, operatorStatus, _, err := c.operatorClient.GetOperatorState()
			assert.NoError(t, err)
			assert.Equal(t, metav1.ConditionUnknown, completionCondition(t, c).Status, "failed topology write must not report a started transition")
			assert.True(t, v1helpers.IsOperatorConditionFalse(operatorStatus.Conditions, upgradeableCondition))

			fault = false
			assert.NoError(t, c.sync(context.Background(), syncCtx), "spec still differs from status despite the in-progress operator conditions")
			assert.Equal(t, wantAttempts+1, attempts)

			recovered := currentInfra(t, c)
			assert.Equal(t, configv1.HighlyAvailableTopologyMode, recovered.Status.ControlPlaneTopology)
			assert.Equal(t, configv1.HighlyAvailableTopologyMode, recovered.Status.InfrastructureTopology)
			assert.Equal(t, reasonTopologyTransitionInProgress, completionCondition(t, c).Reason)
			status := recovered.Status.TopologyTransitionStatus
			if !assert.NotEmpty(t, status.Conditions) {
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

// TestTransitionWriteGuardsConcurrentSpecAndSource checks that conflicting request
// or source changes prevent applying an obsolete transition.
func TestTransitionWriteGuardsConcurrentSpecAndSource(t *testing.T) {
	for _, change := range []string{"spec", "source", "platform"} {
		t.Run(change, func(t *testing.T) {
			infra := snoInfra(configv1.HighlyAvailableTopologyMode)
			c, client, _ := statusTestController(infra)
			seedExistingReport(c, client, currentInfra(t, c))
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
		})
	}
}

// TestRequestValidationRemainsAuthoritative checks that request preflight checks
// reject newly blocked transitions despite an earlier available report.
func TestRequestValidationRemainsAuthoritative(t *testing.T) {
	for _, readFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "known blocker", true: "request read failure"}[readFailure], func(t *testing.T) {
			c, client, _ := statusTestController(snoInfra(""))
			seedExistingReport(c, client, currentInfra(t, c))
			requested := currentInfra(t, c)
			requested.Spec.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
			_, err := c.infraClient.Update(context.Background(), requested, metav1.UpdateOptions{})
			assert.NoError(t, err)

			calls := 0
			readErr := errors.New("request node read failed")
			c.preflightChecks = []TransitionValidatorFunc{func() error {
				calls++
				if readFailure {
					return &clusterStateReadError{err: readErr}
				}
				return errors.New("node became unready after discovery")
			}}

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

// TestRequestReadAndOperatorErrorsAreJoined checks that failed rejection reporting
// preserves both the cluster read error and the operator status error.
func TestRequestReadAndOperatorErrorsAreJoined(t *testing.T) {
	c, _, _ := statusTestController(snoInfra(configv1.HighlyAvailableTopologyMode))
	calls := 0
	readErr := errors.New("request read failed")
	operatorErr := errors.New("request rejection status failed")
	c.preflightChecks = []TransitionValidatorFunc{func() error {
		calls++
		return &clusterStateReadError{err: readErr}
	}}
	c.operatorClient = failingOperatorStatusClient{OperatorClient: c.operatorClient, err: operatorErr}

	err := c.sync(context.Background(), newTestSyncContext())

	assert.ErrorIs(t, err, readErr)
	assert.ErrorIs(t, err, operatorErr)
	assert.Equal(t, configv1.SingleReplicaTopologyMode, currentInfra(t, c).Status.ControlPlaneTopology)
}

// TestInitialLiveInfrastructureReadFailurePreventsNewStart checks that cached
// preflight checks run without authorizing a write after the initial live read fails.
func TestInitialLiveInfrastructureReadFailurePreventsNewStart(t *testing.T) {
	c, client, _ := statusTestController(snoInfra(configv1.HighlyAvailableTopologyMode))
	readErr := errors.New("initial live read unavailable")
	reads, checks := 0, 0
	client.PrependReactor("get", "infrastructures", func(clienttesting.Action) (bool, runtime.Object, error) {
		reads++
		if reads == 1 {
			return true, nil, readErr
		}
		return false, nil, nil
	})
	c.preflightChecks = []TransitionValidatorFunc{func() error { checks++; return nil }}

	assert.ErrorIs(t, c.sync(t.Context(), newTestSyncContext()), readErr)

	assert.Equal(t, 1, checks, "live read failures must not skip mandatory cached checks")
	assert.Zero(t, statusWrites(client), "the cached source cannot allow a new topology write")
	assert.Equal(t, configv1.SingleReplicaTopologyMode, currentInfra(t, c).Status.ControlPlaneTopology)
}

var evaluationTime = time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)

// seedExistingReport stores an available transition report for preservation tests
// without running background evaluation.
func seedExistingReport(c *TopologyTransitionController, client *configfakeclient.Clientset, infra *configv1.Infrastructure) {
	infra.Status.TopologyTransitionStatus = configv1.TopologyTransitionStatus{
		Conditions:  []metav1.Condition{{Type: configv1.TopologyTransitionsEvaluatedConditionType, Status: metav1.ConditionTrue, Reason: "EvaluationComplete", LastTransitionTime: metav1.NewTime(evaluationTime)}},
		Transitions: []configv1.TopologyTransition{{Source: topologyState(infra.Status), Target: configv1.TopologyState{ControlPlaneTopology: configv1.HighlyAvailableTopologyMode, InfrastructureTopology: configv1.HighlyAvailableTopologyMode}, Evaluations: []metav1.Condition{{Type: configv1.TopologyTransitionAvailableConditionType, Status: metav1.ConditionTrue}}}},
	}
	c.ensureTransitionConditions(infra)
	if err := client.Tracker().Update(configv1.SchemeGroupVersion.WithResource("infrastructures"), infra, ""); err != nil {
		panic(err)
	}
}

type failingOperatorStatusClient struct {
	v1helpers.OperatorClient
	err error
}

// UpdateOperatorStatus returns the configured failure to test rejection-report errors.
func (c failingOperatorStatusClient) UpdateOperatorStatus(context.Context, string, *operatorv1.OperatorStatus) (*operatorv1.OperatorStatus, error) {
	return nil, c.err
}
