import { Link } from "react-router-dom";
import { useRuntimes, useInstallDefaultRuntimes } from "@/hooks/use-api";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { ShieldCheck, ShieldX, ExternalLink, Download, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";

function ready(runtime: import("@/lib/api").AgentRuntime) {
  return runtime.status?.conditions?.some(
    (condition) => condition.type === "Ready" && condition.status === "True",
  ) ?? false;
}

export function HarnessesPage() {
  const { data: runtimes, isLoading } = useRuntimes();
  const installDefaults = useInstallDefaultRuntimes();

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
                  </div>
                </CardContent>
              </Card>
            );
          })}
        </div>
      )}
    </div>
  );
}

function Field({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return <div><p className="text-xs text-muted-foreground">{label}</p><p className={mono ? "break-all font-mono text-xs" : "truncate"}>{value}</p></div>;
}
