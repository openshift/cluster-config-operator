package main

import (
	"testing"

	"github.com/openshift-eng/openshift-tests-extension/pkg/flags"
	configv1 "github.com/openshift/api/config/v1"
)

func TestExternalControlPlaneExcludesExtensionSpecs(t *testing.T) {
	registry, err := prepareOperatorTestsRegistry()
	if err != nil {
		t.Fatalf("prepareOperatorTestsRegistry() returned an error: %v", err)
	}

	extension := registry.Get("openshift:payload:cluster-config-operator")
	if extension == nil {
		t.Fatal("cluster-config-operator extension was not registered")
	}

	specs := extension.GetSpecs()
	if len(specs) == 0 {
		t.Fatal("cluster-config-operator extension registered no specs")
	}

	externalSpecs, err := specs.FilterByEnvironment(flags.EnvironmentalFlags{Topology: string(configv1.ExternalTopologyMode)})
	if err != nil {
		t.Fatalf("FilterByEnvironment() returned an error: %v", err)
	}
	if len(externalSpecs) != 0 {
		t.Fatalf("FilterByEnvironment() selected %d specs for an external control plane, want 0", len(externalSpecs))
	}

	selfManagedSpecs, err := specs.FilterByEnvironment(flags.EnvironmentalFlags{Topology: string(configv1.HighlyAvailableTopologyMode)})
	if err != nil {
		t.Fatalf("FilterByEnvironment() returned an error: %v", err)
	}
	if len(selfManagedSpecs) != len(specs) {
		t.Fatalf("FilterByEnvironment() selected %d of %d specs for a self-managed control plane", len(selfManagedSpecs), len(specs))
	}
}
