import { Link, useParams } from "react-router-dom";
import { useRuntimes } from "@/hooks/use-api";
import { Breadcrumbs } from "@/components/breadcrumbs";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { ShieldCheck, ShieldX } from "lucide-react";

export function HarnessDetailPage() {
  const { name } = useParams<{ name: string }>();
  const { data: runtimes, isLoading } = useRuntimes();
  const runtime = runtimes?.find((item) => item.metadata.name === name);

  if (isLoading) return <Skeleton className="h-64 w-full" />;
  if (!runtime) return <p className="text-muted-foreground">Harness not found</p>;

  const readyCondition = runtime.status?.conditions?.find((item) => item.type === "Ready");
  const isReady = readyCondition?.status === "True";

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <Breadcrumbs items={[{ label: "Agents", to: "/agents" }, { label: "Harnesses", to: "/harnesses" }, { label: runtime.metadata.name }]} />
        <div className="flex items-center gap-3">
          <h1 className="text-2xl font-bold font-mono">{runtime.metadata.name}</h1>
          <Badge variant={isReady ? "default" : "destructive"} className="gap-1">
            {isReady ? <ShieldCheck className="h-3 w-3" /> : <ShieldX className="h-3 w-3" />}
            {isReady ? "Ready" : "Not ready"}
          </Badge>
        </div>
        <p className="text-sm text-muted-foreground">Administrator-approved AgentRuntime in {runtime.metadata.namespace || "default"}.</p>
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader><CardTitle className="text-base">Artifact and contract</CardTitle></CardHeader>
          <CardContent className="space-y-4 text-sm">
            <Field label="Approved image" value={runtime.spec.image} mono />
            <Field label="Resolved immutable digest" value={runtime.status?.resolvedImageDigest || "Pending resolution"} mono />
            <Field label="Adapter contract" value={runtime.spec.contractVersion || "Not declared"} />
            <Field label="Support owner" value={runtime.spec.supportOwner || "Not declared"} />
          </CardContent>
        </Card>

        <Card>
          <CardHeader><CardTitle className="text-base">Readiness and conformance</CardTitle></CardHeader>
          <CardContent className="space-y-4 text-sm">
            <Field label="Readiness reason" value={readyCondition?.reason || "Waiting for controller validation"} />
            <Field label="Readiness detail" value={readyCondition?.message || "No readiness condition reported"} />
            <Field label="Conformance status" value={runtime.spec.conformance?.status || "Not declared"} />
            <Field label="Conformance owner" value={runtime.spec.conformance?.owner || "Not declared"} />
          </CardContent>
        </Card>

        <Card>
          <CardHeader><CardTitle className="text-base">Capability provenance</CardTitle></CardHeader>
          <CardContent className="space-y-3 text-sm">
            <p className="text-muted-foreground">These are adapter-maintainer claims. They are not proof that Sympozium mediates or enforces the behavior.</p>
            <div className="flex flex-wrap gap-1">
              {runtime.spec.capabilities?.length ? runtime.spec.capabilities.map((capability) => <Badge key={capability} variant="secondary">{capability}</Badge>) : <span className="text-muted-foreground">No capabilities declared.</span>}
            </div>
            <p className="text-xs text-muted-foreground">Platform-enforced: admission policy, run identity, token boundaries, mounts, NATS subjects, and lifecycle.</p>
          </CardContent>
        </Card>

        <Card>
          <CardHeader><CardTitle className="text-base">Use this harness</CardTitle></CardHeader>
          <CardContent className="space-y-3 text-sm text-muted-foreground">
            <p>Select this runtime on an Agent's <strong className="text-foreground">Harness</strong> tab to make it the inherited default, or choose it in <strong className="text-foreground">Runs → New Run</strong> for one invocation.</p>
            <p>Readiness is necessary, but policy eligibility is evaluated by admission for each AgentRun. A ready harness can still be denied by the Agent's namespace policy.</p>
            <Link to="/agents" className="text-blue-400 hover:text-blue-300">Browse Agents →</Link>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

function Field({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return <div><p className="text-xs text-muted-foreground">{label}</p><p className={mono ? "mt-1 break-all font-mono text-xs" : "mt-1 whitespace-pre-wrap"}>{value}</p></div>;
}
