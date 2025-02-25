include Makefile.variables
include inttest/Makefile.variables

.DELETE_ON_ERROR:

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
CRDOC ?= $(LOCALBIN)/crdoc

## e2e configuration
E2E_CONF_FILE ?= $(shell pwd)/e2e/config/docker.yaml
SKIP_RESOURCE_CLEANUP ?= false
# Artifacts folder generated for e2e tests
ARTIFACTS ?= $(shell pwd)/_artifacts

# Image URL to use all building/pushing image targets
IMG ?= quay.io/k0sproject/k0smotron:latest
# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.26.0

# GO_TEST_DIRS is a list of directories to run go test on, excluding inttests
GO_TEST_DIRS ?= ./api/... ./cmd/... ./internal/...

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

### manifests
manifests_targets += config/crd/bases/bootstrap.cluster.x-k8s.io_k0sconfigs.yaml
config/crd/bases/bootstrap.cluster.x-k8s.io_k0sconfigs.yaml: $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd:generateEmbeddedObjectMeta=true webhook paths="./..." output:crd:artifacts:config=config/crd/bases

manifests_targets += config/crd/bases/k0smotron.io_clusters.yaml
manifests_targets += config/crd/bases/k0smotron.io_jointokenrequests.yaml
manifests_targets += config/crd/bases/bootstrap.cluster.x-k8s.io_k0sconfigs.yaml
manifests_targets += config/crd/bases/bootstrap.cluster.x-k8s.io_k0sworkerconfigs.yaml
manifests_targets += config/crd/bases/bootstrap.cluster.x-k8s.io_k0sworkerconfigtemplates.yaml
manifests_targets += config/crd/bases/bootstrap.cluster.x-k8s.io_k0scontrollerconfigs.yaml
manifests_targets += config/crd/bases/controlplane.cluster.x-k8s.io_k0scontrolplanes.yaml
manifests_targets += config/crd/bases/controlplane.cluster.x-k8s.io_k0smotroncontrolplanes.yaml
manifests_targets += config/crd/bases/infrastructure.cluster.x-k8s.io_remoteclusters.yaml
manifests_targets += config/crd/bases/infrastructure.cluster.x-k8s.io_remotemachines.yaml
config/crd/bases/k0smotron.io_clusters.yaml: $(CONTROLLER_GEN) api/k0smotron.io/v1beta1/k0smotroncluster_types.go
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd:generateEmbeddedObjectMeta=true webhook paths="./..." output:crd:artifacts:config=config/crd/bases

manifests: $(manifests_targets) ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.

### generate
generate_targets += api/k0smotron.io/v1beta1/zz_generated.deepcopy.go
generate_targets += api/bootstrap/v1beta1/zz_generated.deepcopy.go
generate_targets += api/controlplane/v1beta1/zz_generated.deepcopy.go
generate_targets += api/infrastructure/v1beta1/zz_generated.deepcopy.go
$(generate_targets): $(CONTROLLER_GEN)
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

generate: $(generate_targets) clusterapi-manifests ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.


GO_PKGS=$(shell go list ./...)
.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt $(GO_PKGS)

.PHONY: vet
vet: ## Run go vet against code.
	go vet $(GO_PKGS)

.PHONY: test
test: manifests generate fmt vet envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test $(GO_TEST_DIRS) -coverprofile cover.out

e2e: k0smotron-image-bundle.tar install.yaml
	go test -v -tags e2e -run '$(TEST_NAME)' ./e2e  \
	    -artifacts-folder="$(ARTIFACTS)" \
	    -config="$(E2E_CONF_FILE)" \
	    -skip-resource-cleanup=$(SKIP_RESOURCE_CLEANUP) \
		-timeout=30m

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

# If you wish built the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64 ). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: test ## Build docker image with the manager.
	docker build \
	  -t ${IMG} \
	  --build-arg BUILD_IMG=golang:$(GO_VERSION) \
	.

k0smotron-image-bundle.tar: docker-build
	docker save ${IMG} -o k0smotron-image-bundle.tar

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	docker push ${IMG}

# PLATFORMS defines the target platforms for  the manager image be build to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - able to use docker buildx . More info: https://docs.docker.com/build/buildx/
# - have enable BuildKit, More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image for your registry (i.e. if you do not inform a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To properly provided solutions that supports more than one platform you should use this option.
PLATFORMS ?= linux/arm64,linux/amd64
.PHONY: docker-buildx
docker-buildx: test ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- docker buildx create --name project-v3-builder
	docker buildx use project-v3-builder
	- docker buildx build --push --platform=$(PLATFORMS) --tag ${IMG} -f Dockerfile.cross .
	- docker buildx rm project-v3-builder
	rm Dockerfile.cross

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl create -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image k0s/k0smotron=${IMG}
	$(KUSTOMIZE) build config/default | kubectl create -f -

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: release
release: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default > install.yaml
	git checkout config/manager/kustomization.yaml

clusterapi-manifests:
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd:generateEmbeddedObjectMeta=true webhook paths="./api/bootstrap/..." output:crd:artifacts:config=config/clusterapi/bootstrap/bases
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd:generateEmbeddedObjectMeta=true webhook paths="./api/controlplane/..." output:crd:artifacts:config=config/clusterapi/controlplane/bases
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd:generateEmbeddedObjectMeta=true webhook paths="./api/infrastructure/..." output:crd:artifacts:config=config/clusterapi/infrastructure/bases
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd:generateEmbeddedObjectMeta=true webhook paths="./api/k0smotron.io/..." output:crd:artifacts:config=config/clusterapi/k0smotron.io/bases

bootstrap-components.yaml: $(CONTROLLER_GEN) clusterapi-manifests kustomize
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/clusterapi/bootstrap/ > bootstrap-components.yaml
	git checkout config/manager/kustomization.yaml

control-plane-components.yaml: $(CONTROLLER_GEN) clusterapi-manifests kustomize
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/clusterapi/controlplane/ > control-plane-components.yaml
	git checkout config/manager/kustomization.yaml

infrastructure-components.yaml: $(CONTROLLER_GEN) clusterapi-manifests kustomize
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/clusterapi/infrastructure/ > infrastructure-components.yaml
	git checkout config/manager/kustomization.yaml
##@ Build Dependencies

kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary. If wrong version is installed, it will be removed before downloading.
$(KUSTOMIZE): Makefile.variables | $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION)

controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary. If wrong version is installed, it will be overwritten.
$(CONTROLLER_GEN): Makefile.variables | $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

.PHONY: docs
docs:
	$(MAKE) -C docs

.PHONY: docs-serve-dev
docs-serve-dev: DOCS_DEV_PORT ?= 8000
docs-serve-dev:
	$(MAKE) -C docs .docker-image.serve-dev.stamp
	docker run --rm \
	  -v "$(CURDIR):/k0s:ro" \
	  -p '$(DOCS_DEV_PORT):8000' \
	  k0sdocs.docker-image.serve-dev

crdoc: $(CRDOC) ## Download crdoc locally if necessary. If wrong version is installed, it will be overwritten.
$(CRDOC): Makefile.variables | $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install fybrik.io/crdoc@$(CRDOC_VERSION)

.PHONY: docs-generate-reference
docs-generate-reference: $(CRDOC)
	$(CRDOC) --resources config/crd/bases/ --output docs/resource-reference.md

.PHONY: $(smoketests)
$(smoketests): release k0smotron-image-bundle.tar
	$(MAKE) -C inttest $@

.PHONY: smoketest
smoketests: $(smoketests)

.PHONY: clean
clean:
	-$(MAKE) -C inttest clean
	rm -f hack/lint/.golangci-lint.stamp
	rm -rf \
	  $(generate_targets) \
	  $(manifests_targets) \
	  k0smotron-image-bundle.tar \
	  $(LOCALBIN)

hack/lint/.golangci-lint.stamp: hack/lint/Dockerfile Makefile.variables
	docker build \
	  -t k0smotron.golangci-lint \
	  --build-arg BUILD_IMG=golang:$(GO_VERSION) \
	  --build-arg GOLANGCILINT_VERSION=$(GOLANGCILINT_VERSION) \
	  -f hack/lint/Dockerfile \
	  .
	touch -- '$@'

.PHONY: lint
lint: GOLANGCI_LINT_FLAGS ?= --verbose
lint: hack/lint/.golangci-lint.stamp
	docker run \
	  --rm \
	  -v "$(CURDIR):/go/src/github.com/k0smotron/k0smotron:ro" \
	  -w /go/src/github.com/k0smotron/k0smotron \
	  k0smotron.golangci-lint golangci-lint run $(GOLANGCI_LINT_FLAGS) $(GO_LINT_DIRS)

# KinD helpers
.PHONY: kind-cluster
kind-cluster:
	docker create network kind --opt com.docker.network.bridge.enable_ip_masquerade=true || true
	kind create cluster --name k0smotron --config config/samples/capi/docker/kind.yaml

.PHONY: kind-deploy-capi
	clusterctl init --infrastructure docker

.PHONY: kind-deploy-k0smotron
kind-deploy-k0smotron: release k0smotron-image-bundle.tar
	kind load image-archive k0smotron-image-bundle.tar
	kubectl apply --server-side=true -f install.yaml
	kubectl rollout restart -n k0smotron deployment/k0smotron-controller-manager

sbom/spdx.json: go.mod
	mkdir -p -- '$(dir $@)'
	docker run --rm \
	  -v "$(CURDIR)/go.mod:/k0s/go.mod" \
	  -v "$(CURDIR)/embedded-bins/staging/linux/bin:/k0s/bin" \
	  -v "$(CURDIR)/syft.yaml:/tmp/syft.yaml" \
	  -v "$(CURDIR)/sbom:/out" \
	  --user $(BUILD_UID):$(BUILD_GID) \
	  anchore/syft:v0.90.0 \
	  /k0s -o spdx-json@2.2=/out/spdx.json -c /tmp/syft.yaml

.PHONY: sign-sbom
sign-sbom: sbom/spdx.json
	docker run --rm \
	  -v "$(CURDIR):/k0s" \
	  -v "$(CURDIR)/sbom:/out" \
	  -e COSIGN_PASSWORD="$(COSIGN_PASSWORD)" \
	  gcr.io/projectsigstore/cosign:v2.2.0 \
	  sign-blob \
	  --key /k0s/cosign.key \
	  --tlog-upload=false \
	  /k0s/sbom/spdx.json --output-file /out/spdx.json.sig

.PHONY: sign-pub-key
sign-pub-key:
	docker run --rm \
	  -v "$(CURDIR):/k0s" \
	  -v "$(CURDIR)/sbom:/out" \
	  -e COSIGN_PASSWORD="$(COSIGN_PASSWORD)" \
	  gcr.io/projectsigstore/cosign:v2.2.0 \
	  public-key \
	  --key /k0s/cosign.key --output-file /out/cosign.pub
