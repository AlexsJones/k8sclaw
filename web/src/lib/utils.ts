import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";
import type { AgentRun, AgentRunTask, CostEstimate } from "@/lib/api";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

export function formatAge(dateStr: string | undefined): string {
  if (!dateStr) return "—";
  const date = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffSec = Math.floor(diffMs / 1000);
  if (diffSec < 60) return `${diffSec}s`;
  const diffMin = Math.floor(diffSec / 60);
  if (diffMin < 60) return `${diffMin}m`;
  const diffHr = Math.floor(diffMin / 60);
  if (diffHr < 24) return `${diffHr}h`;
  const diffDay = Math.floor(diffHr / 24);
  return `${diffDay}d`;
}

/** Format micro-USD as dollars: 4 decimal places under $1, 2 above. */
export function formatUsd(micro: number): string {
  const dollars = micro / 1e6;
  return `$${dollars.toFixed(dollars < 1 ? 4 : 2)}`;
}

/** The single spend number to show for a run: the real estimate from the
 *  cluster price table when present, else the simulated estimate, else null. */
export function effectiveCost(run: AgentRun): CostEstimate | null {
  return run.status?.costEstimate ?? run.simulatedCostEstimate ?? null;
}

/** Tooltip explaining where a cost estimate came from.
 *  A run whose retry chain lives on one CR (in-pod gate resume) carries the
 *  whole chain's spend; a successor-clone chain carries one attempt per run. */
export function costTooltip(est: CostEstimate, attempts = 0): string {
  const key = est.priceKey ? ` (${est.priceKey})` : "";
  const scope =
    attempts > 1
      ? ` Covers all ${attempts} attempts of this run.`
      : " Covers this run's attempt only.";
  return (
    (est.source === "simulated"
      ? `Based on user-defined simulated rates${key}.`
      : `Estimated from the cluster price table${key}.`) + scope
  );
}

/** Sum the effective estimates of a set of runs in integer micro-USD. */
export function sumEffectiveCosts(runs: AgentRun[]): {
  totalMicro: number;
  anySimulated: boolean;
  count: number;
} {
  let totalMicro = 0;
  let anySimulated = false;
  let count = 0;
  for (const run of runs) {
    const est = effectiveCost(run);
    if (!est) continue;
    totalMicro += est.amountMicro;
    if (est.source === "simulated") anySimulated = true;
    count++;
  }
  return { totalMicro, anySimulated, count };
}

/**
 * Display text for a run's task.
 *
 * `spec.task` is either the prompt string or an object describing an
 * orchestration mode (`harness`, `sidecar-driven`). The object form carries
 * the real task text in `parameters.prompt`, so that is what a reader wants to
 * see; a mode with no prompt falls back to naming the mode. Every consumer
 * goes through here — rendering the object itself throws React error #31 and
 * takes the whole app down with it.
 */
export function taskText(task: AgentRunTask | undefined | null): string {
  if (!task) return "";
  if (typeof task === "string") return task;
  if (task.parameters?.prompt) return task.parameters.prompt;
  const mode = task.mode || "unknown mode";
  return task.tool ? `${mode}: ${task.tool}` : mode;
}

export function truncate(s: string, max: number): string {
  if (s.length <= max) return s;
  return s.slice(0, max - 1) + "…";
}

export function phaseColor(phase: string | undefined): string {
  switch (phase?.toLowerCase()) {
    case "running":
      return "phase-running";
    case "succeeded":
    case "ready":
      return "phase-succeeded";
    case "failed":
    case "error":
      return "phase-failed";
    case "skipped":
      return "phase-skipped";
    case "pending":
      return "phase-pending";
    case "downloading":
    case "loading":
    case "placing":
      return "phase-running";
    case "serving":
      return "phase-serving";
    case "postrunning":
      return "phase-postrunning";
    case "awaitinggate":
      return "phase-awaitinggate";
    default:
      return "bg-secondary text-muted-foreground";
  }
}
