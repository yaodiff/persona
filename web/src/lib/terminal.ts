// The web terminal's pure logic (docs/integrations/terminal.md, docs/protocol/protocol.md "Terminal stream"): which frames to apply in what order,
// base64 → VT bytes, the link status wording and its backoff, the wheel and click geometry, paste bytes, and the xterm
// palette from the page's color tokens. The panel (views/session/terminal/) owns the DOM and the socket.
import { formatDate, t } from "../i18n";

export interface TerminalFrame {
  epoch: string;
  seq: number;
  full: boolean;
  /** The grid the frame is drawn at: the pane's when watching, the panel's when driving. */
  cols: number;
  rows: number;
  /** base64 of the VT bytes. */
  data: string;
}

export type TerminalPush =
  | { kind: "frame"; frame: TerminalFrame }
  | { kind: "ended"; error: string; unsupported: boolean };

/** A `terminal` push as a frame or the stream's end; null for anything else (missing fields). */
export function parseTerminalPush(msg: Record<string, unknown>): TerminalPush | null {
  if (typeof msg.error === "string" && msg.error) return { kind: "ended", error: msg.error, unsupported: msg.unsupported === true };
  const { epoch, seq, data } = msg;
  if (typeof epoch !== "string" || typeof seq !== "number" || typeof data !== "string") return null;
  const num = (v: unknown) => (typeof v === "number" && v > 0 ? Math.floor(v) : 0);
  return { kind: "frame", frame: { epoch, seq, full: msg.full === true, cols: num(msg.cols), rows: num(msg.rows), data } };
}

/** base64 → bytes (xterm decodes the UTF-8 itself, across frames); null when it is not base64. */
export function decodeBase64(b64: string): Uint8Array | null {
  let bin: string;
  try {
    bin = atob(b64);
  } catch {
    return null;
  }
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

/**
 * apply: draw it. duplicate: a replay overlapping what arrived live. stale: the tail of a stream given up on (an old
 * epoch, or diffs before the whole frame a fresh subscription is waiting for). gap: a diff that does not follow —
 * subscribe again without an epoch and start over from a whole frame.
 */
export type FrameVerdict = "apply" | "duplicate" | "stale" | "gap";

/** How long a fresh subscription waits for its whole frame before a stray diff counts as a gap again. */
export const AWAIT_FULL_MS = 5000;

/**
 * Where a stream stands: the epoch and last seq applied. A whole frame always applies; a diff only right after the
 * frame before it in the same epoch. After a gap (or a fresh subscription) diffs that do not follow are the old
 * stream's tail until the whole frame arrives, so one missing diff costs one resubscription, not one per frame.
 */
export class FrameOrder {
  epoch: string | null = null;
  seq = 0;
  private retired = new Set<string>();
  private awaitingSince: number | null = null;

  get awaitingFull() {
    return this.awaitingSince !== null;
  }

  /** A subscription without an epoch went out: its replay starts with a whole frame. */
  restart(now: number) {
    this.awaitingSince = now;
  }

  /** The stream ended: nothing to continue from. */
  reset() {
    if (this.epoch) this.retired.add(this.epoch);
    this.epoch = null;
    this.seq = 0;
    this.awaitingSince = null;
  }

  accept(f: Pick<TerminalFrame, "epoch" | "seq" | "full">, now: number): FrameVerdict {
    if (f.full) {
      if (this.epoch && this.epoch !== f.epoch) this.retired.add(this.epoch);
      this.retired.delete(f.epoch);
      this.epoch = f.epoch;
      this.seq = f.seq;
      this.awaitingSince = null;
      return "apply";
    }
    if (f.epoch === this.epoch) {
      if (f.seq <= this.seq) return "duplicate";
      if (f.seq === this.seq + 1) {
        this.seq = f.seq;
        return "apply";
      }
    } else if (this.retired.has(f.epoch)) {
      return "stale";
    }
    if (this.awaitingSince !== null && now - this.awaitingSince < AWAIT_FULL_MS) return "stale";
    this.epoch = null;
    this.seq = 0;
    this.awaitingSince = now;
    return "gap";
  }
}

// ── link status (iOS TerminalLinkStatus) ────────────────

/** Older than this without a fresh screen counts as stale even before anything fails (a request may hang). */
export const STALE_AFTER_MS = 4000;
/** How long a subscription may go unanswered (no first frame, no word on taking the terminal over) before the feed
 * gives up on it, says why and tries again: the takeover went with a node connection that dropped, or the computer
 * doesn't answer (iOS TerminalFeed.answerLimit, #6327). */
export const ANSWER_LIMIT_MS = 10_000;

/** Seconds to wait after the n-th failure in a row: 1, 2, 4, 8, then every 15. */
export function backoff(failures: number): number {
  return Math.min(15, 2 ** Math.max(0, failures - 1));
}

/** Whether the screen may be old: something failed, or nothing fresh for a while. */
export function isStale(lastOK: number | null, failure: string | null, now: number, since: number | null = null): boolean {
  if (failure) return true;
  const ref = lastOK ?? since;
  return ref !== null && now - ref > STALE_AFTER_MS;
}

/** "Updated 12 seconds ago" / "No terminal yet". */
export function linkHeadline(lastOK: number | null, now: number): string {
  if (lastOK === null) return t("lib-terminal.no_screen_yet");
  const s = Math.max(0, Math.floor((now - lastOK) / 1000));
  if (s < 60) return t("lib-terminal.updated_seconds", { count: s });
  if (s < 3600) return t("lib-terminal.updated_minutes", { count: Math.floor(s / 60) });
  return t("lib-terminal.updated_at", { time: formatDate(lastOK, { hour: "2-digit", minute: "2-digit", hour12: false }) });
}

/** When it tries again, then why: "Retrying in 4s · can't reach the hub", or "Waiting for the computer…" while nothing failed yet. */
export function linkDetail(failure: string | null, nextTry: number, now: number): string {
  if (!failure) return t("lib-terminal.waiting");
  const s = Math.ceil((nextTry - now) / 1000);
  return `${s > 0 ? t("lib-terminal.retry_in", { count: s }) : t("lib-terminal.retrying")} · ${failure}`;
}

// ── input ───────────────────────────────────────────────

/** Pasted text as terminal bytes: as it is, or — with lines in it — one bracketed paste with a return per line. */
export function pasteBytes(text: string): string {
  if (!/[\r\n]/.test(text)) return text;
  return "\x1b[200~" + text.replace(/\r\n/g, "\n").replace(/\n/g, "\r") + "\x1b[201~";
}

/** How many lines a paste has (a trailing newline does not start another). */
export function lineCount(text: string): number {
  return text.replace(/\r\n/g, "\n").replace(/\n$/, "").split("\n").length;
}

/** The cell ([column, row], 0-based) under a point of the screen, clamped to the grid. */
export function cellAt(x: number, y: number, width: number, height: number, cols: number, rows: number): [number, number] {
  const clamp = (v: number, n: number) => Math.max(0, Math.min(n - 1, v));
  const col = width > 0 && cols > 0 ? Math.floor((x / width) * cols) : 0;
  const row = height > 0 && rows > 0 ? Math.floor((y / height) * rows) : 0;
  return [clamp(col, cols), clamp(row, rows)];
}

/**
 * Wheel deltas → whole lines for `terminal_input.scroll` (> 0 back into history). Trackpads send many small pixel
 * deltas: the fraction carries over to the next event, and is dropped when the direction turns.
 */
export class WheelLines {
  private rest = 0;

  add(deltaY: number, deltaMode: number, cellHeight: number, pageRows: number): number {
    if (!cellHeight || !deltaY) return 0;
    const px = deltaMode === 1 ? deltaY * cellHeight : deltaMode === 2 ? deltaY * cellHeight * Math.max(1, pageRows) : deltaY;
    const lines = -px / cellHeight;
    if (Math.sign(lines) !== Math.sign(this.rest)) this.rest = 0;
    this.rest += lines;
    const n = Math.trunc(this.rest) || 0; // never -0
    this.rest -= n;
    return n;
  }

  reset() {
    this.rest = 0;
  }
}

/** A grid worth driving at (smaller than this, Claude Code cannot lay itself out). */
export function usableGrid(g: { cols: number; rows: number } | null | undefined): g is { cols: number; rows: number } {
  return Boolean(g && g.cols >= 10 && g.rows >= 4);
}

/**
 * A font stack with a range-limited `family` first. Named desktop fallbacks can resolve to a CJK face, so the
 * size-adjusted face must precede them as well as the generic family. Its unicode-range limits which glyphs it draws.
 */
export function withFamily(stack: string, family: string): string {
  const parts = stack.split(",").map((s) => s.trim()).filter(Boolean);
  const generic = parts.length && /^monospace$/i.test(parts[parts.length - 1]) ? parts.pop()! : "monospace";
  return [`"${family}"`, ...parts.filter((p) => p !== `"${family}"`), generic].join(", ");
}

// ── colors ──────────────────────────────────────────────

export interface TerminalPalette {
  background: string;
  foreground: string;
  cursor: string;
  cursorAccent: string;
  selectionBackground: string;
  selectionInactiveBackground: string;
  black: string;
  red: string;
  green: string;
  yellow: string;
  blue: string;
  magenta: string;
  cyan: string;
  white: string;
  brightBlack: string;
  brightRed: string;
  brightGreen: string;
  brightYellow: string;
  brightBlue: string;
  brightMagenta: string;
  brightCyan: string;
  brightWhite: string;
}

/**
 * xterm's colors from the page's tokens (`token("--page")` → its value), so the terminal follows light / dark.
 * Magenta and cyan have no page token yet: `--term-magenta` / `--term-cyan` when the stylesheet has them, else the
 * nearest role colors.
 */
export function terminalPalette(token: (name: string) => string, dark: boolean): TerminalPalette {
  const tok = (name: string, ...fallbacks: string[]) => [name, ...fallbacks].map((n) => token(n).trim()).find(Boolean) ?? "";
  const ink = tok("--ink"), ink2 = tok("--ink-2"), ink3 = tok("--ink-3");
  const magenta = tok("--term-magenta", "--red"), cyan = tok("--term-cyan", "--accent-ink");
  return {
    background: tok("--page"),
    foreground: ink,
    cursor: ink,
    cursorAccent: tok("--page"),
    selectionBackground: tok("--accent-tint"),
    selectionInactiveBackground: tok("--hover-2"),
    black: dark ? tok("--line-strong") : ink,
    red: tok("--red"),
    green: tok("--green"),
    yellow: tok("--orange"),
    blue: tok("--accent"),
    magenta,
    cyan,
    white: ink2,
    brightBlack: ink3,
    brightRed: tok("--red"),
    brightGreen: tok("--green"),
    brightYellow: tok("--orange"),
    brightBlue: tok("--accent-ink"),
    brightMagenta: magenta,
    brightCyan: cyan,
    brightWhite: ink,
  };
}

// ── keys (iOS TerminalKeyBytes, TerminalModifiers) ──────

/**
 * A key by herdr's name ("enter", "ctrl+c", "shift+tab", "alt+left") as terminal bytes, as iOS TerminalKeyBytes: CSI
 * for arrows (`1;m` with modifiers), the control character for ctrl+letter, ESC before the key for alt. shift+enter /
 * alt+enter are ESC CR, which Claude Code and Codex take as a new line. `appCursor`: the program asked for application
 * cursor keys (DECCKM), so a bare arrow is SS3 (`ESC O A`), as xterm itself sends then. null for a name it cannot type.
 */
export function encodeKey(name: string, appCursor = false): string | null {
  let parts = name.toLowerCase().split("+");
  // "ctrl++": the key is "+"
  if (parts.length > 2 && parts[parts.length - 1] === "" && parts[parts.length - 2] === "") parts = [...parts.slice(0, -2), "+"];
  const base = parts.pop();
  if (!base) return null;
  const ctrl = parts.includes("ctrl") || parts.includes("control");
  const alt = parts.includes("alt") || parts.includes("meta") || parts.includes("option");
  const shift = parts.includes("shift");
  const esc = "\x1b";
  const csiFinal: Record<string, string> = { up: "A", down: "B", right: "C", left: "D", home: "H", end: "F" };
  const tilde: Record<string, number> = {
    insert: 2, delete: 3, pageup: 5, pagedown: 6, f5: 15, f6: 17, f7: 18, f8: 19, f9: 20, f10: 21, f11: 23, f12: 24,
  };
  const ss3: Record<string, string> = { f1: "P", f2: "Q", f3: "R", f4: "S" };
  const mod = 1 + (shift ? 1 : 0) + (alt ? 2 : 0) + (ctrl ? 4 : 0);
  if (csiFinal[base]) return mod === 1 ? `${esc}${appCursor ? "O" : "["}${csiFinal[base]}` : `${esc}[1;${mod}${csiFinal[base]}`;
  if (tilde[base]) return mod === 1 ? `${esc}[${tilde[base]}~` : `${esc}[${tilde[base]};${mod}~`;
  if (ss3[base]) return mod === 1 ? `${esc}O${ss3[base]}` : `${esc}[1;${mod}${ss3[base]}`;
  const prefix = alt ? esc : "";
  switch (base) {
    case "enter":
    case "return":
      return shift || alt ? `${esc}\r` : "\r";
    case "tab":
      return shift ? `${esc}[Z` : `${prefix}\t`;
    case "esc":
    case "escape":
      return esc;
    case "backspace":
      return prefix + (ctrl ? "\x08" : "\x7f");
    case "space":
      return prefix + (ctrl ? "\x00" : " ");
  }
  if ([...base].length !== 1) return null;
  if (ctrl) {
    if (base >= "a" && base <= "z") return prefix + String.fromCharCode(base.charCodeAt(0) - 0x60);
    switch (base) {
      case "@":
      case "2":
        return `${prefix}\x00`;
      case "[":
      case "3":
        return esc;
      case "\\":
      case "4":
        return `${prefix}\x1c`;
      case "]":
      case "5":
        return `${prefix}\x1d`;
      case "^":
      case "6":
        return `${prefix}\x1e`;
      case "_":
      case "-":
      case "7":
      case "/":
        return `${prefix}\x1f`;
      case "?":
      case "8":
        return `${prefix}\x7f`;
      default:
        return prefix + base;
    }
  }
  return prefix + (shift ? base.toUpperCase() : base);
}

// ── keys the page takes before xterm, and the terminal font ──

export interface KeyEventLike {
  type: string;
  key: string;
  keyCode?: number;
  isComposing?: boolean;
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
}

/**
 * What the page does with a key event before xterm sees it. xterm: leave it to xterm (everything during an IME
 * composition — keyCode 229 is the browsers that do not set isComposing). copy: ⌃C / ⌘C / ⌃⇧C with text selected copies
 * it instead of sending ^C (with nothing selected ^C still interrupts). newline: shift+enter is ESC CR, a new line in
 * Claude Code's box (xterm would send a bare CR). swallow: the keypress / keyup of a key handled on keydown.
 */
export type KeyDecision = "xterm" | "copy" | "newline" | "swallow";

export function keyDecision(e: KeyEventLike, hasSelection: boolean): KeyDecision {
  if (e.isComposing || e.keyCode === 229) return "xterm";
  if (e.key === "Enter" && e.shiftKey && !e.ctrlKey && !e.metaKey && !e.altKey) return e.type === "keydown" ? "newline" : "swallow";
  if (hasSelection && (e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === "c") return e.type === "keydown" ? "copy" : "swallow";
  return "xterm";
}

/** The `document.fonts.load` query for the terminal's font: its size and stack, and a sample of a Latin and a Chinese character. */
export function fontLoadQuery(size: number, stack: string): { font: string; sample: string } {
  return { font: `${size}px ${stack}`, sample: "W中" }; // i18n-ignore: a CJK glyph so the font's CJK face loads
}

/** Whichever comes first: the promise (settled either way) or `ms` — so a font that never loads cannot hold the terminal back. */
export function settleWithin(p: Promise<unknown>, ms: number): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms);
    const done = () => {
      clearTimeout(timer);
      resolve();
    };
    p.then(done, done);
  });
}

/** What xterm sends for a key, back to the key's name (so key bar modifiers can apply to it); null for anything else. */
export function keyNameForBytes(data: string): string | null {
  const names: Record<string, string> = {
    "\r": "enter", "\x7f": "backspace", "\b": "backspace", "\t": "tab", "\x1b": "esc",
    "\x1b[A": "up", "\x1b[B": "down", "\x1b[C": "right", "\x1b[D": "left",
    "\x1bOA": "up", "\x1bOB": "down", "\x1bOC": "right", "\x1bOD": "left",
    "\x1b[H": "home", "\x1b[F": "end", "\x1bOH": "home", "\x1bOF": "end",
    "\x1b[2~": "insert", "\x1b[3~": "delete", "\x1b[5~": "pageup", "\x1b[6~": "pagedown",
  };
  return names[data] ?? null;
}

/** One modifier on the key bar, as iOS (Moshi's): a tap arms it for the next key, a quick second tap locks it, holding keeps it down. */
export type ModifierState = "off" | "once" | "held" | "locked";
export type ModifierKey = "ctrl" | "alt" | "shift";
export const MODIFIER_KEYS: ModifierKey[] = ["ctrl", "alt", "shift"];

export interface Modifiers {
  ctrl: ModifierState;
  alt: ModifierState;
  shift: ModifierState;
  /** A key went out while one was held (letting go then releases it instead of arming it). */
  usedWhileHeld?: boolean;
}

export const NO_MODIFIERS: Modifiers = { ctrl: "off", alt: "off", shift: "off" };

export function modifiersActive(m: Modifiers): boolean {
  return MODIFIER_KEYS.some((k) => m[k] !== "off");
}

/** The key name with the modifiers that are down: "ctrl+alt+x", "shift+tab". esc goes out bare. */
export function withModifiers(m: Modifiers, key: string): string {
  if (key === "esc") return key;
  return [...MODIFIER_KEYS.filter((k) => m[k] !== "off"), key].join("+");
}

/** A key went out: armed-once modifiers release, held and locked ones stay. */
export function modifiersPressed(m: Modifiers): Modifiers {
  const out: Modifiers = { ...m };
  for (const k of MODIFIER_KEYS) {
    if (out[k] === "once") out[k] = "off";
    if (out[k] === "held") out.usedWhileHeld = true;
  }
  return out;
}

/** A tap; `quick` when it follows the previous tap on the same key closely (a double tap locks it). */
export function modifierTapped(m: Modifiers, k: ModifierKey, quick: boolean): Modifiers {
  const next: ModifierState = m[k] === "off" ? "once" : m[k] === "once" ? (quick ? "locked" : "off") : "off";
  return { ...m, [k]: next };
}

/** Held long enough to count as holding it down. */
export function modifierHeld(m: Modifiers, k: ModifierKey): Modifiers {
  return { ...m, [k]: "held", usedWhileHeld: false };
}

/** Let go after holding: off when a key went out meanwhile, else armed for the next one. */
export function modifierReleased(m: Modifiers, k: ModifierKey): Modifiers {
  if (m[k] !== "held") return m;
  return { ...m, [k]: m.usedWhileHeld ? "off" : "once" };
}

/**
 * What one typed character sends while modifiers are down: a key by name ("ctrl+c"), or — shift alone, or a character
 * no key name covers — the text itself (shift capitalized).
 */
export function typedWithModifiers(m: Modifiers, ch: string): { key?: string; text?: string } {
  const ctrlOrAlt = m.ctrl !== "off" || m.alt !== "off";
  if (!ctrlOrAlt) return { text: m.shift !== "off" ? ch.toUpperCase() : ch };
  if (!/^[\x20-\x7e\t]$/.test(ch)) return { text: ch };
  const name = ch === " " ? "space" : ch === "\t" ? "tab" : ch.toLowerCase();
  return { key: withModifiers(m, name) };
}

// ── saved commands (iOS TerminalPrefs.shortcuts) ────────

export const DEFAULT_SHORTCUTS = ["git status", "git diff --stat", "ls -la", "/compact", "/status"];

/** The saved commands from storage: the defaults until there is a list (an empty list stays empty). */
export function decodeShortcuts(json: string | null): string[] {
  if (!json) return [...DEFAULT_SHORTCUTS];
  try {
    const v: unknown = JSON.parse(json);
    if (Array.isArray(v) && v.every((x) => typeof x === "string")) return v;
  } catch {
    // fall through
  }
  return [...DEFAULT_SHORTCUTS];
}

/** A command moved up (-1) or down (+1); out of range leaves the list as it is. */
export function moveShortcut(list: string[], at: number, by: number): string[] {
  const to = at + by;
  if (at < 0 || at >= list.length || to < 0 || to >= list.length) return list;
  const out = [...list];
  [out[at], out[to]] = [out[to], out[at]];
  return out;
}

// ── history (iOS TerminalFeed.loadOlder) ────────────────

/** Lines per page of history, as iOS. */
export const HISTORY_PAGE = 200;

/**
 * What `POST …/screen/before` locates the page by: the first 20 plain lines already shown (history, then the screen
 * down to its last line with text — herdr's reads end there) and how many lines that is in all.
 */
export function historyAnchor(lines: string[]): { anchor: string[]; loaded: number } {
  const all = lines.map((l) => l.replace(/\s+$/, ""));
  while (all.length && !all[all.length - 1]) all.pop();
  return { anchor: all.slice(0, 20), loaded: all.length };
}
