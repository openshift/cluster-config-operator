package topology_transition_controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	machineconfigv1listers "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	operatorv1listers "github.com/openshift/client-go/operator/listers/operator/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	klog "k8s.io/klog/v2"
	"k8s.io/utils/clock"
)

const (
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
	operatorClient      v1helpers.OperatorClient
	infraLister         configlistersv1.InfrastructureLister
	infraClient         configv1client.InfrastructureInterface
	preflightChecks     []PreflightCheck
	transitions         []TransitionDescriptor
	clock               clock.PassiveClock
	evaluationInformers []cache.SharedIndexInformer
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
	clusterVersionLister configlistersv1.ClusterVersionLister,
	clusterVersionInformer cache.SharedIndexInformer,
	kubeAPIServerLister operatorv1listers.KubeAPIServerLister,
	kubeAPIServerInformer cache.SharedIndexInformer,
	openShiftAPIServerLister operatorv1listers.OpenShiftAPIServerLister,
	openShiftAPIServerInformer cache.SharedIndexInformer,
	ingressControllerLister operatorv1listers.IngressControllerNamespaceLister,
	ingressControllerInformer cache.SharedIndexInformer,
	machineConfigLister machineconfigv1listers.MachineConfigLister,
	machineConfigInformer cache.SharedIndexInformer,
	machineConfigPoolLister machineconfigv1listers.MachineConfigPoolLister,
	machineConfigPoolInformer cache.SharedIndexInformer,
	clk clock.PassiveClock,
	recorder events.Recorder,
) factory.Controller {
	listers := TransitionValidationListers{
		NodeLister:               nodeLister,
		EtcdConfigMapLister:      etcdConfigMapLister,
		EtcdLister:               etcdLister,
		KubeAPIServerLister:      kubeAPIServerLister,
		OpenShiftAPIServerLister: openShiftAPIServerLister,
		IngressControllerLister:  ingressControllerLister,
		MachineConfigLister:      machineConfigLister,
		MachineConfigPoolLister:  machineConfigPoolLister,
		InfraClient:              infraClient.Infrastructures(),
	}

	c := &TopologyTransitionController{
		operatorClient:  operatorClient,
		infraLister:     infraLister,
		infraClient:     infraClient.Infrastructures(),
		preflightChecks: buildGlobalPreflightChecks(clusterOperatorLister, clusterVersionLister),
		transitions:     buildSupportedTransitions(listers),
		clock:           clk,
		evaluationInformers: []cache.SharedIndexInformer{
			infraInformer, nodeInformer, etcdConfigMapInformer, etcdOperatorInformer,
			clusterOperatorInformer, clusterVersionInformer,
		},
	}

	return factory.New().
		WithInformers(
			operatorClient.Informer(),
			infraInformer,
			nodeInformer,
			etcdConfigMapInformer,
			etcdOperatorInformer,
			clusterOperatorInformer,
			clusterVersionInformer,
			kubeAPIServerInformer,
			openShiftAPIServerInformer,
			ingressControllerInformer,
			machineConfigInformer,
			machineConfigPoolInformer,
		).
		WithSync(c.sync).
		WithPostStartHooks(c.runEvaluation).
		WithSyncDegradedOnError(operatorClient).
		ResyncEvery(time.Minute).
		ToController("TopologyTransitionController", recorder)
}

// sync reconciles the desired and current control plane topology.
// If a transition is requested, it validates, sets the API completion condition,
// and updates Infrastructure status. If a transition is in progress,
// it checks downstream workloads and clears the condition when complete.
func (c *TopologyTransitionController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	infra, readErr := c.infraClient.Get(ctx, "cluster", metav1.GetOptions{})
	if apierrors.IsNotFound(readErr) {
		syncCtx.Recorder().Warningf("TopologyTransitionController", "Required infrastructures.%s/cluster not found", configv1.GroupName)
		return nil
	}

	if readErr != nil {
		// A failed live read must not stop active-transition checks. The cache
		// is only a fallback for recovery, never permission for a new write.
		var err error
		infra, err = c.infraLister.Get("cluster")
		if err != nil {
			return errors.Join(readErr, err)
		}
	}

	return errors.Join(readErr, c.reconcileInfrastructure(ctx, syncCtx, infra, readErr == nil))
}

func (c *TopologyTransitionController) reconcileInfrastructure(ctx context.Context, syncCtx factory.SyncContext, infra *configv1.Infrastructure, allowTopologyWrite bool) error {
	specTopology := infra.Spec.ControlPlaneTopology
	statusTopology := infra.Status.ControlPlaneTopology

	// Three states:
	// 1. spec != status → a transition was requested, run reconcileTransition
	// 2. spec == status, Completed=False/InProgress → awaiting downstream reconciliation
	// 3. spec == status, otherwise → idle, ensure Upgradeable=True

	// Get the needed operator info to progress
	_, status, _, err := c.operatorClient.GetOperatorState()
	if err != nil {
		return err
	}

	var completion *metav1.Condition
	if infra.Status.TopologyTransitionStatus != nil {
		completion = meta.FindStatusCondition(infra.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionCompletedConditionType)
	}

	transitionInProgress := completion != nil && completion.Status == metav1.ConditionFalse && completion.Reason == reasonTopologyTransitionInProgress
	controllerUpgradeable := v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition)

	switch {
	case specTopology != "" && specTopology != statusTopology:
		return c.reconcileTransition(ctx, syncCtx, infra, allowTopologyWrite)

	case transitionInProgress:
		return c.checkClusterReconciliation(ctx, infra)

	case !controllerUpgradeable:
		// Safety net: if Upgradeable is stuck False from a completed transition
		// whose completion condition was lost (e.g. partial failure), route
		// through reconciliation checks before re-enabling upgrades.
		if upgCond := v1helpers.FindOperatorCondition(status.Conditions, upgradeableCondition); upgCond != nil && upgCond.Reason == reasonTopologyTransitionInProgress {
			return c.checkClusterReconciliation(ctx, infra)
		}

		// Otherwise Upgradeable was blocked by a rejected transition request
		// (unsupported transition or failed preflight check). Since spec no
		// longer differs from status, the cluster is AsExpected; restore
		// Upgradeable and clear the stale API rejection.
		if completion != nil && completion.Status == metav1.ConditionFalse {
			updated, err := c.applyTransitionStatus(ctx, infra, nil, metav1.Condition{
				Type: configv1.TopologyTransitionCompletedConditionType, Status: metav1.ConditionUnknown,
				Reason: "AsExpected", Message: "No topology transition in progress",
			})
			if err != nil || !updated {
				return err
			}
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

	return nil
}

// reconcileTransition finds the matching transition descriptor, runs preflight
// validators, starts the API completion condition, and applies the status update.
func (c *TopologyTransitionController) reconcileTransition(ctx context.Context, syncCtx factory.SyncContext, infra *configv1.Infrastructure, allowTopologyWrite bool) error {
	transition, err := findTransition(infra, c.transitions)
	if err != nil {
		// Report via conditions, not sync error — these are user-fixable states
		// that should not trigger WithSyncDegradedOnError.
		if _, _, condErr := v1helpers.UpdateStatus(ctx, c.operatorClient,
			v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
				Type:    upgradeableCondition,
				Status:  operatorv1.ConditionFalse,
				Reason:  "UnsupportedTransition",
				Message: fmt.Sprintf("Cluster upgrade is not allowed while a topology transition is requested; revert spec.controlPlaneTopology to %s to resolve", infra.Status.ControlPlaneTopology),
			}),
		); condErr != nil {
			return condErr
		}

		if _, condErr := c.applyTransitionStatus(ctx, infra, nil, metav1.Condition{
			Type: configv1.TopologyTransitionCompletedConditionType, Status: metav1.ConditionFalse,
			Reason: "UnsupportedTransition", Message: err.Error(),
		}); condErr != nil {
			return condErr
		}

		syncCtx.Recorder().Warningf("TopologyTransitionUnsupported", "%s", err.Error())
		return nil
	}

	if err := validatePreflight(c.preflightChecks, transition); err != nil {
		var validationReadErr error
		if _, ok := errors.AsType[*clusterStateReadError](err); ok {
			validationReadErr = err
		}

		_, _, condErr := v1helpers.UpdateStatus(ctx, c.operatorClient,
			v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
				Type:    upgradeableCondition,
				Status:  operatorv1.ConditionFalse,
				Reason:  "PreflightCheckFailed",
				Message: fmt.Sprintf("Cluster upgrade is not allowed while a topology transition is pending; resolve preflight failures or revert spec.controlPlaneTopology to %s to resolve", infra.Status.ControlPlaneTopology),
			}),
		)
		if condErr != nil {
			return errors.Join(validationReadErr, condErr)
		}

		_, condErr = c.applyTransitionStatus(ctx, infra, nil, metav1.Condition{
			Type: configv1.TopologyTransitionCompletedConditionType, Status: metav1.ConditionFalse,
			Reason: "PreflightCheckFailed", Message: err.Error(),
		})
		if condErr != nil {
			return errors.Join(validationReadErr, condErr)
		}

		syncCtx.Recorder().Warningf("TopologyTransitionPreflightFailed", "%s", err.Error())
		return validationReadErr
	}

	// Always run request checks, including crash recovery. A failed live read
	// prevents a new topology write even if cached request checks pass.
	if !allowTopologyWrite {
		return nil
	}

	specTopology := infra.Spec.ControlPlaneTopology
	statusTopology := infra.Status.ControlPlaneTopology

	// Block upgrades first. If that succeeds but the infra
	// status update fails, the next sync still sees spec != status and
	// retries reconcileTransition (conditions update is idempotent).
	if _, _, err := v1helpers.UpdateStatus(ctx, c.operatorClient,
		v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
			Type:    upgradeableCondition,
			Status:  operatorv1.ConditionFalse,
			Reason:  reasonTopologyTransitionInProgress,
			Message: fmt.Sprintf("Cluster upgrade is not allowed during topology transition from %s to %s", statusTopology, specTopology),
		}),
	); err != nil {
		return err
	}

	applied, err := c.applyTransitionStatus(ctx, infra, transition, metav1.Condition{
		Type: configv1.TopologyTransitionCompletedConditionType, Status: metav1.ConditionFalse,
		Reason:  reasonTopologyTransitionInProgress,
		Message: "Topology transition is waiting for cluster reconciliation",
	})
	if err != nil {
		return err
	}

	if applied {
		syncCtx.Recorder().Eventf("TopologyTransitionController", "Control plane topology updated from %s to %s", statusTopology, specTopology)
	} else {
		klog.Warningf("TopologyTransitionController: infrastructure spec or source changed during status update; will re-evaluate on next sync")
	}

	return nil
}

// checkClusterReconciliation runs the post-transition TransitionValidators for
// the transition matching the current Infrastructure spec, to verify downstream
// workloads have reconciled after a topology transition. All validators must
// pass before the completion condition becomes True.
func (c *TopologyTransitionController) checkClusterReconciliation(ctx context.Context, infra *configv1.Infrastructure) error {
	_, status, _, err := c.operatorClient.GetOperatorState()
	if err != nil {
		return err
	}

	// Use the API completion condition timestamp as the soak anchor. When
	// completion is absent (safety-net path where the condition was lost),
	// fall back to the Upgradeable condition — both are set at transition
	// start, so either provides a valid lower bound.
	var soakAnchor time.Time
	var completion *metav1.Condition
	if infra.Status.TopologyTransitionStatus != nil {
		completion = meta.FindStatusCondition(infra.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionCompletedConditionType)
	}

	if completion != nil && completion.Reason == reasonTopologyTransitionInProgress && !completion.LastTransitionTime.IsZero() {
		soakAnchor = completion.LastTransitionTime.Time
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

	var transitionValidators []TransitionValidatorFunc
	for i := range c.transitions {
		if matchesSpec(c.transitions[i].To, infra.Spec) {
			transitionValidators = c.transitions[i].TransitionValidators
			break
		}
	}

	for i, v := range transitionValidators {
		if err := v(); err != nil {
			klog.V(4).Infof("TopologyTransitionController: reconciliation check %d/%d not yet satisfied: %v", i+1, len(transitionValidators), err)
			return nil
		}
	}

	updated, updateErr := c.applyTransitionStatus(ctx, infra, nil, metav1.Condition{
		Type: configv1.TopologyTransitionCompletedConditionType, Status: metav1.ConditionTrue,
		Reason: "TopologyTransitionComplete", Message: "Topology transition reconciliation complete",
	})
	if updateErr != nil || !updated {
		return updateErr
	}

	_, _, updateErr = v1helpers.UpdateStatus(ctx, c.operatorClient,
		v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
			Type:    upgradeableCondition,
			Status:  operatorv1.ConditionTrue,
			Reason:  "TopologyTransitionComplete",
			Message: "Topology transition complete, upgrades are allowed",
		}),
	)

	return updateErr
}
