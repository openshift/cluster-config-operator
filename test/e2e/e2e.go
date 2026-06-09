package e2e

import (
	"context"
	"fmt"
	"time"

	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"

	configclient "github.com/openshift/client-go/config/clientset/versioned"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	infrastructureResourceName = "cluster"
	pollInterval               = 5 * time.Second
	pollTimeout                = 2 * time.Minute
)

// Test suite for cluster-config-operator configuration validation
var _ = g.Describe("cluster-config-operator", func() {
	g.It("should establish config.openshift.io CRDs as healthy and serving [apigroup:config.openshift.io][Operator]", func() {
		testConfigCRDsEstablished()
	})

	g.It("should render cluster-wide Infrastructure configuration [apigroup:config.openshift.io][Operator]", func() {
		testInfrastructureConfiguration()
	})
})

// testConfigCRDsEstablished verifies that core config.openshift.io CRDs are established and serving.
// cluster-config-operator is responsible for applying these CRDs during cluster bootstrap.
// This test validates that the API server recognizes and serves these critical API definitions.
func testConfigCRDsEstablished() {
	config, err := getClientConfig()
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to get client config")

	apiextClient, err := apiextensionsclient.NewForConfig(config)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create apiextensions client")

	// Core config.openshift.io CRDs that cluster-config-operator establishes
	coreCRDs := []string{
		"infrastructures.config.openshift.io",
		"featuregates.config.openshift.io",
		"schedulers.config.openshift.io",
		"networks.config.openshift.io",
		"clusterversions.config.openshift.io",
	}

	ctx := context.TODO()
	for _, crdName := range coreCRDs {
		err = wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
			crd, getErr := apiextClient.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crdName, metav1.GetOptions{})
			if getErr != nil {
				g.GinkgoLogr.Info(fmt.Sprintf("Failed to get CRD %s: %v (will retry)", crdName, getErr))
				return false, nil
			}

			// Check if CRD is Established
			established := false
			for _, condition := range crd.Status.Conditions {
				if condition.Type == apiextensionsv1.Established && condition.Status == apiextensionsv1.ConditionTrue {
					established = true
					break
				}
			}

			if !established {
				g.GinkgoLogr.Info(fmt.Sprintf("CRD %s not yet Established (will retry)", crdName))
				return false, nil
			}

			g.GinkgoLogr.Info(fmt.Sprintf("CRD %s is Established and serving", crdName))
			return true, nil
		})

		o.Expect(err).NotTo(o.HaveOccurred(), "timed out waiting for CRD %s to be Established", crdName)
	}
}

// testInfrastructureConfiguration verifies that the cluster-config-operator has successfully
// rendered the cluster-wide Infrastructure configuration object named "cluster".
// This resource is created and maintained by cluster-config-operator during bootstrap and
// contains critical platform metadata such as platform type, API server URLs, and topology.
func testInfrastructureConfiguration() {
	config, err := getClientConfig()
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to get client config")

	configClient, err := configclient.NewForConfig(config)
	o.Expect(err).NotTo(o.HaveOccurred(), "failed to create config client")

	// Poll until the Infrastructure resource is available and valid
	err = wait.PollUntilContextTimeout(context.TODO(), pollInterval, pollTimeout, true, func(ctx context.Context) (bool, error) {
		infra, getErr := configClient.ConfigV1().Infrastructures().Get(ctx, infrastructureResourceName, metav1.GetOptions{})
		if getErr != nil {
			g.GinkgoLogr.Info(fmt.Sprintf("Failed to get Infrastructure resource %s: %v (will retry)", infrastructureResourceName, getErr))
			return false, nil
		}

		// Verify that the Infrastructure resource has required fields populated
		if infra.Status.PlatformStatus == nil {
			g.GinkgoLogr.Info(fmt.Sprintf("Infrastructure %s has nil PlatformStatus (will retry)", infrastructureResourceName))
			return false, nil
		}

		if infra.Status.PlatformStatus.Type == "" {
			g.GinkgoLogr.Info(fmt.Sprintf("Infrastructure %s has empty platform type (will retry)", infrastructureResourceName))
			return false, nil
		}

		g.GinkgoLogr.Info(fmt.Sprintf("Infrastructure %s is valid: platform=%s", infrastructureResourceName, infra.Status.PlatformStatus.Type))
		return true, nil
	})

	o.Expect(err).NotTo(o.HaveOccurred(), "timed out waiting for Infrastructure resource to be valid")
}

// getClientConfig builds a rest.Config from the default kubeconfig loading rules
func getClientConfig() (*rest.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
	return kubeConfig.ClientConfig()
}
