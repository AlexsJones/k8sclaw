# Sympozium reference harness adapter

This is a deterministic conformance fixture for the `v1alpha1` harness
contract. It is not a maintained adapter for an upstream agent harness.

It proves that a contract-compatible image can run as UID 1000 with a
read-only root filesystem, use the writable HOME supplied by Sympozium, read
`TASK`, check the contract version, and write both required result channels.

Build it locally:

```bash
docker build -f examples/harness-reference/Dockerfile -t sympozium-reference-adapter:dev .
```

For a cluster test, publish it to an operator-approved registry and use its
digest in an `AgentRuntime`. The policy must explicitly allow that exact image
prefix and enable `harnessPolicy`.

The published Sympozium fixture already has a digest-pinned, complete smoke
manifest. Run it against a cluster with Sympozium installed:

```bash
kubectl apply -f examples/harness-reference/manifests.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Succeeded \
  agentrun/reference-harness-smoke -n sympozium-harness-reference --timeout=2m
kubectl get agentrun/reference-harness-smoke -n sympozium-harness-reference \
  -o jsonpath='{.status.result}{"\n"}'
```
