package e2e

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestClusterConfigOperatorRunsInCluster(t *testing.T) {
	tests := []struct {
		name     string
		topology configv1.TopologyMode
		expected bool
	}{
		{name: "highly available", topology: configv1.HighlyAvailableTopologyMode, expected: true},
		{name: "highly available arbiter", topology: configv1.HighlyAvailableArbiterMode, expected: true},
		{name: "single replica", topology: configv1.SingleReplicaTopologyMode, expected: true},
		{name: "dual replica", topology: configv1.DualReplicaTopologyMode, expected: true},
		{name: "external", topology: configv1.ExternalTopologyMode, expected: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if actual := clusterConfigOperatorRunsInCluster(test.topology); actual != test.expected {
				t.Fatalf("clusterConfigOperatorRunsInCluster(%q) = %t, want %t", test.topology, actual, test.expected)
			}
		})
	}
}

func TestContainerIsCurrentlyHealthy(t *testing.T) {
	tests := []struct {
		name     string
		status   corev1.ContainerStatus
		expected bool
	}{
		{
			name: "ready and running after clean restarts",
			status: corev1.ContainerStatus{
				Ready:        true,
				RestartCount: 9,
				State: corev1.ContainerState{
					Running: &corev1.ContainerStateRunning{},
				},
			},
			expected: true,
		},
		{
			name: "crash loop",
			status: corev1.ContainerStatus{
				Ready: true,
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
				},
			},
			expected: false,
		},
		{
			name: "running but not ready",
			status: corev1.ContainerStatus{
				State: corev1.ContainerState{
					Running: &corev1.ContainerStateRunning{},
				},
			},
			expected: false,
		},
		{
			name: "terminated",
			status: corev1.ContainerStatus{
				Ready: true,
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{Reason: "Error"},
				},
			},
			expected: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if actual := containerIsCurrentlyHealthy(test.status); actual != test.expected {
				t.Fatalf("containerIsCurrentlyHealthy(%#v) = %t, want %t", test.status, actual, test.expected)
			}
		})
	}
}

func TestExpectedUpgradeableStatus(t *testing.T) {
	tests := []struct {
		name       string
		featureSet configv1.FeatureSet
		isSCOS     bool
		expected   configv1.ConditionStatus
	}{
		{name: "default", featureSet: configv1.Default, expected: configv1.ConditionTrue},
		{name: "tech preview", featureSet: configv1.TechPreviewNoUpgrade, expected: configv1.ConditionFalse},
		{name: "dev preview", featureSet: configv1.DevPreviewNoUpgrade, expected: configv1.ConditionFalse},
		{name: "custom", featureSet: configv1.CustomNoUpgrade, expected: configv1.ConditionFalse},
		{name: "OKD on OpenShift", featureSet: configv1.OKD, expected: configv1.ConditionFalse},
		{name: "OKD on SCOS", featureSet: configv1.OKD, isSCOS: true, expected: configv1.ConditionTrue},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if actual := expectedUpgradeableStatus(test.featureSet, test.isSCOS); actual != test.expected {
				t.Fatalf("expectedUpgradeableStatus(%q, %t) = %q, want %q", test.featureSet, test.isSCOS, actual, test.expected)
			}
		})
	}
}
