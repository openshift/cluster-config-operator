module github.com/openshift/cluster-config-operator

go 1.13

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc
	github.com/ghodss/yaml v1.0.1-0.20190212211648-25d852aebe32 // indirect
	github.com/go-bindata/go-bindata v3.1.2+incompatible
	github.com/openshift/api v0.0.0-20210315130343-ff0ef568c9ca
	github.com/openshift/build-machinery-go v0.0.0-20210202172015-e79b2a6f0d96
	github.com/openshift/client-go v0.0.0-20210112165513-ebc401615f47
	github.com/openshift/library-go v0.0.0-20210127081712-a4f002827e42
	github.com/spf13/cobra v1.1.1
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.6.1
	gopkg.in/gcfg.v1 v1.2.0
	gopkg.in/warnings.v0 v0.1.1 // indirect
	k8s.io/api v0.20.0
	k8s.io/apimachinery v0.20.0
	k8s.io/client-go v0.20.0
	k8s.io/component-base v0.20.0
	k8s.io/klog v1.0.0
	sigs.k8s.io/yaml v1.2.0
)

replace github.com/openshift/api => github.com/patrickdillon/api v0.0.0-20210310201738-f2cf6172ab11
