import { useState, useRef, useEffect, useMemo } from "react";
import { useInstances, useCreateRun, useRuns } from "@/hooks/use-api";
import { useWebSocket, type StreamEvent } from "@/hooks/use-websocket";
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
} from "lucide-react";
import { cn } from "@/lib/utils";

// ── Feed pane (right slide-out) ──────────────────────────────────────────────

export function FeedPane({
  open,
  onToggle,
}: {
  open: boolean;
  onToggle: () => void;
}) {
  const { data: instances } = useInstances();
  const { data: runs } = useRuns();
  const { events } = useWebSocket();
  const createRun = useCreateRun();

  const [activeTab, setActiveTab] = useState<string>("");
  const [message, setMessage] = useState("");
  const scrollRef = useRef<HTMLDivElement>(null);

  // Filter to running / ready instances
  const activeInstances = useMemo(
    () =>
      (instances || []).filter((inst) => {
        const phase = inst.status?.phase?.toLowerCase();
        return phase === "running" || phase === "ready" || phase === "active";
      }),
    [instances]
  );

  // All instances for fallback
  const allInstances = instances || [];

  // Use active instances if available, otherwise show all
  const tabInstances = activeInstances.length > 0 ? activeInstances : allInstances;

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
        .filter((r) => r.spec.instanceRef === activeTab)
        .sort((a, b) => {
          const ta = a.metadata.creationTimestamp || "";
          const tb = b.metadata.creationTimestamp || "";
          return ta.localeCompare(tb);
        }),
    [runs, activeTab]
  );

  // Events for the selected instance (filter by metadata)
  const instanceEvents = useMemo(
    () =>
      events.filter((e) => {
        // Try to match events to the active instance
        const meta = e.data as Record<string, unknown> | undefined;
        if (meta && typeof meta === "object") {
          if (
            (meta as Record<string, unknown>).instanceRef === activeTab ||
            (meta as Record<string, unknown>).instance === activeTab
          ) {
            return true;
          }
        }
        // Also check if the event topic mentions the instance
        return false;
      }),
    [events, activeTab]
  );

  // Build a unified feed: runs + stream events for this instance
  const feed = useMemo(() => {
    const items: FeedItem[] = [];

    // Add runs as feed items
    for (const run of instanceRuns) {
      items.push({
        id: `run-task-${run.metadata.name}`,
        type: "user",
        text: run.spec.task,
        timestamp: run.metadata.creationTimestamp || "",
        meta: run.metadata.name,
      });
      if (run.status?.result) {
        items.push({
          id: `run-result-${run.metadata.name}`,
          type: "agent",
          text: run.status.result,
          timestamp: run.status.completedAt || run.metadata.creationTimestamp || "",
          meta: run.status.phase || "completed",
        });
      } else if (run.status?.error) {
        items.push({
          id: `run-error-${run.metadata.name}`,
          type: "error",
          text: run.status.error,
          timestamp: run.status.completedAt || run.metadata.creationTimestamp || "",
          meta: "failed",
        });
      } else if (run.status?.phase === "Running") {
        items.push({
          id: `run-pending-${run.metadata.name}`,
          type: "thinking",
          text: "Agent is working…",
          timestamp: run.status?.startedAt || run.metadata.creationTimestamp || "",
          meta: "running",
        });
      }
    }

    // Add filtered stream events
    for (const ev of instanceEvents) {
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
      instanceRef: activeTab,
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

  // ── Toggle button (always visible) ──────────────────────────────────────

  if (!open) {
    return (
      <button
        onClick={onToggle}
        className="fixed right-0 top-1/2 -translate-y-1/2 z-40 flex items-center gap-1 rounded-l-lg border border-r-0 border-border/50 bg-card px-1.5 py-3 text-muted-foreground hover:text-indigo-400 hover:bg-indigo-500/5 transition-colors shadow-lg"
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
          <Radio className="h-4 w-4 text-indigo-400" />
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

      {/* Instance selector */}
      {tabInstances.length > 0 ? (
        <>
          <div className="border-b border-border/50 px-3 py-2">
            <Select value={activeTab} onValueChange={setActiveTab}>
              <SelectTrigger className="h-8 text-xs bg-transparent">
                <SelectValue placeholder="Select instance…" />
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
                            : "bg-muted-foreground"
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
                className="h-8 w-8 p-0 bg-gradient-to-r from-indigo-500 to-purple-600 hover:from-indigo-600 hover:to-purple-700 text-white border-0"
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
            No instances available
          </p>
          <p className="text-xs text-muted-foreground/70 mt-1">
            Create an instance or enable a persona pack to get started
          </p>
        </div>
      )}
    </aside>
  );
}

// ── Feed item types ──────────────────────────────────────────────────────────

interface FeedItem {
  id: string;
  type: "user" | "agent" | "error" | "thinking" | "stream";
  text: string;
  timestamp: string;
  meta?: string;
}

function FeedBubble({ item }: { item: FeedItem }) {
  const isUser = item.type === "user";

  return (
    <div
      className={cn("flex gap-2", isUser ? "flex-row-reverse" : "flex-row")}
    >
      {/* Avatar */}
      <div
        className={cn(
          "flex h-6 w-6 shrink-0 items-center justify-center rounded-full text-xs",
          isUser
            ? "bg-indigo-500/20 text-indigo-400"
            : item.type === "error"
            ? "bg-red-500/20 text-red-400"
            : item.type === "thinking"
            ? "bg-amber-500/20 text-amber-400"
            : "bg-emerald-500/20 text-emerald-400"
        )}
      >
        {isUser ? (
          <User className="h-3 w-3" />
        ) : (
          <Bot className="h-3 w-3" />
        )}
      </div>

      {/* Bubble */}
      <div
        className={cn(
          "max-w-[80%] rounded-lg px-3 py-2 text-xs",
          isUser
            ? "bg-indigo-500/15 text-indigo-100 border border-indigo-500/20"
            : item.type === "error"
            ? "bg-red-500/10 text-red-300 border border-red-500/20"
            : item.type === "thinking"
            ? "bg-amber-500/10 text-amber-300 border border-amber-500/20"
            : "bg-muted/50 text-foreground border border-border/50"
        )}
      >
        {item.type === "thinking" ? (
          <span className="flex items-center gap-1.5">
            <Loader2 className="h-3 w-3 animate-spin" />
            {item.text}
          </span>
        ) : (
          <p className="whitespace-pre-wrap break-words">{item.text}</p>
        )}
        {item.meta && (
          <p className="mt-1 text-[10px] opacity-50">{item.meta}</p>
        )}
      </div>
    </div>
  );
}
