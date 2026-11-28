// The rules of an Agent's permission mode menu (#3956, as iOS `SessionModes` from #3719 and Android #3957): which modes the
// composer's picker offers, which picks ask first, and each mode's symbol. The web's header no longer has a mode button
// (#5421, cindy's: the mode is the composer's picker and Session info).
import { t } from "../i18n";
import type { IconName } from "../ui/icons";
import type { MenuItem } from "../ui/menu-model";
import {
  BYPASS_MODE, BYPASS_WARNING, canSwitchToBypass, hasModes, listedModeNow, listedModeOption, listedModesOf, modeTitle, PERMISSION_MODES, switchableModes,
} from "./session-actions";
import type { Session } from "./types";
import { paneClaude } from "./composer-commands";

/** Full access for a terminal Claude Code that cannot reach it with shift+tab (started without a bypass flag): the node
 *  restarts the session in it once its turn is over, resuming the same conversation (node/mode_restart.go, #6476). */
export function bypassRestarts(s: Session, assistant?: boolean): boolean {
  return !(assistant || s.assistant) && s.agent !== "agy" && hasModes(s) && paneClaude(s) && !canSwitchToBypass(s);
}

/** What the Full access row says under its name where picking it does not take effect at once, "" where it does: the
 *  restart it waits for, or the one it will make; a Persona conversation's next message. */
export function bypassNote(s: Session, assistant?: boolean): string {
  if (s.bypass_pending) return t("chat-sessiondialogs.bypass_pending");
  if (bypassRestarts(s, assistant)) return t("chat-sessiondialogs.bypass_restart_detail");
  if ((assistant || s.assistant) && s.permission_mode !== BYPASS_MODE) return t("chat-sessiondialogs.bypass_next_message");
  return "";
}

/** An assistant conversation (and Antigravity, as before) switches without asking. */
const asksFirst = (s: Session, assistant?: boolean) => !(assistant || s.assistant || s.agent === "agy");

/** What a pick asks first (#3719): going into Allow Everything, and leaving it when the agent cannot come back to it from
 *  here (`bypass_available` false: only a new start picks it). The modes a session lists (#8261) are a switch both ways:
 *  only going into Full access asks. */
export function modeConfirmation(s: Session, to: string, assistant?: boolean): "allow" | "leave" | null {
  if (!asksFirst(s, assistant) || to === s.permission_mode) return null;
  if (to === BYPASS_MODE) return "allow";
  if (s.permission_mode === BYPASS_MODE && !s.bypass_available && !listedModesOf(s)) return "leave";
  return null;
}

/** The rows of a session that lists its modes (#8261, `listedModesOf`): its own approval and Full access, the one it runs
 *  in checked; going into Full access asks first, as Claude Code's does. */
function listedMenuItems(s: Session, listed: string[], pick: (mode: string) => void, locked?: boolean): MenuItem[] {
  const now = listedModeNow(s, listed);
  return listed.map((id) => {
    const m = listedModeOption(id);
    const ask = !locked && id === BYPASS_MODE && now !== BYPASS_MODE;
    return {
      key: id,
      label: m.title,
      detail: m.detail,
      radio: true,
      checked: now === id,
      disabled: locked,
      ...(ask ? { danger: true, confirm: t("chat-sessiondialogs.bypass_confirm"), confirmDetail: BYPASS_WARNING } : {}),
      onSelect: () => pick(id),
    };
  });
}

/** The mode menu's rows, the header's and the composer chip's: the modes it can switch to, the current one checked, the
 *  picks that ask first asking (the row turns into its question, a second choice runs it), and Allow Everything shown
 *  disabled with its reason where the agent cannot take it while it runs. `locked`: nothing can be picked (not running).
 *  A session that lists its own modes has those rows alone (`listedMenuItems`). */
export function modeMenuItems(s: Session, pick: (mode: string) => void, opts: { assistant?: boolean; locked?: boolean } = {}): MenuItem[] {
  const listed = opts.assistant ? undefined : listedModesOf(s);
  if (listed) return listedMenuItems(s, listed, pick, opts.locked);
  const mode = s.permission_mode;
  const restart = bypassRestarts(s, opts.assistant);
  const note = bypassNote(s, opts.assistant);
  const allowed = s.capabilities?.permission_modes;
  const choices = switchableModes(opts.assistant || s.assistant, s.agent, canSwitchToBypass(s) || restart);
  const rows: MenuItem[] = (allowed?.length ? choices.filter((m) => allowed.includes(m.id)) : choices).map((m) => {
    const ask = modeConfirmation(s, m.id, opts.assistant);
    const later = m.id === BYPASS_MODE && note ? note : "";
    return {
      key: m.id,
      label: m.title,
      detail: later || m.detail,
      radio: true,
      checked: mode === m.id,
      disabled: opts.locked,
      ...(ask === "allow" ? { danger: true, confirm: t("chat-sessiondialogs.bypass_confirm"), confirmDetail: later ? `${BYPASS_WARNING}\n${later}` : BYPASS_WARNING } : {}),
      ...(ask === "leave" ? { confirm: t("chat-sessiondialogs.leave_bypass_confirm", { mode: modeTitle(m.id) }), confirmDetail: t("chat-sessiondialogs.leave_bypass_detail") } : {}),
      onSelect: () => pick(m.id),
    };
  });
  const offered = rows.some((r) => r.key === BYPASS_MODE);
  if (!offered && asksFirst(s, opts.assistant) && !opts.locked && hasModes(s)) {
    const bypass = PERMISSION_MODES.find((m) => m.id === BYPASS_MODE)!;
    rows.push({ key: BYPASS_MODE, label: bypass.title, detail: bypass.detail, disabled: true, onSelect: () => {} });
  }
  return rows;
}

/** The composer picker's symbol per mode (#5324, cindy's: a raised hand, code, a list, sparkles, a warning triangle); a
 *  mode this app does not know the plain shield. Not reported yet keeps the raised hand, as iOS does, while the picker's
 *  label names no mode (#5756). */
export function pickerIcon(mode: string | null | undefined): IconName {
  if (!mode) return "palm"; // not reported: absent, empty or null alike
  switch (mode) {
    case "default":
      return "palm";
    case "acceptEdits":
      return "code";
    case "plan":
      return "list";
    case "auto":
      return "sparkles";
    case BYPASS_MODE:
      return "warning";
    default:
      return "shield";
  }
}

/** Where unchecking Plan mode (the + panel's check row, #5324) goes: the mode the agent had before it, as this page saw it,
 *  while it can still switch to it; else Default permissions. Never Full access, which only the picker chooses, with its
 *  confirmation. */
export function leavePlanTo(s: Session, before: string | undefined, assistant?: boolean): string {
  if (!before || before === "plan" || before === BYPASS_MODE) return "default";
  const allowed = s.capabilities?.permission_modes;
  const offered = switchableModes(assistant || s.assistant, s.agent, canSwitchToBypass(s));
  const filtered = allowed?.length ? offered.filter((m) => allowed.includes(m.id)) : offered;
  return filtered.some((m) => m.id === before) ? before : "default";
}
