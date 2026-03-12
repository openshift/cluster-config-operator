#!/bin/bash
# Script to run NetworkPolicy e2e tests for cluster-config-operator
# Usage: ./run-tests.sh [test-name]

set -e

KUBECONFIG="${KUBECONFIG:-$HOME/.kube/config}"
export KUBECONFIG

echo "Using kubeconfig: $KUBECONFIG"
echo "================================================"

if [ -n "$1" ]; then
    # Run specific test
    echo "Running test: $1"
    go test -v ./test/e2e -run "$1" -timeout 30m
else
    # Run all NetworkPolicy tests
    echo "Running all NetworkPolicy tests..."
    go test -v ./test/e2e -run 'Test.*NetworkPolicy.*' -timeout 30m
fi
