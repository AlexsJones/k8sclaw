/**
 * canvas-primitives.tsx — Shared node components, edge styling, layout helpers,
 * and shell components used by both the ensemble builder and the read-only
 * ensemble canvases.
 *
 * Adding a new node type or edge style here automatically makes it available
 * in every canvas across the app.
 */

import { useState, createContext, useContext } from "react";
import {
	type Node,
	type Edge,
	type NodeProps,
	Handle,
	Position,
	MarkerType,
} from "@xyflow/react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
	Dialog,
	DialogContent,
	DialogHeader,
	DialogTitle,
	DialogDescription,
} from "@/components/ui/dialog";
import { Background, Controls, MiniMap } from "@xyflow/react";
import { Database, Cpu, Zap, Network } from "lucide-react";
import type {
	AgentConfigSpec,
	AgentConfigRelationship,
	StimulusSpec,
	Model,
} from "@/lib/api";
import { useTriggerStimulus, usePatchEnsembleStimulus } from "@/hooks/use-api";
import { PROVIDERS } from "@/components/onboarding-wizard";

// ══════════════════════════════════════════════════════════════════════════════
// Node data interfaces
// ══════════════════════════════════════════════════════════════════════════════

export interface AgentConfigNodeData {
	persona: AgentConfigSpec;
	packName?: string;
	agentName?: string;
	runPhase?: string;
	runTask?: string;
	hasSharedMemory?: boolean;
	membraneVisibility?: string;
	/** Number of active sub-agent runs spawned by this persona's runs. */
	activeSubagents?: number;
	/** Builder-only: true when the persona has name + systemPrompt filled in. */
	isConfigured?: boolean;
	label: string;
	[key: string]: unknown;
}

export interface ModelNodeData {
	model: Model;
	label: string;
	[key: string]: unknown;
}

export interface ProviderNodeData {
	provider: string;
	label: string;
	baseURL?: string;
	isModelRef?: boolean;
	model?: Model;
	[key: string]: unknown;
}

export interface StimulusNodeData {
	stimulus: StimulusSpec;
	ensembleName: string;
	delivered?: boolean;
	generation?: number;
	label: string;
	[key: string]: unknown;
}

// ══════════════════════════════════════════════════════════════════════════════
// Phase styling (run status indicators)
// ══════════════════════════════════════════════════════════════════════════════

const phaseBorder: Record<string, string> = {
	Running: "ring-2 ring-blue-500/70 shadow-[0_0_12px_rgba(59,130,246,0.3)]",
	Succeeded: "ring-1 ring-green-500/40",
	Failed: "ring-2 ring-red-500/60 shadow-[0_0_12px_rgba(239,68,68,0.3)]",
	Pending: "ring-1 ring-yellow-500/40",
	Serving: "ring-2 ring-violet-500/60 shadow-[0_0_12px_rgba(139,92,246,0.3)]",
	AwaitingDelegate:
		"ring-2 ring-amber-500/60 shadow-[0_0_12px_rgba(245,158,11,0.3)]",
};

const phaseDot: Record<string, string> = {
	Running: "bg-blue-500 animate-pulse",
	Succeeded: "bg-green-500",
	Failed: "bg-red-500",
	Pending: "bg-yellow-500 animate-pulse",
	Serving: "bg-violet-500 animate-pulse",
	AwaitingDelegate: "bg-amber-500 animate-pulse",
	AwaitingGate: "bg-amber-500 animate-pulse",
};

const phaseLabel: Record<string, string> = {
	Running: "Running",
	Succeeded: "Done",
	Failed: "Failed",
	Pending: "Pending",
	Serving: "Serving",
	AwaitingDelegate: "Awaiting",
	AwaitingGate: "Gate",
};

// ══════════════════════════════════════════════════════════════════════════════
// Node components
// ══════════════════════════════════════════════════════════════════════════════

/** Unified agent/persona node used by both the builder and read-only canvases.
 *  Shows run-phase indicators when `runPhase` is set; shows "click to configure"
 *  hint when `isConfigured` is explicitly false (builder mode). */
export function AgentConfigNode({
	data,
}: NodeProps<Node<AgentConfigNodeData>>) {
	const {
		persona,
		packName,
		agentName,
		runPhase,
		runTask,
		hasSharedMemory,
		isConfigured,
	} = data;

	const borderClass = runPhase
		? phaseBorder[runPhase] || ""
		: isConfigured === false
			? ""
			: "";
	const dotClass = runPhase ? phaseDot[runPhase] || "bg-muted-foreground" : "";

	const borderStyle =
		isConfigured === false
			? "border-dashed border-muted-foreground/40"
			: "border-border/60";

	return (
		<div
			className={`border ${borderStyle} bg-card shadow-md px-4 py-3 min-w-[180px] max-w-[220px] cursor-pointer transition-all ${borderClass}`}
		>
			<Handle
				type="target"
				position={Position.Top}
				className="!bg-muted-foreground !w-2 !h-2"
			/>

			{/* Pack name label (for global canvas) */}
			{packName && (
				<p className="text-[9px] text-muted-foreground/50 font-mono mb-1 -mt-0.5 truncate">
					{packName}
				</p>
			)}

			<div className="flex items-center justify-between gap-2 mb-1">
				<span className="font-semibold text-sm truncate">
					{persona.displayName || persona.name || "Unnamed"}
				</span>
				{runPhase && (
					<div className="flex items-center gap-1 shrink-0" title={runPhase}>
						<span className={`h-2 w-2 rounded-full ${dotClass}`} />
						<span className="text-[9px] text-muted-foreground">
							{phaseLabel[runPhase] || runPhase}
						</span>
					</div>
				)}
			</div>

			<p className="text-[10px] text-muted-foreground font-mono mb-1.5 truncate">
				{persona.name || "click to configure"}
			</p>

			{persona.model && (
				<Badge
					variant="outline"
					className="text-[10px] font-mono mb-1.5 block w-fit"
				>
					{persona.model}
				</Badge>
			)}

			{hasSharedMemory && (
				<Badge
					variant="outline"
					className="text-[9px] px-1 py-0 mb-1 gap-0.5 w-fit"
					title={
						data.membraneVisibility
							? `Membrane: ${data.membraneVisibility} visibility`
							: "Shared workflow memory"
					}
				>
					<Database className="h-2.5 w-2.5" />
					{data.membraneVisibility
						? `${data.membraneVisibility}`
						: "shared memory"}
				</Badge>
			)}

			{persona.skills?.includes("subagents") && (
				<Badge
					variant="outline"
					className="text-[9px] px-1 py-0 mb-1 gap-0.5 w-fit border-teal-500/30 text-teal-400"
					title="Can spawn sub-agents dynamically"
				>
					<Network className="h-2.5 w-2.5" />
					sub-agents
				</Badge>
			)}

			<div className="flex flex-wrap gap-0.5">
				{persona.skills?.slice(0, 3).map((sk) => (
					<Badge key={sk} variant="secondary" className="text-[9px] px-1 py-0">
						{sk}
					</Badge>
				))}
				{(persona.skills?.length ?? 0) > 3 && (
					<Badge variant="secondary" className="text-[9px] px-1 py-0">
						+{(persona.skills?.length ?? 0) - 3}
					</Badge>
				)}
			</div>

			{/* Running task preview */}
			{runTask && runPhase === "Running" && (
				<p
					className="text-[9px] text-blue-400/80 mt-1.5 truncate italic"
					title={runTask}
				>
					{runTask}
				</p>
			)}

			{data.activeSubagents != null && data.activeSubagents > 0 && (
				<p
					className="text-[9px] text-teal-400/80 mt-1 truncate"
					title={`${data.activeSubagents} sub-agent${data.activeSubagents !== 1 ? "s" : ""} running`}
				>
					{data.activeSubagents} sub-agent
					{data.activeSubagents !== 1 ? "s" : ""}
				</p>
			)}

			{agentName && !runTask && (
				<p className="text-[9px] text-muted-foreground/60 mt-1.5 truncate">
					{agentName}
				</p>
			)}

			{isConfigured === false && (
				<p className="text-[9px] text-amber-500/80 mt-1.5 italic">
					Click to configure
				</p>
			)}

			<Handle
				type="source"
				position={Position.Bottom}
				className="!bg-muted-foreground !w-2 !h-2"
			/>
		</div>
	);
}

// ── Model node (local inference) ────────────────────────────────────────────

const modelPhaseBorder: Record<string, string> = {
	Ready: "ring-2 ring-emerald-500/60 shadow-[0_0_12px_rgba(16,185,129,0.25)]",
	Loading: "ring-2 ring-blue-500/50 shadow-[0_0_10px_rgba(59,130,246,0.2)]",
	Downloading: "ring-2 ring-amber-500/50",
	Placing: "ring-2 ring-blue-500/50",
	Failed: "ring-2 ring-red-500/60",
};

export function ModelNode({ data }: NodeProps<Node<ModelNodeData>>) {
	const { model } = data;
	const phase = model.status?.phase || "Pending";
	const border = modelPhaseBorder[phase] || "";

	return (
		<div
			className={`border border-primary/40 bg-card shadow-md px-4 py-3 min-w-[180px] max-w-[220px] transition-shadow duration-300 ${border}`}
		>
			<div className="flex items-center gap-1.5 mb-1">
				<Cpu className="h-3.5 w-3.5 text-primary" />
				<span className="font-semibold text-sm text-primary">
					Local Model
				</span>
			</div>

			<p className="text-[10px] text-muted-foreground font-mono mb-1.5 truncate">
				{model.metadata.name}
			</p>

			<div className="flex flex-wrap gap-1 mb-1">
				<Badge
					variant="outline"
					className={`text-[9px] px-1 py-0 ${
						phase === "Ready"
							? "border-emerald-500/50 text-emerald-400"
							: phase === "Failed"
								? "border-red-500/50 text-red-400"
								: "border-blue-500/50 text-blue-400"
					}`}
				>
					{phase}
				</Badge>
				{(model.spec.resources?.gpu ?? 0) > 0 && (
					<Badge variant="outline" className="text-[9px] px-1 py-0">
						GPU: {model.spec.resources?.gpu}
					</Badge>
				)}
			</div>

			{model.status?.endpoint && (
				<p
					className="text-[9px] text-muted-foreground/60 truncate"
					title={model.status.endpoint}
				>
					{model.status.endpoint}
				</p>
			)}

			{model.status?.placedNode && (
				<p className="text-[9px] text-primary/60 truncate mt-0.5">
					node: {model.status.placedNode}
				</p>
			)}

			<Handle
				type="source"
				position={Position.Bottom}
				className="!bg-primary !w-2 !h-2"
			/>
		</div>
	);
}

// ── Provider node (remote LLM inference) ───────────────────────────────────

export const providerColors: Record<
	string,
	{ border: string; text: string; edge: string }
> = {
	openai: {
		border: "border-emerald-500/40",
		text: "text-emerald-400",
		edge: "#10b981",
	},
	anthropic: {
		border: "border-orange-500/40",
		text: "text-orange-400",
		edge: "#f97316",
	},
	"azure-openai": {
		border: "border-blue-500/40",
		text: "text-blue-400",
		edge: "#3b82f6",
	},
	ollama: {
		border: "border-cyan-500/40",
		text: "text-cyan-400",
		edge: "#06b6d4",
	},
	"lm-studio": {
		border: "border-teal-500/40",
		text: "text-teal-400",
		edge: "#14b8a6",
	},
	"llama-server": {
		border: "border-amber-500/40",
		text: "text-amber-400",
		edge: "#f59e0b",
	},
	bedrock: {
		border: "border-yellow-500/40",
		text: "text-yellow-400",
		edge: "#eab308",
	},
	custom: {
		border: "border-gray-500/40",
		text: "text-gray-400",
		edge: "#6b7280",
	},
};

export const defaultProviderColor = {
	border: "border-blue-500/40",
	text: "text-blue-400",
	edge: "#3b82f6",
};

export function ProviderNode({ data }: NodeProps<Node<ProviderNodeData>>) {
	// For local models, render inline with model node styling.
	if (data.isModelRef && data.model) {
		const model = data.model;
		const phase = model.status?.phase || "Pending";
		const border = modelPhaseBorder[phase] || "";
		return (
			<div
				className={`border border-primary/40 bg-card shadow-md px-4 py-3 min-w-[180px] max-w-[220px] transition-shadow duration-300 ${border}`}
			>
				<div className="flex items-center gap-1.5 mb-1">
					<Cpu className="h-3.5 w-3.5 text-primary" />
					<span className="font-semibold text-sm text-primary">
						Local Model
					</span>
				</div>
				<p className="text-[10px] text-muted-foreground font-mono mb-1.5 truncate">
					{model.metadata.name}
				</p>
				<Badge
					variant="outline"
					className={`text-[9px] px-1 py-0 ${phase === "Ready" ? "border-emerald-500/50 text-emerald-400" : phase === "Failed" ? "border-red-500/50 text-red-400" : "border-blue-500/50 text-blue-400"}`}
				>
					{phase}
				</Badge>
				{model.status?.endpoint && (
					<p
						className="text-[9px] text-muted-foreground/60 truncate mt-1"
						title={model.status.endpoint}
					>
						{model.status.endpoint}
					</p>
				)}
				<Handle
					type="source"
					position={Position.Bottom}
					className="!bg-primary !w-2 !h-2"
				/>
			</div>
		);
	}

	const providerDef = PROVIDERS.find((p) => p.value === data.provider);
	const colors = providerColors[data.provider] || defaultProviderColor;
	const Icon = providerDef?.icon || Cpu;

	return (
		<div
			className={`border ${colors.border} bg-card shadow-md px-4 py-3 min-w-[180px] max-w-[220px] transition-shadow duration-300`}
		>
			<div className="flex items-center gap-1.5 mb-1">
				<Icon className={`h-3.5 w-3.5 ${colors.text}`} />
				<span className={`font-semibold text-sm ${colors.text}`}>
					{providerDef?.label || data.provider}
				</span>
			</div>

			{data.baseURL && (
				<p
					className="text-[9px] text-muted-foreground/60 font-mono truncate"
					title={data.baseURL}
				>
					{data.baseURL}
				</p>
			)}

			<Handle
				type="source"
				position={Position.Bottom}
				className="!bg-muted-foreground !w-2 !h-2"
			/>
		</div>
	);
}

// ── Stimulus node + shared dialog ─────────────────────────────────────────

/** Context lets any StimulusNode bubble a click up to the nearest StimulusDialogProvider. */
export const StimulusDialogCtx = createContext<
	((d: StimulusNodeData) => void) | null
>(null);

export function StimulusNode({ data }: NodeProps<Node<StimulusNodeData>>) {
	const { stimulus, delivered, generation } = data;
	const openDialog = useContext(StimulusDialogCtx);

	return (
		<div
			className={`border border-amber-500/40 bg-card shadow-md px-3 py-2.5 min-w-[160px] max-w-[200px] transition-shadow duration-300 cursor-pointer hover:border-amber-500/60 hover:bg-amber-500/5 ${
				delivered ? "ring-1 ring-amber-500/40" : ""
			}`}
			data-testid="stimulus-node"
			onClick={() => openDialog?.(data)}
		>
			<div className="flex items-center gap-1.5 mb-1">
				<Zap className="h-3.5 w-3.5 text-amber-400" />
				<span className="font-semibold text-sm text-amber-300">Stimulus</span>
			</div>

			<p className="text-[10px] text-muted-foreground font-mono mb-1 truncate">
				{stimulus.name || "click to configure"}
			</p>

			<p
				className="text-[9px] text-muted-foreground/80 truncate italic"
				title={stimulus.prompt}
			>
				{stimulus.prompt
					? stimulus.prompt.length > 60
						? stimulus.prompt.slice(0, 60) + "…"
						: stimulus.prompt
					: "No prompt set"}
			</p>

			{generation != null && generation > 0 && (
				<Badge
					variant="outline"
					className="text-[9px] px-1 py-0 mt-1 gap-0.5 w-fit"
				>
					fired ×{generation}
				</Badge>
			)}

			<Handle
				type="source"
				position={Position.Bottom}
				className="!bg-amber-400 !w-2 !h-2"
			/>
		</div>
	);
}

/**
 * StimulusDialogProvider — wraps any canvas that contains StimulusNodes.
 * Renders the shared view/edit/retrigger dialog and provides the click
 * handler via context so nodes can open it.
 */
export function StimulusDialogProvider({
	children,
}: {
	children: React.ReactNode;
}) {
	const [selected, setSelected] = useState<StimulusNodeData | null>(null);
	const [editName, setEditName] = useState("");
	const [editPrompt, setEditPrompt] = useState("");
	const triggerMutation = useTriggerStimulus();
	const patchMutation = usePatchEnsembleStimulus();

	const open = (d: StimulusNodeData) => {
		setSelected(d);
		setEditName(d.stimulus.name);
		setEditPrompt(d.stimulus.prompt);
	};

	return (
		<StimulusDialogCtx.Provider value={open}>
			{children}
			<Dialog
				open={selected !== null}
				onOpenChange={(o) => {
					if (!o) setSelected(null);
				}}
			>
				<DialogContent className="sm:max-w-md">
					<DialogHeader>
						<DialogTitle className="flex items-center gap-2 text-amber-300">
							<Zap className="h-4 w-4 text-amber-400" />
							Stimulus
						</DialogTitle>
						<DialogDescription className="text-xs">
							{selected?.ensembleName}
							{selected?.generation != null && selected.generation > 0 && (
								<span className="ml-2 text-amber-400/60">
									fired {selected.generation}&times;
								</span>
							)}
						</DialogDescription>
					</DialogHeader>
					{selected && (
						<div className="space-y-4">
							<div className="space-y-2">
								<Label htmlFor="stim-name" className="text-xs">
									Name
								</Label>
								<Input
									id="stim-name"
									value={editName}
									onChange={(e) => setEditName(e.target.value)}
									className="text-sm"
								/>
							</div>
							<div className="space-y-2">
								<Label htmlFor="stim-prompt" className="text-xs">
									Prompt
								</Label>
								<Textarea
									id="stim-prompt"
									value={editPrompt}
									onChange={(e) => setEditPrompt(e.target.value)}
									rows={5}
									className="text-sm font-mono"
								/>
							</div>
							<div className="flex items-center gap-2 pt-2">
								<Button
									variant="outline"
									size="sm"
									className="gap-1"
									disabled={triggerMutation.isPending}
									onClick={() => triggerMutation.mutate(selected.ensembleName)}
								>
									<Zap className="h-3.5 w-3.5" />
									{triggerMutation.isPending ? "Triggering..." : "Re-trigger"}
								</Button>
								<Button
									size="sm"
									className="gap-1 ml-auto"
									disabled={
										patchMutation.isPending ||
										(editName === selected.stimulus.name &&
											editPrompt === selected.stimulus.prompt)
									}
									onClick={() => {
										patchMutation.mutate(
											{
												name: selected.ensembleName,
												stimulus: { name: editName.trim(), prompt: editPrompt },
											},
											{ onSuccess: () => setSelected(null) },
										);
									}}
								>
									{patchMutation.isPending ? "Saving..." : "Save"}
								</Button>
							</div>
						</div>
					)}
				</DialogContent>
			</Dialog>
		</StimulusDialogCtx.Provider>
	);
}

// ══════════════════════════════════════════════════════════════════════════════
// Node type registry — single source of truth
// ══════════════════════════════════════════════════════════════════════════════

export const nodeTypes = {
	persona: AgentConfigNode,
	builder: AgentConfigNode, // alias so the builder can use either type string
	model: ModelNode,
	provider: ProviderNode,
	stimulus: StimulusNode,
};

// ══════════════════════════════════════════════════════════════════════════════
// Edge styling — single source of truth
// ══════════════════════════════════════════════════════════════════════════════

export const EDGE_TYPES = [
	"delegation",
	"sequential",
	"supervision",
	"stimulus",
] as const;

export const edgeStyles: Record<
	string,
	{ stroke: string; strokeDasharray?: string }
> = {
	delegation: { stroke: "#3b82f6" },
	sequential: { stroke: "#f59e0b", strokeDasharray: "6 3" },
	supervision: { stroke: "#6b7280", strokeDasharray: "2 4" },
	stimulus: { stroke: "#f59e0b" },
};

export const edgeLabels: Record<string, string> = {
	delegation: "delegates to",
	sequential: "then",
	supervision: "supervises",
	stimulus: "triggers",
};

export function styledEdge(
	id: string,
	source: string,
	target: string,
	relType: string,
): Edge {
	const style = edgeStyles[relType] || edgeStyles.delegation;
	return {
		id,
		source,
		target,
		label: edgeLabels[relType] || relType,
		style,
		data: { relType },
		markerEnd:
			relType !== "supervision"
				? { type: MarkerType.ArrowClosed, color: style.stroke }
				: undefined,
		labelStyle: { fontSize: 10, fill: "#9ca3af" },
		animated: relType === "delegation",
	};
}

// ══════════════════════════════════════════════════════════════════════════════
// Layout helpers
// ══════════════════════════════════════════════════════════════════════════════

export function layoutNodes(
	personas: AgentConfigSpec[],
	relationships: AgentConfigRelationship[],
	offsetX = 0,
	offsetY = 0,
	prefix = "",
): Node<AgentConfigNodeData>[] {
	const outDegree = new Map<string, number>();
	const inDegree = new Map<string, number>();
	for (const r of relationships) {
		if (r.type === "stimulus") continue; // stimulus source is not a persona
		outDegree.set(r.source, (outDegree.get(r.source) || 0) + 1);
		inDegree.set(r.target, (inDegree.get(r.target) || 0) + 1);
	}

	const sorted = [...personas].sort((a, b) => {
		const aScore = (outDegree.get(a.name) || 0) - (inDegree.get(a.name) || 0);
		const bScore = (outDegree.get(b.name) || 0) - (inDegree.get(b.name) || 0);
		if (bScore !== aScore) return bScore - aScore;
		return a.name.localeCompare(b.name);
	});

	const cols = Math.max(2, Math.ceil(Math.sqrt(sorted.length)));
	const xGap = 260;
	const yGap = 200;

	return sorted.map((persona, i) => ({
		id: prefix ? `${prefix}/${persona.name}` : persona.name,
		type: "persona",
		position: {
			x: offsetX + (i % cols) * xGap,
			y: offsetY + Math.floor(i / cols) * yGap,
		},
		data: { persona, label: persona.displayName || persona.name },
	}));
}

export function buildEdges(
	relationships: AgentConfigRelationship[],
	prefix = "",
): Edge[] {
	return relationships.map((rel, i) => {
		const stimId = `__stimulus__${rel.source}`;
		const sourceId =
			rel.type === "stimulus"
				? prefix
					? `${prefix}/${stimId}`
					: stimId
				: prefix
					? `${prefix}/${rel.source}`
					: rel.source;
		const targetId = prefix ? `${prefix}/${rel.target}` : rel.target;
		return styledEdge(
			`e-${prefix}-${i}-${rel.source}-${rel.target}`,
			sourceId,
			targetId,
			rel.type,
		);
	});
}

export function edgesToRelationships(edges: Edge[]): AgentConfigRelationship[] {
	return edges.map((e) => ({
		source: e.source.includes("/") ? e.source.split("/")[1] : e.source,
		target: e.target.includes("/") ? e.target.split("/")[1] : e.target,
		type: (e.data?.relType as AgentConfigRelationship["type"]) || "delegation",
	}));
}

// ══════════════════════════════════════════════════════════════════════════════
// Provider node derivation
// ══════════════════════════════════════════════════════════════════════════════

import type { Ensemble } from "@/lib/api";

interface DerivedProvider {
	id: string;
	provider: string;
	label: string;
	baseURL?: string;
	isModelRef?: boolean;
	model?: Model;
}

/** Derive unique provider/model nodes from ensemble data. */
export function deriveProviders(
	pack: Ensemble,
	modelMap: Map<string, Model>,
): DerivedProvider[] {
	const seen = new Set<string>();
	const result: DerivedProvider[] = [];

	// 1. Ensemble-level modelRef → local model provider
	if (pack.spec.modelRef) {
		const model = modelMap.get(pack.spec.modelRef);
		const key = `model:${pack.spec.modelRef}`;
		if (!seen.has(key)) {
			seen.add(key);
			result.push({
				id: key,
				provider: "local-model",
				label: pack.spec.modelRef,
				isModelRef: true,
				model,
			});
		}
	}

	// 2. Ensemble-level authRefs → cloud providers
	for (const ref of pack.spec.authRefs || []) {
		if (ref.provider && !seen.has(ref.provider)) {
			seen.add(ref.provider);
			const prov = PROVIDERS.find((p) => p.value === ref.provider);
			result.push({
				id: ref.provider,
				provider: ref.provider,
				label: prov?.label || ref.provider,
				baseURL: pack.spec.baseURL,
			});
		}
	}

	// 3. Per-persona provider overrides
	for (const persona of pack.spec.agentConfigs || []) {
		if (persona.provider && !seen.has(persona.provider)) {
			seen.add(persona.provider);
			const prov = PROVIDERS.find((p) => p.value === persona.provider);
			result.push({
				id: persona.provider,
				provider: persona.provider,
				label: prov?.label || persona.provider,
				baseURL: persona.baseURL,
			});
		}
	}

	// 4. Infer from per-agent model fields
	for (const persona of pack.spec.agentConfigs || []) {
		if (persona.model && modelMap.has(persona.model)) {
			const key = `model:${persona.model}`;
			if (!seen.has(key)) {
				seen.add(key);
				result.push({
					id: key,
					provider: "local-model",
					label: persona.model,
					isModelRef: true,
					model: modelMap.get(persona.model),
				});
			}
		}
	}

	return result;
}

/** Determine which provider a persona connects to. */
export function personaProviderId(
	persona: AgentConfigSpec,
	pack: Ensemble,
	modelMap: Map<string, Model>,
): string | null {
	if (persona.provider) return persona.provider;
	if (persona.model && modelMap.has(persona.model))
		return `model:${persona.model}`;
	if (pack.spec.modelRef) return `model:${pack.spec.modelRef}`;
	const defaultRef = (pack.spec.authRefs || [])[0];
	if (defaultRef?.provider) return defaultRef.provider;
	return null;
}

/** Build provider nodes + edges for a pack. */
export function buildProviderNodesAndEdges(
	pack: Ensemble,
	modelMap: Map<string, Model>,
	personas: AgentConfigSpec[],
	offsetX: number,
	prefix: string,
): { nodes: Node<ProviderNodeData | ModelNodeData>[]; edges: Edge[] } {
	const providers = deriveProviders(pack, modelMap);
	if (providers.length === 0) return { nodes: [], edges: [] };

	const cols = Math.max(2, Math.ceil(Math.sqrt(personas.length)));
	const totalWidth = (cols - 1) * 260;
	const providerGap = 240;
	const providerStartX =
		offsetX + totalWidth / 2 - ((providers.length - 1) * providerGap) / 2 - 90;

	const nodes: Node<ProviderNodeData | ModelNodeData>[] = providers.map(
		(prov, i) => ({
			id: prefix ? `${prefix}/__prov__${prov.id}` : `__prov__${prov.id}`,
			type: prov.isModelRef && prov.model ? "model" : "provider",
			position: { x: providerStartX + i * providerGap, y: 0 },
			data:
				prov.isModelRef && prov.model
					? ({ model: prov.model, label: prov.label } as ModelNodeData)
					: ({
							provider: prov.provider,
							label: prov.label,
							baseURL: prov.baseURL,
							isModelRef: prov.isModelRef,
							model: prov.model,
						} as ProviderNodeData),
		}),
	);

	const edges: Edge[] = [];
	for (const persona of personas) {
		const provId = personaProviderId(persona, pack, modelMap);
		if (!provId) continue;
		const provNode = nodes.find((n) => n.id.endsWith(`__prov__${provId}`));
		if (!provNode) continue;

		const targetId = prefix ? `${prefix}/${persona.name}` : persona.name;
		const colors =
			providerColors[provId] ||
			(provId.startsWith("model:")
				? { edge: "#8b5cf6" }
				: defaultProviderColor);

		edges.push({
			id: `prov-${prefix}-${provId}-${persona.name}`,
			source: provNode.id,
			target: targetId,
			type: "default",
			animated: provId.startsWith("model:")
				? modelMap.get(provId.replace("model:", ""))?.status?.phase === "Ready"
				: true,
			style: { stroke: colors.edge, strokeWidth: 1.5, strokeDasharray: "4 3" },
			markerEnd: {
				type: MarkerType.ArrowClosed,
				color: colors.edge,
				width: 14,
				height: 14,
			},
		});
	}

	return { nodes, edges };
}

// ══════════════════════════════════════════════════════════════════════════════
// Run status helpers
// ══════════════════════════════════════════════════════════════════════════════

import type { AgentRun } from "@/lib/api";

/** Build a run-phase map from runs: persona name → { phase, task } */
export function buildRunPhaseMap(
	runs: AgentRun[] | undefined,
	installedPersonas: Array<{ name: string; agentName: string }> | undefined,
): Map<string, { phase: string; task?: string }> {
	const map = new Map<string, { phase: string; task?: string }>();
	if (!runs || !installedPersonas) return map;
	for (const ip of installedPersonas) {
		const instanceRuns = runs
			.filter((r) => r.spec.agentRef === ip.agentName)
			.sort(
				(a, b) =>
					new Date(b.metadata.creationTimestamp || 0).getTime() -
					new Date(a.metadata.creationTimestamp || 0).getTime(),
			);
		if (instanceRuns.length > 0 && instanceRuns[0].status?.phase) {
			map.set(ip.name, {
				phase: instanceRuns[0].status.phase,
				task: instanceRuns[0].spec.task,
			});
		}
	}
	return map;
}

// ══════════════════════════════════════════════════════════════════════════════
// Shell components
// ══════════════════════════════════════════════════════════════════════════════

export const rfDefaults = {
	fitView: false,
	fitViewOptions: { padding: 0.3 },
	minZoom: 0.2,
	maxZoom: 1.5,
	proOptions: { hideAttribution: true },
	colorMode: "dark" as const,
};

export function CanvasShell({ children }: { children: React.ReactNode }) {
	return (
		<>
			<Background gap={48} size={0.5} color="#e8562a" />
			<Controls
				showInteractive={false}
				className="!bg-card !border-border/40 !shadow-md [&>button]:!bg-card [&>button]:!border-border/40 [&>button]:!text-muted-foreground [&>button:hover]:!bg-white/5"
			/>
			<MiniMap
				nodeColor="#e8562a"
				maskColor="rgba(0,0,0,0.7)"
				className="!bg-card !border-border/40"
			/>
			{children}
		</>
	);
}

export function EdgeTypePicker({
	onSelect,
	onCancel,
}: {
	onSelect: (type: string) => void;
	onCancel: () => void;
}) {
	return (
		<div className="absolute top-2 left-1/2 -translate-x-1/2 z-50 flex gap-1 border border-border/60 bg-card shadow-lg p-2">
			<span className="text-xs text-muted-foreground self-center mr-1">
				Type:
			</span>
			{EDGE_TYPES.map((t) => (
				<Button
					key={t}
					variant="ghost"
					size="sm"
					className="text-xs capitalize h-7 px-2"
					onClick={() => onSelect(t)}
					type="button"
				>
					<span
						className="w-2 h-2 rounded-full mr-1.5"
						style={{ backgroundColor: edgeStyles[t].stroke }}
					/>
					{t}
				</Button>
			))}
			<Button
				variant="ghost"
				size="sm"
				className="text-xs h-7 px-2 text-muted-foreground"
				onClick={onCancel}
				type="button"
			>
				Cancel
			</Button>
		</div>
	);
}

export function StatusLegend() {
	const items = [
		{ phase: "Running", dot: "bg-blue-500 animate-pulse" },
		{ phase: "Serving", dot: "bg-violet-500 animate-pulse" },
		{
			phase: "AwaitingDelegate",
			dot: "bg-amber-500 animate-pulse",
			label: "Awaiting",
		},
		{ phase: "Succeeded", dot: "bg-green-500" },
		{ phase: "Failed", dot: "bg-red-500" },
	];
	return (
		<div className="flex items-center gap-3 text-[10px] text-muted-foreground">
			{items.map((it) => (
				<span key={it.phase} className="flex items-center gap-1">
					<span className={`h-1.5 w-1.5 rounded-full ${it.dot}`} />
					{it.label || it.phase}
				</span>
			))}
		</div>
	);
}
