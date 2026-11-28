// @vitest-environment jsdom
// A menu on a narrow screen closes on Back (the browser's button, a phone's back gesture) instead of leaving the page,
// like the drawer and the sheets (#5132); on a wide screen Back is left alone.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Menu } from "./atoms";

let narrow = true;
let root: Root | null = null;
let host: HTMLDivElement | null = null;

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  vi.stubGlobal("CSS", { escape: (value: string) => value });
  window.matchMedia = ((query: string) => ({
    // NARROW is a max-width query: a phone matches it, a desktop does not
    get matches() { return narrow && query.includes("max-width"); },
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
  history.replaceState(null, "", "/t/m/w/tab/s1");
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.unstubAllGlobals();
});

function open(onClose: () => void) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(<Menu anchor={null} onClose={onClose} items={[{ key: "a", label: "文件", onSelect: () => {} }]} />));
}

async function back() {
  await act(async () => {
    const popped = new Promise<void>((done, fail) => {
      const timer = setTimeout(() => fail(new Error("no Back happened")), 1000);
      addEventListener("popstate", () => (clearTimeout(timer), done()), { once: true });
    });
    history.back();
    await popped;
  });
}

describe("menu and Back", () => {
  it("on a narrow screen Back closes the menu and the page stays", async () => {
    narrow = true;
    const before = history.length;
    const onClose = vi.fn();
    open(onClose);
    expect(history.length).toBe(before + 1);
    expect(location.pathname).toBe("/t/m/w/tab/s1");
    await back();
    await act(async () => vi.waitFor(() => expect(onClose).toHaveBeenCalledTimes(1)));
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(location.pathname).toBe("/t/m/w/tab/s1");
  });

  it("on a wide screen the menu pushes nothing", () => {
    narrow = false;
    const before = history.length;
    open(vi.fn());
    expect(history.length).toBe(before);
  });

  it("closes for a window-target pointer event, but not for its own anchor or menu", () => {
    narrow = false;
    const onClose = vi.fn();
    const anchor = document.createElement("button");
    document.body.append(anchor);
    root = createRoot(anchor);
    act(() => root!.render(<Menu anchor={anchor} onClose={onClose} items={[{ key: "a", label: "文件", onSelect: () => {} }]} />));
    act(() => anchor.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true })));
    expect(onClose).not.toHaveBeenCalled();
    act(() => document.querySelector('[role="menuitem"]')!.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true })));
    expect(onClose).not.toHaveBeenCalled();
    // window is an EventTarget but not a DOM Node: never pass it to Node.contains (the Inbox history test does this).
    act(() => window.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true })));
    expect(onClose).toHaveBeenCalledTimes(1);
    anchor.remove();
  });
});
