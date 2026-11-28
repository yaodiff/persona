// `/permissions` in the composer's "/" palette (#6476): it opens the conversation's permission picker with every mode
// (Plan mode included, which the chip's own menu leaves to the + panel) instead of sending the words, wherever the chip
// is shown (an Agent with modes or listed `capabilities.permission_modes` while it runs, or a Persona conversation). Picking a mode switches the running session as
// the chip does (POST …/modes): at once where the agent takes it while it runs; Full access for a terminal Claude Code
// started without a bypass flag restarts the session in it once its turn is over (node/mode_restart.go), which its row
// says before it is picked; a Persona conversation's Full access takes effect with its next message.
import { t } from "../i18n";
import { hasModes, listedModesOf } from "./session-actions";
import { activityOf } from "./format";
import type { Session } from "./types";

export const PERMISSIONS_COMMAND = "/permissions";

/** The palette's row for it (Persona's own command: no agent name beside it). */
export const permissionsCommand = () => ({ word: PERMISSIONS_COMMAND, desc: t("chat-composer.cmd_permissions_desc"), text: PERMISSIONS_COMMAND });

/** `/permissions` alone in the box (what the palette fills in). */
export const isPermissionsCommand = (draft: string) => draft.trim().toLowerCase() === PERMISSIONS_COMMAND;

/** Where the chat shows the permission picker, and so offers `/permissions`: a Persona conversation whatever its state,
 *  an agent with modes or listed `capabilities.permission_modes` while it runs (read-only sources and offline sessions have none). */
export function canPickPermissions(s: Session | undefined, assistant?: boolean): boolean {
  if (!s) return false;
  return Boolean(assistant || s.assistant) || ((hasModes(s) || Boolean(listedModesOf(s))) && activityOf(s) !== "offline");
}

// ── opening the picker from the composer ─────────────────────────────────────

type Opener = () => void;
const openers = new Map<string, Opener>();

/** The permission picker of the conversation `key` answers `/permissions` with its full menu; returns the unsubscribe. */
export function onPermissionPicker(key: string, open: Opener): () => void {
  openers.set(key, open);
  return () => {
    if (openers.get(key) === open) openers.delete(key);
  };
}

/** Opens the conversation's permission menu; false when it has no picker (the composer then sends the words). */
export function requestPermissionPicker(key: string): boolean {
  const open = openers.get(key);
  if (!open) return false;
  open();
  return true;
}
