import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { toast } from "sonner";

/** Show a user-friendly toast for mutation errors.  Network failures get a
 *  clearer message than the raw TypeError from fetch. */
function toastError(err: Error) {
  const isNetwork =
    err instanceof TypeError ||
    /network|failed to fetch|load failed/i.test(err.message);
  toast.error(
    isNetwork
      ? "Connection lost — the port-forward may have dropped. Please retry."
      : err.message,
  );
}

// ── Capabilities ────────────────────────────────────────────────────────────

export function useCapabilities() {
  return useQuery({
    queryKey: ["capabilities"],
    queryFn: api.capabilities.get,
    staleTime: 60_000,
  });
}

// ── Agent Sandbox CRD Management ────────────────────────────────────────────

export function useInstallAgentSandbox() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (version?: string) => api.agentSandbox.install(version),
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: ["capabilities"] });
      toast.success(
        `Installed ${data.installed.length} Agent Sandbox CRDs (${data.version})`,
      );
    },
    onError: toastError,
  });
}

export function useUninstallAgentSandbox() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.agentSandbox.uninstall(),
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: ["capabilities"] });
      toast.success(`Removed ${data.deleted.length} Agent Sandbox CRDs`);
    },
    onError: toastError,
  });
}

// ── Namespaces ───────────────────────────────────────────────────────────────

export function useNamespaces() {
  return useQuery({ queryKey: ["namespaces"], queryFn: api.namespaces.list });
}

// ── Instances ────────────────────────────────────────────────────────────────

export function useAgents() {
  return useQuery({ queryKey: ["agents"], queryFn: api.agents.list });
}

export function useAgent(name: string) {
  return useQuery({
    queryKey: ["agents", name],
    queryFn: () => api.agents.get(name),
    enabled: !!name,
  });
}

export function useDeleteAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.agents.delete,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["agents"] });
      toast.success("Agent deleted");
    },
    onError: toastError,
  });
}

export function useCreateAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.agents.create,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["agents"] });
      toast.success("Agent created");
    },
    onError: toastError,
  });
}

export function usePatchAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      name,
      data,
    }: {
      name: string;
      data: Parameters<typeof api.agents.patch>[1];
    }) => api.agents.patch(name, data),
    onSuccess: (_data, variables) => {
      qc.invalidateQueries({ queryKey: ["agents"] });
      qc.invalidateQueries({ queryKey: ["agents", variables.name] });
      toast.success("Agent updated");
    },
    onError: toastError,
  });
}

// ── Runs ─────────────────────────────────────────────────────────────────────

export function useRuns() {
  return useQuery({
    queryKey: ["runs"],
    queryFn: api.runs.list,
    refetchInterval: 5000,
  });
}

export function useRun(name: string) {
  return useQuery({
    queryKey: ["runs", name],
    queryFn: () => api.runs.get(name),
    enabled: !!name,
    refetchInterval: 5000,
  });
}

export function useCreateRun() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.runs.create,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["runs"] });
      toast.success("Run created");
    },
    onError: toastError,
  });
}

export function useDeleteRun() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.runs.delete,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["runs"] });
      toast.success("Run deleted");
    },
    onError: toastError,
  });
}

export function useGateVerdict() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      name,
      data,
    }: {
      name: string;
      data: Parameters<typeof api.runs.gateVerdict>[1];
    }) => api.runs.gateVerdict(name, data),
    onSuccess: (_data, variables) => {
      qc.invalidateQueries({ queryKey: ["runs"] });
      qc.invalidateQueries({ queryKey: ["runs", variables.name] });
      toast.success(
        `Run ${variables.data.action === "approve" ? "approved" : "rejected"}`,
      );
    },
    onError: toastError,
  });
}

// ── Policies ─────────────────────────────────────────────────────────────────

export function usePolicies() {
  return useQuery({ queryKey: ["policies"], queryFn: api.policies.list });
}

export function usePolicy(name: string) {
  return useQuery({
    queryKey: ["policies", name],
    queryFn: () => api.policies.get(name),
    enabled: !!name,
  });
}

export function useDeletePolicy() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.policies.delete,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["policies"] });
      toast.success("Policy deleted");
    },
    onError: toastError,
  });
}

// ── Skills ───────────────────────────────────────────────────────────────────

export function useSkills() {
  return useQuery({ queryKey: ["skills"], queryFn: api.skills.list });
}

export function useSkill(name: string) {
  return useQuery({
    queryKey: ["skills", name],
    queryFn: () => api.skills.get(name),
    enabled: !!name,
  });
}

// ── Schedules ────────────────────────────────────────────────────────────────

export function useSchedules() {
  return useQuery({ queryKey: ["schedules"], queryFn: api.schedules.list });
}

export function useSchedule(name: string) {
  return useQuery({
    queryKey: ["schedules", name],
    queryFn: () => api.schedules.get(name),
    enabled: !!name,
  });
}

export function useCreateSchedule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.schedules.create,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["schedules"] });
      toast.success("Schedule created");
    },
    onError: toastError,
  });
}

export function useUpdateSchedule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      name,
      ...data
    }: {
      name: string;
      schedule?: string;
      task?: string;
      type?: string;
      suspend?: boolean;
      concurrencyPolicy?: string;
    }) => api.schedules.patch(name, data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["schedules"] });
      toast.success("Schedule updated");
    },
    onError: toastError,
  });
}

export function useDeleteSchedule() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.schedules.delete,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["schedules"] });
      toast.success("Schedule deleted");
    },
    onError: toastError,
  });
}

// ── Ensembles ─────────────────────────────────────────────────────────────

export function useEnsembles() {
  return useQuery({
    queryKey: ["ensembles"],
    queryFn: api.ensembles.list,
  });
}

export function useEnsemble(name: string) {
  return useQuery({
    queryKey: ["ensembles", name],
    queryFn: () => api.ensembles.get(name),
    enabled: !!name,
  });
}

export function useDeleteEnsemble() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.ensembles.delete,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ensembles"] });
      toast.success("Ensemble deleted");
    },
    onError: toastError,
  });
}

export function useActivateEnsemble() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      name,
      ...data
    }: {
      name: string;
      enabled?: boolean;
      provider?: string;
      secretName?: string;
      apiKey?: string;
      awsRegion?: string;
      awsAccessKeyId?: string;
      awsSecretAccessKey?: string;
      awsSessionToken?: string;
      model?: string;
      baseURL?: string;
      channels?: string[];
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
      modelRef?: string;
    }) => api.ensembles.patch(name, data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ensembles"] });
      toast.success("Ensemble updated");
    },
    onError: toastError,
  });
}

export function usePatchEnsembleRelationships() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      name,
      relationships,
      workflowType,
    }: {
      name: string;
      relationships: import("@/lib/api").AgentConfigRelationship[];
      workflowType?: string;
    }) => api.ensembles.patch(name, { relationships, workflowType }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ensembles"] });
      toast.success("Workflow updated");
    },
    onError: toastError,
  });
}

export function useTriggerStimulus() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => api.ensembles.triggerStimulus(name),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ensembles"] });
      toast.success("Stimulus triggered");
    },
    onError: toastError,
  });
}

export function usePatchEnsembleStimulus() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ name, stimulus }: { name: string; stimulus: { name: string; prompt: string } }) =>
      api.ensembles.patch(name, { stimulus }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ensembles"] });
      toast.success("Stimulus updated");
    },
    onError: toastError,
  });
}

export function useInstallDefaultEnsembles() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.ensembles.installDefaults,
    onSuccess: (result) => {
      qc.invalidateQueries({ queryKey: ["ensembles"] });
      const copied = result.copied.length;
      const existing = result.alreadyPresent.length;
      toast.success(
        copied > 0
          ? `Installed ${copied} default pack${copied === 1 ? "" : "s"} (${existing} already present)`
          : `No packs installed (${existing} already present)`,
      );
    },
    onError: toastError,
  });
}

export function useCreateEnsemble() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.ensembles.create,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ensembles"] });
      toast.success("Ensemble created");
    },
    onError: toastError,
  });
}

export function useCloneEnsemble() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      sourceName,
      newName,
    }: {
      sourceName: string;
      newName: string;
    }) => api.ensembles.clone(sourceName, newName),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["ensembles"] });
      toast.success("Ensemble cloned");
    },
    onError: toastError,
  });
}

export function useSharedMemory(
  ensembleName: string,
  filters?: { tags?: string; min_kind?: string; source_agent?: string; limit?: number },
) {
  return useQuery({
    queryKey: ["ensembles", ensembleName, "shared-memory", filters],
    queryFn: () => api.ensembles.listSharedMemory(ensembleName, filters),
    enabled: !!ensembleName,
    refetchInterval: 5000,
  });
}

export function useSharedMemoryProvenance(ensembleName: string, entryId: number | null) {
  return useQuery({
    queryKey: ["ensembles", ensembleName, "shared-memory", entryId, "provenance"],
    queryFn: () => api.ensembles.getSharedMemoryProvenance(ensembleName, entryId!),
    enabled: !!ensembleName && entryId !== null,
  });
}

// ── MCP Servers ─────────────────────────────────────────────────────────────

export function useInstallDefaultMcpServers() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.mcpServers.installDefaults,
    onSuccess: (result) => {
      qc.invalidateQueries({ queryKey: ["mcpServers"] });
      const copied = result.copied.length;
      const existing = result.alreadyPresent.length;
      toast.success(
        copied > 0
          ? `Installed ${copied} default MCP server${copied === 1 ? "" : "s"} (${existing} already present)`
          : `No servers installed (${existing} already present)`,
      );
    },
    onError: toastError,
  });
}

export function useMcpServerAuthStatus(name: string) {
  return useQuery({
    queryKey: ["mcpServers", name, "authStatus"],
    queryFn: () => api.mcpServers.authStatus(name),
    enabled: !!name,
  });
}

export function useMcpServerAuthToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ name, token }: { name: string; token: string }) =>
      api.mcpServers.authToken(name, token),
    onSuccess: (_result, { name }) => {
      qc.invalidateQueries({ queryKey: ["mcpServers", name, "authStatus"] });
      qc.invalidateQueries({ queryKey: ["mcpServers"] });
      toast.success("Token saved");
    },
    onError: toastError,
  });
}

export function useMcpServers() {
  return useQuery({ queryKey: ["mcpServers"], queryFn: api.mcpServers.list });
}

export function useMcpServer(name: string) {
  return useQuery({
    queryKey: ["mcpServers", name],
    queryFn: () => api.mcpServers.get(name),
    enabled: !!name,
  });
}

export function useCreateMcpServer() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.mcpServers.create,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["mcpServers"] });
      toast.success("MCP server created");
    },
    onError: toastError,
  });
}

export function useDeleteMcpServer() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.mcpServers.delete,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["mcpServers"] });
      toast.success("MCP server deleted");
    },
    onError: toastError,
  });
}

export function usePatchMcpServer() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      name,
      ...data
    }: {
      name: string;
      transportType?: string;
      url?: string;
      toolsPrefix?: string;
      timeout?: number;
      toolsAllow?: string[];
      toolsDeny?: string[];
      suspended?: boolean;
    }) => api.mcpServers.patch(name, data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["mcpServers"] });
      toast.success("MCP server updated");
    },
    onError: toastError,
  });
}

// ── Nodes ───────────────────────────────────────────────────────────────────

export function useClusterNodes() {
  return useQuery({
    queryKey: ["nodes"],
    queryFn: api.nodes.list,
  });
}

// ── Models ──────────────────────────────────────────────────────────────────

export function useModels() {
  return useQuery({
    queryKey: ["models"],
    queryFn: () => api.models.list(),
    refetchInterval: 5000,
  });
}

export function useModel(name: string, namespace?: string) {
  return useQuery({
    queryKey: ["models", name, namespace],
    queryFn: () => api.models.get(name, namespace),
    enabled: !!name,
    refetchInterval: 5000,
  });
}

export function useCreateModel() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.models.create,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["models"] });
      toast.success("Model created");
    },
    onError: toastError,
  });
}

export function useDeleteModel() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      name,
      namespace,
    }: {
      name: string;
      namespace?: string;
    }) => api.models.delete(name, namespace),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["models"] });
      toast.success("Model deleted");
    },
    onError: toastError,
  });
}

// ── Cluster Info ─────────────────────────────────────────────────────────────

export function useClusterInfo() {
  return useQuery({
    queryKey: ["cluster", "info"],
    queryFn: api.cluster.info,
    refetchInterval: 15000,
  });
}

// ── Pods ─────────────────────────────────────────────────────────────────────

export function usePods() {
  return useQuery({ queryKey: ["pods"], queryFn: api.pods.list });
}

// ── Gateway ─────────────────────────────────────────────────────────────────

export function useGatewayConfig() {
  return useQuery({
    queryKey: ["gateway"],
    queryFn: api.gateway.get,
    refetchInterval: 10000,
  });
}

export function usePatchGatewayConfig() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.gateway.patch,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["gateway"] });
    },
    onError: toastError,
  });
}

export function useCreateGatewayConfig() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.gateway.create,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["gateway"] });
    },
    onError: toastError,
  });
}

export function useDeleteGatewayConfig() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.gateway.delete,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["gateway"] });
    },
    onError: toastError,
  });
}

// ── Canary ──────────────────────────────────────────────────────────────────

export function useCanaryConfig() {
  return useQuery({
    queryKey: ["canary"],
    queryFn: api.canary.get,
    refetchInterval: 15000,
  });
}

export function usePatchCanaryConfig() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.canary.patch,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["canary"] });
    },
    onError: toastError,
  });
}

// ── Pricing ─────────────────────────────────────────────────────────────────

export function usePricing() {
  return useQuery({
    queryKey: ["pricing"],
    queryFn: api.pricing.get,
  });
}

export function usePutSimulatedPrices() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.pricing.putSimulated,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["pricing"] });
      toast.success("Simulated prices saved");
    },
    onError: toastError,
  });
}

export function useDeleteSimulatedPrices() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.pricing.deleteSimulated,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["pricing"] });
      toast.success("Simulated prices cleared");
    },
    onError: toastError,
  });
}

// ── Observability ───────────────────────────────────────────────────────────

export function useObservabilityMetrics() {
  return useQuery({
    queryKey: ["observability", "metrics"],
    queryFn: api.observability.metrics,
    refetchInterval: 10000,
  });
}

export function useGatewayMetrics(range_?: string) {
  return useQuery({
    queryKey: ["gateway", "metrics", range_],
    queryFn: () => api.gateway.metrics(range_),
    refetchInterval: 10000,
  });
}

// ── DRA inventory (llmfit-dra ResourceSlices) ────────────────────────────────────

export function useDraNodes() {
  return useQuery({
    queryKey: ["dra", "nodes"],
    queryFn: api.dra.nodes,
    refetchInterval: 30000,
  });
}

// ── Accelerator power (energy collector) ─────────────────────────────────────

/** Live per-accelerator power draw. Polls fast because watts are the one thing
 * on these views that genuinely moves; the apiserver caches so this does not
 * hammer the collector. Returns available:false when no collector is
 * installed, and callers omit the power surface rather than showing 0 W.
 *
 * `enabled` is false when a caller supplies its own readings (the topology
 * demo's simulation) — there is no cluster to ask, so don't ask it. Prefer
 * usePowerIndex() in lib/power-context over calling this directly. */
export function usePower(enabled = true) {
  return useQuery({
    queryKey: ["power", "fleet"],
    queryFn: api.power.fleet,
    refetchInterval: 2000,
    enabled,
  });
}

// ── Model Density (llmfit DaemonSet) ─────────────────────────────────────────────

export function useDensityNodes() {
  return useQuery({
    queryKey: ["density", "nodes"],
    queryFn: api.density.nodes,
    refetchInterval: 30000,
  });
}

export function useDensityNode(name: string) {
  return useQuery({
    queryKey: ["density", "nodes", name],
    queryFn: () => api.density.node(name),
    enabled: !!name,
    refetchInterval: 30000,
  });
}

export function useDensityQuery(model: string, minFit?: string) {
  return useQuery({
    queryKey: ["density", "query", model, minFit],
    queryFn: () => api.density.query(model, minFit),
    enabled: !!model,
    refetchInterval: 30000,
  });
}

export function useDensityRuntimes() {
  return useQuery({
    queryKey: ["density", "runtimes"],
    queryFn: api.density.runtimes,
    refetchInterval: 30000,
  });
}

export function useDensityInstalledModels() {
  return useQuery({
    queryKey: ["density", "installed-models"],
    queryFn: api.density.installedModels,
    refetchInterval: 30000,
  });
}

export function useModelCatalog() {
  return useQuery({
    queryKey: ["catalog"],
    queryFn: api.density.catalog,
    refetchInterval: 30000,
  });
}
