# Sympozium Design Document

**Status:** Draft
**Date:** 2026-02-23
**Authors:** Architecture Review

---

## 1. Executive Summary

Sympozium is a Kubernetes-native reimagining of OpenClaw that decomposes the
monolithic gateway into a multi-tenant, horizontally scalable system where
**every sub-agent runs as an ephemeral Kubernetes pod** and
**feature access is gated by Kubernetes-native policy** (admission controllers,
RBAC, and Custom Resource Definitions).

This draws on two prior-art systems:

- **OpenClaw**: the full-featured production system — rich plugin/channel/tool
  ecosystem, deep agent orchestration (sub-agent registry, sandbox containers,
  lane-based command queues, hook lifecycle), but tightly coupled as an
  in-process monolith with file-based state.
- **NanoClaw**: a minimal alternative that already runs agents as ephemeral
  containers, communicates via filesystem IPC, enforces isolation through mount
  boundaries, and uses an external mount allowlist for security policy. Its
  architecture validates the "one container per agent invocation" model.

Sympozium takes the best of both:

| Concern | OpenClaw today | NanoClaw today | Sympozium target |
|---------|---------------|----------------|----------------|
| Agent execution | In-process (shared memory) | Ephemeral container per invocation | Ephemeral **K8s Pod** per invocation |
| Sub-agent orchestration | In-process registry + lane queue | N/A (flat) | **CRD-based** registry with controller reconciliation |
| Sandbox isolation | Docker container (long-lived sidecar) | Container = sandbox (read-only rootfs, cap-drop) | Pod SecurityContext + **PodSecurity admission** |
| IPC | In-process EventEmitter | Filesystem polling (JSON files) | **gRPC sidecar** + shared ephemeral volume |
| Tool/feature gating | In-process tool-policy pipeline (7 layers) | Mount allowlist (external file) | **Admission webhooks** + CRD-based policy |
| State | Files on disk (~/.openclaw/) | SQLite + files | **etcd (CRDs)** + object storage + PostgreSQL |
| Multi-instance | Single-instance (file lock) | Single-instance | **Horizontally scalable** (stateless control plane) |
| Channel connections | In-process per channel | WhatsApp only, in-process | **Channel pods** (one Deployment per channel type) |

---

## 2. Architecture Overview

```
                    ┌─────────────────────────────────────────────┐
                    │              Kubernetes Cluster              │
                    └─────────────────────────────────────────────┘

  ┌──────────────┐   ┌──────────────┐   ┌──────────────┐   ┌──────────────┐
  │   Ingress    │   │ Admission    │   │   Policy     │   │  Cert-Mgr /  │
  │  Controller  │   │  Webhooks    │   │   Engine     │   │   Secrets    │
  │  (TLS, WS)   │   │ (Gatekeep)  │   │  (OPA/Kyverno)│  │              │
  └──────┬───────┘   └──────┬───────┘   └──────┬───────┘   └──────────────┘
         │                  │                  │
         ▼                  ▼                  ▼
  ┌─────────────────────────────────────────────────────────────────────────┐
  │                     Sympozium Control Plane (Deployment, HPA)            │
  │                                                                        │
  │   ┌──────────────┐  ┌────────────────┐  ┌──────────────────────────┐  │
  │   │  API Server   │  │  Agent         │  │  Session Manager         │  │
  │   │  (HTTP + WS)  │  │  Orchestrator  │  │  (session CRUD, history) │  │
  │   │              │  │  (spawn, wait)  │  │                          │  │
  │   └──────────────┘  └───────┬────────┘  └──────────────────────────┘  │
  │                             │                                          │
  │   ┌──────────────────────┐  │  ┌───────────────────────────────────┐  │
  │   │  PersonaPack         │  │  │  Reconcilers: Instance, Policy,  │  │
  │   │  Controller          │  │  │  Schedule, SkillPack, AgentRun   │  │
  │   │  (stamp out agents)  │  │  │                                   │  │
  │   └──────────────────────┘  │  └───────────────────────────────────┘  │
  │                             │                                          │
  │   ┌─────────────────────────┼──────────────────────────────────────┐  │
  │   │  Event Bus (NATS / Redis Streams)                              │  │
  │   └─────────────────────────┼──────────────────────────────────────┘  │
  └─────────────────────────────┼──────────────────────────────────────────┘
                                │
              ┌─────────────────┼─────────────────┐
              ▼                 ▼                  ▼
  ┌──────────────────┐ ┌──────────────────┐ ┌──────────────────┐
  │  Agent Pod       │ │  Agent Pod       │ │  Agent Pod       │
  │  (ephemeral Job) │ │  (ephemeral Job) │ │  (ephemeral Job) │
  │                  │ │                  │ │                  │
  │  ┌────────────┐  │ │  ┌────────────┐  │ │  ┌────────────┐  │
  │  │ Agent      │  │ │  │ Agent      │  │ │  │ Agent      │  │
  │  │ Container  │  │ │  │ Container  │  │ │  │ Container  │  │
  │  └─────┬──────┘  │ │  └────────────┘  │ │  └────────────┘  │
  │        │         │ │                  │ │                  │
  │  ┌─────▼──────┐  │ │                  │ │  ┌────────────┐  │
  │  │ Sandbox    │  │ │                  │ │  │ Browser    │  │
  │  │ Sidecar    │  │ │                  │ │  │ Sidecar    │  │
  │  └────────────┘  │ │                  │ │  └────────────┘  │
  └──────────────────┘ └──────────────────┘ └──────────────────┘

  ┌──────────────┐ ┌──────────────┐ ┌──────────────┐ ┌──────────────┐
  │ Channel Pod  │ │ Channel Pod  │ │ Channel Pod  │ │ Channel Pod  │
  │ (Telegram)   │ │ (WhatsApp)   │ │ (Discord)    │ │ (Slack)      │
  │ Deployment   │ │ StatefulSet  │ │ Deployment   │ │ Deployment   │
  └──────────────┘ └──────────────┘ └──────────────┘ └──────────────┘

  ┌──────────────────────────────────────────────────────────────────┐
  │                        Data Layer                                │
  │  ┌──────────┐  ┌──────────────┐  ┌──────────┐  ┌─────────────┐ │
  │  │ PostgreSQL│  │ Redis/Valkey │  │  MinIO   │  │  etcd       │ │
  │  │ (sessions,│  │ (pub/sub,    │  │  (S3)    │  │  (CRDs,     │ │
  │  │  memory,  │  │  queues,     │  │ transcr. │  │   state)    │ │
  │  │  config)  │  │  locks)      │  │ skills   │  │             │ │
  │  └──────────┘  └──────────────┘  └──────────┘  └─────────────┘ │
  └──────────────────────────────────────────────────────────────────┘
```

---

## 3. Custom Resource Definitions

### 3.1 `SympoziumInstance` — per-user/per-tenant gateway

Replaces the monolithic gateway. Each user or tenant gets a `SympoziumInstance` that
declares their desired channels, agents, and policy bindings.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumInstance
metadata:
  name: alice
  namespace: sympozium
spec:
  # Which channels this instance connects to
  channels:
    - type: telegram
      configRef:
        secret: alice-telegram-creds
    - type: whatsapp
      configRef:
        secret: alice-whatsapp-creds
    - type: discord
      configRef:
        secret: alice-discord-creds

  # Agent configuration
  agents:
    default:
      model: claude-opus-4-0-20250514
      thinking: high
      sandbox:
        enabled: true
        image: ghcr.io/openclaw/sandbox:latest
        resources:
          requests: { cpu: 250m, memory: 512Mi }
          limits: { cpu: "1", memory: 1Gi }
      subagents:
        maxDepth: 2
        maxConcurrent: 5
        maxChildrenPerAgent: 3

  # Skills to mount (from SkillPack CRDs or ConfigMaps)
  skills:
    - skillPackRef: coding-skills
    - skillPackRef: research-skills
    - configMapRef: alice-custom-skills

  # Policy binding (which SympoziumPolicy applies)
  policyRef: standard-user-policy

  # Auth for AI providers
  authRefs:
    - secret: alice-anthropic-key
    - secret: alice-openai-key

status:
  phase: Running
  channels:
    - type: telegram
      status: Connected
      lastHealthCheck: "2026-02-23T10:00:00Z"
    - type: whatsapp
      status: Connected
  activeAgentPods: 2
  totalAgentRuns: 1547
```

### 3.2 `AgentRun` — ephemeral agent execution

Each agent invocation (including sub-agents) produces an `AgentRun` CR. The
Agent Orchestrator controller watches these and reconciles them into K8s Jobs.

This replaces OpenClaw's in-memory `SubagentRunRecord` and maps directly to
NanoClaw's ephemeral container model — but with K8s lifecycle management instead
of `docker run --rm`.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: run-abc123
  namespace: sympozium
  labels:
    sympozium.ai/instance: alice
    sympozium.ai/agent-id: default
    sympozium.ai/session-key: "agent:default:subagent:xyz"
    sympozium.ai/parent-run: run-parent-456   # populated for sub-agents
  ownerReferences:
    - apiVersion: sympozium.ai/v1alpha1
      kind: SympoziumInstance
      name: alice
spec:
  instanceRef: alice
  agentId: default
  sessionKey: "agent:default:subagent:xyz"

  # Parent linkage (for sub-agents)
  parent:
    runName: run-parent-456
    sessionKey: "agent:default:main"
    spawnDepth: 1

  task: "Research the latest Kubernetes security best practices"
  systemPrompt: |
    You are a research sub-agent...

  model:
    provider: anthropic
    model: claude-opus-4-0-20250514
    thinking: high
    authSecretRef: alice-anthropic-key

  sandbox:
    enabled: true
    image: ghcr.io/openclaw/sandbox:latest
    securityContext:
      readOnlyRootFilesystem: true
      runAsNonRoot: true
      capabilities: { drop: [ALL] }
      seccompProfile: { type: RuntimeDefault }
    resources:
      requests: { cpu: 250m, memory: 512Mi }
      limits: { cpu: "1", memory: 1Gi }

  # Skills mounted into the agent pod
  skills:
    - skillPackRef: research-skills
    - configMapRef: alice-custom-skills

  # Tools this agent is allowed to use (from its resolved policy)
  toolPolicy:
    allow: [exec, read, write, edit, apply_patch, image, subagents]
    deny: [browser, canvas, cron, gateway]

  timeout: 300s
  cleanup: delete   # or "keep" for debugging

status:
  phase: Running    # Pending → Running → Succeeded / Failed
  podName: run-abc123-pod
  startedAt: "2026-02-23T10:05:00Z"
  completedAt: null
  result: null      # populated on completion with the agent's final reply
  exitCode: null
```

### 3.3 `SympoziumPolicy` — feature and tool gating

Replaces OpenClaw's 7-layer in-process tool-policy pipeline with a declarative,
auditable K8s resource. Enforced by admission webhooks at pod creation time.

Draws from NanoClaw's external mount-allowlist concept (policy stored outside
the agent's reach) but extends it to cover all capabilities.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumPolicy
metadata:
  name: standard-user-policy
  namespace: sympozium
spec:
  # Tool-level gating
  tools:
    defaultAction: deny
    rules:
      - tools: [exec, read, write, edit, apply_patch]
        action: allow
        conditions:
          sandboxRequired: true    # these tools only allowed inside sandbox
      - tools: [image, sessions_status, subagents]
        action: allow
      - tools: [browser]
        action: allow
        conditions:
          featureGate: browser-automation    # requires FeatureGate to be enabled
          sandboxRequired: true
          sidecar: browser                   # requires browser sidecar in pod
      - tools: [cron]
        action: deny
      - tools: ["group:plugins"]
        action: allow
        conditions:
          featureGate: plugins

  # Exec-level gating (NanoClaw-style mount security + OpenClaw approval flows)
  exec:
    securityLevel: allowlist     # deny | allowlist | full
    approvalMode: on-miss        # off | on-miss | always
    approvalChannel: main        # where approval requests go
    safeBins:
      - git
      - ls
      - cat
      - grep
      - find
      - python3
      - node
      - npm
      - pnpm
    blockedBins:
      - curl      # no network exfil from sandbox
      - wget
      - nc
      - ssh

  # Sub-agent gating
  subagents:
    allowed: true
    maxDepth: 2
    maxConcurrent: 5
    maxChildrenPerAgent: 3
    allowCrossAgent: false    # sub-agents can only spawn same agentId
    requireSandbox: true

  # Sandbox enforcement
  sandbox:
    required: true                        # all agent runs must be sandboxed
    network: none                         # none | restricted | unrestricted
    readOnlyRootFilesystem: true
    capDrop: [ALL]
    seccompProfile: RuntimeDefault
    maxMemory: 1Gi
    maxCPU: "1"
    pidsLimit: 256

  # Mount policy (inspired by NanoClaw's mount-allowlist.json)
  mounts:
    workspaceAccess: rw
    blockedPatterns:
      - .ssh
      - .gnupg
      - .aws
      - .azure
      - .kube
      - .docker
      - credentials
      - .env
      - .netrc
      - id_rsa
      - id_ed25519
      - private_key
    additionalMounts:
      allowlistRef:
        configMap: alice-mount-allowlist
      nonMainReadOnly: true

  # Feature gates — features are off unless explicitly enabled
  featureGates:
    browser-automation: false
    voice-call: false
    canvas: false
    plugins: true
    agent-swarms: true
    memory-search: true
    cron-scheduler: false
    network-access: false      # sandbox network policy
```

### 3.4 `SkillPack` — portable skill bundles

Skills are Markdown instruction bundles that become a CRD. The SkillPack
controller reconciles each SkillPack into a ConfigMap that is projected into
agent pods at `/skills`.

**Sidecar architecture:** When a SkillPack requires runtime tools (e.g. `kubectl`,
`helm`), it declares a `sidecar` spec. The AgentRun controller dynamically injects
the sidecar container into the agent pod and creates scoped RBAC resources
(Role/RoleBinding for namespace-scoped access, ClusterRole/ClusterRoleBinding for
cluster-wide access). The controller itself is bound to `cluster-admin` so it can
create arbitrary RBAC rules declared by SkillPacks without hitting Kubernetes RBAC
escalation prevention. RBAC resources are garbage-collected when the AgentRun
completes or is deleted.

```
┌─────────────────────────────────────────────────┐
│  Agent Pod (Job)                                │
│                                                 │
│  ┌──────────┐  ┌──────────┐  ┌──────────────┐  │
│  │  agent   │  │ipc-bridge│  │skill-k8s-ops │  │
│  │ (runner) │  │ (sidecar)│  │  (sidecar)   │  │
│  └────┬─────┘  └────┬─────┘  └──────┬───────┘  │
│       │              │               │          │
│   /workspace     /ipc            kubectl +      │
│   /skills        NATS            full RBAC      │
│   /ipc                                          │
│                                                 │
│  ServiceAccount: sympozium-agent                 │
│  + Role: sympozium-skill-k8s-ops-<run>           │
│  + ClusterRole: sympozium-skill-k8s-ops-<run>    │
└─────────────────────────────────────────────────┘
```

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: SkillPack
metadata:
  name: coding-skills
  namespace: sympozium
spec:
  skills:
    - name: code-review
      description: "Review code for security and quality"
      requires:
        bins: [git, rg]
      content: |
        # Code Review Skill
        When asked to review code...
    - name: refactoring
      description: "Refactor code to improve quality"
      requires:
        bins: [git]
      content: |
        # Refactoring Skill
        ...
  # Container image requirements (bins this skill pack needs)
  runtimeRequirements:
    image: ghcr.io/openclaw/sandbox-common:latest
  # Optional sidecar container for runtime tools + auto-RBAC
  sidecar:
    image: ghcr.io/alexsjones/sympozium/skill-k8s-ops:latest
    mountWorkspace: true
    resources:
      cpu: "100m"
      memory: "128Mi"
    rbac:
      - apiGroups: [""]
        resources: ["pods", "services"]
        verbs: ["get", "list", "watch"]
    clusterRBAC:
      - apiGroups: [""]
        resources: ["nodes", "namespaces"]
        verbs: ["get", "list", "watch"]
```

### 3.5 `PersonaPack` — pre-configured agent bundles

PersonaPacks are the highest-level abstraction in Sympozium. A single
PersonaPack CRD bundles multiple agent personas — each with a system prompt,
skills, tool policy, schedule, and memory seeds — into a one-click installable
package. Think of them as **Helm Charts for AI agents**.

When a PersonaPack is activated (via the TUI wizard or kubectl), the controller
stamps out all the underlying resources automatically:

```
PersonaPack CR (spec.personas[])
  │
  ├─ For each persona:
  │   ├─ Create SympoziumInstance (inherits model, authRefs, policyRef)
  │   ├─ Create SympoziumSchedule (from persona.schedule)
  │   └─ Create ConfigMap (<name>-memory, from persona.memory.seeds)
  │
  ├─ Set ownerReferences on all generated resources
  │   └─ Deleting the PersonaPack cascades to all children
  │
  └─ Update status:
      ├─ status.personaCount = len(spec.personas)
      ├─ status.installedCount = successfully created
      ├─ status.installedPersonas[] = {name, instanceName, scheduleName}
      └─ status.phase = Ready | Pending | Error
```

**Lifecycle phases:**

| Phase | Meaning |
|-------|---------|
| `Pending` | PersonaPack exists but `authRefs` are empty — waiting for activation |
| `Ready` | All personas successfully stamped out |
| `Error` | One or more personas failed to reconcile |

**CRD spec:**

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: PersonaPack
metadata:
  name: platform-team
spec:
  description: "Core platform engineering agents"
  category: platform
  version: "1.0.0"

  # Personas — each becomes a SympoziumInstance + Schedule
  personas:
    - name: security-guardian
      displayName: "Security Guardian"
      systemPrompt: |
        You are a Kubernetes security specialist...
      skills:
        - k8s-ops
      toolPolicy:
        allow: [read_file, list_directory, execute_command, fetch_url]
        deny: [write_file]
      schedule:
        type: sweep
        interval: "30m"
        task: "Scan all namespaces for security policy violations..."
      memory:
        enabled: true
        seeds:
          - "Follow CIS Kubernetes Benchmark v1.8 guidelines"
    - name: sre-watchdog
      # ... additional personas

  # Shared auth — patched by the TUI wizard during activation
  authRefs:
    - secret: platform-team-openai-key

  # Shared policy reference
  policyRef: default-policy
```

**Ownership model:** All generated resources (Instances, Schedules, ConfigMaps)
carry an `ownerReference` pointing back to the PersonaPack. This gives
Kubernetes-native cascading deletion — removing the PersonaPack removes
everything it created. The controller uses `controllerutil.SetControllerReference`
to establish the owner chain.

**TUI activation flow:** The TUI Personas tab lists all PersonaPacks in the
cluster. Pressing Enter on a pack launches a wizard that collects provider,
API key, and model selection, then creates a Secret and patches the PersonaPack's
`spec.authRefs`. The controller detects the authRef and reconciles all personas
into running instances.

**Built-in packs:** Sympozium ships with two PersonaPacks in `config/personas/`:

| Pack | Personas | Focus |
|------|----------|-------|
| `platform-team` | security-guardian, sre-watchdog, platform-engineer | Security audit, cluster health, scheduled ops |
| `devops-essentials` | incident-responder, cost-analyzer | Incident triage, resource optimisation |

---

## 4. Component Deep-Dive

### 4.1 Control Plane — Agent Orchestrator

The orchestrator is a Kubernetes controller (Deployment, HPA-scalable) that
watches `AgentRun` CRDs and reconciles them into Jobs/Pods.

**Reconciliation loop:**

```
AgentRun created (status.phase = Pending)
  │
  ├─ Validate against SympoziumPolicy (via admission webhook, already passed)
  │
  ├─ Resolve pod spec:
  │   ├─ Base image (sandbox image from SympoziumInstance)
  │   ├─ Sidecar containers (sandbox exec, browser if featureGate enabled)
  │   ├─ Skill sidecars (from SkillPack.spec.sidecar, e.g. kubectl)
  │   ├─ Skill RBAC (Role/ClusterRole + bindings, scoped per-run)
  │   ├─ Volumes (workspace PVC, skills ConfigMaps, session ephemeral vol)
  │   ├─ SecurityContext (from SympoziumPolicy.sandbox)
  │   ├─ NetworkPolicy (from SympoziumPolicy.sandbox.network)
  │   ├─ Resource limits (from SympoziumPolicy.sandbox)
  │   └─ Environment (model auth from Secrets, agent config)
  │
  ├─ Create Job with pod spec
  │   └─ Set ownerReference → AgentRun
  │
  ├─ Update AgentRun status.phase = Running, status.podName = ...
  │
  ├─ Watch pod completion:
  │   ├─ On success: read result from shared volume or gRPC call
  │   │   └─ Update AgentRun status.phase = Succeeded, status.result = ...
  │   ├─ On failure: record error
  │   │   └─ Update AgentRun status.phase = Failed, status.error = ...
  │   └─ On timeout: kill pod
  │       └─ Update AgentRun status.phase = Failed, status.error = "timeout"
  │
  └─ If parent exists: notify parent agent via event bus
      └─ Parent's orchestrator delivers result back to parent session
```

**Sub-agent spawning inside a pod:**

When an agent's tool execution calls `subagents.spawn(...)`, the agent container
doesn't directly create a child pod. Instead:

1. The agent writes a `SubagentSpawnRequest` to its gRPC sidecar (or shared
   volume sentinel).
2. The sidecar relays this to the control plane via the event bus.
3. The orchestrator creates a new `AgentRun` CR with `spec.parent` populated.
4. The child pod runs, completes, writes its result.
5. The orchestrator reads the result from the child `AgentRun` status and
   delivers it to the parent agent (via the parent pod's gRPC sidecar or IPC volume).
6. The parent agent's tool call resolves with the sub-agent's output.

This is the Kubernetes-native equivalent of OpenClaw's
`spawnSubagentDirect()` → `registerSubagentRun()` → `waitForSubagentCompletion()`
flow, and NanoClaw's `runContainerAgent()` → IPC file polling → response parsing
flow.

### 4.2 Agent Pod Structure

Each agent invocation runs as a K8s Job with this pod template:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: run-abc123
  labels:
    sympozium.ai/agent-run: run-abc123
    sympozium.ai/instance: alice
spec:
  ttlSecondsAfterFinished: 300
  activeDeadlineSeconds: 600
  template:
    metadata:
      labels:
        sympozium.ai/agent-run: run-abc123
    spec:
      restartPolicy: Never
      serviceAccountName: sympozium-agent   # minimal RBAC, no cluster access

      securityContext:
        runAsNonRoot: true
        runAsUser: 1000
        fsGroup: 1000
        seccompProfile:
          type: RuntimeDefault

      containers:
        # Main agent container — runs the LLM inference loop
        - name: agent
          image: ghcr.io/openclaw/agent-runner:latest
          securityContext:
            readOnlyRootFilesystem: true
            allowPrivilegeEscalation: false
            capabilities:
              drop: [ALL]
          env:
            - name: AGENT_RUN_ID
              value: run-abc123
            - name: TASK
              valueFrom:
                configMapKeyRef: { name: run-abc123-input, key: task }
            - name: MODEL_PROVIDER
              value: anthropic
          envFrom:
            - secretRef:
                name: alice-anthropic-key
          volumeMounts:
            - name: workspace
              mountPath: /workspace
            - name: skills
              mountPath: /skills
              readOnly: true
            - name: ipc
              mountPath: /ipc
            - name: tmp
              mountPath: /tmp
          resources:
            requests: { cpu: 250m, memory: 512Mi }
            limits: { cpu: "1", memory: 1Gi }

        # Sidecar: IPC bridge to control plane
        - name: ipc-bridge
          image: ghcr.io/openclaw/ipc-bridge:latest
          env:
            - name: AGENT_RUN_ID
              value: run-abc123
            - name: EVENT_BUS_URL
              value: nats://nats.sympozium:4222
          volumeMounts:
            - name: ipc
              mountPath: /ipc
          resources:
            requests: { cpu: 50m, memory: 64Mi }
            limits: { cpu: 100m, memory: 128Mi }

        # Optional sidecar: sandbox exec (if exec tools are enabled)
        - name: sandbox
          image: ghcr.io/openclaw/sandbox:latest
          securityContext:
            readOnlyRootFilesystem: true
            capabilities:
              drop: [ALL]
          command: ["sleep", "infinity"]
          volumeMounts:
            - name: workspace
              mountPath: /workspace
            - name: tmp
              mountPath: /tmp
          resources:
            requests: { cpu: 100m, memory: 256Mi }
            limits: { cpu: 500m, memory: 512Mi }

      volumes:
        - name: workspace
          emptyDir: { sizeLimit: 1Gi }
        - name: skills
          projected:
            sources:
              - configMap: { name: coding-skills }
              - configMap: { name: alice-custom-skills }
        - name: ipc
          emptyDir: { medium: Memory, sizeLimit: 64Mi }
        - name: tmp
          emptyDir: { sizeLimit: 256Mi }
```

**Key design choices:**

- **`emptyDir` for workspace** — ephemeral, scoped to the pod lifetime. For
  persistent workspaces, use a PVC (ReadWriteOnce per agent, or ReadWriteMany
  for shared access). This mirrors NanoClaw's per-group directory isolation.
- **IPC via shared `emptyDir`** — the agent writes spawn requests, tool
  results, and messages to `/ipc`; the IPC bridge sidecar watches and relays
  to the event bus. Same pattern as NanoClaw's filesystem-based IPC, but the
  bridge replaces the polling loop with filesystem watches + gRPC forwarding.
- **Sandbox as sidecar** — `kubectl exec` into the sandbox container replaces
  OpenClaw's `docker exec`. The agent container calls tools via the IPC bridge,
  which `kubectl exec`s into the sandbox sidecar. The sandbox has its own
  SecurityContext, separate from the agent.
- **No Docker socket** — unlike OpenClaw's current sandbox model, there's no
  need for a Docker socket. The sandbox is a sidecar, and sub-agents are new
  pods created by the control plane (not by the agent itself).

### 4.3 IPC Bridge

The IPC bridge sits between the ephemeral agent pod and the durable control
plane. It replaces three current mechanisms:

| Current (OpenClaw) | Current (NanoClaw) | Sympozium IPC Bridge |
|---|---|---|
| In-process EventEmitter | Filesystem polling (`setInterval`) | Sidecar with fswatch + event bus |
| Gateway RPC (`callGateway`) | JSON file drop in `/ipc/messages/` | gRPC to control plane via NATS |
| `agent.wait` long-poll | stdout marker parsing | Event bus subscription with CR status watch |

**IPC protocol (files in shared `/ipc` volume):**

```
/ipc/
├── input/
│   ├── task.json           # Initial task (written by orchestrator before pod start)
│   └── followup-*.json     # Follow-up messages from parent or user
├── output/
│   ├── result.json         # Final agent result (written on completion)
│   ├── stream-*.json       # Streaming output chunks
│   └── status.json         # Agent status updates (thinking, tool use, etc.)
├── spawn/
│   └── request-*.json      # Sub-agent spawn requests (agent → bridge → orchestrator)
├── tools/
│   ├── exec-request-*.json # Bash exec requests (agent → bridge → sandbox sidecar)
│   └── exec-result-*.json  # Exec results (sandbox → bridge → agent)
├── messages/
│   └── send-*.json         # Outbound messages to channels (agent → bridge → channel pod)
└── schedules/
    └── request-*.json      # Schedule upsert/suspend/resume/delete requests (agent → bridge → schedule router)
```

The bridge watches these directories with `inotify`/`fswatch` and translates
file operations into event bus messages. This is the same pattern NanoClaw uses
(JSON file drop → poll → process → delete) but with push-based notification
instead of polling.

### 4.4 Channel Pods

Each channel type runs as its own Deployment (or StatefulSet for channels that
need persistent local state, like WhatsApp's session auth).

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: channel-telegram-alice
  namespace: sympozium
  labels:
    sympozium.ai/instance: alice
    sympozium.ai/channel: telegram
spec:
  replicas: 1
  selector:
    matchLabels:
      sympozium.ai/instance: alice
      sympozium.ai/channel: telegram
  template:
    spec:
      containers:
        - name: telegram
          image: ghcr.io/openclaw/channel-telegram:latest
          env:
            - name: INSTANCE_NAME
              value: alice
            - name: EVENT_BUS_URL
              value: nats://nats.sympozium:4222
          envFrom:
            - secretRef:
                name: alice-telegram-creds
```

Channel pods:
1. Maintain the connection to the external service (Telegram Bot API, WhatsApp Web, Discord Gateway, etc.)
2. Receive inbound messages and publish them to the event bus (`channel.message.received`)
3. Subscribe to outbound message events (`channel.message.send`) and deliver them
4. Report health status via the event bus (replacing OpenClaw's in-process channel health monitor)

This decomposition means channels scale and fail independently. A WhatsApp
reconnection doesn't affect Telegram. A Telegram rate limit doesn't block Discord.

#### Telegram Setup

1. Open Telegram and message [@BotFather](https://t.me/BotFather).
2. Send `/newbot`, choose a name and username — BotFather replies with an API token.
3. Create a Kubernetes secret with the token:
   ```bash
   kubectl create secret generic my-telegram-creds \
     --from-literal=TELEGRAM_BOT_TOKEN=<token-from-botfather>
   ```
4. Reference the secret in your SympoziumInstance:
   ```yaml
   channels:
     - type: telegram
       configRef:
         secret: my-telegram-creds
   ```
5. The controller creates a `channel-telegram` Deployment that long-polls the Telegram Bot API.
   Messages sent to your bot are routed to AgentRuns automatically.

> **Tip:** To find your `chat_id`, send a message to the bot, then visit
> `https://api.telegram.org/bot<TOKEN>/getUpdates` — the `chat.id` field
> in the response is what agents use with `send_channel_message`.

### 4.5 Event Bus

NATS JetStream (or Redis Streams) serves as the nervous system connecting all
components:

**Event topics:**

| Topic | Publisher | Subscriber | Payload |
|-------|-----------|------------|---------|
| `agent.run.requested` | API Server | Orchestrator | AgentRun spec |
| `agent.run.started` | Orchestrator | API Server, parent agent | Run ID, pod name |
| `agent.run.completed` | IPC Bridge | Orchestrator, parent agent | Run ID, result |
| `agent.run.failed` | Orchestrator | API Server, parent agent | Run ID, error |
| `agent.stream.chunk` | IPC Bridge | API Server (WS fan-out) | Session key, text chunk |
| `agent.spawn.request` | IPC Bridge (child) | Orchestrator | Spawn params, parent run |
| `channel.message.received` | Channel Pod | API Server → Orchestrator | Channel, sender, text |
| `channel.message.send` | IPC Bridge | Channel Pod | Channel, target, text |
| `channel.health.update` | Channel Pod | API Server | Channel, status |
| `tool.exec.request` | Agent container | IPC Bridge → Sandbox | Command, workdir |
| `tool.exec.result` | Sandbox sidecar | IPC Bridge → Agent | stdout, stderr, exit code |
| `tool.approval.request` | IPC Bridge | API Server → Channel | Command, context |
| `tool.approval.response` | Channel Pod | IPC Bridge → Agent | approved/denied |

---

## 5. Admission Control & Policy Enforcement

### 5.1 Admission Webhook: `sympozium-policy-enforcer`

A validating + mutating admission webhook intercepts all pod creation requests
with the `sympozium.ai/agent-run` label and enforces `SympoziumPolicy`:

**Validation (reject if violated):**

1. **Sandbox required** — if `SympoziumPolicy.sandbox.required = true`, reject pods
   without the sandbox sidecar container.
2. **SecurityContext** — ensure `readOnlyRootFilesystem`, `runAsNonRoot`,
   `capDrop: ALL`, `seccompProfile` match policy. Reject if the pod spec tries
   to escalate.
3. **Resource limits** — reject pods exceeding `SympoziumPolicy.sandbox.maxMemory`
   / `maxCPU`.
4. **Feature gates** — if the pod spec includes a browser sidecar but
   `featureGates.browser-automation = false`, reject.
5. **Network** — if `SympoziumPolicy.sandbox.network = none`, ensure a matching
   `NetworkPolicy` exists (or inject one).
6. **Mount validation** — validate all `volumeMounts` against
   `SympoziumPolicy.mounts.blockedPatterns`. Reject mounts to `.ssh`, `.kube`, etc.
   This is the K8s-native equivalent of NanoClaw's `validateAdditionalMounts()`.
7. **Sub-agent depth** — check the `sympozium.ai/spawn-depth` annotation against
   `SympoziumPolicy.subagents.maxDepth`. Reject if exceeded.
8. **Concurrency** — count existing `AgentRun` CRs with status `Running` for
   this instance. Reject if `maxConcurrent` exceeded.

**Mutation (inject defaults):**

1. Inject `NetworkPolicy` sidecar label selectors for network isolation.
2. Add default resource limits from `SympoziumPolicy` if not specified.
3. Inject the skills volume from `SympoziumInstance.spec.skills`.
4. Add the `ipc-bridge` sidecar if not present.
5. Set `ttlSecondsAfterFinished` for auto-cleanup.

### 5.2 OPA/Gatekeeper Constraints (declarative)

For cluster-wide policy enforcement beyond Sympozium's own webhook:

```yaml
apiVersion: constraints.gatekeeper.sh/v1beta1
kind: SympoziumSandboxRequired
metadata:
  name: require-sandbox-all-agents
spec:
  match:
    kinds:
      - apiGroups: ["batch"]
        kinds: ["Job"]
    namespaces: ["sympozium"]
    labelSelector:
      matchLabels:
        sympozium.ai/component: agent-run
  parameters:
    requiredContainers: ["sandbox"]
    requiredSecurityContext:
      readOnlyRootFilesystem: true
      runAsNonRoot: true
      capabilities:
        drop: ["ALL"]
```

### 5.3 NetworkPolicy for Agent Pods

Agent pods get a `NetworkPolicy` that implements `SympoziumPolicy.sandbox.network`:

```yaml
# network = "none": full isolation (like NanoClaw's containers + OpenClaw's --network none)
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: agent-network-deny-all
  namespace: sympozium
spec:
  podSelector:
    matchLabels:
      sympozium.ai/component: agent-run
      sympozium.ai/network-policy: none
  policyTypes: [Ingress, Egress]
  # Empty ingress/egress = deny all
  egress:
    # Allow only DNS (needed for the IPC bridge sidecar)
    - to:
        - namespaceSelector: {}
      ports:
        - port: 53
          protocol: UDP
    # Allow event bus (IPC bridge needs this)
    - to:
        - podSelector:
            matchLabels:
              app: nats
      ports:
        - port: 4222
```

```yaml
# network = "restricted": allow specific egress only
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: agent-network-restricted
spec:
  podSelector:
    matchLabels:
      sympozium.ai/component: agent-run
      sympozium.ai/network-policy: restricted
  policyTypes: [Ingress, Egress]
  egress:
    - to:
        - namespaceSelector: {}
      ports:
        - { port: 53, protocol: UDP }    # DNS
        - { port: 443, protocol: TCP }   # HTTPS only
    - to:
        - podSelector:
            matchLabels:
              app: nats
      ports:
        - port: 4222
```

### 5.4 Feature Gates

Feature gates are the primary mechanism for progressive enablement. They are
declared in `SympoziumPolicy.featureGates` and enforced at multiple levels:

| Feature Gate | What it unlocks | Enforcement point |
|---|---|---|
| `browser-automation` | Browser sidecar in agent pods | Admission webhook (rejects browser sidecar if false) |
| `voice-call` | Voice call channel pod | SympoziumInstance controller (skips voice pod creation) |
| `canvas` | Canvas host sidecar | Admission webhook |
| `plugins` | Plugin loading in agent pods | Admission webhook (env var injection) |
| `agent-swarms` | Sub-agent spawning | Admission webhook (rejects spawn depth > 0 if false) |
| `memory-search` | Memory/vector search sidecar | Admission webhook |
| `cron-scheduler` | CronJob creation for scheduled agent runs | Orchestrator (refuses to create CronJobs) |
| `network-access` | NetworkPolicy relaxation | Admission webhook (inject deny-all if false) |

**How a user enables a feature:**

```bash
# Via kubectl
kubectl patch sympoziumpolicy standard-user-policy --type merge \
  -p '{"spec":{"featureGates":{"browser-automation": true}}}'

# Via Sympozium CLI (wrapper)
sympozium features enable browser-automation --instance alice

# Via the chat interface (admin channel)
# User: "@claw enable browser automation"
# → Control plane patches SympoziumPolicy
# → Next agent run gets browser sidecar
```

---

## 6. Data Layer Migration

### 6.1 Session Store: File → PostgreSQL

OpenClaw's file-based session store (`sessions.json` per agent + `.jsonl`
transcripts) becomes a PostgreSQL table:

```sql
CREATE TABLE sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    instance_name   TEXT NOT NULL,   -- SympoziumInstance name
    agent_id        TEXT NOT NULL,
    session_key     TEXT NOT NULL UNIQUE,
    channel         TEXT,
    thread_id       TEXT,
    spawn_depth     INTEGER DEFAULT 0,
    spawned_by      TEXT REFERENCES sessions(session_key),
    metadata        JSONB DEFAULT '{}',
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE transcript_events (
    id              BIGSERIAL PRIMARY KEY,
    session_key     TEXT NOT NULL REFERENCES sessions(session_key),
    role            TEXT NOT NULL,  -- user, assistant, tool_use, tool_result
    content         JSONB NOT NULL,
    created_at      TIMESTAMPTZ DEFAULT now()
);

-- Memory / vector search (pgvector replaces SQLite + vec0)
CREATE TABLE memory_embeddings (
    id              BIGSERIAL PRIMARY KEY,
    instance_name   TEXT NOT NULL,
    agent_id        TEXT NOT NULL,
    source          TEXT NOT NULL,   -- file path, session key, etc.
    chunk           TEXT NOT NULL,
    embedding       vector(1536),    -- dimension depends on model
    metadata        JSONB DEFAULT '{}',
    created_at      TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX ON memory_embeddings
    USING ivfflat (embedding vector_cosine_ops)
    WITH (lists = 100);
```

### 6.2 Config: File → ConfigMap + CRD

`openclaw.json` is decomposed:

| Config section | Sympozium equivalent |
|---|---|
| `gateway.auth` | K8s Secret + Ingress auth |
| `gateway.mode`, `gateway.port` | Service + Ingress spec |
| `agents.defaults` | `SympoziumInstance.spec.agents.default` |
| `agents.defaults.sandbox` | `SympoziumPolicy.spec.sandbox` |
| `agents.defaults.subagents` | `SympoziumPolicy.spec.subagents` |
| `agents.defaults.tools` | `SympoziumPolicy.spec.tools` |
| `channels.*` | `SympoziumInstance.spec.channels` + channel pod Secrets |
| `hooks.*` | Plugin hooks in agent pod ConfigMap |
| `skills.*` | `SkillPack` CRDs |
| `cron.*` | K8s CronJob resources |

### 6.3 Transcripts: Files → Object Storage

Session transcripts (append-only JSONL) move to MinIO/S3:

```
s3://sympozium-transcripts/{instance}/{agentId}/{sessionKey}/{timestamp}.jsonl
```

Agent pods write transcript events to the IPC volume; the IPC bridge flushes
them to object storage in batches.

---

## 7. Migration Path

### Phase 1: Operator + CRDs (foundations)

- Implement CRDs: `SympoziumInstance`, `AgentRun`, `SympoziumPolicy`, `SkillPack`
- Build the Sympozium operator (controller-runtime based)
- `SympoziumInstance` controller: reconcile channel pods + store config
- `AgentRun` controller: create Jobs, watch completion, deliver results
- Agent pod image: minimal Node.js runner that reads task from `/ipc/input/`,
  calls LLM, writes result to `/ipc/output/`
- IPC bridge sidecar: file-watch + NATS publish/subscribe

### Phase 2: Policy enforcement (security)

- Implement `SympoziumPolicy` admission webhook (validating + mutating)
- Feature gates enforcement
- NetworkPolicy generation from `SympoziumPolicy.sandbox.network`
- Mount validation (blocked patterns, allowlist)
- OPA/Gatekeeper constraint templates for cluster-wide policy

### Phase 3: Channel decomposition

- Extract each channel into its own container image
- Channel controller: watches `SympoziumInstance.spec.channels`, reconciles channel pods
- Event bus integration for inbound/outbound message routing
- Channel health monitoring via event bus heartbeats

### Phase 4: Sub-agent orchestration

- Implement spawn request → `AgentRun` CR creation flow
- Parent-child linkage and depth tracking
- Result delivery from child to parent via event bus
- Concurrency enforcement (max concurrent per instance)
- Cleanup controller (TTL, `cleanup: delete` policy)

### Phase 5: Advanced features

- Memory/vector search as a sidecar or shared service (pgvector)
- Browser automation sidecar (Chromium + noVNC, gated by feature flag)
- Canvas host as a separate Deployment
- CronJob integration for scheduled agent runs
- Web UI / Control UI as a separate Deployment

---

## 8. Comparison: OpenClaw Concepts → Sympozium Primitives

| OpenClaw concept | Code location | Sympozium equivalent |
|---|---|---|
| `startGatewayServer()` | `src/gateway/server.impl.ts` | **Control plane Deployment** (API + orchestrator) |
| `SubagentRunRecord` | `src/agents/subagent-registry.types.ts` | **`AgentRun` CRD** |
| `spawnSubagentDirect()` | `src/agents/subagent-spawn.ts` | IPC bridge writes spawn request → orchestrator creates `AgentRun` |
| `waitForSubagentCompletion()` | `src/agents/subagent-spawn.ts` | Watch `AgentRun.status.phase` change to Succeeded |
| Announce flow | `src/agents/subagent-announce.ts` | Orchestrator reads `AgentRun.status.result`, delivers to parent IPC |
| Command queue (lanes) | `src/process/command-queue.ts` | `AgentRun` concurrency limits in SympoziumPolicy + admission webhook |
| Tool policy pipeline | `src/agents/tool-policy-pipeline.ts` | **`SympoziumPolicy` CRD** + admission webhook |
| Sandbox (`docker exec`) | `src/agents/sandbox/docker.ts` | **Sandbox sidecar** container in agent pod |
| FS Bridge | `src/agents/sandbox/fs-bridge.ts` | `kubectl exec` into sandbox sidecar (via IPC bridge) |
| Config hot-reload | `src/gateway/config-reload.ts` | ConfigMap watch + rolling pod restart |
| Channel manager | `src/gateway/server-channels.ts` | **Channel pod** Deployments per channel type |
| Plugin registry | `src/plugins/registry.ts` | Plugin containers, `SkillPack` CRDs |
| Gateway lock | `src/infra/gateway-lock.ts` | **Eliminated** (stateless control plane, no file locks) |
| Session file write lock | `src/agents/session-write-lock.ts` | **Eliminated** (PostgreSQL row-level locking) |
| Memory/SQLite | `src/memory/manager.ts` | **PostgreSQL + pgvector** |
| Cron service | `src/cron/service.ts` | **K8s CronJob** resources |
| mDNS discovery | `src/gateway/server-discovery-runtime.ts` | **K8s Service** discovery |
| Tailscale exposure | `src/gateway/server-tailscale.ts` | **K8s Ingress** |
| Gateway health | `src/gateway/server/health-state.ts` | K8s **liveness/readiness probes** + Prometheus metrics |

| NanoClaw concept | Code location | Sympozium equivalent |
|---|---|---|
| `runContainerAgent()` | `src/container-runner.ts` | Orchestrator creates K8s Job from `AgentRun` spec |
| Container args builder | `buildContainerArgs()` | Pod spec builder in orchestrator |
| Volume mount builder | `buildVolumeMounts()` | Pod volume/volumeMount spec in orchestrator |
| IPC file polling | `src/ipc.ts` | IPC bridge sidecar with `inotify` + event bus |
| Group queue | `src/group-queue.ts` | `AgentRun` concurrency limits per instance |
| Mount allowlist | `src/mount-security.ts` | `SympoziumPolicy.mounts` + admission webhook validation |
| Per-group isolation | Group folder + session dir | Per-`AgentRun` pod with isolated volumes |
| Credential filtering | `readSecrets()` | K8s Secrets mounted only into authorized pods |
| `OUTPUT_START/END_MARKER` | `container-runner.ts` | IPC bridge structured JSON protocol |
| N/A | N/A | **`PersonaPack` CRD** — bundles multiple agent personas into one installable unit; stamps out Instances, Schedules, and memory automatically |

---

## 9. Security Model

```
┌───────────────────────────────────────────────────────────────────────────┐
│                          CLUSTER-LEVEL POLICY                            │
│  • PodSecurity (restricted profile)                                      │
│  • NetworkPolicy (default deny for sympozium namespace)                    │
│  • OPA/Gatekeeper constraints (global guardrails)                        │
│  • RBAC (agent ServiceAccount has zero cluster permissions)              │
└────────────────────────────────────────┬──────────────────────────────────┘
                                         │
┌────────────────────────────────────────▼──────────────────────────────────┐
│                        SYMPOZIUM ADMISSION WEBHOOK                         │
│  • Validates every agent pod against SympoziumPolicy                          │
│  • Enforces feature gates, mount blocklists, resource limits             │
│  • Injects NetworkPolicy labels, security contexts, sidecars            │
│  • Checks sub-agent depth and concurrency limits                         │
└────────────────────────────────────────┬──────────────────────────────────┘
                                         │
┌────────────────────────────────────────▼──────────────────────────────────┐
│                           AGENT POD ISOLATION                            │
│  • readOnlyRootFilesystem: true                                          │
│  • runAsNonRoot: true (uid 1000)                                         │
│  • capabilities: drop ALL                                                │
│  • seccompProfile: RuntimeDefault                                        │
│  • No host network/PID/IPC namespace sharing                             │
│  • No service account token auto-mount                                   │
│  • NetworkPolicy: deny all (or restricted egress)                        │
│  • Resource limits enforced (CPU, memory, pids, ephemeral storage)       │
│  • Secrets mounted only for authorized providers                         │
└──────────────────────────────────────────────────────────────────────────┘
```

**Credential isolation** (improving on both OpenClaw and NanoClaw):

- AI provider API keys are K8s Secrets, mounted as environment variables into
  agent pods via `secretRef`. The agent can read them (necessary for auth), but
  they never touch disk inside the pod (env-only, no file mount).
- **Gateway tokens** are only in the control plane pods, never in agent pods.
- **Channel credentials** are only in channel pods, never in agent pods.
- **Cross-instance isolation**: each `SympoziumInstance` has its own Secrets; the
  admission webhook rejects pods that reference Secrets from other instances.

---

## 10. Observability

```yaml
# ServiceMonitor for Prometheus
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: sympozium-control-plane
spec:
  selector:
    matchLabels:
      app: sympozium-control-plane
  endpoints:
    - port: metrics
      path: /metrics
```

**Key metrics:**

| Metric | Type | Description |
|---|---|---|
| `sympozium_agent_runs_total` | Counter | Total agent runs (by instance, status) |
| `sympozium_agent_run_duration_seconds` | Histogram | Agent run duration |
| `sympozium_agent_runs_active` | Gauge | Currently running agent pods |
| `sympozium_subagent_spawns_total` | Counter | Sub-agent spawn requests |
| `sympozium_subagent_depth` | Histogram | Sub-agent nesting depth distribution |
| `sympozium_tool_calls_total` | Counter | Tool invocations (by tool name, status) |
| `sympozium_tool_policy_denials_total` | Counter | Policy-denied tool calls |
| `sympozium_channel_messages_total` | Counter | Messages in/out per channel |
| `sympozium_channel_health` | Gauge | Channel connection status (0/1) |
| `sympozium_admission_decisions_total` | Counter | Webhook admit/reject counts |

---

## 11. Open Questions

1. **Workspace persistence** — should agent workspaces be ephemeral
   (`emptyDir`) or persistent (`PVC`)? Ephemeral is simpler and more secure
   (no cross-run state leakage), but some skills need persistent workspace
   state across runs (e.g., git repos). Could use `ReadWriteOnce` PVCs per
   instance with cleanup policies.

2. **LLM streaming latency** — the event bus adds a hop for streaming tokens.
   For interactive chats, latency matters. May need a direct WebSocket path
   from agent pod → API server for streaming, bypassing the event bus for
   `agent.stream.chunk` events.

3. **Cost of pod creation** — K8s pod startup is slower than `docker run`.
   Warm pod pools (pre-created, idle agent pods) could reduce cold-start
   latency. Alternatively, use Kata Containers or Firecracker for faster
   microVM boot.

4. **Multi-cluster** — should Sympozium support agents running across clusters?
   The event bus (NATS) supports multi-cluster natively, but CRDs are
   cluster-scoped.

5. **Provider rate limiting** — when many agent pods hit the same AI provider
   simultaneously, rate limits become a concern. A shared rate-limiting proxy
   (e.g., an Envoy sidecar or centralized proxy) may be needed.

6. **Operator framework** — controller-runtime (Go) vs Kopf (Python) vs
   custom Node.js operator (to share code with OpenClaw). Go is the standard
   choice for K8s operators; the agent runner itself stays Node.js/TypeScript.
