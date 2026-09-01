import { useState } from "react";
import { Bot, Loader2, Send } from "lucide-react";
import type { Agent, AgentRuntime, HarnessSession } from "@/lib/api";
import { useCreateHarnessSession, useHarnessSessionChat } from "@/hooks/use-api";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";

type TranscriptTurn = { role: "user" | "assistant"; content: string };

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
  const [message, setMessage] = useState("");
  const [turns, setTurns] = useState<TranscriptTurn[]>([]);
  function send() {
    const content = message.trim(); if (!content || chat.isPending) return;
    setMessage(""); setTurns((current) => [...current, { role: "user", content }]);
    chat.mutate({ name: session.metadata.name, message: content }, { onSuccess: (response) => setTurns((current) => [...current, { role: "assistant", content: response.choices?.[0]?.message?.content || response.error?.message || "The harness returned no message." }]) });
  }
  return <Dialog open={open} onOpenChange={onOpenChange}><DialogContent className="max-w-2xl">
    <DialogHeader><DialogTitle className="flex items-center gap-2"><Bot className="h-5 w-5" />{session.metadata.name}</DialogTitle><DialogDescription>Persistent {session.spec.runtimeRef} session for Agent {session.spec.agentRef}. The transcript shown here is browser-local; the adapter owns its session state.</DialogDescription></DialogHeader>
    <div className="max-h-80 min-h-40 space-y-3 overflow-y-auto border bg-muted/30 p-3 text-sm">{turns.length === 0 ? <p className="text-muted-foreground">Ask the harness a question to begin.</p> : turns.map((turn, index) => <div key={index} className={turn.role === "user" ? "ml-8" : "mr-8"}><p className="mb-1 text-xs text-muted-foreground">{turn.role === "user" ? "You" : "Harness"}</p><p className="whitespace-pre-wrap rounded bg-background p-2">{turn.content}</p></div>)}</div>
    <div className="flex gap-2"><Textarea value={message} onChange={(event) => setMessage(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); send(); } }} placeholder="Ask a question…" /><Button onClick={send} disabled={!message.trim() || chat.isPending}>{chat.isPending ? <Loader2 className="h-4 w-4 animate-spin" /> : <Send className="h-4 w-4" />}</Button></div>
  </DialogContent></Dialog>;
}
