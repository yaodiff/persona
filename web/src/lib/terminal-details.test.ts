import { describe, expect, it } from "vitest";
import {
  CURSOR_STYLES,
  hex6,
  lineEditBytes,
  parseCursorStyle,
  parseFlag,
  shouldRefocus,
  terminalMenu,
  wantsOwnMenu,
  webLink,
  type FocusFacts,
  type MacKeyEvent,
} from "./terminal-details";

const key = (over: Partial<MacKeyEvent>): MacKeyEvent => ({
  type: "keydown",
  key: "a",
  ctrlKey: false,
  metaKey: false,
  altKey: false,
  shiftKey: false,
  ...over,
});

describe("Mac line-editing keys", () => {
  it("maps ⌘⌫ to ^U, ⌘← to ^A and ⌘→ to ^E", () => {
    expect(lineEditBytes(key({ key: "Backspace", metaKey: true }))).toBe("\x15");
    expect(lineEditBytes(key({ key: "ArrowLeft", metaKey: true }))).toBe("\x01");
    expect(lineEditBytes(key({ key: "ArrowRight", metaKey: true }))).toBe("\x05");
  });

  it("leaves the keys without ⌘ to xterm", () => {
    expect(lineEditBytes(key({ key: "Backspace" }))).toBeNull();
    expect(lineEditBytes(key({ key: "ArrowLeft" }))).toBeNull();
    expect(lineEditBytes(key({ key: "ArrowRight", ctrlKey: true }))).toBeNull();
  });

  it("leaves chords with another modifier alone (⌘⇧←, ⌘⌥⌫, ⌘⌃→ are the program's or the browser's)", () => {
    expect(lineEditBytes(key({ key: "ArrowLeft", metaKey: true, shiftKey: true }))).toBeNull();
    expect(lineEditBytes(key({ key: "Backspace", metaKey: true, altKey: true }))).toBeNull();
    expect(lineEditBytes(key({ key: "ArrowRight", metaKey: true, ctrlKey: true }))).toBeNull();
  });

  it("ignores other ⌘ chords, key releases and an IME composition", () => {
    expect(lineEditBytes(key({ key: "ArrowUp", metaKey: true }))).toBeNull();
    expect(lineEditBytes(key({ key: "c", metaKey: true }))).toBeNull();
    expect(lineEditBytes(key({ type: "keyup", key: "Backspace", metaKey: true }))).toBeNull();
    expect(lineEditBytes(key({ key: "Backspace", metaKey: true, isComposing: true }))).toBeNull();
    expect(lineEditBytes(key({ key: "ArrowLeft", metaKey: true, keyCode: 229 }))).toBeNull();
  });
});

describe("which links open", () => {
  it("opens http and https pages", () => {
    expect(webLink("https://example.com/a?b=1#c")).toBe("https://example.com/a?b=1#c");
    expect(webLink("http://localhost:5173/")).toBe("http://localhost:5173/");
    expect(webLink("HTTPS://Example.com")).toBe("https://example.com/");
  });

  it("refuses every other scheme, however the program wrote it", () => {
    for (const u of ["javascript:alert(1)", "file:///etc/passwd", "data:text/html,x", "ftp://example.com/a", "ssh://host", "persona://session/a/b", "mailto:a@example.com", "vbscript:x"]) {
      expect(webLink(u), u).toBeNull();
    }
  });

  it("refuses text that is not an address", () => {
    expect(webLink("")).toBeNull();
    expect(webLink("example.com")).toBeNull();
    expect(webLink("https://")).toBeNull();
    expect(webLink(" https://example.com")).toBeNull();
    expect(webLink("//example.com/x")).toBeNull();
  });
});

describe("the right-click menu", () => {
  it("is Persona's, except with Shift held or from a touch long press", () => {
    expect(wantsOwnMenu({ shiftKey: false, pointerType: "mouse" })).toBe(true);
    expect(wantsOwnMenu({ shiftKey: false })).toBe(true);
    expect(wantsOwnMenu({ shiftKey: true, pointerType: "mouse" })).toBe(false);
    expect(wantsOwnMenu({ shiftKey: false, pointerType: "touch" })).toBe(false);
  });

  it("has the five rows in order", () => {
    expect(terminalMenu({ selection: true, driving: true }).map((m) => m.label)).toEqual(["复制", "粘贴", "全选", "查找…", "清除选区"]);
  });

  it("enables copy and clear with a selection, paste when driving, the rest always", () => {
    const off = (s: { selection: boolean; driving: boolean }) => terminalMenu(s).filter((m) => m.disabled).map((m) => m.key);
    expect(off({ selection: true, driving: true })).toEqual([]);
    expect(off({ selection: false, driving: true })).toEqual(["copy", "clearSelection"]);
    expect(off({ selection: true, driving: false })).toEqual(["paste"]);
    expect(off({ selection: false, driving: false })).toEqual(["copy", "paste", "clearSelection"]);
  });
});

describe("the focus policy", () => {
  const facts = (over: Partial<FocusFacts> = {}): FocusFacts => ({
    driving: true,
    visible: true,
    selection: false,
    overlayOpen: false,
    otherInput: false,
    outside: false,
    ...over,
  });

  it("gives the keyboard back when driving and nothing is in the way", () => {
    expect(shouldRefocus(facts())).toBe(true);
  });

  it("does nothing while watching, or for a terminal that is kept hidden", () => {
    expect(shouldRefocus(facts({ driving: false }))).toBe(false);
    expect(shouldRefocus(facts({ visible: false }))).toBe(false);
  });

  it("leaves a text selection, an open dialog / menu / popover, another input and a control outside the panel alone", () => {
    expect(shouldRefocus(facts({ selection: true }))).toBe(false);
    expect(shouldRefocus(facts({ overlayOpen: true }))).toBe(false);
    expect(shouldRefocus(facts({ otherInput: true }))).toBe(false);
    expect(shouldRefocus(facts({ outside: true }))).toBe(false);
  });
});

describe("terminal options", () => {
  it("lists the cursor styles as 方块 / 竖线 / 下划线", () => {
    expect(CURSOR_STYLES.map((c) => [c.value, c.label])).toEqual([
      ["block", "方块"],
      ["bar", "竖线"],
      ["underline", "下划线"],
    ]);
  });

  it("reads a saved style, the default (block) for anything else", () => {
    expect(parseCursorStyle("bar")).toBe("bar");
    expect(parseCursorStyle("underline")).toBe("underline");
    expect(parseCursorStyle("block")).toBe("block");
    expect(parseCursorStyle(null)).toBe("block");
    expect(parseCursorStyle("beam")).toBe("block");
  });

  it("reads a saved flag: only 1 is on", () => {
    expect(parseFlag("1")).toBe(true);
    expect(parseFlag("0")).toBe(false);
    expect(parseFlag("true")).toBe(false);
    expect(parseFlag(null)).toBe(false);
  });

  it("turns a page color into the #rrggbb xterm's search decorations take", () => {
    expect(hex6("#ef720c")).toBe("#ef720c");
    expect(hex6("#3d9aff29")).toBe("#3d9aff");
    expect(hex6(" #FA0 ")).toBe("#ffaa00");
    expect(hex6("rgb(1, 2, 3)")).toBeNull();
    expect(hex6("")).toBeNull();
  });
});
