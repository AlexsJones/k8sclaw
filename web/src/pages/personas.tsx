import { useState } from "react";
import { Link } from "react-router-dom";
import { usePersonaPacks, useActivatePersonaPack, useDeletePersonaPack } from "@/hooks/use-api";
import { StatusBadge } from "@/components/status-badge";
import { OnboardingWizard, type WizardResult } from "@/components/onboarding-wizard";
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
import { Skeleton } from "@/components/ui/skeleton";
import { Input } from "@/components/ui/input";
import { ExternalLink, Sparkles, PowerOff, Trash2 } from "lucide-react";
import { formatAge } from "@/lib/utils";
import type { PersonaPack } from "@/lib/api";

export function PersonasPage() {
  const { data, isLoading } = usePersonaPacks();
  const activatePack = useActivatePersonaPack();
  const deletePack = useDeletePersonaPack();
  const [search, setSearch] = useState("");

  // Wizard state
  const [wizardOpen, setWizardOpen] = useState(false);
  const [wizardPack, setWizardPack] = useState<PersonaPack | null>(null);

  const filtered = (data || []).filter((p) =>
    p.metadata.name.toLowerCase().includes(search.toLowerCase())
  );

  function openWizard(pack: PersonaPack) {
    setWizardPack(pack);
    setWizardOpen(true);
  }

  function closeWizard() {
    setWizardOpen(false);
    setWizardPack(null);
  }

  function handleComplete(result: WizardResult) {
    if (!wizardPack) return;
    activatePack.mutate(
      {
        name: wizardPack.metadata.name,
        enabled: true,
        provider: result.provider,
        secretName: result.secretName || undefined,
        apiKey: result.apiKey || undefined,
        model: result.model,
        baseURL: result.baseURL || undefined,
        channelConfigs:
          Object.keys(result.channelConfigs).length > 0
            ? result.channelConfigs
            : undefined,
      },
      { onSuccess: closeWizard }
    );
  }

  function handleToggle(pack: PersonaPack) {
    activatePack.mutate({
      name: pack.metadata.name,
      enabled: !pack.spec.enabled,
    });
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">Persona Packs</h1>
          <p className="text-sm text-muted-foreground">
            Pre-configured agent bundles — stamps out Instances, Schedules, and
            memory automatically
          </p>
        </div>
      </div>

      <Input
        placeholder="Search persona packs…"
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        className="max-w-sm"
      />

      {isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-12 w-full" />
          ))}
        </div>
      ) : filtered.length === 0 ? (
        <p className="py-8 text-center text-muted-foreground">
          No persona packs found
        </p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Category</TableHead>
              <TableHead>Version</TableHead>
              <TableHead>Personas</TableHead>
              <TableHead>Installed</TableHead>
              <TableHead>Phase</TableHead>
              <TableHead>Enabled</TableHead>
              <TableHead>Age</TableHead>
              <TableHead className="w-36" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {filtered.map((pack) => (
              <TableRow key={pack.metadata.name}>
                <TableCell className="font-mono text-sm">
                  <Link
                    to={`/personas/${pack.metadata.name}`}
                    className="hover:text-primary flex items-center gap-1"
                  >
                    {pack.metadata.name}
                    <ExternalLink className="h-3 w-3 opacity-50" />
                  </Link>
                </TableCell>
                <TableCell>
                  {pack.spec.category ? (
                    <Badge variant="outline" className="text-xs capitalize">
                      {pack.spec.category}
                    </Badge>
                  ) : (
                    "—"
                  )}
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {pack.spec.version || "—"}
                </TableCell>
                <TableCell className="text-sm">
                  {pack.status?.personaCount ?? pack.spec.personas?.length ?? 0}
                </TableCell>
                <TableCell className="text-sm">
                  {pack.status?.installedCount ?? 0}
                </TableCell>
                <TableCell>
                  <StatusBadge phase={pack.status?.phase} />
                </TableCell>
                <TableCell>
                  {pack.spec.enabled ? (
                    <Badge variant="default" className="text-xs">Yes</Badge>
                  ) : (
                    <Badge variant="secondary" className="text-xs">No</Badge>
                  )}
                </TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {formatAge(pack.metadata.creationTimestamp)}
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-1">
                    {!pack.spec.enabled ? (
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-7 gap-1 text-xs text-indigo-400 hover:text-indigo-300 hover:bg-indigo-500/10"
                        onClick={() => openWizard(pack)}
                      >
                        <Sparkles className="h-3 w-3" />
                        Enable
                      </Button>
                    ) : (
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-7 gap-1 text-xs text-amber-400 hover:text-amber-300 hover:bg-amber-500/10"
                        onClick={() => handleToggle(pack)}
                        disabled={activatePack.isPending}
                      >
                        <PowerOff className="h-3 w-3" />
                        Disable
                      </Button>
                    )}
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-7 w-7 p-0 text-muted-foreground hover:text-red-400"
                      onClick={() => deletePack.mutate(pack.metadata.name)}
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      {/* Shared onboarding wizard */}
      <OnboardingWizard
        open={wizardOpen}
        onClose={closeWizard}
        mode="persona"
        targetName={wizardPack?.metadata.name}
        personaCount={wizardPack?.spec.personas?.length ?? 0}
        defaults={{
          provider: wizardPack?.spec.authRefs?.[0]?.provider || "",
          secretName: wizardPack?.spec.authRefs?.[0]?.secret || "",
          model: wizardPack?.spec.personas?.[0]?.model || "",
          channelConfigs: wizardPack?.spec.channelConfigs || {},
        }}
        onComplete={handleComplete}
        isPending={activatePack.isPending}
      />
    </div>
  );
}
