# Design: accelerator energy collectors

Status: **read path implemented** (§14). Repo: `github.com/sympozium-ai/sympozium`.

> Revised after building against a live collector. The original draft invented a
> JSON protocol and rejected reusing an existing one; ergoz already served a
> vendor-neutral fleet snapshot at `GET /api/v1/fleet`, so §5 now documents the
> contract as it exists rather than one we would have imposed. §12 records why
> the original reasoning was wrong.

---

## 1. Overview & goals

Sympozium schedules LLM work onto accelerators but has no idea what that work
costs in watts. `cost_estimation_design.md` prices tokens against a static
table and **deliberately exempts every local/self-hosted provider** (`ollama`,
`vllm`, `llama-server`, …) because those runs have no per-token invoice. That
exemption leaves the largest real cost of a self-hosted fleet — electricity —
completely unmodelled. Energy is the missing number precisely where the token
number is absent.

Measuring accelerator power draw is out of scope for sympozium and always will
be: it means vendor SMI libraries, RAPL/IPMI, sampling loops, and per-silicon
quirks. That work belongs in a separate component. **ergoz** is one such
component — a closed-source product that measures accelerator power draw. This
design lets sympozium consume ergoz (and anything else that speaks the same
protocol) without sympozium containing a single line of ergoz-specific logic.

Goals:

- **G1** — Zero-config detection. Installing a collector into the trusted
  namespace is sufficient; sympozium lights up on its own. No CRD edit, no
  Helm value, no restart.
- **G2** — `AgentRun.status.energyUsage` is written by the controller at run
  completion, alongside `tokenUsage` and `costEstimate`.
- **G3** — **The collector never learns what an AgentRun is.** It reports
  energy per *device*; sympozium owns the device→pod→AgentRun attribution,
  because sympozium is the only side that has that mapping anyway.
- **G4** — The open-source repo contains the protocol, the client, the
  discovery rule, and a conformance fake. It contains no knowledge of how any
  collector obtains a measurement. `ergoz` appears in docs and default Helm
  values as the reference implementation, and nowhere in `internal/`.
- **G5** — Absent collector, broken collector, or slow collector ⇒ no energy
  data, never a failed run and never a zero. (The pricing loader's fail-open
  rule, restated.)
- **G6** — Repo conventions: no floats in CRD types (int64 micro-units),
  controller-side derivation with no mutating webhook, `make generate &&
  make manifests`, `go test -race ./...` clean.

## 2. Non-goals (v1)

- **No enforcement.** Energy never gates admission or scheduling. No watt
  budgets, no power-aware placement. See §9 — the data is trustworthy enough
  to enforce on, which is a change from token metrics, but v1 does not spend
  that.
- **No carbon.** Grid intensity is a second data source with its own
  regional/temporal modelling. `energyMicroWattHours` is the primitive it
  would later multiply; nothing here blocks it.
- **No time-series storage.** Sympozium keeps a live cache and one frozen
  number per AgentRun. If you want history, point the collector at your TSDB —
  that is what it is for.
- **No push ingestion.** `internal/controller/density_subscriber.go` shows the
  NATS-push variant of this exact shape, and it can be added later against the
  same cache. Pull first: it needs no broker and no collector-side coupling.
- **No sub-run attribution.** Energy is attributed per AgentRun, not per turn
  or per tool call.
- **No CPU/host power.** Accelerators only. The protocol's device model does
  not forbid a collector reporting a CPU package as a device, but sympozium
  attributes nothing to it in v1.

## 3. The seam: what each side knows

This is the load-bearing decision; everything else follows from it.

| | knows about | must not know about |
|---|---|---|
| **Collector (ergoz)** | accelerators, power sampling, node-local hardware, its own vendor SDKs | AgentRuns, Ensembles, pods, sympozium at all |
| **Sympozium** | pod→node→device mapping, run lifecycle, the protocol envelope | how a watt is measured, vendor SMI, sampling strategy |

The contract between them is therefore **energy per device per time window**,
and nothing else. Sympozium asks "how many micro-watt-hours did device
`GPU-4f2a…` consume between T1 and T2"; the collector answers without ever
being told why. Attribution — that this device belonged to that pod for that
window, which was AgentRun `foo` — happens entirely controller-side, from
information sympozium already holds.

This seam is what makes G4 achievable rather than aspirational. A protocol that
tried to make the collector attribute energy to workloads would have to teach
the collector the Kubernetes object model, and the collector's answers would
then encode its own understanding of sympozium's scheduling — coupling in both
directions. Keeping attribution on the sympozium side means the collector's
public surface is *device id → integer*, which reveals nothing about how ergoz
works because there is nothing there to reveal.

## 4. Discovery — capability labels, not product names

Sympozium does not look for ergoz. It looks for **something claiming the
energy-collector role**, in a namespace it already trusts.

A collector advertises itself with a **Service** carrying:

```yaml
metadata:
  labels:
    sympozium.ai/collector: energy          # the role. This is the whole discovery key.
  annotations:
    sympozium.ai/collector-port: "9744"     # optional; defaults to the Service's first port
    sympozium.ai/collector-path: "/api/v1/fleet"   # optional
```

Sympozium lists Services with `sympozium.ai/collector=energy` restricted to the
discovery namespace allowlist (default `[sympozium-system, ergoz-system]`).
**The namespace restriction is a security control, not tidiness** — see §9.

A Service, not DaemonSet pods: a fleet-aggregating collector is a single
cluster endpoint, and a Service is a stabler discovery target than a churning
pod set. Per-node agents stay the collector's business, not sympozium's — which
is the §3 seam applied to discovery. (This is where the design diverges from
`density_poller`, which scrapes llmfit pods directly because llmfit has no
aggregator.)

Detection is runtime-only, never compile-time (`internal/dra`'s rule): negative
answers re-probed on an interval, since the collector may be installed after
sympozium. A positive answer is dropped the moment a fetch fails, so an
uninstalled or relocated collector re-resolves rather than pinning a dead
endpoint.

Because the key is a role label, ergoz is not privileged. Anyone can ship a
collector; ergoz is simply the one whose chart sets the label by default. The
word "ergoz" exists in this repo only in `values.yaml` comments and
`docs/`.

## 5. Wire contract — the fleet snapshot

Plain unauthenticated HTTP + JSON, `GET /api/v1/fleet` on the collector
Service. This is the shape ergoz already served; sympozium adopted it rather
than imposing one. Crucially it needed no vendor concessions — the payload
speaks `node`/`pci`/`driver`/`powerWatts`, with no implementation-specific
vocabulary anywhere in it:

```json
{
  "scrapedAt": "2026-07-15T13:04:25.934629262Z",
  "agentsTotal": 1, "agentsUp": 1, "totalWatts": 24.087, "staleDevices": 0,
  "devices": [
    {"node": "kind-control-plane", "kind": "gpu", "pci": "0000:c3:00.0",
     "vendorId": "1002", "deviceId": "1586", "driver": "amdgpu",
     "powerWatts": 24.087,
     "components": {"socket": 24.862, "gfx": 5.964, "cpu_cores": 6.851, "npu": 0},
     "suspended": false, "stale": false},
    {"node": "kind-control-plane", "kind": "npu", "pci": "0000:c4:00.1",
     "vendorId": "1022", "deviceId": "17f0", "driver": "amdxdna",
     "powerWatts": 0, "suspended": true, "stale": false}
  ]
}
```

**Identity is `node` + `pci`** (full-domain form). That tuple is the join key
against llmfit-dra's `pciAddress` attribute, which is how a reading reaches a
specific accelerator in the UI. Devices are described, never attributed: the
collector has no idea what an AgentRun is (§3 holds).

### 5.1 Semantics the client must encode

These are the whole contract. Each one exists because the naive reading is
wrong, and each is pinned by a test in `internal/collector`:

- **`suspended: true` ⇒ `powerWatts: 0` is synthetic.** A runtime-PM-suspended
  device reports zero because waking it to measure it would defeat the
  purpose. It is not a measurement, must not be summed, and must not render as
  "0 W".
- **`stale: true` keeps the last-known value** and stays listed, so a missed
  scrape is visible rather than a silent gap. Excluded from totals.
- **Absent `components` keys are unmeasured, never zero.** A missing `gfx` does
  not mean the graphics block drew nothing.
- **A device with no readable sensor is indistinguishable from an idle one** in
  the wire payload — both are `powerWatts: 0, suspended: false`. Some Intel
  iGPUs are exactly this case. Sympozium's own `Measured` flag (§5.2) is what
  disambiguates downstream.
- **`devices` serialises as `null`, not `[]`,** when empty.
- **Current power only, never accumulated energy.** Watt-hours are the
  consumer's integral to compute. This is what makes §6 non-trivial.
- The snapshot is served from the collector's cache, refreshed on its own
  ticker (~5s). Polling faster returns repeats.

### 5.2 Normalisation

The wire shape is decoded into sympozium's own types rather than passed
through, so the public surface is ours:

- **Float watts → `int64` milliwatts.** Repo convention forbids floats in CRD
  types; keeping the read path integral means §6 can persist without a
  conversion that changes the number.
- **`Measured bool`** is added, collapsing "suspended", "unmeasurable", and
  "rejected as implausible" into one honest flag. The UI renders `—` plus a
  reason when false. Without it, an idle accelerator and a broken sensor are
  the same JSON.
- **Totals are recomputed locally**, never trusted from the wire: the total
  must agree with the devices we actually accepted.
- Values are range-checked against `MaxDevicePowerMilliwatts` (10 kW) and
  rejected — not clamped — when implausible. A clamped value looks plausible;
  a rejected one is visibly absent.

### 5.3 Health

`GET /healthz` → 200.


## 6. Attribution

At AgentRun completion the controller has: the pod, its `spec.nodeName`, its
start/finish timestamps, and — via the existing DRA/`ModelClaim` path or the
pod's `nvidia.com/gpu` resource status — the devices allocated to it. It asks
the collector cached for that node for the energy on those devices over
`[startTime, completionTime]`, and writes:

```go
// EnergyUsage records accelerator energy attributed to this run, sourced from
// an out-of-tree collector (sympozium.ai/collector.v1). Absent when no
// collector is present, when the run used no accelerators, or when the
// collector could not cover the run's window.
type EnergyUsage struct {
    // EnergyMicroWattHours is total accelerator energy over the run window.
    EnergyMicroWattHours int64 `json:"energyMicroWattHours"`

    // Coverage is the collector's own statement about window completeness:
    // full | partial. "none" is never persisted — it means no estimate.
    // +kubebuilder:validation:Enum=full;partial
    Coverage string `json:"coverage"`

    // Devices is the number of accelerators the estimate covers.
    Devices int32 `json:"devices"`

    // WindowStart/WindowEnd are the bounds the collector actually reported,
    // which may be narrower than the run's own start/completion times.
    WindowStart metav1.Time `json:"windowStart"`
    WindowEnd   metav1.Time `json:"windowEnd"`

    // CollectorImplementation is opaque provenance for support ("ergoz").
    // Nothing in sympozium branches on this value.
    // +optional
    CollectorImplementation string `json:"collectorImplementation,omitempty"`
}
```

Frozen at completion, like `costEstimate`. No backfill.

**Shared devices.** If two pods sat on one device, the device's energy is not
either run's energy. v1 attributes a device's energy to a run only when the run
held that device exclusively for the window; otherwise the device is dropped
from the sum and `coverage` degrades to `partial`. Splitting shared-device
energy by utilisation share needs per-process accounting that not every vendor
exposes, and guessing here would produce numbers that look authoritative and
are not. The honest partial is the better v1.

## 7. Components

Mirrors the llmfit density path almost exactly — deliberately, since it is the
same shape (`detect out-of-tree DaemonSet → HTTP pull → TTL cache → reconciler
reads`) and has already survived contact with production.

- **`internal/collector/`** (new) — protocol types, HTTP client, handshake
  parsing, `Detector` (dra-style), and `Fake` (a `httptest` collector serving
  the protocol; the `dra.Static()` analogue).
- **`internal/controller/energy_poller.go`** — `manager.Runnable`, added via
  `mgr.Add()` in `cmd/controller/main.go`, ticker + goroutine-per-pod +
  10s-timeout client. Structural clone of `DensityPoller`.
- **`internal/controller/energy_cache.go`** — RWMutex map keyed by node, TTL
  staleness (2× poll interval), `GarbageCollect()` on a 5-min ticker. Clone of
  `DensityCache`.
- **`internal/controller/agentrun_controller.go`** — at completion, if the
  cache reports a collector on the run's node, query, attribute (§6), write
  `status.energyUsage`. Any error, timeout, or `coverage: none` ⇒ field absent.
- **`internal/apiserver/server.go`** — `energyUsage` surfaced on the AgentRun
  read path; collector presence + implementation added to the existing
  `GET /api/v1/capabilities` payload.

## 8. Configuration

Helm, following the `llmfit:` / `observability:` block convention:

```yaml
energyCollector:
  # Master switch. Discovery is zero-config when true (G1); set false to
  # stop sympozium probing for collectors at all.
  enabled: true
  discovery:
    # Namespaces sympozium will trust a collector from. Security boundary —
    # see the design doc §9 before widening this.
    namespaces: ["sympozium-system"]
  pollInterval: 60s

# ergoz (https://…) is a collector implementation for accelerator power draw.
# Any component speaking sympozium.ai/collector.v1 works here; ergoz is not
# privileged by sympozium in any way.
```

Controller env: `SYMPOZIUM_ENERGY_COLLECTOR_ENABLED`,
`SYMPOZIUM_ENERGY_COLLECTOR_NAMESPACES`, `SYMPOZIUM_ENERGY_POLL_INTERVAL` —
matching the `SYMPOZIUM_PRICING_*` precedent.

No `SympoziumConfig` field and no per-Agent opt-in in v1: there is nothing to
tune per-agent, and a cluster-wide CR field would be a second source of truth
next to discovery.

## 9. Trust

`cost_estimation_design.md` froze cost at display-only because `tokenUsage`
arrives through an **agent-writable** log marker, and commit `8227556` exists
because an agent forged one. Energy data is categorically different: it comes
from an independent component that the agent cannot write to, and it describes
hardware the agent cannot lie about.

That holds **only because of the namespace allowlist**. Discovery keys on a
label, and labels are cheap — if sympozium listed collectors cluster-wide, any
tenant able to create a pod could stand up a fake collector, claim
`sympozium.ai/collector: energy`, and return whatever energy numbers it liked.
Restricting discovery to namespaces an admin already controls is what makes
"the agent cannot influence this" true rather than hopeful. This is the same
reason `DensityPoller` pins to `sympozium-system`, and it must not be relaxed
into a cluster-wide list "for convenience".

Given that, energy is enforcement-*eligible* in a way token metrics are not —
it is arguably the trustworthy metering channel the cost design's §2 non-goals
were waiting for. v1 still does not enforce (§2): earning that requires
operational confidence in collector uptime first, and a design that fails open
must not also be a design that gates work.

Response handling treats the collector as untrusted *input* regardless: bounded
body reads, `energyMicroWattHours` rejected if negative or above a sanity
ceiling (`MaxEnergyMicroWattHoursPerRun`, mirroring pricing's
`MaxRatePerMTokMicro` overflow bound), unknown JSON fields ignored, and a
device id echoed back that sympozium did not ask for is dropped.

## 10. Testing

- **Unit** — `collector.Fake` serves the protocol from `httptest`; poller,
  cache TTL, attribution, and every degradation path (absent, 500, timeout,
  partial coverage, forged/out-of-range values) test against it. No hardware,
  no ergoz, no network.
- **envtest** (`make test-system`) — AgentRun completion writes/omits
  `status.energyUsage` given a cache in each state.
- **Conformance** — `collector.Fake` doubles as the executable spec. A
  collector author (including ergoz) runs sympozium's conformance test against
  their binary to prove protocol compliance. The protocol test lives here; the
  hardware tests live in the collector's own repo, which is exactly the
  split G4 asks for.

## 11. What this costs ergoz

Ergoz publishes: a role label, a handshake, and `device id → micro-watt-hours`.
That is the entire disclosure. It reveals no sampling strategy, no vendor
integration, no internal model, no proprietary anything — it is the same
information a power meter with a screen would reveal. Meanwhile ergoz keeps a
protocol it does not control unilaterally, which is the honest cost: the
contract is now sympozium's public API, and changing it means a `v2` handshake
rather than a private negotiation.

## 12. Alternatives rejected

- **Sympozium reads a vendor SDK directly.** Pulls per-silicon hardware code
  and its licensing into an OSS control plane. This is the thing the collector
  exists to avoid.
- **Collector attributes energy to AgentRuns itself.** Requires teaching the
  collector the Kubernetes object model and sympozium's scheduling semantics —
  coupling in both directions, and a much wider ergoz surface, to compute
  something sympozium can already compute (§3).
- **Named-vendor plugin interface** (`provider: ergoz` switch in the
  controller). Every implementation detail that leaks becomes a branch someone
  must maintain, and the first competitor forces a refactor. The role label
  costs nothing and generalises for free.
- **Scrape the collector's Prometheus `/metrics` instead of its JSON.** ergoz
  serves both. Rejected, but note the original draft rejected it for partly
  wrong reasons: it claimed a dependency on "a Prometheus deployment and a
  query language", which is false — scraping a collector's own `/metrics` is
  just parsing a text endpoint, no Prometheus required. The reason that
  survives is metric-name coupling: `ergoz_accel_power_watts` would put a
  vendor's name in OSS controller code (violating G4), and metric names are a
  looser contract than a typed payload. Making the names Helm-configurable
  would have salvaged G4, but the fleet JSON is strictly better — it is typed,
  already vendor-neutral, and carries `stale`/`suspended` semantics that a
  gauge cannot express.
- **Invent a `sympozium.ai/collector.v1` protocol and have ergoz implement it.**
  This is what the original draft specified. Rejected on contact with reality:
  ergoz's existing `/api/v1/fleet` was *already* vendor-neutral, so inventing a
  parallel protocol would have bought exactly nothing over adopting it — at the
  cost of changing a shipped product and a two-repo build. The lesson worth
  keeping: check what the peer already serves before designing the interface it
  should serve. The disclosure analysis in §11 held; the protocol design was
  redundant.
- **A `Collector` CRD.** Discovery already answers "is it there?", and a static
  endpoint has no reconcile loop — the same rationale that killed the price-table
  CRD in `cost_estimation_design.md` §13.
- **OTLP push into the existing `observability:` path.** That pipe is
  push-to-external-backend and sampled; the controller would then have to read
  its own telemetry back out of a backend it does not own to make a scheduling
  decision. Wrong direction of data flow.

## 13. Unresolved

- **Shared-device energy.** v1 drops shared devices (§6). If MIG/MPS
  multi-tenancy turns out to be the common case rather than the exception, the
  protocol needs a per-process capability and the honest-partial rule needs
  revisiting.
- **Ensemble aggregates.** Presumably read-side in the apiserver, like cost
  totals. Not designed here.
- **Energy → dollars.** A `energyPricePerKWhMicro` entry in the pricing
  ConfigMap would close the loop on the local-provider exemption (§1) and give
  self-hosted runs a real dollar figure for the first time. Deliberately
  deferred: it is a pricing-table change, and it should not ride along with the
  protocol.

## 14. Implementation status

**Built — the read path (per-accelerator power in the UI):**

- `internal/collector/` — wire types, normalisation (§5.2), Service discovery
  with the namespace allowlist, fail-open fetch with a short TTL cache. No
  implementation name appears in this package; `grep -ri ergoz internal/`
  returns nothing.
- `internal/apiserver/power_handlers.go` — `GET /api/v1/power`. Reports
  `available: false` (HTTP 200) for absent/broken/malformed collectors.
- `internal/apiserver/dra_handlers.go` — exposes `pciAddress` on DRA devices,
  the join key that lets a reading reach a specific accelerator.
- Web — `lib/power.ts` (formatting + the never-render-an-unmeasured-number
  rule), `usePower()`, and `AcceleratorLeaves` decorated with live watts. That
  one component is shared by the Placement & Density node cards and the
  topology node cards, so both surfaces lit up together; topology node headers
  additionally carry a node total with an `n/m measured` qualifier.
- Chart — `energyCollector.enabled` + `energyCollector.discovery.namespaces` →
  `SYMPOZIUM_ENERGY_COLLECTOR_NAMESPACES` on the apiserver. The apiserver's
  ClusterRole already had cluster-wide `services` read; no RBAC change.
- Tests — `internal/collector/collector_test.go` covers discovery, the
  allowlist boundary, `devices: null`, synthetic zeros, stale exclusion,
  implausible-value rejection, and fail-open on 5xx/malformed bodies.

**Verified against live hardware.** An AMD Strix Halo (Ryzen AI MAX+ 395 /
Radeon 8060S) under Kind: discovery resolved `ergoz-system/ergoz-collector:9744`
by label, the GPU read ~22–37 W varying in real time, and the `amdxdna` NPU
rendered `— suspended` rather than a fabricated 0 W.

**Not built — attribution (§6).** `AgentRun.status.energyUsage`, the
`EnergyUsage` type, and the controller-side integration of watts into
watt-hours over a run window are all deferred. G2 is therefore unmet; the
collector reports instantaneous power only (§5.1), so energy is a genuine
integral over a sampled series, not a subtraction of two counters. The
shared-device question in §13 blocks a correct v1 of this on multi-tenant
accelerators.

**Deployment note.** Discovery keys on a label ergoz's chart does not yet set,
so the demo applied it out-of-band:

```
kubectl label svc ergoz-collector -n ergoz-system sympozium.ai/collector=energy
```

This does not survive a `helm upgrade` of ergoz. The label belongs in ergoz's
own chart (a one-line addition to `collector-service.yaml`) — that is the
"installing a collector is enough" promise in G1, and until it ships, G1 is
true only for operators who know to run the command above.
