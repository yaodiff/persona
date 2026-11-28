// Settings › Claude Code / Codex settings (the node's whitelist of ~/.claude/settings.json and ~/.codex/config.toml keys,
// iOS ClaudeSettingsView) and Settings › Connectors › Mail's add form (iOS MailAccountForm, docs/extensions/mail.md): the pure part.
import { hasKey, t } from "../i18n";
import { effortTitle } from "./session-actions";
import type { Machine } from "./types";

// ── agent settings ──

export type AgentKind = "claude" | "codex" | "agy" | "omp" | "grok";

export type SettingValue = boolean | string | null;

/** One editable option, as GET /machines/{mid}/claude-settings (or codex-settings) lists it. value null = unset. */
export interface AgentSetting {
  key: string;
  title: string;
  detail: string;
  type: "bool" | "enum" | "string" | string;
  options?: string[];
  default?: SettingValue;
  value?: SettingValue;
  /** A stored value the agent ignores; value is null so the control shows the default. */
  ignored_value?: string;
}

export const AGENT_TEXT: Record<AgentKind, { name: string; path: string; file: string; footer: string }> = {
  claude: {
    name: "Claude Code",
    path: "claude-settings",
    file: "~/.claude/settings.json",
    footer: t("lib-agent-settings.claude_footer"),
  },
  codex: {
    name: "Codex",
    path: "codex-settings",
    file: "~/.codex/config.toml",
    footer: t("lib-agent-settings.codex_footer"),
  },
  agy: {
    name: "Antigravity",
    path: "agy-settings",
    file: "~/.gemini/antigravity-cli/settings.json",
    footer: t("lib-agent-settings.agy_footer"),
  },
  omp: {
    name: "omp",
    path: "omp-settings",
    file: "~/.omp/agent/config.yml",
    footer: t("lib-agent-settings.omp_footer"),
  },
  grok: {
    name: "Grok",
    path: "grok-settings",
    file: "~/.grok/config.toml",
    footer: t("lib-agent-settings.grok_footer"),
  },
};

// Claude Code's options in plain words (#3599). The node sends a title and detail too (claudeconfig.go); the page's
// language wins when the catalog has the key, so a key the web does not know yet still reads as the node wrote it.
function catalogText(key: string, fallback: string): string {
  return hasKey(key) ? t(key) : fallback;
}

/** What a row is called. */
export function settingTitle(s: Pick<AgentSetting, "key" | "title">): string {
  return catalogText(`lib-agent-settings.setting_${s.key}`, s.title);
}

/** What a row does, in a sentence. */
export function settingDetail(s: Pick<AgentSetting, "key" | "detail" | "ignored_value">): string {
  if (s.key === "effortLevel" && s.ignored_value === "max") return t("lib-agent-settings.setting_effortLevel_ignored_max");
  return catalogText(`lib-agent-settings.setting_${s.key}_detail`, s.detail);
}

// Codex's option values under one prefix: { "on-request": "lib-agent-settings.approval_on_request" }.
function codexOptions(prefix: string, options: string[]): Record<string, string> {
  return Object.fromEntries(options.map((o) => [o, `lib-agent-settings.${prefix}_${o.replace(/-/g, "_")}`]));
}

// An option value's name: effort levels as the session menu calls them, the update channels and Codex's
// config.toml values (#3855) by name.
const OPTION_KEYS: Record<string, Record<string, string>> = {
  service_tier: { default: "lib-agent-settings.speed_standard", fast: "chat-sessiondialogs.speed_fast", priority: "chat-sessiondialogs.speed_fast" },
  autoUpdatesChannel: { latest: "lib-agent-settings.channel_latest", stable: "lib-agent-settings.channel_stable" },
  // Claude Code's default mode (#3026), named as the session menu names the modes.
  "permissions.defaultMode": {
    default: "lib-session-actions.mode_default", acceptEdits: "lib-session-actions.mode_accept_edits", plan: "lib-session-actions.mode_plan",
    auto: "lib-session-actions.mode_auto", bypassPermissions: "lib-session-actions.mode_bypass_permissions",
  },
  model_reasoning_summary: codexOptions("summary", ["auto", "concise", "detailed", "none"]),
  model_verbosity: codexOptions("verbosity", ["low", "medium", "high"]),
  approval_policy: codexOptions("approval", ["untrusted", "on-failure", "on-request", "never"]),
  sandbox_mode: codexOptions("sandbox", ["read-only", "workspace-write", "danger-full-access"]),
};

/** How an enum option reads; an option with no name (a new one, or another agent's) stays as written. */
export function optionText(key: string, option: string): string {
  if (key === "effortLevel" || key === "model_reasoning_effort" || key === "defaultThinkingLevel") return effortTitle(option);
  const k = OPTION_KEYS[key]?.[option];
  return k ? catalogText(k, option) : option;
}

/** The value in effect: what is set, else the default. */
export function effective(s: AgentSetting): SettingValue {
  return s.value ?? s.default ?? null;
}

export function isUnset(s: AgentSetting): boolean {
  return s.value === null || s.value === undefined;
}

/** An optimistic change clears the old stored-value warning until the node answers. */
export function withValue(list: AgentSetting[], key: string, value: SettingValue): AgentSetting[] {
  return list.map((s) => (s.key === key ? { ...s, value, ignored_value: undefined } : s));
}

/** What a text field sends: an empty field is "back to the default" (null), not an empty string written to the file. */
export function textValue(input: string): string | null {
  const v = input.trim();
  return v ? v : null;
}

/** The machine the page edits: the remembered one while it is online, else the first online one, else "". */
export function pickMachine(machines: Pick<Machine, "id" | "online">[], remembered: string): string {
  if (machines.some((m) => m.id === remembered && m.online)) return remembered;
  return machines.find((m) => m.online)?.id ?? "";
}

export interface ApplyResult {
  id: string;
  name: string;
  error?: string;
}

/** One line for a change sent to several machines: "applied to 3 machines", or which failed and why. */
export function applySummary(results: ApplyResult[]): { ok: boolean; text: string } {
  const failed = results.filter((r) => r.error);
  if (!failed.length) return { ok: true, text: results.length > 1 ? t("lib-agent-settings.applied_to", { count: results.length }) : t("lib-agent-settings.saved") };
  const done = results.length - failed.length;
  const why = failed.map((r) => t("lib-agent-settings.failure_item", { name: r.name, error: r.error ?? "" })).join(t("lib-agent-settings.failure_separator"));
  return { ok: false, text: done ? t("lib-agent-settings.partly_applied", { done, failed: failed.length, why }) : t("lib-agent-settings.not_applied", { why }) };
}

/** How a value reads in a message: on / off, the option's name (given the setting's key), or "default". */
export function valueText(v: SettingValue, key?: string): string {
  if (v === null || v === undefined || v === "") return t("lib-agent-settings.value_default");
  if (typeof v === "boolean") return v ? t("lib-agent-settings.value_on") : t("lib-agent-settings.value_off");
  return key ? optionText(key, v) : v;
}

// ── Claude Code › blocked config directories (GET /machines/{mid}/claude-dirs, docs/integrations/claude-integration.md "Blocking configuration directories") ──

/** One config directory of a computer (CLAUDE_CONFIG_DIR), as the hub lists it. */
export interface ClaudeDir {
  path: string;
  default?: boolean;
  blocked?: boolean;
  blocked_by?: string;
  source: "default" | "config" | "found" | string;
  running: number;
  sessions?: number;
}

export interface ClaudeDirsState {
  dirs: ClaudeDir[];
  blocked: string[];
  home?: string;
  live: boolean;
}

/** The path as the user reads it: the computer's home as ~. */
export function shortPath(path: string, home?: string): string {
  if (home && (path === home || path.startsWith(home + "/"))) return "~" + path.slice(home.length);
  return path;
}

/** What a row says under the path: where it came from, how many sessions, how many claudes run from it. */
export function dirMeta(d: ClaudeDir, home?: string): string {
  const parts: string[] = [];
  if (d.default) parts.push(t("lib-agent-settings.dir_default"));
  else if (d.source === "config") parts.push(t("lib-agent-settings.dir_config"));
  else parts.push(t("lib-agent-settings.dir_found"));
  if (d.sessions) parts.push(t("lib-agent-settings.dir_sessions", { count: d.sessions }));
  if (d.running) parts.push(t("lib-agent-settings.dir_running", { count: d.running }));
  if (d.blocked && d.blocked_by && !samePath(d.blocked_by, d.path, home)) parts.push(t("lib-agent-settings.dir_blocked_by", { rule: d.blocked_by }));
  return parts.join(" · ");
}

/** The patterns after a row's "Block" / "Unblock": blocking adds the directory's own path; unblocking lifts the rule that
 *  blocks it (a glob lifts every directory it covers — the row says which rule). */
export function toggledPatterns(state: ClaudeDirsState, d: ClaudeDir): string[] {
  if (!d.blocked) return state.blocked.includes(d.path) ? state.blocked : [...state.blocked, shortPath(d.path, state.home)];
  const rule = d.blocked_by ?? d.path;
  return state.blocked.filter((p) => p !== rule);
}

/** Why a typed pattern cannot be added ("" = it can). */
export function patternError(input: string, existing: string[]): string {
  const p = input.trim().replace(/\/+$/, "");
  if (!p) return t("lib-agent-settings.pattern_empty");
  if (!p.startsWith("/") && !p.startsWith("~/")) return t("lib-agent-settings.pattern_prefix");
  if (p === "~" || p === "/") return t("lib-agent-settings.pattern_root");
  if (existing.includes(p)) return t("lib-agent-settings.pattern_exists");
  return "";
}

/** The patterns with a typed one added (trimmed, no trailing /). */
export function withPattern(existing: string[], input: string): string[] {
  return [...existing, input.trim().replace(/\/+$/, "")];
}

/** Rules that block none of the listed directories (a glob nothing matches yet, a directory not seen): listed on
 *  their own so they can be deleted; the others are lifted from the rows they block. */
export function looseRules(state: ClaudeDirsState): string[] {
  return state.blocked.filter((p) => !state.dirs.some((d) => d.blocked && (d.blocked_by === p || samePath(p, d.path, state.home))));
}

/** A pattern written as this directory's path, with ~ or without. */
function samePath(pattern: string, path: string, home?: string): boolean {
  return pattern === path || pattern === shortPath(path, home);
}

export interface MailAccount {
  id: string;
  name?: string;
  email: string;
  provider?: string;
  auth?: string;
  imap_host?: string;
  status?: string;
  error?: string;
}

/** "Connectors"' summary of the accounts: "2 accounts", "2 · with errors", "not added". */
export function mailSummary(accounts: MailAccount[] | null): string {
  if (!accounts) return "";
  if (!accounts.length) return t("lib-agent-settings.mail_none");
  if (accounts.some((a) => a.status === "error")) return t("lib-agent-settings.mail_with_error", { count: accounts.length });
  return t("lib-agent-settings.mail_accounts", { count: accounts.length });
}
