// The composer's @ menu (iOS PromptComposer.swift PromptItems, BeautifulUI PromptToken): first-screen mention parsing
// and session display helpers. Panel-only listing and quote/workspace builders live in lib/mentions-panel.ts so their
// code and lib-mentions strings stay off the first screen. No DOM here.

import { t } from "../i18n";
import { normalizeTraeCliName } from "./agent-kind";
import { baseName } from "./format";
import type { Item, Session } from "./types";

export interface MentionToken {
  /** Offset of the `@`. */
  start: number;
  /** Offset just past the query (the caret). */
  end: number;
  query: string;
}

/** A typed `@word` at a word boundary. Object replacement characters mark atomic editor tokens. */
export function mentionAt(text: string, caret: number): MentionToken | null {
  const before = text.slice(0, caret);
  const m = /(^|[\s\uFFFC\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}\p{Script=Hangul}])@([^\s@\uFFFC]*)$/u.exec(before);
  if (!m) return null;
  const start = before.length - m[2].length - 1;
  return { start, end: caret, query: m[2] };
}

/** The draft with the token replaced by `insert`, and where the caret goes. */
export function replaceToken(text: string, token: MentionToken, insert: string): { text: string; caret: number } {
  const before = text.slice(0, token.start);
  let after = text.slice(token.end);
  // a picked file ends in a space: don't double it
  if (insert.endsWith(" ") && after.startsWith(" ")) after = after.slice(1);
  return { text: before + insert + after, caret: before.length + insert.length };
}

/** Files the chat's tools read or wrote, newest first, at most `limit` (PromptItems.files). Absolute paths. */
export function chatFiles(items: Item[], limit = 12): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (let i = items.length - 1; i >= 0 && out.length < limit; i--) {
    const it = items[i];
    if (it.kind !== "tool_use") continue;
    const p = it.tool_input?.file_path ?? it.tool_input?.notebook_path;
    if (typeof p !== "string" || !p || seen.has(p)) continue;
    seen.add(p);
    out.push(p);
  }
  return out;
}

/** Who answers in a session ("Claude" / "Codex" / "TRAE CLI" / a runtime's own name; Session.agentLabel). */
export function agentLabel(s: Pick<Session, "agent" | "agent_label" | "source_kind">): string {
  if (s.agent === "codex" || s.source_kind === "codex_app_server" || s.source_kind === "codex_history") return "Codex";
  if (!s.agent || s.agent === "claude") return "Claude";
  return normalizeTraeCliName(s.agent_label || s.agent);
}

/** A session's folder by its name: "app" for /home/me/code/app, and for C:\Users\me\code\app, a Windows computer's path
 *  read by its own separator (`baseName`, #9901); "" for none or the root. */
export function folderName(cwd?: string): string {
  return /[^\\/]/.test(cwd ?? "") ? baseName(cwd) : "";
}

/** A session's title as the @ menu shows it (Session.displayTitle). */
export function displayTitle(s: Pick<Session, "title" | "cwd">): string {
  const title = s.title?.trim();
  return !title || title === "Claude Code" ? folderName(s.cwd) || t("lib-format.untitled_session") : title;
}
