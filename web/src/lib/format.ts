import { parseAgentPhase, type AgentPhase, type AgentView } from "./agent-view-wire";
import { inferAgentView } from "./legacy-agent-view";
import type { AgentState, StatusMarkState, Interaction, Session, SessionCapabilities } from "./types";
import { formatDate, formatRelative, t } from "../i18n";
import { normalizeTraeCliName, sessionAgentName } from "./agent-kind";
import { usageLimitLabel } from "./usage-limit";
import { workLine } from "./work";
import { closeoutLine, closeoutSuffix, shownCloseout } from "./closeout";

// Missing session metadata is not permission to write. A native row (no source_kind) that carries capabilities is held
// to them, as every other source is: a stopped Agent's row says read and terminal only (#4754). A native row without
// them (every row from an older node) keeps its existing behavior: it may do everything.
export function sessionCan(s: Session | undefined, operation: keyof SessionCapabilities): boolean {
  if (!s || s.deleted) return false;
  if (operation === "attach") return Boolean(s.source_kind && s.capabilities?.attach === true && !s.attached);
  if (operation === "close") return s.capabilities?.close ?? sessionCan(s, "terminal");
  if (!s.source_kind && !s.capabilities) return true;
  return s.capabilities?.[operation] === true;
}

/** Rename (#6294): the node's `capabilities.rename` where it says (a runtime's session, which has no terminal), else as
 *  before: a row that can send and is the user's own Agent or a Persona conversation (no other source). */
export function sessionCanRename(s: Session | undefined): boolean {
  if (!s) return false;
  const said = s.capabilities?.rename;
  if (typeof said === "boolean") return said;
  return sessionCan(s, "send") && (Boolean(s.assistant) || !s.source_kind);
}

/** A stopped Agent (#4754, docs/integrations/engines.md "Agents run in a terminal"): an Agent of a terminal engine that is
 *  not running. Its row keeps its conversation and its terminal and takes no messages; the one way back is Resume on its
 *  terminal page. Only a node that knows it says so (capabilities read and terminal, no send): an older node's offline
 *  row has no capabilities and keeps today's behavior. A Codex thread nothing has open is marked the same way (node
 *  codex_registry.go, #5521), though its row keeps its Codex history source. */
export function isStoppedAgent(s: Session | undefined): boolean {
  if (!s || s.assistant || (s.source_kind && s.source_kind !== "codex_history")) return false;
  if (s.status !== "offline" && s.status !== "interrupted") return false;
  const c = s.capabilities;
  return Boolean(c && c.read === true && c.terminal === true && !c.send);
}

/** A script request explicitly outside a session has no session-source approval
 *  policy. A nonempty session id still requires its real session metadata. */
export function interactionCanAnswer(ix: Pick<Interaction, "session_id">, session: Session | undefined): boolean {
  return ix.session_id === "" || sessionCan(session, "approve");
}

/** Why this page can't answer a card (#7497), as iOS (#6868) and Android (`approvalBlockedReason`) say it: null when it
 *  can; "attach" when linking the session live would let it (a Codex thread Persona hasn't joined yet); else "computer",
 *  an unknown session included. */
export function answerBlock(ix: Pick<Interaction, "session_id">, session: Session | undefined): "attach" | "computer" | null {
  if (interactionCanAnswer(ix, session)) return null;
  return sessionCan(session, "attach") ? "attach" : "computer";
}

// The node writes some reasons with their own "View only: " in Chinese, which the subtitle adds already (#7803;
// Android's READ_ONLY_PREFIX).
const NODE_READ_ONLY_PREFIX = "仅查看："; // i18n-ignore: the node's prefix it strips

export function sourceSubtitle(s?: Session): string {
  if (!s?.source_kind) return "";
  const label = s.source_kind === "codex_app_server" ? t("lib-format.source_codex_live") : s.source_kind === "codex_history" ? t("lib-format.source_codex_history") : s.source_kind === "identity_conflict" ? t("lib-format.source_identity_conflict")
    : s.source_kind === "claude_bg" ? t("lib-format.source_claude_background")
    // Claude outside Persona's terminals (docs/integrations/claude-integration.md "Claude outside Persona's terminals")
    : s.source_kind === "claude_desktop" ? t("lib-format.source_claude_desktop")
    : s.source_kind === "claude_cli" ? t("lib-format.source_claude_terminal")
    // an agent runtime's session (docs/integrations/runtimes.md): its own name, else its agent's ("Claude Code", a Paseo
    // provider's), never a raw id (#8142)
    : s.source_kind === "runtime" ? normalizeTraeCliName(s.agent_label) || sessionAgentName(s)
    : s.source_kind;
  const detail = s.source_kind !== "identity_conflict" && s.agent_name ? ` · ${normalizeTraeCliName(s.agent_name)}` : "";
  if (sessionCan(s, "send")) return label + detail;
  const given = s.read_only_reason ?? "";
  const reason = (given.startsWith(NODE_READ_ONLY_PREFIX) ? given.slice(NODE_READ_ONLY_PREFIX.length) : given).trim();
  return t("lib-format.read_only", { source: label + detail, reason: reason || t("lib-format.read_only_default_reason") });
}

/** The command that resumes the session in a terminal; none ("") for a runtime's session, which its runtime keeps: a
 *  Paseo provider such as Grok has no such command, an id of the runtime's making (`paseo-…`) is no Claude session's,
 *  and a Claude Code conversation Paseo runs would get a second writer (#8142). */
export function resumeCommand(s: Session): string {
  if (s.source_kind === "runtime") return "";
  const codex = s.agent === "codex" || s.source_kind === "codex_app_server" || s.source_kind === "codex_history";
  const command = codex ? "codex resume" : s.agent === "traex" ? "traex resume" : "claude --resume";
  const resume = `${command} ${s.source_thread_id || s.session_id}`;
  // Without a folder `cd  && …` would resume in the home folder instead.
  return s.cwd ? `cd ${shellPath(s.cwd)} && ${resume}` : resume;
}

/** A path as one shell word: as is when it is plain, else single-quoted (as the Mac's resumeLine does), so a space,
 * `;` or `$(…)` in it stays part of the path. A leading `~` / `~/` stays outside the quotes so it still expands. */
export function shellPath(path: string): string {
  const home = path === "~" ? "~" : path.startsWith("~/") ? "~/" : "";
  const rest = path.slice(home.length);
  return !rest || /^[\w@%+=:,./-]+$/.test(rest) ? path : `${home}'${rest.replace(/'/g, `'\\''`)}'`;
}

export function sessionMenuActions(s: Session): string[] {
  const foreground = viewOf(s).reason?.kind !== "background" && !s.background_working && !s.background_waiting;
  return [
    ...(sessionCan(s, "attach") ? ["attach"] : []),
    ...(stateOf(s) === "working" && foreground && sessionCan(s, "interrupt") ? ["stop"] : []),
    ...(sessionCan(s, "terminal") ? ["resume"] : []),
    ...(!s.assistant && activityOf(s) !== "offline" && sessionCan(s, "close") ? ["close"] : []),
  ];
}

// "Delete chat" only takes a conversation out of the list (the hub notes it; the transcript stays) until it is used again:
// the statuses the hub keeps out of the lists (hub/deleted_sessions.go stillDeleted), not the assistant's (they archive)
// and not one still starting (issue #801). Idle (finished and seen) too: it stays hidden until it works again.
const DELETABLE_STATUSES = new Set(["offline", "done", "idle", "unknown", "interrupted"]);

export function sessionDeletable(s: Session): boolean {
  return !s.assistant && DELETABLE_STATUSES.has(s.status) && !s.starting;
}

/** A rolling upgrade can mix rows from old and new managers in one response. */
export function viewOf(s: Session): AgentView {
  if (!s.view) return inferAgentView(s);
  if (typeof parseAgentPhase(s.view.phase) === "string") return s.view;
  return { ...s.view, phase: "idle", reason: { kind: "host_unknown" } };
}

export function stateOf(s: Session): AgentPhase {
  return viewOf(s).phase;
}

/** The older host vocabulary used by read receipts, notification transitions and terminal controls.
 * Done alone cannot tell these callers whether a reply is unread or a process is running. */
export function activityOf(s: Session): AgentState {
  if (!s.view && s.state) return s.state;
  const { phase, reason } = viewOf(s);
  if (phase === "working" || phase === "needs_you") return phase;
  if (phase === "done" && s.locallySeen) return "idle";
  if (reason?.kind === "unseen" || (reason?.kind === "asks" && reason.unseen)) return "unread";
  if (reason?.kind === "not_running") return "offline";
  if (reason?.kind === "host_unknown") return "unknown";
  return "idle";
}

export function backgroundWaiting(s: Session): boolean {
  if (!s.view && s.background_waiting && s.background_working === undefined) return stateOf(s) === "working";
  const reason = viewOf(s).reason;
  return reason?.kind === "background" && reason.waiting === true;
}

export function stateColor(st: StatusMarkState): string {
  switch (st) {
    case "needs_you":
      return "var(--status-needs)";
    case "working":
      return "var(--status-working)";
    case "error":
      return "var(--red)";
    case "done":
    case "unread":
      return "var(--status-unread)";
    case "offline":
      return "var(--status-offline)";
    default:
      return "var(--status-idle)";
  }
}

export function byAttention(a: Session, b: Session): number {
  const d = viewOf(a).rank - viewOf(b).rank;
  if (d) return d;
  const ta = stateOf(a) === "working" ? a.state_since || a.updated_at : a.updated_at;
  const tb = stateOf(b) === "working" ? b.state_since || b.updated_at : b.updated_at;
  return (tb ?? "").localeCompare(ta ?? "") || a.session_id.localeCompare(b.session_id) || a.machine_id.localeCompare(b.machine_id);
}

/** Why it waits for you, in its host's words (a botmux attention), while it needs you; a reason left over from before
 *  (offline, seen) stays out. */
export function attentionReason(s: Session): string | undefined {
  const reason = s.attention_reason?.trim();
  return reason && stateOf(s) === "needs_you" ? reason : undefined;
}

/** The line a list row shows for it: why it waits for you when its host says so, else why its host stopped it on an
 *  error, else where its work stands when the hub triaged its turn (docs/assistant/agent-work.md), with where its closeout
 *  stands on merged and deployed after it, else its last words (iOS listPreview). */
export function sessionPreview(s: Session): string {
  return attentionReason(s) ?? failureReason(s) ?? usageLimitLabel(s) ?? closeoutPreview(s) ?? workLine(s) ?? s.last_text ?? "";
}

/** A stopped session's line when its agent ended the turn with a closeout (lib/closeout.ts): the hub's line as before
 *  (the closeout's own words when the hub gave none: the last words would be the raw block), then "merged · not
 *  deployed" where the agent said or the hub found it. One string: the row truncates it like any other line. */
function closeoutPreview(s: Session): string | undefined {
  const c = shownCloseout(s);
  if (!c) return undefined;
  const line = workLine(s) ?? closeoutLine(c);
  const suffix = closeoutSuffix(c);
  return line && suffix ? t("lib-work.closeout_line", { line, suffix }) : line || suffix || undefined;
}

/** A failure notice without the warning sign botmux starts it with: the page draws its own (iOS Session.failureText). */
export function failureText(reason: string): string {
  return reason.trim().replace(/^[\u26A0\uFE0F\s]+/u, "").trim();
}

/** Why the host stopped the session's turn on an error it will not retry by itself (failure_reason), while the
 *  session is not working (iOS listPreview, FailureNote); undefined otherwise. */
export function failureReason(s: Session | undefined): string | undefined {
  if (!s?.failure_reason || stateOf(s) === "working") return undefined;
  return failureText(s.failure_reason) || undefined;
}

const CTRL_CHARS = /[\u0000-\u001f\u007f]/;

/** Validates a credential-free http(s) URL (https-only when `httpsOnly` is true), rejecting single-slash
 *  same-origin relative URLs (`https:/admin`), backslash/userinfo authority confusion, and C0/DEL control
 *  characters. Returns the canonical `URL.href` or undefined. */
export function safeWebURL(raw?: string, httpsOnly = false): string | undefined {
  const s = raw?.trim();
  if (!s || CTRL_CHARS.test(s)) return undefined;
  const m = httpsOnly ? /^https:\/\//i.exec(s) : /^https?:\/\//i.exec(s);
  if (!m) return undefined;
  const auth = s.slice(m[0].length).split(/[/?#]/, 1)[0];
  if (!auth || auth.includes("@") || auth.includes("\\")) return undefined;
  try {
    const u = new URL(s);
    if ((httpsOnly ? u.protocol !== "https:" : u.protocol !== "http:" && u.protocol !== "https:") || !u.hostname || u.username || u.password) return undefined;
    return u.href;
  } catch {
    return undefined;
  }
}

/** "Open in Lark": the session's place outside Persona, a web (https) link only (iOS externalLink). */
export function externalLink(s?: Pick<Session, "external_url">): string | undefined {
  return safeWebURL(s?.external_url, true);
}

type SessionNameFields = Pick<Session, "title" | "cwd" | "assistant" | "slot_name" | "slot_id" | "tab_id" | "space_kind" | "host">;

export function herdrTabName(s: SessionNameFields): string {
  const kind = s.space_kind || s.host;
  if (s.assistant || !(s.slot_id || s.tab_id) || (kind && kind !== "herdr" && kind !== "herdr-core")) return "";
  return s.slot_name?.trim() || "";
}

// Keep the terminal prompt recognizer in sync with server/internal/proto/session_name.go.
const SHELL_PROMPT_TITLE = /^[A-Za-z0-9_][A-Za-z0-9_.-]*@[A-Za-z0-9_][A-Za-z0-9_.-]*:(?:[ \t]+[^\r\n]+|[~/][^\r\n]*)$/;

export function sessionDisplayTab(s: SessionNameFields, preferTab = false): string {
  const title = s.title?.trim() || "";
  return preferTab || !title || SHELL_PROMPT_TITLE.test(title) ? herdrTabName(s) : "";
}

export function sessionTitle(s: SessionNameFields, preferTab = false): string {
  return sessionDisplayTab(s, preferTab) || s.title?.trim() || baseName(s.cwd) || t("lib-format.untitled_session");
}

export function sessionTitleDetail(s: SessionNameFields, preferTab: boolean): string {
  const tab = preferTab ? herdrTabName(s) : "";
  const title = s.title?.trim() || "";
  return tab && tab !== title ? title : "";
}

// A path from a Windows computer: a drive letter or a UNC share. Only there is a backslash a separator; in any other
// path it is a character of a name.
export const WINDOWS_PATH = /^[A-Za-z]:[\\/]|^\\\\|^\/\/[^\\/]+[\\/]/;

export function baseName(p?: string): string {
  if (!p) return "";
  const parts = pathSlashes(p).replace(/\/+$/, "").split("/");
  return parts[parts.length - 1] || p;
}

/** Remote Windows paths use either separator. A backslash inside a POSIX filename stays literal. */
export function pathSlashes(path: string): string {
  return WINDOWS_PATH.test(path) ? path.replaceAll("\\", "/") : path;
}

export function tabLabel(s: Session): string {
  const n = s.slot_name?.trim();
  if (!n) return s.slot_index ? `#${s.slot_index}` : "";
  return /^\d+$/.test(n) ? `#${n}` : n;
}

export function shortPath(p?: string): string {
  if (!p) return "";
  return p.replace(/^\/(Users|home|data\d*\/home)\/[^/]+/, "~");
}

/** `style` "narrow" is for a crowded row's end ("13m ago" in English; Chinese reads the same in every style). */
export function ago(iso?: string, now = Date.now(), style: Intl.RelativeTimeFormatStyle = "long"): string {
  if (!iso) return "";
  const ts = Date.parse(iso);
  if (!Number.isFinite(ts) || ts < 86400_000) return "";
  const s = Math.round((now - ts) / 1000);
  const opts: Intl.RelativeTimeFormatOptions = { numeric: "auto", style };
  // "Just now" rather than Intl's "now" for under a minute
  if (s < 45) return t("lib-format.just_now");
  // Rounded, a unit steps up once it reaches the next one: 59.5 minutes is "1 hour ago", not "60 minutes ago".
  const minutes = Math.round(s / 60);
  if (minutes < 60) return formatRelative(-minutes, "minute", opts);
  const hours = Math.round(s / 3600);
  if (hours < 24) return formatRelative(-hours, "hour", opts);
  // Days by the local calendar, not by 24 h spans: yesterday morning seen late tonight is yesterday, not the day before yesterday (#4281).
  // At least 1: 23.5 hours ago within one calendar day still reads as "yesterday", not "today".
  const days = Math.max(1, calendarDays(ts, now));
  if (days < 7) return formatRelative(-days, "day", opts);
  return formatDate(ts, { month: "numeric", day: "numeric" });
}

/** How many local midnights lie between `from` and `to` (rounded: a DST day is 23 or 25 hours). */
function calendarDays(from: number, to: number): number {
  const midnight = (ms: number) => {
    const d = new Date(ms);
    d.setHours(0, 0, 0, 0);
    return d.getTime();
  };
  return Math.round((midnight(to) - midnight(from)) / 86400_000);
}

export function duration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return t("lib-format.seconds", { s });
  const m = Math.floor(s / 60);
  if (m < 60) return s % 60 ? t("lib-format.minutes_seconds", { m, s: s % 60 }) : t("lib-format.minutes", { m });
  const h = Math.floor(m / 60);
  return m % 60 ? t("lib-format.hours_minutes", { h, m: m % 60 }) : t("lib-format.hours", { h });
}

// For a running timer in a list: minutes are enough once past one.
export function shortDuration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return t("lib-format.seconds", { s });
  const m = Math.floor(s / 60);
  if (m < 60) return t("lib-format.minutes", { m });
  return m % 60 ? t("lib-format.hours_minutes", { h: Math.floor(m / 60), m: m % 60 }) : t("lib-format.hours", { h: m / 60 });
}

export function clock(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const today = new Date();
  const hm = `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
  if (d.toDateString() === today.toDateString()) return hm;
  return t("lib-format.date_time", { date: formatDate(d, { month: "short", day: "numeric" }), time: hm });
}

/** 18:20 today, "Yesterday", "Wed" within the week, "Oct 28" this year, else 2025-10-28 (LarkTime.short). */
export function shortTime(iso?: string, now = new Date()): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return "";
  const day = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const days = Math.round((day(now) - day(d)) / 86400_000);
  if (days <= 0) return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
  if (days === 1) return t("lib-inbox.yesterday");
  if (now.getTime() - d.getTime() < 6 * 86400_000) return formatDate(d, { weekday: "short" });
  if (d.getFullYear() === now.getFullYear()) return formatDate(d, { month: "short", day: "numeric" });
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

/** "512 B", "1.5 KB", "250 MB". The unit and the decimal follow the figure as it is shown, after rounding, as the
 *  machine pages' bytesBin does: 1023.5 KB is "1.0 MB", not "1024 KB"; 99.95 KB is "100 KB", not "100.0 KB" (#9969). */
export function bytes(n?: number): string {
  if (n === undefined || !Number.isFinite(n)) return "—";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1023.5 && i < u.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n >= 99.95 || i === 0 ? Math.round(n) : n.toFixed(1)} ${u[i]}`;
}

/**
 * A random (v4) UUID. crypto.randomUUID only exists in a secure context (HTTPS, localhost): a hub opened over plain
 * http gets one from getRandomValues, which is everywhere. The hub takes only UUIDs as op ids (X-Persona-Op-Id, #1651).
 */
export function uuid(): string {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

/** Whether s is a UUID as the hub checks op ids. */
export function isUUID(s: string): boolean {
  return /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(s);
}
