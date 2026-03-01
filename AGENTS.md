# AGENTS.md — Contributor Guide for AI Agents

This file helps AI coding agents (Copilot, Cursor, Cline, etc.) understand the Sympozium project structure and development workflow.

---

## Project Overview

Sympozium is a **Kubernetes-native agent orchestration platform** written in Go. Every AI agent runs as an ephemeral Kubernetes pod (Job), with policy enforcement via CRDs, admission webhooks, and RBAC. Communication flows through NATS JetStream and a filesystem-based IPC bridge.

- **Language:** Go 1.25+
- **Module:** `github.com/alexsjones/sympozium`
- **K8s API version:** `sympozium.ai/v1alpha1`

---

## Repository Layout

```
api/v1alpha1/           # CRD type definitions (SympoziumInstance, AgentRun, SympoziumPolicy, SkillPack, SympoziumSchedule, PersonaPack)
cmd/
  agent-runner/         # Agent container — LLM loop + tool execution
  apiserver/            # HTTP + WebSocket API server
  controller/           # Controller manager (all reconcilers + routers)
  ipc-bridge/           # IPC bridge sidecar (fsnotify → NATS)
  sympozium/             # CLI + TUI (Bubble Tea)
  webhook/              # Admission webhook server
channels/
  telegram/             # Channel pod — Telegram bot
  slack/                # Channel pod — Slack (Socket Mode + Events API)
  discord/              # Channel pod — Discord bot
  whatsapp/             # Channel pod — WhatsApp
config/
  crd/bases/            # Generated CRD YAML manifests
  manager/              # Controller manager deployment
  personas/             # Built-in PersonaPack YAML definitions
  rbac/                 # RBAC roles
  samples/              # Sample CR YAML files
  skills/               # Built-in SkillPack YAML definitions
  policies/             # Built-in SympoziumPolicy presets
  webhook/              # Webhook configuration
images/                 # Dockerfiles for all components
internal/
  apiserver/            # API server implementation
  channel/              # Channel types
  controller/           # Reconcilers (AgentRun, SympoziumInstance, SympoziumPolicy, SympoziumSchedule, SkillPack, PersonaPack) + routers (Channel, Schedule)
  eventbus/             # NATS JetStream client + topic constants
  ipc/                  # IPC bridge (fsnotify watcher, protocol, file handlers)
  orchestrator/         # Pod builder + spawner for agent Jobs
  session/              # Session store
  webhook/              # Policy enforcer
migrations/             # PostgreSQL schema migrations
test/integration/       # Integration test scripts (shell)
docs/                   # Design & contributor documentation
```

---

## Key CRDs

| CRD | Purpose |
|-----|---------|
| `SympoziumInstance` | An agent identity — provider config, model, enabled skills, channel bindings |
| `AgentRun` | A single agent invocation — task, result, phase lifecycle |
| `SympoziumPolicy` | Policy rules enforced by the admission webhook |
| `SkillPack` | Bundled skills (Markdown instructions) + optional sidecar container + RBAC |
| `SympoziumSchedule` | Cron-based recurring AgentRun creation (heartbeat, scheduled, sweep) |
| `PersonaPack` | Pre-configured agent bundles — stamps out Instances, Schedules, and memory automatically |

Type definitions live in `api/v1alpha1/`. After modifying types, regenerate with:

```bash
make generate    # deepcopy + CRD manifests
make manifests   # CRD YAML only
```

---

## Development Environment Setup

### Prerequisites

- Go 1.25+
- Docker
- [Kind](https://kind.sigs.k8s.io/) (Kubernetes in Docker)
- kubectl
- An LLM API key (e.g. `OPENAI_API_KEY`)

### Create a Kind Cluster & Install Sympozium

```bash
# Create cluster
kind create cluster --name kind

# Install CRDs
make install

# Build all images
make docker-build TAG=v0.0.32

# Load images into Kind (all components)
for img in controller apiserver ipc-bridge webhook agent-runner \
           channel-telegram channel-slack channel-discord channel-whatsapp \
           skill-k8s-ops skill-sre-observability; do
  kind load docker-image ghcr.io/alexsjones/sympozium/$img:v0.0.32 --name kind
done

# Deploy the control plane
kubectl apply -k config/
```

### Build & Test Cycle

After code changes:

```bash
# Build everything
make build

# Run unit tests
make test

# Build specific image + reload into Kind
make docker-build-agent-runner TAG=v0.0.32
kind load docker-image ghcr.io/alexsjones/sympozium/agent-runner:v0.0.32 --name kind

# Restart the controller to pick up new images
kubectl rollout restart deployment sympozium-controller-manager -n sympozium-system
```

### Common Build Targets

```bash
make build              # Build all binaries
make test               # Run unit tests with race detector
make test-short         # Run short tests only
make test-integration   # Run all integration tests (requires Kind + API key)
make vet                # go vet
make fmt                # gofmt
make tidy               # go mod tidy
make docker-build       # Build all Docker images
make docker-build-<name> TAG=v0.0.32   # Build a specific image
make generate           # Regenerate deepcopy + CRD manifests
make manifests          # Regenerate CRD YAML only
make clean              # Remove build artifacts
```

---

## Integration Tests

Integration tests live in `test/integration/` and run against a real Kind cluster with a real LLM.

### Running Tests

```bash
# All integration tests
make test-integration

# Single test
./test/integration/test-write-file.sh

# Override model or timeout
TEST_MODEL=gpt-5.2 TEST_TIMEOUT=180 ./test/integration/test-write-file.sh
```

### Existing Tests

| Test | What it validates |
|------|-------------------|
| `test-write-file.sh` | `write_file` tool — agent writes a file, script verifies content |
| `test-anthropic-write-file.sh` | `write_file` tool using Anthropic provider — validates provider parity |
| `test-k8s-ops-nodes.sh` | `k8s-ops` skill — agent runs kubectl via sidecar |
| `test-telegram-channel.sh` | Telegram channel deployment + message flow |
| `test-slack-channel.sh` | Slack channel deployment (Socket Mode) |

### Writing New Tests

See `docs/writing-integration-tests.md` for the full guide and template. Tests follow this pattern:

1. Create a `SympoziumInstance` + `AgentRun` with a deterministic task
2. Poll `status.phase` until `Succeeded` or `Failed`
3. Validate results (pod logs, status, filesystem)
4. Clean up all test resources

Add new tests to the `test-integration` target in the `Makefile`.

---

## Agent Tools

The agent-runner has 7 built-in tools defined in `cmd/agent-runner/tools.go`:

| Tool | Category | Description |
|------|----------|-------------|
| `execute_command` | IPC (sidecar) | Run shell commands in the skill sidecar |
| `read_file` | Native | Read file contents |
| `write_file` | Native | Write/create files |
| `list_directory` | Native | List directory contents |
| `send_channel_message` | IPC (bridge) | Send messages to Telegram/Slack/Discord/WhatsApp |
| `fetch_url` | Native | HTTP GET a URL and return the body |
| `schedule_task` | IPC (bridge) | Create/update/suspend/resume/delete SympoziumSchedule CRDs |

See `docs/writing-tools.md` for the full guide on adding new tools.

---

## Key Architecture Patterns

### IPC Flow (Agent ↔ Control Plane)

```
Agent tool writes JSON → /ipc/<dir>/*.json → fsnotify watcher → NATS publish → Controller handles
```

Directories: `/ipc/tools/` (sidecar exec), `/ipc/messages/` (channel messages), `/ipc/schedules/` (schedule requests).

### Event Bus (NATS Topics)

Key topics in `internal/eventbus/types.go`:

- `agent.run.requested/started/completed/failed` — AgentRun lifecycle
- `channel.message.received/send` — Channel message flow
- `schedule.upsert` — Agent self-scheduling requests
- `tool.exec.request/result` — Sidecar tool execution

### Memory

Each SympoziumInstance has a ConfigMap (`<name>-memory`) mounted at `/memory/MEMORY.md`. The controller extracts memory markers (`__SYMPOZIUM_MEMORY__...__SYMPOZIUM_MEMORY_END__`) from agent output and patches the ConfigMap.

### Skills

SkillPacks are CRDs containing Markdown instructions + optional sidecar definitions. When enabled on a SympoziumInstance, skills are mounted at `/skills/` and sidecars are injected into agent pods. See `docs/writing-skills.md`.

---

## Documentation Index

| Document | Location | Content |
|----------|----------|---------|
| Design document | `docs/sympozium-design.md` | Full architecture, CRD schemas, data flow, security model |
| Writing tools | `docs/writing-tools.md` | How to add new agent tools |
| Writing skills | `docs/writing-skills.md` | How to create SkillPack CRDs |
| Writing integration tests | `docs/writing-integration-tests.md` | Test patterns and templates |
| Sample CRs | `config/samples/` | Example SympoziumInstance, AgentRun, SympoziumPolicy, SympoziumSchedule, SkillPack |
| CRD definitions | `api/v1alpha1/` | Go type definitions for all CRDs |
| Built-in PersonaPacks | `config/personas/` | Pre-configured agent bundles (platform-team, devops-essentials) |

---

## Common Tasks for Agents

### Adding a new tool
1. Add constant + definition + handler in `cmd/agent-runner/tools.go`
2. If IPC-based, add watcher in `internal/ipc/bridge.go` and topic in `internal/eventbus/types.go`
3. If it needs a controller handler, add a router in `internal/controller/`
4. Rebuild `agent-runner` (and `ipc-bridge`/`controller` if changed)
5. Write an integration test in `test/integration/`
6. Document in `docs/writing-tools.md`

### Adding a new channel
1. Create `channels/<name>/main.go`
2. Create `images/channel-<name>/Dockerfile`
3. Add to `CHANNELS` list in `Makefile`
4. The controller's `buildChannelDeployment` in `internal/controller/sympoziuminstance_controller.go` handles deployment

### Modifying a CRD
1. Edit type in `api/v1alpha1/<name>_types.go`
2. Run `make generate` to regenerate deepcopy and CRD YAML
3. Run `make install` to apply updated CRDs to cluster
4. Update the reconciler in `internal/controller/`

### Adding a PersonaPack
1. Create a YAML file in `config/personas/<name>.yaml`
2. Define personas with system prompts, skills, schedules, and memory seeds
3. Apply: `kubectl apply -f config/personas/<name>.yaml`
4. Activate via the TUI Personas tab or by patching `spec.authRefs` with kubectl

### Rebuilding after changes
```bash
# Compile check
go build ./...

# Rebuild affected images
make docker-build-<component> TAG=v0.0.32

# Load into Kind
kind load docker-image ghcr.io/alexsjones/sympozium/<component>:v0.0.32 --name kind

# Restart controller if controller/ipc-bridge/agent-runner changed
kubectl rollout restart deployment sympozium-controller-manager -n sympozium-system
```
