// "Start new Agents as Orca chats" (#17650, docs/integrations/engines.md "Orca chats as New Agent's default"). The hub
// decides where a start goes (assistant/orca_chat_default.go): with a computer's switch on, a person's New Agent start that
// keeps the default Claude Code or Codex opens as the computer's Orca chat. It tells New Agent where a start that keeps the
// default goes (`default_opens` on GET /machines/{mid}/launchers); the form only checks that this start still keeps it.

import type { Machine } from "./types";

/** The computer's switch as the Engine section shows it: on or off where the hub sends it, null (no switch) where it does
 *  not, and for a value this client does not know. */
export function orcaChatSwitch(machine: Pick<Machine, "orca_chat_starts">): boolean | null {
  const v = machine.orca_chat_starts as string | undefined;
  return v === "on" ? true : v === "off" ? false : null;
}

/** `default_opens`: the Orca chat launcher, its name, the permission modes with which a default start opens there, the model
 *  and effort that go with it, and `dropped` when the default's model or effort does not (Orca's own runs). */
export interface DefaultOpens {
  launcher: string;
  display_name?: string;
  permission_modes?: string[];
  model?: string;
  effort?: string;
  dropped?: boolean;
}

/** What the form has picked for this start, as it sends it. */
export interface StartPick {
  /** "" while it follows the default */
  launcher: string;
  /** a preset is picked */
  tier: boolean;
  /** a model picked for this start ("": the default's) */
  model: string;
  /** the host picked ("" or undefined: the computer's default host) */
  host?: string;
  /** the permission mode sent ("": the launcher's own) */
  mode: string;
  ultracode: boolean;
  /** a faster speed picked (sent with the launcher named, so the start no longer keeps the default) */
  serviceTier?: string;
}

/** The Orca chat this start opens as, or null when it opens where it always did: the hub's `default_opens`, while the start
 *  keeps the default (no launcher, preset, model, faster speed or ultracode picked, no host but Orca) with a permission mode
 *  the chat takes. Plan mode is never among them (an Orca chat promises no read-only start). */
export function opensAsOrcaChat(opens: DefaultOpens | undefined | null, pick: StartPick): DefaultOpens | null {
  if (!opens?.launcher) return null;
  if (pick.launcher || pick.tier || pick.model || pick.ultracode || pick.serviceTier) return null;
  if (pick.host && pick.host !== "orca") return null;
  if (pick.mode && !(opens.permission_modes ?? []).includes(pick.mode)) return null;
  return opens;
}
