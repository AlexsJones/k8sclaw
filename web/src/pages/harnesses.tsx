import { Link } from "react-router-dom";
import { useState } from "react";
import { useAgents, useDeleteHarnessSession, useHarnessSessions, useRuntimes, useInstallDefaultRuntimes } from "@/hooks/use-api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { ShieldCheck, ShieldX, ExternalLink, Download, Plus, MessageSquare, Square } from "lucide-react";
import { Button } from "@/components/ui/button";
import { HarnessSessionChatDialog, StartHarnessSessionDialog } from "@/components/harness-session-dialog";

function ready(runtime: import("@/lib/api").AgentRuntime) {
  return runtime.status?.conditions?.some(
    (condition) => condition.type === "Ready" && condition.status === "True",
  ) ?? false;
}

export function HarnessesPage() {
  const { data: runtimes, isLoading } = useRuntimes();
  const { data: agents } = useAgents();
  const { data: sessions } = useHarnessSessions();
  const installDefaults = useInstallDefaultRuntimes();
  const stopSession = useDeleteHarnessSession();
  const [startingRuntime, setStartingRuntime] = useState<import("@/lib/api").AgentRuntime | null>(null);
  const [chattingSession, setChattingSession] = useState<import("@/lib/api").HarnessSession | null>(null);

  if (isLoading) {
    return <Skeleton className="h-64 w-full" />;
  }

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-bold">Harnesses</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            Administrator-approved execution adapters. These are trusted runtime
            profiles, not arbitrary container images.
          </p>
        </div>
        <Button variant="outline" size="sm" onClick={() => installDefaults.mutate()} disabled={installDefaults.isPending}>
          <Download className="mr-2 h-4 w-4" /> {installDefaults.isPending ? "Installing…" : "Install defaults"}
        </Button>
      </div>

      {(runtimes || []).length === 0 ? (
        <Card>
          <CardContent className="space-y-4 py-8 text-sm text-muted-foreground">
            <p>No approved harnesses are registered in this namespace.</p>
            <p>Install Pi and Hermes as curated, digest-pinned defaults. This also installs the required harness policy; it does not create an Agent or credentials.</p>
            <Button size="sm" onClick={() => installDefaults.mutate()} disabled={installDefaults.isPending}>
              <Download className="mr-2 h-4 w-4" /> {installDefaults.isPending ? "Installing…" : "Install default harnesses"}
            </Button>
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          {(runtimes || []).map((runtime) => {
            const isReady = ready(runtime);
            return (
              <Card key={runtime.metadata.name}>
                <CardHeader className="pb-3">
                  <div className="flex items-start justify-between gap-3">
                    <div>
                      <CardTitle className="font-mono text-base">
                        {runtime.metadata.name}
                      </CardTitle>
                      <p className="mt-1 text-xs text-muted-foreground">
                        {runtime.metadata.namespace || "default"}
                      </p>
                    </div>
                    <Badge variant={isReady ? "default" : "destructive"} className="gap-1">
                      {isReady ? <ShieldCheck className="h-3 w-3" /> : <ShieldX className="h-3 w-3" />}
                      {isReady ? "Ready" : "Not ready"}
                    </Badge>
                  </div>
                </CardHeader>
                <CardContent className="space-y-3 text-sm">
                  <Field label="Executed image" value={runtime.status?.resolvedImageDigest || runtime.spec.image} mono />
                  <div className="grid grid-cols-2 gap-3">
                    <Field label="Contract" value={runtime.spec.contractVersion || "not declared"} />
                    <Field label="Support owner" value={runtime.spec.supportOwner || "not declared"} />
                    <Field label="Conformance" value={runtime.spec.conformance?.status || "not declared"} />
                    <Field label="Conformance owner" value={runtime.spec.conformance?.owner || "not declared"} />
                  </div>
                  <div>
                    <p className="text-xs text-muted-foreground">Adapter-claimed capabilities</p>
                    <div className="mt-1 flex flex-wrap gap-1">
                      {runtime.spec.capabilities?.length ? runtime.spec.capabilities.map((capability) => (
                        <Badge key={capability} variant="secondary">{capability}</Badge>
                      )) : <span className="text-xs text-muted-foreground">None declared</span>}
                    </div>
                  </div>
                  <p className="text-xs text-muted-foreground">
                    Policy, per-run identity, mounts, NATS, and lifecycle are platform-enforced.
                    Adapter capabilities above are claims, not verified platform behavior.
                  </p>
                  <div className="flex gap-4 text-xs">
                    <Link to={`/harnesses/${runtime.metadata.name}`} className="inline-flex items-center gap-1 text-blue-400 hover:text-blue-300">
                      Inspect harness <ExternalLink className="h-3 w-3" />
                    </Link>
                    <Link to={`/agents?runtime=${encodeURIComponent(runtime.metadata.name)}&policy=harness-examples`} className="inline-flex items-center gap-1 text-blue-400 hover:text-blue-300">
                      <Plus className="h-3 w-3" /> Create Agent using this harness
                    </Link>
                    {runtime.spec.contractVersion === "v1alpha2" && runtime.spec.session?.protocol === "openai-chat" && <Button variant="link" className="h-auto p-0 text-xs text-blue-400 hover:text-blue-300" onClick={() => setStartingRuntime(runtime)}><MessageSquare className="mr-1 h-3 w-3" /> Start interactive session</Button>}
                  </div>
                </CardContent>
              </Card>
            );
          })}
        </div>
      )}

      {(sessions || []).length > 0 && <Card>
        <CardHeader><CardTitle className="text-base">Interactive sessions</CardTitle></CardHeader>
        <CardContent className="space-y-2">{sessions?.map((session) => <div key={session.metadata.name} className="flex items-center justify-between gap-3 border p-3 text-sm"><div><p className="font-mono">{session.metadata.name}</p><p className="text-xs text-muted-foreground">{session.spec.agentRef} · {session.spec.runtimeRef} · {session.status?.phase || "Pending"}</p></div><div className="flex gap-2">{session.status?.phase === "Ready" && <Button size="sm" onClick={() => setChattingSession(session)}><MessageSquare className="mr-2 h-4 w-4" /> Open</Button>}<Button size="sm" variant="outline" disabled={stopSession.isPending} onClick={() => stopSession.mutate(session.metadata.name)}><Square className="mr-2 h-3 w-3" /> Stop</Button></div></div>)}</CardContent>
      </Card>}
      {startingRuntime && <StartHarnessSessionDialog open={true} onOpenChange={(open) => { if (!open) setStartingRuntime(null); }} runtime={startingRuntime} agents={agents || []} />}
      {chattingSession && <HarnessSessionChatDialog open={true} onOpenChange={(open) => { if (!open) setChattingSession(null); }} session={chattingSession} />}
    </div>
  );
}

function Field({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return <div><p className="text-xs text-muted-foreground">{label}</p><p className={mono ? "break-all font-mono text-xs" : "truncate"}>{value}</p></div>;
}
