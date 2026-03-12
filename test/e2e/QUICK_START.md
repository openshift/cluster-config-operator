# Quick Start Guide - NetworkPolicy E2E Tests

## TL;DR - Run Tests Now

```bash
cd /path/to/cluster-config-operator

# Run all NetworkPolicy tests
go test -v ./test/e2e -run 'Test.*NetworkPolicy.*' -timeout 30m

# Or use the convenience script
./test/e2e/run-tests.sh
```

## Quick Test Commands

### Discovery - What NetworkPolicies exist?
```bash
go test -v ./test/e2e -run TestConfigNamespaceNetworkPolicies
```
This shows all NetworkPolicies in the three config namespaces.

### Enforcement - Do the policies work?
```bash
go test -v ./test/e2e -run TestConfigNamespacesNetworkPolicyEnforcement
```
This verifies NetworkPolicies are correctly enforcing traffic rules.

### Operator-Specific Tests
```bash
go test -v ./test/e2e -run TestConfigOperatorNetworkPolicyEnforcement
```
Tests specific to openshift-config-operator namespace.

### Generic Enforcement Tests
```bash
go test -v ./test/e2e -run TestGenericNetworkPolicyEnforcement
```
Tests basic NetworkPolicy functionality in a test namespace.

## What Gets Tested?

The tests verify NetworkPolicy configuration in these namespaces:
- **openshift-config-operator** - Operator namespace with running pods
- **openshift-config** - Configuration storage (usually no pods)
- **openshift-config-managed** - Managed configuration (usually no pods)

## Requirements

✅ OpenShift cluster running
✅ Kubeconfig available (`KUBECONFIG` set, or default `$HOME/.kube/config`)
✅ Go 1.19+ installed

## Troubleshooting

**DNS connectivity test timing out?**
→ This is now handled gracefully - the test will skip DNS checks if not configured

**"No such file or directory" error?**
→ Check that `/home/yinzhou/kubeconfig` exists and is readable

**"connection refused" error?**
→ Verify your cluster is running and kubeconfig is correct

**Tests timeout?**
→ Increase timeout: `go test -v ./test/e2e -run TestXXX -timeout 60m`

**Need different kubeconfig path?**
→ Set `KUBECONFIG=/path/to/kubeconfig` before running tests

**Still having issues?**
→ See [TROUBLESHOOTING.md](./TROUBLESHOOTING.md) for detailed guidance

## See More

- [TROUBLESHOOTING.md](./TROUBLESHOOTING.md) - Detailed troubleshooting guide
- [README_NETWORK_POLICY_TESTS.md](./README_NETWORK_POLICY_TESTS.md) - Full documentation
- [GO_TEST_EXAMPLES.md](../GO_TEST_EXAMPLES.md) - More go test examples
