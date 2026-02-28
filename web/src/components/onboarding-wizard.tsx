import { useEffect, useMemo, useState } from "react";
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
  Wrench,
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
  skills: string[];
  channels: string[];
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
  /** Available SkillPacks to choose from */
  availableSkills?: string[];
  /** Pre-fill form values */
  defaults?: Partial<WizardResult>;
  /** Called when the user clicks Activate / Create */
  onComplete: (result: WizardResult) => void;
  isPending: boolean;
}

// ── Steps ────────────────────────────────────────────────────────────────────

type WizardStep = "name" | "provider" | "apikey" | "model" | "skills" | "channels" | "confirm" | "channelAction";

function stepsForMode(mode: "instance" | "persona"): WizardStep[] {
  if (mode === "instance") {
    return ["name", "provider", "apikey", "model", "skills", "channels", "confirm", "channelAction"];
  }
  return ["provider", "apikey", "model", "skills", "channels", "confirm", "channelAction"];
}

// ── Step indicator ───────────────────────────────────────────────────────────

function StepIndicator({ steps, current }: { steps: WizardStep[]; current: WizardStep }) {
  const labels: Record<WizardStep, string> = {
    name: "Name",
    provider: "Provider",
    apikey: "Auth",
    model: "Model",
    skills: "Skills",
    channels: "Channels",
    confirm: "Confirm",
    channelAction: "Finalize",
  };
  const icons: Record<WizardStep, React.ReactNode> = {
    name: <Server className="h-3.5 w-3.5" />,
    provider: <Bot className="h-3.5 w-3.5" />,
    apikey: <Key className="h-3.5 w-3.5" />,
    model: <Sparkles className="h-3.5 w-3.5" />,
    skills: <Wrench className="h-3.5 w-3.5" />,
    channels: <MessageSquare className="h-3.5 w-3.5" />,
    confirm: <Check className="h-3.5 w-3.5" />,
    channelAction: <Key className="h-3.5 w-3.5" />,
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
  availableSkills = [],
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
    skills: defaults?.skills || [],
    channels: defaults?.channels || Object.keys(defaults?.channelConfigs || {}),
    channelConfigs: defaults?.channelConfigs || {},
  });
  const [channelActionIdx, setChannelActionIdx] = useState(0);

  const stepIdx = steps.indexOf(step);

  const canNext = (() => {
    switch (step) {
      case "name":
        return !!form.name.trim();
      case "provider":
        return !!form.provider;
      case "apikey":
        if (form.provider === "ollama") return true;
        return !!form.secretName || !!form.apiKey;
      case "model":
        return !!form.model;
      case "skills":
        return true;
      case "channelAction":
        return true;
      default:
        return true;
    }
  })();

  const actionChannels = useMemo(
    () => form.channels.filter((c) => c !== "whatsapp"),
    [form.channels]
  );
  const hasActionChannels = actionChannels.length > 0;

  function next() {
    if (step === "confirm") {
      if (hasActionChannels) {
        setChannelActionIdx(0);
        setStep("channelAction");
      } else {
        onComplete(form);
      }
      return;
    }
    if (step === "channelAction") {
      if (channelActionIdx < actionChannels.length - 1) {
        setChannelActionIdx(channelActionIdx + 1);
      } else {
        onComplete(form);
      }
      return;
    }
    if (stepIdx < steps.length - 1) setStep(steps[stepIdx + 1]);
  }
  function prev() {
    if (stepIdx > 0) setStep(steps[stepIdx - 1]);
  }

  function handleClose() {
    setStep(steps[0]);
    setChannelActionIdx(0);
    onClose();
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
      skills: d.skills || [],
      channels: d.channels || Object.keys(d.channelConfigs || {}),
      channelConfigs: d.channelConfigs || {},
    });
    setStep(steps[0]);
    setChannelActionIdx(0);
  }

  const defaultsKey = JSON.stringify(defaults || {});
  useEffect(() => {
    if (open) {
      resetWith(defaults || {});
    }
  }, [open, defaultsKey]);

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
              ? "Configure a new SympoziumInstance with provider, model, and skills."
              : "Configure provider, model, skills, and channels to activate this persona pack."}
          </DialogDescription>
        </DialogHeader>

        <StepIndicator steps={steps.filter((s) => s !== "channelAction")} current={step === "channelAction" ? "confirm" : step} />

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

        {/* ── Skills step ───────────────────────────────────────────── */}
        {step === "skills" && (
          <div className="space-y-3">
            <p className="text-sm text-muted-foreground">
              Select SkillPacks to attach.
            </p>
            {availableSkills.length === 0 ? (
              <p className="rounded-md border border-border/50 bg-muted/20 px-3 py-2 text-xs text-muted-foreground">
                No SkillPacks found in cluster.
              </p>
            ) : (
              <ScrollArea className="h-52 rounded-md border border-border/50">
                <div className="p-1 space-y-1">
                  {availableSkills.map((skill) => {
                    const selected = form.skills.includes(skill);
                    return (
                      <button
                        key={skill}
                        type="button"
                        onClick={() => {
                          const next = selected
                            ? form.skills.filter((s) => s !== skill)
                            : [...form.skills, skill];
                          setForm({ ...form, skills: next });
                        }}
                        className={cn(
                          "flex w-full items-center justify-between rounded-md border px-2.5 py-2 text-left text-xs transition-colors",
                          selected
                            ? "border-indigo-500/40 bg-indigo-500/15 text-indigo-300"
                            : "border-transparent hover:border-border/60 hover:bg-white/5"
                        )}
                      >
                        <span className="font-mono">{skill}</span>
                        <span className="text-[10px]">{selected ? "Selected" : "Select"}</span>
                      </button>
                    );
                  })}
                </div>
              </ScrollArea>
            )}
            <p className="text-xs text-muted-foreground">
              {form.skills.length > 0
                ? `${form.skills.length} skill${form.skills.length === 1 ? "" : "s"} selected`
                : "No skills selected"}
            </p>
          </div>
        )}

        {/* ── Channels step ─────────────────────────────────────────── */}
        {step === "channels" && (
          <div className="space-y-4">
            <p className="text-sm text-muted-foreground">
              Select channels to bind. Channel-specific setup happens after confirmation.
            </p>
            {CHANNELS.map((ch) => (
              <button
                key={ch.value}
                type="button"
                onClick={() => {
                  const selected = form.channels.includes(ch.value);
                  const nextChannels = selected
                    ? form.channels.filter((c) => c !== ch.value)
                    : [...form.channels, ch.value];
                  const nextConfigs = { ...form.channelConfigs };
                  if (selected) {
                    delete nextConfigs[ch.value];
                  }
                  setForm({ ...form, channels: nextChannels, channelConfigs: nextConfigs });
                }}
                className={cn(
                  "flex w-full items-center justify-between rounded-md border px-3 py-2 text-left text-sm transition-colors",
                  form.channels.includes(ch.value)
                    ? "border-indigo-500/40 bg-indigo-500/15 text-indigo-300"
                    : "border-border/50 hover:bg-white/5"
                )}
              >
                <span>{ch.label}</span>
                <span className="text-xs">{form.channels.includes(ch.value) ? "Selected" : "Select"}</span>
              </button>
            ))}
            {form.channels.includes("whatsapp") && (
              <p className="text-xs text-muted-foreground">
                WhatsApp setup will open a QR pairing modal after creation/activation.
              </p>
            )}
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
              <div className="flex justify-between gap-4">
                <span className="text-muted-foreground">Skills</span>
                <span className="font-mono text-right">
                  {form.skills.length > 0 ? form.skills.join(", ") : "—"}
                </span>
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
              {form.channels.length > 0 && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Channels</span>
                  <span>{form.channels.join(", ")}</span>
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

        {/* ── Channel action step (post-confirm) ───────────────────── */}
        {step === "channelAction" && (
          <div className="space-y-4">
            {actionChannels.length === 0 ? (
              <p className="text-sm text-muted-foreground">
                No additional channel setup required.
              </p>
            ) : (
              <>
                <p className="text-sm text-muted-foreground">
                  Channel-specific setup ({channelActionIdx + 1}/{actionChannels.length})
                </p>
                <div className="space-y-2">
                  <Label>
                    {actionChannels[channelActionIdx]} Secret Name
                  </Label>
                  <Input
                    value={form.channelConfigs[actionChannels[channelActionIdx]] || ""}
                    onChange={(e) => {
                      const ch = actionChannels[channelActionIdx];
                      const configs = { ...form.channelConfigs };
                      if (e.target.value.trim()) {
                        configs[ch] = e.target.value.trim();
                      } else {
                        delete configs[ch];
                      }
                      setForm({ ...form, channelConfigs: configs });
                    }}
                    placeholder={`${mode === "persona" ? targetName : form.name}-${actionChannels[channelActionIdx]}-secret`}
                    className="h-8 text-sm font-mono"
                    autoFocus
                  />
                  <p className="text-xs text-muted-foreground">
                    Use an existing secret that contains the channel token.
                  </p>
                </div>
              </>
            )}
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

          {step === "confirm" || step === "channelAction" ? (
            <Button
              size="sm"
              className="gap-1 bg-gradient-to-r from-indigo-500 to-purple-600 hover:from-indigo-600 hover:to-purple-700 text-white border-0"
              onClick={next}
              disabled={isPending}
            >
              {isPending ? (
                "Working…"
              ) : (
                <>
                  {step === "channelAction" && channelActionIdx < actionChannels.length - 1 ? (
                    <>
                      Next Channel <ChevronRight className="h-4 w-4" />
                    </>
                  ) : step === "confirm" && hasActionChannels ? (
                    <>
                      Finalize Channels <ChevronRight className="h-4 w-4" />
                    </>
                  ) : (
                    <>
                      {completeIcon} {completeLabel}
                    </>
                  )}
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
