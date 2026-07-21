package topology_transition_controller

import (
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	machineconfigv1listers "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
	operatorv1listers "github.com/openshift/client-go/operator/listers/operator/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"
)

// TransitionValidatorFunc defines validation functions for transitions
type TransitionValidatorFunc func() error

// TransitionDescriptor describes a topology transition with its source/target
// state, per-transition validation functions, and a status updater. From
// matches against the current Infrastructure status, To matches against the
// desired Infrastructure spec. Zero-value fields act as wildcards in both
// matchers.
type TransitionDescriptor struct {
	From                 configv1.InfrastructureStatus
	To                   configv1.InfrastructureSpec
	PreflightValidators  []TransitionValidatorFunc
	UpdateStatus         func(infra *configv1.Infrastructure)
	TransitionValidators []TransitionValidatorFunc
}

type TransitionValidationListers struct {
	NodeLister               corev1listers.NodeLister
	EtcdConfigMapLister      corev1listers.ConfigMapNamespaceLister
	EtcdLister               operatorv1listers.EtcdLister
	KubeAPIServerLister      operatorv1listers.KubeAPIServerLister
	OpenShiftAPIServerLister operatorv1listers.OpenShiftAPIServerLister
	IngressControllerLister  operatorv1listers.IngressControllerNamespaceLister
	MachineConfigLister      machineconfigv1listers.MachineConfigLister
	MachineConfigPoolLister  machineconfigv1listers.MachineConfigPoolLister
}

// buildSupportedTransitions returns the set of permitted topology transitions
// with validators wired to the necessary tools for accessing cluster information.
func buildSupportedTransitions(listers TransitionValidationListers) []TransitionDescriptor {
	return []TransitionDescriptor{
		// SNO to HA Compact on platformType: None
		{
			From: configv1.InfrastructureStatus{
				ControlPlaneTopology:   configv1.SingleReplicaTopologyMode,
				InfrastructureTopology: configv1.SingleReplicaTopologyMode,
				PlatformStatus:         &configv1.PlatformStatus{Type: configv1.NonePlatformType},
			},
			To: configv1.InfrastructureSpec{
				ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
			},
			PreflightValidators: []TransitionValidatorFunc{
				validateControlPlaneNodeCount(3, listers.NodeLister),
				validateExactInfrastructureNodeCount(0, listers.NodeLister),
				validateControlPlaneNodesSchedulable(3, listers.NodeLister),
				validateControlPlaneNodesReady(3, listers.NodeLister),
				validateEtcdQuorum(listers.EtcdLister),
				validateEtcdNotProgressing(listers.EtcdLister),
				validateEtcdVotingMembers(3, listers.EtcdConfigMapLister),
			},
			UpdateStatus: func(infra *configv1.Infrastructure) {
				infra.Status.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
				infra.Status.InfrastructureTopology = configv1.HighlyAvailableTopologyMode
			},
			TransitionValidators: []TransitionValidatorFunc{
				validateControlPlaneNodesSchedulable(3, listers.NodeLister),
				validateControlPlaneNodesReady(3, listers.NodeLister),
				validateWorkerNodesReady(2, listers.NodeLister),
				validateEtcdQuorum(listers.EtcdLister),
				validateEtcdNotProgressing(listers.EtcdLister),
				validateEtcdVotingMembers(3, listers.EtcdConfigMapLister),
				validateMachineConfigNotPresent("50-master-dnsmasq-configuration", listers.MachineConfigLister),
				validateNewRenderedMasterConfig(listers.MachineConfigLister),
				validateNewRenderedWorkerConfig(listers.MachineConfigLister),
				validateMachineConfigPoolReadyCount(3, listers.MachineConfigPoolLister),
				validateIngressRouterCount(2, listers.IngressControllerLister),
				validateKubeAPIServerNodeCount(3, listers.KubeAPIServerLister),
				validateOpenShiftAPIServerReadyReplicas(3, listers.OpenShiftAPIServerLister),
			},
		},
	}
}

// matchesStatus returns true if every non-zero field in descriptor equals
// the corresponding field in actual. Zero-value fields are skipped.
func matchesStatus(descriptor, actual configv1.InfrastructureStatus) bool {
	if descriptor.ControlPlaneTopology != "" && descriptor.ControlPlaneTopology != actual.ControlPlaneTopology {
		return false
	}
	if descriptor.InfrastructureTopology != "" && descriptor.InfrastructureTopology != actual.InfrastructureTopology {
		return false
	}
	if descriptor.PlatformStatus != nil && descriptor.PlatformStatus.Type != "" {
		if actual.PlatformStatus == nil || descriptor.PlatformStatus.Type != actual.PlatformStatus.Type {
			return false
		}
	}
	return true
}

// matchesSpec returns true if every non-zero field in descriptor equals
// the corresponding field in actual. Zero-value fields are skipped.
func matchesSpec(descriptor, actual configv1.InfrastructureSpec) bool {
	if descriptor.ControlPlaneTopology != "" && descriptor.ControlPlaneTopology != actual.ControlPlaneTopology {
		return false
	}
	return true
}

// findTransition returns the TransitionDescriptor matching the current
// Infrastructure state, or an error if no supported transition matches.
func findTransition(infra *configv1.Infrastructure, transitions []TransitionDescriptor) (*TransitionDescriptor, error) {
	for i := range transitions {
		transition := &transitions[i]
		if matchesStatus(transition.From, infra.Status) && matchesSpec(transition.To, infra.Spec) {
			return transition, nil
		}
	}
	platformType := configv1.PlatformType("Unknown")
	if infra.Status.PlatformStatus != nil {
		platformType = infra.Status.PlatformStatus.Type
	}
	return nil, fmt.Errorf("transition from {controlPlane=%s, infrastructure=%s, platform=%s} to {controlPlane=%s} is not supported",
		infra.Status.ControlPlaneTopology, infra.Status.InfrastructureTopology,
		platformType, infra.Spec.ControlPlaneTopology)
}
