// Shared presentation helpers for llmfit-dra device inventory
// (GET /api/v1/dra/nodes) — used by the topology node cards and the
// Placement & Density nodes tree.
import type { DraDevice } from "@/lib/api";

/** Vendor model strings can be slash-joined variant lists ("Radeon Graphics /
 * Radeon 8050S Graphics / Radeon 8060S Graphics") — keep the most specific
 * variant and cap the length so one device can't blow up a card layout. */
export function shortModel(model: string): string {
  const last = model.split("/").map((s) => s.trim()).filter(Boolean).pop() || model;
  return last.length > 28 ? last.slice(0, 27) + "…" : last;
}

/** Compact chip text for a DRA device: kind + the fact that matters most. */
export function draDeviceLabel(d: DraDevice): string {
  if (d.kind === "nic") {
    const rate = d.rateGbps ? ` ${d.rateGbps}G` : "";
    return `nic · ${d.linkLayer || "rdma"}${rate}`;
  }
  const model = d.model ? ` · ${shortModel(d.model)}` : "";
  const mem = d.memoryGi ? ` ${d.memoryGi}Gi` : "";
  return `${d.kind}${model}${mem}`;
}

/** Fuller single-line description for list/tree views where there is room
 * for the physics: bandwidth and compute alongside identity. */
export function draDeviceDetail(d: DraDevice): string {
  if (d.kind === "nic") {
    const rate = d.rateGbps ? ` · ${d.rateGbps} Gb` : "";
    // A NIC's part matters as much as a GPU's on a fabric-bound node — an NDR
    // ConnectX and a RoCE ConnectX-6 are not interchangeable for GPUDirect.
    const model = d.model ? `${shortModel(d.model)} · ` : "";
    return `${model}${d.linkLayer || "rdma"}${rate}`;
  }
  const parts: string[] = [];
  if (d.model) parts.push(shortModel(d.model));
  if (d.memoryGi) parts.push(`${d.memoryGi}Gi`);
  if (d.memoryBandwidthGBs) parts.push(`${d.memoryBandwidthGBs} GB/s`);
  if (d.computeTFLOPS) parts.push(`${d.computeTFLOPS} TF`);
  return parts.join(" · ");
}

/** One entry per accelerator *flavour* (identical kind+model+link collapse
 * into a ×N count). Order: gpu, npu, nic; CPU is the node itself, not a leaf. */
export interface DraGroup {
  key: string;
  kind: string;
  count: number;
  sample: DraDevice;
  healthy: boolean;
  names: string[];
  reasons: string[];
  /** The devices behind the ×N count. Identical devices collapse for
   * inventory, but power is per-device — a view showing watts must expand
   * the group rather than attribute one reading to N accelerators. */
  devices: DraDevice[];
}

export function groupAccelerators(devices: DraDevice[]): DraGroup[] {
  const order: Record<string, number> = { gpu: 0, npu: 1, nic: 2 };
  const groups = new Map<string, DraGroup>();
  for (const d of devices) {
    if (d.kind === "cpu") continue;
    const key = [d.kind, d.model || "", d.memoryGi || 0, d.linkLayer || "", d.rateGbps || 0].join("|");
    const g = groups.get(key);
    if (g) {
      g.count++;
      g.names.push(d.name);
      g.devices.push(d);
      g.healthy = g.healthy && d.healthy;
      if (!d.healthy && d.healthReason) g.reasons.push(d.healthReason);
    } else {
      groups.set(key, {
        key,
        kind: d.kind,
        count: 1,
        sample: d,
        healthy: d.healthy,
        names: [d.name],
        reasons: !d.healthy && d.healthReason ? [d.healthReason] : [],
        devices: [d],
      });
    }
  }
  return [...groups.values()].sort(
    (a, b) => (order[a.kind] ?? 9) - (order[b.kind] ?? 9) || a.key.localeCompare(b.key),
  );
}
