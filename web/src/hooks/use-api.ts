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

// ── Namespaces ───────────────────────────────────────────────────────────────

export function useNamespaces() {
  return useQuery({ queryKey: ["namespaces"], queryFn: api.namespaces.list });
}

// ── Instances ────────────────────────────────────────────────────────────────

export function useInstances() {
  return useQuery({ queryKey: ["instances"], queryFn: api.instances.list });
}

export function useInstance(name: string) {
  return useQuery({
    queryKey: ["instances", name],
    queryFn: () => api.instances.get(name),
    enabled: !!name,
  });
}

export function useDeleteInstance() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.instances.delete,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["instances"] });
      toast.success("Instance deleted");
    },
    onError: toastError,
  });
}

export function useCreateInstance() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.instances.create,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["instances"] });
      toast.success("Instance created");
    },
    onError: toastError,
  });
}

// ── Runs ─────────────────────────────────────────────────────────────────────

export function useRuns() {
  return useQuery({ queryKey: ["runs"], queryFn: api.runs.list });
}

export function useRun(name: string) {
  return useQuery({
    queryKey: ["runs", name],
    queryFn: () => api.runs.get(name),
    enabled: !!name,
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

// ── PersonaPacks ─────────────────────────────────────────────────────────────

export function usePersonaPacks() {
  return useQuery({
    queryKey: ["personaPacks"],
    queryFn: api.personaPacks.list,
  });
}

export function usePersonaPack(name: string) {
  return useQuery({
    queryKey: ["personaPacks", name],
    queryFn: () => api.personaPacks.get(name),
    enabled: !!name,
  });
}

export function useDeletePersonaPack() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: api.personaPacks.delete,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["personaPacks"] });
      toast.success("Persona pack deleted");
    },
    onError: toastError,
  });
}

export function useActivatePersonaPack() {
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
      model?: string;
      baseURL?: string;
      channels?: string[];
      channelConfigs?: Record<string, string>;
      policyRef?: string;
      skills?: string[];
    }) => api.personaPacks.patch(name, data),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["personaPacks"] });
      toast.success("Persona pack updated");
    },
    onError: toastError,
  });
}

// ── Pods ─────────────────────────────────────────────────────────────────────

export function usePods() {
  return useQuery({ queryKey: ["pods"], queryFn: api.pods.list });
}
