// Go to definition in a code component (UI version 13, docs/extensions/extensions.md "UI version 13"): which name in
// which line a click or the caret is on, and the values a code's navigate action is sent with. Pure, so it is tested
// without a DOM; views/extensions/CodeViews.tsx reads the click and the keys.

export type NavIntent = "definition" | "references" | "back";

/** Characters a name is made of: letters, digits, `_` and `$` in any script. */
const NAME = /[\p{L}\p{N}_$]/u;

/**
 * The name at column (1-based, in characters) of line: the run of name characters the column is in, or just after
 * (the caret at a name's end). Empty when there is none.
 */
export function wordAt(line: string, column: number): string {
  const chars = Array.from(line);
  let i = Math.min(Math.max(column - 1, 0), chars.length);
  if (!NAME.test(chars[i] ?? "") && i > 0 && NAME.test(chars[i - 1])) i--;
  if (!NAME.test(chars[i] ?? "")) return "";
  let a = i;
  let b = i;
  while (a > 0 && NAME.test(chars[a - 1])) a--;
  while (b < chars.length && NAME.test(chars[b])) b++;
  return chars.slice(a, b).join("");
}

/** Columns count characters, not UTF-16 units: the column after the text before the caret. */
export const columnAfter = (before: string) => Array.from(before).length + 1;

/** The values a navigate action carries: where in the file, the name there and what to do with it. */
export function navigateValues(path: string, intent: NavIntent, at?: { line: number; column: number; text: string }): Record<string, string> {
  if (intent === "back" || !at) return { path, intent: "back" };
  return { path, line: String(at.line), column: String(at.column), word: wordAt(at.text, at.column), intent };
}

export interface NavKey {
  key: string;
  metaKey: boolean;
  ctrlKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
}

/**
 * What a key in the code asks for, IntelliJ's keys: ⌘B (Ctrl+B elsewhere) a definition, ⌥F7 (Alt+F7) the usages, ⌘[
 * (Ctrl+Alt+← elsewhere) back. Null for any other key.
 */
export function navKey(e: NavKey, mac: boolean): NavIntent | null {
  const mod = mac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
  if (mod && !e.altKey && !e.shiftKey && e.key.toLowerCase() === "b") return "definition";
  if (e.altKey && !e.metaKey && !e.ctrlKey && !e.shiftKey && e.key === "F7") return "references";
  if (mac && e.metaKey && !e.ctrlKey && !e.altKey && !e.shiftKey && e.key === "[") return "back";
  if (!mac && e.ctrlKey && e.altKey && !e.metaKey && !e.shiftKey && e.key === "ArrowLeft") return "back";
  return null;
}

/** Whether a click follows the name it is on: ⌘-click on a Mac, Ctrl-click elsewhere. */
export const navClick = (e: { metaKey: boolean; ctrlKey: boolean; altKey: boolean; shiftKey: boolean }, mac: boolean) =>
  (mac ? e.metaKey : e.ctrlKey) && !e.altKey && !e.shiftKey;
