import { useState } from "react";
import { Link } from "react-router-dom";
import { useInstances, useDeleteInstance, useCreateInstance, useSkills } from "@/hooks/use-api";
import { StatusBadge } from "@/components/status-badge";
import { OnboardingWizard, type WizardResult } from "@/components/onboarding-wizard";
import { WhatsAppQRModal } from "@/components/whatsapp-qr-modal";
import {
  Table,
  TableHeader,
  TableRow,
  TableHead,
  TableBody,
  TableCell,
} from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Plus, Trash2, ExternalLink } from "lucide-react";
import { formatAge } from "@/lib/utils";

export function InstancesPage() {
  const { data, isLoading } = useInstances();
  const { data: skillPacks } = useSkills();
  const deleteInstance = useDeleteInstance();
  const createInstance = useCreateInstance();
  const [wizardOpen, setWizardOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [whatsAppInstance, setWhatsAppInstance] = useState<string | null>(null);

  const filtered = (data || []).filter((inst) =>
    inst.metadata.name.toLowerCase().includes(search.toLowerCase())
  );

  function handleComplete(result: WizardResult) {
    createInstance.mutate(
      {
        name: result.name,
        provider: result.provider,
        model: result.model,
        baseURL: result.baseURL || undefined,
        secretName: result.secretName || undefined,
        skills: result.skills,
        channels: result.channels,
        channelConfigs:
          Object.keys(result.channelConfigs).length > 0
            ? result.channelConfigs
            : undefined,
      },
      {
        onSuccess: () => {
          setWizardOpen(false);
          if (result.channels.includes("whatsapp")) {
            setWhatsAppInstance(result.name);
          }
        },
      }
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">Instances</h1>
          <p className="text-sm text-muted-foreground">
            Manage SympoziumInstances — each represents an agent identity
          </p>
        </div>
        <Button
          size="sm"
          className="bg-gradient-to-r from-indigo-500 to-purple-600 hover:from-indigo-600 hover:to-purple-700 text-white border-0"
          onClick={() => setWizardOpen(true)}
        >
          <Plus className="mr-2 h-4 w-4" /> Create Instance
        </Button>
      </div>

      <Input
        placeholder="Search instances…"
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
        <p className="py-8 text-center text-muted-foreground">
          No instances found
        </p>
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
              <TableHead>Age</TableHead>
              <TableHead className="w-20" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {filtered.map((inst) => (
              <TableRow key={inst.metadata.name}>
                <TableCell className="font-mono text-sm">
                  <Link
                    to={`/instances/${inst.metadata.name}`}
                    className="hover:text-primary flex items-center gap-1"
                  >
                    {inst.metadata.name}
                    <ExternalLink className="h-3 w-3 opacity-50" />
                  </Link>
                </TableCell>
                <TableCell className="text-sm">
                  {inst.spec.authRefs?.[0]?.provider || "—"}
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
                <TableCell className="text-sm text-muted-foreground">
                  {formatAge(inst.metadata.creationTimestamp)}
                </TableCell>
                <TableCell>
                  <Button
                    variant="ghost"
                    size="icon"
                    onClick={() => deleteInstance.mutate(inst.metadata.name)}
                    disabled={deleteInstance.isPending}
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

      {/* Shared onboarding wizard in instance mode */}
      <OnboardingWizard
        open={wizardOpen}
        onClose={() => setWizardOpen(false)}
        mode="instance"
        availableSkills={(skillPacks || []).map((s) => s.metadata.name)}
        defaults={{ provider: "openai", model: "gpt-4o", skills: ["k8s-ops"] }}
        onComplete={handleComplete}
        isPending={createInstance.isPending}
      />

      <WhatsAppQRModal
        open={!!whatsAppInstance}
        onClose={() => setWhatsAppInstance(null)}
        instanceName={whatsAppInstance || undefined}
      />
    </div>
  );
}
