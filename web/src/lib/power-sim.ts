// Simulated accelerator power for the topology demo. DEMO ONLY.
//
// Nothing here may be imported by a view that renders real cluster state. The
// numbers are invented; they exist so the demo canvas shows the power surface
// doing what it does on real hardware — moving.
//
// The simulation is a pure function of (device, time): no state, no random
// walk to drift out of band, and identical for every viewer at a given moment.
// It deliberately reproduces the awkward states as well as the happy one — a
// suspended device and a stale node — because those are exactly what the UI
// has to get right, and a demo that only ever shows a clean reading hides the
// interesting half of the design.
import type { DevicePower } from "@/lib/api";
import type { PowerIndex } from "@/lib/power";

/** Idle/busy band for a device class, in watts. Roughly the real envelopes:
 * an A100 SXM tops out near 400 W, an H100 near 700 W, an L40S near 350 W. */
export interface PowerBand {
  idleWatts: number;
  busyWatts: number;
}

export interface SimDevice {
  node: string;
  address: string;
  kind: string;
  driver: string;
  band: PowerBand;
  /** Fraction of the band this device typically sits at (0 = idle, 1 = busy). */
  load: number;
  /** Runtime-PM suspended: reports a synthetic 0 W, never a measurement. */
  suspended?: boolean;
  /** Last-known value the collector could not refresh. */
  stale?: boolean;
}

/** Stable per-device phase, so each accelerator breathes independently but
 * identically across reloads. */
function phaseOf(key: string): number {
  let h = 0;
  for (let i = 0; i < key.length; i++) h = (h * 31 + key.charCodeAt(i)) | 0;
  return ((h % 1000) / 1000) * Math.PI * 2;
}

function clamp01(x: number): number {
  return x < 0 ? 0 : x > 1 ? 1 : x;
}

/** Where in its band a device sits at time t: a slow drift (workload phases)
 * plus a faster ripple (per-batch variation), both smooth so the number reads
 * as a measurement rather than a random number generator. */
function bandPosition(key: string, load: number, t: number): number {
  const p = phaseOf(key);
  const slow = Math.sin(t / 11000 + p);
  const fast = Math.sin(t / 2300 + p * 3);
  return clamp01(load + 0.16 * slow + 0.06 * fast);
}

function watts(d: SimDevice, t: number): number {
  const u = bandPosition(d.node + "/" + d.address, d.load, t);
  return d.band.idleWatts + (d.band.busyWatts - d.band.idleWatts) * u;
}

/** One device's reading at time t, in the same shape the real collector
 * produces — including the honesty flags, so the demo exercises the same
 * rendering paths as live data. */
export function simulateDevice(d: SimDevice, t: number): DevicePower {
  if (d.suspended) {
    return {
      node: d.node,
      address: d.address,
      kind: d.kind,
      driver: d.driver,
      powerMilliwatts: 0, // synthetic: asleep, not measured
      suspended: true,
      stale: false,
      measured: false,
    };
  }
  // A stale device holds a frozen value: it is the last thing the collector
  // saw, so it must not animate.
  const w = d.stale ? watts(d, 0) : watts(d, t);
  const mw = Math.round(w * 1000);
  return {
    node: d.node,
    address: d.address,
    kind: d.kind,
    driver: d.driver,
    powerMilliwatts: mw,
    components: {
      socket: mw,
      gfx: Math.round(mw * 0.82),
      cpu_cores: Math.round(mw * 0.11),
    },
    suspended: false,
    stale: !!d.stale,
    measured: true,
  };
}

export function simulatePower(devices: SimDevice[], t: number): PowerIndex {
  const idx: PowerIndex = new Map();
  for (const d of devices) {
    idx.set(`${d.node}/${d.address}`, simulateDevice(d, t));
  }
  return idx;
}

export const BANDS: Record<string, PowerBand> = {
  a100: { idleWatts: 52, busyWatts: 390 },
  h100: { idleWatts: 74, busyWatts: 690 },
  h200: { idleWatts: 78, busyWatts: 700 },
  l40s: { idleWatts: 38, busyWatts: 344 },
  rtx4090: { idleWatts: 22, busyWatts: 442 },
};
