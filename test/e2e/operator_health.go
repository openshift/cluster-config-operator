package e2e

import (
	"bufio"
	"fmt"
	"strings"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = g.Describe("[Operator][Serial] Operator Health", func() {
	var (
		ctx = testContext()
	)

	g.Context("Deployment Verification", func() {
		g.It("should have a running deployment with ready replicas", func() {
			k8sClient, err := getKubernetesClient()
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to create kubernetes client")

			o.Eventually(func() error {
				deployment, err := k8sClient.AppsV1().Deployments(operatorNamespace).Get(ctx, operatorName, metav1.GetOptions{})
				if err != nil {
					return fmt.Errorf("failed to get deployment: %w", err)
				}

				if deployment.Status.ReadyReplicas < 1 {
					return fmt.Errorf("deployment has %d ready replicas, expected at least 1", deployment.Status.ReadyReplicas)
				}

				return nil
			}, pollTimeout, pollInterval).Should(o.Succeed())
		})
	})

	g.Context("Pod Health", func() {
		g.It("should have running pods without fatal errors in logs", func() {
			k8sClient, err := getKubernetesClient()
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to create kubernetes client")

			// First, verify pods are running
			var pods *corev1.PodList
			o.Eventually(func() error {
				podList, err := k8sClient.CoreV1().Pods(operatorNamespace).List(ctx, metav1.ListOptions{
					LabelSelector: "app=openshift-config-operator",
				})
				if err != nil {
					return fmt.Errorf("failed to list pods: %w", err)
				}

				if len(podList.Items) == 0 {
					return fmt.Errorf("no pods found with label app=openshift-config-operator")
				}

				for _, pod := range podList.Items {
					if pod.Status.Phase != corev1.PodRunning {
						return fmt.Errorf("pod %s is in phase %s, expected Running", pod.Name, pod.Status.Phase)
					}
				}

				pods = podList
				return nil
			}, pollTimeout, pollInterval).Should(o.Succeed())

			// Check pod logs for fatal errors
			for _, pod := range pods.Items {
				podLogs, err := k8sClient.CoreV1().Pods(operatorNamespace).GetLogs(pod.Name, &corev1.PodLogOptions{
					Container: operatorName,
					TailLines: int64Ptr(100),
				}).DoRaw(ctx)
				o.Expect(err).NotTo(o.HaveOccurred(), "failed to get logs for pod %s", pod.Name)

				// Check for fatal errors in logs
				scanner := bufio.NewScanner(strings.NewReader(string(podLogs)))
				for scanner.Scan() {
					line := scanner.Text()
					if strings.Contains(strings.ToLower(line), "fatal") ||
						strings.Contains(strings.ToLower(line), "panic") {
						g.Fail(fmt.Sprintf("found fatal error in pod %s logs: %s", pod.Name, line))
					}
				}
			}
		})
	})
})
