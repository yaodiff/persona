import type { Session } from "./types";

export interface SpeedOption {
  id: string;
  name?: string;
  description?: string;
}

export function speedId(tier: string): string {
  if (tier === "default") return "standard";
  if (tier === "priority") return "fast";
  return tier;
}

/** Keep native tier IDs in the RPC. Older hubs can advertise Fast alone, with Standard implicit. */
export function speedOptions(s: Pick<Session, "service_tiers"> | undefined): SpeedOption[] {
  const choices = new Map<string, SpeedOption>();
  if (!s?.service_tiers?.length) return [];
  choices.set("standard", { id: "standard" });
  for (const tier of s.service_tiers) {
    if (!tier.id) continue;
    const id = speedId(tier.id);
    choices.set(id, { ...tier, id });
  }
  if (choices.size < 2) return [];
  const order = ["standard", "fast", "ultrafast"];
  const rank = (id: string) => order.includes(id) ? order.indexOf(id) : order.length;
  return [...choices.values()].sort((a, b) => rank(a.id) - rank(b.id));
}

/** Faster tiers understood by the new-conversation composer, from the selected model's native catalog. */
export function startSpeedOptions(model: Pick<Session, "service_tiers"> | undefined): SpeedOption[] | undefined {
  const options = speedOptions(model).filter(({ id }) => id === "standard" || id === "fast" || id === "ultrafast");
  return options.length > 1 ? options : undefined;
}

export function startSpeedTier(model: Pick<Session, "service_tiers"> | undefined, speed?: string): string | undefined {
  if (!speed || speed === "standard" || !startSpeedOptions(model)?.some(({ id }) => id === speed)) return undefined;
  return speed === "fast" ? "priority" : speed;
}

export function speedTier(s: Session, speed: string): string | undefined {
  if (!speedOptions(s).some((choice) => choice.id === speed)) return undefined;
  return s.service_tiers?.find((tier) => speedId(tier.id) === speed)?.id ?? "default";
}
