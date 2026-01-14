package main

// This file imports test packages to ensure they are registered with the OTE framework.
// The blank import causes the test's init() functions to run, which registers Ginkgo specs.

import (
	_ "github.com/openshift/cluster-config-operator/test/e2e"
)
