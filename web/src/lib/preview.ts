// Dev server previews (docs/web/preview.md, #150): the logic of the side panel that shows a session's dev server, which a
// headless browser on that computer draws (the node's preview.go). The pictures and messages it sends, the input
// the panel sends back, the servers it offers, and a picked element as a comment for the agent. Drawn by
// views/session/PreviewPanel.tsx.
import { BASE } from "./base";
import { enc, getToken } from "./api";
import { MAX_CODE_LINES, truncateLines, type ElementSpot } from "./comments";
import type { DraftQuote } from "./drafts";
import type { Port } from "./types";

// The parts the first screen uses (chat links, port chips, comment cards) live in preview-link.ts, so this file loads
// with the panel.
export { PREVIEW_FEATURE, addressOf, localhostLink } from "./preview-link";
import { localhostLink } from "./preview-link";

/** The hub feature advertised in `hello.features` when isolated wildcard-subdomain preview origins are configured. */
export const PREVIEW_ORIGIN_FEATURE = "preview_origin";

/** Sandbox permissions for the isolated-origin preview iframe: allows scripts, isolated-origin storage/cookies,
 *  forms, modals and popups while forbidding top-level navigation of the hub page. */
export const DIRECT_IFRAME_SANDBOX = "allow-scripts allow-same-origin allow-forms allow-modals allow-popups";

/** Maximum wait for the direct preview iframe to finish loading before falling back to the browser stream. */
export const DIRECT_LOAD_TIMEOUT_MS = 15_000;

export type PreviewMode = "direct" | "stream";

export interface PreviewGrant {
  url: string;
  origin: string;
  expires_at?: string;
}

export type PreviewUnreachableCode =
  | "port_gone"
  | "offline"
  | "timeout"
  | "too_many_connections"
  | "port_not_allowed"
  | "bad_gateway"
  | "auth_failed"
  | string;

export type DirectNavTarget =
  | { kind: "same_port"; path: string }
  | { kind: "other_port"; port: number; path: string }
  | { kind: "external"; url: string }
  | { kind: "invalid" };

/** Stable per-machine, per-port key for remembering fallback and mode choices within a preview panel. */
export function previewPortKey(mid: string, port: number): string {
  return `${mid}:${port}`;
}

/** Chooses the active preview mode: direct iframe when the hub advertises `preview_origin` unless remembered as
 *  fallen back or manually switched; always stream when `preview_origin` is absent. */
export function initialPreviewMode(opts: {
  hasPreviewOrigin: boolean;
  rememberedFallback: boolean;
  manualChoice?: PreviewMode;
}): PreviewMode {
  if (!opts.hasPreviewOrigin) return "stream";
  if (opts.manualChoice) return opts.manualChoice;
  if (opts.rememberedFallback) return "stream";
  return "direct";
}

/** Validates a `POST /api/v1/machines/{mid}/preview/grant` response before loading it into an iframe. */
export function parsePreviewGrant(raw: unknown): PreviewGrant | null {
  if (!raw || typeof raw !== "object") return null;
  const rec = raw as Record<string, unknown>;
  if (typeof rec.url !== "string" || typeof rec.origin !== "string" || !rec.url || !rec.origin) return null;
  try {
    const u = new URL(rec.url);
    const o = new URL(rec.origin);
    if ((u.protocol !== "http:" && u.protocol !== "https:") || (o.protocol !== "http:" && o.protocol !== "https:")) return null;
    if (u.origin !== rec.origin || o.origin !== rec.origin) return null;
    const out: PreviewGrant = { url: u.href, origin: o.origin };
    if (typeof rec.expires_at === "string" && rec.expires_at) out.expires_at = rec.expires_at;
    return out;
  } catch {
    return null;
  }
}

/** Resolves a relative path against a granted preview origin, refusing any input that would escape that origin. */
export function directOriginURL(origin: string, path: string): string | null {
  if (!path || !path.startsWith("/") || path.startsWith("//") || hasCtrlOrSpace(path)) return null;
  try {
    const o = new URL(origin);
    const u = new URL(path, o.origin);
    return u.origin === o.origin ? u.href : null;
  } catch {
    return null;
  }
}

/** Validates an unreachable `postMessage` from the preview iframe; ignores anything whose origin or source does not match. */
export function parseUnreachableMessage(
  ev: { origin: string; source: unknown; data: unknown },
  expectedOrigin: string,
  expectedWindow: unknown,
): { code: PreviewUnreachableCode } | null {
  if (!expectedOrigin || expectedWindow == null) return null;
  if (ev.origin !== expectedOrigin || ev.source !== expectedWindow) return null;
  if (!ev.data || typeof ev.data !== "object") return null;
  const rec = ev.data as Record<string, unknown>;
  if (rec.persona_preview !== "unreachable") return null;
  if (typeof rec.code !== "string" || !rec.code) return null;
  return { code: rec.code };
}

/** Parses address bar input while in direct preview mode: same-port path, another localhost port to re-grant, or external URL. */
export function parseDirectNav(input: string, currentPort: number, hostnames: readonly string[] = []): DirectNavTarget {
  const v = input.trim();
  if (!v || hasCtrlOrSpace(v)) return { kind: "invalid" };
  if (v.startsWith("/")) {
    if (v.startsWith("//")) return { kind: "invalid" };
    try {
      const u = new URL(v, "http://localhost");
      if (u.origin !== "http://localhost") return { kind: "invalid" };
      return { kind: "same_port", path: u.pathname + u.search + u.hash };
    } catch {
      return { kind: "invalid" };
    }
  }
  const local = localhostLink(v, hostnames);
  if (local) {
    return local.port === currentPort
      ? { kind: "same_port", path: local.path }
      : { kind: "other_port", port: local.port, path: local.path };
  }
  const ext = safeExternal(v);
  if (ext) return { kind: "external", url: ext };
  return { kind: "invalid" };
}


/** A launch.json entry as `…/preview/servers` returns it. */
export interface PreviewServer {
  name: string;
  port?: number;
  url?: string;
  command?: string[];
  env?: Record<string, string>;
  cwd?: string;
  listening: boolean;
  title?: string;
  problem?: "no_name" | "no_port" | "not_local" | "no_run" | "bad_env" | string;
}

export interface PreviewServers {
  file?: string;
  servers: PreviewServer[];
  error?: string;
  /** The browser previews open in on that computer; absent when it has none. */
  browser?: string;
}

/** One server the panel offers: a launch.json entry, or a port an agent of the session listens on. */
export interface PreviewOption {
  key: string;
  name: string;
  port: number;
  /** Where it opens: a path on its server. */
  path: string;
  listening: boolean;
  title?: string;
  /** The entry's command: it can be started from the panel. */
  command?: string[];
  env?: Record<string, string>;
  cwd?: string;
  from: "launch" | "agent";
}

/** The servers to offer: the usable launch.json entries in the file's order, then the agents' ports none of them has. */
export function previewOptions(res: PreviewServers | null, ports: readonly Port[] = []): PreviewOption[] {
  const out: PreviewOption[] = [];
  for (const s of res?.servers ?? []) {
    if (s.problem || !s.port) continue;
    let path = "/";
    if (s.url) {
      try {
        const u = new URL(s.url);
        path = u.pathname + u.search + u.hash || "/";
      } catch {
        // the node checked it; a path it could not read opens at /
      }
    }
    // an agent's port is listening even when the probe missed it (still compiling)
    const listening = s.listening || ports.some((p) => p.port === s.port);
    out.push({ key: `launch:${s.name}`, name: s.name, port: s.port, path, listening, title: s.title, command: s.command, env: s.env, cwd: s.cwd, from: "launch" });
  }
  for (const p of ports) {
    if (out.some((o) => o.port === p.port)) continue;
    out.push({ key: `port:${p.port}`, name: p.title || `:${p.port}`, port: p.port, path: "/", listening: true, title: p.title, from: "agent" });
  }
  return out;
}

/** The entries launch.json has that cannot be used, for the panel to say why. */
export function previewProblems(res: PreviewServers | null): PreviewServer[] {
  return (res?.servers ?? []).filter((s) => s.problem);
}

/** The command line an entry runs, as the confirmation shows it (each word quoted as the node quotes it). */
export function commandLine(o: Pick<PreviewOption, "command" | "env">): string {
  const q = (s: string) => (/^[A-Za-z0-9_./-]+$/.test(s) ? s : `'${s.replace(/'/g, `'\\''`)}'`);
  const env = Object.keys(o.env ?? {})
    .sort()
    .map((k) => q(`${k}=${o.env![k]}`));
  return [...(env.length ? ["env", ...env] : []), ...(o.command ?? []).map(q)].join(" ");
}

/** The example the empty panel shows: what to put in .claude/launch.json. */
export const LAUNCH_EXAMPLE = `{
  "version": "0.0.1",
  "configurations": [
    {
      "name": "web",
      "runtimeExecutable": "npm",
      "runtimeArgs": ["run", "dev"],
      "port": 5173
    }
  ]
}`;

// ── the socket ────────────────────────────────────────

export interface PreviewSize {
  w: number;
  h: number;
  dpr: number;
  mobile: boolean;
}

/** A phone's viewport (CSS pixels): what "Phone" shows, scaled to the panel. */
export const PHONE_SIZE = { w: 390, h: 844 };

/** The preview's WebSocket address. A browser cannot set headers on one, so the token goes in the query, as the
 *  other sockets do. */
export function previewSocketURL(loc: { protocol: string; host: string }, mid: string, port: number, path: string, size: PreviewSize, token = getToken()): string {
  const scheme = loc.protocol === "https:" ? "wss" : "ws";
  const q = new URLSearchParams({ port: String(port), path: path || "/", w: String(Math.round(size.w)), h: String(Math.round(size.h)), dpr: String(size.dpr) });
  if (size.mobile) q.set("mobile", "1");
  if (token) q.set("token", token);
  return `${scheme}://${loc.host}${BASE}/api/v1/machines/${enc(mid)}/preview/connect?${q}`;
}

/** A binary message from the node: a picture of the page, or a screenshot. */
export type PreviewBinary = { kind: "frame"; seq: number; w: number; h: number; data: Uint8Array } | { kind: "shot"; id: number; data: Uint8Array };

export function parseBinary(buf: ArrayBuffer): PreviewBinary | null {
  const v = new DataView(buf);
  if (buf.byteLength < 5) return null;
  const tag = String.fromCharCode(v.getUint8(0));
  if (tag === "f" && buf.byteLength >= 9) return { kind: "frame", seq: v.getUint32(1), w: v.getUint16(5), h: v.getUint16(7), data: new Uint8Array(buf, 9) };
  if (tag === "s") return { kind: "shot", id: v.getUint32(1), data: new Uint8Array(buf, 5) };
  return null;
}

/** Identifies a binary `f` frame's image format from its magic bytes (never assumes JPEG). */
export function frameImageMime(data: Uint8Array): "image/png" | "image/jpeg" | null {
  if (data.length >= 4 && data[0] === 0x89 && data[1] === 0x50 && data[2] === 0x4e && data[3] === 0x47) return "image/png";
  if (data.length >= 3 && data[0] === 0xff && data[1] === 0xd8 && data[2] === 0xff) return "image/jpeg";
  return null;
}

/** Display dimensions (CSS px) for the preview canvas inside `area`, preserving aspect ratio and aligning desktop
 *  display size to physical pixels where possible without altering layout on higher-DPR displays. */
export function canvasFit(
  area: { w: number; h: number },
  page: { w: number; h: number },
  opts: { mobile: boolean; dpr: number; backing?: { w: number; h: number } | null },
): { w: number; h: number; scale: number } {
  const pw = Math.max(1, page.w);
  const ph = Math.max(1, page.h);
  const aw = Math.max(0, area.w);
  const ah = Math.max(0, area.h);
  const scale = Math.min(aw / pw, ah / ph, 1);
  const dpr = opts.dpr > 0 && Number.isFinite(opts.dpr) ? opts.dpr : 1;
  if (!opts.mobile && scale >= 1) {
    // At 1:1 desktop CSS scale, if the node floored int(w * dpr) on a fractional DPR (e.g. 1.25/1.5),
    // align the CSS box to backing / dpr so each backing pixel lands on one physical pixel.
    const b = opts.backing;
    const w = b && b.w > 0 && Math.abs(b.w - pw * dpr) <= 1 && b.w / dpr <= aw ? b.w / dpr : pw;
    const h = b && b.h > 0 && Math.abs(b.h - ph * dpr) <= 1 && b.h / dpr <= ah ? b.h / dpr : ph;
    return { w, h, scale: 1 };
  }
  if (!opts.mobile) {
    return { w: Math.floor(pw * scale * dpr) / dpr, h: Math.floor(ph * scale * dpr) / dpr, scale };
  }
  return { w: Math.floor(pw * scale), h: Math.floor(ph * scale), scale };
}

export const MAX_SELECT_OPTIONS = 1000;
export const MAX_SELECT_LABEL = 200;
export const SELECT_POPUP_MAX_HEIGHT = 280;
const SELECT_POPUP_EDGE = 8;
const SELECT_POPUP_GAP = 4;
const SELECT_POPUP_MIN_WIDTH = 120;

export interface PreviewSelectRect {
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface PreviewSelectOption {
  index: number;
  label: string;
  disabled?: boolean;
  group?: string;
}

export interface PreviewSelectState {
  id: number;
  rect: PreviewSelectRect;
  selected: number;
  options: PreviewSelectOption[];
}

/** Sanitizes and caps a `select_open` payload from the node so untrusted pages cannot flood or bloat the popup. */
export function normalizeSelectOpen(m: PreviewSelectState): PreviewSelectState | null {
  if (!Number.isInteger(m.id) || m.id <= 0 || !m.rect || !Array.isArray(m.options)) return null;
  const cap = (s: unknown) => (typeof s === "string" ? s.slice(0, MAX_SELECT_LABEL) : "");
  const num = (n: unknown) => (typeof n === "number" && Number.isFinite(n) ? n : 0);
  const options: PreviewSelectOption[] = [];
  for (const raw of m.options.slice(0, MAX_SELECT_OPTIONS)) {
    if (!raw || typeof raw.index !== "number" || !Number.isInteger(raw.index) || raw.index < 0) continue;
    const opt: PreviewSelectOption = { index: raw.index, label: cap(raw.label) };
    if (raw.disabled) opt.disabled = true;
    if (typeof raw.group === "string" && raw.group) opt.group = cap(raw.group);
    options.push(opt);
  }
  return {
    id: m.id,
    rect: { x: num(m.rect.x), y: num(m.rect.y), w: Math.max(0, num(m.rect.w)), h: Math.max(0, num(m.rect.h)) },
    selected: typeof m.selected === "number" && Number.isInteger(m.selected) ? m.selected : -1,
    options,
  };
}

export interface SelectPopupPlacement {
  left: number;
  top: number;
  minWidth: number;
  maxWidth: number;
  maxHeight: number;
  placement: "bottom" | "top";
}

/** Maps a page-space `<select>` rect onto the canvas box inside the preview container, preferring below and flipping
 *  above when below lacks room. */
export function placeSelectPopup(
  rect: PreviewSelectRect,
  page: { w: number; h: number },
  canvasBox: { left: number; top: number; w: number; h: number },
  container: { w: number; h: number },
  naturalHeight: number,
  measuredWidth?: number,
): SelectPopupPlacement {
  const pw = Math.max(1, page.w);
  const ph = Math.max(1, page.h);
  const sx = canvasBox.w / pw;
  const sy = canvasBox.h / ph;
  const selLeft = canvasBox.left + rect.x * sx;
  const selTop = canvasBox.top + rect.y * sy;
  const selW = rect.w * sx;
  const selH = rect.h * sy;
  const selBottom = selTop + selH;

  const maxWidth = Math.max(0, container.w - 2 * SELECT_POPUP_EDGE);
  const minWidth = Math.min(Math.max(selW, SELECT_POPUP_MIN_WIDTH), maxWidth);
  const usedWidth = Math.min(Math.max(minWidth, measuredWidth ?? minWidth), maxWidth);
  const left = Math.max(SELECT_POPUP_EDGE, Math.min(selLeft, container.w - SELECT_POPUP_EDGE - usedWidth));

  const spaceBelow = Math.max(0, container.h - SELECT_POPUP_EDGE - (selBottom + SELECT_POPUP_GAP));
  const spaceAbove = Math.max(0, selTop - SELECT_POPUP_GAP - SELECT_POPUP_EDGE);
  const desiredH = Math.min(Math.max(0, naturalHeight), SELECT_POPUP_MAX_HEIGHT);
  const flip = desiredH > spaceBelow && spaceAbove > spaceBelow;
  const placement: "bottom" | "top" = flip ? "top" : "bottom";
  const maxHeight = Math.max(0, Math.min(SELECT_POPUP_MAX_HEIGHT, flip ? spaceAbove : spaceBelow));
  const shownH = Math.min(Math.max(0, naturalHeight), maxHeight);
  const rawTop = flip ? selTop - SELECT_POPUP_GAP - shownH : selBottom + SELECT_POPUP_GAP;
  const maxTop = Math.max(SELECT_POPUP_EDGE, container.h - SELECT_POPUP_EDGE - shownH);
  const top = Math.max(SELECT_POPUP_EDGE, Math.min(rawTop, maxTop));

  return { left: Math.round(left), top: Math.round(top), minWidth: Math.round(minWidth), maxWidth: Math.round(maxWidth), maxHeight: Math.round(maxHeight), placement };
}

/** First or last non-disabled option position in `options`, or -1 when none is enabled. */
export function edgeSelectOption(options: readonly PreviewSelectOption[], last: boolean): number {
  if (last) {
    for (let i = options.length - 1; i >= 0; i--) if (!options[i].disabled) return i;
    return -1;
  }
  for (let i = 0; i < options.length; i++) if (!options[i].disabled) return i;
  return -1;
}

/** Initial keyboard highlight for `select_open`: the selected option if enabled, else -1 (next arrow lands on an enabled item). */
export function initialSelectActive(options: readonly PreviewSelectOption[], selected: number): number {
  if (selected >= 0) {
    const i = options.findIndex((o) => o.index === selected);
    if (i >= 0 && !options[i].disabled) return i;
    return -1;
  }
  return edgeSelectOption(options, false);
}

/** Next non-disabled option position in direction `dir` (1 down, -1 up), clamping at ends. */
export function stepSelectOption(options: readonly PreviewSelectOption[], from: number, dir: 1 | -1): number {
  if (from < 0) return edgeSelectOption(options, dir === -1);
  for (let i = from + dir; i >= 0 && i < options.length; i += dir) {
    if (!options[i].disabled) return i;
  }
  return from;
}

/** What the node says, as JSON. */
export type PreviewMessage =
  | { t: "ready"; browser: string; w: number; h: number }
  | { t: "page"; url: string; title: string; loading: boolean; back: boolean; forward: boolean }
  | { t: "external"; url: string }
  | { t: "failed"; url: string; error: string }
  | { t: "dialog"; kind: "alert" | "confirm" | "prompt" | "beforeunload"; message: string; default?: string }
  | { t: "dialog_closed" }
  | { t: "pick"; on: boolean }
  | ({ t: "picked" } & PickedElement)
  | { t: "pick_failed" }
  | { t: "shot_failed"; id: number; error: string }
  | { t: "copied"; text: string }
  | { t: "notice"; code: string }
  | ({ t: "select_open" } & PreviewSelectState)
  | { t: "select_close"; id: number }
  | { t: "crashed" }
  | { t: "closed"; code: string }
  | { t: "error"; code: string; error: string };

/** The element picked in the page, as the node describes it (preview_pick.js). */
export interface PickedElement {
  selector: string;
  tag: string;
  text: string;
  html: string;
  role?: string;
  label?: string;
  component?: string;
  source?: { file: string; line: number } | null;
  rect?: { x: number; y: number; w: number; h: number };
  url: string;
  title?: string;
}

// ── input ─────────────────────────────────────────────

/** The modifier keys as the browser on the computer counts them (CDP: Alt 1, Ctrl 2, Meta 4, Shift 8). */
export function modsOf(e: { altKey: boolean; ctrlKey: boolean; metaKey: boolean; shiftKey: boolean }): number {
  return (e.altKey ? 1 : 0) | (e.ctrlKey ? 2 : 0) | (e.metaKey ? 4 : 0) | (e.shiftKey ? 8 : 0);
}

/** The point under the pointer in the page's CSS pixels, from where it is on the picture (drawn at `rect`). */
export function toPage(x: number, y: number, rect: { left: number; top: number; width: number; height: number }, page: { w: number; h: number }) {
  const px = rect.width > 0 ? ((x - rect.left) * page.w) / rect.width : 0;
  const py = rect.height > 0 ? ((y - rect.top) * page.h) / rect.height : 0;
  return { x: Math.max(0, Math.min(page.w, Math.round(px))), y: Math.max(0, Math.min(page.h, Math.round(py))) };
}

/** The mouse button by its DOM number, as CDP names it. */
export function buttonName(b: number): "left" | "middle" | "right" | "back" | "forward" | "none" {
  return (["left", "middle", "right", "back", "forward"] as const)[b] ?? "none";
}

const isMac = () => typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);

/** The page's own editing command a shortcut stands for (⌘A / Ctrl+A select all…), or null: a headless browser has no
 *  menu to take them from, and copying must reach this computer's clipboard. */
export function editCommand(e: { key: string; altKey: boolean; ctrlKey: boolean; metaKey: boolean; shiftKey: boolean }, mac = isMac()): string | null {
  const mod = mac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
  if (!mod || e.altKey) return null;
  switch (e.key.toLowerCase()) {
    case "a":
      return e.shiftKey ? null : "selectAll";
    case "c":
      return e.shiftKey ? null : "copy";
    case "x":
      return e.shiftKey ? null : "cut";
    case "z":
      return e.shiftKey ? "redo" : "undo";
    case "y":
      return mac || e.shiftKey ? null : "redo";
  }
  return null;
}

/** A key pressed in the preview as the message for it; `text` when it types a character. Null for what is not sent
 *  as a key: an input method at work (its text comes when it is done), a modifier alone. */
export function keyMessage(
  e: { key: string; code: string; keyCode: number; location: number; repeat: boolean; isComposing: boolean; altKey: boolean; ctrlKey: boolean; metaKey: boolean; shiftKey: boolean },
  type: "down" | "up",
): Record<string, unknown> | null {
  if (e.isComposing || e.key === "Process" || e.key === "Dead" || e.key === "Unidentified") return null;
  const msg: Record<string, unknown> = { t: "key", type, key: e.key, code: e.code, key_code: e.keyCode, mods: modsOf(e), location: e.location };
  if (e.repeat) msg.repeat = true;
  if (type === "down") {
    const printable = [...e.key].length === 1 && !e.ctrlKey && !e.metaKey;
    if (printable) msg.text = e.key;
    else if (e.key === "Enter") msg.text = "\r";
  }
  return msg;
}

// ── addresses ─────────────────────────────────────────

function hasCtrlOrSpace(url: string): boolean {
  for (let i = 0; i < url.length; i++) {
    const c = url.charCodeAt(i);
    if (c <= 0x20 || c === 0x7f) return true;
  }
  return /\s/.test(url);
}

/** An address the page wanted to open elsewhere, when it may be opened in a new tab here (credential-free http / https only). */
export function safeExternal(url: string): string | null {
  const trimmed = url.trim();
  if (!trimmed || hasCtrlOrSpace(trimmed) || !/^https?:\/\//i.test(trimmed)) return null;
  const auth = trimmed.replace(/^https?:\/\//i, "").split(/[/?#]/, 1)[0];
  if (!auth || auth.includes("@") || auth.includes("\\")) return null;
  try {
    const u = new URL(trimmed);
    return (u.protocol === "http:" || u.protocol === "https:") && u.hostname !== "" && u.username === "" && u.password === "" ? u.href : null;
  } catch {
    return null;
  }
}

// ── a picked element as a comment ─────────────────────

/** The element the user picked as a comment's quote: its HTML is the code (cut to MAX_CODE_LINES lines). */
export function elementQuote(el: PickedElement, at: { mid: string; sid: string; cwd: string; port: number }): DraftQuote {
  const lines = prettyHTML(el.html).split("\n");
  const cut = truncateLines(lines, MAX_CODE_LINES);
  const spot: ElementSpot = { url: el.url, selector: el.selector || el.tag, omitted: cut.omitted, mid: at.mid, sid: at.sid, cwd: at.cwd, port: at.port };
  if (el.component) spot.component = el.component;
  if (el.source?.file) spot.source = el.source.line ? `${relativeSource(el.source.file, at.cwd)}:${el.source.line}` : relativeSource(el.source.file, at.cwd);
  const text = el.text.trim();
  if (text) spot.text = text.length > 120 ? `${text.slice(0, 119)}…` : text;
  return { text: cut.lines.join("\n"), comment: { body: "", element: spot } };
}

/** A source path a dev build gave, relative to the session's folder when inside it (webpack's and vite's are absolute). */
function relativeSource(file: string, cwd: string): string {
  const f = file.replace(/^webpack:\/\/\/?(?:\.\/)?/, "").replace(/^file:\/\//, "");
  const root = cwd.replace(/\/+$/, "");
  return root && f.startsWith(root + "/") ? f.slice(root.length + 1) : f;
}

/** outerHTML with a line per tag when it came as one long line (most frameworks write it so), so 60 lines say more. */
export function prettyHTML(html: string): string {
  if (html.includes("\n") || html.length < 120) return html;
  return html.replace(/>\s*</g, ">\n<");
}
