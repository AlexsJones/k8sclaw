import { useState } from "react";
import { useModelList } from "@/hooks/use-model-list";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Sparkles,
  Power,
  Server,
  ChevronRight,
  ChevronLeft,
  Check,
  Key,
  Bot,
  MessageSquare,
  Loader2,
  Search,
} from "lucide-react";
import { cn } from "@/lib/utils";

// ── Shared constants ─────────────────────────────────────────────────────────

export const PROVIDERS = [
  { value: "openai", label: "OpenAI", defaultModel: "gpt-4o" },
  { value: "anthropic", label: "Anthropic", defaultModel: "claude-sonnet-4-20250514" },
  { value: "azure-openai", label: "Azure OpenAI", defaultModel: "gpt-4o" },
  { value: "ollama", label: "Ollama", defaultModel: "llama3" },
  { value: "custom", label: "Custom", defaultModel: "" },
];

const CHANNELS = [
  { value: "discord", label: "Discord" },
  { value: "slack", label: "Slack" },
  { value: "telegram", label: "Telegram" },
  { value: "whatsapp", label: "WhatsApp" },
];

// ── Types ────────────────────────────────────────────────────────────────────

export interface WizardResult {
  name: string;
  provider: string;
  apiKey: string;
  secretName: string;
  model: string;
  baseURL: string;
  channelConfigs: Record<string, string>;
}

interface OnboardingWizardProps {
  open: boolean;
  onClose: () => void;
  /** "instance" shows a Name step first; "persona" skips it */
  mode: "instance" | "persona";
  /** Display name shown in the dialog title */
  targetName?: string;
  /** Number of personas in the pack (persona mode only) */
  personaCount?: number;
  /** Pre-fill form values */
  defaults?: Partial<WizardResult>;
  /** Called when the user clicks Activate / Create */
  onComplete: (result: WizardResult) => void;
  isPending: boolean;
}

// ── Steps ────────────────────────────────────────────────────────────────────

type WizardStep = "name" | "provider" | "apikey" | "model" | "channels" | "confirm";

function stepsForMode(mode: "instance" | "persona"): WizardStep[] {
  if (mode === "instance") {
    return ["name", "provider", "apikey", "model", "channels", "confirm"];
  }
  return ["provider", "apikey", "model", "channels", "confirm"];
}

// ── Step indicator ───────────────────────────────────────────────────────────

function StepIndicator({ steps, current }: { steps: WizardStep[]; current: WizardStep }) {
  const labels: Record<WizardStep, string> = {
    name: "Name",
    provider: "Provider",
    apikey: "Auth",
    model: "Model",
    channels: "Channels",
    confirm: "Confirm",
  };
  const icons: Record<WizardStep, React.ReactNode> = {
    name: <Server className="h-3.5 w-3.5" />,
    provider: <Bot className="h-3.5 w-3.5" />,
    apikey: <Key className="h-3.5 w-3.5" />,
    model: <Sparkles className="h-3.5 w-3.5" />,
    channels: <MessageSquare className="h-3.5 w-3.5" />,
    confirm: <Check className="h-3.5 w-3.5" />,
  };
  const idx = steps.indexOf(current);

  return (
    <div className="flex flex-wrap items-center justify-center gap-1 mb-6">
      {steps.map((step, i) => (
        <div key={step} className="flex items-center gap-1">
          <div
            className={cn(
              "flex items-center gap-1 rounded-full px-2 py-1 text-[11px] font-medium transition-colors",
              i < idx
                ? "bg-indigo-500/20 text-indigo-400"
                : i === idx
                ? "bg-indigo-500 text-white"
                : "bg-muted text-muted-foreground"
            )}
          >
            {i < idx ? <Check className="h-3 w-3" /> : icons[step]}
            <span>{labels[step]}</span>
          </div>
          {i < steps.length - 1 && (
            <ChevronRight className="h-3 w-3 text-muted-foreground" />
          )}
        </div>
      ))}
    </div>
  );
}

// ── Model selector with search ───────────────────────────────────────────────

function ModelSelector({
  provider,
  apiKey,
  value,
  onChange,
}: {
  provider: string;
  apiKey: string;
  value: string;
  onChange: (v: string) => void;
}) {
  const { models, isLoading, isLive } = useModelList(provider, apiKey);
  const [search, setSearch] = useState("");

  const filtered = models.filter((m) =>
    m.toLowerCase().includes(search.toLowerCase())
  );

  return (
    <div className="space-y-2">
      <Label>Model</Label>

      {/* Search input */}
      <div className="relative">
        <Search className="absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
        <Input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Search models…"
          className="h-8 pl-8 text-sm"
        />
      </div>

      {isLoading ? (
        <div className="flex items-center gap-2 py-4 text-xs text-muted-foreground justify-center">
          <Loader2 className="h-3.5 w-3.5 animate-spin" />
          Fetching models from {provider}…
        </div>
      ) : (
        <ScrollArea className="h-44 rounded-md border border-border/50">
          <div className="p-1 space-y-0.5">
            {filtered.length === 0 ? (
              <p className="py-3 text-center text-xs text-muted-foreground">
                No models match "{search}"
              </p>
            ) : (
              filtered.map((m) => (
                <button
                  key={m}
                  type="button"
                  onClick={() => onChange(m)}
                  className={cn(
                    "flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-xs font-mono transition-colors text-left",
                    m === value
                      ? "bg-indigo-500/15 text-indigo-400 border border-indigo-500/30"
                      : "text-foreground hover:bg-white/5 border border-transparent"
                  )}
                >
                  {m === value && <Check className="h-3 w-3 shrink-0" />}
                  <span className="truncate">{m}</span>
                </button>
              ))
            )}
          </div>
        </ScrollArea>
      )}

      {/* Custom input */}
      <div className="space-y-1">
        <Label className="text-xs text-muted-foreground">
          Or enter a custom model name
        </Label>
        <Input
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder="gpt-4o"
          className="h-8 text-sm font-mono"
        />
      </div>

      {isLive && (
        <p className="text-[10px] text-emerald-400/70">
          ✓ Live models fetched from {provider} API
        </p>
      )}
    </div>
  );
}

// ── Main wizard component ────────────────────────────────────────────────────

export function OnboardingWizard({
  open,
  onClose,
  mode,
  targetName,
  personaCount,
  defaults,
  onComplete,
  isPending,
}: OnboardingWizardProps) {
  const steps = stepsForMode(mode);
  const [step, setStep] = useState<WizardStep>(steps[0]);
  const [form, setForm] = useState<WizardResult>({
    name: defaults?.name || "",
    provider: defaults?.provider || "",
    apiKey: defaults?.apiKey || "",
    secretName: defaults?.secretName || "",
    model: defaults?.model || "",
    baseURL: defaults?.baseURL || "",
    channelConfigs: defaults?.channelConfigs || {},
  });

  const stepIdx = steps.indexOf(step);

  const canNext = (() => {
    switch (step) {
      case "name":
        return !!form.name.trim();
      case "provider":
        return !!form.provider;
      case "apikey":
        return !!form.secretName || !!form.apiKey;
      case "model":
        return !!form.model;
      default:
        return true;
    }
  })();

  function next() {
    if (stepIdx < steps.length - 1) setStep(steps[stepIdx + 1]);
  }
  function prev() {
    if (stepIdx > 0) setStep(steps[stepIdx - 1]);
  }

  function handleClose() {
    setStep(steps[0]);
    onClose();
  }

  function handleComplete() {
    onComplete(form);
  }

  // Reset form when defaults change (new wizard opened)
  function resetWith(d: Partial<WizardResult>) {
    setForm({
      name: d.name || "",
      provider: d.provider || "",
      apiKey: d.apiKey || "",
      secretName: d.secretName || "",
      model: d.model || "",
      baseURL: d.baseURL || "",
      channelConfigs: d.channelConfigs || {},
    });
    setStep(steps[0]);
  }

  // Expose reset via key change (parent passes new defaults)
  const defaultsKey = JSON.stringify(defaults);
  useState(() => {
    resetWith(defaults || {});
  });
  // Also reset when defaults change via effect
  const [prevKey, setPrevKey] = useState(defaultsKey);
  if (defaultsKey !== prevKey) {
    setPrevKey(defaultsKey);
    resetWith(defaults || {});
  }

  const titleIcon =
    mode === "instance" ? (
      <Server className="h-5 w-5 text-indigo-400" />
    ) : (
      <Sparkles className="h-5 w-5 text-indigo-400" />
    );
  const titleText =
    mode === "instance"
      ? "Create Instance"
      : `Enable ${targetName || "Pack"}`;
  const completeLabel = mode === "instance" ? "Create" : "Activate";
  const completeIcon =
    mode === "instance" ? (
      <Server className="h-4 w-4" />
    ) : (
      <Power className="h-4 w-4" />
    );

  return (
    <Dialog open={open} onOpenChange={(v) => !v && handleClose()}>
      <DialogContent className="sm:max-w-md overflow-hidden">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            {titleIcon}
            {mode === "persona" ? (
              <>
                Enable{" "}
                <span className="font-mono text-indigo-400">{targetName}</span>
              </>
            ) : (
              "Create Instance"
            )}
          </DialogTitle>
          <DialogDescription>
            {mode === "instance"
              ? "Configure a new SympoziumInstance with provider and model."
              : "Configure provider, model, and optional channels to activate this persona pack."}
          </DialogDescription>
        </DialogHeader>

        <StepIndicator steps={steps} current={step} />

        {/* ── Name step (instance only) ─────────────────────────────── */}
        {step === "name" && (
          <div className="space-y-4">
            <div className="space-y-2">
              <Label>Instance Name</Label>
              <Input
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                placeholder="my-agent"
                autoFocus
              />
            </div>
          </div>
        )}

        {/* ── Provider step ─────────────────────────────────────────── */}
        {step === "provider" && (
          <div className="space-y-4">
            <div className="space-y-2">
              <Label>AI Provider</Label>
              <Select
                value={form.provider}
                onValueChange={(v) => {
                  const prov = PROVIDERS.find((p) => p.value === v);
                  setForm({
                    ...form,
                    provider: v,
                    model: form.model || prov?.defaultModel || "",
                  });
                }}
              >
                <SelectTrigger>
                  <SelectValue placeholder="Select a provider…" />
                </SelectTrigger>
                <SelectContent>
                  {PROVIDERS.map((p) => (
                    <SelectItem key={p.value} value={p.value}>
                      {p.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            {(form.provider === "ollama" ||
              form.provider === "custom" ||
              form.provider === "azure-openai") && (
              <div className="space-y-2">
                <Label>Base URL</Label>
                <Input
                  value={form.baseURL}
                  onChange={(e) => setForm({ ...form, baseURL: e.target.value })}
                  placeholder={
                    form.provider === "ollama"
                      ? "http://localhost:11434/v1"
                      : "https://your-endpoint.openai.azure.com/v1"
                  }
                />
              </div>
            )}
          </div>
        )}

        {/* ── Auth step ─────────────────────────────────────────────── */}
        {step === "apikey" && (
          <div className="space-y-4">
            {(form.provider === "openai" || form.provider === "anthropic") && (
              <div className="space-y-2">
                <Label>API Key</Label>
                <Input
                  type="password"
                  value={form.apiKey}
                  onChange={(e) =>
                    setForm({ ...form, apiKey: e.target.value })
                  }
                  placeholder="sk-…"
                  autoComplete="off"
                />
                <p className="text-xs text-muted-foreground">
                  A Kubernetes Secret will be created automatically from this
                  key. Also used to fetch available models.
                </p>
              </div>
            )}
            <div className="space-y-2">
              <Label>K8s Secret Name <span className="text-muted-foreground font-normal">(optional if API Key provided)</span></Label>
              <Input
                value={form.secretName}
                onChange={(e) =>
                  setForm({ ...form, secretName: e.target.value })
                }
                placeholder="my-provider-api-key"
              />
              <p className="text-xs text-muted-foreground">
                Use an existing Kubernetes Secret, or leave blank to
                auto-create one from the API Key above.
              </p>
            </div>
          </div>
        )}

        {/* ── Model step ────────────────────────────────────────────── */}
        {step === "model" && (
          <div className="space-y-2">
            <ModelSelector
              provider={form.provider}
              apiKey={form.apiKey}
              value={form.model}
              onChange={(v) => setForm({ ...form, model: v })}
            />
            {mode === "persona" && personaCount !== undefined && (
              <p className="text-xs text-muted-foreground">
                Applied to all{" "}
                <span className="text-indigo-400">{personaCount}</span>{" "}
                personas.
              </p>
            )}
          </div>
        )}

        {/* ── Channels step ─────────────────────────────────────────── */}
        {step === "channels" && (
          <div className="space-y-4">
            <p className="text-sm text-muted-foreground">
              Optionally map channel types to their config secret names.
            </p>
            {CHANNELS.map((ch) => (
              <div key={ch.value} className="space-y-1">
                <Label className="text-xs capitalize">{ch.label} Secret</Label>
                <Input
                  value={form.channelConfigs[ch.value] || ""}
                  onChange={(e) => {
                    const configs = { ...form.channelConfigs };
                    if (e.target.value) {
                      configs[ch.value] = e.target.value;
                    } else {
                      delete configs[ch.value];
                    }
                    setForm({ ...form, channelConfigs: configs });
                  }}
                  placeholder={`${ch.value}-bot-token`}
                  className="h-8 text-sm"
                />
              </div>
            ))}
          </div>
        )}

        {/* ── Confirm step ──────────────────────────────────────────── */}
        {step === "confirm" && (
          <div className="space-y-3">
            <div className="rounded-lg border border-indigo-500/20 bg-indigo-500/5 p-4 space-y-2 text-sm">
              {mode === "instance" && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Name</span>
                  <span className="font-mono text-indigo-400">{form.name}</span>
                </div>
              )}
              {mode === "persona" && targetName && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Pack</span>
                  <span className="font-mono text-indigo-400">{targetName}</span>
                </div>
              )}
              <div className="flex justify-between">
                <span className="text-muted-foreground">Provider</span>
                <span>{form.provider}</span>
              </div>
              <div className="flex justify-between">
                <span className="text-muted-foreground">Secret</span>
                <span className="font-mono">{form.secretName || "—"}</span>
              </div>
              <div className="flex justify-between">
                <span className="text-muted-foreground">Model</span>
                <span className="font-mono">{form.model}</span>
              </div>
              {form.baseURL && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Base URL</span>
                  <span className="font-mono text-xs truncate max-w-[200px]">
                    {form.baseURL}
                  </span>
                </div>
              )}
              {mode === "persona" && personaCount !== undefined && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Personas</span>
                  <span>{personaCount}</span>
                </div>
              )}
              {Object.keys(form.channelConfigs).length > 0 && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Channels</span>
                  <span>{Object.keys(form.channelConfigs).join(", ")}</span>
                </div>
              )}
            </div>
            <p className="text-xs text-muted-foreground">
              {mode === "instance"
                ? "A new SympoziumInstance will be created with this configuration."
                : "The controller will stamp out Instances, Schedules, and ConfigMaps for each persona."}
            </p>
          </div>
        )}

        {/* ── Navigation ────────────────────────────────────────────── */}
        <div className="flex items-center justify-between pt-2">
          <Button
            variant="ghost"
            size="sm"
            onClick={prev}
            disabled={stepIdx === 0}
            className="gap-1"
          >
            <ChevronLeft className="h-4 w-4" /> Back
          </Button>

          {step === "confirm" ? (
            <Button
              size="sm"
              className="gap-1 bg-gradient-to-r from-indigo-500 to-purple-600 hover:from-indigo-600 hover:to-purple-700 text-white border-0"
              onClick={handleComplete}
              disabled={isPending}
            >
              {isPending ? (
                "Working…"
              ) : (
                <>
                  {completeIcon} {completeLabel}
                </>
              )}
            </Button>
          ) : (
            <Button
              size="sm"
              onClick={next}
              disabled={!canNext}
              className="gap-1 bg-gradient-to-r from-indigo-500 to-purple-600 hover:from-indigo-600 hover:to-purple-700 text-white border-0"
            >
              Next <ChevronRight className="h-4 w-4" />
            </Button>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
