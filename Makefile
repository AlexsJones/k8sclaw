# Sympozium Makefile
# Kubernetes-native agent orchestration platform

# Image registry — matches ghcr.io/<owner>/<repo>/<image>
REGISTRY ?= ghcr.io/alexsjones/sympozium
TAG ?= latest

# Tool versions
CONTROLLER_GEN_VERSION ?= v0.17.2

# Go parameters
GOCMD = go
GOBUILD = $(GOCMD) build
GOTEST = $(GOCMD) test
GOVET = $(GOCMD) vet
GOMOD = $(GOCMD) mod

# Local tool binaries
LOCALBIN ?= $(shell pwd)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen

# Binary output directory
BIN_DIR = bin

# All binaries
BINARIES = controller apiserver ipc-bridge webhook agent-runner sympozium

# All channel binaries
CHANNELS = telegram whatsapp discord slack

# All images
IMAGES = controller apiserver ipc-bridge webhook agent-runner \
         channel-telegram channel-whatsapp channel-discord channel-slack \
         skill-k8s-ops skill-sre-observability

.PHONY: all build test clean generate manifests docker-build docker-push install help web-build web-dev web-dev-serve web-clean web-install setup-hooks

all: build

##@ General

setup-hooks: ## Configure git to use .githooks (enables pre-commit formatting check)
	git config core.hooksPath .githooks

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

test-integration: ## Run integration tests (requires Kind cluster + API keys)
	./test/integration/test-write-file.sh
	./test/integration/test-anthropic-write-file.sh
	./test/integration/test-k8s-ops-nodes.sh
	./test/integration/test-telegram-channel.sh
	./test/integration/test-slack-channel.sh

vet: ## Run go vet
	$(GOVET) ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

fmt: ## Run gofmt
	gofmt -s -w .

tidy: ## Run go mod tidy
	$(GOMOD) tidy

##@ Code Generation

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Install controller-gen locally
$(CONTROLLER_GEN):
	@mkdir -p $(LOCALBIN)
	GOBIN=$(LOCALBIN) $(GOCMD) install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

generate: controller-gen ## Generate code (deepcopy, CRD manifests)
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/..."
	$(CONTROLLER_GEN) rbac:roleName=sympozium-manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases
	@$(MAKE) helm-sync

manifests: controller-gen ## Generate CRD manifests
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:artifacts:config=config/crd/bases
	@$(MAKE) helm-sync

##@ Web UI

web-install: ## Install frontend dependencies
	cd web && npm ci

web-build: web-install ## Build the frontend for embedding
	cd web && npm run build

web-dev: ## Start the frontend dev server (hot-reload, proxy to :8080)
	cd web && npm run dev

web-dev-serve: ## Vite hot-reload + port-forward to in-cluster apiserver (no rebuild needed)
	@echo "==> Port-forwarding sympozium-apiserver to localhost:8080"
	@echo "==> Vite dev server on http://localhost:$(VITE_PORT) (proxies /api + /ws to :8080)"
	@echo "==> Edit web/src/ and changes hot-reload instantly."
	@echo ""
	@trap 'kill 0' EXIT; \
		kubectl port-forward -n $(SYMPOZIUM_NAMESPACE) svc/sympozium-apiserver 8080:8080 & \
		cd web && npx vite --port $(VITE_PORT); \
		wait

web-clean: ## Remove frontend build artifacts
	rm -rf web/dist web/node_modules

##@ Local Development

SYMPOZIUM_TOKEN ?= dev-token
SYMPOZIUM_NAMESPACE ?= sympozium-system
API_ADDR ?= :8080
VITE_PORT ?= 5173

NATS_LOCAL_PORT ?= 4222

port-forward-nats: ## Port-forward NATS from the cluster to localhost:4222
	kubectl port-forward -n sympozium-system svc/nats $(NATS_LOCAL_PORT):4222

serve-api: build-apiserver ## Run the API server locally (connects to current kubeconfig cluster)
	SYMPOZIUM_UI_TOKEN=$(SYMPOZIUM_TOKEN) $(BIN_DIR)/apiserver \
		--addr $(API_ADDR) \
		--namespace $(SYMPOZIUM_NAMESPACE) \
		--serve-ui=false \
		--event-bus-url nats://localhost:$(NATS_LOCAL_PORT)

serve-api-ui: web-build build-apiserver ## Run the API server with embedded UI (production-like, no hot-reload)
	SYMPOZIUM_UI_TOKEN=$(SYMPOZIUM_TOKEN) $(BIN_DIR)/apiserver \
		--addr $(API_ADDR) \
		--namespace $(SYMPOZIUM_NAMESPACE) \
		--serve-ui=true \
		--event-bus-url nats://localhost:$(NATS_LOCAL_PORT)

dev: ## Start API server, Vite dev server, and NATS port-forward for rapid local iteration
	@echo "==> Starting API server on $(API_ADDR), Vite dev server on :$(VITE_PORT), NATS port-forward on :$(NATS_LOCAL_PORT)"
	@echo "==> Open http://localhost:$(VITE_PORT) in your browser"
	@echo "==> API token: $(SYMPOZIUM_TOKEN)"
	@echo ""
	$(MAKE) -j3 port-forward-nats serve-api web-dev

##@ Docker

docker-build: $(addprefix docker-build-,$(IMAGES)) ## Build all Docker images

docker-build-%: ## Build a specific Docker image
	docker build --build-arg IMAGE_TAG=$(TAG) -t $(REGISTRY)/$*:$(TAG) -f images/$*/Dockerfile .

docker-push: $(addprefix docker-push-,$(IMAGES)) ## Push all Docker images

docker-push-%: ## Push a specific Docker image
	docker push $(REGISTRY)/$*:$(TAG)

KIND_CLUSTER ?= kind

kind-load: $(addprefix kind-load-,$(IMAGES)) ## Load all Docker images into Kind

kind-load-%: ## Load a specific image into Kind (e.g., make kind-load-controller)
	kind load docker-image $(REGISTRY)/$*:$(TAG) --name $(KIND_CLUSTER)

kind-reload: docker-build kind-load ## Build all images and load into Kind
	kubectl rollout restart deployment sympozium-controller-manager -n sympozium-system

set-images: ## Stamp REGISTRY/TAG into K8s manifests
	cd config/manager && kustomize edit set image \
		controller=$(REGISTRY)/controller:$(TAG) \
		apiserver=$(REGISTRY)/apiserver:$(TAG)
	cd config/webhook && kustomize edit set image \
		webhook=$(REGISTRY)/webhook:$(TAG)
	@echo "Images set to $(REGISTRY)/*:$(TAG)"

##@ Deployment

install: manifests ## Install CRDs, skills, personas, and policies into the K8s cluster
	kubectl apply -f config/crd/bases/
	kubectl create namespace sympozium-system --dry-run=client -o yaml | kubectl apply -f -
	kubectl apply -f config/skills/
	kubectl apply -f config/personas/
	kubectl apply -f config/policies/

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

##@ Helm

helm-sync: ## Sync CRDs and appVersion into the Helm chart
	@echo "Syncing CRDs to charts/sympozium/crds/..."
	@mkdir -p charts/sympozium/crds
	cp config/crd/bases/*.yaml charts/sympozium/crds/
	@echo "Done."

helm-sync-check: ## Check that Helm chart CRDs are in sync (CI use)
	@diff -qr config/crd/bases/ charts/sympozium/crds/ > /dev/null 2>&1 \
		|| (echo "ERROR: Helm chart CRDs are out of sync. Run 'make helm-sync'" && exit 1)
	@echo "Helm chart CRDs are in sync."

helm-lint: ## Lint the Helm chart
	helm lint charts/sympozium/

##@ Clean

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)
	rm -f coverage.out
