package operator

import configv1 "github.com/openshift/api/config/v1"

// relatedObjects must stay in sync with the ClusterOperator manifest.
func relatedObjects() []configv1.ObjectReference {
	return []configv1.ObjectReference{
		{Group: "operator.openshift.io", Resource: "configs", Name: "cluster"},
		{Group: "", Resource: "namespaces", Name: "openshift-config"},
		{Group: "", Resource: "namespaces", Name: "openshift-config-operator"},
		{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: "system:openshift:operator:openshift-config-operator"},
	}
}
