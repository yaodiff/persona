// @vitest-environment jsdom
// The palette for a screen reader and a keyboard: the field is named for what it does (a combobox over a listbox of
// options), the highlighted row is announced as the arrows move it, Tab stays inside, and closing gives the focus back.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { closePalette, getPalette, openPalette, setPaletteQuery, usePalette } from "../lib/palette";
import { closeHelp, toggleHelp, useHelpOpen } from "../lib/shortcut-help";
import { helpKey, paletteKey } from "../lib/use-shortcuts";
import { registerShortcut } from "../lib/shortcuts";
import { Palette } from "./Palette";
import { ShortcutHelp } from "./ShortcutHelp";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
// the modules read the colour scheme when they load
vi.hoisted(() => {
  (globalThis as Record<string, unknown>).matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
});

let host: HTMLDivElement;
let root: Root;
let opener: HTMLButtonElement;

beforeEach(() => {
  // focusables() keeps only shown controls; jsdom lays nothing out, so every element would count as hidden
  vi.spyOn(Element.prototype, "getClientRects").mockReturnValue([{}] as unknown as DOMRectList);
  Element.prototype.scrollIntoView ??= () => {};
  opener = document.createElement("button");
  opener.textContent = "open";
  host = document.createElement("div");
  document.body.append(opener, host);
  root = createRoot(host);
  opener.focus();
});

afterEach(() => {
  act(() => root.unmount());
  closePalette();
  closeHelp();
  host.remove();
  opener.remove();
  vi.restoreAllMocks();
});

function show(open: boolean) {
  act(() => root.render(<Palette open={open} onClose={() => {}} onNewAgent={() => {}} />));
}

/** Opens it in command mode: the app's own commands (New agent, New chat) are always listed. */
function openCommands() {
  act(() => openPalette("command"));
  show(true);
}

const dialog = () => document.querySelector<HTMLElement>("[role=dialog]")!;
const field = () => dialog().querySelector<HTMLInputElement>("input")!;
const selected = () => [...dialog().querySelectorAll<HTMLElement>("[role=option][aria-selected=true]")];

function key(el: Element, k: string, shift = false): boolean {
  const e = new KeyboardEvent("keydown", { key: k, shiftKey: shift, bubbles: true, cancelable: true });
  act(() => void el.dispatchEvent(e));
  return e.defaultPrevented;
}

describe("the palette for assistive technology", () => {
  it("names its field for what it does and makes it a combobox over a listbox", () => {
    openCommands();
    const input = field();
    expect(input.getAttribute("role")).toBe("combobox");
    expect(input.getAttribute("aria-label")).toBe("命令面板");
    expect(input.getAttribute("aria-label")).not.toBe("Esc");
    expect(input.getAttribute("aria-autocomplete")).toBe("list");
    expect(input.getAttribute("aria-expanded")).toBe("true");
    const list = document.getElementById(input.getAttribute("aria-controls")!);
    expect(list?.getAttribute("role")).toBe("listbox");
    expect(list!.querySelectorAll("[role=option]").length).toBeGreaterThan(1);
    expect(dialog().getAttribute("aria-modal")).toBe("true");
  });

  it("names the search field after the search outside command mode", () => {
    act(() => openPalette("jump"));
    show(true);
    expect(field().getAttribute("aria-label")).toBe("搜索会话");
  });

  it("points aria-activedescendant at the highlighted option as the arrows move it", () => {
    openCommands();
    const input = field();
    const first = selected();
    expect(first).toHaveLength(1);
    expect(input.getAttribute("aria-activedescendant")).toBe(first[0].id);
    key(input, "ArrowDown");
    const next = selected();
    expect(next).toHaveLength(1);
    expect(next[0]).not.toBe(first[0]);
    expect(input.getAttribute("aria-activedescendant")).toBe(next[0].id);
    expect(first[0].getAttribute("aria-selected")).toBe("false");
  });

  it("keeps Tab inside the dialog", () => {
    openCommands();
    expect(document.activeElement).toBe(field());
    const options = [...dialog().querySelectorAll<HTMLElement>("button")];
    const last = options[options.length - 1];
    last.focus();
    expect(key(last, "Tab")).toBe(true);
    expect(document.activeElement).toBe(field());
    expect(key(field(), "Tab", true)).toBe(true);
    expect(document.activeElement).toBe(last);
  });

  it("gives the focus back to what opened it on close", () => {
    openCommands();
    expect(document.activeElement).toBe(field());
    show(false);
    expect(document.activeElement).toBe(opener);
  });

  it("gives the focus back to what opened it after a command opened a dialog in its place", () => {
    // the shell's palette and shortcut help: picking "Keyboard shortcuts" closes the one and opens the other in one render
    const unregister = registerShortcut("help", () => toggleHelp());
    function Shell() {
      const palette = usePalette();
      return (
        <>
          <Palette open={palette.open} onClose={closePalette} onNewAgent={() => {}} />
          <ShortcutHelp />
        </>
      );
    }
    try {
      act(() => openPalette("command"));
      act(() => root.render(<Shell />));
      act(() => setPaletteQuery(">app:help"));
      key(field(), "Enter");
      // the help Dialog is named by its title; the palette's card by aria-label
      const help = document.querySelector<HTMLElement>("[role=dialog][aria-labelledby]")!;
      expect(help.contains(document.activeElement)).toBe(true);
      key(document.activeElement!, "Escape");
      expect(document.activeElement).toBe(opener);
    } finally {
      unregister();
    }
  });
});

// Esc closed the palette only from its field: Tab to a result or the Filter, and Esc (the key its corner names) did
// nothing, so ⌘/ then opened the shortcut help over it.
describe("Esc anywhere in the palette", () => {
  /** The shell's palette: its own store, closed by Esc as App.tsx closes it. */
  function Shell() {
    const palette = usePalette();
    return (
      <>
        <Palette open={palette.open} onClose={closePalette} onNewAgent={() => {}} />
        <ShortcutHelp />
        <span data-help-open={String(useHelpOpen())} />
      </>
    );
  }
  const open = (mode: "jump" | "command") => {
    act(() => openPalette(mode));
    act(() => root.render(<Shell />));
  };

  it("closes it from a result row Tab reached, and gives the focus back to what opened it", () => {
    open("command");
    const row = dialog().querySelector<HTMLElement>("[role=option]")!;
    row.focus();
    expect(key(row, "Escape")).toBe(false);
    expect(getPalette().open).toBe(false);
    expect(document.activeElement).toBe(opener);
  });

  it("closes it from the Filter beside the field", () => {
    open("jump");
    const filter = dialog().querySelector<HTMLElement>("[data-testid=palette-agent-filter]")!;
    filter.focus();
    key(filter, "Escape");
    expect(getPalette().open).toBe(false);
    expect(document.activeElement).toBe(opener);
  });

  it("leaves it open for an Esc that ends an input method's word in the field", () => {
    open("jump");
    act(() => void field().dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", isComposing: true, bubbles: true, cancelable: true })));
    expect(getPalette().open).toBe(true);
    key(field(), "Escape");
    expect(getPalette().open).toBe(false);
  });

  it("⌘/ puts the shortcut help in its place, never over it; ⌘K puts it back", () => {
    open("jump");
    act(() => helpKey());
    expect(getPalette().open).toBe(false);
    expect(document.querySelector("[data-help-open]")!.getAttribute("data-help-open")).toBe("true");
    expect(document.querySelectorAll('[role=dialog][aria-modal="true"]')).toHaveLength(1);
    const help = document.querySelector<HTMLElement>("[role=dialog][aria-labelledby]")!;
    expect(help.contains(document.activeElement)).toBe(true);
    act(() => paletteKey("jump"));
    expect(document.querySelector("[data-help-open]")!.getAttribute("data-help-open")).toBe("false");
    expect(getPalette().open).toBe(true);
    expect(document.activeElement).toBe(field());
    // and its Esc gives the focus back to where the first of them was opened from
    key(field(), "Escape");
    expect(document.activeElement).toBe(opener);
  });
});
