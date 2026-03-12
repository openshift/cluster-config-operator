# NetworkPolicy E2E Tests for cluster-config-operator

This directory contains end-to-end tests for verifying NetworkPolicy enforcement in OpenShift config-related namespaces.

## Overview

The tests verify NetworkPolicy configuration and enforcement for:
- `openshift-config-operator` - The cluster-config-operator namespace
- `openshift-config` - Configuration storage namespace
- `openshift-config-managed` - Managed configuration namespace

## Test Files

- **network_policy_enforcement_test.go** - Main test file containing all NetworkPolicy tests
- **network_policy_utils.go** - Shared utility functions for NetworkPolicy testing
- **main_test.go** - Test suite entry point

## Available Tests

### 1. TestGenericNetworkPolicyEnforcement
Tests basic NetworkPolicy enforcement behavior:
- Default allow-all (no policies)
- Default deny-all policy
- Ingress-only allow
- Combined ingress + egress allow

### 2. TestConfigOperatorNetworkPolicyEnforcement
Tests NetworkPolicy enforcement in the `openshift-config-operator` namespace:
- Verifies NetworkPolicies exist (`config-operator-networkpolicy`, `default-deny-all`)
- Tests allowed port 8443 ingress to operator pods
- Tests denied ports (not in NetworkPolicy)
- Verifies operator egress to DNS (port 5353)

### 3. TestConfigNamespaceNetworkPolicies
Discovery test that examines all three config namespaces:
- Lists all NetworkPolicies in each namespace
- Shows detailed policy information (pod selectors, ingress/egress rules)
- Lists pods running in each namespace
- Useful for understanding the current state

### 4. TestConfigNamespacesNetworkPolicyEnforcement
Comprehensive enforcement test for all three config namespaces:
- Validates NetworkPolicies exist
- For namespaces with running pods, verifies pods remain healthy
- Ensures NetworkPolicies don't block legitimate traffic

## Running the Tests

### Prerequisites
- A running OpenShift cluster
- Kubeconfig file at `/home/yinzhou/kubeconfig`
- NetworkPolicies deployed in the target namespaces
- Go 1.19+ installed

### Method 1: Using `go test` (Recommended)

The simplest way to run the tests:

```bash
cd /home/yinzhou/repos/cluster-config-operator

# Run all NetworkPolicy tests
go test -v ./test/e2e -run 'Test.*NetworkPolicy.*' -timeout 30m

# Run a specific test
go test -v ./test/e2e -run TestConfigNamespaceNetworkPolicies -timeout 30m

# Run with more verbosity
go test -v ./test/e2e -run TestConfigNamespacesNetworkPolicyEnforcement -timeout 30m
```

### Method 2: Using the Convenience Script

```bash
cd /home/yinzhou/repos/cluster-config-operator

# Run all NetworkPolicy tests
./test/e2e/run-tests.sh

# Run a specific test
./test/e2e/run-tests.sh TestConfigNamespaceNetworkPolicies
```

### Method 3: Using Compiled Test Binary

If you prefer to compile once and run multiple times:

```bash
cd /home/yinzhou/repos/cluster-config-operator

# Build the test binary
go test -c ./test/e2e -o cluster-config-operator-tests

# Run all NetworkPolicy tests
./cluster-config-operator-tests -test.run 'Test.*NetworkPolicy.*' -test.v

# Run individual tests
./cluster-config-operator-tests -test.run TestConfigNamespaceNetworkPolicies -test.v
```

### Individual Test Examples with `go test`

```bash
# Discovery test - see what NetworkPolicies exist
go test -v ./test/e2e -run TestConfigNamespaceNetworkPolicies

# Enforcement test - verify policies work correctly
go test -v ./test/e2e -run TestConfigNamespacesNetworkPolicyEnforcement

# Config operator specific tests
go test -v ./test/e2e -run TestConfigOperatorNetworkPolicyEnforcement

# Generic enforcement behavior
go test -v ./test/e2e -run TestGenericNetworkPolicyEnforcement

# Run all tests (including non-NetworkPolicy tests)
go test -v ./test/e2e
```

## Customizing the Kubeconfig Path

The tests use a hardcoded kubeconfig path. To change it, edit `test/e2e/network_policy_utils.go`:

```go
func getKubeConfig() (*restclient.Config, error) {
    loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
    loadingRules.ExplicitPath = "/path/to/your/kubeconfig"  // <-- Change this
    // ...
}
```

## Test Architecture

The tests follow this pattern:

1. **Setup**: Create Kubernetes client using kubeconfig
2. **Discovery**: List NetworkPolicies and pods in target namespaces
3. **Validation**: Verify NetworkPolicy specifications
4. **Enforcement**: Test connectivity to ensure policies work as expected
5. **Cleanup**: Delete any test pods created during the test

## Expected NetworkPolicies

The tests expect to find NetworkPolicies in the `openshift-config-operator` namespace:
- `config-operator-networkpolicy` - Allows specific ingress/egress for the operator
- `default-deny-all` - Default deny policy for the namespace

The `openshift-config` and `openshift-config-managed` namespaces may or may not have NetworkPolicies depending on your cluster configuration.

## Troubleshooting

### Tests fail with "failed to get kubeconfig"
- Verify the kubeconfig file exists at `/home/yinzhou/kubeconfig`
- Ensure you have read permissions on the kubeconfig file
- Update the path in `network_policy_utils.go` if needed

### Tests fail with "failed to get namespace"
- Verify the cluster is running
- Ensure the namespaces exist (they should be created automatically by OpenShift)
- Check your kubeconfig has permissions to access these namespaces

### Connectivity tests fail
- Check that the NetworkPolicies are correctly deployed
- Verify the cluster's network plugin supports NetworkPolicies
- Check pod security policies aren't blocking test pod creation
- Review NetworkPolicy logs for any errors

## Contributing

When adding new tests:
1. Add test functions to `network_policy_enforcement_test.go`
2. Use helper functions from `network_policy_utils.go` for common operations
3. Follow the existing test patterns for consistency
4. Update this README with new test descriptions
