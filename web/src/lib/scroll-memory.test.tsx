// @vitest-environment jsdom
// Back to Overview and to Settings's first page lands where the reader had scrolled (#5133); a fresh visit starts at the top.
// Two shapes of page: one that unmounts when a row opens another page (Overview → a conversation), and one scroller whose
// content swaps under it (Settings's list → a category).
import { act, useRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { navigate, useRoute } from "./router";
import { forgetScrolls, scrollTarget, useScrollMemory } from "./scroll-memory";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function Overview() {
  const box = useRef<HTMLDivElement>(null);
  useScrollMemory(box, "/overview");
  return <div ref={box} data-testid="scroller" />;
}

function Shell() {
  const route = useRoute();
  return route.name === "home" ? <Overview /> : <p>conversation</p>;
}

// Settings: one scroller; the list and each category are keys of it
function Swapping({ page }: { page: string }) {
  const box = useRef<HTMLDivElement>(null);
  useScrollMemory(box, page);
  return <div ref={box} data-testid="scroller">{page}</div>;
}

let root: Root;
let host: HTMLDivElement;
const scroller = () => host.querySelector<HTMLElement>("[data-testid=scroller]")!;
const scrollTo = (top: number) => {
  scroller().scrollTop = top;
  scroller().dispatchEvent(new Event("scroll"));
};
const back = () =>
  act(async () => {
    const popped = new Promise((r) => addEventListener("popstate", r, { once: true }));
    history.back();
    await popped;
  });

beforeEach(() => {
  forgetScrolls();
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("scrollTarget", () => {
  it("is the remembered spot only on a return", () => {
    expect(scrollTarget("/nowhere", true)).toBe(0);
    expect(scrollTarget("/nowhere", false)).toBe(0);
  });
});

describe("useScrollMemory", () => {
  it("puts 总览 back where it was after Back from a conversation it opened", async () => {
    navigate("/overview");
    act(() => root.render(<Shell />));
    scrollTo(900);
    act(() => navigate("/s/mac/s1"));
    expect(host.textContent).toBe("conversation");
    await back();
    expect(location.pathname).toBe("/overview");
    expect(scroller().scrollTop).toBe(900);
  });

  it("opens 总览 at the top when it is reached by a link, not by Back", async () => {
    navigate("/overview");
    act(() => root.render(<Shell />));
    scrollTo(900);
    act(() => navigate("/s/mac/s1"));
    act(() => navigate("/overview"));
    expect(scroller().scrollTop).toBe(0);
  });

  it("leaves a fresh scroller on a fresh visit alone: no scroll write that would lay the page out there and then", () => {
    navigate("/overview");
    const writes: number[] = [];
    const own = Object.getOwnPropertyDescriptor(Element.prototype, "scrollTop")!;
    Object.defineProperty(Element.prototype, "scrollTop", { configurable: true, get: own.get, set(v: number) { writes.push(v); own.set!.call(this, v); } });
    try {
      act(() => root.render(<Shell />));
    } finally {
      Object.defineProperty(Element.prototype, "scrollTop", own);
    }
    expect(writes).toEqual([]);
    expect(scroller().scrollTop).toBe(0);
  });

  it("puts the settings list back after Back from a category, and opens the category at the top", async () => {
    history.replaceState(null, "", "/settings");
    act(() => root.render(<Swapping page="settings" />));
    scrollTo(600);
    // the category goes on top of the list (Settings' pick)
    history.pushState(null, "", "/settings?tab=models");
    act(() => root.render(<Swapping page="settings/models/" />));
    expect(scroller().scrollTop).toBe(0);
    scrollTo(40);
    await back();
    act(() => root.render(<Swapping page="settings" />));
    expect(scroller().scrollTop).toBe(600);
  });
});
