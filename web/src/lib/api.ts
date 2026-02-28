// Sympozium API client — types match the Go CRD structs.

// ── Common K8s types ─────────────────────────────────────────────────────────

export interface ObjectMeta {
  name: string;
  namespace?: string;
  creationTimestamp?: string;
  labels?: Record<string, string>;
  annotations?: Record<string, string>;
  generateName?: string;
}

export interface Condition {
  type: string;
  status: string;
  reason: string;
  message: string;
  lastTransitionTime: string;
}

// ── SympoziumInstance ────────────────────────────────────────────────────────

export interface SecretRef {
  provider: string;
  secret: string;
}

export interface MemorySpec {
  enabled: boolean;
  maxSizeKB?: number;
  systemPrompt?: string;
}

export interface ChannelSpec {
  type: string;
  configRef?: SecretRef;
}

export interface AgentConfig {
  model: string;
  baseURL?: string;
  thinking?: string;
}

export interface AgentsSpec {
  default: AgentConfig;
}

export interface SkillRef {
  skillPackRef: string;
  configMapRef?: string;
}

export interface ChannelStatus {
  type: string;
  status: string;
  lastHealthCheck?: string;
  message?: string;
}

export interface SympoziumInstanceSpec {
  channels?: ChannelSpec[];
  agents: AgentsSpec;
  skills?: SkillRef[];
  policyRef?: string;
  authRefs?: SecretRef[];
  memory?: MemorySpec;
}

export interface SympoziumInstanceStatus {
  phase?: string;
  channels?: ChannelStatus[];
  activeAgentPods?: number;
  totalAgentRuns?: number;
  conditions?: Condition[];
}

export interface SympoziumInstance {
  metadata: ObjectMeta;
  spec: SympoziumInstanceSpec;
  status?: SympoziumInstanceStatus;
}

// ── AgentRun ─────────────────────────────────────────────────────────────────

export interface ModelSpec {
  provider?: string;
  model?: string;
  baseURL?: string;
  thinking?: string;
  authSecretRef?: string;
}

export interface ToolPolicySpec {
  allow?: string[];
  deny?: string[];
}

export interface TokenUsage {
  inputTokens: number;
  outputTokens: number;
  totalTokens: number;
  toolCalls: number;
  durationMs: number;
}

export interface AgentRunSpec {
  instanceRef: string;
  agentId: string;
  sessionKey: string;
  task: string;
  systemPrompt?: string;
  model?: ModelSpec;
  toolPolicy?: ToolPolicySpec;
  timeout?: string;
  cleanup?: string;
}

export interface AgentRunStatus {
  phase?: string;
  podName?: string;
  jobName?: string;
  startedAt?: string;
  completedAt?: string;
  result?: string;
  error?: string;
  exitCode?: number;
  tokenUsage?: TokenUsage;
  conditions?: Condition[];
}

export interface AgentRun {
  metadata: ObjectMeta;
  spec: AgentRunSpec;
  status?: AgentRunStatus;
}

// ── SympoziumPolicy ──────────────────────────────────────────────────────────

export interface ToolGatingRule {
  tool: string;
  action: string;
}

export interface ToolGatingSpec {
  defaultAction?: string;
  rules?: ToolGatingRule[];
}

export interface SandboxPolicySpec {
  required?: boolean;
  defaultImage?: string;
  maxCPU?: string;
  maxMemory?: string;
  allowHostMounts?: boolean;
}

export interface EgressRule {
  host: string;
  port: number;
}

export interface NetworkPolicySpec {
  denyAll?: boolean;
  allowDNS?: boolean;
  allowEventBus?: boolean;
  allowedEgress?: EgressRule[];
}

export interface SympoziumPolicySpec {
  sandboxPolicy?: SandboxPolicySpec;
  toolGating?: ToolGatingSpec;
  featureGates?: Record<string, boolean>;
  networkPolicy?: NetworkPolicySpec;
}

export interface SympoziumPolicyStatus {
  boundInstances?: number;
  conditions?: Condition[];
}

export interface SympoziumPolicy {
  metadata: ObjectMeta;
  spec: SympoziumPolicySpec;
  status?: SympoziumPolicyStatus;
}

// ── SkillPack ────────────────────────────────────────────────────────────────

export interface Skill {
  name: string;
  description?: string;
  content?: string;
  requires?: { bins?: string[]; tools?: string[] };
}

export interface RBACRule {
  apiGroups: string[];
  resources: string[];
  verbs: string[];
}

export interface SkillSidecar {
  image: string;
  command?: string[];
  mountWorkspace?: boolean;
  rbac?: RBACRule[];
  clusterRBAC?: RBACRule[];
}

export interface SkillPackSpec {
  skills: Skill[];
  category?: string;
  source?: string;
  version?: string;
  sidecar?: SkillSidecar;
}

export interface SkillPackStatus {
  phase?: string;
  configMapName?: string;
  skillCount?: number;
  conditions?: Condition[];
}

export interface SkillPack {
  metadata: ObjectMeta;
  spec: SkillPackSpec;
  status?: SkillPackStatus;
}

// ── SympoziumSchedule ────────────────────────────────────────────────────────

export interface SympoziumScheduleSpec {
  instanceRef: string;
  schedule: string;
  task: string;
  type?: string;
  suspend?: boolean;
  concurrencyPolicy?: string;
  includeMemory?: boolean;
}

export interface SympoziumScheduleStatus {
  phase?: string;
  lastRunTime?: string;
  nextRunTime?: string;
  lastRunName?: string;
  totalRuns?: number;
  conditions?: Condition[];
}

export interface SympoziumSchedule {
  metadata: ObjectMeta;
  spec: SympoziumScheduleSpec;
  status?: SympoziumScheduleStatus;
}

// ── PersonaPack ──────────────────────────────────────────────────────────────

export interface PersonaToolPolicy {
  allow?: string[];
  deny?: string[];
}

export interface PersonaSchedule {
  type: string;
  interval?: string;
  cron?: string;
  task: string;
}

export interface PersonaMemory {
  enabled: boolean;
  seeds?: string[];
}

export interface PersonaSpec {
  name: string;
  displayName?: string;
  systemPrompt: string;
  model?: string;
  skills?: string[];
  toolPolicy?: PersonaToolPolicy;
  schedule?: PersonaSchedule;
  memory?: PersonaMemory;
  channels?: string[];
}

export interface InstalledPersona {
  name: string;
  instanceName: string;
  scheduleName?: string;
}

export interface PersonaPackSpec {
  enabled?: boolean;
  description?: string;
  category?: string;
  version?: string;
  personas: PersonaSpec[];
  authRefs?: SecretRef[];
  excludePersonas?: string[];
  channelConfigs?: Record<string, string>;
  policyRef?: string;
}

export interface PersonaPackStatus {
  phase?: string;
  personaCount?: number;
  installedCount?: number;
  installedPersonas?: InstalledPersona[];
  conditions?: Condition[];
}

export interface PersonaPack {
  metadata: ObjectMeta;
  spec: PersonaPackSpec;
  status?: PersonaPackStatus;
}

// ── Pod info (returned by /api/v1/pods) ──────────────────────────────────────

export interface PodInfo {
  name: string;
  namespace: string;
  phase: string;
  nodeName?: string;
  podIP?: string;
  startTime?: string;
  restartCount: number;
  instanceRef?: string;
  labels?: Record<string, string>;
}

// ── API client ───────────────────────────────────────────────────────────────

/** Typed error so callers can inspect the HTTP status code. */
export class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

const TOKEN_KEY = "sympozium_token";
const NS_KEY = "sympozium_namespace";

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY);
}

export function setToken(token: string) {
  localStorage.setItem(TOKEN_KEY, token);
}

export function clearToken() {
  localStorage.removeItem(TOKEN_KEY);
}

export function getNamespace(): string {
  return localStorage.getItem(NS_KEY) || "default";
}

export function setNamespace(ns: string) {
  localStorage.setItem(NS_KEY, ns);
}

async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const token = getToken();
  const headers = new Headers(init?.headers);
  if (!headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  if (token) {
    // Ensure the token only contains valid HTTP header characters (Latin1).
    // Strip any non-Latin1 codepoints that can cause Firefox ByteString errors.
    const safeToken = token.replace(/[^\x00-\xFF]/g, "");
    headers.set("Authorization", `Bearer ${safeToken}`);
  }

  const ns = getNamespace();
  const separator = path.includes("?") ? "&" : "?";
  const url = `${path}${separator}namespace=${ns}`;

  // Retry network errors (port-forward drops, transient failures) up to 2
  // times with a short delay.  Non-network errors (4xx, 5xx) are NOT retried
  // here — React Query handles those via its own retry config.
  const maxAttempts = 3;
  let lastError: unknown;
  for (let attempt = 0; attempt < maxAttempts; attempt++) {
    try {
      const res = await fetch(url, { ...init, headers });
      if (res.status === 401) {
        throw new ApiError("Unauthorized", 401);
      }
      if (res.status === 204) return undefined as T;
      if (!res.ok) {
        const text = await res.text();
        throw new Error(text || `HTTP ${res.status}`);
      }
      return res.json();
    } catch (err) {
      lastError = err;
      // Only retry on network-level failures (TypeError from fetch).
      // Don't retry application-level errors (ApiError, HTTP errors).
      const isNetworkError =
        err instanceof TypeError ||
        (err instanceof Error &&
          !("status" in err) &&
          /network|failed to fetch|load failed|aborted/i.test(err.message));
      if (!isNetworkError || attempt >= maxAttempts - 1) {
        throw err;
      }
      // Wait before retrying (1s, then 2s).
      await new Promise((r) => setTimeout(r, 1000 * (attempt + 1)));
    }
  }
  throw lastError;
}

// ── Instances ────────────────────────────────────────────────────────────────

export const api = {
  instances: {
    list: () => apiFetch<SympoziumInstance[]>("/api/v1/instances"),
    get: (name: string) =>
      apiFetch<SympoziumInstance>(`/api/v1/instances/${name}`),
    delete: (name: string) =>
      apiFetch<void>(`/api/v1/instances/${name}`, { method: "DELETE" }),
    create: (data: {
      name: string;
      provider: string;
      model: string;
      baseURL?: string;
      secretName?: string;
      policyRef?: string;
      skills?: string[];
      channels?: string[];
      channelConfigs?: Record<string, string>;
    }) =>
      apiFetch<SympoziumInstance>("/api/v1/instances", {
        method: "POST",
        body: JSON.stringify(data),
      }),
  },

  runs: {
    list: () => apiFetch<AgentRun[]>("/api/v1/runs"),
    get: (name: string) => apiFetch<AgentRun>(`/api/v1/runs/${name}`),
    create: (data: {
      instanceRef: string;
      task: string;
      model?: string;
      timeout?: string;
    }) =>
      apiFetch<AgentRun>("/api/v1/runs", {
        method: "POST",
        body: JSON.stringify(data),
      }),
    delete: (name: string) =>
      apiFetch<void>(`/api/v1/runs/${name}`, { method: "DELETE" }),
  },

  policies: {
    list: () => apiFetch<SympoziumPolicy[]>("/api/v1/policies"),
    get: (name: string) =>
      apiFetch<SympoziumPolicy>(`/api/v1/policies/${name}`),
    delete: (name: string) =>
      apiFetch<void>(`/api/v1/policies/${name}`, { method: "DELETE" }),
  },

  skills: {
    list: () => apiFetch<SkillPack[]>("/api/v1/skills"),
    get: (name: string) => apiFetch<SkillPack>(`/api/v1/skills/${name}`),
  },

  schedules: {
    list: () => apiFetch<SympoziumSchedule[]>("/api/v1/schedules"),
    get: (name: string) =>
      apiFetch<SympoziumSchedule>(`/api/v1/schedules/${name}`),
    create: (data: {
      instanceRef: string;
      schedule: string;
      task: string;
      type?: string;
      name?: string;
    }) =>
      apiFetch<SympoziumSchedule>("/api/v1/schedules", {
        method: "POST",
        body: JSON.stringify(data),
      }),
    delete: (name: string) =>
      apiFetch<void>(`/api/v1/schedules/${name}`, { method: "DELETE" }),
  },

  personaPacks: {
    list: () => apiFetch<PersonaPack[]>("/api/v1/personapacks"),
    get: (name: string) =>
      apiFetch<PersonaPack>(`/api/v1/personapacks/${name}`),
    delete: (name: string) =>
      apiFetch<void>(`/api/v1/personapacks/${name}`, { method: "DELETE" }),
    patch: (
      name: string,
      data: {
        enabled?: boolean;
        provider?: string;
        secretName?: string;
        apiKey?: string;
        model?: string;
        baseURL?: string;
        channels?: string[];
        channelConfigs?: Record<string, string>;
        policyRef?: string;
        skills?: string[];
      }
    ) =>
      apiFetch<PersonaPack>(`/api/v1/personapacks/${name}`, {
        method: "PATCH",
        body: JSON.stringify(data),
      }),
  },

  pods: {
    list: () => apiFetch<PodInfo[]>("/api/v1/pods"),
    logs: (name: string) =>
      apiFetch<{ logs: string }>(`/api/v1/pods/${name}/logs`),
  },

  namespaces: {
    list: () => apiFetch<string[]>("/api/v1/namespaces"),
  },
};
