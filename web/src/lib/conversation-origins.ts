// Where an assistant conversation comes from (docs/protocol/protocol.md "Origin of assistant chats", #3264): the user's own (the app, a
// channel's /new), or one the hub opened by itself (the inbox's AI twin, a scheduled task, conversation_new, a plugin's /agent…).
// The sidebar's "Recent" lists both (#5195), each of the latter with its group's mark (OriginMark); "All chats" filters by
// origin. Pure functions, tested without a DOM.
import { t } from "../i18n";
import type { IconName } from "../ui/icons";
import type { BrandId } from "./connectors";
import { byConversationOrder } from "./conversation-order";
import { byTime } from "./times";
import type { AssistantInfo, Item, Session } from "./types";

/** The sidebar's groups of automatic conversations, in its order; `other` is any origin this page doesn't know. */
export const AUTO_ORIGINS = ["inbox", "task", "lark_agent", "spawned", "extension", "other"] as const;
export type AutoOrigin = (typeof AUTO_ORIGINS)[number];

/** What "All chats" can be filtered to: everything, the user's own, one group of automatic ones, or every chat an
 *  external source opened (`external`: Lark /agent's and other extensions', EXTERNAL_ORIGINS; the phone tab bar's
 *  External, lib/phone-nav.ts). */
export type OriginFilter = "all" | "mine" | "external" | AutoOrigin;

const USER_ORIGINS = new Set(["", "user", "channel"]);

/** The automatic group a conversation is in; undefined for the user's own (an older hub says nothing: all are).
 *  `background`: the background supervisor's id (lib/background-supervisor.ts, #5082). That conversation is with the
 *  scheduled tasks' whatever origin its row carries: never one of the user's own. */
export function autoOrigin(s: Pick<Session, "origin"> & { session_id?: string }, background?: string): AutoOrigin | undefined {
  if (background && s.session_id === background) return "task";
  const o = s.origin ?? "";
  if (USER_ORIGINS.has(o)) return undefined;
  return (AUTO_ORIGINS as readonly string[]).includes(o) ? (o as AutoOrigin) : "other";
}

export const isAutoConversation = (s: Pick<Session, "origin"> & { session_id?: string }, background?: string) => autoOrigin(s, background) !== undefined;

export function originLabel(o: AutoOrigin): string {
  return t(`assistant-conversations.origin_${o}`);
}

/** What a row's origin mark is named (#5195): where an external conversation came from ("From Lark /agent"), else its
 *  group's name, as All chats' filter for it says it. */
export function originMarkLabel(o: AutoOrigin): string {
  return (EXTERNAL_ORIGINS as readonly string[]).includes(o) ? externalFrom(o as ExternalOrigin) : originLabel(o);
}

/** The words for an origin's mark and the line under the opening message: "From <name>" while the mark draws the icon of
 *  the extension `name` names (#10358, OriginMark's `onName`), else the origin's own (originMarkLabel). */
export function openerFrom(o: AutoOrigin, name: string | undefined): string {
  return name ? t("chat-segmentview.from", { name }) : originMarkLabel(o);
}

/** Each group's mark (OriginMark), on the rows of the conversations in it: the sidebar's and All chats' (#5195). */
export const ORIGIN_ICON: Record<AutoOrigin, IconName> = { inbox: "inbox", task: "clock", lark_agent: "at", spawned: "fork", extension: "extension", other: "more" };
/** A group with a brand of its own draws its mark instead of the icon (#3758). */
export const ORIGIN_BRAND: Partial<Record<AutoOrigin, BrandId>> = { lark_agent: "lark" };

/** The conversations an extension opened (#3758): Lark /agent's and any other extension's. */
export const EXTERNAL_ORIGINS = ["lark_agent", "extension"] as const;
export type ExternalOrigin = (typeof EXTERNAL_ORIGINS)[number];

/** The extension that opened an external conversation, by id (#10358): its `opened_by`, else its `origin_ref`, which for
 *  these two origins is that extension too (the hub's start notes both, server/internal/assistant/start.go
 *  requestOrigin); undefined for any other origin and for one nobody noted (a conversation the backfill judged). */
export function openerOf(origin: string | undefined, s: Pick<Session, "opened_by" | "origin_ref"> | undefined): string | undefined {
  if (!(EXTERNAL_ORIGINS as readonly string[]).includes(origin ?? "")) return undefined;
  return s?.opened_by?.trim() || s?.origin_ref?.trim() || undefined;
}

/** Where an external conversation came from, by its origin with the spaces trimmed; undefined for every other one (the
 *  user's own, a channel's /new, the inbox's, a task's, one spawned from a chat, an origin this page doesn't know). */
export function externalOrigin(s: Pick<Session, "origin"> | undefined): ExternalOrigin | undefined {
  const o = s?.origin?.trim() ?? "";
  return (EXTERNAL_ORIGINS as readonly string[]).includes(o) ? (o as ExternalOrigin) : undefined;
}

/** "From Lark /agent", "From an extension": under the message it was opened with, and the name of its mark on a row. */
export function externalFrom(o: ExternalOrigin): string {
  return o === "extension" ? t("assistant-conversations.from_extension") : t("chat-segmentview.from", { name: originLabel(o) });
}

/** The message an external conversation was opened with: its first user message (lib/turns.ts's rule: a text, or a
 *  picture that is not a tool's), with where it came from. Only once the loaded items reach the conversation's start: while
 *  earlier ones are still to load (`hasMore`), the first one loaded is not the first. */
export function externalOpening(s: Pick<Session, "origin"> | undefined, items: readonly Pick<Item, "id" | "role" | "kind" | "tool_use_id">[], hasMore: boolean): { id: string; origin: ExternalOrigin } | undefined {
  const origin = externalOrigin(s);
  if (!origin || hasMore) return undefined;
  const first = items.find((i) => i.role === "user" && (i.kind === "text" || (i.kind === "image" && !i.tool_use_id)));
  return first ? { id: first.id, origin } : undefined;
}

/** The filter of a `/history/<filter>` address; anything else is all of them. */
export function parseOriginFilter(v: string | undefined): OriginFilter {
  return v === "mine" || v === "external" || (AUTO_ORIGINS as readonly string[]).includes(v ?? "") ? (v as OriginFilter) : "all";
}

export function matchesOrigin(s: Pick<Session, "origin"> & { session_id?: string }, filter: OriginFilter, background?: string): boolean {
  if (filter === "all") return true;
  const o = autoOrigin(s, background);
  if (filter === "external") return (EXTERNAL_ORIGINS as readonly string[]).includes(o ?? "");
  return filter === "mine" ? o === undefined : o === filter;
}

const working = (s: Session) => s.state === "working" || s.status === "working" || !!s.starting;
const needsYou = (s: Session) => s.state === "needs_you";

/** How a set of conversations stands, for a folded row: how many, how many run, how many wait for the user. */
export interface OriginTally {
  count: number;
  working: number;
  needsYou: number;
}

function tally(list: readonly Session[]): OriginTally {
  return { count: list.length, working: list.filter(working).length, needsYou: list.filter(needsYou).length };
}

/** The automatic conversations (not archived, not the main one) by group, only the groups that have any, in the
 *  sidebar's order, and all of them together. */
export function autoConversations(conversations: readonly Session[], main: string | undefined, background?: string): { total: OriginTally; groups: (OriginTally & { origin: AutoOrigin })[] } {
  const auto = conversations.filter((c) => c.session_id !== main && !c.archived && isAutoConversation(c, background));
  const groups = AUTO_ORIGINS.map((origin) => ({ origin, ...tally(auto.filter((c) => autoOrigin(c, background) === origin)) })).filter((g) => g.count > 0);
  return { total: tally(auto), groups };
}

/** The sidebar's "Recent" under Persona chats (#4873, #5195), newest first: every conversation whoever opened it (the
 *  user, Lark /agent, the inbox…; a row says which with its origin's mark) except the main one, the leaders (常驻,
 *  listed above it), the archived ones, the hub's built-in background supervisor (`background`, its id: listed with the
 *  scheduled tasks' chats in "All chats" only, #4927) and the scheduled tasks' own chats (#6228: those have their own
 *  entry on Scheduled Tasks and stay in "All chats", so they no longer crowd Recent), by last activity (equal times by
 *  id, as iOS). The sidebar shows the first ones (lib/sidebar-recent.ts, #5146). `withTasks`: the user's "Show scheduled
 *  task chats" (#8324, `assistant.recent_task_chats`) brings the background supervisor and the tasks' chats back, by time. */
export function recentConversations(conversations: readonly Session[], main: string | undefined, leaders: readonly string[], background?: string, withTasks = false, order?: readonly string[]): Session[] {
  const lead = new Set(leaders);
  const recent = conversations
    .filter((c) => c.session_id !== main && !lead.has(c.session_id) && !c.archived
      && (withTasks || (!(background && c.session_id === background) && autoOrigin(c, background) !== "task")))
    .sort((a, b) => byTime(b.updated_at, a.updated_at) || (a.session_id < b.session_id ? -1 : a.session_id > b.session_id ? 1 : 0));
  return byConversationOrder(recent, order);
}

/** The assistant's conversations in `rows` (its machine's, as the session list or a push brings them) that the held
 *  answer of GET /api/v1/assistant does not list (#10190): one made elsewhere while the page is open (an integration's
 *  POST /api/v1/assistant/conversations, a chat started on the phone), or the background supervisor the hub made or
 *  replaced since (#5082). Not the main conversation or the named background supervisor, and none the answer leaves out
 *  anyway (archived, deleted, another space's). Only reading the answer again brings such a conversation into Recent
 *  and All chats with everything it gives one (lib/store.ts does, once for each). */
export function unlistedConversations(assistant: Pick<AssistantInfo, "main" | "background_supervisor" | "conversations">, rows: readonly Session[]): string[] {
  const candidates = rows.filter((s) => s.assistant && !s.archived && !s.deleted && !s.space);
  if (!candidates.length) return [];
  const listed = new Set([assistant.main, assistant.background_supervisor, ...assistant.conversations.map((c) => c.session_id)]);
  return candidates.filter((s) => !listed.has(s.session_id)).map((s) => s.session_id);
}
