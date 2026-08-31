/**
 * TopologyPage — bird's-eye ReactFlow canvas showing the full cluster topology:
 * K8s nodes + providers, deployed models, ensembles + agents, and gateway routes.
 */

import { useMemo, useCallback, useRef, useEffect, useState, useContext } from "react";
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
  Handle,
  Position,
  MarkerType,
  applyNodeChanges,
  applyEdgeChanges,
  useReactFlow,
  ReactFlowProvider,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import {
  useAgents,
  useRuns,
  useEnsembles,
  useModels,
  useGatewayConfig,
  useDensityNodes,
  useDraNodes,
  useRuntimes,
} from "@/hooks/use-api";
import { StimulusDialogProvider, StimulusDialogCtx } from "@/components/canvas-primitives";
import type { StimulusNodeData } from "@/components/canvas-primitives";
import { AcceleratorLeaves } from "@/components/accelerator-leaves";
import { formatWatts, nodeTotal } from "@/lib/power";
import { usePowerIndex } from "@/lib/power-context";
import { useProviderNodes } from "@/hooks/use-provider-nodes";
import {
  Server,
  Cpu,
  Globe,
  Users,
  Activity,
  Radio,
  User,
  Bot,
  Lock,
  Unlock,
  RotateCcw,
  Zap,
  GitBranch,
  GitFork,
  ListOrdered,
  Eye,
  Network,
  Shield,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import type {
  Agent,
  Ensemble,
  Model,
  AgentRun,
  AgentRuntime,
  ProviderNode,
  NodeProvider,
  GatewayConfigResponse,
  DensityNodeSummary,
  DraNodeSummary,
  DraDevice,
} from "@/lib/api";
import { taskText } from "@/lib/utils";
import { Link } from "react-router-dom";
import { useArrowKeyPan, KeyboardGuide } from "@/hooks/use-arrow-key-pan";
import Dagre from "@dagrejs/dagre";

// ── Custom node components ────────────────────────────────────────────────────

function K8sNodeNode({ data }: NodeProps<Node<K8sNodeData>>) {
  const f = data.fitness;
  // Live accelerator draw for this node, when an energy collector is present.
  // Only measured, fresh readings are summed — see lib/power.nodeTotal.
  const power = nodeTotal(usePowerIndex(), data.name);
  return (
    <div className="border border-foreground/20 bg-card px-4 py-3 min-w-[240px] shadow-md cursor-pointer hover:border-primary/50 hover:bg-primary/5 transition-colors">
      <Handle type="target" position={Position.Top} className="!bg-foreground !w-2 !h-2" />
      <Handle type="source" position={Position.Bottom} className="!bg-foreground !w-2 !h-2" />
      <div className="flex items-center gap-2 mb-2">
        <Server className="h-4 w-4 text-foreground" />
        <span className="font-semibold text-sm text-foreground">{data.name}</span>
        {f?.stale && (
          <Badge variant="destructive" className="text-[8px] px-1 py-0">stale</Badge>
        )}
        {power.measured > 0 && (
          <span
            className="ml-auto flex items-center gap-1 font-mono text-[10px] tabular-nums text-amber-600 dark:text-amber-500"
            title={
              power.measured === power.total
                ? `${formatWatts(power.milliwatts)} across ${power.total} accelerator(s)`
                : `${formatWatts(power.milliwatts)} across ${power.measured} of ${power.total} accelerators — the rest are suspended or unmeasurable`
            }
          >
            <Zap className="h-3 w-3" />
            {formatWatts(power.milliwatts)}
            {power.measured < power.total && (
              <span className="text-muted-foreground">
                {power.measured}/{power.total}
              </span>
            )}
          </span>
        )}
      </div>
      <p className="text-[10px] text-muted-foreground font-mono mb-1">{data.ip}</p>
      {f && f.totalRamGb > 0 && (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-0.5 text-[10px] text-muted-foreground mb-1">
          <span>{Math.round(f.totalRamGb)} GB RAM</span>
          <span>{f.cpuCores} cores</span>
          {f.hasGpu && f.gpuName && (
            <span className="text-foreground">{f.gpuName}{f.gpuVramGb ? ` ${Math.round(f.gpuVramGb)}GB` : ""}</span>
          )}
          {!f.hasGpu && (
            <span>{f.backend || "CPU"}</span>
          )}
          <span>{f.modelFitCount} models fit</span>
        </div>
      )}
      {data.providers.length > 0 && (
        <div className="flex flex-wrap gap-1 mt-1">
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
      {(data.accelerators?.length ?? 0) > 0 && (
        <div className="max-w-[280px]">
          <AcceleratorLeaves devices={data.accelerators!} node={data.name} />
        </div>
      )}
    </div>
  );
}

function ModelNode({ data }: NodeProps<Node<ModelNodeData>>) {
  const phaseColor =
    data.phase === "Ready"
      ? "text-green-400 border-green-500/30 bg-green-500/5"
      : data.phase === "Failed"
        ? "text-red-400 border-red-500/30 bg-red-500/5"
        : "text-yellow-400 border-yellow-500/30 bg-yellow-500/5";

  return (
    <div className={`border px-4 py-3 min-w-[180px] shadow-md ${phaseColor}`}>
      <Handle type="target" position={Position.Top} className="!bg-muted-foreground !w-2 !h-2" />
      <Handle type="source" position={Position.Bottom} className="!bg-muted-foreground !w-2 !h-2" />
      <div className="flex items-center gap-2 mb-1">
        <Cpu className="h-4 w-4 text-muted-foreground" />
        <Link to={`/models/${data.name}?namespace=${data.namespace}`} className="font-semibold text-sm hover:underline">
          {data.name}
        </Link>
      </div>
      <div className="flex items-center gap-2 text-[10px] text-muted-foreground">
        <Badge variant="outline" className="text-[9px] border-muted-foreground/30 text-muted-foreground">Pod</Badge>
        <span>{data.serverType || "llama-cpp"}</span>
        {data.gpu > 0 && <span>GPU:{data.gpu}</span>}
        <Badge variant="outline" className="text-[9px]">{data.phase}</Badge>
      </div>
      {data.phase !== "Ready" && (data.message || data.placementMessage) && (
        <div
          className={`mt-1.5 max-w-[220px] truncate text-[10px] ${data.phase === "Failed" ? "text-red-400/90" : "text-yellow-400/80"}`}
          title={[data.message, data.placementMessage].filter(Boolean).join("\n")}
        >
          {data.phase === "Failed" ? "" : "waiting: "}
          {data.message || data.placementMessage}
        </div>
      )}
    </div>
  );
}

function EnsembleNode({ data }: NodeProps<Node<EnsembleNodeData>>) {
  const active = data.enabled;
  return (
    <Link to={`/ensembles/${data.name}`} className="block">
      <div className={`border px-3 py-2 shadow-md transition-colors cursor-pointer ${
        active
          ? "border-primary/30 bg-primary/5 hover:border-primary/50 hover:bg-primary/10"
          : "border-border/20 bg-muted/5 opacity-50 hover:opacity-70"
      }`}>
        <Handle type="target" position={Position.Top} className={active ? "!bg-primary !w-2 !h-2" : "!bg-muted-foreground/40 !w-2 !h-2"} />
        <Handle type="source" position={Position.Bottom} className={active ? "!bg-primary !w-2 !h-2" : "!bg-muted-foreground/40 !w-2 !h-2"} />
        <div className="flex items-center gap-2">
          <Users className={`h-3.5 w-3.5 shrink-0 ${active ? "text-primary" : "text-muted-foreground/40"}`} />
          <span className={`font-medium text-xs truncate max-w-[140px] ${active ? "text-primary" : "text-muted-foreground/60"}`}>
            {data.name}
          </span>
          <span className="text-[9px] text-muted-foreground/40 shrink-0">
            {data.personas.length}
          </span>
          {active && (
            <span className="h-1.5 w-1.5 rounded-full bg-green-500 shrink-0" />
          )}
          {data.runningCount > 0 && (
            <span className="text-[9px] text-cyan-400 shrink-0">{data.runningCount} running</span>
          )}
        </div>
        {active && (data.hasDelegation || data.hasSequential || data.hasSupervision || data.hasSubagents) && (
          <div className="flex items-center gap-1 mt-1">
            {data.hasDelegation && (
              <span title="Delegation">
                <GitFork className="h-2.5 w-2.5 text-primary/60" />
              </span>
            )}
            {data.hasSequential && (
              <span title="Sequential">
                <ListOrdered className="h-2.5 w-2.5 text-amber-400/60" />
              </span>
            )}
            {data.hasSupervision && (
              <span title="Supervision">
                <Eye className="h-2.5 w-2.5 text-gray-400/60" />
              </span>
            )}
            {data.hasSubagents && (
              <span title="Sub-agents">
                <Network className="h-2.5 w-2.5 text-teal-400/60" />
              </span>
            )}
          </div>
        )}
      </div>
    </Link>
  );
}

function TopologyStimulusNode({ data }: NodeProps<Node<TopologyStimulusNodeData>>) {
  const openDialog = useContext(StimulusDialogCtx);

  const handleClick = () => {
    openDialog?.({
      stimulus: { name: data.name, prompt: data.prompt },
      ensembleName: data.ensembleName,
      delivered: data.delivered,
      generation: data.generation,
      label: data.name,
    });
  };

  return (
    <div
      className="border border-amber-500/30 bg-amber-500/5 px-2.5 py-1.5 shadow-sm min-w-[100px] max-w-[160px] cursor-pointer hover:border-amber-500/50 hover:bg-amber-500/10 transition-colors"
      onClick={handleClick}
    >
      <Handle type="target" position={Position.Top} className="!bg-amber-400 !w-1.5 !h-1.5" />
      <Handle type="source" position={Position.Bottom} className="!bg-amber-400 !w-1.5 !h-1.5" />
      <div className="flex items-center gap-1.5">
        <Zap className="h-3 w-3 text-amber-400 shrink-0" />
        <span className="text-[10px] font-medium text-amber-300 truncate">{data.name}</span>
        {data.generation != null && data.generation > 0 && (
          <span className="text-[8px] text-amber-400/60 shrink-0">&times;{data.generation}</span>
        )}
      </div>
    </div>
  );
}

function PersonaNode({ data }: NodeProps<Node<PersonaNodeData>>) {
  const dotClass = data.runPhase === "Running" || data.runPhase === "Serving"
    ? "bg-blue-500 animate-pulse"
    : data.runPhase === "Succeeded"
      ? "bg-green-500"
      : data.runPhase === "Failed"
        ? "bg-red-500"
        : "bg-muted-foreground/40";

  return (
    <div className="border border-border/50 bg-card px-3 py-1.5 shadow-sm min-w-[120px]">
      <Handle type="target" position={Position.Top} className="!bg-primary !w-1.5 !h-1.5" />
      <Handle type="source" position={Position.Bottom} className="!bg-primary !w-1.5 !h-1.5" />
      <div className="flex items-center gap-1.5">
        <User className="h-3 w-3 text-primary shrink-0" />
        <span className="text-[11px] font-medium truncate">{data.displayName || data.name}</span>
        {data.runPhase && (
          <span className={`h-1.5 w-1.5 rounded-full shrink-0 ${dotClass}`} />
        )}
      </div>
    </div>
  );
}

interface StandaloneAgentNodeData {
  name: string;
  model: string;
  runPhase?: string;
  [key: string]: unknown;
}

function StandaloneAgentNode({ data }: NodeProps<Node<StandaloneAgentNodeData>>) {
  const dotClass = data.runPhase === "Running" || data.runPhase === "Serving"
    ? "bg-blue-500 animate-pulse"
    : data.runPhase === "Succeeded"
      ? "bg-green-500"
      : data.runPhase === "Failed"
        ? "bg-red-500"
        : "bg-muted-foreground/40";

  return (
    <div className="border border-primary/30 bg-card px-3 py-2 shadow-sm min-w-[150px]">
      <Handle type="target" position={Position.Top} className="!bg-primary !w-1.5 !h-1.5" />
      <Handle type="source" position={Position.Bottom} className="!bg-primary !w-1.5 !h-1.5" />
      <div className="flex items-center gap-1.5">
        <Bot className="h-3.5 w-3.5 text-primary shrink-0" />
        <span className="text-[11px] font-medium truncate">{data.name}</span>
        {data.runPhase && (
          <span className={`h-1.5 w-1.5 rounded-full shrink-0 ${dotClass}`} />
        )}
      </div>
      {data.model && (
        <p className="text-[9px] text-muted-foreground/70 truncate mt-0.5" title={data.model}>
          {data.model}
        </p>
      )}
    </div>
  );
}

interface AgentRunNodeData {
  runName: string;
  task: string;
  phase: string;
  isSubAgent?: boolean;
  label: string;
  [key: string]: unknown;
}

interface HarnessNodeData {
  name: string;
  ready: boolean;
  owner: string;
  [key: string]: unknown;
}

function HarnessNode({ data }: NodeProps<Node<HarnessNodeData>>) {
  return (
    <div className="border border-amber-500/40 bg-amber-500/5 px-3 py-2 shadow-sm min-w-[150px]">
      <Handle type="target" position={Position.Top} className="!bg-amber-400 !w-1.5 !h-1.5" />
      <Handle type="source" position={Position.Bottom} className="!bg-amber-400 !w-1.5 !h-1.5" />
      <div className="flex items-center gap-1.5">
        <Shield className="h-3.5 w-3.5 text-amber-400 shrink-0" />
        <Link to={`/harnesses/${data.name}`} className="text-[11px] font-medium hover:underline truncate">{data.name}</Link>
      </div>
      <p className="mt-0.5 text-[9px] text-muted-foreground">{data.ready ? "Ready" : "Not ready"}{data.owner ? ` · ${data.owner}` : ""}</p>
    </div>
  );
}

const runPhaseBorder: Record<string, string> = {
  Running: "border-blue-500/50 bg-blue-500/5",
  Pending: "border-yellow-500/50 bg-yellow-500/5",
  Serving: "border-muted-foreground/50 bg-card",
  PostRunning: "border-amber-500/50 bg-amber-500/5",
  AwaitingDelegate: "border-amber-500/50 bg-amber-500/5",
};

const runPhaseDot: Record<string, string> = {
  Running: "bg-blue-500 animate-pulse",
  Pending: "bg-yellow-500 animate-pulse",
  Serving: "bg-violet-500 animate-pulse",
  PostRunning: "bg-amber-500 animate-pulse",
  AwaitingDelegate: "bg-amber-500 animate-pulse",
};

function AgentRunNode({ data }: NodeProps<Node<AgentRunNodeData>>) {
  const border = runPhaseBorder[data.phase] || "border-border/50 bg-card";
  const dot = runPhaseDot[data.phase] || "bg-muted-foreground/40";
  const Icon = data.isSubAgent ? GitBranch : Activity;
  const iconColor = data.isSubAgent ? "text-teal-400" : "text-cyan-400";
  const handleColor = data.isSubAgent ? "!bg-teal-400" : "!bg-cyan-400";

  return (
    <div className={`border ${border} px-2 py-1 shadow-sm min-w-[100px] max-w-[140px]`}>
      <Handle type="target" position={Position.Top} className={`${handleColor} !w-1.5 !h-1.5`} />
      <Handle type="source" position={Position.Bottom} className={`${handleColor} !w-1.5 !h-1.5`} />
      <div className="flex items-center gap-1">
        <Icon className={`h-2.5 w-2.5 ${iconColor} shrink-0`} />
        <span className="text-[9px] font-mono truncate">{data.runName.slice(-8)}</span>
        <span className={`h-1.5 w-1.5 rounded-full shrink-0 ${dot}`} />
      </div>
      <p className="text-[8px] text-muted-foreground/70 truncate mt-0.5" title={data.task}>
        {data.task.length > 30 ? data.task.slice(0, 30) + "…" : data.task}
      </p>
    </div>
  );
}

function CloudProviderNode({ data }: NodeProps<Node<CloudProviderNodeData>>) {
  return (
    <div className="rounded-lg border border-orange-500/30 bg-orange-500/5 px-3 py-2 shadow-md">
      <Handle type="target" position={Position.Top} className="!bg-orange-500 !w-2 !h-2" />
      <Handle type="source" position={Position.Bottom} className="!bg-orange-500 !w-2 !h-2" />
      <div className="flex items-center gap-2">
        <Globe className="h-3.5 w-3.5 text-orange-400" />
        <span className="font-medium text-xs text-orange-300">{data.label}</span>
        <Badge variant="outline" className="text-[9px] border-orange-500/30 text-orange-400">API</Badge>
      </div>
    </div>
  );
}

function GatewayNode({ data }: NodeProps<Node<GatewayNodeData>>) {
  const configured = data.ready || (data.phase && data.phase !== "Not Configured");
  return (
    <div className={`border px-4 py-3 min-w-[200px] shadow-md ${
      configured
        ? "border-amber-500/30 bg-amber-500/5"
        : "border-border/20 bg-muted/5 opacity-50"
    }`}>
      <Handle type="source" position={Position.Bottom} className={configured ? "!bg-amber-500 !w-2 !h-2" : "!bg-muted-foreground/40 !w-2 !h-2"} />
      <div className="flex items-center gap-2 mb-1">
        <Globe className={`h-4 w-4 ${configured ? "text-amber-400" : "text-muted-foreground/40"}`} />
        <span className={`font-semibold text-sm ${configured ? "text-amber-300" : "text-muted-foreground/60"}`}>Gateway</span>
        <Badge
          variant="outline"
          className={`text-[9px] ${data.ready ? "border-green-500/30 text-green-400" : "border-muted text-muted-foreground"}`}
        >
          {data.ready ? "Ready" : data.phase || "Not Configured"}
        </Badge>
      </div>
      {data.address && (
        <p className="text-[10px] text-muted-foreground font-mono">{data.address}</p>
      )}
      {data.routes.length > 0 && (
        <div className="mt-1.5 flex flex-wrap gap-1">
          {data.routes.map((r) => (
            <Badge
              key={r}
              variant="outline"
              className="text-[9px] border-amber-500/20 text-amber-400"
            >
              <Radio className="h-2.5 w-2.5 mr-0.5" />
              {r}
            </Badge>
          ))}
        </div>
      )}
    </div>
  );
}

// ── Node data types ───────────────────────────────────────────────────────────

interface K8sNodeData {
  name: string;
  ip: string;
  providers: { name: string; models: string[] }[];
  accelerators?: DraDevice[];
  fitness?: {
    totalRamGb: number;
    availableRamGb: number;
    cpuCores: number;
    hasGpu: boolean;
    gpuName: string | null;
    gpuVramGb: number | null;
    modelFitCount: number;
    stale: boolean;
    backend: string;
  };
  [key: string]: unknown;
}

interface ModelNodeData {
  name: string;
  namespace: string;
  phase: string;
  serverType: string;
  gpu: number;
  placedNode?: string;
  message?: string;
  placementMessage?: string;
  [key: string]: unknown;
}

interface EnsembleNodeData {
  name: string;
  description: string;
  enabled: boolean;
  personas: string[];
  runningCount: number;
  hasDelegation?: boolean;
  hasSequential?: boolean;
  hasSupervision?: boolean;
  hasSubagents?: boolean;
  [key: string]: unknown;
}

interface TopologyStimulusNodeData {
  name: string;
  prompt: string;
  ensembleName: string;
  delivered?: boolean;
  generation?: number;
  [key: string]: unknown;
}

interface PersonaNodeData {
  name: string;
  displayName: string;
  runPhase?: string;
  [key: string]: unknown;
}

interface CloudProviderNodeData {
  provider: string;
  label: string;
  [key: string]: unknown;
}

interface GatewayNodeData {
  ready: boolean;
  phase: string;
  address: string;
  routes: string[];
  [key: string]: unknown;
}

export const nodeTypes = {
  k8sNode: K8sNodeNode,
  model: ModelNode,
  ensemble: EnsembleNode,
  stimulus: TopologyStimulusNode,
  persona: PersonaNode,
  agent: StandaloneAgentNode,
  agentRun: AgentRunNode,
  harness: HarnessNode,
  cloudProvider: CloudProviderNode,
  gateway: GatewayNode,
};

// ── Layout ────────────────────────────────────────────────────────────────────

/** Estimated node dimensions for dagre layout (width, height). */
export const NODE_SIZES: Record<string, [number, number]> = {
  gateway:       [220, 70],
  k8sNode:       [280, 110],
  cloudProvider: [180, 50],
  model:         [200, 70],
  ensemble:      [200, 50],
  stimulus:      [140, 40],
  persona:       [150, 50],
  agent:         [170, 56],
  agentRun:      [140, 40],
  harness:       [170, 56],
};

/** Run dagre layout on nodes and edges, positioning top-to-bottom. */
export function applyDagreLayout(nodes: Node[], edges: Edge[]): void {
  const g = new Dagre.graphlib.Graph({ compound: true })
    .setDefaultEdgeLabel(() => ({}))
    .setGraph({
      rankdir: "TB",
      nodesep: 60,
      ranksep: 100,
      edgesep: 30,
    });

  for (const node of nodes) {
    const [w, h] = NODE_SIZES[node.type || ""] || [160, 50];
    if (node.parentId) continue; // skip children of compound nodes
    g.setNode(node.id, { width: w, height: h });
  }

  for (const edge of edges) {
    // Only add edges between nodes that exist in the graph (skip child-only edges).
    if (g.hasNode(edge.source) && g.hasNode(edge.target)) {
      g.setEdge(edge.source, edge.target);
    }
  }

  Dagre.layout(g);

  for (const node of nodes) {
    if (node.parentId) continue;
    const pos = g.node(node.id);
    if (pos) {
      const [w, h] = NODE_SIZES[node.type || ""] || [160, 50];
      // dagre returns center positions; ReactFlow uses top-left.
      node.position = { x: pos.x - w / 2, y: pos.y - h / 2 };
    }
  }
}

/** Build a stable fingerprint from entity IDs so we know when layout needs recomputing. */
function entityFingerprint(
  providerNodes: ProviderNode[],
  models: Model[],
  ensembles: Ensemble[],
  agents: Agent[],
  runtimes: AgentRuntime[],
  hasGateway: boolean,
  draNodes?: DraNodeSummary[],
): string {
  const parts = [
    providerNodes.map((n) => n.nodeName).sort().join(","),
    (draNodes || []).map((n) => n.nodeName).sort().join(","),
    models.map((m) => m.metadata.name).sort().join(","),
    ensembles.map((e) => e.metadata.name).sort().join(","),
    agents.map((a) => a.metadata.name).sort().join(","),
    runtimes.map((r) => r.metadata.name).sort().join(","),
    hasGateway ? "gw" : "",
  ];
  return parts.join("|");
}

interface RunPhaseMap {
  [agentName: string]: string; // latest run phase per stamped agent name
}

function buildTopology(
  providerNodes: ProviderNode[],
  models: Model[],
  ensembles: Ensemble[],
  agents: Agent[],
  runtimes: AgentRuntime[],
  gateway: GatewayConfigResponse | undefined,
  runningByEnsemble: Record<string, number>,
  webEndpointAgents: string[],
  runPhases: RunPhaseMap,
  activeRuns: AgentRun[],
  densityNodes?: DensityNodeSummary[],
  draNodes?: DraNodeSummary[],
): { nodes: Node[]; edges: Edge[] } {
  const nodes: Node[] = [];
  const edges: Edge[] = [];
  const P = { x: 0, y: 0 }; // placeholder — dagre computes real positions

  // Agents stamped out by an ensemble are rendered as personas under their
  // ensemble; everything else is a standalone agent with its own node.
  const ensembleAgentRefs = new Set<string>();
  for (const ens of ensembles) {
    for (const cfg of ens.spec.agentConfigs || []) {
      ensembleAgentRefs.add(`${ens.metadata.name}-${cfg.name}`);
    }
  }
  const standaloneAgents = agents.filter(
    (a) => !ensembleAgentRefs.has(a.metadata.name),
  );

  // A harness is execution infrastructure. Render only harnesses actually
  // selected by an Agent, keeping the topology focused on live relationships.
  const referencedRuntimeNames = new Set(
    agents.map((agent) => agent.spec.runtimeRef).filter(Boolean),
  );
  for (const runtime of runtimes.filter((runtime) => referencedRuntimeNames.has(runtime.metadata.name))) {
    const ready = runtime.status?.conditions?.some(
      (condition) => condition.type === "Ready" && condition.status === "True",
    ) || false;
    nodes.push({
      id: `harness-${runtime.metadata.name}`,
      type: "harness",
      position: P,
      data: { name: runtime.metadata.name, ready, owner: runtime.spec.supportOwner || "" },
    });
  }

  // ── Provider detection ─────────────────────────────────────────────────
  const PROVIDER_LABELS: Record<string, string> = {
    openai: "OpenAI", anthropic: "Anthropic", "azure-openai": "Azure OpenAI",
    bedrock: "AWS Bedrock", "lm-studio": "LM Studio", ollama: "Ollama",
    "llama-server": "llama-server", unsloth: "Unsloth", custom: "Custom",
    vllm: "vLLM", tgi: "TGI",
  };

  function inferProvider(baseURL: string): string | null {
    if (!baseURL) return null;
    if (baseURL.includes("/proxy/lm-studio/") || baseURL.includes(":1234")) return "lm-studio";
    if (baseURL.includes("/proxy/ollama/") || baseURL.includes(":11434")) return "ollama";
    if (baseURL.includes("/proxy/vllm/") || baseURL.includes(":8000")) return "vllm";
    if (baseURL.includes("/proxy/llama-cpp/") || baseURL.includes(":8080/v1")) return "llama-server";
    if (baseURL.includes("openai.com")) return "openai";
    if (baseURL.includes("anthropic.com")) return "anthropic";
    return null;
  }

  const ensProviders = new Map<string, string>();
  const ensProviderMap = new Map<string, string>();
  const LOCAL_PROVIDERS = new Set(["lm-studio", "ollama", "llama-server", "vllm", "unsloth"]);

  for (const ens of ensembles) {
    for (const ref of ens.spec.authRefs || []) {
      if (ref.provider) {
        ensProviders.set(ref.provider, PROVIDER_LABELS[ref.provider] || ref.provider);
        ensProviderMap.set(ens.metadata.name, ref.provider);
      }
    }
    if (!ensProviderMap.has(ens.metadata.name) && ens.spec.baseURL) {
      const isModelEndpoint = models.some(
        (m) => m.status?.endpoint && ens.spec.baseURL?.includes(m.status.endpoint.replace("/v1", "")),
      );
      if (!isModelEndpoint) {
        const inferred = inferProvider(ens.spec.baseURL);
        if (inferred) {
          ensProviders.set(inferred, PROVIDER_LABELS[inferred] || inferred);
          ensProviderMap.set(ens.metadata.name, inferred);
        }
      }
    }
  }

  // Standalone agents reference their model the same two ways ensembles do:
  // a baseURL pointing at a Model CR's endpoint, or an external provider.
  const agentModelMap = new Map<string, string>(); // agent name → model name
  const agentProviderMap = new Map<string, string>(); // agent name → provider
  for (const agent of standaloneAgents) {
    const baseURL = agent.spec.agents?.default?.baseURL;
    const matchedModel = baseURL
      ? models.find(
          (m) => m.status?.endpoint && baseURL.includes(m.status.endpoint.replace("/v1", "")),
        )
      : undefined;
    if (matchedModel) {
      agentModelMap.set(agent.metadata.name, matchedModel.metadata.name);
      continue;
    }
    let prov = (agent.spec.authRefs || []).find((r) => r.provider)?.provider;
    if (!prov && baseURL) prov = inferProvider(baseURL) || undefined;
    if (prov) {
      ensProviders.set(prov, PROVIDER_LABELS[prov] || prov);
      agentProviderMap.set(agent.metadata.name, prov);
    }
  }

  // ── Gateway ────────────────────────────────────────────────────────────
  if (gateway) {
    nodes.push({
      id: "gateway",
      type: "gateway",
      position: P,
      data: {
        ready: gateway.ready,
        phase: gateway.phase || "",
        address: gateway.address || "",
        routes: webEndpointAgents,
      },
    });
  }

  // ── K8s Nodes ──────────────────────────────────────────────────────────
  const densityMap = new Map(
    (densityNodes || []).map((fn) => [fn.nodeName, fn]),
  );
  const draMap = new Map(
    (draNodes || []).map((dn) => [dn.nodeName, dn.devices]),
  );
  for (const pn of providerNodes) {
    const fn = densityMap.get(pn.nodeName);
    nodes.push({
      id: `node-${pn.nodeName}`,
      type: "k8sNode",
      position: P,
      data: {
        name: pn.nodeName,
        ip: pn.nodeIP,
        providers: pn.providers.map((p) => ({ name: p.name, models: p.models })),
        accelerators: draMap.get(pn.nodeName),
        fitness: fn
          ? {
              totalRamGb: fn.system.total_ram_gb,
              availableRamGb: fn.system.available_ram_gb,
              cpuCores: fn.system.cpu_cores,
              hasGpu: fn.system.has_gpu,
              gpuName: fn.system.gpu_name,
              gpuVramGb: fn.system.gpu_vram_gb,
              modelFitCount: fn.modelFitCount,
              stale: fn.stale,
              backend: fn.system.backend,
            }
          : undefined,
      },
    });
  }
  // Nodes known only through DRA slices (accelerators but no discovered
  // inference provider) still deserve a card — the inventory is the point.
  const providerNodeNames = new Set(providerNodes.map((pn) => pn.nodeName));
  for (const [nodeName, devices] of draMap) {
    if (providerNodeNames.has(nodeName)) continue;
    nodes.push({
      id: `node-${nodeName}`,
      type: "k8sNode",
      position: P,
      data: {
        name: nodeName,
        ip: "",
        providers: [],
        accelerators: devices,
        fitness: undefined,
      },
    });
  }

  // ── Providers ──────────────────────────────────────────────────────────
  for (const [prov, label] of ensProviders) {
    nodes.push({
      id: `cp-${prov}`,
      type: "cloudProvider",
      position: P,
      data: { provider: prov, label },
    });
    // Edge: K8s node → local provider.
    if (LOCAL_PROVIDERS.has(prov)) {
      for (const pn of providerNodes) {
        if (pn.providers.some((dp) => dp.name === prov || dp.name === prov.replace("-", ""))) {
          edges.push({
            id: `e-node-${pn.nodeName}-cp-${prov}`,
            source: `node-${pn.nodeName}`,
            target: `cp-${prov}`,
            style: { stroke: "#f97316", strokeWidth: 1.5 },
            markerEnd: { type: MarkerType.ArrowClosed, color: "#f97316" },
          });
        }
      }
    }
  }

  // ── Models ─────────────────────────────────────────────────────────────
  for (const m of models) {
    const modelId = `model-${m.metadata.name}`;
    nodes.push({
      id: modelId,
      type: "model",
      position: P,
      data: {
        name: m.metadata.name,
        namespace: m.metadata.namespace,
        phase: m.status?.phase || "Pending",
        serverType: m.spec.inference?.serverType || "llama-cpp",
        gpu: m.spec.resources?.gpu ?? 0,
        placedNode: m.status?.placedNode,
        message: m.status?.message,
        placementMessage: m.status?.placementMessage,
      },
    });
    if (m.status?.placedNode) {
      const nodeId = `node-${m.status.placedNode}`;
      if (providerNodes.some((pn) => pn.nodeName === m.status?.placedNode)) {
        edges.push({
          id: `e-${modelId}-${nodeId}`,
          source: nodeId,
          target: modelId,
          style: { stroke: "#8a8c82", strokeWidth: 1.5 },
          markerEnd: { type: MarkerType.ArrowClosed, color: "#8a8c82" },
          animated: m.status?.phase === "Loading",
          label: "runs on",
          labelStyle: { fontSize: 9, fill: "#8a8c82" },
          labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
          labelBgPadding: [4, 2] as [number, number],
        });
      }
    } else if (providerNodes.length === 1) {
      edges.push({
        id: `e-${modelId}-node-${providerNodes[0].nodeName}`,
        source: `node-${providerNodes[0].nodeName}`,
        target: modelId,
        style: { stroke: "#8a8c8280", strokeWidth: 1 },
        markerEnd: { type: MarkerType.ArrowClosed, color: "#8a8c8280" },
      });
    }
  }

  // ── Standalone agents (not stamped by an ensemble) ────────────────────
  for (const agent of standaloneAgents) {
    const agentId = `agent-${agent.metadata.name}`;
    nodes.push({
      id: agentId,
      type: "agent",
      position: P,
      data: {
        name: agent.metadata.name,
        model: agent.spec.agents?.default?.model || "",
        runPhase: runPhases[agent.metadata.name],
      },
    });
    if (agent.spec.runtimeRef && runtimes.some((runtime) => runtime.metadata.name === agent.spec.runtimeRef)) {
      edges.push({
        id: `e-${agentId}-harness-${agent.spec.runtimeRef}`,
        source: agentId,
        target: `harness-${agent.spec.runtimeRef}`,
        style: { stroke: "#f59e0b", strokeWidth: 1.5, strokeDasharray: "4 3" },
        markerEnd: { type: MarkerType.ArrowClosed, color: "#f59e0b" },
        label: "executes with",
        labelStyle: { fontSize: 9, fill: "#8a8c82" },
        labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
        labelBgPadding: [4, 2] as [number, number],
      });
    }
    const modelName = agentModelMap.get(agent.metadata.name);
    if (modelName) {
      edges.push({
        id: `e-${agentId}-model-${modelName}`,
        source: `model-${modelName}`,
        target: agentId,
        style: { stroke: "#6366f1", strokeWidth: 1.5, strokeDasharray: "4 3" },
        markerEnd: { type: MarkerType.ArrowClosed, color: "#6366f1" },
        animated: true,
        label: "inference",
        labelStyle: { fontSize: 9, fill: "#8a8c82" },
        labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
        labelBgPadding: [4, 2] as [number, number],
      });
    }
    const prov = agentProviderMap.get(agent.metadata.name);
    if (prov) {
      edges.push({
        id: `e-cp-${prov}-${agentId}`,
        source: `cp-${prov}`,
        target: agentId,
        style: { stroke: "#f97316", strokeWidth: 1.5, strokeDasharray: "4 3" },
        markerEnd: { type: MarkerType.ArrowClosed, color: "#f97316" },
        label: "inference",
        labelStyle: { fontSize: 9, fill: "#8a8c82" },
        labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
        labelBgPadding: [4, 2] as [number, number],
      });
    }
  }

  // ── Ensembles (active only) ────────────────────────────────────────────
  const activeEnsembles = ensembles.filter((e) => e.spec.enabled);

  function addEnsembleEdges(ensId: string, ens: Ensemble) {
    const edgeColor = "#6366f1";
    const provEdgeColor = "#f97316";
    if (ens.spec.modelRef) {
      const modelId = `model-${ens.spec.modelRef}`;
      if (models.some((m) => m.metadata.name === ens.spec.modelRef)) {
        edges.push({
          id: `e-${ensId}-${modelId}`,
          source: modelId,
          target: ensId,
          style: { stroke: edgeColor, strokeWidth: 1.5, strokeDasharray: "4 3" },
          markerEnd: { type: MarkerType.ArrowClosed, color: edgeColor },
          animated: true,
          label: "inference",
          labelStyle: { fontSize: 9, fill: "#8a8c82" },
          labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
          labelBgPadding: [4, 2] as [number, number],
        });
      }
    } else if (ens.spec.baseURL) {
      const matchedModel = models.find(
        (m) => m.status?.endpoint && ens.spec.baseURL?.includes(m.status.endpoint.replace("/v1", "")),
      );
      if (matchedModel) {
        edges.push({
          id: `e-${ensId}-model-${matchedModel.metadata.name}`,
          source: `model-${matchedModel.metadata.name}`,
          target: ensId,
          style: { stroke: edgeColor, strokeWidth: 1.5, strokeDasharray: "4 3" },
          markerEnd: { type: MarkerType.ArrowClosed, color: edgeColor },
          label: "inference",
          labelStyle: { fontSize: 9, fill: "#8a8c82" },
          labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
          labelBgPadding: [4, 2] as [number, number],
        });
      }
    }
    const ensProv = ensProviderMap.get(ens.metadata.name);
    if (ensProv) {
      edges.push({
        id: `e-cp-${ensProv}-${ensId}`,
        source: `cp-${ensProv}`,
        target: ensId,
        style: { stroke: provEdgeColor, strokeWidth: 1.5, strokeDasharray: "4 3" },
        markerEnd: { type: MarkerType.ArrowClosed, color: provEdgeColor },
        label: "inference",
        labelStyle: { fontSize: 9, fill: "#8a8c82" },
        labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
        labelBgPadding: [4, 2] as [number, number],
      });
    }
  }

  for (const ens of activeEnsembles) {
    const ensId = `ens-${ens.metadata.name}`;
    const configs = ens.spec.agentConfigs || [];
    const personas = configs.map((p) => p.displayName || p.name);
    const rels = ens.spec.relationships || [];
    const skills = configs.flatMap((p) => p.skills || []);

    nodes.push({
      id: ensId,
      type: "ensemble",
      position: P,
      data: {
        name: ens.metadata.name,
        description: ens.spec.description || "",
        enabled: true,
        personas,
        runningCount: runningByEnsemble[ens.metadata.name] || 0,
        hasDelegation: rels.some((r) => r.type === "delegation"),
        hasSequential: rels.some((r) => r.type === "sequential"),
        hasSupervision: rels.some((r) => r.type === "supervision"),
        hasSubagents: skills.includes("subagents"),
      },
    });
    addEnsembleEdges(ensId, ens);

    // Stimulus trigger node.
    if (ens.spec.stimulus) {
      const stimId = `${ensId}-stim`;
      nodes.push({
        id: stimId,
        type: "stimulus",
        position: P,
        data: {
          name: ens.spec.stimulus.name,
          prompt: ens.spec.stimulus.prompt,
          ensembleName: ens.metadata.name,
          delivered: ens.status?.stimulusDelivered,
          generation: ens.status?.stimulusGeneration,
          label: ens.spec.stimulus.name,
        },
      });
      edges.push({
        id: `e-${ensId}-stim`,
        source: ensId,
        target: stimId,
        style: { stroke: "#f59e0b60", strokeWidth: 1 },
      });
      const stimRel = (ens.spec.relationships || []).find((r) => r.type === "stimulus");
      if (stimRel) {
        edges.push({
          id: `e-stim-${ensId}-${stimRel.target}`,
          source: stimId,
          target: `${ensId}-p-${stimRel.target}`,
          style: { stroke: "#f59e0b", strokeWidth: 1.5 },
          markerEnd: { type: MarkerType.ArrowClosed, color: "#f59e0b" },
          label: "triggers",
          labelStyle: { fontSize: 8, fill: "#8a8c82" },
          labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
          labelBgPadding: [4, 2] as [number, number],
        });
      }
    }

    // Persona nodes.
    for (const cfg of configs) {
      const pid = `${ensId}-p-${cfg.name}`;
      const stampedName = `${ens.metadata.name}-${cfg.name}`;
      nodes.push({
        id: pid,
        type: "persona",
        position: P,
        data: {
          name: cfg.name,
          displayName: cfg.displayName || cfg.name,
          runPhase: runPhases[stampedName],
        },
      });
      edges.push({
        id: `e-${ensId}-${pid}`,
        source: ensId,
        target: pid,
        style: { stroke: "#e8562a40", strokeWidth: 1 },
      });
    }

    // Relationship edges between personas.
    for (const rel of ens.spec.relationships || []) {
      if (rel.type === "stimulus") continue;
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

    // Active run nodes below personas.
    for (const run of activeRuns) {
      const ref = run.spec?.agentRef;
      if (ref && ref.startsWith(ens.metadata.name + "-")) {
        const runId = `run-${run.metadata.name}`;
        const isSubAgent = !!run.spec?.parent;

        // Determine the edge source: sub-agents connect to their parent run
        // (if it exists in the active set), otherwise fall back to the persona.
        let edgeSource: string;
        if (isSubAgent) {
          const parentRunId = `run-${run.spec.parent!.runName}`;
          const parentExists = activeRuns.some(
            (r) => r.metadata.name === run.spec.parent!.runName,
          );
          edgeSource = parentExists
            ? parentRunId
            : `${ensId}-p-${ref.slice(ens.metadata.name.length + 1)}`;
        } else {
          const personaName = ref.slice(ens.metadata.name.length + 1);
          edgeSource = `${ensId}-p-${personaName}`;
        }

        nodes.push({
          id: runId,
          type: "agentRun",
          position: P,
          data: {
            runName: run.metadata.name,
            task: taskText(run.spec.task),
            phase: run.status?.phase || "Pending",
            isSubAgent,
            label: run.metadata.name,
          },
        });
        edges.push({
          id: `e-run-${run.metadata.name}`,
          source: edgeSource,
          target: runId,
          style: {
            stroke: isSubAgent ? "#2dd4bf40" : "#22d3ee40",
            strokeWidth: 1,
            ...(isSubAgent ? { strokeDasharray: "4 2" } : {}),
          },
          animated: true,
        });
      }
    }
  }

  // ── Ad-hoc active runs (not belonging to any ensemble) ─────────────────
  for (const run of activeRuns) {
    if (run.spec?.agentRef && !ensembleAgentRefs.has(run.spec.agentRef)) {
      const runId = `run-${run.metadata.name}`;
      const isSubAgent = !!run.spec?.parent;
      nodes.push({
        id: runId,
        type: "agentRun",
        position: P,
        data: {
          runName: run.metadata.name,
          task: taskText(run.spec.task),
          phase: run.status?.phase || "Pending",
          isSubAgent,
          label: run.metadata.name,
        },
      });
      // Connect ad-hoc sub-agents to their parent run if it exists;
      // top-level runs connect to their standalone agent's node.
      if (isSubAgent) {
        const parentRunId = `run-${run.spec.parent!.runName}`;
        const parentExists = activeRuns.some(
          (r) => r.metadata.name === run.spec.parent!.runName,
        );
        if (parentExists) {
          edges.push({
            id: `e-run-${run.metadata.name}`,
            source: parentRunId,
            target: runId,
            style: { stroke: "#2dd4bf40", strokeWidth: 1, strokeDasharray: "4 2" },
            animated: true,
          });
        }
      } else if (standaloneAgents.some((a) => a.metadata.name === run.spec.agentRef)) {
        edges.push({
          id: `e-run-${run.metadata.name}`,
          source: `agent-${run.spec.agentRef}`,
          target: runId,
          style: { stroke: "#22d3ee40", strokeWidth: 1 },
          animated: true,
        });
      }
    }
  }

  // ── Gateway edges ──────────────────────────────────────────────────────
  if (gateway) {
    for (const agentName of webEndpointAgents) {
      const ownerEns = ensembles.find((ens) =>
        (ens.spec.agentConfigs || []).some(
          (p) => `${ens.metadata.name}-${p.name}` === agentName,
        ),
      );
      const target = ownerEns
        ? `ens-${ownerEns.metadata.name}`
        : standaloneAgents.some((a) => a.metadata.name === agentName)
          ? `agent-${agentName}`
          : null;
      if (target) {
        edges.push({
          id: `e-gw-${agentName}`,
          source: "gateway",
          target,
          style: { stroke: "#f59e0b", strokeWidth: 1.5, strokeDasharray: "6 3" },
          markerEnd: { type: MarkerType.ArrowClosed, color: "#f59e0b" },
          label: "web endpoint",
          labelStyle: { fontSize: 9, fill: "#8a8c82" },
          labelBgStyle: { fill: "#09090b", fillOpacity: 0.8 },
          labelBgPadding: [4, 2] as [number, number],
        });
      }
    }
    if (providerNodes.length > 0) {
      edges.push({
        id: "e-gw-node",
        source: "gateway",
        target: `node-${providerNodes[0].nodeName}`,
        style: { stroke: "#f59e0b40", strokeWidth: 1 },
      });
    }
  }

  // ── Apply dagre layout ─────────────────────────────────────────────────
  applyDagreLayout(nodes, edges);

  return { nodes, edges };
}

// ── Inner component (needs ReactFlowProvider above it) ────────────────────────

const TOPO_POSITIONS_KEY = "sympozium_topology_positions";
const TOPO_LOCKED_KEY = "sympozium_topology_locked";

function savePositions(nodes: Node[]) {
  const map: Record<string, { x: number; y: number }> = {};
  for (const n of nodes) {
    map[n.id] = { x: n.position.x, y: n.position.y };
  }
  localStorage.setItem(TOPO_POSITIONS_KEY, JSON.stringify(map));
}

function loadPositions(): Record<string, { x: number; y: number }> | null {
  try {
    const raw = localStorage.getItem(TOPO_POSITIONS_KEY);
    return raw ? JSON.parse(raw) : null;
  } catch {
    return null;
  }
}

function TopologyCanvas() {
  const { data: ensembles } = useEnsembles();
  const { data: models } = useModels();
  const { data: agents } = useAgents();
  const { data: runtimes } = useRuntimes();
  const { data: runs } = useRuns();
  const { data: providerNodes } = useProviderNodes(true);
  const { data: gateway } = useGatewayConfig();
  const { data: densityData } = useDensityNodes();
  const { data: draData } = useDraNodes();
  const { fitView } = useReactFlow();
  useArrowKeyPan();

  const [rfNodes, setNodesState] = useState<Node[]>([]);
  const [rfEdges, setEdgesState] = useState<Edge[]>([]);

  const setNodesRef = useRef(setNodesState);
  const setEdgesRef = useRef(setEdgesState);
  setNodesRef.current = setNodesState;
  setEdgesRef.current = setEdgesState;

  const onNodesChange = useCallback(
    (changes: NodeChange[]) => setNodesRef.current(
      (prev) => applyNodeChanges(changes, prev)),
    [],
  );
  const onEdgesChange = useCallback(
    (changes: EdgeChange[]) => setEdgesRef.current(
      (prev) => applyEdgeChanges(changes, prev)),
    [],
  );
  const [locked, setLocked] = useState(() => localStorage.getItem(TOPO_LOCKED_KEY) === "true");
  const [selectedK8sNode, setSelectedK8sNode] = useState<ProviderNode | null>(null);

  // Track when we've done the initial fitView so we don't re-fit on every refetch.
  const hasFitRef = useRef(false);
  // Track the entity fingerprint so we only recompute layout when entities change.
  const prevFingerprintRef = useRef("");

  const runningByEnsemble = useMemo(() => {
    const counts: Record<string, number> = {};
    for (const run of runs || []) {
      if (run.status?.phase === "Running" || run.status?.phase === "Serving") {
        const agentRef = run.spec?.agentRef;
        if (agentRef) {
          for (const ens of ensembles || []) {
            if (
              (ens.spec.agentConfigs || []).some(
                (p) => `${ens.metadata.name}-${p.name}` === agentRef,
              )
            ) {
              counts[ens.metadata.name] = (counts[ens.metadata.name] || 0) + 1;
              break;
            }
          }
        }
      }
    }
    return counts;
  }, [runs, ensembles]);

  const webEndpointAgents = useMemo(() => {
    return (agents || [])
      .filter((a) =>
        (a.spec?.skills || []).some(
          (s) =>
            s.skillPackRef === "web-endpoint" ||
            s.skillPackRef === "skillpack-web-endpoint",
        ),
      )
      .map((a) => a.metadata.name);
  }, [agents]);

  // Latest run phase per stamped agent name (for persona status dots).
  const runPhases = useMemo<RunPhaseMap>(() => {
    const map: RunPhaseMap = {};
    for (const run of runs || []) {
      const ref = run.spec?.agentRef;
      if (ref && run.status?.phase) {
        const existing = map[ref];
        // Prefer active phases over terminal ones.
        if (
          !existing ||
          run.status.phase === "Running" ||
          run.status.phase === "Serving"
        ) {
          map[ref] = run.status.phase;
        }
      }
    }
    return map;
  }, [runs]);

  // Active (non-terminal) runs — these appear as nodes on the canvas and
  // disappear once the run completes.
  const activeRuns = useMemo(
    () =>
      (runs || []).filter((r) => {
        const phase = r.status?.phase;
        return (
          phase === "Running" ||
          phase === "Pending" ||
          phase === "Serving" ||
          phase === "PostRunning" ||
          phase === "AwaitingDelegate"
        );
      }),
    [runs],
  );

  // Fingerprint of active run names — drives layout recomputation when runs start/finish.
  const activeRunFingerprint = useMemo(
    () => activeRuns.map((r) => r.metadata.name).sort().join(","),
    [activeRuns],
  );

  // Recompute layout only when the set of entities changes (add/remove),
  // not on every status update or data refetch.
  useEffect(() => {
    const fp = entityFingerprint(
      providerNodes || [],
      models || [],
      ensembles || [],
      agents || [],
      runtimes || [],
      !!gateway,
      draData?.nodes,
    ) + "|runs:" + activeRunFingerprint;

    const entitiesChanged = fp !== prevFingerprintRef.current;
    prevFingerprintRef.current = fp;

    if (entitiesChanged) {
      const { nodes, edges } = buildTopology(
        providerNodes || [],
        models || [],
        ensembles || [],
        agents || [],
        runtimes || [],
        gateway,
        runningByEnsemble,
        webEndpointAgents,
        runPhases,
        activeRuns,
        densityData?.nodes,
        draData?.nodes,
      );

      // Apply saved positions if available.
      const saved = loadPositions();
      if (saved) {
        for (const n of nodes) {
          if (saved[n.id]) {
            n.position = saved[n.id];
          }
        }
      }

      setNodesRef.current(nodes);
      setEdgesRef.current(edges);

      // Fit view on first load only.
      if (!hasFitRef.current) {
        setTimeout(() => fitView({ padding: 0.2, duration: 300 }), 100);
      }
      hasFitRef.current = true;
    } else {
      // Entities are the same — just update node data in-place (status, run counts)
      // without changing positions.
      setNodesRef.current((prev) => {
        const { nodes: freshNodes } = buildTopology(
          providerNodes || [],
          models || [],
          ensembles || [],
          agents || [],
          runtimes || [],
          gateway,
          runningByEnsemble,
          webEndpointAgents,
          runPhases,
          activeRuns,
          densityData?.nodes,
          draData?.nodes,
        );
        const freshMap = new Map(freshNodes.map((n) => [n.id, n]));
        return prev.map((n) => {
          const fresh = freshMap.get(n.id);
          if (fresh) {
            return { ...n, data: fresh.data };
          }
          return n;
        });
      });
      setEdgesRef.current(() => {
        const { edges: freshEdges } = buildTopology(
          providerNodes || [],
          models || [],
          ensembles || [],
          agents || [],
          runtimes || [],
          gateway,
          runningByEnsemble,
          webEndpointAgents,
          runPhases,
          activeRuns,
          densityData?.nodes,
          draData?.nodes,
        );
        return freshEdges;
      });
    }
  }, [providerNodes, models, ensembles, agents, runtimes, gateway, runningByEnsemble, webEndpointAgents, runPhases, activeRuns, activeRunFingerprint, densityData, draData]);

  // Save positions to localStorage after any node drag ends.
  const handleNodesChange = useCallback(
    (changes: Parameters<typeof onNodesChange>[0]) => {
      onNodesChange(changes);
      // Save after position changes (drag end).
      const hasDragStop = changes.some((c) => c.type === "position" && c.dragging === false);
      if (hasDragStop) {
        // Use a microtask so state has settled.
        requestAnimationFrame(() => {
          setNodesRef.current((current) => {
            savePositions(current);
            return current;
          });
        });
      }
    },
    [onNodesChange],
  );

  function handleReset() {
    localStorage.removeItem(TOPO_POSITIONS_KEY);
    prevFingerprintRef.current = ""; // force layout recompute
    hasFitRef.current = false;
  }

  function toggleLock() {
    setLocked((prev) => {
      const next = !prev;
      localStorage.setItem(TOPO_LOCKED_KEY, String(next));
      return next;
    });
  }

  const handleNodeClick = useCallback(
    (_event: React.MouseEvent, node: Node) => {
      if (node.type === "k8sNode") {
        const pn = (providerNodes || []).find(
          (p) => p.nodeName === (node.data as K8sNodeData).name,
        );
        if (pn) setSelectedK8sNode(pn);
      }
    },
    [providerNodes],
  );

  // Models placed on the selected K8s node.
  const modelsOnSelectedNode = useMemo(() => {
    if (!selectedK8sNode) return [];
    return (models || []).filter(
      (m) => m.status?.placedNode === selectedK8sNode.nodeName,
    );
  }, [selectedK8sNode, models]);

  return (
    <div className="h-[calc(100vh-4rem)]">
      <div className="flex items-center justify-between px-4 py-2 border-b border-border">
        <div>
          <h1 className="text-lg font-bold">Topology</h1>
          <p className="text-xs text-muted-foreground">
            Cluster-wide view of nodes, models, ensembles, and gateway
          </p>
        </div>
        <div className="flex items-center gap-3">
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
          <div className="flex items-center gap-1 ml-2">
            <Button
              variant={locked ? "default" : "outline"}
              size="sm"
              className="h-7 text-[10px] gap-1"
              onClick={toggleLock}
            >
              {locked ? <Lock className="h-3 w-3" /> : <Unlock className="h-3 w-3" />}
              {locked ? "Locked" : "Unlocked"}
            </Button>
            <Button
              variant="outline"
              size="sm"
              className="h-7 text-[10px] gap-1"
              onClick={handleReset}
            >
              <RotateCcw className="h-3 w-3" /> Reset
            </Button>
          </div>
        </div>
      </div>
      <div className="h-[calc(100%-3rem)]">
        <ReactFlow
          nodes={rfNodes}
          edges={rfEdges}
          onNodesChange={handleNodesChange}
          onEdgesChange={onEdgesChange}
          onNodeClick={handleNodeClick}
          nodeTypes={nodeTypes}
          proOptions={{ hideAttribution: true }}
          minZoom={0.2}
          maxZoom={2}
          nodesDraggable={!locked}
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
                case "cloudProvider":
                  return "#e8562a";
                case "k8sNode":
                  return "#f0ece4";
                case "model":
                  return "#8a8c82";
                case "ensemble":
                  return "#e8562a";
                case "persona":
                  return "#f0ece4";
                case "gateway":
                  return "#e8562a";
                default:
                  return "#333330";
              }
            }}
          />
        </ReactFlow>
      </div>

      {/* K8s Node detail panel */}
      <Dialog
        open={selectedK8sNode !== null}
        onOpenChange={(open) => { if (!open) setSelectedK8sNode(null); }}
      >
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2 text-foreground">
              <Server className="h-4 w-4 text-foreground" />
              {selectedK8sNode?.nodeName}
            </DialogTitle>
            <DialogDescription className="font-mono text-xs">
              {selectedK8sNode?.nodeIP}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 text-sm">
            {/* Providers */}
            {selectedK8sNode && selectedK8sNode.providers.length > 0 && (
              <div>
                <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-2">
                  Inference Providers
                </h4>
                <div className="space-y-2">
                  {selectedK8sNode.providers.map((p) => (
                    <div
                      key={p.name}
                      className="border border-foreground/20 bg-card p-2"
                    >
                      <div className="flex items-center justify-between mb-1">
                        <span className="font-medium text-foreground">{p.name}</span>
                        <span className="text-[10px] text-muted-foreground font-mono">
                          port {p.port}
                          {p.proxyPort ? ` / proxy ${p.proxyPort}` : ""}
                        </span>
                      </div>
                      {p.models?.length > 0 && (
                        <div className="flex flex-wrap gap-1">
                          {p.models.map((m) => (
                            <Badge
                              key={m}
                              variant="outline"
                              className="text-[9px] border-foreground/20 text-foreground"
                            >
                              {m}
                            </Badge>
                          ))}
                        </div>
                      )}
                      {p.lastProbe && (
                        <p className="text-[10px] text-muted-foreground mt-1">
                          Last probe: {new Date(p.lastProbe).toLocaleString()}
                        </p>
                      )}
                    </div>
                  ))}
                </div>
              </div>
            )}

            {/* Models placed on this node */}
            {modelsOnSelectedNode.length > 0 && (
              <div>
                <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-2">
                  Models on this Node
                </h4>
                <div className="space-y-1.5">
                  {modelsOnSelectedNode.map((m) => (
                    <div
                      key={m.metadata.name}
                      className="flex items-center justify-between rounded-md border border-muted-foreground/20 bg-card px-2 py-1.5"
                    >
                      <Link
                        to={`/models/${m.metadata.name}?namespace=${m.metadata.namespace}`}
                        className="font-medium text-xs text-muted-foreground hover:underline"
                      >
                        {m.metadata.name}
                      </Link>
                      <Badge
                        variant="outline"
                        className={`text-[9px] ${
                          m.status?.phase === "Ready"
                            ? "border-green-500/30 text-green-400"
                            : m.status?.phase === "Failed"
                              ? "border-red-500/30 text-red-400"
                              : "border-yellow-500/30 text-yellow-400"
                        }`}
                      >
                        {m.status?.phase || "Pending"}
                      </Badge>
                    </div>
                  ))}
                </div>
              </div>
            )}

            {/* Labels */}
            {selectedK8sNode?.labels && Object.keys(selectedK8sNode.labels).length > 0 && (
              <div>
                <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-2">
                  Labels
                </h4>
                <div className="flex flex-wrap gap-1 max-h-32 overflow-y-auto">
                  {Object.entries(selectedK8sNode.labels).map(([k, v]) => (
                    <Badge
                      key={k}
                      variant="outline"
                      className="text-[9px] font-mono"
                    >
                      {k}={v}
                    </Badge>
                  ))}
                </div>
              </div>
            )}
          </div>
        </DialogContent>
      </Dialog>

    </div>
  );
}

// ── Exported page (wraps in ReactFlowProvider) ────────────────────────────────

export function TopologyPage() {
  return (
    <ReactFlowProvider>
      <StimulusDialogProvider>
        <TopologyCanvas />
      </StimulusDialogProvider>
    </ReactFlowProvider>
  );
}
