package e2e

import (
	"fmt"
	"os"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// getKubeConfig returns the Kubernetes client configuration.
// It honors KUBECONFIG when set and otherwise uses the default client-go loading rules.
func getKubeConfig() (*restclient.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig := os.Getenv("KUBECONFIG"); kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
	return kubeConfig.ClientConfig()
}

// logNetworkPolicyDetails logs detailed information about a NetworkPolicy.
func logNetworkPolicyDetails(t interface{ Logf(string, ...interface{}) }, label string, policy *networkingv1.NetworkPolicy) {
	t.Logf("networkpolicy %s details:", label)
	t.Logf("  podSelector=%v policyTypes=%v", policy.Spec.PodSelector.MatchLabels, policy.Spec.PolicyTypes)
	for i, rule := range policy.Spec.Ingress {
		t.Logf("  ingress[%d]: ports=%s from=%s", i, formatPorts(rule.Ports), formatPeers(rule.From))
	}
	for i, rule := range policy.Spec.Egress {
		t.Logf("  egress[%d]: ports=%s to=%s", i, formatPorts(rule.Ports), formatPeers(rule.To))
	}
}

// formatPorts formats NetworkPolicy ports for logging.
func formatPorts(ports []networkingv1.NetworkPolicyPort) string {
	if len(ports) == 0 {
		return "[]"
	}
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		proto := "TCP"
		if p.Protocol != nil {
			proto = string(*p.Protocol)
		}
		if p.Port == nil {
			out = append(out, fmt.Sprintf("%s:any", proto))
			continue
		}
		out = append(out, fmt.Sprintf("%s:%s", proto, p.Port.String()))
	}
	return fmt.Sprintf("[%s]", joinStrings(out))
}

// formatPeers formats NetworkPolicy peers for logging.
func formatPeers(peers []networkingv1.NetworkPolicyPeer) string {
	if len(peers) == 0 {
		return "[]"
	}
	out := make([]string, 0, len(peers))
	for _, peer := range peers {
		if peer.IPBlock != nil {
			out = append(out, fmt.Sprintf("ipBlock=%s except=%v", peer.IPBlock.CIDR, peer.IPBlock.Except))
			continue
		}
		ns := formatSelector(peer.NamespaceSelector)
		pod := formatSelector(peer.PodSelector)
		if ns == "" && pod == "" {
			out = append(out, "{}")
			continue
		}
		out = append(out, fmt.Sprintf("ns=%s pod=%s", ns, pod))
	}
	return fmt.Sprintf("[%s]", joinStrings(out))
}

// formatSelector formats a label selector for logging.
func formatSelector(sel *metav1.LabelSelector) string {
	if sel == nil {
		return ""
	}
	if len(sel.MatchLabels) == 0 && len(sel.MatchExpressions) == 0 {
		return "{}"
	}
	return fmt.Sprintf("labels=%v exprs=%v", sel.MatchLabels, sel.MatchExpressions)
}

// joinStrings joins a slice of strings with commas.
func joinStrings(items []string) string {
	if len(items) == 0 {
		return ""
	}
	out := items[0]
	for i := 1; i < len(items); i++ {
		out += ", " + items[i]
	}
	return out
}
