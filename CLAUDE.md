# CLAUDE.md

Guidance for Claude Code working in this repo. The canonical contributor guide is
**AGENTS.md** — read it for repo layout, CRD list, and common tasks. This file
covers only what's easy to get wrong.

## Build / test / verify
- Go 1.25+. Module: `github.com/sympozium-ai/sympozium`.
- `make build` — all binaries. `go build ./...` for a quick compile check.
- `make test` runs `go test -race ./...` — the **race detector is the bar**; a
  change isn't done until `go test -race ./...`, `go vet ./...`, and `gofmt` are clean.
- `make test-system` (envtest) runs controller tests against a real apiserver with
  no cluster — use it for reconcile-logic changes.
- Integration tests (`test/integration/*.sh`) need a Kind cluster + a model. They
  work against any OpenAI-compatible provider — set `spec.model.provider` +
  `spec.model.baseURL` (e.g. a local `llama-server`/`ollama`/`lm-studio`), not just `openai`.

## After editing CRD types (api/v1alpha1/*)
Always run **`make manifests`** (or `make generate` for deepcopy too). This
regenerates `config/crd/bases/` **and** syncs both chart copies
(`charts/sympozium/crds/`, `charts/sympozium-crds/templates/`). CI fails if they
drift (`make helm-sync-check`). Never hand-edit generated CRD YAML or
`zz_generated.deepcopy.go`.

## Updating the Pi/Hermes example adapter versions
`charts/sympozium/values.yaml`'s `harnessExamples` block is the only file to
edit for an adapter's name, image digest, or default enablement — the
`--reuse-values` fallback dict in `charts/sympozium/templates/harness-examples.yaml`
is generated from it. Run **`make helm-sync`** after editing it; `make
helm-sync-check` (CI) fails if the two have drifted. Never hand-edit the block
between the `sync-harness-defaults:begin`/`:end` markers.

## Naming — these renames recur as bugs
- The CRD kind is **`Agent`** (formerly `SympoziumInstance` — do not use).
- AgentRun/Schedule reference an agent via **`agentRef`** + **`agentId`** (not `instanceRef`/`agentID`).
- Ensemble members live under **`agentConfigs`** (not `personas`).
- The run-pod label is **`sympozium.ai/agent-run`** (hyphen, not `agentrun`).
- `AgentRun.spec.model` requires `provider`, `model`, `authSecretRef`; `toolPolicy`
  actions are only `allow`/`deny`; `cleanup` is only `delete`/`keep`.

## Conventions to match when adding code
- **Secrets → env**: inject only via the `allowedAuthSecretKeys` allowlist with
  per-key `SecretKeyRef`. Never `EnvFrom` a whole secret into an agent/sidecar pod.
- **Pod security**: agent pods run non-root, `readOnlyRootFilesystem`, `drop: [ALL]`,
  RuntimeDefault seccomp. Match this for any new pod spec.
- **Admission**: the webhook (`cmd/webhook`) is a **separate, validation-only**
  deployment — there is no wired mutating webhook, so don't assume field defaulting
  happens at admission; do it in the controller.
- **Image tags**: control-plane deployments use a `v`-prefixed tag; spawned run pods
  use `SYMPOZIUM_IMAGE_TAG` **unprefixed** (`_helpers.tpl` vs the controller env).
- Agents are LLM-driven and treated as **adversarial** (prompt injection) — validate
  anything an agent writes into `/ipc` or a CR field before acting on it.

## Deploy / commit
- `helm lint charts/sympozium` after chart changes; the two-chart split
  (`sympozium-crds` + `sympozium`) means CRDs upgrade independently.
- Commit or push only when asked; branch off `main` first. Don't hand-commit
  generated artifacts — run `make generate`/`manifests` and commit the result.
