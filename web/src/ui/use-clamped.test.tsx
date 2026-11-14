// @vitest-environment jsdom
// useClamped (#4715, #4742): a clamped text says it is cut on its first layout, and again whenever its box changes
// (a side panel opens, the window narrows) without the card re-rendering.
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { useClamped } from "./use-clamped";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root;
let host: HTMLDivElement;
let tall = 0;
let reads = 0;
const observers: (() => void)[] = [];
const saved = { scroll: Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight"), client: Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientHeight"), ro: globalThis.ResizeObserver };

beforeEach(() => {
  reads = 0;
  Object.defineProperty(HTMLElement.prototype, "scrollHeight", { configurable: true, get: () => (reads++, tall) });
  Object.defineProperty(HTMLElement.prototype, "clientHeight", { configurable: true, get: () => 80 });
  globalThis.ResizeObserver = class {
    constructor(private cb: () => void) {}
    observe() {
      observers.push(this.cb);
    }
    unobserve() {}
    disconnect() {
      const i = observers.indexOf(this.cb);
      if (i >= 0) observers.splice(i, 1);
    }
  } as unknown as typeof ResizeObserver;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  observers.length = 0;
  if (saved.scroll) Object.defineProperty(HTMLElement.prototype, "scrollHeight", saved.scroll);
  if (saved.client) Object.defineProperty(HTMLElement.prototype, "clientHeight", saved.client);
  globalThis.ResizeObserver = saved.ro;
});

function Probe({ text = "text", tick = 0, words = false }: { text?: string; tick?: number; words?: boolean }) {
  const [open, setOpen] = useState(false);
  const { ref, cut } = useClamped<HTMLDivElement>(open, "y", words ? text : undefined);
  return (
    <>
      <div ref={ref} data-tick={tick}>{text}</div>
      {cut && <button onClick={() => setOpen((o) => !o)}>{open ? "less" : "more"}</button>}
    </>
  );
}

const resize = () => act(() => observers.forEach((f) => f()));

describe("useClamped", () => {
  it("shows the expander once a narrower box cuts the text, and drops it once it fits again", () => {
    tall = 80;
    act(() => root.render(<Probe />));
    expect(host.querySelector("button")).toBeNull();
    expect(observers.length).toBe(1);
    tall = 200; // a side panel opened: the same text now needs more lines
    resize();
    expect(host.querySelector("button")?.textContent).toBe("more");
    tall = 80; // wide again
    resize();
    expect(host.querySelector("button")).toBeNull();
  });

  it("measures a card first laid out hidden once its box appears", () => {
    tall = 0;
    act(() => root.render(<Probe />));
    expect(host.querySelector("button")).toBeNull();
    tall = 300;
    resize();
    expect(host.querySelector("button")).not.toBeNull();
  });

  // audit item 34: it measured after every render, and each read of the size laid the page out first; a long Inbox
  // paid that on every update of its draft cards
  it("does not read the size again when it renders with the same words", () => {
    tall = 80;
    act(() => root.render(<Probe />));
    const first = reads;
    expect(first).toBeGreaterThan(0);
    for (let tick = 1; tick <= 5; tick++) act(() => root.render(<Probe tick={tick} />));
    expect(reads).toBe(first);
  });

  it("measures again when its words change in the same box", async () => {
    tall = 80;
    act(() => root.render(<Probe />));
    expect(host.querySelector("button")).toBeNull();
    tall = 200; // longer words: the clamped box keeps its height, so only the words say so
    await act(async () => root.render(<Probe text="a much longer text" />));
    expect(host.querySelector("button")?.textContent).toBe("more");
  });

  // a caller with a plain string passes it: the new words are measured in the same layout, before the page paints,
  // not by the observer afterwards (a frame of a longer text without its "Show all")
  it("measures words it is given in the same layout as they change", async () => {
    tall = 80;
    act(() => root.render(<Probe words />));
    const first = reads;
    act(() => root.render(<Probe words tick={1} />));
    expect(reads).toBe(first);
    tall = 200;
    act(() => root.render(<Probe words text="a much longer text" />));
    expect(host.querySelector("button")?.textContent).toBe("more");
    await act(async () => { await Promise.resolve(); }); // the observer's own look, which finds the same
  });

  it("keeps 收起 while open, whatever the box does", () => {
    tall = 200;
    act(() => root.render(<Probe />));
    act(() => host.querySelector("button")!.click());
    tall = 80;
    resize();
    expect(host.querySelector("button")?.textContent).toBe("less");
  });
});
