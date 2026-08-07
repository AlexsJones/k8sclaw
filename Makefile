# Sympozium Makefile
# Kubernetes-native agent orchestration platform

# Image registry — matches ghcr.io/<owner>/<repo>/<image>
REGISTRY ?= ghcr.io/sympozium-ai/sympozium
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
BINARIES = controller apiserver ipc-bridge webhook agent-runner sympozium web-proxy node-probe

# All channel binaries
CHANNELS = telegram whatsapp discord slack

# All images
IMAGES = controller apiserver ipc-bridge webhook agent-runner web-proxy node-probe \
         channel-telegram channel-whatsapp channel-discord channel-slack \
		 skill-k8s-ops skill-sre-observability skill-github-gitops skill-llmfit skill-memory \
		 llmfit-daemon mcp-bridge

.PHONY: all build test clean generate manifests docker-build docker-push install help web-build web-dev web-dev-serve web-clean web-install setup-hooks integration-tests ux-tests tail-agents

all: build

##@ General

setup-hooks: ## Configure git to use .githooks (enables pre-commit formatting check)
	git config core.hooksPath .githooks

help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

build: $(addprefix build-,$(BINARIES)) $(addprefix build-channel-,$(CHANNELS)) ## Build all binaries

build-channel-%: ## Build a specific channel binary
	$(GOBUILD) -o $(BIN_DIR)/channel-$* ./channels/$*/

build-%: ## Build a specific binary (e.g., make build-controller)
	$(GOBUILD) -o $(BIN_DIR)/$* ./cmd/$*/

test: ## Run tests
	$(GOTEST) -race -coverprofile=coverage.out ./...

test-short: ## Run short tests
	$(GOTEST) -short ./...

test-integration: ## Run integration tests (requires Kind cluster + API keys)
	./test/integration/test-write-file.sh
	./test/integration/test-anthropic-write-file.sh
	./test/integration/test-k8s-ops-nodes.sh
	./test/integration/test-llmfit-cluster-fit.sh
	./test/integration/test-telegram-channel.sh
	./test/integration/test-slack-channel.sh
	./test/integration/test-mcp-bridge.sh
	./test/integration/test-lifecycle-hooks.sh

integration-tests: ## Run API smoke regression tests (Ensembles, ad-hoc Instances, Skills, Policies, Schedules)
	bash ./test/integration/test-api-smoke.sh
	bash ./test/integration/test-api-ensemble-provider-switch.sh
	bash ./test/integration/test-api-ensemble-adhoc-correctness.sh
	bash ./test/integration/test-api-agentrun-container-shape.sh
	bash ./test/integration/test-api-ensemble-provisioning.sh
	bash ./test/integration/test-api-schedule-dispatch.sh
	bash ./test/integration/test-api-observability.sh
	bash ./test/integration/test-api-web-endpoint.sh
	bash ./test/integration/test-api-serving-mode.sh
	bash ./test/integration/test-api-capabilities.sh

UX_PORT ?= $(shell lsof -ti :5173 >/dev/null 2>&1 && echo 5173 || (lsof -ti :5174 >/dev/null 2>&1 && echo 5174 || echo 5173))
SERVE_PORT ?= 9090
CYPRESS_TEST_MODEL ?= qwen/qwen3.5-9b
CYPRESS_SPEC ?=

# Build --spec flag only when CYPRESS_SPEC is set
_CYPRESS_SPEC_FLAG := $(if $(CYPRESS_SPEC),--spec "$(CYPRESS_SPEC)",)

ux-tests: web-install ## Run Cypress UX tests against Vite dev server (make web-dev-serve)
	$(eval API_TOKEN := $(shell kubectl get secret -n sympozium-system sympozium-ui-token -o jsonpath='{.data.token}' 2>/dev/null | base64 -d))
	@./hack/check-ux-backend.sh $(UX_PORT) "$(API_TOKEN)"
	@./hack/check-llm-ready.sh $(CYPRESS_TEST_MODEL)
	cd web && CYPRESS_BASE_URL=http://localhost:$(UX_PORT) CYPRESS_API_TOKEN=$(API_TOKEN) CYPRESS_TEST_MODEL=$(CYPRESS_TEST_MODEL) npx cypress run $(_CYPRESS_SPEC_FLAG)

ux-tests-open: web-install ## Open Cypress interactive runner against Vite dev server (make web-dev-serve)
	$(eval API_TOKEN := $(shell kubectl get secret -n sympozium-system sympozium-ui-token -o jsonpath='{.data.token}' 2>/dev/null | base64 -d))
	@./hack/check-ux-backend.sh $(UX_PORT) "$(API_TOKEN)"
	@./hack/check-llm-ready.sh $(CYPRESS_TEST_MODEL)
	cd web && CYPRESS_BASE_URL=http://localhost:$(UX_PORT) CYPRESS_API_TOKEN=$(API_TOKEN) CYPRESS_TEST_MODEL=$(CYPRESS_TEST_MODEL) npx cypress open

ux-tests-serve: web-install ## Run Cypress UX tests against `sympozium serve` (port 9090 by default; override SERVE_PORT)
	$(eval API_TOKEN := $(shell kubectl get secret -n sympozium-system sympozium-ui-token -o jsonpath='{.data.token}' 2>/dev/null | base64 -d))
	@./hack/check-ux-backend.sh $(SERVE_PORT) "$(API_TOKEN)"
	@./hack/check-llm-ready.sh $(CYPRESS_TEST_MODEL)
	cd web && CYPRESS_BASE_URL=http://localhost:$(SERVE_PORT) CYPRESS_API_TOKEN=$(API_TOKEN) CYPRESS_TEST_MODEL=$(CYPRESS_TEST_MODEL) npx cypress run $(_CYPRESS_SPEC_FLAG)

ux-tests-serve-open: web-install ## Open Cypress interactive runner against `sympozium serve` (port 9090 by default)
	$(eval API_TOKEN := $(shell kubectl get secret -n sympozium-system sympozium-ui-token -o jsonpath='{.data.token}' 2>/dev/null | base64 -d))
	@./hack/check-ux-backend.sh $(SERVE_PORT) "$(API_TOKEN)"
	@./hack/check-llm-ready.sh $(CYPRESS_TEST_MODEL)
	cd web && CYPRESS_BASE_URL=http://localhost:$(SERVE_PORT) CYPRESS_API_TOKEN=$(API_TOKEN) CYPRESS_TEST_MODEL=$(CYPRESS_TEST_MODEL) npx cypress open

test-web-proxy: ## Run web-proxy HTTP API tests (requires a running web-endpoint service)
	bash ./test/integration/test-web-proxy-api.sh

vet: ## Run go vet
	$(GOVET) ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

fmt: ## Run gofmt
	gofmt -s -w .

tidy: ## Run go mod tidy
	$(GOMOD) tidy

##@ Code Generation

ENVTEST ?= $(LOCALBIN)/setup-envtest
ENVTEST_K8S_VERSION ?= 1.31.0

.PHONY: envtest
envtest: $(ENVTEST) ## Install setup-envtest locally
$(ENVTEST):
	@mkdir -p $(LOCALBIN)
	GOBIN=$(LOCALBIN) $(GOCMD) install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

test-system: envtest ## Run system tests (envtest — no cluster needed, fast)
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
	$(GOCMD) test -tags system ./test/system/ -v -count=1 -timeout 120s

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Install controller-gen locally
$(CONTROLLER_GEN):
	@mkdir -p $(LOCALBIN)
	GOBIN=$(LOCALBIN) $(GOCMD) install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

generate: controller-gen ## Generate code (deepcopy, CRD manifests)
	GOFLAGS=-mod=mod $(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/..."
	GOFLAGS=-mod=mod $(CONTROLLER_GEN) crd paths="./..." output:crd:artifacts:config=config/crd/bases
	@$(MAKE) helm-sync

manifests: controller-gen ## Generate CRD manifests
	GOFLAGS=-mod=mod $(CONTROLLER_GEN) crd paths="./api/..." output:crd:artifacts:config=config/crd/bases
	@$(MAKE) helm-sync

##@ Web UI

web-install: ## Install frontend dependencies
	cd web && npm ci

web-build: web-install ## Build the frontend for embedding
	cd web && npm run build

web-dev: web-install ## Start the frontend dev server (hot-reload, proxy to :8080)
	cd web && npm run dev

web-dev-serve: web-install ## Vite hot-reload + port-forward to in-cluster apiserver (no rebuild needed)
	@set -e; \
	echo "==> Checking cluster connectivity..."; \
	if ! kubectl cluster-info >/dev/null 2>&1; then \
		echo "ERROR: Cannot reach Kubernetes cluster. Is your Kind cluster running?"; \
		echo "  Try: kind get clusters"; \
		exit 1; \
	fi; \
	if ! kubectl get svc -n $(SYMPOZIUM_NAMESPACE) sympozium-apiserver >/dev/null 2>&1; then \
		echo "ERROR: Service sympozium-apiserver not found in namespace $(SYMPOZIUM_NAMESPACE)."; \
		echo "  Is Sympozium installed? Try: make install"; \
		exit 1; \
	fi; \
	APISERVER_TOKEN=$$( \
		kubectl get deploy -n $(SYMPOZIUM_NAMESPACE) sympozium-apiserver -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="SYMPOZIUM_UI_TOKEN")].value}' 2>/dev/null \
	); \
	if [ -z "$$APISERVER_TOKEN" ]; then \
		SECRET_NAME=$$(kubectl get deploy -n $(SYMPOZIUM_NAMESPACE) sympozium-apiserver -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="SYMPOZIUM_UI_TOKEN")].valueFrom.secretKeyRef.name}' 2>/dev/null); \
		SECRET_KEY=$$(kubectl get deploy -n $(SYMPOZIUM_NAMESPACE) sympozium-apiserver -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="SYMPOZIUM_UI_TOKEN")].valueFrom.secretKeyRef.key}' 2>/dev/null); \
		if [ -z "$$SECRET_KEY" ]; then SECRET_KEY=token; fi; \
		if [ -n "$$SECRET_NAME" ]; then \
			APISERVER_TOKEN=$$(kubectl get secret -n $(SYMPOZIUM_NAMESPACE) "$$SECRET_NAME" -o jsonpath="{.data.$$SECRET_KEY}" 2>/dev/null | base64 -d 2>/dev/null); \
		fi; \
	fi; \
	if [ -z "$$APISERVER_TOKEN" ]; then \
		echo "ERROR: Could not resolve API token from apiserver deployment."; \
		echo "  Check: kubectl get deploy -n $(SYMPOZIUM_NAMESPACE) sympozium-apiserver -o yaml | grep SYMPOZIUM_UI_TOKEN"; \
		exit 1; \
	fi; \
	STALE_PID=$$(lsof -ti tcp:$(API_LOCAL_PORT) 2>/dev/null || true); \
	if [ -n "$$STALE_PID" ]; then \
		echo "==> Killing stale process on port $(API_LOCAL_PORT) (pid $$STALE_PID)"; \
		kill $$STALE_PID 2>/dev/null; sleep 1; \
		kill -9 $$STALE_PID 2>/dev/null || true; \
	fi; \
	echo ""; \
	echo "============================================"; \
	echo "  Sympozium Web Dev Server"; \
	echo "============================================"; \
	echo "  UI:     http://localhost:$(VITE_PORT)"; \
	echo "  API:    localhost:$(API_LOCAL_PORT) (port-forward)"; \
	echo "  Token:  $$APISERVER_TOKEN"; \
	echo "============================================"; \
	echo ""; \
	PF_LOG=/tmp/sympozium-web-dev-serve-portforward.log; \
	rm -f $$PF_LOG; \
	trap 'kill 0' EXIT; \
	kubectl port-forward -n $(SYMPOZIUM_NAMESPACE) --address 127.0.0.1 svc/sympozium-apiserver $(API_LOCAL_PORT):8080 >$$PF_LOG 2>&1 & \
	PF_PID=$$!; \
	READY=0; \
	for i in $$(seq 1 30); do \
		if ! kill -0 $$PF_PID >/dev/null 2>&1; then \
			echo "ERROR: kubectl port-forward exited early."; \
			echo "---- port-forward log ----"; \
			cat $$PF_LOG; \
			echo "--------------------------"; \
			exit 1; \
		fi; \
		if curl -fsS http://127.0.0.1:$(API_LOCAL_PORT)/healthz >/dev/null 2>&1; then \
			READY=1; \
			break; \
		fi; \
		sleep 1; \
	done; \
	if [ $$READY -ne 1 ]; then \
		echo "ERROR: Timed out waiting for apiserver on localhost:$(API_LOCAL_PORT)."; \
		echo "---- port-forward log ----"; \
		cat $$PF_LOG; \
		echo "--------------------------"; \
		exit 1; \
	fi; \
	echo "==> Port-forward ready."; \
	echo ""; \
	cd web && API_LOCAL_PORT=$(API_LOCAL_PORT) npm run dev -- --port $(VITE_PORT)

web-clean: ## Remove frontend build artifacts
	rm -rf web/dist web/node_modules

##@ Local Development

SYMPOZIUM_TOKEN ?= $(shell t=$$(kubectl get secret -n sympozium-system -l app.kubernetes.io/component=apiserver -o jsonpath='{.items[0].data.token}' 2>/dev/null | base64 -d 2>/dev/null); [ -n "$$t" ] && echo "$$t" || echo dev-token)
SYMPOZIUM_NAMESPACE ?= sympozium-system
API_ADDR ?= :8081
VITE_PORT ?= 5173

API_LOCAL_PORT ?= 8081
NATS_LOCAL_PORT ?= 4222

AGENT_NAMESPACE ?= default
TAIL_ARGS ?=

tail-agents: ## Follow the logs of every agent run, live (AGENT_NAMESPACE=default; TAIL_ARGS='--skills')
	@NAMESPACE=$(AGENT_NAMESPACE) hack/tail-agents.sh $(TAIL_ARGS); \
	code=$$?; \
	if [ $$code -eq 130 ] || [ $$code -eq 143 ]; then exit 0; fi; \
	exit $$code

port-forward-nats: ## Port-forward NATS from the cluster to localhost:4222
	kubectl port-forward -n sympozium-system --address 127.0.0.1 svc/nats $(NATS_LOCAL_PORT):4222

serve-api: build-apiserver ## Run the API server locally (connects to current kubeconfig cluster)
	@PID=$$(lsof -ti tcp:$(subst :,,$(API_ADDR)) 2>/dev/null); \
	if [ -n "$$PID" ]; then \
		echo "==> Killing stale process on port $(API_ADDR) (pid $$PID)"; \
		kill $$PID 2>/dev/null; sleep 1; \
		kill -9 $$PID 2>/dev/null || true; \
	fi
	@echo "==> Waiting for NATS on localhost:$(NATS_LOCAL_PORT)..."
	@for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do \
		nc -z localhost $(NATS_LOCAL_PORT) 2>/dev/null && break; \
		sleep 1; \
	done
	@echo "==> API server token: $(SYMPOZIUM_TOKEN)"
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

run-controller: build-controller ## Run the controller manager locally against the current kubeconfig cluster
	@echo "==> Scaling down in-cluster controller so local one takes over..."
	@kubectl scale deploy/sympozium-controller-manager -n $(SYMPOZIUM_NAMESPACE) --replicas=0 2>/dev/null || true
	@echo "==> Starting local controller manager (metrics :9090, health :9091)"
	@cleanup() { \
		echo ""; \
		echo "==> Restoring in-cluster controller..."; \
		kubectl scale deploy/sympozium-controller-manager -n $(SYMPOZIUM_NAMESPACE) --replicas=1 2>/dev/null || true; \
		echo "==> Done."; \
	}; \
	trap cleanup EXIT INT TERM; \
	$(BIN_DIR)/controller \
		--metrics-bind-address :9090 \
		--health-probe-bind-address :9091 \
		--nats-url nats://localhost:$(NATS_LOCAL_PORT)

dev: ## Start API server, Vite dev server, and NATS port-forward for rapid local iteration
	@echo "==> Starting API server on $(API_ADDR), Vite dev server on :$(VITE_PORT), NATS port-forward on :$(NATS_LOCAL_PORT)"
	@echo "==> Open http://localhost:$(VITE_PORT) in your browser"
	@echo "==> API token: $(SYMPOZIUM_TOKEN)"
	@echo ""
	$(MAKE) -j3 port-forward-nats serve-api web-dev

dev-all: ## Start everything locally: controller, API server, Vite, and NATS port-forward
	@echo ""
	@echo "============================================"
	@echo "  Sympozium Local Development"
	@echo "============================================"
	@echo "  UI:        http://localhost:$(VITE_PORT)"
	@echo "  API:       http://localhost$(API_ADDR)"
	@echo "  UI Token:  $(SYMPOZIUM_TOKEN)"
	@echo "============================================"
	@echo ""
	@echo "==> Ensuring NATS is running..."
	@kubectl scale deploy/nats -n $(SYMPOZIUM_NAMESPACE) --replicas=1 2>/dev/null || true
	@kubectl rollout status deploy/nats -n $(SYMPOZIUM_NAMESPACE) --timeout=60s 2>/dev/null || true
	@echo "==> Scaling down in-cluster controller and apiserver..."
	@kubectl scale deploy/sympozium-controller-manager -n $(SYMPOZIUM_NAMESPACE) --replicas=0 2>/dev/null || true
	@kubectl scale deploy/sympozium-apiserver -n $(SYMPOZIUM_NAMESPACE) --replicas=0 2>/dev/null || true
	@cleanup() { \
		echo ""; \
		echo "==> Restoring in-cluster deployments..."; \
		kubectl scale deploy/sympozium-controller-manager -n $(SYMPOZIUM_NAMESPACE) --replicas=1 2>/dev/null || true; \
		kubectl scale deploy/sympozium-apiserver -n $(SYMPOZIUM_NAMESPACE) --replicas=1 2>/dev/null || true; \
		echo "==> Done."; \
	}; \
	trap cleanup EXIT INT TERM; \
	$(MAKE) -j4 port-forward-nats serve-api web-dev run-controller-inner

run-controller-inner: build-controller
	@$(BIN_DIR)/controller \
		--metrics-bind-address :9090 \
		--health-probe-bind-address :9091 \
		--nats-url nats://localhost:$(NATS_LOCAL_PORT)

##@ Docker

DOCKER_PLATFORMS ?= linux/amd64,linux/arm64

docker-build: $(addprefix docker-build-,$(IMAGES)) ## Build all Docker images (native arch)

docker-build-%: ## Build a specific Docker image (native arch)
	docker buildx build --build-arg IMAGE_TAG=$(TAG) --load -t $(REGISTRY)/$*:$(TAG) -f images/$*/Dockerfile .

docker-buildx: $(addprefix docker-buildx-,$(IMAGES)) ## Build all Docker images for amd64+arm64

docker-buildx-%: ## Build and push a specific multi-arch image (e.g., make docker-buildx-controller)
	docker buildx build --build-arg IMAGE_TAG=$(TAG) --platform $(DOCKER_PLATFORMS) --push -t $(REGISTRY)/$*:$(TAG) -f images/$*/Dockerfile .

docker-push: $(addprefix docker-push-,$(IMAGES)) ## Push all Docker images

docker-push-%: ## Push a specific Docker image
	docker push $(REGISTRY)/$*:$(TAG)

KIND_CLUSTER ?= kind

kind-load: $(addprefix kind-load-,$(IMAGES)) ## Load all Docker images into Kind

kind-load-%: ## Load a specific image into Kind (e.g., make kind-load-controller)
	kind load docker-image $(REGISTRY)/$*:$(TAG) --name $(KIND_CLUSTER)

kind-reload: docker-build kind-load ## Build all images and load into Kind
	kubectl rollout restart deployment sympozium-controller-manager -n sympozium-system
	-kubectl rollout restart daemonset sympozium-node-probe -n sympozium-system 2>/dev/null
	-kubectl rollout restart daemonset sympozium-llmfit-daemon -n sympozium-system 2>/dev/null

##@ Deployment

GATEWAY_API_CRDS_URL ?= https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.1/standard-install.yaml

install: manifests ## Install Sympozium via Helm chart (CRDs, control plane, built-ins)
	kubectl apply --server-side --force-conflicts -f charts/sympozium/crds/
	@echo "Installing Gateway API CRDs..."
	kubectl apply --server-side --force-conflicts -f $(GATEWAY_API_CRDS_URL)
	helm upgrade --install sympozium charts/sympozium/ \
		--namespace sympozium-system --create-namespace \
		--set createNamespace=false \
		--set image.tag=$(TAG) \
		--set certManager.enabled=false \
		--set webhook.enabled=false \
		--set celln.enabled=true \
		--skip-crds

uninstall: ## Uninstall Sympozium (Helm release, CRDs, namespace)
	@set -eu; \
	echo "==> Removing finalizers from Sympozium resources"; \
	for crd in \
		sympoziumconfigs.sympozium.ai \
		personapacks.sympozium.ai \
		agents.sympozium.ai \
		sympoziumschedules.sympozium.ai \
		sympoziumpolicies.sympozium.ai \
		skillpacks.sympozium.ai \
		agentruns.sympozium.ai; do \
		if kubectl get crd "$$crd" >/dev/null 2>&1; then \
			kubectl get "$$crd" -A -o name 2>/dev/null | while read -r obj; do \
				[ -n "$$obj" ] || continue; \
				kubectl patch "$$obj" --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true; \
			done; \
			kubectl delete "$$crd" -A --all --ignore-not-found --timeout=60s >/dev/null 2>&1 || true; \
		fi; \
	done; \
	echo "==> Uninstalling Helm release"; \
	helm uninstall sympozium --namespace sympozium-system 2>/dev/null || true; \
	echo "==> Deleting Sympozium CRDs"; \
	kubectl delete -f charts/sympozium/crds/ --ignore-not-found >/dev/null 2>&1 || true; \
	echo "==> Removing Gateway API CRDs"; \
	kubectl delete --ignore-not-found -f $(GATEWAY_API_CRDS_URL) >/dev/null 2>&1 || true; \
	echo "==> Deleting namespace $(SYMPOZIUM_NAMESPACE)"; \
	kubectl delete namespace $(SYMPOZIUM_NAMESPACE) --ignore-not-found --timeout=120s >/dev/null 2>&1 || true

deploy: install ## Deploy Sympozium (alias for install)

undeploy: uninstall ## Undeploy Sympozium (alias for uninstall)

deploy-samples: ## Deploy sample CRs
	kubectl apply -f config/samples/

##@ Database

db-migrate: ## Run database migrations
	@echo "Running migrations against $${DATABASE_URL}"
	psql "$${DATABASE_URL}" -f migrations/001_initial.sql

##@ Helm

helm-sync: ## Sync CRDs and appVersion into the Helm charts
	@echo "Syncing CRDs to charts/sympozium/crds/..."
	@mkdir -p charts/sympozium/crds
	cp config/crd/bases/*.yaml charts/sympozium/crds/
	@echo "Syncing CRDs to charts/sympozium-crds/templates/..."
	@mkdir -p charts/sympozium-crds/templates
	@rm -f charts/sympozium-crds/templates/sympozium.ai_*.yaml
	cp config/crd/bases/*.yaml charts/sympozium-crds/templates/
	@echo "Done."

helm-sync-check: ## Check that Helm chart CRDs are in sync (CI use)
	@diff -qr config/crd/bases/ charts/sympozium/crds/ > /dev/null 2>&1 \
		|| (echo "ERROR: Helm chart CRDs are out of sync. Run 'make helm-sync'" && exit 1)
	@tmp=$$(mktemp -d); \
	cp config/crd/bases/*.yaml $$tmp/; \
	diff -qr $$tmp/ charts/sympozium-crds/templates/ \
		| grep -v ': NOTES.txt$$' \
		| grep . \
		&& (echo "ERROR: sympozium-crds chart templates are out of sync. Run 'make helm-sync'" && rm -rf $$tmp && exit 1) \
		|| true; \
	rm -rf $$tmp
	@echo "Helm chart CRDs are in sync."

helm-lint: ## Lint the Helm charts
	helm lint charts/sympozium/
	helm lint charts/sympozium-crds/

##@ Clean

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)
	rm -f coverage.out
