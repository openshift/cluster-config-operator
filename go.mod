module github.com/openshift/cluster-config-operator

go 1.16

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc
	github.com/ghodss/yaml v1.0.1-0.20190212211648-25d852aebe32 // indirect
	github.com/go-bindata/go-bindata v3.1.2+incompatible
	github.com/openshift/api v0.0.0-20210817132244-67c28690af52
	github.com/openshift/build-machinery-go v0.0.0-20210712174854-1bb7fd1518d3
	github.com/openshift/client-go v0.0.0-20210730113412-1811c1b3fc0e
	github.com/openshift/library-go v0.0.0-20210811133500-5e31383de2a7
	github.com/spf13/cobra v1.1.3
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.7.0
	google.golang.org/grpc v1.39.1 // indirect
	gopkg.in/gcfg.v1 v1.2.0
	gopkg.in/warnings.v0 v0.1.1 // indirect
	k8s.io/api v0.22.0-rc.0
	k8s.io/apimachinery v0.22.0-rc.0
	k8s.io/client-go v0.22.0-rc.0
	k8s.io/component-base v0.22.0-rc.0
	k8s.io/klog/v2 v2.9.0
	sigs.k8s.io/yaml v1.2.0
)
