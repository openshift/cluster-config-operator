# Go Test Examples for NetworkPolicy E2E Tests

This document provides copy-paste ready `go test` commands for running NetworkPolicy e2e tests.

## Setup

```bash
cd /path/to/cluster-config-operator
export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/config}"
```

## Run All NetworkPolicy Tests

```bash
go test -v ./test/e2e -run 'Test.*NetworkPolicy.*' -timeout 30m
```

## Run Individual Tests

### 1. Discover NetworkPolicies in Config Namespaces
```bash
go test -v ./test/e2e -run TestConfigNamespaceNetworkPolicies -timeout 10m
```
**What it does:**
- Lists all NetworkPolicies in openshift-config-operator, openshift-config, and openshift-config-managed
- Shows detailed policy information (selectors, rules)
- Lists all pods in each namespace

**Use this when:**
- You want to see what NetworkPolicies are deployed
- Debugging NetworkPolicy configuration
- Understanding the current state

---

### 2. Test NetworkPolicy Enforcement
```bash
go test -v ./test/e2e -run TestConfigNamespacesNetworkPolicyEnforcement -timeout 20m
```
**What it does:**
- Validates NetworkPolicies exist in all three namespaces
- Verifies existing pods are healthy (policies don't block legitimate traffic)
- Tests enforcement for namespaces with running pods

**Use this when:**
- Verifying NetworkPolicies work correctly
- After deploying new NetworkPolicies
- Regression testing

---

### 3. Test Config Operator NetworkPolicy Specifically
```bash
go test -v ./test/e2e -run TestConfigOperatorNetworkPolicyEnforcement -timeout 20m
```
**What it does:**
- Verifies config-operator-networkpolicy and default-deny-all exist
- Tests allowed port 8443 ingress
- Tests denied ports
- Verifies DNS egress on port 5353

**Use this when:**
- Testing openshift-config-operator namespace specifically
- Verifying operator connectivity
- Debugging operator NetworkPolicy issues

---

### 4. Test Generic NetworkPolicy Behavior
```bash
go test -v ./test/e2e -run TestGenericNetworkPolicyEnforcement -timeout 20m
```
**What it does:**
- Creates a temporary test namespace
- Tests default allow-all behavior
- Tests default deny-all policy
- Tests ingress-only and egress-only rules
- Tests combined ingress+egress rules

**Use this when:**
- Verifying basic NetworkPolicy functionality
- Testing the CNI plugin supports NetworkPolicies
- Learning how NetworkPolicies work

---

## Advanced Options

### Run with Custom Timeout
```bash
go test -v ./test/e2e -run TestConfigNamespaceNetworkPolicies -timeout 60m
```

### Run Multiple Tests
```bash
go test -v ./test/e2e -run 'TestConfigNamespaceNetworkPolicies|TestConfigNamespacesNetworkPolicyEnforcement' -timeout 30m
```

### Run with JSON Output (for CI/CD)
```bash
go test -v ./test/e2e -run 'Test.*NetworkPolicy.*' -json -timeout 30m > test-results.json
```

### Run with Less Verbose Output
```bash
go test ./test/e2e -run 'Test.*NetworkPolicy.*' -timeout 30m
```

### Run All Tests in e2e Package
```bash
go test -v ./test/e2e -timeout 30m
```

## Quick Test Scenarios

### Scenario 1: Just deployed NetworkPolicies, verify they work
```bash
# First, see what was deployed
go test -v ./test/e2e -run TestConfigNamespaceNetworkPolicies

# Then verify enforcement
go test -v ./test/e2e -run TestConfigNamespacesNetworkPolicyEnforcement
```

### Scenario 2: Debugging NetworkPolicy issues
```bash
# Run discovery with verbose output
go test -v ./test/e2e -run TestConfigNamespaceNetworkPolicies -timeout 10m 2>&1 | tee discovery.log

# Run enforcement test
go test -v ./test/e2e -run TestConfigNamespacesNetworkPolicyEnforcement -timeout 20m 2>&1 | tee enforcement.log
```

### Scenario 3: CI/CD Pipeline
```bash
# Run all tests with timeout and JSON output
go test -v ./test/e2e -run 'Test.*NetworkPolicy.*' -json -timeout 30m > test-results.json

# Check exit code
if [ $? -eq 0 ]; then
    echo "✅ All tests passed"
else
    echo "❌ Tests failed"
    exit 1
fi
```

## Useful Test Flags

| Flag | Description | Example |
|------|-------------|---------|
| `-v` | Verbose output | `go test -v ./test/e2e` |
| `-run` | Run specific test(s) | `go test -run TestConfig ./test/e2e` |
| `-timeout` | Set timeout | `go test -timeout 30m ./test/e2e` |
| `-json` | JSON output | `go test -json ./test/e2e` |
| `-count` | Run N times | `go test -count 3 ./test/e2e` |
| `-failfast` | Stop on first failure | `go test -failfast ./test/e2e` |
| `-list` | List tests without running | `go test -list TestConfig ./test/e2e` |

## Environment Variables

```bash
# Use different kubeconfig
export KUBECONFIG=/path/to/kubeconfig
go test -v ./test/e2e -run TestConfigNamespaceNetworkPolicies

# Set Go test flags
export GOFLAGS="-v -timeout=60m"
go test ./test/e2e -run TestConfigNamespaceNetworkPolicies
```

## Common Issues

### Issue: Tests can't find kubeconfig
```
Error: failed to get kubeconfig: invalid configuration: no configuration has been provided
```
****Solution:** `getKubeConfig()` honors `KUBECONFIG`; set it to your desired file path (or rely on the default `$HOME/.kube/config`).

### Issue: Tests timeout
```
panic: test timed out after 10m0s
```
**Solution:** Increase timeout: `-timeout 30m` or `-timeout 60m`

### Issue: Permission denied
```
Error: failed to list NetworkPolicies: forbidden: User "system:anonymous" cannot list resource "networkpolicies"
```
**Solution:** Check your kubeconfig has proper permissions and is logged in to the cluster

## Next Steps

- Read [QUICK_START.md](./e2e/QUICK_START.md) for a quick reference
- Read [README_NETWORK_POLICY_TESTS.md](./e2e/README_NETWORK_POLICY_TESTS.md) for detailed documentation
- Run the tests and verify your NetworkPolicies work correctly!
