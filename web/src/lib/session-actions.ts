// What the chat's ⋯ menu changes on a session: notifications (mutes), the run modes, the name
// (docs/protocol/protocol.md `/mutes`, `/modes`, `/rename`; iOS Pins.togglingMute, SessionModes).
import { t } from "../i18n";
import type { AgentMutes } from "./agent-groups";
import { herdrTabName, sessionCan } from "./format";
import type { Session } from "./types";

export interface ModeOption {
  id: string;
  title: string;
  detail: string;
}

export const PERMISSION_MODES: ModeOption[] = [
  { id: "default", title: t("lib-session-actions.mode_default"), detail: t("lib-session-actions.mode_default_detail") },
  { id: "acceptEdits", title: t("lib-session-actions.mode_accept_edits"), detail: t("lib-session-actions.mode_accept_edits_detail") },
  { id: "plan", title: t("lib-session-actions.mode_plan"), detail: t("lib-session-actions.mode_plan_detail") },
  { id: "auto", title: t("lib-session-actions.mode_auto"), detail: t("lib-session-actions.mode_auto_detail") },
  { id: "bypassPermissions", title: t("lib-session-actions.mode_bypass_permissions"), detail: t("lib-session-actions.mode_bypass_permissions_detail") },
];

const commonEfforts = ["low", "medium", "high", "xhigh", "max"];

// The tier names are shared. Each engine keeps its own descriptions.
function effortOptionsFor(ids: string[], detailPrefix: string): ModeOption[] {
  return ids.map((id) => ({
    id,
    title: t(`lib-session-actions.effort_${id}`),
    detail: t(`lib-session-actions.${detailPrefix}_${id}_detail`),
  }));
}

export const EFFORTS = effortOptionsFor(commonEfforts, "effort");

/** Codex's reasoning efforts (node/codex_effort.go). A thread offers those of its model in `Session.efforts`. */
export const CODEX_EFFORTS = effortOptionsFor(["minimal", ...commonEfforts, "ultra"], "codex_effort");

/** Modes a running session can switch to: "Allow all" is chosen only when it starts. */
export const SWITCHABLE_MODES = PERMISSION_MODES.filter((m) => m.id !== "bypassPermissions");

export const BYPASS_MODE = "bypassPermissions";

/** What "Allow all" means, said in red where it is on (iOS's warning). */
export const BYPASS_WARNING = t("lib-session-actions.bypass_warning");

/** An assistant conversation (the main one too) switches to any mode, "Allow all" included: the hub restarts its
 * Claude with the new mode (#2416). So does an agent whose Claude was started with a flag that puts "Allow all" in
 * its mode cycle (`bypass`: Session.bypass_available, or it is in that mode now; #1379). Other agents keep
 * SWITCHABLE_MODES. */
const ALL_MODES = PERMISSION_MODES.map((m) => (m.id === BYPASS_MODE ? { ...m, detail: t("lib-session-actions.mode_bypass_permissions_assistant_detail") } : m));

export function switchableModes(assistant: boolean | undefined, agent?: string, bypass?: boolean): ModeOption[] {
  return assistant || agent === "agy" || bypass ? ALL_MODES : SWITCHABLE_MODES;
}

/** Whether a running agent's Claude can be switched to "Allow all" (see switchableModes). */
export function canSwitchToBypass(s: Session): boolean {
  return Boolean(s.bypass_available || s.permission_mode === BYPASS_MODE);
}

export function modeTitle(id?: string): string {
  return id ? (PERMISSION_MODES.find((m) => m.id === id)?.title ?? id) : "";
}

export function effortTitle(id?: string): string {
  return id ? (CODEX_EFFORTS.find((m) => m.id === id)?.title ?? id) : "";
}

/** The two chips (Claude Code's TUI bottom bar). The node says where they work (`capabilities.modes`, #1843): a runtime
 *  that applies them (Orca, Antigravity), never a Claude outside Persona's terminals nor an agent in a terminal whose mode
 *  is its start's (cursor, agy: the node refuses the switch). An older node does not say, and the chips follow the agent:
 *  Claude Code in Persona's terminals, Antigravity's runtime; Codex, agy in a terminal pane and other sources not, as on
 *  iOS and Android (`canChangeModes`). */
export function hasModes(s: Session | undefined): boolean {
  if (!s || s.assistant) return false;
  if (typeof s.capabilities?.modes === "boolean") return s.capabilities.modes;
  if (s.source_kind) return s.agent === "agy";
  return !s.agent || s.agent === "claude";
}

/** A session's own approval, the first of the modes a session lists (`listedModesOf`): "Launcher default", not "Ask",
 *  since the agent's own settings may already approve everything (#8261). */
export const LAUNCHER_DEFAULT: ModeOption = { id: "default", title: t("lib-session-actions.mode_launcher_default"), detail: t("lib-session-actions.mode_launcher_default_detail") };

/** The permission modes the composer switches a session of an agent that is not Claude Code between (#8261, the node's
 *  `capabilities.permission_modes`): its own approval and Full access, on Paseo's Grok, OpenCode or Codex. Undefined with
 *  fewer than two, for a Persona conversation, and where Claude Code's chips stand (`hasModes`). */
export function listedModesOf(s: Session | undefined): string[] | undefined {
  const modes = s?.capabilities?.permission_modes;
  if (!s || s.assistant || hasModes(s) || !modes || modes.length < 2) return undefined;
  return modes;
}

/** The listed mode `s` runs in: the one it reports when listed, else its own (`default`): no mode reported is its own. */
export function listedModeNow(s: Session, listed: string[]): string {
  return s.permission_mode && listed.includes(s.permission_mode) ? s.permission_mode : "default";
}

/** A listed mode's name and description: its own approval is "Launcher default", Full access says it can be switched
 *  back (a switch, unlike Claude Code's start-only Full access), the rest as Claude Code names them. */
export function listedModeOption(id: string): ModeOption {
  if (id === "default") return LAUNCHER_DEFAULT;
  if (id === BYPASS_MODE) return { id, title: t("lib-session-actions.mode_bypass_permissions"), detail: t("lib-session-actions.mode_bypass_listed_detail") };
  return PERMISSION_MODES.find((m) => m.id === id) ?? { id, title: id, detail: "" };
}

/** Thinking effort for sessions with advertised levels and no permission menu. */
export function hasEffortOnly(s: Session | undefined): boolean {
  return Boolean(s && !s.assistant && !hasModes(s) && s.efforts?.length && sessionCan(s, "send"));
}

/** The effort menu's choices: the session's own levels when the node lists them, Claude Code's otherwise. */
export function effortOptions(s: Session): ModeOption[] {
  if (!s.efforts?.length) return EFFORTS;
  return s.efforts.map((id) => CODEX_EFFORTS.find((m) => m.id === id) ?? { id, title: id, detail: "" });
}

const key = (s: Pick<Session, "machine_id" | "session_id">) => `${s.machine_id}/${s.session_id}`;

/** Whether the hub sends this agent's notifications (the rule of GET /mutes). */
export function isMuted(s: Pick<Session, "machine_id" | "session_id" | "space_name">, m: AgentMutes): boolean {
  if (m.sessions.includes(key(s))) return true;
  if (m.allowed?.includes(key(s))) return false;
  return Boolean(s.space_name && m.spaces.includes(`${s.machine_id}/${s.space_name}`));
}

/** The mutes after flipping one agent: its own switch sits on top of its space's (iOS Pins.togglingMute). */
export function toggleMute(s: Pick<Session, "machine_id" | "session_id" | "space_name">, m: AgentMutes): AgentMutes {
  const k = key(s);
  const sessions = m.sessions.filter((x) => x !== k);
  const allowed = (m.allowed ?? []).filter((x) => x !== k);
  const spaceMuted = Boolean(s.space_name && m.spaces.includes(`${s.machine_id}/${s.space_name}`));
  if (isMuted(s, m)) return { spaces: m.spaces, sessions, allowed: spaceMuted ? [...allowed, k] : allowed };
  return { spaces: m.spaces, sessions: spaceMuted ? sessions : [...sessions, k], allowed };
}

/** The rename request (iOS RenameSessionAlert): only what changed; the herdr tab only for an agent in a herdr pane.
 * Returns null when nothing changed. */
export function renameBody(s: Session, tab: string, title: string, unified = false): { tab_name?: string; title?: string } | null {
  const t = tab.trim(), n = title.trim();
  if (unified) {
    if (!t) return null;
    const cli = s.agent ?? "claude";
    const syncTitle = cli === "claude" || cli === "codex";
    if (t === herdrTabName(s) && (!syncTitle || t === s.title)) return null;
    return syncTitle ? { tab_name: t, title: t } : { tab_name: t };
  }
  const body: { tab_name?: string; title?: string } = {};
  if (!s.assistant && s.slot_id && t && t !== (s.slot_name ?? "")) body.tab_name = t;
  if (n && n !== s.title) body.title = n;
  return Object.keys(body).length ? body : null;
}
