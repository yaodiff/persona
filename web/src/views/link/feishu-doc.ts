// Reading a Feishu document for the panel (docs/extensions/feishu.md "Documents", the phone's FeishuDocReader / FeishuDocsModels):
// the Markdown export first (fast, faithful lists and tables); when the owner forbids export, the ProseMirror-style
// JSON tree. Bases, whiteboards and files say what they are and offer the tab. Reads are cached and can start early
// (a pointer resting on the link), so the panel usually opens on a finished page.
import { ApiError, api, enc, getToken } from "../../lib/api";
import { BASE } from "../../lib/base";
import { safeWebURL } from "../../lib/format";
import { safeApiPath } from "../../lib/safe-media-url";
import { docKind, DOC_LABEL, setLinkPrefetcher, type DocKind } from "../../lib/links";
import { itemHeaders, otherManager, withManager } from "../../lib/manager-choice";
import { t } from "../../i18n";

export interface DocNode {
  type: string;
  attrs?: Record<string, unknown>;
  content?: DocNode[];
  marks?: { type: string; attrs?: Record<string, unknown> }[];
  text?: string;
}

interface Authority {
  copy?: boolean;
}

export type DocContent =
  | { kind: "markdown"; title: string; markdown: string; copy: boolean; partial: boolean }
  | { kind: "tree"; title: string; doc: DocNode; copy: boolean; partial: boolean }
  | { kind: "unsupported"; title: string; doc: DocKind; message: string; image?: string; file?: string };

export interface DocInfo {
  owner?: string;
  updatedAt?: string;
  views?: number;
  comments?: number;
}

/** A failed read: the Lark gateway's `{"error":{"code"}}` or the hub's `{"error":"…"}`. */
export class DocError extends Error {
  status: number;
  code?: string;
  constructor(status: number, code: string | undefined, message: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

/** What to tell the user about a failed read (FeishuDocsFailure, plus the hub's own reasons). */
export function docErrorText(status: number, code?: string, raw?: string): string {
  switch (code) {
    case "DOCUMENT_EXPORT_FORBIDDEN":
      return t("link-feishu-doc.export_forbidden");
    case "DOCUMENT_FORMAT_UNSUPPORTED":
      return t("link-feishu-doc.format_unsupported");
    case "DOCUMENT_NOT_FOUND":
      return t("link-feishu-doc.doc_not_found");
    case "DOCUMENT_FORBIDDEN":
    case "DOCUMENT_PERMISSION_DENIED":
      return t("link-feishu-doc.doc_forbidden");
    case "DOCUMENT_TIMEOUT":
      return t("link-feishu-doc.timeout");
    case "DOCUMENT_INVALID_URL":
      return t("link-feishu-doc.invalid_url");
  }
  if (code?.startsWith("AUTH_")) return t("link-feishu-doc.lark_signed_out");
  const text = raw ?? "";
  if (/not configured|not enabled/i.test(text)) return t("link-feishu-doc.not_configured");
  switch (status) {
    case 0:
      return t("link-feishu-doc.hub_unreachable");
    case 401:
      return t("link-feishu-doc.session_expired");
    case 403:
      return t("link-feishu-doc.forbidden");
    case 404:
      return t("link-feishu-doc.not_found");
    case 502:
    case 503:
    case 504:
      // i18n-ignore: matches the hub / Lark gateway error text
      return /lark|飞书/i.test(text) ? t("link-feishu-doc.lark_unreachable") : t("link-feishu-doc.lark_unavailable");
  }
  return code || text || `HTTP ${status}`;
}

export function docErrorFromBody(status: number, body: Record<string, unknown>): DocError {
  const e = body.error;
  const obj = e && typeof e === "object" ? (e as { code?: unknown; message?: unknown }) : undefined;
  // The Lark gateway: {"error":{"code","message"}}; the hub's own errors: {"error":"…","code":<its generic code>}
  const code = typeof obj?.code === "string" ? obj.code : typeof e === "string" && /^[A-Z][A-Z_]+$/.test(e) ? e : undefined;
  const raw = typeof e === "string" ? e : typeof obj?.message === "string" ? obj.message : undefined;
  return new DocError(status, code, docErrorText(status, code, raw));
}

export type Get = (path: string) => Promise<Record<string, unknown>>;

// Through the hub's read-only Lark gateway window, as manager (the chat's the link was in; none: the default). The token
// is passed explicitly: a Lark gateway 401 (Feishu signed out) must not sign this browser out of the hub (api() does
// that on any 401 when it picks the token itself).
export function hubReader(manager?: string): Get {
  return async (path) => {
    try {
      return await api<Record<string, unknown>>(`/lark/raw/${path}`, { token: getToken(), headers: itemHeaders(manager) });
    } catch (e) {
      if (e instanceof ApiError) throw docErrorFromBody(e.status, e.body);
      throw new DocError(0, undefined, docErrorText(0));
    }
  };
}

const hubGet = hubReader();

function bool(v: unknown, fallback: boolean) {
  return typeof v === "boolean" ? v : fallback;
}

function fromTree(r: Record<string, unknown>, url: string, title?: string): DocContent {
  const kind = docKind(url);
  const name = (typeof r.title === "string" && r.title) || title || DOC_LABEL[kind];
  const copy = bool((r.authority as Authority | undefined)?.copy, true);
  if ("base" in r) return { kind: "unsupported", title: name, doc: "bitable", message: t("link-feishu-doc.bitable_unsupported") };
  const o = r.object as { kind?: string; token?: string; image?: string; empty?: boolean } | undefined;
  if (o?.kind === "file") return { kind: "unsupported", title: name, doc: "file", message: t("link-feishu-doc.drive_file"), file: o.token };
  if (o?.kind === "whiteboard") return { kind: "unsupported", title: name, doc: "whiteboard", message: o.empty ? t("link-feishu-doc.whiteboard_empty") : t("link-feishu-doc.whiteboard"), image: o.image };
  if (o) return { kind: "unsupported", title: name, doc: kind, message: t("link-feishu-doc.content_unsupported") };
  const doc = (r.content as DocNode | undefined) ?? { type: "doc" };
  if ("spreadsheet" in r && !(doc.content ?? []).length) return { kind: "unsupported", title: name, doc: "sheet", message: t("link-feishu-doc.sheet_forbidden") };
  // the panel draws the title itself: a first heading that repeats it goes
  const blocks = doc.content ?? [];
  const first = blocks[0];
  const body = first?.type === "heading" && plainText(first).trim() === name.trim() ? { ...doc, content: blocks.slice(1) } : doc;
  return { kind: "tree", title: name, doc: body, copy, partial: r.partial === true };
}

/** Markdown when the owner allows export, else the JSON tree; what neither can show becomes `unsupported`. */
export async function readDoc(url: string, title: string | undefined, get: Get = hubGet): Promise<DocContent> {
  const kind = docKind(url);
  if (kind === "bitable") return { kind: "unsupported", title: title || DOC_LABEL.bitable, doc: "bitable", message: t("link-feishu-doc.bitable_unsupported") };
  if (kind !== "file" && kind !== "whiteboard") {
    try {
      const r = await get(`documents/read?url=${enc(url)}&format=markdown`);
      if (typeof r.markdown === "string") {
        const name = (typeof r.title === "string" && r.title) || title || DOC_LABEL[kind];
        return { kind: "markdown", title: name, markdown: r.markdown, copy: bool((r.authority as Authority | undefined)?.copy, true), partial: r.partial === true };
      }
      // a Lark gateway (or the mock hub) that answers with the tree anyway: no second request
      if (r.content && typeof r.content === "object") return fromTree(r, url, title);
    } catch (e) {
      if (!(e instanceof DocError && (e.code === "DOCUMENT_EXPORT_FORBIDDEN" || e.code === "DOCUMENT_FORMAT_UNSUPPORTED" || e.status === 422))) throw e;
    }
  }
  try {
    return fromTree(await get(`documents/read?url=${enc(url)}&format=json`), url, title);
  } catch (e) {
    if (e instanceof DocError && (e.code === "DOCUMENT_FORMAT_UNSUPPORTED" || e.status === 422)) {
      return { kind: "unsupported", title: title || DOC_LABEL[kind], doc: kind, message: t("link-feishu-doc.content_unsupported") };
    }
    throw e;
  }
}

export async function readInfo(url: string, get: Get = hubGet): Promise<DocInfo> {
  const r = await get(`documents/reader/info?url=${enc(url)}`);
  const owner = r.owner as { name?: string } | undefined;
  const num = (v: unknown) => (typeof v === "number" ? v : undefined);
  return { owner: owner?.name || undefined, updatedAt: typeof r.updatedAt === "string" ? r.updatedAt : undefined, views: num(r.views), comments: num(r.comments) };
}

// ── Markdown export → what the panel renders ──

/** Stands for a <br> until the line-break plugin turns it into a real break (markdown here renders no raw HTML). */
export const BR = "\u2028";

/**
 * The Lark gateway's export as the panel shows it (FeishuDocsMarkdownExport.prepare):
 * - drops the leading `# Title` and `[Source](<url>)` lines (the panel draws its own title),
 * - points media at the hub (`http://<lark-gateway>/api/documents/…` → `/api/v1/lark/raw/documents/…`), with the token
 *   an <img> cannot send as a header,
 * - turns `<br>` (table cells, mostly) into breaks; code is left alone.
 */
const GATEWAY_API_RE = /^https?:\/\/[A-Za-z0-9.-]+(:\d+)?\/api\/(?!v1\/)/i;
const GATEWAY_API_URL_RE = /https?:\/\/[A-Za-z0-9.-]+(:\d+)?\/api\/(?!v1\/)[^)\s>]+/gi;

export function prepareMarkdown(md: string, title?: string, token = "", base = BASE, manager?: string): string {
  let lines = md
    .replace(GATEWAY_API_URL_RE, (match) => {
      const candidate = match.replace(GATEWAY_API_RE, "/api/v1/lark/raw/");
      return safeApiPath(candidate, "/api/v1/lark/raw/", base) ?? "";
    })
    .split("\n");
  const blank = (s?: string) => s !== undefined && !s.trim();
  while (blank(lines[0])) lines.shift();
  const first = lines[0] ?? "";
  if (first.startsWith("# ") && (!title || unescape(first.slice(2)).trim() === title.trim())) lines.shift();
  while (blank(lines[0])) lines.shift();
  if ((lines[0] ?? "").startsWith("[Source](")) lines.shift();
  while (blank(lines[0])) lines.shift();
  let fence = "";
  lines = lines.map((line) => {
    const t = line.trim();
    if (fence) {
      if (t.startsWith(fence)) fence = "";
      return line;
    }
    if (t.startsWith("```") || t.startsWith("~~~")) {
      fence = t.slice(0, 3);
      return line;
    }
    return line.replace(/<br\s*\/?>/gi, BR);
  });
  const withToken = (u: string) => (token ? `${u}${u.includes("?") ? "&" : "?"}token=${enc(token)}` : u);
  // under the sub-path the page is served at (lib/base.ts): images with the token, other links to the hub as they are;
  // both name the manager that read the document
  return lines
    .join("\n")
    .replace(/(!\[[^\]]*\]\(<?)(\/api\/v1\/[^)>\s]+)/g, (_, pre: string, u: string) => {
      const safe = safeApiPath(u, "/api/v1/", base);
      return safe ? pre + base + withToken(withManager(safe, manager)) : pre;
    })
    .replace(/((?<!!)\[[^\]]*\]\(<?)(\/api\/v1\/[^)>\s]+)/g, (_, pre: string, u: string) => {
      const safe = safeApiPath(u, "/api/v1/", base);
      return safe ? pre + base + withManager(safe, manager) : pre;
    });
}

/** Removes Markdown backslash escapes (`\-`, `\.`…) for plain display. */
export function unescape(s: string): string {
  return s.replace(/\\([\\`*_{}[\]()#+\-.!|<>~])/g, "$1");
}

// ── JSON tree helpers (FeishuDocsTree) ──

export function attr(n: DocNode, key: string): string | undefined {
  const v = n.attrs?.[key];
  return typeof v === "string" ? v : typeof v === "number" ? String(v) : undefined;
}

export function num(n: DocNode, key: string): number | undefined {
  const v = n.attrs?.[key];
  const x = typeof v === "number" ? v : typeof v === "string" ? Number(v) : NaN;
  return Number.isFinite(x) ? x : undefined;
}

/**
 * The number each ordered list shows. Every docx list item arrives as its own one-item `orderedList`; consecutive ones
 * keep counting unless one says `restart` (then it starts at its `start`).
 */
export function orderedNumbers(siblings: DocNode[]): (number | undefined)[] {
  const out: (number | undefined)[] = [];
  let next: number | undefined;
  for (const n of siblings) {
    if (n.type !== "orderedList") {
      out.push(undefined);
      next = undefined;
      continue;
    }
    const start = num(n, "start") ?? 1;
    const restart = n.attrs?.restart === true || n.attrs?.restart === "true";
    const value = restart || next === undefined ? start : next;
    out.push(value);
    next = value + Math.max(1, (n.content ?? []).filter((c) => c.type === "listItem").length);
  }
  return out;
}

/** All text below a node, mentions by their label. */
export function plainText(n: DocNode): string {
  if (n.text !== undefined) return n.text;
  if (n.type === "mention") return attr(n, "label") ?? "";
  if (n.type === "hardBreak") return "\n";
  if (n.type === "equation") return attr(n, "latex") ?? "";
  return (n.content ?? []).map(plainText).join("");
}

/** Media under the hub (`/api/v1/…`) needs the token on the URL, and the manager that read the document; a Lark gateway
 *  path is moved under the hub first. Unsafe schemes and traversing paths return "". */
export function mediaURL(src: string, manager?: string, token = getToken()): string {
  const trimmed = src.trim();
  if (!trimmed) return "";
  const rewritten = GATEWAY_API_RE.test(trimmed);
  const moved = rewritten ? trimmed.replace(GATEWAY_API_RE, "/api/v1/lark/raw/") : trimmed;
  if (moved.startsWith("/api/v1/")) {
    const safe = safeApiPath(moved, rewritten ? "/api/v1/lark/raw/" : "/api/v1/");
    if (!safe) return "";
    const u = withManager(safe, manager);
    return `${BASE}${u}${u.includes("?") ? "&" : "?"}token=${enc(token)}`;
  }
  return safeWebURL(moved) ?? "";
}

// ── cache ──

const MAX = 12;
const docs = new Map<string, Promise<DocContent>>();

// one url read as two managers is two documents (each reads it with its own Feishu account)
const docKey = (url: string, manager: string | undefined) => JSON.stringify([otherManager(manager) ?? "", url]);

/** The document as manager reads it, from memory when it was read (or started) before; `fresh` reads it again. */
export function loadDoc(url: string, title?: string, fresh = false, manager?: string): Promise<DocContent> {
  const key = docKey(url, manager);
  const have = docs.get(key);
  if (have && !fresh) {
    docs.delete(key);
    docs.set(key, have); // most recently used last
    return have;
  }
  const p = readDoc(url, title, hubReader(manager));
  docs.set(key, p);
  p.catch(() => docs.get(key) === p && docs.delete(key)); // a failure is not kept
  while (docs.size > MAX) docs.delete(docs.keys().next().value!);
  return p;
}

setLinkPrefetcher((url, manager) => {
  if (!docs.has(docKey(url, manager))) void loadDoc(url, undefined, false, manager).catch(() => {});
});

// ── AI summary and outline (FeishuDocReader's summaryCard and outline) ──

/** Feishu's cached AI summary of a document, when it has one ready; null otherwise (none, still writing, not allowed). */
export async function readSummary(url: string, get: Get = hubGet): Promise<string | null> {
  const r = await get(`documents/ai-summary?url=${enc(url)}`);
  const text = typeof r.summary === "string" ? r.summary.trim() : "";
  return r.state === "ready" && text ? text : null;
}

export interface OutlineItem {
  /** Index among the document's headings, in order (what the reader scrolls to). */
  index: number;
  text: string;
  /** 0 for the document's top level of headings, 1 under it… */
  depth: number;
}

/** The outline from the document's headings in order: blank ones left out, levels counted from the highest one used. */
export function docOutline(headings: { level: number; text: string }[]): OutlineItem[] {
  const shown = headings.map((h, index) => ({ index, text: h.text.replace(/\s+/g, " ").trim(), level: h.level })).filter((h) => h.text);
  const top = Math.min(...shown.map((h) => h.level));
  return shown.map((h) => ({ index: h.index, text: h.text, depth: Math.min(3, h.level - top) }));
}
