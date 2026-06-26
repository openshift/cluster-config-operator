package topology_transition_controller

import (
	"errors"
	"fmt"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
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

// validatePreflight runs global preflight checks followed by
// transition-specific validators. Returns a combined error containing all
// validation failures.
func validatePreflight(globalChecks []TransitionValidatorFunc, transition *TransitionDescriptor) error {
	var errs []error
	for _, v := range globalChecks {
		if err := v(); err != nil {
			errs = append(errs, fmt.Errorf("transition validation failed: %w", err))
		}
	}
	for _, v := range transition.Validators {
		if err := v(); err != nil {
			errs = append(errs, fmt.Errorf("transition validation failed: %w", err))
		}
	}
	return errors.Join(errs...)
}

// isControlPlaneNode returns true if the node carries either the modern
// control-plane or the legacy master role label.
func isControlPlaneNode(node *corev1.Node) bool {
	_, hasControlPlane := node.Labels["node-role.kubernetes.io/control-plane"]
	_, hasMaster := node.Labels["node-role.kubernetes.io/master"]
	return hasControlPlane || hasMaster
}

// listControlPlaneNodes returns all nodes with either the control-plane or
// legacy master role label.
func listControlPlaneNodes(nodeLister corev1listers.NodeLister) ([]*corev1.Node, error) {
	allNodes, err := nodeLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	var result []*corev1.Node
	for _, node := range allNodes {
		if isControlPlaneNode(node) {
			result = append(result, node)
		}
	}
	return result, nil
}

// validateControlPlaneNodeCount returns a TransitionValidator that checks
// the number of control plane nodes meets the requirement.
func validateControlPlaneNodeCount(required int, nodeLister corev1listers.NodeLister) TransitionValidatorFunc {
	return func() error {
		nodes, err := listControlPlaneNodes(nodeLister)
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
			if !isControlPlaneNode(node) {
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
		nodes, err := listControlPlaneNodes(nodeLister)
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

// checkClusterOperatorsStable checks whether all ClusterOperators (except
// config-operator itself) have reached a stable state. Returns a list of
// descriptions for unstable operators (empty = all stable). This is the shared
// core used by both the preflight validator and the post-transition
// reconciliation check.
func checkClusterOperatorsStable(coLister configlistersv1.ClusterOperatorLister) ([]string, error) {
	operators, err := coLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	var unstable []string
	for _, co := range operators {
		if co.Name == selfClusterOperatorName {
			continue
		}
		var issues []string
		availableSeen := false
		progressingSeen := false
		degradedSeen := false
		for _, cond := range co.Status.Conditions {
			switch cond.Type {
			case configv1.OperatorAvailable:
				availableSeen = true
				if cond.Status != configv1.ConditionTrue {
					issues = append(issues, "Available="+string(cond.Status))
				}
			case configv1.OperatorProgressing:
				progressingSeen = true
				if cond.Status != configv1.ConditionFalse {
					issues = append(issues, "Progressing="+string(cond.Status))
				}
			case configv1.OperatorDegraded:
				degradedSeen = true
				if cond.Status != configv1.ConditionFalse {
					issues = append(issues, "Degraded="+string(cond.Status))
				}
			}
		}
		if !availableSeen {
			issues = append(issues, "Available condition missing")
		}
		if !progressingSeen {
			issues = append(issues, "Progressing condition missing")
		}
		if !degradedSeen {
			issues = append(issues, "Degraded condition missing")
		}
		if len(issues) > 0 {
			unstable = append(unstable, fmt.Sprintf("%s: %s", co.Name, strings.Join(issues, ", ")))
		}
	}
	return unstable, nil
}

// validateClusterOperatorsStable returns a TransitionValidatorFunc that checks
// all ClusterOperators are stable before allowing a topology transition.
func validateClusterOperatorsStable(coLister configlistersv1.ClusterOperatorLister) TransitionValidatorFunc {
	return func() error {
		unstable, err := checkClusterOperatorsStable(coLister)
		if err != nil {
			return fmt.Errorf("failed to check cluster operator stability: %w", err)
		}
		if len(unstable) > 0 {
			return fmt.Errorf("cluster operators are not stable: %s", strings.Join(unstable, "; "))
		}
		return nil
	}
}

// validateControlPlaneNodesReady returns a TransitionValidatorFunc that checks
// the required number of control plane nodes have a Ready=True condition.
func validateControlPlaneNodesReady(required int, nodeLister corev1listers.NodeLister) TransitionValidatorFunc {
	return func() error {
		nodes, err := listControlPlaneNodes(nodeLister)
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
