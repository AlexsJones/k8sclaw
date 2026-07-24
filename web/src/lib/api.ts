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

// ── Agent ────────────────────────────────────────────────────────

export interface SecretRef {
  provider?: string;
  secret: string;
}

export interface MemorySpec {
  enabled: boolean;
  maxSizeKB?: number;
  autoStore?: boolean;
  systemPrompt?: string;
}

export interface ChannelSpec {
  type: string;
  configRef?: SecretRef;
}

export interface AgentSandboxInstanceSpec {
  enabled: boolean;
  runtimeClass?: string;
  warmPool?: {
    size?: number;
    runtimeClass?: string;
  };
}

export interface EnvVar {
  name: string;
  value: string;
}

export interface LifecycleHookContainer {
  name: string;
  image: string;
  command?: string[];
  args?: string[];
  env?: EnvVar[];
  timeout?: string;
  gate?: boolean;
}

export interface RBACRule {
  apiGroups: string[];
  resources: string[];
  verbs: string[];
}

export interface LifecycleHooks {
  preRun?: LifecycleHookContainer[];
  postRun?: LifecycleHookContainer[];
  rbac?: RBACRule[];
  gateDefault?: string;
}

export interface GateVerdictRequest {
  action: "approve" | "reject" | "rewrite";
  response?: string;
  reason?: string;
}

export interface AgentConfig {
  model: string;
  baseURL?: string;
  thinking?: string;
  nodeSelector?: Record<string, string>;
  agentSandbox?: AgentSandboxInstanceSpec;
  lifecycle?: LifecycleHooks;
}

export interface AgentsSpec {
  default: AgentConfig;
}

export interface SkillRef {
  skillPackRef: string;
  configMapRef?: string;
  params?: Record<string, string>;
}

export interface ChannelStatus {
  type: string;
  status: string;
  lastHealthCheck?: string;
  message?: string;
}

export interface AgentSpec {
  channels?: ChannelSpec[];
  agents: AgentsSpec;
  skills?: SkillRef[];
  policyRef?: string;
  authRefs?: SecretRef[];
  memory?: MemorySpec;
}

export interface AgentStatus {
  phase?: string;
  channels?: ChannelStatus[];
  activeAgentPods?: number;
  totalAgentRuns?: number;
  conditions?: Condition[];
}

export interface Agent {
  metadata: ObjectMeta;
  spec: AgentSpec;
  status?: AgentStatus;
}

// ── AgentRun ─────────────────────────────────────────────────────────────────

export interface ModelSpec {
  provider?: string;
  model?: string;
  baseURL?: string;
  thinking?: string;
  authSecretRef?: string;
  nodeSelector?: Record<string, string>;
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

/** A cost estimate in integer micro-USD (1e-6 USD). `source` is
 *  "defaultTable" for real estimates from the cluster price table, or
 *  "simulated" for hypothetical estimates from user-defined simulated prices. */
export interface CostEstimate {
  amountMicro: number;
  inputAmountMicro: number;
  outputAmountMicro: number;
  currency: string;
  source: string;
  priceKey?: string;
  estimatedAt?: string;
}

export interface ParentRunRef {
  runName: string;
  sessionKey: string;
  spawnDepth: number;
}

export interface AgentRunSpec {
  agentRef: string;
  agentId: string;
  sessionKey: string;
  task: string;
  systemPrompt?: string;
  model?: ModelSpec;
  toolPolicy?: ToolPolicySpec;
  timeout?: string;
  cleanup?: string;
  mode?: string;
  lifecycle?: LifecycleHooks;
  parent?: ParentRunRef;
}

export interface DelegateStatus {
  childRunName: string;
  targetPersona?: string;
  batchId?: string;
  taskId?: string;
  phase?: string;
  result?: string;
  error?: string;
}

export interface AgentRunStatus {
  phase?: string;
  podName?: string;
  jobName?: string;
  deploymentName?: string;
  serviceName?: string;
  startedAt?: string;
  completedAt?: string;
  result?: string;
  error?: string;
  exitCode?: number;
  tokenUsage?: TokenUsage;
  costEstimate?: CostEstimate;
  postRunJobName?: string;
  gateVerdict?: string;
  delegates?: DelegateStatus[];
  conditions?: Condition[];
}

export interface AgentRun {
  metadata: ObjectMeta;
  spec: AgentRunSpec;
  status?: AgentRunStatus;
  /** Hypothetical estimate from user-defined simulated prices ("source":"simulated"). */
  simulatedCostEstimate?: CostEstimate;
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

export interface HostPathMount {
  hostPath: string;
  mountPath: string;
  readOnly?: boolean;
}

export interface HostAccessSpec {
  enabled?: boolean;
  hostNetwork?: boolean;
  hostPID?: boolean;
  privileged?: boolean;
  runAsRoot?: boolean;
  mounts?: HostPathMount[];
}

export interface SkillSidecar {
  image: string;
  command?: string[];
  mountWorkspace?: boolean;
  rbac?: RBACRule[];
  clusterRBAC?: RBACRule[];
  secretRef?: string;
  secretMountPath?: string;
  hostAccess?: HostAccessSpec;
}

// ── Gateway Config ──────────────────────────────────────────────────────────

export interface GatewayConfigResponse {
  enabled: boolean;
  gatewayClassName?: string;
  name?: string;
  baseDomain?: string;
  tlsEnabled: boolean;
  certManagerClusterIssuer?: string;
  tlsSecretName?: string;
  phase?: string;
  ready: boolean;
  address?: string;
  listenerCount?: number;
  message?: string;
}

export interface PatchGatewayConfigRequest {
  enabled?: boolean;
  gatewayClassName?: string;
  name?: string;
  baseDomain?: string;
  tlsEnabled?: boolean;
  certManagerClusterIssuer?: string;
  tlsSecretName?: string;
}

export interface CanaryCheck {
  name: string;
  status: "pass" | "fail";
  details: string;
}

export interface CanaryConfigResponse {
  enabled: boolean;
  interval?: string;
  model?: string;
  provider?: string;
  baseURL?: string;
  authSecretRef?: string;
  ensembleCreated: boolean;
  lastRunPhase?: string;
  lastRunTime?: string;
  healthStatus?: string;
  lastRunResult?: string;
  checks?: CanaryCheck[];
}


export interface PatchCanaryConfigRequest {
  enabled?: boolean;
  interval?: string;
  model?: string;
  provider?: string;
  baseURL?: string;
  authSecretRef?: string;
  modelRef?: string;
}

export interface GithubAuthStatusResponse {
  status: string;
}

export interface MetricBreakdown {
  label: string;
  value: number;
}

export interface ObservabilityMetricsResponse {
  collectorReachable: boolean;
  collectorError?: string;
  collectedAt: string;
  namespace: string;
  agentRunsTotal: number;
  inputTokensTotal: number;
  outputTokensTotal: number;
  toolInvocations: number;
  runStatus?: Record<string, number>;
  inputByModel?: MetricBreakdown[];
  outputByModel?: MetricBreakdown[];
  toolsByName?: MetricBreakdown[];
  rawMetricNames?: string[];
}

export interface GatewayBucket {
  ts: number;
  requests: number;
  errors: number;
  avgDurationSec: number;
}

export interface GatewayMetricsResponse {
  totalRequests: number;
  successCount: number;
  errorCount: number;
  avgDurationSec: number;
  uptimeSec: number;
  servingInstances: number;
  buckets: GatewayBucket[];
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
  agentRef: string;
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

// ── Ensemble ──────────────────────────────────────────────────────────────

export interface AgentConfigToolPolicy {
  allow?: string[];
  deny?: string[];
}

export interface AgentConfigSchedule {
  type: string;
  interval?: string;
  cron?: string;
  task: string;
}

export interface AgentConfigMemory {
  enabled: boolean;
  autoStore?: boolean;
  seeds?: string[];
}

export interface AgentConfigWebEndpoint {
  enabled: boolean;
  hostname?: string;
}

export interface SubagentsSpec {
  maxDepth?: number;
  maxConcurrent?: number;
  maxChildrenPerAgent?: number;
}

export interface AgentConfigSpec {
  name: string;
  displayName?: string;
  systemPrompt: string;
  model?: string;
  provider?: string;
  baseURL?: string;
  skills?: string[];
  toolPolicy?: AgentConfigToolPolicy;
  schedule?: AgentConfigSchedule;
  memory?: AgentConfigMemory;
  channels?: string[];
  webEndpoint?: AgentConfigWebEndpoint;
  lifecycle?: LifecycleHooks;
  subagents?: SubagentsSpec;
}

export interface InstalledAgentConfig {
  name: string;
  agentName: string;
  scheduleName?: string;
}

export interface SharedMemoryAccessRule {
  agentConfig: string;
  access: "read-write" | "read-only";
}

export interface PermeabilityRule {
  agentConfig: string;
  defaultVisibility?: "public" | "trusted" | "private";
  exposeTags?: string[];
  acceptTags?: string[];
}

export interface TrustGroup {
  name: string;
  agentConfigs: string[];
}

export interface TokenBudgetSpec {
  maxTokens?: number;
  maxTokensPerRun?: number;
  action?: "halt" | "warn";
}

export interface CircuitBreakerSpec {
  consecutiveFailures?: number;
  cooldownDuration?: string;
}

export interface TimeDecaySpec {
  ttl?: string;
  decayFunction?: "linear" | "exponential";
}

export interface EvidencePolicySpec {
  minKind?: "tool_result" | "external_source" | "llm_interpretation" | "agent_opinion";
}

export interface MembraneSpec {
  defaultVisibility?: "public" | "trusted" | "private";
  permeability?: PermeabilityRule[];
  trustGroups?: TrustGroup[];
  tokenBudget?: TokenBudgetSpec;
  circuitBreaker?: CircuitBreakerSpec;
  timeDecay?: TimeDecaySpec;
  evidencePolicy?: EvidencePolicySpec;
}

export interface SharedMemorySpec {
  enabled: boolean;
  storageSize?: string;
  accessRules?: SharedMemoryAccessRule[];
  membrane?: MembraneSpec;
}

export interface AgentConfigRelationship {
  source: string;
  target: string;
  type: "delegation" | "sequential" | "supervision" | "stimulus";
  condition?: string;
  timeout?: string;
  resultFormat?: string;
}

export interface StimulusSpec {
  name: string;
  prompt: string;
  /**
   * When the stimulus fires. "onReady" (the server-side default) delivers it
   * automatically once every agent is ready; "manual" leaves the ensemble idle
   * until someone triggers it.
   */
  trigger?: "onReady" | "manual";
}

export interface EnsembleSpec {
  enabled?: boolean;
  description?: string;
  category?: string;
  version?: string;
  agentConfigs: AgentConfigSpec[];
  authRefs?: SecretRef[];
  excludeAgentConfigs?: string[];
  channelConfigs?: Record<string, string>;
  policyRef?: string;
  skillParams?: Record<string, Record<string, string>>;
  taskOverride?: string;
  stimulus?: StimulusSpec;
  relationships?: AgentConfigRelationship[];
  workflowType?: "autonomous" | "pipeline" | "delegation";
  sharedMemory?: SharedMemorySpec;
  /** Default for whether generated agents auto-store each run's summary. */
  autoStoreMemory?: boolean;
  /** Base URL for the inference endpoint. */
  baseURL?: string;
  /** References a Model CR for cluster-local inference. */
  modelRef?: string;
}

export interface EnsembleStatus {
  phase?: string;
  agentConfigCount?: number;
  installedCount?: number;
  installedPersonas?: InstalledAgentConfig[];
  sharedMemoryReady?: boolean;
  tokenBudgetUsed?: number;
  circuitBreakerOpen?: boolean;
  consecutiveDelegateFailures?: number;
  allAgentsServing?: boolean;
  stimulusDelivered?: boolean;
  stimulusGeneration?: number;
  conditions?: Condition[];
}

export interface EvidenceTrace {
  kind: "tool_result" | "external_source" | "llm_interpretation" | "agent_opinion";
  tool_call?: string;
  raw_result?: string;
  source?: string;
  confidence?: number;
  derived_from?: number[];
}

export interface SharedMemoryEntry {
  id: number;
  content: string;
  tags?: string[];
  visibility?: string;
  source_agent?: string;
  parent_id?: number;
  seq?: number;
  evidence?: EvidenceTrace;
  created_at: string;
  updated_at: string;
}

export interface Ensemble {
  metadata: ObjectMeta;
  spec: EnsembleSpec;
  status?: EnsembleStatus;
}

export interface InstallDefaultEnsemblesResponse {
  sourceNamespace: string;
  targetNamespace: string;
  copied: string[];
  alreadyPresent: string[];
}

// ── MCPServer ────────────────────────────────────────────────────────────────

export interface MCPSecretRef {
  name: string;
}

export interface MCPServerDeployment {
  image: string;
  cmd?: string;
  args?: string[];
  port?: number;
  env?: Record<string, string>;
  secretRefs?: MCPSecretRef[];
  serviceAccountName?: string;
}

export interface MCPServerSpec {
  transportType: string;
  url?: string;
  deployment?: MCPServerDeployment;
  toolsPrefix: string;
  timeout?: number;
  replicas?: number;
  toolsAllow?: string[];
  toolsDeny?: string[];
  suspended?: boolean;
}

export interface MCPServerStatus {
  ready: boolean;
  url?: string;
  toolCount?: number;
  tools?: string[];
  conditions?: Condition[];
}

export interface MCPServer {
  metadata: ObjectMeta;
  spec: MCPServerSpec;
  status?: MCPServerStatus;
}

export interface InstallDefaultMCPServersResponse {
  sourceNamespace: string;
  targetNamespace: string;
  copied: string[];
  alreadyPresent: string[];
}

export interface MCPServerAuthStatusResponse {
  status: string;
  secretName: string;
}

// ── Model (cluster-local inference) ──────────────────────────────────────────

export interface ModelSource {
  url?: string;
  filename?: string;
  modelID?: string;
}

export interface ModelStorage {
  size?: string;
  storageClass?: string;
}

export interface InferenceSpec {
  serverType?: "llama-cpp" | "vllm" | "tgi" | "custom";
  image?: string;
  port?: number;
  contextSize?: number;
  args?: string[];
  huggingFaceTokenSecret?: string;
}

export interface ModelResources {
  gpu?: number;
  memory?: string;
  cpu?: string;
}

export interface ModelPlacement {
  mode?: "auto" | "manual";
}

export interface ModelCRDSpec {
  source: ModelSource;
  storage?: ModelStorage;
  inference?: InferenceSpec;
  resources?: ModelResources;
  nodeSelector?: Record<string, string>;
  placement?: ModelPlacement;
}

export interface ModelStatus {
  phase?: string;
  endpoint?: string;
  message?: string;
  placedNode?: string;
  placementScore?: number;
  placementMessage?: string;
  conditions?: Condition[];
}

export interface Model {
  metadata: ObjectMeta;
  spec: ModelCRDSpec;
  status?: ModelStatus;
}

// ── Cluster Nodes ───────────────────────────────────────────────────────────

export interface ClusterNode {
  name: string;
  labels?: Record<string, string>;
  ready: boolean;
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
  labels?: Record<string, string>;
  agentRef?: string;
}

// ── Cluster Info ─────────────────────────────────────────────────────────

export interface ClusterInfoResponse {
  nodes: number;
  namespaces: number;
  pods: number;
  version?: string;
}

// ── Provider Discovery ───────────────────────────────────────────────────────

export interface NodeProvider {
  name: string;
  port: number;
  proxyPort?: number;
  models: string[];
  lastProbe?: string;
}

export interface ProviderNode {
  nodeName: string;
  nodeIP: string;
  providers: NodeProvider[];
  labels?: Record<string, string>;
}

export interface ProviderModelsResponse {
  models: string[];
  source: string;
}

// ── Capabilities ─────────────────────────────────────────────────────────────

export interface CapabilityStatus {
  available: boolean;
  reason?: string;
}

export interface CapabilitiesResponse {
  agentSandbox: CapabilityStatus;
}

// ── Model Density (llmfit DaemonSet telemetry) ─────────────────────────────────────

export interface DensityGPU {
  name: string;
  vram_gb: number;
  backend: string;
  count: number;
  unified_memory: boolean;
}

export interface DensitySystemSpecs {
  total_ram_gb: number;
  available_ram_gb: number;
  cpu_cores: number;
  cpu_name: string;
  has_gpu: boolean;
  gpu_vram_gb: number | null;
  gpu_name: string | null;
  gpu_count: number;
  unified_memory: boolean;
  backend: string;
  gpus: DensityGPU[];
}

export interface DensityModelFit {
  name: string;
  score: number;
  fit_level: string;
  runtime: string;
  best_quant: string;
  estimated_tps: number;
  memory_required_gb: number;
  memory_available_gb: number;
  utilization_pct: number;
  category: string;
}

export interface DensityRuntime {
  name: string;
  installed: boolean;
}

export interface DensityInstalledModel {
  name: string;
  runtime: string;
}

export interface DensityNodeSummary {
  nodeName: string;
  lastSeen: string;
  stale: boolean;
  system: DensitySystemSpecs;
  modelFitCount: number;
  runtimes?: DensityRuntime[];
  installedModels?: DensityInstalledModel[];
}

export interface DensityNodeDetail {
  NodeName: string;
  LastSeen: string;
  System: DensitySystemSpecs;
  ModelFits: DensityModelFit[];
  Runtimes: DensityRuntime[];
  InstalledModels: DensityInstalledModel[];
}

export interface DraDevice {
  name: string;
  kind: string; // gpu | npu | cpu | nic
  model?: string;
  vendor?: string;
  memoryGi?: number;
  memoryBandwidthGBs?: number;
  computeTFLOPS?: number;
  healthy: boolean;
  healthReason?: string;
  linkLayer?: string; // nic: infiniband | ethernet (RoCE)
  rateGbps?: number;  // nic: link rate
}

export interface DraNodeSummary {
  nodeName: string;
  devices: DraDevice[];
}

export interface DraNodesResponse {
  available: boolean;
  nodes: DraNodeSummary[];
  total: number;
}

export interface DensityNodesResponse {
  nodes: DensityNodeSummary[];
  total: number;
}

export interface DensityNodeResult {
  nodeName: string;
  score: number;
  fitLevel: string;
  model: DensityModelFit;
}

export interface DensityQueryResponse {
  query: string;
  rankedNodes: DensityNodeResult[];
}

export interface DensityRuntimesResponse {
  nodes: { nodeName: string; runtimes: DensityRuntime[] }[];
}

export interface DensityInstalledModelsResponse {
  nodes: { nodeName: string; models: DensityInstalledModel[] }[];
}

export interface CatalogEntry {
  modelName: string;
  bestScore: number;
  bestNode: string;
  fitLevel: string;
  nodes: { nodeName: string; score: number; fitLevel: string }[];
}

export interface CatalogResponse {
  models: CatalogEntry[];
  total: number;
}

// ── Pricing (estimated / simulated spend) ────────────────────────────────────

/** One price-table entry. Amounts are integer micro-USD per 1M tokens
 *  (2500000 → $2.50/MTok). Used for both the cluster default table and
 *  user-defined simulated prices. */
export interface SimulatedPrice {
  provider: string;
  match: string;
  inputPerMTokMicro: number;
  outputPerMTokMicro: number;
}

export interface SimulatedPricing {
  simulatedEnabled: boolean;
  simulatedPrices: SimulatedPrice[];
  updatedAt?: string;
  updatedBy?: string;
}

export interface PricingResponse {
  currency: string;
  defaultTable: SimulatedPrice[];
  simulated: SimulatedPricing | null;
  localProviders: string[];
  /** False when the apiserver runs without auth — pricing writes return 403. */
  writable: boolean;
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
export const AUTH_UNAUTHORIZED_EVENT = "sympozium-auth-unauthorized";

function handleUnauthorized() {
  clearToken();
  window.dispatchEvent(new Event(AUTH_UNAUTHORIZED_EVENT));
}

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY);
}

export function setToken(token: string) {
  localStorage.setItem(TOKEN_KEY, token);
}

export function clearToken() {
  localStorage.removeItem(TOKEN_KEY);
}

// consumeUrlToken reads a login token from the URL fragment (#token=...),
// stores it, and scrubs it from the address bar. The fragment — never a
// query parameter — keeps the token out of server logs and Referer headers.
export function consumeUrlToken(): string | null {
  const match = window.location.hash.match(/^#token=(.+)$/);
  if (!match) return null;
  // Strip non-ASCII characters that would break HTTP headers (Firefox ByteString error)
  const token = decodeURIComponent(match[1]).replace(/[^\x20-\x7E]/g, "");
  history.replaceState(
    null,
    "",
    window.location.pathname + window.location.search,
  );
  if (!token) return null;
  setToken(token);
  return token;
}

export function getNamespace(): string {
  return localStorage.getItem(NS_KEY) || "default";
}

export function setNamespace(ns: string) {
  localStorage.setItem(NS_KEY, ns);
}

async function apiFetch<T>(
  path: string,
  init?: RequestInit & { skipNamespace?: boolean },
): Promise<T> {
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

  let url = path;
  if (!init?.skipNamespace) {
    const ns = getNamespace();
    const separator = path.includes("?") ? "&" : "?";
    url = `${path}${separator}namespace=${ns}`;
  }

  // Retry network errors (port-forward drops, transient failures) up to 2
  // times with a short delay.  Non-network errors (4xx, 5xx) are NOT retried
  // here — React Query handles those via its own retry config.
  const maxAttempts = 3;
  let lastError: unknown;
  for (let attempt = 0; attempt < maxAttempts; attempt++) {
    try {
      const res = await fetch(url, { ...init, headers });
      if (res.status === 401) {
        handleUnauthorized();
        throw new ApiError("Unauthorized", 401);
      }
      if (res.status === 204) return undefined as T;
      if (!res.ok) {
        const text = await res.text();
        // ApiError preserves the status code so callers can distinguish
        // e.g. 403 (writes disabled) from validation errors.
        throw new ApiError(text || `HTTP ${res.status}`, res.status);
      }
      // Guard against SPA HTML fallback for unrecognised routes which
      // would throw a SyntaxError from .json().  Only reject responses
      // that are explicitly HTML — missing Content-Type is fine (Go's
      // WriteHeader before writeJSON drops the header but body is JSON).
      const ct = res.headers.get("Content-Type") || "";
      if (ct.includes("text/html")) {
        throw new Error(
          "Unexpected HTML response — the API endpoint may not exist on this server version",
        );
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

// ── Agents ────────────────────────────────────────────────────────────────

export const api = {
  agents: {
    list: () => apiFetch<Agent[]>("/api/v1/agents"),
    get: (name: string) =>
      apiFetch<Agent>(`/api/v1/agents/${name}`),
    delete: (name: string) =>
      apiFetch<void>(`/api/v1/agents/${name}`, { method: "DELETE" }),
    patch: (
      name: string,
      data: {
        webEndpoint?: {
          enabled?: boolean;
          hostname?: string;
          rateLimit?: { requestsPerMinute?: number };
        };
        lifecycle?: LifecycleHooks | null;
        requireApproval?: boolean;
      },
    ) =>
      apiFetch<Agent>(`/api/v1/agents/${name}`, {
        method: "PATCH",
        body: JSON.stringify(data),
      }),
    create: (data: {
      name: string;
      provider: string;
      model: string;
      baseURL?: string;
      secretName?: string;
      apiKey?: string;
      awsRegion?: string;
      awsAccessKeyId?: string;
      awsSecretAccessKey?: string;
      awsSessionToken?: string;
      policyRef?: string;
      skills?: SkillRef[];
      channels?: ChannelSpec[];
      heartbeatInterval?: string;
      nodeSelector?: Record<string, string>;
      agentSandbox?: { enabled: boolean; runtimeClass?: string };
      runTimeout?: string;
      requireApproval?: boolean;
    }) =>
      apiFetch<Agent>("/api/v1/agents", {
        method: "POST",
        body: JSON.stringify(data),
      }),
  },

  runs: {
    list: () => apiFetch<AgentRun[]>("/api/v1/runs"),
    get: (name: string) => apiFetch<AgentRun>(`/api/v1/runs/${name}`),
    create: (data: {
      agentRef: string;
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
    gateVerdict: (name: string, data: GateVerdictRequest) =>
      apiFetch<AgentRun>(`/api/v1/runs/${name}/gate-verdict`, {
        method: "POST",
        body: JSON.stringify(data),
      }),
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
      agentRef: string;
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
    patch: (
      name: string,
      data: {
        schedule?: string;
        task?: string;
        type?: string;
        suspend?: boolean;
        concurrencyPolicy?: string;
      },
    ) =>
      apiFetch<SympoziumSchedule>(`/api/v1/schedules/${name}`, {
        method: "PATCH",
        body: JSON.stringify(data),
      }),
  },

  ensembles: {
    list: () => apiFetch<Ensemble[]>("/api/v1/ensembles"),
    get: (name: string) => apiFetch<Ensemble>(`/api/v1/ensembles/${name}`),
    delete: (name: string) =>
      apiFetch<void>(`/api/v1/ensembles/${name}`, { method: "DELETE" }),
    patch: (
      name: string,
      data: {
        enabled?: boolean;
        provider?: string;
        secretName?: string;
        apiKey?: string;
        model?: string;
        baseURL?: string;
        channelConfigs?: Record<string, string>;
        policyRef?: string;
        heartbeatInterval?: string;
        skillParams?: Record<string, Record<string, string>>;
        githubToken?: string;
        agentConfigs?: Array<{
          name: string;
          systemPrompt?: string;
          skills?: string[];
        }>;
        agentSandbox?: { enabled: boolean; runtimeClass?: string };
        relationships?: AgentConfigRelationship[];
        workflowType?: string;
        sharedMemory?: SharedMemorySpec;
        stimulus?: StimulusSpec;
      },
    ) =>
      apiFetch<Ensemble>(`/api/v1/ensembles/${name}`, {
        method: "PATCH",
        body: JSON.stringify(data),
      }),
    listSharedMemory: (
      name: string,
      opts?: { tags?: string; limit?: number; min_kind?: string; source_agent?: string },
    ) => {
      const params = new URLSearchParams();
      if (opts?.tags) params.set("tags", opts.tags);
      if (opts?.limit) params.set("limit", String(opts.limit));
      if (opts?.min_kind) params.set("min_kind", opts.min_kind);
      if (opts?.source_agent) params.set("source_agent", opts.source_agent);
      const qs = params.toString();
      return apiFetch<{ success: boolean; content: SharedMemoryEntry[] }>(
        `/api/v1/ensembles/${name}/shared-memory${qs ? `?${qs}` : ""}`,
      );
    },
    getSharedMemoryProvenance: (name: string, entryId: number) =>
      apiFetch<{ success: boolean; content: SharedMemoryEntry[] }>(
        `/api/v1/ensembles/${name}/shared-memory/${entryId}/provenance`,
      ),
    create: (data: {
      name: string;
      description?: string;
      category?: string;
      workflowType?: string;
      agentConfigs: AgentConfigSpec[];
      relationships?: AgentConfigRelationship[];
      sharedMemory?: SharedMemorySpec;
      modelRef?: string;
      stimulus?: StimulusSpec;
    }) =>
      apiFetch<Ensemble>("/api/v1/ensembles", {
        method: "POST",
        body: JSON.stringify(data),
      }),
    clone: (sourceName: string, newName: string) =>
      apiFetch<Ensemble>(`/api/v1/ensembles/${sourceName}/clone`, {
        method: "POST",
        body: JSON.stringify({ name: newName }),
      }),
    installDefaults: () =>
      apiFetch<InstallDefaultEnsemblesResponse>(
        "/api/v1/ensembles/install-defaults",
        {
          method: "POST",
        },
      ),
    triggerStimulus: (name: string) =>
      apiFetch<{ runName: string; target: string; stimulus: string }>(
        `/api/v1/ensembles/${name}/stimulus/trigger`,
        { method: "POST" },
      ),
  },

  mcpServers: {
    list: () => apiFetch<MCPServer[]>("/api/v1/mcpservers"),
    get: (name: string) => apiFetch<MCPServer>(`/api/v1/mcpservers/${name}`),
    create: (data: {
      name: string;
      transportType: string;
      toolsPrefix: string;
      url?: string;
      image?: string;
      timeout?: number;
      toolsAllow?: string[];
      toolsDeny?: string[];
      env?: Record<string, string>;
      secretRefs?: string[];
      args?: string[];
    }) =>
      apiFetch<MCPServer>("/api/v1/mcpservers", {
        method: "POST",
        body: JSON.stringify(data),
      }),
    delete: (name: string) =>
      apiFetch<void>(`/api/v1/mcpservers/${name}`, { method: "DELETE" }),
    patch: (
      name: string,
      data: {
        transportType?: string;
        url?: string;
        toolsPrefix?: string;
        timeout?: number;
        toolsAllow?: string[];
        toolsDeny?: string[];
        suspended?: boolean;
      },
    ) =>
      apiFetch<MCPServer>(`/api/v1/mcpservers/${name}`, {
        method: "PATCH",
        body: JSON.stringify(data),
      }),
    installDefaults: () =>
      apiFetch<InstallDefaultMCPServersResponse>(
        "/api/v1/mcpservers/install-defaults",
        { method: "POST" },
      ),
    authStatus: (name: string) =>
      apiFetch<MCPServerAuthStatusResponse>(
        `/api/v1/mcpservers/${name}/auth/status`,
      ),
    authToken: (name: string, token: string) =>
      apiFetch<MCPServerAuthStatusResponse>(
        `/api/v1/mcpservers/${name}/auth/token`,
        { method: "POST", body: JSON.stringify({ token }) },
      ),
  },

  nodes: {
    list: () => apiFetch<ClusterNode[]>("/api/v1/nodes"),
  },

  models: {
    list: (namespace?: string) =>
      apiFetch<Model[]>(
        `/api/v1/models${namespace ? `?namespace=${namespace}` : ""}`,
        { skipNamespace: true },
      ),
    get: (name: string, namespace?: string) =>
      apiFetch<Model>(
        `/api/v1/models/${name}${namespace ? `?namespace=${namespace}` : ""}`,
        { skipNamespace: true },
      ),
    create: (data: {
      name: string;
      serverType?: string;
      url?: string;
      modelID?: string;
      filename?: string;
      storageSize?: string;
      storageClass?: string;
      gpu?: number;
      memory?: string;
      cpu?: string;
      contextSize?: number;
      image?: string;
      port?: number;
      args?: string[];
      nodeSelector?: Record<string, string>;
      placement?: string;
      namespace?: string;
      huggingFaceTokenSecret?: string;
    }) =>
      apiFetch<Model>("/api/v1/models", {
        method: "POST",
        body: JSON.stringify(data),
        skipNamespace: true,
      }),
    delete: (name: string, namespace?: string) =>
      apiFetch<void>(
        `/api/v1/models/${name}${namespace ? `?namespace=${namespace}` : ""}`,
        { method: "DELETE", skipNamespace: true },
      ),
  },

  pods: {
    list: () => apiFetch<PodInfo[]>("/api/v1/pods"),
    logs: (name: string) =>
      apiFetch<{ logs: string }>(`/api/v1/pods/${name}/logs`),
  },

  namespaces: {
    list: () => apiFetch<string[]>("/api/v1/namespaces"),
  },

  cluster: {
    info: () => apiFetch<ClusterInfoResponse>("/api/v1/cluster"),
  },

  capabilities: {
    get: () => apiFetch<CapabilitiesResponse>("/api/v1/capabilities"),
  },

  agentSandbox: {
    install: (version?: string) =>
      apiFetch<{ installed: string[]; version: string }>(
        "/api/v1/agent-sandbox/install",
        {
          method: "POST",
          body: JSON.stringify(version ? { version } : {}),
        },
      ),
    uninstall: () =>
      apiFetch<{ deleted: string[] }>("/api/v1/agent-sandbox/install", {
        method: "DELETE",
      }),
  },

  observability: {
    metrics: () =>
      apiFetch<ObservabilityMetricsResponse>("/api/v1/observability/metrics"),
  },

  gateway: {
    get: () =>
      apiFetch<GatewayConfigResponse>("/api/v1/gateway", {
        skipNamespace: true,
      }),
    create: (data: PatchGatewayConfigRequest) =>
      apiFetch<GatewayConfigResponse>("/api/v1/gateway", {
        method: "POST",
        body: JSON.stringify(data),
        skipNamespace: true,
      }),
    patch: (data: PatchGatewayConfigRequest) =>
      apiFetch<GatewayConfigResponse>("/api/v1/gateway", {
        method: "PATCH",
        body: JSON.stringify(data),
        skipNamespace: true,
      }),
    delete: () =>
      apiFetch<void>("/api/v1/gateway", {
        method: "DELETE",
        skipNamespace: true,
      }),
    metrics: (range?: string) =>
      apiFetch<GatewayMetricsResponse>(
        `/api/v1/gateway/metrics${range ? `?range=${range}` : ""}`,
        { skipNamespace: true },
      ),
  },

  canary: {
    get: () =>
      apiFetch<CanaryConfigResponse>("/api/v1/canary", {
        skipNamespace: true,
      }),
    patch: (data: PatchCanaryConfigRequest) =>
      apiFetch<CanaryConfigResponse>("/api/v1/canary", {
        method: "PATCH",
        body: JSON.stringify(data),
        skipNamespace: true,
      }),
  },

  pricing: {
    get: () =>
      apiFetch<PricingResponse>("/api/v1/pricing", {
        skipNamespace: true,
      }),
    putSimulated: (data: {
      simulatedEnabled: boolean;
      simulatedPrices: SimulatedPrice[];
    }) =>
      apiFetch<SimulatedPricing>("/api/v1/pricing/simulated", {
        method: "PUT",
        body: JSON.stringify(data),
        skipNamespace: true,
      }),
    deleteSimulated: () =>
      apiFetch<void>("/api/v1/pricing/simulated", {
        method: "DELETE",
        skipNamespace: true,
      }),
  },

  providers: {
    nodes: () => apiFetch<ProviderNode[]>("/api/v1/providers/nodes"),
    models: (baseURL: string, apiKey?: string) =>
      apiFetch<ProviderModelsResponse>(
        `/api/v1/providers/models?baseURL=${encodeURIComponent(baseURL)}`,
        apiKey ? { headers: { "X-Provider-Api-Key": apiKey } } : undefined,
      ),
    bedrockModels: (data: {
      region: string;
      accessKeyId: string;
      secretAccessKey: string;
      sessionToken?: string;
    }) =>
      apiFetch<ProviderModelsResponse>("/api/v1/providers/bedrock/models", {
        method: "POST",
        body: JSON.stringify(data),
      }),
  },

  dra: {
    nodes: () =>
      apiFetch<DraNodesResponse>("/api/v1/dra/nodes", {
        skipNamespace: true,
      }),
  },

  density: {
    nodes: () =>
      apiFetch<DensityNodesResponse>("/api/v1/density/nodes", {
        skipNamespace: true,
      }),
    node: (name: string) =>
      apiFetch<DensityNodeDetail>(`/api/v1/density/nodes/${name}`, {
        skipNamespace: true,
      }),
    runtimes: () =>
      apiFetch<DensityRuntimesResponse>("/api/v1/density/runtimes", {
        skipNamespace: true,
      }),
    installedModels: () =>
      apiFetch<DensityInstalledModelsResponse>(
        "/api/v1/density/installed-models",
        { skipNamespace: true },
      ),
    query: (model: string, minFit?: string) =>
      apiFetch<DensityQueryResponse>(
        `/api/v1/density/query?model=${encodeURIComponent(model)}${minFit ? `&min_fit=${minFit}` : ""}`,
        { skipNamespace: true },
      ),
    catalog: () =>
      apiFetch<CatalogResponse>("/api/v1/catalog", {
        skipNamespace: true,
      }),
  },

  githubAuth: {
    setToken: (token: string) =>
      apiFetch<GithubAuthStatusResponse>(
        "/api/v1/skills/github-gitops/auth/token",
        {
          method: "POST",
          body: JSON.stringify({ token }),
        },
      ),
    status: () =>
      apiFetch<GithubAuthStatusResponse>(
        "/api/v1/skills/github-gitops/auth/status",
      ),
  },
};
