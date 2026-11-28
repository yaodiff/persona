// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { FOCUS_REFRESH_MS, lookupSnapshot, replaceLookupSnapshot, TEXT_LOOKUP_CHANGED, type LookupApi, type LookupState } from "./client";
import { startTextLookup } from "./listen";

const READY: LookupState = { available: true, underline_saved: false, words: [], name: "Example" };

function harness(state: LookupState | null, features: string[]) {
  const calls: string[] = [];
  const opened: string[] = [];
  const hub = new Map<string, Set<(msg: Record<string, unknown>) => void>>();
  const win = new Map<string, Set<() => void>>();
  const api: LookupApi = {
    state: async () => {
      calls.push("state");
      if (!state) throw new Error("off");
      return state;
    },
    lookup: async () => ({ found: true, word: "hello", senses: [], saved: false }),
    save: async () => ({ saved: true }),
    remove: async () => ({ saved: false }),
  };
  const stop = startTextLookup({
    features: () => features,
    api,
    open: (hit) => opened.push(hit.word),
    delay: 0,
    doubleClick: 30,
    now: () => Date.now(),
    onHub: (type, listener) => {
      const set = hub.get(type) ?? new Set();
      set.add(listener);
      hub.set(type, set);
      return () => set.delete(listener);
    },
    onWindow: (type, listener) => {
      const set = win.get(type) ?? new Set();
      set.add(listener);
      win.set(type, set);
      return () => set.delete(listener);
    },
  });
  return { calls, opened, hub, win, stop };
}

interface ClickOpts extends MouseEventInit {
  /** Where the text sits: a host the case builds, or a plain paragraph. */
  host?: HTMLElement;
  /** The element the click lands on, when it is not the text (a field beside it). */
  on?: Element;
  /** Pointer travel between down and up. */
  move?: number;
}

function clickWord(word: string, opts: ClickOpts = {}) {
  const { host, on, move = 0, ...extra } = opts;
  const p = host ?? document.createElement("p");
  p.append(`Say ${word} there.`);
  if (!p.isConnected) document.body.append(p);
  const text = p.lastChild as Text;
  const rect = new DOMRect(0, 0, 200, 20);
  Range.prototype.getClientRects = () => ({ length: 1, 0: rect, item: () => rect }) as unknown as DOMRectList;
  Range.prototype.getBoundingClientRect = () => rect;
  (document as unknown as Record<string, unknown>).caretPositionFromPoint = () => ({
    offsetNode: text,
    offset: text.data.indexOf(word),
  });
  const target = on ?? text;
  const init = { clientX: 10, clientY: 10, button: 0, detail: 1, pointerId: 1, bubbles: true, cancelable: true, ...extra };
  target.dispatchEvent(new PointerEvent("pointerdown", init));
  if (move) target.dispatchEvent(new PointerEvent("pointermove", { ...init, clientX: 10 + move }));
  target.dispatchEvent(new PointerEvent("pointerup", { ...init, clientX: 10 + move }));
  target.dispatchEvent(new MouseEvent("click", { ...init, clientX: 10 + move }));
  return text;
}

async function ready() {
  const h = harness(READY, ["text_lookup"]);
  await vi.waitFor(() => expect(lookupSnapshot()?.available).toBe(true));
  return h;
}

/** Long enough for a lookup that was going to open (delay 0) to have opened. */
const settle = () => new Promise((r) => setTimeout(r, 20));

function el(tag: string, attrs: Record<string, string> = {}, parent: HTMLElement = document.body): HTMLElement {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, v);
  parent.append(node);
  return node;
}

/** A field next to the text: the click lands on the field even if the caret API points at the words. */
function beside(tag: string, attrs: Record<string, string> = {}) {
  const host = el("div");
  const field = el(tag, attrs, host);
  return { host, on: field };
}

afterEach(() => {
  document.body.replaceChildren();
  replaceLookupSnapshot(null);
  vi.useRealTimers();
});

describe("the page listener", () => {
  it("never asks when the feature flag is missing", async () => {
    const h = harness(READY, []);
    await vi.waitFor(() => expect(lookupSnapshot()?.available).toBe(false));
    expect(h.calls).toEqual([]);
    clickWord("hello");
    await new Promise((r) => setTimeout(r, 10));
    expect(h.opened).toEqual([]);
    h.stop();
  });

  it("stays inert when the provider is unavailable, and asks again on the change event", async () => {
    const h = harness({ available: false, underline_saved: false, words: [] }, ["text_lookup"]);
    await vi.waitFor(() => expect(h.calls).toEqual(["state"]));
    clickWord("hello");
    await new Promise((r) => setTimeout(r, 10));
    expect(h.opened).toEqual([]);
    h.hub.get(TEXT_LOOKUP_CHANGED)?.forEach((fn) => fn({}));
    await vi.waitFor(() => expect(h.calls.length).toBe(2));
    h.stop();
  });

  it("opens the word after a plain click and not after a double-click", async () => {
    const h = harness(READY, ["text_lookup"]);
    await vi.waitFor(() => expect(lookupSnapshot()?.available).toBe(true));
    clickWord("hello");
    await vi.waitFor(() => expect(h.opened).toEqual(["hello"]));
    document.body.replaceChildren();
    clickWord("there");
    const again = { clientX: 10, clientY: 10, detail: 2, bubbles: true, pointerId: 1 };
    document.body.dispatchEvent(new PointerEvent("pointerdown", again));
    document.body.dispatchEvent(new MouseEvent("click", again));
    await new Promise((r) => setTimeout(r, 20));
    expect(h.opened).toEqual(["hello"]);
    h.stop();
  });

  it("lets a plain click through as a control check", async () => {
    const h = await ready();
    clickWord("hello", { host: el("article") });
    await vi.waitFor(() => expect(h.opened).toEqual(["hello"]));
    h.stop();
  });

  it.each<[string, () => ClickOpts]>([
    ["a link", () => ({ host: el("a", { href: "#next" }) })],
    ["a button", () => ({ host: el("button") })],
    ["a role=button row", () => ({ host: el("span", {}, el("div", { role: "button", tabindex: "0" })) })],
    ["a role=link row", () => ({ host: el("span", {}, el("span", { role: "link", tabindex: "0" })) })],
    ["an option in a list", () => ({ host: el("div", { role: "option" }) })],
    ["an Inbox card whose head is a link", () => ({ host: el("span", {}, el("a", { href: "#inbox-1" }, el("div", { "data-item": "1" }))) })],
    ["an input", () => beside("input")],
    ["a textarea", () => beside("textarea")],
    ["a select", () => beside("select")],
    ["contenteditable", () => ({ host: el("div", { contenteditable: "true" }) })],
    ["contenteditable=plaintext-only", () => ({ host: el("div", { contenteditable: "plaintext-only" }) })],
    ["code", () => ({ host: el("code", {}, el("p")) })],
    ["pre", () => ({ host: el("pre") })],
    ["kbd", () => ({ host: el("kbd", {}, el("p")) })],
    ["samp", () => ({ host: el("samp", {}, el("p")) })],
    ["a terminal", () => ({ host: el("div", {}, el("div", { class: "xterm" })) })],
    ["data-no-lookup", () => ({ host: el("p", {}, el("section", { "data-no-lookup": "" })) })],
  ])("does nothing on %s", async (_name, build) => {
    const h = await ready();
    clickWord("hello", build());
    await settle();
    expect(h.opened).toEqual([]);
    h.stop();
  });

  it.each<[string, ClickOpts]>([
    ["Alt", { altKey: true }],
    ["Ctrl", { ctrlKey: true }],
    ["Meta", { metaKey: true }],
    ["Shift", { shiftKey: true }],
    ["the middle button", { button: 1 }],
    ["a triple click", { detail: 3 }],
    ["a double click's second press", { detail: 2 }],
    ["a drag", { move: 12 }],
  ])("does nothing on %s", async (_name, opts) => {
    const h = await ready();
    clickWord("hello", opts);
    await settle();
    expect(h.opened).toEqual([]);
    h.stop();
  });

  it("does nothing while text is selected", async () => {
    const h = await ready();
    const p = el("p");
    p.append("Some selected words. ");
    document.getSelection()!.selectAllChildren(p);
    clickWord("hello", { host: p });
    await settle();
    expect(h.opened).toEqual([]);
    document.getSelection()!.removeAllRanges();
    h.stop();
  });

  it("drops a pending lookup when a selection starts before it fires", async () => {
    // a slower listener, so the selection lands between the click and the lookup
    const opened: string[] = [];
    const stop = startTextLookup({
      features: () => ["text_lookup"],
      api: { state: async () => READY, lookup: async () => ({ found: false, word: "", senses: [], saved: false }), save: async () => ({}), remove: async () => ({}) },
      open: (hit) => opened.push(hit.word),
      delay: 30,
      onHub: () => () => {},
      onWindow: () => () => {},
    });
    await vi.waitFor(() => expect(lookupSnapshot()?.available).toBe(true));
    const text = clickWord("hello");
    const range = document.createRange();
    range.setStart(text, 0);
    range.setEnd(text, 3);
    document.getSelection()!.addRange(range);
    document.dispatchEvent(new Event("selectionchange"));
    await new Promise((r) => setTimeout(r, 60));
    expect(opened).toEqual([]);
    document.getSelection()!.removeAllRanges();
    stop();
  });

  it("adds no pointer listeners and no styles while lookup is off", async () => {
    const spy = vi.spyOn(document, "addEventListener");
    const h = harness({ available: false, underline_saved: true, words: ["hello"] }, ["text_lookup"]);
    await vi.waitFor(() => expect(h.calls).toEqual(["state"]));
    await settle();
    const types = spy.mock.calls.map((c) => c[0]);
    expect(types).not.toContain("click");
    expect(types).not.toContain("pointerdown");
    expect(document.head.querySelector("[data-text-lookup]")).toBeNull();
    spy.mockRestore();
    h.stop();
  });

  it("detaches the pointer listeners when the provider goes away", async () => {
    let state: LookupState = READY;
    const opened: string[] = [];
    const hub = new Set<() => void>();
    const stop = startTextLookup({
      features: () => ["text_lookup"],
      api: { state: async () => state, lookup: async () => ({ found: false, word: "", senses: [], saved: false }), save: async () => ({}), remove: async () => ({}) },
      open: (hit) => opened.push(hit.word),
      delay: 0,
      onHub: (type, fn) => {
        if (type === TEXT_LOOKUP_CHANGED) hub.add(fn as () => void);
        return () => {};
      },
      onWindow: () => () => {},
    });
    await vi.waitFor(() => expect(lookupSnapshot()?.available).toBe(true));
    state = { available: false, underline_saved: false, words: [] };
    hub.forEach((fn) => fn());
    await vi.waitFor(() => expect(lookupSnapshot()?.available).toBe(false));
    clickWord("hello");
    await settle();
    expect(opened).toEqual([]);
    stop();
  });

  it("asks again when the tab comes back, at most once per window", async () => {
    let clock = 1_000_000;
    const calls: string[] = [];
    const win = new Map<string, () => void>();
    const stop = startTextLookup({
      features: () => ["text_lookup"],
      api: { state: async () => { calls.push("state"); return READY; }, lookup: async () => ({ found: false, word: "", senses: [], saved: false }), save: async () => ({}), remove: async () => ({}) },
      open: () => {},
      now: () => clock,
      onHub: () => () => {},
      onWindow: (type, fn) => { win.set(type, fn); return () => win.delete(type); },
    });
    await vi.waitFor(() => expect(calls).toEqual(["state"]));
    win.get("focus")!();
    expect(calls).toEqual(["state"]);
    clock += FOCUS_REFRESH_MS + 1;
    win.get("focus")!();
    await vi.waitFor(() => expect(calls).toEqual(["state", "state"]));
    stop();
  });
});

describe("the saved-word underline from the listener", () => {
  function stubHighlights() {
    const highlights = { set: vi.fn(), delete: vi.fn() };
    class Highlight extends Set<AbstractRange> {}
    vi.stubGlobal("Highlight", Highlight);
    vi.stubGlobal("CSS", { highlights });
    return highlights;
  }
  afterEach(() => {
    vi.unstubAllGlobals();
    document.head.querySelector("[data-text-lookup]")?.remove();
  });

  it("adds the underline only when underline_saved is on", async () => {
    stubHighlights();
    const on = harness({ ...READY, underline_saved: true, words: ["hello"] }, ["text_lookup"]);
    await vi.waitFor(() => expect(document.head.querySelector("[data-text-lookup]")).not.toBeNull());
    on.stop();
    expect(document.head.querySelector("[data-text-lookup]")).toBeNull();

    const off = harness({ ...READY, underline_saved: false, words: ["hello"] }, ["text_lookup"]);
    await vi.waitFor(() => expect(lookupSnapshot()?.available).toBe(true));
    await settle();
    expect(document.head.querySelector("[data-text-lookup]")).toBeNull();
    off.stop();
  });
});
