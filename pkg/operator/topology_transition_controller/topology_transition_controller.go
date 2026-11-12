package topology_transition_controller

import (
	"context"
	"fmt"
	"strings"
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

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	appsv1listers "k8s.io/client-go/listers/apps/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	klog "k8s.io/klog/v2"
	"k8s.io/utils/clock"
)

const (
	transitionProgressingCondition = "TopologyTransitionControllerProgressing"
	upgradeableCondition           = "TopologyTransitionControllerUpgradeable"

	reasonTopologyTransitionComplete = "TopologyTransitionComplete"

	reasonUnsupportedTransition = "UnsupportedTransition"
	reasonPreflightCheckFailed  = "PreflightCheckFailed"

	messageTransitionReconciliationComplete = "Topology transition reconciliation complete"

	// minReconciliationSoakTime is the minimum time to wait after a transition
	// starts before accepting reconciliation checks as passing. This prevents
	// premature completion when downstream operators haven't started progressing yet.
	minReconciliationSoakTime = 2 * time.Minute
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
	openShiftAPIServerDeploymentLister appsv1listers.DeploymentNamespaceLister,
	openShiftAPIServerDeploymentInformer cache.SharedIndexInformer,
	ingressControllerLister operatorv1listers.IngressControllerNamespaceLister,
	ingressControllerInformer cache.SharedIndexInformer,
	machineConfigLister machineconfigv1listers.MachineConfigLister,
	machineConfigInformer cache.SharedIndexInformer,
	machineConfigPoolLister machineconfigv1listers.MachineConfigPoolLister,
	machineConfigPoolInformer cache.SharedIndexInformer,
	imageRegistryDeploymentLister appsv1listers.DeploymentNamespaceLister,
	imageRegistryDeploymentInformer cache.SharedIndexInformer,
	consoleDeploymentLister appsv1listers.DeploymentNamespaceLister,
	consoleDeploymentInformer cache.SharedIndexInformer,
	monitoringStatefulSetLister appsv1listers.StatefulSetNamespaceLister,
	monitoringStatefulSetInformer cache.SharedIndexInformer,
	clk clock.PassiveClock,
	recorder events.Recorder,
) factory.Controller {
	listers := TransitionValidationListers{
		NodeLister:                         nodeLister,
		EtcdConfigMapLister:                etcdConfigMapLister,
		EtcdLister:                         etcdLister,
		KubeAPIServerLister:                kubeAPIServerLister,
		OpenShiftAPIServerDeploymentLister: openShiftAPIServerDeploymentLister,
		IngressControllerLister:            ingressControllerLister,
		MachineConfigLister:                machineConfigLister,
		MachineConfigPoolLister:            machineConfigPoolLister,
		OperatorClient:                     operatorClient,
		ClusterVersionLister:               clusterVersionLister,
		ImageRegistryDeploymentLister:      imageRegistryDeploymentLister,
		ConsoleDeploymentLister:            consoleDeploymentLister,
		MonitoringStatefulSetLister:        monitoringStatefulSetLister,
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
			openShiftAPIServerDeploymentInformer,
			ingressControllerInformer,
			machineConfigInformer,
			machineConfigPoolInformer,
			imageRegistryDeploymentInformer,
			consoleDeploymentInformer,
			monitoringStatefulSetInformer,
		).
		WithSync(c.sync).
		WithPostStartHooks(c.runEvaluation).
		WithSyncDegradedOnError(operatorClient).
		ResyncEvery(time.Minute).
		ToController("TopologyTransitionController", recorder)
}

// sync reconciles the desired and current topology across both the control
// plane and infrastructure axes. If a transition is requested on either axis,
// it validates, sets a progressing condition, and updates Infrastructure
// status. If a transition is in progress, it checks downstream workloads and
// clears the condition when complete.
func (c *TopologyTransitionController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	infra, err := c.infraLister.Get("cluster")
	if errors.IsNotFound(err) {
		syncCtx.Recorder().Warningf("TopologyTransitionController", "Required infrastructures.%s/cluster not found", configv1.GroupName)
		return nil
	}

	if err != nil {
		return err
	}

	// Three states:
	// 1. spec != status on any topology axis -> a transition was requested, run reconcileTransition
	// 2. spec == status with an in-flight transition (Progressing=True or
	//    TopologyTransitionCompleted=False) -> awaiting downstream reconciliation
	// 3. spec == status, no in-flight transition -> idle, ensure Upgradeable=True

	// Get the needed operator info to progress
	_, status, _, err := c.operatorClient.GetOperatorState()
	if err != nil {
		return err
	}

	transitionProgressing := v1helpers.IsOperatorConditionTrue(status.Conditions, transitionProgressingCondition)
	controllerUpgradeable := v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition)

	// TopologyTransitionCompleted=False on the Infrastructure CR is the
	// authoritative signal that a transition was committed but has not yet
	// converged. Spec/status equality must not be treated as completion:
	// after a commit (or a CCO crash that lost the Progressing condition),
	// only Completed=True closes out the transition.
	transitionIncomplete := meta.IsStatusConditionFalse(infra.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionCompletedConditionType)

	switch {
	case hasTopologyDivergence(infra):
		return c.reconcileTransition(ctx, syncCtx, infra)

	case transitionProgressing || transitionIncomplete:
		return c.checkClusterReconciliation(ctx, syncCtx, infra)

	case !controllerUpgradeable:
		// Safety net: if Upgradeable is stuck False from a completed transition
		// whose progressing condition was lost (e.g. partial failure), route
		// through reconciliation checks before re-enabling upgrades.
		if upgCond := v1helpers.FindOperatorCondition(status.Conditions, upgradeableCondition); upgCond != nil && c.isTransitionName(upgCond.Reason) {
			return c.checkClusterReconciliation(ctx, syncCtx, infra)
		}

		// Otherwise Upgradeable was blocked by a rejected transition request
		// (unsupported transition or failed preflight check). Since spec no
		// longer differs from status, the request was withdrawn; clear both
		// conditions back to idle so a stale rejection reason doesn't linger
		// on transitionProgressingCondition.
		return c.setTransitionOperatorConditions(ctx,
			operatorv1.ConditionFalse, operatorv1.ConditionTrue,
			"AsExpected",
			"No topology transition in progress",
			"No topology transition in progress",
		)
	}

	return nil
}

// hasTopologyDivergence returns true if any topology axis in the
// Infrastructure spec diverges from the corresponding status field.
func hasTopologyDivergence(infra *configv1.Infrastructure) bool {
	if isTopologyChangeRequested(infra.Spec.ControlPlaneTopology, infra.Status.ControlPlaneTopology) {
		return true
	}
	if isTopologyChangeRequested(infra.Spec.InfrastructureTopology, infra.Status.InfrastructureTopology) {
		return true
	}
	return false
}

// describeRequestedTransition returns a human-readable description of the
// requested topology changes between spec and status.
func describeRequestedTransition(infra *configv1.Infrastructure) string {
	var parts []string
	if isTopologyChangeRequested(infra.Spec.ControlPlaneTopology, infra.Status.ControlPlaneTopology) {
		parts = append(parts, fmt.Sprintf("controlPlane: %s->%s", infra.Status.ControlPlaneTopology, infra.Spec.ControlPlaneTopology))
	}
	if isTopologyChangeRequested(infra.Spec.InfrastructureTopology, infra.Status.InfrastructureTopology) {
		parts = append(parts, fmt.Sprintf("infrastructure: %s->%s", infra.Status.InfrastructureTopology, infra.Spec.InfrastructureTopology))
	}
	return strings.Join(parts, ", ")
}

// validateTransitionConvergence returns an error when applying the
// transition's status update would leave an explicitly requested spec
// topology diverging from status. Such a state matches no descriptor and
// would wedge reconciliation in UnsupportedTransition, so it is rejected
// before any mutation. Spec fields left empty are allowed: the transition
// determines their target, as with the omitted infrastructure target in
// SNOToHACompact.
func validateTransitionConvergence(infra *configv1.Infrastructure, transition *TransitionDescriptor) error {
	simulated := infra.DeepCopy()
	transition.UpdateStatus(simulated)

	var conflicts []string
	if isTopologyChangeRequested(infra.Spec.ControlPlaneTopology, simulated.Status.ControlPlaneTopology) {
		conflicts = append(conflicts, fmt.Sprintf("spec.controlPlaneTopology=%s conflicts with the transition target %s", infra.Spec.ControlPlaneTopology, simulated.Status.ControlPlaneTopology))
	}
	if isTopologyChangeRequested(infra.Spec.InfrastructureTopology, simulated.Status.InfrastructureTopology) {
		conflicts = append(conflicts, fmt.Sprintf("spec.infrastructureTopology=%s conflicts with the transition target %s", infra.Spec.InfrastructureTopology, simulated.Status.InfrastructureTopology))
	}
	if len(conflicts) == 0 {
		return nil
	}
	return fmt.Errorf("transition %s cannot be applied: %s; clear the conflicting spec field or set it to the transition target to resolve",
		transition.Name, strings.Join(conflicts, "; "))
}

// blockTransition sets Progressing=False and Upgradeable=False with the given
// reason and messages, records a warning event, and returns nil so callers can
// propagate the return directly. Errors from the condition write are bubbled up
// instead.
func (c *TopologyTransitionController) blockTransition(
	ctx context.Context,
	syncCtx factory.SyncContext,
	reason, progressingMessage, upgradeableMessage, eventName string,
) error {
	if condErr := c.setTransitionOperatorConditions(ctx,
		operatorv1.ConditionFalse, operatorv1.ConditionFalse,
		reason, progressingMessage, upgradeableMessage,
	); condErr != nil {
		return condErr
	}
	syncCtx.Recorder().Warningf(eventName, "%s", progressingMessage)
	return nil
}

// setTransitionOperatorConditions writes the controller's Progressing and
// Upgradeable operator conditions in one status update. The pair always
// shares a single reason so operator status stays internally consistent;
// each condition keeps its own message.
func (c *TopologyTransitionController) setTransitionOperatorConditions(
	ctx context.Context,
	progressing, upgradeable operatorv1.ConditionStatus,
	reason, progressingMessage, upgradeableMessage string,
) error {
	_, _, err := v1helpers.UpdateStatus(ctx, c.operatorClient,
		v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
			Type: transitionProgressingCondition, Status: progressing, Reason: reason, Message: progressingMessage,
		}),
		v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
			Type: upgradeableCondition, Status: upgradeable, Reason: reason, Message: upgradeableMessage,
		}),
	)
	return err
}

// reconcileTransition finds the matching transition descriptor, runs preflight
// validators, sets a progressing condition, and applies the status update.
func (c *TopologyTransitionController) reconcileTransition(ctx context.Context, syncCtx factory.SyncContext, infra *configv1.Infrastructure) error {
	transition, err := findTransition(infra, c.transitions)
	if err == nil {
		err = validateTransitionConvergence(infra, transition)
	}
	if err != nil {
		// Report via conditions, not sync error — these are user-fixable states
		// that should not trigger WithSyncDegradedOnError.
		return c.blockTransition(ctx, syncCtx,
			reasonUnsupportedTransition,
			err.Error(),
			fmt.Sprintf("Cluster upgrade is not allowed while a topology transition is requested; set spec.controlPlaneTopology=%s and spec.infrastructureTopology=%s to match status and resolve",
				infra.Status.ControlPlaneTopology, infra.Status.InfrastructureTopology),
			"TopologyTransitionUnsupported",
		)
	}

	if err := validatePreflight(c.preflightChecks, transition); err != nil {
		return c.blockTransition(ctx, syncCtx,
			reasonPreflightCheckFailed,
			err.Error(),
			fmt.Sprintf("Cluster upgrade is not allowed while a topology transition is pending; resolve preflight failures or set spec.controlPlaneTopology=%s and spec.infrastructureTopology=%s to match status and resolve",
				infra.Status.ControlPlaneTopology, infra.Status.InfrastructureTopology),
			"TopologyTransitionPreflightFailed",
		)
	}

	divergence := describeRequestedTransition(infra)

	// Set operator conditions first. If conditions succeed but the infra
	// status update fails, the next sync still sees spec != status and
	// retries reconcileTransition (conditions update is idempotent).
	if err := c.setTransitionOperatorConditions(ctx,
		operatorv1.ConditionTrue, operatorv1.ConditionFalse,
		transition.Name,
		fmt.Sprintf("Topology transition in progress: %s", divergence),
		fmt.Sprintf("Cluster upgrade is not allowed during topology transition: %s", divergence),
	); err != nil {
		return err
	}

	applied, err := c.applyTopologyStatus(ctx, infra, transition)
	if err != nil || !applied {
		return err
	}

	syncCtx.Recorder().Eventf("TopologyTransitionController", "Topology transition applied: %s", divergence)
	return nil
}

// checkClusterReconciliation runs the post-transition TransitionValidators for
// the transition matching the current Infrastructure spec, to verify downstream
// workloads have reconciled after a topology transition. All validators must
// pass before the progressing condition is cleared.
//
// Only a committed transition (TopologyTransitionCompleted=False on the
// Infrastructure CR) is reconciled here. Progressing conditions without a
// commit mean the request was withdrawn before the status write landed:
// running that transition's validators would wedge Upgradeable=False forever
// on checks that can never pass, so the stale conditions are unwound instead.
func (c *TopologyTransitionController) checkClusterReconciliation(ctx context.Context, syncCtx factory.SyncContext, infra *configv1.Infrastructure) error {
	_, status, _, err := c.operatorClient.GetOperatorState()
	if err != nil {
		return err
	}

	completion := meta.FindStatusCondition(infra.Status.TopologyTransitionStatus.Conditions, configv1.TopologyTransitionCompletedConditionType)
	switch {
	case completion == nil || completion.Status == metav1.ConditionUnknown:
		// The transition was never committed: the request was withdrawn
		// between setting the gating conditions and the status commit. Clear
		// both conditions back to idle so a stale transition reason does not
		// linger and block upgrades.
		if _, _, updateErr := v1helpers.UpdateStatus(ctx, c.operatorClient,
			v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
				Type:    upgradeableCondition,
				Status:  operatorv1.ConditionTrue,
				Reason:  "AsExpected",
				Message: "No topology transition in progress",
			}),
			v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
				Type:    transitionProgressingCondition,
				Status:  operatorv1.ConditionFalse,
				Reason:  "AsExpected",
				Message: "No topology transition in progress",
			}),
		); updateErr != nil {
			return updateErr
		}
		syncCtx.Recorder().Eventf("TopologyTransitionController", "Topology transition request withdrawn before commit; upgrade gating cleared")
		return nil

	case completion.Status == metav1.ConditionTrue:
		// Completion was already recorded on the Infrastructure CR but the
		// operator condition clear failed. Redo it idempotently without
		// re-running the soak or validators.
		return c.clearTransitionConditions(ctx)
	}

	// Completed=False: the transition is committed and in flight. Anchor the
	// soak on the status commit timestamp: the Completed=False condition is
	// written atomically with the topology change, so its LastTransitionTime
	// is the moment downstream operators began reacting to the new topology.
	// The operator conditions are set before the commit and can predate it by
	// failed commit attempts, so they are only fallbacks (for markers without
	// a timestamp) — anchoring on them would under-soak relative to the
	// commit.
	var soakAnchor time.Time
	if !completion.LastTransitionTime.IsZero() {
		soakAnchor = completion.LastTransitionTime.Time
	} else if progressingCond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition); progressingCond != nil && !progressingCond.LastTransitionTime.IsZero() {
		soakAnchor = progressingCond.LastTransitionTime.Time
	} else if upgCond := v1helpers.FindOperatorCondition(status.Conditions, upgradeableCondition); upgCond != nil && !upgCond.LastTransitionTime.IsZero() {
		soakAnchor = upgCond.LastTransitionTime.Time
	}

	if !soakAnchor.IsZero() && c.clock.Since(soakAnchor) < minReconciliationSoakTime {
		klog.V(4).Infof("TopologyTransitionController: within reconciliation soak period (%s elapsed of %s minimum)", c.clock.Since(soakAnchor), minReconciliationSoakTime)
		return nil
	}

	// Identify the committed transition. The Completed=False condition's
	// Reason carries the transition name and was written atomically with the
	// commit, so it is authoritative. The operator conditions are fallbacks
	// for markers written without a name. If the committed transition cannot
	// be identified at all, do not complete: marking a transition done
	// without running its validators would re-enable upgrades on a cluster
	// whose workloads may not have reconciled.
	var activeTransition *TransitionDescriptor
	if name := completion.Reason; name != "" {
		activeTransition = findTransitionByName(name, c.transitions)
	}
	if activeTransition == nil {
		if progressingCond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition); progressingCond != nil {
			activeTransition = findTransitionByName(progressingCond.Reason, c.transitions)
		}
	}
	if activeTransition == nil {
		if upgCond := v1helpers.FindOperatorCondition(status.Conditions, upgradeableCondition); upgCond != nil {
			activeTransition = findTransitionByName(upgCond.Reason, c.transitions)
		}
	}
	if activeTransition == nil {
		klog.V(2).Infof("TopologyTransitionController: committed transition %q cannot be identified; withholding completion", completion.Reason)
		_, _, updateErr := v1helpers.UpdateStatus(ctx, c.operatorClient,
			v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
				Type:    upgradeableCondition,
				Status:  operatorv1.ConditionFalse,
				Reason:  "UnknownCommittedTransition",
				Message: fmt.Sprintf("Committed topology transition %q is not a registered transition; post-transition reconciliation cannot be verified", completion.Reason),
			}),
		)
		return updateErr
	}

	transitionValidators := activeTransition.TransitionValidators

	for i, check := range transitionValidators {
		if err := check.Validate(); err != nil {
			klog.V(4).Infof("TopologyTransitionController: reconciliation check %d/%d %s not yet satisfied: %v", i+1, len(transitionValidators), check.Name, err)
			return nil
		}
	}

	// Mark the transition complete on the Infrastructure CR first. If this
	// write succeeds but the operator condition update below fails, the next
	// sync still sees Completed=False (or the Progressing condition) and
	// retries idempotently.
	if err := c.completeTopologyTransition(ctx); err != nil {
		return err
	}

	return c.clearTransitionConditions(ctx)
}

// clearTransitionConditions closes out the operator-side transition gating:
// Upgradeable is re-enabled and Progressing is cleared.
func (c *TopologyTransitionController) clearTransitionConditions(ctx context.Context) error {
	return c.setTransitionOperatorConditions(ctx,
		operatorv1.ConditionFalse, operatorv1.ConditionTrue,
		reasonTopologyTransitionComplete,
		messageTransitionReconciliationComplete,
		"Topology transition complete, upgrades are allowed",
	)
}

func (c *TopologyTransitionController) isTransitionName(reason string) bool {
	return findTransitionByName(reason, c.transitions) != nil
}
