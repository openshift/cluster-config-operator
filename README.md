# OpenShift Cluster Config Operator

The cluster-config-operator manages OpenShift cluster configuration with a minimal set of runtime controllers. It provides:

1. A render command which creates initial bootstrap manifests
2. Controllers for managing specific cluster configuration aspects

**Note:** This repo is not accepting new control loop contributions. It has a constrained scope and should not be expanded with additional logic — jointly owned repos like this end up having no owner to handle migration over time as dependencies move.

## Building

To build the operator and test binary:

```bash
make build
```

This produces:
- `cluster-config-operator` — the operator binary
- `cluster-config-operator-tests-ext` — the test binary compatible with the OpenShift Tests Extension framework

## Controllers

The operator runs the following controllers:

- **Feature Gates Controller** — Manages feature gate configuration and version tracking for the cluster
- **Kube Cloud Config Controller** — Synthesizes cloud provider configuration for Kubernetes components from Infrastructure and user-provided ConfigMaps
- **AWS Platform Service Location Controller** — Configures AWS service endpoints for platform components
- **Platform Status Migration Controller** — Handles migration of platform status fields in Infrastructure
- **Feature Upgradeable Controller** — Controls cluster upgradeability based on feature gate configuration
- **Latency Sensitive Removal Controller** — Manages removal of deprecated latency-sensitive workload class (temporary migration controller)
- **OKD Feature Set Migration Controller** — Handles migration of Default featureset to OKD for OKD builds (temporary migration controller)

## Testing

This repository uses the [OpenShift Tests Extension (OTE)](https://github.com/openshift-eng/openshift-tests-extension) framework.

### Running tests

```bash
# Run all tests in the suite
./cluster-config-operator-tests-ext run-suite openshift/cluster-config-operator/all

# Run a specific test
./cluster-config-operator-tests-ext run-test "test-name"

# Generate JUnit output
./cluster-config-operator-tests-ext run-suite openshift/cluster-config-operator/all --junit-path /tmp/junit.xml
```

### Listing tests

```bash
# List all test suites
./cluster-config-operator-tests-ext list suites

# List tests in a specific suite
./cluster-config-operator-tests-ext list tests --suite=openshift/cluster-config-operator/all
```

For more information, see the [OpenShift Tests Extension documentation](https://github.com/openshift-eng/openshift-tests-extension).

## Dependencies

Dependencies are managed through [Go Modules](https://github.com/golang/go/wiki/Modules).

When updating dependencies:

```bash
go mod tidy
go mod vendor
```

### Key Dependencies

| Repository | Role |
|---|---|
| [openshift/api](https://github.com/openshift/api) | OCP API type definitions including config.openshift.io |
| [openshift/client-go](https://github.com/openshift/client-go) | Typed clients for OCP resources |
| [openshift/library-go](https://github.com/openshift/library-go) | Shared OCP library code and controller framework |
| [openshift/build-machinery-go](https://github.com/openshift/build-machinery-go) | Standard Makefile targets |
| [k8s.io/client-go](https://github.com/kubernetes/client-go) | Kubernetes API client library |

## Security

If you've found a security issue that you'd like to disclose confidentially, please contact Red Hat's Product Security team. Details at https://access.redhat.com/security/team/contact

Do not file security issues as public GitHub issues.

## License

cluster-config-operator is licensed under the [Apache License, Version 2.0](http://www.apache.org/licenses/).
