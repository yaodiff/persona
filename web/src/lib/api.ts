import { storedCredentials, storeCredentials, CLIENT_HEADERS, OP_HEADERS, compatibleEnvelope, requestForHub } from "./compatibility";
// REST client for the hub. The page is served by the hub itself (same origin), so every path is relative.

import { MANAGER_HEADER, managerHeaderFor } from "./manager-choice";
import { BASE } from "./base";
import { intercepted, reloadForSignIn } from "./edge-auth";
import { requestCrumb } from "./error-reports";
import { currentLanguage, t } from "../i18n";
import { HubUnreachableError, isNetworkFailure, isProgramError } from "./network-failure";
import type { Item } from "./types";
import { clearFeedCache } from "./feed-cache";
import { credentialEpoch, renewCredentialEpoch } from "./credential-epoch";
import { clientInstance } from "./device-id";

let ownerToken: string | undefined;
let ownerEpoch: string | undefined;
let ownerGeneration = 0;
const credentialListeners = new Set<(owner: string) => void>();
interface SharedGetResult {
  data: unknown;
  rawText: string;
  isJson: boolean;
  headers: Headers;
  status: number;
}
interface InFlightGet {
  path: string;
  fetcher: typeof fetch;
  controller: AbortController;
  waiters: number;
  delivered: number;
  promise: Promise<SharedGetResult>;
}
const inFlightGets = new Map<string, InFlightGet>();
function publishCredentialOwner(): string {
  inFlightGets.clear();
  const owner = `credential-${++ownerGeneration}`;
  credentialListeners.forEach((listener) => listener(owner));
  return owner;
}
/** Opaque in-memory auth partition; never expose credentials in tree keys or persisted drafts. */
export function credentialOwner(): string {
  const token = getToken();
  const epoch = credentialEpoch();
  if (token !== ownerToken || epoch !== ownerEpoch) {
    ownerToken = token;
    ownerEpoch = epoch;
    return publishCredentialOwner();
  }
  return `credential-${ownerGeneration}`;
}

/** Retire work when pairing or sign-out replaces its credential, including a return to the same token. */
export function onCredentialChange(listener: (owner: string) => void): () => void {
  credentialListeners.add(listener);
  return () => { credentialListeners.delete(listener); };
}

/** React editors follow the shared credential subscription and other tabs' storage changes. */
export function subscribeCredentialOwner(listener: () => void): () => void {
  const stop = onCredentialChange(listener);
  const storage = () => { credentialOwner(); };
  window.addEventListener("storage", storage);
  return () => { stop(); window.removeEventListener("storage", storage); };
}

export function getToken(): string {
  return storedCredentials().token;
}

export function setToken(token: string, tokenId?: string) {
  if (token !== getToken() || !token) clearFeedCache();
  storeCredentials(token, tokenId);
  credentialOwner();
}

/** A successful sign-in is a new owner even if the hub returned the same token. */
export function setPairedToken(token: string, tokenId?: string) {
  credentialOwner();
  storeCredentials(token, tokenId);
  const shared = renewCredentialEpoch();
  clearFeedCache();
  if (shared) credentialOwner();
  else {
    ownerToken = getToken();
    ownerEpoch = credentialEpoch();
    publishCredentialOwner();
  }
}

export class ApiError extends Error {
  status: number;
  body: Record<string, unknown>;
  isJson: boolean;
  /** The hub logs a failed request under this id (X-Request-Id): for a report, not for the message itself. */
  requestId?: string;
  constructor(status: number, message: string, body: Record<string, unknown>, isJson = true, requestId?: string, readonly requestPath?: string) {
    super(message);
    this.status = status;
    this.body = body;
    this.isJson = isJson;
    this.requestId = requestId;
  }
  get code(): string | undefined {
    const code = typeof this.body.code === "string" ? this.body.code : undefined;
    const path = this.requestPath?.split("?")[0];
    // Older hubs lost this business code. Match only their exact refusal on knowledge/recording routes,
    // never a phrase in an arbitrary internal error. Keep the original body for diagnostics.
    const knowledge = path === "/kb" || path?.startsWith("/kb/") || path === "/kb-search"
      || path === "/recordings" || path?.startsWith("/recordings/");
    if (knowledge && this.status === 503 && code === "internal" && this.body.detail === "the knowledge base is off") return "kb_disabled";
    return code;
  }
}

/** A computer's 404 not_found about a session's history (#11450): it carries the computer's `reason`, or its words in
 * `detail`. The hub's own 404 (an unknown computer, a missing route) carries neither, and may answer otherwise later. */
export function sessionHistoryNotFound(e: unknown): e is ApiError {
  return e instanceof ApiError && e.status === 404 && e.code === "not_found" &&
    (typeof e.body.reason === "string" || typeof e.body.detail === "string");
}

let onUnauthorized: () => void = () => {};
/** What a 401 does from now on; returns what it did before, for a caller that sets it only for a while. */
export function handleUnauthorized(fn: () => void): () => void {
  const before = onUnauthorized;
  onUnauthorized = fn;
  return before;
}

/**
 * Who this page is to the hub: the platform and version its socket's client_hello names, and every request's
 * X-Persona-Client and X-Persona-Features. X-Persona-Client-Instance names this tab and matches client_hello.instance,
 * so approval_mirrors cannot be borrowed from another tab. MCP forms and links are answered only to a request whose
 * platform and version match a connection that declared them (docs/protocol/protocol.md "MCP elicitation").
 */
export const CLIENT_PLATFORM = "web";
export const CLIENT_VERSION = "1";
/** What this page can render beyond the basics (client_hello `features`). `persona_card_ring`: it draws ```persona-card
 *  rings (lib/assistant-view.ts); a client without it gets each ring as a stacked bar (the hub's card_compat.go). */
/** `managers`: the hub pushes {"type":"managers"} when its roster changes (Settings › Managers, lib/managers.ts). */
/** `agent_message_hold`: it shows a message the hub holds for an Agent whose computer is away (lib/undelivered.ts,
 *  #10661); each message send declares it too (lib/message-send.ts). */
/**
 * The pong timeout (seconds) this page asks the hub for in client_hello. The hub's default of 15 s is tight on a
 * high-latency public link whose outbound path changes now and then: a ping or its pong lost across a switch closed
 * the socket (pong_timeout) every 30-250 s. 45 s rides through a switch yet stays under the page's own 70 s quiet
 * limit, so a dead link is still dropped. Only the timeout is asked for: the 20 s interval stays the hub's.
 */
export const CLIENT_PONG_TIMEOUT = 45;
// Keep the socket replay features at 4–6 so the store can reuse them without repeating their strings.
export const CLIENT_FEATURES = ["elicitation", "extension_operations", "extension_auth", "managers", "extension_ui_stream_v1", "persona_contracts_v1", "approval_mirrors", "persona_card_ring", "closeout.markdown.v1", "agent_message_hold", "tool_views"];

// A read the hub has not answered by then fails instead of loading forever (#4492): a restarting hub, a stuck node or a
// half-dead network. Longer than the hub's own cut-offs for asking a computer (30–40 s), so when the hub has an error
// that says which computer, that one arrives first. Writes have none: one that took effect must not read as failed.
export const READ_TIMEOUT_MS = 45_000;

/** The `timeout` for a read the hub itself gives `hubMs` or longer (a node that starts a CLI, a gateway): a few seconds
 *  past the hub's own limit, so the hub's error (which computer, which launcher) or a late answer comes first (#5035). */
export const pastHub = (hubMs: number): number => hubMs + 5_000;

/** The waits before a read's second and third try: a dropped connection or a proxy's 502 usually clears within seconds.
 *  None under Vitest, whose stub hubs answer once and whose failures would otherwise all wait these out. */
const readRetryMs = () => (import.meta.env.MODE === "test" ? [] : [300, 1_000]);

/** The hub did not answer a read within its time (`api`'s `timeout`). */
export class HubTimeoutError extends Error {
  constructor() {
    super(t("lib-api.hub_no_response"));
    this.name = "HubTimeoutError";
  }
}

type ApiOptions = RequestInit & { json?: unknown; query?: Record<string, string>; token?: string; tokenOwner?: string; isAuthContextCurrent?: () => boolean; onResponse?: (headers: Headers, status: number) => void; readBlob?: (response: Response, signal?: AbortSignal | null) => Promise<Blob>; allowHtmlResponse?: boolean; dedupe?: boolean };

/** Detach in-flight GET entries so subsequent reads issue a fresh request (writes, store mutations, resets). */
export function invalidateInFlightGets(prefix?: string): void {
  if (!prefix) {
    inFlightGets.clear();
    return;
  }
  for (const [key, entry] of inFlightGets) {
    if (entry.path === prefix || entry.path.startsWith(`${prefix}?`) || entry.path.startsWith(`${prefix}/`)) {
      inFlightGets.delete(key);
    }
  }
}

function abortError(reason: unknown): DOMException {
  return isAbort(reason) ? reason : new DOMException("aborted", "AbortError");
}

function buildHeaders(path: string, init: ApiOptions): Headers {
  const headers = new Headers(init.headers);
  headers.set("Authorization", `Bearer ${init.token ?? getToken()}`);
  // the hub writes its error and status text in the interface language (docs/protocol/i18n.md "Protocol")
  if (!headers.has("Accept-Language")) headers.set("Accept-Language", currentLanguage());
  CLIENT_HEADERS.forEach(name => headers.set(name, `${CLIENT_PLATFORM}/${CLIENT_VERSION}`));
  headers.set(CLIENT_HEADERS[0] + "-Instance", clientInstance);
  if (!headers.has("X-Persona-Features")) headers.set("X-Persona-Features", CLIENT_FEATURES.join(","));
  const op = OP_HEADERS.map(name => headers.get(name)).find(Boolean);
  if (op) OP_HEADERS.forEach(name => headers.set(name, op));
  // the manager the assistant page shows, on the assistant's requests (lib/manager-choice.ts); none for the default
  const manager = managerHeaderFor(path);
  if (manager && !headers.has(MANAGER_HEADER)) headers.set(MANAGER_HEADER, manager);
  return headers;
}

function dedupeKey(path: string, owner: string | undefined, init: ApiOptions, headers: Headers): string {
  const entries = [...headers.entries()].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
  return JSON.stringify([path, owner ?? "", init.token ?? "", init.allowHtmlResponse ? 1 : 0, init.credentials ?? "", init.cache ?? "", entries]);
}

function withQuery(path: string, query?: Record<string, string>): string {
  const q = new URLSearchParams(query);
  return q.size ? `${path}${path.includes("?") ? "&" : "?"}${q}` : path;
}

/** `tokenOwner`: explicit credentials opt into 401 recovery only while both the token and credential generation are current.
 *  `isAuthContextCurrent`: an optional additional lifetime fence for a request scoped to a view or manager.
 *  `timeout`: ms before a read gives up with HubTimeoutError (default READ_TIMEOUT_MS for GET, none otherwise; 0: none).
 *  `dedupe`: coalesce identical in-flight GET requests (default true; pass false for reads that must bypass an older in-flight GET). */
export function api<T>(path: string, init: ApiOptions & { timeout?: number } = {}): Promise<T> {
  return call<T>(path, init, false);
}

/** A hub route's bytes (an image it serves, such as a meeting photo's) with `api`'s credentials, headers, time limit and
 *  errors: for what an `<img>` can't fetch itself, since it sends no header (the bearer token, the manager's).
 *  `readBlob`: an optional bounded reader; omitted, existing binary callers keep their ordinary blob read. */
export function apiBlob(path: string, init: ApiOptions & { timeout?: number } = {}): Promise<Blob> {
  return call<Blob>(path, init, true);
}

async function call<T>(path: string, init: ApiOptions & { timeout?: number }, binary: boolean): Promise<T> {
  const { timeout, dedupe, query, ...rest } = init;
  const requestPath = withQuery(path, query);
  const read = (init.method ?? "GET").toUpperCase() === "GET";
  if (!read) inFlightGets.clear();
  const limit = timeout ?? (read ? READ_TIMEOUT_MS : 0);
  const canDedupe = !binary && read && dedupe !== false && init.body === undefined && init.json === undefined && !init.isAuthContextCurrent;
  if (canDedupe) return callSharedGet<T>(requestPath, rest, limit);
  if (!limit) {
    try {
      return await request<T>(requestPath, rest, binary);
    } finally {
      if (!read) inFlightGets.clear();
    }
  }
  // The caller's own signal still aborts as before (an AbortError); only the time running out becomes HubTimeoutError.
  const controller = new AbortController();
  const outer = init.signal;
  const forward = () => controller.abort(outer?.reason);
  if (outer?.aborted) forward();
  else outer?.addEventListener("abort", forward, { once: true });
  let late = false;
  const timer = setTimeout(() => {
    late = true;
    controller.abort();
  }, limit);
  try {
    for (let tries = 0; ; tries++) {
      try {
        return await request<T>(requestPath, { ...rest, signal: controller.signal }, binary);
      } catch (e) {
        // asking again is safe for a read: a gap gets a couple more tries, all inside the read's own time limit
        if (!read || tries >= readRetryMs().length || !isTransient(e)) throw e;
        await pause(readRetryMs()[tries], controller.signal);
      }
    }
  } catch (e) {
    throw late && isAbort(e) ? new HubTimeoutError() : e;
  } finally {
    if (!read) inFlightGets.clear();
    clearTimeout(timer);
    outer?.removeEventListener("abort", forward);
  }
}

function callSharedGet<T>(path: string, init: ApiOptions, limit: number): Promise<T> {
  const outer = init.signal;
  if (outer?.aborted) return Promise.reject(abortError(outer.reason));
  const owner = init.token === undefined ? credentialOwner() : init.tokenOwner;
  const headers = buildHeaders(path, init);
  const key = dedupeKey(path, owner, init, headers);
  const fetcher = fetch;
  let entry = inFlightGets.get(key);
  if (entry && entry.fetcher !== fetcher) {
    inFlightGets.delete(key);
    entry = undefined;
  }
  if (!entry) {
    const controller = new AbortController();
    const created: InFlightGet = { path, fetcher, controller, waiters: 0, delivered: 0, promise: null! };
    inFlightGets.set(key, created);
    const { onResponse: _onResponse, signal: _signal, ...sharedInit } = init;
    created.promise = runSharedGet(key, created, path, { ...sharedInit, signal: controller.signal }, owner, headers);
    // Prevent unhandled rejections if all waiters leave and cancel the shared controller.
    created.promise.catch(() => {});
    entry = created;
  }
  const active = entry;
  active.waiters++;
  return new Promise<T>((resolve, reject) => {
    let done = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const detach = (cancel: boolean): boolean => {
      if (done) return false;
      done = true;
      clearTimeout(timer);
      outer?.removeEventListener("abort", onAbort);
      active.waiters--;
      if (cancel && active.waiters === 0) {
        if (inFlightGets.get(key) === active) inFlightGets.delete(key);
        active.controller.abort();
      }
      return true;
    };
    const onAbort = () => {
      if (detach(true)) reject(abortError(outer?.reason));
    };
    outer?.addEventListener("abort", onAbort, { once: true });
    if (limit > 0) {
      timer = setTimeout(() => {
        if (detach(true)) reject(new HubTimeoutError());
      }, limit);
    }
    active.promise.then(
      (result) => {
        if (!detach(false)) return;
        init.onResponse?.(result.headers, result.status);
        const value = active.delivered++ > 0 && result.isJson && result.rawText
          ? compatibleEnvelope(JSON.parse(result.rawText))
          : result.data;
        resolve(value as T);
      },
      (err) => {
        if (!detach(false)) return;
        reject(err);
      },
    );
  });
}

function pause(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((done, fail) => {
    const timer = setTimeout(done, ms);
    signal.addEventListener("abort", () => {
      clearTimeout(timer);
      fail(new DOMException("aborted", "AbortError"));
    }, { once: true });
  });
}

async function runSharedGet(key: string, entry: InFlightGet, path: string, init: ApiOptions, owner: string | undefined, headers: Headers): Promise<SharedGetResult> {
  const { onResponse: _onResponse, isAuthContextCurrent: _fence, readBlob: _readBlob, allowHtmlResponse, dedupe: _dedupe, ...fetchOptions } = init;
  const method = init.method ?? "GET";
  try {
    for (let tries = 0; ; tries++) {
      try {
        let res: Response;
        try {
          // Native browser fetch requires the global receiver, not the in-flight entry that stores it.
          res = await entry.fetcher.call(globalThis, `${BASE}/api/v1${path}`, { redirect: "manual", ...fetchOptions, headers });
        } catch (e) {
          if (!isAbort(e)) requestCrumb(method, path, 0);
          throw unreachable(e);
        }
        const notModified = res.status === 304 && headers.has("If-None-Match");
        if (!notModified && !allowHtmlResponse && intercepted(res)) {
          requestCrumb(method, path, res.status);
          if (owner === undefined || (owner === credentialOwner() && (init.token === undefined || init.token === getToken()))) {
            reloadForSignIn();
          }
          throw new ApiError(res.status || 401, t("lib-api.hub_no_response"), {}, false);
        }
        const text = await bodyText(res);
        let data: Record<string, unknown> = {};
        let isJson = false;
        try {
          data = text ? JSON.parse(text) : {};
          isJson = typeof data === "object" && data !== null && !Array.isArray(data);
        } catch {
          data = { error: /^\s*</.test(text) ? "" : text };
          isJson = false;
        }
        if (!res.ok && !notModified) {
          requestCrumb(method, path, res.status);
          if (res.status === 401 && owner !== undefined && owner === credentialOwner() && (init.token === undefined || init.token === getToken())) {
            onUnauthorized();
          }
          const msg = typeof data.error === "string" && data.error ? data.error : `HTTP ${res.status}`;
          throw new ApiError(res.status, msg, data, isJson, res.headers?.get("X-Request-Id") ?? undefined, path);
        }
        return { data: compatibleEnvelope(data), rawText: text, isJson, headers: res.headers, status: res.status };
      } catch (e) {
        if (tries >= readRetryMs().length || !isTransient(e)) throw e;
        await pause(readRetryMs()[tries], entry.controller.signal);
      }
    }
  } finally {
    if (inFlightGets.get(key) === entry) inFlightGets.delete(key);
  }
}

async function request<T>(path: string, init: ApiOptions, binary = false): Promise<T> {
  const { onResponse, isAuthContextCurrent, readBlob, allowHtmlResponse, dedupe: _dedupe, ...fetchOptions } = init;
  const owner = init.token === undefined ? credentialOwner() : init.tokenOwner;
  const headers = buildHeaders(path, init);
  let body = init.body;
  if (init.json !== undefined) {
    headers.set("Content-Type", "application/json");
    body = JSON.stringify(requestForHub(path, init.json));
  }
  const method = init.method ?? "GET";
  let res: Response;
  try {
    // manual: a redirect (the sign-in layer in front of the hub has expired) arrives as one instead of being followed
    // into a cross-origin page this script cannot read; the hub itself never redirects its API
    res = await fetch(`${BASE}/api/v1${path}`, { redirect: "manual", ...fetchOptions, headers, body });
  } catch (e) {
    // a breadcrumb for an error report: the method, the path's pattern (no ids, no query) and that it did not get there
    if (!isAbort(e)) requestCrumb(method, path, 0);
    throw unreachable(e);
  }
  // A 304 answers a request that sent a validator (If-None-Match): the caller keeps what it has. It is neither the
  // sign-in layer's redirect nor an error.
  const notModified = res.status === 304 && headers.has("If-None-Match");
  // The session visualization route is the one /api route whose own answer is a text/html fragment (#10321): its
  // callers opt out of the sign-in-page guard, which otherwise assumes the hub only ever answers /api with JSON.
  if (!notModified && !allowHtmlResponse && intercepted(res)) {
    requestCrumb(method, path, res.status);
    // Retired requests cannot reload a replacement view or credential; an unowned pairing probe can still sign in.
    if ((isAuthContextCurrent?.() ?? true)
      && (owner === undefined || (owner === credentialOwner() && (init.token === undefined || init.token === getToken())))) {
      reloadForSignIn(); // the layer in front takes over the page and sends it to its sign-in
    }
    throw new ApiError(res.status || 401, t("lib-api.hub_no_response"), {}, false);
  }
  if (binary && res.ok) {
    onResponse?.(res.headers, res.status);
    return (await (readBlob ? readBlob(res, init.signal) : res.blob().catch((e: unknown) => { throw isAbort(e) ? e : unreachable(e); }))) as T;
  }
  const text = await bodyText(res);
  let data: Record<string, unknown> = {};
  let isJson = false;
  try {
    data = text ? JSON.parse(text) : {};
    isJson = typeof data === "object" && data !== null && !Array.isArray(data);
  } catch {
    // a proxy's HTML error page says nothing a person can use: the status stands in for it
    data = { error: /^\s*</.test(text) ? "" : text };
    isJson = false;
  }
  if (!res.ok && !notModified) {
    requestCrumb(method, path, res.status);
    if (res.status === 401 && owner !== undefined && owner === credentialOwner() && (init.token === undefined || init.token === getToken())
      && (isAuthContextCurrent?.() ?? true)) onUnauthorized();
    const msg = typeof data.error === "string" && data.error ? data.error : `HTTP ${res.status}`;
    throw new ApiError(res.status, msg, data, isJson, res.headers?.get("X-Request-Id") ?? undefined, path);
  }
  onResponse?.(res.headers, res.status);
  return compatibleEnvelope(data) as T;
}

export const enc = encodeURIComponent;

export function sessionPath(mid: string, sid: string) {
  return `/sessions/${enc(mid)}/${enc(sid)}`;
}

export function attachSession(mid: string, sid: string): Promise<unknown> {
  return api(`${sessionPath(mid, sid)}/attach`, { method: "POST" });
}

// An <img> can't send a header: the hub also takes the token as a query parameter (it never logs queries).
export function imageURL(mid: string, sid: string, itemID: string) {
  return `${BASE}/api/v1${sessionPath(mid, sid)}/items/${enc(itemID)}/image?token=${enc(getToken())}`;
}

/** An item whole, when the page carries it cut (Item.full_size): `GET …/items/{id}/full`. */
export function fullItem(mid: string, sid: string, itemID: string): Promise<Item> {
  return api<Item>(`${sessionPath(mid, sid)}/items/${enc(itemID)}/full`);
}

/** A Codex visualization item's self-contained HTML fragment: `GET …/items/{id}/visualization`, read as text.
 *  The route legitimately answers text/html, so the edge sign-in-page guard is explicitly opted out. */
export async function visualizationHTML(mid: string, sid: string, itemID: string): Promise<string> {
  const blob = await apiBlob(`${sessionPath(mid, sid)}/items/${enc(itemID)}/visualization`, { allowHtmlResponse: true });
  return blob.text();
}

/** A machine file's bytes by Range (docs/protocol/protocol.md "File preview"): `<video>` / `<audio>` seek through it, a viewer
 * fetches it whole. `inline` lets the browser show it in place (images, media, PDF only; anything else downloads). */
export function fileDownloadURL(mid: string, path: string, cwd?: string, opts: { inline?: boolean } = {}) {
  return `${BASE}/api/v1/machines/${enc(mid)}/file/download?path=${enc(path)}&cwd=${enc(cwd ?? "")}${opts.inline ? "&inline=1" : ""}&token=${enc(getToken())}`;
}

// Fresh legacy URLs must miss the browser cache after this client upgrades. A page-load identity also retries
// after the node upgrades later; the same URL remains reusable while this page is open.
const thumbnailCacheEpoch = `v2-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;

/** A machine file's thumbnail, at most `size` px on its long side; 404 `no_thumbnail` when the machine can't draw it. */
export function fileThumbURL(mid: string, path: string, cwd?: string, size = 256) {
  return `${BASE}/api/v1/machines/${enc(mid)}/file/thumb?path=${enc(path)}&cwd=${enc(cwd ?? "")}&size=${size}&token=${enc(getToken())}&thumb=${thumbnailCacheEpoch}`;
}

export function isAbort(e: unknown): e is DOMException {
  return e instanceof DOMException && e.name === "AbortError";
}

/** Whether the request may not have reached a running hub: fetch failed, or something in front of it answered 502/503/504. */
export function isTransient(e: unknown): boolean {
  if (isNetworkFailure(e)) return true; // fetch: the connection failed (an abort is a DOMException); a page bug is no gap
  // the hub's own errors carry a code or say what was delivered (a machine offline, a node timing out): those are
  // answers, not gaps
  return e instanceof ApiError && [502, 503, 504].includes(e.status) && (!e.isJson || (!e.code && e.body.delivered === undefined));
}

/** A coded business refusal is an answer, not evidence of a hub outage. `delivered: none` marks a plainly offline
 * computer where nothing went out. Original fields stay on ApiError for diagnostics, never interpolated here. */
function businessFailureText(e: ApiError): string | undefined {
  if (e.code === "kb_disabled") return t("lib-api.knowledge_disabled");
  if (e.code === "kb_unavailable") return t("lib-api.knowledge_unavailable");
  if (e.code === "kb_read_only") return t("lib-api.knowledge_read_only");
  if (e.code === "assistant_offline") return t("lib-api.manager_offline");
  if (e.code === "node_unreachable") return t(e.body.delivered === "none" ? "lib-api.computer_offline" : "lib-api.computer_dropped");
  if (e.code === "timeout") return t("lib-api.computer_timeout");
  // a stopped Agent takes no messages (#4754): Resume on its terminal page runs it again
  if (e.code === "agent_stopped") return t("lib-api.agent_stopped");
  // a dialog has the Agent's terminal (#7343): the same refusal comes back until it is closed there, so say that rather
  // than "try again later"
  if (e.code === "dialog_open") return t("lib-api.dialog_open");
  // nothing the hub can call directly is set up for the request (an Inbox draft): where to set one up
  if (e.code === "model_unavailable") return t("lib-api.model_unavailable");
  return undefined;
}

export { HubUnreachableError, isNetworkFailure } from "./network-failure";

/** fetch's own rejection as HubUnreachableError (an abort stays an AbortError): only a request that never got its answer
 *  may later read "Can't reach the hub" (errorText). */
function unreachable(e: unknown): unknown {
  return e instanceof TypeError && !(e instanceof HubUnreachableError) ? new HubUnreachableError(e) : e;
}

/** The answer's body, a connection that drops partway through it counting as never reached (the hub logged its 200). */
async function bodyText(res: Response): Promise<string> {
  try {
    return await res.text();
  } catch (e) {
    throw isAbort(e) ? e : unreachable(e);
  }
}

/** The hub itself answered with a sentence for the reader: a to-do write's refusal (it says what reached the Agent,
 *  `delivered`) with its own error and a business code. Other coded 5xx bodies keep the generic line. */
function hubWorded(e: ApiError): boolean {
  return e.isJson && typeof e.body.error === "string" && e.body.error.trim() !== "" && !!e.code && e.code !== "internal"
    && typeof e.body.delivered === "string";
}

export function errorText(e: unknown): string {
  if (e instanceof ApiError) {
    const business = businessFailureText(e);
    if (business) return business;
    // Keep both the status and the original reason for diagnostics and existing error-specific explanations.
    const status = `HTTP ${e.status}`;
    const detail = e.code === "internal" && e.status >= 500 ? status
      : e.message && e.message !== status ? `${status}: ${e.message}` : status;
    if (e.status === 404 || e.code === "not_found" || /^not[ _]found$/i.test(e.message.trim())) {
      return t("lib-api.not_found", { detail });
    }
    // The hub's own answer says why in the reader's language (an accept its Agent did not take, #17431): that, not
    // "the service couldn't complete this request". The generic line is for what stands in front of the hub (an empty
    // or non-JSON gateway page) and for an internal error, whose words are not for the reader.
    if (e.status === 502 || e.status === 503 || e.status === 504) {
      return hubWorded(e) ? e.message : t("lib-api.hub_unavailable", { detail });
    }
    if (e.status >= 500 && e.status < 600) return t("lib-api.server_error", { detail });
    return e.message;
  }
  if (isNetworkFailure(e)) return t("lib-api.cant_reach_hub");
  // a TypeError from this page's own code (a row it did not expect) is not a network gap: say what broke
  if (isProgramError(e)) return t("lib-api.page_error", { detail: e.message });
  return e instanceof Error ? e.message : String(e);
}
