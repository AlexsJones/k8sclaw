import { useEffect } from "react";
import { useParams, Link } from "react-router-dom";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { useRun, useGateVerdict, useRuntimes } from "@/hooks/use-api";
import { StatusBadge } from "@/components/status-badge";
import { Card, CardHeader, CardTitle, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { Button } from "@/components/ui/button";
import {
  Clock,
  Cpu,
  DollarSign,
  Zap,
  AlertTriangle,
  ShieldCheck,
  ShieldX,
  Pencil,
  ShieldAlert,
  Check,
  X,
  RotateCcw,
} from "lucide-react";
import { Breadcrumbs } from "@/components/breadcrumbs";
import { useRunsSeen } from "@/hooks/use-runs-seen";
import { costTooltip, effectiveCost, formatAge, formatUsd, taskText } from "@/lib/utils";

// Presentation for each gate verdict. "retried" is amber rather than red: the
// attempt was superseded by another one, which is not a failure — the chain may
// still succeed. Unknown verdicts fall back to the red "something went wrong"
// treatment.
const GATE_VERDICT_STYLES: Record<
  string,
  { border: string; text: string; icon: typeof ShieldCheck }
> = {
  approved: {
    border: "border-green-500/30 bg-green-500/5",
    text: "text-green-400",
    icon: ShieldCheck,
  },
  "allowed-by-default": {
    border: "border-green-500/30 bg-green-500/5",
    text: "text-green-400",
    icon: ShieldCheck,
  },
  rewritten: {
    border: "border-blue-500/30 bg-blue-500/5",
    text: "text-blue-400",
    icon: Pencil,
  },
  retried: {
    border: "border-amber-500/30 bg-amber-500/5",
    text: "text-amber-400",
    icon: RotateCcw,
  },
  rejected: {
    border: "border-red-500/30 bg-red-500/5",
    text: "text-red-400",
    icon: ShieldX,
  },
};

const GATE_VERDICT_FALLBACK = {
  border: "border-red-500/30 bg-red-500/5",
  text: "text-red-400",
  icon: ShieldAlert,
};

function gateVerdictStyle(verdict: string) {
  return GATE_VERDICT_STYLES[verdict] ?? GATE_VERDICT_FALLBACK;
}

export function RunDetailPage() {
  const { name } = useParams<{ name: string }>();
  const { data: run, isLoading } = useRun(name || "");
  const gateVerdict = useGateVerdict();
  const runtimes = useRuntimes();
  const { markSeenUpTo } = useRunsSeen();

  const isAwaitingGate =
    run?.status?.phase === "PostRunning" &&
    !run?.status?.gateVerdict &&
    run?.spec.lifecycle?.postRun?.some((h) => h.gate);

  // Mark this run as seen when viewing its detail page.
  useEffect(() => {
    if (run?.metadata.creationTimestamp) {
      markSeenUpTo(run.metadata.creationTimestamp);
    }
  }, [run?.metadata.creationTimestamp, markSeenUpTo]);

  if (isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-64" />
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }

  if (!run) {
    return <p className="text-muted-foreground">Run not found</p>;
  }

  const usage = run.status?.tokenUsage;
  const duration = usage?.durationMs
    ? `${(usage.durationMs / 1000).toFixed(1)}s`
    : "—";
  const est = effectiveCost(run);
  const taskMode = typeof run.spec.task === "object" ? run.spec.task : undefined;
  const runtimeName = (taskMode?.mode === "harness" ? taskMode.parameters?.runtime : undefined) || run.status?.harnessRuntimeRef;
  const runtime = runtimeName ? runtimes.data?.find((item) => item.metadata.name === runtimeName) : undefined;
  const isHarnessRun = Boolean(runtimeName || run.status?.harnessImageDigest);

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <Breadcrumbs
          items={[
            { label: "Ensembles", to: "/ensembles" },
            {
              label: run.spec.agentRef,
              to: `/agents/${run.spec.agentRef}`,
            },
            { label: run.metadata.name },
          ]}
        />
        <h1 className="text-xl font-bold font-mono">{run.metadata.name}</h1>
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <StatusBadge phase={run.status?.phase} />
          <span>·</span>
          {formatAge(run.metadata.creationTimestamp)} ago
        </div>
      </div>

      {/* Stats row */}
      {(usage || est) && (
        <div className="grid gap-4 sm:grid-cols-4">
          {usage && (
            <>
              <Card>
                <CardContent className="flex items-center gap-3 p-4">
                  <Zap className="h-5 w-5 text-amber-400" />
                  <div>
                    <p className="text-sm text-muted-foreground">
                      Total Tokens
                    </p>
                    <p className="text-lg font-bold">
                      {usage.totalTokens.toLocaleString()}
                    </p>
                  </div>
                </CardContent>
              </Card>
              <Card>
                <CardContent className="flex items-center gap-3 p-4">
                  <Cpu className="h-5 w-5 text-blue-400" />
                  <div>
                    <p className="text-sm text-muted-foreground">Tool Calls</p>
                    <p className="text-lg font-bold">{usage.toolCalls}</p>
                  </div>
                </CardContent>
              </Card>
              <Card>
                <CardContent className="flex items-center gap-3 p-4">
                  <Clock className="h-5 w-5 text-purple-400" />
                  <div>
                    <p className="text-sm text-muted-foreground">Duration</p>
                    <p className="text-lg font-bold">{duration}</p>
                  </div>
                </CardContent>
              </Card>
              <Card>
                <CardContent className="flex items-center gap-3 p-4">
                  <div>
                    <p className="text-sm text-muted-foreground">In / Out</p>
                    <p className="text-sm font-mono">
                      {usage.inputTokens.toLocaleString()} /{" "}
                      {usage.outputTokens.toLocaleString()}
                    </p>
                  </div>
                </CardContent>
              </Card>
            </>
          )}
          {est && (
            <Card>
              <CardContent className="flex items-center gap-3 p-4">
                <DollarSign className="h-5 w-5 text-green-400" />
                <div className="min-w-0" title={costTooltip(est)}>
                  <p className="text-sm text-muted-foreground">Est. spend</p>
                  <p className="text-lg font-bold">
                    {formatUsd(est.amountMicro)}
                  </p>
                  <p className="text-xs text-muted-foreground">
                    in {formatUsd(est.inputAmountMicro)} · out{" "}
                    {formatUsd(est.outputAmountMicro)}
                  </p>
                </div>
              </CardContent>
            </Card>
          )}
        </div>
      )}

      {/* PostRunning banner */}
      {run.status?.phase === "PostRunning" && (
        <div className="flex items-center gap-2 rounded-lg border border-orange-500/30 bg-orange-500/5 p-3">
          <Clock className="h-4 w-4 text-orange-400 animate-spin" />
          <div className="text-sm">
            <span className="font-medium text-orange-400">
              Post-run hooks executing
            </span>
            {run.status.postRunJobName && (
              <span className="text-muted-foreground ml-2 font-mono">
                Job: {run.status.postRunJobName}
              </span>
            )}
          </div>
        </div>
      )}

      {/* Gate approval action bar */}
      {isAwaitingGate && (
        <div
          data-testid="gate-approval-bar"
          className="flex items-center justify-between rounded-lg border border-amber-500/40 bg-amber-500/10 p-4"
        >
          <div className="flex items-center gap-2">
            <ShieldAlert className="h-5 w-5 text-amber-400" />
            <div>
              <p className="text-sm font-medium text-amber-400">
                Approval required
              </p>
              <p className="text-xs text-muted-foreground">
                This run's response is being held by a gate hook. Review and
                approve or reject.
              </p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <Button
              size="sm"
              variant="outline"
              data-testid="gate-reject-detail-btn"
              className="border-red-500/40 text-red-400 hover:bg-red-500/10"
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
              <X className="mr-1 h-4 w-4" />
              Reject
            </Button>
            <Button
              size="sm"
              data-testid="gate-approve-detail-btn"
              className="bg-green-600 text-white hover:bg-green-700 border-0"
              onClick={() =>
                gateVerdict.mutate({
                  name: run.metadata.name,
                  data: { action: "approve", reason: "manual-approval" },
                })
              }
              disabled={gateVerdict.isPending}
            >
              <Check className="mr-1 h-4 w-4" />
              Approve
            </Button>
          </div>
        </div>
      )}

      {/* PostRunFailed condition */}
      {run.status?.conditions?.some(
        (c) => c.type === "PostRunFailed" && c.status === "True",
      ) && (
        <div className="flex items-center gap-2 rounded-lg border border-yellow-500/30 bg-yellow-500/5 p-3">
          <AlertTriangle className="h-4 w-4 text-yellow-500" />
          <span className="text-sm text-yellow-500">
            One or more post-run hooks failed (agent outcome unchanged)
          </span>
        </div>
      )}

      {/* Gate verdict banner */}
      {run.status?.gateVerdict &&
        (() => {
          const style = gateVerdictStyle(run.status.gateVerdict);
          const Icon = style.icon;
          return (
            <div
              data-testid="gate-verdict-banner"
              className={`flex items-center gap-2 rounded-lg border p-3 ${style.border}`}
            >
              <Icon className={`h-4 w-4 ${style.text}`} />
              <span className={`text-sm ${style.text}`}>
                Response gate: {run.status.gateVerdict}
                {run.status.retryOf && ` (retry of ${run.status.retryOf})`}
              </span>
            </div>
          );
        })()}

      {isHarnessRun && (
        <Card>
          <CardHeader className="pb-3">
            <CardTitle className="flex items-center gap-2 text-base">
              <ShieldCheck className="h-4 w-4 text-cyan-400" />
              External runtime provenance
            </CardTitle>
          </CardHeader>
          <CardContent className="grid gap-3 text-sm sm:grid-cols-2">
            <div><span className="text-muted-foreground">Runtime</span><p className="font-mono">{runtimeName || "inline image"}</p></div>
            <div><span className="text-muted-foreground">Selection</span><p>{run.status?.harnessRuntimeSource === "agent-default" ? "Agent default" : "Run override"}</p></div>
            <div><span className="text-muted-foreground">Image digest</span><p className="font-mono break-all">{run.status?.harnessImageDigest || runtime?.status?.resolvedImageDigest || "pending"}</p></div>
            <div><span className="text-muted-foreground">Contract</span><p>{run.status?.harnessContractVersion || runtime?.spec.contractVersion || "not declared"}</p></div>
            <div><span className="text-muted-foreground">Support owner</span><p>{runtime?.spec.supportOwner || "not declared"}</p></div>
            <div className="sm:col-span-2"><span className="text-muted-foreground">Capability provenance</span><p>{runtime?.spec.capabilities?.length ? runtime.spec.capabilities.join(", ") + " (runtime claim; platform policy still enforced)" : "No runtime capabilities claimed"}</p></div>
          </CardContent>
        </Card>
      )}

      <Tabs defaultValue="task">
        <TabsList>
          <TabsTrigger value="task">Task</TabsTrigger>
          <TabsTrigger value="result">Result</TabsTrigger>
          <TabsTrigger value="spec">Spec</TabsTrigger>
        </TabsList>

        <TabsContent value="task">
          <Card>
            <CardContent className="pt-6">
              <pre className="whitespace-pre-wrap text-sm">{taskText(run.spec.task)}</pre>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="result">
          <Card>
            <CardContent className="pt-6">
              {run.status?.result ? (
                <div className="prose prose-sm prose-invert max-w-none">
                  <ReactMarkdown remarkPlugins={[remarkGfm]}>
                    {run.status.result}
                  </ReactMarkdown>
                </div>
              ) : run.status?.error ? (
                <div className="space-y-2">
                  <Badge variant="destructive">Error</Badge>
                  <pre className="whitespace-pre-wrap text-sm text-destructive">
                    {run.status.error}
                  </pre>
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">
                  {run.status?.phase === "Running"
                    ? "Run is still in progress…"
                    : run.status?.phase === "PostRunning"
                      ? "Agent completed, running post-hooks…"
                      : "No result available"}
                </p>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="spec">
          <Card>
            <CardContent className="pt-6">
              <pre className="text-xs font-mono whitespace-pre-wrap rounded bg-muted/50 p-4 overflow-auto max-h-96">
                {JSON.stringify(run.spec, null, 2)}
              </pre>
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>

      {/* Pod info */}
      {run.status?.podName && (
        <>
          <Separator />
          <div className="text-sm text-muted-foreground">
            Pod: <span className="font-mono">{run.status.podName}</span>
            {run.status.exitCode !== undefined && (
              <>
                {" "}
                · Exit code:{" "}
                <span className="font-mono">{run.status.exitCode}</span>
              </>
            )}
            {run.status.postRunJobName && (
              <>
                {" "}
                · PostRun Job:{" "}
                <span className="font-mono">{run.status.postRunJobName}</span>
              </>
            )}
          </div>
        </>
      )}
    </div>
  );
}
