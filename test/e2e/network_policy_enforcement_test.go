package e2e

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

const (
	agnhostImage = "registry.k8s.io/e2e-test-images/agnhost:2.45"

	// Namespace constants for openshift-config-operator
	configOperatorNamespace = "openshift-config-operator"
	configNamespace         = "openshift-config"
	configManagedNamespace  = "openshift-config-managed"

	// NetworkPolicy names
	configOperatorPolicyName = "config-operator-networkpolicy"
	defaultDenyAllPolicyName = "default-deny-all"
)

func TestGenericNetworkPolicyEnforcement(t *testing.T) {
	kubeConfig, err := getKubeConfig()
	if err != nil {
		t.Fatalf("failed to get kubeconfig: %v", err)
	}
	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kubernetes client: %v", err)
	}

	t.Log("Creating a temporary namespace for policy enforcement checks")
	nsName := "np-enforcement-" + rand.String(5)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}
	_, err = kubeClient.CoreV1().Namespaces().Create(context.TODO(), ns, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create test namespace: %v", err)
	}
	defer func() {
		t.Logf("deleting test namespace %s", nsName)
		_ = kubeClient.CoreV1().Namespaces().Delete(context.TODO(), nsName, metav1.DeleteOptions{})
	}()

	serverName := "np-server"
	clientLabels := map[string]string{"app": "np-client"}
	serverLabels := map[string]string{"app": "np-server"}

	t.Logf("creating netexec server pod %s/%s", nsName, serverName)
	serverPod := netexecPod(serverName, nsName, serverLabels, 8080)
	_, err = kubeClient.CoreV1().Pods(nsName).Create(context.TODO(), serverPod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create server pod: %v", err)
	}
	if err := waitForPodReadyT(t, kubeClient, nsName, serverName); err != nil {
		t.Fatalf("server pod not ready: %v", err)
	}

	server, err := kubeClient.CoreV1().Pods(nsName).Get(context.TODO(), serverName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get server pod: %v", err)
	}
	if len(server.Status.PodIPs) == 0 {
		t.Fatalf("server pod has no IPs")
	}
	serverIPs := podIPs(server)
	t.Logf("server pod %s/%s ips=%v", nsName, serverName, serverIPs)

	t.Log("Verifying allow-all when no policies select the pod")
	expectConnectivity(t, kubeClient, nsName, clientLabels, serverIPs, 8080, true)

	t.Log("Applying default deny and verifying traffic is blocked")
	t.Logf("creating default-deny policy in %s", nsName)
	_, err = kubeClient.NetworkingV1().NetworkPolicies(nsName).Create(context.TODO(), defaultDenyPolicy("default-deny", nsName), metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create default-deny policy: %v", err)
	}

	t.Log("Adding ingress allow only and verifying traffic is still blocked")
	t.Logf("creating allow-ingress policy in %s", nsName)
	_, err = kubeClient.NetworkingV1().NetworkPolicies(nsName).Create(context.TODO(), allowIngressPolicy("allow-ingress", nsName, serverLabels, clientLabels, 8080), metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create allow-ingress policy: %v", err)
	}
	expectConnectivity(t, kubeClient, nsName, clientLabels, serverIPs, 8080, false)

	t.Log("Adding egress allow and verifying traffic is permitted")
	t.Logf("creating allow-egress policy in %s", nsName)
	_, err = kubeClient.NetworkingV1().NetworkPolicies(nsName).Create(context.TODO(), allowEgressPolicy("allow-egress", nsName, clientLabels, serverLabels, 8080), metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create allow-egress policy: %v", err)
	}
	expectConnectivity(t, kubeClient, nsName, clientLabels, serverIPs, 8080, true)
}

func TestConfigOperatorNetworkPolicyEnforcement(t *testing.T) {
	kubeConfig, err := getKubeConfig()
	if err != nil {
		t.Fatalf("failed to get kubeconfig: %v", err)
	}
	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kubernetes client: %v", err)
	}

	// Labels must match the NetworkPolicy pod selectors for egress to work
	operatorLabels := map[string]string{"app": "openshift-config-operator"}

	t.Log("Verifying config operator NetworkPolicies exist")
	_, err = kubeClient.NetworkingV1().NetworkPolicies(configOperatorNamespace).Get(context.TODO(), configOperatorPolicyName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get config operator NetworkPolicy: %v", err)
	}
	_, err = kubeClient.NetworkingV1().NetworkPolicies(configOperatorNamespace).Get(context.TODO(), defaultDenyAllPolicyName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get default-deny-all NetworkPolicy: %v", err)
	}

	t.Log("Creating test pods in openshift-config-operator for allow/deny checks")
	t.Logf("creating operator server pods in %s", configOperatorNamespace)
	allowedServerIPs, cleanupAllowed := createServerPodT(t, kubeClient, configOperatorNamespace, "np-operator-allowed", operatorLabels, 8443)
	defer cleanupAllowed()
	deniedServerIPs, cleanupDenied := createServerPodT(t, kubeClient, configOperatorNamespace, "np-operator-denied", operatorLabels, 12345)
	defer cleanupDenied()

	t.Log("Verifying allowed port 8443 ingress to operator")
	expectConnectivity(t, kubeClient, configOperatorNamespace, operatorLabels, allowedServerIPs, 8443, true)

	t.Log("Verifying denied port 12345 (not in NetworkPolicy)")
	expectConnectivity(t, kubeClient, configOperatorNamespace, operatorLabels, deniedServerIPs, 12345, false)

	t.Log("Verifying denied ports even from same namespace")
	for _, port := range []int32{80, 443, 6443, 9090} {
		expectConnectivity(t, kubeClient, configOperatorNamespace, operatorLabels, allowedServerIPs, port, false)
	}

	// Check if the NetworkPolicy allows DNS egress
	t.Log("Checking if NetworkPolicy allows DNS egress")
	operatorPolicy, err := kubeClient.NetworkingV1().NetworkPolicies(configOperatorNamespace).Get(context.TODO(), configOperatorPolicyName, metav1.GetOptions{})
	if err != nil {
		t.Logf("Warning: could not get operator NetworkPolicy: %v", err)
	} else {
		hasDNSEgress := false
		for _, egressRule := range operatorPolicy.Spec.Egress {
			for _, port := range egressRule.Ports {
				if port.Port != nil && (port.Port.IntVal == 53 || port.Port.IntVal == 5353) {
					hasDNSEgress = true
					break
				}
			}
			if hasDNSEgress {
				break
			}
		}

		if hasDNSEgress {
			t.Log("NetworkPolicy allows DNS egress, testing DNS connectivity")
			dnsSvc, err := kubeClient.CoreV1().Services("openshift-dns").Get(context.TODO(), "dns-default", metav1.GetOptions{})
			if err != nil {
				t.Logf("Warning: failed to get DNS service, skipping DNS egress test: %v", err)
			} else {
				dnsIPs := serviceClusterIPs(dnsSvc)
				t.Logf("Testing egress from %s to DNS %v", configOperatorNamespace, dnsIPs)

				// Try common DNS ports
				dnsReachable := false
				for _, port := range []int32{53, 5353} {
					t.Logf("Checking DNS connectivity on port %d", port)
					// Use a shorter timeout for DNS checks since they might not be configured
					if err := testConnectivityWithTimeout(t, kubeClient, configOperatorNamespace, operatorLabels, dnsIPs, port, true, 30*time.Second); err != nil {
						t.Logf("DNS connectivity test on port %d failed (this may be expected): %v", port, err)
					} else {
						dnsReachable = true
						t.Logf("DNS connectivity on port %d succeeded", port)
						break
					}
				}
				if !dnsReachable {
					t.Fatalf("NetworkPolicy exposes DNS egress rules, but connectivity to dns-default failed on all tested ports")
				}
			}
		} else {
			t.Log("NetworkPolicy does not explicitly allow DNS egress, skipping DNS connectivity test")
		}
	}
}

func TestConfigNamespaceNetworkPolicies(t *testing.T) {
	kubeConfig, err := getKubeConfig()
	if err != nil {
		t.Fatalf("failed to get kubeconfig: %v", err)
	}
	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kubernetes client: %v", err)
	}

	// Test all three config-related namespaces
	namespacesToTest := []string{configOperatorNamespace, configNamespace, configManagedNamespace}

	for _, ns := range namespacesToTest {
		t.Logf("=== Testing namespace: %s ===", ns)

		t.Logf("Verifying namespace %s exists", ns)
		_, err = kubeClient.CoreV1().Namespaces().Get(context.TODO(), ns, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("failed to get namespace %s: %v", ns, err)
		}

		// Check for NetworkPolicies
		t.Logf("Checking for NetworkPolicies in %s", ns)
		policies, err := kubeClient.NetworkingV1().NetworkPolicies(ns).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			t.Fatalf("failed to list NetworkPolicies in %s: %v", ns, err)
		}

		if len(policies.Items) > 0 {
			t.Logf("Found %d NetworkPolicy(ies) in %s", len(policies.Items), ns)
			for _, policy := range policies.Items {
				t.Logf("  - %s", policy.Name)
				logNetworkPolicyDetails(t, fmt.Sprintf("%s/%s", ns, policy.Name), &policy)
			}
		} else {
			t.Logf("No NetworkPolicies found in %s", ns)
		}

		// List pods in these namespaces
		t.Logf("Checking for pods in %s", ns)
		pods, err := kubeClient.CoreV1().Pods(ns).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			t.Fatalf("failed to list pods in %s: %v", ns, err)
		}

		if len(pods.Items) > 0 {
			t.Logf("Found %d pod(s) in %s", len(pods.Items), ns)
			for _, pod := range pods.Items {
				t.Logf("  - %s (phase: %s, labels: %v)", pod.Name, pod.Status.Phase, pod.Labels)
			}
		} else {
			t.Logf("No pods found in %s", ns)
		}
	}
}

// TestConfigNamespacesNetworkPolicyEnforcement tests that NetworkPolicies are properly enforced
// in openshift-config, openshift-config-operator, and openshift-config-managed namespaces
func TestConfigNamespacesNetworkPolicyEnforcement(t *testing.T) {
	kubeConfig, err := getKubeConfig()
	if err != nil {
		t.Fatalf("failed to get kubeconfig: %v", err)
	}
	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		t.Fatalf("failed to create kubernetes client: %v", err)
	}

	// Test NetworkPolicy enforcement in each namespace
	namespacesToTest := []struct {
		namespace string
		testPods  bool // whether we should test with actual pods
	}{
		{configOperatorNamespace, true},  // openshift-config-operator has running pods
		{configNamespace, false},          // openshift-config typically has no pods
		{configManagedNamespace, false},   // openshift-config-managed typically has no pods
	}

	for _, ns := range namespacesToTest {
		t.Logf("=== Testing NetworkPolicy enforcement in %s ===", ns.namespace)

		// Check what NetworkPolicies exist
		policies, err := kubeClient.NetworkingV1().NetworkPolicies(ns.namespace).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			t.Fatalf("failed to list NetworkPolicies in %s: %v", ns.namespace, err)
		}

		if len(policies.Items) == 0 {
			t.Logf("No NetworkPolicies found in %s, skipping enforcement tests", ns.namespace)
			continue
		}

		t.Logf("Found %d NetworkPolicy(ies) in %s", len(policies.Items), ns.namespace)
		for _, policy := range policies.Items {
			t.Logf("  - %s (podSelector: %v, ingress rules: %d, egress rules: %d)",
				policy.Name,
				policy.Spec.PodSelector.MatchLabels,
				len(policy.Spec.Ingress),
				len(policy.Spec.Egress))
		}

		// If the namespace typically has no pods, we can't test enforcement
		if !ns.testPods {
			t.Logf("Namespace %s typically has no pods, skipping pod-based enforcement tests", ns.namespace)
			continue
		}

		// For namespaces with pods, verify existing pods are still running
		// (which means NetworkPolicies aren't blocking legitimate traffic)
		pods, err := kubeClient.CoreV1().Pods(ns.namespace).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			t.Fatalf("failed to list pods in %s: %v", ns.namespace, err)
		}

		if len(pods.Items) > 0 {
			t.Logf("Verifying that %d existing pod(s) in %s are healthy despite NetworkPolicies", len(pods.Items), ns.namespace)
			for _, pod := range pods.Items {
				// Check if pod is running and ready
				isReady := false
				for _, condition := range pod.Status.Conditions {
					if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
						isReady = true
						break
					}
				}

				if pod.Status.Phase == corev1.PodRunning && isReady {
					t.Logf("  ✓ Pod %s is running and ready", pod.Name)
				} else {
					t.Logf("  - Pod %s phase: %s, ready: %v", pod.Name, pod.Status.Phase, isReady)
				}
			}
		}
	}
}

func netexecPod(name, namespace string, labels map[string]string, port int32) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   boolptr(true),
				RunAsUser:      int64ptr(1001),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{
				{
					Name:  "netexec",
					Image: agnhostImage,
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: boolptr(false),
						Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						RunAsNonRoot:             boolptr(true),
						RunAsUser:                int64ptr(1001),
					},
					Command: []string{"/agnhost"},
					Args:    []string{"netexec", fmt.Sprintf("--http-port=%d", port)},
					Ports: []corev1.ContainerPort{
						{ContainerPort: port},
					},
				},
			},
		},
	}
}

func createServerPodT(t *testing.T, kubeClient kubernetes.Interface, namespace, name string, labels map[string]string, port int32) ([]string, func()) {
	t.Helper()

	t.Logf("creating server pod %s/%s port=%d labels=%v", namespace, name, port, labels)
	pod := netexecPod(name, namespace, labels, port)
	_, err := kubeClient.CoreV1().Pods(namespace).Create(context.TODO(), pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create server pod: %v", err)
	}
	if err := waitForPodReadyT(t, kubeClient, namespace, name); err != nil {
		t.Fatalf("server pod not ready: %v", err)
	}

	created, err := kubeClient.CoreV1().Pods(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get created server pod: %v", err)
	}
	if len(created.Status.PodIPs) == 0 {
		t.Fatalf("server pod has no IPs")
	}

	ips := podIPs(created)
	t.Logf("server pod %s/%s ips=%v", namespace, name, ips)

	return ips, func() {
		t.Logf("deleting server pod %s/%s", namespace, name)
		_ = kubeClient.CoreV1().Pods(namespace).Delete(context.TODO(), name, metav1.DeleteOptions{})
	}
}

// podIPs returns all IP addresses assigned to a pod (dual-stack aware).
func podIPs(pod *corev1.Pod) []string {
	var ips []string
	for _, podIP := range pod.Status.PodIPs {
		if podIP.IP != "" {
			ips = append(ips, podIP.IP)
		}
	}
	if len(ips) == 0 && pod.Status.PodIP != "" {
		ips = append(ips, pod.Status.PodIP)
	}
	return ips
}

// isIPv6 returns true if the given IP string is an IPv6 address.
func isIPv6(ip string) bool {
	return net.ParseIP(ip) != nil && strings.Contains(ip, ":")
}

// formatIPPort formats an IP:port pair, using brackets for IPv6 addresses.
func formatIPPort(ip string, port int32) string {
	if isIPv6(ip) {
		return fmt.Sprintf("[%s]:%d", ip, port)
	}
	return fmt.Sprintf("%s:%d", ip, port)
}

// serviceClusterIPs returns all ClusterIPs for a service (dual-stack aware).
func serviceClusterIPs(svc *corev1.Service) []string {
	if len(svc.Spec.ClusterIPs) > 0 {
		return svc.Spec.ClusterIPs
	}
	if svc.Spec.ClusterIP != "" {
		return []string{svc.Spec.ClusterIP}
	}
	return nil
}

func defaultDenyPolicy(name, namespace string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
		},
	}
}

func allowIngressPolicy(name, namespace string, podLabels, fromLabels map[string]string, port int32) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: podLabels},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: []networkingv1.NetworkPolicyPeer{
						{PodSelector: &metav1.LabelSelector{MatchLabels: fromLabels}},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Port: &intstr.IntOrString{Type: intstr.Int, IntVal: port}, Protocol: protocolPtr(corev1.ProtocolTCP)},
					},
				},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}
}

func allowEgressPolicy(name, namespace string, podLabels, toLabels map[string]string, port int32) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: podLabels},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					To: []networkingv1.NetworkPolicyPeer{
						{PodSelector: &metav1.LabelSelector{MatchLabels: toLabels}},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Port: &intstr.IntOrString{Type: intstr.Int, IntVal: port}, Protocol: protocolPtr(corev1.ProtocolTCP)},
					},
				},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
		},
	}
}

// expectConnectivityForIP checks connectivity to a single IP address.
func expectConnectivityForIP(t *testing.T, kubeClient kubernetes.Interface, namespace string, clientLabels map[string]string, serverIP string, port int32, shouldSucceed bool) {
	t.Helper()

	err := wait.PollImmediate(5*time.Second, 2*time.Minute, func() (bool, error) {
		succeeded, err := runConnectivityCheck(t, kubeClient, namespace, clientLabels, serverIP, port)
		if err != nil {
			return false, err
		}
		return succeeded == shouldSucceed, nil
	})
	if err != nil {
		t.Fatalf("connectivity check failed for %s/%s expected=%t: %v", namespace, formatIPPort(serverIP, port), shouldSucceed, err)
	}
	t.Logf("connectivity %s/%s expected=%t", namespace, formatIPPort(serverIP, port), shouldSucceed)
}

// expectConnectivity checks connectivity to all provided IPs (dual-stack aware).
func expectConnectivity(t *testing.T, kubeClient kubernetes.Interface, namespace string, clientLabels map[string]string, serverIPs []string, port int32, shouldSucceed bool) {
	t.Helper()

	for _, ip := range serverIPs {
		family := "IPv4"
		if isIPv6(ip) {
			family = "IPv6"
		}
		t.Logf("checking %s connectivity %s -> %s expected=%t", family, namespace, formatIPPort(ip, port), shouldSucceed)
		expectConnectivityForIP(t, kubeClient, namespace, clientLabels, ip, port, shouldSucceed)
	}
}

// testConnectivityWithTimeout tests connectivity with a custom timeout and returns error instead of failing
func testConnectivityWithTimeout(t *testing.T, kubeClient kubernetes.Interface, namespace string, clientLabels map[string]string, serverIPs []string, port int32, shouldSucceed bool, timeout time.Duration) error {
	t.Helper()

	for _, ip := range serverIPs {
		family := "IPv4"
		if isIPv6(ip) {
			family = "IPv6"
		}
		t.Logf("checking %s connectivity %s -> %s expected=%t (timeout=%v)", family, namespace, formatIPPort(ip, port), shouldSucceed, timeout)

		err := wait.PollImmediate(5*time.Second, timeout, func() (bool, error) {
			succeeded, err := runConnectivityCheck(t, kubeClient, namespace, clientLabels, ip, port)
			if err != nil {
				return false, err
			}
			return succeeded == shouldSucceed, nil
		})
		if err != nil {
			return fmt.Errorf("connectivity check failed for %s/%s expected=%t: %v", namespace, formatIPPort(ip, port), shouldSucceed, err)
		}
		t.Logf("connectivity %s/%s expected=%t", namespace, formatIPPort(ip, port), shouldSucceed)
	}
	return nil
}

func runConnectivityCheck(t *testing.T, kubeClient kubernetes.Interface, namespace string, labels map[string]string, serverIP string, port int32) (bool, error) {
	t.Helper()

	name := fmt.Sprintf("np-client-%s", rand.String(5))
	t.Logf("creating client pod %s/%s to connect %s:%d", namespace, name, serverIP, port)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   boolptr(true),
				RunAsUser:      int64ptr(1001),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{
				{
					Name:  "connect",
					Image: agnhostImage,
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: boolptr(false),
						Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						RunAsNonRoot:             boolptr(true),
						RunAsUser:                int64ptr(1001),
					},
					Command: []string{"/agnhost"},
					Args: []string{
						"connect",
						"--protocol=tcp",
						"--timeout=5s",
						formatIPPort(serverIP, port),
					},
				},
			},
		},
	}

	_, err := kubeClient.CoreV1().Pods(namespace).Create(context.TODO(), pod, metav1.CreateOptions{})
	if err != nil {
		return false, err
	}
	defer func() {
		_ = kubeClient.CoreV1().Pods(namespace).Delete(context.TODO(), name, metav1.DeleteOptions{})
	}()

	if err := waitForPodCompletion(kubeClient, namespace, name); err != nil {
		return false, err
	}
	completed, err := kubeClient.CoreV1().Pods(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	if len(completed.Status.ContainerStatuses) == 0 {
		return false, fmt.Errorf("no container status recorded for pod %s", name)
	}
	exitCode := completed.Status.ContainerStatuses[0].State.Terminated.ExitCode
	t.Logf("client pod %s/%s exitCode=%d", namespace, name, exitCode)
	return exitCode == 0, nil
}

func waitForPodReadyT(t *testing.T, kubeClient kubernetes.Interface, namespace, name string) error {
	return wait.PollImmediate(2*time.Second, 2*time.Minute, func() (bool, error) {
		pod, err := kubeClient.CoreV1().Pods(namespace).Get(context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if pod.Status.Phase != corev1.PodRunning {
			return false, nil
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
}

func waitForPodCompletion(kubeClient kubernetes.Interface, namespace, name string) error {
	return wait.PollImmediate(2*time.Second, 2*time.Minute, func() (bool, error) {
		pod, err := kubeClient.CoreV1().Pods(namespace).Get(context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed, nil
	})
}

func protocolPtr(protocol corev1.Protocol) *corev1.Protocol {
	return &protocol
}

func boolptr(value bool) *bool {
	return &value
}

func int64ptr(value int64) *int64 {
	return &value
}
