# Persistent AgentHarness sessions

## Purpose

`AgentRuntime` currently selects a trusted **one-shot** adapter for an
`AgentRun`. A persistent binding on an Agent does not imply a persistent pod.
This design adds a separate, explicitly managed interactive session so a user
can select a harness, wait for it to become ready, and converse with it without
overloading an ephemeral AgentRun.

## Implemented lifecycle

A namespaced `HarnessSession` resource binds the runtime to an Agent:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: HarnessSession
metadata:
  name: analyst-session
spec:
  agentRef: analyst
  runtimeRef: pi-session-v0-84-4
  desiredState: running
  idleTimeout: 1h
```

The controller resolves and records the immutable runtime digest, creates a
PVC, Deployment, ClusterIP Service, and NetworkPolicy, and reports `Pending`,
`Ready`, `Draining`, or `Failed`. Deletion removes the complete session;
`desiredState: stopped` removes only the private workload and preserves the CR
and PVC for resume.

`idleTimeout` is active. The authenticated API proxy updates
`status.lastActivityTime`, request counts, active-request state, the latest
request ID and outcome, and error counts. Once the timeout expires with no
request in flight, the controller stops the workload and records
`Ready=False`, reason `IdleTimeout`. Setting `desiredState: running` resumes it.

The API/UI talks to the Session through a Sympozium-owned proxy. It does not
expose pod exec, a ServiceAccount token, or direct NATS access to a browser.

## Contract boundary

Existing `v1alpha1` Pi and Hermes images remain **one-shot only**. Persistent
sessions use the implemented `v1alpha2` adapter contract with:

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
- Session pods set `automountServiceAccountToken: false`; the adapter receives
  no Kubernetes API token.
- The runtime image stays digest-pinned and policy-approved, and the resolved
  digest is recorded in status.
- Harness containers retain narrowed mounts and cannot publish directly to
  control-plane NATS subjects.
- Host networking/PID/access, privileged SkillPacks, lifecycle RBAC, and
  unsupported MCP/tool paths remain rejected.
- The UI sees only proxy-mediated requests and audit records, never raw model
  or MCP credentials.

## Verified behavior

1. Selecting a session-capable runtime creates a persistent pod and Service.
2. A real credential-backed request receives a response through the proxy.
3. Stop, idle expiry, cancellation, and restart have deterministic status and
   cleanup behavior.
4. One-shot Pi and Hermes AgentRuns remain supported and regression-tested.

This lifecycle is covered by the real-model
`test/integration/test-persistent-harness-session.sh` test for both maintained
Pi and Hermes session adapters. The implementation issue is complete in
[#392](https://github.com/sympozium-ai/sympozium/issues/392); broader
AgentHarness hardening remains tracked in
[#349](https://github.com/sympozium-ai/sympozium/issues/349).
