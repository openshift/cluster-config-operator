package operator

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"sigs.k8s.io/yaml"
)

func TestRelatedObjectsIncludesClusterRoleBinding(t *testing.T) {
	want := configv1.ObjectReference{
		Group:    "rbac.authorization.k8s.io",
		Resource: "clusterrolebindings",
		Name:     "system:openshift:operator:openshift-config-operator",
	}
	count := 0
	for _, ref := range relatedObjects() {
		if reflect.DeepEqual(ref, want) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected binding exactly once, got %d", count)
	}
}

func TestRelatedObjectsMatchManifest(t *testing.T) {
	path := filepath.Join("..", "..", "manifests",
		"0000_10_config-operator_08_clusteroperator.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var co configv1.ClusterOperator
	if err := yaml.Unmarshal(data, &co); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(relatedObjects(), co.Status.RelatedObjects) {
		t.Fatalf("Go and manifest relatedObjects differ:\nGo: %#v\nYAML: %#v",
			relatedObjects(), co.Status.RelatedObjects)
	}
}
