// Settings › Lark › "Daily sync to your knowledge base": the owner lets a bot's app read Feishu / Lark as them, and the
// hub reads what changed every day into the knowledge base (the larksync connector). The routes live under the bot's own
// /api/v1/bots/feishu prefix, so a page that pins /bots to a manager (IntegrationDetail) reaches the same one here.
// Pure helpers and the API calls; the section is views/settings/LarkSyncSection.tsx, the OAuth return page
// views/settings/LarkOAuthReturn.tsx.
import { t } from "../i18n";
import { ApiError, api, enc } from "./api";
import { BASE, stripBase } from "./base";
import type { ConnectorPrompt } from "./connectors";
import { safeWebURL } from "./format";
import { MANAGER_HEADER, managerHeaderFor } from "./manager-choice";

/** What a bot's app may read as the owner (larksyncwire TierDocs / TierFull). */
export const TIER_DOCS = "docs_standard";
export const TIER_FULL = "full_with_im";
export const TIERS = [TIER_FULL, TIER_DOCS] as const;

/** The path the open platform sends the owner back to after authorizing; the hub accepts only this path. */
export const OAUTH_RETURN_PATH = "/lark-oauth";

/** Re-authorizing is suggested once this many days or fewer are left of the platform's one-year authorization. */
export const REAUTH_SOON_DAYS = 14;

export interface LarkSyncAccount {
  id: string;
  label: string;
  brand?: string;
  name?: string;
}

export interface LarkSyncBot {
  bot_id: string;
  label: string;
  app_id?: string;
  domain?: string;
  /** chat_and_sync (the chat bot) or sync_only. */
  role?: string;
  tier?: string;
  /** ready | awaiting_approval | degraded; absent: nothing known. */
  approval?: string;
  /** The chat bot of bots.feishu, listed first. */
  primary?: boolean;
  has_secret?: boolean;
  /** A registration (its QR step) is under way: asked again with the register route. */
  registering?: boolean;
}

export interface LarkSyncBinding {
  key: string;
  bot_id: string;
  account_id: string;
  label?: string;
  tier?: string;
  enabled: boolean;
  /** larksyncwire Health*: ok | unauthorized | needs_reauth | rate_limited | scope_missing | failing | awaiting_approval. */
  health: string;
  error?: string;
  granted_scopes?: string[];
  authorized_at?: string;
  /** Days left of the authorization; absent when unknown. */
  reauth_due_days?: number;
  last_run_at?: string;
  next_run_at?: string;
  last_messages?: number;
  last_docs?: number;
  running?: boolean;
}

export interface LarkSyncView {
  available: boolean;
  /** The local hour the daily run starts at. */
  schedule_hour: number;
  /** Whether chat-enabled bindings run a lightweight 15-minute incremental chat sync; absent on older hubs. */
  im_poll?: boolean;
  accounts: LarkSyncAccount[];
  bots: LarkSyncBot[];
  bindings: LarkSyncBinding[];
}

/** How the hub lets the owner authorize: a browser redirect, a code on screen (device flow), or the redirect URL to add
 *  by hand in the app's console first (the app could not be configured for it). */
export interface LarkSyncAuthorize {
  mode: "browser" | "device" | "manual";
  url?: string;
  prompt?: ConnectorPrompt;
  redirect_uri?: string;
  console_url?: string;
}

/** A hub answer as a sync view, or null when it is none: an older hub that does not have the sync (404, or another
 *  route's answer), or one without the Lark module (`available` false). Absent ⇒ the section hides. */
export function asSyncView(raw: unknown): LarkSyncView | null {
  if (!raw || typeof raw !== "object") return null;
  const v = raw as Partial<LarkSyncView>;
  if (v.available !== true || !Array.isArray(v.bots) || !Array.isArray(v.bindings)) return null;
  return {
    available: true,
    schedule_hour: typeof v.schedule_hour === "number" && v.schedule_hour >= 0 && v.schedule_hour < 24 ? Math.floor(v.schedule_hour) : 3,
    ...(typeof v.im_poll === "boolean" ? { im_poll: v.im_poll } : {}),
    accounts: Array.isArray(v.accounts) ? v.accounts : [],
    bots: v.bots,
    bindings: v.bindings,
  };
}

/** A read that failed because the hub has no such route: an older hub. Anything else is a real failure. */
export function isAbsent(e: unknown): boolean {
  return e instanceof ApiError && (e.status === 404 || e.status === 405 || e.status === 501);
}

const SYNC = "/bots/feishu/sync";
const bot = (id: string) => `${SYNC}/bots/${enc(id)}`;
const binding = (key: string) => `${SYNC}/bindings/${enc(key)}`;
const view = (raw: unknown) => asSyncView(raw);

/** The sync view; null on a hub without it. */
export async function syncView(): Promise<LarkSyncView | null> {
  try {
    return view(await api<unknown>(SYNC));
  } catch (e) {
    if (isAbsent(e)) return null;
    throw e;
  }
}

export const setSyncSchedule = (hour: number) => api<unknown>(SYNC, { method: "PUT", json: { schedule_hour: hour } }).then(view);

export const setSyncIMPoll = (on: boolean) => api<unknown>(SYNC, { method: "PUT", json: { im_poll: on } }).then(view);

/** Starts creating a sync-only bot's app by scanning: the prompt carries the QR code. */
export const createSyncBot = (body: { label: string; tier: string; region?: string }) =>
  api<{ bot_id: string; prompt: ConnectorPrompt }>(`${SYNC}/bots`, { method: "POST", json: body });

/** Asks a registration (a new bot's, or adding permissions to an app) again: done once the owner confirmed it. */
export const registerSyncBot = (id: string, step: string) => api<ConnectorPrompt>(`${bot(id)}/register`, { method: "POST", json: { step } });

/** Adds a tier's read permissions to an existing app (`primary`: the chat bot's), by scanning. */
export const addSyncScopes = (id: string, tier: string) => api<ConnectorPrompt>(`${bot(id)}/scopes`, { method: "POST", json: { tier } });

export const removeSyncBot = (id: string) => api<unknown>(bot(id), { method: "DELETE" }).then(view);

export const authorizeSyncBot = (id: string, redirectURI: string, tier?: string) =>
  api<LarkSyncAuthorize>(`${bot(id)}/authorize`, { method: "POST", json: tier ? { redirect_uri: redirectURI, tier } : { redirect_uri: redirectURI } });

export const pollSyncAuthorize = (id: string) => api<ConnectorPrompt>(`${bot(id)}/authorize/poll`, { method: "POST" });

/** Hands the platform's code back to the hub (the OAuth return page). `manager`: the one the authorization began on. */
export const finishSyncOAuth = (state: string, code: string, manager?: string) =>
  api<unknown>(`${SYNC}/oauth`, { method: "POST", json: { state, code }, ...(manager ? { headers: { [MANAGER_HEADER]: manager } } : {}) }).then(view);

export const updateSyncBinding = (key: string, patch: { enabled?: boolean; label?: string; tier?: string }) =>
  api<unknown>(binding(key), { method: "PUT", json: patch }).then(view);

export const removeSyncBinding = (key: string) => api<unknown>(binding(key), { method: "DELETE" }).then(view);

export const runSyncBinding = (key: string) => api<unknown>(`${binding(key)}/run`, { method: "POST" }).then(view);

/** Where the platform sends the owner back: this page's origin, under the sub-path the hub is served at. */
export function oauthRedirectURI(origin = location.origin, base = BASE): string {
  return `${origin}${base}${OAUTH_RETURN_PATH}`;
}

/** The bots in the order the page lists them: the chat bot first, then the sync bots as the hub sent them. */
export function orderedBots(v: LarkSyncView): LarkSyncBot[] {
  return [...v.bots.filter((b) => b.primary), ...v.bots.filter((b) => !b.primary)];
}

export function bindingsOf(v: LarkSyncView, botID: string): LarkSyncBinding[] {
  return v.bindings.filter((b) => b.bot_id === botID);
}

/** A binding's name: its own label, else its account's, else the account's name, else a numbered fallback. */
export function bindingTitle(v: LarkSyncView, b: LarkSyncBinding): string {
  const at = v.accounts.findIndex((a) => a.id === b.account_id);
  const account = at >= 0 ? v.accounts[at] : undefined;
  const n = at >= 0 ? at + 1 : v.bindings.indexOf(b) + 1;
  return b.label || account?.label || account?.name || t("settings-larksync.account_fallback", { n: Math.max(1, n) });
}

export function botTitle(b: LarkSyncBot): string {
  return b.primary ? t("settings-larksync.chat_bot") : b.label || t("settings-larksync.sync_bot");
}

export type Tone = "green" | "red" | "orange" | "accent" | undefined;

/** The plain words and the pill tone of a binding's state; a running sync wins over the last run's outcome. */
export function healthOf(b: Pick<LarkSyncBinding, "health" | "running" | "enabled">): { text: string; tone: Tone } {
  if (b.running) return { text: t("settings-larksync.health_running"), tone: "accent" };
  switch (b.health) {
    case "ok":
      return b.enabled ? { text: t("settings-larksync.health_ok"), tone: "green" } : { text: t("settings-larksync.health_paused"), tone: undefined };
    case "unauthorized":
      return { text: t("settings-larksync.health_unauthorized"), tone: undefined };
    case "needs_reauth":
      return { text: t("settings-larksync.health_needs_reauth"), tone: "red" };
    case "rate_limited":
      return { text: t("settings-larksync.health_rate_limited"), tone: "orange" };
    case "scope_missing":
      return { text: t("settings-larksync.health_scope_missing"), tone: "orange" };
    case "awaiting_approval":
      return { text: t("settings-larksync.health_awaiting"), tone: "orange" };
    case "failing":
      return { text: t("settings-larksync.health_failing"), tone: "red" };
    default:
      return { text: t("settings-larksync.health_unknown"), tone: undefined };
  }
}

/** The pill for a bot's app, when it says something: waiting for the admin, or set up without some permissions. */
export function approvalOf(b: LarkSyncBot): { text: string; tone: Tone } | null {
  if (b.approval === "awaiting_approval") return { text: t("settings-larksync.approval_awaiting"), tone: "orange" };
  if (b.approval === "degraded") return { text: t("settings-larksync.approval_degraded"), tone: "orange" };
  return null;
}

export function tierText(tier?: string): string {
  return tier === TIER_FULL ? t("settings-larksync.tier_full") : t("settings-larksync.tier_docs");
}

/** The binding has to be authorized (again) before it can sync. */
export function needsAuthorize(b: LarkSyncBinding): boolean {
  return b.health === "needs_reauth" || b.health === "unauthorized" || b.health === "scope_missing";
}

/** Days left when the authorization ends soon enough to say so; null otherwise. */
export function reauthSoon(b: LarkSyncBinding): number | null {
  const days = b.reauth_due_days;
  if (typeof days !== "number" || !Number.isFinite(days) || days > REAUTH_SOON_DAYS || needsAuthorize(b)) return null;
  return Math.max(0, Math.floor(days));
}

/** The return page's query: the platform's `code` and `state`, or its `error` when the owner declined. */
export function oauthReturn(search: string): { code: string; state: string; error: string } {
  const q = new URLSearchParams(search);
  return { code: q.get("code") ?? "", state: q.get("state") ?? "", error: q.get("error") ?? "" };
}

// Where the return page goes back to, and which manager the authorization began on: kept in this tab's session storage
// across the trip to the open platform (another tab, or none kept, goes to the Lark settings on the default manager).
const RETURN_KEY = `persona.larkSyncReturn${BASE ? `:${BASE}` : ""}`;
/** The Lark settings, on either settings layout (nav.ts redirects the legacy address to the integration page). */
export const LARK_SETTINGS_PATH = "/settings?tab=connectors&connector=feishu-bot";

export function rememberReturn(back = stripBase(location.pathname) + location.search, manager = managerHeaderFor(SYNC)) {
  try {
    sessionStorage.setItem(RETURN_KEY, JSON.stringify({ back, manager: manager ?? "" }));
  } catch {
    // no storage (a private window): the return page goes to the Lark settings
  }
}

/** What rememberReturn kept: the page to go back to (an app path, never another origin) and the manager. Reading
 *  leaves it in place (a page may draw twice); forgetReturn drops it once the authorization is through. */
export function readReturn(): { back: string; manager?: string } {
  let raw: string | null = null;
  try {
    raw = sessionStorage.getItem(RETURN_KEY);
  } catch {
    // no storage
  }
  try {
    const v = raw ? (JSON.parse(raw) as { back?: unknown; manager?: unknown }) : {};
    const back = typeof v.back === "string" && v.back.startsWith("/") && !v.back.startsWith("//") ? v.back : LARK_SETTINGS_PATH;
    return typeof v.manager === "string" && v.manager ? { back, manager: v.manager } : { back };
  } catch {
    return { back: LARK_SETTINGS_PATH };
  }
}

export function forgetReturn() {
  try {
    sessionStorage.removeItem(RETURN_KEY);
  } catch {
    // no storage
  }
}

/** Leaves the page for the open platform's authorization (a function of its own so tests can catch it). */
export function leaveFor(url: string) {
  const safe = safeWebURL(url, true);
  if (!safe) return;
  location.assign(safe);
}
