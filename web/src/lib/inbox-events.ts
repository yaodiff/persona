// Inbox events (docs/assistant/inbox.md "Events and handling chain"): what a hub of this phase adds on an inbox item — the section it
// is in (`status`, with "In progress" between "Needs you" and "FYI"), urgent, the manager's summary, the processing conversation
// (`handling`), at most one suggestion with its version, and the handling chain. Every field is optional: an item from an
// older hub has none of them and the inbox draws exactly what it drew before.
//
// The pure parts are plain functions so the tests run them without a DOM; `actOnSuggestion` is the one request.

import { t } from "../i18n";
import { api, ApiError, errorText } from "./api";
import { itemHeaders } from "./manager-choice";
import { uuid } from "./format";
import type { DocChange, InboxItem } from "./inbox";
import type { Route } from "./router";

// ── wire types ───────────────────────────────────────────

export type InboxStatus = "action" | "working" | "fyi" | "noise";
export type HandlingState = "looking" | "agent" | "waiting" | "done" | "failed";

export interface InboxHandling {
  owner?: string;
  state: HandlingState | string;
  machine_id?: string;
  session_id?: string;
  since?: string;
  /** failed only: a plain sentence */
  error?: string;
}

export type SuggestionKind = "reply" | "agent" | "conclusion" | "todo";

export interface InboxSuggestion {
  version: number;
  kind: SuggestionKind | string;
  text: string;
  note?: string;
  /** A to-do suggestion's due: a day (2006-01-02, all day) or a moment (RFC 3339); absent: no due date. */
  due?: string;
  /** Needs judgment: confirming hands it back to the processing conversation instead of running it. */
  handback?: boolean;
  machine_id?: string;
  changes?: DocChange[];
  at?: string;
  /** The conversation the item was handed to that handed it in (Hand to AI); absent for the item's own AI twin. */
  conversation?: { machine_id: string; session_id: string };
}

export type ChainLink =
  | { type: "source"; position?: string; uid?: number; url?: string }
  | { type: "session"; machine_id: string; session_id: string }
  | { type: "item"; id: string }
  | { type: "todo"; id: string };

export interface ChainMessage {
  sender?: string;
  text: string;
  at?: string;
}

export interface ChainEntry {
  id: string;
  at?: string;
  kind: string;
  text: string;
  /** Where this step happened: a source id, assistant, agent or user. */
  via?: string;
  /** How a done entry filed the item (a done_by value, or muted). */
  by?: string;
  edited?: boolean;
  detail?: string;
  count?: number;
  sender?: string;
  messages?: ChainMessage[];
  link?: ChainLink | { type: string };
}

export interface InboxRelated {
  id: string;
  title: string;
  source?: string;
}

export type SuggestionAction = "confirm" | "reject" | "instruct";

// ── reading an item ──────────────────────────────────────

/** Whether the hub sent any of this phase's fields: only then the detail draws the event (an older hub: as before). */
export function hasEvent(it: InboxItem): boolean {
  return Boolean(it.chain?.length || it.suggestion || it.recap || it.handling);
}

/** The section of an open item: the hub's `status` when it sends one, else the triage tier as before (empty = "Needs you"). */
export function statusOf(it: InboxItem): InboxStatus {
  switch (it.status) {
    case "action":
    case "working":
    case "fyi":
    case "noise":
      return it.status;
  }
  return it.tier === "fyi" || it.tier === "noise" ? it.tier : "action";
}

/** The word an "In progress" row shows for what is going on; undefined when nothing is. */
export function handlingWord(it: InboxItem): string | undefined {
  switch (it.handling?.state) {
    case "looking":
      return t("lib-inbox-events.state_looking");
    case "agent":
      return t("lib-inbox-events.state_agent");
    case "waiting":
      return t("lib-inbox-events.state_waiting");
    default:
      return undefined;
  }
}

/** Urgent in "Needs you" while the manager still works on it: the card says "AI is preparing a suggestion". */
export function preparing(it: InboxItem): boolean {
  return Boolean(it.urgent) && it.handling?.state === "looking" && !it.suggestion;
}

export function handlingFailed(it: InboxItem): boolean {
  return it.handling?.state === "failed";
}

/** The suggestion's name: "Suggested reply" / "Suggested Agent" / "Conclusion" (an unknown kind: "Suggestion"). */
export function suggestionTitle(s: InboxSuggestion): string {
  switch (s.kind) {
    case "reply":
      return t("lib-inbox-events.kind_reply");
    case "agent":
      return t("lib-inbox-events.kind_agent");
    case "conclusion":
      return t("lib-inbox-events.kind_conclusion");
    case "todo":
      return t("lib-inbox-events.kind_todo");
    default:
      return t("lib-inbox-events.kind_generic");
  }
}

/** The first line of the suggestion's text, for a row. */
export function firstLine(text: string): string {
  return text.split(/\r?\n/).map((l) => l.trim()).find(Boolean) ?? "";
}

/** What the card's buttons are: its confirm label, and whether "Edit" is offered (a conclusion is only read). */
export function suggestionButtons(s: InboxSuggestion): { confirm: string; edit: boolean } {
  if (s.handback) return { confirm: t("lib-inbox-events.confirm"), edit: true };
  switch (s.kind) {
    case "reply":
      return { confirm: t("lib-inbox-events.send"), edit: true };
    case "agent":
      return { confirm: t("lib-inbox-events.start"), edit: true };
    case "conclusion":
      return { confirm: t("lib-inbox-events.got_it"), edit: false };
    case "todo":
      return { confirm: t("lib-inbox-events.add_todo"), edit: false };
    default:
      return { confirm: t("lib-inbox-events.confirm"), edit: true };
  }
}

/** The chain as drawn: oldest first; folded, the three newest (all when there are three or fewer). */
export function chainRows(chain: ChainEntry[] | undefined, open: boolean): ChainEntry[] {
  const all = chain ?? [];
  return open || all.length <= 3 ? all : all.slice(-3);
}

/** What tapping an entry does: go somewhere, or expand its detail / messages in place (nothing to expand: nothing). */
export type ChainTap =
  | { kind: "source"; position?: string; uid?: number; url?: string }
  | { kind: "route"; route: Route }
  | { kind: "expand" }
  | { kind: "none" };

/** Where an entry goes. `conversation` maps a processing conversation to its route (the assistant's or an agent's);
 *  `manager`: whose item the chain is (an item it links is that manager's too). */
export function chainTap(e: ChainEntry, conversation: (mid: string, sid: string) => Route, manager?: string): ChainTap {
  const l = e.link as Record<string, unknown> | undefined;
  if (l?.type === "source") {
    return { kind: "source", position: typeof l.position === "string" ? l.position : undefined, uid: typeof l.uid === "number" ? l.uid : undefined, url: typeof l.url === "string" ? l.url : undefined };
  }
  if (l?.type === "session" && typeof l.machine_id === "string" && typeof l.session_id === "string" && l.machine_id && l.session_id) {
    return { kind: "route", route: conversation(l.machine_id, l.session_id) };
  }
  if (l?.type === "todo" && typeof l.id === "string" && l.id) return { kind: "route", route: { name: "todos", id: l.id } };
  if (l?.type === "item" && typeof l.id === "string" && l.id) return { kind: "route", route: manager ? { name: "inbox", id: l.id, manager } : { name: "inbox", id: l.id } };
  return canExpand(e) ? { kind: "expand" } : { kind: "none" };
}

export function canExpand(e: ChainEntry): boolean {
  return Boolean(e.detail || e.messages?.length);
}

/** The time of an entry: 10:03 today, else the day and the time (Oct 28 10:03). */
export function chainTime(iso?: string, now = new Date()): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const hm = `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
  const same = d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate();
  return same ? hm : t("lib-inbox-events.day_time", { month: d.getMonth() + 1, day: d.getDate(), time: hm });
}

/** The item with its mail thread cut at `uid`, so the mail body opens that mail (the body shows a thread's last). */
export function itemAtUid(it: InboxItem, uid?: number): InboxItem {
  const m = it.mail;
  if (!m || uid === undefined) return it;
  const i = m.uids.indexOf(uid);
  return i < 0 ? it : { ...it, mail: { ...m, uids: m.uids.slice(0, i + 1) } };
}

// ── acting on the suggestion ─────────────────────────────

export type SuggestionResult =
  | { ok: true; item: InboxItem }
  | { ok: false; stale: true; item?: InboxItem; error: string }
  | { ok: false; stale: false; error: string; code?: string };

/**
 * POST /api/v1/inbox/{id}/suggestion: "Confirm" (with the edited text when "Edit" changed it), "Reject", or "Say something" (instruct).
 * The version shown goes with it, so a suggestion that changed since is not run: the hub answers 409 suggestion_stale
 * with the item as it is now.
 */
export async function actOnSuggestion(path: string, version: number, action: SuggestionAction, text?: string, clientID = uuid(), manager?: string, due?: string): Promise<SuggestionResult> {
  try {
    // the item's manager (another than the default: docs/assistant/managers.md "Clients") is named on its request
    const item = await api<InboxItem>(`${path}/suggestion`, { method: "POST", json: { version, action, text: text || undefined, due: due || undefined, client_id: clientID }, headers: itemHeaders(manager) });
    return { ok: true, item };
  } catch (e) {
    if (e instanceof ApiError && e.status === 409 && e.body?.code === "suggestion_stale") {
      const item = e.body.item as InboxItem | undefined;
      return { ok: false, stale: true, item: item?.id ? item : undefined, error: t("lib-inbox-events.stale") };
    }
    return { ok: false, stale: false, error: errorText(e), code: e instanceof ApiError && typeof e.body?.code === "string" ? e.body.code : undefined };
  }
}

/** The toast after a suggestion was acted on. */
export function actedText(s: InboxSuggestion, action: SuggestionAction): string {
  if (action === "reject") return t("lib-inbox-events.rejected");
  if (action === "instruct") return t("lib-inbox-events.instructed");
  if (s.handback) return t("lib-inbox-events.handed_back");
  switch (s.kind) {
    case "reply":
      return t("lib-inbox-events.sent");
    case "agent":
      return t("lib-inbox-events.agent_started");
    case "conclusion":
      return t("lib-inbox-events.filed");
    case "todo":
      return t("lib-inbox-events.todo_added");
    default:
      return t("lib-inbox-events.handed_back");
  }
}
