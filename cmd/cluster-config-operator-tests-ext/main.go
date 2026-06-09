// Package main provides the OpenShift Test Extension (OTE) CLI binary for cluster-config-operator.
//
// DEPLOYMENT MODEL:
// Once this binary is statically compiled (CGO_ENABLED=0), compressed, and included in the
// component Dockerfile, it will be registered in openshift/origin's test extension registry.
// The tests will then automatically execute through origin's orchestration infrastructure in
// matching existing CI jobs WITHOUT requiring standalone job configurations in openshift/release.
// For further information, please refer to the documentation at:
// https://github.com/openshift-eng/openshift-tests-extension/blob/main/cmd/example-tests/main.go

package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	otecmd "github.com/openshift-eng/openshift-tests-extension/pkg/cmd"
	oteextension "github.com/openshift-eng/openshift-tests-extension/pkg/extension"
	oteginkgo "github.com/openshift-eng/openshift-tests-extension/pkg/ginkgo"

	"k8s.io/klog/v2"

	// Import the test package to register Ginkgo test suites
	_ "github.com/openshift/cluster-config-operator/test/e2e"
)

func main() {
	cmd, err := newOperatorTestCommand()
	if err != nil {
		klog.Fatal(err)
	}
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func newOperatorTestCommand() (*cobra.Command, error) {
	registry, err := prepareOperatorTestsRegistry()
	if err != nil {
		return nil, err
	}

	cmd := &cobra.Command{
		Use:   "cluster-config-operator-tests-ext",
		Short: "A binary used to run cluster-config-operator tests as part of OTE.",
		Long:  "Cluster Config Operator Tests Extension",
		Run: func(cmd *cobra.Command, args []string) {
			if err := cmd.Help(); err != nil {
				klog.Fatal(err)
			}
		},
	}

	cmd.AddCommand(otecmd.DefaultExtensionCommands(registry)...)
	return cmd, nil
}

func prepareOperatorTestsRegistry() (*oteextension.Registry, error) {
	registry := oteextension.NewRegistry()
	extension := oteextension.NewExtension("openshift", "payload", "cluster-config-operator")

	// parallel suite runs non-serial, non-disruptive tests concurrently with parallelism of 4.
	extension.AddSuite(oteextension.Suite{
		Name:        "openshift/cluster-config-operator/operator/parallel",
		Parallelism: 4,
		Qualifiers: []string{
			`!name.contains("[Serial]") && !name.contains("[Disruptive]")`,
		},
	})

	// <Place-holder> serial suite runs serial or disruptive tests one at a time, may impact cluster stability.
	extension.AddSuite(oteextension.Suite{
		Name:             "openshift/cluster-config-operator/operator/serial",
		Parallelism:      1,
		ClusterStability: oteextension.ClusterStabilityDisruptive,
		Qualifiers: []string{
			`name.contains("[Serial]") || name.contains("[Disruptive]")`,
		},
	})

	specs, err := oteginkgo.BuildExtensionTestSpecsFromOpenShiftGinkgoSuite()
	if err != nil {
		return nil, fmt.Errorf("couldn't build extension test specs from ginkgo: %w", err)
	}

	extension.AddSpecs(specs)
	registry.Register(extension)
	return registry, nil
}
