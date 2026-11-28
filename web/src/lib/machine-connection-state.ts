import type { Machine, MachineLinkSwitch } from "./types";

/** A switch to Persona Link is under way (#10662, lib/link-switch.ts): the computer may be offline and of kind link
 *  meanwhile, as expected. Here, not there, because the first screen reads it. */
export function linkSwitchRunning(s?: MachineLinkSwitch | null): boolean {
  const p = s?.phase;
  return p === "checking" || p === "installing" || p === "connecting" || p === "rolling_back";
}

/** Offline on purpose: an update restarting its node, or a switch to Persona Link reconnecting it (#10662). Pure, so
 *  the machine alerts read it without counting as a deployment read (lib/machine-alerts.ts). */
export function disconnectExpected(m: Machine): boolean {
  return !m.online && ((!m.deploy_error && (m.deploy_phase === "restarting" || m.deploy_phase === "confirming")) || linkSwitchRunning(m.link_switch));
}

/** The hub advertises remote updates. Older hubs could only update SSH nodes. */
export function supportsDeployment(m: Machine): boolean {
  return m.kind !== "local" && (m.can_deploy ?? (m.kind !== "link" && m.kind !== "service"));
}
