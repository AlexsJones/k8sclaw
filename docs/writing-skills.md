# Writing Skills for KubeClaw

This guide explains how to create a SkillPack — from simple Markdown instruction bundles to full sidecar containers with auto-provisioned RBAC.

---

## Concepts

A **SkillPack** is a Kubernetes CRD that bundles one or more skills. When toggled on a ClawInstance, the skills are mounted into every AgentRun pod for that instance.

There are three layers to a skill, each optional beyond the first:

| Layer | What it does | When you need it |
|-------|-------------|-----------------|
| **Skills** (Markdown) | Instructions the agent reads at `/skills/` | Always — this is the core of every SkillPack |
| **Sidecar** (Container) | Runtime tools injected as a pod sidecar | When the skill needs binaries like `kubectl`, `helm`, `terraform` |
| **RBAC** (Roles) | Kubernetes permissions auto-provisioned per run | When the sidecar needs to talk to the Kubernetes API |

```
┌─────────────────────────────────────────────────────────┐
│  SkillPack CRD                                          │
│                                                         │
│  spec.skills[]         → ConfigMap → mounted at /skills │
│  spec.sidecar.image    → Container injected into pod    │
│  spec.sidecar.rbac[]   → Role + RoleBinding (per run)   │
│  spec.sidecar.clusterRBAC[] → ClusterRole (per run)     │
└─────────────────────────────────────────────────────────┘
```

---

## Step 1: Write the Skills (Markdown)

Every skill is a Markdown document that tells the agent _how_ to perform a task. The agent reads these as files at runtime.

```yaml
apiVersion: kubeclaw.io/v1alpha1
kind: SkillPack
metadata:
  name: my-skill
spec:
  category: devops        # grouping in the TUI (kubernetes, security, devops, etc.)
  version: "0.1.0"
  source: custom           # builtin, imported, or custom
  skills:
    - name: deploy-check
      description: Verify a Kubernetes deployment is healthy
      content: |
        # Deployment Health Check

        When asked to check a deployment, run these steps:

        ## 1. Get rollout status
        ```
        kubectl rollout status deployment/<name> -n <namespace>
        ```

        ## 2. Check pod health
        ```
        kubectl get pods -l app=<name> -n <namespace>
        ```

        ## 3. Inspect events
        ```
        kubectl get events -n <namespace> --sort-by=.lastTimestamp | tail -10
        ```

        Report the status as a table with columns: Pod, Status, Restarts, Age.
      requires:
        bins:
          - kubectl        # documents which binaries the skill expects
        tools:
          - bash           # documents which agent tools are needed
```

### Tips for good skill content

- **Be prescriptive** — give the agent exact commands to run, not vague instructions.
- **Use Markdown headings** — the agent parses structure. `## Steps` is better than a wall of text.
- **Include output formats** — tell the agent how to present results (tables, summaries, etc.).
- **Specify error handling** — what should the agent do if a command fails?
- **List `requires`** — even though it's informational, it documents what the sidecar must provide.

### Applying the basic SkillPack

If your skill only needs Markdown (no tools), you're done:

```bash
kubectl apply -f config/skills/my-skill.yaml
```

The SkillPack controller creates a ConfigMap (`skillpack-my-skill`) containing your skill content. When an agent pod runs, the ConfigMap is projected into `/skills/`.

---

## Step 2: Build a Sidecar Image (optional)

If your skill references binaries (`kubectl`, `helm`, `terraform`, etc.), you need a sidecar container that provides them. The agent can then `exec` into the sidecar or use the shared `/workspace` volume.

### Dockerfile

Create a Dockerfile at `images/skill-<name>/Dockerfile`:

```dockerfile
# images/skill-my-tool/Dockerfile

# Multi-stage: grab the binary you need
FROM bitnami/kubectl:1.31 AS kubectl

# Minimal base image
FROM alpine:3.20

# Install supporting tools
RUN apk add --no-cache \
    bash \
    curl \
    jq \
    && adduser -D -u 1000 agent

# Copy the binary from the builder stage
COPY --from=kubectl /opt/bitnami/kubectl/bin/kubectl /usr/local/bin/kubectl

# Run as non-root (must match the pod's runAsUser: 1000)
USER 1000
WORKDIR /workspace

# Default: sleep forever so the sidecar stays alive for the agent run
CMD ["sleep", "infinity"]
```

### Key requirements

| Requirement | Why |
|------------|-----|
| **`USER 1000`** | Agent pods run as UID 1000 with `runAsNonRoot: true`. Your sidecar must match. |
| **`CMD ["sleep", "infinity"]`** | The sidecar runs alongside the agent. It must stay alive for the duration of the run. |
| **Minimal image** | Keep the image small. Use multi-stage builds to copy only the binaries you need. |
| **No secrets baked in** | Use `env` in the sidecar spec or Kubernetes Secrets — never bake credentials into images. |

### Build and push

```bash
docker build -t ghcr.io/yourorg/skill-my-tool:latest images/skill-my-tool/
docker push ghcr.io/yourorg/skill-my-tool:latest
```

### Registering a built-in skill image in CI

If the skill is **bundled with KubeClaw** (i.e. lives under `images/` and `config/skills/`), you must add it to the build pipeline so it is built and pushed automatically:

1. **Makefile** — append `skill-<name>` to the `IMAGES` variable.
2. **`.github/workflows/build.yaml`** — add `skill-<name>` to the `image` matrix.
3. **`.github/workflows/release.yaml`** — add `skill-<name>` to the `image` matrix.

For example, to add a new `skill-my-tool`:

```makefile
# Makefile
IMAGES = controller apiserver ... skill-k8s-ops skill-my-tool
```

```yaml
# .github/workflows/build.yaml & release.yaml
matrix:
  image:
    - ...
    - skill-k8s-ops
    - skill-my-tool
```

This ensures `make docker-build` / `make docker-push` and CI all build the sidecar image alongside the other KubeClaw components.

---

## Step 3: Add the Sidecar to the SkillPack

Add a `sidecar` block to your SkillPack spec:

```yaml
spec:
  skills:
    - name: deploy-check
      # ... (Markdown content as above)

  sidecar:
    # Required: the container image
    image: ghcr.io/yourorg/skill-my-tool:latest

    # Optional: override the entrypoint (default: ["sleep", "infinity"])
    command: ["sleep", "infinity"]

    # Optional: environment variables
    env:
      - name: KUBECONFIG
        value: /workspace/.kube/config

    # Optional: mount /workspace into the sidecar (default: true)
    mountWorkspace: true

    # Optional: resource requests/limits
    resources:
      cpu: "100m"
      memory: "128Mi"
```

When the AgentRun controller sees this SkillPack in the run's skills list, it injects the sidecar as an additional container named `skill-<skillpack-name>`.

---

## Step 4: Define RBAC (optional)

If the sidecar needs to talk to the Kubernetes API (e.g. `kubectl get pods`), declare RBAC rules. The controller automatically creates and cleans up these resources per AgentRun.

### Namespace-scoped RBAC (`rbac`)

Creates a **Role** + **RoleBinding** in the AgentRun's namespace, bound to the `kubeclaw-agent` ServiceAccount:

```yaml
  sidecar:
    image: ghcr.io/yourorg/skill-my-tool:latest
    rbac:
      # Read pods, services, and deployments
      - apiGroups: [""]
        resources: ["pods", "pods/log", "services"]
        verbs: ["get", "list", "watch"]
      - apiGroups: ["apps"]
        resources: ["deployments", "statefulsets"]
        verbs: ["get", "list", "watch", "update", "patch"]
```

### Cluster-scoped RBAC (`clusterRBAC`)

Creates a **ClusterRole** + **ClusterRoleBinding** for resources that span namespaces:

```yaml
  sidecar:
    clusterRBAC:
      # Read-only access to nodes and namespaces
      - apiGroups: [""]
        resources: ["nodes", "namespaces"]
        verbs: ["get", "list", "watch"]
```

### Security model

| Aspect | How it works |
|--------|-------------|
| **Scoping** | Namespace RBAC is scoped to the run's namespace. Cluster RBAC is cluster-wide but typically read-only. |
| **Lifecycle** | Namespace-scoped Roles and RoleBindings have an `ownerReference` to the AgentRun — Kubernetes garbage-collects them automatically. Cluster-scoped resources are cleaned up by the controller on AgentRun deletion. |
| **Labelling** | All RBAC resources are labelled with `kubeclaw.io/agent-run`, `kubeclaw.io/skill`, and `kubeclaw.io/managed-by: kubeclaw` for auditing. |
| **Least privilege** | Each SkillPack declares exactly the permissions it needs. There is no shared god-role — each skill gets its own scoped RBAC. |
| **Ephemeral** | RBAC exists only while the AgentRun exists. When the run finishes (or is deleted), permissions are revoked. |

### RBAC naming convention

```
Role/ClusterRole:           kubeclaw-skill-<skillpack>-<agentrun>
RoleBinding/ClusterRoleBinding: kubeclaw-skill-<skillpack>-<agentrun>
```

---

## Step 5: Toggle the Skill

### Via the TUI

1. Press `s` on a ClawInstance to drill into the Skills view.
2. Use `Space` or `Enter` to toggle the skill on/off.
3. The next AgentRun will include the sidecar and RBAC.

### Via kubectl

```bash
kubectl patch clawinstance <name> --type=merge \
  -p '{"spec":{"skills":[{"skillPackRef":"my-skill"}]}}'
```

### Via the `/skills` command

```
/skills <instance-name>
```

---

## Complete Example: k8s-ops

The built-in `k8s-ops` skill is the reference implementation. Here's how all three layers come together:

### File layout

```
config/skills/k8s-ops.yaml      # SkillPack CRD (skills + sidecar + RBAC)
images/skill-k8s-ops/Dockerfile  # Sidecar container image
```

### SkillPack YAML (abbreviated)

```yaml
apiVersion: kubeclaw.io/v1alpha1
kind: SkillPack
metadata:
  name: k8s-ops
spec:
  category: kubernetes
  version: "0.1.0"
  source: builtin
  skills:
    - name: cluster-overview
      description: Inspect cluster state and summarise health
      content: |
        # Cluster Overview
        1. `kubectl get nodes -o wide`
        2. `kubectl get pods -A --field-selector=status.phase!=Running`
        ...
      requires:
        bins: [kubectl]
    - name: pod-troubleshoot
      description: Diagnose and fix pod issues
      content: |
        # Pod Troubleshooting
        ...
    - name: resource-management
      description: Scale, update, and manage resources
      content: |
        # Resource Management
        ...
  sidecar:
    image: ghcr.io/alexsjones/kubeclaw/skill-k8s-ops:latest
    command: ["sleep", "infinity"]
    mountWorkspace: true
    resources:
      cpu: "100m"
      memory: "128Mi"
    rbac:
      - apiGroups: [""]
        resources: ["pods", "pods/log", "services", "configmaps", "events"]
        verbs: ["get", "list", "watch"]
      - apiGroups: ["apps"]
        resources: ["deployments", "statefulsets", "replicasets"]
        verbs: ["get", "list", "watch", "update", "patch"]
    clusterRBAC:
      - apiGroups: [""]
        resources: ["nodes", "namespaces"]
        verbs: ["get", "list", "watch"]
```

### What happens at runtime

```
1. User toggles k8s-ops on instance "alice"
   → ClawInstance.spec.skills = [{skillPackRef: "k8s-ops"}]

2. AgentRun created for instance "alice"
   → Controller resolves SkillPack "k8s-ops"
   → Finds sidecar spec and RBAC rules

3. Controller creates:
   → Role "kubeclaw-skill-k8s-ops-alice-run-xyz" (namespace)
   → RoleBinding "kubeclaw-skill-k8s-ops-alice-run-xyz"
   → ClusterRole "kubeclaw-skill-k8s-ops-alice-run-xyz"
   → ClusterRoleBinding "kubeclaw-skill-k8s-ops-alice-run-xyz"

4. Job pod created with containers:
   → agent (agent-runner image, reads /skills/)
   → ipc-bridge (NATS forwarder)
   → skill-k8s-ops (kubectl + bash + curl + jq)

5. Agent reads skill Markdown, runs kubectl commands via sidecar.

6. Run completes → Job cleaned up → RBAC garbage-collected.
```

---

## Troubleshooting

| Issue | Check |
|-------|-------|
| Skill content not appearing | `kubectl get configmap skillpack-<name>` — does it exist? |
| Sidecar not injected | Does the SkillPack have `spec.sidecar.image`? Is the skill toggled on the instance? |
| Permission denied in sidecar | Check RBAC: `kubectl get role,rolebinding -l kubeclaw.io/skill=<name>` |
| Sidecar crash | Check pod logs: `kubectl logs <pod> -c skill-<name>` |
| Image pull error | Verify the sidecar image exists and is accessible from the cluster |
| UID mismatch | Sidecar must run as UID 1000 (same as the pod's `securityContext.runAsUser`) |

---

## Quick Reference

```yaml
# Minimal SkillPack (Markdown only)
apiVersion: kubeclaw.io/v1alpha1
kind: SkillPack
metadata:
  name: my-skill
spec:
  category: devops
  skills:
    - name: my-task
      description: Do the thing
      content: |
        # Instructions
        Run `echo hello`.

---

# Full SkillPack (Markdown + Sidecar + RBAC)
apiVersion: kubeclaw.io/v1alpha1
kind: SkillPack
metadata:
  name: my-full-skill
spec:
  category: kubernetes
  version: "1.0.0"
  source: custom
  skills:
    - name: my-task
      description: Do the thing
      content: |
        # Instructions
        ...
      requires:
        bins: [kubectl]
  sidecar:
    image: my-registry/my-sidecar:latest
    mountWorkspace: true
    resources:
      cpu: "100m"
      memory: "128Mi"
    rbac:
      - apiGroups: [""]
        resources: ["pods"]
        verbs: ["get", "list"]
    clusterRBAC:
      - apiGroups: [""]
        resources: ["nodes"]
        verbs: ["get", "list", "watch"]
```
