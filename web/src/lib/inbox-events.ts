// Inbox events (docs/assistant/inbox.md "Events and handling chain"): what a hub of this phase adds on an inbox item — the section it
// is in (`status`, with "In progress" between "Needs you" and "FYI"), urgent, the manager's summary, the processing conversation
// (`handling`), at most one suggestion with its version, and the handling chain. Every field is optional: an item from an
// older hub has none of them and the inbox draws exactly what it drew before.
//
// The pure parts are plain functions so the tests run them without a DOM. What the first screen's lib/inbox.ts needs
// (`statusOf`, and `actOnSuggestion`, the one request, with its toast `actedText`) is in lib/inbox-suggestion.ts, re-exported
// here: the rest is the Inbox page's and the hand-off card's, which load lazily, so it and its words stay off the first screen.

import { t } from "../i18n";
import type { DocChange, InboxItem } from "./inbox";
import type { Route } from "./router";

export { actedText, actOnSuggestion, statusOf, type SuggestionResult } from "./inbox-suggestion";

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

export type SuggestionKind = "reply" | "handoff" | "agent" | "conclusion" | "todo";

export interface InboxMention {
  id: string;
  name: string;
}

const MENTION_FENCE_OPEN_RE = /^```[a-zA-Z0-9_+#-]*\s*$/;
const MENTION_FENCE_CLOSE_RE = /^```\s*$/;
const MENTION_CODE_OR_LINK_RE = /`[^`\n]+`|\[[^\]\n]+\]\(https?:\/\/[^\s<>()]+\)/g;
const MENTION_WORD_CHAR_RE = /^[\p{L}\p{N}_-]/u;

function mentionPresentInSegment(segment: string, needle: string): boolean {
  let offset = 0;
  while (offset <= segment.length - needle.length) {
    const idx = segment.indexOf(needle, offset);
    if (idx < 0) return false;
    const rest = segment.slice(idx + needle.length);
    if (!rest || !MENTION_WORD_CHAR_RE.test(rest)) return true;
    offset = idx + 1;
  }
  return false;
}

function mentionPresentInText(text: string, name: string): boolean {
  if (!text || !name) return false;
  const needle = `@${name}`;
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  for (let i = 0; i < lines.length; ) {
    if (MENTION_FENCE_OPEN_RE.test(lines[i])) {
      let j = i + 1;
      while (j < lines.length && !MENTION_FENCE_CLOSE_RE.test(lines[j])) j++;
      if (j < lines.length) {
        i = j + 1;
        continue;
      }
    }
    const line = lines[i];
    MENTION_CODE_OR_LINK_RE.lastIndex = 0;
    let pos = 0;
    let match: RegExpExecArray | null;
    while ((match = MENTION_CODE_OR_LINK_RE.exec(line)) !== null) {
      if (match.index > pos && mentionPresentInSegment(line.slice(pos, match.index), needle)) return true;
      pos = match.index + match[0].length;
    }
    if (pos < line.length && mentionPresentInSegment(line.slice(pos), needle)) return true;
    i++;
  }
  return false;
}

export function activeMentions(text: string, mentions?: InboxMention[]): InboxMention[] {
  if (!text || !mentions?.length) return [];
  return mentions.filter((m) => m.id && m.name && mentionPresentInText(text, m.name));
}

export interface InboxSuggestion {
  version: number;
  kind: SuggestionKind | string;
  text: string;
  mentions?: InboxMention[];
  note?: string;
  /** A to-do suggestion's due: a day (2006-01-02, all day) or a moment (RFC 3339); absent: no due date. */
  due?: string;
  /** Needs judgment: confirming hands it back to the processing conversation instead of running it. */
  handback?: boolean;
  machine_id?: string;
  /** The existing conversation proposed by a handoff suggestion. */
  session_id?: string;
  target_title?: string;
  changes?: DocChange[];
  at?: string;
  /** The conversation the item was handed to that handed it in (Hand to AI); absent for the item's own AI twin. */
  conversation?: { machine_id: string; session_id: string };
}

/** A server-kept reply attempt. Its text is the actual confirmed payload, including edits. */
export interface InboxSuggestionAttempt {
  version: number;
  status: "sending" | "failed" | "unknown" | "sent";
  retryable: boolean;
  code?: string;
  retry_after?: number;
  text: string;
  mentions?: InboxMention[];
  at: string;
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
export function suggestionTitle(s: InboxSuggestion, isMainTarget = false): string {
  switch (s.kind) {
    case "reply":
      return t("lib-inbox-events.kind_reply");
    case "handoff":
      return t("lib-inbox-events.kind_handoff", { target: s.target_title || (isMainTarget ? t("sidebar.main_chat") : t("lib-format.untitled_session")) });
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
    case "handoff":
      return { confirm: t("lib-inbox-events.hand_off"), edit: true };
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
