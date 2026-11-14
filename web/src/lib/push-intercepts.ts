// Low-distraction mode's held pushes, decoded independently of the view. The API is newest first;
// this module preserves that order inside two level groups and filters by the device's calendar day.

export interface PushIntercept {
  at: string;
  level: string;
  reason: string;
  summary?: string;
  title?: string;
  body?: string;
  related_key?: string;
  manager?: string;
  also?: string[];
  todo_id?: string;
  machine_id?: string;
  session_id?: string;
  group?: string;
  url?: string;
}

export type HeldPushDestination =
  | { kind: "todo"; id: string }
  | { kind: "session"; machineId: string; sessionId: string; group?: string }
  | { kind: "url"; url: string };

export interface HeldPushItem extends PushIntercept {
  /** Stable across refreshes, including duplicate records with the same fields. */
  id: string;
  date: Date;
  /** The primary summary first, then titles folded into this record by the hub. */
  summaries: string[];
}

export interface HeldPushGroups {
  p1: HeldPushItem[];
  p2: HeldPushItem[];
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function text(value: unknown): string | undefined {
  return typeof value === "string" ? value : undefined;
}

/** Date.parse accepts RFC3339 on all supported browsers; trim nanoseconds for older Safari. */
function timestamp(value: string): Date | undefined {
  const normalized = value.replace(/(\.\d{3})\d+(?=Z$|[+-]\d{2}:\d{2}$)/, "$1");
  const milliseconds = Date.parse(normalized);
  return Number.isFinite(milliseconds) ? new Date(milliseconds) : undefined;
}

function oneLine(value: string | undefined): string {
  return (value ?? "").trim().replace(/\s+/g, " ");
}

export function heldPushSummaries(item: Pick<PushIntercept, "summary" | "title" | "body" | "also">): string[] {
  const primary = oneLine(item.summary) || oneLine(item.title) || oneLine(item.body);
  const merged = (item.also ?? []).map(oneLine).filter(Boolean);
  const all = primary ? [primary, ...merged] : merged;
  return all.length ? all : [""];
}

export function localDayKey(date: Date, timeZone: string): string {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone,
    calendar: "gregory",
    numberingSystem: "latn",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(date);
  const get = (type: string) => parts.find((part) => part.type === type)?.value ?? "";
  return `${get("year")}-${get("month")}-${get("day")}`;
}

export function deviceTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

/** Decode only P1/P2 rows. A deduplicated P0 already alerted once and is not relabelled as P1/P2. */
export function heldPushGroups(input: unknown, now: Date = new Date(), timeZone: string = deviceTimeZone()): HeldPushGroups {
  if (!record(input) || !Array.isArray(input.intercepts)) throw new Error("Invalid push intercepts response");
  const today = localDayKey(now, timeZone);
  const seen = new Map<string, number>();
  const out: HeldPushGroups = { p1: [], p2: [] };
  for (const raw of input.intercepts) {
    if (!record(raw)) continue;
    const at = text(raw.at);
    const level = text(raw.level);
    const reason = text(raw.reason);
    if (!at || (level !== "P1" && level !== "P2") || !reason) continue;
    const date = timestamp(at);
    if (!date || localDayKey(date, timeZone) !== today) continue;
    const item: PushIntercept = {
      at,
      level,
      reason,
      summary: text(raw.summary),
      title: text(raw.title),
      body: text(raw.body),
      related_key: text(raw.related_key),
      manager: text(raw.manager),
      also: Array.isArray(raw.also) ? raw.also.filter((value): value is string => typeof value === "string") : undefined,
      todo_id: text(raw.todo_id),
      machine_id: text(raw.machine_id),
      session_id: text(raw.session_id),
      group: text(raw.group),
      url: text(raw.url),
    };
    const base = JSON.stringify([item.at, item.level, item.reason, item.related_key, item.manager]);
    const occurrence = seen.get(base) ?? 0;
    seen.set(base, occurrence + 1);
    const withID: HeldPushItem = { ...item, id: occurrence ? `${base}#${occurrence}` : base, date, summaries: heldPushSummaries(item) };
    out[level === "P1" ? "p1" : "p2"].push(withID);
  }
  return out;
}

export function lnModeEnabled(input: unknown): boolean {
  return record(input) && input.enabled === true;
}

function httpsURL(value: string | undefined): string | undefined {
  if (!value) return undefined;
  try {
    const url = new URL(value);
    return url.protocol === "https:" || url.protocol === "http:" ? url.href : undefined;
  } catch {
    return undefined;
  }
}

/** Only typed destinations and two bounded legacy forms are followed; everything else stays read-only. */
export function heldPushDestination(item: PushIntercept, knownTodoIDs: ReadonlySet<string>): HeldPushDestination | undefined {
  if (item.todo_id?.trim() && knownTodoIDs.has(item.todo_id)) return { kind: "todo", id: item.todo_id };
  if (item.machine_id?.trim() && item.session_id?.trim()) {
    return { kind: "session", machineId: item.machine_id, sessionId: item.session_id, group: item.group || undefined };
  }
  const direct = httpsURL(item.url);
  if (direct) return { kind: "url", url: direct };
  const related = item.related_key?.trim() ?? "";
  if (related.startsWith("todo:")) {
    const id = related.slice("todo:".length).split("@", 1)[0];
    if (id && knownTodoIDs.has(id)) return { kind: "todo", id };
  }
  const legacy = httpsURL(related);
  return legacy ? { kind: "url", url: legacy } : undefined;
}
