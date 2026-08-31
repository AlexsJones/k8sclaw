# Helm Chart

For production and GitOps workflows, deploy the control plane using Helm.

## Prerequisites

[cert-manager](https://cert-manager.io/) is required for webhook TLS:

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.17.1/cert-manager.yaml
```

## Install

Sympozium ships as two charts: `sympozium-crds` (the CRDs) and `sympozium` (the control plane). The CRDs live in their own chart so `helm upgrade` can roll schema changes forward — Helm 3 [never touches files under a chart's `crds/` directory on upgrade](https://helm.sh/docs/chart_best_practices/custom_resource_definitions/). Install the CRDs first, then the control plane:

```bash
helm upgrade --install sympozium-crds ./charts/sympozium-crds \
  --namespace sympozium-system --create-namespace

helm upgrade --install sympozium ./charts/sympozium \
  --namespace sympozium-system \
  --skip-crds --set createNamespace=false
```

Both charts are kept in lockstep. Always upgrade `sympozium-crds` before `sympozium`.

`--skip-crds` on the second command assumes the `sympozium-crds` release is installed. If you choose to use only the `sympozium` chart, omit `--skip-crds` so the CRDs bundled in that chart are applied — but you will then forfeit the ability to roll CRD schema changes forward via `helm upgrade`.

> **Uninstall ordering.** Removing `sympozium-crds` cascade-deletes every Agent, AgentRun, SkillPack, Ensemble, SympoziumPolicy, etc. across **all** namespaces. Always `helm uninstall sympozium` first, then `helm uninstall sympozium-crds`.

> The legacy single-chart install (`helm install sympozium ./charts/sympozium`) still works for fresh clusters that will never need a CRD upgrade.

See [`charts/sympozium/values.yaml`](https://github.com/sympozium-ai/sympozium/blob/main/charts/sympozium/values.yaml) for all configuration options. The `sympozium-crds` chart has no configurable values.

### Celln (hermetic execution)

Enabled by default (`celln.enabled: true`) so a standard installation includes
the Celln router and controller configuration. The privileged installer and
router schedule only on nodes explicitly labelled `celln.dev/kvm=true`; label
KVM-capable hosts deliberately before they receive host setup. Disable all
Celln resources with `--set celln.enabled=false`. See [Celln Backend](../concepts/celln-backend.md).

### AgentHarness examples

The chart installs the maintained, experimental Pi and Hermes adapter catalog
entries by default, alongside the built-in Ensemble catalog. They appear in
**Agents → Harnesses** when the UI is set to the chart namespace (normally
`sympozium-system`). Each is pinned to an immutable digest and carries its
conformance reference.

The chart also creates a `harness-examples` policy that permits only these two
images. It does **not** create or modify an Agent, so no external harness runs
until an operator explicitly selects both that policy and a runtime on an
Agent. The examples are stateless and currently unmetered; they do not support
MCP/SkillPack tools, native tool filtering, personas, resume, or subagents.

Disable the catalog, or select only one adapter, with values such as:

```yaml
harnessExamples:
  enabled: false
  # Or retain the catalog but install Pi only:
  pi:
    enabled: true
  hermes:
    enabled: false
```

## Observability

The Helm chart deploys a built-in OpenTelemetry collector by default:

```yaml
observability:
  enabled: true
  collector:
    service:
      otlpGrpcPort: 4317
      otlpHttpPort: 4318
      metricsPort: 8889
```

Disable it if you already run a shared collector:

```yaml
observability:
  enabled: false
```

## Web UI

```yaml
apiserver:
  webUI:
    enabled: true       # Serve the embedded web dashboard (default: true)
    token: ""           # Explicit token; leave blank to auto-generate a Secret
```

If `token` is left empty, Helm creates a `<release>-ui-token` Secret with a random 32-character token.

## Node Probe (inference provider discovery)

The node-probe DaemonSet discovers inference providers (Ollama, vLLM, llama-cpp) installed directly on cluster nodes. It probes localhost ports and annotates nodes so the web wizard can offer model selection and node pinning.

```yaml
nodeProbe:
  enabled: false          # Opt-in: deploys a DaemonSet on all nodes
  config:
    probeInterval: 30s
    targets:
      - name: ollama
        port: 11434
        healthPath: /api/tags
        modelsPath: /api/tags
      - name: vllm
        port: 8000
        healthPath: /health
        modelsPath: /v1/models
      - name: llama-cpp
        port: 8080
        healthPath: /health
        modelsPath: /v1/models
  resources:
    requests:
      cpu: 50m
      memory: 32Mi
    limits:
      cpu: 100m
      memory: 64Mi
  tolerations:
    - operator: Exists    # Run on all nodes including GPU-tainted nodes
  nodeSelector: {}        # Limit which nodes run the probe
```

The DaemonSet uses `hostNetwork: true` to probe localhost on the host. It runs as non-root with a read-only filesystem. Its RBAC is minimal: `nodes: [get, patch]`.

See the [Ollama guide](../guides/ollama.md) for full details on node-based inference.

## llmfit DaemonSet (hardware density telemetry)

The llmfit DaemonSet runs on every node, continuously reporting hardware specs and model density scores. The controller and API server poll each pod to build a cluster-wide FitnessCache that powers instant model placement, the Model Density UI, Prometheus metrics, and the density API.

```yaml
llmfit:
  daemonset:
    enabled: true           # Deployed by default
    eventInterval: 60       # Seconds between fitness polls
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 200m
        memory: 256Mi
    tolerations:
      - operator: Exists    # Run on all nodes including GPU-tainted
    nodeSelector: {}
  liveEviction:
    enabled: false          # Re-place models when fitness degrades
    checkInterval: 30s
    degradeThreshold: 0.3
  webhook:
    preflightValidation: false  # Reject Models that won't fit
```

The DaemonSet mounts `/proc`, `/sys`, `/dev`, `/run/udev` read-only for hardware detection. It runs as non-root with `readOnlyRootFilesystem: true` and `SYS_PTRACE` for `/proc` access. RBAC is minimal: `nodes: [get]`.

When `enabled: true` (default), Model CRs with `placement.mode: auto` use cached density data for instant placement instead of spawning probe pods.

See the [llmfit skill docs](../skills/llmfit.md) for full details on the density API, web UI, and Prometheus metrics.

## Agent Sandbox (Kubernetes CRD)

Integrates with [kubernetes-sigs/agent-sandbox](https://github.com/kubernetes-sigs/agent-sandbox) for kernel-level isolation (gVisor/Kata), warm pools, and suspend/resume lifecycle. See the [Agent Sandbox concept doc](../concepts/agent-sandbox.md) for details.

```yaml
agentSandbox:
  enabled: false                   # Master switch — requires agent-sandbox CRDs installed
  defaultRuntimeClass: "gvisor"    # Default runtimeClassName for Sandbox CRs
  rbac: true                       # Grant controller RBAC for Sandbox/SandboxClaim/SandboxWarmPool
```

When `enabled: true`, the controller:
- Creates RBAC rules for `agents.x-k8s.io` resources (Sandbox, SandboxClaim, SandboxWarmPool)
- Checks for agent-sandbox CRDs at startup and enables the feature if found
- Routes AgentRuns with `agentSandbox.enabled: true` to Sandbox CRs instead of Jobs

## Network Policies

```yaml
networkPolicies:
  enabled: true
  extraEgressPorts: []    # add non-standard API server ports here (e.g. [6444] for k3s)
```
