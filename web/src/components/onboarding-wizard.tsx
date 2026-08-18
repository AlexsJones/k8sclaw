import { useEffect, useMemo, useRef, useState } from "react";
import { useModelList } from "@/hooks/use-model-list";
import { useProviderNodes } from "@/hooks/use-provider-nodes";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
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
  Clock,
  Cpu,
  FileCode,
  Cloud,
  Terminal,
  Settings,
  Wifi,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { useCapabilities, useModels } from "@/hooks/use-api";
import { api } from "@/lib/api";
import {
  YamlModal,
  instanceYamlFromWizard,
  ensembleYamlFromWizard,
} from "@/components/yaml-panel";

// ── Shared constants ─────────────────────────────────────────────────────────

// Provider icon components (inline SVGs for brands, lucide for generic)
const OpenAIIcon = ({ className }: { className?: string }) => (
  <svg viewBox="0 0 24 24" fill="currentColor" className={className}>
    <path d="M22.282 9.821a5.985 5.985 0 0 0-.516-4.91 6.046 6.046 0 0 0-6.51-2.9A6.065 6.065 0 0 0 4.981 4.18a5.985 5.985 0 0 0-3.998 2.9 6.046 6.046 0 0 0 .743 7.097 5.98 5.98 0 0 0 .51 4.911 6.051 6.051 0 0 0 6.515 2.9A5.985 5.985 0 0 0 13.26 24a6.056 6.056 0 0 0 5.772-4.206 5.99 5.99 0 0 0 3.997-2.9 6.056 6.056 0 0 0-.747-7.073zM13.26 22.43a4.476 4.476 0 0 1-2.876-1.04l.141-.081 4.779-2.758a.795.795 0 0 0 .392-.681v-6.737l2.02 1.168a.071.071 0 0 1 .038.052v5.583a4.504 4.504 0 0 1-4.494 4.494zM3.6 18.304a4.47 4.47 0 0 1-.535-3.014l.142.085 4.783 2.759a.771.771 0 0 0 .78 0l5.843-3.369v2.332a.08.08 0 0 1-.033.062L9.74 19.95a4.5 4.5 0 0 1-6.14-1.646zM2.34 7.896a4.485 4.485 0 0 1 2.366-1.973V11.6a.766.766 0 0 0 .388.676l5.815 3.355-2.02 1.168a.076.076 0 0 1-.071 0l-4.83-2.786A4.504 4.504 0 0 1 2.34 7.872zm16.597 3.855l-5.833-3.387L15.119 7.2a.076.076 0 0 1 .071 0l4.83 2.791a4.494 4.494 0 0 1-.676 8.105v-5.678a.79.79 0 0 0-.407-.667zm2.01-3.023l-.141-.085-4.774-2.782a.776.776 0 0 0-.785 0L9.409 9.23V6.897a.066.066 0 0 1 .028-.061l4.83-2.787a4.5 4.5 0 0 1 6.68 4.66zm-12.64 4.135l-2.02-1.164a.08.08 0 0 1-.038-.057V6.075a4.5 4.5 0 0 1 7.375-3.453l-.142.08L8.704 5.46a.795.795 0 0 0-.393.681zm1.097-2.365l2.602-1.5 2.607 1.5v2.999l-2.597 1.5-2.607-1.5z" />
  </svg>
);

const AnthropicIcon = ({ className }: { className?: string }) => (
  <svg viewBox="0 0 24 24" fill="currentColor" className={className}>
    <path d="M13.827 3.52h3.603L24 20.48h-3.603l-6.57-16.96zm-7.258 0h3.767L16.906 20.48h-3.674l-1.587-4.29H5.647l-1.588 4.29H.48L6.569 3.52zm1.04 3.79L5.2 13.48h4.92L7.61 7.31z" />
  </svg>
);

const AWSIcon = ({ className }: { className?: string }) => (
  <svg viewBox="0 0 24 24" fill="currentColor" className={className}>
    <path d="M6.763 10.036c0 .296.032.535.088.71.064.176.144.368.256.576.04.063.056.127.056.183 0 .08-.048.16-.152.24l-.503.335a.383.383 0 0 1-.208.072c-.08 0-.16-.04-.239-.112a2.47 2.47 0 0 1-.287-.374 6.18 6.18 0 0 1-.248-.47c-.622.734-1.405 1.101-2.347 1.101-.67 0-1.205-.191-1.596-.574-.391-.384-.59-.894-.59-1.533 0-.678.239-1.23.726-1.644.487-.415 1.133-.623 1.955-.623.272 0 .551.024.846.064.296.04.6.104.918.176v-.583c0-.607-.127-1.03-.375-1.277-.255-.248-.686-.367-1.3-.367-.28 0-.568.032-.863.104-.296.072-.583.16-.863.272a2.287 2.287 0 0 1-.28.104.488.488 0 0 1-.127.024c-.112 0-.168-.08-.168-.247v-.391c0-.128.016-.224.056-.28a.597.597 0 0 1 .224-.167c.279-.144.614-.264 1.005-.36a4.84 4.84 0 0 1 1.246-.152c.95 0 1.644.216 2.091.647.439.43.662 1.085.662 1.963v2.586zm-3.24 1.214c.263 0 .534-.048.822-.144.287-.096.543-.271.758-.51.128-.152.224-.32.272-.512.047-.191.08-.423.08-.694v-.335a6.66 6.66 0 0 0-.735-.136 6.02 6.02 0 0 0-.75-.048c-.535 0-.926.104-1.19.32-.263.215-.39.518-.39.917 0 .375.095.655.295.846.191.2.47.296.838.296zm6.41.862c-.144 0-.24-.024-.304-.08-.064-.048-.12-.16-.168-.311L7.586 5.55a1.398 1.398 0 0 1-.072-.32c0-.128.064-.2.191-.2h.783c.151 0 .255.025.31.08.065.048.113.16.16.312l1.342 5.284 1.245-5.284c.04-.16.088-.264.151-.312a.549.549 0 0 1 .32-.08h.638c.152 0 .256.025.32.08.063.048.12.16.151.312l1.261 5.348 1.381-5.348c.048-.16.104-.264.16-.312a.52.52 0 0 1 .311-.08h.743c.127 0 .2.065.2.2 0 .04-.009.08-.017.128a1.137 1.137 0 0 1-.056.2l-1.923 6.17c-.048.16-.104.264-.168.312a.549.549 0 0 1-.312.08h-.687c-.151 0-.255-.024-.32-.08-.063-.056-.119-.16-.15-.32l-1.238-5.148-1.23 5.14c-.04.16-.087.264-.15.32-.065.056-.177.08-.32.08zm10.256.215c-.415 0-.83-.048-1.229-.143-.399-.096-.71-.2-.918-.32-.128-.071-.216-.151-.248-.215a.51.51 0 0 1-.048-.224v-.407c0-.167.064-.247.183-.247a.45.45 0 0 1 .144.024c.048.016.12.048.2.08.271.12.566.215.878.279.319.064.63.096.95.096.502 0 .894-.088 1.165-.264a.86.86 0 0 0 .415-.758.777.777 0 0 0-.215-.559c-.144-.151-.415-.287-.806-.415l-1.157-.36c-.583-.183-1.014-.454-1.277-.813a1.902 1.902 0 0 1-.4-1.158c0-.335.073-.63.216-.886.144-.255.335-.479.575-.654.24-.184.51-.32.83-.415a3.57 3.57 0 0 1 1.005-.136c.175 0 .359.008.535.032.183.024.35.056.518.088.16.04.312.08.455.127.144.048.256.096.336.144a.69.69 0 0 1 .24.2.43.43 0 0 1 .071.263v.375c0 .168-.064.256-.184.256a.83.83 0 0 1-.303-.096 3.652 3.652 0 0 0-1.532-.311c-.455 0-.815.071-1.062.223-.248.152-.375.383-.375.695 0 .224.08.416.24.567.16.152.454.304.877.44l1.134.358c.574.184.99.44 1.237.767.247.327.367.702.367 1.117 0 .343-.072.655-.207.926-.144.272-.336.511-.583.703-.248.2-.543.343-.886.447-.36.111-.742.167-1.142.167z" />
    <path d="M21.698 16.207c-2.626 1.94-6.442 2.969-9.722 2.969-4.598 0-8.74-1.7-11.87-4.526-.247-.223-.025-.527.27-.351 3.384 1.963 7.559 3.153 11.877 3.153 2.914 0 6.114-.607 9.06-1.852.439-.2.814.287.385.607z" />
    <path d="M22.792 14.961c-.336-.43-2.22-.207-3.074-.103-.255.032-.295-.192-.063-.36 1.5-1.053 3.967-.75 4.254-.399.287.36-.08 2.826-1.485 4.007-.216.184-.423.088-.327-.151.319-.79 1.03-2.57.695-2.994z" />
  </svg>
);

const LlamaIcon = ({ className }: { className?: string }) => (
  <svg viewBox="0 0 24 24" fill="currentColor" className={className}>
    <path d="M8.32 2c-.094.004-.17.072-.21.156L6.468 5.84l-.183.426c-.6-.135-1.24-.197-1.856-.127A3.77 3.77 0 0 0 2.2 7.32C1.39 8.23.97 9.462.97 10.706v.426c.012.3.022.6.07.898.186 1.166.66 2.238 1.413 3.107.025.39.087.778.182 1.16.353 1.404 1.065 2.628 2.02 3.502.17.505.398.983.688 1.41.518.762 1.228 1.35 2.084 1.628.094.137.196.26.303.38.56.616 1.283.862 2.007.783h.465c.724.08 1.447-.167 2.007-.782.107-.12.21-.244.303-.381.856-.279 1.566-.866 2.084-1.628.29-.427.518-.905.688-1.41.955-.874 1.667-2.098 2.02-3.502a7.34 7.34 0 0 0 .182-1.16c.754-.87 1.227-1.94 1.414-3.107.047-.299.057-.599.07-.898v-.426c0-1.244-.42-2.477-1.232-3.385a3.77 3.77 0 0 0-2.228-1.183 5.08 5.08 0 0 0-1.856.127l-.183-.426L13.84 2.156A.27.27 0 0 0 13.63 2h-.17a.27.27 0 0 0-.243.182l-.735 1.862-.247-.627A.27.27 0 0 0 11.99 3.2h-.17a.27.27 0 0 0-.17.07.27.27 0 0 0-.073.112l-.735 1.862-.247-.627A.27.27 0 0 0 10.35 4.4h-.17a.27.27 0 0 0-.243.182L9.202 6.44l-.247-.627A.27.27 0 0 0 8.71 5.6h-.17a.27.27 0 0 0-.22.127z" />
  </svg>
);

const OllamaIcon = ({ className }: { className?: string }) => (
  <svg viewBox="0 0 24 24" fill="currentColor" className={className}>
    <path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm0 3c1.66 0 3 1.34 3 3s-1.34 3-3 3-3-1.34-3-3 1.34-3 3-3zm0 14.2c-2.5 0-4.71-1.28-6-3.22.03-1.99 4-3.08 6-3.08 1.99 0 5.97 1.09 6 3.08-1.29 1.94-3.5 3.22-6 3.22z" />
  </svg>
);

export const PROVIDERS = [
  {
    value: "openai",
    label: "OpenAI",
    defaultModel: "gpt-4o",
    defaultBaseURL: "",
    icon: OpenAIIcon,
  },
  {
    value: "anthropic",
    label: "Anthropic",
    defaultModel: "claude-sonnet-4-20250514",
    defaultBaseURL: "",
    icon: AnthropicIcon,
  },
  {
    value: "azure-openai",
    label: "Azure OpenAI",
    defaultModel: "gpt-4o",
    defaultBaseURL: "",
    icon: Cloud,
  },
  {
    value: "ollama",
    label: "Ollama",
    defaultModel: "llama3",
    defaultBaseURL: "http://ollama.default.svc:11434/v1",
    icon: OllamaIcon,
  },
  {
    value: "lm-studio",
    label: "LM Studio",
    defaultModel: "",
    defaultBaseURL: "http://localhost:1234/v1",
    icon: Cpu,
  },
  {
    value: "llama-server",
    label: "llama-server",
    defaultModel: "",
    defaultBaseURL: "http://localhost:8080/v1",
    icon: LlamaIcon,
  },
  {
    value: "unsloth",
    label: "Unsloth",
    defaultModel: "",
    defaultBaseURL: "http://localhost:8080/v1",
    icon: Terminal,
  },
  {
    value: "bedrock",
    label: "AWS Bedrock",
    defaultModel: "anthropic.claude-sonnet-4-20250514-v1:0",
    defaultBaseURL: "",
    icon: AWSIcon,
  },
  {
    value: "custom",
    label: "Custom",
    defaultModel: "",
    defaultBaseURL: "",
    icon: Settings,
  },
];

const CHANNELS = [
  { value: "discord", label: "Discord" },
  { value: "slack", label: "Slack" },
  { value: "telegram", label: "Telegram" },
  { value: "whatsapp", label: "WhatsApp" },
];

// ── Types ────────────────────────────────────────────────────────────────────

const HEARTBEAT_INTERVALS = [
  { value: "30m", label: "Every 30 minutes" },
  { value: "1h", label: "Every hour" },
  { value: "6h", label: "Every 6 hours" },
  { value: "24h", label: "Once a day" },
];

function heartbeatOptions(mode: "agent" | "persona" | "canary") {
  return [
    {
      value: "",
      label: mode === "persona" ? "Ensemble default" : "No heartbeat",
    },
    ...HEARTBEAT_INTERVALS,
  ];
}

export interface WizardResult {
  name: string;
  provider: string;
  apiKey: string;
  secretName: string;
  awsRegion: string;
  awsAccessKeyId: string;
  awsSecretAccessKey: string;
  awsSessionToken: string;
  model: string;
  baseURL: string;
  skills: string[];
  channels: string[];
  channelConfigs: Record<string, string>;
  heartbeatInterval: string;
  /** Web endpoint rate limit (requests per minute) when web-endpoint skill is selected */
  webEndpointRPM?: string;
  /** Custom hostname for web endpoint HTTPRoute */
  webEndpointHostname?: string;
  /** GitHub repository (owner/repo) when github-gitops skill is selected */
  githubRepo?: string;
  /** GitHub personal access token for the github-gitops skill */
  githubToken?: string;
  /** Team instructions propagated into each instance's memory */
  githubTeamInstructions?: string;
  /** Node selector for pinning agent pods to specific nodes */
  nodeSelector?: Record<string, string>;
  /** Enable Agent Sandbox (kernel-level isolation via gVisor/Kata) */
  agentSandboxEnabled?: boolean;
  /** Runtime class for Agent Sandbox (e.g., "gvisor", "kata") */
  agentSandboxRuntimeClass?: string;
  /** Maximum duration per agent run (e.g., "30m", "1h"). Defaults to 10m cloud / 30m local. */
  runTimeout?: string;
  /** Require manual approval before agent responses are delivered. */
  requireApproval?: boolean;
  /** References a cluster-local Model CR for inference (no API key needed). */
  modelRef?: string;
}

interface OnboardingWizardProps {
  open: boolean;
  onClose: () => void;
  /** "agent" shows a Name step first; "persona" skips it; "canary" shows only provider/apikey/model */
  mode: "agent" | "persona" | "canary";
  /** Display name shown in the dialog title */
  targetName?: string;
  /** Number of personas in the pack (persona mode only) */
  agentConfigCount?: number;
  /** Available SkillPacks to choose from */
  availableSkills?: string[];
  /** Pre-fill form values */
  defaults?: Partial<WizardResult>;
  /** Called when the user clicks Activate / Create */
  onComplete: (result: WizardResult) => void;
  isPending: boolean;
}

// ── Steps ────────────────────────────────────────────────────────────────────

type WizardStep =
  | "name"
  | "provider"
  | "apikey"
  | "model"
  | "skills"
  | "heartbeat"
  | "channels"
  | "confirm"
  | "channelAction";

function stepsForMode(mode: "agent" | "persona" | "canary"): WizardStep[] {
  if (mode === "canary") {
    return ["provider", "apikey", "model"];
  }
  if (mode === "agent") {
    return [
      "name",
      "provider",
      "apikey",
      "model",
      "skills",
      "heartbeat",
      "channels",
      "confirm",
      "channelAction",
    ];
  }
  return [
    "provider",
    "apikey",
    "model",
    "skills",
    "heartbeat",
    "channels",
    "confirm",
    "channelAction",
  ];
}

// ── Step indicator ───────────────────────────────────────────────────────────

function StepIndicator({
  steps,
  current,
}: {
  steps: WizardStep[];
  current: WizardStep;
}) {
  const labels: Record<WizardStep, string> = {
    name: "Name",
    provider: "Provider",
    apikey: "Auth",
    model: "Model",
    skills: "Skills",
    heartbeat: "Heartbeat",
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
    heartbeat: <Clock className="h-3.5 w-3.5" />,
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
                ? "bg-blue-500/20 text-blue-400"
                : i === idx
                  ? "bg-blue-500 text-white"
                  : "bg-muted text-muted-foreground",
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
  baseURL,
  value,
  onChange,
  bedrockCredentials,
}: {
  provider: string;
  apiKey: string;
  baseURL?: string;
  value: string;
  onChange: (v: string) => void;
  bedrockCredentials?: import("@/hooks/use-model-list").BedrockCredentials;
}) {
  const { models, isLoading, isLive } = useModelList(
    provider,
    apiKey,
    baseURL,
    bedrockCredentials,
  );
  const [search, setSearch] = useState("");

  const filtered = models.filter((m) =>
    m.toLowerCase().includes(search.toLowerCase()),
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
                      ? "bg-blue-500/15 text-blue-400 border border-blue-500/30"
                      : "text-foreground hover:bg-white/5 border border-transparent",
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

// ── Canary connection test ───────────────────────────────────────────────────

function CanaryConnectionTest({ baseURL }: { baseURL: string }) {
  const [result, setResult] = useState<{
    reachable?: boolean;
    error?: string;
    models?: number;
    testing?: boolean;
  }>({});

  async function runTest() {
    setResult({ testing: true });
    try {
      // Use the same in-cluster proxy endpoint that model listing uses,
      // so the test exercises the real network path (pod → provider).
      const res = await api.providers.models(baseURL);
      setResult({
        reachable: true,
        models: res.models.length,
      });
    } catch (err) {
      setResult({ reachable: false, error: String(err) });
    }
  }

  return (
    <div className="space-y-2 pt-2">
      <div className="flex items-center gap-2">
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={runTest}
          disabled={result.testing}
          className="text-xs"
        >
          {result.testing ? (
            <>
              <Loader2 className="h-3 w-3 animate-spin mr-1" />
              Testing...
            </>
          ) : (
            <>
              <Wifi className="h-3 w-3 mr-1" />
              Test Connection
            </>
          )}
        </Button>
        {result.reachable === true && (
          <span className="flex items-center gap-1 text-xs text-green-400">
            <Check className="h-3 w-3" />
            Reachable
            {result.models !== undefined &&
              result.models > 0 &&
              ` · ${result.models} model${result.models === 1 ? "" : "s"}`}
          </span>
        )}
        {result.reachable === false && (
          <span className="text-xs text-red-400 truncate max-w-xs" title={result.error}>
            Not reachable{result.error ? `: ${result.error.split(":").slice(-1)[0].trim()}` : ""}
          </span>
        )}
      </div>
      <p className="text-[10px] text-muted-foreground">
        Tests if the provider URL is reachable from inside the cluster.
      </p>
    </div>
  );
}

// ── Main wizard component ────────────────────────────────────────────────────

export function OnboardingWizard({
  open,
  onClose,
  mode,
  targetName,
  agentConfigCount,
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
    skills: Array.from(new Set([...(defaults?.skills || []), "memory"])),
    channels: defaults?.channels || Object.keys(defaults?.channelConfigs || {}),
    channelConfigs: defaults?.channelConfigs || {},
    heartbeatInterval: defaults?.heartbeatInterval || "",
    webEndpointRPM: defaults?.webEndpointRPM || "60",
    webEndpointHostname: defaults?.webEndpointHostname || "",
    githubRepo: defaults?.githubRepo || "",
    githubToken: defaults?.githubToken || "",
    githubTeamInstructions: defaults?.githubTeamInstructions || "",
    nodeSelector: defaults?.nodeSelector,
    agentSandboxEnabled: defaults?.agentSandboxEnabled ?? false,
    agentSandboxRuntimeClass: defaults?.agentSandboxRuntimeClass || "gvisor",
    runTimeout: defaults?.runTimeout || "",
    requireApproval: defaults?.requireApproval ?? false,
    awsRegion: defaults?.awsRegion || "",
    awsAccessKeyId: defaults?.awsAccessKeyId || "",
    awsSecretAccessKey: defaults?.awsSecretAccessKey || "",
    awsSessionToken: defaults?.awsSessionToken || "",
  });
  const [inferenceMode, setInferenceMode] = useState<"workload" | "node">(
    "workload",
  );
  const [channelActionIdx, setChannelActionIdx] = useState(0);
  const [showYaml, setShowYaml] = useState(false);
  const { data: capabilities } = useCapabilities();
  const { data: clusterModels } = useModels();
  const [usingLocalModel, setUsingLocalModel] = useState(false);
  const readyModels = (clusterModels || []).filter(
    (m) => m.status?.phase === "Ready",
  );

  const isLocalProvider =
    form.provider === "ollama" ||
    form.provider === "lm-studio" ||
    form.provider === "llama-server" ||
    form.provider === "unsloth" ||
    form.provider === "custom";
  // Unsloth is served via llama.cpp's llama-server or vLLM, both of which
  // are already probed by node-probe under their own target names. When the
  // user picks "unsloth" in the UI, match nodes that expose either of those.
  const nodeProviderMatches = (probeName: string) => {
    if (form.provider === "custom") return true;
    if (form.provider === "unsloth") {
      return (
        probeName === "unsloth" ||
        probeName === "llama-cpp" ||
        probeName === "vllm"
      );
    }
    if (form.provider === "llama-server") {
      return probeName === "llama-cpp";
    }
    return probeName === form.provider;
  };
  const { data: providerNodes, isLoading: nodesLoading } =
    useProviderNodes(isLocalProvider);
  // Track whether the user manually toggled inference mode so we don't override it.
  const userOverrodeInferenceMode = useRef(false);
  // Auto-switch to "node" mode when matching providers are discovered.
  useEffect(() => {
    if (
      !isLocalProvider ||
      nodesLoading ||
      !providerNodes ||
      userOverrodeInferenceMode.current
    )
      return;
    const hasMatch = providerNodes.some((n) =>
      n.providers.some((p) => nodeProviderMatches(p.name)),
    );
    if (hasMatch) {
      setInferenceMode("node");
    }
  }, [providerNodes, nodesLoading, form.provider]);
  // Reset the override flag when the provider changes.
  useEffect(() => {
    userOverrodeInferenceMode.current = false;
  }, [form.provider]);

  const stepIdx = steps.indexOf(step);

  // RFC 1123 subdomain: lowercase alphanumeric, '-' or '.', must start/end alphanumeric.
  const rfc1123Re = /^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$/;
  const nameValid =
    form.name.length > 0 &&
    form.name.length <= 253 &&
    rfc1123Re.test(form.name);
  const nameError =
    form.name.length > 0 && !nameValid
      ? "Must be lowercase alphanumeric, '-' or '.', and start/end with alphanumeric (RFC 1123)"
      : "";

  const canNext = (() => {
    switch (step) {
      case "name":
        return nameValid;
      case "provider":
        return !!form.provider;
      case "apikey":
        if (
          form.provider === "ollama" ||
          form.provider === "lm-studio" ||
          form.provider === "llama-server" ||
          form.provider === "unsloth" ||
          // Custom endpoints are commonly self-hosted OpenAI-compatible
          // servers (for example a node-probed llama-server). Credentials
          // are optional for those servers, but remain available below for
          // custom endpoints that do require authentication.
          form.provider === "custom"
        )
          return true;
        if (form.provider === "bedrock")
          return !!form.secretName || !!form.awsRegion;
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
    [form.channels],
  );
  const hasActionChannels = actionChannels.length > 0;

  function completeWithDefaults() {
    // Apply default baseURL for local providers if the user left it empty.
    const result = { ...form };
    if (!result.baseURL) {
      const prov = PROVIDERS.find((p) => p.value === result.provider);
      if (prov?.defaultBaseURL) {
        result.baseURL = prov.defaultBaseURL;
      }
    }
    onComplete(result);
  }

  function next() {
    if (step === "confirm") {
      if (hasActionChannels) {
        setChannelActionIdx(0);
        setStep("channelAction");
      } else {
        completeWithDefaults();
      }
      return;
    }
    if (step === "channelAction") {
      if (channelActionIdx < actionChannels.length - 1) {
        setChannelActionIdx(channelActionIdx + 1);
      } else {
        completeWithDefaults();
      }
      return;
    }
    // Skip apikey and model steps when using a cluster-local Model
    let nextIdx = stepIdx + 1;
    while (
      nextIdx < steps.length &&
      usingLocalModel &&
      (steps[nextIdx] === "apikey" || steps[nextIdx] === "model")
    ) {
      nextIdx++;
    }
    if (nextIdx < steps.length) {
      setStep(steps[nextIdx]);
    } else {
      completeWithDefaults();
    }
  }
  function prev() {
    // Skip apikey and model steps when going back too
    let prevIdx = stepIdx - 1;
    while (
      prevIdx >= 0 &&
      usingLocalModel &&
      (steps[prevIdx] === "apikey" || steps[prevIdx] === "model")
    ) {
      prevIdx--;
    }
    if (prevIdx >= 0) setStep(steps[prevIdx]);
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
      heartbeatInterval: d.heartbeatInterval || "",
      webEndpointRPM: d.webEndpointRPM || "60",
      webEndpointHostname: d.webEndpointHostname || "",
      githubRepo: d.githubRepo || "",
      githubToken: d.githubToken || "",
      githubTeamInstructions: d.githubTeamInstructions || "",
      nodeSelector: d.nodeSelector,
      awsRegion: d.awsRegion || "",
      awsAccessKeyId: d.awsAccessKeyId || "",
      awsSecretAccessKey: d.awsSecretAccessKey || "",
      awsSessionToken: d.awsSessionToken || "",
    });
    setStep(steps[0]);
    setChannelActionIdx(0);
    setInferenceMode("workload");
  }

  const defaultsKey = JSON.stringify(defaults || {});
  useEffect(() => {
    if (open) {
      resetWith(defaults || {});
    }
  }, [open, defaultsKey]);

  const titleIcon =
    mode === "agent" ? (
      <Server className="h-5 w-5 text-blue-400" />
    ) : (
      <Sparkles className="h-5 w-5 text-blue-400" />
    );
  const titleText =
    mode === "canary"
      ? "Configure System Canary"
      : mode === "agent"
        ? "Create Agent"
        : `Enable ${targetName || "Pack"}`;
  const completeLabel =
    mode === "canary" ? "Start Canary" : mode === "agent" ? "Create" : "Activate";
  const completeIcon =
    mode === "canary" ? (
      <Power className="h-4 w-4" />
    ) : mode === "agent" ? (
      <Server className="h-4 w-4" />
    ) : (
      <Power className="h-4 w-4" />
    );

  return (
    <Dialog open={open} onOpenChange={(v) => !v && handleClose()}>
      <DialogContent className="sm:max-w-2xl overflow-hidden">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            {titleIcon}
            {mode === "canary"
              ? "Configure System Canary"
              : mode === "persona"
                ? (
                  <>
                    Enable{" "}
                    <span className="font-mono text-blue-400">{targetName}</span>
                  </>
                )
                : "Create Agent"}
          </DialogTitle>
          <DialogDescription>
            {mode === "canary"
              ? "Choose a provider and model for the system health canary."
              : mode === "agent"
                ? "Configure a new Agent with provider, model, and skills."
                : "Configure provider, model, skills, and channels to activate this ensemble."}
          </DialogDescription>
        </DialogHeader>

        <StepIndicator
          steps={steps.filter((s) => s !== "channelAction")}
          current={step === "channelAction" ? "confirm" : step}
        />

        {/* ── Name step (instance only) ─────────────────────────────── */}
        {step === "name" && (
          <div className="space-y-4">
            <div className="space-y-2">
              <Label>Agent Name</Label>
              <Input
                value={form.name}
                onChange={(e) => {
                  // Auto-sanitize: lowercase, replace spaces/underscores with hyphens.
                  const v = e.target.value
                    .toLowerCase()
                    .replace(/[\s_]+/g, "-");
                  setForm({ ...form, name: v });
                }}
                placeholder="my-agent"
                autoFocus
                className={nameError ? "border-red-500" : ""}
              />
              {nameError && <p className="text-xs text-red-500">{nameError}</p>}
            </div>
          </div>
        )}

        {/* ── Provider step ─────────────────────────────────────────── */}
        {step === "provider" && (
          <div className="space-y-4">
            <div className="space-y-2">
              <Label>AI Provider</Label>
              <Select
                value={usingLocalModel ? `model:${form.model}` : form.provider}
                onValueChange={(v) => {
                  if (v.startsWith("model:")) {
                    // Cluster-local Model selected
                    const modelName = v.slice(6);
                    const model = readyModels.find(
                      (m) => m.metadata.name === modelName,
                    );
                    if (model) {
                      setUsingLocalModel(true);
                      setForm({
                        ...form,
                        provider: "openai",
                        model: model.metadata.name,
                        baseURL: model.status?.endpoint || "",
                        apiKey: "",
                        secretName: "",
                        modelRef: model.metadata.name,
                      });
                    }
                  } else {
                    setUsingLocalModel(false);
                    const prov = PROVIDERS.find((p) => p.value === v);
                    setForm({
                      ...form,
                      provider: v,
                      model: form.model || prov?.defaultModel || "",
                      baseURL: prov?.defaultBaseURL || "",
                      modelRef: undefined,
                    });
                  }
                }}
              >
                <SelectTrigger>
                  <SelectValue placeholder="Select a provider…" />
                </SelectTrigger>
                <SelectContent>
                  {readyModels.length > 0 && (
                    <>
                      {readyModels.map((m) => (
                        <SelectItem
                          key={`model:${m.metadata.name}`}
                          value={`model:${m.metadata.name}`}
                        >
                          <span className="flex items-center gap-2">
                            <Cpu className="h-4 w-4 shrink-0 text-green-500" />
                            {m.metadata.name}
                            <span className="text-xs text-muted-foreground">
                              (Local Model)
                            </span>
                          </span>
                        </SelectItem>
                      ))}
                    </>
                  )}
                  {PROVIDERS.map((p) => (
                    <SelectItem key={p.value} value={p.value}>
                      <span className="flex items-center gap-2">
                        <p.icon className="h-4 w-4 shrink-0" />
                        {p.label}
                      </span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            {/* Inference mode toggle for local providers */}
            {isLocalProvider && (
              <div className="space-y-2">
                <Label>Inference Source</Label>
                <div className="flex gap-2">
                  <button
                    type="button"
                    onClick={() => {
                      userOverrodeInferenceMode.current = true;
                      setInferenceMode("workload");
                      setForm({ ...form, nodeSelector: undefined });
                    }}
                    className={cn(
                      "flex-1 flex items-center justify-center gap-1.5 rounded-md border px-3 py-2 text-xs transition-colors",
                      inferenceMode === "workload"
                        ? "border-blue-500/40 bg-blue-500/15 text-blue-300"
                        : "border-border/50 hover:bg-white/5",
                    )}
                  >
                    <Server className="h-3.5 w-3.5" /> In-cluster service
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      userOverrodeInferenceMode.current = true;
                      setInferenceMode("node");
                    }}
                    className={cn(
                      "flex-1 flex items-center justify-center gap-1.5 rounded-md border px-3 py-2 text-xs transition-colors",
                      inferenceMode === "node"
                        ? "border-blue-500/40 bg-blue-500/15 text-blue-300"
                        : "border-border/50 hover:bg-white/5",
                    )}
                  >
                    <Cpu className="h-3.5 w-3.5" /> Installed on node
                  </button>
                </div>
              </div>
            )}

            {/* In-cluster service: manual Base URL input */}
            {(form.provider === "azure-openai" ||
              (isLocalProvider && inferenceMode === "workload")) && (
              <div className="space-y-2">
                <Label>Base URL</Label>
                <Input
                  value={form.baseURL}
                  onChange={(e) =>
                    setForm({ ...form, baseURL: e.target.value })
                  }
                  placeholder={
                    form.provider === "ollama"
                      ? "http://ollama.default.svc:11434/v1"
                      : form.provider === "lm-studio"
                        ? "http://localhost:1234/v1"
                        : form.provider === "unsloth"
                          ? "http://localhost:8080/v1"
                          : "https://your-endpoint.openai.azure.com/v1"
                  }
                />
              </div>
            )}

            {/* Node-based: discover and select a node */}
            {isLocalProvider && inferenceMode === "node" && (
              <div className="space-y-2">
                <Label>Select Node</Label>
                {nodesLoading ? (
                  <div className="flex items-center gap-2 py-4 text-xs text-muted-foreground justify-center">
                    <Loader2 className="h-3.5 w-3.5 animate-spin" />
                    Discovering nodes...
                  </div>
                ) : !providerNodes ||
                  providerNodes.filter((n) =>
                    n.providers.some((p) => nodeProviderMatches(p.name)),
                  ).length === 0 ? (
                  <div className="rounded-md border border-border/50 bg-muted/20 px-3 py-3 text-xs text-muted-foreground">
                    {providerNodes && providerNodes.length > 0
                      ? `No nodes with ${form.provider} detected. Found other providers on ${providerNodes.length} node${providerNodes.length === 1 ? "" : "s"}.`
                      : "No nodes with inference providers detected. Is the node-probe DaemonSet enabled?"}
                  </div>
                ) : (
                  <ScrollArea className="h-40 rounded-md border border-border/50">
                    <div className="p-1 space-y-0.5">
                      {providerNodes
                        .filter((node) =>
                          node.providers.some((p) =>
                            nodeProviderMatches(p.name),
                          ),
                        )
                        .map((node) => {
                          const isSelected =
                            form.nodeSelector?.["kubernetes.io/hostname"] ===
                            node.nodeName;
                          const nodeProviders = node.providers
                            .filter(
                              (p) =>
                                !form.provider || nodeProviderMatches(p.name),
                            )
                            .map((p) => p.name);
                          const nodeModels = node.providers
                            .filter(
                              (p) =>
                                !form.provider || nodeProviderMatches(p.name),
                            )
                            .flatMap((p) => p.models);
                          const providerInfo =
                            node.providers.find((p) =>
                              nodeProviderMatches(p.name),
                            ) || node.providers[0];

                          return (
                            <button
                              key={node.nodeName}
                              type="button"
                              onClick={() => {
                                if (providerInfo) {
                                  // Use the node-probe reverse proxy when available,
                                  // so the cluster can reach host-installed providers.
                                  const base = providerInfo.proxyPort
                                    ? `http://${node.nodeIP}:${providerInfo.proxyPort}/proxy/${providerInfo.name}/v1`
                                    : `http://${node.nodeIP}:${providerInfo.port}/v1`;
                                  setForm({
                                    ...form,
                                    baseURL: base,
                                    nodeSelector: {
                                      "kubernetes.io/hostname": node.nodeName,
                                    },
                                  });
                                }
                              }}
                              className={cn(
                                "flex w-full items-start gap-2 rounded-md px-2.5 py-2 text-left text-xs transition-colors",
                                isSelected
                                  ? "bg-blue-500/15 text-blue-400 border border-blue-500/30"
                                  : "text-foreground hover:bg-white/5 border border-transparent",
                              )}
                            >
                              <Cpu className="h-3.5 w-3.5 mt-0.5 shrink-0" />
                              <div className="min-w-0">
                                <div className="font-mono truncate">
                                  {node.nodeName}
                                </div>
                                <div className="text-[10px] text-muted-foreground">
                                  {node.nodeIP} &middot;{" "}
                                  {nodeProviders.join(", ")}
                                  {nodeModels.length > 0 &&
                                    ` · ${nodeModels.length} model${nodeModels.length === 1 ? "" : "s"}`}
                                </div>
                              </div>
                              {isSelected && (
                                <Check className="h-3 w-3 shrink-0 mt-0.5 ml-auto" />
                              )}
                            </button>
                          );
                        })}
                    </div>
                  </ScrollArea>
                )}
              </div>
            )}
          </div>
        )}

        {/* ── Auth step ─────────────────────────────────────────────── */}
        {step === "apikey" && (
          <ScrollArea className="max-h-[60vh]">
            <div className="space-y-4">
              {form.provider !== "bedrock" &&
                form.provider !== "ollama" &&
                form.provider !== "lm-studio" &&
                form.provider !== "llama-server" &&
                form.provider !== "unsloth" && (
                  <div className="space-y-2">
                    <Label>
                      API Key
                      {form.provider === "custom" && (
                        <span className="text-muted-foreground font-normal">
                          {" "}(optional)
                        </span>
                      )}
                    </Label>
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
                      {form.provider === "custom"
                        ? "Optional for endpoints that require authentication. Leave blank for unauthenticated local servers."
                        : "A Kubernetes Secret will be created automatically from this key. Also used to fetch available models."}
                    </p>
                  </div>
                )}
              {form.provider === "bedrock" && (
                <>
                  <div className="space-y-2">
                    <Label>AWS Region</Label>
                    <Input
                      value={form.awsRegion}
                      onChange={(e) =>
                        setForm({ ...form, awsRegion: e.target.value })
                      }
                      placeholder="us-east-1"
                    />
                  </div>
                  <div className="space-y-2">
                    <Label>AWS Access Key ID</Label>
                    <Input
                      value={form.awsAccessKeyId}
                      onChange={(e) =>
                        setForm({ ...form, awsAccessKeyId: e.target.value })
                      }
                      placeholder="AKIA…"
                      autoComplete="off"
                    />
                  </div>
                  <div className="space-y-2">
                    <Label>AWS Secret Access Key</Label>
                    <Input
                      type="password"
                      value={form.awsSecretAccessKey}
                      onChange={(e) =>
                        setForm({ ...form, awsSecretAccessKey: e.target.value })
                      }
                      placeholder="wJalr…"
                      autoComplete="off"
                    />
                  </div>
                  <div className="space-y-2">
                    <Label>
                      AWS Session Token{" "}
                      <span className="text-muted-foreground font-normal">
                        (optional)
                      </span>
                    </Label>
                    <Input
                      type="password"
                      value={form.awsSessionToken}
                      onChange={(e) =>
                        setForm({ ...form, awsSessionToken: e.target.value })
                      }
                      placeholder="For temporary credentials"
                      autoComplete="off"
                    />
                  </div>
                  <p className="text-xs text-muted-foreground">
                    A Kubernetes Secret with your AWS credentials will be
                    created automatically. For EKS with IRSA, provide only the
                    region and use a pre-existing secret.
                  </p>
                </>
              )}
              <div className="space-y-2">
                <Label>
                  K8s Secret Name{" "}
                  <span className="text-muted-foreground font-normal">
                    (optional if credentials provided)
                  </span>
                </Label>
                <Input
                  value={form.secretName}
                  onChange={(e) =>
                    setForm({ ...form, secretName: e.target.value })
                  }
                  placeholder="my-provider-api-key"
                />
                <p className="text-xs text-muted-foreground">
                  Use an existing Kubernetes Secret, or leave blank to
                  auto-create one from the credentials above.
                </p>
              </div>
            </div>
          </ScrollArea>
        )}

        {/* ── Model step ────────────────────────────────────────────── */}
        {step === "model" && (
          <div className="space-y-2">
            <ModelSelector
              provider={form.provider}
              apiKey={form.apiKey}
              baseURL={form.baseURL}
              value={form.model}
              onChange={(v) => setForm({ ...form, model: v })}
              bedrockCredentials={
                form.provider === "bedrock" && form.awsAccessKeyId
                  ? {
                      region: form.awsRegion || "us-east-1",
                      accessKeyId: form.awsAccessKeyId,
                      secretAccessKey: form.awsSecretAccessKey,
                      sessionToken: form.awsSessionToken || undefined,
                    }
                  : undefined
              }
            />
            {mode === "persona" && agentConfigCount !== undefined && (
              <p className="text-xs text-muted-foreground">
                Applied to all{" "}
                <span className="text-blue-400">{agentConfigCount}</span> personas.
              </p>
            )}
            {mode === "canary" && form.baseURL && (
              <CanaryConnectionTest baseURL={form.baseURL} />
            )}
          </div>
        )}

        {/* ── Skills step ───────────────────────────────────────────── */}
        {step === "skills" && (
          <ScrollArea className="max-h-[60vh]">
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
                    {[...availableSkills]
                      .sort((a, b) => a.localeCompare(b))
                      .map((skill) => {
                        const selected = form.skills.includes(skill);
                        const locked = skill === "memory";
                        return (
                          <button
                            key={skill}
                            type="button"
                            disabled={locked}
                            onClick={() => {
                              if (locked) return;
                              const next = selected
                                ? form.skills.filter((s) => s !== skill)
                                : [...form.skills, skill];
                              setForm({ ...form, skills: next });
                            }}
                            className={cn(
                              "flex w-full items-center justify-between rounded-md border px-2.5 py-2 text-left text-xs transition-colors",
                              locked
                                ? "border-blue-500/40 bg-blue-500/15 text-blue-300 opacity-70 cursor-not-allowed"
                                : selected
                                  ? "border-blue-500/40 bg-blue-500/15 text-blue-300"
                                  : "border-transparent hover:border-border/60 hover:bg-white/5",
                            )}
                          >
                            <span className="font-mono">{skill}</span>
                            <span className="text-[10px]">
                              {locked
                                ? "Required"
                                : selected
                                  ? "Selected"
                                  : "Select"}
                            </span>
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

              {/* Web endpoint inline config */}
              {form.skills.includes("web-endpoint") && (
                <div className="rounded-md border border-blue-500/20 bg-blue-500/5 p-3 space-y-2">
                  <p className="text-xs font-medium text-blue-400">
                    Web Endpoint Config
                  </p>
                  <div className="space-y-1">
                    <Label className="text-xs">Rate Limit (req/min)</Label>
                    <Input
                      type="number"
                      value={form.webEndpointRPM || "60"}
                      onChange={(e) =>
                        setForm({ ...form, webEndpointRPM: e.target.value })
                      }
                      className="h-7 text-xs"
                    />
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">
                      Custom Hostname{" "}
                      <span className="text-muted-foreground">(optional)</span>
                    </Label>
                    <Input
                      value={form.webEndpointHostname || ""}
                      onChange={(e) =>
                        setForm({
                          ...form,
                          webEndpointHostname: e.target.value,
                        })
                      }
                      placeholder="auto from gateway"
                      className="h-7 text-xs"
                    />
                  </div>
                </div>
              )}

              {/* GitHub GitOps inline config */}
              {form.skills.includes("github-gitops") && (
                <div className="rounded-md border border-blue-500/20 bg-blue-500/5 p-3 space-y-2">
                  <p className="text-xs font-medium text-blue-400">
                    GitHub GitOps Config
                  </p>
                  <div className="space-y-1">
                    <Label className="text-xs">Repository</Label>
                    <Input
                      value={form.githubRepo || ""}
                      onChange={(e) =>
                        setForm({ ...form, githubRepo: e.target.value })
                      }
                      placeholder="owner/repo"
                      className="h-7 text-xs font-mono"
                    />
                    <p className="text-[10px] text-muted-foreground">
                      The GitHub repository this team will target for issues and
                      PRs.
                    </p>
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">Personal Access Token</Label>
                    <Input
                      type="password"
                      value={form.githubToken || ""}
                      onChange={(e) =>
                        setForm({ ...form, githubToken: e.target.value })
                      }
                      placeholder="github_pat_..."
                      autoComplete="off"
                      className="h-7 text-xs font-mono"
                    />
                    <p className="text-[10px] text-muted-foreground">
                      A token with repo access. Stored as a cluster secret.
                    </p>
                  </div>
                  <div className="space-y-1">
                    <Label className="text-xs">
                      Team Instructions{" "}
                      <span className="text-muted-foreground">(optional)</span>
                    </Label>
                    <Textarea
                      value={form.githubTeamInstructions || ""}
                      onChange={(e) =>
                        setForm({
                          ...form,
                          githubTeamInstructions: e.target.value,
                        })
                      }
                      placeholder="Describe the project goals, coding standards, architecture decisions, or any context each agent should know…"
                      rows={4}
                      className="text-xs resize-y"
                    />
                    <p className="text-[10px] text-muted-foreground">
                      Shared instructions propagated into every agent's
                      memory. Each agent will use these alongside its role.
                    </p>
                  </div>
                </div>
              )}

              {/* Subagents info */}
              {form.skills.includes("subagents") && (
                <div className="rounded-md border border-teal-500/20 bg-teal-500/5 p-3 space-y-1">
                  <p className="text-xs font-medium text-teal-400">
                    Sub-Agents Enabled
                  </p>
                  <p className="text-[10px] text-muted-foreground">
                    Agents with this skill can dynamically spawn child agents to
                    parallelize work. Limits (max depth, concurrency, children
                    per agent) are configured per persona in the ensemble
                    definition.
                  </p>
                </div>
              )}

              {/* Agent Sandbox toggle */}
              <div
                className={cn(
                  "rounded-md border p-3 space-y-2",
                  capabilities?.agentSandbox?.available
                    ? form.agentSandboxEnabled
                      ? "border-blue-500/20 bg-blue-500/5"
                      : "border-border/50"
                    : "border-border/30 opacity-60",
                )}
              >
                <div className="flex items-center justify-between">
                  <div>
                    <p className="text-xs font-medium">Agent Sandbox</p>
                    <p className="text-[10px] text-muted-foreground">
                      Kernel-level isolation via gVisor/Kata
                    </p>
                  </div>
                  <button
                    type="button"
                    disabled={!capabilities?.agentSandbox?.available}
                    onClick={() =>
                      setForm({
                        ...form,
                        agentSandboxEnabled: !form.agentSandboxEnabled,
                      })
                    }
                    className={cn(
                      "relative inline-flex h-5 w-9 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                      form.agentSandboxEnabled &&
                        capabilities?.agentSandbox?.available
                        ? "bg-blue-500"
                        : "bg-muted",
                      !capabilities?.agentSandbox?.available &&
                        "cursor-not-allowed",
                    )}
                  >
                    <span
                      className={cn(
                        "pointer-events-none block h-4 w-4 rounded-full bg-background shadow-lg ring-0 transition-transform",
                        form.agentSandboxEnabled &&
                          capabilities?.agentSandbox?.available
                          ? "translate-x-4"
                          : "translate-x-0",
                      )}
                    />
                  </button>
                </div>
                {!capabilities?.agentSandbox?.available && (
                  <p className="text-[10px] text-yellow-500">
                    {capabilities?.agentSandbox?.reason ||
                      "Agent Sandbox CRDs not installed"}
                  </p>
                )}
                {form.agentSandboxEnabled &&
                  capabilities?.agentSandbox?.available && (
                    <div className="space-y-1">
                      <Label className="text-xs">Runtime Class</Label>
                      <Select
                        value={form.agentSandboxRuntimeClass || "gvisor"}
                        onValueChange={(v) =>
                          setForm({ ...form, agentSandboxRuntimeClass: v })
                        }
                      >
                        <SelectTrigger className="h-7 text-xs">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="gvisor">gVisor</SelectItem>
                          <SelectItem value="kata">Kata Containers</SelectItem>
                        </SelectContent>
                      </Select>
                    </div>
                  )}
              </div>

              {/* Run Timeout */}
              <div className="rounded-md border border-border/50 p-3 space-y-2">
                <div>
                  <p className="text-xs font-medium">Run Timeout</p>
                  <p className="text-[10px] text-muted-foreground">
                    Max duration per agent run. Local models (Ollama, LM Studio)
                    default to 30m, cloud to 10m.
                  </p>
                </div>
                <Select
                  value={form.runTimeout || "default"}
                  onValueChange={(v) =>
                    setForm({ ...form, runTimeout: v === "default" ? "" : v })
                  }
                >
                  <SelectTrigger className="h-7 text-xs">
                    <SelectValue placeholder="Provider default" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="default">Provider default</SelectItem>
                    <SelectItem value="10m">10 minutes</SelectItem>
                    <SelectItem value="30m">30 minutes</SelectItem>
                    <SelectItem value="1h">1 hour</SelectItem>
                    <SelectItem value="2h">2 hours</SelectItem>
                  </SelectContent>
                </Select>
              </div>

              {/* Require Approval */}
              <div className="rounded-md border border-border/50 p-3">
                <label
                  className="flex items-start gap-3 cursor-pointer"
                  data-testid="require-approval-checkbox"
                >
                  <input
                    type="checkbox"
                    className="mt-0.5 h-4 w-4 rounded border-border accent-amber-500"
                    checked={form.requireApproval ?? false}
                    onChange={(e) =>
                      setForm({ ...form, requireApproval: e.target.checked })
                    }
                  />
                  <div>
                    <p className="text-xs font-medium">
                      Require manual approval
                    </p>
                    <p className="text-[10px] text-muted-foreground">
                      Hold agent responses until an operator approves or rejects
                      them via the UI or API.
                    </p>
                  </div>
                </label>
              </div>
            </div>
          </ScrollArea>
        )}

        {/* ── Heartbeat step ──────────────────────────────────────── */}
        {step === "heartbeat" && (
          <div className="space-y-4">
            <p className="text-sm text-muted-foreground">
              {mode === "persona"
                ? "How often should personas wake up? This overrides each persona's default schedule."
                : "How often should this agent wake up on a heartbeat schedule?"}
            </p>
            {heartbeatOptions(mode).map((opt) => (
              <button
                key={opt.value}
                type="button"
                onClick={() =>
                  setForm({ ...form, heartbeatInterval: opt.value })
                }
                className={cn(
                  "flex w-full items-center justify-between rounded-md border px-3 py-2 text-left text-sm transition-colors",
                  form.heartbeatInterval === opt.value
                    ? "border-blue-500/40 bg-blue-500/15 text-blue-300"
                    : "border-border/50 hover:bg-white/5",
                )}
              >
                <span>{opt.label}</span>
                <span className="text-xs">
                  {form.heartbeatInterval === opt.value ? "Selected" : "Select"}
                </span>
              </button>
            ))}
          </div>
        )}

        {/* ── Channels step ─────────────────────────────────────────── */}
        {step === "channels" && (
          <div className="space-y-4">
            <p className="text-sm text-muted-foreground">
              Select channels to bind. Channel-specific setup happens after
              confirmation.
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
                  setForm({
                    ...form,
                    channels: nextChannels,
                    channelConfigs: nextConfigs,
                  });
                }}
                className={cn(
                  "flex w-full items-center justify-between rounded-md border px-3 py-2 text-left text-sm transition-colors",
                  form.channels.includes(ch.value)
                    ? "border-blue-500/40 bg-blue-500/15 text-blue-300"
                    : "border-border/50 hover:bg-white/5",
                )}
              >
                <span>{ch.label}</span>
                <span className="text-xs">
                  {form.channels.includes(ch.value) ? "Selected" : "Select"}
                </span>
              </button>
            ))}
            {form.channels.includes("whatsapp") && (
              <p className="text-xs text-muted-foreground">
                WhatsApp setup will open a QR pairing modal after
                creation/activation.
              </p>
            )}
          </div>
        )}

        {/* ── Confirm step ──────────────────────────────────────────── */}
        {step === "confirm" && (
          <div className="space-y-3">
            <div className="rounded-lg border border-blue-500/20 bg-blue-500/5 p-4 space-y-2 text-sm">
              {mode === "agent" && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Name</span>
                  <span className="font-mono text-blue-400">{form.name}</span>
                </div>
              )}
              {mode === "persona" && targetName && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Pack</span>
                  <span className="font-mono text-blue-400">{targetName}</span>
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
              {form.nodeSelector?.["kubernetes.io/hostname"] && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Node</span>
                  <span className="font-mono text-xs">
                    {form.nodeSelector["kubernetes.io/hostname"]}
                  </span>
                </div>
              )}
              {mode === "persona" && agentConfigCount !== undefined && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Personas</span>
                  <span>{agentConfigCount}</span>
                </div>
              )}
              {form.heartbeatInterval && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Heartbeat</span>
                  <span>
                    {HEARTBEAT_INTERVALS.find(
                      (o) => o.value === form.heartbeatInterval,
                    )?.label || form.heartbeatInterval}
                  </span>
                </div>
              )}
              {form.channels.length > 0 && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Channels</span>
                  <span>{form.channels.join(", ")}</span>
                </div>
              )}
              {form.skills.includes("web-endpoint") && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Web Endpoint</span>
                  <span className="text-xs">
                    {form.webEndpointRPM || "60"} rpm
                    {form.webEndpointHostname
                      ? `, ${form.webEndpointHostname}`
                      : ""}
                  </span>
                </div>
              )}
              {form.skills.includes("github-gitops") && form.githubRepo && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">GitHub Repo</span>
                  <span className="font-mono text-xs">{form.githubRepo}</span>
                </div>
              )}
              {form.skills.includes("github-gitops") && form.githubToken && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">GitHub Token</span>
                  <span className="text-xs text-emerald-400">provided</span>
                </div>
              )}
              {form.skills.includes("github-gitops") &&
                form.githubTeamInstructions && (
                  <div className="flex justify-between">
                    <span className="text-muted-foreground">
                      Team Instructions
                    </span>
                    <span className="text-xs text-emerald-400">provided</span>
                  </div>
                )}
              {form.agentSandboxEnabled && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Agent Sandbox</span>
                  <span className="text-xs">
                    {form.agentSandboxRuntimeClass || "gvisor"}
                  </span>
                </div>
              )}
              {form.runTimeout && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">Run Timeout</span>
                  <span className="text-xs">{form.runTimeout}</span>
                </div>
              )}
              {form.requireApproval && (
                <div className="flex justify-between">
                  <span className="text-muted-foreground">
                    Require Approval
                  </span>
                  <span className="text-xs text-amber-400">Enabled</span>
                </div>
              )}
            </div>
            <div className="flex items-center justify-between">
              <p className="text-xs text-muted-foreground">
                {mode === "agent"
                  ? "A new Agent will be created with this configuration."
                  : "The controller will stamp out Agents, Schedules, and ConfigMaps for each persona."}
              </p>
              <Button
                variant="ghost"
                size="sm"
                className="h-7 gap-1.5 text-xs text-muted-foreground hover:text-foreground"
                onClick={() => setShowYaml(true)}
              >
                <FileCode className="h-3.5 w-3.5" />
                Show YAML
              </Button>
            </div>
            <YamlModal
              open={showYaml}
              onClose={() => setShowYaml(false)}
              yaml={
                mode === "agent"
                  ? instanceYamlFromWizard(form)
                  : ensembleYamlFromWizard(
                      targetName || "<pack-name>",
                      form,
                      agentConfigCount,
                    )
              }
              title={
                mode === "agent"
                  ? `Agent — ${form.name || "<agent>"}`
                  : `Ensemble — ${targetName || "<pack>"}`
              }
            />
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
                  Channel-specific setup ({channelActionIdx + 1}/
                  {actionChannels.length})
                </p>
                <div className="space-y-2">
                  <Label>{actionChannels[channelActionIdx]} Secret Name</Label>
                  <Input
                    value={
                      form.channelConfigs[actionChannels[channelActionIdx]] ||
                      ""
                    }
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
              className="gap-1 bg-primary hover:bg-primary/90 text-primary-foreground border-0"
              onClick={next}
              disabled={isPending}
            >
              {isPending ? (
                "Working…"
              ) : (
                <>
                  {step === "channelAction" &&
                  channelActionIdx < actionChannels.length - 1 ? (
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
              className="gap-1 bg-primary hover:bg-primary/90 text-primary-foreground border-0"
            >
              Next <ChevronRight className="h-4 w-4" />
            </Button>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
