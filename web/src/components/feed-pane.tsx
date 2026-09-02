import { useState, useRef, useEffect, useMemo } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import {
  useAgents,
  useCreateRun,
  useRuns,
  useRuntimes,
  useHarnessSessions,
  useCreateHarnessSession,
  useSetHarnessSessionState,
} from "@/hooks/use-api";
import { useWebSocket, type StreamEvent } from "@/hooks/use-websocket";
import { HarnessSessionChatDialog } from "@/components/harness-session-dialog";
import type { Agent, AgentRuntime, HarnessSession } from "@/lib/api";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  MessageSquare,
  X,
  Send,
  ChevronLeft,
  ChevronRight,
  Bot,
  User,
  Loader2,
  Radio,
  ShieldAlert,
  Play,
  Square,
} from "lucide-react";
import { cn, taskText } from "@/lib/utils";

// ── Feed pane (right slide-out) ──────────────────────────────────────────────

type FeedMode = "runs" | "persistent";

function isPersistentRuntime(runtime: AgentRuntime | undefined) {
  return (
    runtime?.spec.contractVersion === "v1alpha2" &&
    runtime.spec.session?.protocol === "openai-chat"
  );
}

function defaultChatSessionName(agentName: string) {
  return `${agentName.slice(0, 57).replace(/-+$/, "")}-chat`;
}

function sessionFor(
  agent: Agent,
  runtime: AgentRuntime,
  sessions: HarnessSession[],
) {
  return sessions.find(
    (session) =>
      session.spec.agentRef === agent.metadata.name &&
      session.spec.runtimeRef === runtime.metadata.name,
  );
}

export function FeedPane({
  open,
  onToggle,
}: {
  open: boolean;
  onToggle: () => void;
}) {
  const { data: instances } = useAgents();
  const { data: runs } = useRuns();
  const { data: runtimes } = useRuntimes();
  const { data: harnessSessions } = useHarnessSessions();
  const { events } = useWebSocket();
  const createRun = useCreateRun();
  const createHarnessSession = useCreateHarnessSession();
  const setHarnessSessionState = useSetHarnessSessionState();

  const [mode, setMode] = useState<FeedMode>("runs");
  const [activeTab, setActiveTab] = useState<string>("");
  const [message, setMessage] = useState("");
  const [chattingSession, setChattingSession] = useState<HarnessSession | null>(null);
  const scrollRef = useRef<HTMLDivElement>(null);

  const runtimeFor = (inst: Agent) =>
    runtimes?.find((runtime) => runtime.metadata.name === inst.spec.runtimeRef);

  // Split agents by execution mode. Session-only (v1alpha2 openai-chat) harnesses
  // run as a persistent HarnessSession; everything else dispatches a one-shot AgentRun.
  const oneShotInstances = useMemo(
    () => (instances || []).filter((inst) => !isPersistentRuntime(runtimeFor(inst))),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [instances, runtimes],
  );

  const persistentAgents = useMemo(
    () => (instances || []).filter((inst) => isPersistentRuntime(runtimeFor(inst))),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [instances, runtimes],
  );

  // Filter to running / ready instances (one-shot tab only)
  const activeInstances = useMemo(
    () =>
      oneShotInstances.filter((inst) => {
        const phase = inst.status?.phase?.toLowerCase();
        return phase === "running" || phase === "ready" || phase === "active";
      }),
    [oneShotInstances],
  );

  // All instances for fallback
  const allInstances = oneShotInstances;

  // Use active instances if available, otherwise show all
  const tabInstances =
    activeInstances.length > 0 ? activeInstances : allInstances;

  // Auto-select first tab
  useEffect(() => {
    if (!activeTab && tabInstances.length > 0) {
      setActiveTab(tabInstances[0].metadata.name);
    }
  }, [tabInstances, activeTab]);

  // Runs for the selected instance
  const instanceRuns = useMemo(
    () =>
      (runs || [])
        .filter((r) => r.spec.agentRef === activeTab)
        .sort((a, b) => {
          const ta = a.metadata.creationTimestamp || "";
          const tb = b.metadata.creationTimestamp || "";
          return ta.localeCompare(tb);
        }),
    [runs, activeTab],
  );

  // Events for the selected instance (filter by metadata)
  const instanceEvents = useMemo(
    () =>
      events.filter((e) => {
        // Try to match events to the active instance
        const meta = e.data as Record<string, unknown> | undefined;
        if (meta && typeof meta === "object") {
          if (
            (meta as Record<string, unknown>).agentRef === activeTab ||
            (meta as Record<string, unknown>).instance === activeTab
          ) {
            return true;
          }
        }
        // Also check if the event topic mentions the instance
        return false;
      }),
    [events, activeTab],
  );

  // Build a unified feed: runs + stream events for this instance
  const feed = useMemo(() => {
    const items: FeedItem[] = [];

    // Add runs as feed items
    for (const run of instanceRuns) {
      items.push({
        id: `run-task-${run.metadata.name}`,
        type: "user",
        text: taskText(run.spec.task),
        timestamp: run.metadata.creationTimestamp || "",
        meta: run.metadata.name,
      });
      if (run.status?.result) {
        items.push({
          id: `run-result-${run.metadata.name}`,
          type: "agent",
          text: run.status.result,
          timestamp:
            run.status.completedAt || run.metadata.creationTimestamp || "",
          meta: run.status.phase || "completed",
        });
      } else if (run.status?.error) {
        items.push({
          id: `run-error-${run.metadata.name}`,
          type: "error",
          text: run.status.error,
          timestamp:
            run.status.completedAt || run.metadata.creationTimestamp || "",
          meta: "failed",
        });
      } else if (
        run.status?.phase === "PostRunning" &&
        !run.status?.gateVerdict &&
        run.spec.lifecycle?.postRun?.some((h) => h.gate)
      ) {
        items.push({
          id: `run-gate-${run.metadata.name}`,
          type: "system",
          text: "Awaiting approval — response held by gate",
          timestamp:
            run.status?.startedAt || run.metadata.creationTimestamp || "",
          meta: "gate-pending",
        });
      } else if (run.status?.phase === "Running") {
        items.push({
          id: `run-pending-${run.metadata.name}`,
          type: "thinking",
          text: "Agent is working…",
          timestamp:
            run.status?.startedAt || run.metadata.creationTimestamp || "",
          meta: "running",
        });
      }
    }

    // Add filtered stream events
    for (const ev of instanceEvents) {
      // Handle stimulus delivery events.
      if (ev.topic === "ensemble.stimulus.delivered") {
        const meta = ev.data as Record<string, unknown> | undefined;
        const source =
          meta && typeof meta === "object"
            ? (meta as Record<string, string>).triggerSource || "auto"
            : "auto";
        items.push({
          id: `stimulus-${ev.timestamp}`,
          type: "system",
          text: `Stimulus prompt sent (${source})`,
          timestamp: ev.timestamp,
          meta: "stimulus",
        });
        continue;
      }

      const data = ev.data as Record<string, unknown> | undefined;
      const chunk =
        typeof data === "object" && data
          ? (data as Record<string, unknown>).content ||
            (data as Record<string, unknown>).chunk ||
            (data as Record<string, unknown>).message
          : null;
      if (chunk && typeof chunk === "string") {
        items.push({
          id: `stream-${ev.timestamp}`,
          type: "stream",
          text: chunk,
          timestamp: ev.timestamp,
          meta: ev.topic,
        });
      }
    }

    // Sort by timestamp
    items.sort((a, b) => a.timestamp.localeCompare(b.timestamp));
    return items;
  }, [instanceRuns, instanceEvents]);

  // Auto-scroll on new items
  useEffect(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [feed.length]);

  // Send a task
  function handleSend() {
    const task = message.trim();
    if (!task || !activeTab) return;
    createRun.mutate({
      agentRef: activeTab,
      task,
    });
    setMessage("");
  }

  function handleKeyDown(e: React.KeyboardEvent) {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      handleSend();
    }
  }

  function startSession(agent: Agent, runtime: AgentRuntime) {
    createHarnessSession.mutate({
      name: defaultChatSessionName(agent.metadata.name),
      agentRef: agent.metadata.name,
      runtimeRef: runtime.metadata.name,
    });
  }

  // ── Toggle button (always visible) ──────────────────────────────────────

  if (!open) {
    return (
      <button
        onClick={onToggle}
        className="fixed right-0 top-1/2 -translate-y-1/2 z-40 flex items-center gap-1 rounded-l-lg border border-r-0 border-border/50 bg-card px-1.5 py-3 text-muted-foreground hover:text-blue-400 hover:bg-blue-500/5 transition-colors shadow-lg"
        title="Open feed"
      >
        <ChevronLeft className="h-4 w-4" />
        <MessageSquare className="h-4 w-4" />
      </button>
    );
  }

  return (
    <aside className="fixed right-0 top-0 z-50 flex h-full w-96 flex-col border-l border-border/50 bg-card shadow-2xl shadow-black/30">
      {/* Header */}
      <div className="flex h-14 items-center justify-between border-b border-border/50 px-3">
        <div className="flex items-center gap-2">
          <Radio className="h-4 w-4 text-blue-400" />
          <span className="text-sm font-semibold">Feed</span>
        </div>
        <Button
          size="sm"
          variant="ghost"
          className="h-7 w-7 p-0"
          onClick={onToggle}
        >
          <ChevronRight className="h-4 w-4" />
        </Button>
      </div>

      {/* Mode switch */}
      <div className="flex gap-1 border-b border-border/50 px-3 py-2">
        <button
          onClick={() => setMode("runs")}
          className={cn(
            "flex-1 rounded-md px-2 py-1 text-xs font-medium transition-colors",
            mode === "runs"
              ? "bg-muted text-foreground"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          Runs
        </button>
        <button
          onClick={() => setMode("persistent")}
          className={cn(
            "flex-1 rounded-md px-2 py-1 text-xs font-medium transition-colors",
            mode === "persistent"
              ? "bg-muted text-foreground"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          Persistent
        </button>
      </div>

      {mode === "persistent" ? (
        persistentAgents.length === 0 ? (
          <div className="flex flex-1 flex-col items-center justify-center p-6 text-center">
            <Bot className="h-10 w-10 text-muted-foreground/40 mb-3" />
            <p className="text-sm font-medium text-muted-foreground">
              No persistent agents
            </p>
            <p className="text-xs text-muted-foreground/70 mt-1">
              Agents backed by a v1alpha2 openai-chat harness appear here as
              durable conversations.
            </p>
          </div>
        ) : (
          <ScrollArea className="flex-1">
            <div className="space-y-2 p-3">
              {persistentAgents.map((agent) => {
                const runtime = runtimeFor(agent)!;
                const session = sessionFor(
                  agent,
                  runtime,
                  harnessSessions || [],
                );
                return (
                  <PersistentAgentItem
                    key={agent.metadata.name}
                    agent={agent}
                    runtime={runtime}
                    session={session}
                    onStart={() => startSession(agent, runtime)}
                    onOpen={() => session && setChattingSession(session)}
                    onResume={() =>
                      session &&
                      setHarnessSessionState.mutate({
                        name: session.metadata.name,
                        desiredState: "running",
                      })
                    }
                    onStop={() =>
                      session &&
                      setHarnessSessionState.mutate({
                        name: session.metadata.name,
                        desiredState: "stopped",
                      })
                    }
                    pending={createHarnessSession.isPending}
                  />
                );
              })}
            </div>
          </ScrollArea>
        )
      ) : tabInstances.length > 0 ? (
        <>
          <div className="border-b border-border/50 px-3 py-2">
            <Select value={activeTab} onValueChange={setActiveTab}>
              <SelectTrigger className="h-8 text-xs bg-transparent">
                <SelectValue placeholder="Select agent…" />
              </SelectTrigger>
              <SelectContent>
                {tabInstances.map((inst) => (
                  <SelectItem
                    key={inst.metadata.name}
                    value={inst.metadata.name}
                    className="text-xs"
                  >
                    <span className="flex items-center gap-2">
                      <span
                        className={cn(
                          "h-1.5 w-1.5 rounded-full",
                          inst.status?.phase?.toLowerCase() === "running" ||
                            inst.status?.phase?.toLowerCase() === "ready" ||
                            inst.status?.phase?.toLowerCase() === "active"
                            ? "bg-emerald-400"
                            : "bg-muted-foreground",
                        )}
                      />
                      {inst.metadata.name}
                    </span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {/* Feed content */}
          <ScrollArea className="flex-1" ref={scrollRef}>
            <div className="space-y-3 p-3">
              {feed.length === 0 ? (
                <div className="flex flex-col items-center justify-center py-12 text-center">
                  <Bot className="h-8 w-8 text-muted-foreground/50 mb-2" />
                  <p className="text-sm text-muted-foreground">
                    No activity yet
                  </p>
                  <p className="text-xs text-muted-foreground/70 mt-1">
                    Send a task to get started
                  </p>
                </div>
              ) : (
                feed.map((item) => <FeedBubble key={item.id} item={item} />)
              )}
            </div>
          </ScrollArea>

          {/* Input */}
          <div className="border-t border-border/50 p-3">
            <div className="flex items-center gap-2">
              <Input
                value={message}
                onChange={(e) => setMessage(e.target.value)}
                onKeyDown={handleKeyDown}
                placeholder={`Task for ${activeTab}…`}
                className="flex-1 h-8 text-sm"
                disabled={createRun.isPending}
              />
              <Button
                size="sm"
                className="h-8 w-8 p-0 bg-primary hover:bg-primary/90 text-primary-foreground border-0"
                onClick={handleSend}
                disabled={!message.trim() || createRun.isPending}
              >
                {createRun.isPending ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                ) : (
                  <Send className="h-3.5 w-3.5" />
                )}
              </Button>
            </div>
          </div>
        </>
      ) : (
        <div className="flex flex-1 flex-col items-center justify-center p-6 text-center">
          <Bot className="h-10 w-10 text-muted-foreground/40 mb-3" />
          <p className="text-sm font-medium text-muted-foreground">
            No one-shot agents available
          </p>
          <p className="text-xs text-muted-foreground/70 mt-1">
            Create an instance or enable an ensemble to get started
          </p>
        </div>
      )}

      {chattingSession && (
        <HarnessSessionChatDialog
          open={true}
          onOpenChange={(open) => {
            if (!open) setChattingSession(null);
          }}
          session={chattingSession}
        />
      )}
    </aside>
  );
}

// ── Persistent agent item ────────────────────────────────────────────────────

function PersistentAgentItem({
  agent,
  runtime,
  session,
  onStart,
  onOpen,
  onResume,
  onStop,
  pending,
}: {
  agent: Agent;
  runtime: AgentRuntime;
  session?: HarnessSession;
  onStart: () => void;
  onOpen: () => void;
  onResume: () => void;
  onStop: () => void;
  pending: boolean;
}) {
  const phase = session?.status?.phase;
  const stopped =
    session?.spec.desiredState === "stopped" || phase === "Draining";
  const failed = phase === "Failed";

  return (
    <div className="rounded-lg border p-3 text-sm">
      <div className="flex items-center justify-between gap-2">
        <div className="min-w-0">
          <p className="truncate font-mono text-xs">{agent.metadata.name}</p>
          <p className="text-[10px] text-muted-foreground">
            {runtime.metadata.name}
          </p>
        </div>
        {session && (
          <Badge
            variant={phase === "Ready" ? "default" : "outline"}
            className="gap-1 shrink-0"
          >
            {phase === "Ready"
              ? "Ready"
              : phase === "Failed"
                ? "Failed"
                : stopped
                  ? "Stopped"
                  : "Starting"}
          </Badge>
        )}
      </div>

      <div className="mt-2 flex flex-wrap gap-1.5">
        {!session ? (
          <Button
            size="sm"
            variant="outline"
            onClick={onStart}
            disabled={pending}
            className="h-7 text-xs"
          >
            {pending ? (
              <Loader2 className="mr-1 h-3 w-3 animate-spin" />
            ) : (
              <Play className="mr-1 h-3 w-3" />
            )}
            Start
          </Button>
        ) : phase === "Ready" ? (
          <>
            <Button size="sm" onClick={onOpen} className="h-7 text-xs">
              <MessageSquare className="mr-1 h-3 w-3" />Open
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={onStop}
              className="h-7 text-xs"
            >
              <Square className="mr-1 h-3 w-3" />Stop
            </Button>
          </>
        ) : stopped ? (
          <Button
            size="sm"
            variant="outline"
            onClick={onResume}
            className="h-7 text-xs"
          >
            <Play className="mr-1 h-3 w-3" />Resume
          </Button>
        ) : failed ? (
          <Button
            size="sm"
            variant="outline"
            onClick={onResume}
            className="h-7 text-xs"
          >
            Retry
          </Button>
        ) : (
          <span className="flex items-center gap-1.5 text-[10px] text-muted-foreground">
            <Loader2 className="h-3 w-3 animate-spin" />
            {session.metadata.name}
          </span>
        )}
      </div>

      {session && (failed || stopped) && (
        <p className="mt-1.5 text-[10px] text-muted-foreground">
          {failed
            ? session.status?.conditions?.find((c) => c.type === "Ready")
                ?.message || "Session failed to start"
            : `${session.metadata.name} · stopped`}
        </p>
      )}
    </div>
  );
}

// ── Feed item types ──────────────────────────────────────────────────────────

interface FeedItem {
  id: string;
  type: "user" | "agent" | "error" | "thinking" | "stream" | "system";
  text: string;
  timestamp: string;
  meta?: string;
}

function FeedBubble({ item }: { item: FeedItem }) {
  const isUser = item.type === "user";

  return (
    <div className={cn("flex gap-2", isUser ? "flex-row-reverse" : "flex-row")}>
      {/* Avatar */}
      <div
        className={cn(
          "flex h-6 w-6 shrink-0 items-center justify-center rounded-full text-xs",
          isUser
            ? "bg-blue-500/20 text-blue-400"
            : item.type === "error"
              ? "bg-red-500/20 text-red-400"
              : item.type === "thinking"
                ? "bg-amber-500/20 text-amber-400"
                : item.type === "system"
                  ? "bg-amber-500/20 text-amber-400"
                  : "bg-emerald-500/20 text-emerald-400",
        )}
      >
        {isUser ? <User className="h-3 w-3" /> : <Bot className="h-3 w-3" />}
      </div>

      {/* Bubble */}
      <div
        className={cn(
          "max-w-[80%] rounded-lg px-3 py-2 text-xs",
          isUser
            ? "bg-blue-500/15 text-blue-100 border border-blue-500/20"
            : item.type === "error"
              ? "bg-red-500/10 text-red-300 border border-red-500/20"
              : item.type === "thinking"
                ? "bg-amber-500/10 text-amber-300 border border-amber-500/20"
                : item.type === "system"
                  ? "bg-amber-500/10 text-amber-300 border border-amber-500/30"
                  : "bg-muted/50 text-foreground border border-border/50",
        )}
      >
        {item.type === "system" ? (
          <span className="flex items-center gap-1.5">
            <ShieldAlert className="h-3 w-3" />
            {item.text}
          </span>
        ) : item.type === "thinking" ? (
          <span className="flex items-center gap-1.5">
            <Loader2 className="h-3 w-3 animate-spin" />
            {item.text}
          </span>
        ) : isUser ? (
          <div className="prose prose-sm prose-invert prose-feed break-word max-w-none">
            <ReactMarkdown remarkPlugins={[remarkGfm]}>
              {item.text}
            </ReactMarkdown>
          </div>
        ) : (
          <div className="prose prose-sm prose-invert prose-feed break-word max-w-none">
            <ReactMarkdown remarkPlugins={[remarkGfm]}>
              {item.text}
            </ReactMarkdown>
          </div>
        )}
        {item.meta && (
          <p className="mt-1 text-[10px] opacity-50">{item.meta}</p>
        )}
      </div>
    </div>
  );
}
