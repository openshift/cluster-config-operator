# Troubleshooting NetworkPolicy E2E Tests

## Common Test Failures and Solutions

### DNS Connectivity Test Timeout

**Error:**
```
connectivity check failed for openshift-config-operator/172.30.0.10:5353 expected=true: timed out waiting for the condition
```

**What it means:**
The `TestConfigOperatorNetworkPolicyEnforcement` test tries to verify that the NetworkPolicy allows DNS egress traffic. This test may fail if:
1. The NetworkPolicy doesn't include DNS egress rules
2. DNS service is not available or configured differently
3. The DNS port is different (53 vs 5353)
4. The NetworkPolicy allows all egress traffic (making specific DNS tests unnecessary)

**Solution (v2 - Already Fixed):**
The test has been updated to:
1. Check if the NetworkPolicy actually has DNS egress rules before testing
2. Try both common DNS ports (53 and 5353)
3. Use shorter timeouts (30 seconds instead of 2 minutes)
4. Fail when DNS egress is declared but connectivity to `dns-default` fails on all tested ports
5. Skip DNS tests if the NetworkPolicy doesn't specify DNS rules

**How the improved test works:**
```go
// The test now:
// 1. Checks if NetworkPolicy has DNS egress rules
// 2. Only tests DNS if rules exist
// 3. Tries multiple DNS ports
// 4. Requires at least one DNS connectivity success when DNS egress rules are present
```

**Alternative - Run Tests Without DNS Check:**
If you want to completely skip DNS connectivity testing, you can run the other tests:

```bash
# Run discovery test (no connectivity checks)
go test -v ./test/e2e -run TestConfigNamespaceNetworkPolicies

# Run enforcement test for all namespaces
go test -v ./test/e2e -run TestConfigNamespacesNetworkPolicyEnforcement

# Run generic enforcement test (creates test namespace)
go test -v ./test/e2e -run TestGenericNetworkPolicyEnforcement
```

---

### Test Pods Failing to Create

**Error:**
```
failed to create server pod: pods "np-operator-allowed" is forbidden: unable to validate against any security context constraint
```

**What it means:**
OpenShift security policies are preventing test pods from being created.

**Solution:**
The test pods use secure defaults:
- Non-root user (UID 1001)
- Dropped all capabilities
- No privilege escalation
- Seccomp profile enabled

If this still fails, check:
1. Your cluster's SecurityContextConstraints (SCC)
2. Whether the namespace has proper service accounts
3. Whether you have permissions to create pods in the test namespace

**Workaround:**
Run tests that don't create pods:
```bash
go test -v ./test/e2e -run TestConfigNamespaceNetworkPolicies
```

---

### NetworkPolicy Not Found

**Error:**
```
failed to get config operator NetworkPolicy: networkpolicies.networking.k8s.io "config-operator-networkpolicy" not found
```

**What it means:**
The expected NetworkPolicy doesn't exist in the namespace.

**Solution:**
1. Check if NetworkPolicies are deployed:
   ```bash
   oc get networkpolicies -n openshift-config-operator
   oc get networkpolicies -n openshift-config
   oc get networkpolicies -n openshift-config-managed
   ```

2. If no NetworkPolicies exist, the test will skip enforcement checks and only do discovery

3. The `TestConfigOperatorNetworkPolicyEnforcement` expects these policies in `openshift-config-operator`:
   - `config-operator-networkpolicy`
   - `default-deny-all`

---

### Kubeconfig Not Found

**Error:**
```
failed to get kubeconfig: invalid configuration: no configuration has been provided
```

**Solution:**
The kubeconfig path is hardcoded to `/home/yinzhou/kubeconfig`. To change it:

Set `KUBECONFIG` to your desired kubeconfig, or rely on the default `$HOME/.kube/config`:
   ```bash
   export KUBECONFIG=/path/to/kubeconfig
   go test -v ./test/e2e -run TestConfigNamespaceNetworkPolicies
   ```

---

### Namespace Not Found

**Error:**
```
failed to get namespace openshift-config-operator: namespaces "openshift-config-operator" not found
```

**What it means:**
You're testing against a cluster that doesn't have the expected OpenShift config namespaces.

**Solution:**
These namespaces should exist in any OpenShift cluster:
- `openshift-config-operator`
- `openshift-config`
- `openshift-config-managed`

If they don't exist:
1. Verify you're connected to an OpenShift cluster (not vanilla Kubernetes)
2. Check if you have the right kubeconfig
3. Verify the cluster is properly installed

---

### Test Timeout

**Error:**
```
panic: test timed out after 10m0s
```

**Solution:**
Increase the timeout:
```bash
go test -v ./test/e2e -run TestConfigOperatorNetworkPolicyEnforcement -timeout 30m
```

Or use the convenience script which has 30m timeout:
```bash
./test/e2e/run-tests.sh TestConfigOperatorNetworkPolicyEnforcement
```

---

## Test-Specific Guidance

### TestConfigOperatorNetworkPolicyEnforcement

**What it tests:**
- Verifies NetworkPolicies exist
- Tests allowed ingress on port 8443
- Tests denied ingress on random ports
- Optionally tests DNS egress (if configured in NetworkPolicy)

**Expected NetworkPolicies:**
- `config-operator-networkpolicy` - Should allow port 8443 ingress
- `default-deny-all` - Default deny policy

**Common issues:**
1. DNS egress test timing out → Fixed in v2, now skips if not configured
2. Port 8443 not allowed → Check NetworkPolicy ingress rules
3. Can't create test pods → Check SCC/RBAC permissions

**Skip this test if:**
- You don't have NetworkPolicies deployed yet
- You're just doing discovery

**Run instead:**
```bash
go test -v ./test/e2e -run TestConfigNamespaceNetworkPolicies
```

---

### TestConfigNamespaceNetworkPolicies

**What it tests:**
- Lists all NetworkPolicies in the three config namespaces
- Shows pods in each namespace
- Displays detailed policy information

**This test should never fail** unless:
- Namespaces don't exist
- You don't have read permissions
- Kubeconfig is wrong

**Use this test for:**
- Discovery
- Understanding what's deployed
- Debugging NetworkPolicy configuration

---

### TestConfigNamespacesNetworkPolicyEnforcement

**What it tests:**
- Validates NetworkPolicies exist in all three namespaces
- For namespaces with pods, verifies they're healthy
- Ensures NetworkPolicies don't block legitimate traffic

**This test is safe** because:
- It only checks existing pods
- Doesn't create new pods
- Doesn't do connectivity tests
- Just validates health

**Use this test when:**
- You want to verify policies don't break existing workloads
- You want broad validation without creating test pods
- You're in a production environment

---

### TestGenericNetworkPolicyEnforcement

**What it tests:**
- Creates a temporary test namespace
- Tests basic NetworkPolicy functionality
- Validates default deny, ingress, and egress rules

**This test may fail if:**
- Can't create namespaces
- Can't create pods (SCC issues)
- CNI doesn't support NetworkPolicies
- Connectivity tests timeout

**Use this test to:**
- Verify NetworkPolicy support in your cluster
- Test basic functionality
- Validate CNI configuration

**Skip this test if:**
- You can't create namespaces
- You have strict pod security policies
- You only want to test existing NetworkPolicies

---

## Recommended Test Order

### 1. Start with Discovery
```bash
go test -v ./test/e2e -run TestConfigNamespaceNetworkPolicies
```
This is safe and shows what exists.

### 2. Validate Existing Workloads
```bash
go test -v ./test/e2e -run TestConfigNamespacesNetworkPolicyEnforcement
```
This checks that existing pods are healthy.

### 3. Test Specific Policies (if they exist)
```bash
go test -v ./test/e2e -run TestConfigOperatorNetworkPolicyEnforcement
```
This does detailed testing but may have issues if NetworkPolicies aren't deployed exactly as expected.

### 4. Test Generic Functionality (optional)
```bash
go test -v ./test/e2e -run TestGenericNetworkPolicyEnforcement
```
This creates temporary resources and tests basic NetworkPolicy behavior.

---

## Getting Help

If tests are still failing:

1. **Collect logs:**
   ```bash
   go test -v ./test/e2e -run TestConfigNamespaceNetworkPolicies 2>&1 | tee discovery.log
   ```

2. **Check cluster state:**
   ```bash
   oc get networkpolicies -A | grep config
   oc get pods -n openshift-config-operator
   oc get pods -n openshift-config
   oc get pods -n openshift-config-managed
   ```

3. **Verify connectivity:**
   ```bash
   oc get svc -n openshift-dns
   oc get networkpolicies -n openshift-config-operator -o yaml
   ```

4. **Check permissions:**
   ```bash
   oc auth can-i list networkpolicies -n openshift-config-operator
   oc auth can-i create pods -n openshift-config-operator
   ```

5. **Review the test code** to understand what it expects vs. what your cluster has

---

## Summary of v2 Improvements

The tests have been improved to be more resilient:

✅ **DNS tests are now optional** - Only run if NetworkPolicy has DNS egress rules
✅ **Better error messages** - Warnings instead of failures for expected scenarios
✅ **Shorter timeouts** - DNS tests use 30s timeout instead of 2 minutes
✅ **Multiple DNS ports** - Tests both port 53 and 5353
✅ **Graceful degradation** - Tests skip sections that aren't applicable
✅ **Better logging** - Shows what's being tested and why

The tests are now much more flexible and should work across different NetworkPolicy configurations.
