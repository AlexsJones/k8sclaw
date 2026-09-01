import { useEffect, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import {
  useAgents,
  useDeleteAgent,
  useCreateAgent,
  useSkills,
  useRuntimes,
  usePolicies,
} from "@/hooks/use-api";
import { StatusBadge } from "@/components/status-badge";
import {
  OnboardingWizard,
  type WizardResult,
} from "@/components/onboarding-wizard";
import { WhatsAppQRModal } from "@/components/whatsapp-qr-modal";
import {
  Table,
  TableHeader,
  TableRow,
  TableHead,
  TableBody,
  TableCell,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Plus, Trash2, ExternalLink, ShieldAlert } from "lucide-react";
import { formatAge } from "@/lib/utils";

export function AgentsPage() {
  const { data, isLoading } = useAgents();
  const { data: skillPacks } = useSkills();
  const { data: runtimes } = useRuntimes();
  const { data: policies } = usePolicies();
  const [searchParams] = useSearchParams();
  const deleteAgent = useDeleteAgent();
  const createAgent = useCreateAgent();
  const [wizardOpen, setWizardOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [whatsAppInstance, setWhatsAppInstance] = useState<string | null>(null);
  const autoOpenConsumed = useRef(false);
  const harnessIncompatibleSkills = (skillPacks || [])
    .filter((skill) => skill.spec.sidecar?.hostAccess?.enabled)
    .map((skill) => skill.metadata.name);

  useEffect(() => {
    if (searchParams.get("create") === "1" && !autoOpenConsumed.current) {
      autoOpenConsumed.current = true;
      setWizardOpen(true);
    }
  }, [searchParams]);

  const filtered = (data || [])
    .filter((inst) =>
      inst.metadata.name.toLowerCase().includes(search.toLowerCase()),
    )
    .sort((a, b) => a.metadata.name.localeCompare(b.metadata.name));

  function handleComplete(result: WizardResult) {
    createAgent.mutate(
      {
        name: result.name,
        provider: result.provider,
        model: result.model,
        baseURL: result.baseURL || undefined,
        secretName: result.secretName || undefined,
        apiKey: result.apiKey || undefined,
        awsRegion: result.awsRegion || undefined,
        awsAccessKeyId: result.awsAccessKeyId || undefined,
        awsSecretAccessKey: result.awsSecretAccessKey || undefined,
        awsSessionToken: result.awsSessionToken || undefined,
        runtimeRef: result.runtimeRef || undefined,
        policyRef: result.policyRef || undefined,
        skills: result.skills.map((skillPackRef) => {
          if (skillPackRef === "web-endpoint") {
            const params: Record<string, string> = {};
            if (result.webEndpointRPM && result.webEndpointRPM !== "60") {
              params.rate_limit_rpm = result.webEndpointRPM;
            }
            if (result.webEndpointHostname) {
              params.hostname = result.webEndpointHostname;
            }
            return {
              skillPackRef,
              params: Object.keys(params).length > 0 ? params : undefined,
            };
          }
          return { skillPackRef };
        }),
        channels: result.channels.map((type) => ({
          type,
          configRef: result.channelConfigs[type]
            ? { provider: "", secret: result.channelConfigs[type] }
            : undefined,
        })),
        heartbeatInterval: result.heartbeatInterval || undefined,
        agentSandbox: result.agentSandboxEnabled
          ? {
              enabled: true,
              runtimeClass: result.agentSandboxRuntimeClass || "gvisor",
            }
          : undefined,
        runTimeout: result.runTimeout || undefined,
        requireApproval: result.requireApproval || undefined,
      },
      {
        onSuccess: () => {
          setWizardOpen(false);
          if (result.channels.includes("whatsapp")) {
            setWhatsAppInstance(result.name);
          }
        },
      },
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">Agents</h1>
          <p className="text-sm text-muted-foreground">
            Manage agents — each represents an agent identity
          </p>
        </div>
        <Button
          size="sm"
          className="bg-primary hover:bg-primary/90 text-primary-foreground border-0"
          onClick={() => setWizardOpen(true)}
        >
          <Plus className="mr-2 h-4 w-4" /> Create Agent
        </Button>
      </div>

      <Input
        placeholder="Search agents…"
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        className="max-w-sm"
      />

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 5 }).map((_, i) => (
            <Skeleton key={i} className="h-12 w-full" />
          ))}
        </div>
      ) : filtered.length === 0 ? (
        <div className="py-12 text-center space-y-3">
          <p className="text-muted-foreground">
            {search ? "No agents match your search" : "No agents yet"}
          </p>
          {!search && (
            <p className="text-sm text-muted-foreground">
              <Link
                to="/ensembles"
                className="text-blue-400 hover:text-blue-300"
              >
                Enable an ensemble
              </Link>{" "}
              to create agents automatically, or{" "}
              <button
                onClick={() => setWizardOpen(true)}
                className="text-blue-400 hover:text-blue-300"
              >
                create one manually
              </button>
              .
            </p>
          )}
        </div>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Provider</TableHead>
              <TableHead>Model</TableHead>
              <TableHead>Skills</TableHead>
              <TableHead>Channels</TableHead>
              <TableHead>Phase</TableHead>
              <TableHead>Runs</TableHead>
              <TableHead>Tokens</TableHead>
              <TableHead>Age</TableHead>
              <TableHead className="w-20" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {filtered.map((inst) => (
              <TableRow key={inst.metadata.name}>
                <TableCell className="font-mono text-sm">
                  <div className="flex items-center gap-2">
                    <Link
                      to={`/agents/${inst.metadata.name}`}
                      className="hover:text-primary flex items-center gap-1"
                    >
                      {inst.metadata.name}
                      <ExternalLink className="h-3 w-3 opacity-50" />
                    </Link>
                    {inst.metadata.labels?.["sympozium.ai/ensemble"] && (
                      <Link to={`/ensembles/${inst.metadata.labels["sympozium.ai/ensemble"]}`}>
                        <Badge
                          variant="outline"
                          className="text-[10px] px-1.5 py-0 text-blue-400 border-blue-500/30 hover:bg-blue-500/10"
                        >
                          {inst.metadata.labels["sympozium.ai/ensemble"]}
                        </Badge>
                      </Link>
                    )}
                    {inst.spec.agents?.default?.lifecycle?.postRun?.some(
                      (h) => h.gate,
                    ) && (
                      <span
                        data-testid="agent-gate-badge"
                        className="inline-flex items-center gap-0.5 rounded-full border border-amber-500/30 bg-amber-500/10 px-1.5 py-0 text-[10px] font-medium text-amber-400"
                        title="Requires manual approval"
                      >
                        <ShieldAlert className="h-2.5 w-2.5" />
                        Gated
                      </span>
                    )}
                  </div>
                </TableCell>
                <TableCell className="text-sm">
                  {inst.spec.authRefs?.[0]?.provider ||
                    (() => {
                      const base = inst.spec.agents?.default?.baseURL || "";
                      if (base.includes("ollama") || base.includes(":11434"))
                        return "ollama";
                      if (base.includes("lm-studio") || base.includes(":1234"))
                        return "lm-studio";
                      if (base.includes("llama-server")) return "llama-server";
                      if (base.includes("unsloth") || base.includes(":8080"))
                        return "unsloth";
                      if (base) return "custom";
                      return "—";
                    })()}
                </TableCell>
                <TableCell className="text-sm">
                  {inst.spec.agents?.default?.model || "—"}
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {inst.spec.skills?.length || 0}
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {inst.spec.channels?.length || 0}
                </TableCell>
                <TableCell>
                  <StatusBadge phase={inst.status?.phase} />
                </TableCell>
                <TableCell className="text-sm">
                  {inst.status?.totalAgentRuns ?? 0}
                </TableCell>
                <TableCell className="text-sm">—</TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {formatAge(inst.metadata.creationTimestamp)}
                </TableCell>
                <TableCell>
                  <Button
                    variant="ghost"
                    size="icon"
                    onClick={() => deleteAgent.mutate(inst.metadata.name)}
                    disabled={deleteAgent.isPending}
                    title="Delete"
                  >
                    <Trash2 className="h-4 w-4 text-destructive" />
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      {/* Shared onboarding wizard in agent mode */}
      <OnboardingWizard
        open={wizardOpen}
        onClose={() => setWizardOpen(false)}
        mode="agent"
        availableSkills={(skillPacks || []).map((s) => s.metadata.name)}
        availableRuntimes={runtimes || []}
        availablePolicies={policies || []}
        harnessIncompatibleSkills={harnessIncompatibleSkills}
        defaults={{
          provider: "openai",
          model: "gpt-4o",
          skills: ["k8s-ops", "llmfit", "memory"],
          ...(searchParams.get("runtime") ? {
            runtimeRef: searchParams.get("runtime") || "",
            policyRef: searchParams.get("policy") || "harness-examples",
          } : {}),
        }}
        onComplete={handleComplete}
        isPending={createAgent.isPending}
      />

      <WhatsAppQRModal
        open={!!whatsAppInstance}
        onClose={() => setWhatsAppInstance(null)}
        agentName={whatsAppInstance || undefined}
      />
    </div>
  );
}
