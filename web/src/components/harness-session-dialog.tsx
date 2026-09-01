import { useEffect, useMemo, useRef, useState } from "react";
import { Bot, Loader2, Pause, Send, TriangleAlert } from "lucide-react";
import type { Agent, AgentRuntime, HarnessSession } from "@/lib/api";
import { getNamespace } from "@/lib/api";
import { useCreateHarnessSession, useHarnessSessionChat, useSetHarnessSessionState } from "@/hooks/use-api";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";

type TranscriptTurn = { role: "user" | "assistant"; content: string };

function transcriptStorageKey(session: HarnessSession) {
  return `sympozium.harness-session.transcript.${getNamespace()}.${session.metadata.name}`;
}

function loadTranscript(session: HarnessSession): TranscriptTurn[] {
  try {
    const value = localStorage.getItem(transcriptStorageKey(session));
    if (!value) return [];
    const parsed: unknown = JSON.parse(value);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((turn): turn is TranscriptTurn =>
      !!turn && typeof turn === "object" &&
      ((turn as TranscriptTurn).role === "user" || (turn as TranscriptTurn).role === "assistant") &&
      typeof (turn as TranscriptTurn).content === "string",
    ).slice(-200);
  } catch {
    return [];
  }
}

export function StartHarnessSessionDialog({ open, onOpenChange, runtime, agents }: { open: boolean; onOpenChange: (open: boolean) => void; runtime: AgentRuntime; agents: Agent[] }) {
  const create = useCreateHarnessSession();
  const [agentRef, setAgentRef] = useState(agents[0]?.metadata.name || "");
  const [name, setName] = useState("");

  function submit() {
    if (!agentRef || !name.trim()) return;
    create.mutate({ name: name.trim(), agentRef, runtimeRef: runtime.metadata.name }, { onSuccess: () => onOpenChange(false) });
  }

  return <Dialog open={open} onOpenChange={onOpenChange}><DialogContent>
    <DialogHeader><DialogTitle>Start interactive session</DialogTitle><DialogDescription>Creates one private, persistent pod for this Agent. It is reached only through Sympozium; it is not an exposed service.</DialogDescription></DialogHeader>
    <div className="space-y-3">
      <div><label className="text-sm font-medium">Agent</label><Select value={agentRef} onValueChange={setAgentRef}><SelectTrigger className="mt-1"><SelectValue placeholder="Choose an Agent" /></SelectTrigger><SelectContent>{agents.map((agent) => <SelectItem key={agent.metadata.name} value={agent.metadata.name}>{agent.metadata.name}</SelectItem>)}</SelectContent></Select></div>
      <div><label className="text-sm font-medium">Session name</label><Input className="mt-1" value={name} onChange={(event) => setName(event.target.value)} placeholder="analyst-session" /></div>
      <p className="text-xs text-muted-foreground">The Agent’s credential allowlist still applies. One-shot AgentRuns remain separate and unchanged.</p>
      <Button className="w-full" disabled={!agentRef || !name.trim() || create.isPending} onClick={submit}>{create.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}Start session</Button>
    </div>
  </DialogContent></Dialog>;
}

export function HarnessSessionChatDialog({ session, open, onOpenChange }: { session: HarnessSession; open: boolean; onOpenChange: (open: boolean) => void }) {
  const chat = useHarnessSessionChat();
  const setSessionState = useSetHarnessSessionState();
  const [message, setMessage] = useState("");
  const [turns, setTurns] = useState<TranscriptTurn[]>(() => loadTranscript(session));
  const [failedMessage, setFailedMessage] = useState<string | null>(null);
  const transcriptKey = useMemo(() => transcriptStorageKey(session), [session.metadata.name]);
  const transcriptRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    setTurns(loadTranscript(session));
    setFailedMessage(null);
  }, [session.metadata.name]);

  useEffect(() => {
    try { localStorage.setItem(transcriptKey, JSON.stringify(turns.slice(-200))); } catch { /* Browser storage is an enhancement, never a chat failure. */ }
    transcriptRef.current?.scrollTo({ top: transcriptRef.current.scrollHeight, behavior: "smooth" });
  }, [transcriptKey, turns]);

  function send() {
    const content = message.trim(); if (!content || chat.isPending) return;
    setMessage(""); setFailedMessage(null); setTurns((current) => [...current, { role: "user", content }]);
    chat.mutate({ name: session.metadata.name, message: content }, {
      onSuccess: (response) => setTurns((current) => [...current, { role: "assistant", content: response.choices?.[0]?.message?.content || response.error?.message || "The harness returned no message." }]),
      onError: () => setFailedMessage(content),
    });
  }
  const ready = session.status?.phase === "Ready";
  return <Dialog open={open} onOpenChange={onOpenChange}><DialogContent className="max-w-2xl">
    <DialogHeader><DialogTitle className="flex items-center gap-2"><Bot className="h-5 w-5" />{session.metadata.name}</DialogTitle><DialogDescription>Persistent {session.spec.runtimeRef} session for Agent {session.spec.agentRef}. Conversation context stays in the private harness; this device retains the visible transcript across refreshes.</DialogDescription></DialogHeader>
    <div className="flex items-center justify-between rounded border px-3 py-2 text-xs"><span className={ready ? "text-emerald-500" : "text-amber-500"}>{ready ? "● Connected" : `● ${session.status?.phase || "Starting"}`}</span><Button variant="ghost" size="sm" onClick={() => setSessionState.mutate({ name: session.metadata.name, desiredState: "stopped" })} disabled={!ready || setSessionState.isPending}><Pause className="mr-1 h-3.5 w-3.5" />Stop session</Button></div>
    <div ref={transcriptRef} className="max-h-80 min-h-40 space-y-3 overflow-y-auto border bg-muted/30 p-3 text-sm">{turns.length === 0 ? <p className="text-muted-foreground">Ask the harness a question to begin.</p> : turns.map((turn, index) => <div key={index} className={turn.role === "user" ? "ml-8" : "mr-8"}><p className="mb-1 text-xs text-muted-foreground">{turn.role === "user" ? "You" : "Harness"}</p><p className="whitespace-pre-wrap rounded bg-background p-2">{turn.content}</p></div>)}</div>
    {failedMessage && <div className="flex items-center justify-between rounded border border-destructive/50 bg-destructive/5 p-2 text-sm"><span className="flex items-center gap-2"><TriangleAlert className="h-4 w-4 text-destructive" />Message was not delivered.</span><Button variant="outline" size="sm" onClick={() => { setMessage(failedMessage); setFailedMessage(null); }}>Retry</Button></div>}
    <div className="flex gap-2"><Textarea value={message} onChange={(event) => setMessage(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); send(); } }} placeholder={ready ? "Ask a question…" : "Waiting for the persistent session…"} disabled={!ready} /><Button onClick={send} disabled={!ready || !message.trim() || chat.isPending}>{chat.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : <Send className="h-4 w-4" />}</Button></div>
  </DialogContent></Dialog>;
}
