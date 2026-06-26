package topology_transition_controller

import (
	"context"
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	operatorv1listers "github.com/openshift/client-go/operator/listers/operator/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"
	klog "k8s.io/klog/v2"
	"k8s.io/utils/clock"
)

const (
	transitionCondition  = "TopologyTransitionControllerProgressing"
	upgradeableCondition = "TopologyTransitionControllerUpgradeable"

	reasonTopologyTransitionInProgress = "TopologyTransitionInProgress"

	// minReconciliationSoakTime is the minimum time to wait after a transition
	// starts before accepting reconciliation checks as passing. This prevents
	// premature completion when downstream operators haven't started progressing yet.
	minReconciliationSoakTime = 5 * time.Minute
)

// TopologyTransitionController manages day-2 control plane topology transitions.
// It watches for changes to the desired topology in the Infrastructure spec,
// validates the transition, updates Infrastructure status, and monitors
// downstream workloads to verify reconciliation.
type TopologyTransitionController struct {
	operatorClient       v1helpers.OperatorClient
	infraLister          configlistersv1.InfrastructureLister
	infraClient          configv1client.InfrastructureInterface
	reconciliationChecks []func(context.Context) (bool, error)
	transitions          []TransitionDescriptor
	clock                clock.PassiveClock
}

// NewController returns a new TopologyTransitionController.
func NewController(
	operatorClient v1helpers.OperatorClient,
	infraClient configv1client.InfrastructuresGetter,
	infraLister configlistersv1.InfrastructureLister,
	infraInformer cache.SharedIndexInformer,
	nodeLister corev1listers.NodeLister,
	nodeInformer cache.SharedIndexInformer,
	etcdConfigMapLister corev1listers.ConfigMapNamespaceLister,
	etcdConfigMapInformer cache.SharedIndexInformer,
	etcdLister operatorv1listers.EtcdLister,
	etcdOperatorInformer cache.SharedIndexInformer,
	clusterOperatorLister configlistersv1.ClusterOperatorLister,
	clusterOperatorInformer cache.SharedIndexInformer,
	clk clock.PassiveClock,
	recorder events.Recorder,
) factory.Controller {
	c := &TopologyTransitionController{
		operatorClient: operatorClient,
		infraLister:    infraLister,
		infraClient:    infraClient.Infrastructures(),
		reconciliationChecks: []func(context.Context) (bool, error){
			checkAllClusterOperatorsStable(clusterOperatorLister),
		},
		transitions: buildSupportedTransitions(nodeLister, etcdConfigMapLister, etcdLister),
		clock:       clk,
	}
	return factory.New().
		WithInformers(
			operatorClient.Informer(),
			infraInformer,
			nodeInformer,
			etcdConfigMapInformer,
			etcdOperatorInformer,
			clusterOperatorInformer,
		).
		WithSync(c.sync).
		WithSyncDegradedOnError(operatorClient).
		ResyncEvery(time.Minute).
		ToController("TopologyTransitionController", recorder)
}

// sync reconciles the desired and current control plane topology.
// If a transition is requested, it validates, sets a progressing condition,
// and updates Infrastructure status. If a transition is in progress,
// it checks downstream workloads and clears the condition when complete.
func (c *TopologyTransitionController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	infra, err := c.infraLister.Get("cluster")
	if errors.IsNotFound(err) {
		syncCtx.Recorder().Warningf("TopologyTransitionController", "Required infrastructures.%s/cluster not found", configv1.GroupName)
		return nil
	}
	if err != nil {
		return err
	}

	specTopology := infra.Spec.ControlPlaneTopology
	statusTopology := infra.Status.ControlPlaneTopology

	// Three states:
	// 1. spec != status → a transition was requested, run reconcileTransition
	// 2. spec == status, Progressing=True → transition applied, awaiting downstream reconciliation
	// 3. spec == status, Progressing!=True → idle, ensure Upgradeable=True
	if specTopology == "" || specTopology == statusTopology {
		_, status, _, err := c.operatorClient.GetOperatorState()
		if err != nil {
			return err
		}

		if !v1helpers.IsOperatorConditionTrue(status.Conditions, transitionCondition) {
			// Safety net: if Upgradeable is stuck False from a completed transition
			// whose progressing condition was lost (e.g. partial failure), route
			// through reconciliation checks before re-enabling upgrades.
			upgCond := v1helpers.FindOperatorCondition(status.Conditions, upgradeableCondition)
			if upgCond != nil && upgCond.Status == operatorv1.ConditionFalse && upgCond.Reason == reasonTopologyTransitionInProgress {
				return c.checkClusterReconciliation(ctx)
			}

			_, _, updateErr := v1helpers.UpdateStatus(ctx, c.operatorClient,
				v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
					Type:    upgradeableCondition,
					Status:  operatorv1.ConditionTrue,
					Reason:  "AsExpected",
					Message: "No topology transition in progress",
				}),
			)
			return updateErr
		}

		// Post transition checks
		return c.checkClusterReconciliation(ctx)
	}

	// A transition was requested
	return c.reconcileTransition(ctx, syncCtx, infra)
}

// reconcileTransition finds the matching transition descriptor, runs preflight
// validators, sets a progressing condition, and applies the status update.
func (c *TopologyTransitionController) reconcileTransition(ctx context.Context, syncCtx factory.SyncContext, infra *configv1.Infrastructure) error {
	transition, err := findTransition(infra, c.transitions)
	if err != nil {
		// Report via conditions, not sync error — these are user-fixable states
		// that should not trigger WithSyncDegradedOnError.
		if _, _, condErr := v1helpers.UpdateStatus(ctx, c.operatorClient,
			v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
				Type:    transitionCondition,
				Status:  operatorv1.ConditionFalse,
				Reason:  "UnsupportedTransition",
				Message: err.Error(),
			}),
			v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
				Type:    upgradeableCondition,
				Status:  operatorv1.ConditionFalse,
				Reason:  "UnsupportedTransition",
				Message: "Cluster upgrade is not allowed while a topology transition is requested; revert spec.controlPlaneTopology to resolve",
			}),
		); condErr != nil {
			return condErr
		}
		syncCtx.Recorder().Warningf("TopologyTransitionUnsupported", "%s", err.Error())
		return nil
	}

	if err := validatePreflight(transition); err != nil {
		if _, _, condErr := v1helpers.UpdateStatus(ctx, c.operatorClient,
			v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
				Type:    transitionCondition,
				Status:  operatorv1.ConditionFalse,
				Reason:  "PreflightCheckFailed",
				Message: err.Error(),
			}),
			v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
				Type:    upgradeableCondition,
				Status:  operatorv1.ConditionFalse,
				Reason:  "PreflightCheckFailed",
				Message: "Cluster upgrade is not allowed while a topology transition is pending; resolve preflight failures or revert spec.controlPlaneTopology",
			}),
		); condErr != nil {
			return condErr
		}
		syncCtx.Recorder().Warningf("TopologyTransitionPreflightFailed", "%s", err.Error())
		return nil
	}

	specTopology := infra.Spec.ControlPlaneTopology
	statusTopology := infra.Status.ControlPlaneTopology

	// Set operator conditions first. If conditions succeed but the infra
	// status update fails, the next sync still sees spec != status and
	// retries reconcileTransition (conditions update is idempotent).
	if _, _, err := v1helpers.UpdateStatus(ctx, c.operatorClient,
		v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
			Type:    upgradeableCondition,
			Status:  operatorv1.ConditionFalse,
			Reason:  reasonTopologyTransitionInProgress,
			Message: fmt.Sprintf("Cluster upgrade is not allowed during topology transition from %s to %s", statusTopology, specTopology),
		}),
		v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
			Type:    transitionCondition,
			Status:  operatorv1.ConditionTrue,
			Reason:  reasonTopologyTransitionInProgress,
			Message: fmt.Sprintf("Transitioning control plane topology from %s to %s", statusTopology, specTopology),
		}),
	); err != nil {
		return err
	}

	// Guard against the spec changing between the initial lister read and the
	// API write. Without this, a user reverting spec mid-sync would leave
	// status diverged from spec until the next sync detects the mismatch.
	var specChanged bool
	if err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		current, err := c.infraClient.Get(ctx, "cluster", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if !matchesSpec(transition.To, current.Spec) {
			specChanged = true
			return nil
		}
		transition.UpdateStatus(current)
		_, err = c.infraClient.UpdateStatus(ctx, current, metav1.UpdateOptions{})
		return err
	}); err != nil {
		return err
	}

	if specChanged {
		klog.Warningf("TopologyTransitionController: infrastructure spec changed during status update; will re-evaluate on next sync")
		return nil
	}

	syncCtx.Recorder().Eventf("TopologyTransitionController", "Control plane topology updated from %s to %s", statusTopology, specTopology)
	return nil
}

// checkClusterReconciliation runs all reconciliation checks to verify downstream
// workloads have reconciled after a topology transition. All checks must pass
// before the progressing condition is cleared.
func (c *TopologyTransitionController) checkClusterReconciliation(ctx context.Context) error {
	_, status, _, err := c.operatorClient.GetOperatorState()
	if err != nil {
		return err
	}

	// Use the Progressing condition timestamp as the soak anchor. When
	// Progressing is absent (safety-net path where the condition was lost),
	// fall back to the Upgradeable condition — both are set at transition
	// start, so either provides a valid lower bound.
	var soakAnchor time.Time
	progressingCond := v1helpers.FindOperatorCondition(status.Conditions, transitionCondition)
	if progressingCond != nil && !progressingCond.LastTransitionTime.IsZero() {
		soakAnchor = progressingCond.LastTransitionTime.Time
	} else {
		upgCond := v1helpers.FindOperatorCondition(status.Conditions, upgradeableCondition)
		if upgCond != nil && !upgCond.LastTransitionTime.IsZero() {
			soakAnchor = upgCond.LastTransitionTime.Time
		}
	}

	if !soakAnchor.IsZero() && c.clock.Since(soakAnchor) < minReconciliationSoakTime {
		klog.V(4).Infof("TopologyTransitionController: within reconciliation soak period (%s elapsed of %s minimum)", c.clock.Since(soakAnchor), minReconciliationSoakTime)
		return nil
	}

	for i, check := range c.reconciliationChecks {
		reconciled, err := check(ctx)
		if err != nil {
			return err
		}
		if !reconciled {
			klog.V(4).Infof("TopologyTransitionController: reconciliation check %d/%d not yet satisfied", i+1, len(c.reconciliationChecks))
			return nil
		}
	}

	_, _, updateErr := v1helpers.UpdateStatus(ctx, c.operatorClient,
		v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
			Type:    upgradeableCondition,
			Status:  operatorv1.ConditionTrue,
			Reason:  "TopologyTransitionComplete",
			Message: "Topology transition complete, upgrades are allowed",
		}),
		v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
			Type:    transitionCondition,
			Status:  operatorv1.ConditionFalse,
			Reason:  "TopologyTransitionComplete",
			Message: "Topology transition reconciliation complete",
		}),
	)
	return updateErr
}
