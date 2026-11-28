import { conversationKeyDeleted, onConversationDeleted } from "./conversation-deleted";
import { APP_SCHEMES, assistantPath } from "./compatibility";
// Where a link opens (CLAUDE.md "Links open inside the app", docs/web/web.md "Links"). In a browser the browser is the app:
// ordinary pages open in a new tab (most sites refuse to be framed and the page cannot tell it was refused, so a
// framed panel would just be blank), Feishu documents and Moments posts open in the side panel's reader (the same links
// the phone reads natively, FeishuLinks.route) when the hub reads Feishu (feature `lark`; otherwise they are ordinary
// pages), and persona://session links move to that conversation. This hub's own agent and task pages (/s/<machine>/
// <session>, /todos?id=…, the links the assistant writes) open where the browser's setting says (lib/agent-open.ts,
// #5424): in the side panel next to where they were clicked, the default, or as pages of their own (#5096); on a narrow
// screen, which has no room beside the page, as pages either way. Its conversations (/persona/<id>) are pages. The
// hub's own pages are those of this page's origin and of every address the hub advertises (hello's endpoints, its
// public_url first: the address the assistant writes, which may not be the one this page was opened at).
//
// Also the hand-off between the reader and the composers: text picked in the panel goes to the composer the user is
// writing in, as <persona-context> tags in the next message (docs/protocol/protocol.md "Tags in messages").
import { t } from "../i18n";
import { useSyncExternalStore } from "react";
import { safeWebURL, uuid } from "./format";
import { openPanel } from "./panel";
import { agentOpenMode } from "./agent-open";
import { openLinkPage, taskRoute } from "./handoff";
import { navigate, parse as parseRoute } from "./router";
import { BASE, stripBase } from "./base";
import { contextXML } from "./context-xml";
import { otherManager } from "./manager-choice";
import { localhostLink, PREVIEW_FEATURE } from "./preview-link";
import type { PortLink } from "./port-links";
import { getSession, getState, hubHas, larkOn, onHubEvent } from "./store";
import { mediaMatches, NARROW } from "./viewport";

/** An Agent's or a task's link opens in the side panel: Agents open there (lib/agent-open.ts) and there is room. */
const beside = () => agentOpenMode() === "panel" && !mediaMatches(NARROW);

/** What a Feishu document link points at, from the link alone (FeishuDocsKind.from). A wiki link does not say. */
export type DocKind = "doc" | "sheet" | "bitable" | "mindnote" | "file" | "slides" | "wiki" | "page" | "whiteboard" | "other";

export type LinkRoute =
  | { kind: "doc"; doc: DocKind } // a Feishu cloud document: the panel's reader
  | { kind: "moments"; id: string } // a Moments post: the panel
  | { kind: "session"; mid: string; sid: string } // persona://session/<machine>/<session>: that conversation
  | { kind: "agent"; mid: string; sid: string } // this hub's /s/<machine>/<session>: that agent's page
  | { kind: "task"; id: string } // this hub's /todos?id=<todo>: that task's Agent's page, or its to-do's while it has none
  | { kind: "conversation"; sid: string; mid?: string; manager?: string } // this hub's /persona/<id>: that assistant conversation, in place (with the computer and manager its address names)
  | { kind: "page"; path: string } // any other page of this hub's app (the Inbox, settings…, or one message: ?at=): in place (docs/protocol/deep-links.md)
  | { kind: "app"; url: string } // an app link to a page (persona://open<path>, persona://task|kb|settings|extension/…): read when opened (lib/deep-links.ts)
  | { kind: "web" } // any other http(s) page: a new tab
  | { kind: "system" } // mailto: / tel:: the browser hands it to the system
  | { kind: "none" }; // anything else (javascript:, other apps' schemes, not a URL): not followed

const FEISHU_HOSTS = ["larkoffice.com", "feishu.cn", "larksuite.com"];
// First path segments the reader opens (FeishuLinks.isDoc).
const DOC_PATHS = ["docx", "wiki", "docs", "sheets", "base", "mindnotes", "file", "slides"];

function parse(url: string, base?: string | URL): URL | null {
  try {
    return new URL(url, base);
  } catch {
    return null;
  }
}

function isFeishuHost(host: string) {
  const h = host.toLowerCase();
  return FEISHU_HOSTS.some((d) => h === d || h.endsWith(`.${d}`));
}

// First path segments whose kind is another word; any other kind is its own segment (DOC_LABEL's keys).
const DOC_SEGMENT: Partial<Record<string, DocKind>> = { docx: "doc", docs: "doc", sheets: "sheet", base: "bitable", mindnotes: "mindnote", board: "whiteboard" };

export function docKind(url: string): DocKind {
  const first = (parse(url)?.pathname ?? "").split("/").filter(Boolean)[0] ?? "";
  return Object.hasOwn(DOC_SEGMENT, first) ? DOC_SEGMENT[first]! : Object.hasOwn(DOC_LABEL, first) ? (first as DocKind) : "other";
}

export const DOC_LABEL: Record<DocKind, string> = {
  doc: t("lib-links.doc"),
  sheet: t("lib-links.sheet"),
  bitable: t("lib-links.bitable"),
  mindnote: t("lib-links.mindnote"),
  file: t("lib-links.file"),
  slides: t("lib-links.slides"),
  wiki: t("lib-links.wiki"),
  page: t("lib-links.page"),
  whiteboard: t("lib-links.whiteboard"),
  other: t("lib-links.doc"),
};

/** A Moments share link's post id (LarkMoments.postID). */
function momentsPostID(u: URL): string | null {
  if (!u.pathname.endsWith("/moments/detail")) return null;
  const id = u.searchParams.get("postId") ?? "";
  return /^\d+$/.test(id) ? id : null;
}

// The origins of the addresses the hub advertises (hello's endpoints), kept from its last hello.
let advertised: string[] = [];
onHubEvent("hello", (msg) => {
  advertised = hubOrigins(msg.endpoints);
});

/** The origins of a hello's `endpoints` ({url}[]); anything else in the list is skipped. */
export function hubOrigins(endpoints: unknown): string[] {
  if (!Array.isArray(endpoints)) return [];
  return endpoints.flatMap((e) => {
    const u = typeof e === "object" && e && typeof (e as { url?: unknown }).url === "string" ? parse((e as { url: string }).url) : null;
    return u && (u.protocol === "http:" || u.protocol === "https:") ? [u.origin] : [];
  });
}

const pageOrigin = () => (typeof location !== "undefined" ? [location.origin, ...advertised] : advertised);

/** Whether an image URL is this page's or an advertised hub's own (its attachments and extension images), so it loads
 *  inline. Relative and non-http(s) URLs are the hub's too; every other host stays behind the tap-to-load gate. */
export function isOwnImageURL(src: string): boolean {
  // Resolve protocol-relative URLs (//host/x) against the page origin: without a base they fail to parse and would be
  // mistaken for the hub's own, letting a third-party image load past the gate (#8162).
  const u = parse(src.trim(), typeof location !== "undefined" ? location.href : undefined);
  if (!u || (u.protocol !== "http:" && u.protocol !== "https:")) return true;
  return pageOrigin().includes(u.origin);
}

/** A page of the hub this page is served by (router.ts's paths, under the sub-path the page is served at; the route table,
 * docs/protocol/deep-links.md), or null. An agent (/s/…), a task (/todos…) and a conversation (/persona/…) open as they
 * always did; one message of them (?at=) and any other page of this app (the Inbox, settings, knowledge…) in place. An
 * address that is no page of it (/admin, /share/…), and a link with a user name or password, stays a web page. */
function hubPage(u: URL, origin: string | string[], base: string): LinkRoute | null {
  const origins = (Array.isArray(origin) ? origin : [origin]).filter(Boolean);
  if (!origins.includes(u.origin) || u.username || u.password) return null;
  if (base && u.pathname !== base && !u.pathname.startsWith(base + "/")) return null;
  let p: string[];
  try {
    p = stripBase(u.pathname, base).split("/").filter(Boolean).map(decodeURIComponent);
  } catch {
    return null;
  }
  const path = stripBase(u.pathname, base);
  if (u.searchParams.has("at")) p = []; // one message: its page, which lands there
  if (p[0] === "s" && p[1] && p[2] && p.length === 3) return { kind: "agent", mid: p[1], sid: p[2] };
  if (assistantPath(p[0]) && p[1] && p[1] !== "new" && p.length === 2) {
    const mid = u.searchParams.get("machine");
    const manager = u.searchParams.get("manager");
    return { kind: "conversation", sid: p[1], ...(mid ? { mid } : {}), ...(manager ? { manager } : {}) };
  }
  if (p[0] === "todos" && p.length <= 2) {
    const id = p[1] || u.searchParams.get("id");
    if (id) return { kind: "task", id };
  }
  return parseRoute(path, u.search).name === "not-found" ? null : { kind: "page", path: path + u.search + u.hash };
}

const CTRL = /[\u0000-\u001f\u007f]/;

function safeSystemHref(raw: string): string | undefined {
  if (!raw || CTRL.test(raw)) return undefined;
  const u = parse(raw);
  if (!u) return undefined;
  const scheme = u.protocol.toLowerCase();
  if (scheme !== "mailto:" && scheme !== "tel:") return undefined;
  if (u.username || u.password) return undefined;
  const rest = raw.slice(scheme.length);
  if (!rest || rest.startsWith("//") || !(u.pathname || u.search)) return undefined;
  return raw;
}

function isApiPath(raw: string): boolean {
  if (!raw.startsWith("/") || CTRL.test(raw) || raw.includes("\\") || raw.includes("#")) return false;
  const p = raw.split("?", 1)[0];
  if (p.includes("//") || /%(?:25)*5c/i.test(p) || /(?:^|\/|%2f)(?:\.|%(?:25)*2e){1,2}(?:$|\/|%2f)/i.test(p)) return false;
  const u = parse(raw, "https://hub.invalid");
  return !!u && u.origin === "https://hub.invalid" && (u.pathname.startsWith("/api/") || (!!BASE && u.pathname.startsWith(`${BASE}/api/`)));
}

// A link relative to the page it is on, as a Markdown file links its own sections and the files beside it: a
// fragment (#heading) or a path from the page's folder (./other.md). No backslash, control character or `..` segment,
// so it names no other site or scheme and no page above that folder. A bare relative path (api/v1/…) stays refused.
function isRelativeLink(raw: string): boolean {
  if (!/^(?:#|\.\/)/.test(raw) || CTRL.test(raw) || raw.includes("\\")) return false;
  const p = raw.split(/[?#]/, 1)[0];
  return !/%(?:25)*5c/i.test(p) && !/(?:^|\/|%2f)(?:\.|%(?:25)*2e){2}(?:$|\/|%2f)/i.test(p);
}

/**
 * Where `url` opens. `lark`: whether the hub reads Feishu (store.larkOn); without it Feishu links are web pages.
 * `origin`: this page's and the hub's advertised ones, for telling the hub's own pages from other sites.
 */
export function linkRoute(url: string, lark = larkOn(), origin: string | string[] = pageOrigin(), base = BASE): LinkRoute {
  const raw = url.trim();
  if (!raw || CTRL.test(raw)) return { kind: "none" };
  const u = parse(raw);
  if (!u) return { kind: "none" };
  const scheme = u.protocol.toLowerCase();
  if (APP_SCHEMES.has(scheme.slice(0, -1))) {
    if (u.username || u.password) return { kind: "none" };
    // persona://session/<mid>/<sid>: the host is "session"
    try {
      const parts = `${u.host}${u.pathname}`.split("/").filter(Boolean).map(decodeURIComponent);
      if (parts[0] === "session" && parts[1] && parts[2] && !u.search) return { kind: "session", mid: parts[1], sid: parts[2] };
    } catch {
      // A malformed percent escape in model output must not crash the transcript render.
      return { kind: "none" };
    }
    // the route table's other forms (docs/protocol/deep-links.md): read when opened, off the first screen
    return /^(open|session|task|kb|settings|extension)$/.test(u.host) ? { kind: "app", url: raw } : { kind: "none" };
  }
  if (scheme === "mailto:" || scheme === "tel:") return safeSystemHref(raw) ? { kind: "system" } : { kind: "none" };
  if (scheme !== "http:" && scheme !== "https:") return { kind: "none" };
  const safe = safeWebURL(raw);
  if (!safe) return { kind: "none" };
  const su = parse(safe)!;
  const own = hubPage(su, origin, base);
  if (own) return own;
  if (lark && isFeishuHost(su.hostname)) {
    const first = su.pathname.split("/").filter(Boolean)[0] ?? "";
    if (DOC_PATHS.includes(first)) return { kind: "doc", doc: docKind(safe) };
    const post = momentsPostID(su);
    if (post) return { kind: "moments", id: post };
  }
  return { kind: "web" };
}

/**
 * Whether a link opens inside the page (panel or route) rather than in a new tab. A Base never does: the reader can't
 * show one (the phone says so and offers Feishu), so the tab is one click shorter.
 */
export function opensInApp(url: string): boolean {
  if (previewTarget(url)) return true;
  const r = linkRoute(url);
  return (r.kind === "doc" && r.doc !== "bitable") || r.kind === "moments" || r.kind === "session" || r.kind === "agent" || r.kind === "task" || r.kind === "conversation" || r.kind === "page" || r.kind === "app";
}

/**
 * A localhost link on an agent's page (`http://localhost:5173/x`, as a dev server prints it): that port of the
 * agent's computer, shown in the preview panel (docs/web/preview.md) instead of a tab on this one's localhost. Null
 * elsewhere, and on a hub without previews.
 */
export function previewTarget(url: string): { mid: string; sid: string; cwd: string; port: number; path: string } | null {
  if (typeof location === "undefined" || !hubHas(getState().features, PREVIEW_FEATURE)) return null;
  const r = parseRoute(stripBase(location.pathname), location.search.replace(/^\?/, ""));
  if (r.name !== "session" && r.name !== "tab") return null;
  const mid = r.mid;
  const at = localhostLink(url, machineHostnames(mid));
  if (!at) return null;
  // This hub's own pages (a hub on the agent's computer, this page opened at localhost) open as hub pages, not in a preview.
  const u = parse(url);
  if (u && hubPage(u, pageOrigin(), BASE)) return null;
  const sid = r.name === "session" ? r.sid : r.sid ?? "";
  const s = sid ? getSession(mid, sid) : undefined;
  if (!s?.cwd) return null;
  return { mid, sid, cwd: s.cwd, ...at };
}

/** The names a computer goes by in links to its ports: its host name, when the hub knows it. */
export function machineHostnames(mid: string): string[] {
  const h = getState().machines.find((m) => m.id === mid)?.hostname;
  return h ? [h] : [];
}

/** Whether a port link of an agent's computer is one of this hub's own pages (the hub runs on that computer and the
 *  answer links its /s/…, /todos or /persona/… page; iOS isHubPage, #2696): it is no dev server, so it gets no chip. */
export function isHubPortLink(l: PortLink, hostnames: readonly string[]): boolean {
  const scheme = l.secure ? "https" : "http";
  return ["127.0.0.1", "localhost", ...hostnames].some((host) => {
    const u = parse(`${scheme}://${host}:${l.port}${l.path}`);
    return !!u && !!hubPage(u, pageOrigin(), BASE);
  });
}

/** The href to put on an <a>: persona://session links become the in-app path, so ⌘-click opens a tab of that page;
 *  disallowed schemes and unsafe URLs resolve to undefined rather than leaking into <a href>. */
export function linkHref(url: string): string | undefined {
  const raw = url.trim();
  const r = linkRoute(raw);
  if (r.kind === "session") return `${BASE}/s/${encodeURIComponent(r.mid)}/${encodeURIComponent(r.sid)}`;
  if (r.kind === "app") return r.url;
  if (r.kind === "system") return safeSystemHref(raw);
  if (r.kind === "none") return isApiPath(raw) || isRelativeLink(raw) ? raw : undefined;
  return safeWebURL(raw);
}

/** Opens a link where it belongs (see the top of this file). `title` names it in the panel until the page says;
 *  `manager`: the one whose chat it is in (useLinkManager), which reads the document or post it opens in the panel. */
export function openLink(url: string, title?: string, manager?: string) {
  const preview = previewTarget(url);
  if (preview) {
    openPanel({ kind: "preview", ...preview });
    return;
  }
  const r = linkRoute(url);
  switch (r.kind) {
    case "session":
      navigate({ name: "session", mid: r.mid, sid: r.sid });
      return;
    // the side panel on a wide screen while Agents open there (#5424), else a page of its own (#5096, cindy's session
    // links), the chat keeping its place and, from a Persona conversation, the page naming it (#13164)
    case "agent":
      if (beside()) openPanel({ kind: "agent", mid: r.mid, sid: r.sid });
      else openLinkPage({ name: "session", mid: r.mid, sid: r.sid });
      return;
    case "task":
      if (beside()) openPanel({ kind: "task", todo: r.id });
      else openLinkPage(taskRoute(r.id));
      return;
    case "conversation":
      navigate({ name: "persona", sid: r.sid, ...(r.mid ? { mid: r.mid } : {}), ...(r.manager ? { manager: r.manager } : {}) });
      return;
    case "page":
      navigate(r.path);
      return;
    case "app":
      void import("./deep-links").then(({ appPath }) => {
        const to = appPath(r.url);
        if (to) openLink(location.origin + BASE + to, title, manager); // as the link to that page of this hub opens
      });
      return;
    case "doc":
    case "moments": {
      const safe = safeWebURL(url);
      if (!safe) return;
      if (opensInApp(safe)) {
        openPanel({ kind: "link", url: safe, title: title || undefined, manager: otherManager(manager) });
        return;
      }
      window.open(safe, "_blank", "noopener,noreferrer");
      return;
    }
    case "web": {
      const safe = safeWebURL(url);
      if (safe) window.open(safe, "_blank", "noopener,noreferrer");
      return;
    }
    case "system": {
      const safe = safeSystemHref(url.trim());
      if (safe) location.href = safe;
      return;
    }
    case "none":
      return;
  }
}

/**
 * onClick for an <a href={linkHref(url)}>: a plain click on a link that opens in the page opens it there; ⌘ / Ctrl /
 * Shift / middle clicks and links that belong in a tab keep the browser's own behavior.
 */
export function clickLink(e: React.MouseEvent, url: string | undefined, title?: string, manager?: string) {
  if (!url || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
  if (!opensInApp(url)) return;
  e.preventDefault();
  openLink(url, title, manager);
}

// ── preloading (CLAUDE.md: opening speed first) ──────────

let prefetcher: ((url: string, manager?: string) => void) | null = null;

/** The reader registers how to warm a document (views/link/feishu-doc.ts). */
export function setLinkPrefetcher(fn: (url: string, manager?: string) => void) {
  prefetcher = fn;
}

/** A link the pointer rests on: start reading it (as manager, as openLink would) so the panel opens on a finished page. */
export function prefetchLink(url: string | undefined, manager?: string) {
  if (url && opensInApp(url) && linkRoute(url).kind === "doc") prefetcher?.(url, manager);
}

// ── <persona-context> ─────────────────────────────────────

/** A piece of a page the user picked (LinkSheet.swift ContextRef). `text` is "(whole document)" in Chinese for a whole document. */
export interface ContextRef {
  id: string;
  /** "workspace": a folder on a computer picked in the @ menu, carrying `machine` and `cwd` (docs/protocol/protocol.md "Tags in messages").
   *  "repository": an assistant-bound repository reference picked in the @ menu, carrying `repo_id`, `provider` and `path`.
   *  "forward": a message forwarded from another chat (lib/forward.ts, docs/protocol/protocol.md "Forwarded messages"). */
  kind: "context" | "edit" | "workspace" | "repository" | "forward" | "skill" | "file";
  title: string;
  url: string;
  text: string;
  /** Extra attributes written on the tag after kind / title / url (a workspace's `machine` and `cwd`, a forward's `msg` and metadata). */
  attrs?: Record<string, string>;
  /** A forward's line under its card over the box: which computer it comes from and where it goes. Shown, never sent. */
  route?: string;
}

// i18n-ignore: a marker inside <persona-context>, the app (QuoteViews.swift) matches it
export const WHOLE = "（整篇）";

// writing the tags: lib/context-xml.ts (no store, no router: New agent and the tests take it alone)
export { contextXML, withContexts } from "./context-xml";

/** "AI edit": the picked text and what to do with it, sent at once (LinkSheet.swift sendEdit). */
export function editMessage(r: Pick<ContextRef, "title" | "url" | "text">, ask: string): string {
  const a = ask.trim();
  return `${contextXML({ ...r, kind: "edit" })}\n\n${a ? t("lib-links.edit_with_ask", { ask: a }) : t("lib-links.edit_plain")}`;
}

export function newContext(kind: ContextRef["kind"], title: string, url: string, text: string): ContextRef {
  return { id: uuid(), kind, title, url, text: text.trim() || WHOLE };
}

// ── contexts waiting in a composer (memory only, per draft key) ──

const pending = new Map<string, ContextRef[]>();
const ctxListeners = new Set<() => void>();
const NONE: ContextRef[] = [];

function emitContexts() {
  ctxListeners.forEach((l) => l());
}

export function getContexts(key: string): ContextRef[] {
  return pending.get(key) ?? NONE;
}

export function addContext(key: string, ref: ContextRef) {
  if (conversationKeyDeleted(key)) return;
  const cur = getContexts(key);
  // the same passage of the same page once
  if (cur.some((c) => ref.kind === "skill" ? c.kind === "skill" && c.attrs?.ref === ref.attrs?.ref : c.url === ref.url && c.text === ref.text && c.kind === ref.kind)) return;
  pending.set(key, [...cur, ref]);
  emitContexts();
}

export function removeContext(key: string, id: string) {
  const next = getContexts(key).filter((c) => c.id !== id);
  if (next.length) pending.set(key, next);
  else pending.delete(key);
  emitContexts();
}

export function useContexts(key: string): ContextRef[] {
  return useSyncExternalStore(
    (l) => {
      ctxListeners.add(l);
      return () => ctxListeners.delete(l);
    },
    () => getContexts(key),
  );
}

// ── the composer being written in ────────────────────────

/** What a mounted composer lets the panel do: its draft key, and sending a message now ("AI edit"). */
export interface ComposerHandle {
  key: string;
  send: (text: string) => Promise<boolean>;
  focus: () => void;
  /** False while it can't send (a read-only source): picked text then goes elsewhere. */
  ready?: () => boolean;
}

let composers: ComposerHandle[] = []; // most recently mounted / focused last
const waiting: { key?: string; run: (c: ComposerHandle) => void }[] = [];

/** A composer mounts: it becomes the active one. Returns the unregister for its cleanup. */
export function registerComposer(h: ComposerHandle): () => void {
  composers = [...composers.filter((c) => c !== h), h];
  for (let i = 0; i < waiting.length; i++) {
    const w = waiting[i];
    if (w.key !== undefined && w.key !== h.key) continue;
    waiting.splice(i--, 1);
    w.run(h);
  }
  return () => {
    composers = composers.filter((c) => c !== h);
  };
}

/** The user went back to a composer (focus): it is the active one again. */
export function activateComposer(h: ComposerHandle) {
  if (composers.includes(h)) composers = [...composers.filter((c) => c !== h), h];
}

export function activeComposer(): ComposerHandle | undefined {
  return composers[composers.length - 1];
}

/** The active composer if it can send now. */
export function usableComposer(): ComposerHandle | undefined {
  const c = activeComposer();
  return c && (c.ready?.() ?? true) ? c : undefined;
}

/** Runs `run` with the composer for `key` once it mounts (now, if it is mounted); after `ms` gives up (`expired`). */
export function whenComposer(key: string | undefined, run: (c: ComposerHandle) => void, expired?: () => void, ms = 8000) {
  const now = key === undefined ? activeComposer() : composers.find((c) => c.key === key);
  if (now) {
    run(now);
    return;
  }
  const w = { key, run };
  waiting.push(w);
  setTimeout(() => {
    const i = waiting.indexOf(w);
    if (i < 0) return;
    waiting.splice(i, 1);
    expired?.();
  }, ms);
}

onConversationDeleted((mid, sid) => {
  pending.delete(`${mid}/${sid}`);
  emitContexts();
});
