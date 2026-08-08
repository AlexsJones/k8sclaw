import { useState, useEffect, useRef } from "react";
import { Link } from "react-router-dom";
import {
  useRuns,
  useDeleteRun,
  useCreateRun,
  useAgents,
  useObservabilityMetrics,
  useGateVerdict,
} from "@/hooks/use-api";
import { StatusBadge } from "@/components/status-badge";
import {
  Table,
  TableHeader,
  TableRow,
  TableHead,
  TableBody,
  TableCell,
} from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
  DialogDescription,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Plus,
  Trash2,
  ExternalLink,
  ShieldAlert,
  Check,
  X,
} from "lucide-react";
import {
  costTooltip,
  effectiveCost,
  formatAge,
  formatUsd,
  sumEffectiveCosts,
  truncate,
} from "@/lib/utils";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useRunsSeen } from "@/hooks/use-runs-seen";
import type { AgentRun } from "@/lib/api";

/** Returns true when a run is in PostRunning with a gate hook awaiting a verdict. */
function isAwaitingGate(run: AgentRun): boolean {
  if (run.status?.phase !== "PostRunning") return false;
  if (run.status?.gateVerdict) return false; // already resolved
  return !!run.spec.lifecycle?.postRun?.some((h) => h.gate);
}

export function RunsPage() {
  const { data, isLoading } = useRuns();
  const instances = useAgents();
  const observability = useObservabilityMetrics();
  const deleteRun = useDeleteRun();
  const createRun = useCreateRun();
  const gateVerdict = useGateVerdict();
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const { isUnseen, markAllSeen } = useRunsSeen();
  const markedRef = useRef(false);
  const [form, setForm] = useState({
    agentRef: "",
    task: "",
    model: "",
    timeout: "5m",
    backend: "job",
  });

  // Mark all runs as seen after a short delay so "new" dots are visible briefly.
  useEffect(() => {
    if (markedRef.current || isLoading || !data) return;
    markedRef.current = true;
    const timer = setTimeout(markAllSeen, 2000);
    return () => clearTimeout(timer);
  }, [isLoading, data, markAllSeen]);

  const sorted = (data || []).sort((a, b) => {
    const ta = a.metadata.creationTimestamp ? new Date(a.metadata.creationTimestamp).getTime() : 0;
    const tb = b.metadata.creationTimestamp ? new Date(b.metadata.creationTimestamp).getTime() : 0;
    return tb - ta;
  });

  const filtered = sorted.filter(
    (r) =>
      r.metadata.name.toLowerCase().includes(search.toLowerCase()) ||
      r.spec.agentRef.toLowerCase().includes(search.toLowerCase()) ||
      r.spec.task.toLowerCase().includes(search.toLowerCase()),
  );

  const spend = sumEffectiveCosts(filtered);

  const handleCreate = () => {
    createRun.mutate(form, {
      onSuccess: () => {
        setOpen(false);
        setForm({ agentRef: "", task: "", model: "", timeout: "5m", backend: "job" });
      },
    });
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">Runs</h1>
          <p className="text-sm text-muted-foreground">
            AgentRuns — individual agent invocations
          </p>
        </div>
        <Dialog open={open} onOpenChange={setOpen}>
          <DialogTrigger asChild>
            <Button
              size="sm"
              className="bg-primary hover:bg-primary/90 text-primary-foreground border-0"
            >
              <Plus className="mr-2 h-4 w-4" /> New Run
            </Button>
          </DialogTrigger>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Create Run</DialogTitle>
              <DialogDescription>
                Task an agent instance to perform work.
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-4 pt-2">
              <div className="space-y-2">
                <Label>Instance</Label>
                <Select
                  value={form.agentRef}
                  onValueChange={(v) => setForm({ ...form, agentRef: v })}
                >
                  <SelectTrigger>
                    <SelectValue placeholder="Select agent" />
                  </SelectTrigger>
                  <SelectContent>
                    {(instances.data || []).map((inst) => (
                      <SelectItem
                        key={inst.metadata.name}
                        value={inst.metadata.name}
                      >
                        {inst.metadata.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <Label>Task</Label>
                <Textarea
                  value={form.task}
                  onChange={(e) => setForm({ ...form, task: e.target.value })}
                  placeholder="Describe the task for the agent…"
                  rows={4}
                />
              </div>
              <div className="grid grid-cols-2 gap-4">
                <div className="space-y-2">
                  <Label>Model (optional)</Label>
                  <Input
                    value={form.model}
                    onChange={(e) =>
                      setForm({ ...form, model: e.target.value })
                    }
                    placeholder="gpt-4o"
                  />
                </div>
                <div className="space-y-2">
                  <Label>Timeout</Label>
                  <Input
                    value={form.timeout}
                    onChange={(e) =>
                      setForm({ ...form, timeout: e.target.value })
                    }
                    placeholder="5m"
                  />
                </div>
              </div>
              <div className="space-y-2">
                <Label>Backend</Label>
                <Select
                  value={form.backend}
                  onValueChange={(v) => setForm({ ...form, backend: v })}
                >
                  <SelectTrigger>
                    <SelectValue placeholder="Standard (Kubernetes Job)" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="job">Standard — Kubernetes Job</SelectItem>
                    <SelectItem value="celln">Celln — hermetic, hardware-isolated</SelectItem>
                  </SelectContent>
                </Select>
                {form.backend === "celln" && (
                  <>
                    <p className="text-xs text-muted-foreground mt-1">
                      Celln runs one bounded computation in a sealed microVM.
                      No ensembles, delegation, shared memory, or streaming.
                      Best for single-shot high-risk or sensitive tasks.
                    </p>
                    <p className="text-xs text-amber-500/80 mt-1">
                      Uses whatever AI provider is configured on the KVM
                      host, not this run's Model field — and may be
                      disabled or unconfigured for this cluster. If the run
                      fails immediately with a router or provider error,
                      ask your cluster admin whether Celln is enabled and
                      has an API key or CLI set up.
                    </p>
                  </>
                )}
              </div>
              <Button
                className="w-full bg-primary hover:bg-primary/90 text-primary-foreground border-0"
                onClick={handleCreate}
                disabled={
                  !form.agentRef || !form.task || createRun.isPending
                }
              >
                {createRun.isPending ? "Creating…" : "Create Run"}
              </Button>
            </div>
          </DialogContent>
        </Dialog>
      </div>

      <Input
        placeholder="Search runs…"
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        className="max-w-sm"
      />

      <div className="grid gap-4 md:grid-cols-5">
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">
              Collector
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-xl font-semibold">
              {observability.data?.collectorReachable
                ? "Connected"
                : observability.data?.collectorError?.includes("no such host")
                  ? "Not reachable"
                  : "Unavailable"}
            </p>
            {observability.data?.collectorError && (
              <p className="mt-1 text-xs text-muted-foreground">
                {observability.data.collectorError.includes("no such host")
                  ? "Collector DNS not resolvable — running outside cluster?"
                  : observability.data.collectorError}
              </p>
            )}
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">
              Agent Runs
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-xl font-semibold">
              {(observability.data?.agentRunsTotal || 0).toLocaleString()}
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">
              Token Usage
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-sm font-mono">
              {(observability.data?.inputTokensTotal || 0).toLocaleString()} in
              / {(observability.data?.outputTokensTotal || 0).toLocaleString()}{" "}
              out
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">
              Tool Calls
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-xl font-semibold">
              {(observability.data?.toolInvocations || 0).toLocaleString()}
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-muted-foreground">
              Est. Spend
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p
              className="text-xl font-semibold"
              title={
                spend.anySimulated
                  ? "Estimated spend for the runs listed below — includes simulated rates"
                  : "Estimated spend for the runs listed below"
              }
            >
              {spend.count > 0 ? formatUsd(spend.totalMicro) : "—"}
            </p>
          </CardContent>
        </Card>
      </div>

      {observability.data?.inputByModel?.length ? (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Model Token Breakdown</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="grid gap-2 md:grid-cols-2">
              {observability.data.inputByModel.slice(0, 6).map((row) => {
                const out =
                  observability.data?.outputByModel?.find(
                    (x) => x.label === row.label,
                  )?.value || 0;
                return (
                  <div key={row.label} className="rounded border p-3">
                    <p className="text-xs text-muted-foreground">{row.label}</p>
                    <p className="font-mono text-sm">
                      {Math.round(row.value).toLocaleString()} in /{" "}
                      {Math.round(out).toLocaleString()} out
                    </p>
                  </div>
                );
              })}
            </div>
          </CardContent>
        </Card>
      ) : null}

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 5 }).map((_, i) => (
            <Skeleton key={i} className="h-12 w-full" />
          ))}
        </div>
      ) : filtered.length === 0 ? (
        <div className="py-12 text-center space-y-3">
          <p className="text-muted-foreground">
            {search ? "No runs match your search" : "No runs yet"}
          </p>
          {!search && (
            <p className="text-sm text-muted-foreground">
              Runs are created when you dispatch a task to an{" "}
              <Link
                to="/agents"
                className="text-blue-400 hover:text-blue-300"
              >
                Instance
              </Link>
              , or automatically via a{" "}
              <Link
                to="/schedules"
                className="text-blue-400 hover:text-blue-300"
              >
                Schedule
              </Link>
              .
            </p>
          )}
        </div>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Instance</TableHead>
              <TableHead>Task</TableHead>
              <TableHead>Phase</TableHead>
              <TableHead>Tokens</TableHead>
              <TableHead>Est. Spend</TableHead>
              <TableHead>Age</TableHead>
              <TableHead className="w-20" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {filtered.map((run) => (
              <TableRow key={run.metadata.name}>
                <TableCell className="font-mono text-xs">
                  <Link
                    to={`/runs/${run.metadata.name}`}
                    className="hover:text-primary flex items-center gap-1"
                  >
                    {isUnseen(run) && (
                      <span
                        className="h-2 w-2 rounded-full bg-blue-500 shrink-0"
                        title="New"
                      />
                    )}
                    {truncate(run.metadata.name, 32)}
                    <ExternalLink className="h-3 w-3 opacity-50" />
                  </Link>
                </TableCell>
                <TableCell className="text-sm">
                  <Link
                    to={`/agents/${run.spec.agentRef}`}
                    className="hover:text-primary"
                  >
                    {run.spec.agentRef}
                  </Link>
                </TableCell>
                <TableCell className="max-w-xs text-sm text-muted-foreground">
                  {truncate(run.spec.task, 60)}
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-1.5">
                    <StatusBadge phase={run.status?.phase} />
                    {isAwaitingGate(run) && (
                      <span
                        data-testid="gate-pending-badge"
                        className="inline-flex items-center gap-1 rounded-full border border-amber-500/40 bg-amber-500/10 px-2 py-0.5 text-[10px] font-medium text-amber-400"
                        title="Awaiting gate approval"
                      >
                        <ShieldAlert className="h-3 w-3" />
                        Approval
                      </span>
                    )}
                  </div>
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {run.status?.tokenUsage
                    ? `${run.status.tokenUsage.totalTokens.toLocaleString()}`
                    : "—"}
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {(() => {
                    const est = effectiveCost(run);
                    return est ? (
                      <span title={costTooltip(est)}>
                        {formatUsd(est.amountMicro)}
                      </span>
                    ) : null;
                  })()}
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {formatAge(run.metadata.creationTimestamp)}
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-1">
                    {isAwaitingGate(run) && (
                      <>
                        <Button
                          variant="ghost"
                          size="icon"
                          data-testid="gate-approve-btn"
                          onClick={() =>
                            gateVerdict.mutate({
                              name: run.metadata.name,
                              data: {
                                action: "approve",
                                reason: "manual-approval",
                              },
                            })
                          }
                          disabled={gateVerdict.isPending}
                          title="Approve"
                        >
                          <Check className="h-4 w-4 text-green-400" />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          data-testid="gate-reject-btn"
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
                          title="Reject"
                        >
                          <X className="h-4 w-4 text-red-400" />
                        </Button>
                      </>
                    )}
                    <Button
                      variant="ghost"
                      size="icon"
                      onClick={() => deleteRun.mutate(run.metadata.name)}
                      disabled={deleteRun.isPending}
                      title="Delete"
                    >
                      <Trash2 className="h-4 w-4 text-destructive" />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}
