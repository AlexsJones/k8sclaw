# K8sClaw
---
<p align="center">
  <img src="icon.svg" alt="k8sclaw icon" width="128" height="128">
</p>

**Kubernetes-native AI Agent Management Platform**

K8sClaw decomposes a monolithic AI agent gateway into a multi-tenant, horizontally scalable system where every sub-agent runs as an ephemeral Kubernetes pod.

### Quick install (macOS / Linux)

```bash
curl -fsSL https://deploy.k8sclaw.ai/install.sh | sh
```

Downloads the latest CLI binary from GitHub and installs it to `/usr/local/bin` (or `~/.local/bin`).

### Deploy to your cluster

```bash
k8sclaw install
```

That's it. This downloads the release manifests and applies CRDs, controllers, webhook, RBAC, and network policies to your current `kubectl` context.

To remove:

```bash
k8sclaw uninstall
```

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                  Control Plane                       │
│  ┌──────────┐  ┌──────────┐  ┌────────────────────┐│
│  │Controller │  │   API    │  │     Admission      ││
│  │ Manager   │  │  Server  │  │     Webhook        ││
│  └────┬─────┘  └────┬─────┘  └────────────────────┘│
│       │              │                               │
│  ┌────┴──────────────┴────┐                         │
│  │    NATS Event Bus      │                         │
│  └────────────────────────┘                         │
│                                                      │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐          │
│  │ Telegram │  │  Slack   │  │ Discord  │  ...      │
│  │ Channel  │  │ Channel  │  │ Channel  │          │
│  └──────────┘  └──────────┘  └──────────┘          │
│                                                      │
│  ┌──────────────────────────────────────┐           │
│  │         Agent Pods (ephemeral)        │          │
│  │  ┌─────────┐ ┌───────┐ ┌──────────┐ │          │
│  │  │  Agent  │ │  IPC  │ │ Sandbox  │ │          │
│  │  │Container│ │Bridge │ │(optional)│ │          │
│  │  └─────────┘ └───────┘ └──────────┘ │          │
│  └──────────────────────────────────────┘           │
└─────────────────────────────────────────────────────┘
```

## Custom Resources

| CRD | Description |
|-----|-------------|
| `ClawInstance` | Per-user/per-tenant gateway configuration |
| `AgentRun` | Ephemeral agent execution (maps to a K8s Job) |
| `ClawPolicy` | Feature and tool gating policy |
| `SkillPack` | Portable skill bundles (generates ConfigMaps) |

## Project Structure

```
k8sclaw/
├── api/v1alpha1/           # CRD type definitions
├── cmd/                    # Binary entry points
│   ├── controller/         # Controller manager
│   ├── apiserver/          # HTTP + WebSocket API server
│   ├── ipc-bridge/         # IPC bridge sidecar
│   ├── webhook/            # Admission webhook
│   └── k8sclaw/            # CLI tool
├── internal/               # Internal packages
│   ├── controller/         # Kubernetes controllers
│   ├── orchestrator/       # Agent pod builder & spawner
│   ├── apiserver/          # API server handlers
│   ├── eventbus/           # NATS JetStream event bus
│   ├── ipc/                # IPC bridge (fsnotify + NATS)
│   ├── webhook/            # Policy enforcement webhooks
│   ├── session/            # Session persistence (PostgreSQL)
│   └── channel/            # Channel base types
├── channels/               # Channel pod implementations
│   ├── telegram/
│   ├── whatsapp/
│   ├── discord/
│   └── slack/
├── images/                 # Dockerfiles
├── config/                 # Kubernetes manifests
│   ├── crd/bases/          # CRD YAML definitions
│   ├── manager/            # Controller + API server deployment
│   ├── rbac/               # ServiceAccount, ClusterRole, bindings
│   ├── webhook/            # Webhook deployment + configuration
│   ├── network/            # NetworkPolicy for agent isolation
│   └── samples/            # Example custom resources
├── migrations/             # PostgreSQL schema migrations
├── docs/                   # Design documentation
├── go.mod
├── Makefile
└── README.md
```

## Prerequisites

- Kubernetes cluster (v1.28+)
- NATS with JetStream enabled
- PostgreSQL with pgvector extension
- `kubectl` configured to your cluster

## Quick Start

### Install the CLI

```bash
# macOS / Linux
curl -fsSL https://deploy.k8sclaw.ai/install.sh | sh

# Or build from source
make build-k8sclaw
```

### Deploy K8sClaw

```bash
# Install to your cluster
k8sclaw install

# Or install a specific version
k8sclaw install --version v0.1.0

# Create a sample ClawInstance
kubectl apply -f https://raw.githubusercontent.com/AlexsJones/k8sclaw/main/config/samples/clawinstance_sample.yaml
```

### Use the CLI

```bash
# List instances
k8sclaw instances list

# List agent runs
k8sclaw runs list

# Enable a feature gate
k8sclaw features enable browser-automation --policy default-policy

# List feature gates
k8sclaw features list --policy default-policy
```

### Remove K8sClaw

```bash
k8sclaw uninstall
```

## Development

```bash
# Run tests
make test

# Run linter
make lint

# Generate CRD manifests
make manifests

# Run the controller locally (requires kubeconfig)
make run
```

## Key Design Decisions

- **Ephemeral Agent Pods**: Each agent run creates a Kubernetes Job with a pod containing the agent container, IPC bridge sidecar, and optional sandbox sidecar
- **IPC via filesystem**: Agent ↔ control plane communication uses filesystem-based IPC (`/ipc` volume) watched by the bridge sidecar, enabling language-agnostic agent implementations
- **NATS JetStream**: Used as the event bus for decoupled inter-component communication with durable subscriptions
- **NetworkPolicy isolation**: Agent pods run with deny-all network policies; only the IPC bridge sidecar connects to the event bus
- **Policy-as-CRD**: ClawPolicy resources gate tool access, sandbox requirements, and feature flags, enforced by admission webhooks

## Configuration

### Environment Variables

| Variable | Component | Description |
|----------|-----------|-------------|
| `EVENT_BUS_URL` | All | NATS server URL |
| `DATABASE_URL` | API Server | PostgreSQL connection string |
| `INSTANCE_NAME` | Channels | Owning ClawInstance name |
| `TELEGRAM_BOT_TOKEN` | Telegram | Bot API token |
| `SLACK_BOT_TOKEN` | Slack | Bot OAuth token |
| `DISCORD_BOT_TOKEN` | Discord | Bot token |
| `WHATSAPP_ACCESS_TOKEN` | WhatsApp | Cloud API access token |

## License

Apache License 2.0
