// The Inbox page's own helpers (docs/assistant/inbox.md): the words for mutes, rules, bulk actions and the triage banner,
// the "Hand to AI" message, Feishu message parts, cards and sends. Split from inbox.ts, which the sidebar's badge and the chat
// load on every first screen, so that only the lazily loaded Inbox page and its settings carry them (#8806).

import { itemHeaders, stampedHeaders } from "./manager-choice";
import { t } from "../i18n";
import { api, enc, getToken } from "./api";
import { inboxConversationPage, spacePage } from "./conversation-page";
import { openFromInbox } from "./inbox-nav";
import type { ContextRef } from "./links";
import { larkPath } from "./lark-accounts";
import { mailReading } from "./mail";
import { baseName, uuid } from "./format";
import type { Route } from "./router";
import { getSession, getState } from "./store";
import type { Interaction, Session } from "./types";
import { type DocChange, type InboxDoc, type InboxItem, type InboxMute, type InboxMuteOption, type InboxParent, type InboxParentKind, type InboxPolicy, type InboxTree, type InboxTriage, type LarkMention, type LarkMessage, type LarkPart, type MomentContent, type PolicyTier, type PolicyTwin, connectorReference, contextXML, docTypeName, isLark, itemPath, itemRequest, itemRoute, sourceMailbox } from "./inbox";

/** The menu's words for a mute option (iOS MuteOption.title). */
export function muteTitle(o: Pick<InboxMuteOption, "tag">): string {
  return t(o.tag === "repo" ? "lib-inbox-page.mute_repo" : o.tag === "chat" ? "lib-inbox-page.mute_chat" : "lib-inbox-page.mute_sender");
}

/** The name under the menu's words, on one line: a repository without its owner (example-repo). */
export function muteShortName(o: InboxMuteOption): string {
  return o.tag === "repo" ? o.name.split("/").pop() || o.name : o.name;
}

/** What kind of thing a mute is, for people: "Repository" / "Chat" / "Sender". */
export function muteTagName(tag: string): string {
  return t(tag === "repo" ? "lib-inbox-page.mute_tag_repo" : tag === "chat" ? "lib-inbox-page.mute_tag_chat" : "lib-inbox-page.mute_tag_sender");
}

/** The "Muted" list's second line: the source's name, then "Repository" / "Chat" / "Sender". */
export function muteLine(m: InboxMute): string {
  return [m.source_name || m.source, muteTagName(m.tag)].join(" · ");
}

/** The mutes, newest first (iOS InboxMutesView). */
export function sortMutes(list: InboxMute[]): InboxMute[] {
  // By time, not by string: RFC3339Nano drops trailing zeros, so "…:05Z" and "…:05.1Z" compare the wrong way.
  const ts = (m: InboxMute) => (m.created_at ? Date.parse(m.created_at) || 0 : 0);
  return [...list].sort((a, b) => ts(b) - ts(a));
}

/** `manager`: whose inbox the node is in (an item's, docs/assistant/managers.md "Clients"); none: the default's. */
export function fetchInboxTree(kind: InboxParentKind, id: string, manager?: string): Promise<InboxTree> {
  const path = kind === "session" ? id.split("/").map(enc).join("/") : enc(id).replace(/%3A/gi, ":");
  return api<InboxTree>(`/inbox/tree/${kind}/${path}`, { headers: itemHeaders(manager) });
}

/** Where a node of a line opens; `manager`: whose line it is (its items are that manager's too). */
export function parentRoute(p: InboxParent, manager?: string): Route | undefined {
  if (p.kind === "inbox") return itemRoute({ id: p.id, manager });
  if (p.kind === "todo") return { name: "todos", id: p.id };
  if (p.kind === "agent") {
    const colon = p.id.indexOf(":");
    return colon > 0 && colon < p.id.length - 1 ? { name: "agents", id: p.id } : undefined;
  }
  const i = p.id.indexOf("/");
  return i > 0 && i < p.id.length - 1 ? { name: "session", mid: p.id.slice(0, i), sid: p.id.slice(i + 1) } : undefined;
}

/** Inbox links can identify an Assistant conversation even when its session is missing from the store. */
export function inboxSessionRoute(mid: string, sid: string, manager?: string, assumeAssistant = true): Route {
  const row = getSession(mid, sid);
  const { assistant } = getState();
  const own = assistant?.conversations.some((c) => c.machine_id === mid && c.session_id === sid);
  if (!row?.assistant && !own && (row || !assumeAssistant)) return { name: "session", mid, sid };
  if (row?.space) return spacePage(row.space, sid);
  return inboxConversationPage(sid, mid, row?.manager ?? manager, assistant?.machine_id);
}

export function openInboxConversation(mid: string, sid: string, manager?: string, assumeAssistant = true): void {
  openFromInbox(inboxSessionRoute(mid, sid, manager, assumeAssistant));
}

/** Whether "AI draft reply" applies: a connector's thread only when the hub can answer it (a CI run cannot). */
export function canDraft(it: InboxItem): boolean {
  return !it.connector || !!it.connector.can_reply;
}

/** The row's text under the summary says what the summary already says (the assistant's own rows: its text is the
 *  summary, or its first words cut with an ellipsis when it is long), so it is not drawn twice. */
export function repeatsSummary(it: InboxItem): boolean {
  const summary = it.summary?.trim();
  const text = it.preview?.trim();
  if (it.source !== "assistant" || !summary || !text) return false;
  const head = text.endsWith("…") ? text.slice(0, -1) : text;
  return text.startsWith(summary) || (!!head && summary.startsWith(head));
}

/** Where it came from, for the detail page's subtitle. */
export function sourceLine(it: InboxItem): string {
  if (it.doc) return [docTypeName(it.doc.type), it.preview].filter(Boolean).join(" · ");
  if (it.moment) return it.moment.my_post ? t("lib-inbox-page.moments_my_post") : t("lib-inbox.moments");
  if (it.connector) return [it.account_name || it.source, connectorReference(it)].filter(Boolean).join(" · ");
  // Lark · <name> while the hub reads more than one Feishu account
  // i18n-ignore: the hub names its Lark accounts "Lark · <name>" in Chinese
  if (isLark(it)) return [it.account_name?.startsWith("飞书 · ") ? it.account_name : t("lib-inbox.lark"), it.lark?.is_group ? t("lib-inbox.kind_group") : it.kind === "bot" ? t("lib-inbox.kind_bot") : t("lib-inbox.kind_dm")].join(" · ");
  const from = it.mail?.from;
  return [it.account_name, sourceMailbox(it), from?.name && from.email ? `${from.name} <${from.email}>` : from?.email || it.sender].filter(Boolean).join(" · ");
}

/** The AI twin's proposed status move as a line ("Status: In progress → Done · write the doc", in Chinese). */
export function docChangeLine(change: DocChange, doc?: InboxDoc): string {
  const row = doc?.rows?.find((r) => r.ref === change.ref);
  const st = row?.status?.find((s) => s.field === change.field);
  if (!row || !st) return change.value;
  return t("lib-inbox-page.doc_change", { field: st.name, from: st.value || t("lib-inbox-page.not_set"), to: change.value, row: row.title });
}

/** The part of an address before the @ (chips stay short), unless that alone says nothing ("c@…"). */
export function mailShort(email: string): string {
  const name = email.split("@")[0] ?? email;
  return name.length >= 3 ? name : email;
}

export function interactionAgentTitle(ix: Interaction): string {
  return ix.session_title?.trim() || baseName(ix.cwd) || "Agent";
}

// ── Hand to AI ───────────────────────────────────────────

export const HANDOFF_PRESETS = [t("lib-inbox-page.handoff_summarize"), t("lib-inbox-page.handoff_research"), t("lib-inbox-page.handoff_handle")];

/** The item as context the chat shows as a chip, then what to do with it (InboxHandOffSheet.message). */
export function handOffMessage(it: InboxItem, ask: string): string {
  const ref = handOffContext(it);
  return `${contextXML(ref.kind, ref.title, ref.url, ref.text)}\n\n${ask.trim() || HANDOFF_PRESETS[0]}`;
}

/** The item as a `<persona-context kind="context">` (InboxHandOffSheet.contextRef): where it came from, its words, and how
 *  the agent reads more or answers. "New Agent…" (#5933) puts it over New agent's first message as its card. */
export function handOffContext(it: InboxItem): ContextRef {
  const title = `${it.connector ? it.account_name || it.source : isLark(it) ? t("lib-inbox.lark") : t("lib-inbox.kind_mail")} · ${it.title}`;
  let text = it.context?.trim() ? it.context : [it.sender ?? "", it.preview].filter(Boolean).join(t("lib-inbox.sender_separator"));
  if (it.lark) {
    const ol = it.lark.ol_account;
    const account = ol ? t("lib-inbox-page.handoff_lark_account", { account: ol }) : "";
    text += `\n\n${t("lib-inbox-page.handoff_lark", { feed: it.lark.feed_id, account })}`;
  }
  if (it.connector?.url) text += `\n\n${t("lib-inbox-page.handoff_connector", { name: it.account_name || it.source, url: it.connector.url })}`;
  const uid = it.mail?.uids[it.mail.uids.length - 1];
  // a hub with mail off has no mail_read / mail_send to point the agent at (lib/mail.ts mailReading)
  if (it.mail && uid !== undefined && mailReading().kind === "full") text += `\n\n${t("lib-inbox-page.handoff_mail", { account: it.mail.account_id, mailbox: it.mail.mailbox || "INBOX", uid })}`;
  return { id: `inbox:${it.id}`, kind: "context", title, url: "", text };
}

/** Best-effort record of a hand-off the client already delivered itself; no text means the manager must not send it again.
 *  Old managers reject a record-only request (400, or 404 / 405 without the route). Every failure is quiet: delivery
 *  already succeeded, so recording never interrupts the close and navigation that follow it. */
export async function recordHandOff(item: InboxItem, session: Pick<Session, "machine_id" | "session_id">, kind: "agent" | "conversation", title?: string): Promise<void> {
  try {
    await api<void>(`${itemPath(item.id)}/handoff`, {
      method: "POST",
      json: {
        session: { machine_id: session.machine_id, session_id: session.session_id },
        kind,
        title: title ?? "",
        client_id: uuid(),
      },
      headers: itemRequest(item),
    });
  } catch {
    // A missing record is not a failed hand-off; do not toast, retry or change the dialog.
  }
}

/** Inline parts flow in one paragraph; the others (pictures, files, code, cards, system lines) stand alone. */
export function isInlinePart(p: LarkPart): boolean {
  switch (p.kind) {
    case "text":
      return !p.metadata?.markdown && !p.metadata?.system;
    case "at":
    case "link":
    case "docs":
    case "table":
      return true;
    case "emotion":
      return !p.url;
    default:
      return false;
  }
}

/** Parts in runs: consecutive inline ones together, each block part on its own. */
export function partRuns(parts: LarkPart[]): { inline: boolean; parts: LarkPart[] }[] {
  const out: { inline: boolean; parts: LarkPart[] }[] = [];
  for (const p of parts) {
    const inline = isInlinePart(p);
    const last = out[out.length - 1];
    if (inline && last?.inline) last.parts.push(p);
    else out.push({ inline, parts: [p] });
  }
  return out;
}

/** Bracketed-name-style text (like `[Smile]` in Chinese) for an emoji key without its picture. */
export function emojiLabel(key: string, text?: string): string {
  if (text) return text;
  let k = key.replace(/^Lark_Emoji_/, "");
  const us = k.lastIndexOf("_");
  if (us > 0) k = k.slice(0, us);
  return `[${k}]`;
}

/** A post / comment's text, emoji as `[key]`. */
export function momentText(c?: MomentContent): string {
  if (!c?.segments?.length) return c?.text ?? "";
  return c.segments.map((s) => (s.kind === "emoji" ? emojiLabel(s.key ?? s.emojiKey ?? "", s.text) : s.text ?? "")).join("");
}

/** The model settings "Set up" opens: inbox triage, the purpose that sorts the inbox. */
export const TRIAGE_SETTINGS = "/settings?tab=model-config&purpose=inbox.triage";

/**
 * The quiet notice over the list naming the sources the hub could not read this round (#1849): one line per source with a
 * `read_error`, "name: the hub's read_reason" (the hub's own fallback words when it gives none), so a list that is behind
 * does not look complete. Empty when every source was read, or from an older hub. Apart from the triage banner: a source
 * can be unread while triage is ok and nothing waits. The same lines as Android (InboxTriageText.unread).
 */
export function triageUnread(tr: InboxTriage | null | undefined): string[] {
  if (!tr || typeof tr !== "object" || !Array.isArray(tr.sources)) return [];
  return tr.sources
    .filter((x) => x && typeof x.read_error === "string" && x.read_error.trim())
    .map((x) => t("lib-inbox-page.read_failed_line", {
      name: (x.name ?? "").trim() || x.source || "",
      reason: (x.read_reason ?? "").trim() || t("lib-inbox-page.read_failed"),
    }));
}

/** What the user muted, in every source (GET /api/v1/inbox/mutes), newest first. `manager`: whose mutes (each manager
 *  keeps its own; named even when it is the default, which may move before an Unmute; stamped on each); none: the
 *  default's, nothing named. */
export async function listInboxMutes(manager?: string): Promise<InboxMute[]> {
  const r = await api<{ mutes?: InboxMute[] }>("/inbox/mutes", { headers: stampedHeaders(manager) });
  return sortMutes((r.mutes ?? []).map((m) => (manager ? { ...m, manager } : m)));
}

/** "Unmute" (`mute`'s manager's, named even when it is the default now): only what comes in later comes back. */
export function unmuteInbox(mute: Pick<InboxMute, "id" | "manager">): Promise<unknown> {
  return api(`/inbox/mutes/${enc(mute.id)}`, { method: "DELETE", headers: stampedHeaders(mute.manager) });
}

/** The tags a policy can name, the most specific first: a sender's policy beats its chat's (the hub's rule). */
const POLICY_TAGS = ["sender", "chat", "repo"];

/** The policy that applies to an item, as the hub picks it: the same source, one of its mute options, sender before chat before repository. */
export function policyFor(it: InboxItem, policies: InboxPolicy[]): InboxPolicy | undefined {
  let best: InboxPolicy | undefined;
  let rank = POLICY_TAGS.length;
  for (const o of it.mutes ?? []) {
    const r = POLICY_TAGS.indexOf(o.tag);
    if (r < 0 || r >= rank) continue;
    const p = policies.find((x) => x.source === it.source && x.tag === o.tag && x.value === o.value);
    if (p) [best, rank] = [p, r];
  }
  return best;
}

/** The menu's words for setting a policy on an option: "Rule for this repository / chat / sender…". */
export function policyMenuTitle(o: Pick<InboxMuteOption, "tag">): string {
  return t(o.tag === "repo" ? "lib-inbox-page.policy_repo" : o.tag === "chat" ? "lib-inbox-page.policy_chat" : "lib-inbox-page.policy_sender");
}

/** A policy tier's words: "Always Needs you" / "FYI" / "Collapsed"; "" is "Leave to triage". */
export function policyTierName(tier: PolicyTier): string {
  return t(tier === "action" ? "lib-inbox-page.policy_tier_action" : tier === "fyi" ? "lib-inbox-page.policy_tier_fyi" : tier === "noise" ? "lib-inbox-page.policy_tier_noise" : "lib-inbox-page.policy_tier_none");
}

/** A policy twin's words: "AI twin first" / "AI twin never"; "" is "As set". */
export function policyTwinName(twin: PolicyTwin): string {
  return t(twin === "first" ? "lib-inbox-page.policy_twin_first" : twin === "never" ? "lib-inbox-page.policy_twin_never" : "lib-inbox-page.policy_twin_none");
}

/** What a policy does, for people: "Always Needs you · AI twin first", one part when it sets one. */
export function policySummary(p: Pick<InboxPolicy, "tier" | "twin">): string {
  const parts: string[] = [];
  if (p.tier) parts.push(policyTierName(p.tier));
  if (p.twin) parts.push(policyTwinName(p.twin));
  return parts.join(" · ");
}

/** The policies, newest first. `manager`: whose rules (named even when it is the default, which may move before a
 *  Remove; stamped on each); none: the default's, nothing named. */
export async function listInboxPolicies(manager?: string): Promise<InboxPolicy[]> {
  const r = await api<{ policies?: InboxPolicy[] }>("/inbox/policies", { headers: stampedHeaders(manager) });
  const ts = (p: InboxPolicy) => (p.created_at ? Date.parse(p.created_at) || 0 : 0);
  return [...(r.policies ?? [])].map((p) => (manager ? { ...p, manager } : p)).sort((a, b) => ts(b) - ts(a));
}

/** Remove a policy (`policy`'s manager's, named even when it is the default now): items keep the tier it gave until new
 *  content is triaged again. */
export function removeInboxPolicy(policy: Pick<InboxPolicy, "id" | "manager">): Promise<unknown> {
  return api(`/inbox/policies/${enc(policy.id)}`, { method: "DELETE", headers: stampedHeaders(policy.manager) });
}

/** From this many items on, Clean up all asks first ("Will clean up N items"); fewer go at once, with Undo on the toast. */
export const BULK_CONFIRM_AT = 10;

/** What Clean up all covers, for its label: the filters' names and the search, or All. */
export function bulkScope(filters: string[], query: string, count: number): string {
  return count ? t("lib-inbox-page.scope_count", { scope: bulkScopeName(filters, query), count }) : t("lib-inbox-page.scope_none");
}

/** The filters' names and the search, or "All": what a bulk action covers, without the count. */
export function bulkScopeName(filters: string[], query: string): string {
  const parts = filters.filter(Boolean);
  if (query.trim()) parts.push(t("lib-inbox-page.scope_search", { query: query.trim() }));
  return parts.length ? parts.join(" · ") : t("lib-inbox-page.scope_all");
}

const mentionList = (mentions?: LarkMention[]) => (mentions?.length ? mentions.map((m) => ({ user_id: m.user_id, name: m.name })) : undefined);

/** Sends a message to a Feishu conversation via the native hub API, as account `ol` (undefined: the hub's own). */
export async function sendLarkMessage(feed: string, text: string, clientID?: string, ol?: number, mentions?: LarkMention[], manager?: string): Promise<{ message_id?: string }> {
  return api<{ message_id?: string }>(larkPath(`/lark/conversations/${enc(feed)}/messages`, ol, manager), {
    method: "POST",
    json: { text, client_id: clientID, mentions: mentionList(mentions) },
    token: getToken(),
  });
}

/** Replies to a Feishu message (either quoted in chat or in its topic thread). */
export async function replyLarkMessage(
  messageID: string,
  text: string,
  thread = false,
  clientID?: string,
  ol?: number,
  mentions?: LarkMention[],
  manager?: string,
): Promise<{ message_id?: string }> {
  return api<{ message_id?: string }>(larkPath(`/lark/messages/${enc(messageID)}/replies`, ol, manager), {
    method: "POST",
    json: { text, thread, client_id: clientID, mentions: mentionList(mentions) },
    token: getToken(),
  });
}

/** Computes the next position string for an optimistic LarkMessage in the list. */
export function nextLarkPosition(messages: LarkMessage[]): string {
  let max = 0;
  for (const m of messages) {
    const n = Number(m.position);
    if (Number.isFinite(n) && n > max) max = n;
  }
  return String(max > 0 ? max + 1 : Date.now());
}

// ── Feishu cards ─────────────────────────────────────────

const CARD_SKIP = new Set(["button", "action", "overflow", "select_static", "select_person", "date_picker", "picker_time", "picker_datetime", "hr", "img"]);

/** A card simplified to its title and lines of text (no buttons: the page only reads). */
export function cardText(meta: Record<string, unknown> | undefined): { title: string; lines: string[] } {
  if (!meta) return { title: "", lines: [] };
  const str = (v: unknown): string => (typeof v === "string" ? v : v && typeof v === "object" && typeof (v as { content?: unknown }).content === "string" ? (v as { content: string }).content : "");
  const header = meta.header as Record<string, unknown> | undefined;
  const title = str(meta.title) || str(header?.title) || "";
  const lines: string[] = [];
  const walk = (node: unknown) => {
    if (Array.isArray(node)) return node.forEach(walk);
    if (!node || typeof node !== "object") return;
    const o = node as Record<string, unknown>;
    if (typeof o.tag === "string" && CARD_SKIP.has(o.tag)) return;
    const own = str(o.text) || (typeof o.content === "string" ? o.content : "");
    if (own.trim()) lines.push(own.trim());
    for (const [k, v] of Object.entries(o)) if (k !== "text" && k !== "content" && v && typeof v === "object") walk(v);
  };
  walk(meta.elements ?? (meta.body as Record<string, unknown> | undefined)?.elements ?? meta.body);
  return { title, lines: lines.filter((l, i) => l !== title && l !== lines[i - 1]) };
}
