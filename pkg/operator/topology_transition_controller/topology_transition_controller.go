package topology_transition_controller

import (
	"context"
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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	klog "k8s.io/klog/v2"
	"k8s.io/utils/clock"
)

const (
	transitionProgressingCondition = "TopologyTransitionControllerProgressing"
	upgradeableCondition           = "TopologyTransitionControllerUpgradeable"

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
	operatorClient  v1helpers.OperatorClient
	infraLister     configlistersv1.InfrastructureLister
	infraClient     configv1client.InfrastructureInterface
	preflightChecks []TransitionValidatorFunc
	transitions     []TransitionDescriptor
	clock           clock.PassiveClock
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
	}
	c := &TopologyTransitionController{
		operatorClient: operatorClient,
		infraLister:    infraLister,
		infraClient:    infraClient.Infrastructures(),
		preflightChecks: []TransitionValidatorFunc{
			validateClusterOperatorsStable(clusterOperatorLister),
		},
		transitions: buildSupportedTransitions(listers),
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
			kubeAPIServerInformer,
			openShiftAPIServerInformer,
			ingressControllerInformer,
			machineConfigInformer,
			machineConfigPoolInformer,
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

	// Get the needed operator info to progress
	_, status, _, err := c.operatorClient.GetOperatorState()
	if err != nil {
		return err
	}
	transitionProgressing := v1helpers.IsOperatorConditionTrue(status.Conditions, transitionProgressingCondition)
	controllerUpgradeable := v1helpers.IsOperatorConditionTrue(status.Conditions, upgradeableCondition)

	switch {
	case specTopology != "" && specTopology != statusTopology:
		return c.reconcileTransition(ctx, syncCtx, infra)

	case transitionProgressing:
		return c.checkClusterReconciliation(ctx, infra)

	case !controllerUpgradeable:
		// Safety net: if Upgradeable is stuck False from a completed transition
		// whose progressing condition was lost (e.g. partial failure), route
		// through reconciliation checks before re-enabling upgrades.
		if upgCond := v1helpers.FindOperatorCondition(status.Conditions, upgradeableCondition); upgCond != nil && upgCond.Reason == reasonTopologyTransitionInProgress {
			return c.checkClusterReconciliation(ctx, infra)
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
// validators, sets a progressing condition, and applies the status update.
func (c *TopologyTransitionController) reconcileTransition(ctx context.Context, syncCtx factory.SyncContext, infra *configv1.Infrastructure) error {
	transition, err := findTransition(infra, c.transitions)
	if err != nil {
		// Report via conditions, not sync error — these are user-fixable states
		// that should not trigger WithSyncDegradedOnError.
		if _, _, condErr := v1helpers.UpdateStatus(ctx, c.operatorClient,
			v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
				Type:    transitionProgressingCondition,
				Status:  operatorv1.ConditionFalse,
				Reason:  "UnsupportedTransition",
				Message: err.Error(),
			}),
			v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
				Type:    upgradeableCondition,
				Status:  operatorv1.ConditionFalse,
				Reason:  "UnsupportedTransition",
				Message: fmt.Sprintf("Cluster upgrade is not allowed while a topology transition is requested; revert spec.controlPlaneTopology to %s to resolve", infra.Status.ControlPlaneTopology),
			}),
		); condErr != nil {
			return condErr
		}
		syncCtx.Recorder().Warningf("TopologyTransitionUnsupported", "%s", err.Error())
		return nil
	}

	if err := validatePreflight(c.preflightChecks, transition); err != nil {
		if _, _, condErr := v1helpers.UpdateStatus(ctx, c.operatorClient,
			v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
				Type:    transitionProgressingCondition,
				Status:  operatorv1.ConditionFalse,
				Reason:  "PreflightCheckFailed",
				Message: err.Error(),
			}),
			v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
				Type:    upgradeableCondition,
				Status:  operatorv1.ConditionFalse,
				Reason:  "PreflightCheckFailed",
				Message: fmt.Sprintf("Cluster upgrade is not allowed while a topology transition is pending; resolve preflight failures or revert spec.controlPlaneTopology to %s to resolve", infra.Status.ControlPlaneTopology),
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
			Type:    transitionProgressingCondition,
			Status:  operatorv1.ConditionTrue,
			Reason:  reasonTopologyTransitionInProgress,
			Message: fmt.Sprintf("Transitioning control plane topology from %s to %s", statusTopology, specTopology),
		}),
	); err != nil {
		return err
	}

	current, err := c.infraClient.Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return err
	}

	// Guard against the spec changing between the initial lister read and the
	// API write. Without this, a user reverting spec mid-sync would leave
	// status diverged from spec until the next sync detects the mismatch.
	if !matchesSpec(transition.To, current.Spec) {
		klog.Warningf("TopologyTransitionController: infrastructure spec changed during status update; will re-evaluate on next sync")
		return nil
	}

	// Update the infra status
	transition.UpdateStatus(current)
	if _, err = c.infraClient.UpdateStatus(ctx, current, metav1.UpdateOptions{}); err != nil {
		return err
	}

	syncCtx.Recorder().Eventf("TopologyTransitionController", "Control plane topology updated from %s to %s", statusTopology, specTopology)
	return nil
}

// checkClusterReconciliation runs the post-transition TransitionValidators for
// the transition matching the current Infrastructure spec, to verify downstream
// workloads have reconciled after a topology transition. All validators must
// pass before the progressing condition is cleared.
func (c *TopologyTransitionController) checkClusterReconciliation(ctx context.Context, infra *configv1.Infrastructure) error {
	_, status, _, err := c.operatorClient.GetOperatorState()
	if err != nil {
		return err
	}

	// Use the Progressing condition timestamp as the soak anchor. When
	// Progressing is absent (safety-net path where the condition was lost),
	// fall back to the Upgradeable condition — both are set at transition
	// start, so either provides a valid lower bound.
	var soakAnchor time.Time
	progressingCond := v1helpers.FindOperatorCondition(status.Conditions, transitionProgressingCondition)
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

	_, _, updateErr := v1helpers.UpdateStatus(ctx, c.operatorClient,
		v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
			Type:    upgradeableCondition,
			Status:  operatorv1.ConditionTrue,
			Reason:  "TopologyTransitionComplete",
			Message: "Topology transition complete, upgrades are allowed",
		}),
		v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
			Type:    transitionProgressingCondition,
			Status:  operatorv1.ConditionFalse,
			Reason:  "TopologyTransitionComplete",
			Message: "Topology transition reconciliation complete",
		}),
	)
	return updateErr
}
