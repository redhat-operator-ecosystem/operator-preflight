.DEFAULT_GOAL:=help

BINARY?=preflight
IMAGE_BUILDER?=podman
IMAGE_REPO?=quay.io/opdev
VERSION=$(shell git rev-parse HEAD)
RELEASE_TAG ?= "0.0.0"

PLATFORMS=linux darwin
ARCHITECTURES_LINUX=amd64 arm64 ppc64le s390x
ARCHITECTURES_MAC=amd64 arm64

.PHONY: build
build:
	CGO_ENABLED=0 go build -o $(BINARY) -ldflags "-X github.com/redhat-openshift-ecosystem/openshift-preflight/version.commit=$(VERSION) -X github.com/redhat-openshift-ecosystem/openshift-preflight/version.version=$(RELEASE_TAG)" cmd/preflight/main.go
	@ls | grep -e '^preflight$$' &> /dev/null

.PHONY: build-multi-arch-linux
build-multi-arch-linux: $(addprefix build-linux-,$(ARCHITECTURES_LINUX))

define LINUX_ARCHITECTURE_template
.PHONY: build-linux-$(1)
build-linux-$(1):
	GOOS=linux GOARCH=$(1) CGO_ENABLED=0 go build -o $(BINARY)-linux-$(1) -ldflags "-X github.com/redhat-openshift-ecosystem/openshift-preflight/version.commit=$(VERSION) \
				-X github.com/redhat-openshift-ecosystem/openshift-preflight/version.version=$(RELEASE_TAG)" cmd/preflight/main.go
endef

$(foreach arch,$(ARCHITECTURES_LINUX),$(eval $(call LINUX_ARCHITECTURE_template,$(arch))))

.PHONY: build-multi-arch-mac
build-multi-arch-mac: $(addprefix build-mac-,$(ARCHITECTURES_MAC))

define MAC_ARCHITECTURE_template
.PHONY: build-mac-$(1)
build-mac-$(1):
	GOOS=darwin GOARCH=$(1) go build -o $(BINARY)-darwin-$(1) -ldflags "-X github.com/redhat-openshift-ecosystem/openshift-preflight/version.commit=$(VERSION) \
				-X github.com/redhat-openshift-ecosystem/openshift-preflight/version.version=$(RELEASE_TAG)" cmd/preflight/main.go
endef

$(foreach arch,$(ARCHITECTURES_MAC),$(eval $(call MAC_ARCHITECTURE_template,$(arch))))

.PHONY: fmt
fmt: gofumpt
	${GOFUMPT} -l -w .
	git diff --exit-code

.PHONY: tidy
tidy:
	go mod tidy
	git diff --exit-code

.PHONY: image-build
image-build:
	$(IMAGE_BUILDER) build --build-arg release_tag=$(RELEASE_TAG) --build-arg preflight_commit=$(VERSION) -t $(IMAGE_REPO)/preflight:$(VERSION) .

.PHONY: image-push
image-push:
	$(IMAGE_BUILDER) push $(IMAGE_REPO)/preflight:$(VERSION)

.PHONY: test
test:
	go test -v $$(go list ./... | grep -v e2e) \
	-ldflags "-X github.com/redhat-openshift-ecosystem/openshift-preflight/version.commit=bar -X github.com/redhat-openshift-ecosystem/openshift-preflight/version.version=foo"

.PHONY: cover
cover:
	go test -v \
	 -ldflags "-X github.com/redhat-openshift-ecosystem/openshift-preflight/version.commit=bar -X github.com/redhat-openshift-ecosystem/openshift-preflight/version.version=foo" \
	 $$(go list ./... | grep -v e2e) \
	 -race \
	 -cover -coverprofile=coverage.out

.PHONY: vet
vet:
	go vet ./...

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter checks.
	$(GOLANGCI_LINT) run
	
.PHONY: test-e2e
test-e2e:
	./test/e2e/operator-test.sh

.PHONY: test-e2e-customized-env
test-e2e-customized-env:
	./test/e2e/operator-test-customized-env.sh

.PHONY: ensure-release-tag-set
ensure-release-tag-set:
	echo "Make sure the RELEASE_TAG value is set."
	test $(RELEASE_TAG) != "0.0.0"


.PHONY: verify-image
verify-image: cosign ensure-release-tag-set
	${COSIGN} verify \
		--certificate-identity https://github.com/redhat-openshift-ecosystem/openshift-preflight/.github/workflows/build-multiarch.yml@refs/tags/$(RELEASE_TAG) \
		--certificate-oidc-issuer https://token.actions.githubusercontent.com \
		quay.io/opdev/preflight:$(RELEASE_TAG)

.PHONY: clean
clean:
	@go clean
	@# cleans the binary created by make build
	$(shell if [ -f "$(BINARY)" ]; then rm -f $(BINARY); fi)
	@# cleans all the binaries created by make build-multi-arch
	$(foreach GOOS, $(PLATFORMS),\
	$(foreach GOARCH, $(ARCHITECTURES_LINUX),\
	$(shell if [ -f "$(BINARY)-$(GOOS)-$(GOARCH)" ]; then rm -f $(BINARY)-$(GOOS)-$(GOARCH); fi)))
	$(foreach GOOS, $(PLATFORMS),\
	$(foreach GOARCH, $(ARCHITECTURES_MAC),\
	$(shell if [ -f "$(BINARY)-$(GOOS)-$(GOARCH)" ]; then rm -f $(BINARY)-$(GOOS)-$(GOARCH); fi)))

GOLANGCI_LINT = $(shell pwd)/bin/golangci-lint
GOLANGCI_LINT_VERSION ?= v1.62.2
golangci-lint: $(GOLANGCI_LINT)
$(GOLANGCI_LINT):
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION))

GOFUMPT = $(shell pwd)/bin/gofumpt
gofumpt: ## Download envtest-setup locally if necessary.
	$(call go-install-tool,$(GOFUMPT),mvdan.cc/gofumpt@latest)

COSIGN = $(shell pwd)/bin/cosign
COSIGN_VERSION ?= v2.0.0
cosign: ## Download envtest-setup locally if necessary.
	$(call go-install-tool,$(COSIGN),github.com/sigstore/cosign/v2/cmd/cosign@$(COSIGN_VERSION))


# go-get-tool will 'go get' any package $2 and install it to $1.
PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
define go-install-tool
@[ -f $(1) ] || { \
GOBIN=$(PROJECT_DIR)/bin go install $(2) ;\
}
endef
