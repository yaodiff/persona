// What the Agents list's menus do (iOS AgentRowMenu / SpaceMenuItems): pin, mute, hand to Persona. Each change shows at
// once and is put back if the hub refuses it.
import { t } from "../i18n";
import { toast } from "../ui/atoms";
import { agentHandoffText } from "./agent-handoff";
import { credentialOwner, errorText, pastHub, sessionPath } from "./api";
import { activityOf, sessionCan } from "./format";
import { managerHeaderFor } from "./manager-choice";
import { stopMessageFeedback } from "./message-send";
import { apiOp } from "./ops";
import { unseen } from "./seen-outbox";
import { askPersona } from "./outbox";
import { isPinned, isSpacePinned, spaceKey, togglePin, toggleSpacePin, toggled, type AgentPins } from "./pins";
import { isMuted, toggleMute } from "./session-actions";
import { SESSIONS_LOOKUP_FEATURE } from "./runtime-list";
import { canInterruptAgentRow, getState, saveMutes, savePins } from "./store";
import type { Session } from "./types";

/** A finished reply can be marked as seen. Muting suppresses Just finished for legacy rows;
 *  with a manager view the phase and reason already account for the user's preferences. */
export function canMarkAgentSeen(s: Session, muted: boolean): boolean {
  return unseen(s) && (Boolean(s.view) || !muted || activityOf(s) === "unread");
}

const pendingInterrupts = new Map<string, Promise<void>>();

/** The row and chat can interrupt the same turn while its first request is still in flight. */
export function interruptAgent(mid: string, sid: string): Promise<void> {
  const path = `${sessionPath(mid, sid)}/interrupt`;
  const requestKey = () => JSON.stringify([credentialOwner(), managerHeaderFor(path), path]);
  const key = requestKey();
  const pending = pendingInterrupts.get(key);
  if (pending) return pending;
  const current = () => requestKey() === key;
  const manager = managerHeaderFor(path);
  async function send() {
    if (getState().features.includes(SESSIONS_LOOKUP_FEATURE)) {
      // A filtered list can refresh before the chat's retained detail. Ask about this Agent only.
      const { exactSession } = await import("./runtime-session");
      if (!current()) throw new DOMException("Retired interrupt", "AbortError");
      const row = await exactSession(mid, sid, undefined, manager);
      if (!row || !sessionCan(row, "interrupt") || !canInterruptAgentRow(row, true)) {
        throw new Error(t("lib-agent-actions.interrupt_unavailable"));
      }
    }
    const result = await apiOp<{ state?: string }>(path, { method: "POST", timeout: pastHub(90_000), isAuthContextCurrent: current });
    if (!current()) throw new DOMException("Retired interrupt", "AbortError");
    if (result.state === "unknown") throw new Error(t("lib-new-agent.outcome_unknown"));
    stopMessageFeedback(mid, sid);
  }
  const request = send().finally(() => pendingInterrupts.delete(key));
  pendingInterrupts.set(key, request);
  return request;
}

async function putPins(next: AgentPins, done: string) {
  try {
    await savePins(next);
    toast(done, "ok");
  } catch (e) {
    toast(t("lib-agent-actions.save_failed", { error: errorText(e) }), "error");
  }
}

export function flipPin(s: Session) {
  const pins = getState().pins;
  return putPins(togglePin(s, pins), isPinned(s, pins) ? t("lib-agent-actions.unpinned") : t("lib-agent-actions.pinned"));
}

export function flipSpacePin(machineId: string, spaceName: string) {
  const pins = getState().pins;
  return putPins(toggleSpacePin(machineId, spaceName, pins), isSpacePinned(machineId, spaceName, pins) ? t("lib-agent-actions.space_unpinned") : t("lib-agent-actions.space_pinned"));
}

async function putMutes(next: ReturnType<typeof getState>["mutes"], done: string) {
  try {
    await saveMutes(next);
    toast(done, "ok");
  } catch (e) {
    toast(t("lib-agent-actions.save_failed", { error: errorText(e) }), "error");
  }
}

/** One agent's notifications: its own switch sits on top of its space's. */
export function flipMute(s: Session) {
  const next = toggleMute(s, getState().mutes);
  return putMutes(next, isMuted(s, next) ? t("lib-agent-actions.muted") : t("lib-agent-actions.unmuted"));
}

/** A space's notifications (the agents turned back on inside it keep theirs). */
export function flipSpaceMute(machineId: string, spaceName: string) {
  const m = getState().mutes;
  const key = spaceKey(machineId, spaceName);
  return putMutes({ ...m, spaces: toggled(m.spaces, key) }, m.spaces.includes(key) ? t("lib-agent-actions.space_unmuted") : t("lib-agent-actions.space_muted"));
}

/** "Hand to Persona": the agent goes to Persona's main conversation (a new one becomes it when there is none), and the page
 * moves there (the app's askPersona). */
export async function handToPersona(s: Session, machineName?: string) {
  try {
    await askPersona(agentHandoffText(s, machineName));
  } catch (e) {
    toast(t("lib-agent-actions.send_failed", { error: errorText(e) }), "error");
  }
}
