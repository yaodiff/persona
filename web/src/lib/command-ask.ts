// Claude Code asking on its own terminal to run a shell command (its permission prompt, or its safety check in bypass
// mode), as the hub reads it off the screen (#5265, #5266): whether a screen card is one, and its Allow / Deny. The words
// the card says it in are lib/command-ask-lines.ts, with the lazy views/chat/CommandScreenCard.tsx (off the first screen).
// The same rules as the iPhone's CommandAsk (apple/Shared/CommandAsk.swift).
import type { DialogOption, ScreenDialog } from "./types";

export interface CommandAsk {
  command: string;
  /** The agent's own words for it. */
  description?: string;
  /** The boxed warning as Claude Code words it, what it is about ("delete"), and the countdown sentence. */
  warning?: string;
  risk?: string;
  deadline?: string;
  /** When the countdown runs out (ms, the hub's clock), and whether it lets the request through then. */
  deadlineAt?: number;
  deadlineAllows: boolean;
}

/** screen.describe preserves the terminal's command heading in detail even when dialogwire omits an empty command. */
export function isCommandPrompt(d?: ScreenDialog): boolean {
  return !!d && (d.command !== undefined || /^(Bash|PowerShell) command\b/.test(d.detail ?? ""));
}

/** The command prompt of a screen card's dialog; null for any other dialog, and from a hub before #5265. */
export function commandAsk(d: ScreenDialog | undefined): CommandAsk | null {
  const command = d?.command?.trim();
  if (!d || command === undefined) return null;
  const at = d.deadline_at ? Date.parse(d.deadline_at) : NaN;
  return {
    command,
    description: d.description?.trim() || undefined,
    warning: d.warning?.trim() || undefined,
    risk: d.risk,
    deadline: d.deadline?.trim() || undefined,
    deadlineAt: Number.isFinite(at) ? at : undefined,
    deadlineAllows: d.deadline_action === "allow",
  };
}

/** The prompt's Yes and No, for the card's Allow and Deny: only a command prompt with just those two choices. One with
 *  more ("Yes, and don't ask again for …") keeps its own labels. */
export function allowDeny(d: ScreenDialog | undefined): { allow: DialogOption; deny: DialogOption } | null {
  if (!commandAsk(d) || d?.multi) return null;
  const options = d?.options ?? [];
  if (options.length !== 2 || options.some((o) => o.kind)) return null;
  const words = options.map((o) => o.label.trim().toLowerCase());
  return words[0] === "yes" && words[1] === "no" ? { allow: options[0], deny: options[1] } : null;
}
