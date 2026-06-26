package topology_transition_controller

import (
	"errors"
	"fmt"

	operatorv1listers "github.com/openshift/client-go/operator/listers/operator/v1"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	corev1listers "k8s.io/client-go/listers/core/v1"
)

const (
	etcdEndpointsConfigMapName      = "etcd-endpoints"
	etcdNamespace                   = "openshift-etcd"
	etcdMembersAvailableCondition   = "EtcdMembersAvailable"
	etcdMembersProgressingCondition = "EtcdMembersProgressing"
)

// validatePreflight runs all transition-specific validators for the given
// descriptor. Returns a combined error containing all validation failures.
func validatePreflight(transition *TransitionDescriptor) error {
	var errs []error
	for _, v := range transition.Validators {
		if err := v(); err != nil {
			errs = append(errs, fmt.Errorf("transition validation failed: %w", err))
		}
	}
	return errors.Join(errs...)
}

// validateControlPlaneNodeCount returns a TransitionValidator that checks
// the number of control plane nodes meets the requirement.
func validateControlPlaneNodeCount(required int, nodeLister corev1listers.NodeLister) TransitionValidatorFunc {
	return func() error {
		selector := labels.SelectorFromSet(labels.Set{
			"node-role.kubernetes.io/control-plane": "",
		})
		nodes, err := nodeLister.List(selector)
		if err != nil {
			return fmt.Errorf("failed to list control plane nodes: %w", err)
		}
		if len(nodes) < required {
			return fmt.Errorf("insufficient control plane nodes: need %d, have %d", required, len(nodes))
		}
		return nil
	}
}

// validateExactInfrastructureNodeCount returns a TransitionValidator that checks
// the number of dedicated infrastructure (worker) nodes equals the expected count exactly.
func validateExactInfrastructureNodeCount(expected int, nodeLister corev1listers.NodeLister) TransitionValidatorFunc {
	return func() error {
		selector := labels.SelectorFromSet(labels.Set{
			"node-role.kubernetes.io/worker": "",
		})
		nodes, err := nodeLister.List(selector)
		if err != nil {
			return fmt.Errorf("failed to list infrastructure nodes: %w", err)
		}
		dedicatedWorkers := 0
		for _, node := range nodes {
			if _, hasControlPlane := node.Labels["node-role.kubernetes.io/control-plane"]; !hasControlPlane {
				dedicatedWorkers++
			}
		}
		if dedicatedWorkers != expected {
			return fmt.Errorf("unexpected infrastructure node count: expected %d dedicated workers, have %d", expected, dedicatedWorkers)
		}
		return nil
	}
}

// validateEtcdNotProgressing returns a TransitionValidatorFunc that checks the
// EtcdMembersProgressing condition on the etcds.operator.openshift.io/cluster CR
// to verify etcd is not in the middle of scaling up or adding members.
func validateEtcdNotProgressing(etcdLister operatorv1listers.EtcdLister) TransitionValidatorFunc {
	return func() error {
		etcd, err := etcdLister.Get("cluster")
		if err != nil {
			return fmt.Errorf("failed to get etcd operator CR: %w", err)
		}
		if v1helpers.IsOperatorConditionTrue(etcd.Status.Conditions, etcdMembersProgressingCondition) {
			cond := v1helpers.FindOperatorCondition(etcd.Status.Conditions, etcdMembersProgressingCondition)
			return fmt.Errorf("etcd is still progressing: %s", cond.Message)
		}
		return nil
	}
}

// validateEtcdVotingMembers returns a TransitionValidatorFunc that checks the
// etcd-endpoints ConfigMap in openshift-etcd to verify the required number of
// voting members are present. The Data keys in this ConfigMap are maintained by
// the cluster-etcd-operator's EtcdEndpointsController: each key is a voting
// member identifier (hex member ID, or node name in fallback mode) mapping to
// an IP address. Learner (non-voting) members and the bootstrap member are
// excluded by that controller, so len(Data) equals the voting member count.
func validateEtcdVotingMembers(required int, configMapLister corev1listers.ConfigMapNamespaceLister) TransitionValidatorFunc {
	return func() error {
		cm, err := configMapLister.Get(etcdEndpointsConfigMapName)
		if err != nil {
			return fmt.Errorf("failed to get %s/%s ConfigMap: %w", etcdNamespace, etcdEndpointsConfigMapName, err)
		}
		votingMembers := len(cm.Data)
		if votingMembers < required {
			return fmt.Errorf("insufficient etcd voting members: need %d, have %d", required, votingMembers)
		}
		return nil
	}
}

// validateEtcdQuorum returns a TransitionValidatorFunc that checks the
// EtcdMembersAvailable condition on the etcds.operator.openshift.io/cluster CR
// to verify etcd has quorum.
func validateEtcdQuorum(etcdLister operatorv1listers.EtcdLister) TransitionValidatorFunc {
	return func() error {
		etcd, err := etcdLister.Get("cluster")
		if err != nil {
			return fmt.Errorf("failed to get etcd operator CR: %w", err)
		}
		if !v1helpers.IsOperatorConditionTrue(etcd.Status.Conditions, etcdMembersAvailableCondition) {
			return fmt.Errorf("etcd does not have quorum: %s condition is not True", etcdMembersAvailableCondition)
		}
		return nil
	}
}

// validateControlPlaneNodesSchedulable returns a TransitionValidator that checks
// the number of schedulable control plane nodes meets the requirement.
func validateControlPlaneNodesSchedulable(required int, nodeLister corev1listers.NodeLister) TransitionValidatorFunc {
	return func() error {
		selector := labels.SelectorFromSet(labels.Set{
			"node-role.kubernetes.io/control-plane": "",
		})
		nodes, err := nodeLister.List(selector)
		if err != nil {
			return fmt.Errorf("failed to list control plane nodes: %w", err)
		}

		schedulable := 0
		for _, node := range nodes {
			if !node.Spec.Unschedulable {
				schedulable++
			}
		}
		if schedulable < required {
			return fmt.Errorf("insufficient schedulable control plane nodes: need %d, have %d", required, schedulable)
		}
		return nil
	}
}

// validateControlPlaneNodesReady returns a TransitionValidatorFunc that checks
// the required number of control plane nodes have a Ready=True condition.
func validateControlPlaneNodesReady(required int, nodeLister corev1listers.NodeLister) TransitionValidatorFunc {
	return func() error {
		selector := labels.SelectorFromSet(labels.Set{
			"node-role.kubernetes.io/control-plane": "",
		})
		nodes, err := nodeLister.List(selector)
		if err != nil {
			return fmt.Errorf("failed to list control plane nodes: %w", err)
		}

		readyCount := 0
		for _, node := range nodes {
			for _, cond := range node.Status.Conditions {
				if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
					readyCount++
					break
				}
			}
		}
		if readyCount < required {
			return fmt.Errorf("insufficient ready control plane nodes: need %d, have %d", required, readyCount)
		}
		return nil
	}
}
