// "Close…" on an Agent (#7125, docs/web/web.md "Archived agents"): every place that closes an Agent opens the one
// dialog (views/chat/SessionDialogs.tsx CloseAgentDialog, mounted by views/CloseAgentHost.tsx): its row menu, the chat's
// ⋯, and "Close pane" on the pane it runs in (the Agents tree, the Terminal page, the command palette), so a busy agent
// is never closed without the warning. What the dialog does is lib/agent-archives.ts closeOffer.
import { t } from "../i18n";
import type { MenuItem } from "../ui/menu-model";
import { closeOffer } from "./agent-archives";
import { getSession, getState } from "./store";
import type { Session } from "./types";

const openers = new Set<(s: Session) => void>();

/** Opens "Close…" for session s (the app shell listens); the dialog reads the latest row of it as it goes. */
export function openCloseAgent(s: Session) {
  openers.forEach((l) => l(s));
}

export function onOpenCloseAgent(l: (s: Session) => void) {
  openers.add(l);
  return () => {
    openers.delete(l);
  };
}

/** What a menu row that closes session s does: open "Close…", or, busy where its node has no plain close, stay
 *  disabled saying why. Undefined: there is nothing to close it with (offline, the assistant's). */
export function closeAgentAction(s: Session, features: readonly string[] = getState().features): Pick<MenuItem, "onSelect" | "disabled" | "detail"> | undefined {
  const offer = closeOffer(s, features);
  if (offer === "no") return undefined;
  if (offer === "busy") return { onSelect: () => {}, disabled: true, detail: t("agentmenus.close_busy") };
  return { onSelect: () => openCloseAgent(s) };
}

/** closeAgentAction for the session machine mid lists as sid (a pane's agent); undefined for one this page doesn't list. */
export function closeListedAgent(mid: string, sid: string): Pick<MenuItem, "onSelect" | "disabled" | "detail"> | undefined {
  const s = getSession(mid, sid);
  return s ? closeAgentAction(s) : undefined;
}
