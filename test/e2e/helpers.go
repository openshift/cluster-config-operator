package e2e

import (
	"context"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	operatorNamespace   = "openshift-config-operator"
	operatorName        = "openshift-config-operator"
	clusterOperatorName = "config-operator"
	pollTimeout         = 2 * time.Minute
	pollInterval        = 5 * time.Second
)

// getKubernetesClient returns a Kubernetes client for interacting with the cluster.
func getKubernetesClient() (kubernetes.Interface, error) {
	config, err := getRestConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

// getRestConfig returns a REST config for the cluster.
func getRestConfig() (*rest.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
	return kubeConfig.ClientConfig()
}

// testContext returns a context for test operations.
func testContext() context.Context {
	return context.Background()
}

// int64Ptr returns a pointer to an int64 value.
func int64Ptr(i int64) *int64 {
	return &i
}
