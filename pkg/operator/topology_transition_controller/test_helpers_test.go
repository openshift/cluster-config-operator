package topology_transition_controller

import (
	"context"
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	configfakeclient "github.com/openshift/client-go/config/clientset/versioned/fake"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	operatorv1listers "github.com/openshift/client-go/operator/listers/operator/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	clocktesting "k8s.io/utils/clock/testing"
)

func newTestSyncContext() factory.SyncContext {
	return factory.NewSyncContext(
		"TopologyTransitionController",
		events.NewInMemoryRecorder(
			"TopologyTransitionController",
			clocktesting.NewFakePassiveClock(time.Now()),
		),
	)
}

func newTestInfra(specTopology, statusTopology, statusInfraTopology configv1.TopologyMode, platformType configv1.PlatformType) *configv1.Infrastructure {
	infra := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Status: configv1.InfrastructureStatus{
			ControlPlaneTopology:   statusTopology,
			InfrastructureTopology: statusInfraTopology,
		},
	}
	if specTopology != "" {
		infra.Spec.ControlPlaneTopology = specTopology
	}
	if platformType != "" {
		infra.Status.PlatformStatus = &configv1.PlatformStatus{Type: platformType}
	}
	return infra
}

func newTestControlPlaneNode(name string, unschedulable bool) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"node-role.kubernetes.io/control-plane": "",
			},
		},
		Spec: corev1.NodeSpec{
			Unschedulable: unschedulable,
		},
	}
}

func newTestControlPlaneNodeWithConditions(name string, unschedulable bool, conditions []corev1.NodeCondition) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"node-role.kubernetes.io/control-plane": "",
			},
		},
		Spec: corev1.NodeSpec{
			Unschedulable: unschedulable,
		},
		Status: corev1.NodeStatus{
			Conditions: conditions,
		},
	}
}

func readyNodeCondition() []corev1.NodeCondition {
	return []corev1.NodeCondition{
		{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
	}
}

func notReadyNodeCondition() []corev1.NodeCondition {
	return []corev1.NodeCondition{
		{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
	}
}

func newTestWorkerNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"node-role.kubernetes.io/worker": "",
			},
		},
	}
}

func newTestDualRoleNode(name string, unschedulable bool) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"node-role.kubernetes.io/control-plane": "",
				"node-role.kubernetes.io/worker":        "",
			},
		},
		Spec: corev1.NodeSpec{
			Unschedulable: unschedulable,
		},
	}
}

func newTestDualRoleNodeWithConditions(name string, unschedulable bool, conditions []corev1.NodeCondition) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"node-role.kubernetes.io/control-plane": "",
				"node-role.kubernetes.io/worker":        "",
			},
		},
		Spec: corev1.NodeSpec{
			Unschedulable: unschedulable,
		},
		Status: corev1.NodeStatus{
			Conditions: conditions,
		},
	}
}

func newTestEtcdEndpointsConfigMap(memberCount int) *corev1.ConfigMap {
	data := map[string]string{}
	for i := 0; i < memberCount; i++ {
		data[fmt.Sprintf("member-%d", i)] = fmt.Sprintf("10.0.0.%d", i+1)
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      etcdEndpointsConfigMapName,
			Namespace: etcdNamespace,
		},
		Data: data,
	}
}

func newTestEtcdCR(membersAvailable bool, progressing bool) *operatorv1.Etcd {
	conditions := []operatorv1.OperatorCondition{}
	if membersAvailable {
		conditions = append(conditions, operatorv1.OperatorCondition{
			Type:   etcdMembersAvailableCondition,
			Status: operatorv1.ConditionTrue,
			Reason: "EtcdQuorate",
		})
	} else {
		conditions = append(conditions, operatorv1.OperatorCondition{
			Type:   etcdMembersAvailableCondition,
			Status: operatorv1.ConditionFalse,
			Reason: "NoQuorum",
		})
	}
	if progressing {
		conditions = append(conditions, operatorv1.OperatorCondition{
			Type:    etcdMembersProgressingCondition,
			Status:  operatorv1.ConditionTrue,
			Reason:  "MembersNotStarted",
			Message: "1 member has not started",
		})
	} else {
		conditions = append(conditions, operatorv1.OperatorCondition{
			Type:   etcdMembersProgressingCondition,
			Status: operatorv1.ConditionFalse,
			Reason: "AsExpected",
		})
	}
	return &operatorv1.Etcd{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Status: operatorv1.EtcdStatus{
			StaticPodOperatorStatus: operatorv1.StaticPodOperatorStatus{
				OperatorStatus: operatorv1.OperatorStatus{
					Conditions: conditions,
				},
			},
		},
	}
}

// noopTransitions returns a transitions list that matches SNO→HA on None
// without real listers — validators always pass.
func noopTransitions() []TransitionDescriptor {
	return []TransitionDescriptor{
		{
			From: configv1.InfrastructureStatus{
				ControlPlaneTopology:   configv1.SingleReplicaTopologyMode,
				InfrastructureTopology: configv1.SingleReplicaTopologyMode,
				PlatformStatus:         &configv1.PlatformStatus{Type: configv1.NonePlatformType},
			},
			To: configv1.InfrastructureSpec{
				ControlPlaneTopology: configv1.HighlyAvailableTopologyMode,
			},
			Validators: nil,
			UpdateStatus: func(infra *configv1.Infrastructure) {
				infra.Status.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
				infra.Status.InfrastructureTopology = configv1.HighlyAvailableTopologyMode
			},
		},
	}
}

// transitionConditions returns the standard in-progress operator conditions.
func transitionInProgressConditions() []operatorv1.OperatorCondition {
	return transitionInProgressConditionsAt(time.Now().Add(-10 * time.Minute))
}

func transitionInProgressConditionsAt(t time.Time) []operatorv1.OperatorCondition {
	return []operatorv1.OperatorCondition{
		{
			Type:               transitionCondition,
			Status:             operatorv1.ConditionTrue,
			Reason:             reasonTopologyTransitionInProgress,
			LastTransitionTime: metav1.NewTime(t),
		},
		{
			Type:               upgradeableCondition,
			Status:             operatorv1.ConditionFalse,
			Reason:             reasonTopologyTransitionInProgress,
			LastTransitionTime: metav1.NewTime(t),
		},
	}
}

func newTestController(infra *configv1.Infrastructure, conditions []operatorv1.OperatorCondition, transitions []TransitionDescriptor, reconciliationChecks []func(context.Context) (bool, error)) *TopologyTransitionController {
	return newTestControllerWithClock(infra, conditions, transitions, reconciliationChecks, clocktesting.NewFakePassiveClock(time.Now()))
}

func newTestControllerWithClock(infra *configv1.Infrastructure, conditions []operatorv1.OperatorCondition, transitions []TransitionDescriptor, reconciliationChecks []func(context.Context) (bool, error), clk *clocktesting.FakePassiveClock) *TopologyTransitionController {
	fakeConfigClient := configfakeclient.NewSimpleClientset(infra)

	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	if err := indexer.Add(infra); err != nil {
		panic(fmt.Sprintf("failed to add infra to indexer: %v", err))
	}

	operatorStatus := &operatorv1.OperatorStatus{}
	if len(conditions) > 0 {
		operatorStatus.Conditions = conditions
	}

	if reconciliationChecks == nil {
		reconciliationChecks = []func(context.Context) (bool, error){
			func(ctx context.Context) (bool, error) { return true, nil },
		}
	}

	return &TopologyTransitionController{
		operatorClient: v1helpers.NewFakeOperatorClient(
			&operatorv1.OperatorSpec{},
			operatorStatus,
			nil,
		),
		infraLister:          configlistersv1.NewInfrastructureLister(indexer),
		infraClient:          fakeConfigClient.ConfigV1().Infrastructures(),
		reconciliationChecks: reconciliationChecks,
		transitions:          transitions,
		clock:                clk,
	}
}

// testFixture provides fake listers for building real transition descriptors.
type testFixture struct {
	nodeIndexer cache.Indexer
	cmIndexer   cache.Indexer
	etcdIndexer cache.Indexer
	nodeLister  corev1listers.NodeLister
	cmLister    corev1listers.ConfigMapNamespaceLister
	etcdLister  operatorv1listers.EtcdLister
}

func newTestFixture() *testFixture {
	nodeIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	cmIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	etcdIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})

	return &testFixture{
		nodeIndexer: nodeIndexer,
		cmIndexer:   cmIndexer,
		etcdIndexer: etcdIndexer,
		nodeLister:  corev1listers.NewNodeLister(nodeIndexer),
		cmLister:    corev1listers.NewConfigMapLister(cmIndexer).ConfigMaps(etcdNamespace),
		etcdLister:  operatorv1listers.NewEtcdLister(etcdIndexer),
	}
}

func (f *testFixture) withNodes(nodes ...*corev1.Node) *testFixture {
	for _, n := range nodes {
		if err := f.nodeIndexer.Add(n); err != nil {
			panic(fmt.Sprintf("failed to add node to indexer: %v", err))
		}
	}
	return f
}

func (f *testFixture) withEtcdEndpoints(memberCount int) *testFixture {
	if err := f.cmIndexer.Add(newTestEtcdEndpointsConfigMap(memberCount)); err != nil {
		panic(fmt.Sprintf("failed to add configmap to indexer: %v", err))
	}
	return f
}

func (f *testFixture) withEtcdCR(membersAvailable bool, progressing bool) *testFixture {
	if err := f.etcdIndexer.Add(newTestEtcdCR(membersAvailable, progressing)); err != nil {
		panic(fmt.Sprintf("failed to add etcd CR to indexer: %v", err))
	}
	return f
}

func (f *testFixture) buildTransitions() []TransitionDescriptor {
	return buildSupportedTransitions(f.nodeLister, f.cmLister, f.etcdLister)
}

func (f *testFixture) newController(infra *configv1.Infrastructure, conditions []operatorv1.OperatorCondition) *TopologyTransitionController {
	return newTestController(infra, conditions, f.buildTransitions(), nil)
}

func newTestClusterOperator(name string, available, progressing, degraded configv1.ConditionStatus) *configv1.ClusterOperator {
	return &configv1.ClusterOperator{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: configv1.ClusterOperatorStatus{
			Conditions: []configv1.ClusterOperatorStatusCondition{
				{Type: configv1.OperatorAvailable, Status: available},
				{Type: configv1.OperatorProgressing, Status: progressing},
				{Type: configv1.OperatorDegraded, Status: degraded},
			},
		},
	}
}

func newTestClusterOperatorLister(operators ...*configv1.ClusterOperator) configlistersv1.ClusterOperatorLister {
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for _, co := range operators {
		if err := indexer.Add(co); err != nil {
			panic(fmt.Sprintf("failed to add cluster operator to indexer: %v", err))
		}
	}
	return configlistersv1.NewClusterOperatorLister(indexer)
}
