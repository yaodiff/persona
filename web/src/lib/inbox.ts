// The unified inbox (docs/assistant/inbox.md): the hub's items (Feishu chats, mail threads, Feishu documents, Moments (company feed) posts, and
// the connectors' threads: GitHub, Telegram…, drawn from their generic `connector` block),
// kept in one module-level list so the sidebar's badge and the page share it. Pulled on first use, again on `inbox`
// pushes and on (re)connect. Marking done applies at once and is sent in the background; a failure puts it back.
//
// The pure parts (filters, sections, labels, the "Hand to AI" message) are plain functions so the tests can run them
// without a DOM.

import { itemHeaders, otherManager, sameManager, stampedHeaders } from "./manager-choice";
import { scoped, useScope } from "./current-assistant";
import { t } from "../i18n";
import { useEffect, useRef, useSyncExternalStore } from "react";
import { api, ApiError, enc, errorText } from "./api";
import type { AgentMutes } from "./agent-groups";
import { agentName } from "./agent-kind";
import { connectorOpenTitle } from "./connector-profiles";
import { contextBody } from "./context-body";
import type { Route } from "./router";
import { isResumed } from "./reconnect";
import { sameValue } from "./stable-lines";
import { onHubEvent } from "./store";
import type { Interaction, Session } from "./types";
import { toast } from "../ui/atoms";
import { byTime } from "./times";
import { homeKey } from "./folded-rows";
import { supervisorHeld } from "./supervisor-held";
import { inFolderFilter } from "./inbox-folders";
import type { ChainEntry, InboxHandling, InboxRelated, InboxStatus, InboxSuggestion, InboxSuggestionAttempt, SuggestionAction } from "./inbox-events";
import { actedText, actOnSuggestion, statusOf } from "./inbox-suggestion"; // not inbox-events.ts: that one is the Inbox page's
import type { MailAccount } from "./agent-settings";
import type { ExternalRef } from "../features/external-sources/api";
import type { InboxStoredSources } from "./inbox-sources-wire";

// Not the Inbox's: re-exported from their own modules until their importers import them from there (#11538).
export { withToken } from "./media-url";
export { shortTime } from "./format";
export { conversationRoute } from "./conversation-page";
export { comparePosition, mergeMessages, type LarkMention, type LarkMessage, type LarkPage, type LarkPart } from "./lark-messages";
export type { MailAccount };

// ── wire types (docs/protocol/protocol.md "Inbox API") ─────────

export interface DocOption {
  name: string;
  color?: string;
}

export interface DocStatus {
  field: string;
  name: string;
  value: string;
  options: DocOption[];
}

export interface DocRow {
  ref: string;
  table?: string;
  title: string;
  fields?: { name: string; value: string }[];
  status?: DocStatus[];
}

export interface DocChange {
  ref: string;
  field: string;
  value: string;
}

export interface InboxDoc {
  url: string;
  link?: string;
  type: string; // docx | doc | sheet | base | other
  notice: string; // mention | comment | reply
  notice_id?: string;
  rows?: DocRow[];
  reply?: { kind: string; label: string };
  error?: string;
}

export interface InboxAgent {
  /** The business task this event is about; distinct from its draft-processing session. */
  todo_id?: string;
  state: string; // skipped | queued | running | drafted | failed
  task?: string;
  reason?: string;
  machine_id?: string;
  session_id?: string;
  draft?: string;
  note?: string;
  error?: string;
  changes?: DocChange[];
}

/** Deterministic PR/Issue auto-forwarding metadata persisted on an Inbox item (#12492). */
export interface InboxTodoForward {
  todo_id: string;
  todo_title?: string;
  canonical_url?: string;
  match_kind?: "pull_request" | "issue" | string;
  status: "forwarded" | "queued" | "resumed" | "fallback" | "failed" | string;
  machine_id?: string;
  session_id?: string;
  reason?: string;
  forwarded_at?: string;
}

export interface InboxItem {
  id: string;
  /** The manager whose inbox it is, in the hub's merged list (docs/protocol/protocol.md "Per-manager routing"); none: the default.
   *  Two managers may list the same id (one account in both): an item is its id and its manager (itemKey). */
  manager?: string;
  source: string; // lark | mail | a connector's id (github, telegram…)
  kind: string; // dm | mention | group | bot | mail | doc | moment
  account: string;
  account_name: string;
  title: string;
  /** Where the matter happens: a group's name, the other person of a direct chat, or a repository. */
  where?: string;
  sender?: string;
  avatar?: string;
  avatar_url?: string;
  preview: string;
  count: number;
  started_at?: string;
  updated_at?: string;
  tier?: string;
  summary?: string;
  /** An assistant row's text as Markdown, its line breaks kept (#9041): the detail draws it; `summary` and `preview`
   *  are the same words without the marks. Older hubs leave it out. */
  body?: string;
  reason?: string;
  read?: boolean;
  done_at?: string | null;
  done_by?: string | null;
  /** What it holds now, as listed: a bulk "Done" sends it back (an item with new messages since stays open). */
  version?: string;
  lark?: {
    feed_id: string; is_group: boolean; is_topic?: boolean; start_position?: string; mention_position?: string; at_all?: boolean;
    /** The Lark gateway account the chat is in; `ol_account` only when that is not the hub's own (docs/extensions/feishu.md "Multiple accounts"). */
    account_id?: number; ol_account?: number;
  };
  mail?: { account_id: string; mailbox?: string; uids: number[]; message_id?: string; from?: { name?: string; email?: string }; direct?: boolean; thread?: string };
  doc?: InboxDoc;
  moment?: { post_id: string; notice?: string; reply_to?: string; my_post?: boolean; post_text?: string };
  /** A connector's thread (GitHub…): `source` is the connector's id, `account_name` its name. */
  connector?: InboxConnector;
  /** The hub's normalized source followed by its ancestors in lookup order. */
  external_path?: ExternalRef[];
  /** Only in GET /inbox/{id}: the conversation as text. */
  context?: string;
  /** Only in GET /inbox/{id}, for a mail: whether the hub clipped the newest mail's text when it stored `context`.
   *  Absent: the hub does not know (or is from before it sent this). Read it through storedMailClipping (lib/mail-stored.ts). */
  mail_body_truncated?: boolean;
  /** Only in GET /inbox/{id}: the knowledge base chat it came from, when there is one (#15356). */
  kb_chat?: { id: string; path?: string } | null;
  agent?: InboxAgent;
  /** Deterministic PR/Issue auto-forward status and linked to-do metadata (#12492). */
  todo_forward?: InboxTodoForward;
  /** New foreign mail translated by the hub as it came in (for a device whose Settings › Translation asks), by target language. */
  translations?: Record<string, { subject: string; preview: string }>;
  /** What it grew out of (docs/assistant/inbox.md "Relationships: parents and children"): another item, a todo, a
   *  coding Agent's session ("<machine>/<session>") or a personal Agent's session ("<source>:<sessionKey>"). */
  parent?: InboxParent;
  /** The folder path it is filed under ("work/alerts/engine"; docs/assistant/inbox.md "Folders"); absent: unfiled. `folder_by`
   *  says who filed it: the model, or the user (then the model leaves it alone). */
  folder?: string;
  folder_by?: "model" | "user";
  /** What it can be muted by (docs/assistant/inbox.md "Muting"): its repository, chat or sender in its source. */
  mutes?: InboxMuteOption[];
  // ── events (docs/assistant/inbox.md "Events and handling chain"; lib/inbox-events.ts): absent from an older hub ──
  /** The section of an open item; absent: by `tier` as before. */
  status?: InboxStatus;
  /** What or whom it waits on, the next step, and when to look again. */
  follow?: { who?: string; next?: string; check_at?: string };
  /** The manager's state key; apps translate it with the shared table in spec/fixtures/inbox/now.json. */
  now?: string;
  /** Urgent: stays in "Needs you" while the AI prepares a suggestion. */
  urgent?: boolean;
  /** Summary by the manager: background, progress, where it is stuck. */
  recap?: string;
  handling?: InboxHandling;
  suggestion?: InboxSuggestion;
  /** Authoritative reply outcomes retained by the server, including superseded suggestion versions. */
  suggestion_attempts?: InboxSuggestionAttempt[];
  /** Handling chain, oldest first. */
  chain?: ChainEntry[];
  /** The rows merged into this one by hand (the hub hides them; #6480). */
  merged_from?: { id: string; source: string; account_name?: string; title?: string }[];
  related?: InboxRelated[];
  /** The conversations it was handed to with "Hand to AI" (POST /inbox/{id}/handoff), oldest first: they may write back to it. */
  handoffs?: InboxHandoff[];
}

/** One hand-off of an item to Persona's main conversation. */
export interface InboxHandoff {
  machine_id: string;
  session_id: string;
  at?: string;
}

/** One thing an item can be muted by: a repository, a chat or a sender of its source. */
export interface InboxMuteOption {
  tag: string; // repo | chat | sender
  value: string;
  name: string;
}

/** A mute the user set (GET /api/v1/inbox/mutes): nothing of `value` under `tag` in `source` comes into the inbox. */
export interface InboxMute extends InboxMuteOption {
  id: string;
  source: string;
  source_name?: string;
  created_at?: string;
  /** Whose mute it is (several managers: each one keeps its own; listInboxMutes stamps it); none: the default's. */
  manager?: string;
}

export type InboxParentKind = "inbox" | "todo" | "session" | "agent";
export interface InboxParent { kind: InboxParentKind; id: string; by?: "continues" | "connector" | "user" }

/** GET /api/v1/inbox/tree/{kind}/{id}: the node's ancestors (nearest first; the last may be a todo, a coding Agent
 * session or a personal Agent session, with no item) and the items right under it. */
export interface InboxTree {
  node: InboxParent;
  item?: InboxItem;
  ancestors: (InboxParent & { item?: InboxItem })[];
  children: InboxItem[];
}

/** The route of an inbox item: its id, and its manager when that is another than the default (two managers may list
 *  the same id). */
export function itemRoute(it: { id: string; manager?: string }): Extract<Route, { name: "inbox" }> {
  const other = otherManager(it.manager);
  return other ? { name: "inbox", id: it.id, manager: other } : { name: "inbox", id: it.id };
}

export interface InboxConnector {
  thread: string;
  /** The row's tag: "review requested". */
  label?: string;
  /** The thread on the web. */
  url?: string;
  /** A link the system opens instead (sms:…, tg://…): where the user answers it. */
  open_url?: string;
  /** Where a reply goes and who sees it. */
  reply_to?: string;
  can_reply?: boolean;
  /** The thread's picture as the hub's avatars route serves it (`ItemAvatar`, docs/protocol/protocol.md "Avatars"): the
   *  other person of a private chat, a group's own. Apart from the item's `avatar` (Feishu's). Without one (an older
   *  hub, a connector that sends none), the initial. */
  avatar_url?: string;
  rule_tier?: string;
}

// ── one item ─────────────────────────────────────────────

export type Tier = "action" | "fyi" | "noise";
/** A section of open items: the tiers, and "In progress" (an event the AI or an Agent works on, or waiting for the other side). */
export type SectionKey = Tier | "working";
export const TIERS: { key: SectionKey; title: string }[] = [
  { key: "action", title: t("lib-inbox.tier_action") },
  { key: "working", title: t("lib-inbox.tier_working") },
  { key: "fyi", title: t("lib-inbox.tier_fyi") },
  { key: "noise", title: t("lib-inbox.tier_noise") },
];

/** Items not triaged yet count as "Needs you" until the AI says otherwise. */
export function triage(it: InboxItem): Tier {
  return it.tier === "fyi" || it.tier === "noise" ? it.tier : "action";
}

export function isDone(it: InboxItem): boolean {
  return Boolean(it.done_at);
}

export function isLark(it: InboxItem): boolean {
  return it.source === "lark";
}

/** Lark in the filter chips and source groups: the Lark gateway's items and lark-cli's ("Lark · my messages") are one
 *  "Lark", so the list never shows two Lark sources side by side. */
export function isLarkSource(it: InboxItem): boolean {
  return isLark(it) || it.source === "larkcli";
}

/** The thread as people write it (example-org/example-repo#12), undefined when it is only an id. */
export function connectorReference(it: InboxItem): string | undefined {
  const t = it.connector?.thread;
  return t && t.includes("#") ? t : undefined;
}

/** The button that opens a connector item's `open_url`, where the user answers it. */
export function openTitle(it: InboxItem): string {
  const url = it.connector?.open_url;
  if (url?.startsWith("sms:")) return t("lib-inbox.reply_in_messages");
  return connectorOpenTitle(it.source) ?? (url ? tightCJK(t("lib-inbox.open_in", { name: it.account_name || it.source })) : t("lib-inbox.open"));
}

/**
 * Drops template spaces between adjacent Chinese characters. Spaces around Latin product names remain;
 * other scripts are left alone.
 */
export function tightCJK(text: string): string {
  return text.replace(/(?<=\p{Script=Han}) (?=\p{Script=Han})/gu, "");
}

/** A connector item's `open_url` as the browser opens it; an sms: one carries the reply as its body (the kept edit, or
 *  the AI twin's draft). */
export function openURL(it: InboxItem): string | undefined {
  const raw = it.connector?.open_url;
  if (!raw) return undefined;
  const draft = unsentReplies.get(itemKey(it)) ?? agentDraft(it);
  return raw.startsWith("sms:") && draft ? `${raw}&body=${encodeURIComponent(draft)}` : raw;
}

const AT_ME = t("lib-inbox.kind_at_me");

/** The short tag next to the title. */
export function kindLabel(it: InboxItem): string | undefined {
  if (it.connector) return it.connector.label || undefined;
  switch (it.kind) {
    case "dm":
      return t("lib-inbox.kind_dm");
    case "mention":
      return it.lark?.at_all ? t("lib-inbox.kind_at_all") : AT_ME;
    case "bot":
      return t("lib-inbox.kind_bot");
    case "doc":
      return it.doc?.notice === "comment" || it.doc?.notice === "reply" ? t("lib-inbox.kind_comment") : AT_ME;
    case "moment":
      return it.moment?.notice?.startsWith("atIn") ? AT_ME : t("lib-inbox.moments");
    default:
      return undefined;
  }
}

/** @me is red (it is about you); every other tag is neutral. */
export function labelIsUrgent(it: InboxItem): boolean {
  return it.kind === "mention" || kindLabel(it) === AT_ME;
}

export function docTypeName(type?: string): string {
  switch (type) {
    case "sheet":
      return t("lib-inbox.doc_sheet");
    case "base":
      return t("lib-inbox.doc_base");
    case "docx":
    case "doc":
      return t("lib-inbox.doc_doc");
    default:
      return t("lib-inbox.doc_cloud");
  }
}

/** Who wrote what: the row's line under the summary (or its second line before triage); `preview` is the hub's
 * translation of a foreign mail's when it is shown. */
export function fromLine(it: InboxItem, preview?: string): string {
  if (it.source === "assistant") return it.preview; // the assistant's own row: the tag already says who, so no "Assistant:" prefix
  if (it.doc) return `${docTypeName(it.doc.type)} · ${it.preview}`; // the preview names who: "<name> mentioned you"
  if (it.moment) return t("lib-inbox.sender_line", { sender: it.sender ?? "", text: it.preview });
  if (it.connector) {
    // example-org/example-repo#12 · bob: please take a look (full-width colon)
    const who = [it.sender ?? "", it.preview].filter(Boolean).join(t("lib-inbox.sender_separator"));
    return [connectorReference(it) ?? "", who].filter(Boolean).join(" · ");
  }
  if (preview && it.sender) return t("lib-inbox.sender_line", { sender: it.sender, text: preview });
  // a Feishu direct chat's title already is the sender
  if (!it.sender || (isLark(it) && it.lark && !it.lark.is_group)) return it.preview;
  return t("lib-inbox.sender_line", { sender: it.sender, text: it.preview });
}

/** The mail's IMAP folder. The default inbox needs no label. */
export function sourceMailbox(it: InboxItem): string | undefined {
  const mailbox = it.mail?.mailbox;
  return mailbox && mailbox.toLowerCase() !== "inbox" ? mailbox : undefined;
}

export function agentDraft(it: InboxItem): string | undefined {
  return it.agent?.state === "drafted" && it.agent.draft ? it.agent.draft : undefined;
}

/** A proposed status move's key: its row and field. */
export const docChangeKey = (c: DocChange) => `${c.ref}|${c.field}`;

/** The AI twin's proposed status moves the user left ticked (`dropped`: the unticked ones' docChangeKey): what goes
 *  with the reply. */
export function keptChanges(it: InboxItem, dropped: readonly string[]): DocChange[] {
  const changes = agentDraft(it) ? it.agent?.changes ?? [] : [];
  return dropped.length ? changes.filter((c) => !dropped.includes(docChangeKey(c))) : changes;
}

export function agentWorking(it: InboxItem): boolean {
  return it.agent?.state === "queued" || it.agent?.state === "running";
}

/** Item ids are `lark:<feed>:<pos>` / `mail:<acc>:<thread>:<uid>`: the colons stay as they are in the path. */
export function itemPath(id: string): string {
  return `/inbox/${enc(id).replace(/%3A/gi, ":")}`;
}

/** An item's identity in the list: its id and the manager whose inbox it is (two managers may list the same id, one
 *  account in both). A hub with one manager stamps every item alike, so this is unique as the id is. */
export function itemKey(it: { id: string; manager?: string }): string {
  return it.manager ? `${it.manager}\u0000${it.id}` : it.id;
}

/** Whether a and b are the same item: the same id of the same manager (none: the default's). */
export function sameItem(a: { id: string; manager?: string }, b: { id: string; manager?: string }): boolean {
  return a.id === b.id && sameManager(a.manager, b.manager);
}

/** The header naming an item's manager on a request about it (none for the default's). */
export const itemRequest = (it: { manager?: string }) => itemHeaders(it.manager);

// ── filters and sections ─────────────────────────────────

/** source: "" all, "feishu", a mail account id or a connector's id; kind: "" all, mention, dm, group, mail, agent;
 *  folder: "" all, a folder path (it and everything under it) or UNFILED. */
export interface InboxFilter {
  source: string;
  kind: string;
  query: string;
  folder?: string;
}

export const KIND_FILTERS: { key: string; label: string }[] = [
  { key: "mention", label: AT_ME },
  { key: "dm", label: t("lib-inbox.kind_dm") },
  { key: "group", label: t("lib-inbox.kind_group") },
  { key: "bot", label: t("lib-inbox.kind_bot") },
  { key: "doc", label: t("lib-inbox.filter_doc") },
  { key: "mail", label: t("lib-inbox.kind_mail") },
  { key: "agent", label: "Agent" },
];

function hasText(fields: (string | undefined)[], query: string): boolean {
  const q = query.trim().toLowerCase();
  return !q || fields.some((f) => f?.toLowerCase().includes(q));
}

export function matches(it: InboxItem, f: InboxFilter): boolean {
  if (f.source === "feishu" && !isLarkSource(it)) return false;
  if (f.source && f.source !== "feishu" && it.account !== f.source && it.mail?.account_id !== f.source) return false;
  if (f.kind === "agent") return false; // only the waiting agents
  if (f.folder && !inFolderFilter(it, f.folder)) return false;
  if (f.kind) {
    const ok = it.kind === f.kind || (f.kind === "mention" && (it.kind === "doc" || (it.kind === "moment" && kindLabel(it) === AT_ME)));
    if (!ok) return false;
  }
  return hasText([it.title, it.sender, it.preview, it.summary, it.where], f.query);
}

const byUpdated = (a: InboxItem, b: InboxItem) => byTime(b.updated_at, a.updated_at) || a.id.localeCompare(b.id);
const byDoneAt = (a: InboxItem, b: InboxItem) => byTime(b.done_at, a.done_at) || a.id.localeCompare(b.id);

export interface InboxSections {
  tiers: Record<SectionKey, InboxItem[]>;
  done: InboxItem[];
}

/** The open items by section (the hub's `status`, else the triage tier; newest first) and the done ones (most
 * recently done first). */
export function sections(items: InboxItem[], f: InboxFilter): InboxSections {
  const out: InboxSections = { tiers: { action: [], working: [], fyi: [], noise: [] }, done: [] };
  for (const it of items) {
    if (!matches(it, f)) continue;
    if (isDone(it)) out.done.push(it);
    else out.tiers[statusOf(it)].push(it);
  }
  for (const t of TIERS) out.tiers[t.key].sort(byUpdated);
  out.done.sort(byDoneAt);
  return out;
}

/** The sidebar badge: unread open items in Needs you; reading never completes them. */
export function badgeCount(items: InboxItem[]): number {
  return items.filter((it) => !isDone(it) && !it.read && statusOf(it) === "action").length;
}

/** The connectors with items in the list (GitHub…), one filter chip each, in the order they first come. */
export function connectorSources(items: InboxItem[]): { id: string; name: string }[] {
  const seen = new Map<string, string>();
  for (const it of items) if (it.connector && !isLarkSource(it) && !seen.has(it.account)) seen.set(it.account, it.account_name || it.source);
  return [...seen].map(([id, name]) => ({ id, name }));
}

/** Mail accounts for the filter chips: the hub's list, else the ones the items name. */
export function mailAccounts(accounts: MailAccount[], items: InboxItem[]): MailAccount[] {
  if (accounts.length) return accounts;
  const seen = new Map<string, MailAccount>();
  for (const it of items) if (it.source === "mail" && !seen.has(it.account)) seen.set(it.account, { id: it.account, email: it.account_name || it.account });
  return [...seen.values()];
}

// ── agents waiting on the user (they head "Needs you") ────────

/** Muted: the agent itself, or its space unless the agent was turned back on there (the hub's rule). A child session's
 *  card goes by its parent's (#5120): the child has no row of its own to mute it from, and the switcher's count
 *  (lib/manager-attention.ts) mutes it the same way. */
export function interactionMuted(ix: Interaction, sessions: Record<string, Session[]>, mutes: AgentMutes): boolean {
  if (ix.folded_under) return interactionMuted({ ...ix, machine_id: ix.folded_under_machine_id || ix.machine_id, session_id: ix.folded_under, folded_under: undefined, folded_under_machine_id: undefined }, sessions, mutes);
  const key = `${ix.machine_id}/${ix.session_id}`;
  if (mutes.sessions.includes(key)) return true;
  if (mutes.allowed?.includes(key)) return false;
  const space = (sessions[ix.machine_id] ?? []).find((s) => s.session_id === ix.session_id)?.space_name;
  return Boolean(space && mutes.spaces.includes(`${ix.machine_id}/${space}`));
}

/** What it waits for: "Claude is asking you", "Plan waiting for your approval"…; Persona's ask_user and other agents by
 *  name: the card's own name for an agent the app doesn't know (a Paseo provider's label, "Grok", #8142), never its id. */
export function interactionTitle(ix: Interaction): string {
  if (ix.tool_name === "ask_user") return t("lib-inbox.ix_persona_asks");
  const name = ix.agent && ix.agent !== "claude" ? agentName(ix.agent, ix.agent_label) : "";
  if (name && ix.kind === "question") return t("lib-inbox.ix_named_asks", { name });
  if (name && ix.kind === "permission") return t("lib-inbox.ix_named_permission", { name });
  if (ix.kind === "elicitation") return ix.elicitation?.mode === "url" ? t("lib-inbox.ix_elicitation_url") : t("lib-inbox.ix_elicitation_form");
  return { question: t("lib-inbox.ix_claude_asks"), plan: t("lib-inbox.ix_plan"), permission: t("lib-inbox.ix_permission"), screen: t("lib-inbox.ix_screen") }[ix.kind];
}

/** What it asks, in a line. */
export function interactionAsk(ix: Interaction): string {
  const input = ix.tool_input as { description?: unknown; command?: unknown } | undefined;
  const str = (v: unknown) => (typeof v === "string" ? v : "");
  switch (ix.kind) {
    case "question":
      return ix.questions?.[0]?.question || ix.summary || "";
    case "permission":
      return ix.summary || str(input?.description) || str(input?.command) || ix.tool_title || ix.tool_name || "";
    case "screen":
      return ix.dialog?.question || ix.summary || "";
    case "elicitation":
      return ix.elicitation?.message?.trim() || ix.summary || "";
    default:
      return ix.summary || "";
  }
}

export function waitingAgents(interactions: Interaction[], sessions: Record<string, Session[]>, mutes: AgentMutes, f: InboxFilter): Interaction[] {
  if (f.source || f.folder || (f.kind && f.kind !== "agent")) return [];
  // a card a task's supervisor holds is not the user's yet (lib/supervisor-held.ts)
  const shown = interactions
    .filter((ix) => !ix.mirror_of && !supervisorHeld(ix) && !interactionMuted(ix, sessions, mutes) && hasText([ix.session_title, ix.summary, interactionTitle(ix), interactionAsk(ix)], f.query))
    .sort((a, b) => byTime(a.created_at, b.created_at) || a.id.localeCompare(b.id));
  return withChildCardsUnderParents(shown);
}

/** A child session's cards come with its parent's (#5120), where the parent's oldest card (or its first child card) is;
 *  the rest keep their order. The same list when no card is a child's. */
function withChildCardsUnderParents(cards: Interaction[]): Interaction[] {
  if (!cards.some((ix) => ix.folded_under)) return cards;
  const homes = new Set(cards.filter((ix) => ix.folded_under).map(homeKey));
  const group = (ix: Interaction) => (ix.session_id && homes.has(homeKey(ix)) ? homeKey(ix) : `card:${ix.id}`);
  const at = new Map<string, number>();
  for (const ix of cards) if (!at.has(group(ix))) at.set(group(ix), at.size);
  return [...cards].sort((a, b) => at.get(group(a))! - at.get(group(b))!);
}

function escAttr(s: string) {
  return s.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

/** `<persona-context kind="…" title="…" url="…">` the way the app writes it (ContextRef.xml). */
export function contextXML(kind: string, title: string, url: string, text: string): string {
  return `<persona-context kind="${kind}" title="${escAttr(title)}" url="${escAttr(url)}">\n${contextBody(text)}\n</persona-context>`;
}

/** "Hand to AI" to Persona's main conversation (docs/assistant/inbox.md "Item actions"): the item's manager sends text there
 *  after a block naming the item, or starts the conversation with it, and the conversation may write back to the item.
 *  The same clientID again sends nothing more. Resolves to the conversation it went to. */
const pendingHandoffs = new Map<string, Promise<InboxHandoff>>();

export function handOffToMain(item: InboxItem, text: string, clientID: string, reusePending = false): Promise<InboxHandoff> {
  const snapshot = suggestionSnapshot(item);
  const observed = state.uncertainReplies;
  const key = JSON.stringify([itemKey(snapshot), text, reusePending ? true : clientID]);
  const pending = pendingHandoffs.get(key);
  if (pending) return pending;
  const request = api<{ session: InboxHandoff; item?: InboxItem }>(`${itemPath(item.id)}/handoff`, {
    method: "POST", json: { text, client_id: clientID, ...(reusePending ? { reuse_pending: true } : {}) }, headers: itemRequest(item),
  }).then((r) => {
    if (r.item?.id === item.id) reconcileSuggestionItem(snapshot, r.item, observed);
    return r.session;
  }).finally(() => { if (pendingHandoffs.get(key) === request) pendingHandoffs.delete(key); });
  pendingHandoffs.set(key, request);
  return request;
}

// ── Moments ──────────────────────────────────────────────

export interface MomentContent {
  text?: string;
  segments?: { kind?: string; text?: string; key?: string; emojiKey?: string }[];
}

export interface MomentPost {
  id: string;
  userName?: string;
  userAvatarUrl?: string;
  content?: MomentContent;
  images?: { url?: string; width?: number; height?: number }[];
  reactionGroups?: { type: string; count: number }[];
  reactionCount?: number;
  commentCount?: number;
  createTime?: number;
  categoryNames?: string[];
}

export interface MomentComment {
  id: string;
  userName?: string;
  userAvatarUrl?: string;
  content?: MomentContent;
  createTime?: number;
  replyUserName?: string;
  reactionCount?: number;
}

// ── triage: the new items held back from the list (#1773) ───

/**
 * How triage stands (GET /api/v1/inbox `triage`, server/internal/assistant/inbox_admit.go): new items the model could not
 * sort wait outside the list. An older hub sends nothing.
 */
export interface InboxTriage {
  /** ok | unconfigured | paused | error | timeout | behind */
  state: string;
  /** The state for people, the hub's own words; may be empty. */
  reason?: string;
  waiting: number;
  sources?: InboxTriageSource[];
}

export interface InboxTriageSource {
  source: string;
  name?: string;
  state: string;
  reason?: string;
  waiting: number;
  /** The latest round could not read this source (`timeout` / `rate_limited` / `failed`): what it lists may be behind. */
  read_error?: string;
  /** That for people, in the hub's language; may be empty. */
  read_reason?: string;
}

/** What a state says when the hub gives no reason (the same words on iOS and Android). */
export const TRIAGE_REASONS: Record<string, string> = {
  unconfigured: t("lib-inbox.triage_unconfigured"),
  paused: t("lib-inbox.triage_paused"),
  error: t("lib-inbox.triage_error"),
  timeout: t("lib-inbox.triage_timeout"),
  behind: t("lib-inbox.triage_behind"),
};

/**
 * The banner over the list while new items wait for triage; null when none wait, triage is ok, or the hub is older.
 * One source waiting names it ("3 Lark messages waiting to be sorted: …"), several do not; the reason is the hub's, else the
 * state's words (an unknown state without a reason says only how many wait). `configure`: "Set up", only for a model
 * not set up.
 */
export function triageBanner(tr: InboxTriage | null | undefined): { text: string; configure: boolean } | null {
  if (!tr || typeof tr !== "object" || !tr.state || tr.state === "ok" || !(Number(tr.waiting) > 0)) return null;
  const sources = (Array.isArray(tr.sources) ? tr.sources : []).filter((x) => x && Number(x.waiting) > 0);
  const name = sources.length === 1 ? (sources[0].name ?? "").trim() : "";
  const reason = (tr.reason ?? "").trim() || TRIAGE_REASONS[tr.state] || "";
  const count = Number(tr.waiting);
  const waiting = name ? t("lib-inbox.triage_waiting_named", { count, name }) : t("lib-inbox.triage_waiting", { count });
  const text = reason ? t("lib-inbox.triage_waiting_reason", { waiting, reason }) : waiting;
  return { text, configure: tr.state === "unconfigured" };
}

// ── the list, shared by the page and the sidebar badge ───

interface InboxState {
  items: InboxItem[];
  /** The hub's triage state from the last list; null from an older hub. */
  triage: InboxTriage | null;
  /** Every retained built-in source, including completed history. Null from an old or mixed-version hub (unknown). */
  storedSources: InboxStoredSources | null;
  loaded: boolean;
  loading: boolean;
  error: string;
  /** Items whose reply is being sent (the row shows a spinner, a second tap does nothing). */
  sending: string[];
  /** A confirmation whose result/readback is unavailable. Only matching server receipts can clear this local safety hint. */
  uncertainReplies: Record<string, InboxSuggestionAttempt>;
  /** Status moves being written, by "<item>|<row>|<field>". */
  moving: string[];
  /** Done items older than the list's few days, from the hub's history (GET /api/v1/inbox/done, #2200), newest first. */
  history: InboxItem[];
  /** The managers ("" for the only one) with more history to give, or not asked yet. */
  historyMore: string[];
  historyLoading: boolean;
  /** Why a manager's last page didn't come, by manager ("" for the only one); an offline one has none (it is asked again later). */
  historyError: Record<string, string>;
  /** Each manager's done items per calendar day in this device's zone ("YYYY-MM-DD" → count; GET /api/v1/inbox/done/days,
   *  #2967), by manager ("" for the only one). null: its hub has no day counts, and Done pages the history from the newest
   *  as before; missing: not known yet (or the manager was offline). */
  doneDays: Record<string, Record<string, number> | null>;
  /** Done groups loading a page of their own (#2969), by "<manager>|<group key>". */
  groupLoading: string[];
  /** Done groups whose items have all come, by "<manager>|<group key>": a page of theirs reached the group's start. */
  groupComplete: string[];
  /** Items a route names being read from the hub (loadRoutedItem), by "<manager>\u0000<id>". */
  routing: string[];
}

const EMPTY: InboxState = { items: [], triage: null, storedSources: null, loaded: false, loading: false, error: "", sending: [], uncertainReplies: {}, moving: [], history: [], historyMore: [""], historyLoading: false, historyError: {}, doneDays: {}, groupLoading: [], groupComplete: [], routing: [] };
let state: InboxState = EMPTY;
const listeners = new Set<() => void>();
/** A Map that tells its watchers when it changes. The unsent replies live outside InboxState: a keystroke in the draft
 *  dialog redraws only the card of that item (useUnsentReply), not every list and badge reading the inbox. */
class WatchedMap<V> extends Map<string, V> {
  readonly #watchers = new Set<() => void>();
  /** Subscribes to every change; returns the unsubscribe (useSyncExternalStore's subscribe). */
  readonly watch = (l: () => void) => {
    this.#watchers.add(l);
    return () => {
      this.#watchers.delete(l);
    };
  };
  override set(key: string, value: V): this {
    super.set(key, value);
    this.#watchers.forEach((l) => l());
    return this;
  }
  override delete(key: string): boolean {
    const had = super.delete(key);
    if (had) this.#watchers.forEach((l) => l());
    return had;
  }
  override clear() {
    if (!this.size) return;
    super.clear();
    this.#watchers.forEach((l) => l());
  }
}

/** Unsent reply edits and accepted generations, by item (itemKey): the draft dialog keeps them when it closes without
 *  Cancel and the item's draft card shows and sends them. Send clears them and a failure restores its text. */
export const unsentReplies = new WatchedMap<string>();
/** The proposed status moves the user unticked in the draft dialog, by item (itemKey), as docChangeKey; kept, cleared
 *  and restored with the item's text. */
export const droppedChanges = new WatchedMap<readonly string[]>();

/** Keeps the item's unticked proposed changes (none: forgets them). */
export function keepDropped(key: string, dropped: readonly string[]) {
  if (dropped.length) droppedChanges.set(key, dropped);
  else droppedChanges.delete(key);
}

const NONE: readonly string[] = [];

/** The item's kept reply edit (undefined: none, the AI twin's draft stands); redraws when it changes. */
export function useUnsentReply(key: string): string | undefined {
  const read = () => unsentReplies.get(key);
  return useSyncExternalStore(unsentReplies.watch, read, read);
}

/** The item's unticked proposed changes (docChangeKey); redraws when they change. */
export function useDroppedChanges(key: string): readonly string[] {
  const read = () => droppedChanges.get(key) ?? NONE;
  return useSyncExternalStore(droppedChanges.watch, read, read);
}

// Every request about an item names its manager where it is made (itemRequest): two managers may list the same id, so
// nothing is looked up by path.
function set(patch: Partial<InboxState>) {
  state = { ...state, ...patch };
  listeners.forEach((l) => l());
}

export function getInbox(): InboxState {
  return state;
}

function patchItem(ref: { id: string; manager?: string }, fn: (it: InboxItem) => InboxItem) {
  set({ items: state.items.map((it) => (sameItem(it, ref) ? fn(it) : it)) });
}

/** The list's copy of an item (by id and manager), if it has it. */
function listed(ref: { id: string; manager?: string }): InboxItem | undefined {
  return state.items.find((x) => sameItem(x, ref));
}

/** The hub's copy of an item in place of the one shown. An answer about one item comes from its manager alone, which
 *  doesn't stamp it (the merged list does): it keeps the stamp it had, so its group and its next requests stay its own. */
function keepStamp(fresh: InboxItem, before: InboxItem): InboxItem {
  return fresh.manager || !before.manager ? fresh : { ...fresh, manager: before.manager };
}

/** Puts the hub's copy of one item in place (a fresh read after a status move, a reply). An answer is its manager's,
 *  unstamped: `manager` says whose (the item's the request was about); none: the default's. */
export function replaceItem(item: InboxItem, manager?: string) {
  if (item?.id) patchItem({ id: item.id, manager: item.manager ?? manager }, (before) => keepStamp(item, before));
}

/** Rows the hub changed or took out of the list (a merge, a to-do): its copies replace ours (`manager` says whose answer it is:
 *  the default's answers carry no stamp), and `gone` ids leave the list (a merged row's members are hidden by the hub). */
export function applyInboxChange(changed: InboxItem[], gone: string[], manager?: string) {
  const fresh = new Map(changed.map((it) => [it.id, it]));
  const out = new Set(gone);
  const same = (it: InboxItem) => sameManager(it.manager, manager);
  set({
    items: withPending(state.items.filter((it) => !(same(it) && out.has(it.id))).map((it) => (same(it) && fresh.has(it.id) ? keepStamp(fresh.get(it.id)!, it) : it))),
  });
}

/** A done / open mark still being sent: what the item shows until the hub answers. A mute's mark also carries its
 * reason ("Muted: <name>"). */
type DoneMark = Pick<InboxItem, "done_at" | "done_by" | "reason">;
/** A mark and, once the hub took it, the last list request that may have left before that (see listSeq). */
type MarkEntry = { mark: DoneMark; tookAt?: number };
/** Marks by item: a list that left before the hub took one must not put it back here (every inbox push and hello
 * reloads). A mark is laid over every list while its request is out, and once the hub took it, over the lists that left
 * before that. The newest mark of an item wins. */
const pendingDone = new Map<string, MarkEntry>();
/** Numbers the list requests (reloadInbox, refreshInbox) in the order they leave; listsOut holds those not back yet. */
let listSeq = 0;
const listsOut = new Set<number>();

/** Starts a list request: its number. */
function beginList(): number {
  listsOut.add(++listSeq);
  return listSeq;
}

/** Ends a list request and drops the taken marks no list still out can undo. */
function endList(seq: number) {
  listsOut.delete(seq);
  pruneMarks();
}

function pruneMarks() {
  for (const [id, e] of pendingDone) {
    if (e.tookAt === undefined) continue;
    if (![...listsOut].some((s) => s <= e.tookAt!)) pendingDone.delete(id);
  }
}

/** Whether an item has a mark whose request is still out. */
function markOut(id: string): boolean {
  const e = pendingDone.get(id);
  return !!e && e.tookAt === undefined;
}

/** Lays the marks over a list from the hub. Without a request number (an answer that came after the hub took the
 * marks) only the marks still out are laid. */
function withPending(items: InboxItem[], seq?: number): InboxItem[] {
  if (!pendingDone.size) return items;
  return items.map((it) => {
    const e = pendingDone.get(itemKey(it));
    if (!e) return it;
    const lay = e.tookAt === undefined || (seq !== undefined && seq <= e.tookAt);
    return lay ? { ...it, ...e.mark } : it;
  });
}

/** Sets a new mark for an item, replacing any older one. */
function putMark(id: string, mark: DoneMark) {
  pendingDone.set(id, { mark });
}

/** Ends a mark's request unless a newer mark of the same item replaced it meanwhile. Taken: it stays over the lists
 * that left before now, with the hub's done fields when it answered the item. Not taken: it is dropped (those lists
 * have the item as the hub still has it). `key` is the item's itemKey; `hub` the hub's answer about it. */
function settleMark(key: string, mark: DoneMark, taken: boolean, hub?: InboxItem) {
  const e = pendingDone.get(key);
  if (e?.mark !== mark) return;
  if (taken) {
    e.tookAt = listSeq;
    if (hub) e.mark = { done_at: hub.done_at, done_by: hub.done_by };
  } else pendingDone.delete(key);
  pruneMarks();
}

/** Marks that did not go through, by item (itemKey): the current copies (maybe fresher than when they were marked) take
 * back the done state they had (and the reason, for a mark that set one), except those with a newer mark still out. */
function undoMarks(was: Map<string, DoneMark>, withReason = false) {
  set({
    items: state.items.map((it) => {
      const k = itemKey(it);
      const w = was.get(k);
      if (!w || markOut(k)) return it;
      const back = { ...it, done_at: w.done_at, done_by: w.done_by };
      return withReason ? { ...back, reason: w.reason } : back;
    }),
  });
}

let started = false;
let inFlight: Promise<void> | null = null;
let again = false;

/** The hub's last list as it came (before the marks were laid over it) and what names it: its cursor, so a reload asks
 * only for what changed since (docs/protocol/protocol.md "Inbox delta"), and its validator, sent as If-None-Match: a
 * 304 says the list is still this one ("Inbox validator"). Memory only: a page's first list is whole. */
let lastList: { etag: string; cursor: string; items: InboxItem[]; triage: InboxTriage | null; storedSources: InboxStoredSources | null } | null = null;
/** What a reload sends before it has a validator: a tag no list has, which asks the hub for its own. */
const NO_LIST_TAG = 'W/"i1-"';
/** Until when reloads ask without `since`: an answer had no cursor (an older hub or manager, or one that failed), so the
 * validator does the work, and deltas are tried again ten minutes later. */
let deltasOff = 0;

function start() {
  if (started) return;
  started = true;
  onHubEvent("inbox", () => void reloadInbox());
  onHubEvent("hello", (msg) => !isResumed(msg) && void reloadInbox()); // resumed: the missed inbox pushes are replayed
  void reloadInbox();
}

/** `next` with every item that reads the same as the one of that id we have kept as our object, and our array when
 *  nothing changed: a re-read is fresh JSON, and new objects would render every memoized row again on each push. */
export function keepSameItems(prev: InboxItem[], next: InboxItem[]): InboxItem[] {
  const before = new Map(prev.map((it) => [itemKey(it), it]));
  let all = prev.length === next.length;
  const out = next.map((it, i) => {
    const old = before.get(itemKey(it));
    const kept = old && sameValue(old, it) ? old : it;
    if (kept !== prev[i]) all = false;
    return kept;
  });
  return all ? prev : out;
}

/** Pulls the list; a request asked for while one is out runs once more after it (bursts of pushes collapse). */
export function reloadInbox(): Promise<void> {
  if (inFlight) {
    again = true;
    return inFlight;
  }
  inFlight = (async () => {
    set({ loading: true });
    try {
      do {
        again = false;
        const seq = beginList();
        try {
          let status = 0;
          let etag = "";
          const held = lastList;
          const observed = state.uncertainReplies;
          // "0" until a list is held: one no list has, so the hub answers the whole list and its cursor
          const since = Date.now() < deltasOff ? "" : held?.cursor || "0";
          // If-None-Match too: a hub that knows no `since` still answers a list it has with a 304
          const r = await api<{ items?: InboxItem[]; triage?: InboxTriage; stored_sources?: InboxStoredSources | null; cursor?: string; delta?: boolean; removed?: { id: string; manager?: string }[] }>(`/inbox?sources=all${since && `&since=${enc(since)}`}`, {
            headers: { "If-None-Match": held?.etag || NO_LIST_TAG },
            onResponse: (headers, code) => {
              status = code;
              etag = headers?.get("ETag") ?? "";
            },
          });
          // 304: the list is still the one kept, and the marks are laid over it again as over any list of this seq
          let list = held;
          if (status !== 304) {
            const cursor = r.cursor ?? "";
            if (since && !cursor) deltasOff = Date.now() + 600_000;
            // a delta answers only the cursor sent, so the list it changes is the one held
            list = { etag, cursor, items: r.delta && held ? applyInboxDelta(held.items, r.items ?? [], r.removed ?? []) : r.items ?? [], triage: r.triage ?? null, storedSources: r.stored_sources ?? null };
            lastList = etag || cursor ? list : null;
          }
          if (list) set({ items: keepSameItems(state.items, withPending(list.items, seq)), triage: list.triage, storedSources: list.storedSources, loaded: true, error: "",
            uncertainReplies: replyHintsAfterRead(status === 304 ? [] : r.items ?? [], observed) });
        } finally {
          endList(seq);
        }
      } while (again);
    } catch (e) {
      set({ error: errorText(e), loaded: true });
    } finally {
      set({ loading: false });
      inFlight = null;
    }
  })();
  return inFlight;
}

/** `held` with a delta applied (docs/protocol/protocol.md "Inbox delta"): changed items in place, new ones in, removed
 *  ones out, in the hub's order (each manager's items together, the managers as they came; newest first, then by id). */
export function applyInboxDelta(held: InboxItem[], changed: InboxItem[], removed: { id: string; manager?: string }[]): InboxItem[] {
  const gone = new Set(removed.map(itemKey));
  const fresh = new Map(changed.map((it) => [itemKey(it), it]));
  const out = held.filter((it) => !gone.has(itemKey(it))).map((it) => {
    const k = itemKey(it);
    const now = fresh.get(k) ?? it;
    fresh.delete(k);
    return now;
  });
  out.push(...fresh.values());
  const managers = new Map<string | undefined, number>();
  for (const it of out) if (!managers.has(it.manager)) managers.set(it.manager, managers.size);
  return out.sort((a, b) => managers.get(a.manager)! - managers.get(b.manager)! || byTime(b.updated_at, a.updated_at) || (a.id < b.id ? -1 : +(a.id > b.id)));
}

/** "Refresh": the hub collects Feishu and mail now, then lists. Its whole list has no cursor: what is held stays, so the
 * next reload's delta is still from the list its cursor names. */
export async function refreshInbox(): Promise<void> {
  set({ loading: true });
  const seq = beginList();
  const observed = state.uncertainReplies;
  try {
    const r = await api<{ items?: InboxItem[]; triage?: InboxTriage; stored_sources?: InboxStoredSources | null }>("/inbox/refresh?sources=all", { method: "POST" });
    set({ items: keepSameItems(state.items, withPending(r.items ?? [], seq)), triage: r.triage ?? null, storedSources: r.stored_sources ?? null, loaded: true, error: "",
      uncertainReplies: replyHintsAfterRead(r.items ?? [], observed) });
  } catch (e) {
    toast(t("lib-inbox.refresh_failed", { error: errorText(e) }), "error");
  } finally {
    endList(seq);
    set({ loading: false });
  }
}

/** "Done" / "Move back": shown at once, sent in the background; a failure puts the item back. */
/** Marks an item done or open; resolves to whether the hub took it. */
export async function setDone(item: InboxItem, done: boolean): Promise<boolean> {
  const before = listed(item);
  if (!before) return done ? false : reopenFromHistory(item);
  const key = itemKey(before);
  const mark: DoneMark = { done_at: done ? new Date().toISOString() : null, done_by: done ? "user" : null };
  putMark(key, mark);
  patchItem(before, (it) => ({ ...it, ...mark }));
  try {
    const r = await api<InboxItem>(`${itemPath(item.id)}/done`, { method: "POST", json: { done }, headers: itemRequest(before) });
    const hub = r?.id === item.id ? keepStamp(r, before) : undefined;
    settleMark(key, mark, true, hub);
    // The hub answers the item as it has it now; a newer mark still out stays on top.
    if (hub) replaceItem(withPending([hub])[0]);
    return true;
  } catch (e) {
    settleMark(key, mark, false);
    undoMarks(new Map([[key, before]]));
    toast(t(done ? "lib-inbox.done_failed" : "lib-inbox.undone_failed", { error: errorText(e) }), "error");
    return false;
  }
}

// ── Done's history (#2200) ───

/** Where a manager's next page of done history starts: (done_at, id) of the last item it gave. */
type DoneCursor = { before: string; before_id: string };
/** Each manager's next page ("" for the only one): undefined not asked yet, null at its end or on a hub without it. */
const historyNext = new Map<string, DoneCursor | null>();

/**
 * One more page of done items from each manager that has more (the list holds only the last few days), appended to
 * `history` newest first, each stamped with its manager. A hub without the history (404 / 405) has nothing more: Done
 * then shows what the list holds. `managers`: the managers' ids, or [undefined] for one.
 */
export async function loadOlderDone(managers: (string | undefined)[]): Promise<void> {
  const asks = managers.filter((m) => historyNext.get(m ?? "") !== null);
  if (state.historyLoading || !asks.length) return;
  set({ historyLoading: true });
  const pages = await Promise.allSettled(asks.map(async (m) => {
    const cur = historyNext.get(m ?? "");
    const q = new URLSearchParams({ sources: "all", limit: "100" });
    if (cur) {
      q.set("before", cur.before);
      q.set("before_id", cur.before_id);
    }
    try {
      const r = await api<{ items?: InboxItem[]; next?: DoneCursor }>(`/inbox/done?${q}`, { headers: itemHeaders(m) });
      historyNext.set(m ?? "", r.next ?? null);
      return (r.items ?? []).map((it) => (m && !it.manager ? { ...it, manager: m } : it));
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 405)) {
        historyNext.set(m ?? "", null);
        return [];
      }
      throw e;
    }
  }));
  const got = pages.flatMap((p) => (p.status === "fulfilled" ? p.value : []));
  const errors: Record<string, string> = { ...state.historyError };
  pages.forEach((p, i) => {
    const m = asks[i] ?? "";
    // a manager offline (503) or gone (404) is asked again next time, without a word: its Done is what the list has
    const quiet = p.status === "rejected" && p.reason instanceof ApiError && (p.reason.status === 503 || p.reason.status === 404);
    if (p.status === "rejected" && !quiet) errors[m] = errorText(p.reason);
    else delete errors[m];
  });
  const seen = new Set(state.history.map(itemKey));
  set({
    history: [...state.history, ...got.filter((it) => !seen.has(itemKey(it)))],
    historyMore: managers.map((m) => m ?? "").filter((m) => historyNext.get(m) !== null),
    historyLoading: false,
    historyError: errors,
  });
}

/** Where each Done group's next page starts, once one of its pages has come and the group isn't complete. */
const groupNext = new Map<string, DoneCursor>();

/**
 * Each manager's done items per day (GET /api/v1/inbox/done/days, #2967) in this device's zone, so Done lists every
 * day's group with its count before any of its items load. A hub without them (404 / 405, or 400 for a zone it doesn't
 * know) gets null: Done pages its history from the newest, as before. An offline manager stays unknown and is asked
 * again next time. Returns the managers that have no counts.
 */
export async function loadDoneDays(managers: (string | undefined)[]): Promise<(string | undefined)[]> {
  const q = new URLSearchParams({ tz: Intl.DateTimeFormat().resolvedOptions().timeZone, sources: "all" });
  const got = await Promise.allSettled(managers.map(async (m) => {
    try {
      const r = await api<{ days?: { day: string; count: number }[] }>(`/inbox/done/days?${q}`, { headers: itemHeaders(m) });
      return Object.fromEntries((r.days ?? []).map((d) => [d.day, d.count]));
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 405 || e.status === 400)) return null;
      throw e;
    }
  }));
  const doneDays = { ...state.doneDays };
  got.forEach((p, i) => {
    if (p.status === "fulfilled") doneDays[managers[i] ?? ""] = p.value;
  });
  set({ doneDays });
  return managers.filter((m) => doneDays[m ?? ""] === null);
}

/**
 * One page of a Done group's own items (#2969): from the group's end (`before` alone is a moment: the items done before
 * it), then from where its last page ended. The group is complete when a page reaches its start or the history ends.
 * The items join `history`; those of older groups a page also brings stay there for theirs.
 */
export async function loadDoneGroup(manager: string | undefined, key: string, range: { start: number; end: number }): Promise<void> {
  const id = `${manager ?? ""}|${key}`;
  if (state.groupLoading.includes(id) || state.groupComplete.includes(id)) return;
  set({ groupLoading: [...state.groupLoading, id] });
  const q = new URLSearchParams({ sources: "all", limit: "100" });
  const cur = groupNext.get(id) ?? pastListed(manager, range);
  if (cur) {
    q.set("before", cur.before);
    q.set("before_id", cur.before_id);
  } else if (Number.isFinite(range.end)) q.set("before", new Date(range.end).toISOString());
  const m = manager ?? "";
  try {
    const r = await api<{ items?: InboxItem[]; next?: DoneCursor }>(`/inbox/done?${q}`, { headers: itemHeaders(manager) });
    const got = (r.items ?? []).map((it) => (manager && !it.manager ? { ...it, manager } : it));
    const last = got.at(-1)?.done_at;
    const complete = !r.next || !last || Date.parse(last) < range.start;
    if (complete) groupNext.delete(id);
    else groupNext.set(id, r.next!);
    const seen = new Set(state.history.map(itemKey));
    const { [m]: _, ...errors } = state.historyError;
    set({
      history: [...state.history, ...got.filter((it) => !seen.has(itemKey(it)))],
      groupLoading: state.groupLoading.filter((x) => x !== id),
      groupComplete: complete ? [...state.groupComplete, id] : state.groupComplete,
      historyError: errors,
    });
  } catch (e) {
    // a manager offline (503) or gone (404) is asked again when the group opens again, without a word
    const quiet = e instanceof ApiError && (e.status === 503 || e.status === 404);
    set({ groupLoading: state.groupLoading.filter((x) => x !== id), historyError: quiet ? state.historyError : { ...state.historyError, [m]: errorText(e) } });
  }
}

/**
 * Where a group's first page starts when the list already holds its newest done items: just past the oldest one the
 * list holds of that manager, if it falls in the group. The list's done items are the newest of the history (every
 * open item and the newest 200 done within three days, #5549), so a page from the group's end would bring only rows
 * the list has, and its Show older would seem to do nothing.
 */
function pastListed(manager: string | undefined, range: { start: number; end: number }): DoneCursor | undefined {
  // the history's order is done_at, then id, both descending; a mark of this app's is not the hub's done_at
  let oldest: InboxItem | undefined;
  let at = Infinity;
  for (const it of state.items) {
    if (!it.done_at || !sameManager(it.manager, manager) || pendingDone.has(itemKey(it))) continue;
    const t = Date.parse(it.done_at);
    if (t < at || (t === at && oldest && it.id < oldest.id)) [oldest, at] = [it, t];
  }
  return oldest && at >= range.start && at < range.end ? { before: oldest.done_at!, before_id: oldest.id } : undefined;
}

/** Done as the list shows it: the list's done items, then the history's it doesn't have (the list's copy is newer). */
export function doneWithHistory(done: InboxItem[], history: InboxItem[], f: InboxFilter): InboxItem[] {
  const listedKeys = new Set(done.map(itemKey));
  const older = history.filter((it) => isDone(it) && matches(it, f) && !listedKeys.has(itemKey(it)));
  return older.length ? [...done, ...older].sort(byDoneAt) : done;
}

/** The routes asked for an item neither the list nor the loaded history had (loadRoutedItem): each once. */
const routesAsked = new Set<string>();

/**
 * An item a route names that neither the list nor the loaded history has, such as a link to one done past what the
 * list carries (only the newest done items, #5549): the hub's copy joins `history`, where the route finds it. Asked
 * once per item; an open one comes with the list, and one the hub doesn't have stays gone. `routing` holds it meanwhile.
 */
export function loadRoutedItem(id: string, manager: string | undefined): void {
  const key = `${manager ?? ""}\u0000${id}`;
  if (routesAsked.has(key)) return;
  routesAsked.add(key);
  set({ routing: [...state.routing, key] });
  void api<InboxItem>(itemPath(id), { headers: itemHeaders(manager) })
    .then((r) => {
      const it = r?.id === id && isDone(r) ? (manager && !r.manager ? { ...r, manager } : r) : undefined;
      if (it && !state.history.some((x) => sameItem(x, it)) && !listed(it)) set({ history: [...state.history, it] });
    })
    .catch(() => undefined)
    .finally(() => set({ routing: state.routing.filter((x) => x !== key) }));
}

/** Move back on an item only the history has (done before the list's days): the hub takes it back into its list. */
async function reopenFromHistory(item: InboxItem): Promise<boolean> {
  const old = state.history.find((x) => sameItem(x, item));
  if (!old) return false;
  try {
    await api<InboxItem>(`${itemPath(item.id)}/done`, { method: "POST", json: { done: false }, headers: itemHeaders(old.manager) });
    set({ history: state.history.filter((x) => !sameItem(x, item)) });
    void reloadInbox();
    return true;
  } catch (e) {
    toast(t("lib-inbox.undone_failed", { error: errorText(e) }), "error");
    return false;
  }
}

/** "Move to folder…": files the item under `folder` by hand ("" in no folder), or hands it back to the model (null).
 *  Shown at once; a failure puts it back. Resolves to whether the hub took it. */
export async function moveToFolder(item: InboxItem, folder: string | null): Promise<boolean> {
  const before = listed(item);
  if (!before) return false;
  patchItem(before, (it) => ({ ...it, folder: folder || undefined, folder_by: folder === null ? undefined : "user" }));
  try {
    const r = await api<InboxItem>(`${itemPath(item.id)}/folder`, { method: "PUT", json: { folder }, headers: itemRequest(before) });
    if (r?.id === item.id) replaceItem(keepStamp(r, before));
    return true;
  } catch (e) {
    patchItem(before, (it) => ({ ...it, folder: before.folder, folder_by: before.folder_by }));
    toast(t("lib-inbox.folder_failed", { error: errorText(e) }), "error");
    return false;
  }
}

/** Whether a mute covers an item: the same source, still open, and the same thing among what it can be muted by; with
 *  `manager`, only that manager's items (each manager keeps its own mutes and rules). */
export function muteCovers(source: string, o: InboxMuteOption, it: InboxItem, manager?: { manager?: string }): boolean {
  return it.source === source && !isDone(it) && (it.mutes ?? []).some((m) => m.tag === o.tag && m.value === o.value) && (!manager || sameManager(it.manager, manager.manager));
}

/**
 * Mute this repository / chat / sender (iOS InboxStore.mute): the item and the open ones of its source the mute covers go to "Done"
 * at once ("Muted: <name>"), the hub keeps the mute and files them too; a failure puts them back.
 */
export async function muteItem(item: InboxItem, o: InboxMuteOption): Promise<void> {
  // Only the covered items are put back: a reload during the request (every hub inbox event reloads) keeps its items.
  // The mute is the item's manager's: it covers that manager's items only.
  const before = new Map(state.items.filter((it) => muteCovers(item.source, o, it, item)).map((it) => [itemKey(it), it]));
  // A mark like setDone's: a list that left before the hub filed them does not put them back open.
  const mark: DoneMark = { done_at: new Date().toISOString(), done_by: "read", reason: t("lib-inbox.muted_reason", { name: o.name }) };
  for (const key of before.keys()) putMark(key, mark);
  set({ items: state.items.map((it) => (before.has(itemKey(it)) ? { ...it, ...mark } : it)) });
  try {
    await api("/inbox/mutes", { method: "POST", json: { item_id: item.id, tag: o.tag }, headers: itemRequest(item) });
    before.forEach((_, key) => settleMark(key, mark, true));
    toast(t("lib-inbox.muted", { name: o.name }), "ok");
  } catch (e) {
    before.forEach((_, key) => settleMark(key, mark, false));
    undoMarks(before, true);
    toast(t("lib-inbox.mute_failed", { error: errorText(e) }), "error");
  }
}

/** A policy's tier (docs/assistant/inbox.md "Policies"): "" leaves the tier to triage. */
export type PolicyTier = "" | "action" | "fyi" | "noise";
/** What a policy does with the AI twin: "first" hands it over first (even when not Needs you), "never" keeps it away; "" as configured. */
export type PolicyTwin = "" | "first" | "never";

/** A policy the user set (GET /api/v1/inbox/policies): items of `source` with `value` under `tag` get its tier and twin. */
export interface InboxPolicy extends InboxMuteOption {
  id: string;
  source: string;
  source_name?: string;
  tier?: PolicyTier;
  twin?: PolicyTwin;
  created_at?: string;
  updated_at?: string;
  /** Whose rule it is (several managers: each one keeps its own; listInboxPolicies stamps it); none: the default's. */
  manager?: string;
}

/** Whether a policy's tier reaches an item at once (the hub's rule): the same thing, open, triaged, not being handled,
 *  and the same manager's (each manager keeps its own rules). */
function policyReaches(source: string, o: InboxMuteOption, it: InboxItem, manager: { manager?: string }): boolean {
  return muteCovers(source, o, it, manager) && Boolean(it.tier) && statusOf(it) !== "working";
}

/**
 * Set the policy of what an item has under an option's tag (POST /api/v1/inbox/policies), or change it (PATCH, with
 * `id`): the open items it covers take its tier at once ("Your rule: <name>"); the hub does the same and the next
 * push confirms it; a failure puts them back. Setting neither part removes the policy. Resolves to the policy, or null
 * when the hub refused. `stamped`: name `manager` even when it is the default (a rule a list read from each manager).
 */
export async function saveInboxPolicy(target: { item?: InboxItem; option: InboxMuteOption; source: string; id?: string; manager?: string; stamped?: boolean }, tier: PolicyTier, twin: PolicyTwin): Promise<InboxPolicy | null> {
  const { option: o, source } = target;
  // the rule is the manager's whose item (or rule) it is: it reaches that manager's items only
  const owner = { manager: target.manager ?? target.item?.manager };
  const before = new Map(tier ? state.items.filter((it) => policyReaches(source, o, it, owner)).map((it) => [itemKey(it), it]) : []);
  const reason = t("lib-inbox.policy_reason", { name: o.name });
  const statusFor = (it: InboxItem) => (it.status && it.status !== "working" ? tier || it.status : it.status);
  set({ items: state.items.map((it) => (before.has(itemKey(it)) && it.tier !== tier ? { ...it, tier, status: statusFor(it), reason, urgent: tier === "action" ? it.urgent : false } : it)) });
  const headers = target.stamped ? stampedHeaders(owner.manager) : itemHeaders(owner.manager);
  try {
    const r = target.id
      ? await api<{ policy: InboxPolicy }>(`/inbox/policies/${enc(target.id)}`, { method: "PATCH", json: { tier, twin }, headers })
      : await api<{ policy: InboxPolicy }>("/inbox/policies", { method: "POST", json: { item_id: target.item?.id, tag: o.tag, tier, twin }, headers });
    toast(tier || twin ? t("lib-inbox.policy_saved", { name: o.name }) : t("lib-inbox.policy_removed", { name: o.name }), "ok");
    return r.policy && owner.manager ? { ...r.policy, manager: owner.manager } : r.policy;
  } catch (e) {
    set({ items: state.items.map((it) => before.get(itemKey(it)) ?? it) });
    toast(t("lib-inbox.policy_failed", { error: errorText(e) }), "error");
    return null;
  }
}

/** What a bulk action does (POST /api/v1/inbox/bulk): `dismiss` is Clean up all (done_by=dismissed), `done` files a section
 * (Mark all as seen / Clear all), `reopen` is Move back (their Undo). */
export type BulkAction = "done" | "dismiss" | "reopen";

/** What a bulk request did: the items it moved as the hub has them now, the ones already so, and the ones it left
 * (`changed`: new messages since the list; `missing`: gone from the hub). */
export type BulkResult = { changed: InboxItem[]; unchanged: string[]; skipped: { id: string; reason: string; manager?: string }[] };

/** The toast after a bulk request. */
export function bulkMessage(action: BulkAction | boolean, r: BulkResult): string {
  const a: BulkAction = action === true ? "done" : action === false ? "reopen" : action;
  const moved = r.changed.length;
  const fresh = r.skipped.filter((s) => s.reason === "changed").length;
  const head = !moved
    ? t(a === "reopen" ? "lib-inbox.bulk_undone_none" : "lib-inbox.bulk_done_none")
    : t(a === "reopen" ? "lib-inbox.bulk_undone" : a === "dismiss" ? "lib-inbox.bulk_cleaned" : "lib-inbox.bulk_done", { count: moved });
  return fresh ? t("lib-inbox.bulk_fresh", { head, count: fresh }) : head;
}

/** What the managers answered, put together, and the items of those that did not answer (with why). */
type BulkOutcome = { result: BulkResult; failed: { items: InboxItem[]; error: unknown }[] };

/** One bulk request. A hub from before /api/v1/inbox/bulk (404 / 405) gets it the old way (POST /api/v1/inbox/done):
 * it has no Dismiss, so Clean up all there files the items as done (done_by=user), which still clears them out of the list. */
async function bulkRequest(action: BulkAction, targets: InboxItem[]): Promise<BulkOutcome> {
  // Each manager files its own items (the merged list stamps them; the hub sends /inbox/bulk to the manager the request
  // names, docs/protocol/protocol.md "Per-manager routing"): one request per manager, the answers put together, each one's items
  // and skips stamped with its manager (it answers unstamped) so the same id of two managers stays apart. One manager
  // failing (offline) leaves the others' done.
  const byManager = new Map<string, InboxItem[]>();
  for (const it of targets) {
    const m = otherManager(it.manager) ?? "";
    byManager.set(m, [...(byManager.get(m) ?? []), it]);
  }
  const groups = [...byManager];
  const settled = await Promise.allSettled(groups.map(([m, list]) => bulkOne(action, list, m || undefined)));
  const answers: BulkResult[] = [];
  const failed: BulkOutcome["failed"] = [];
  settled.forEach((s, i) => {
    if (s.status === "rejected") return void failed.push({ items: groups[i][1], error: s.reason });
    // whose each answered item is: the manager asked (its own items carry their stamp; the default's may have none)
    const own = new Map(groups[i][1].map((it) => [it.id, it.manager]));
    const r = s.value;
    answers.push({
      changed: (r.changed ?? []).map((it) => (it.manager || !own.get(it.id) ? it : { ...it, manager: own.get(it.id) })),
      unchanged: r.unchanged ?? [],
      skipped: (r.skipped ?? []).map((x) => ({ ...x, manager: x.manager ?? own.get(x.id) })),
    });
  });
  if (!answers.length) throw failed[0].error;
  if (answers.length === 1) return { result: answers[0], failed };
  const result = answers.reduce<BulkResult>((all, r) => ({
    changed: [...all.changed, ...(r.changed ?? [])],
    unchanged: [...all.unchanged, ...(r.unchanged ?? [])],
    skipped: [...all.skipped, ...(r.skipped ?? [])],
  }), { changed: [], unchanged: [], skipped: [] });
  return { result, failed };
}

async function bulkOne(action: BulkAction, targets: InboxItem[], manager: string | undefined): Promise<BulkResult> {
  const items = targets.map((it) => ({ id: it.id, version: it.version }));
  const headers = itemHeaders(manager);
  try {
    return await api<BulkResult>("/inbox/bulk", { method: "POST", json: { action, items }, headers });
  } catch (e) {
    if (!(e instanceof ApiError) || (e.status !== 404 && e.status !== 405)) throw e;
    return api<BulkResult>("/inbox/done", { method: "POST", json: { done: action !== "reopen", items }, headers });
  }
}

/** Clean up all (the view's open items), Mark all as seen / Clear all (a section), and their Undo: one request for
 * them all, each with the version it was listed at, so the hub leaves one that took new messages since open (it goes
 * back here too, and the list is pulled again). Shown at once; a failure puts them all back. Filing ends with "Undo" on
 * the toast. `action` true / false is done / reopen. */
export async function setAllDone(items: InboxItem[], action: BulkAction | boolean, undoable = true): Promise<void> {
  const a: BulkAction = action === true ? "done" : action === false ? "reopen" : action;
  const done = a !== "reopen";
  const seen = new Set<string>();
  // Undo reopens what the hub said it moved, listed or not: the list carries only the newest done items (#5549), so
  // after a large Clean up all most of them are no longer in it.
  const targets = items
    .map((it) => listed(it) ?? (!done && !undoable ? it : undefined))
    .filter((it): it is InboxItem => !!it && isDone(it) !== done && !seen.has(itemKey(it)) && !!seen.add(itemKey(it)));
  if (!targets.length) return;
  const before = new Map(targets.map((it) => [itemKey(it), it]));
  const now = new Date().toISOString();
  const by = a === "dismiss" ? "dismissed" : "user";
  const mark: DoneMark = { done_at: done ? now : null, done_by: done ? by : null };
  for (const key of before.keys()) putMark(key, mark);
  set({ items: state.items.map((it) => (before.has(itemKey(it)) ? { ...it, ...mark } : it)) });
  try {
    const { result: r, failed } = await bulkRequest(a, targets);
    // The ones the hub left (new messages, gone), and those of a manager that did not answer, are not laid over earlier
    // lists; they take back their done state on the current copy below.
    const lost = new Set(failed.flatMap((f) => f.items.map(itemKey)));
    const left = new Set([...(r.skipped ?? []).map((x) => itemKey(x)), ...lost]);
    const fresh = new Map((r.changed ?? []).map((it) => [itemKey(it), it]));
    before.forEach((_, key) => settleMark(key, mark, !left.has(key), fresh.get(key)));
    set({ items: withPending(state.items.map((it) => (fresh.has(itemKey(it)) ? keepStamp(fresh.get(itemKey(it))!, it) : it))) });
    if (left.size) undoMarks(new Map([...before].filter(([key]) => left.has(key))));
    if ((r.skipped ?? []).some((s) => s.reason === "changed")) void reloadInbox();
    const moved = r.changed ?? [];
    const undo = undoable && done && moved.length ? { label: t("lib-inbox.undo"), run: () => void setAllDone(moved, "reopen", false) } : undefined;
    if (failed.length) toast(t("lib-inbox.bulk_partly", { head: bulkMessage(a, r), count: lost.size, error: errorText(failed[0].error) }), "error", undo);
    else toast(bulkMessage(a, r), "ok", undo);
  } catch (e) {
    before.forEach((_, key) => settleMark(key, mark, false));
    undoMarks(before);
    toast(t(!done ? "lib-inbox.undone_failed" : a === "dismiss" ? "lib-inbox.clean_failed" : "lib-inbox.done_failed", { error: errorText(e) }), "error");
  }
}

/** "Send": the hub replies in the chat / by mail / in the document (moving the rows' status in `changes` first) and
 * files the item as done. Filed at once; a failure puts the item back and keeps the text and the unticked changes for
 * the draft card and dialog. */
export async function sendReply(item: InboxItem, text: string, changes: DocChange[] = [], expectedSender?: "bot" | "user"): Promise<boolean> {
  const key = itemKey(item);
  if (state.sending.includes(key)) return false;
  const before = listed(item);
  set({ sending: [...state.sending, key] });
  // A mark like setDone's: a list that left before the hub filed it does not put it back open.
  const mark: DoneMark = { done_at: new Date().toISOString(), done_by: "replied" };
  // (Also for an item the list does not have yet: a list that left before the send must not bring it in open.)
  putMark(key, mark);
  patchItem(item, (it) => ({ ...it, ...mark }));
  const dropped = droppedChanges.get(key);
  unsentReplies.delete(key);
  droppedChanges.delete(key);
  try {
    const r = await api<InboxItem>(`${itemPath(item.id)}/reply`, { method: "POST", json: { text, changes: changes.length ? changes : undefined, expected_sender_identity: expectedSender }, headers: itemRequest(item) });
    const hub = r?.id === item.id ? keepStamp(r, item) : undefined;
    settleMark(key, mark, true, hub);
    if (hub) replaceItem(withPending([hub])[0]);
    toast(t("lib-inbox.sent_done"), "ok");
    return true;
  } catch (e) {
    settleMark(key, mark, false);
    undoMarks(new Map([[key, before ?? item]]));
    if (text) unsentReplies.set(key, text);
    if (dropped) droppedChanges.set(key, dropped);
    toast(t("lib-inbox.reply_failed", { error: errorText(e) }), "error");
    return false;
  } finally {
    set({ sending: state.sending.filter((x) => x !== key) });
  }
}

/** Identity of a local confirmation whose delivery could not be read back. */
export function uncertainReplyKey(item: InboxItem, version: number, conversation = item.suggestion?.conversation?.session_id ?? ""): string {
  return JSON.stringify([itemKey(item), version, conversation]);
}

function suggestionSnapshot(item: InboxItem): InboxItem {
  return listed(item) ?? state.history.find((x) => sameItem(x, item)) ?? item;
}

/** Only receipts returned by this read clear hints it already knew about; held delta rows are not fresh receipts. */
function replyHintsAfterRead(items: InboxItem[], observed: InboxState["uncertainReplies"]): InboxState["uncertainReplies"] {
  const hints = { ...state.uncertainReplies };
  for (const item of items) for (const a of item.suggestion_attempts ?? []) {
    const key = uncertainReplyKey(item, a.version, a.conversation?.session_id ?? "");
    if (hints[key] === observed[key]) delete hints[key];
  }
  return hints;
}

/** Keep a store update that arrived during this request; only merge its reply receipts into that current item. */
function reconcileSuggestionItem(ref: InboxItem, fresh: InboxItem, observed = state.uncertainReplies): boolean {
  if (fresh.id !== ref.id || fresh.manager && !sameManager(fresh.manager, ref.manager)) return false;
  const before = listed(ref) ?? state.history.find((x) => sameItem(x, ref));
  const attempts = new Map<number, InboxSuggestionAttempt>();
  for (const a of before?.suggestion_attempts ?? []) attempts.set(a.version, a);
  for (const a of fresh.suggestion_attempts ?? []) {
    const old = attempts.get(a.version);
    if (old && (Date.parse(old.at) > Date.parse(a.at) || old.status === "sent" || old.status === "unknown" && a.status === "failed")) continue;
    attempts.set(a.version, a);
  }
  const latest = (it: InboxItem | undefined) => Math.max(it?.suggestion?.version ?? 0, ...(it?.suggestion_attempts ?? []).map((a) => a.version));
  const kept = before && (before !== ref || latest(before) > Math.max(latest(fresh), ref.suggestion?.version ?? 0)) ? before : fresh;
  // Match the server's existing bounded handling-chain history; missing older receipts stay unknown to the UI.
  const receipts = [...attempts.values()].sort((a, b) => a.version - b.version).slice(-50);
  const next = { ...kept, ...(receipts.length ? { suggestion_attempts: receipts } : {}) };
  const uncertain = replyHintsAfterRead([keepStamp(fresh, ref)], observed);
  const replace = (it: InboxItem) => sameItem(it, ref) ? keepStamp(next, it) : it;
  set({ items: state.items.map(replace), history: state.history.map(replace), uncertainReplies: uncertain });
  return true;
}

async function readSuggestionItem(item: InboxItem): Promise<void> {
  const snapshot = suggestionSnapshot(item);
  const observed = state.uncertainReplies;
  try {
    const fresh = await api<InboxItem>(itemPath(item.id), { headers: itemRequest(item) });
    reconcileSuggestionItem(snapshot, fresh, observed);
  } catch { /* The local uncertain hint stays; a failed GET cannot authorize another send. */ }
}

/** Run only the displayed suggestion version; reconcile its outcome with updates received while it was in flight. */
export async function suggestionAct(item: InboxItem, action: SuggestionAction, text?: string, due?: string, expectedVersion = item.suggestion?.version): Promise<boolean> {
  const current = suggestionSnapshot(item);
  const s = current.suggestion;
  const key = itemKey(current);
  if (!s || s.version !== expectedVersion || state.sending.includes(key)) return false;
  const attempt = current.suggestion_attempts?.find((a) => a.version === s.version);
  const confirmation = action === "confirm" && s.kind === "reply" && !s.handback;
  const uncertainKey = uncertainReplyKey(current, s.version);
  if (confirmation && (state.uncertainReplies[uncertainKey] || attempt && (attempt.status !== "failed" || !attempt.retryable))) return false;
  // A stale editor must not silently confirm different words from the frozen attempt.
  if (confirmation && attempt && text !== undefined && text !== attempt.text) return false;
  const sentText = attempt?.text ?? text ?? s.text;
  set({ sending: [...state.sending, key] });
  try {
    // A Retry confirms the frozen server payload; it never submits the current editor's draft as a new send.
    const r = await actOnSuggestion(itemPath(current.id), s.version, action, confirmation && attempt ? undefined : text, undefined, current.manager, due);
    if (r.ok) {
      if (r.item?.id === current.id) reconcileSuggestionItem(current, r.item);
      toast(actedText(s, action), "ok");
      return true;
    }
    if (r.stale) {
      if (r.item) reconcileSuggestionItem(current, r.item);
      else void reloadInbox();
      toast(r.error, "info");
    } else if (s.kind === "handoff" && r.code === "handoff_target_unavailable") {
      if (r.item) reconcileSuggestionItem(current, r.item);
      toast(t("lib-inbox-events.handoff_unavailable"), "error");
    } else {
      if (confirmation) {
        set({ uncertainReplies: { ...state.uncertainReplies, [uncertainKey]: {
          version: s.version, status: "unknown", retryable: false, text: sentText, at: new Date().toISOString(), conversation: s.conversation,
        } } });
        if (!r.item || !reconcileSuggestionItem(current, r.item)) await readSuggestionItem(current);
      }
      if (r.code === "inbox_item_done") void reloadInbox();
      toast(t("lib-inbox.suggestion_failed", { error: r.error }), "error");
    }
    return false;
  } finally {
    set({ sending: state.sending.filter((x) => x !== key) });
  }
}

export function movingKey(item: InboxItem, row: DocRow, status: DocStatus) {
  return `${itemKey(item)}|${row.ref}|${status.field}`;
}

/** Moves a row's status in its document: shown at once, written by the hub, the item replaced by the hub's fresh
 * read; a failure puts the old value back. */
export async function setDocStatus(item: InboxItem, row: DocRow, status: DocStatus, value: string): Promise<void> {
  const key = movingKey(item, row, status);
  if (value === status.value || state.moving.includes(key)) return;
  const before = listed(item);
  set({ moving: [...state.moving, key] });
  patchItem(item, (it) => ({
    ...it,
    doc: it.doc && {
      ...it.doc,
      rows: it.doc.rows?.map((r) => (r.ref !== row.ref ? r : { ...r, status: r.status?.map((s) => (s.field === status.field ? { ...s, value } : s)) })),
    },
  }));
  try {
    const fresh = await api<InboxItem>(`${itemPath(item.id)}/doc/status`, { method: "POST", json: { ref: row.ref, field: status.field, value }, headers: itemRequest(item) });
    if (fresh?.id === item.id) replaceItem(fresh, item.manager);
    toast(t("lib-inbox.status_changed", { name: status.name, value }), "ok");
  } catch (e) {
    if (before) patchItem(item, () => before);
    toast(t("lib-inbox.status_failed", { error: errorText(e) }), "error");
  } finally {
    set({ moving: state.moving.filter((x) => x !== key) });
  }
}

// Selectors may build new arrays: a result shallow-equal to the last one is the last one (see store.ts).
function same(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) return true;
  if (Array.isArray(a) && Array.isArray(b)) return a.length === b.length && a.every((x, i) => Object.is(x, b[i]));
  return false;
}

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
};

export function useInbox<T>(select: (s: InboxState) => T): T {
  useEffect(start, []);
  const last = useRef<{ v: T } | null>(null);
  const read = () => {
    const v = select(state);
    if (last.current && same(last.current.v, v)) return last.current.v;
    last.current = { v };
    return v;
  };
  // the same read on a server render (a test's renderToStaticMarkup): the list as it is
  return useSyncExternalStore(subscribe, read, read);
}

/** How many unread open items the inbox has (the sidebar's badge). */
export function useInboxBadge(): number {
  // the current assistant's, with several (docs/assistant/managers.md "Clients")
  const scope = useScope();
  return useInbox((s) => badgeCount(scoped(s.items, scope)));
}

/** Tests only: back to an empty, unstarted list. */
export function resetInboxForTests() {
  state = EMPTY;
  historyNext.clear();
  groupNext.clear();
  routesAsked.clear();
  started = false;
  inFlight = null;
  again = false;
  lastList = null;
  deltasOff = 0;
  pendingDone.clear();
  listsOut.clear();
  listSeq = 0;
  unsentReplies.clear();
  droppedChanges.clear();
  pendingHandoffs.clear();
}
