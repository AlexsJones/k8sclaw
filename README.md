<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/brand/logo-plate-dark.svg">
    <img src="docs/assets/brand/logo-plate-light.svg" alt="Sympozium — agentic control plane, K8s-native" width="540">
  </picture>
</p>

<p align="center">

  <em>
  Agents don't need better prompts. They need shared situational awareness.<br>
  Sympozium is a <b>coordination layer</b> for multi-agent AI systems on Kubernetes &mdash;<br>
  selective permeability, structured handoffs, and shared memory.<br>
  Every agent is a Pod. Every policy is a CRD. Every execution is a Job.</em><br><br>
  From the creator of <a href="https://github.com/k8sgpt-ai/k8sgpt">k8sgpt</a> and <a href="https://github.com/AlexsJones/llmfit">llmfit</a>
</p>

<p align="center">
  <b>
  This project is under active development. API's will change, things will break. Be brave.
  <b />
</p>
<p align="center">
  <a href="https://github.com/sympozium-ai/sympozium/actions"><img src="https://github.com/sympozium-ai/sympozium/actions/workflows/build.yaml/badge.svg" alt="Build"></a>
  <a href="https://github.com/sympozium-ai/sympozium/releases/latest"><img src="https://img.shields.io/github/v/release/sympozium-ai/sympozium" alt="Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue" alt="License"></a>
</p>

<p align="center">
  <img src="docs/assets/family-stack.gif" alt="Sympozium family stack — coordination, models, isolation, energy, Kubernetes" width="800">
</p>

---

> **Full documentation:** [deploy.sympozium.ai/docs](https://deploy.sympozium.ai/docs/)

---

> [!IMPORTANT]
> **AgentHarness is available experimentally.** Run an administrator-approved
> external adapter in an isolated AgentRun, or choose a session-capable runtime
> for a private, continuing **Agent → Chat** conversation. Kubernetes policy,
> identity, skills, memory, MCP, observability, and audit remain under platform
> control. Start with the [AgentHarness guide](https://deploy.sympozium.ai/docs/guides/agentharness/)
> and the digest-pinned [reference adapter](examples/harness-reference/). This
> is an adapter boundary—not permission to run arbitrary upstream images.

---

### Quick Install (macOS / Linux)

**Homebrew:**
```bash
brew tap sympozium-ai/sympozium
brew install sympozium
```

**Shell installer:**
```bash
curl -fsSL https://deploy.sympozium.ai/install.sh | sh
```

Then deploy to your cluster and activate your first agents:

```bash
sympozium install          # deploys CRDs, controllers, and built-in Ensembles
sympozium                  # launch the TUI — go to Ensembles tab, press Enter to onboard
sympozium serve            # open the web dashboard (port-forwards to the in-cluster UI)
```

### What it looks like

<p align="center">
  <img src="demo.gif" alt="Sympozium dashboard" width="800">
</p>

### Advanced: Helm Chart

**Prerequisites:** [cert-manager](https://cert-manager.io/) (for webhook TLS):
```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.17.1/cert-manager.yaml
```

Sympozium can be installed as two charts: `sympozium-crds` (the CRDs, so they can be upgraded) and `sympozium` (the control plane). Install the CRDs first, then the control plane:

```bash
helm repo add sympozium https://deploy.sympozium.ai/charts
helm repo update

helm upgrade --install sympozium-crds sympozium/sympozium-crds \
  --namespace sympozium-system --create-namespace

helm upgrade --install sympozium sympozium/sympozium \
  --namespace sympozium-system \
  --skip-crds --set createNamespace=false
```

> `--skip-crds` on the second command assumes you installed `sympozium-crds`
> first. If you skip the CRDs chart, drop `--skip-crds` so the bundled CRDs
> in the `sympozium` chart are applied instead.

See [`charts/sympozium/values.yaml`](charts/sympozium/values.yaml) for configuration options, or the [Helm Chart docs](https://deploy.sympozium.ai/docs/reference/helm/) for the full guide.

---

## Why Sympozium?

Containers needed orchestration. Agents need coordination.

Sympozium is a **Kubernetes-native coordination layer** for multi-agent AI systems. It solves the same problem Kubernetes solved for containers &mdash; but for agents that need to share context, hand off tasks, and maintain shared situational awareness.

**And that is the whole product.** Sympozium decides what agents *do*. Where compute *happens* is the job of a capability layer &mdash; [llmfit-dra](https://github.com/sympozium-ai/llmfit-dra), a Kubernetes DRA driver that places models by physics through the stock scheduler. How tokens *move* is the serving engine's job (vLLM, SGLang, llama.cpp). When an agent needs a model, Sympozium *claims* one the way an application claims a PersistentVolume &mdash; it never decides where it runs. See [Positioning](https://deploy.sympozium.ai/docs/positioning/) for the boundary and what's deliberately out of scope.

### Agent Coordination

| | |
|---|---|
| **Synthetic Membrane** | Selective permeability for agent teams &mdash; control what agents share via trust groups, visibility tags, and field-level gating. [Read the paper](https://zenodo.org/records/20070699) |
| **Agent Workflows** | Delegation, sequential pipelines, supervision, and stimulus triggers between personas &mdash; visualised on an interactive canvas |
| **Shared Workflow Memory** | Pack-level SQLite memory pool for cross-persona knowledge sharing with per-persona access control and time decay |
| **Ensembles** | Helm-like bundles for AI agent teams &mdash; activate a pack and the controller stamps out Agents, Schedules, and memory |

### Platform Infrastructure

| | |
|---|---|
| **Model Endpoints for Agents** | Declare models as CRDs &mdash; GGUF or HuggingFace weights are downloaded, an inference server (llama.cpp, vLLM, TGI, or custom) is deployed, and OpenAI-compatible endpoints are exposed for your personas. No API keys required. Placement is *claimed*, not decided here: with [llmfit-dra](https://github.com/sympozium-ai/llmfit-dra) installed, the stock scheduler places models by physics |
| **Skill Sidecars** | Every skill runs in its own sidecar with ephemeral least-privilege RBAC, garbage-collected on completion |
| **Multi-Channel** | Telegram, Slack, Discord, WhatsApp &mdash; each channel is a dedicated Deployment backed by NATS JetStream |
| **Persistent Memory** | SQLite + FTS5 on a PersistentVolume &mdash; memories survive across ephemeral pod runs |
| **Scheduled Tasks** | Cron-based recurring agent runs for periodic workflows, data syncs, and automated checks |
| **Agent Sandbox** | Kernel-level isolation via [kubernetes-sigs/agent-sandbox](https://deploy.sympozium.ai/docs/concepts/agent-sandbox/) &mdash; gVisor or Kata with warm pools for instant starts |
| **MCP Servers** | External tool providers via Model Context Protocol with auto-discovery and allow/deny filtering |
| **TUI & Web UI** | Terminal and browser dashboards with live workflow canvas, or skip the UI entirely with Helm and kubectl |
| **Policy & Governance** | Cluster-wide `SympoziumPolicy` CRD &mdash; tool gating (allow/deny/ask), sandbox requirements, network egress rules, and image-registry allowlists, enforced by an admission webhook |
| **Serving Mode** | Run an agent as a long-lived, OpenAI-compatible + MCP HTTP endpoint instead of a one-shot Job &mdash; agents as services |
| **Observability & Cost** | OpenTelemetry traces and metrics, Prometheus endpoints, per-run trace IDs, and token usage with estimated cost on every AgentRun |
| **Any AI Provider** | OpenAI, Anthropic, AWS Bedrock, Azure, Ollama, or any OpenAI-compatible endpoint (Groq, Mistral, DeepSeek, OpenRouter, vLLM, LM Studio, &hellip;) &mdash; no vendor lock-in |

---

## Documentation

| Topic | Link |
|-------|------|
| Getting Started | [deploy.sympozium.ai/docs/getting-started](https://deploy.sympozium.ai/docs/getting-started/) |
| Positioning &mdash; what Sympozium is (and isn't) | [deploy.sympozium.ai/docs/positioning](https://deploy.sympozium.ai/docs/positioning/) |
| Architecture | [deploy.sympozium.ai/docs/architecture](https://deploy.sympozium.ai/docs/architecture/) |
| Custom Resources | [deploy.sympozium.ai/docs/concepts/custom-resources](https://deploy.sympozium.ai/docs/concepts/custom-resources/) |
| Ensembles | [deploy.sympozium.ai/docs/concepts/ensembles](https://deploy.sympozium.ai/docs/concepts/ensembles/) |
| Skills & Sidecars | [deploy.sympozium.ai/docs/concepts/skills](https://deploy.sympozium.ai/docs/concepts/skills/) |
| Sidecar-Driven Mode | [deploy.sympozium.ai/docs/modes/sidecar-driven](https://deploy.sympozium.ai/docs/modes/sidecar-driven/) |
| Harness Mode (external agent harnesses) | [deploy.sympozium.ai/docs/modes/harness](https://deploy.sympozium.ai/docs/modes/harness/) |
| Persistent Memory | [deploy.sympozium.ai/docs/concepts/persistent-memory](https://deploy.sympozium.ai/docs/concepts/persistent-memory/) |
| Channels | [deploy.sympozium.ai/docs/concepts/channels](https://deploy.sympozium.ai/docs/concepts/channels/) |
| Agent Sandboxing | [deploy.sympozium.ai/docs/concepts/agent-sandbox](https://deploy.sympozium.ai/docs/concepts/agent-sandbox/) |
| Hermetic Workloads (Celln) | [deploy.sympozium.ai/docs/concepts/celln-backend](https://deploy.sympozium.ai/docs/concepts/celln-backend/) |
| Security | [deploy.sympozium.ai/docs/concepts/security](https://deploy.sympozium.ai/docs/concepts/security/) |
| CLI & TUI Reference | [deploy.sympozium.ai/docs/reference/cli](https://deploy.sympozium.ai/docs/reference/cli/) |
| Helm Chart | [deploy.sympozium.ai/docs/reference/helm](https://deploy.sympozium.ai/docs/reference/helm/) |
| Local Models | [deploy.sympozium.ai/docs/guides/local-models](https://deploy.sympozium.ai/docs/guides/local-models/) |
| Ollama & Local Inference | [deploy.sympozium.ai/docs/guides/ollama](https://deploy.sympozium.ai/docs/guides/ollama/) |
| Writing Skills | [deploy.sympozium.ai/docs/guides/writing-skills](https://deploy.sympozium.ai/docs/guides/writing-skills/) |
| Writing Tools | [deploy.sympozium.ai/docs/guides/writing-tools](https://deploy.sympozium.ai/docs/guides/writing-tools/) |
| Writing Orchestrator Sidecars | [deploy.sympozium.ai/docs/sidecars/writing-orchestrator-sidecars](https://deploy.sympozium.ai/docs/sidecars/writing-orchestrator-sidecars/) |
| LM Studio & Local Inference | [deploy.sympozium.ai/docs/guides/lm-studio](https://deploy.sympozium.ai/docs/guides/lm-studio/) |
| llama-server | [deploy.sympozium.ai/docs/guides/llama-server](https://deploy.sympozium.ai/docs/guides/llama-server/) |
| Unsloth | [deploy.sympozium.ai/docs/guides/unsloth](https://deploy.sympozium.ai/docs/guides/unsloth/) |
| Writing Ensembles | [deploy.sympozium.ai/docs/guides/writing-ensembles](https://deploy.sympozium.ai/docs/guides/writing-ensembles/) |
| Your First AgentRun | [deploy.sympozium.ai/docs/guides/first-agentrun](https://deploy.sympozium.ai/docs/guides/first-agentrun/) |
| Adding a Task Mode | [deploy.sympozium.ai/docs/modes/extension-guide](https://deploy.sympozium.ai/docs/modes/extension-guide/) |

---

## Development

```bash
make test        # run tests
make test-system # run envtest system tests (no cluster needed)
make lint        # run linter
make manifests   # generate CRD manifests
make run         # run controller locally (needs kubeconfig)
```

## License

MIT License
