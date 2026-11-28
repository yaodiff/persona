// The Messages page's one list (docs/extensions/lark-migration.md "Personal Agents in Messages"): the Feishu chats and
// the personal Agents (lib/agents.ts) side by side, the latest message first, as Feishu's own app lists its bots and
// chats together. Pure logic; Messages.tsx draws it.
//
// The Feishu list is paged by the gateway and keeps the gateway's own order; Agents are put in between it by their
// rank time without moving any Feishu row. While Feishu has a next page, an Agent older than the oldest Feishu chat
// loaded waits: a later page may hold chats newer than it, and an Agent shown at the bottom now would jump up past them.
// With no next page (or while searching what is loaded) every Agent that matches is shown.
import { matchesQuery, type AgentSession } from "../../lib/agents";
import { filterConversations, type LarkConversation } from "./lark-chats";

/** Which conversations the page lists: all, only Feishu's, only the personal Agents. */
export type FeedFilter = "all" | "lark" | "agents";
export const FEED_FILTERS: readonly FeedFilter[] = ["all", "lark", "agents"];

export type FeedItem =
  | { kind: "lark"; key: string; at: number; chat: LarkConversation }
  | { kind: "agent"; key: string; at: number; session: AgentSession };

/** A row's key in the list and in `data-chat`: a Feishu chat's id as it is, an Agent's under its own prefix. */
export const agentKey = (id: string) => `agent:${id}`;

/** The opened conversation in `/messages/<feed id>` or `/messages/agent/<session id>`: two segments for an Agent, so
 * no Feishu id (one segment) is ever read as one. */
export type MessagesTarget = { kind: "lark"; id: string } | { kind: "agent"; id: string };

export function messagesTarget(path: readonly string[]): MessagesTarget | undefined {
  if (path[0] === "agent" && path[1] && path.length === 2) return { kind: "agent", id: path[1] };
  if (path[0] && path.length === 1) return { kind: "lark", id: path[0] };
  return undefined;
}

export const targetKey = (t: MessagesTarget | undefined) => (!t ? undefined : t.kind === "agent" ? agentKey(t.id) : t.id);

/** A Feishu chat's time (ms); 0 when it has none that parses. */
export function larkTime(c: Pick<LarkConversation, "updated_at">): number {
  const ms = Date.parse(c.updated_at);
  return Number.isFinite(ms) && ms > 0 ? ms : 0;
}

const agentTime = (s: Pick<AgentSession, "rankTimeMs">) => (s.rankTimeMs > 0 ? s.rankTimeMs : 0);
const larkItem = (chat: LarkConversation): FeedItem => ({ kind: "lark", key: chat.id, at: larkTime(chat), chat });
const agentItem = (session: AgentSession): FeedItem => ({ kind: "agent", key: agentKey(session.id), at: agentTime(session), session });

/** Agents newest first (ties by id, so the order holds across refreshes). */
function newestAgents(list: readonly AgentSession[]): AgentSession[] {
  return [...list].sort((a, b) => agentTime(b) - agentTime(a) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
}

/** `lark` in its own order with `agents` put in: each Agent goes before the first Feishu chat older than it. */
export function interleave(lark: readonly LarkConversation[], agents: readonly AgentSession[]): FeedItem[] {
  const rest = newestAgents(agents);
  const out: FeedItem[] = [];
  let i = 0;
  for (const c of lark) {
    const at = larkTime(c);
    while (i < rest.length && agentTime(rest[i]!) > at) out.push(agentItem(rest[i++]!));
    out.push(larkItem(c));
  }
  while (i < rest.length) out.push(agentItem(rest[i++]!));
  return out;
}

/** The oldest time among the loaded Feishu chats: Agents older than it wait for the next page. */
export function pageBoundary(lark: readonly LarkConversation[]): number {
  let min = Number.POSITIVE_INFINITY;
  for (const c of lark) min = Math.min(min, larkTime(c));
  return min;
}

export interface FeedInput {
  /** Feishu's pinned chats and the rest of its loaded rows (arrangeConversations). */
  larkPinned: readonly LarkConversation[];
  larkRows: readonly LarkConversation[];
  /** Whether Feishu has another page after the loaded ones. */
  larkMore: boolean;
  agents: readonly AgentSession[];
  /** The ids of the pinned Agents (AgentPrefs.pins), in the user's order. */
  agentPins: readonly string[];
  /** Whether Agents are listed at all (the mix switch); off, the page is Feishu's alone. */
  mix: boolean;
  filter: FeedFilter;
  query: string;
}

export interface Feed {
  pinned: FeedItem[];
  rows: FeedItem[];
  /** Whether more Feishu pages are to be read for this view (none while only Agents are listed or a search is on). */
  pagesLarkMore: boolean;
}

/** The page's shelf of pinned conversations and its list, mixed by the latest message. */
export function arrangeFeed(x: FeedInput): Feed {
  const showLark = x.filter !== "agents" || !x.mix;
  const showAgents = x.mix && x.filter !== "lark";
  const pins = new Set(x.agentPins);
  const agents = showAgents ? x.agents : [];
  const pinnedAgents = x.agentPins.flatMap((id) => agents.find((s) => s.id === id) ?? []);
  const restAgents = agents.filter((s) => !pins.has(s.id));
  const larkPinned = showLark ? x.larkPinned : [];
  const larkRows = showLark ? x.larkRows : [];
  const q = x.query.trim();
  if (q) {
    // the search covers what is loaded of both, pinned ones too, every match shown
    const lark = filterConversations([...larkPinned, ...larkRows], q);
    return { pinned: [], rows: interleave(lark, agents.filter((s) => matchesQuery(s, q))), pagesLarkMore: false };
  }
  const pinned = interleave(larkPinned, pinnedAgents);
  if (!showLark) return { pinned, rows: interleave([], restAgents), pagesLarkMore: false };
  // the next page may hold newer chats than an older Agent: it waits (and an Agent with no time with it)
  const bound = x.larkMore ? pageBoundary(larkRows) : Number.NEGATIVE_INFINITY;
  const waiting = x.larkMore && !larkRows.length;
  const shownAgents = waiting ? [] : restAgents.filter((s) => agentTime(s) >= bound && (!x.larkMore || agentTime(s) > 0));
  return { pinned, rows: interleave(larkRows, shownAgents), pagesLarkMore: x.larkMore };
}

/** The second line of an Agent's row: its latest message, "Me: …" for the user's own. */
export function agentPreview(s: Pick<AgentSession, "previewText" | "previewIsMine">, mine: (text: string) => string): string {
  const text = s.previewText?.replace(/\s+/g, " ").trim() ?? "";
  if (!text) return "";
  return s.previewIsMine ? mine(text) : text;
}
