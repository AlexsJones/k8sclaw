# K8sClaw

<p align="center">
  <img src="icon.svg" alt="k8sclaw icon" width="128" height="128">
</p>

<p align="center">
  <strong>Kubernetes-native AI Agent Management Platform</strong><br>
  <em>Decompose monolithic AI agent gateways into multi-tenant, horizontally scalable systems where every sub-agent runs as an ephemeral Kubernetes pod.</em><br><br>
  From the creator of <a href="https://github.com/k8sgpt-ai/k8sgpt">k8sgpt</a> and <a href="https://github.com/AlexsJones/llmfit">llmfit</a>
</p>

<p align="center">
  <a href="https://github.com/AlexsJones/k8sclaw/actions"><img src="https://github.com/AlexsJones/k8sclaw/actions/workflows/build.yaml/badge.svg" alt="Build"></a>
  <a href="https://github.com/AlexsJones/k8sclaw/releases/latest"><img src="https://img.shields.io/github/v/release/AlexsJones/k8sclaw" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue" alt="License"></a>
</p>

---

### Quick Install (macOS / Linux)

```bash
curl -fsSL https://deploy.k8sclaw.ai/install.sh | sh
```

### Deploy to Your Cluster

```bash
k8sclaw install          # CRDs, controllers, webhook, NATS, RBAC, network policies
k8sclaw onboard          # interactive setup wizard — instance, provider, channel
k8sclaw                  # launch the interactive TUI (default command)
k8sclaw uninstall        # clean removal
```

## Interactive TUI

Running `k8sclaw` with no arguments launches a **k9s-style interactive terminal UI** with full cluster management.

### Views

| View | Description |
|------|-------------|
| Instances | ClawInstance list with status, channels, and agent config |
| Runs | AgentRun list with phase, duration, and associated instance |
| Channels | Channel pod status (Telegram, Slack, Discord, WhatsApp) |

### Keybindings

| Key | Action |
|-----|--------|
| `l` | View logs for the selected resource |
| `d` | Describe the selected resource (kubectl describe) |
| `x` | Delete the selected resource (with confirmation) |
| `R` | Switch to Runs view |
| `O` | Launch the onboard wizard |
| `Esc` | Go back / close panel |
| `?` | Toggle help |
| `Tab` | Cycle between views |
| `/` | Slash commands — `/run`, `/instances`, `/runs`, `/channels` |

### Slash Commands

| Command | Description |
|---------|-------------|
| `/run <task>` | Create and submit an AgentRun with the given task |
| `/instances` | Switch to Instances view |
| `/runs` | Switch to Runs view |
| `/channels` | Switch to Channels view |

## Architecture

```mermaid
graph TB
    subgraph CP["Control Plane"]
        CM[Controller Manager]
        API[API Server]
        WH[Admission Webhook]
        NATS[(NATS JetStream)]
        CM --- NATS
        API --- NATS
        WH -.- CM
    end

    subgraph CH["Channel Pods"]
        TG[Telegram]
        SL[Slack]
        DC[Discord]
        WA[WhatsApp]
    end

    subgraph AP["Agent Pods · ephemeral"]
        direction LR
        A1[Agent Container]
        IPC[IPC Bridge]
        SB[Sandbox]
    end

    TG & SL & DC & WA --- NATS
    NATS --- IPC
    A1 -. "/ipc volume" .- IPC
    A1 -. optional .- SB

    style CP fill:#1a1a2e,stroke:#e94560,color:#fff
    style CH fill:#16213e,stroke:#0f3460,color:#fff
    style AP fill:#0f3460,stroke:#53354a,color:#fff
    style NATS fill:#e94560,stroke:#fff,color:#fff
```

## Custom Resources

| CRD | Description |
|-----|-------------|
| `ClawInstance` | Per-user / per-tenant gateway configuration |
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
├── images/                 # Dockerfiles
├── config/                 # Kubernetes manifests
│   ├── crd/bases/          # CRD YAML definitions
│   ├── manager/            # Controller + API server deployment
│   ├── rbac/               # ServiceAccount, ClusterRole, bindings
│   ├── webhook/            # Webhook deployment + configuration
│   ├── network/            # NetworkPolicy for agent isolation
│   ├── nats/               # NATS JetStream deployment
│   ├── cert/               # TLS certificate resources
│   └── samples/            # Example custom resources
├── migrations/             # PostgreSQL schema migrations
├── docs/                   # Design documentation
├── Makefile
└── README.md
```

## Getting Started

### 1. Install the CLI

```bash
curl -fsSL https://deploy.k8sclaw.ai/install.sh | sh
```

This detects your OS and architecture, downloads the latest release binary, and installs it to `/usr/local/bin` (or `~/.local/bin`).

### 2. Deploy K8sClaw to your cluster

```bash
k8sclaw install
```

This applies CRDs, RBAC, the controller manager, API server, admission webhook, NATS event bus,
cert-manager (if not present), and network policies to your current `kubectl` context.

To install a specific version:

```bash
k8sclaw install --version v0.0.13
```

### 3. Onboard — interactive setup wizard

```bash
k8sclaw onboard
```

The wizard walks you through five steps:

```
  ╔═══════════════════════════════════════════╗
  ║         K8sClaw · Onboarding Wizard       ║
  ╚═══════════════════════════════════════════╝

  📋 Step 1/5 — Cluster check
  📋 Step 2/5 — Name your ClawInstance
  📋 Step 3/5 — Choose your AI provider
  📋 Step 4/5 — Connect a channel (optional)
  📋 Step 5/5 — Apply default policy
```

**Step 3** supports any GenAI provider:

| Provider | Base URL | API Key |
|----------|----------|---------|
| OpenAI | (default) | `OPENAI_API_KEY` |
| Anthropic | (default) | `ANTHROPIC_API_KEY` |
| Azure OpenAI | your endpoint | `AZURE_OPENAI_API_KEY` |
| Ollama | `http://ollama:11434/v1` | none |
| Any OpenAI-compatible | custom URL | custom |

**Step 4** connects a messaging channel — Telegram (easiest), Slack, Discord, or WhatsApp.
The wizard creates the K8s Secrets, ClawPolicy, and ClawInstance for you.

### 4. Launch K8sClaw

```bash
k8sclaw
```

This launches the interactive TUI — browse instances, runs, and channels; view logs and describe output inline; submit agent runs with `/run <task>`.

You can also use the CLI directly:

```bash
k8sclaw instances list                              # list instances
k8sclaw runs list                                   # list agent runs
k8sclaw features enable browser-automation \
  --policy default-policy                           # enable a feature gate
k8sclaw features list --policy default-policy       # list feature gates
```

### 5. Remove K8sClaw

```bash
k8sclaw uninstall
```

## Development

```bash
make test        # run tests
make lint        # run linter
make manifests   # generate CRD manifests
make run         # run controller locally (needs kubeconfig)
```

## Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| **Ephemeral Agent Pods** | Each agent run creates a K8s Job — agent container + IPC bridge sidecar + optional sandbox |
| **Filesystem IPC** | Agent ↔ control plane via `/ipc` volume watched by the bridge sidecar — language-agnostic |
| **NATS JetStream** | Decoupled event bus with durable subscriptions |
| **NetworkPolicy isolation** | Agent pods get deny-all; only the IPC bridge connects to the bus |
| **Policy-as-CRD** | `ClawPolicy` resources gate tools, sandboxes, and feature flags via admission webhooks |

## Configuration

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
