// Presentation helpers for accelerator power draw (GET /api/v1/power).
//
// One rule governs this file: never render a number we did not measure. A
// suspended accelerator, an unmeasurable one, and a genuinely idle one all
// arrive as 0 mW — but only the last is a reading. Showing "0 W" for the other
// two invents data, so they render as a dash with the reason.
import type { DevicePower, PowerResponse } from "@/lib/api";

/** Watts, at a precision that matches the sensors (~0.1 W) without implying
 * more. Sub-watt readings keep a second decimal so an idling device doesn't
 * flatten to "0.0 W" and read as "off". */
export function formatWatts(milliwatts: number): string {
  const w = milliwatts / 1000;
  if (w > 0 && w < 1) return `${w.toFixed(2)} W`;
  return `${w.toFixed(1)} W`;
}

/** Why a device has no reading, or null when it does. */
export function unmeasuredReason(d: DevicePower): string | null {
  if (d.measured) return null;
  if (d.suspended) return "suspended";
  return "unmeasurable";
}

/** What to render for a device's power: a measured value, or a dash. */
export function powerText(d: DevicePower): string {
  return d.measured ? formatWatts(d.powerMilliwatts) : "—";
}

/** Index power readings by "node/pciAddress" — the identity tuple shared by
 * the collector and DRA inventory. */
export type PowerIndex = Map<string, DevicePower>;

export function indexPower(resp: PowerResponse | undefined): PowerIndex {
  const idx: PowerIndex = new Map();
  if (!resp?.available) return idx;
  for (const d of resp.devices || []) idx.set(`${d.node}/${d.address}`, d);
  return idx;
}

export function lookupPower(
  idx: PowerIndex,
  node: string | undefined,
  pciAddress: string | undefined,
): DevicePower | undefined {
  if (!node || !pciAddress) return undefined;
  return idx.get(`${node}/${pciAddress}`);
}

/** Sum of measured, fresh readings for one node, plus how many devices that
 * covers — so a caller can say "2 of 3 measured" instead of implying the
 * total is the whole node. */
export function nodeTotal(
  idx: PowerIndex,
  node: string,
): { milliwatts: number; measured: number; total: number } {
  let milliwatts = 0;
  let measured = 0;
  let total = 0;
  for (const d of idx.values()) {
    if (d.node !== node) continue;
    total++;
    if (d.measured && !d.stale) {
      milliwatts += d.powerMilliwatts;
      measured++;
    }
  }
  return { milliwatts, measured, total };
}

/** Component breakdown ordered for reading: the whole first, then the parts.
 * Keys are collector-defined and open-ended, so unknown ones sort last rather
 * than being dropped. */
export function orderedComponents(d: DevicePower): Array<[string, number]> {
  const order = ["socket", "gfx", "cpu_cores", "npu"];
  return Object.entries(d.components || {}).sort(
    (a, b) =>
      (order.indexOf(a[0]) + 1 || 99) - (order.indexOf(b[0]) + 1 || 99) ||
      a[0].localeCompare(b[0]),
  );
}
