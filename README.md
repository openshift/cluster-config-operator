# cluster-config-operator

This repo is not open for control loop contributions.
This repo only exists to hold the CRD manifests required to bootstrap a cluster, it is not a place to add logic because jointly owned repos
like this one end up having no owner to handle migration over time as dependencies move.

The canonical location for OpenShift cluster configuration. This repo includes:
 1. The source of all CRD manifests for config.openshift.io
 2. A render command which creates the initial CRs for all config.openshift.io resource.

Test Pod Security Enforcement being disabled.

