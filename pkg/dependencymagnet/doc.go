//go:build tools
// +build tools

// go mod won't pull in code that isn't depended upon, but we have some code we don't depend on from code that must be included
// for our build to work.
package dependencymagnet

import (
	_ "github.com/go-bindata/go-bindata"
	_ "github.com/openshift/build-machinery-go"

	_ "github.com/openshift/api/authorization/v1"
	_ "github.com/openshift/api/config/v1"
	_ "github.com/openshift/api/operator/v1alpha1"
	_ "github.com/openshift/api/quota/v1"
	_ "github.com/openshift/api/security/v1"
	_ "github.com/openshift/api/securityinternal/v1"
)
