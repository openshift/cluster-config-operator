module github.com/openshift/cluster-config-operator

go 1.13

require (
	github.com/openshift/api v0.0.0-20200122114642-1108c9abdb99
	github.com/openshift/library-go v0.0.0-20200122154921-7ed6868961c3
	github.com/prometheus/client_golang v1.1.0
	github.com/spf13/cobra v0.0.5
	github.com/spf13/pflag v1.0.5
	k8s.io/apimachinery v0.17.3-beta.0
	k8s.io/component-base v0.17.1
	k8s.io/klog v1.0.0
)
