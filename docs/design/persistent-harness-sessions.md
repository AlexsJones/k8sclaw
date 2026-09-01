# Persistent AgentHarness sessions

## Purpose

`AgentRuntime` currently selects a trusted **one-shot** adapter for an
`AgentRun`. A persistent binding on an Agent does not imply a persistent pod.
This design adds a separate, explicitly managed interactive session so a user
can select a harness, wait for it to become ready, and converse with it without
overloading an ephemeral AgentRun.

## First implementation slice

Introduce a namespaced `HarnessSession` resource owned by an Agent:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: HarnessSession
metadata:
  name: analyst-session
spec:
  agentRef: analyst
  runtimeRef: pi-v1
  desiredState: running
  idleTimeout: 1h
```

The controller resolves and records the immutable runtime digest once, creates
a Deployment and ClusterIP Service, and reports `Pending`, `Ready`, `Draining`,
or `Failed`. Deletion, an explicit `desiredState: stopped`, and idle expiry
remove the workload and its per-session identity.

The API/UI talks to the Session through a Sympozium-owned proxy. It does not
expose pod exec, a ServiceAccount token, or direct NATS access to a browser.

## Contract boundary

Existing `v1alpha1` Pi and Hermes images remain **one-shot only**. Persistent
sessions require a new adapter contract version with:

- readiness and bounded request handling;
- an authenticated local request/response endpoint;
- cancellation and shutdown semantics;
- optional streaming declared explicitly;
- structured per-request result and usage records.

The session controller must reject a runtime that does not declare this new
contract. It must never keep a completed one-shot AgentRun pod alive as a
substitute for a session.

## Security invariants

- The Agent remains the sole authority for provider credential allowlisting.
- Every session gets a unique ServiceAccount with `automountServiceAccountToken:
  false`; trusted sidecars receive only the projected tokens they need.
- The runtime image stays digest-pinned and policy-approved, and the resolved
  digest is recorded in status.
- Harness containers retain narrowed mounts and cannot publish directly to
  control-plane NATS subjects.
- Host networking/PID/access, privileged SkillPacks, lifecycle RBAC, and
  unsupported MCP/tool paths remain rejected.
- The UI sees only proxy-mediated requests and audit records, never raw model
  or MCP credentials.

## Completion evidence for the first slice

1. Selecting a session-capable runtime creates a persistent pod and Service.
2. A real credential-backed request receives a response through the proxy.
3. Stop, idle expiry, cancellation, and restart have deterministic status and
   cleanup behavior.
4. One-shot Pi and Hermes AgentRuns remain supported and regression-tested.

Tracked in [#392](https://github.com/sympozium-ai/sympozium/issues/392).
