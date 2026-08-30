# Serving Mode

Serving mode extends Sympozium's execution model beyond ephemeral Jobs. When a skill requires a long-running process (e.g. the `web-endpoint` skill), the controller creates a **Deployment + Service** instead of a Job — turning an agent into a persistent HTTP endpoint.

---

## Overview

By default, every AgentRun creates an ephemeral Kubernetes Job that runs once and terminates. Serving mode adds a second execution path:

| Mode | Kubernetes resource | Lifecycle | Use case |
|------|-------------------|-----------|----------|
| `task` (default) | Job | Runs once, then completes | Chat responses, scheduled tasks, one-shot operations |
| `server` | Deployment + Service | Long-running, requeues every 30s | HTTP APIs, webhook receivers, persistent endpoints |

---

## How it works

### Automatic detection

The controller auto-detects server mode during AgentRun reconciliation. If **any** skill sidecar has `requiresServer: true` in its SkillPack definition, the effective mode becomes `server` — no manual configuration required.

```yaml
# In a SkillPack definition
spec:
  sidecar:
    requiresServer: true    # Triggers serving mode
    ports:
      - name: http
        containerPort: 8080
```

### Reconciliation flow

1. **Pending** — Controller detects `requiresServer: true` on a resolved sidecar and calls `reconcilePendingServer()`:
   - Creates a unique `sympozium-run-<run>` ServiceAccount for the serving run
   - Creates ephemeral RBAC for the skill
   - Generates an API key Secret (if not provided)
   - Builds and creates a Deployment with the sidecar container
   - Creates a ClusterIP Service exposing the sidecar ports
   - Transitions the AgentRun to **Serving** phase

2. **Serving** — Controller periodically monitors the Deployment:
   - Checks Deployment health (ready replicas)
   - Attempts to create an HTTPRoute if a Kubernetes Gateway is configured and ready
   - Requeues every 30 seconds (no timeout enforcement — server runs are indefinite)

3. **Deletion** — When the AgentRun is deleted:
   - The Deployment, Service, and HTTPRoute are garbage-collected via `ownerReferences`
   - Skill RBAC (Roles, ClusterRoles, Bindings) is cleaned up by the finalizer

### AgentRun lifecycle phases

```
Pending → Serving → (deleted)
              ↓
           Failed   (if Deployment disappears or creation errors)
```

The `Serving` phase is unique to server-mode runs. It indicates the Deployment is active and the controller is monitoring it.

---

## API types

### AgentRunSpec

```go
type AgentRunSpec struct {
    // Mode is "task" (default, Job) or "server" (Deployment).
    // Typically auto-detected from skill sidecars.
    Mode string `json:"mode,omitempty"`
    // ...
}
```

### AgentRunStatus

```go
type AgentRunStatus struct {
    Phase          AgentRunPhase `json:"phase,omitempty"`
    DeploymentName string        `json:"deploymentName,omitempty"` // Server mode only
    ServiceName    string        `json:"serviceName,omitempty"`    // Server mode only
    // ...
}
```

### SkillSidecar

```go
type SkillSidecar struct {
    RequiresServer bool         `json:"requiresServer,omitempty"` // Triggers server mode
    Ports          []SidecarPort `json:"ports,omitempty"`          // Exposed by the Service
    // ...
}
```

---

## Gateway integration

When a Kubernetes Gateway API is configured via `SympoziumConfig`, the controller automatically creates an HTTPRoute for server-mode runs:

1. Reads `SympoziumConfig` named `default` in `sympozium-system`
2. Checks `spec.gateway.enabled` and `status.gateway.ready`
3. Derives hostname: explicit `hostname` param → `<instance>.<baseDomain>` → skip
4. Creates an HTTPRoute pointing to the server Service on port 8080

```
Internet → Gateway → HTTPRoute → Service → Deployment (web-proxy)
                                                ↓
                                         Creates AgentRun Jobs
                                         per incoming request
```

If no gateway is configured, the Service remains ClusterIP-only — accessible within the cluster via `<service>.<namespace>.svc`.

---

## Metrics

Serving mode exposes OpenTelemetry metrics:

| Metric | Type | Description |
|--------|------|-------------|
| `sympozium.web_endpoint.serving` | Counter | Incremented when a server-mode Deployment is created |
| `sympozium.web_endpoint.gateway_not_ready` | Counter | Incremented when HTTPRoute creation is skipped due to unready gateway |
| `sympozium.web_endpoint.route_created` | Counter | Incremented when an HTTPRoute is successfully created |

All metrics include the `sympozium.instance` attribute.

---

## The `sympozium serve` CLI command

Separately from serving mode (which runs agents as APIs), the `sympozium serve` CLI command provides local access to the **web dashboard**:

```bash
sympozium serve
```

This port-forwards the in-cluster API server to your local machine and opens the web UI. It is not related to agent serving mode — it is the local development/admin interface.

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `9090` | Local port to forward to |
| `--open` | `false` | Automatically open a browser |
| `--service-namespace` | `sympozium-system` | Namespace of the apiserver service |

The command:
1. Discovers the `sympozium-apiserver` Service in the target namespace
2. Retrieves the UI authentication token from the `sympozium-ui-token` Secret (or Deployment env)
3. Starts a `kubectl port-forward` with automatic reconnection (exponential backoff)
4. Prints the URL and token for browser access

---

## Example: exposing an agent as an API

```bash
# 1. Enable web-endpoint skill on an instance
kubectl patch agent my-agent --type=merge -p '{
  "spec": {
    "skills": [{"skillPackRef": "web-endpoint", "params": {"rate_limit_rpm": "120"}}]
  }
}'

# 2. The controller creates a server-mode AgentRun automatically
kubectl get agentruns -l sympozium.ai/instance=my-agent
# NAME                    PHASE     AGE
# my-agent-server-abc123  Serving   30s

# 3. Check the Service
kubectl get svc -l sympozium.ai/instance=my-agent
# NAME                           TYPE        PORT(S)
# my-agent-server-abc123-server  ClusterIP   8080/TCP

# 4. Call the agent (from within the cluster)
curl -X POST http://my-agent-server-abc123-server:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-..." \
  -H "Content-Type: application/json" \
  -d '{"model":"default","messages":[{"role":"user","content":"Hello!"}]}'
```
