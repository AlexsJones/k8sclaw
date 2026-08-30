import { Badge } from "@/components/ui/badge";
import { Zap } from "lucide-react";
import { draDeviceDetail, groupAccelerators } from "@/lib/dra";
import {
  formatWatts,
  lookupPower,
  orderedComponents,
  powerText,
  unmeasuredReason,
} from "@/lib/power";
import { usePowerIndex } from "@/lib/power-context";
import type { DevicePower, DraDevice } from "@/lib/api";

/** Live power draw for one accelerator, joined from the energy collector.
 * A device with no reading renders a dash and why — never "0 W", which would
 * be indistinguishable from a genuinely idle accelerator. */
function PowerChip({ power }: { power: DevicePower }) {
  const reason = unmeasuredReason(power);
  const components = orderedComponents(power);

  // The tooltip carries the decomposition (socket vs gfx on an APU) and the
  // provenance of a missing reading, so the row itself stays one line.
  const title = reason
    ? `no reading — ${reason}`
    : [
        `${formatWatts(power.powerMilliwatts)} board/socket`,
        ...components.map(([k, mw]) => `${k}: ${formatWatts(mw)}`),
        power.stale ? "last-known value — collector could not refresh" : "",
      ]
        .filter(Boolean)
        .join("\n");

  if (reason) {
    return (
      <span className="shrink-0 whitespace-nowrap text-muted-foreground/60 tabular-nums" title={title}>
        — <span className="text-[9px] uppercase tracking-wider">{reason}</span>
      </span>
    );
  }

  return (
    <span
      className={
        power.stale
          ? "shrink-0 whitespace-nowrap tabular-nums text-muted-foreground italic"
          : "shrink-0 whitespace-nowrap tabular-nums text-amber-600 dark:text-amber-500"
      }
      title={title}
    >
      {powerText(power)}
      {power.stale && <span className="ml-1 text-[9px] uppercase tracking-wider">stale</span>}
    </span>
  );
}

function DeviceRow({
  device,
  power,
  prefix,
  label,
  title,
}: {
  device: DraDevice;
  power?: DevicePower;
  prefix: string;
  label: string;
  title: string;
}) {
  return (
    <div
      className={
        device.healthy
          ? "flex items-baseline gap-1.5"
          : "flex items-baseline gap-1.5 text-destructive"
      }
      title={title}
    >
      <span className="text-muted-foreground/60 select-none">{prefix}</span>
      <span className="uppercase text-[10px] tracking-wider text-muted-foreground shrink-0">
        {label}
      </span>
      <span className="truncate">{draDeviceDetail(device)}</span>
      {!device.healthy && (
        <Badge variant="destructive" className="text-[9px] px-1 py-0 shrink-0">
          {device.healthReason || "unhealthy"}
        </Badge>
      )}
      {power && (
        <span className="ml-auto shrink-0 pl-1.5">
          <PowerChip power={power} />
        </span>
      )}
    </div>
  );
}

/** Accelerators as tree leaves under a node — inventory from llmfit-dra
 * ResourceSlices, decorated with live power draw when an energy collector is
 * present. Used by the Placement & Density node cards and the topology's K8s
 * node cards.
 *
 * Pass `node` to enable the power join: readings are keyed by node + PCI
 * address, so without the node name a device is not uniquely identifiable and
 * the view degrades to plain inventory. */
export function AcceleratorLeaves({
  devices,
  node,
}: {
  devices: DraDevice[];
  node?: string;
}) {
  const powerIdx = usePowerIndex();

  const groups = groupAccelerators(devices);
  if (groups.length === 0) return null;

  // Identical accelerators collapse into a ×N row for inventory, but power is
  // per-device: expand any group we have readings for, rather than showing one
  // device's watts as if it spoke for all N.
  const rows = groups.flatMap((g) => {
    const powers = g.devices.map((d) => lookupPower(powerIdx, node, d.pciAddress));
    const groupTitle = g.healthy
      ? g.names.join(", ")
      : `${g.names.join(", ")} — ${g.reasons.join(", ") || "unhealthy"}`;

    if (g.count === 1 || !powers.some(Boolean)) {
      return [
        {
          key: g.key,
          device: g.sample,
          power: powers[0],
          label: g.count > 1 ? `${g.count}× ${g.kind}` : g.kind,
          title: groupTitle,
        },
      ];
    }
    return g.devices.map((d, i) => ({
      key: `${g.key}|${d.name}`,
      device: d,
      power: powers[i],
      label: g.kind,
      title: d.healthy ? d.name : `${d.name} — ${d.healthReason || "unhealthy"}`,
    }));
  });

  return (
    <div className="pt-1 font-mono text-xs">
      <div className="flex items-center gap-1.5 text-muted-foreground">
        <Zap className="h-3 w-3" />
        <span>accelerators</span>
      </div>
      {rows.map((r, i) => (
        <DeviceRow
          key={r.key}
          device={r.device}
          power={r.power}
          label={r.label}
          title={r.title}
          prefix={i === rows.length - 1 ? "└─" : "├─"}
        />
      ))}
    </div>
  );
}
