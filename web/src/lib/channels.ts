// Settings › Extensions › Channels (docs/extensions/channels.md): every way of talking to Persona — the apps, the Feishu bot, WeChat, /agent in
// Feishu, voice, and the chat app bots extensions contribute — as GET /api/v1/channels lists them; one channel's page
// (its switch, binding a step at a time, where its messages go, what it does, a test message), and the source mark on
// a message that came from one (its client_msg_id is `<channel>-<message id>`).
//
// The pure parts (state words, sections, the source of a message, the bind steps) are plain functions for the tests.

import { formatList, t } from "../i18n";
import { api, enc } from "./api";
import type { ConnectorPrompt } from "./connectors";
import type { ScopedAssistantClient } from "./scoped-assistant-api";

// ── wire types (server/internal/channel) ─────────────────

export type ChannelState = "connected" | "not_connected" | "error" | "disabled" | "not_installed";

export interface ChannelCapabilities {
  images_in?: boolean;
  files_in?: boolean;
  voice_in?: boolean;
  format?: "markdown" | "plain";
  max_text?: number;
  asks?: "none" | "text" | "buttons";
  push?: boolean;
  reply_only?: boolean;
  typing?: boolean;
  progress?: boolean;
  threads?: boolean;
}

export interface ChannelTarget {
  kind: "main" | "new" | "conversation" | "agent";
  machine_id?: string;
  session_id?: string;
  title?: string;
}

export type ChannelThreadMode = "channel_default" | "per_group_and_topic" | "per_topic";

export const CHANNEL_THREAD_MODES: readonly ChannelThreadMode[] = ["channel_default", "per_group_and_topic", "per_topic"];

/** Matches channel.MaxThreadIdleHours on the hub (10 years). 0 means the default 30-day timeout. */
export const MAX_THREAD_IDLE_HOURS = 3650 * 24;

export interface ChannelFeature {
  id: string;
  title: string;
  description?: string;
  on: boolean;
}

/** One of GET /api/v1/channels (channel.View). */
export interface Channel {
  id: string;
  name: string;
  kind: "app" | "im" | "mention" | "voice";
  source: "builtin" | "plugin" | "market";
  plugin?: string;
  market?: string;
  installed: boolean;
  enabled: boolean;
  state: ChannelState;
  error?: string;
  account?: { id?: string; name: string };
  description?: string;
  capabilities: ChannelCapabilities;
  target?: ChannelTarget;
  /** Group/topic routing mode when integrations_v2 is enabled; omitted on older hubs. */
  thread_mode?: ChannelThreadMode;
  /** Inactivity timeout in hours before a routed group/topic starts a new conversation (0 = default 30 days). */
  thread_idle_hours?: number;
  features?: ChannelFeature[];
  bind: "none" | "qr" | "code" | "fields" | "config";
  surfaces?: string[];
  last_event_at?: string;
  read_only?: boolean;
}

export interface ChannelTargetOverride {
  mode: "topics" | "groups_and_topics" | "threads" | (string & {});
}

/** GET /api/v1/channels/{id} (channel.Detail). */
export interface ChannelDetail extends Channel {
  waiting: { answers: number; asks: number };
  conversations?: { machine_id: string; session_id: string; title?: string; at: string }[];
  /** The channel's own full page in the app ("settings/connectors/feishu-bot", "settings/plugins/<id>"). */
  settings_url?: string;
  /** Set when topic or thread routing currently takes precedence over the channel's default target. */
  target_override?: ChannelTargetOverride;
}

/** A bind step (connector.Prompt); a channel adds pair_code, a code to send to the bot, to the kinds a connector has. */
export type ChannelPrompt = ConnectorPrompt;

// ── the hub's interface ──────────────────────────────────

export function channelDetail(id: string, client?: ScopedAssistantClient): Promise<ChannelDetail> {
  const request = client?.request ?? api;
  return request<ChannelDetail>(`/channels/${enc(id)}`);
}

export interface ChannelUpdateBody {
  enabled?: boolean;
  target?: ChannelTarget;
  features?: Record<string, boolean>;
  thread_mode?: ChannelThreadMode;
  thread_idle_hours?: number;
}

export function updateChannel(id: string, body: ChannelUpdateBody, client?: ScopedAssistantClient): Promise<ChannelDetail> {
  const request = client?.request ?? api;
  return request<ChannelDetail>(`/channels/${enc(id)}`, { method: "PUT", json: body });
}

/** Normalizes a channel's thread_mode, falling back to channel_default when absent or unknown. */
export function normalizeThreadMode(mode?: string): ChannelThreadMode {
  if (mode === "per_group_and_topic" || mode === "per_topic" || mode === "channel_default") return mode;
  return "channel_default";
}

/** Whether the hub advertised thread_mode support on this channel (older hubs omit it). */
export function supportsThreadRouting(c: Pick<Channel, "thread_mode">): boolean {
  return typeof c.thread_mode === "string" && c.thread_mode.length > 0;
}

/** Whether hours is a valid thread_idle_hours value (0 = default 30 days, up to MAX_THREAD_IDLE_HOURS). */
export function isValidThreadIdleHours(hours: number): boolean {
  return Number.isInteger(hours) && hours >= 0 && hours <= MAX_THREAD_IDLE_HOURS;
}

export function bindChannel(id: string, step: string, values: Record<string, string>, client?: ScopedAssistantClient): Promise<ChannelPrompt> {
  const request = client?.request ?? api;
  return request<ChannelPrompt>(`/channels/${enc(id)}/bind`, { method: "POST", json: { step, values } });
}

export function unbindChannel(id: string, client?: ScopedAssistantClient): Promise<ChannelDetail> {
  const request = client?.request ?? api;
  return request<ChannelDetail>(`/channels/${enc(id)}/binding`, { method: "DELETE" });
}

export interface ChannelTestResult {
  ok: boolean;
  reply_only?: boolean;
  note?: string;
}

export function testChannel(id: string, client?: ScopedAssistantClient): Promise<ChannelTestResult> {
  const request = client?.request ?? api;
  return request<ChannelTestResult>(`/channels/${enc(id)}/test`, { method: "POST" });
}

// ── words ────────────────────────────────────────────────

export type ChannelTone = "green" | "red" | "idle";

export function channelStateText(c: Channel): string {
  switch (c.state) {
    case "connected":
      return t("lib-channels.state_connected");
    case "error":
      return t("lib-channels.state_error");
    case "disabled":
      return t("lib-channels.state_disabled");
    case "not_installed":
      return t("lib-channels.state_not_installed");
  }
  return t("lib-channels.state_not_connected");
}

export function channelTone(c: Channel): ChannelTone {
  if (c.state === "connected") return "green";
  if (c.state === "error") return "red";
  return "idle";
}

/** The row's second line: the error, the account, else what it is. */
export function channelSubtitle(c: Channel): string {
  if (c.state === "error" && c.error) return c.error;
  if (c.kind === "app" && c.surfaces?.length) return c.surfaces.join(" · ");
  if (c.account?.name && c.state !== "not_installed") return c.account.name;
  return c.description ?? "";
}

/** The list's sections: the apps, the installed ones, those a market has. Empty sections are left out. */
export function channelSections(list: Channel[]): { title: string; footer?: string; channels: Channel[] }[] {
  const apps = list.filter((c) => c.kind === "app");
  const installed = list.filter((c) => c.kind !== "app" && c.source !== "market");
  const market = list.filter((c) => c.source === "market");
  return [
    { title: "Persona", footer: t("lib-channels.section_apps_footer"), channels: apps },
    { title: t("lib-channels.section_installed"), footer: t("lib-channels.section_installed_footer"), channels: installed },
    { title: t("lib-channels.section_market"), footer: t("lib-channels.section_market_footer"), channels: market },
  ].filter((s) => s.channels.length > 0);
}

/** "3 connected", with the ones that went wrong. */
export function channelsSummary(list: Channel[]): string {
  const on = list.filter((c) => c.kind !== "app" && c.state === "connected").length;
  const bad = list.filter((c) => c.state === "error").length;
  return bad > 0 ? t("settings-channels.summary_with_errors", { on, bad }) : t("settings-channels.summary", { count: on });
}

export function targetText(target?: ChannelTarget): string {
  switch (target?.kind) {
    case "new":
      return t("lib-channels.target_new");
    case "conversation":
      return target.title || t("lib-channels.target_conversation");
    case "agent":
      return target.title ? t("lib-channels.target_agent_titled", { title: target.title }) : t("lib-channels.target_agent");
  }
  return t("lib-channels.target_main");
}

/** What the channel can do, in a few words for its page. */
export function capabilityText(c: ChannelCapabilities): string[] {
  const out: string[] = [];
  const takes = [t("lib-channels.takes_text"), c.images_in && t("lib-channels.takes_images"), c.files_in && t("lib-channels.takes_files"), c.voice_in && t("lib-channels.takes_voice")].filter((x): x is string => !!x);
  out.push(t("lib-channels.takes", { kinds: formatList(takes, { type: "conjunction", style: "narrow" }) }));
  out.push(c.format === "plain" ? t("lib-channels.format_plain") : t("lib-channels.format_markdown"));
  if (c.max_text) out.push(t("lib-channels.max_text", { count: c.max_text }));
  if (c.asks === "text") out.push(t("lib-channels.asks_text"));
  else if (c.asks === "buttons") out.push(t("lib-channels.asks_buttons"));
  if (c.reply_only) out.push(t("lib-channels.reply_only"));
  if (c.typing) out.push(t("lib-channels.typing"));
  if (c.progress) out.push(t("lib-channels.progress"));
  return out;
}

export function bindTitle(c: Pick<Channel, "bind">): string {
  switch (c.bind) {
    case "qr":
      return t("lib-channels.bind_qr");
    case "code":
      return t("lib-channels.bind_code");
  }
  return t("lib-channels.bind");
}

export function isBound(c: Channel): boolean {
  return c.state === "connected" || (c.state === "error" && !!c.account);
}

/** Whether the page can bind it: it binds here, is on, and is not bound. */
export function canBind(c: Channel): boolean {
  return !c.read_only && c.source !== "market" && c.bind !== "none" && c.bind !== "config" && c.enabled && !isBound(c);
}

/** A step the app sends again by itself (a QR code to scan, a code to enter or send), not one with fields. */
export function isPolled(p?: ChannelPrompt | null): boolean {
  return !!p && !p.done && (p.kind === "qr" || p.kind === "pair_code" || p.kind === "device_code");
}

/** Where a channel's settings_url ("settings/connectors/feishu-bot" or "settings/persona#repositories") opens in the settings page. */
export function settingsTarget(url: string): { tab: string; sub: string | null; anchor?: string } | null {
  const hashIdx = url.indexOf("#");
  const base = hashIdx >= 0 ? url.slice(0, hashIdx) : url;
  const anchor = hashIdx >= 0 ? url.slice(hashIdx + 1) : "";
  const m = /^settings\/([a-z-]+)(?:\/(.+))?$/.exec(base);
  if (!m) return null;
  if (anchor && !/^[a-z0-9-]+$/i.test(anchor)) return null;
  return { tab: settingsTab(m[1]), sub: m[2] ?? null, ...(anchor ? { anchor } : {}) };
}

/** A settings tab by its name now: `plugins` (a hub or a link from before #1844 / #1845) is `extensions`. */
export function settingsTab(tab: string): string;
export function settingsTab(tab: string | null): string | null;
export function settingsTab(tab: string | null): string | null {
  return tab === "plugins" ? "extensions" : tab;
}

// The list and the source marks conversations draw live in channel-source.ts, off Settings' code (first screen).
export { channelOf, conversationChannel, forgetChannels, listChannels, useChannels } from "./channel-source";
