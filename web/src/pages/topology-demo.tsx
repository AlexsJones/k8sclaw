/**
 * TopologyDemoPage — hardcoded busy topology for demos.
 * Hidden route: /topology/demo
 */

import { useState, useCallback, useRef, useEffect, useMemo } from "react";
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  type Node,
  type Edge,
  type NodeProps,
  type NodeChange,
  type EdgeChange,
  applyNodeChanges,
  applyEdgeChanges,
  useReactFlow,
  ReactFlowProvider,
  MarkerType,
  Handle,
  Position,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { HardDrive } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { StimulusDialogProvider } from "@/components/canvas-primitives";
import { nodeTypes, NODE_SIZES } from "@/pages/topology";
import { draDeviceDetail, groupAccelerators } from "@/lib/dra";
import { SimulatedPowerContext } from "@/lib/power-context";
import { BANDS, simulatePower, type PowerBand, type SimDevice } from "@/lib/power-sim";
import type { PowerIndex } from "@/lib/power";
import type { DraDevice } from "@/lib/api";
import { useArrowKeyPan, KeyboardGuide } from "@/hooks/use-arrow-key-pan";
import Dagre from "@dagrejs/dagre";

// ── Accelerator inventory helpers ────────────────────────────────────────────
// Mirrors what llmfit-dra reports from ResourceSlices (GET /api/v1/dra/nodes).
// The node cards group identical flavours into a "×N" leaf, so these fixtures
// stamp out one DraDevice per physical card rather than a pre-summed count.

type GpuFlavour = {
  model: string;
  vendor: string;
  memoryGi: number;
  memoryBandwidthGBs: number;
  computeTFLOPS: number;
};

/** Real published specs, so the demo reads as a plausible fleet rather than
 * invented numbers: HBM capacity, memory bandwidth, dense BF16 throughput.
 *
 * Form factor is load-bearing, not cosmetic. SXM5 is a module soldered to an
 * HGX baseboard — it only exists in 4-/8-GPU HGX systems, and those bays are
 * fixed at populate-all. H200 NVL is the PCIe card (600 W, 2-/4-way NVLink
 * bridge) and is the only H200 a slotted chassis can take. Pairing them the
 * other way round describes hardware that cannot be bought, so each flavour
 * below carries the chassis class it is legal in. */
const GPU: Record<string, GpuFlavour> = {
  // HGX baseboard only. Dense BF16 989 TF (1,979 with sparsity).
  h200sxm: { model: "NVIDIA H200-SXM5-141GB", vendor: "nvidia", memoryGi: 141, memoryBandwidthGBs: 4800, computeTFLOPS: 989 },
  // PCIe double-wide. Same 141 GB HBM3e / 4.8 TB/s, lower clocks: dense BF16
  // 836 TF (1,671 with sparsity).
  h200nvl: { model: "NVIDIA H200 NVL 141GB", vendor: "nvidia", memoryGi: 141, memoryBandwidthGBs: 4800, computeTFLOPS: 836 },
  l40s: { model: "NVIDIA L40S 48GB", vendor: "nvidia", memoryGi: 48, memoryBandwidthGBs: 864, computeTFLOPS: 362 },
};

/** Network accelerators. On a fabric-bound fleet these are inventory in their
 * own right: the HGX boxes carry one NDR ConnectX per GPU (the 1:1 GPUDirect
 * RDMA ratio the vendors ship), plus BlueField DPUs for the north-south and
 * storage path that would otherwise burn host cores. */
type NicFlavour = {
  model: string;
  vendor: string;
  linkLayer: "infiniband" | "ethernet";
  rateGbps: number;
};

// Vendor rides in its own field, so the model string stays short enough that
// the link rate survives truncation in a node card.
const NIC: Record<string, NicFlavour> = {
  // MCX75310AAS-NEAT — single-port OSFP, PCIe 5.0 x16. The GPUDirect card.
  cx7ndr: { model: "ConnectX-7 NDR", vendor: "nvidia", linkLayer: "infiniband", rateGbps: 400 },
  // B3220 — 2× 200GbE, 16 Arm A78 cores. Infrastructure offload, not GPUDirect.
  bf3: { model: "BlueField-3 B3220", vendor: "nvidia", linkLayer: "ethernet", rateGbps: 200 },
  // Workstation-class RoCE. What a tower actually has.
  cx6dx: { model: "ConnectX-6 Dx", vendor: "nvidia", linkLayer: "ethernet", rateGbps: 100 },
};

/** N identical accelerators of one flavour. `unhealthy` marks a single card
 * bad so the demo also shows the degraded path the cards render in red. */
function cards(
  kind: "gpu" | "npu",
  prefix: string,
  count: number,
  f: GpuFlavour,
  unhealthy?: { index: number; reason: string },
): DraDevice[] {
  return Array.from({ length: count }, (_, i) => {
    const bad = unhealthy?.index === i;
    return {
      name: `${prefix}-${i}`,
      kind,
      model: f.model,
      vendor: f.vendor,
      memoryGi: f.memoryGi,
      memoryBandwidthGBs: f.memoryBandwidthGBs,
      computeTFLOPS: f.computeTFLOPS,
      healthy: !bad,
      ...(bad ? { healthReason: unhealthy!.reason } : {}),
    };
  });
}

/** Fabric NICs — what makes multi-node tensor parallelism possible, and the
 * reason a card-dense node is worth scheduling onto at all. */
function nics(prefix: string, count: number, f: NicFlavour): DraDevice[] {
  return Array.from({ length: count }, (_, i) => ({
    name: `${prefix}-${i}`,
    kind: "nic",
    model: f.model,
    vendor: f.vendor,
    healthy: true,
    linkLayer: f.linkLayer,
    rateGbps: f.rateGbps,
  }));
}

// ── Workstation node ─────────────────────────────────────────────────────────
// Demo-only. The shared K8sNodeNode in topology.tsx renders a cluster node as a
// flat spec sheet, which makes an 8-GPU box look like any other row in a list.
// This card leads with the physical machine — chassis, then the bays the
// accelerators are actually plugged into — so a viewer reads "compute node with
// cards attached" rather than "a node that happens to mention a GPU name".
// It stays local to /topology/demo so the live topology page is untouched.

interface WorkstationData extends Record<string, unknown> {
  name: string;
  ip: string;
  chassis: string;
  formFactor: string;
  totalRamGb: number;
  cpuCores: number;
  backend: string;
  /** Physical accelerator slots in the chassis — the denominator of "6/8 used". */
  bays: number;
  modelFitCount: number;
  stale?: boolean;
  providers: { name: string; models: string[] }[];
  accelerators: DraDevice[];
}

/** The bay strip: one glyph per physical slot, filled or empty. This is the
 * whole point of the card — you can see the cards seated in the machine, and
 * see the headroom left in a half-populated box. */
function BayStrip({ cards: seated, bays }: { cards: DraDevice[]; bays: number }) {
  const slots = Array.from({ length: bays }, (_, i) => seated[i]);
  return (
    <div className="flex flex-wrap gap-[3px] mt-1 mb-1.5">
      {slots.map((d, i) => (
        <div
          key={i}
          title={
            d
              ? `bay ${i}: ${d.model}${d.healthy ? "" : ` — ${d.healthReason}`}`
              : `bay ${i}: empty`
          }
          className={
            "h-3.5 w-2.5 border-[1.5px] " +
            (!d
              ? "border-dashed border-foreground/25"
              : d.healthy
                ? "border-emerald-400/70 bg-emerald-400/40"
                : "border-destructive bg-destructive/40")
          }
        />
      ))}
    </div>
  );
}

function WorkstationNode({ data }: NodeProps<Node<WorkstationData>>) {
  const seated = data.accelerators.filter((d) => d.kind === "gpu" || d.kind === "npu");
  const fabric = data.accelerators.filter((d) => d.kind === "nic");
  const groups = groupAccelerators(seated);
  const fabricGroups = groupAccelerators(fabric);
  const faults = seated.filter((d) => !d.healthy);

  return (
    <div className="border border-foreground/20 bg-card min-w-[280px] max-w-[300px] shadow-md cursor-pointer hover:border-primary/50 hover:bg-primary/5 transition-colors">
      <Handle type="target" position={Position.Top} className="!bg-foreground !w-2 !h-2" />
      <Handle type="source" position={Position.Bottom} className="!bg-foreground !w-2 !h-2" />

      {/* Chassis header — the machine itself */}
      <div className="flex items-center justify-between border-b border-foreground/15 bg-foreground/[0.04] px-3 py-1">
        <span className="text-[8px] uppercase tracking-[0.15em] text-muted-foreground">
          Workstation
        </span>
        <span className="text-[8px] font-mono text-muted-foreground">
          {data.formFactor}
        </span>
      </div>

      <div className="px-3 py-2">
        <div className="flex items-center gap-2">
          <HardDrive className="h-4 w-4 text-foreground shrink-0" />
          <span className="font-semibold text-sm text-foreground truncate">{data.name}</span>
          {data.stale && (
            <Badge variant="destructive" className="text-[8px] px-1 py-0">stale</Badge>
          )}
        </div>
        <p className="text-[10px] text-muted-foreground font-mono">{data.ip}</p>
        <p className="text-[10px] text-muted-foreground truncate" title={data.chassis}>
          {data.chassis}
        </p>
        <div className="flex flex-wrap items-center gap-x-2.5 text-[10px] text-muted-foreground mt-0.5">
          <span>{data.totalRamGb} GB RAM</span>
          <span>{data.cpuCores} cores</span>
          <span>{data.backend}</span>
          <span>{data.modelFitCount} models fit</span>
        </div>

        {/* Accelerator bays */}
        <div className="mt-2 pt-1.5 border-t border-foreground/10">
          <div className="flex items-center justify-between">
            <span className="text-[8px] uppercase tracking-[0.15em] text-muted-foreground">
              Accelerator bays
            </span>
            <span
              className={
                "text-[9px] font-mono " +
                (faults.length > 0 ? "text-destructive" : "text-muted-foreground")
              }
            >
              {seated.length}/{data.bays} used
              {faults.length > 0 && ` · ${faults.length} fault`}
            </span>
          </div>
          {data.bays > 0 ? (
            <>
              <BayStrip cards={seated} bays={data.bays} />
              {groups.map((g) => (
                <div
                  key={g.key}
                  className={
                    "flex items-baseline gap-1.5 font-mono text-[10px] " +
                    (g.healthy ? "" : "text-destructive")
                  }
                  title={g.healthy ? g.names.join(", ") : `${g.names.join(", ")} — ${g.reasons.join(", ")}`}
                >
                  <span className="text-muted-foreground/60 select-none">└─</span>
                  <span className="shrink-0">{g.count}×</span>
                  <span className="truncate">{draDeviceDetail(g.sample)}</span>
                </div>
              ))}
            </>
          ) : (
            <p className="text-[10px] text-muted-foreground/70 italic mt-1">
              no accelerators — CPU inference only
            </p>
          )}
        </div>

        {/* Fabric — what makes multi-node tensor parallelism possible */}
        {fabricGroups.length > 0 && (
          <div className="mt-1.5 pt-1.5 border-t border-foreground/10">
            <span className="text-[8px] uppercase tracking-[0.15em] text-muted-foreground">
              Fabric
            </span>
            {fabricGroups.map((g) => (
              <div key={g.key} className="flex items-baseline gap-1.5 font-mono text-[10px]">
                <span className="text-muted-foreground/60 select-none">└─</span>
                <span className="shrink-0">{g.count}×</span>
                <span className="truncate">{draDeviceDetail(g.sample)}</span>
              </div>
            ))}
          </div>
        )}

        {data.providers.length > 0 && (
          <div className="flex flex-wrap gap-1 mt-2">
            {data.providers.map((p) => (
              <Badge
                key={p.name}
                variant="outline"
                className="text-[9px] border-foreground/20 text-foreground"
              >
                {p.name}
                {p.models?.length > 0 && ` (${p.models.length})`}
              </Badge>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

// ── Demo data generator ──────────────────────────────────────────────────────

// ── Simulated accelerators (demo only) ───────────────────────────────────────
// The demo has no cluster, so it invents both the inventory and the power
// readings. Specs are realistic (an A100 SXM really does idle near 50 W and
// peak near 400 W) so the canvas reads as a plausible fleet.

const GPU_SPECS: Record<
  string,
  {
    band: PowerBand;
    model: string;
    memoryGi: number;
    memoryBandwidthGBs: number;
    computeTFLOPS: number;
  }
> = {
  "NVIDIA A100 80GB": { band: BANDS.a100, model: "NVIDIA A100-SXM4-80GB", memoryGi: 80, memoryBandwidthGBs: 2039, computeTFLOPS: 312 },
  "NVIDIA H100 80GB": { band: BANDS.h100, model: "NVIDIA H100-SXM5-80GB", memoryGi: 80, memoryBandwidthGBs: 3350, computeTFLOPS: 989 },
  "NVIDIA H200 141GB": { band: BANDS.h200, model: "NVIDIA H200-SXM5-141GB", memoryGi: 141, memoryBandwidthGBs: 4800, computeTFLOPS: 989 },
  "NVIDIA L40S 48GB": { band: BANDS.l40s, model: "NVIDIA L40S", memoryGi: 48, memoryBandwidthGBs: 864, computeTFLOPS: 362 },
  "NVIDIA RTX 4090 24GB": { band: BANDS.rtx4090, model: "NVIDIA GeForce RTX 4090", memoryGi: 24, memoryBandwidthGBs: 1008, computeTFLOPS: 165 },
};

// Per-node accelerator count and how hard it is working (0 idle → 1 pegged).
// Loads stay <= ~0.78 so that load + the 0.22 drift amplitude lands at the
// band ceiling only occasionally: a real GPU does sit at its power cap under
// sustained load, but a demo parked on the rail shows a frozen number.
// The awkward cases are deliberate, not oversights: `suspendLast` parks one
// card so the demo shows a synthetic zero rendering as "— suspended", and the
// node whose fitness is already stale freezes its reading. Those are the
// states the UI most has to get right.
const DEMO_ACCEL: Record<string, { count: number; load: number; suspendLast?: boolean }> = {
  "gpu-node-a100-01": { count: 2, load: 0.78 },
  "gpu-node-a100-02": { count: 2, load: 0.71 },
  "gpu-node-a100-03": { count: 1, load: 0.63 },
  "gpu-node-a100-04": { count: 2, load: 0.24, suspendLast: true },
  "gpu-node-h100-01": { count: 2, load: 0.79 },
  "gpu-node-h100-02": { count: 2, load: 0.77 },
  "gpu-node-h100-03": { count: 1, load: 0.35 },
  "gpu-node-h200-01": { count: 2, load: 0.77 },
  "gpu-node-h200-02": { count: 1, load: 0.46 },
  "gpu-node-l40s-01": { count: 1, load: 0.58 },
  "gpu-node-l40s-02": { count: 1, load: 0.4 },
  "gpu-node-4090-01": { count: 1, load: 0.67 },
};

/** Accelerator inventory + matching simulation specs for one demo node. */
function demoAccelerators(
  name: string,
  gpuName: string,
  stale: boolean,
): { devices: DraDevice[]; sims: SimDevice[] } {
  const spec = GPU_SPECS[gpuName];
  const cfg = DEMO_ACCEL[name];
  if (!spec || !cfg) return { devices: [], sims: [] };

  const devices: DraDevice[] = [];
  const sims: SimDevice[] = [];
  for (let i = 0; i < cfg.count; i++) {
    // Plausible slot addressing; the exact value only has to be stable and
    // unique per node, since node+pci is the join key.
    const pci = `0000:${(0x1a + i * 8).toString(16).padStart(2, "0")}:00.0`;
    const suspended = !!cfg.suspendLast && i === cfg.count - 1;
    devices.push({
      name: `gpu-${pci.replace(/[:.]/g, "-")}`,
      kind: "gpu",
      model: spec.model,
      vendor: "nvidia",
      memoryGi: spec.memoryGi,
      memoryBandwidthGBs: spec.memoryBandwidthGBs,
      computeTFLOPS: spec.computeTFLOPS,
      healthy: true,
      pciAddress: pci,
    });
    sims.push({
      node: name,
      address: pci,
      kind: "gpu",
      driver: "nvidia",
      band: spec.band,
      load: cfg.load,
      suspended,
      stale,
    });
  }
  return { devices, sims };
}

function buildDemoTopology(): { nodes: Node[]; edges: Edge[]; simDevices: SimDevice[] } {
  const nodes: Node[] = [];
  const edges: Edge[] = [];
  const P = { x: 0, y: 0 };

  // ── Gateway ──────────────────────────────────────────────────────────────
  nodes.push({
    id: "gateway",
    type: "gateway",
    position: P,
    data: {
      ready: true,
      phase: "Ready",
      address: "gateway.sympozium.prod.internal:443",
      routes: [
        "prod-support-agent",
        "content-gen-writer",
        "incident-responder",
        "api-docs-agent",
        "chat-gateway",
        "financial-reports",
      ],
    },
  });

  // ── Workstations ─────────────────────────────────────────────────────────
  // An H200 estate: the same accelerator scaled across differently-shaped
  // machines, so the cards show full boxes, half-populated boxes with headroom,
  // faulted bays, and — for contrast — a render box and a CPU-only node.
  const k8sNodes = [
    {
      // HGX 8-GPU, 8U. 8× ConnectX-7 is the vendor's 1:1 GPUDirect ratio;
      // 2× BlueField-3 carry storage and north-south off the host cores.
      name: "ws-h200-01",
      ip: "10.42.3.30",
      chassis: "Supermicro AS-8125GS-TNHR",
      formFactor: "8U rack",
      totalRamGb: 2048, cpuCores: 192, backend: "CUDA", bays: 8, modelFitCount: 18,
      providers: [{ name: "vllm", models: ["llama-3.1-405b", "deepseek-r1-671b"] }],
      accelerators: [
        ...cards("gpu", "h200", 8, GPU.h200sxm),
        ...nics("mlx5", 8, NIC.cx7ndr),
        ...nics("bf3", 2, NIC.bf3),
      ],
    },
    {
      name: "ws-h200-02",
      ip: "10.42.3.31",
      chassis: "Supermicro AS-8125GS-TNHR",
      formFactor: "8U rack",
      totalRamGb: 2048, cpuCores: 192, backend: "CUDA", bays: 8, modelFitCount: 17,
      providers: [{ name: "vllm", models: ["llama-3.3-70b", "nemotron-70b"] }],
      accelerators: [
        ...cards("gpu", "h200", 8, GPU.h200sxm),
        ...nics("mlx5", 8, NIC.cx7ndr),
        ...nics("bf3", 2, NIC.bf3),
      ],
    },
    {
      name: "ws-h200-03",
      ip: "10.42.3.32",
      chassis: "Dell PowerEdge XE9680",
      formFactor: "6U rack",
      totalRamGb: 2048, cpuCores: 224, backend: "CUDA", bays: 8, modelFitCount: 14,
      providers: [{ name: "vllm", models: ["mixtral-8x22b"] }, { name: "tgi", models: ["command-r-plus"] }],
      // One bay faulted — the strip renders that slot red. The board still
      // has all 8 modules seated; a dead SXM is a fault, not an empty bay.
      accelerators: [
        ...cards("gpu", "h200", 8, GPU.h200sxm, { index: 5, reason: "ECC uncorrectable" }),
        ...nics("mlx5", 8, NIC.cx7ndr),
        ...nics("bf3", 2, NIC.bf3),
      ],
    },
    {
      // 5U liquid-cooled HGX. Fixed 8-GPU baseboard, so it is always full —
      // headroom on an SXM box is not a thing that exists.
      name: "ws-h200-04",
      ip: "10.42.3.33",
      chassis: "Lenovo ThinkSystem SR780a V3",
      formFactor: "5U rack",
      totalRamGb: 1024, cpuCores: 128, backend: "CUDA", bays: 8, modelFitCount: 16,
      providers: [{ name: "vllm", models: ["qwen2.5-72b", "codestral-22b"] }],
      accelerators: [...cards("gpu", "h200", 8, GPU.h200sxm), ...nics("mlx5", 8, NIC.cx7ndr)],
    },
    {
      // PCIe NVL box: 4 of 8 slots filled, so this is where the half-populated
      // "headroom" visual belongs — slots you really could fill later.
      name: "ws-h200-05",
      ip: "10.42.3.34",
      chassis: "Lenovo ThinkSystem SR675 V3",
      formFactor: "3U rack",
      totalRamGb: 1024, cpuCores: 128, backend: "CUDA", bays: 8, modelFitCount: 8,
      providers: [{ name: "vllm", models: ["llama-3.1-70b", "qwen2.5-32b"] }],
      accelerators: [
        ...cards("gpu", "h200nvl", 4, GPU.h200nvl),
        ...nics("mlx5", 2, NIC.cx7ndr),
        ...nics("bf3", 1, NIC.bf3),
      ],
    },
    {
      name: "ws-h200-07",
      ip: "10.42.3.36",
      chassis: "Dell PowerEdge XE9680",
      formFactor: "6U rack",
      totalRamGb: 2048, cpuCores: 224, backend: "CUDA", bays: 8, modelFitCount: 16,
      providers: [{ name: "vllm", models: ["dbrx-instruct", "mistral-large-2"] }],
      accelerators: [
        ...cards("gpu", "h200", 8, GPU.h200sxm),
        ...nics("mlx5", 8, NIC.cx7ndr),
        ...nics("bf3", 2, NIC.bf3),
      ],
    },
    {
      // Tower class. No tower takes a 600 W H200 NVL — an L40S box is what a
      // team actually has under a desk, so these are named for what they hold.
      name: "ws-l40s-02",
      ip: "10.42.4.41",
      chassis: "Supermicro SYS-741GE-TNRT",
      formFactor: "tower",
      totalRamGb: 512, cpuCores: 64, backend: "CUDA", bays: 4, modelFitCount: 4,
      providers: [{ name: "vllm", models: ["gemma-2-27b", "starcoder2-15b"] }],
      accelerators: [...cards("gpu", "l40s", 4, GPU.l40s), ...nics("mlx5", 1, NIC.cx6dx)],
    },
    {
      name: "ws-l40s-03",
      ip: "10.42.4.42",
      chassis: "Supermicro SYS-741GE-TNRT",
      formFactor: "tower",
      totalRamGb: 512, cpuCores: 64, backend: "CUDA", bays: 4, modelFitCount: 2,
      providers: [{ name: "ollama", models: ["llama-3.2-8b", "phi-4-14b"] }],
      accelerators: [...cards("gpu", "l40s", 2, GPU.l40s), ...nics("mlx5", 1, NIC.cx6dx)],
    },
    {
      name: "ws-l40s-01",
      ip: "10.42.4.40",
      chassis: "Dell Precision 7960 Tower",
      formFactor: "tower",
      totalRamGb: 256, cpuCores: 32, backend: "CUDA", bays: 4, modelFitCount: 2,
      providers: [
        { name: "vllm", models: ["whisper-large-v3", "nomic-embed-text"] },
        { name: "tgi", models: ["e5-mistral-7b"] },
      ],
      accelerators: [
        ...cards("gpu", "l40s", 2, GPU.l40s, { index: 1, reason: "thermal throttle" }),
        ...nics("mlx5", 1, NIC.cx6dx),
      ],
      stale: true,
    },
    {
      name: "cpu-node-arm-01",
      ip: "10.42.8.80",
      chassis: "Ampere Altra Max M128-30",
      formFactor: "1U rack",
      totalRamGb: 256, cpuCores: 128, backend: "CPU", bays: 0, modelFitCount: 1,
      providers: [{ name: "llama.cpp", models: ["qwen2.5-3b-gguf"] }],
      accelerators: [],
    },
  ];

  const simDevices: SimDevice[] = [];
  for (const pn of k8sNodes) {
    const { devices, sims } = demoAccelerators(pn.name, pn.fitness.gpuName, pn.fitness.stale);
    simDevices.push(...sims);
    nodes.push({
      id: `node-${pn.name}`,
      type: "workstation",
      position: P,
      data: { ...pn, accelerators: devices },
    });
  }

  // ── Cloud Providers ──────────────────────────────────────────────────────
  const cloudProviders = [
    { id: "openai", label: "OpenAI" },
    { id: "anthropic", label: "Anthropic" },
    { id: "azure-openai", label: "Azure OpenAI" },
    { id: "bedrock", label: "AWS Bedrock" },
    { id: "google", label: "Google Vertex AI" },
    { id: "groq", label: "Groq" },
  ];

  for (const cp of cloudProviders) {
    nodes.push({
      id: `cp-${cp.id}`,
      type: "cloudProvider",
      position: P,
      data: { provider: cp.id, label: cp.label },
    });
  }

  // ── Models (deployed pods) ───────────────────────────────────────────────
  const models = [
    { name: "llama-3.1-405b", phase: "Ready", serverType: "vllm", gpu: 4, node: "ws-h200-01" },
    { name: "deepseek-r1-671b", phase: "Ready", serverType: "vllm", gpu: 4, node: "ws-h200-01" },
    { name: "llama-3.3-70b", phase: "Ready", serverType: "vllm", gpu: 4, node: "ws-h200-02" },
    { name: "nemotron-70b", phase: "Ready", serverType: "vllm", gpu: 4, node: "ws-h200-02" },
    { name: "mixtral-8x22b", phase: "Ready", serverType: "vllm", gpu: 4, node: "ws-h200-03" },
    { name: "command-r-plus", phase: "Ready", serverType: "tgi", gpu: 3, node: "ws-h200-03" },
    { name: "qwen2.5-72b", phase: "Loading", serverType: "vllm", gpu: 2, node: "ws-h200-04" },
    { name: "codestral-22b", phase: "Ready", serverType: "vllm", gpu: 2, node: "ws-h200-04" },
    { name: "llama-3.1-70b", phase: "Ready", serverType: "vllm", gpu: 2, node: "ws-h200-05" },
    { name: "qwen2.5-32b", phase: "Ready", serverType: "vllm", gpu: 2, node: "ws-h200-05" },
    // 27B at bf16 is ~54 GB — over one 48 GB L40S, so it runs TP=2.
    { name: "gemma-2-27b", phase: "Ready", serverType: "vllm", gpu: 2, node: "ws-l40s-02" },
    { name: "starcoder2-15b", phase: "Ready", serverType: "vllm", gpu: 1, node: "ws-l40s-02" },
    { name: "dbrx-instruct", phase: "Loading", serverType: "vllm", gpu: 4, node: "ws-h200-07" },
    { name: "mistral-large-2", phase: "Ready", serverType: "vllm", gpu: 4, node: "ws-h200-07" },
    { name: "llama-3.2-8b", phase: "Ready", serverType: "ollama", gpu: 1, node: "ws-l40s-03" },
    { name: "phi-4-14b", phase: "Ready", serverType: "ollama", gpu: 1, node: "ws-l40s-03" },
    { name: "whisper-large-v3", phase: "Ready", serverType: "vllm", gpu: 1, node: "ws-l40s-01" },
    { name: "e5-mistral-7b", phase: "Ready", serverType: "tgi", gpu: 1, node: "ws-l40s-01" },
    { name: "nomic-embed-text", phase: "Failed", serverType: "vllm", gpu: 1, node: "ws-l40s-01" },
    { name: "qwen2.5-3b-gguf", phase: "Ready", serverType: "llama.cpp", gpu: 0, node: "cpu-node-arm-01" },
  ];

  for (const m of models) {
    const modelId = `model-${m.name}`;
    nodes.push({
      id: modelId,
      type: "model",
      position: P,
      data: { name: m.name, namespace: "sympozium-system", phase: m.phase, serverType: m.serverType, gpu: m.gpu },
    });
    edges.push({
      id: `e-${modelId}-node-${m.node}`,
      source: `node-${m.node}`,
      target: modelId,
      style: { stroke: "#8a8c82", strokeWidth: 1.5 },
      markerEnd: { type: MarkerType.ArrowClosed, color: "#8a8c82" },
      animated: m.phase === "Loading",
      label: "runs on",
      labelStyle: { fontSize: 9, fill: "#8a8c82" },
      labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
      labelBgPadding: [4, 2] as [number, number],
    });
  }

  // ── Ensembles ────────────────────────────────────────────────────────────
  const ensembles: {
    name: string; personas: string[];
    rels: { type: string; source: string; target: string }[];
    provider: string | null; model: string | null;
    stimulus: { name: string; generation: number } | null;
    runningCount: number;
    features: { delegation: boolean; sequential: boolean; supervision: boolean; subagents: boolean };
    webEndpoint: boolean;
  }[] = [
    {
      name: "research-team",
      personas: ["researcher", "analyst", "synthesizer", "fact-checker"],
      rels: [
        { type: "delegation", source: "researcher", target: "analyst" },
        { type: "delegation", source: "researcher", target: "fact-checker" },
        { type: "sequential", source: "analyst", target: "synthesizer" },
      ],
      provider: "anthropic",
      model: "deepseek-r1-671b",
      stimulus: { name: "daily-research", generation: 14 },
      runningCount: 3,
      features: { delegation: true, sequential: true, supervision: false, subagents: true },
      webEndpoint: false,
    },
    {
      name: "code-review",
      personas: ["reviewer", "security-scanner", "style-linter"],
      rels: [
        { type: "delegation", source: "reviewer", target: "security-scanner" },
        { type: "delegation", source: "reviewer", target: "style-linter" },
      ],
      provider: "openai",
      model: "codestral-22b",
      stimulus: { name: "pr-webhook", generation: 42 },
      runningCount: 2,
      features: { delegation: true, sequential: false, supervision: false, subagents: true },
      webEndpoint: false,
    },
    {
      name: "customer-support",
      personas: ["triage-agent", "resolver", "escalation-agent", "feedback-collector"],
      rels: [
        { type: "sequential", source: "triage-agent", target: "resolver" },
        { type: "delegation", source: "resolver", target: "escalation-agent" },
        { type: "supervision", source: "escalation-agent", target: "resolver" },
      ],
      provider: null,
      model: "llama-3.1-70b",
      stimulus: { name: "ticket-stream", generation: 187 },
      runningCount: 5,
      features: { delegation: true, sequential: true, supervision: true, subagents: false },
      webEndpoint: true,
    },
    {
      name: "data-pipeline",
      personas: ["ingester", "transformer", "validator", "loader", "monitor"],
      rels: [
        { type: "sequential", source: "ingester", target: "transformer" },
        { type: "sequential", source: "transformer", target: "validator" },
        { type: "sequential", source: "validator", target: "loader" },
        { type: "supervision", source: "monitor", target: "loader" },
      ],
      provider: "azure-openai",
      model: "mixtral-8x22b",
      stimulus: { name: "cron-hourly", generation: 1203 },
      runningCount: 4,
      features: { delegation: false, sequential: true, supervision: true, subagents: false },
      webEndpoint: false,
    },
    {
      name: "content-gen",
      personas: ["writer", "editor", "seo-optimizer"],
      rels: [
        { type: "sequential", source: "writer", target: "editor" },
        { type: "sequential", source: "editor", target: "seo-optimizer" },
      ],
      provider: null,
      model: "llama-3.3-70b",
      stimulus: null,
      runningCount: 1,
      features: { delegation: false, sequential: true, supervision: false, subagents: false },
      webEndpoint: true,
    },
    {
      name: "security-audit",
      personas: ["vuln-scanner", "compliance-checker", "report-gen"],
      rels: [
        { type: "delegation", source: "vuln-scanner", target: "compliance-checker" },
        { type: "sequential", source: "compliance-checker", target: "report-gen" },
      ],
      provider: "bedrock",
      model: "llama-3.1-405b",
      stimulus: { name: "nightly-scan", generation: 89 },
      runningCount: 2,
      features: { delegation: true, sequential: true, supervision: false, subagents: true },
      webEndpoint: false,
    },
    {
      name: "incident-response",
      personas: ["detector", "diagnostician", "remediator", "communicator"],
      rels: [
        { type: "sequential", source: "detector", target: "diagnostician" },
        { type: "delegation", source: "diagnostician", target: "remediator" },
        { type: "sequential", source: "remediator", target: "communicator" },
        { type: "supervision", source: "communicator", target: "remediator" },
      ],
      provider: "google",
      model: "gemma-2-27b",
      stimulus: { name: "alert-feed", generation: 31 },
      runningCount: 3,
      features: { delegation: true, sequential: true, supervision: true, subagents: true },
      webEndpoint: true,
    },
    {
      name: "doc-writer",
      personas: ["api-crawler", "doc-author", "reviewer"],
      rels: [
        { type: "sequential", source: "api-crawler", target: "doc-author" },
        { type: "supervision", source: "reviewer", target: "doc-author" },
      ],
      provider: "groq",
      model: null,
      stimulus: null,
      runningCount: 1,
      features: { delegation: false, sequential: true, supervision: true, subagents: false },
      webEndpoint: true,
    },
    // ── New ensembles ──────────────────────────────────────────────────────
    {
      name: "code-gen",
      personas: ["planner", "coder", "test-writer", "ci-runner"],
      rels: [
        { type: "sequential", source: "planner", target: "coder" },
        { type: "sequential", source: "coder", target: "test-writer" },
        { type: "delegation", source: "test-writer", target: "ci-runner" },
      ],
      provider: "anthropic",
      model: "starcoder2-15b",
      stimulus: { name: "issue-assignment", generation: 67 },
      runningCount: 3,
      features: { delegation: true, sequential: true, supervision: false, subagents: true },
      webEndpoint: false,
    },
    {
      name: "translation-hub",
      personas: ["translator", "localizer", "qa-reviewer"],
      rels: [
        { type: "sequential", source: "translator", target: "localizer" },
        { type: "supervision", source: "qa-reviewer", target: "localizer" },
      ],
      provider: null,
      model: "command-r-plus",
      stimulus: { name: "content-update", generation: 312 },
      runningCount: 2,
      features: { delegation: false, sequential: true, supervision: true, subagents: false },
      webEndpoint: false,
    },
    {
      name: "financial-analysis",
      personas: ["data-collector", "risk-modeler", "report-writer", "compliance-reviewer"],
      rels: [
        { type: "sequential", source: "data-collector", target: "risk-modeler" },
        { type: "sequential", source: "risk-modeler", target: "report-writer" },
        { type: "supervision", source: "compliance-reviewer", target: "report-writer" },
        { type: "delegation", source: "risk-modeler", target: "compliance-reviewer" },
      ],
      provider: "openai",
      model: "llama-3.1-405b",
      stimulus: { name: "market-close", generation: 521 },
      runningCount: 4,
      features: { delegation: true, sequential: true, supervision: true, subagents: false },
      webEndpoint: true,
    },
    {
      name: "rag-indexer",
      personas: ["crawler", "chunker", "embedder", "index-writer"],
      rels: [
        { type: "sequential", source: "crawler", target: "chunker" },
        { type: "sequential", source: "chunker", target: "embedder" },
        { type: "sequential", source: "embedder", target: "index-writer" },
      ],
      provider: null,
      model: "e5-mistral-7b",
      stimulus: { name: "doc-watcher", generation: 4820 },
      runningCount: 3,
      features: { delegation: false, sequential: true, supervision: false, subagents: false },
      webEndpoint: false,
    },
    {
      name: "chat-gateway",
      personas: ["router", "general-agent", "specialist-agent", "memory-manager"],
      rels: [
        { type: "delegation", source: "router", target: "general-agent" },
        { type: "delegation", source: "router", target: "specialist-agent" },
        { type: "sequential", source: "general-agent", target: "memory-manager" },
      ],
      provider: "anthropic",
      model: "nemotron-70b",
      stimulus: null,
      runningCount: 4,
      features: { delegation: true, sequential: true, supervision: false, subagents: true },
      webEndpoint: true,
    },
    {
      name: "deploy-orchestrator",
      personas: ["change-detector", "build-agent", "test-runner", "deployer", "rollback-agent"],
      rels: [
        { type: "sequential", source: "change-detector", target: "build-agent" },
        { type: "sequential", source: "build-agent", target: "test-runner" },
        { type: "sequential", source: "test-runner", target: "deployer" },
        { type: "delegation", source: "deployer", target: "rollback-agent" },
        { type: "supervision", source: "rollback-agent", target: "deployer" },
      ],
      provider: null,
      model: "phi-4-14b",
      stimulus: { name: "git-push-main", generation: 198 },
      runningCount: 3,
      features: { delegation: true, sequential: true, supervision: true, subagents: true },
      webEndpoint: false,
    },
    {
      name: "voice-transcription",
      personas: ["audio-ingest", "transcriber", "summarizer"],
      rels: [
        { type: "sequential", source: "audio-ingest", target: "transcriber" },
        { type: "sequential", source: "transcriber", target: "summarizer" },
      ],
      provider: null,
      model: "whisper-large-v3",
      stimulus: { name: "meeting-end", generation: 73 },
      runningCount: 2,
      features: { delegation: false, sequential: true, supervision: false, subagents: false },
      webEndpoint: false,
    },
    {
      name: "eval-harness",
      personas: ["prompt-gen", "model-runner", "scorer", "reporter"],
      rels: [
        { type: "sequential", source: "prompt-gen", target: "model-runner" },
        { type: "sequential", source: "model-runner", target: "scorer" },
        { type: "sequential", source: "scorer", target: "reporter" },
      ],
      provider: "groq",
      model: "mistral-large-2",
      stimulus: { name: "nightly-eval", generation: 45 },
      runningCount: 2,
      features: { delegation: false, sequential: true, supervision: false, subagents: false },
      webEndpoint: false,
    },
  ];

  // Ensemble + persona + stimulus nodes
  for (const ens of ensembles) {
    const ensId = `ens-${ens.name}`;
    nodes.push({
      id: ensId,
      type: "ensemble",
      position: P,
      data: {
        name: ens.name,
        description: "",
        enabled: true,
        personas: ens.personas,
        runningCount: ens.runningCount,
        hasDelegation: ens.features.delegation,
        hasSequential: ens.features.sequential,
        hasSupervision: ens.features.supervision,
        hasSubagents: ens.features.subagents,
      },
    });

    // Provider / model edges
    if (ens.model) {
      edges.push({
        id: `e-${ensId}-model-${ens.model}`,
        source: `model-${ens.model}`,
        target: ensId,
        style: { stroke: "#6366f1", strokeWidth: 1.5, strokeDasharray: "4 3" },
        markerEnd: { type: MarkerType.ArrowClosed, color: "#6366f1" },
        animated: true,
        label: "inference",
        labelStyle: { fontSize: 9, fill: "#8a8c82" },
        labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
        labelBgPadding: [4, 2] as [number, number],
      });
    }
    if (ens.provider) {
      edges.push({
        id: `e-cp-${ens.provider}-${ensId}`,
        source: `cp-${ens.provider}`,
        target: ensId,
        style: { stroke: "#f97316", strokeWidth: 1.5, strokeDasharray: "4 3" },
        markerEnd: { type: MarkerType.ArrowClosed, color: "#f97316" },
        label: "inference",
        labelStyle: { fontSize: 9, fill: "#8a8c82" },
        labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
        labelBgPadding: [4, 2] as [number, number],
      });
    }

    // Stimulus
    if (ens.stimulus) {
      const stimId = `${ensId}-stim`;
      nodes.push({
        id: stimId,
        type: "stimulus",
        position: P,
        data: {
          name: ens.stimulus.name,
          prompt: "Demo stimulus prompt",
          ensembleName: ens.name,
          delivered: true,
          generation: ens.stimulus.generation,
          label: ens.stimulus.name,
        },
      });
      edges.push({
        id: `e-${ensId}-stim`,
        source: ensId,
        target: stimId,
        style: { stroke: "#f59e0b60", strokeWidth: 1 },
      });
      // Stimulus triggers first persona
      edges.push({
        id: `e-stim-${ensId}-${ens.personas[0]}`,
        source: stimId,
        target: `${ensId}-p-${ens.personas[0]}`,
        style: { stroke: "#f59e0b", strokeWidth: 1.5 },
        markerEnd: { type: MarkerType.ArrowClosed, color: "#f59e0b" },
        label: "triggers",
        labelStyle: { fontSize: 8, fill: "#8a8c82" },
        labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
        labelBgPadding: [4, 2] as [number, number],
      });
    }

    // Persona nodes
    const runPhases = ["Running", "Running", "Serving", "Succeeded", "Running", "Pending"];
    for (let i = 0; i < ens.personas.length; i++) {
      const pName = ens.personas[i];
      const pid = `${ensId}-p-${pName}`;
      nodes.push({
        id: pid,
        type: "persona",
        position: P,
        data: {
          name: pName,
          displayName: pName.replace(/-/g, " ").replace(/\b\w/g, (c: string) => c.toUpperCase()),
          runPhase: i < ens.runningCount ? runPhases[i % runPhases.length] : (Math.random() > 0.5 ? "Succeeded" : undefined),
        },
      });
      edges.push({
        id: `e-${ensId}-${pid}`,
        source: ensId,
        target: pid,
        style: { stroke: "#e8562a40", strokeWidth: 1 },
      });
    }

    // Relationship edges
    for (const rel of ens.rels) {
      const srcId = `${ensId}-p-${rel.source}`;
      const tgtId = `${ensId}-p-${rel.target}`;
      const relColor =
        rel.type === "delegation" ? "#f0ece4"
          : rel.type === "sequential" ? "#fbbf24"
            : "#8a8c82";
      edges.push({
        id: `e-rel-${ensId}-${rel.source}-${rel.target}`,
        source: srcId,
        target: tgtId,
        style: rel.type === "delegation"
          ? { stroke: relColor, strokeWidth: 1.5 }
          : { stroke: relColor, strokeWidth: 1, strokeDasharray: "4 3" },
        markerEnd: { type: MarkerType.ArrowClosed, color: relColor },
        label: rel.type,
        labelStyle: { fontSize: 8, fill: "#8a8c82" },
        labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
        labelBgPadding: [4, 2] as [number, number],
        animated: rel.type === "delegation",
      });
    }

    // Gateway edges for web endpoints
    if (ens.webEndpoint) {
      edges.push({
        id: `e-gw-${ens.name}`,
        source: "gateway",
        target: ensId,
        style: { stroke: "#f59e0b", strokeWidth: 1.5, strokeDasharray: "6 3" },
        markerEnd: { type: MarkerType.ArrowClosed, color: "#f59e0b" },
        label: "web endpoint",
        labelStyle: { fontSize: 9, fill: "#8a8c82" },
        labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
        labelBgPadding: [4, 2] as [number, number],
      });
    }
  }

  // ── Active runs ──────────────────────────────────────────────────────────
  const activeRuns = [
    // research-team
    { id: "a1b2c3d4", task: "Analyzing quarterly revenue trends across EMEA", phase: "Running", ensemble: "research-team", persona: "researcher" },
    { id: "e5f6g7h8", task: "Cross-referencing patent filings with market data", phase: "Running", ensemble: "research-team", persona: "analyst" },
    { id: "x9k2m4n6", task: "Verifying cited sources in research summary", phase: "Running", ensemble: "research-team", persona: "fact-checker" },
    // code-review
    { id: "i9j0k1l2", task: "Reviewing PR #847: auth middleware refactor", phase: "Running", ensemble: "code-review", persona: "reviewer" },
    { id: "m3n4o5p6", task: "Scanning for SQL injection in query builder", phase: "Running", ensemble: "code-review", persona: "security-scanner" },
    // customer-support
    { id: "q7r8s9t0", task: "Resolving ticket #4521: billing discrepancy", phase: "Running", ensemble: "customer-support", persona: "resolver" },
    { id: "u1v2w3x4", task: "Triaging incoming ticket batch #892", phase: "Running", ensemble: "customer-support", persona: "triage-agent" },
    { id: "y5z6a7b8", task: "Escalating P1: payment gateway timeout", phase: "Running", ensemble: "customer-support", persona: "escalation-agent" },
    { id: "c9d0e1f2", task: "Collecting CSAT survey responses", phase: "Serving", ensemble: "customer-support", persona: "feedback-collector" },
    // data-pipeline
    { id: "g3h4i5j6", task: "Transforming raw clickstream events", phase: "Running", ensemble: "data-pipeline", persona: "transformer" },
    { id: "k7l8m9n0", task: "Validating schema for batch #12847", phase: "Running", ensemble: "data-pipeline", persona: "validator" },
    { id: "o1p2q3r4", task: "Loading validated records to warehouse", phase: "Running", ensemble: "data-pipeline", persona: "loader" },
    { id: "s5t6u7v8", task: "Monitoring pipeline throughput metrics", phase: "Serving", ensemble: "data-pipeline", persona: "monitor" },
    // content-gen
    { id: "w9x0y1z2", task: "Writing blog post: 5 tips for API design", phase: "Running", ensemble: "content-gen", persona: "writer" },
    // security-audit
    { id: "b3c4d5e6", task: "Running CVE scan on container images", phase: "Running", ensemble: "security-audit", persona: "vuln-scanner" },
    { id: "f7g8h9i0", task: "Checking SOC2 compliance requirements", phase: "Running", ensemble: "security-audit", persona: "compliance-checker" },
    // incident-response
    { id: "j1k2l3m4", task: "Investigating memory spike on prod-web-03", phase: "Running", ensemble: "incident-response", persona: "diagnostician" },
    { id: "n5o6p7q8", task: "Applying hotfix to connection pool config", phase: "Running", ensemble: "incident-response", persona: "remediator" },
    { id: "r9s0t1u2", task: "Drafting incident update for #INC-2847", phase: "Pending", ensemble: "incident-response", persona: "communicator" },
    // doc-writer
    { id: "v3w4x5y6", task: "Crawling /v2/payments API endpoints", phase: "Running", ensemble: "doc-writer", persona: "api-crawler" },
    // code-gen
    { id: "cg01ab02", task: "Planning implementation for JIRA-4291", phase: "Running", ensemble: "code-gen", persona: "planner" },
    { id: "cg03cd04", task: "Implementing auth token refresh logic", phase: "Running", ensemble: "code-gen", persona: "coder" },
    { id: "cg05ef06", task: "Generating unit tests for TokenService", phase: "Running", ensemble: "code-gen", persona: "test-writer" },
    // translation-hub
    { id: "th01gh02", task: "Translating docs to Japanese (batch 14)", phase: "Running", ensemble: "translation-hub", persona: "translator" },
    { id: "th03ij04", task: "Localizing date formats for ja-JP", phase: "Running", ensemble: "translation-hub", persona: "localizer" },
    // financial-analysis
    { id: "fa01kl02", task: "Collecting NASDAQ close data 2026-05-17", phase: "Running", ensemble: "financial-analysis", persona: "data-collector" },
    { id: "fa03mn04", task: "Running Monte Carlo on portfolio risk", phase: "Running", ensemble: "financial-analysis", persona: "risk-modeler" },
    { id: "fa05op06", task: "Generating daily risk exposure report", phase: "Running", ensemble: "financial-analysis", persona: "report-writer" },
    { id: "fa07qr08", task: "Reviewing Basel III compliance flags", phase: "Serving", ensemble: "financial-analysis", persona: "compliance-reviewer" },
    // rag-indexer
    { id: "ri01st02", task: "Crawling confluence wiki space ENG", phase: "Running", ensemble: "rag-indexer", persona: "crawler" },
    { id: "ri03uv04", task: "Chunking 847 documents at 512 tokens", phase: "Running", ensemble: "rag-indexer", persona: "chunker" },
    { id: "ri05wx06", task: "Generating embeddings for chunk batch 23", phase: "Running", ensemble: "rag-indexer", persona: "embedder" },
    // chat-gateway
    { id: "cw01yz02", task: "Routing user query to specialist", phase: "Running", ensemble: "chat-gateway", persona: "router" },
    { id: "cw03ab04", task: "Handling general Q&A session", phase: "Running", ensemble: "chat-gateway", persona: "general-agent" },
    { id: "cw05cd06", task: "Deep-diving billing API question", phase: "Running", ensemble: "chat-gateway", persona: "specialist-agent" },
    { id: "cw07ef08", task: "Persisting conversation to memory store", phase: "Serving", ensemble: "chat-gateway", persona: "memory-manager" },
    // deploy-orchestrator
    { id: "do01gh02", task: "Detected 3 new commits on main", phase: "Running", ensemble: "deploy-orchestrator", persona: "change-detector" },
    { id: "do03ij04", task: "Building container image v2.14.7", phase: "Running", ensemble: "deploy-orchestrator", persona: "build-agent" },
    { id: "do05kl06", task: "Running integration test suite", phase: "Running", ensemble: "deploy-orchestrator", persona: "test-runner" },
    // voice-transcription
    { id: "vt01mn02", task: "Ingesting recording: eng-standup-0518", phase: "Running", ensemble: "voice-transcription", persona: "audio-ingest" },
    { id: "vt03op04", task: "Transcribing 47min meeting audio", phase: "Running", ensemble: "voice-transcription", persona: "transcriber" },
    // eval-harness
    { id: "eh01qr02", task: "Generating MMLU prompts batch 8", phase: "Running", ensemble: "eval-harness", persona: "prompt-gen" },
    { id: "eh03st04", task: "Running inference on 500 eval prompts", phase: "Running", ensemble: "eval-harness", persona: "model-runner" },
  ];

  // Sub-agent runs
  const subRuns = [
    { id: "sub-aa11bb", task: "Deep-diving EMEA market segment Q3", phase: "Running", parent: "a1b2c3d4", ensemble: "research-team", persona: "researcher" },
    { id: "sub-cc22dd", task: "Checking OWASP Top 10 patterns", phase: "Running", parent: "m3n4o5p6", ensemble: "code-review", persona: "security-scanner" },
    { id: "sub-ee33ff", task: "Querying PagerDuty for correlated alerts", phase: "Running", parent: "j1k2l3m4", ensemble: "incident-response", persona: "diagnostician" },
    { id: "sub-gg44hh", task: "Fetching CVE database for libssl", phase: "Running", parent: "b3c4d5e6", ensemble: "security-audit", persona: "vuln-scanner" },
    { id: "sub-ii55jj", task: "Running pytest on generated tests", phase: "Running", parent: "cg05ef06", ensemble: "code-gen", persona: "test-writer" },
    { id: "sub-kk66ll", task: "Querying Bloomberg terminal feed", phase: "Running", parent: "fa01kl02", ensemble: "financial-analysis", persona: "data-collector" },
    { id: "sub-mm77nn", task: "Spawning canary deploy to staging", phase: "Pending", parent: "do05kl06", ensemble: "deploy-orchestrator", persona: "test-runner" },
  ];

  for (const run of activeRuns) {
    const runId = `run-${run.id}`;
    const personaId = `ens-${run.ensemble}-p-${run.persona}`;
    nodes.push({
      id: runId,
      type: "agentRun",
      position: P,
      data: { runName: run.id, task: run.task, phase: run.phase, isSubAgent: false, label: run.id },
    });
    edges.push({
      id: `e-run-${run.id}`,
      source: personaId,
      target: runId,
      style: { stroke: "#22d3ee40", strokeWidth: 1 },
      animated: true,
    });
  }

  for (const run of subRuns) {
    const runId = `run-${run.id}`;
    const parentRunId = `run-${run.parent}`;
    nodes.push({
      id: runId,
      type: "agentRun",
      position: P,
      data: { runName: run.id, task: run.task, phase: run.phase, isSubAgent: true, label: run.id },
    });
    edges.push({
      id: `e-run-${run.id}`,
      source: parentRunId,
      target: runId,
      style: { stroke: "#2dd4bf40", strokeWidth: 1, strokeDasharray: "4 2" },
      animated: true,
    });
  }

  // ── Gateway → every K8s node (forces same dagre rank) ─────────────────
  for (const pn of k8sNodes) {
    edges.push({
      id: `e-gw-node-${pn.name}`,
      source: "gateway",
      target: `node-${pn.name}`,
      style: { stroke: "#f59e0b20", strokeWidth: 1 },
    });
  }

  applyDemoLayout(nodes, edges);
  return { nodes, edges, simDevices };
}

// ── Hybrid layout: manual bands for infra, dagre for ensemble subtrees ───────

function applyDemoLayout(nodes: Node[], edges: Edge[]): void {
  const nodeMap = new Map(nodes.map((n) => [n.id, n]));

  // Y bands. The workstation cards are ~250px tall — far taller than the flat
  // node card this demo used to draw — so the rows below them need the room or
  // the fleet overlaps the models it serves.
  const Y_GW = 0;
  const Y_K8S = 200;
  const Y_INFRA = 520; // models + cloud providers
  const Y_ENS_BASE = 780; // ensembles start here; dagre offsets below

  // ── 1. Classify nodes ────────────────────────────────────────────────────
  const infraTypes = new Set(["gateway", "workstation", "cloudProvider", "model"]);
  const infraNodes: Node[] = [];
  const appNodes: Node[] = []; // ensemble, stimulus, persona, agentRun

  for (const n of nodes) {
    if (infraTypes.has(n.type || "")) infraNodes.push(n);
    else appNodes.push(n);
  }

  // ── 2. K8s nodes — evenly spaced row ─────────────────────────────────────
  const k8s = infraNodes.filter((n) => n.type === "workstation");
  const K8S_GAP = 360;
  const totalInfraW = k8s.length * K8S_GAP;
  const k8sStart = -totalInfraW / 2;
  const k8sCenters = new Map<string, number>();

  for (let i = 0; i < k8s.length; i++) {
    const x = k8sStart + i * K8S_GAP;
    k8s[i].position = { x, y: Y_K8S };
    k8sCenters.set(k8s[i].id, x + 140);
  }

  // ── 3. Gateway — centered above K8s ──────────────────────────────────────
  const gw = infraNodes.filter((n) => n.type === "gateway");
  if (gw.length) {
    gw[0].position = { x: -110, y: Y_GW };
  }

  // ── 4. Models — clustered under parent K8s node ──────────────────────────
  const modelsByK8s = new Map<string, Node[]>();
  for (const e of edges) {
    if (e.source.startsWith("node-") && e.target.startsWith("model-")) {
      const m = nodeMap.get(e.target);
      if (m) {
          if (!modelsByK8s.has(e.source)) modelsByK8s.set(e.source, []);
          modelsByK8s.get(e.source)!.push(m);
        }
    }
  }

  const modelCenters = new Map<string, number>();
  for (const [k8sId, models] of modelsByK8s) {
    const cx = k8sCenters.get(k8sId) || 0;
    const gap = 220;
    const start = cx - (models.length * gap) / 2;
    for (let i = 0; i < models.length; i++) {
      const x = start + i * gap;
      models[i].position = { x, y: Y_INFRA };
      modelCenters.set(models[i].id, x + 100);
    }
  }

  // ── 5. Cloud providers — spread evenly across the full width ─────────────
  const cps = infraNodes.filter((n) => n.type === "cloudProvider");
  const cpSpacing = totalInfraW / (cps.length + 1);
  const cpCenters = new Map<string, number>();
  for (let i = 0; i < cps.length; i++) {
    const x = k8sStart + (i + 1) * cpSpacing - 90;
    cps[i].position = { x, y: Y_INFRA - 60 };
    cpCenters.set(cps[i].id, x + 90);
  }

  // ── 6. Ensemble subtrees — dagre per-ensemble, then arrange in a row ─────
  // Group app nodes by ensemble
  const ensNodes = appNodes.filter((n) => n.type === "ensemble");

  // Find which ensemble each app node belongs to via edges
  const ensOwner = new Map<string, string>(); // node id → ensemble id
  for (const ens of ensNodes) ensOwner.set(ens.id, ens.id);

  // BFS-walk edges from each ensemble to discover its subtree
  for (const ens of ensNodes) {
    const queue = [ens.id];
    while (queue.length) {
      const cur = queue.shift()!;
      for (const e of edges) {
        if (e.source === cur && !ensOwner.has(e.target) && !infraTypes.has(nodeMap.get(e.target)?.type || "")) {
          ensOwner.set(e.target, ens.id);
          queue.push(e.target);
        }
      }
    }
  }

  // Compute "desired X" for each ensemble based on its model/provider source
  const ensDesired: { id: string; x: number }[] = [];
  for (const ens of ensNodes) {
    let x = 0;
    let count = 0;
    for (const e of edges) {
      if (e.target === ens.id) {
        const mc = modelCenters.get(e.source);
        const cc = cpCenters.get(e.source);
        if (mc != null) { x += mc; count++; }
        else if (cc != null) { x += cc; count++; }
      }
    }
    ensDesired.push({ id: ens.id, x: count > 0 ? x / count : 0 });
  }
  ensDesired.sort((a, b) => a.x - b.x);

  // Run dagre on each ensemble subtree independently
  const subtreeLayouts = new Map<string, { width: number; nodes: { id: string; x: number; y: number }[] }>();

  for (const ens of ensDesired) {
    const treeNodeIds = new Set<string>();
    for (const [nid, owner] of ensOwner) {
      if (owner === ens.id) treeNodeIds.add(nid);
    }
    const treeNodes = appNodes.filter((n) => treeNodeIds.has(n.id));
    const treeEdges = edges.filter((e) => treeNodeIds.has(e.source) && treeNodeIds.has(e.target));

    const g = new Dagre.graphlib.Graph({ compound: true })
      .setDefaultEdgeLabel(() => ({}))
      .setGraph({ rankdir: "TB", nodesep: 40, ranksep: 80, edgesep: 20 });

    for (const n of treeNodes) {
      const [w, h] = DEMO_NODE_SIZES[n.type || ""] || [160, 50];
      g.setNode(n.id, { width: w, height: h });
    }
    for (const e of treeEdges) {
      if (g.hasNode(e.source) && g.hasNode(e.target)) {
        g.setEdge(e.source, e.target);
      }
    }

    Dagre.layout(g);

    const positions: { id: string; x: number; y: number }[] = [];
    let minX = Infinity;
    let maxX = -Infinity;
    for (const n of treeNodes) {
      const pos = g.node(n.id);
      if (pos) {
        const [w, h] = DEMO_NODE_SIZES[n.type || ""] || [160, 50];
        const px = pos.x - w / 2;
        const py = pos.y - h / 2;
        positions.push({ id: n.id, x: px, y: py });
        minX = Math.min(minX, px);
        maxX = Math.max(maxX, px + w);
      }
    }

    // Normalize X so subtree starts at 0
    const width = maxX - minX;
    for (const p of positions) p.x -= minX;

    subtreeLayouts.set(ens.id, { width, nodes: positions });
  }

  // Arrange subtrees in a row, respecting desired X but with minimum gap
  const SUBTREE_GAP = 60;
  let cursorX = k8sStart - 200; // start a bit before the K8s row

  for (const ens of ensDesired) {
    const layout = subtreeLayouts.get(ens.id);
    if (!layout) continue;

    // Try to place near desired X, but don't overlap previous
    const desiredStart = ens.x - layout.width / 2;
    const x = Math.max(cursorX, desiredStart);

    for (const pos of layout.nodes) {
      const n = nodeMap.get(pos.id);
      if (n) {
        n.position = { x: x + pos.x, y: Y_ENS_BASE + pos.y };
      }
    }

    cursorX = x + layout.width + SUBTREE_GAP;
  }
}

// ── Demo Canvas ──────────────────────────────────────────────────────────────

// ── Random lifecycle simulation ──────────────────────────────────────────────

const RANDOM_TASKS = [
  "Summarizing user feedback batch #",
  "Reindexing search corpus shard ",
  "Generating embeddings for doc ",
  "Analyzing latency traces from ",
  "Compiling regression report for Q",
  "Scanning dependencies in module ",
  "Evaluating prompt variant #",
  "Reconciling ledger entries batch ",
  "Processing webhook payload from ",
  "Classifying support tickets batch #",
];

const GPU_WARNINGS = [
  "VRAM pressure 94% — consider eviction",
  "Thermal throttle: 83\u00b0C",
  "OOM risk — 2.1 GB free",
  "ECC error count: 12 (non-fatal)",
  "PCIe bandwidth saturated",
  "Inference queue depth: 47",
  "Memory fragmentation warning",
  "GPU utilization 99% sustained",
];

const TRAFFIC_MESSAGES = [
  "Shifting traffic: failover to backup",
  "Rebalancing inference load",
  "Routing overflow to cloud provider",
  "Model hot-swap in progress",
  "Scaling replicas 2 \u2192 3",
  "Draining requests for upgrade",
  "Traffic ramp: canary 10% \u2192 50%",
  "Migrating inference to H200 pool",
];

function pickRandom<T>(arr: T[]): T {
  return arr[Math.floor(Math.random() * arr.length)];
}

function randomId(): string {
  return Math.random().toString(36).slice(2, 10);
}

// Floating annotation node rendered above K8s / model nodes
function AnnotationNode({ data }: NodeProps<Node<{ message: string; variant: "warning" | "info" }>>) {
  const color = data.variant === "warning"
    ? "border-amber-500/60 bg-amber-500/10 text-amber-300"
    : "border-cyan-500/60 bg-cyan-500/10 text-cyan-300";
  return (
    <div className={`border ${color} px-2.5 py-1 rounded shadow-lg max-w-[220px] animate-in fade-in slide-in-from-bottom-2 duration-500`}>
      <p className="text-[9px] font-mono leading-tight">{data.message}</p>
    </div>
  );
}

// Extend nodeTypes with the demo-only annotation and workstation cards.
const demoNodeTypes = {
  ...nodeTypes,
  annotation: AnnotationNode,
  workstation: WorkstationNode,
};

/** Workstation cards are much taller than the shared k8sNode card they replace,
 * so dagre needs their real footprint or the fleet row overlaps the models
 * beneath it. */
const DEMO_NODE_SIZES: Record<string, [number, number]> = {
  ...NODE_SIZES,
  workstation: [300, 250],
};

function useDemoSimulation(
  setNodes: React.Dispatch<React.SetStateAction<Node[]>>,
  setEdges: React.Dispatch<React.SetStateAction<Edge[]>>,
) {
  const timersRef = useRef<ReturnType<typeof setTimeout>[]>([]);

  useEffect(() => {
    const timers = timersRef.current;

    // ── Fast ticker: agent runs spawn/complete every 2-4s ──────────────
    function scheduleRunEvent() {
      const delay = 2000 + Math.random() * 2000;
      timers.push(setTimeout(() => {
        if (Math.random() < 0.6) {
          // Spawn run
          setNodes((prev) => {
            const personas = prev.filter((n) => n.type === "persona");
            if (!personas.length) return prev;
            const parent = pickRandom(personas);
            const id = `run-sim-${randomId()}`;
            const task = pickRandom(RANDOM_TASKS) + Math.floor(Math.random() * 9000 + 1000);
            const phase = pickRandom(["Running", "Pending", "Serving"]);
            const newRun: Node = {
              id,
              type: "agentRun",
              position: { x: parent.position.x + (Math.random() - 0.5) * 60, y: parent.position.y + 120 },
              data: { runName: id.slice(4), task, phase, isSubAgent: Math.random() < 0.2, label: id.slice(4) },
            };
            setEdges((prevEdges) => [
              ...prevEdges,
              {
                id: `e-${id}`,
                source: parent.id,
                target: id,
                style: { stroke: Math.random() < 0.2 ? "#2dd4bf40" : "#22d3ee40", strokeWidth: 1, ...(Math.random() < 0.2 ? { strokeDasharray: "4 2" } : {}) },
                animated: true,
              },
            ]);
            return [...prev, newRun];
          });
        } else {
          // Remove run
          setNodes((prev) => {
            const runs = prev.filter((n) => n.type === "agentRun");
            if (runs.length < 8) return prev;
            const target = pickRandom(runs);
            setEdges((prevEdges) => prevEdges.filter((e) => e.source !== target.id && e.target !== target.id));
            return prev.filter((n) => n.id !== target.id);
          });
        }
        scheduleRunEvent();
      }, delay));
    }

    // ── Status ticker: phase flips every 3-5s ──────────────────────────
    function scheduleStatusEvent() {
      const delay = 3000 + Math.random() * 2000;
      timers.push(setTimeout(() => {
        const action = Math.random();
        if (action < 0.4) {
          // Flip model phase
          setNodes((prev) => {
            const models = prev.filter((n) => n.type === "model");
            if (!models.length) return prev;
            const target = pickRandom(models);
            const phases = ["Ready", "Loading", "Failed", "Ready", "Ready"];
            const newPhase = pickRandom(phases);
            return prev.map((n) =>
              n.id === target.id ? { ...n, data: { ...n.data, phase: newPhase } } : n,
            );
          });
        } else if (action < 0.75) {
          // Flip persona run phase
          setNodes((prev) => {
            const personas = prev.filter((n) => n.type === "persona");
            if (!personas.length) return prev;
            const target = pickRandom(personas);
            const phases = ["Running", "Succeeded", "Failed", "Serving", undefined];
            return prev.map((n) =>
              n.id === target.id ? { ...n, data: { ...n.data, runPhase: pickRandom(phases) } } : n,
            );
          });
        } else {
          // Flip ensemble running count
          setNodes((prev) => {
            const ensembles = prev.filter((n) => n.type === "ensemble");
            if (!ensembles.length) return prev;
            const target = pickRandom(ensembles);
            const delta = Math.random() > 0.4 ? 1 : -1;
            const newCount = Math.max(0, Math.min(12, (Number(target.data.runningCount) || 0) + delta));
            return prev.map((n) =>
              n.id === target.id ? { ...n, data: { ...n.data, runningCount: newCount } } : n,
            );
          });
        }
        scheduleStatusEvent();
      }, delay));
    }

    // ── GPU warning ticker: pop up warnings every 4-8s ─────────────────
    function scheduleWarning() {
      const delay = 4000 + Math.random() * 4000;
      timers.push(setTimeout(() => {
        setNodes((prev) => {
          const k8sNodes = prev.filter((n) => n.type === "workstation");
          if (!k8sNodes.length) return prev;
          const target = pickRandom(k8sNodes);
          const annoId = `anno-warn-${randomId()}`;
          const annotation: Node = {
            id: annoId,
            type: "annotation",
            position: { x: target.position.x + 20, y: target.position.y - 40 },
            data: { message: pickRandom(GPU_WARNINGS), variant: "warning" },
            selectable: false,
            draggable: false,
          };
          // Auto-remove after 3-5s
          timers.push(setTimeout(() => {
            setNodes((p) => p.filter((n) => n.id !== annoId));
          }, 3000 + Math.random() * 2000));
          return [...prev, annotation];
        });
        scheduleWarning();
      }, delay));
    }

    // ── Traffic shift ticker: inference routing changes every 5-9s ──────
    function scheduleTraffic() {
      const delay = 5000 + Math.random() * 4000;
      timers.push(setTimeout(() => {
        setNodes((prev) => {
          // Show near a model or cloud provider
          const targets = prev.filter((n) => n.type === "model" || n.type === "cloudProvider");
          if (!targets.length) return prev;
          const target = pickRandom(targets);
          const annoId = `anno-traffic-${randomId()}`;
          const annotation: Node = {
            id: annoId,
            type: "annotation",
            position: { x: target.position.x - 10, y: target.position.y - 35 },
            data: { message: pickRandom(TRAFFIC_MESSAGES), variant: "info" },
            selectable: false,
            draggable: false,
          };
          timers.push(setTimeout(() => {
            setNodes((p) => p.filter((n) => n.id !== annoId));
          }, 4000 + Math.random() * 2000));
          return [...prev, annotation];
        });
        // Also toggle animated on a random inference edge
        setEdges((prev) => {
          const inferenceEdges = prev.filter((e) => e.label === "inference");
          if (!inferenceEdges.length) return prev;
          const target = pickRandom(inferenceEdges);
          return prev.map((e) =>
            e.id === target.id ? { ...e, animated: !e.animated } : e,
          );
        });
        scheduleTraffic();
      }, delay));
    }

    // Kick off all simulation loops
    scheduleRunEvent();
    scheduleStatusEvent();
    scheduleWarning();
    scheduleTraffic();

    return () => {
      for (const t of timers) clearTimeout(t);
      timers.length = 0;
    };
  }, [setNodes, setEdges]);
}

/** Ticks the simulated power readings. 1s matches roughly what a real
 * collector's scrape interval delivers, so the numbers move at a believable
 * rate rather than strobing. */
function useSimulatedPowerIndex(specs: SimDevice[]): PowerIndex {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);
  return useMemo(() => simulatePower(specs, now), [specs, now]);
}

function DemoCanvas() {
  const { fitView } = useReactFlow();
  const [rfNodes, setNodes] = useState<Node[]>([]);
  const [rfEdges, setEdges] = useState<Edge[]>([]);
  const [simDevices, setSimDevices] = useState<SimDevice[]>([]);
  const hasFitRef = useRef(false);
  const powerIndex = useSimulatedPowerIndex(simDevices);

  useArrowKeyPan();
  useDemoSimulation(setNodes, setEdges);

  const setNodesRef = useRef(setNodes);
  const setEdgesRef = useRef(setEdges);
  setNodesRef.current = setNodes;
  setEdgesRef.current = setEdges;

  useEffect(() => {
    const { nodes, edges, simDevices } = buildDemoTopology();
    setNodesRef.current(nodes);
    setEdgesRef.current(edges);
    setSimDevices(simDevices);
    if (!hasFitRef.current) {
      setTimeout(() => fitView({ padding: 0.15, duration: 400 }), 150);
      hasFitRef.current = true;
    }
  }, [fitView]);

  const onNodesChange = useCallback(
    (changes: NodeChange[]) => setNodesRef.current((prev) => applyNodeChanges(changes, prev)),
    [],
  );
  const onEdgesChange = useCallback(
    (changes: EdgeChange[]) => setEdgesRef.current((prev) => applyEdgeChanges(changes, prev)),
    [],
  );

  return (
    // Simulated readings are scoped to this subtree: the node cards below read
    // them instead of polling the collector, and no real view can see them.
    <SimulatedPowerContext.Provider value={powerIndex}>
    <div className="h-[calc(100vh-4rem)]">
      <div className="flex items-center justify-between px-4 py-2 border-b border-border">
        <div>
          <h1 className="text-lg font-bold">Topology</h1>
          <p className="text-xs text-muted-foreground">
            Cluster-wide view of nodes, models, ensembles, and gateway
          </p>
        </div>
        <div className="flex items-center gap-3 text-[10px] text-muted-foreground">
          <span className="flex items-center gap-1">
            <span className="h-2 w-2 rounded-full bg-amber-500" /> Gateway
          </span>
          <span className="flex items-center gap-1">
            <span className="h-2 w-2 rounded-full bg-emerald-500" /> K8s Nodes
          </span>
          <span className="flex items-center gap-1">
            <span className="h-2 w-2 rounded-full bg-orange-500" /> Providers
          </span>
          <span className="flex items-center gap-1">
            <span className="h-2 w-2 rounded-full bg-violet-500" /> Models (Pod)
          </span>
          <span className="flex items-center gap-1">
            <span className="h-2 w-2 rounded-full bg-blue-500" /> Ensembles
          </span>
          <span className="flex items-center gap-1">
            <span className="h-2 w-2 rounded-full bg-blue-400" /> Agents
          </span>
        </div>
      </div>
      <div className="h-[calc(100%-3rem)]">
        <ReactFlow
          nodes={rfNodes}
          edges={rfEdges}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          nodeTypes={demoNodeTypes}
          proOptions={{ hideAttribution: true }}
          minZoom={0.1}
          maxZoom={2}
          nodesDraggable
          nodesConnectable={false}
          className="topology-canvas"
        >
          <Background color="#e8562a" gap={48} size={0.5} />
          <Controls showInteractive={false} />
          <KeyboardGuide />
          <MiniMap
            style={{ background: "hsl(var(--card))" }}
            maskColor="rgba(0,0,0,0.6)"
            nodeColor={(node) => {
              switch (node.type) {
                case "cloudProvider": return "#e8562a";
                case "workstation": return "#f0ece4";
                case "model": return "#8a8c82";
                case "ensemble": return "#e8562a";
                case "persona": return "#f0ece4";
                case "gateway": return "#e8562a";
                case "agentRun": return "#22d3ee";
                default: return "#333330";
              }
            }}
          />
        </ReactFlow>
      </div>
    </div>
    </SimulatedPowerContext.Provider>
  );
}

// ── Exported page ────────────────────────────────────────────────────────────

export function TopologyDemoPage() {
  return (
    <ReactFlowProvider>
      <StimulusDialogProvider>
        <DemoCanvas />
      </StimulusDialogProvider>
    </ReactFlowProvider>
  );
}
