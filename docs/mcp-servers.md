# MCP Servers

Sympozium integrates with [Model Context Protocol (MCP)](https://modelcontextprotocol.io/) servers to extend agent capabilities with specialized tools. MCP servers provide domain-specific functionality — Kubernetes diagnostics, observability, databases, and more — that agents can invoke during task execution.

## Architecture Overview

```
┌─────────────────────────────────────────────────┐
│ Cluster                                          │
│                                                  │
│  MCPServer CRs (shared, one per cluster)        │
│  ┌──────────────┐  ┌──────────────┐             │
│  │ dynatrace-mcp│  │ k8s-net-mcp  │             │
│  │ (stdio)      │  │ (http)       │             │
│  └──────┬───────┘  └──────┬───────┘             │
│         ▼                  ▼                     │
│  ┌──────────────┐  ┌──────────────┐             │
│  │ Deployment + │  │ Deployment + │             │
│  │ Service      │  │ Service      │             │
│  └──────┬───────┘  └──────┬───────┘             │
│         │                  │                     │
│         └────────┬─────────┘                     │
│                  ▼                                │
│  ┌────────────────────────────────┐              │
│  │ Agent Pod                      │              │
│  │  ├─ init: mcp-discover        │              │
│  │  ├─ agent-runner              │              │
│  │  ├─ mcp-bridge ──→ HTTP ──→ MCPServers      │
│  │  └─ ipc-bridge                │              │
│  └────────────────────────────────┘              │
└─────────────────────────────────────────────────┘
```

MCP servers are deployed as **shared services** in the cluster. All agents connect to them via HTTP through the MCP bridge sidecar. This avoids deploying one MCP server per agent pod.

## MCPServer Custom Resource

The `MCPServer` CRD manages the full lifecycle of MCP servers. It supports three deployment modes:

### Stdio Transport (e.g., Dynatrace, GitHub)

For MCP servers that communicate via stdin/stdout. Sympozium wraps them with an HTTP adapter automatically.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: dynatrace-mcp
  namespace: sympozium-system
spec:
  transportType: stdio
  toolsPrefix: dt
  timeout: 30
  deployment:
    image: mcp/dynatrace-mcp-server:latest
    cmd: "node"
    args: ["/app/dist/index.js"]
    env:
      DT_GRAIL_QUERY_BUDGET_GB: "100"
      DT_MCP_DISABLE_TELEMETRY: "true"
    secretRefs:
      - name: dynatrace-mcp-secret
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 500m
        memory: 512Mi
```

**What happens:** The controller creates a Deployment with the HTTP-to-stdio adapter (`mcp-bridge --stdio-adapter`), which spawns the MCP server process and translates HTTP ↔ stdin/stdout. A Service is created at `dynatrace-mcp.sympozium-system.svc:8080`.

### HTTP Transport (e.g., k8s-networking-mcp, otel-collector-mcp)

For MCP servers that already expose an HTTP endpoint (Streamable HTTP / JSON-RPC).

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: k8s-networking-mcp
  namespace: sympozium-system
spec:
  transportType: http
  toolsPrefix: k8s_net
  timeout: 30
  deployment:
    image: ghcr.io/henrikrexed/k8s-networking-mcp:latest
    port: 8080
    env:
      LOG_LEVEL: "info"
    serviceAccountName: k8s-networking-mcp
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 500m
        memory: 256Mi
```

**What happens:** The controller creates a Deployment with the user's image directly (no adapter needed) and a Service.

> **Note:** HTTP MCP servers that need Kubernetes API access (like k8s-networking-mcp) require a ServiceAccount with appropriate RBAC. See [RBAC Configuration](#rbac-configuration) below.

### External (Pre-existing Servers)

For MCP servers already running in your cluster or externally.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: external-mcp
  namespace: sympozium-system
spec:
  transportType: http
  toolsPrefix: ext
  url: http://existing-mcp.monitoring.svc:8080
```

**What happens:** No Deployment is created. The controller just validates the URL and exposes it in `status.url` for agents to use.

## Tool Filtering

MCP servers can expose dozens of tools. For example, `k8s-networking-mcp` exposes 42 tools, each costing ~300 tokens in the agent's context window. If your agent only needs a subset, you can filter tools with `toolsAllow` and `toolsDeny` to reduce token overhead and keep the agent focused.

### How It Works

- **`toolsAllow`** — allowlist. If set, only these tools are registered. Tool names are specified **without** the `toolsPrefix`.
- **`toolsDeny`** — denylist. Applied **after** `toolsAllow`. Removes matching tools from the final set.
- You can use either field alone, or both together.

### Filtering on MCPServer CRD

Set `toolsAllow`/`toolsDeny` on the MCPServer spec to define cluster-wide defaults for that server:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: k8s-networking-mcp
  namespace: sympozium-system
spec:
  transportType: http
  toolsPrefix: k8s_net
  toolsAllow:
    - get_pods
    - get_services
    - get_endpoints
    - get_ingresses
    - get_network_policies
    - describe_pod
    - describe_service
    - get_pod_logs
    - check_connectivity
    - get_gateway_routes
    - get_virtual_services
    - diagnose_service
  deployment:
    image: ghcr.io/henrikrexed/k8s-networking-mcp:latest
    port: 8080
    serviceAccountName: k8s-networking-mcp
```

This reduces the exposed tools from 42 to 12, saving ~9K tokens per agent run.

### Filtering on MCPServerRef (Per-Instance)

You can also set `toolsAllow`/`toolsDeny` on individual `mcpServers` entries in an `Agent`. This lets different agents use different subsets of the same MCP server:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: network-triage-agent
spec:
  agents:
    default:
      model: gpt-4o
  mcpServers:
    - name: k8s-networking-mcp
      toolsPrefix: k8snet
      toolsAllow:
        - get_pods
        - get_services
        - describe_pod
        - get_pod_logs
        - diagnose_service
    - name: dynatrace-mcp
      toolsPrefix: dynatrace
      toolsDeny:
        - delete_dashboard
        - update_settings
```

### Inheritance Behavior

If an `MCPServerRef` in your `Agent` does **not** set `toolsAllow` or `toolsDeny`, the values from the `MCPServer` CRD spec are inherited automatically. If the ref sets its own values, they take precedence (no merging — the ref's list fully replaces the CRD default).

| MCPServer CRD | MCPServerRef | Result |
|---------------|-------------|--------|
| `toolsAllow: [a, b, c]` | _(not set)_ | `[a, b, c]` inherited |
| `toolsAllow: [a, b, c]` | `toolsAllow: [a, b]` | `[a, b]` (ref wins) |
| _(not set)_ | `toolsAllow: [a, b]` | `[a, b]` |
| _(not set)_ | _(not set)_ | All tools exposed |

The same logic applies to `toolsDeny`.

## Connecting Agents to MCP Servers

In your `Agent`, reference MCP servers by name:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: my-agent
spec:
  agents:
    default:
      model: gpt-4o
  # ... other config ...
  mcpServers:
    - name: dynatrace-mcp
      toolsPrefix: dynatrace
    - name: k8s-networking-mcp
      toolsPrefix: k8snet
    - name: otel-collector-mcp
      toolsPrefix: otel
```

The controller automatically resolves each name to the MCPServer's Service URL from `status.url`. No manual URL configuration needed.

### Inline URL (Legacy / Simple)

You can also specify URLs directly without an MCPServer CR:

```yaml
mcpServers:
  - name: my-mcp
    url: http://my-mcp-service.namespace.svc:8080
    toolsPrefix: my
    timeout: 30
```

This is useful for quick testing or external services you don't want to manage via CRD.

## MCPServer Status

Check the status of your MCP servers:

```bash
kubectl get mcpservers -n sympozium-system
```

```
NAME                  TRANSPORT   READY   URL                                            TOOLS   AGE
dynatrace-mcp         stdio       True    http://dynatrace-mcp.sympozium-system.svc:8080   15    5m
k8s-networking-mcp    http        True    http://k8s-networking-mcp.sympozium-system.svc:8080  42  5m
otel-collector-mcp    http        True    http://otel-collector-mcp.sympozium-system.svc:8080   7  5m
```

Detailed status:

```bash
kubectl describe mcpserver dynatrace-mcp -n sympozium-system
```

Shows:
- `status.ready` — whether the MCP server is serving requests
- `status.url` — resolved Service URL
- `status.toolCount` — number of tools discovered
- `status.tools` — list of tool names
- `status.conditions` — Deployed, Ready, ToolsDiscovered

## Configuration Reference

### MCPServer Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `transportType` | string | Yes | - | `stdio` or `http` |
| `url` | string | No | - | URL for external servers (no deployment) |
| `toolsPrefix` | string | Yes | - | Prefix for tool names (e.g., `dt`, `k8s_net`) |
| `timeout` | int | No | 30 | Per-request timeout in seconds |
| `toolsAllow` | []string | No | - | Tool names (without prefix) to expose. If set, only these are registered |
| `toolsDeny` | []string | No | - | Tool names (without prefix) to hide. Applied after `toolsAllow` |
| `replicas` | int | No | 1 | Number of replicas |
| `deployment` | object | No | - | Deployment spec (see below) |

### Deployment Spec

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `image` | string | Yes | - | Container image |
| `cmd` | string | No | - | Command override (stdio: the MCP server binary) |
| `args` | []string | No | - | Command arguments |
| `port` | int | No | 8080 | HTTP port (http transport only) |
| `env` | map | No | - | Environment variables |
| `secretRefs` | []SecretRef | No | - | Kubernetes Secrets to mount as env vars |
| `resources` | ResourceRequirements | No | - | CPU/memory requests and limits |
| `serviceAccountName` | string | No | - | ServiceAccount for RBAC |

### SecretRef

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Name of the Kubernetes Secret |

All keys from the Secret are injected as environment variables.

## RBAC Configuration

MCP servers that interact with the Kubernetes API need a ServiceAccount with appropriate permissions.

Example for k8s-networking-mcp (read-only cluster access):

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: k8s-networking-mcp
  namespace: sympozium-system
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: k8s-networking-mcp
rules:
  - apiGroups: ["", "apps", "networking.k8s.io", "gateway.networking.k8s.io"]
    resources: ["*"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["networking.istio.io", "security.istio.io"]
    resources: ["*"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: k8s-networking-mcp
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: k8s-networking-mcp
subjects:
  - kind: ServiceAccount
    name: k8s-networking-mcp
    namespace: sympozium-system
```

## Default Catalog

Sympozium ships six MCP servers as default catalog entries. They are installed automatically when `defaultMcpServers.enabled` is `true` (the default). Each server creates an MCPServer CR and Deployment, but the pod will remain `Ready: false` until you create the referenced Secret with credentials.

| Server | Prefix | Transport | Secret | Description |
|--------|--------|-----------|--------|-------------|
| GitHub | `github` | stdio | `mcp-github-token` | GitHub API — repos, issues, PRs, code search |
| Grafana | `grafana` | stdio | `mcp-grafana-token` | Dashboards, PromQL, Loki logs, Tempo traces, alerting, OnCall |
| Kubernetes | `k8s` | http | _(in-cluster SA)_ | Native K8s API — pods, logs, metrics, resource inspection (read-only) |
| ArgoCD | `argocd` | stdio | `mcp-argocd-token` | GitOps — applications, sync status, resource trees, events |
| Postgres | `pg` | stdio | `mcp-postgres-token` | Database queries, performance analysis, index tuning (read-only) |

### Configuring Default Servers

Each server needs a Secret with its credentials. Create them as needed:

```bash
# GitHub
kubectl create secret generic mcp-github-token \
  -n sympozium-system \
  --from-literal=GITHUB_PERSONAL_ACCESS_TOKEN=ghp_xxxx

# Grafana
kubectl create secret generic mcp-grafana-token \
  -n sympozium-system \
  --from-literal=GRAFANA_URL=https://grafana.example.com \
  --from-literal=GRAFANA_SERVICE_ACCOUNT_TOKEN=glsa_xxxx

# ArgoCD
kubectl create secret generic mcp-argocd-token \
  -n sympozium-system \
  --from-literal=ARGOCD_SERVER=argocd.example.com \
  --from-literal=ARGOCD_AUTH_TOKEN=xxxx

# Postgres
kubectl create secret generic mcp-postgres-token \
  -n sympozium-system \
  --from-literal=DATABASE_URI=postgresql://user:pass@host:5432/dbname
```

The **Kubernetes** MCP server uses an in-cluster ServiceAccount (`k8s-mcp`) with read-only RBAC — no Secret needed. It is configured with `toolsDeny` to block write operations (`delete_resource`, `create_resource`, `update_resource`) by default. To enable write access, remove these entries from the MCPServer CR's `toolsDeny` list and expand the ClusterRole verbs.

The **Postgres** server ships with a `toolsDeny` default to prevent destructive operations (`execute_write_query`).

## Secrets Management

For MCP servers that need API tokens or credentials:

```bash
# Create secret for Dynatrace
kubectl create secret generic dynatrace-mcp-secret \
  -n sympozium-system \
  --from-literal=DT_URL=https://your-env.live.dynatrace.com \
  --from-literal=DT_TOKEN=dt0c01.xxxx
```

Reference it in the MCPServer CR:

```yaml
spec:
  deployment:
    secretRefs:
      - name: dynatrace-mcp-secret
```

All keys from the Secret are injected as environment variables into the MCP server container.

## Observability

### Traces

All MCP tool calls are traced end-to-end:

```
agent.run → tool.dispatch → mcp.bridge.tool_call → mcp.server.call
```

- **agent.run**: The overall agent execution span
- **tool.dispatch**: Agent-runner dispatching a tool call
- **mcp.bridge.tool_call**: MCP bridge forwarding to the server
- **mcp.server.call**: (stdio adapter only) The adapter wrapping the stdio call

Trace context (`traceparent`) is propagated through the entire chain.

### Metrics

The MCP bridge emits:
- `mcp.bridge.tool_calls` — counter by server, tool
- `mcp.bridge.tool_errors` — counter by server, tool
- `mcp.bridge.tool_duration_ms` — histogram by server, tool

The stdio adapter additionally emits:
- `mcp.server.requests` — counter by server, tool, status
- `mcp.server.errors` — counter by server, tool, error_type
- `mcp.server.duration` — histogram by server, tool

### Health Checks

Each MCPServer Deployment has readiness and liveness probes:
- Liveness: `/healthz` — is the process alive?
- Readiness: `/readyz` — is the process alive AND tools discovered?

## SkillPacks for MCP Tools

MCP tools alone aren't enough — agents need **diagnostic methodology** to use them effectively. SkillPacks provide this guidance.

See [Writing SkillPacks](guides/writing-skills.md) for the general SkillPack guide. For MCP-specific SkillPacks:

### Creating an MCP SkillPack

A good MCP SkillPack includes:
1. **When to use each tool** — which tool for which symptom
2. **Diagnostic methodology** — step-by-step investigation flow
3. **Protocol-specific guidance** — e.g., gRPC ≠ HTTP routing
4. **Common issues table** — symptom → tool → what to look for → fix

Example structure:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: skillpack-dynatrace-diagnostics
  namespace: sympozium-system
  labels:
    sympozium.ai/skillpack: "true"
data:
  dynatrace-diagnostics.md: |
    # Dynatrace Diagnostics

    ## Available Tools
    - `dt_query_logs` — Query Grail logs with DQL
    - `dt_query_metrics` — Query metrics with DQL
    - `dt_get_problems` — Get active problems from Davis AI
    - `dt_get_entities` — List monitored entities

    ## Diagnostic Methodology
    1. Start with `dt_get_problems` to see if Davis already detected the issue
    2. Use `dt_query_logs` with DQL to investigate specific services
    3. Use `dt_query_metrics` for performance trends
    ...
```

Enable it in values.yaml or per-instance.

## Troubleshooting

### MCP Server Not Ready

```bash
kubectl describe mcpserver <name> -n sympozium-system
kubectl logs deploy/<name> -n sympozium-system
```

Common causes:
- Image pull failure (check image name and registry access)
- Secret not found (check secretRefs names)
- Process crash (stdio: check cmd/args, view adapter logs)

### Agent Not Using MCP Tools

Check the agent-runner logs for:
```
Loaded X MCP tool(s) from manifest
tools enabled: Y tool(s) registered
```

If `X = 0`:
- MCP bridge couldn't reach the MCPServer Service
- Init container timed out (check init container logs)

If tools load but agent doesn't use them:
- Enable a SkillPack with diagnostic methodology
- Check the system prompt includes MCP tool guidance

### Stdio Server Crashing

Check adapter logs:
```bash
kubectl logs deploy/<name> -n sympozium-system
```

The adapter restarts the stdio process automatically with backoff. Look for:
- `stdio process exited with code X` — the MCP server binary is crashing
- `restart backoff: Xs` — adapter is throttling restarts

### Tool Calls Timing Out

Default timeout is 30s. Increase per-server:
```yaml
spec:
  timeout: 60  # seconds
```

Or check if the MCP server is overloaded (add more replicas):
```yaml
spec:
  replicas: 2
```

## Examples

### Complete Dynatrace Setup

```bash
# 1. Create secret
kubectl create secret generic dynatrace-mcp-secret \
  -n sympozium-system \
  --from-literal=DT_URL=https://abc12345.live.dynatrace.com \
  --from-literal=DT_TOKEN=dt0c01.sample.secret

# 2. Deploy MCPServer
kubectl apply -f - <<EOF
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: dynatrace-mcp
  namespace: sympozium-system
spec:
  transportType: stdio
  toolsPrefix: dt
  timeout: 30
  deployment:
    image: mcp/dynatrace-mcp-server:latest
    cmd: "node"
    args: ["/app/dist/index.js"]
    env:
      DT_GRAIL_QUERY_BUDGET_GB: "100"
      DT_MCP_DISABLE_TELEMETRY: "true"
    secretRefs:
      - name: dynatrace-mcp-secret
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
EOF

# 3. Verify
kubectl get mcpservers -n sympozium-system

# 4. Reference in your instance
kubectl patch agent my-agent --type merge -p '
spec:
  mcpServers:
    - name: dynatrace-mcp
'
```

### Complete k8s-networking Setup

```bash
# 1. Create RBAC
kubectl apply -f rbac/k8s-networking-mcp.yaml

# 2. Deploy MCPServer
kubectl apply -f - <<EOF
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: k8s-networking-mcp
  namespace: sympozium-system
spec:
  transportType: http
  toolsPrefix: k8s_net
  deployment:
    image: ghcr.io/henrikrexed/k8s-networking-mcp:latest
    port: 8080
    serviceAccountName: k8s-networking-mcp
EOF
```

## Community & Optional Servers

These MCP servers are not included in the default catalog but can be added with a single `kubectl apply`. Copy-paste the YAML and create the referenced Secret.

### PagerDuty MCP

Incident lifecycle management — create, acknowledge, escalate incidents. Requires building the image from the official repository (no pre-built public image available).

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: pagerduty
  namespace: sympozium-system
spec:
  transportType: stdio
  toolsPrefix: pagerduty
  timeout: 30
  toolsDeny:
    - delete_incident
  deployment:
    # Build from https://github.com/PagerDuty/pagerduty-mcp-server
    # docker build -t your-registry/pagerduty-mcp:latest .
    image: your-registry/pagerduty-mcp:latest
    secretRefs:
      - name: mcp-pagerduty-token
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 250m
        memory: 256Mi
```

```bash
kubectl create secret generic mcp-pagerduty-token \
  -n sympozium-system \
  --from-literal=PAGERDUTY_USER_API_KEY=xxxx
```

### Datadog MCP

For teams using Datadog instead of Grafana for observability. Provides metrics, logs, traces, dashboards, monitors, APM, and incident tools.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: datadog
  namespace: sympozium-system
spec:
  transportType: stdio
  toolsPrefix: datadog
  timeout: 30
  deployment:
    image: mcp/datadog
    cmd: node
    args:
      - dist/index.js
    secretRefs:
      - name: mcp-datadog-token
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 500m
        memory: 512Mi
```

```bash
kubectl create secret generic mcp-datadog-token \
  -n sympozium-system \
  --from-literal=DD_API_KEY=xxxx \
  --from-literal=DD_APP_KEY=xxxx
```

### k8s-networking MCP

Specialized Kubernetes networking diagnostics — network policies, DNS resolution, connectivity checks, route tracing, and service mesh inspection. Exposes 42+ tools (consider using `toolsAllow` to limit context cost).

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: k8s-networking-mcp
  namespace: sympozium-system
spec:
  transportType: http
  toolsPrefix: k8s_net
  timeout: 30
  toolsAllow:
    - get_pods
    - get_services
    - get_network_policies
    - check_connectivity
    - dns_lookup
    - diagnose_service
  deployment:
    image: ghcr.io/henrikrexed/k8s-networking-mcp:latest
    port: 8080
    serviceAccountName: k8s-networking-mcp
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 500m
        memory: 256Mi
```

Requires a ServiceAccount with networking RBAC — see [RBAC Configuration](#rbac-configuration).

### Terraform MCP

HashiCorp Terraform Registry integration and HCP Terraform workspace management. Useful for infrastructure-as-code workflows.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: terraform
  namespace: sympozium-system
spec:
  transportType: stdio
  toolsPrefix: terraform
  timeout: 30
  deployment:
    image: hashicorp/terraform-mcp-server:latest
    cmd: terraform-mcp-server
    args:
      - stdio
    secretRefs:
      - name: mcp-terraform-token
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 500m
        memory: 256Mi
```

```bash
kubectl create secret generic mcp-terraform-token \
  -n sympozium-system \
  --from-literal=TFC_TOKEN=xxxx
```

### Loki MCP

Dedicated Grafana Loki log querying. Only needed if you run Loki without Grafana — the default Grafana MCP server already includes Loki support.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: loki
  namespace: sympozium-system
spec:
  transportType: stdio
  toolsPrefix: loki
  timeout: 30
  deployment:
    image: grafana/loki-mcp:latest
    cmd: loki-mcp
    secretRefs:
      - name: mcp-loki-token
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 500m
        memory: 256Mi
```

### Rootly MCP

AI-native incident management with on-call, incident response, and auto-remediation. Alternative to PagerDuty.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: rootly
  namespace: sympozium-system
spec:
  transportType: stdio
  toolsPrefix: rootly
  timeout: 30
  deployment:
    image: mcp/rootly
    cmd: node
    args:
      - dist/index.js
    secretRefs:
      - name: mcp-rootly-token
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 500m
        memory: 256Mi
```

### Slack MCP

Extended Slack integration beyond the built-in channel support — message history search, thread replies, channel management. Only needed if agents require read access to Slack conversations.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: MCPServer
metadata:
  name: slack
  namespace: sympozium-system
spec:
  transportType: stdio
  toolsPrefix: slack
  timeout: 30
  deployment:
    image: mcp/slack
    cmd: node
    args:
      - dist/index.js
    secretRefs:
      - name: mcp-slack-token
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 250m
        memory: 256Mi
```
