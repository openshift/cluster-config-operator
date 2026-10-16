package topology_transition_controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	applyoperatorv1 "github.com/openshift/client-go/operator/applyconfigurations/operator/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
)

const evaluationDegradedCondition = "TopologyTransitionEvaluationDegraded"

type evaluationKey struct{}

// runEvaluation starts only after the factory has synced all caches. The queue
// and listeners belong to this hook, so cancellation before cache sync creates
// no evaluation workers. A single key coalesces bursts, including changes made
// while an evaluation is running, without blocking transition reconciliation.
func (c *TopologyTransitionController) runEvaluation(ctx context.Context, _ factory.SyncContext) error {
	if ctx.Err() != nil {
		return nil
	}

	clk, ok := c.clock.(clock.WithTicker)
	if !ok {
		clk = clock.RealClock{}
	}
	queue := workqueue.NewTypedRateLimitingQueueWithConfig(
		workqueue.NewTypedItemExponentialFailureRateLimiter[evaluationKey](time.Second, time.Minute),
		workqueue.TypedRateLimitingQueueConfig[evaluationKey]{Clock: clk},
	)
	defer queue.ShutDown()
	stop := context.AfterFunc(ctx, queue.ShutDown)
	defer stop()

	nextInformer := 0
	queue.Add(evaluationKey{})
	for {
		key, shutdown := queue.Get()
		if shutdown || ctx.Err() != nil {
			return nil
		}

		// The delayed key also supplies the periodic refresh. Retrying sooner
		// replaces it; each pass schedules the next refresh within one minute.
		queue.AddAfter(key, time.Minute)

		// Keep successful registrations while retrying a failed one. A hook
		// setup error must not permanently disable background evaluation.
		var err error
		for nextInformer < len(c.evaluationInformers) && ctx.Err() == nil {
			informer := c.evaluationInformers[nextInformer]
			handler, registrationErr := informer.AddEventHandler(evaluationEventHandler(queue))
			if registrationErr != nil {
				err = fmt.Errorf("register evaluation listener: %w", registrationErr)
				break
			}
			defer func() {
				if err := informer.RemoveEventHandler(handler); err != nil {
					klog.Warningf("Removing topology evaluation listener: %v", err)
				}
			}()
			nextInformer++
		}

		if err != nil {
			err = c.reportEvaluationDegraded(ctx, err)
		} else {
			err = c.syncEvaluation(ctx)
		}
		if ctx.Err() == nil {
			if err != nil {
				klog.Warningf("Topology transition evaluation failed: %v", err)
				queue.AddRateLimited(key)
			} else {
				queue.Forget(key)
			}
		}
		queue.Done(key)
	}
}

func (c *TopologyTransitionController) syncEvaluation(ctx context.Context) error {
	_, err := c.refreshDiscovery(ctx)

	return c.reportEvaluationDegraded(ctx, err)
}

func (c *TopologyTransitionController) reportEvaluationDegraded(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return err
	}

	status, reason, message := operatorv1.ConditionFalse, "AsExpected", ""
	if err != nil {
		status, reason, message = operatorv1.ConditionTrue, "EvaluationError", err.Error()
	}

	_, operatorStatus, _, readErr := c.operatorClient.GetOperatorState()
	if readErr != nil {
		return errors.Join(err, readErr)
	}

	old := v1helpers.FindOperatorCondition(operatorStatus.Conditions, evaluationDegradedCondition)
	if old != nil && old.Status == status && old.Reason == reason && old.Message == message {
		return err
	}

	condition := applyoperatorv1.OperatorStatus().WithConditions(applyoperatorv1.OperatorCondition().
		WithType(evaluationDegradedCondition).WithStatus(status).WithReason(reason).WithMessage(message))
	writeErr := c.operatorClient.ApplyOperatorStatus(ctx,
		factory.ControllerFieldManager("TopologyTransitionController", "evaluationDegraded"), condition)

	return errors.Join(err, writeErr)
}

func evaluationEventHandler(queue workqueue.TypedRateLimitingInterface[evaluationKey]) cache.ResourceEventHandler {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			runtimeObj, _ := obj.(runtime.Object)

			if _, relevant := evaluationInput(runtimeObj); relevant {
				queue.Add(evaluationKey{})
			}
		},
		UpdateFunc: func(old, updated any) {
			oldObj, _ := old.(runtime.Object)
			updatedObj, _ := updated.(runtime.Object)
			oldInput, oldRelevant := evaluationInput(oldObj)
			updatedInput, updatedRelevant := evaluationInput(updatedObj)

			if oldRelevant != updatedRelevant || !equality.Semantic.DeepEqual(oldInput, updatedInput) {
				queue.Add(evaluationKey{})
			}
		},
		DeleteFunc: func(obj any) {
			if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}
			runtimeObj, _ := obj.(runtime.Object)

			if _, relevant := evaluationInput(runtimeObj); relevant {
				queue.Add(evaluationKey{})
			}
		},
	}
}

type evaluationInputSnapshot struct {
	Infrastructure            *evaluationInfrastructureInput
	Node                      *evaluationNodeInput
	EtcdEndpoints             map[string]string
	EtcdConditions            map[string]operatorv1.OperatorCondition
	ClusterOperatorConditions map[configv1.ClusterStatusConditionType]configv1.ConditionStatus
	ClusterVersionConditions  map[configv1.ClusterStatusConditionType]configv1.ConditionStatus
}

type evaluationInfrastructureInput struct {
	Generation int64
	Topology   configv1.TopologyState
	Platform   configv1.PlatformType
}

type evaluationNodeInput struct {
	ControlPlane  bool
	Worker        bool
	WorkerValue   string
	Unschedulable bool
	Ready         bool
}

// Keep report-only Infrastructure writes and our own ClusterOperator status out
// of the evaluation inputs. Resource versions and unrelated metadata are not
// inputs either. Only preflight informers are registered with this handler.
func evaluationInput(obj runtime.Object) (evaluationInputSnapshot, bool) {
	switch obj := obj.(type) {
	case *configv1.Infrastructure:
		if obj.Name != "cluster" {
			return evaluationInputSnapshot{}, false
		}

		var platform configv1.PlatformType
		if obj.Status.PlatformStatus != nil {
			platform = obj.Status.PlatformStatus.Type
		}

		return evaluationInputSnapshot{Infrastructure: &evaluationInfrastructureInput{
			Generation: obj.Generation,
			Topology:   topologyState(obj.Status),
			Platform:   platform,
		}}, true

	case *corev1.Node:
		workerValue, worker := obj.Labels["node-role.kubernetes.io/worker"]
		return evaluationInputSnapshot{Node: &evaluationNodeInput{
			ControlPlane:  isControlPlaneNode(obj),
			Worker:        worker,
			WorkerValue:   workerValue,
			Unschedulable: obj.Spec.Unschedulable,
			Ready:         countReadyNodes([]*corev1.Node{obj}) == 1,
		}}, true

	case *corev1.ConfigMap:
		if obj.Namespace == etcdNamespace && obj.Name == etcdEndpointsConfigMapName {
			return evaluationInputSnapshot{EtcdEndpoints: obj.Data}, true
		}

	case *operatorv1.Etcd:
		if obj.Name == "cluster" {
			conditions := map[string]operatorv1.OperatorCondition{}
			for _, condition := range obj.Status.Conditions {
				if condition.Type == etcdMembersAvailableCondition || condition.Type == etcdMembersProgressingCondition {
					input := operatorv1.OperatorCondition{Type: condition.Type, Status: condition.Status}

					if condition.Type == etcdMembersProgressingCondition {
						input.Message = condition.Message
					}

					conditions[condition.Type] = input
				}
			}

			return evaluationInputSnapshot{EtcdConditions: conditions}, true
		}

	case *configv1.ClusterOperator:
		if obj.Name != selfClusterOperatorName {
			return evaluationInputSnapshot{
				ClusterOperatorConditions: evaluationConfigConditions(obj.Status.Conditions, configv1.OperatorAvailable, configv1.OperatorProgressing, configv1.OperatorDegraded),
			}, true
		}

	case *configv1.ClusterVersion:
		if obj.Name == clusterVersionName {
			return evaluationInputSnapshot{
				ClusterVersionConditions: evaluationConfigConditions(obj.Status.Conditions, configv1.OperatorProgressing),
			}, true
		}
	}

	return evaluationInputSnapshot{}, false
}

func evaluationConfigConditions(conditions []configv1.ClusterOperatorStatusCondition, types ...configv1.ClusterStatusConditionType) map[configv1.ClusterStatusConditionType]configv1.ConditionStatus {
	inputs := map[configv1.ClusterStatusConditionType]configv1.ConditionStatus{}
	for _, condition := range conditions {
		if slices.Contains(types, condition.Type) {
			inputs[condition.Type] = condition.Status
		}
	}

	return inputs
}
