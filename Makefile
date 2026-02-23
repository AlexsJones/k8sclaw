# K8sClaw Makefile
# Kubernetes-native agent orchestration platform

# Image registry — matches ghcr.io/<owner>/<repo>/<image>
REGISTRY ?= ghcr.io/alexsjones/k8sclaw
TAG ?= latest

# Go parameters
GOCMD = go
GOBUILD = $(GOCMD) build
GOTEST = $(GOCMD) test
GOVET = $(GOCMD) vet
GOMOD = $(GOCMD) mod

# Binary output directory
BIN_DIR = bin

# All binaries
BINARIES = controller apiserver ipc-bridge webhook k8sclaw

# All channel binaries
CHANNELS = telegram whatsapp discord slack

# All images
IMAGES = controller apiserver ipc-bridge webhook \
         channel-telegram channel-whatsapp channel-discord channel-slack

.PHONY: all build test clean generate manifests docker-build docker-push install help

all: build

##@ General

help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

build: $(addprefix build-,$(BINARIES)) $(addprefix build-channel-,$(CHANNELS)) ## Build all binaries

build-%: ## Build a specific binary (e.g., make build-controller)
	$(GOBUILD) -o $(BIN_DIR)/$* ./cmd/$*/

build-channel-%: ## Build a specific channel binary
	$(GOBUILD) -o $(BIN_DIR)/channel-$* ./channels/$*/

test: ## Run tests
	$(GOTEST) -race -coverprofile=coverage.out ./...

test-short: ## Run short tests
	$(GOTEST) -short ./...

vet: ## Run go vet
	$(GOVET) ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

fmt: ## Run gofmt
	gofmt -s -w .

tidy: ## Run go mod tidy
	$(GOMOD) tidy

##@ Code Generation

generate: ## Generate code (deepcopy, CRD manifests)
	controller-gen object:headerFile="hack/boilerplate.go.txt" paths="./api/..."
	controller-gen rbac:roleName=k8sclaw-manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

manifests: ## Generate CRD manifests
	controller-gen crd paths="./api/..." output:crd:artifacts:config=config/crd/bases

##@ Docker

docker-build: $(addprefix docker-build-,$(IMAGES)) ## Build all Docker images

docker-build-%: ## Build a specific Docker image
	docker build -t $(REGISTRY)/$*:$(TAG) -f images/$*/Dockerfile .

docker-push: $(addprefix docker-push-,$(IMAGES)) ## Push all Docker images

docker-push-%: ## Push a specific Docker image
	docker push $(REGISTRY)/$*:$(TAG)

set-images: ## Stamp REGISTRY/TAG into K8s manifests
	cd config/manager && kustomize edit set image \
		controller=$(REGISTRY)/controller:$(TAG) \
		apiserver=$(REGISTRY)/apiserver:$(TAG)
	cd config/webhook && kustomize edit set image \
		webhook=$(REGISTRY)/webhook:$(TAG)
	@echo "Images set to $(REGISTRY)/*:$(TAG)"

##@ Deployment

install: manifests ## Install CRDs into the K8s cluster
	kubectl apply -f config/crd/bases/

uninstall: ## Uninstall CRDs from the K8s cluster
	kubectl delete -f config/crd/bases/

deploy: manifests ## Deploy controller to the K8s cluster
	kubectl apply -k config/

undeploy: ## Undeploy controller from the K8s cluster
	kubectl delete -k config/

deploy-samples: ## Deploy sample CRs
	kubectl apply -f config/samples/

##@ Database

db-migrate: ## Run database migrations
	@echo "Running migrations against $${DATABASE_URL}"
	psql "$${DATABASE_URL}" -f migrations/001_initial.sql

##@ Clean

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)
	rm -f coverage.out
