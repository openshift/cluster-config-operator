package topology_transition_controller

import (
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	machineconfigurationv1 "github.com/openshift/api/machineconfiguration/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	configfakeclient "github.com/openshift/client-go/config/clientset/versioned/fake"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	machineconfigv1listers "github.com/openshift/client-go/machineconfiguration/listers/machineconfiguration/v1"
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

func newTestLegacyMasterNode(name string, unschedulable bool) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"node-role.kubernetes.io/master": "",
			},
		},
		Spec: corev1.NodeSpec{
			Unschedulable: unschedulable,
		},
	}
}

func newTestLegacyMasterNodeWithConditions(name string, unschedulable bool, conditions []corev1.NodeCondition) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"node-role.kubernetes.io/master": "",
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

func newTestWorkerNodeWithConditions(name string, conditions []corev1.NodeCondition) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"node-role.kubernetes.io/worker": "",
			},
		},
		Status: corev1.NodeStatus{
			Conditions: conditions,
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
			PreflightValidators: nil,
			UpdateStatus: func(infra *configv1.Infrastructure) {
				infra.Status.ControlPlaneTopology = configv1.HighlyAvailableTopologyMode
				infra.Status.InfrastructureTopology = configv1.HighlyAvailableTopologyMode
			},
			TransitionValidators: nil,
		},
	}
}

// noopTransitionsWithValidators returns the same transitions list as
// noopTransitions, but with the given TransitionValidators wired in so tests
// can control post-transition reconciliation behavior.
func noopTransitionsWithValidators(validators ...TransitionValidatorFunc) []TransitionDescriptor {
	transitions := noopTransitions()
	transitions[0].TransitionValidators = validators
	return transitions
}

// reconciliationTestTransitions returns a transitions list whose To spec is a
// wildcard (matches any Infrastructure spec), used to control
// checkClusterReconciliation behavior in tests independent of the infra's
// current spec topology.
func reconciliationTestTransitions(validators ...TransitionValidatorFunc) []TransitionDescriptor {
	return []TransitionDescriptor{
		{
			To:                   configv1.InfrastructureSpec{},
			TransitionValidators: validators,
		},
	}
}

// transitionProgressingConditions returns the standard in-progress operator conditions.
func transitionInProgressConditions() []operatorv1.OperatorCondition {
	return transitionInProgressConditionsAt(time.Now().Add(-10 * time.Minute))
}

func transitionInProgressConditionsAt(t time.Time) []operatorv1.OperatorCondition {
	return []operatorv1.OperatorCondition{
		{
			Type:               transitionProgressingCondition,
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

func newTestController(infra *configv1.Infrastructure, conditions []operatorv1.OperatorCondition, preflightChecks []TransitionValidatorFunc, transitions []TransitionDescriptor) *TopologyTransitionController {
	return newTestControllerWithClock(infra, conditions, preflightChecks, transitions, clocktesting.NewFakePassiveClock(time.Now()))
}

func newTestControllerWithClock(infra *configv1.Infrastructure, conditions []operatorv1.OperatorCondition, preflightChecks []TransitionValidatorFunc, transitions []TransitionDescriptor, clk *clocktesting.FakePassiveClock) *TopologyTransitionController {
	fakeConfigClient := configfakeclient.NewSimpleClientset(infra)

	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	if err := indexer.Add(infra); err != nil {
		panic(fmt.Sprintf("failed to add infra to indexer: %v", err))
	}

	operatorStatus := &operatorv1.OperatorStatus{}
	if len(conditions) > 0 {
		operatorStatus.Conditions = conditions
	}

	return &TopologyTransitionController{
		operatorClient: v1helpers.NewFakeOperatorClient(
			&operatorv1.OperatorSpec{},
			operatorStatus,
			nil,
		),
		infraLister:     configlistersv1.NewInfrastructureLister(indexer),
		infraClient:     fakeConfigClient.ConfigV1().Infrastructures(),
		preflightChecks: preflightChecks,
		transitions:     transitions,
		clock:           clk,
	}
}

// testFixture provides fake listers for building real transition descriptors.
type testFixture struct {
	nodeIndexer cache.Indexer
	cmIndexer   cache.Indexer
	etcdIndexer cache.Indexer
	coIndexer   cache.Indexer
	mcIndexer   cache.Indexer
	mcpIndexer  cache.Indexer
	icIndexer   cache.Indexer
	kasIndexer  cache.Indexer
	oasIndexer  cache.Indexer

	nodeLister corev1listers.NodeLister
	cmLister   corev1listers.ConfigMapNamespaceLister
	etcdLister operatorv1listers.EtcdLister
	coLister   configlistersv1.ClusterOperatorLister
	mcLister   machineconfigv1listers.MachineConfigLister
	mcpLister  machineconfigv1listers.MachineConfigPoolLister
	icLister   operatorv1listers.IngressControllerNamespaceLister
	kasLister  operatorv1listers.KubeAPIServerLister
	oasLister  operatorv1listers.OpenShiftAPIServerLister
}

// ingressOperatorNamespace is the namespace IngressControllers live in.
const ingressOperatorNamespace = "openshift-ingress-operator"

func newTestFixture() *testFixture {
	nodeIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	cmIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	etcdIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	coIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	mcIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	mcpIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	icIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	kasIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	oasIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})

	return &testFixture{
		nodeIndexer: nodeIndexer,
		cmIndexer:   cmIndexer,
		etcdIndexer: etcdIndexer,
		coIndexer:   coIndexer,
		mcIndexer:   mcIndexer,
		mcpIndexer:  mcpIndexer,
		icIndexer:   icIndexer,
		kasIndexer:  kasIndexer,
		oasIndexer:  oasIndexer,

		nodeLister: corev1listers.NewNodeLister(nodeIndexer),
		cmLister:   corev1listers.NewConfigMapLister(cmIndexer).ConfigMaps(etcdNamespace),
		etcdLister: operatorv1listers.NewEtcdLister(etcdIndexer),
		coLister:   configlistersv1.NewClusterOperatorLister(coIndexer),
		mcLister:   machineconfigv1listers.NewMachineConfigLister(mcIndexer),
		mcpLister:  machineconfigv1listers.NewMachineConfigPoolLister(mcpIndexer),
		icLister:   operatorv1listers.NewIngressControllerLister(icIndexer).IngressControllers(ingressOperatorNamespace),
		kasLister:  operatorv1listers.NewKubeAPIServerLister(kasIndexer),
		oasLister:  operatorv1listers.NewOpenShiftAPIServerLister(oasIndexer),
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

func (f *testFixture) withClusterOperators(operators ...*configv1.ClusterOperator) *testFixture {
	for _, co := range operators {
		if err := f.coIndexer.Add(co); err != nil {
			panic(fmt.Sprintf("failed to add cluster operator to indexer: %v", err))
		}
	}
	return f
}

func (f *testFixture) withMachineConfigs(configs ...*machineconfigurationv1.MachineConfig) *testFixture {
	for _, mc := range configs {
		if err := f.mcIndexer.Add(mc); err != nil {
			panic(fmt.Sprintf("failed to add MachineConfig to indexer: %v", err))
		}
	}
	return f
}

func (f *testFixture) withMachineConfigPool(pool *machineconfigurationv1.MachineConfigPool) *testFixture {
	if err := f.mcpIndexer.Add(pool); err != nil {
		panic(fmt.Sprintf("failed to add MachineConfigPool to indexer: %v", err))
	}
	return f
}

func (f *testFixture) withIngressController(ic *operatorv1.IngressController) *testFixture {
	if err := f.icIndexer.Add(ic); err != nil {
		panic(fmt.Sprintf("failed to add IngressController to indexer: %v", err))
	}
	return f
}

func (f *testFixture) withKubeAPIServer(kas *operatorv1.KubeAPIServer) *testFixture {
	if err := f.kasIndexer.Add(kas); err != nil {
		panic(fmt.Sprintf("failed to add KubeAPIServer to indexer: %v", err))
	}
	return f
}

func (f *testFixture) withOpenShiftAPIServer(oas *operatorv1.OpenShiftAPIServer) *testFixture {
	if err := f.oasIndexer.Add(oas); err != nil {
		panic(fmt.Sprintf("failed to add OpenShiftAPIServer to indexer: %v", err))
	}
	return f
}

func (f *testFixture) buildTransitions() []TransitionDescriptor {
	return buildSupportedTransitions(TransitionValidationListers{
		NodeLister:               f.nodeLister,
		EtcdConfigMapLister:      f.cmLister,
		EtcdLister:               f.etcdLister,
		KubeAPIServerLister:      f.kasLister,
		OpenShiftAPIServerLister: f.oasLister,
		IngressControllerLister:  f.icLister,
		MachineConfigLister:      f.mcLister,
		MachineConfigPoolLister:  f.mcpLister,
	})
}

func (f *testFixture) buildPreflightChecks() []TransitionValidatorFunc {
	return []TransitionValidatorFunc{
		validateClusterOperatorsStable(f.coLister),
	}
}

func (f *testFixture) newController(infra *configv1.Infrastructure, conditions []operatorv1.OperatorCondition) *TopologyTransitionController {
	return newTestController(infra, conditions, f.buildPreflightChecks(), f.buildTransitions())
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

func newTestMachineConfig(name string) *machineconfigurationv1.MachineConfig {
	return &machineconfigurationv1.MachineConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
}

func newTestMachineConfigPool(name string, machineCount, readyMachineCount int32) *machineconfigurationv1.MachineConfigPool {
	return &machineconfigurationv1.MachineConfigPool{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: machineconfigurationv1.MachineConfigPoolStatus{
			MachineCount:      machineCount,
			ReadyMachineCount: readyMachineCount,
		},
	}
}

func newTestIngressController(name string, availableReplicas int32) *operatorv1.IngressController {
	return &operatorv1.IngressController{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ingressOperatorNamespace},
		Status: operatorv1.IngressControllerStatus{
			AvailableReplicas: availableReplicas,
		},
	}
}

func newTestKubeAPIServerCR(nodeCount int) *operatorv1.KubeAPIServer {
	nodeStatuses := make([]operatorv1.NodeStatus, nodeCount)
	return &operatorv1.KubeAPIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Status: operatorv1.KubeAPIServerStatus{
			StaticPodOperatorStatus: operatorv1.StaticPodOperatorStatus{
				NodeStatuses: nodeStatuses,
			},
		},
	}
}

func newTestOpenShiftAPIServerCR(readyReplicas int32) *operatorv1.OpenShiftAPIServer {
	return &operatorv1.OpenShiftAPIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Status: operatorv1.OpenShiftAPIServerStatus{
			OperatorStatus: operatorv1.OperatorStatus{
				ReadyReplicas: readyReplicas,
			},
		},
	}
}
