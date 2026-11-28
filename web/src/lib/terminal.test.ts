import { readFileSync } from "node:fs";
import { describe, expect, it, vi } from "vitest";
import {
  AWAIT_FULL_MS,
  DEFAULT_SHORTCUTS,
  FrameOrder,
  NO_MODIFIERS,
  type Modifiers,
  WheelLines,
  backoff,
  cellAt,
  decodeBase64,
  decodeShortcuts,
  encodeKey,
  fontLoadQuery,
  historyAnchor,
  historyEnd,
  POLL_FAST_FOR_MS,
  POLL_MIN_MS,
  POLL_MS,
  pollDelay,
  keyDecision,
  keyNameForBytes,
  isStale,
  lineCount,
  linkDetail,
  linkHeadline,
  modifierHeld,
  modifierReleased,
  modifierTapped,
  modifiersActive,
  modifiersPressed,
  moveShortcut,
  parseTerminalPush,
  pasteBytes,
  settleWithin,
  terminalPalette,
  typedWithModifiers,
  usableGrid,
  withFamily,
  withModifiers,
} from "./terminal";

const f = (epoch: string, seq: number, full = false) => ({ epoch, seq, full });

describe("frame order", () => {
  it("applies a whole frame, then diffs that follow it", () => {
    const o = new FrameOrder();
    expect(o.accept(f("e1", 1, true), 0)).toBe("apply");
    expect(o.accept(f("e1", 2), 0)).toBe("apply");
    expect(o.accept(f("e1", 3), 0)).toBe("apply");
    expect([o.epoch, o.seq]).toEqual(["e1", 3]);
  });

  it("drops a replay that overlaps what arrived live", () => {
    const o = new FrameOrder();
    o.accept(f("e1", 1, true), 0);
    o.accept(f("e1", 2), 0);
    expect(o.accept(f("e1", 2), 0)).toBe("duplicate");
    expect(o.accept(f("e1", 1), 0)).toBe("duplicate");
    expect(o.accept(f("e1", 3), 0)).toBe("apply");
  });

  it("calls a missing diff a gap, once, and waits for the whole frame of the new subscription", () => {
    const o = new FrameOrder();
    o.accept(f("e1", 1, true), 0);
    expect(o.accept(f("e1", 3), 10)).toBe("gap");
    expect(o.epoch).toBeNull();
    expect(o.awaitingFull).toBe(true);
    // what was already on its way is not another gap
    expect(o.accept(f("e1", 4), 20)).toBe("stale");
    expect(o.accept(f("e1", 5), 30)).toBe("stale");
    // the replay starts over from the whole frame
    expect(o.accept(f("e1", 1, true), 40)).toBe("apply");
    expect(o.awaitingFull).toBe(false);
    expect(o.accept(f("e1", 2), 50)).toBe("apply");
  });

  it("asks again when the whole frame never comes", () => {
    const o = new FrameOrder();
    o.accept(f("e1", 1, true), 0);
    expect(o.accept(f("e1", 5), 0)).toBe("gap");
    expect(o.accept(f("e1", 6), AWAIT_FULL_MS - 1)).toBe("stale");
    expect(o.accept(f("e1", 7), AWAIT_FULL_MS + 1)).toBe("gap");
  });

  it("starts over on a new epoch only from its whole frame", () => {
    const o = new FrameOrder();
    o.accept(f("e1", 1, true), 0);
    expect(o.accept(f("e2", 1), 0)).toBe("gap");
    expect(o.accept(f("e2", 1, true), 0)).toBe("apply");
    expect(o.epoch).toBe("e2");
  });

  it("ignores the tail of a stream it moved on from (driving ↔ watching)", () => {
    const o = new FrameOrder();
    o.accept(f("watch", 1, true), 0);
    o.accept(f("watch", 2), 0);
    o.restart(100);
    expect(o.accept(f("ctl", 1, true), 110)).toBe("apply");
    // the old stream's frames still in flight: not a gap, long after too
    expect(o.accept(f("watch", 3), 120)).toBe("stale");
    expect(o.accept(f("watch", 4), 120 + AWAIT_FULL_MS * 2)).toBe("stale");
    expect(o.accept(f("ctl", 2), 130)).toBe("apply");
    // the same epoch coming back with a whole frame is live again
    expect(o.accept(f("watch", 1, true), 140)).toBe("apply");
    expect(o.accept(f("watch", 2), 150)).toBe("apply");
  });

  it("forgets where it was when the stream ends", () => {
    const o = new FrameOrder();
    o.accept(f("e1", 1, true), 0);
    o.reset();
    expect([o.epoch, o.seq]).toEqual([null, 0]);
    expect(o.accept(f("e1", 2), 0)).toBe("stale");
    expect(o.accept(f("e2", 1, true), 0)).toBe("apply");
  });
});

describe("terminal pushes", () => {
  it("parse a frame", () => {
    expect(parseTerminalPush({ type: "terminal", epoch: "e", seq: 3, full: true, cols: 96, rows: 24, data: "QQ==" })).toEqual({
      kind: "frame",
      frame: { epoch: "e", seq: 3, full: true, cols: 96, rows: 24, data: "QQ==" },
    });
    expect(parseTerminalPush({ epoch: "e", seq: 4, data: "" })).toEqual({ kind: "frame", frame: { epoch: "e", seq: 4, full: false, cols: 0, rows: 0, data: "" } });
  });

  it("parse the end of a stream, unsupported or not", () => {
    expect(parseTerminalPush({ error: "not running", machine_id: "m" })).toEqual({ kind: "ended", error: "not running", unsupported: false });
    expect(parseTerminalPush({ error: "unsupported: old node", unsupported: true })).toEqual({ kind: "ended", error: "unsupported: old node", unsupported: true });
  });

  it("ignore anything else", () => {
    expect(parseTerminalPush({ epoch: "e" })).toBeNull();
    expect(parseTerminalPush({ seq: "1", epoch: "e", data: "" })).toBeNull();
  });

  it("decode base64 to the VT bytes (UTF-8 left to xterm)", () => {
    const vt = "\x1b[2J\x1b[1;1H你好";
    const b64 = btoa(String.fromCharCode(...new TextEncoder().encode(vt)));
    expect(new TextDecoder().decode(decodeBase64(b64)!)).toBe(vt);
    expect(decodeBase64("")).toEqual(new Uint8Array());
    expect(decodeBase64("not base64!")).toBeNull();
  });
});

describe("link status", () => {
  it("backs off 1, 2, 4, 8, then every 15 seconds", () => {
    expect([1, 2, 3, 4, 5, 6, 20].map(backoff)).toEqual([1, 2, 4, 8, 15, 15, 15]);
    expect(backoff(0)).toBe(1);
  });

  it("is stale on a failure or after 4 seconds without a fresh screen", () => {
    expect(isStale(1000, null, 4000)).toBe(false);
    expect(isStale(1000, null, 5001)).toBe(true);
    expect(isStale(1000, "连不上 hub", 1000)).toBe(true);
    // nothing yet: stale once the wait since subscribing runs long
    expect(isStale(null, null, 9000)).toBe(false);
    expect(isStale(null, null, 9000, 6000)).toBe(false);
    expect(isStale(null, null, 9000, 1000)).toBe(true);
  });

  it("says how old the screen is", () => {
    expect(linkHeadline(null, 0)).toBe("暂未连接到终端");
    expect(linkHeadline(1000, 13_400)).toBe("12 秒前更新");
    expect(linkHeadline(0, 125_000)).toBe("2 分钟前更新");
    expect(linkHeadline(new Date(2026, 10, 2, 9, 5).getTime(), new Date(2026, 10, 2, 11, 0).getTime())).toBe("更新于 09:05");
  });

  it("says when it tries again and why", () => {
    expect(linkDetail(null, 0, 0)).toBe("正在等待电脑响应…");
    expect(linkDetail("连不上 hub", 4000, 200)).toBe("4 秒后重试 · 连不上 hub");
    expect(linkDetail("连不上 hub", 4000, 4000)).toBe("正在重试… · 连不上 hub");
  });
});

describe("input", () => {
  it("pastes one line as typed and several as one bracketed paste", () => {
    expect(pasteBytes("ls -la")).toBe("ls -la");
    expect(pasteBytes("a\nb\r\nc")).toBe("\x1b[200~a\rb\rc\x1b[201~");
    expect(lineCount("a\nb\r\nc\n")).toBe(3);
    expect(lineCount("one")).toBe(1);
  });

  it("finds the cell under a point, inside the grid", () => {
    expect(cellAt(0, 0, 800, 480, 100, 30)).toEqual([0, 0]);
    expect(cellAt(15.9, 31.9, 800, 480, 100, 30)).toEqual([1, 1]);
    expect(cellAt(799, 479, 800, 480, 100, 30)).toEqual([99, 29]);
    expect(cellAt(900, -5, 800, 480, 100, 30)).toEqual([99, 0]);
  });

  it("turns wheel deltas into whole lines, back into history going up", () => {
    const w = new WheelLines();
    // pixels (trackpad): fractions carry over
    expect(w.add(-10, 0, 16, 30)).toBe(0);
    expect(w.add(-10, 0, 16, 30)).toBe(1);
    expect(w.add(-28, 0, 16, 30)).toBe(2);
    // the direction turns: the leftover goes
    expect(w.add(8, 0, 16, 30)).toBe(0);
    expect(w.add(8, 0, 16, 30)).toBe(-1);
    // lines and pages
    w.reset();
    expect(w.add(3, 1, 16, 30)).toBe(-3);
    expect(w.add(-1, 2, 16, 30)).toBe(30);
    expect(w.add(0, 0, 16, 30)).toBe(0);
  });

  it("drives only a usable grid", () => {
    expect(usableGrid({ cols: 102, rows: 30 })).toBe(true);
    expect(usableGrid({ cols: 9, rows: 30 })).toBe(false);
    expect(usableGrid({ cols: 40, rows: 3 })).toBe(false);
    expect(usableGrid(null)).toBe(false);
  });
});

describe("palette", () => {
  const light: Record<string, string> = {
    "--page": "#fafafb", "--ink": "#1f2124", "--ink-2": "#62656b", "--ink-3": "#9a9da3", "--accent": "#0285ff", "--accent-ink": "#0170dd",
    "--accent-tint": "#e9f3ff", "--hover-2": "#e7e9eb", "--line-strong": "#e0e2e5", "--red": "#e3474c", "--green": "#189a4d", "--orange": "#ef720c",
  };

  it("takes every color from the page's tokens", () => {
    const p = terminalPalette((n) => ` ${light[n] ?? ""}`, false);
    expect(p.background).toBe("#fafafb");
    expect(p.foreground).toBe("#1f2124");
    expect(p.black).toBe("#1f2124");
    expect(p.blue).toBe("#0285ff");
    expect(p.yellow).toBe("#ef720c");
    // no terminal tokens yet: the nearest role colors
    expect(p.magenta).toBe("#e3474c");
    expect(p.cyan).toBe("#0170dd");
    expect(Object.values(p).every(Boolean)).toBe(true);
  });

  it("uses the terminal tokens once the stylesheet has them, and a visible black on dark", () => {
    const p = terminalPalette((n) => ({ ...light, "--term-magenta": "m", "--term-cyan": "c" })[n] ?? "", true);
    expect([p.magenta, p.cyan, p.brightMagenta, p.brightCyan]).toEqual(["m", "c", "m", "c"]);
    expect(p.black).toBe("#e0e2e5");
  });
});

describe("keys", () => {
  it("encodes keys by herdr's names, as iOS", () => {
    expect(encodeKey("esc")).toBe("\x1b");
    expect(encodeKey("enter")).toBe("\r");
    expect(encodeKey("shift+enter")).toBe("\x1b\r");
    expect(encodeKey("tab")).toBe("\t");
    expect(encodeKey("shift+tab")).toBe("\x1b[Z");
    expect(encodeKey("ctrl+c")).toBe("\x03");
    expect(encodeKey("ctrl+alt+x")).toBe("\x1b\x18");
    expect(encodeKey("up")).toBe("\x1b[A");
    expect(encodeKey("ctrl+left")).toBe("\x1b[1;5D");
    expect(encodeKey("shift+pageup")).toBe("\x1b[5;2~");
    expect(encodeKey("f1")).toBe("\x1bOP");
    expect(encodeKey("alt+backspace")).toBe("\x1b\x7f");
    expect(encodeKey("ctrl+space")).toBe("\x00");
    expect(encodeKey("ctrl++")).toBe("+");
    expect(encodeKey("shift+a")).toBe("A");
    expect(encodeKey("ctrl+[")).toBe("\x1b");
    expect(encodeKey("nope")).toBeNull();
  });

  it("sends a bare arrow as SS3 when the program asked for application cursor keys", () => {
    expect(encodeKey("down", true)).toBe("\x1bOB");
    expect(encodeKey("ctrl+down", true)).toBe("\x1b[1;5B");
  });
});

describe("modifiers", () => {
  it("arms on a tap, locks on a quick second tap, lets go on the next", () => {
    let m = modifierTapped(NO_MODIFIERS, "ctrl", false);
    expect(m.ctrl).toBe("once");
    expect(modifierTapped(m, "ctrl", false).ctrl).toBe("off");
    m = modifierTapped(m, "ctrl", true);
    expect(m.ctrl).toBe("locked");
    expect(modifierTapped(m, "ctrl", false).ctrl).toBe("off");
  });

  it("applies to the next key, then the armed ones let go and the locked stay", () => {
    const m: Modifiers = { ctrl: "once", alt: "locked", shift: "off" };
    expect(withModifiers(m, "x")).toBe("ctrl+alt+x");
    expect(withModifiers(m, "esc")).toBe("esc");
    const after = modifiersPressed(m);
    expect([after.ctrl, after.alt]).toEqual(["off", "locked"]);
    expect(modifiersActive(after)).toBe(true);
    expect(modifiersActive(NO_MODIFIERS)).toBe(false);
  });

  it("held: released to off when a key went out meanwhile, else armed", () => {
    let m = modifierHeld(NO_MODIFIERS, "shift");
    expect(modifierReleased(m, "shift").shift).toBe("once");
    m = modifiersPressed(m);
    expect(m.shift).toBe("held");
    expect(modifierReleased(m, "shift").shift).toBe("off");
  });

  it("turns a typed character into a key with ctrl / alt, or capitalizes it with shift alone", () => {
    expect(typedWithModifiers({ ...NO_MODIFIERS, ctrl: "once" }, "C")).toEqual({ key: "ctrl+c" });
    expect(typedWithModifiers({ ...NO_MODIFIERS, alt: "once" }, " ")).toEqual({ key: "alt+space" });
    expect(typedWithModifiers({ ...NO_MODIFIERS, shift: "once" }, "a")).toEqual({ text: "A" });
    expect(typedWithModifiers({ ...NO_MODIFIERS, ctrl: "once" }, "中")).toEqual({ text: "中" });
  });
});

describe("saved commands", () => {
  it("starts from the defaults, keeps an emptied list empty and ignores junk", () => {
    expect(decodeShortcuts(null)).toEqual(DEFAULT_SHORTCUTS);
    expect(decodeShortcuts("[]")).toEqual([]);
    expect(decodeShortcuts('["make"]')).toEqual(["make"]);
    expect(decodeShortcuts("{")).toEqual(DEFAULT_SHORTCUTS);
    expect(decodeShortcuts("[1]")).toEqual(DEFAULT_SHORTCUTS);
  });

  it("moves a command up or down within the list", () => {
    expect(moveShortcut(["a", "b", "c"], 1, -1)).toEqual(["b", "a", "c"]);
    expect(moveShortcut(["a", "b", "c"], 1, 1)).toEqual(["a", "c", "b"]);
    expect(moveShortcut(["a", "b"], 0, -1)).toEqual(["a", "b"]);
  });
});

describe("history anchor", () => {
  it("is the first 20 lines shown, trailing empty lines dropped, with how many there are", () => {
    const lines = Array.from({ length: 30 }, (_, i) => `line ${i}  `).concat(["", "  "]);
    const a = historyAnchor(lines);
    expect(a.loaded).toBe(30);
    expect(a.anchor).toHaveLength(20);
    expect(a.anchor[0]).toBe("line 0");
    expect(historyAnchor(["", ""])).toEqual({ anchor: [], loaded: 0 });
  });

  it("says where scrolling up stopped: more, the start, older output not kept, herdr's cap", () => {
    expect(historyEnd({ has_more: true, at_limit: false })).toBe("more");
    expect(historyEnd({ has_more: false, at_limit: false })).toBe("start");
    expect(historyEnd({ has_more: false, at_limit: true, dropped: true })).toBe("dropped");
    expect(historyEnd({ has_more: false, at_limit: true })).toBe("limit"); // a node without `dropped`
    expect(historyEnd({})).toBe("start");
  });
});

describe("poll pace", () => {
  it("reads at the node's pace while the screen changes, once a second when it is idle or no pace is given", () => {
    const now = 100_000;
    expect(pollDelay(undefined, now, now)).toBe(POLL_MS);
    expect(pollDelay(250, now - 100, now)).toBe(250);
    expect(pollDelay(250, now - POLL_FAST_FOR_MS - 1, now)).toBe(POLL_MS);
    expect(pollDelay(50, now, now)).toBe(POLL_MIN_MS);
    expect(pollDelay(5000, now, now)).toBe(POLL_MS);
    expect(pollDelay(Number.NaN, now, now)).toBe(POLL_MS);
  });
});

describe("key names from xterm's bytes", () => {
  it("names the keys modifiers can apply to", () => {
    expect(keyNameForBytes("\r")).toBe("enter");
    expect(keyNameForBytes("\x1bOA")).toBe("up");
    expect(keyNameForBytes("\x1b[3~")).toBe("delete");
    expect(keyNameForBytes("a")).toBeNull();
  });
});

describe("font stack", () => {
  it("puts a range-limited fallback before named families, once", () => {
    expect(withFamily('"JetBrains Mono", Menlo, monospace', "X")).toBe('"X", "JetBrains Mono", Menlo, monospace');
    expect(withFamily("Menlo", "X")).toBe('"X", Menlo, monospace');
    expect(withFamily('Menlo, "X", monospace', "X")).toBe('"X", Menlo, monospace');
  });

  it("prefers Linux monospace faces before UI and substituted desktop fallbacks", () => {
    const css = readFileSync(new URL("../styles.css", import.meta.url), "utf8");
    const stack = /--font-mono:\s*([^;]+);/.exec(css)?.[1] ?? "";
    const families = stack.split(",").map((family) => family.trim().replaceAll('"', ""));
    expect(families.at(-1)).toBe("monospace");
    for (const family of ["DejaVu Sans Mono", "Liberation Mono"]) {
      expect(families.indexOf(family)).toBeGreaterThanOrEqual(0);
      expect(families.indexOf(family)).toBeLessThan(families.indexOf("ui-monospace"));
      expect(families.indexOf(family)).toBeLessThan(families.indexOf("Menlo"));
    }
  });
});

const key = (over: Partial<Parameters<typeof keyDecision>[0]>) => ({
  type: "keydown",
  key: "a",
  ctrlKey: false,
  metaKey: false,
  altKey: false,
  shiftKey: false,
  ...over,
});

describe("key decisions", () => {
  it("copies on ⌃C / ⌘C / ⌃⇧C only with text selected", () => {
    expect(keyDecision(key({ key: "c", ctrlKey: true }), true)).toBe("copy");
    expect(keyDecision(key({ key: "c", metaKey: true }), true)).toBe("copy");
    expect(keyDecision(key({ key: "C", ctrlKey: true, shiftKey: true }), true)).toBe("copy");
    expect(keyDecision(key({ key: "c", ctrlKey: true }), false)).toBe("xterm"); // ^C still interrupts
    expect(keyDecision(key({ key: "c" }), true)).toBe("xterm"); // a plain c is typed
    expect(keyDecision(key({ key: "c", ctrlKey: true, altKey: true }), true)).toBe("xterm");
    expect(keyDecision(key({ key: "v", ctrlKey: true }), true)).toBe("xterm");
  });

  it("leaves the keypress of a copy to nobody", () => {
    expect(keyDecision(key({ type: "keypress", key: "c", ctrlKey: true }), true)).toBe("swallow");
    expect(keyDecision(key({ type: "keyup", key: "c", metaKey: true }), true)).toBe("swallow");
  });

  it("makes shift+enter a new line, on keydown only", () => {
    expect(keyDecision(key({ key: "Enter", shiftKey: true }), false)).toBe("newline");
    expect(keyDecision(key({ type: "keypress", key: "Enter", shiftKey: true }), false)).toBe("swallow");
    expect(keyDecision(key({ key: "Enter" }), false)).toBe("xterm");
    expect(keyDecision(key({ key: "Enter", shiftKey: true, ctrlKey: true }), false)).toBe("xterm");
  });

  it("leaves everything to xterm while an IME composes", () => {
    expect(keyDecision(key({ key: "Enter", shiftKey: true, isComposing: true }), false)).toBe("xterm");
    expect(keyDecision(key({ key: "c", ctrlKey: true, isComposing: true }), true)).toBe("xterm");
    expect(keyDecision(key({ key: "Process", keyCode: 229 }), false)).toBe("xterm");
    expect(keyDecision(key({ key: "Enter", shiftKey: true, keyCode: 229 }), false)).toBe("xterm");
  });
});

describe("waiting for the font", () => {
  it("asks for the size, the stack and a Latin and a Chinese sample", () => {
    expect(fontLoadQuery(13, '"JetBrains Mono", monospace')).toEqual({ font: '13px "JetBrains Mono", monospace', sample: "W中" });
  });

  it("settles when the font loads, fails, or takes too long", async () => {
    vi.useFakeTimers();
    try {
      let done = 0;
      const never = new Promise<void>(() => {});
      void settleWithin(never, 1500).then(() => done++);
      await vi.advanceTimersByTimeAsync(1499);
      expect(done).toBe(0);
      await vi.advanceTimersByTimeAsync(1);
      expect(done).toBe(1);
      await settleWithin(Promise.resolve(), 1500);
      await settleWithin(Promise.reject(new Error("font")), 1500);
    } finally {
      vi.useRealTimers();
    }
  });
});
