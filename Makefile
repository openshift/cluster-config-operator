all: build
.PHONY: all

# Include the library makefile
include $(addprefix ./vendor/github.com/openshift/build-machinery-go/make/, \
	golang.mk \
	targets/openshift/deps-gomod.mk \
	targets/openshift/bindata.mk \
	targets/openshift/images.mk \
)

IMAGE_REGISTRY?=registry.svc.ci.openshift.org

# This will call a macro called "build-image" which will generate image specific targets based on the parameters:
# $0 - macro name
# $1 - target suffix
# $2 - Dockerfile path
# $3 - context directory for image build
# It will generate target "image-$(1)" for builing the image an binding it as a prerequisite to target "images".
$(call build-image,ocp-cluster-config-operator,$(IMAGE_REGISTRY)/ocp/4.2:cluster-config-operator,./Dockerfile.rhel7,.)

$(call verify-golang-versions,Dockerfile.rhel7)

clean:
	$(RM) ./cluster-config-operator
.PHONY: clean

# -------------------------------------------------------------------
# OpenShift Tests Extension (Cluster Config Operator)
# -------------------------------------------------------------------
TESTS_EXT_BINARY := cluster-config-operator-tests-ext
TESTS_EXT_PACKAGE := ./cmd/cluster-config-operator-tests-ext

TESTS_EXT_GIT_COMMIT := $(shell git rev-parse --short HEAD)
TESTS_EXT_BUILD_DATE := $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')
TESTS_EXT_GIT_TREE_STATE := $(shell if git diff --quiet; then echo clean; else echo dirty; fi)

TESTS_EXT_LDFLAGS := -X 'github.com/openshift-eng/openshift-tests-extension/pkg/version.CommitFromGit=$(TESTS_EXT_GIT_COMMIT)' \
                     -X 'github.com/openshift-eng/openshift-tests-extension/pkg/version.BuildDate=$(TESTS_EXT_BUILD_DATE)' \
                     -X 'github.com/openshift-eng/openshift-tests-extension/pkg/version.GitTreeState=$(TESTS_EXT_GIT_TREE_STATE)'

# -------------------------------------------------------------------
# Build binary with metadata (CI-compliant)
# -------------------------------------------------------------------
.PHONY: tests-ext-build

tests-ext-build:
	GOOS=$(GOOS) GOARCH=$(GOARCH) GO_COMPLIANCE_POLICY=exempt_all CGO_ENABLED=0 \
	go build -o $(TESTS_EXT_BINARY) -ldflags "$(TESTS_EXT_LDFLAGS)" $(TESTS_EXT_PACKAGE)

# -------------------------------------------------------------------
# Run "update" and strip env-specific metadata
# -------------------------------------------------------------------
.PHONY: tests-ext-update

tests-ext-update: tests-ext-build
	./$(TESTS_EXT_BINARY) update
	for f in .openshift-tests-extension/*.json; do \
		jq 'map(del(.codeLocations))' "$f" > tmpp && mv tmpp "$f"; \
	done
