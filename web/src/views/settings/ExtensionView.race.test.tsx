// @vitest-environment jsdom
// An extension view's answers come back in any order: a redraw (extension_ui {invalidate}, refresh_after) that left
// before a press, a page pushed or a search typed again is older than what they answered, and does not put its view
// back over theirs. The hub is faked: every draw and press waits until the test answers it.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../../lib/api";

type Asked = { kind: "draw" | "press"; action?: string; state?: string; values?: Record<string, string>; answer: (view: unknown) => void };
const hub = vi.hoisted(() => ({ asked: [] as Asked[], listeners: new Map<string, Set<(m: Record<string, unknown>) => void>>(), openLink: vi.fn(), toast: vi.fn() }));

vi.mock("../../lib/links", async (original) => ({
  ...(await original<typeof import("../../lib/links")>()),
  openLink: (...args: unknown[]) => hub.openLink(...args),
}));
vi.mock("../../lib/extension-ui", async (original) => {
  const real = await original<typeof import("../../lib/extension-ui")>();
  return {
    ...real,
    renderUI: (_ext: string, _ui: string, _ctx: unknown, state?: string) =>
      new Promise((resolve) => hub.asked.push({ kind: "draw", state, answer: (v) => resolve(real.readView(v)) })),
    actUI: (_ext: string, _ui: string, action: string, opts: { state?: string; values?: Record<string, string> } = {}) =>
      new Promise((resolve, reject) => hub.asked.push({
        kind: "press", action, state: opts.state, values: opts.values,
        answer: (v) => {
          if (v instanceof Error) return reject(v);
          if (v && typeof v === "object" && !("sections" in v)) {
            const o = v as { view?: unknown; [k: string]: unknown };
            return resolve({ ...o, ...(o.view ? { view: real.readView(o.view) } : {}) });
          }
          return resolve({ view: real.readView(v) });
        },
      })),
  };
});
vi.mock("../../lib/store", async (original) => ({
  ...(await original<typeof import("../../lib/store")>()),
  onHubEvent: (type: string, l: (m: Record<string, unknown>) => void) => {
    if (!hub.listeners.has(type)) hub.listeners.set(type, new Set());
    hub.listeners.get(type)!.add(l);
    return () => void hub.listeners.get(type)?.delete(l);
  },
}));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: (...args: unknown[]) => hub.toast(...args) }));
import { ExtensionUI } from "./ExtensionView";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.IS_REACT_ACT_ENVIRONMENT = true;
  g.matchMedia = (q: string) => ({ matches: q.includes("min-width"), addEventListener() {}, removeEventListener() {} });
});

/** A page saying `word`, with a row that pushes page b, a button `go`, and a search box. */
const page = (word: string, state = word) => ({
  state,
  search: { action: { id: "search" }, value: "" },
  sections: [{
    items: [
      { type: "text", text: `正文 ${word}` },
      { type: "row", title: "打开 b", action: { id: "open", push: { state: "b" } } },
      { type: "button", title: "执行", action: { id: "go" } },
    ],
  }],
});

const SEARCH_DEBOUNCE_MS = 300;

let host: HTMLDivElement;
let root: Root;
const settle = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const answer = async (a: Asked | undefined, view: unknown) => {
  expect(a).toBeTruthy();
  await act(async () => a!.answer(view));
  await settle();
};
/** The texts of the view on screen (the body lines). */
const shown = () => {
  const out: string[] = [];
  const walk = document.createTreeWalker(host, NodeFilter.SHOW_TEXT);
  for (let n = walk.nextNode(); n; n = walk.nextNode()) if (/^正文 /.test(n.textContent ?? "")) out.push(n.textContent!);
  return out;
};
const invalidate = async () => {
  await act(async () => hub.listeners.get("extension_ui")?.forEach((l) => l({ type: "extension_ui", extension: "demo", invalidate: true })));
  await settle();
};
/** Types `v` into the search box and lets its debounce (ExtensionView: asked 300 ms after the last key) run out. */
const type = async (v: string) => {
  const box = host.querySelector("input")!;
  await act(async () => {
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    set.call(box, v);
    box.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () => { await vi.advanceTimersByTimeAsync(SEARCH_DEBOUNCE_MS); });
  await settle();
};
const button = (text: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.includes(text));

beforeEach(async () => {
  hub.asked = [];
  hub.listeners.clear();
  hub.openLink.mockReset();
  hub.toast.mockReset();
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "visible" });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<ExtensionUI extension="demo" ui="main" history={false} />));
  await settle();
  await answer(hub.asked.shift(), page("a"));
  expect(shown()).toEqual(["正文 a"]);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
});

describe("ExtensionUI answers out of order", () => {
  it("a redraw that left before a press does not put the old view back over the press's", async () => {
    await invalidate();
    const redraw = hub.asked.shift();
    expect(redraw?.kind).toBe("draw");
    await act(async () => button("执行")!.click());
    await settle();
    const press = hub.asked.shift();
    expect(press?.action).toBe("go");
    await answer(press, page("pressed", "a2"));
    expect(shown()).toEqual(["正文 pressed"]);
    await answer(redraw, page("a")); // the redraw's answer comes last
    expect(shown()).toEqual(["正文 pressed"]);
    // what is sent next is the newest view's state
    await act(async () => button("执行")!.click());
    await settle();
    expect(hub.asked.shift()?.state).toBe("a2");
  });

  it("a redraw of the page below does not draw it over a page pushed meanwhile", async () => {
    await invalidate();
    const redraw = hub.asked.shift();
    expect(redraw?.state).toBe("a");
    await act(async () => button("打开 b")!.click());
    await settle();
    const drawB = hub.asked.shift();
    expect(drawB?.state).toBe("b");
    await answer(drawB, page("b"));
    expect(shown()).toEqual(["正文 b"]);
    await answer(redraw, page("a"));
    expect(shown()).toEqual(["正文 b"]);
  });

  it("an earlier search answered last does not replace a later one's results", async () => {
    // fake only the timers: the search debounce runs out at once instead of sleeping in real time
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    await type("x");
    const first = hub.asked.shift();
    expect(first?.values?.q).toBe("x");
    await type("xy");
    const second = hub.asked.shift();
    expect(second?.values?.q).toBe("xy");
    await answer(second, { ...page("xy"), search: { action: { id: "search" }, value: "xy" } });
    await answer(first, { ...page("x"), search: { action: { id: "search" }, value: "x" } });
    expect(shown()).toEqual(["正文 xy"]);
  });

  it("an earlier search that fails as unsupported does not take away the view a later one drew", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    await type("x");
    const first = hub.asked.shift();
    await type("xy");
    const second = hub.asked.shift();
    await answer(second, { ...page("xy"), search: { action: { id: "search" }, value: "xy" } });
    await answer(first, new ApiError(404, "unsupported", { code: "unsupported" }));
    expect(shown()).toEqual(["正文 xy"]);
  });

  it("suppresses late run() side effects (openLink, toast, reload, pop) after unmount", async () => {
    const backSpy = vi.spyOn(history, "back").mockImplementation(() => {});
    try {
      await act(async () => button("执行")!.click());
      await settle();
      const press = hub.asked.shift();
      expect(press?.action).toBe("go");
      act(() => root.unmount());
      await answer(press, { toast: "late", open_url: "https://example.com/late", pop: true, reload: true });
      expect(hub.openLink).not.toHaveBeenCalled();
      expect(hub.toast).not.toHaveBeenCalled();
      expect(backSpy).not.toHaveBeenCalled();
      expect(hub.asked).toEqual([]);
    } finally {
      backSpy.mockRestore();
    }
  });

  it("suppresses late run() pop, openLink, toast, and reload after a page push", async () => {
    await act(async () => button("执行")!.click());
    await settle();
    const press = hub.asked.shift();
    expect(press?.action).toBe("go");
    // Open page b while page a's action is still in flight (via search/push or prop change).
    act(() => root.render(<ExtensionUI extension="demo" ui="other" history={false} />));
    await settle();
    const drawOther = hub.asked.shift();
    await answer(drawOther, page("other"));
    expect(shown()).toEqual(["正文 other"]);
    await answer(press, { toast: "stale", open_url: "https://example.com/stale", pop: true, reload: true });
    expect(shown()).toEqual(["正文 other"]);
    expect(hub.openLink).not.toHaveBeenCalled();
    expect(hub.toast).not.toHaveBeenCalled();
    expect(hub.asked).toEqual([]);
  });

  it("re-arms refresh_after when an interval elapses while an action is busy", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    act(() => root.render(<ExtensionUI extension="demo" ui="refresh-test" history={false} />));
    await settle();
    const initial = hub.asked.shift();
    await answer(initial, { ...page("r1"), refresh_after: 30 });
    expect(shown()).toEqual(["正文 r1"]);

    // Start an action so busy is active while the 30s refresh deadline passes.
    await act(async () => button("执行")!.click());
    await settle();
    const press = hub.asked.shift();
    expect(press?.action).toBe("go");

    await act(async () => { await vi.advanceTimersByTimeAsync(35_000); });
    expect(hub.asked.filter((a) => a.kind === "draw")).toHaveLength(0);

    // Finish the action without redrawing the view (drawnAt stays stale): refresh must immediately fire.
    await answer(press, { toast: "done" });
    const refreshes = hub.asked.filter((a) => a.kind === "draw");
    expect(refreshes).toHaveLength(1);
    await answer(refreshes[0], { ...page("r2"), refresh_after: 30 });
    expect(shown()).toEqual(["正文 r2"]);
  });

  it("suppresses stale redraws and late run() side effects across push -> pop ABA back to Page A", async () => {
    // Visit 1 of Page A starts a redraw before pushing Page B.
    await invalidate();
    const visit1Draw = hub.asked.shift();
    expect(visit1Draw?.kind).toBe("draw");

    // Push Page B, start run() on Page B, then click Back (enabled during busy) to return to Page A (Visit 2).
    await act(async () => button("打开 b")!.click());
    await settle();
    await answer(hub.asked.shift(), page("b"));
    expect(shown()).toEqual(["正文 b"]);

    await act(async () => button("执行")!.click());
    await settle();
    const pageBPress = hub.asked.shift();
    expect(pageBPress?.action).toBe("go");

    const backBtn = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("返回"))!;
    await act(async () => backBtn.click());
    await settle();
    await answer(hub.asked.shift(), page("a-visit2"));
    expect(shown()).toEqual(["正文 a-visit2"]);

    // Visit 1's redraw (identical pageKey & top.id) and Page B's late run() resolve now:
    // neither may overwrite Visit 2 or trigger toast, openLink, pop, or reload.
    await answer(visit1Draw, page("stale-visit1"));
    await answer(pageBPress, { view: page("stale-b"), toast: "stale", open_url: "https://example.com/aba", pop: true, reload: true });
    expect(shown()).toEqual(["正文 a-visit2"]);
    expect(hub.toast).not.toHaveBeenCalled();
    expect(hub.openLink).not.toHaveBeenCalled();
    expect(hub.asked).toEqual([]);
  });

  it("suppresses late run() side effects when context.item or manager switches on the same extension/ui", async () => {
    act(() => root.render(<ExtensionUI extension="demo" ui="main" context={{ item: "item-1" }} manager="m1" history={false} />));
    await settle();
    await answer(hub.asked.shift(), page("item-1"));
    expect(shown()).toEqual(["正文 item-1"]);

    await act(async () => button("执行")!.click());
    await settle();
    const item1Press = hub.asked.shift();
    expect(item1Press?.action).toBe("go");

    // Switch context.item and manager while extension & ui remain identical.
    act(() => root.render(<ExtensionUI extension="demo" ui="main" context={{ item: "item-2" }} manager="m2" history={false} />));
    await settle();
    await answer(hub.asked.shift(), page("item-2"));
    expect(shown()).toEqual(["正文 item-2"]);

    await answer(item1Press, { view: page("stale-item-1"), toast: "item1-late", open_url: "https://example.com/item1", reload: true });
    expect(shown()).toEqual(["正文 item-2"]);
    expect(hub.toast).not.toHaveBeenCalled();
    expect(hub.openLink).not.toHaveBeenCalled();
    expect(hub.asked).toEqual([]);
  });

  it("clears loading spinner when run() preempts an in-flight redraw", async () => {
    act(() => root.render(<ExtensionUI extension="demo" ui="titled" title="Header" history={false} />));
    await settle();
    await answer(hub.asked.shift(), { ...page("a"), title: "Header" });

    // Start an invalidation redraw (shows header <Spinner />), then preempt it with run().
    await invalidate();
    const redraw = hub.asked.shift();
    expect(redraw?.kind).toBe("draw");
    expect(host.querySelector("h2")?.parentElement?.querySelector("span[aria-hidden]")).toBeTruthy();

    await act(async () => button("执行")!.click());
    await settle();
    const press = hub.asked.shift();
    expect(press?.action).toBe("go");
    // Preempted redraw must immediately clear loading spinner rather than staying stuck forever.
    expect(host.querySelector("h2")?.parentElement?.querySelector("span[aria-hidden]")).toBeNull();

    await answer(press, { toast: "done" });
    await answer(redraw, { ...page("old"), title: "Header" });
    expect(host.querySelector("h2")?.parentElement?.querySelector("span[aria-hidden]")).toBeNull();
  });

  it("does not fire a duplicate useRefresh draw when run() ends with r.reload after refresh_after elapsed", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    act(() => root.render(<ExtensionUI extension="demo" ui="refresh-reload" history={false} />));
    await settle();
    await answer(hub.asked.shift(), { ...page("r1"), refresh_after: 30 });

    await act(async () => button("执行")!.click());
    await settle();
    const press = hub.asked.shift();
    await act(async () => { await vi.advanceTimersByTimeAsync(35_000); });
    expect(hub.asked.filter((a) => a.kind === "draw")).toHaveLength(0);

    // Resolving with reload triggers exactly ONE renderUI (from r.reload), not a second duplicate from useRefresh.
    await answer(press, { reload: true });
    const draws = hub.asked.filter((a) => a.kind === "draw");
    expect(draws).toHaveLength(1);
    await answer(draws[0], { ...page("r2"), refresh_after: 30 });
    expect(shown()).toEqual(["正文 r2"]);
  });
});
