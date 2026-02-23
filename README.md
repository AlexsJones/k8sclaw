# K8sClaw
---
<p align="center">
  <img src="icon.svg" alt="llmfit icon" width="128" height="128">
</p>


**Kubernetes-native AI Agent Management Platform**

K8sClaw decomposes a monolithic AI agent gateway into a multi-tenant, horizontally scalable system where every sub-agent runs as an ephemeral Kubernetes pod.

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

- Go 1.23+
- Docker
- Kubernetes cluster (v1.28+)
- NATS with JetStream enabled
- PostgreSQL with pgvector extension

## Quick Start

### Build

```bash
# Build all binaries
make build

# Build all Docker images
make docker-build

# Build a specific component
make build-controller
make build-apiserver
```

### Deploy

```bash
# Install CRDs
make install

# Deploy to cluster
make deploy

# Create a sample ClawInstance
kubectl apply -f config/samples/clawinstance_sample.yaml
```

### CLI

```bash
# Build the CLI
make build-k8sclaw

# List instances
k8sclaw instances list

# List agent runs
k8sclaw runs list

# Enable a feature gate
k8sclaw features enable browser-automation --policy default-policy

# List feature gates
k8sclaw features list --policy default-policy
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
