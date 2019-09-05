all: build
.PHONY: all

# Include the library makefile
include $(addprefix ./vendor/github.com/openshift/library-go/alpha-build-machinery/make/, \
	golang.mk \
	targets/openshift/deps.mk \
	targets/openshift/images.mk \
)

# This will call a macro called "build-image" which will generate image specific targets based on the parameters:
# $0 - macro name
# $1 - target suffix
# $2 - Dockerfile path
# $3 - context directory for image build
# It will generate target "image-$(1)" for builing the image an binding it as a prerequisite to target "images".
$(call build-image,ocp-cluster-config-operator,registry.svc.ci.openshift.org/ocp/4.2:cluster-config-operator,./Dockerfile,.)

# This will call a macro called "add-bindata" which will generate bindata specific targets based on the parameters:
# $0 - macro name
# $1 - target suffix
# $2 - input dirs
# $3 - prefix
# $4 - pkg
# $5 - output
# It will generate targets {update,verify}-bindata-$(1) logically grouping them in unsuffixed versions of these targets
# and also hooked into {update,verify}-generated for broader integration.
$(call add-bindata,v3.11.0,./bindata/v3.11.0/...,bindata,v311_00_assets,pkg/operator/v311_00_assets/bindata.go)

clean:
	$(RM) ./cluster-config-operator
.PHONY: clean

CRD_SCHEMA_GEN_APIS := $(shell echo ./vendor/github.com/openshift/api/{authorization/v1,config/v1,quota/v1,security/v1,operator/v1alpha1})
CRD_SCHEMA_GEN_VERSION := v0.2.1

crd-schema-gen:
	git clone -b $(CRD_SCHEMA_GEN_VERSION) --single-branch --depth 1 https://github.com/kubernetes-sigs/controller-tools.git $(CRD_SCHEMA_GEN_TEMP)
	cd $(CRD_SCHEMA_GEN_TEMP); GO111MODULE=on go build ./cmd/controller-gen
update-codegen-crds: CRD_SCHEMA_GEN_TEMP :=$(shell mktemp -d)
update-codegen-crds: crd-schema-gen
	$(CRD_SCHEMA_GEN_TEMP)/controller-gen schemapatch:manifests=./manifests output:dir=./manifests paths="$(subst $() $(),;,$(CRD_SCHEMA_GEN_APIS))"
verify-codegen-crds: update-codegen-crds
	git diff -q manifests/ || { echo "Changed manifests: "; echo; git diff; false; }

update-codegen: update-codegen-crds
verify-codegen: verify-codegen-crds
verify: verify-codegen
.PHONY: update-codegen-crds update-codegen verify-codegen-crds verify-codegen verify crd-schema-gen
