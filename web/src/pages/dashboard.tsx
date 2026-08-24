import { useCallback, useMemo, useState } from "react";
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/card";
import { StatusBadge } from "@/components/status-badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableHeader,
  TableRow,
  TableHead,
  TableBody,
  TableCell,
} from "@/components/ui/table";
import {
  useAgents,
  useRuns,
  useClusterInfo,
  useObservabilityMetrics,
  useGateVerdict,
  useEnsembles,
  useModels,
  useCanaryConfig,
} from "@/hooks/use-api";
import { useWebSocket } from "@/hooks/use-websocket";
import { cn, formatAge, taskText, truncate } from "@/lib/utils";
import { Link } from "react-router-dom";
import {
  Server,
  Play,
  Wrench,
  Clock,
  Activity,
  Gauge,
  Zap,
  RotateCcw,
  GripVertical,
  Lock,
  Unlock,
  AlertTriangle,
  CheckCircle2,
  XCircle,
  Radio,
  ShieldAlert,
  Check,
  X,
  Users,
  Cpu,
} from "lucide-react";
import { DashboardEnsembleCanvas } from "@/components/ensemble-canvas";
import { Button } from "@/components/ui/button";
import { ResponsiveGridLayout, useContainerWidth } from "react-grid-layout";
import type { Layout, ResponsiveLayouts } from "react-grid-layout";
import "react-grid-layout/css/styles.css";

// ---------------------------------------------------------------------------
// Layout persistence
// ---------------------------------------------------------------------------

const LAYOUT_KEY = "sympozium-dashboard-layout-v3";
const LAYOUT_LOCKED_KEY = "sympozium-dashboard-locked";

type PanelId =
  | "activity"
  | "tokensByModel"
  | "topTools"
  | "recentRuns"
  | "eventStream"
  | "runStatus"
  | "recentErrors"
  | "teamCanvas"
  | "localModels";

const DEFAULT_LAYOUTS: ResponsiveLayouts = {
  lg: [
    // Top row: activity chart + recent runs
    { i: "activity", x: 0, y: 0, w: 6, h: 7, minW: 4, minH: 3 },
    { i: "recentRuns", x: 6, y: 0, w: 6, h: 7, minW: 4, minH: 4 },
    // Second row: three equal panels
    { i: "tokensByModel", x: 0, y: 7, w: 4, h: 5, minW: 3, minH: 3 },
    { i: "topTools", x: 4, y: 7, w: 4, h: 5, minW: 3, minH: 3 },
    { i: "localModels", x: 8, y: 7, w: 4, h: 5, minW: 3, minH: 3 },
    // Team canvas + run status
    { i: "teamCanvas", x: 0, y: 12, w: 9, h: 8, minW: 6, minH: 5 },
    { i: "runStatus", x: 9, y: 12, w: 3, h: 8, minW: 3, minH: 4 },
    // Bottom row
    { i: "eventStream", x: 0, y: 19, w: 9, h: 6, minW: 4, minH: 4 },
    { i: "recentErrors", x: 9, y: 19, w: 3, h: 6, minW: 3, minH: 4 },
  ],
  md: [
    { i: "activity", x: 0, y: 0, w: 5, h: 7, minW: 4, minH: 3 },
    { i: "recentRuns", x: 5, y: 0, w: 5, h: 7, minW: 4, minH: 4 },
    { i: "tokensByModel", x: 0, y: 7, w: 4, h: 5, minW: 3, minH: 3 },
    { i: "topTools", x: 4, y: 7, w: 3, h: 5, minW: 3, minH: 3 },
    { i: "localModels", x: 7, y: 7, w: 3, h: 5, minW: 3, minH: 3 },
    { i: "teamCanvas", x: 0, y: 12, w: 7, h: 8, minW: 5, minH: 5 },
    { i: "runStatus", x: 7, y: 12, w: 3, h: 8, minW: 3, minH: 4 },
    { i: "eventStream", x: 0, y: 19, w: 7, h: 6, minW: 4, minH: 4 },
    { i: "recentErrors", x: 7, y: 19, w: 3, h: 6, minW: 3, minH: 4 },
  ],
  sm: [
    { i: "activity", x: 0, y: 0, w: 6, h: 7, minW: 4, minH: 3 },
    { i: "recentRuns", x: 0, y: 7, w: 6, h: 6, minW: 4, minH: 4 },
    { i: "tokensByModel", x: 0, y: 13, w: 6, h: 5, minW: 3, minH: 3 },
    { i: "topTools", x: 0, y: 18, w: 6, h: 5, minW: 3, minH: 3 },
    { i: "localModels", x: 0, y: 23, w: 6, h: 5, minW: 3, minH: 3 },
    { i: "teamCanvas", x: 0, y: 28, w: 6, h: 8, minW: 4, minH: 5 },
    { i: "runStatus", x: 0, y: 36, w: 6, h: 5, minW: 3, minH: 4 },
    { i: "eventStream", x: 0, y: 41, w: 6, h: 6, minW: 4, minH: 4 },
    { i: "recentErrors", x: 0, y: 47, w: 6, h: 5, minW: 4, minH: 4 },
  ],
};

function loadLayouts(): ResponsiveLayouts {
  try {
    const raw = localStorage.getItem(LAYOUT_KEY);
    if (raw) return JSON.parse(raw);
  } catch {
    // fall through
  }
  return DEFAULT_LAYOUTS;
}

function saveLayouts(layouts: ResponsiveLayouts) {
  localStorage.setItem(LAYOUT_KEY, JSON.stringify(layouts));
}

function loadLocked(): boolean {
  try {
    const raw = localStorage.getItem(LAYOUT_LOCKED_KEY);
    if (raw !== null) return JSON.parse(raw);
  } catch {
    // fall through
  }
  return true;
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type ActivityBucket = {
  ts: number;
  label: string;
  runs: number;
  failed: number;
  durationTotalSec: number;
  durationSamples: number;
  durationValuesSec: number[];
  avgDurationSec: number;
  p95DurationSec: number;
  agentsInstalled: number;
  serving: number;
};

type DurationMode = "avg" | "p95";

function percentile(values: number[], p: number): number {
  if (values.length === 0) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  const idx = Math.max(
    0,
    Math.min(sorted.length - 1, Math.ceil((p / 100) * sorted.length) - 1),
  );
  return sorted[idx];
}

type RangeKey = "1h" | "24h" | "7d";

function buildActivityBuckets(
  runs: NonNullable<ReturnType<typeof useRuns>["data"]>,
  instances: NonNullable<ReturnType<typeof useAgents>["data"]>,
  range: RangeKey,
): ActivityBucket[] {
  const now = new Date();
  const buckets: ActivityBucket[] = [];

  const configs: Record<
    RangeKey,
    {
      count: number;
      stepMs: number;
      labelFn: (d: Date) => string;
      startFn: () => Date;
    }
  > = {
    "1h": {
      count: 60,
      stepMs: 60 * 1000,
      labelFn: (d) =>
        d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }),
      startFn: () => {
        const s = new Date(now.getTime() - 59 * 60 * 1000);
        s.setSeconds(0, 0);
        return s;
      },
    },
    "24h": {
      count: 24,
      stepMs: 60 * 60 * 1000,
      labelFn: (d) => d.toLocaleTimeString([], { hour: "numeric" }),
      startFn: () => {
        const s = new Date(now.getTime() - 23 * 60 * 60 * 1000);
        s.setMinutes(0, 0, 0);
        return s;
      },
    },
    "7d": {
      count: 7,
      stepMs: 24 * 60 * 60 * 1000,
      labelFn: (d) =>
        d.toLocaleDateString([], { month: "short", day: "numeric" }),
      startFn: () => {
        const s = new Date(now);
        s.setHours(0, 0, 0, 0);
        s.setDate(s.getDate() - 6);
        return s;
      },
    },
  };

  const cfg = configs[range];
  const start = cfg.startFn();

  for (let i = 0; i < cfg.count; i++) {
    const d = new Date(start.getTime() + i * cfg.stepMs);
    buckets.push({
      ts: d.getTime(),
      label: cfg.labelFn(d),
      runs: 0,
      failed: 0,
      durationTotalSec: 0,
      durationSamples: 0,
      durationValuesSec: [],
      avgDurationSec: 0,
      p95DurationSec: 0,
      agentsInstalled: 0,
      serving: 0,
    });
  }

  for (const run of runs || []) {
    const created = new Date(run.metadata.creationTimestamp || "").getTime();
    if (!Number.isFinite(created) || created < buckets[0].ts) continue;
    const idx = Math.floor((created - buckets[0].ts) / cfg.stepMs);
    if (idx < 0 || idx >= buckets.length) continue;
    buckets[idx].runs++;
    const phase = (run.status?.phase || "").toLowerCase();
    if (phase === "failed" || phase === "error") buckets[idx].failed++;
    const durationSec = (run.status?.tokenUsage?.durationMs || 0) / 1000;
    if (durationSec > 0) {
      buckets[idx].durationTotalSec += durationSec;
      buckets[idx].durationSamples++;
      buckets[idx].durationValuesSec.push(durationSec);
    }
  }

  for (const b of buckets) {
    b.avgDurationSec =
      b.durationSamples > 0 ? b.durationTotalSec / b.durationSamples : 0;
    b.p95DurationSec = percentile(b.durationValuesSec, 95);
  }

  const createdAt = (instances || [])
    .map((i) => new Date(i.metadata.creationTimestamp || "").getTime())
    .filter((n) => Number.isFinite(n))
    .sort((a, b) => a - b);
  let ptr = 0;
  for (let i = 0; i < buckets.length; i++) {
    const bucketEnd = buckets[i].ts + cfg.stepMs - 1;
    while (ptr < createdAt.length && createdAt[ptr] <= bucketEnd) ptr++;
    buckets[i].agentsInstalled = ptr;
  }

  countServingPerBucket(runs, buckets, cfg.stepMs);
  return buckets;
}

function countServingPerBucket(
  runs: NonNullable<ReturnType<typeof useRuns>["data"]>,
  buckets: ActivityBucket[],
  bucketMs: number,
) {
  const servingRuns = (runs || []).filter(
    (r) => (r.status?.phase || "").toLowerCase() === "serving",
  );
  for (const run of servingRuns) {
    const created = new Date(run.metadata.creationTimestamp || "").getTime();
    if (!Number.isFinite(created)) continue;
    for (let i = 0; i < buckets.length; i++) {
      const bucketEnd = buckets[i].ts + bucketMs - 1;
      if (created <= bucketEnd) {
        buckets[i].serving++;
      }
    }
  }
}

function linePath(points: Array<{ x: number; y: number }>) {
  if (!points.length) return "";
  return points.map((p, i) => `${i === 0 ? "M" : "L"}${p.x},${p.y}`).join(" ");
}

function compactTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return n.toString();
}

// ---------------------------------------------------------------------------
// Panel wrapper — drag handle + consistent chrome
// ---------------------------------------------------------------------------

function PanelWrapper({
  children,
  title,
  icon: Icon,
  locked,
  actions,
}: {
  children: React.ReactNode;
  title: string;
  icon?: React.ComponentType<{ className?: string }>;
  locked: boolean;
  actions?: React.ReactNode;
}) {
  return (
    <Card className="h-full flex flex-col overflow-hidden">
      <CardHeader className="flex flex-row items-center justify-between gap-2 py-3 px-4 space-y-0">
        <div className="flex items-center gap-2 min-w-0">
          {!locked && (
            <GripVertical className="drag-handle h-4 w-4 shrink-0 cursor-grab text-muted-foreground/50 hover:text-muted-foreground active:cursor-grabbing" />
          )}
          {Icon && <Icon className="h-4 w-4 shrink-0 text-muted-foreground" />}
          <CardTitle className="text-sm truncate">{title}</CardTitle>
        </div>
        {actions && (
          <div className="flex items-center gap-1 shrink-0">{actions}</div>
        )}
      </CardHeader>
      <CardContent className="flex-1 overflow-auto pt-0 px-4 pb-4">
        {children}
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
// Local Models panel
// ---------------------------------------------------------------------------

function LocalModelsPanel() {
  const { data: models, isLoading } = useModels();

  if (isLoading) {
    return (
      <div className="space-y-2">
        {Array.from({ length: 2 }).map((_, i) => (
          <Skeleton key={i} className="h-10 w-full" />
        ))}
      </div>
    );
  }

  const items = models || [];

  if (items.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-4 text-center">
        <Cpu className="h-8 w-8 text-muted-foreground/30 mb-2" />
        <p className="text-xs text-muted-foreground">No local models deployed</p>
        <Link
          to="/models"
          className="text-xs text-blue-400 hover:text-blue-300 mt-1"
        >
          Deploy a model
        </Link>
      </div>
    );
  }

  return (
    <div className="space-y-2">
      {items.map((m) => (
        <Link
          key={m.metadata.name}
          to={`/models/${m.metadata.name}`}
          className="block rounded-lg border border-border/50 px-3 py-2 hover:border-border transition-colors"
        >
          <div className="flex items-center justify-between gap-2 mb-0.5">
            <span className="text-xs font-mono text-foreground truncate">
              {m.metadata.name}
            </span>
            <StatusBadge phase={m.status?.phase} />
          </div>
          <div className="flex items-center gap-3 text-[11px] text-muted-foreground">
            <span>{m.spec.resources?.gpu ? `${m.spec.resources.gpu} GPU` : "CPU"}</span>
            <span>{m.spec.resources?.memory || "16Gi"} RAM</span>
            {m.status?.endpoint && (
              <span className="truncate max-w-[120px]">
                {m.status.endpoint.replace(/^https?:\/\//, "").replace(/\/v1$/, "")}
              </span>
            )}
          </div>
        </Link>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Main dashboard
// ---------------------------------------------------------------------------

export function DashboardPage() {
  const instances = useAgents();
  const runs = useRuns();
  const clusterInfo = useClusterInfo();
  const { data: canaryConfig } = useCanaryConfig();
  const observability = useObservabilityMetrics();
  const { events, connected } = useWebSocket();
  const [range, setRange] = useState<RangeKey>("24h");
  const [durationMode, setDurationMode] = useState<DurationMode>("avg");
  const [hoverIdx, setHoverIdx] = useState<number | null>(null);
  const [layouts, setLayouts] = useState<ResponsiveLayouts>(loadLayouts);
  const [locked, setLocked] = useState(loadLocked);
  const { width: containerWidth, containerRef } = useContainerWidth({
    initialWidth: 1200,
  });

  // --- Computed data ---
  const recentRuns = useMemo(
    () =>
      (runs.data || [])
        .sort(
          (a, b) =>
            new Date(b.metadata.creationTimestamp || "").getTime() -
            new Date(a.metadata.creationTimestamp || "").getTime(),
        )
        .slice(0, 8),
    [runs.data],
  );

  const activeRuns = useMemo(
    () =>
      (runs.data || []).filter(
        (r) =>
          r.status?.phase === "Running" ||
          r.status?.phase === "Pending" ||
          r.status?.phase === "Serving",
      ),
    [runs.data],
  );

  const failedRuns = useMemo(
    () =>
      (runs.data || [])
        .filter((r) => {
          const p = (r.status?.phase || "").toLowerCase();
          return p === "failed" || p === "error";
        })
        .sort(
          (a, b) =>
            new Date(b.metadata.creationTimestamp || "").getTime() -
            new Date(a.metadata.creationTimestamp || "").getTime(),
        )
        .slice(0, 6),
    [runs.data],
  );

  const gateVerdict = useGateVerdict();

  const awaitingApproval = useMemo(
    () =>
      (runs.data || []).filter(
        (r) =>
          r.status?.phase === "PostRunning" &&
          !r.status?.gateVerdict &&
          r.spec.lifecycle?.postRun?.some((h) => h.gate),
      ),
    [runs.data],
  );

  const activity = useMemo(
    () => buildActivityBuckets(runs.data || [], instances.data || [], range),
    [runs.data, instances.data, range],
  );
  const totalInRange = useMemo(
    () => activity.reduce((acc, b) => acc + b.runs, 0),
    [activity],
  );
  const failedInRange = useMemo(
    () => activity.reduce((acc, b) => acc + b.failed, 0),
    [activity],
  );
  const failureRate =
    totalInRange > 0 ? (failedInRange / totalInRange) * 100 : 0;
  const avgDurationSecInRange = useMemo(() => {
    const dur = activity.reduce((acc, b) => acc + b.durationTotalSec, 0);
    const samples = activity.reduce((acc, b) => acc + b.durationSamples, 0);
    return samples > 0 ? dur / samples : 0;
  }, [activity]);
  const p95DurationSecInRange = useMemo(() => {
    const all = activity.flatMap((b) => b.durationValuesSec);
    return percentile(all, 95);
  }, [activity]);

  const totalPods = clusterInfo.data?.pods ?? 0;
  const totalAgentPods = (instances.data || []).reduce(
    (sum, inst) => sum + (inst.status?.activeAgentPods ?? 0),
    0,
  );

  // --- Run status breakdown ---
  const runStatusCounts = useMemo(() => {
    const counts: Record<string, number> = {};
    for (const run of runs.data || []) {
      const p = (run.status?.phase || "unknown").toLowerCase();
      counts[p] = (counts[p] || 0) + 1;
    }
    return counts;
  }, [runs.data]);

  // --- Observability data ---
  const obs = observability.data;
  const inputByModel = obs?.inputByModel || [];
  const outputByModel = obs?.outputByModel || [];
  const toolsByName = obs?.toolsByName || [];

  // Merge input/output by model for display
  const modelBreakdown = useMemo(() => {
    const map = new Map<string, { input: number; output: number }>();
    for (const row of inputByModel) {
      const entry = map.get(row.label) || { input: 0, output: 0 };
      entry.input = row.value;
      map.set(row.label, entry);
    }
    for (const row of outputByModel) {
      const entry = map.get(row.label) || { input: 0, output: 0 };
      entry.output = row.value;
      map.set(row.label, entry);
    }
    return [...map.entries()]
      .map(([label, v]) => ({
        label,
        input: v.input,
        output: v.output,
        total: v.input + v.output,
      }))
      .sort((a, b) => b.total - a.total)
      .slice(0, 6);
  }, [inputByModel, outputByModel]);
  const maxModelTotal = Math.max(1, ...modelBreakdown.map((m) => m.total));

  // Top tools sorted
  const topTools = useMemo(
    () => [...toolsByName].sort((a, b) => b.value - a.value).slice(0, 8),
    [toolsByName],
  );
  const maxToolValue = Math.max(1, ...topTools.map((t) => t.value));

  // --- Layout handlers ---
  const handleLayoutChange = useCallback(
    (_layout: Layout, allLayouts: ResponsiveLayouts) => {
      setLayouts(allLayouts);
      saveLayouts(allLayouts);
    },
    [],
  );

  const handleResetLayout = useCallback(() => {
    setLayouts(DEFAULT_LAYOUTS);
    saveLayouts(DEFAULT_LAYOUTS);
  }, []);

  const handleToggleLock = useCallback(() => {
    setLocked((prev) => {
      const next = !prev;
      localStorage.setItem(LAYOUT_LOCKED_KEY, JSON.stringify(next));
      return next;
    });
  }, []);

  // --- Stat tiles ---
  const stats = [
    {
      label: "Total Tokens",
      value: compactTokens(
        (obs?.inputTokensTotal || 0) + (obs?.outputTokensTotal || 0),
      ),
      icon: Zap,
      color: "text-amber-400",
    },
    {
      label: "Tool Calls",
      value: (obs?.toolInvocations || 0).toLocaleString(),
      icon: Wrench,
      color: "text-blue-400",
    },
    {
      label: "Active Runs",
      value: activeRuns.length,
      icon: Gauge,
      color: "text-violet-400",
    },
    {
      label: "Failure Rate",
      value: totalInRange > 0 ? `${failureRate.toFixed(1)}%` : "—",
      icon: AlertTriangle,
      color: failureRate > 10 ? "text-red-400" : "text-muted-foreground",
    },
    {
      label: "Avg Duration",
      value:
        avgDurationSecInRange > 0
          ? `${avgDurationSecInRange.toFixed(1)}s`
          : "—",
      icon: Clock,
      color: "text-cyan-400",
    },
    {
      label: "Agents",
      value: instances.data?.length ?? "—",
      icon: Server,
      color: "text-purple-400",
    },
  ];

  // --- Activity chart geometry ---
  const chartW = 760;
  const chartH = 250;
  const padX = 32;
  const padY = 20;
  const innerW = chartW - padX * 2;
  const innerH = chartH - padY * 2;
  const durationFor = (b: ActivityBucket) =>
    durationMode === "p95" ? b.p95DurationSec : b.avgDurationSec;
  const maxDurationY = Math.max(1, ...activity.map((b) => durationFor(b)));
  const maxAgentsY = Math.max(1, ...activity.map((b) => b.agentsInstalled));
  const barW = Math.max(
    3,
    Math.min(14, (activity.length > 0 ? innerW / activity.length : 8) * 0.7),
  );
  const xFor = (idx: number) =>
    padX +
    (activity.length <= 1
      ? innerW / 2
      : (idx / (activity.length - 1)) * innerW);
  const yForDuration = (value: number) =>
    padY + innerH - (value / maxDurationY) * innerH;
  const yForAgents = (value: number) =>
    padY + innerH - (value / maxAgentsY) * innerH;
  const maxServingY = Math.max(1, ...activity.map((b) => b.serving));
  const yForServing = (value: number) =>
    padY + innerH - (value / maxServingY) * innerH;
  const durationPoints = activity.map((b, i) => ({
    x: xFor(i),
    y: yForDuration(durationFor(b)),
  }));
  const servingPoints = activity.map((b, i) => ({
    x: xFor(i),
    y: yForServing(b.serving),
  }));
  const totalServing =
    activity.length > 0 ? activity[activity.length - 1].serving : 0;
  const activePoint = hoverIdx !== null ? activity[hoverIdx] : null;
  const activeX = hoverIdx !== null ? xFor(hoverIdx) : null;
  const activeYDuration =
    hoverIdx !== null ? yForDuration(durationFor(activity[hoverIdx])) : null;

  // --- Run status donut geometry ---
  const statusEntries = useMemo(() => {
    const palette: Record<string, string> = {
      succeeded: "#34d399",
      failed: "#f87171",
      error: "#f87171",
      running: "#60a5fa",
      pending: "#facc15",
      serving: "#fbbf24",
      unknown: "#6b7280",
    };
    const entries = Object.entries(runStatusCounts).map(([phase, count]) => ({
      phase,
      count,
      color: palette[phase] || palette.unknown,
    }));
    entries.sort((a, b) => b.count - a.count);
    return entries;
  }, [runStatusCounts]);
  const totalRuns = statusEntries.reduce((s, e) => s + e.count, 0);

  // --- Render ---
  return (
    <div className="space-y-6">
      {/* Header + Cluster Status bar */}
      <div className="flex items-center justify-between gap-6">
        <div className="shrink-0">
          <h1 className="text-2xl font-bold text-white">Dashboard</h1>
          <p className="text-sm text-muted-foreground">
            Overview of your Sympozium cluster
          </p>
        </div>
        <div className="flex flex-1 items-center gap-6 rounded-lg border border-border/40 bg-card/50 px-5 py-3 text-sm text-muted-foreground">
          <div className="flex items-center gap-2 font-medium text-white shrink-0">
            <Activity className="h-4 w-4 text-emerald-400" />
            Cluster Status
          </div>
          <div className="h-8 w-px bg-border/60 shrink-0" />
          <div className="flex flex-1 items-center justify-around gap-4">
            <div className="text-center">
              <div className="text-xs text-muted-foreground">Nodes</div>
              <div className="text-lg font-semibold text-foreground">
                {clusterInfo.data?.nodes ?? "—"}
              </div>
            </div>
            <div className="text-center">
              <div className="text-xs text-muted-foreground">Pods</div>
              <div className="text-lg font-semibold text-foreground">
                {clusterInfo.data?.pods ?? "—"}
              </div>
            </div>
            <div className="text-center">
              <div className="text-xs text-muted-foreground">Namespaces</div>
              <div className="text-lg font-semibold text-foreground">
                {clusterInfo.data?.namespaces ?? "—"}
              </div>
            </div>
            <div className="text-center">
              <div className="text-xs text-muted-foreground">Agent Pods</div>
              <div className="text-lg font-semibold text-foreground">
                {totalAgentPods}
              </div>
            </div>
            {clusterInfo.data?.version && (
              <div className="text-center">
                <div className="text-xs text-muted-foreground">K8s Version</div>
                <div className="text-lg font-semibold text-foreground">
                  {clusterInfo.data.version}
                </div>
              </div>
            )}
            {canaryConfig?.enabled && (
              <div className="text-center">
                <div className="text-xs text-muted-foreground">System Health</div>
                {canaryConfig.checks && canaryConfig.checks.length > 0 ? (
                  <div className="flex items-center gap-1 mt-1">
                    {canaryConfig.checks.map((check) => (
                      <div
                        key={check.name}
                        title={`${check.name}: ${check.details}`}
                        className={cn(
                          "h-3 w-3 rounded-sm",
                          check.status === "pass" ? "bg-green-500" : "bg-red-500",
                        )}
                      />
                    ))}
                  </div>
                ) : (
                  <div
                    className={cn(
                      "text-lg font-semibold",
                      canaryConfig.healthStatus === "healthy"
                        ? "text-green-400"
                        : canaryConfig.healthStatus === "degraded"
                          ? "text-yellow-400"
                          : canaryConfig.healthStatus === "unhealthy"
                            ? "text-red-400"
                            : "text-muted-foreground",
                    )}
                  >
                    {canaryConfig.healthStatus
                      ? canaryConfig.healthStatus.charAt(0).toUpperCase() +
                        canaryConfig.healthStatus.slice(1)
                      : "Pending"}
                  </div>
                )}
              </div>
            )}
          </div>
        </div>
      </div>

      {/* Awaiting approval alert */}
      {awaitingApproval.length > 0 && (
        <div
          data-testid="gate-approval-alert"
          className="rounded-lg border border-amber-500/40 bg-amber-500/10 p-4 space-y-3"
        >
          <div className="flex items-center gap-2">
            <ShieldAlert className="h-5 w-5 text-amber-400" />
            <span className="text-sm font-medium text-amber-400">
              {awaitingApproval.length} run
              {awaitingApproval.length > 1 ? "s" : ""} awaiting approval
            </span>
          </div>
          <div className="space-y-2">
            {awaitingApproval.map((run) => (
              <div
                key={run.metadata.name}
                data-testid="gate-approval-card"
                className="flex items-center justify-between rounded-md border border-amber-500/20 bg-background/50 px-3 py-2"
              >
                <div className="min-w-0 mr-3">
                  <Link
                    to={`/runs/${run.metadata.name}`}
                    className="text-xs font-mono text-foreground truncate hover:text-primary block"
                  >
                    {truncate(run.metadata.name, 30)}
                  </Link>
                  <p className="text-[11px] text-muted-foreground truncate">
                    {truncate(taskText(run.spec.task), 60)}
                  </p>
                </div>
                <div className="flex items-center gap-1.5 shrink-0">
                  <Button
                    size="sm"
                    variant="ghost"
                    data-testid="gate-dash-approve-btn"
                    className="h-6 px-2 text-xs text-green-400 hover:bg-green-500/10"
                    onClick={() =>
                      gateVerdict.mutate({
                        name: run.metadata.name,
                        data: { action: "approve", reason: "manual-approval" },
                      })
                    }
                    disabled={gateVerdict.isPending}
                  >
                    <Check className="mr-1 h-3 w-3" />
                    Approve
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    data-testid="gate-dash-reject-btn"
                    className="h-6 px-2 text-xs text-red-400 hover:bg-red-500/10"
                    onClick={() =>
                      gateVerdict.mutate({
                        name: run.metadata.name,
                        data: {
                          action: "reject",
                          response: "Rejected by operator",
                          reason: "manual-rejection",
                        },
                      })
                    }
                    disabled={gateVerdict.isPending}
                  >
                    <X className="mr-1 h-3 w-3" />
                    Reject
                  </Button>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Stat tiles row */}
      <Card className="overflow-hidden">
        <CardContent className="p-0">
          <div className="grid grid-cols-2 divide-x divide-y divide-border/60 sm:grid-cols-3 lg:grid-cols-6 lg:divide-y-0">
            {stats.map((stat) => (
              <div
                key={stat.label}
                className="flex min-h-24 items-center gap-3 px-4 py-3"
              >
                <div className="rounded-md bg-background/60 p-2 border border-border/50">
                  <stat.icon className={`h-4 w-4 ${stat.color}`} />
                </div>
                <div className="min-w-0">
                  <div className="text-[11px] uppercase tracking-wide text-muted-foreground">
                    {stat.label}
                  </div>
                  {runs.isLoading ? (
                    <Skeleton className="mt-1 h-6 w-10" />
                  ) : (
                    <div className="text-xl font-bold leading-tight">
                      {stat.value}
                    </div>
                  )}
                </div>
              </div>
            ))}
          </div>
        </CardContent>
      </Card>

      {/* Layout controls */}
      <div className="flex items-center justify-end gap-2">
        <Button
          size="sm"
          variant="ghost"
          className="h-7 text-xs gap-1.5 text-muted-foreground"
          onClick={handleToggleLock}
        >
          {locked ? (
            <>
              <Lock className="h-3 w-3" /> Locked
            </>
          ) : (
            <>
              <Unlock className="h-3 w-3" /> Unlocked
            </>
          )}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          className="h-7 text-xs gap-1.5 text-muted-foreground"
          onClick={handleResetLayout}
        >
          <RotateCcw className="h-3 w-3" /> Reset Layout
        </Button>
      </div>

      {/* Draggable grid panels */}
      <div ref={containerRef}>
        <ResponsiveGridLayout
          className="layout"
          width={containerWidth}
          layouts={layouts}
          breakpoints={{ lg: 1200, md: 768, sm: 0 }}
          cols={{ lg: 12, md: 10, sm: 6 }}
          rowHeight={40}
          onLayoutChange={handleLayoutChange}
          dragConfig={{ enabled: !locked, handle: ".drag-handle" }}
          resizeConfig={{ enabled: !locked }}
          containerPadding={[0, 0]}
          margin={[16, 16]}
        >
          {/* ---- Agent Activity ---- */}
          <div key="activity">
            <PanelWrapper
              title="Agent Activity"
              icon={Activity}
              locked={locked}
              actions={
                <div className="flex items-center gap-1">
                  <Button
                    size="sm"
                    variant={durationMode === "avg" ? "default" : "outline"}
                    className="h-6 text-[11px] px-2"
                    onClick={() => setDurationMode("avg")}
                  >
                    Avg
                  </Button>
                  <Button
                    size="sm"
                    variant={durationMode === "p95" ? "default" : "outline"}
                    className="h-6 text-[11px] px-2"
                    onClick={() => setDurationMode("p95")}
                  >
                    P95
                  </Button>
                  <div className="w-px h-4 bg-border/60 mx-1" />
                  <Button
                    size="sm"
                    variant={range === "1h" ? "default" : "outline"}
                    className="h-6 text-[11px] px-2"
                    onClick={() => setRange("1h")}
                  >
                    1h
                  </Button>
                  <Button
                    size="sm"
                    variant={range === "24h" ? "default" : "outline"}
                    className="h-6 text-[11px] px-2"
                    onClick={() => setRange("24h")}
                  >
                    24hr
                  </Button>
                  <Button
                    size="sm"
                    variant={range === "7d" ? "default" : "outline"}
                    className="h-6 text-[11px] px-2"
                    onClick={() => setRange("7d")}
                  >
                    7d
                  </Button>
                </div>
              }
            >
              <div className="mb-2 flex flex-wrap items-center gap-4 text-xs">
                <span className="text-muted-foreground">
                  Total runs:{" "}
                  <span className="text-foreground font-semibold">
                    {totalInRange}
                  </span>
                </span>
                <span className="text-cyan-400">
                  {durationMode === "p95" ? "P95 duration" : "Avg duration"}:{" "}
                  <span className="font-semibold">
                    {(durationMode === "p95"
                      ? p95DurationSecInRange
                      : avgDurationSecInRange
                    ).toFixed(1)}
                    s
                  </span>
                </span>
                <span className="text-red-400">
                  Failed: <span className="font-semibold">{failedInRange}</span>
                </span>
                {totalServing > 0 && (
                  <span className="text-yellow-400">
                    Serving:{" "}
                    <span className="font-semibold">{totalServing}</span>
                  </span>
                )}
              </div>
              {runs.isLoading ? (
                <Skeleton className="h-full min-h-[180px] w-full" />
              ) : (
                <div className="relative h-full min-h-[180px] w-full rounded-lg border border-border/50 bg-background/40 p-2">
                  <svg
                    viewBox={`0 0 ${chartW} ${chartH}`}
                    className="h-full w-full"
                    onMouseLeave={() => setHoverIdx(null)}
                  >
                    {[0, 0.25, 0.5, 0.75, 1].map((t) => {
                      const y = padY + innerH - innerH * t;
                      return (
                        <line
                          key={t}
                          x1={padX}
                          x2={chartW - padX}
                          y1={y}
                          y2={y}
                          stroke="currentColor"
                          className="text-border/60"
                          strokeWidth={1}
                        />
                      );
                    })}
                    {activeX !== null && (
                      <line
                        x1={activeX}
                        x2={activeX}
                        y1={padY}
                        y2={chartH - padY}
                        stroke="currentColor"
                        className="text-blue-300/60"
                        strokeDasharray="4 3"
                        strokeWidth={1}
                      />
                    )}
                    {activity.map((b, i) => (
                      <rect
                        key={b.ts}
                        x={xFor(i) - barW / 2}
                        y={yForAgents(b.agentsInstalled)}
                        width={barW}
                        height={padY + innerH - yForAgents(b.agentsInstalled)}
                        className="pointer-events-none fill-cyan-400/15"
                      />
                    ))}
                    <path
                      d={linePath(durationPoints)}
                      fill="none"
                      stroke="currentColor"
                      className="text-blue-400"
                      strokeWidth={2.5}
                    />
                    {totalServing > 0 && (
                      <path
                        d={linePath(servingPoints)}
                        fill="none"
                        stroke="currentColor"
                        className="text-yellow-400"
                        strokeWidth={2}
                        strokeDasharray="6 3"
                      />
                    )}
                    {totalServing > 0 &&
                      activity.map((b, i) => (
                        <circle
                          key={`srv-${b.ts}`}
                          cx={xFor(i)}
                          cy={yForServing(b.serving)}
                          r={hoverIdx === i ? 4 : 2.5}
                          className="fill-yellow-400"
                        />
                      ))}
                    {activity.map((b, i) => (
                      <g key={`pt-${b.ts}`}>
                        <circle
                          cx={xFor(i)}
                          cy={yForDuration(durationFor(b))}
                          r={hoverIdx === i ? 5 : 3.5}
                          className="cursor-pointer fill-blue-400"
                          onMouseEnter={() => setHoverIdx(i)}
                        />
                        {b.failed > 0 && (
                          <circle
                            cx={xFor(i)}
                            cy={yForDuration(durationFor(b))}
                            r={hoverIdx === i ? 7 : 6}
                            className="cursor-pointer fill-transparent stroke-red-400"
                            strokeWidth={2}
                            onMouseEnter={() => setHoverIdx(i)}
                          />
                        )}
                      </g>
                    ))}
                    {activity.map((b, i) => (
                      <text
                        key={`x-${b.ts}`}
                        x={xFor(i)}
                        y={chartH - 5}
                        textAnchor="middle"
                        className={`fill-current text-[10px] ${i % Math.ceil(activity.length / 8) === 0 || i === activity.length - 1 ? "text-muted-foreground" : "text-transparent"}`}
                      >
                        {b.label}
                      </text>
                    ))}
                    {activeX !== null && activeYDuration !== null && (
                      <circle
                        cx={activeX}
                        cy={activeYDuration}
                        r={6}
                        className="fill-blue-300/30"
                      />
                    )}
                  </svg>
                  {activePoint && (
                    <div className="pointer-events-none absolute right-3 top-3 rounded-md border border-white/10 bg-black/70 px-3 py-2 text-xs backdrop-blur">
                      <div className="font-semibold text-foreground">
                        {activePoint.label}
                      </div>
                      <div className="text-blue-300">
                        {durationMode === "p95"
                          ? "P95 duration"
                          : "Avg duration"}
                        :{" "}
                        {(durationMode === "p95"
                          ? activePoint.p95DurationSec
                          : activePoint.avgDurationSec
                        ).toFixed(1)}
                        s
                      </div>
                      <div className="text-muted-foreground">
                        Runs: {activePoint.runs}
                      </div>
                      <div className="text-red-300">
                        Failed: {activePoint.failed}
                      </div>
                      <div className="text-cyan-300">
                        Agents installed: {activePoint.agentsInstalled}
                      </div>
                      {activePoint.serving > 0 && (
                        <div className="text-yellow-300">
                          Serving: {activePoint.serving}
                        </div>
                      )}
                    </div>
                  )}
                  <div className="mt-1 flex items-center gap-4 px-2 text-xs">
                    <span className="inline-flex items-center gap-2 text-muted-foreground">
                      <span className="h-2 w-2 rounded-full bg-blue-400" />
                      {durationMode === "p95"
                        ? "P95 run duration"
                        : "Avg run duration"}
                    </span>
                    <span className="inline-flex items-center gap-2 text-muted-foreground">
                      <span className="h-2 w-2 rounded-sm bg-cyan-400/40" />
                      Agents installed
                    </span>
                    <span className="inline-flex items-center gap-2 text-muted-foreground">
                      <span className="h-2 w-2 rounded-full bg-yellow-400" />
                      Serving
                    </span>
                    <span className="inline-flex items-center gap-2 text-muted-foreground">
                      <span className="h-2 w-2 rounded-full bg-red-400" />
                      Failed
                    </span>
                  </div>
                </div>
              )}
            </PanelWrapper>
          </div>

          {/* ---- Token Usage by Model ---- */}
          <div key="tokensByModel">
            <PanelWrapper
              title="Token Usage by Model"
              icon={Zap}
              locked={locked}
            >
              {observability.isLoading ? (
                <div className="space-y-3">
                  {Array.from({ length: 3 }).map((_, i) => (
                    <Skeleton key={i} className="h-10 w-full" />
                  ))}
                </div>
              ) : modelBreakdown.length === 0 ? (
                <p className="text-sm text-muted-foreground py-4">
                  No model data yet
                </p>
              ) : (
                <div className="space-y-3">
                  {modelBreakdown.map((m) => (
                    <div key={m.label}>
                      <div className="flex items-center justify-between mb-1">
                        <span className="text-xs font-mono text-foreground truncate max-w-[60%]">
                          {m.label}
                        </span>
                        <span className="text-xs text-muted-foreground font-mono">
                          {compactTokens(m.input)} in /{" "}
                          {compactTokens(m.output)} out
                        </span>
                      </div>
                      <div className="h-2 w-full rounded-full bg-background/60 border border-border/50 overflow-hidden">
                        <div className="h-full flex">
                          <div
                            className="bg-blue-400 transition-all"
                            style={{
                              width: `${(m.input / maxModelTotal) * 100}%`,
                            }}
                          />
                          <div
                            className="bg-emerald-400 transition-all"
                            style={{
                              width: `${(m.output / maxModelTotal) * 100}%`,
                            }}
                          />
                        </div>
                      </div>
                    </div>
                  ))}
                  <div className="flex items-center gap-4 text-[11px] text-muted-foreground pt-1">
                    <span className="inline-flex items-center gap-1.5">
                      <span className="h-2 w-2 rounded-full bg-blue-400" />{" "}
                      Input
                    </span>
                    <span className="inline-flex items-center gap-1.5">
                      <span className="h-2 w-2 rounded-full bg-emerald-400" />{" "}
                      Output
                    </span>
                  </div>
                </div>
              )}
            </PanelWrapper>
          </div>

          {/* ---- Top Tools ---- */}
          <div key="topTools">
            <PanelWrapper
              title="Tool Invocations"
              icon={Wrench}
              locked={locked}
            >
              {observability.isLoading ? (
                <div className="space-y-3">
                  {Array.from({ length: 4 }).map((_, i) => (
                    <Skeleton key={i} className="h-8 w-full" />
                  ))}
                </div>
              ) : topTools.length === 0 ? (
                <p className="text-sm text-muted-foreground py-4">
                  No tool data yet
                </p>
              ) : (
                <div className="space-y-2">
                  {topTools.map((t) => (
                    <div key={t.label} className="flex items-center gap-3">
                      <span className="text-xs font-mono text-foreground w-28 truncate shrink-0">
                        {t.label}
                      </span>
                      <div className="flex-1 h-2 rounded-full bg-background/60 border border-border/50 overflow-hidden">
                        <div
                          className="h-full bg-blue-400/70 rounded-full transition-all"
                          style={{
                            width: `${(t.value / maxToolValue) * 100}%`,
                          }}
                        />
                      </div>
                      <span className="text-xs text-muted-foreground font-mono w-12 text-right shrink-0">
                        {t.value >= 1000
                          ? compactTokens(t.value)
                          : Math.round(t.value).toLocaleString()}
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </PanelWrapper>
          </div>

          {/* ---- Recent Runs ---- */}
          <div key="recentRuns">
            <PanelWrapper
              title="Recent Runs"
              icon={Play}
              locked={locked}
              actions={
                <Link
                  to="/runs"
                  className="text-[11px] text-muted-foreground hover:text-foreground"
                >
                  View all →
                </Link>
              }
            >
              {runs.isLoading ? (
                <div className="space-y-2">
                  {Array.from({ length: 4 }).map((_, i) => (
                    <Skeleton key={i} className="h-8 w-full" />
                  ))}
                </div>
              ) : recentRuns.length === 0 ? (
                <p className="text-sm text-muted-foreground">No runs yet</p>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Name</TableHead>
                      <TableHead>Instance</TableHead>
                      <TableHead>Phase</TableHead>
                      <TableHead>Tokens</TableHead>
                      <TableHead>Duration</TableHead>
                      <TableHead>Age</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {recentRuns.map((run) => (
                      <TableRow key={run.metadata.name}>
                        <TableCell className="font-mono text-xs">
                          <Link
                            to={`/runs/${run.metadata.name}`}
                            className="hover:text-primary"
                          >
                            {truncate(run.metadata.name, 24)}
                          </Link>
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {run.spec.agentRef}
                        </TableCell>
                        <TableCell>
                          <StatusBadge phase={run.status?.phase} />
                        </TableCell>
                        <TableCell className="text-xs font-mono text-muted-foreground">
                          {run.status?.tokenUsage?.totalTokens
                            ? compactTokens(run.status.tokenUsage.totalTokens)
                            : "—"}
                        </TableCell>
                        <TableCell className="text-xs font-mono text-muted-foreground">
                          {run.status?.tokenUsage?.durationMs
                            ? `${(run.status.tokenUsage.durationMs / 1000).toFixed(1)}s`
                            : "—"}
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {formatAge(run.metadata.creationTimestamp)}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </PanelWrapper>
          </div>

          {/* ---- Event Stream ---- */}
          <div key="eventStream">
            <PanelWrapper
              title="Event Stream"
              icon={Radio}
              locked={locked}
              actions={
                <div className="flex items-center gap-2">
                  <span className="text-[11px] text-muted-foreground">
                    {events.length} events
                  </span>
                  {connected ? (
                    <span className="relative flex h-2 w-2">
                      <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-75" />
                      <span className="relative inline-flex h-2 w-2 rounded-full bg-emerald-400" />
                    </span>
                  ) : (
                    <span className="h-2 w-2 rounded-full bg-red-400" />
                  )}
                </div>
              }
            >
              <div className="h-full space-y-1 overflow-auto rounded-lg bg-background/50 border border-border/50 p-3 font-mono text-xs">
                {events.length === 0 ? (
                  <p className="text-muted-foreground">
                    {connected
                      ? "Waiting for events…"
                      : "Connecting to stream…"}
                  </p>
                ) : (
                  events
                    .slice()
                    .reverse()
                    .map((evt, i) => (
                      <div key={i} className="text-muted-foreground">
                        <span className="text-emerald-400/80">
                          {new Date(evt.timestamp).toLocaleTimeString()}
                        </span>{" "}
                        <span className="text-blue-400">{evt.topic}</span>{" "}
                        {typeof evt.data === "string"
                          ? truncate(evt.data, 80)
                          : truncate(JSON.stringify(evt.data), 80)}
                      </div>
                    ))
                )}
              </div>
            </PanelWrapper>
          </div>

          {/* ---- Run Status Breakdown ---- */}
          <div key="runStatus">
            <PanelWrapper
              title="Run Status"
              icon={CheckCircle2}
              locked={locked}
            >
              {runs.isLoading ? (
                <Skeleton className="h-32 w-full" />
              ) : totalRuns === 0 ? (
                <p className="text-sm text-muted-foreground py-4">
                  No runs yet
                </p>
              ) : (
                <div className="flex items-start gap-6">
                  {/* Donut chart */}
                  <div className="shrink-0">
                    <svg width="100" height="100" viewBox="0 0 100 100">
                      {(() => {
                        const cx = 50,
                          cy = 50,
                          r = 38,
                          stroke = 10;
                        const circumference = 2 * Math.PI * r;
                        let offset = 0;
                        return statusEntries.map((entry) => {
                          const pct = entry.count / totalRuns;
                          const dash = pct * circumference;
                          const gap = circumference - dash;
                          const el = (
                            <circle
                              key={entry.phase}
                              cx={cx}
                              cy={cy}
                              r={r}
                              fill="none"
                              stroke={entry.color}
                              strokeWidth={stroke}
                              strokeDasharray={`${dash} ${gap}`}
                              strokeDashoffset={-offset}
                              transform={`rotate(-90 ${cx} ${cy})`}
                              className="transition-all"
                            />
                          );
                          offset += dash;
                          return el;
                        });
                      })()}
                      <text
                        x="50"
                        y="50"
                        textAnchor="middle"
                        dominantBaseline="central"
                        className="fill-foreground text-lg font-bold"
                        fontSize="18"
                      >
                        {totalRuns}
                      </text>
                    </svg>
                  </div>
                  {/* Legend */}
                  <div className="space-y-1.5 min-w-0">
                    {statusEntries.map((entry) => (
                      <div
                        key={entry.phase}
                        className="flex items-center gap-2 text-xs"
                      >
                        <span
                          className="h-2.5 w-2.5 rounded-full shrink-0"
                          style={{ backgroundColor: entry.color }}
                        />
                        <span className="text-muted-foreground capitalize">
                          {entry.phase}
                        </span>
                        <span className="font-semibold text-foreground ml-auto">
                          {entry.count}
                        </span>
                      </div>
                    ))}
                  </div>
                </div>
              )}
            </PanelWrapper>
          </div>

          {/* ---- Recent Errors ---- */}
          <div key="recentErrors">
            <PanelWrapper title="Recent Errors" icon={XCircle} locked={locked}>
              {runs.isLoading ? (
                <div className="space-y-2">
                  {Array.from({ length: 3 }).map((_, i) => (
                    <Skeleton key={i} className="h-10 w-full" />
                  ))}
                </div>
              ) : failedRuns.length === 0 ? (
                <div className="flex flex-col items-center justify-center py-6 text-muted-foreground">
                  <CheckCircle2 className="h-8 w-8 text-emerald-400/50 mb-2" />
                  <p className="text-sm">No recent errors</p>
                </div>
              ) : (
                <div className="space-y-2">
                  {failedRuns.map((run) => (
                    <Link
                      key={run.metadata.name}
                      to={`/runs/${run.metadata.name}`}
                      className="block rounded-lg border border-red-500/20 bg-red-500/5 px-3 py-2 hover:border-red-500/40 transition-colors"
                    >
                      <div className="flex items-center justify-between gap-2 mb-0.5">
                        <span className="text-xs font-mono text-foreground truncate">
                          {truncate(run.metadata.name, 36)}
                        </span>
                        <span className="text-[11px] text-muted-foreground shrink-0">
                          {formatAge(run.metadata.creationTimestamp)}
                        </span>
                      </div>
                      {run.status?.error && (
                        <p className="text-[11px] text-red-400/80 truncate">
                          {truncate(run.status.error, 100)}
                        </p>
                      )}
                    </Link>
                  ))}
                </div>
              )}
            </PanelWrapper>
          </div>
          {/* ---- Local Models ---- */}
          <div key="localModels">
            <PanelWrapper title="Local Models" icon={Cpu} locked={locked}>
              <LocalModelsPanel />
            </PanelWrapper>
          </div>
          {/* ---- Team Canvas ---- */}
          <div key="teamCanvas">
            <PanelWrapper title="Team Canvas" icon={Users} locked={locked}>
              <DashboardEnsembleCanvas />
            </PanelWrapper>
          </div>
        </ResponsiveGridLayout>
      </div>
    </div>
  );
}
