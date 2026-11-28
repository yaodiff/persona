// @vitest-environment jsdom
// An open menu is placed again when the window or the visual viewport resizes, so it never keeps stale coordinates.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// jsdom has no matchMedia (useMedia reads it); `media.narrow` turns on the phone-width query a test needs
const media = vi.hoisted(() => {
  const state = { narrow: false };
  window.matchMedia ??= ((query: string) => ({
    get matches() {
      return state.narrow && query === "(max-width: 1023px)";
    },
    media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false,
  })) as typeof window.matchMedia;
  return state;
});
import { Menu, type MenuItem } from "./atoms";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const WIDTH = 208;
let host: HTMLDivElement;
let root: Root;
let anchor: HTMLButtonElement;
const startWidth = window.innerWidth;
const startHeight = window.innerHeight;
const jsdomOffsetHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight")!;

function setInnerWidth(w: number) {
  Object.defineProperty(window, "innerWidth", { configurable: true, writable: true, value: w });
}
function setInnerHeight(h: number) {
  Object.defineProperty(window, "innerHeight", { configurable: true, writable: true, value: h });
}

beforeEach(() => {
  setInnerWidth(1024);
  host = document.createElement("div");
  document.body.append(host);
  anchor = document.createElement("button");
  document.body.append(anchor);
  anchor.getBoundingClientRect = () => ({ left: 960, right: 1000, top: 40, bottom: 64, width: 40, height: 24, x: 960, y: 40, toJSON: () => ({}) });
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  anchor.remove();
  setInnerWidth(startWidth);
  setInnerHeight(startHeight);
  media.narrow = false;
  delete (window as { visualViewport?: unknown }).visualViewport;
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", jsdomOffsetHeight);
  vi.restoreAllMocks();
});

const panel = () => document.querySelector<HTMLElement>("[data-menu]")!;
const render = () =>
  act(() => root.render(<Menu anchor={anchor} align="right" width={WIDTH} onClose={() => {}} items={[{ key: "a", label: "Alpha", onSelect: () => {} }]} />));

describe("Menu placement on resize", () => {
  it("moves back inside a narrowed window", () => {
    render();
    expect(panel().style.left).toBe(`${1000 - WIDTH}px`);
    setInnerWidth(400);
    act(() => window.dispatchEvent(new Event("resize")));
    expect(parseFloat(panel().style.left)).toBeLessThanOrEqual(400 - WIDTH - 8);
  });

  it("stops listening for resize once closed", () => {
    const remove = vi.spyOn(window, "removeEventListener");
    render();
    act(() => root.unmount());
    root = createRoot(host); // afterEach unmounts again
    expect(remove.mock.calls.some(([type]) => type === "resize")).toBe(true);
  });
});

// A phone: a visual viewport jsdom lacks, and a menu whose height honors its inline max-height the way a browser's does.
const NATURAL = 450;
function phone() {
  media.narrow = true;
  setInnerHeight(800);
  const vv = Object.assign(new EventTarget(), { height: 800, offsetTop: 0, scale: 1 });
  Object.defineProperty(window, "visualViewport", { configurable: true, value: vv });
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) {
      if (!this.hasAttribute("data-menu")) return 0;
      const caps = [...this.style.maxHeight.matchAll(/(\d+(?:\.\d+)?)px/g)].map((m) => parseFloat(m[1]));
      return Math.min(NATURAL, ...caps);
    },
  });
  anchor.getBoundingClientRect = () => ({ left: 960, right: 1000, top: 376, bottom: 400, width: 40, height: 24, x: 960, y: 376, toJSON: () => ({}) });
  return vv;
}
const keyboard = (vv: EventTarget & { height: number }, height: number) => {
  vv.height = height;
  act(() => vv.dispatchEvent(new Event("resize")));
};

describe("Menu placement on a visual viewport resize (a phone keyboard)", () => {
  it("keeps above the keyboard when it opens", () => {
    const vv = phone();
    render();
    expect(panel().style.maxHeight).toBe("");
    keyboard(vv, 300); // only the visual viewport changes: window 'resize' does not fire
    expect(panel().style.top).toBe("8px");
    expect(panel().style.maxHeight).toContain("284px");
  });

  it("is placed for its full height again once the keyboard closes", () => {
    const vv = phone();
    vv.height = 300;
    render();
    expect(panel().style.maxHeight).toContain("284px");
    keyboard(vv, 800);
    expect(panel().style.maxHeight).toBe("");
    expect(parseFloat(panel().style.top) + NATURAL).toBeLessThanOrEqual(800 - 8);
  });
});

// #5584: rows that wrap are taller in a narrower panel. Without a width yet the panel is as wide as its text, so a menu
// measured that way above its anchor came out too short and its bottom covered the chip that opened it.
describe("Menu placement with wrapping rows", () => {
  it("measures its height at the width it will have, so a menu above its anchor ends above it", () => {
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
      configurable: true,
      get(this: HTMLElement) {
        if (!this.hasAttribute("data-menu")) return 0;
        return this.style.width === `${WIDTH}px` ? 240 : 120; // wrapped at its width, one line each without one
      },
    });
    anchor.getBoundingClientRect = () => ({ left: 400, right: 440, top: 600, bottom: 624, width: 40, height: 24, x: 400, y: 600, toJSON: () => ({}) });
    act(() => root.render(<Menu anchor={anchor} side="top" width={WIDTH} onClose={() => {}} items={[{ key: "a", label: "Alpha", detail: "A sentence that wraps", onSelect: () => {} }]} />));
    expect(panel().style.top).toBe(`${600 - 240 - 6}px`);
  });
});

it("updates an open submenu when its parent moves and its available actions change", () => {
  const open = vi.fn();
  const paint = (ready: boolean) => act(() => root.render(<Menu anchor={anchor} onClose={() => {}} items={[
    ...(ready ? [{ key: "seen", label: "Mark as seen", onSelect: () => {} }] : []),
    { key: "more", label: "More", onSelect: () => {}, children: [
      { key: "review", label: "Review", disabled: !ready, onSelect: open },
    ] },
  ]} />));
  paint(false);
  act(() => document.querySelector<HTMLButtonElement>('[aria-haspopup="menu"]')!.click());
  const review = () => document.querySelector<HTMLButtonElement>('[data-menu]:last-of-type [role="menuitem"]')!;
  expect(review().disabled).toBe(true);
  paint(true);
  expect(review().disabled).toBe(false);
  act(() => review().click());
  expect(open).toHaveBeenCalledOnce();
});

it("keeps keyboard selection on the same command across a submenu update", () => {
  const run = vi.fn();
  const paint = (keys: string[]) => act(() => root.render(<Menu anchor={anchor} onClose={() => {}} items={[
    { key: "more", label: "More", onSelect: () => {}, children: keys.map((key) => ({ key, label: key, onSelect: () => run(key) })) },
  ]} />));
  const press = (key: string) => act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key })));
  paint(["mute", "delete"]);
  press("Home");
  press("ArrowRight");
  paint(["delete"]);
  press("Enter");
  expect(run).not.toHaveBeenCalled();
});

it("ignores a lazy submenu's result after the keyboard has left it", async () => {
  let loaded!: (items: MenuItem[]) => void;
  const done = vi.fn();
  const list = new Promise<MenuItem[]>((resolve) => { loaded = resolve; });
  act(() => root.render(<Menu anchor={anchor} onClose={() => {}} items={[
    { key: "launch", label: "Launch", onSelect: () => {}, children: () => list },
    { key: "done", label: "Done", onSelect: done },
  ]} />));
  const press = (key: string) => act(() => { window.dispatchEvent(new KeyboardEvent("keydown", { key })); });
  press("Home");
  press("ArrowRight");
  press("ArrowLeft");
  press("ArrowDown");
  await act(async () => loaded([]));
  press("Enter");
  expect(done).toHaveBeenCalledOnce();
});
