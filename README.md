# cluster-config-operator
The canonical location for OpenShift cluster configuration.  This repo includes:
 1. The source of all CRD manifests for config.openshift.io
 2. A render command which creates the initial CRs for all config.openshift.io resource.
 3. Future: An operator that handles migration needs as config.openshift.io is expanded.
