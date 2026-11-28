import { navigate } from "./router";

export interface UsageFocusRequest {
  mid: string;
  mode: "reclaim";
}

const KEY = "usage_focus";

/** Opens the Machines page's Storage view on `mid` with the map in reclaim mode. */
export function openUsageMap(mid: string): void {
  navigate({ name: "machine", mid, subview: "storage", mode: "reclaim" });
}

/** Compatibility with fleet entries written before contextual storage URLs. The request the current history entry carries, taken off it (so it is acted on once); null without one. */
export function takeUsageFocus(): UsageFocusRequest | null {
  const state = typeof history !== "undefined" ? (history.state as Record<string, unknown> | null) : null;
  const req = state?.[KEY] as UsageFocusRequest | undefined;
  if (!req || typeof req.mid !== "string" || req.mode !== "reclaim") return null;
  const rest = { ...state };
  delete rest[KEY];
  history.replaceState(rest, "", location.href);
  return { mid: req.mid, mode: "reclaim" };
}
