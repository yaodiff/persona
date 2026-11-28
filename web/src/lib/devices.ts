// Settings › Devices' grouping and sorting, aligned across web, Apple and Android: GET /auth/tokens returns active
// credentials, revoked credentials (with revoked_at), and currently connected credential IDs (`online`). Active
// credentials are grouped into Devices (phones, watches, full desktops), Browsers (`web`), Computers (`link`), and
// Integrations (scoped / peer / other non-full), ordered with current first, then online, then most recently used.
import { byTime } from "./times";

export interface DeviceTimes {
  id: string;
  last_seen?: string;
  revoked_at?: string;
}

export interface DeviceEntry extends DeviceTimes {
  kind?: string;
  role?: string;
  created_at?: string;
}

export type DeviceGroupKey = "devices" | "browsers" | "links" | "integrations";

export interface DeviceGroup<T extends DeviceEntry> {
  key: DeviceGroupKey;
  items: T[];
}

const GROUP_ORDER: readonly DeviceGroupKey[] = ["devices", "browsers", "links", "integrations"];

export function deviceGroupOf(token: Pick<DeviceEntry, "kind" | "role">): DeviceGroupKey {
  if (token.role === "scoped") return "integrations";
  if (token.kind === "web") return "browsers";
  if (token.kind === "link") return "links";
  if (token.kind === "phone" || token.kind === "watch" || (token.kind === "other" && token.role === "full")) {
    return "devices";
  }
  return "integrations";
}

export function splitDevices<T extends DeviceTimes>(tokens: T[], current?: string): { active: T[]; revoked: T[] } {
  const active = tokens
    .filter((t) => !t.revoked_at)
    .sort((a, b) => (a.id === current ? -1 : b.id === current ? 1 : byTime(b.last_seen, a.last_seen)));
  const revoked = tokens.filter((t) => t.revoked_at).sort((a, b) => byTime(b.revoked_at, a.revoked_at));
  return { active, revoked };
}

export function groupDevices<T extends DeviceEntry>(
  tokens: T[],
  current?: string,
  onlineIds?: Iterable<string>,
): { groups: DeviceGroup<T>[]; activeCount: number; revoked: T[] } {
  const online = new Set(onlineIds ?? []);
  const buckets = new Map<DeviceGroupKey, T[]>(GROUP_ORDER.map((k) => [k, []]));
  const revoked: T[] = [];
  let activeCount = 0;
  for (const tok of tokens) {
    if (tok.revoked_at) {
      revoked.push(tok);
      continue;
    }
    activeCount++;
    buckets.get(deviceGroupOf(tok))!.push(tok);
  }
  const byActivity = (a: T, b: T) => {
    if (a.id === current) return -1;
    if (b.id === current) return 1;
    const aOn = online.has(a.id);
    const bOn = online.has(b.id);
    if (aOn !== bOn) return aOn ? -1 : 1;
    return byTime(b.last_seen ?? b.created_at, a.last_seen ?? a.created_at);
  };
  const groups: DeviceGroup<T>[] = [];
  for (const key of GROUP_ORDER) {
    const items = buckets.get(key)!;
    if (items.length > 0) {
      items.sort(byActivity);
      groups.push({ key, items });
    }
  }
  revoked.sort((a, b) => byTime(b.revoked_at, a.revoked_at));
  return { groups, activeCount, revoked };
}
