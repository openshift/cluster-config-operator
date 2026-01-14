package e2e

import (
	g "github.com/onsi/ginkgo/v2"
	o "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NOTE: Basic ClusterOperator status checks (Available/Degraded/Progressing) are covered by
// origin monitor tests (pkg/monitortests/clusterversionoperator/legacycvomonitortests).
// These tests focus on operator-specific metadata and configuration verification.

var _ = g.Describe("[Operator][Parallel] ClusterOperator Verification", func() {
	var (
		ctx          = testContext()
		configClient configclient.Interface
	)

	g.BeforeEach(func() {
		var err error
		configClient, err = getConfigClient()
		o.Expect(err).NotTo(o.HaveOccurred(), "failed to create config client")
	})

	g.Context("Version Information", func() {
		g.It("should report version information", func() {
			co, err := configClient.ConfigV1().ClusterOperators().Get(ctx, clusterOperatorName, metav1.GetOptions{})
			o.Expect(err).NotTo(o.HaveOccurred())

			g.By("checking operator version is reported")
			operatorVersion := findVersion(co.Status.Versions, "operator")
			o.Expect(operatorVersion).NotTo(o.BeNil(), "operator version should be reported")
			o.Expect(operatorVersion.Version).NotTo(o.BeEmpty(), "operator version should not be empty")

			g.By("checking feature-gates version is reported")
			featureGatesVersion := findVersion(co.Status.Versions, "feature-gates")
			o.Expect(featureGatesVersion).NotTo(o.BeNil(), "feature-gates version should be reported")
			// Note: feature-gates version can be empty string initially
		})
	})

	g.Context("RelatedObjects", func() {
		g.It("should track all required related objects", func() {
			co, err := configClient.ConfigV1().ClusterOperators().Get(ctx, clusterOperatorName, metav1.GetOptions{})
			o.Expect(err).NotTo(o.HaveOccurred())

			g.By("checking operator.openshift.io/configs resource is tracked")
			found := findRelatedObject(co.Status.RelatedObjects, configv1.ObjectReference{
				Group:    "operator.openshift.io",
				Resource: "configs",
				Name:     "cluster",
			})
			o.Expect(found).To(o.BeTrue(), "should track operator.openshift.io/configs/cluster in relatedObjects")

			g.By("checking openshift-config namespace is tracked")
			found = findRelatedObject(co.Status.RelatedObjects, configv1.ObjectReference{
				Resource: "namespaces",
				Name:     "openshift-config",
			})
			o.Expect(found).To(o.BeTrue(), "should track openshift-config namespace in relatedObjects")

			g.By("checking openshift-config-operator namespace is tracked")
			found = findRelatedObject(co.Status.RelatedObjects, configv1.ObjectReference{
				Resource: "namespaces",
				Name:     "openshift-config-operator",
			})
			o.Expect(found).To(o.BeTrue(), "should track openshift-config-operator namespace in relatedObjects")
		})
	})

	g.Context("Required Namespaces", func() {
		g.It("should have all required namespaces", func() {
			k8sClient, err := getKubernetesClient()
			o.Expect(err).NotTo(o.HaveOccurred())

			g.By("checking openshift-config namespace exists")
			ns, err := k8sClient.CoreV1().Namespaces().Get(ctx, "openshift-config", metav1.GetOptions{})
			o.Expect(err).NotTo(o.HaveOccurred(), "openshift-config namespace should exist")
			o.Expect(ns.Name).To(o.Equal("openshift-config"))

			g.By("checking openshift-config-managed namespace exists")
			ns, err = k8sClient.CoreV1().Namespaces().Get(ctx, "openshift-config-managed", metav1.GetOptions{})
			o.Expect(err).NotTo(o.HaveOccurred(), "openshift-config-managed namespace should exist")
			o.Expect(ns.Name).To(o.Equal("openshift-config-managed"))

			g.By("checking openshift-config-operator namespace exists")
			ns, err = k8sClient.CoreV1().Namespaces().Get(ctx, operatorNamespace, metav1.GetOptions{})
			o.Expect(err).NotTo(o.HaveOccurred(), "openshift-config-operator namespace should exist")
			o.Expect(ns.Name).To(o.Equal(operatorNamespace))
		})
	})

	g.Context("Operator Config CR", func() {
		g.It("should exist", func() {
			config, err := getRestConfig()
			o.Expect(err).NotTo(o.HaveOccurred())

			// Use dynamic client to get the operator config CR
			dynamicClient, err := getDynamicClient(config)
			o.Expect(err).NotTo(o.HaveOccurred())

			gvr := operatorv1.GroupVersion.WithResource("configs")
			obj, err := dynamicClient.Resource(gvr).Get(ctx, "cluster", metav1.GetOptions{})
			o.Expect(err).NotTo(o.HaveOccurred(), "operator.openshift.io/configs/cluster CR should exist")
			o.Expect(obj).NotTo(o.BeNil())
			o.Expect(obj.GetName()).To(o.Equal("cluster"))
		})
	})
})

// findCondition finds a condition by type in the conditions slice
func findCondition(conditions []configv1.ClusterOperatorStatusCondition, conditionType configv1.ClusterStatusConditionType) *configv1.ClusterOperatorStatusCondition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

// findVersion finds a version by name in the versions slice
func findVersion(versions []configv1.OperandVersion, name string) *configv1.OperandVersion {
	for i := range versions {
		if versions[i].Name == name {
			return &versions[i]
		}
	}
	return nil
}

// findRelatedObject checks if a related object exists in the relatedObjects slice
func findRelatedObject(relatedObjects []configv1.ObjectReference, target configv1.ObjectReference) bool {
	for _, obj := range relatedObjects {
		if obj.Group == target.Group &&
			obj.Resource == target.Resource &&
			obj.Name == target.Name {
			return true
		}
	}
	return false
}
