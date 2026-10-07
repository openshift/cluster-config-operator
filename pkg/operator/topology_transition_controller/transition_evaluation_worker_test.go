package topology_transition_controller

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	configfakeclient "github.com/openshift/client-go/config/clientset/versioned/fake"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	applyoperatorv1 "github.com/openshift/client-go/operator/applyconfigurations/operator/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"
)

func TestTransitionSyncDoesNotEvaluateIdleInfrastructure(t *testing.T) {
	c, client, _ := statusTestController(snoInfra(""))
	c.preflightChecks = []PreflightCheck{{Type: "BackgroundOnly", Validate: func() error {
		t.Error("idle transition sync must not evaluate discovery")
		return errors.New("evaluation unavailable")
	}}}

	assert.NoError(t, c.sync(t.Context(), newTestSyncContext()))

	assert.Zero(t, statusWrites(client))
	assert.Equal(t, configv1.TopologyTransitionStatus{}, currentInfra(t, c).Status.TopologyTransitionStatus)
}

func TestEvaluationStartsWithoutTransitionQueueWork(t *testing.T) {
	controller, _ := newPublicEvaluationTestController(t, &evaluationTestInformer{SharedIndexInformer: v1helpers.NewFakeSharedIndexInformer()})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		controller.controller.Run(ctx, 0)
	}()
	defer func() { cancel(); <-done }()

	updated := receiveCompletedEvaluation(t, ctx, controller.writes)
	assert.Len(t, updated.Status.TopologyTransitionStatus.Transitions, 1)
}

type publicEvaluationTestController struct {
	controller factory.Controller
	writes     chan *configv1.Infrastructure
}

func newPublicEvaluationTestController(t *testing.T, informer cache.SharedIndexInformer) (publicEvaluationTestController, *configfakeclient.Clientset) {
	t.Helper()

	infra := snoInfra("")
	f := readyFixture()
	client := configfakeclient.NewSimpleClientset(infra)
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	assert.NoError(t, indexer.Add(infra))
	operatorClient := v1helpers.NewFakeOperatorClient(&operatorv1.OperatorSpec{}, &operatorv1.OperatorStatus{}, nil)
	writes := make(chan *configv1.Infrastructure, 1)
	client.PrependReactor("update", "infrastructures", func(action clienttesting.Action) (bool, runtime.Object, error) {
		writes <- action.(clienttesting.UpdateAction).GetObject().(*configv1.Infrastructure).DeepCopy()
		return false, nil, nil
	})
	controller := NewController(operatorClient, client.ConfigV1(), configlistersv1.NewInfrastructureLister(indexer), informer,
		f.nodeLister, informer, f.cmLister, informer, f.etcdLister, informer, f.coLister, informer, f.cvLister, informer,
		f.kasLister, informer, f.oasLister, informer, f.icLister, informer, f.mcLister, informer, f.mcpLister, informer,
		clocktesting.NewFakeClock(evaluationTime), newTestSyncContext().Recorder())

	return publicEvaluationTestController{controller, writes}, client
}

type evaluationTestInformer struct {
	cache.SharedIndexInformer
	handlers   chan cache.ResourceEventHandler
	removed    atomic.Int32
	registered atomic.Int32
	gate       <-chan struct{}
	checked    chan struct{}
	checkOnce  sync.Once
	failAdd    atomic.Bool
}

func (i *evaluationTestInformer) AddEventHandler(handler cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error) {
	if i.failAdd.Swap(false) {
		return nil, errors.New("listener registration unavailable")
	}
	i.registered.Add(1)
	if i.handlers != nil {
		i.handlers <- handler
	}

	return nil, nil
}

func (i *evaluationTestInformer) HasSynced() bool {
	if i.gate == nil {
		return true
	}
	i.checkOnce.Do(func() { close(i.checked) })

	select {
	case <-i.gate:
		return true
	default:
		return false
	}
}

func (i *evaluationTestInformer) RemoveEventHandler(cache.ResourceEventHandlerRegistration) error {
	i.removed.Add(1)

	return nil
}

type lockedEvaluationOperatorClient struct {
	v1helpers.OperatorClient
	mu        sync.Mutex
	reads     chan struct{}
	applies   chan string
	failApply atomic.Bool
}

func (c *lockedEvaluationOperatorClient) GetOperatorState() (*operatorv1.OperatorSpec, *operatorv1.OperatorStatus, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	spec, status, rv, err := c.OperatorClient.GetOperatorState()
	if c.reads != nil {
		c.reads <- struct{}{}
	}

	return spec.DeepCopy(), status.DeepCopy(), rv, err
}

func (c *lockedEvaluationOperatorClient) UpdateOperatorStatus(ctx context.Context, rv string, status *operatorv1.OperatorStatus) (*operatorv1.OperatorStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	updated, err := c.OperatorClient.UpdateOperatorStatus(ctx, rv, status)

	return updated.DeepCopy(), err
}

func (c *lockedEvaluationOperatorClient) ApplyOperatorStatus(ctx context.Context, manager string, status *applyoperatorv1.OperatorStatusApplyConfiguration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.applies != nil {
		c.applies <- manager
	}
	if c.failApply.Swap(false) {
		return errors.New("operator apply unavailable")
	}

	return c.OperatorClient.ApplyOperatorStatus(ctx, manager, status)
}

type evaluationTimer struct {
	after time.Duration
	timer clock.Timer
}

type observedEvaluationClock struct {
	*clocktesting.FakeClock
	timers chan evaluationTimer
}

func (c *observedEvaluationClock) NewTimer(after time.Duration) clock.Timer {
	timer := c.FakeClock.NewTimer(after)
	c.timers <- evaluationTimer{after, timer}

	return timer
}

func receiveEvaluation[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()

	select {
	case value := <-ch:
		return value
	case <-ctx.Done():
		t.Fatal("evaluation test did not reach the next step")
		var zero T
		return zero
	}
}

func startTestEvaluation(t *testing.T, c *TopologyTransitionController) (context.Context, context.CancelFunc, <-chan struct{}, *evaluationTestInformer, *observedEvaluationClock) {
	t.Helper()

	informer := &evaluationTestInformer{
		SharedIndexInformer: v1helpers.NewFakeSharedIndexInformer(),
		handlers:            make(chan cache.ResourceEventHandler, 1),
	}

	return startTestEvaluationWithInformer(t, c, informer)
}

func startTestEvaluationWithInformer(t *testing.T, c *TopologyTransitionController, informer *evaluationTestInformer) (context.Context, context.CancelFunc, <-chan struct{}, *evaluationTestInformer, *observedEvaluationClock) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	clk := &observedEvaluationClock{FakeClock: clocktesting.NewFakeClock(evaluationTime), timers: make(chan evaluationTimer, 100)}
	c.clock = clk
	c.evaluationInformers = []cache.SharedIndexInformer{informer}
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = c.runEvaluation(ctx, newTestSyncContext())
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cleanupCancel()
		receiveEvaluation(t, cleanupCtx, done)
		assert.NoError(t, runErr)
		assert.Equal(t, int32(1), informer.removed.Load())
	})

	return ctx, cancel, done, informer, clk
}

func TestEvaluationRetriesListenerFailureInsteadOfStoppingHook(t *testing.T) {
	c, client, _ := statusTestController(snoInfra(""))
	operatorClient := &lockedEvaluationOperatorClient{OperatorClient: c.operatorClient, applies: make(chan string, 10)}
	c.operatorClient = operatorClient
	informer := &evaluationTestInformer{
		SharedIndexInformer: v1helpers.NewFakeSharedIndexInformer(),
		handlers:            make(chan cache.ResourceEventHandler, 1),
	}
	informer.failAdd.Store(true)
	ctx, _, _, _, clk := startTestEvaluationWithInformer(t, c, informer)

	receiveEvaluation(t, ctx, operatorClient.applies)
	waitEvaluationTimer(t, ctx, clk, time.Second)
	assert.Zero(t, statusWrites(client), "evaluation must wait for its listeners")
	_, status, _, err := c.operatorClient.GetOperatorState()
	assert.NoError(t, err)
	assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, evaluationDegradedCondition))
	clk.Step(time.Second)
	receiveEvaluation(t, ctx, informer.handlers)
	receiveEvaluation(t, ctx, operatorClient.applies)
	_, status, _, err = c.operatorClient.GetOperatorState()
	assert.NoError(t, err)
	assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, evaluationDegradedCondition))
	assert.Equal(t, 1, statusWrites(client))
}

func TestEvaluationRefreshesReportWithoutRewritingUnchangedDegradedCondition(t *testing.T) {
	c, client, _ := statusTestController(snoInfra(""))
	operatorClient := &lockedEvaluationOperatorClient{OperatorClient: c.operatorClient, applies: make(chan string, 10)}
	c.operatorClient = operatorClient
	var failure error
	c.preflightChecks = []PreflightCheck{{Type: "Controlled", Validate: func() error { return failure }}}

	assert.NoError(t, c.syncEvaluation(t.Context()))
	assert.NoError(t, c.syncEvaluation(t.Context()))
	assert.Equal(t, 1, len(operatorClient.applies))
	assert.Equal(t, 4, statusWrites(client))

	failure = &clusterStateReadError{err: errors.New("node cache unavailable")}
	assert.Error(t, c.syncEvaluation(t.Context()))
	assert.Error(t, c.syncEvaluation(t.Context()))
	assert.Equal(t, 2, len(operatorClient.applies), "stable failure must not rewrite the degraded condition")
	assert.Equal(t, 8, statusWrites(client))

	failure = nil
	assert.NoError(t, c.syncEvaluation(t.Context()))
	assert.NoError(t, c.syncEvaluation(t.Context()))
	assert.Equal(t, 3, len(operatorClient.applies))
	assert.Equal(t, 12, statusWrites(client))
}

func TestEvaluationCoalescesBurstAndRunsPendingPassThenPeriodicRefresh(t *testing.T) {
	c, client, _ := statusTestController(snoInfra(""))
	operatorClient := &lockedEvaluationOperatorClient{OperatorClient: c.operatorClient, reads: make(chan struct{}, 10)}
	c.operatorClient = operatorClient
	started := make(chan int32, 10)
	release := make(chan struct{})
	var calls atomic.Int32
	c.preflightChecks = []PreflightCheck{{Type: "Controlled", Validate: func() error {
		started <- calls.Add(1)
		select {
		case <-release:
		case <-t.Context().Done():
		}
		return nil
	}}}
	ctx, _, _, informer, clk := startTestEvaluation(t, c)
	t.Cleanup(func() { close(release) })
	handler := receiveEvaluation(t, ctx, informer.handlers)
	assert.Equal(t, int32(1), receiveEvaluation(t, ctx, started))

	for range 100 {
		handler.OnAdd(newTestControlPlaneNode("master", false), false)
	}
	assert.Equal(t, int32(1), calls.Load(), "events must not start concurrent evaluations")
	release <- struct{}{}
	assert.Equal(t, int32(2), receiveEvaluation(t, ctx, started), "one pending follow-up must run")
	assert.Equal(t, 3, statusWrites(client))
	receiveEvaluation(t, ctx, operatorClient.reads)
	release <- struct{}{}
	receiveEvaluation(t, ctx, operatorClient.reads)
	assert.Equal(t, int32(2), calls.Load(), "a burst should yield only one follow-up")
	assert.Equal(t, 4, statusWrites(client), "each pass reports pending before its result")

	receiveEvaluation(t, ctx, clk.timers)
	clk.Step(time.Minute)
	assert.Equal(t, int32(3), receiveEvaluation(t, ctx, started), "periodic evaluation runs without new events")
}

func waitEvaluationTimer(t *testing.T, ctx context.Context, clk *observedEvaluationClock, after time.Duration) {
	t.Helper()

	for {
		if receiveEvaluation(t, ctx, clk.timers).after == after {
			return
		}
	}
}

func TestEvaluationRetriesReportAndDegradedWriteFailures(t *testing.T) {
	for _, fault := range []string{"report write", "degraded write", "read error"} {
		t.Run(fault, func(t *testing.T) {
			c, client, _ := statusTestController(snoInfra(""))
			operatorClient := &lockedEvaluationOperatorClient{OperatorClient: c.operatorClient, applies: make(chan string, 10)}
			c.operatorClient = operatorClient
			var failed atomic.Bool
			failed.Store(true)
			var checks atomic.Int32
			c.preflightChecks = []PreflightCheck{{Type: "Controlled", Validate: func() error {
				checks.Add(1)
				if fault == "read error" && failed.Load() {
					return &clusterStateReadError{err: errors.New("node cache unavailable")}
				}
				if !failed.Load() {
					return errors.New("current blocker")
				}
				return nil
			}}}
			if fault == "report write" {
				client.PrependReactor("update", "infrastructures", func(clienttesting.Action) (bool, runtime.Object, error) {
					if failed.Load() {
						return true, nil, errors.New("report write unavailable")
					}
					return false, nil, nil
				})
			}
			operatorClient.failApply.Store(fault == "degraded write")
			ctx, _, _, _, clk := startTestEvaluation(t, c)
			receiveEvaluation(t, ctx, operatorClient.applies)
			waitEvaluationTimer(t, ctx, clk, time.Second)

			_, status, _, err := c.operatorClient.GetOperatorState()
			assert.NoError(t, err)
			if fault != "degraded write" {
				assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, evaluationDegradedCondition))
			}
			failed.Store(false)
			clk.Step(time.Second)
			receiveEvaluation(t, ctx, operatorClient.applies)
			waitEvaluationTimer(t, ctx, clk, time.Minute)

			_, status, _, err = c.operatorClient.GetOperatorState()
			assert.NoError(t, err)
			assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, evaluationDegradedCondition))
			stored := currentInfra(t, c).Status.TopologyTransitionStatus
			if assert.Len(t, stored.Transitions, 1) {
				check := meta.FindStatusCondition(stored.Transitions[0].Evaluations, "Controlled")
				if assert.NotNil(t, check) {
					assert.Equal(t, metav1.ConditionFalse, check.Status)
					assert.Equal(t, "current blocker", check.Message, "retry must evaluate current state")
				}
			}
			wantChecks := int32(2)
			if fault == "report write" {
				wantChecks = 1 // pending write failed before checks could start
			}
			assert.Equal(t, wantChecks, checks.Load(), "retry must evaluate after pending status can be written")
		})
	}
}

func TestEvaluationErrorDoesNotDegradeSuccessfulTransitionSync(t *testing.T) {
	c, _, _ := statusTestController(snoInfra(""))
	c.preflightChecks = []PreflightCheck{{Type: "Unreadable", Validate: func() error {
		return &clusterStateReadError{err: errors.New("node cache unavailable")}
	}}}
	assert.Error(t, c.syncEvaluation(t.Context()))
	operatorClient := &lockedEvaluationOperatorClient{OperatorClient: c.operatorClient, applies: make(chan string, 10)}
	c.operatorClient = operatorClient
	syncCtx := newTestSyncContext()
	controller := factory.New().WithSync(c.sync).WithSyncContext(syncCtx).
		WithSyncDegradedOnError(c.operatorClient).ToController("TopologyTransitionController", syncCtx.Recorder())
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	done := make(chan struct{})
	go func() { controller.Run(ctx, 1); close(done) }()
	defer func() { cancel(); <-done }()

	syncCtx.Queue().Add(factory.DefaultQueueKey)
	manager := receiveEvaluation(t, ctx, operatorClient.applies)

	assert.Equal(t, factory.ControllerFieldManager("TopologyTransitionController", "reportDegraded"), manager)
	_, status, _, err := c.operatorClient.GetOperatorState()
	assert.NoError(t, err)
	assert.True(t, v1helpers.IsOperatorConditionTrue(status.Conditions, evaluationDegradedCondition))
	assert.True(t, v1helpers.IsOperatorConditionFalse(status.Conditions, "TopologyTransitionControllerDegraded"))
}

func TestPausedTargetEvaluationDoesNotBlockSafeRequestOrProgress(t *testing.T) {
	infra := newTestInfra(configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.HighlyAvailableTopologyMode, configv1.NonePlatformType)
	infra.ResourceVersion = "1"
	c, client, _ := statusTestController(infra)
	c.operatorClient = &lockedEvaluationOperatorClient{OperatorClient: c.operatorClient, applies: make(chan string, 10)}
	blocked, release := make(chan struct{}), make(chan struct{})
	var targetChecks atomic.Int32
	var requestChecks atomic.Int32
	var progressChecks atomic.Int32
	c.transitions[0].PreflightValidators = []PreflightCheck{{Type: "RequestMandatory", Validate: func() error { requestChecks.Add(1); return nil }}}
	c.transitions[0].TransitionValidators = []TransitionValidatorFunc{func() error { progressChecks.Add(1); return nil }}
	target := noopTransitions()[0]
	target.From.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
	target.From.InfrastructureTopology = configv1.HighlyAvailableTopologyMode
	target.To.ControlPlaneTopology = configv1.SingleReplicaTopologyMode
	target.UpdateStatus = func(infra *configv1.Infrastructure) {
		infra.Status.ControlPlaneTopology = configv1.SingleReplicaTopologyMode
		infra.Status.InfrastructureTopology = configv1.SingleReplicaTopologyMode
	}
	target.PreflightValidators = []PreflightCheck{{Type: "TargetOnly", Validate: func() error {
		if targetChecks.Add(1) == 1 {
			close(blocked)
			<-release
		}
		return nil
	}}}
	c.transitions = append(c.transitions, target)
	var revision atomic.Int32
	revision.Store(2)
	var conflicts atomic.Int32
	resource := configv1.SchemeGroupVersion.WithResource("infrastructures")
	reports := make(chan *configv1.Infrastructure, 1)
	client.PrependReactor("update", "infrastructures", func(action clienttesting.Action) (bool, runtime.Object, error) {
		update := action.(clienttesting.UpdateAction).GetObject().(*configv1.Infrastructure).DeepCopy()
		live, err := client.Tracker().Get(resource, "", "cluster")
		if err != nil {
			return true, nil, err
		}
		if update.ResourceVersion != live.(*configv1.Infrastructure).ResourceVersion {
			conflicts.Add(1)
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "infrastructures"}, "cluster", errors.New("source changed"))
		}
		update.ResourceVersion = strconv.Itoa(int(revision.Add(1)))
		if err := client.Tracker().Update(resource, update, ""); err != nil {
			return true, nil, err
		}

		if meta.IsStatusConditionTrue(update.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionsEvaluatedConditionType) {
			select {
			case reports <- update.DeepCopy():
			default:
			}
		}

		return true, update, nil
	})
	ctx, _, _, _, clk := startTestEvaluation(t, c)
	t.Cleanup(func() { close(release) })
	receiveEvaluation(t, ctx, blocked)

	// A fresh source/request arrives while the old target-side pass is paused.
	requested := snoInfra(configv1.HighlyAvailableTopologyMode)
	requested.ResourceVersion = "2"
	assert.NoError(t, client.Tracker().Update(resource, requested, ""))
	assert.NoError(t, c.sync(t.Context(), newTestSyncContext()))
	assert.Equal(t, int32(1), requestChecks.Load(), "request checks must remain mandatory")
	assert.Equal(t, configv1.HighlyAvailableTopologyMode, currentInfra(t, c).Status.ControlPlaneTopology)
	assert.Equal(t, reasonTopologyTransitionInProgress, completionCondition(t, c).Reason)
	clk.Step(minReconciliationSoakTime)
	assert.NoError(t, c.sync(t.Context(), newTestSyncContext()))
	assert.Equal(t, int32(1), progressChecks.Load())
	assert.Equal(t, metav1.ConditionTrue, completionCondition(t, c).Status)
	assert.Equal(t, int32(1), targetChecks.Load(), "both main syncs completed while evaluation was paused")

	release <- struct{}{}
	// Check the finished report; the next periodic pass can already be pending.
	stored := receiveCompletedEvaluation(t, ctx, reports)

	assert.Equal(t, int32(1), conflicts.Load())
	assert.Equal(t, configv1.HighlyAvailableTopologyMode, stored.Status.ControlPlaneTopology)
	completion := meta.FindStatusCondition(stored.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionCompletedConditionType)
	if assert.NotNil(t, completion) {
		assert.Equal(t, metav1.ConditionTrue, completion.Status, "conflicted evaluation must preserve completion")
	}
	if assert.Len(t, stored.Status.TopologyTransitionStatus.Transitions, 1) {
		assert.Equal(t, configv1.HighlyAvailableTopologyMode, stored.Status.TopologyTransitionStatus.Transitions[0].Source.ControlPlaneTopology)
	}
}

func TestEvaluationRelevantEventsEnqueueAddUpdateAndDelete(t *testing.T) {
	for _, obj := range []runtime.Object{
		snoInfra(""), newTestControlPlaneNode("master", false), newTestEtcdEndpointsConfigMap(3), newTestEtcdCR(true, false),
		newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse), newTestClusterVersion(false),
	} {
		t.Run(obj.GetObjectKind().GroupVersionKind().Kind+"/"+metaObjectName(obj), func(t *testing.T) {
			queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[evaluationKey]())
			defer queue.ShutDown()
			handler := evaluationEventHandler(queue)
			handler.OnAdd(obj, false)
			assert.Equal(t, 1, queue.Len())
			key, _ := queue.Get()
			queue.Done(key)
			handler.OnDelete(cache.DeletedFinalStateUnknown{Obj: obj})
			assert.Equal(t, 1, queue.Len())
			key, _ = queue.Get()
			queue.Done(key)

			updated := obj.DeepCopyObject()
			switch updated := updated.(type) {
			case *configv1.Infrastructure:
				updated.Generation++
			case *corev1.Node:
				updated.Spec.Unschedulable = true
			case *corev1.ConfigMap:
				updated.Data["new-member"] = "192.0.2.1"
			case *operatorv1.Etcd:
				updated.Status.Conditions[0].Status = operatorv1.ConditionFalse
			case *configv1.ClusterOperator:
				updated.Status.Conditions[0].Status = configv1.ConditionFalse
			case *configv1.ClusterVersion:
				updated.Status.Conditions[0].Status = configv1.ConditionTrue
			}
			handler.OnUpdate(obj, updated)
			assert.Equal(t, 1, queue.Len())
		})
	}
}

func metaObjectName(obj runtime.Object) string {
	accessor, _ := meta.Accessor(obj)

	return accessor.GetName()
}

func TestEvaluationWaitsForAllCachesAndDoesNotStartWhenStoppedEarly(t *testing.T) {
	for _, stopEarly := range []bool{false, true} {
		t.Run(map[bool]string{false: "cache becomes ready", true: "stop before cache ready"}[stopEarly], func(t *testing.T) {
			gate := make(chan struct{})
			informer := &evaluationTestInformer{SharedIndexInformer: v1helpers.NewFakeSharedIndexInformer(), gate: gate, checked: make(chan struct{})}
			controller, client := newPublicEvaluationTestController(t, informer)
			registrations := informer.registered.Load()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			done := make(chan struct{})
			go func() { controller.controller.Run(ctx, 0); close(done) }()
			defer func() { cancel(); <-done }()
			receiveEvaluation(t, ctx, informer.checked)

			assert.Zero(t, statusWrites(client), "no evaluation before cache sync")
			assert.Equal(t, registrations, informer.registered.Load(), "evaluation listeners must not start before cache sync")
			if stopEarly {
				cancel()
				receiveEvaluation(t, t.Context(), done)
				assert.Equal(t, registrations, informer.registered.Load())
				assert.Zero(t, informer.removed.Load(), "stopping before sync must not create evaluation listeners")
				return
			}

			close(gate)
			updated := receiveCompletedEvaluation(t, ctx, controller.writes)
			assert.Len(t, updated.Status.TopologyTransitionStatus.Transitions, 1)
			cancel()
			receiveEvaluation(t, t.Context(), done)
			assert.Equal(t, registrations+6, informer.registered.Load())
			assert.Equal(t, int32(6), informer.removed.Load(), "Run must wait for evaluation hook cleanup")
		})
	}
}

type blockingEvaluationInfrastructureClient struct {
	configv1client.InfrastructureInterface
	started chan struct{}
}

func (c blockingEvaluationInfrastructureClient) Get(ctx context.Context, name string, options metav1.GetOptions) (*configv1.Infrastructure, error) {
	close(c.started)
	<-ctx.Done()

	return nil, ctx.Err()
}

func TestEvaluationShutdownIdleInFlightAndDelayedRetry(t *testing.T) {
	for _, state := range []string{"idle", "in flight", "delayed retry"} {
		t.Run(state, func(t *testing.T) {
			c, _, _ := statusTestController(snoInfra(""))
			operatorClient := &lockedEvaluationOperatorClient{OperatorClient: c.operatorClient, applies: make(chan string, 10)}
			c.operatorClient = operatorClient
			started := make(chan struct{})
			var checks atomic.Int32
			if state == "in flight" {
				c.infraClient = blockingEvaluationInfrastructureClient{InfrastructureInterface: c.infraClient, started: started}
			}
			if state == "delayed retry" {
				c.preflightChecks = []PreflightCheck{{Type: "Unreadable", Validate: func() error {
					checks.Add(1)
					return &clusterStateReadError{err: errors.New("node cache unavailable")}
				}}}
			}
			ctx, cancel, done, _, clk := startTestEvaluation(t, c)
			if state == "in flight" {
				receiveEvaluation(t, ctx, started)
			} else {
				receiveEvaluation(t, ctx, operatorClient.applies)
				if state == "delayed retry" {
					waitEvaluationTimer(t, ctx, clk, time.Second)
				} else {
					waitEvaluationTimer(t, ctx, clk, time.Minute)
				}
			}

			cancel()
			receiveEvaluation(t, t.Context(), done)

			if state == "delayed retry" {
				assert.Equal(t, int32(1), checks.Load(), "cancel must not wait for or run the delayed retry")
			}
		})
	}
}

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
	c.preflightChecks = []PreflightCheck{{Type: "Mandatory", Validate: func() error { checks++; return nil }}}

	assert.ErrorIs(t, c.sync(t.Context(), newTestSyncContext()), readErr)

	assert.Equal(t, 1, checks, "live read failures must not skip mandatory cached checks")
	assert.Zero(t, statusWrites(client), "the cached source cannot allow a new topology write")
	assert.Equal(t, configv1.SingleReplicaTopologyMode, currentInfra(t, c).Status.ControlPlaneTopology)
}

func TestEvaluationEventsIgnoreReportsAndUnrelatedStatus(t *testing.T) {
	infra := snoInfra("")
	report := infra.DeepCopy()
	report.Status.TopologyTransitionStatus = configv1.TopologyTransitionStatus{Conditions: []metav1.Condition{{
		Type: configv1.TopologyTransitionsEvaluatedConditionType, Status: metav1.ConditionTrue,
	}}}
	node := newTestControlPlaneNodeWithConditions("master", false, readyNodeCondition())
	heartbeat := node.DeepCopy()
	heartbeat.Status.Conditions[0].LastHeartbeatTime = metav1.NewTime(evaluationTime)
	co := newTestClusterOperator("etcd", configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse)
	coOther := co.DeepCopy()
	coOther.Status.Conditions = append(coOther.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorUpgradeable, Status: configv1.ConditionFalse})
	etcd := newTestEtcdCR(true, false)
	etcdOther := etcd.DeepCopy()
	etcdOther.Status.Conditions = append(etcdOther.Status.Conditions, operatorv1.OperatorCondition{Type: "UnrelatedDegraded", Status: operatorv1.ConditionTrue})
	cv := newTestClusterVersion(false)
	cvOther := cv.DeepCopy()
	cvOther.Status.Conditions = append(cvOther.Status.Conditions, configv1.ClusterOperatorStatusCondition{Type: configv1.OperatorAvailable, Status: configv1.ConditionFalse})
	self := newTestClusterOperator(selfClusterOperatorName, configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionFalse)
	selfDegraded := newTestClusterOperator(selfClusterOperatorName, configv1.ConditionTrue, configv1.ConditionFalse, configv1.ConditionTrue)

	for _, tc := range []struct {
		name         string
		old, updated any
	}{
		{"evaluation report", infra, report},
		{"node heartbeat", node, heartbeat},
		{"other operator condition", co, coOther},
		{"unrelated etcd status", etcd, etcdOther},
		{"unrelated version status", cv, cvOther},
		{"self degraded", self, selfDegraded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queue := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[evaluationKey]())
			defer queue.ShutDown()

			evaluationEventHandler(queue).OnUpdate(tc.old, tc.updated)

			assert.Zero(t, queue.Len(), "unchanged preflight inputs must not enqueue evaluation")
		})
	}
}

func TestTopologyWriteInvalidatesEvaluationWithoutRunningTargetChecks(t *testing.T) {
	infra := snoInfra(configv1.HighlyAvailableTopologyMode)
	c, client, _ := statusTestController(infra)
	assert.NoError(t, c.mergeDiscovery(infra))
	assert.NoError(t, client.Tracker().Update(configv1.SchemeGroupVersion.WithResource("infrastructures"), infra, ""))
	target := noopTransitions()[0]
	target.From.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
	target.From.InfrastructureTopology = configv1.HighlyAvailableTopologyMode
	target.PreflightValidators = []PreflightCheck{{Type: "TargetOnly", Validate: func() error {
		t.Error("topology writes must not run target-side discovery checks")
		return errors.New("target evaluation unavailable")
	}}}
	c.transitions = append(c.transitions, target)

	assert.NoError(t, c.sync(t.Context(), newTestSyncContext()))

	stored := currentInfra(t, c)
	assert.Equal(t, configv1.HighlyAvailableTopologyMode, stored.Status.ControlPlaneTopology)
	assert.Empty(t, stored.Status.TopologyTransitionStatus.Transitions)
	evaluated := meta.FindStatusCondition(stored.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionsEvaluatedConditionType)
	if assert.NotNil(t, evaluated) {
		assert.Equal(t, metav1.ConditionUnknown, evaluated.Status)
		assert.Equal(t, "EvaluationPending", evaluated.Reason)
	}
	assert.Equal(t, reasonTopologyTransitionInProgress, completionCondition(t, c).Reason)
}

func receiveCompletedEvaluation(t *testing.T, ctx context.Context, reports <-chan *configv1.Infrastructure) *configv1.Infrastructure {
	t.Helper()

	for {
		report := receiveEvaluation(t, ctx, reports)
		if !meta.IsStatusConditionPresentAndEqual(report.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionsEvaluatedConditionType, metav1.ConditionUnknown) {
			return report
		}
	}
}
