// One place that answers "what is this accelerator drawing?", so the node
// views don't care whether the answer came from a real collector or a demo.
//
// The override exists for the topology demo, which has no cluster behind it.
// It is deliberately opt-in and provider-scoped: a component only sees
// simulated readings if some ancestor explicitly supplied them, so simulated
// watts can never appear on a real view by accident. That is the same rule the
// pricing design applies to simulated prices — synthetic numbers live on a
// separate path and never touch the real one.
import { createContext, useContext } from "react";
import { indexPower, type PowerIndex } from "@/lib/power";
import { usePower } from "@/hooks/use-api";

/** Non-null only inside a demo/simulation subtree. */
export const SimulatedPowerContext = createContext<PowerIndex | null>(null);

/** Power readings for the current subtree: simulated when an ancestor provides
 * them, otherwise live from the energy collector. */
export function usePowerIndex(): PowerIndex {
  const simulated = useContext(SimulatedPowerContext);
  // Skip the network entirely when readings are supplied — there is no
  // cluster to poll in a demo.
  const { data } = usePower(simulated === null);
  return simulated ?? indexPower(data);
}

/** True when the readings in scope are simulated. Views that could be mistaken
 * for real telemetry must label themselves with this. */
export function useIsSimulatedPower(): boolean {
  return useContext(SimulatedPowerContext) !== null;
}
