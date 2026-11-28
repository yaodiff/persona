// Comments on a Feishu document in the reader (#1290; the phone's FeishuDocsComments / FeishuDocsCommentsModel):
// the threads from the hub's read-only Lark gateway window, and, when the plugin says `lark_doc_comments`, the user's own
// writes from this browser: a comment on the whole document or on a passage, a reply to a thread, resolve and reopen.
// They are not the AI's, so no gate asks (as with replies in the inbox, #1203). Routes: docs/protocol/protocol.md "Lark API".
// No DOM here.

import { ApiError, api, enc, getToken, isNetworkFailure } from "./api";
import type { LarkPerson } from "./lark-people";
import { itemHeaders } from "./manager-choice";
import { t } from "../i18n";

export interface DocSegment {
  kind: string; // text | mention | link…
  text: string;
  href?: string;
}

export interface DocReply {
  id: string;
  author: { id?: string; name: string };
  content: DocSegment[];
  createdAt?: string;
  edited?: boolean;
  imageCount?: number;
}

export interface DocThread {
  id: string;
  /** A comment on the whole document; else it is on `quote`. */
  whole: boolean;
  resolved: boolean;
  quote: string;
  anchorBlockId?: string;
  createdAt?: string;
  replies: DocReply[];
}

/** Where "Send" goes: a new comment on the whole document, a reply to a thread, or a new comment on a passage. */
export type CommentTarget =
  | { kind: "document" }
  | { kind: "thread"; id: string; name: string; text: string }
  | { kind: "passage"; quote: string; blockId?: string };

export const ON_DOCUMENT: CommentTarget = { kind: "document" };

export function replyText(r: DocReply): string {
  return r.content.map((s) => s.text).join("");
}

function str(v: unknown): string | undefined {
  return typeof v === "string" ? v : undefined;
}

/** `documents/comments` as the plugin gives it; what does not look like a thread is left out. */
export function parseThreads(raw: unknown): DocThread[] {
  const list = (raw as { threads?: unknown } | null)?.threads;
  if (!Array.isArray(list)) return [];
  const out: DocThread[] = [];
  for (const th of list as Record<string, unknown>[]) {
    if (!th || typeof th !== "object" || typeof th.id !== "string") continue;
    const replies: DocReply[] = [];
    for (const r of Array.isArray(th.replies) ? (th.replies as Record<string, unknown>[]) : []) {
      if (!r || typeof r !== "object" || typeof r.id !== "string") continue;
      const a = (r.author ?? {}) as Record<string, unknown>;
      const content = (Array.isArray(r.content) ? (r.content as Record<string, unknown>[]) : [])
        .filter((s) => s && typeof s.text === "string")
        .map((s) => ({ kind: str(s.kind) ?? "text", text: s.text as string, href: str(s.href) }));
      replies.push({ id: r.id, author: { id: str(a.id), name: str(a.name) || t("lib-doc-comments.lark_user") }, content, createdAt: str(r.createdAt), edited: r.edited === true, imageCount: typeof r.imageCount === "number" ? r.imageCount : undefined });
    }
    out.push({ id: th.id, whole: th.whole === true, resolved: th.resolved === true, quote: str(th.quote) ?? "", anchorBlockId: str(th.anchorBlockId), createdAt: str(th.createdAt), replies });
  }
  return out;
}

function lastActivity(t: DocThread): number {
  let best = Number.NEGATIVE_INFINITY;
  for (const r of t.replies) {
    const x = Date.parse(r.createdAt ?? "");
    if (Number.isFinite(x) && x > best) best = x;
  }
  if (best > Number.NEGATIVE_INFINITY) return best;
  const c = Date.parse(t.createdAt ?? "");
  return Number.isFinite(c) ? c : Number.NEGATIVE_INFINITY;
}

/** Open threads first, newest activity first; resolved ones after (feishuDocsSortedThreads). */
export function sortThreads(threads: DocThread[]): DocThread[] {
  return [...threads].sort((a, b) => (a.resolved !== b.resolved ? (a.resolved ? 1 : -1) : lastActivity(b) - lastActivity(a)));
}

/** What makes a retry the same send: the same words to the same place of the same document keep their `client_id`. */
export function sendKey(url: string, target: CommentTarget, text: string): string {
  return JSON.stringify([url, target, text]);
}

/** The `client_id` for a send: the pending one when it is a retry of the same send, else a new one. */
export function clientIdFor(pending: { key: string; id: string } | null, key: string, fresh: () => string = newId): { key: string; id: string } {
  return pending?.key === key ? pending : { key, id: fresh() };
}

function newId(): string {
  return globalThis.crypto?.randomUUID?.() ?? `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

/** The hub's words for a failed write (the plugin's, such as "the Lark gateway is not signed in to Lark"…), else what went wrong. */
export function writeErrorText(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === "comment_unsupported") return t("lib-doc-comments.comment_unsupported");
    return e.message;
  }
  if (isNetworkFailure(e)) return t("lib-api.cant_reach_hub");
  return e instanceof Error ? e.message : String(e);
}

// ── the routes ──
// The token is passed explicitly: a Lark gateway 401 (Feishu signed out) must not sign this browser out of the hub.
// `manager`: the one the reader reads the document as (none: the default).

const opts = (manager: string | undefined, json?: unknown) => ({ token: getToken(), headers: itemHeaders(manager), ...(json === undefined ? {} : { method: "POST", json }) });

/** The threads; `null` when the write's answer could not read them back (the write itself went through). */
function threadsOf(r: { threads?: unknown }): DocThread[] | null {
  return Array.isArray(r.threads) ? sortThreads(parseThreads(r)) : null;
}

export async function readComments(url: string, manager?: string): Promise<DocThread[]> {
  return sortThreads(parseThreads(await api(`/lark/raw/documents/comments?url=${enc(url)}`, opts(manager))));
}

/** Writes `text` where `target` says; the threads as Feishu has them afterwards (or null, see threadsOf). */
export async function sendComment(url: string, target: CommentTarget, text: string, mentions: LarkPerson[], clientId: string, manager?: string): Promise<DocThread[] | null> {
  const body: Record<string, unknown> = { url, content: text, mentions: mentions.map((m) => ({ user_id: m.user_id, name: m.name })), client_id: clientId };
  if (target.kind === "thread") return threadsOf(await api(`/lark/documents/comments/${enc(target.id)}/replies`, opts(manager, body)));
  if (target.kind === "passage") {
    body.quote = target.quote;
    if (target.blockId) body.block_id = target.blockId;
  }
  return threadsOf(await api("/lark/documents/comments", opts(manager, body)));
}

export async function setResolved(url: string, thread: string, resolved: boolean, manager?: string): Promise<DocThread[] | null> {
  return threadsOf(await api(`/lark/documents/comments/${enc(thread)}/resolve`, opts(manager, { url, resolved })));
}

/** People to @ in a document's comment, by name (no chat to take members from). */
export async function findDocPeople(url: string, q: string, signal?: AbortSignal, manager?: string): Promise<LarkPerson[]> {
  const r = await api<{ members?: LarkPerson[] }>(`/lark/documents/people?url=${enc(url)}&q=${enc(q)}`, { ...opts(manager), signal });
  return (r.members ?? []).filter((m) => m && typeof m.user_id === "string" && typeof m.name === "string");
}
