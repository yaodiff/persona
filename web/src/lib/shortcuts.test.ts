import { afterEach, describe, expect, it, vi } from "vitest";
import {
  activeBindings,
  ariaKeyshortcuts,
  bindingsOf,
  clearBinding,
  conflictOf,
  EMPTY_PREFS,
  getBindings,
  getConfigured,
  getShortcutPrefs,
  isDefault,
  replaceBinding,
  resetAllBindings,
  resetBinding,
  sanitizePrefs,
  setBinding,
  setShortcutEnabled,
  setShortcutsOn,
  canonChord,
  checkBinding,
  chordOfEvent,
  chordParts,
  dispatchKey,
  eventChord,
  expandChord,
  focusKind,
  formatChord,
  holdShortcuts,
  jumpHint,
  LOCAL_SHORTCUTS,
  matchShortcut,
  parseChord,
  passesToApp,
  platformOf,
  registerShortcut,
  reservedWhy,
  RESERVED,
  runShortcut,
  sanitizeOverrides,
  SHORTCUTS,
  type Bindings,
  type Dispatchable,
  type KeyEventLike,
} from "./shortcuts";

const key = (k: string, mods: Partial<Pick<KeyEventLike, "ctrlKey" | "metaKey" | "altKey" | "shiftKey">> = {}, extra: Partial<KeyEventLike> = {}): KeyEventLike => ({
  key: k,
  ctrlKey: false,
  metaKey: false,
  altKey: false,
  shiftKey: false,
  ...mods,
  ...extra,
});
const ctrl = (k: string, more: Partial<KeyEventLike> = {}, extra: Partial<KeyEventLike> = {}) => key(k, { ctrlKey: true, ...more }, extra);
const cmd = (k: string, more: Partial<KeyEventLike> = {}, extra: Partial<KeyEventLike> = {}) => key(k, { metaKey: true, ...more }, extra);

describe("platform", () => {
  it("is a Mac for Apple platforms and Windows / Linux otherwise", () => {
    expect(platformOf({ platform: "MacIntel" })).toBe("mac");
    expect(platformOf({ userAgentData: { platform: "macOS" }, platform: "x" })).toBe("mac");
    expect(platformOf({ userAgent: "Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X)" })).toBe("mac");
    expect(platformOf({ platform: "Linux x86_64" })).toBe("other");
    expect(platformOf({ platform: "Win32" })).toBe("other");
    expect(platformOf(undefined)).toBe("other");
  });
});

describe("chord parsing", () => {
  it("reads a chord into its parts and spells it one way", () => {
    expect(parseChord("Mod+Alt+Shift+Up")).toEqual({ alt: true, shift: true, key: "Up" });
    expect(canonChord("ctrl+shift+p")).toBe("Mod+Shift+P");
    expect(canonChord("Shift+Alt+mod+b")).toBe("Mod+Alt+Shift+B");
    expect(canonChord("alt+Ctrl+b")).toBe("Mod+Alt+B");
    expect(canonChord("cmd+,")).toBe("Mod+,");
    expect(canonChord("Mod+enter")).toBe("Mod+Enter");
    expect(canonChord("Mod+f5")).toBe("Mod+F5");
    expect(canonChord("Mod+1-9")).toBe("Mod+1-9");
  });
  it("needs Mod and a key: a bare key is not a chord", () => {
    expect(parseChord("K")).toBeNull();
    expect(parseChord("Shift+K")).toBeNull();
    expect(parseChord("Alt+K")).toBeNull();
    expect(parseChord("Mod")).toBeNull();
    expect(parseChord("Mod+")).toBeNull();
    expect(parseChord("Mod+Esc")).toBeNull();
    expect(parseChord("Mod+Tab")).toBeNull();
    expect(parseChord("Mod+Hyper+K")).toBeNull();
    expect(parseChord("Mod+F13")).toBeNull();
  });
  it("expands the digits chord into its nine", () => {
    expect(expandChord("Mod+Alt+1-9")).toEqual(Array.from({ length: 9 }, (_, i) => `Mod+Alt+${i + 1}`));
    expect(expandChord("Mod+K")).toEqual(["Mod+K"]);
    expect(expandChord("nope")).toEqual([]);
  });
});

describe("chord formatting", () => {
  it("writes Ctrl / Alt / Shift on Windows and Linux", () => {
    expect(chordParts("Mod+Alt+Shift+Up", "other")).toEqual(["Ctrl", "Alt", "Shift", "↑"]);
    expect(formatChord("Mod+Shift+P", "other")).toBe("Ctrl+Shift+P");
    expect(formatChord("Mod+1-9", "other")).toBe("Ctrl+1–9");
  });
  it("writes ⌥⇧⌘ in Apple's order on a Mac", () => {
    expect(chordParts("Mod+Alt+Shift+Up", "mac")).toEqual(["⌥", "⇧", "⌘", "↑"]);
    expect(formatChord("Mod+K", "mac")).toBe("⌘K");
    expect(formatChord("Mod+Alt+Left", "mac")).toBe("⌥⌘←");
    expect(formatChord("Mod+,", "mac")).toBe("⌘,");
  });
  it("is empty for something that is not a chord", () => {
    expect(chordParts("K", "mac")).toEqual([]);
  });
});

describe("keyboard events", () => {
  it("takes Ctrl as Mod on Windows / Linux and ⌘ as Mod on a Mac, not the other way round", () => {
    expect(chordOfEvent(ctrl("k"), "other")).toBe("Mod+K");
    expect(chordOfEvent(cmd("k"), "other")).toBeNull();
    expect(chordOfEvent(cmd("k"), "mac")).toBe("Mod+K");
    // ⌃K on a Mac is the terminal's (kill line)
    expect(chordOfEvent(ctrl("k"), "mac")).toBeNull();
    expect(chordOfEvent(ctrl("k", { metaKey: true }), "mac")).toBeNull();
  });
  it("is nothing without Mod: bare keys, Shift and Alt alone", () => {
    expect(chordOfEvent(key("k"), "other")).toBeNull();
    expect(chordOfEvent(key("K", { shiftKey: true }), "other")).toBeNull();
    expect(chordOfEvent(key("k", { altKey: true }), "mac")).toBeNull();
    expect(chordOfEvent(key("Enter", { shiftKey: true }), "other")).toBeNull();
  });
  it("is nothing while only a modifier is down, or for keys chords do not use", () => {
    expect(chordOfEvent(ctrl("Control"), "other")).toBeNull();
    expect(chordOfEvent(cmd("Meta"), "mac")).toBeNull();
    expect(chordOfEvent(ctrl("Escape"), "other")).toBeNull();
    expect(chordOfEvent(ctrl("Tab"), "other")).toBeNull();
  });
  it("names a key by its position when Option composed or Shift changed what it types", () => {
    expect(chordOfEvent(cmd("∫", { altKey: true }, { code: "KeyB" }), "mac")).toBe("Mod+Alt+B");
    expect(chordOfEvent(ctrl("!", { shiftKey: true }, { code: "Digit1" }), "other")).toBe("Mod+Shift+1");
    expect(chordOfEvent(ctrl("<", { shiftKey: true }, { code: "Comma" }), "other")).toBe("Mod+Shift+,");
    expect(chordOfEvent(ctrl("ArrowUp", { altKey: true, shiftKey: true }, { code: "ArrowUp" }), "other")).toBe("Mod+Alt+Shift+Up");
    expect(chordOfEvent(ctrl("ArrowLeft", { altKey: true }), "other")).toBe("Mod+Alt+Left");
    expect(chordOfEvent(ctrl("Enter", { shiftKey: true }), "other")).toBe("Mod+Shift+Enter");
  });
  it("follows the layout's labels for letters", () => {
    // Dvorak: the key at KeyJ's position types "c"
    expect(chordOfEvent(ctrl("c", {}, { code: "KeyJ" }), "other")).toBe("Mod+C");
  });
  it("does not take AltGr (Ctrl+Alt on some layouts) for Mod+Alt", () => {
    const altGr = ctrl("@", { altKey: true }, { code: "KeyQ", getModifierState: (k: string) => k === "AltGraph" });
    expect(chordOfEvent(altGr, "other")).toBeNull();
    expect(eventChord(altGr, "other")?.mod).toBe(false);
  });
  it("keeps the chord of a key without Mod for the settings to answer", () => {
    expect(eventChord(key("k"), "other")).toEqual({ alt: false, shift: false, key: "K", mod: false });
  });
});

describe("the registry", () => {
  it("has a Chinese label, a valid default chord and a scope for every command, each id and chord once", () => {
    const ids = new Set<string>();
    const chords = new Set<string>();
    for (const s of SHORTCUTS) {
      expect(s.label).toMatch(/[一-鿿]/);
      expect(parseChord(s.chord), s.id).not.toBeNull();
      expect(canonChord(s.chord)).toBe(s.chord);
      expect(["global", "session", "sidebar"]).toContain(s.scope);
      expect(ids.has(s.id)).toBe(false);
      ids.add(s.id);
      for (const c of expandChord(s.chord)) {
        expect(chords.has(c), `${s.id} ${c}`).toBe(false);
        chords.add(c);
      }
    }
  });
  it("carries Moshi's keymap", () => {
    const defaults = Object.fromEntries(SHORTCUTS.map((s) => [s.id, s.chord]));
    expect(defaults).toMatchObject({
      jump: "Mod+K",
      commands: "Mod+Shift+P",
      "terminal-chat": "Mod+J",
      "terminal-mode": "Mod+Alt+F",
      "right-panel": "Mod+B",
      "left-sidebar": "Mod+Alt+B",
      "sidebar-grouping": "Mod+E",
      maximize: "Mod+Shift+Enter",
      "focus-input": "Mod+I",
      "collapse-tree": "Mod+Alt+Shift+Up",
      "expand-tree": "Mod+Alt+Shift+Down",
      "prev-row": "Mod+Alt+Up",
      "next-row": "Mod+Alt+Down",
      settings: "Mod+,",
      machine: "Mod+1-9",
      "prev-tab": "Mod+Alt+Left",
      "next-tab": "Mod+Alt+Right",
      help: "Mod+/",
    });
  });
  it("never defaults to a reserved chord, on any platform", () => {
    for (const s of SHORTCUTS) for (const p of ["mac", "other"] as const) expect(reservedWhy(s.chord, p), `${s.id} on ${p}`).toBeNull();
  });
  it("every chord of the reserved list has Mod", () => {
    for (const r of RESERVED) expect(parseChord(r.chord), r.chord).not.toBeNull();
  });
});

describe("reserved chords", () => {
  it("keeps the browser's, the text fields' and (on Windows / Linux) the terminal's", () => {
    expect(reservedWhy("Mod+T", "other")).toBe("已被浏览器占用");
    expect(reservedWhy("Mod+Shift+N", "mac")).toBe("已被浏览器占用");
    expect(reservedWhy("Mod+C", "other")).toBe("已被文本编辑功能占用");
    expect(reservedWhy("Mod+Enter", "mac")).not.toBeNull();
    expect(reservedWhy("Mod+W", "other")).not.toBeNull();
    expect(reservedWhy("Mod+Q", "mac")).not.toBeNull();
  });
  it("is platform-aware: ⌘Q is the Mac's, Ctrl+Q is free; ⌃D is the terminal's only off the Mac", () => {
    expect(reservedWhy("Mod+Q", "other")).toBeNull();
    expect(reservedWhy("Mod+M", "other")).toBeNull();
    expect(reservedWhy("Mod+Z", "mac")).toBe("已被文本编辑功能占用");
  });
  it("covers a whole digits chord when one of its nine is reserved", () => {
    expect(reservedWhy("Mod+1-9", "other")).toBeNull();
    expect(reservedWhy("Mod+0", "other")).toBe("已被浏览器占用");
  });
});

describe("rebinding", () => {
  const defaults = bindingsOf({});
  const pressed = (k: string, more: Partial<KeyEventLike> = {}, mod = true) => eventChord(mod ? ctrl(k, more) : key(k, more), "other");

  it("accepts a free chord and says it canonically", () => {
    expect(checkBinding("jump", pressed("o", { shiftKey: true }), defaults, "other")).toEqual({ ok: true, chord: "Mod+Shift+O" });
  });
  it("asks for Ctrl / ⌘ when the chord has none", () => {
    expect(checkBinding("jump", pressed("o", {}, false), defaults, "other")).toMatchObject({ ok: false, reason: "mod", message: "需要包含 Ctrl" });
    expect(checkBinding("jump", eventChord(key("o"), "mac"), defaults, "mac")).toMatchObject({ ok: false, reason: "mod", message: "需要包含 ⌘" });
    expect(checkBinding("jump", eventChord(key("o", { shiftKey: true, altKey: true }), "mac"), defaults, "mac")).toMatchObject({ reason: "mod" });
  });
  it("names the command that already has the chord", () => {
    expect(checkBinding("jump", pressed("j"), defaults, "other")).toEqual({ ok: false, reason: "conflict", message: "已被「终端 ⇄ 对话」占用", with: "terminal-chat", chord: "Mod+J" });
    // its own chord is not a conflict with itself
    expect(checkBinding("jump", pressed("k"), defaults, "other")).toEqual({ ok: true, chord: "Mod+K" });
  });
  it("follows rebinding: a chord freed by moving a command is free", () => {
    const moved: Bindings = { ...defaults, "terminal-chat": "Mod+Shift+K" };
    expect(checkBinding("jump", pressed("j"), moved, "other")).toEqual({ ok: true, chord: "Mod+J" });
    expect(checkBinding("jump", pressed("k", { shiftKey: true }), moved, "other")).toMatchObject({ reason: "conflict" });
  });
  it("refuses reserved chords with the reason", () => {
    expect(checkBinding("jump", pressed("t"), defaults, "other")).toMatchObject({ ok: false, reason: "reserved" });
    expect(checkBinding("jump", pressed("t"), defaults, "other")).toMatchObject({ message: expect.stringContaining("已被浏览器占用") });
  });
  it("refuses keys chords do not use", () => {
    expect(checkBinding("jump", eventChord(ctrl("Tab"), "other"), defaults, "other")).toMatchObject({ ok: false, reason: "key" });
    expect(checkBinding("jump", null, defaults, "other")).toMatchObject({ ok: false, reason: "key" });
  });
  it("binds the digits command to the nine digits, whatever one it was pressed on", () => {
    expect(checkBinding("machine", pressed("3", { altKey: true }), defaults, "other")).toEqual({ ok: true, chord: "Mod+Alt+1-9" });
    expect(checkBinding("machine", pressed("k"), defaults, "other")).toMatchObject({ ok: false, reason: "digits" });
  });
  it("sees a digit chord taking one of another command's digits as a conflict", () => {
    const withDigit: Bindings = { ...defaults, jump: "Mod+Alt+4" };
    expect(checkBinding("machine", pressed("1", { altKey: true }), withDigit, "other")).toMatchObject({ ok: false, reason: "conflict", with: "jump" });
    expect(checkBinding("jump", pressed("5"), defaults, "other")).toMatchObject({ reason: "conflict", with: "machine" });
  });
});

describe("stored bindings", () => {
  it("keeps what still makes sense", () => {
    expect(sanitizeOverrides({ jump: "ctrl+shift+o", help: "Mod+F1" }, "other")).toEqual({ jump: "Mod+Shift+O", help: "Mod+F1" });
  });
  it("drops unknown commands, bad chords, reserved ones and chords that are the default anyway", () => {
    expect(sanitizeOverrides({ nope: "Mod+O", jump: "K", help: "Mod+T", settings: "Mod+," }, "other")).toEqual({});
    expect(sanitizeOverrides("x")).toEqual({});
    expect(sanitizeOverrides(null)).toEqual({});
    expect(sanitizeOverrides({ jump: 3 })).toEqual({});
  });
  it("lets only one command keep a chord stored twice", () => {
    const out = sanitizeOverrides({ jump: "Mod+Shift+O", help: "Mod+Shift+O" }, "other");
    expect(out).toEqual({ jump: "Mod+Shift+O" });
  });
  it("reads the effective chord of every command", () => {
    const b = bindingsOf({ jump: "Mod+Shift+O" });
    expect(b.jump).toBe("Mod+Shift+O");
    expect(b.commands).toBe("Mod+Shift+P");
    expect(Object.keys(b).sort()).toEqual(SHORTCUTS.map((s) => s.id).sort());
  });
});

// ── the dispatcher ───────────────────────────────────────

function event(e: KeyEventLike, target?: unknown): Dispatchable & { prevented: boolean; stopped: boolean } {
  const out = {
    ...e,
    target,
    prevented: false,
    stopped: false,
    defaultPrevented: false,
    preventDefault() {
      out.prevented = true;
    },
    stopPropagation() {
      out.stopped = true;
    },
  };
  return out;
}

/** An element as `closest` sees it: it matches the selectors that mention one of `kinds`. */
const el = (...kinds: string[]) => ({ closest: (sel: string) => (kinds.some((k) => sel.includes(k)) ? {} : null) });
const terminal = el(".xterm", "textarea");
const input = el("input, textarea");
const page = el();

const cleanups: (() => void)[] = [];
const on = (id: string, run: Parameters<typeof registerShortcut>[1]) => cleanups.push(registerShortcut(id, run));
afterEach(() => {
  while (cleanups.length) cleanups.pop()!();
});

describe("focus", () => {
  it("tells the terminal from a text field from the page", () => {
    expect(focusKind(terminal)).toBe("terminal");
    expect(focusKind(input)).toBe("input");
    expect(focusKind(page)).toBe("page");
    expect(focusKind(null)).toBe("page");
    expect(focusKind(undefined)).toBe("page");
    expect(focusKind({ isContentEditable: true, closest: () => null })).toBe("input");
    // a window or document as the target
    expect(focusKind({})).toBe("page");
  });
});

describe("dispatching a key", () => {
  it("runs the command, and consumes the event so the browser (and xterm) leave it", () => {
    const run = vi.fn();
    on("jump", run);
    const e = event(ctrl("k"), page);
    expect(dispatchKey(e, { platform: "other" })).toBe(true);
    expect(run).toHaveBeenCalledWith(expect.objectContaining({ focus: "page" }));
    expect(e.prevented).toBe(true);
    expect(e.stopped).toBe(true);
  });
  it("fires while typing in a text field: every chord has Mod, so it is never a character", () => {
    const run = vi.fn();
    on("jump", run);
    const e = event(ctrl("k"), input);
    expect(dispatchKey(e, { platform: "other" })).toBe(true);
    expect(run).toHaveBeenCalledWith(expect.objectContaining({ focus: "input" }));
    expect(e.prevented).toBe(true);
  });
  it("fires in the terminal (Ctrl+K opens the palette, not ^K), saying so", () => {
    const run = vi.fn();
    on("jump", run);
    const e = event(ctrl("k"), terminal);
    expect(dispatchKey(e, { platform: "other" })).toBe(true);
    expect(run).toHaveBeenCalledWith(expect.objectContaining({ focus: "terminal" }));
    expect(e.prevented).toBe(true);
  });
  it("leaves herdr's prefix to herdr in the full surface (Ctrl+B is 右侧面板 elsewhere), and only there", () => {
    const run = vi.fn();
    on("right-panel", run);
    const full = el("[data-herdr-full]", ".xterm", "textarea");
    const inFull = event(ctrl("b"), full);
    expect(dispatchKey(inFull, { platform: "other" })).toBe(false);
    expect(inFull.prevented).toBe(false);
    expect(run).not.toHaveBeenCalled();
    // the single-pane terminal and the page keep it
    expect(dispatchKey(event(ctrl("b"), terminal), { platform: "other" })).toBe(true);
    expect(dispatchKey(event(ctrl("b"), page), { platform: "other" })).toBe(true);
    expect(run).toHaveBeenCalledTimes(2);
    // another chord of the registry is still Persona's in the full surface
    const jump = vi.fn();
    on("jump", jump);
    expect(dispatchKey(event(ctrl("k"), full), { platform: "other" })).toBe(true);
    expect(jump).toHaveBeenCalledTimes(1);
  });
  it("leaves bare keys alone, in a text field and in the terminal", () => {
    const run = vi.fn();
    for (const id of SHORTCUTS.map((s) => s.id)) on(id, run);
    for (const target of [input, terminal, page]) {
      for (const k of [key("k"), key("K", { shiftKey: true }), key("b", { altKey: true }), key("Enter", { shiftKey: true }), key("ArrowUp", { altKey: true }), key("1"), key(",")]) {
        const e = event(k, target);
        expect(dispatchKey(e, { platform: "other" })).toBe(false);
        expect(e.prevented).toBe(false);
      }
    }
    expect(run).not.toHaveBeenCalled();
  });
  it("leaves a Mod key that is nobody's alone (Ctrl+C reaches the terminal)", () => {
    for (const id of SHORTCUTS.map((s) => s.id)) on(id, vi.fn());
    for (const k of ["c", "v", "x", "a", "d", "z", "t", "w"]) {
      const e = event(ctrl(k), terminal);
      expect(dispatchKey(e, { platform: "other" })).toBe(false);
      expect(e.prevented).toBe(false);
    }
  });
  it("leaves a command alone that nothing handles here", () => {
    const e = event(ctrl("k"), page);
    expect(dispatchKey(e, { platform: "other" })).toBe(false);
    expect(e.prevented).toBe(false);
  });
  it("leaves the key alone when the command says it does not apply", () => {
    const run = vi.fn(() => false);
    on("terminal-chat", run);
    const e = event(ctrl("j"), page);
    expect(dispatchKey(e, { platform: "other" })).toBe(false);
    expect(run).toHaveBeenCalled();
    expect(e.prevented).toBe(false);
    expect(e.stopped).toBe(false);
  });
  it("hands the digit to the machine command", () => {
    const run = vi.fn();
    on("machine", run);
    expect(dispatchKey(event(ctrl("3", {}, { code: "Digit3" }), page), { platform: "other" })).toBe(true);
    expect(run).toHaveBeenLastCalledWith(expect.objectContaining({ digit: 3 }));
    expect(dispatchKey(event(ctrl("0", {}, { code: "Digit0" }), page), { platform: "other" })).toBe(false);
    expect(dispatchKey(event(ctrl("3", { altKey: true }, { code: "Digit3" }), page), { platform: "other" })).toBe(false);
  });
  it("focuses the waiting card on its default chord, Mod+Alt+Enter — free of the reserved and the other commands", () => {
    const run = vi.fn();
    on("focus-card", run);
    // Mod+Enter is the dialogs' (reserved), Mod+Shift+Enter maximizes; Mod+Alt+Enter is nobody else's, on any platform
    for (const platform of ["other", "mac"] as const) {
      expect(reservedWhy("Mod+Alt+Enter", platform)).toBeNull();
      expect(SHORTCUTS.filter((s) => s.id !== "focus-card").some((s) => expandChord(s.chord).includes("Mod+Alt+Enter"))).toBe(false);
    }
    expect(dispatchKey(event(ctrl("Enter", { altKey: true }), input), { platform: "other" })).toBe(true);
    expect(run).toHaveBeenCalledTimes(1);
    // it rebinds like every command: the new chord fires, the default no longer does
    const bindings = bindingsOf({ "focus-card": "Mod+Alt+K" });
    expect(dispatchKey(event(ctrl("k", { altKey: true }), page), { platform: "other", bindings })).toBe(true);
    expect(dispatchKey(event(ctrl("Enter", { altKey: true }), page), { platform: "other", bindings })).toBe(false);
    expect(run).toHaveBeenCalledTimes(2);
  });
  it("uses the user's chords, not the defaults", () => {
    const run = vi.fn();
    on("jump", run);
    const bindings = bindingsOf({ jump: "Mod+Shift+O" });
    expect(dispatchKey(event(ctrl("k"), page), { platform: "other", bindings })).toBe(false);
    expect(dispatchKey(event(ctrl("o", { shiftKey: true }), page), { platform: "other", bindings })).toBe(true);
  });
  it("does not run during an IME composition or for an event someone already handled", () => {
    const run = vi.fn();
    on("jump", run);
    expect(dispatchKey(event(ctrl("k", {}, { isComposing: true }), input), { platform: "other" })).toBe(false);
    expect(dispatchKey(event(ctrl("k", {}, { keyCode: 229 }), input), { platform: "other" })).toBe(false);
    const handled = event(ctrl("k"), input);
    handled.defaultPrevented = true;
    expect(dispatchKey(handled, { platform: "other" })).toBe(false);
    expect(run).not.toHaveBeenCalled();
  });
  it("runs only the commands that work over a dialog while one is open", () => {
    const jump = vi.fn();
    const panel = vi.fn();
    on("jump", jump);
    on("right-panel", panel);
    expect(dispatchKey(event(ctrl("b"), page), { platform: "other", overlay: true })).toBe(false);
    expect(dispatchKey(event(ctrl("k"), page), { platform: "other", overlay: true })).toBe(true);
    expect(panel).not.toHaveBeenCalled();
    expect(jump).toHaveBeenCalledTimes(1);
  });
  it("runs a held key once, except the ones made for repeating", () => {
    const toggle = vi.fn();
    const next = vi.fn();
    on("right-panel", toggle);
    on("next-row", next);
    const held = (e: KeyEventLike) => event({ ...e, repeat: true }, page);
    const b = held(ctrl("b"));
    expect(dispatchKey(b, { platform: "other" })).toBe(true);
    expect(b.prevented).toBe(true);
    expect(toggle).not.toHaveBeenCalled();
    expect(dispatchKey(held(ctrl("ArrowDown", { altKey: true })), { platform: "other" })).toBe(true);
    expect(next).toHaveBeenCalledTimes(1);
  });
  it("stands aside while a chord is being recorded", () => {
    const run = vi.fn();
    on("jump", run);
    const release = holdShortcuts();
    const e = event(ctrl("k"), page);
    expect(dispatchKey(e, { platform: "other" })).toBe(false);
    expect(passesToApp(ctrl("k"), undefined, "other")).toBe(false);
    release();
    release();
    expect(dispatchKey(event(ctrl("k"), page), { platform: "other" })).toBe(true);
  });
});

describe("the terminal's passthrough decision", () => {
  it("hands registered chords to the app: Ctrl+K is not sent to the program", () => {
    on("jump", vi.fn());
    expect(passesToApp(ctrl("k"), undefined, "other")).toBe(true);
    expect(passesToApp(cmd("k"), undefined, "mac")).toBe(true);
    // ⌃K on a Mac is the program's
    expect(passesToApp(ctrl("k"), undefined, "mac")).toBe(false);
  });
  it("does the same for the keypress and keyup of the key", () => {
    on("jump", vi.fn());
    expect(passesToApp(ctrl("k", {}, { type: "keypress" }), undefined, "other")).toBe(true);
    expect(passesToApp(ctrl("k", {}, { type: "keyup" }), undefined, "other")).toBe(true);
  });
  it("keeps the program's keys: bare keys, Ctrl+C, Ctrl+R, Ctrl+Z, Alt+B", () => {
    for (const id of SHORTCUTS.map((s) => s.id)) on(id, vi.fn());
    expect(passesToApp(key("k"), undefined, "other")).toBe(false);
    expect(passesToApp(key("Enter", { shiftKey: true }), undefined, "other")).toBe(false);
    for (const k of ["c", "r", "z", "d", "a", "l", "u", "w"]) expect(passesToApp(ctrl(k), undefined, "other"), k).toBe(false);
    expect(passesToApp(key("b", { altKey: true }), undefined, "other")).toBe(false);
  });
  it("takes the user's chord, and gives the old one back to the program", () => {
    on("jump", vi.fn());
    const bindings = bindingsOf({ jump: "Mod+Shift+O" });
    expect(passesToApp(ctrl("o", { shiftKey: true }), bindings, "other")).toBe(true);
    expect(passesToApp(ctrl("k"), bindings, "other")).toBe(false);
  });
  it("does not take a chord nothing handles (no shell mounted)", () => {
    expect(passesToApp(ctrl("k"), undefined, "other")).toBe(false);
  });
  it("leaves an IME composition to xterm", () => {
    on("jump", vi.fn());
    expect(passesToApp(ctrl("k", {}, { isComposing: true }), undefined, "other")).toBe(false);
    expect(passesToApp(ctrl("k", {}, { keyCode: 229 }), undefined, "other")).toBe(false);
  });
  it("matches the command a key is for", () => {
    expect(matchShortcut(ctrl("p", { shiftKey: true }), undefined, "other")?.def.id).toBe("commands");
    expect(matchShortcut(ctrl("ArrowUp", { altKey: true, shiftKey: true }), undefined, "other")?.def.id).toBe("collapse-tree");
    expect(matchShortcut(ctrl("ArrowUp", { altKey: true }), undefined, "other")?.def.id).toBe("prev-row");
    expect(matchShortcut(ctrl("q"), undefined, "other")).toBeUndefined();
  });
});

describe("running a command by id (the palette)", () => {
  it("runs the handler, and reports a command that is not here or does not apply", () => {
    const run = vi.fn();
    on("settings", run);
    expect(runShortcut("settings")).toBe(true);
    expect(run).toHaveBeenCalledWith({ focus: "page" });
    on("help", () => false);
    expect(runShortcut("help")).toBe(false);
    expect(runShortcut("jump")).toBe(false);
  });
});

describe("the search box's hint", () => {
  it("keeps the ⌘K it has always shown while 跳转 has its default chord", () => {
    expect(jumpHint(bindingsOf({}), "other")).toBe("⌘K");
    expect(jumpHint(bindingsOf({}), "mac")).toBe("⌘K");
  });
  it("shows the chord it has been given", () => {
    expect(jumpHint(bindingsOf({ jump: "Mod+Shift+O" }), "other")).toBe("Ctrl+Shift+O");
    expect(jumpHint(bindingsOf({ jump: "Mod+Shift+O" }), "mac")).toBe("⇧⌘O");
  });
});

describe("keys a component handles itself (the terminal's find bar)", () => {
  it("are reserved on the platform that uses them, so nothing can be bound to them", () => {
    for (const platform of ["mac", "other"] as const) {
      for (const l of LOCAL_SHORTCUTS) expect(reservedWhy(l.chord[platform], platform), `${l.id} on ${platform}`).not.toBeNull();
    }
    expect(checkBinding("jump", eventChord(ctrl("f", { shiftKey: true }), "other"), bindingsOf({}), "other")).toMatchObject({ ok: false, reason: "reserved" });
    expect(checkBinding("jump", eventChord(cmd("f"), "mac"), bindingsOf({}), "mac")).toMatchObject({ ok: false, reason: "reserved" });
  });
  it("are no command's default, and a key they use never reaches the app from the terminal", () => {
    for (const platform of ["mac", "other"] as const) {
      for (const l of LOCAL_SHORTCUTS) {
        for (const s of SHORTCUTS) expect(expandChord(s.chord)).not.toContain(l.chord[platform]);
      }
    }
    for (const id of SHORTCUTS.map((s) => s.id)) on(id, vi.fn());
    expect(passesToApp(ctrl("f", { shiftKey: true }), undefined, "other")).toBe(false);
    expect(passesToApp(ctrl("f"), undefined, "other")).toBe(false);
    expect(passesToApp(cmd("f"), undefined, "mac")).toBe(false);
    const e = event(ctrl("f", { shiftKey: true }), terminal);
    expect(dispatchKey(e, { platform: "other" })).toBe(false);
    expect(e.prevented).toBe(false);
  });
});

describe("clearing, turning off and the switch for all", () => {
  afterEach(() => resetAllBindings());

  it("a cleared command has no chord, and does not fall back to its default", () => {
    clearBinding("jump");
    expect(getConfigured().jump).toBe("");
    expect(getBindings().jump).toBe("");
    expect(matchShortcut(ctrl("k"), getBindings(), "other")).toBeUndefined();
    // a stored "" survives a reload
    expect(sanitizePrefs(JSON.parse(JSON.stringify(getShortcutPrefs())), "other").overrides).toEqual({ jump: "" });
    // and its old chord is free for another command
    expect(conflictOf("help", "Mod+K", getConfigured())).toBeUndefined();
  });
  it("a cleared command's key does nothing: the event is left to the browser / the terminal", () => {
    const run = vi.fn();
    on("jump", run);
    clearBinding("jump");
    const e = event(ctrl("k"), page);
    expect(dispatchKey(e, { platform: "other" })).toBe(false);
    expect(passesToApp(ctrl("k"), undefined, "other")).toBe(false);
    expect(run).not.toHaveBeenCalled();
    expect(e.prevented).toBe(false);
  });
  it("a command turned off keeps its chord, which does nothing until it is back on", () => {
    const run = vi.fn();
    on("jump", run);
    setShortcutEnabled("jump", false);
    expect(getConfigured().jump).toBe("Mod+K");
    expect(getBindings().jump).toBe("");
    expect(dispatchKey(event(ctrl("k"), page), { platform: "other" })).toBe(false);
    // the others still work
    expect(getBindings().commands).toBe("Mod+Shift+P");
    // its chord still counts as taken: turning it back on must not clash
    expect(checkBinding("help", eventChord(ctrl("k"), "other"), getConfigured(), "other")).toMatchObject({ reason: "conflict", with: "jump" });
    setShortcutEnabled("jump", true);
    expect(dispatchKey(event(ctrl("k"), page), { platform: "other" })).toBe(true);
    expect(run).toHaveBeenCalledTimes(1);
  });
  it("the switch for all turns every shortcut off and back on, keeping each one's setting", () => {
    const run = vi.fn();
    on("jump", run);
    setShortcutEnabled("help", false);
    setBinding("commands", "Mod+Shift+O");
    setShortcutsOn(false);
    expect(Object.values(getBindings()).every((c) => c === "")).toBe(true);
    expect(dispatchKey(event(ctrl("k"), page), { platform: "other" })).toBe(false);
    expect(passesToApp(ctrl("k"), undefined, "other")).toBe(false);
    setShortcutsOn(true);
    expect(getBindings().jump).toBe("Mod+K");
    expect(getBindings().help).toBe("");
    expect(getBindings().commands).toBe("Mod+Shift+O");
    expect(dispatchKey(event(ctrl("k"), page), { platform: "other" })).toBe(true);
  });
  it("替换 takes the chord and clears the command that had it", () => {
    const check = checkBinding("jump", eventChord(ctrl("j"), "other"), getConfigured(), "other");
    expect(check).toMatchObject({ ok: false, reason: "conflict", with: "terminal-chat", chord: "Mod+J" });
    if (check.ok || !check.with || !check.chord) throw new Error("expected a conflict");
    replaceBinding("jump", check.chord, check.with);
    expect(getConfigured().jump).toBe("Mod+J");
    expect(getConfigured()["terminal-chat"]).toBe("");
    expect(matchShortcut(ctrl("j"), getBindings(), "other")?.def.id).toBe("jump");
  });
  it("恢复默认 brings back the chord and turns it on; for all, everything", () => {
    clearBinding("jump");
    setShortcutEnabled("help", false);
    expect(isDefault("jump")).toBe(false);
    expect(isDefault("help")).toBe(false);
    resetBinding("jump");
    resetBinding("help");
    expect(getBindings().jump).toBe("Mod+K");
    expect(getBindings().help).toBe("Mod+/");
    setBinding("jump", "Mod+Shift+O");
    setShortcutsOn(false);
    resetAllBindings();
    expect(getShortcutPrefs()).toEqual(EMPTY_PREFS);
    expect(getBindings()).toEqual(bindingsOf({}));
  });
  it("finds the command a default would clash with before it is restored", () => {
    // "Go to" cleared, and its ⌘K given to "Help": restoring "Go to" would take "Help"'s chord
    const b = bindingsOf({ jump: "", help: "Mod+K" });
    expect(conflictOf("jump", "Mod+K", b)?.id).toBe("help");
    expect(conflictOf("jump", "Mod+K", bindingsOf({}))).toBeUndefined();
  });
  it("reads the prefs as stored, and the first version's plain overrides", () => {
    expect(sanitizePrefs({ jump: "Mod+Shift+O" }, "other")).toEqual({ overrides: { jump: "Mod+Shift+O" }, disabled: [], off: false });
    expect(sanitizePrefs({ overrides: { jump: "" }, disabled: ["help", "nope", "help", 3], off: true }, "other")).toEqual({ overrides: { jump: "" }, disabled: ["help"], off: true });
    expect(sanitizePrefs(null)).toEqual(EMPTY_PREFS);
    expect(sanitizePrefs({ off: "yes" })).toEqual(EMPTY_PREFS);
  });
  it("leaves a command with nothing when a stored chord took its default too", () => {
    // "Go to" (first in the registry) was given "Help"'s ⌘/ and "Help"'s own override lost: "Help"'s default is taken, so it gets none
    expect(sanitizeOverrides({ jump: "Mod+/" }, "other")).toEqual({ jump: "Mod+/", help: "" });
    expect(activeBindings({ overrides: {}, disabled: ["jump"], off: false }).jump).toBe("");
  });
  it("the search box shows no hint once 跳转 has no chord", () => {
    expect(jumpHint(bindingsOf({ jump: "" }), "other")).toBe("");
  });
});

describe("opening Persona (#5581)", () => {
  afterEach(() => resetAllBindings());
  it("is a global command on Mod+Alt+P, free of the reserved chords and the other commands on both platforms", () => {
    const def = SHORTCUTS.find((s) => s.id === "persona")!;
    expect(def).toMatchObject({ chord: "Mod+Alt+P", scope: "global", label: "打开 Persona" });
    expect(def.overlay).toBeFalsy();
    for (const p of ["mac", "other"] as const) expect(reservedWhy(def.chord, p)).toBeNull();
    expect(conflictOf("persona", def.chord, bindingsOf({}))).toBeUndefined();
    expect(formatChord(def.chord, "mac")).toBe("⌥⌘P");
    expect(formatChord(def.chord, "other")).toBe("Ctrl+Alt+P");
  });
  it("⌥⌘P / Ctrl+Alt+P runs it, also when ⌥ composed the key into π", () => {
    const run = vi.fn();
    on("persona", run);
    expect(dispatchKey(event(ctrl("p", { altKey: true }, { code: "KeyP" }), page), { platform: "other" })).toBe(true);
    expect(dispatchKey(event(cmd("π", { altKey: true }, { code: "KeyP" }), page), { platform: "mac" })).toBe(true);
    expect(run).toHaveBeenCalledTimes(2);
    // not with a dialog or the palette open, and Mod+Shift+P is still the palette's
    expect(dispatchKey(event(ctrl("p", { altKey: true }, { code: "KeyP" }), page), { platform: "other", overlay: true })).toBe(false);
    expect(matchShortcut(ctrl("p", { shiftKey: true }), bindingsOf({}), "other")?.def.id).toBe("commands");
  });
  it("follows a rebinding, and a cleared or turned-off one does nothing", () => {
    const run = vi.fn();
    on("persona", run);
    expect(checkBinding("persona", eventChord(ctrl("y", { shiftKey: true }), "other"), getConfigured(), "other")).toEqual({ ok: true, chord: "Mod+Shift+Y" });
    setBinding("persona", "Mod+Shift+Y");
    expect(getBindings().persona).toBe("Mod+Shift+Y");
    expect(dispatchKey(event(ctrl("p", { altKey: true }), page), { platform: "other", bindings: getBindings() })).toBe(false);
    expect(dispatchKey(event(ctrl("y", { shiftKey: true }), page), { platform: "other", bindings: getBindings() })).toBe(true);
    expect(run).toHaveBeenCalledTimes(1);
    setShortcutEnabled("persona", false);
    expect(getBindings().persona).toBe("");
    expect(getConfigured().persona).toBe("Mod+Shift+Y");
    setShortcutEnabled("persona", true);
    clearBinding("persona");
    expect(getBindings().persona).toBe("");
    // another command can't silently take its chord
    expect(checkBinding("jump", eventChord(ctrl("p", { altKey: true }), "other"), bindingsOf({}), "other")).toMatchObject({ reason: "conflict", with: "persona" });
  });
  it("a chord the user had already chosen for another command wins over the new default (upgrade)", () => {
    // a command after it in the list, and one before it: either keeps its chord, Persona takes none
    expect(sanitizeOverrides({ "focus-input": "Mod+Alt+P" }, "other")).toEqual({ "focus-input": "Mod+Alt+P", persona: "" });
    expect(sanitizeOverrides({ jump: "Mod+Alt+P" }, "other")).toEqual({ jump: "Mod+Alt+P", persona: "" });
    const b = bindingsOf(sanitizeOverrides({ "focus-input": "Mod+Alt+P" }, "other"));
    expect(b["focus-input"]).toBe("Mod+Alt+P");
    expect(b.persona).toBe("");
    // two chosen chords still clash as before: the first keeps it, the other goes back to its default
    expect(sanitizeOverrides({ jump: "Mod+Shift+O", help: "Mod+Shift+O" }, "other")).toEqual({ jump: "Mod+Shift+O" });
  });
  it("writes the chord for aria-keyshortcuts", () => {
    expect(ariaKeyshortcuts("Mod+Alt+P", "mac")).toBe("Meta+Alt+P");
    expect(ariaKeyshortcuts("Mod+Alt+P", "other")).toBe("Control+Alt+P");
    expect(ariaKeyshortcuts("Mod+Shift+Up", "other")).toBe("Control+Shift+ArrowUp");
    expect(ariaKeyshortcuts("Mod+1-9", "other")).toBe("");
    expect(ariaKeyshortcuts("", "other")).toBe("");
  });
});
