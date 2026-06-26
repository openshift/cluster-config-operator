package topology_transition_controller

import (
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
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
	From         configv1.InfrastructureStatus
	To           configv1.InfrastructureSpec
	Validators   []TransitionValidatorFunc
	UpdateStatus func(infra *configv1.Infrastructure)
}

// buildSupportedTransitions returns the set of permitted topology transitions
// with validators wired to the necessary tools for accessing cluster information.
func buildSupportedTransitions(nodeLister corev1listers.NodeLister, etcdConfigMapLister corev1listers.ConfigMapNamespaceLister, etcdLister operatorv1listers.EtcdLister) []TransitionDescriptor {
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
			Validators: []TransitionValidatorFunc{
				validateControlPlaneNodeCount(3, nodeLister),
				validateExactInfrastructureNodeCount(0, nodeLister),
				validateControlPlaneNodesSchedulable(3, nodeLister),
				validateControlPlaneNodesReady(3, nodeLister),
				validateEtcdQuorum(etcdLister),
				validateEtcdNotProgressing(etcdLister),
				validateEtcdVotingMembers(3, etcdConfigMapLister),
			},
			UpdateStatus: func(infra *configv1.Infrastructure) {
				infra.Status.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
				infra.Status.InfrastructureTopology = configv1.HighlyAvailableTopologyMode
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
