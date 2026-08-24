import { useEffect, useRef } from "react";
import { toast } from "sonner";
import type { AgentRun } from "@/lib/api";
import { taskText } from "@/lib/utils";
import { useRuns } from "./use-api";

/**
 * Watches for run phase transitions and fires toast notifications.
 * Mount this once near the app root (e.g. in Layout).
 *
 * Tracks each run's last-known phase. When a run transitions to a
 * terminal state (Succeeded / Failed), a toast is shown.
 */
export function useRunNotifications() {
  const { data: runs } = useRuns();
  const phasesRef = useRef<Map<string, string>>(new Map());
  const initializedRef = useRef(false);

  useEffect(() => {
    if (!runs) return;

    // On first load, snapshot all current phases without toasting —
    // we only want to notify about transitions that happen while the
    // user is actively using the app.
    if (!initializedRef.current) {
      initializedRef.current = true;
      for (const run of runs) {
        phasesRef.current.set(run.metadata.name, run.status?.phase || "");
      }
      return;
    }

    for (const run of runs) {
      const name = run.metadata.name;
      const phase = run.status?.phase || "";
      const prev = phasesRef.current.get(name);

      // New run appeared that we haven't seen before.
      if (prev === undefined) {
        phasesRef.current.set(name, phase);
        if (phase === "Running" || phase === "Pending") {
          toast.info(`Run started: ${shortName(name)}`, {
            description: truncateTask(run),
            duration: 4000,
          });
        }
        continue;
      }

      // Phase hasn't changed.
      if (prev === phase) continue;

      // Phase changed — update and notify.
      phasesRef.current.set(name, phase);

      if (
        phase === "PostRunning" &&
        run.spec.lifecycle?.postRun?.some((h) => h.gate)
      ) {
        toast.warning(`Approval required: ${shortName(name)}`, {
          description:
            "A gate hook is holding this run's response. Review and approve or reject.",
          duration: 10000,
        });
      } else if (phase === "Succeeded") {
        toast.success(`Run succeeded: ${shortName(name)}`, {
          description: truncateTask(run),
          duration: 5000,
        });
      } else if (phase === "Failed") {
        toast.error(`Run failed: ${shortName(name)}`, {
          description: run.status?.error
            ? run.status.error.slice(0, 120)
            : truncateTask(run),
          duration: 8000,
        });
      } else if (phase === "Skipped") {
        toast.info(`Run skipped: ${shortName(name)}`, {
          description: run.status?.result
            ? run.status.result.slice(0, 120)
            : "A preRun hook skipped this run (no work to do).",
          duration: 5000,
        });
      }
    }

    // Clean up runs that no longer exist (deleted).
    const currentNames = new Set(runs.map((r) => r.metadata.name));
    for (const name of phasesRef.current.keys()) {
      if (!currentNames.has(name)) {
        phasesRef.current.delete(name);
      }
    }
  }, [runs]);
}

function shortName(name: string): string {
  return name.length > 40 ? name.slice(0, 37) + "..." : name;
}

function truncateTask(run: AgentRun): string {
  const task = taskText(run.spec.task);
  return task.length > 80 ? task.slice(0, 77) + "..." : task;
}
