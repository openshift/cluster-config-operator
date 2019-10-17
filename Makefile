all: build
.PHONY: all

# Include the library makefile
include $(addprefix ./vendor/github.com/openshift/library-go/alpha-build-machinery/make/, \
	golang.mk \
	targets/openshift/deps.mk \
	targets/openshift/images.mk \
	targets/openshift/crd-schema-gen.mk \
)

IMAGE_REGISTRY?=registry.svc.ci.openshift.org

# This will call a macro called "build-image" which will generate image specific targets based on the parameters:
# $0 - macro name
# $1 - target suffix
# $2 - Dockerfile path
# $3 - context directory for image build
# It will generate target "image-$(1)" for builing the image an binding it as a prerequisite to target "images".
$(call build-image,ocp-cluster-config-operator,$(IMAGE_REGISTRY)/ocp/4.2:cluster-config-operator,./Dockerfile.rhel7,.)

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

# Set crd-schema-gen variables
CRD_SCHEMA_GEN_APIS := $(shell echo ./vendor/github.com/openshift/api/{authorization/v1,config/v1,quota/v1,security/v1,operator/v1alpha1})
CRD_SCHEMA_GEN_VERSION :=v0.2.1

update-codegen: update-codegen-crds
.PHONY: update-codegen

verify-codegen: verify-codegen-crds
.PHONY: verify-codegen
