# cluster-config-operator

This repo is not open for control loop contributions.
This repo only exists to hold the CRD manifests required to bootstrap a cluster, it is not a place to add logic because jointly owned repos
like this one end up having no owner to handle migration over time as dependencies move.

The canonical location for OpenShift cluster configuration. This repo includes:
 1. The source of all CRD manifests for config.openshift.io
 2. A render command which creates the initial CRs for all config.openshift.io resource.

## Tests

This repository is compatible with the [OpenShift Tests Extension (OTE)](https://github.com/openshift-eng/openshift-tests-extension) framework.

### Building the test binary

```bash
make build
```

### Running test suites and tests

```bash
# Run a specific test suite or test
./cluster-config-operator-tests-ext run-suite openshift/cluster-config-operator/all
./cluster-config-operator-tests-ext run-test "test-name"

# Run with JUnit output
./cluster-config-operator-tests-ext run-suite openshift/cluster-config-operator/all --junit-path /tmp/junit.xml
```

### Listing available tests and suites

```bash
# List all test suites
./cluster-config-operator-tests-ext list suites

# List tests in a suite
./cluster-config-operator-tests-ext list tests --suite=openshift/cluster-config-operator/all
```

For more information about the OTE framework, see the [openshift-tests-extension documentation](https://github.com/openshift-eng/openshift-tests-extension).

