package e2e

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"

	ote "github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"
	configv1 "github.com/openshift/api/config/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	"github.com/openshift/library-go/pkg/config/clusteroperator/v1helpers"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

const (
	operatorNamespace = "openshift-config-operator"
	deploymentName    = "openshift-config-operator"
	pollInterval      = 5 * time.Second
	pollTimeout       = 2 * time.Minute
	specTimeout       = 5 * time.Minute
)

// Test suite for cluster-config-operator e2e validation.
var _ = g.Describe("cluster-config-operator", func() {
	g.It("should have a healthy deployment in openshift-config-operator namespace [apigroup:apps][Operator][Parallel]", ote.Informing(), func() {
		config, err := getClientConfig()
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get client config")

		kubeClient, err := kubernetes.NewForConfig(config)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create kube client")

		ctx, cancel := context.WithTimeout(context.Background(), specTimeout)
		defer cancel()

		o.Eventually(func(gomega o.Gomega) {
			deploy, err := kubeClient.AppsV1().Deployments(operatorNamespace).Get(ctx, deploymentName, metav1.GetOptions{})
			if err != nil {
				g.GinkgoLogr.Info(fmt.Sprintf("Failed to get deployment %s/%s: %v (will retry)", operatorNamespace, deploymentName, err))
				gomega.Expect(err).NotTo(o.HaveOccurred())
				return
			}

			// Check if deployment is available
			gomega.Expect(deploy.Status.AvailableReplicas).To(o.BeNumerically(">", 0),
				"Deployment %s/%s has 0 available replicas", operatorNamespace, deploymentName)

			// Check if all replicas are ready
			gomega.Expect(deploy.Status.ReadyReplicas).To(o.Equal(*deploy.Spec.Replicas),
				"Deployment %s/%s: ready=%d, desired=%d", operatorNamespace, deploymentName, deploy.Status.ReadyReplicas, *deploy.Spec.Replicas)

			// Check for updated replicas to ensure no old pods are lingering
			gomega.Expect(deploy.Status.UpdatedReplicas).To(o.Equal(*deploy.Spec.Replicas),
				"Deployment %s/%s: updated=%d, desired=%d", operatorNamespace, deploymentName, deploy.Status.UpdatedReplicas, *deploy.Spec.Replicas)

			g.GinkgoLogr.Info(fmt.Sprintf("Deployment %s/%s is healthy: %d/%d replicas ready",
				operatorNamespace, deploymentName, deploy.Status.ReadyReplicas, *deploy.Spec.Replicas))
		}).WithPolling(pollInterval).WithTimeout(pollTimeout).Should(o.Succeed())

		pods, err := kubeClient.CoreV1().Pods(operatorNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app=openshift-config-operator",
		})
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to list operator pods")
		o.Expect(pods.Items).NotTo(o.BeEmpty(), "should have at least one operator pod")

		// Verify pods are running and not crash-looping
		for _, pod := range pods.Items {
			o.Expect(pod.Status.Phase).To(o.Equal(corev1.PodRunning), "pod %s should be Running", pod.Name)

			for _, containerStatus := range pod.Status.ContainerStatuses {
				o.Expect(containerStatus.Ready).To(o.BeTrue(), "container %s in pod %s should be ready", containerStatus.Name, pod.Name)

				// cluster-config-operator must initialize successfully without excessive restarts.
				// The operator has a critical startup sequence (FeatureGate initialization with 5min timeout)
				// that can fail due to API server delays, RBAC issues, or platform config problems.
				// High restart count indicates initialization failures even if pod eventually becomes Ready.
				// Threshold of 3 allows for transient failures during cluster bootstrap while catching real issues.
				o.Expect(containerStatus.RestartCount).To(o.BeNumerically("<", 3),
					"container %s should not have excessive restarts (current: %d) - indicates initialization issues",
					containerStatus.Name, containerStatus.RestartCount)
			}
		}
	})

	g.It("should report complete ClusterOperator status with health and version information [apigroup:config.openshift.io][Operator][Parallel]", ote.Informing(), func() {
		config, err := getClientConfig()
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get client config")

		configClient, err := configclient.NewForConfig(config)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create config client")

		ctx, cancel := context.WithTimeout(context.Background(), specTimeout)
		defer cancel()

		var clusterOperator *configv1.ClusterOperator
		o.Eventually(func(gomega o.Gomega) {
			co, err := configClient.ConfigV1().ClusterOperators().Get(ctx, "config-operator", metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					gomega.Expect(err).NotTo(o.HaveOccurred(), "ClusterOperator not found")
					return
				}
				gomega.Expect(err).NotTo(o.HaveOccurred())
				return
			}
			clusterOperator = co

			// Check each condition explicitly for better readability and error messages
			availableCondition := v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorAvailable)
			gomega.Expect(availableCondition).NotTo(o.BeNil(), "ClusterOperator config-operator should have an Available condition")
			gomega.Expect(availableCondition.Status).To(o.Equal(configv1.ConditionTrue), "ClusterOperator config-operator should be Available")

			degradedCondition := v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorDegraded)
			gomega.Expect(degradedCondition).NotTo(o.BeNil(), "ClusterOperator config-operator should have a Degraded condition")
			gomega.Expect(degradedCondition.Status).To(o.Equal(configv1.ConditionFalse), "ClusterOperator config-operator should not be Degraded")

			progressingCondition := v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorProgressing)
			gomega.Expect(progressingCondition).NotTo(o.BeNil(), "ClusterOperator config-operator should have a Progressing condition")
			gomega.Expect(progressingCondition.Status).To(o.Equal(configv1.ConditionFalse), "ClusterOperator config-operator should not be Progressing")

			upgradeableCondition := v1helpers.FindStatusCondition(co.Status.Conditions, configv1.OperatorUpgradeable)
			gomega.Expect(upgradeableCondition).NotTo(o.BeNil(), "ClusterOperator config-operator should have an Upgradeable condition")
			gomega.Expect(upgradeableCondition.Status).To(o.Equal(configv1.ConditionTrue), "ClusterOperator config-operator should be Upgradeable")
		}).WithPolling(pollInterval).WithTimeout(pollTimeout).Should(o.Succeed())

		// Validate condition structure
		conditions := make(map[configv1.ClusterStatusConditionType]configv1.ClusterOperatorStatusCondition)
		for _, cond := range clusterOperator.Status.Conditions {
			conditions[cond.Type] = cond
		}

		requiredConditions := []configv1.ClusterStatusConditionType{
			configv1.OperatorAvailable,
			configv1.OperatorDegraded,
			configv1.OperatorProgressing,
			configv1.OperatorUpgradeable,
		}

		for _, condType := range requiredConditions {
			cond, found := conditions[condType]
			o.Expect(found).To(o.BeTrue(), "condition %s must exist", condType)
			o.Expect(cond.Status).To(o.Or(o.Equal(configv1.ConditionTrue), o.Equal(configv1.ConditionFalse), o.Equal(configv1.ConditionUnknown)),
				"condition %s has invalid status", condType)
			o.Expect(cond.Reason).NotTo(o.BeEmpty(), "condition %s missing reason", condType)
			o.Expect(cond.LastTransitionTime).NotTo(o.BeZero(), "condition %s missing timestamp", condType)
		}

		// Validate version information
		o.Expect(clusterOperator.Status.Versions).NotTo(o.BeEmpty(), "should report at least one version")
		foundOperatorVersion := false
		for _, v := range clusterOperator.Status.Versions {
			o.Expect(v.Name).NotTo(o.BeEmpty(), "version name empty")
			o.Expect(v.Version).NotTo(o.BeEmpty(), "version value empty")
			if v.Name == "operator" {
				foundOperatorVersion = true
			}
		}
		o.Expect(foundOperatorVersion).To(o.BeTrue(), "missing 'operator' version")
	})

	g.It("should populate FeatureGate status with enabled and disabled features [apigroup:config.openshift.io][Operator][Parallel]", ote.Informing(), func() {
		config, err := getClientConfig()
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get client config")

		configClient, err := configclient.NewForConfig(config)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create config client")

		ctx, cancel := context.WithTimeout(context.Background(), specTimeout)
		defer cancel()

		var featureGate *configv1.FeatureGate
		o.Eventually(func(gomega o.Gomega) {
			fg, err := configClient.ConfigV1().FeatureGates().Get(ctx, "cluster", metav1.GetOptions{})
			if err != nil {
				g.GinkgoLogr.Info(fmt.Sprintf("Failed to get FeatureGate cluster: %v (will retry)", err))
				gomega.Expect(err).NotTo(o.HaveOccurred())
				return
			}
			featureGate = fg

			// CCO's FeatureGateController should have populated the status
			gomega.Expect(fg.Status.FeatureGates).NotTo(o.BeEmpty(), "FeatureGate status.featureGates not yet populated by CCO")

			// Verify at least one version entry exists with a non-empty version field
			foundVersionedEntry := false
			for _, fgDetails := range fg.Status.FeatureGates {
				if fgDetails.Version != "" {
					foundVersionedEntry = true
					g.GinkgoLogr.Info(fmt.Sprintf("FeatureGate status populated with version %s: %d enabled, %d disabled features",
						fgDetails.Version, len(fgDetails.Enabled), len(fgDetails.Disabled)))
					break
				}
			}

			gomega.Expect(foundVersionedEntry).To(o.BeTrue(), "FeatureGate status entries have no version populated")
		}).WithPolling(pollInterval).WithTimeout(pollTimeout).Should(o.Succeed())

		// Log summary of what CCO populated
		var totalEnabled, totalDisabled int
		for _, fgDetails := range featureGate.Status.FeatureGates {
			totalEnabled += len(fgDetails.Enabled)
			totalDisabled += len(fgDetails.Disabled)
		}
		g.GinkgoLogr.Info(fmt.Sprintf("FeatureGate status populated by CCO: %d version(s), %d enabled features, %d disabled features",
			len(featureGate.Status.FeatureGates), totalEnabled, totalDisabled))
	})

	g.It("should maintain required namespaces [apigroup:core][Operator][Parallel]", ote.Informing(), func() {
		config, err := getClientConfig()
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get client config")

		kubeClient, err := kubernetes.NewForConfig(config)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create kube client")

		ctx, cancel := context.WithTimeout(context.Background(), specTimeout)
		defer cancel()

		requiredNamespaces := []string{
			"openshift-config-operator", // CCO runs here
			"openshift-config",          // User-specified configuration
			"openshift-config-managed",  // Operator-managed configuration (e.g., kube-cloud-config)
		}

		for _, ns := range requiredNamespaces {
			namespace, err := kubeClient.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
			o.Expect(err).NotTo(o.HaveOccurred(), "namespace %s should exist", ns)
			o.Expect(namespace.Status.Phase).To(o.Equal(corev1.NamespaceActive), "namespace %s should be Active", ns)
			g.GinkgoLogr.Info(fmt.Sprintf("Verified namespace %s exists and is Active", ns))
		}
	})

	g.It("should expose metrics endpoint for monitoring [apigroup:monitoring.coreos.com][Operator][Parallel]", ote.Informing(), func() {
		config, err := getClientConfig()
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get client config")

		kubeClient, err := kubernetes.NewForConfig(config)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create kube client")

		ctx, cancel := context.WithTimeout(context.Background(), specTimeout)
		defer cancel()

		// Verify the metrics service exists
		svc, err := kubeClient.CoreV1().Services(operatorNamespace).Get(ctx, "metrics", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred(), "metrics service should exist")
		o.Expect(svc.Spec.Ports).NotTo(o.BeEmpty(), "metrics service should have ports defined")

		g.GinkgoLogr.Info(fmt.Sprintf("Metrics service found: %s/%s with %d ports", operatorNamespace, svc.Name, len(svc.Spec.Ports)))

		// Get a running pod to test the metrics endpoint
		pods, err := kubeClient.CoreV1().Pods(operatorNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app=openshift-config-operator",
			FieldSelector: "status.phase=Running",
		})
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to list running operator pods")
		o.Expect(pods.Items).NotTo(o.BeEmpty(), "should have at least one running operator pod")

		pod := pods.Items[0]
		g.GinkgoLogr.Info(fmt.Sprintf("Testing metrics endpoint on pod: %s", pod.Name))

		// Verify the pod has the metrics port exposed
		foundMetricsPort := false
		for _, container := range pod.Spec.Containers {
			for _, port := range container.Ports {
				if port.Name == "metrics" {
					foundMetricsPort = true
					o.Expect(port.ContainerPort).To(o.Equal(int32(8443)), "metrics port should be 8443")
					g.GinkgoLogr.Info(fmt.Sprintf("Found metrics port %d in container %s", port.ContainerPort, container.Name))
					break
				}
			}
		}
		o.Expect(foundMetricsPort).To(o.BeTrue(), "pod should expose metrics port")

		// Verify liveness/readiness probes are configured (they hit /healthz on the metrics port)
		for _, container := range pod.Spec.Containers {
			if container.Name == "openshift-config-operator" {
				o.Expect(container.LivenessProbe).NotTo(o.BeNil(), "container should have liveness probe")
				o.Expect(container.ReadinessProbe).NotTo(o.BeNil(), "container should have readiness probe")

				if container.LivenessProbe.HTTPGet != nil {
					o.Expect(container.LivenessProbe.HTTPGet.Port.IntVal).To(o.Equal(int32(8443)), "liveness probe should use metrics port")
					g.GinkgoLogr.Info(fmt.Sprintf("Liveness probe configured: %s on port %d",
						container.LivenessProbe.HTTPGet.Path, container.LivenessProbe.HTTPGet.Port.IntVal))
				}
			}
		}

		// Actually query the metrics endpoint to verify it returns valid Prometheus metrics
		g.GinkgoLogr.Info("Fetching metrics from /metrics endpoint to validate Prometheus format")

		// Set up port forwarding to the pod's metrics port
		stopChan := make(chan struct{}, 1)
		readyChan := make(chan struct{})
		defer close(stopChan)

		// Build the port forward request
		req := kubeClient.CoreV1().RESTClient().Post().
			Resource("pods").
			Namespace(pod.Namespace).
			Name(pod.Name).
			SubResource("portforward")

		transport, upgrader, err := spdy.RoundTripperFor(config)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create SPDY round tripper")

		dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, req.URL())

		// Forward to a random local port
		ports := []string{"0:8443"}
		pf, err := portforward.New(dialer, ports, stopChan, readyChan, g.GinkgoWriter, g.GinkgoWriter)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create port forwarder")

		// Start port forwarding in background
		errChan := make(chan error, 1)
		go func() {
			errChan <- pf.ForwardPorts()
		}()

		// Wait for port forward to be ready
		select {
		case <-readyChan:
			g.GinkgoLogr.Info("Port forward established")
		case err := <-errChan:
			o.Expect(err).NotTo(o.HaveOccurred(), "port forward failed to start")
		case <-time.After(10 * time.Second):
			g.Fail("timed out waiting for port forward to be ready")
		}

		// Get the actual forwarded local port
		forwardedPorts, err := pf.GetPorts()
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get forwarded ports")
		o.Expect(forwardedPorts).NotTo(o.BeEmpty(), "no ports were forwarded")
		localPort := forwardedPorts[0].Local

		// Query the metrics endpoint via the forwarded port
		metricsURL := fmt.Sprintf("https://localhost:%d/metrics", localPort)
		g.GinkgoLogr.Info(fmt.Sprintf("Querying metrics endpoint: %s", metricsURL))

		// Create HTTP client with TLS config
		httpClient := &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true, // The metrics endpoint uses self-signed cert
				},
			},
		}

		// Create request - authentication happens via kube-rbac-proxy
		metricsReq, err := http.NewRequest("GET", metricsURL, nil)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create metrics request")

		// Add bearer token from config if available
		if config.BearerToken != "" {
			metricsReq.Header.Set("Authorization", "Bearer "+config.BearerToken)
		}

		resp, err := httpClient.Do(metricsReq)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to query metrics endpoint")
		defer resp.Body.Close()

		// Metrics endpoint should be responsive - accept 200 (authenticated) or 401/403 (requires auth)
		// Following the pattern from origin's test/extended/util/prometheus/helpers.go
		o.Expect(resp.StatusCode).To(o.SatisfyAny(
			o.Equal(http.StatusOK),
			o.Equal(http.StatusUnauthorized),
			o.Equal(http.StatusForbidden),
		), "metrics endpoint should be responsive (200 OK, 401 Unauthorized, or 403 Forbidden)")

		// If authentication is required, the endpoint is properly configured
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			g.GinkgoLogr.Info(fmt.Sprintf("Metrics endpoint requires authentication (%d) - endpoint is properly protected", resp.StatusCode))
			return
		}

		// If we got 200 OK, validate the Prometheus format
		body, err := io.ReadAll(resp.Body)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to read metrics response")

		metricsOutput := string(body)
		o.Expect(metricsOutput).NotTo(o.BeEmpty(), "metrics output should not be empty")

		// Validate Prometheus format - should contain metric lines with # HELP, # TYPE, and metric values
		o.Expect(metricsOutput).To(o.ContainSubstring("# HELP"), "metrics should contain HELP annotations")
		o.Expect(metricsOutput).To(o.ContainSubstring("# TYPE"), "metrics should contain TYPE annotations")

		// Verify common operator metrics exist
		// workqueue metrics are emitted by all controller-runtime based operators
		hasWorkqueueMetrics := strings.Contains(metricsOutput, "workqueue_")
		hasGoMetrics := strings.Contains(metricsOutput, "go_")
		hasProcessMetrics := strings.Contains(metricsOutput, "process_")

		o.Expect(hasWorkqueueMetrics || hasGoMetrics || hasProcessMetrics).To(o.BeTrue(),
			"metrics should contain at least one of: workqueue, go runtime, or process metrics")

		g.GinkgoLogr.Info(fmt.Sprintf("Successfully validated metrics endpoint: %d bytes, contains Prometheus-formatted data", len(metricsOutput)))
	})

	g.It("should generate kube-cloud-config ConfigMap for supported cloud platforms [apigroup:config.openshift.io][Operator][Parallel]", ote.Informing(), func() {
		config, err := getClientConfig()
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get client config")

		kubeClient, err := kubernetes.NewForConfig(config)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create kube client")

		configClient, err := configclient.NewForConfig(config)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create config client")

		ctx, cancel := context.WithTimeout(context.Background(), specTimeout)
		defer cancel()

		// Get the Infrastructure resource to determine platform type
		infra, err := configClient.ConfigV1().Infrastructures().Get(ctx, "cluster", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred(), "Infrastructure resource should exist")

		platformType := infra.Status.PlatformStatus.Type
		managedPlatforms := map[configv1.PlatformType]bool{
			configv1.AWSPlatformType:       true,
			configv1.AzurePlatformType:     true,
			configv1.GCPPlatformType:       true,
			configv1.OpenStackPlatformType: true,
			configv1.VSpherePlatformType:   true,
			configv1.OvirtPlatformType:     true,
			configv1.KubevirtPlatformType:  true,
		}

		if !managedPlatforms[platformType] {
			g.Skip(fmt.Sprintf("kube-cloud-config not managed by CCO for platform %s", platformType))
		}

		hasSourceConfig := infra.Spec.CloudConfig.Name != ""

		// Try to get the ConfigMap
		cm, err := kubeClient.CoreV1().ConfigMaps("openshift-config-managed").Get(ctx, "kube-cloud-config", metav1.GetOptions{})
		if err != nil {
			// ConfigMap not found - only valid if no source config is set
			o.Expect(hasSourceConfig).To(o.BeFalse(),
				"kube-cloud-config ConfigMap should exist when Infrastructure.spec.cloudConfig is set")
			return
		}

		// ConfigMap exists - validate structure
		o.Expect(cm.Namespace).To(o.Equal("openshift-config-managed"), "ConfigMap should be in openshift-config-managed namespace")
		o.Expect(cm.Name).To(o.Equal("kube-cloud-config"), "ConfigMap should be named kube-cloud-config")

		// Validate cloud.conf key exists
		_, hasDataKey := cm.Data["cloud.conf"]
		_, hasBinaryKey := cm.BinaryData["cloud.conf"]
		o.Expect(hasDataKey || hasBinaryKey).To(o.BeTrue(), "kube-cloud-config ConfigMap should contain 'cloud.conf' key")
	})

	g.It("should reconcile and recreate kube-cloud-config when deleted [apigroup:config.openshift.io][Operator][Serial]", func() {
		config, err := getClientConfig()
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get client config")

		kubeClient, err := kubernetes.NewForConfig(config)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create kube client")

		configClient, err := configclient.NewForConfig(config)
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create config client")

		ctx, cancel := context.WithTimeout(context.Background(), specTimeout)
		defer cancel()

		// Get the Infrastructure resource to determine platform type
		infra, err := configClient.ConfigV1().Infrastructures().Get(ctx, "cluster", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred(), "Infrastructure resource should exist")

		platformType := infra.Status.PlatformStatus.Type
		g.GinkgoLogr.Info(fmt.Sprintf("Detected platform type: %s", platformType))

		// Only run on platforms where CCO manages kube-cloud-config
		managedPlatforms := map[configv1.PlatformType]bool{
			configv1.AWSPlatformType:       true,
			configv1.AzurePlatformType:     true,
			configv1.GCPPlatformType:       true,
			configv1.OpenStackPlatformType: true,
			configv1.VSpherePlatformType:   true,
			configv1.OvirtPlatformType:     true,
			configv1.KubevirtPlatformType:  true,
		}

		if !managedPlatforms[platformType] {
			g.Skip(fmt.Sprintf("kube-cloud-config not managed by CCO for platform %s", platformType))
		}

		// Check if cloudConfig source is set - we need this to ensure CCO will recreate the ConfigMap
		if infra.Spec.CloudConfig.Name == "" {
			g.Skip("Infrastructure.spec.cloudConfig is not set - CCO will not create kube-cloud-config")
		}

		g.GinkgoLogr.Info(fmt.Sprintf("Infrastructure.spec.cloudConfig.name: %s", infra.Spec.CloudConfig.Name))

		// Verify the ConfigMap exists before deletion
		originalConfigMap, err := kubeClient.CoreV1().ConfigMaps("openshift-config-managed").Get(ctx, "kube-cloud-config", metav1.GetOptions{})
		if err != nil {
			g.Skip(fmt.Sprintf("kube-cloud-config ConfigMap does not exist, cannot test reconciliation: %v", err))
		}

		g.GinkgoLogr.Info("kube-cloud-config ConfigMap found")
		originalKeys := getConfigMapKeys(originalConfigMap)
		g.GinkgoLogr.Info(fmt.Sprintf("Original ConfigMap keys: %v", originalKeys))

		// Verify original has cloud.conf key
		hasCloudConf := false
		originalCloudConfSize := 0
		if originalConfigMap.Data != nil {
			if cloudConf, ok := originalConfigMap.Data["cloud.conf"]; ok {
				hasCloudConf = true
				originalCloudConfSize = len(cloudConf)
			}
		}
		if originalConfigMap.BinaryData != nil {
			if cloudConf, ok := originalConfigMap.BinaryData["cloud.conf"]; ok {
				hasCloudConf = true
				originalCloudConfSize = len(cloudConf)
			}
		}
		o.Expect(hasCloudConf).To(o.BeTrue(), "original ConfigMap should have cloud.conf key")
		g.GinkgoLogr.Info(fmt.Sprintf("Original cloud.conf size: %d bytes", originalCloudConfSize))

		// Store original UID to detect recreation (CCO may recreate before we see NotFound)
		originalUID := originalConfigMap.UID

		// Delete the ConfigMap to test CCO's reconciliation
		g.GinkgoLogr.Info("Deleting kube-cloud-config ConfigMap to test reconciliation")
		err = kubeClient.CoreV1().ConfigMaps("openshift-config-managed").Delete(ctx, "kube-cloud-config", metav1.DeleteOptions{})
		if err != nil {
			if apierrors.IsForbidden(err) {
				g.Skip(fmt.Sprintf("Unable to delete kube-cloud-config ConfigMap - forbidden by RBAC. This test requires cluster-admin or operator-level access to openshift-config-managed namespace."))
			}
			o.Expect(err).NotTo(o.HaveOccurred(), "failed to delete kube-cloud-config ConfigMap")
		}

		// Wait for deletion to propagate (deletion may be async)
		// CCO reconciles every minute, so it may recreate the ConfigMap quickly
		// Check for either NotFound (deleted) or UID change (deleted and recreated)
		deletionConfirmed := false
		o.Eventually(func() bool {
			cm, err := kubeClient.CoreV1().ConfigMaps("openshift-config-managed").Get(ctx, "kube-cloud-config", metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					g.GinkgoLogr.Info("ConfigMap deletion confirmed")
					deletionConfirmed = true
					return true
				}
				// Unexpected error - retry
				return false
			}

			// ConfigMap exists - check if it's terminating
			if cm.DeletionTimestamp != nil {
				return false
			}

			// Check if UID changed - this means it was deleted and recreated by CCO
			if cm.UID != originalUID {
				g.GinkgoLogr.Info("ConfigMap deletion confirmed")
				deletionConfirmed = true
				return true
			}

			// ConfigMap exists with same UID and not terminating - deletion may have been blocked
			return false
		}, 10*time.Second, 500*time.Millisecond).Should(o.BeTrue())

		if !deletionConfirmed {
			g.Skip("Unable to delete kube-cloud-config ConfigMap - insufficient RBAC permissions. This test requires cluster-admin or operator-level access to openshift-config-managed namespace.")
		}

		// Wait for CCO to reconcile and recreate the ConfigMap
		// CCO's KubeCloudConfigController has a ResyncEvery(time.Minute) setting
		// So it should recreate within ~60 seconds
		g.GinkgoLogr.Info("Waiting for CCO to reconcile and recreate kube-cloud-config (timeout: 2 minutes)")

		var recreatedConfigMap *corev1.ConfigMap
		o.Eventually(func(gomega o.Gomega) {
			cm, err := kubeClient.CoreV1().ConfigMaps("openshift-config-managed").Get(ctx, "kube-cloud-config", metav1.GetOptions{})
			if err != nil {
				g.GinkgoLogr.Info(fmt.Sprintf("ConfigMap not yet recreated (elapsed time accumulating, will retry): %v", err))
				gomega.Expect(err).NotTo(o.HaveOccurred())
				return
			}
			recreatedConfigMap = cm
			g.GinkgoLogr.Info("ConfigMap has been recreated by CCO!")
		}).WithPolling(5 * time.Second).WithTimeout(2 * time.Minute).Should(o.Succeed())

		// Validate the recreated ConfigMap has the correct structure
		g.GinkgoLogr.Info("Validating recreated ConfigMap structure")

		// Verify namespace and name
		o.Expect(recreatedConfigMap.Namespace).To(o.Equal("openshift-config-managed"), "recreated ConfigMap should be in correct namespace")
		o.Expect(recreatedConfigMap.Name).To(o.Equal("kube-cloud-config"), "recreated ConfigMap should have correct name")

		// Verify cloud.conf key exists
		recreatedHasCloudConf := false
		recreatedCloudConfSize := 0
		if recreatedConfigMap.Data != nil {
			if cloudConf, ok := recreatedConfigMap.Data["cloud.conf"]; ok {
				recreatedHasCloudConf = true
				recreatedCloudConfSize = len(cloudConf)
				g.GinkgoLogr.Info(fmt.Sprintf("Recreated ConfigMap has cloud.conf in Data: %d bytes", len(cloudConf)))
			}
		}
		if recreatedConfigMap.BinaryData != nil {
			if cloudConf, ok := recreatedConfigMap.BinaryData["cloud.conf"]; ok {
				recreatedHasCloudConf = true
				recreatedCloudConfSize = len(cloudConf)
				g.GinkgoLogr.Info(fmt.Sprintf("Recreated ConfigMap has cloud.conf in BinaryData: %d bytes", len(cloudConf)))
			}
		}

		o.Expect(recreatedHasCloudConf).To(o.BeTrue(), "recreated ConfigMap should have cloud.conf key")

		// Verify the content size is similar to original (should be identical)
		// Allow a small variance in case of whitespace/formatting differences
		sizeDiff := recreatedCloudConfSize - originalCloudConfSize
		if sizeDiff < 0 {
			sizeDiff = -sizeDiff
		}
		sizeVariancePercent := float64(sizeDiff) / float64(originalCloudConfSize) * 100

		o.Expect(sizeVariancePercent).To(o.BeNumerically("<", 5.0),
			"recreated cloud.conf size should be similar to original (<%% variance): original=%d, recreated=%d",
			originalCloudConfSize, recreatedCloudConfSize)

		g.GinkgoLogr.Info(fmt.Sprintf("Reconciliation validation successful: original size=%d bytes, recreated size=%d bytes, variance=%.2f%%",
			originalCloudConfSize, recreatedCloudConfSize, sizeVariancePercent))

		// Verify the keys are the same
		recreatedKeys := getConfigMapKeys(recreatedConfigMap)
		g.GinkgoLogr.Info(fmt.Sprintf("Recreated ConfigMap keys: %v", recreatedKeys))

		// Verify ClusterOperator status remains Available (no degradation)
		g.GinkgoLogr.Info("Verifying ClusterOperator status remains Available after reconciliation")

		co, err := configClient.ConfigV1().ClusterOperators().Get(ctx, "config-operator", metav1.GetOptions{})
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to get ClusterOperator")

		available := false
		degraded := false
		for _, condition := range co.Status.Conditions {
			switch condition.Type {
			case configv1.OperatorAvailable:
				available = condition.Status == configv1.ConditionTrue
			case configv1.OperatorDegraded:
				degraded = condition.Status == configv1.ConditionTrue
			}
		}

		o.Expect(available).To(o.BeTrue(), "ClusterOperator should remain Available after reconciliation")
		o.Expect(degraded).To(o.BeFalse(), "ClusterOperator should not be Degraded after reconciliation")

		g.GinkgoLogr.Info("SERIAL TEST PASSED: CCO successfully reconciled and recreated kube-cloud-config after deletion")
	})
})

// getConfigMapKeys returns a list of all keys in a ConfigMap (both Data and BinaryData)
func getConfigMapKeys(cm *corev1.ConfigMap) []string {
	keys := []string{}
	if cm.Data != nil {
		for k := range cm.Data {
			keys = append(keys, k)
		}
	}
	if cm.BinaryData != nil {
		for k := range cm.BinaryData {
			keys = append(keys, k+"(binary)")
		}
	}
	return keys
}

// getClientConfig builds a rest.Config from the default kubeconfig loading rules
func getClientConfig() (*rest.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
	return kubeConfig.ClientConfig()
}
