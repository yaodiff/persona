// @vitest-environment jsdom
import { openAgentMore } from "../../test/agent-menu";
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { jumpWhenOpen } from "../../lib/forward";
import { applyCompleteSessions, reset } from "../../lib/store";
import type { Item, Session } from "../../lib/types";
import { forgetExpanded } from "./expanded";
import type { NavRailTick } from "../../lib/nav-rail";

const pointer = vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  const state = { fine: false };
  g.matchMedia = (query: string) => ({ get matches() { return state.fine && /pointer: fine|prefers-reduced-motion/.test(query); }, addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} };
  return state;
});
const rail = vi.hoisted(() => ({
  props: undefined as { ticks: NavRailTick[]; onJump: (id: string) => Promise<void> } | undefined,
  toast: vi.fn(),
}));
vi.mock("./NavRail", () => ({ NavRail: (props: NonNullable<typeof rail.props>) => { rail.props = props; return null; } }));
vi.mock("../../ui/atoms", async (importOriginal) => ({ ...await importOriginal<typeof import("../../ui/atoms")>(), toast: rail.toast }));
const transcript = vi.hoisted(() => ({
  items: [] as Item[], loading: false, hasMore: false, streaming: "", error: "",
  history: "" as string, replaceEpoch: 0,
  loadEarlier: vi.fn<() => Promise<symbol | undefined>>(),
  reload: vi.fn(),
  expandFold: vi.fn<() => Promise<boolean>>(),
}));
vi.mock("./useTranscript", () => ({ useTranscript: () => transcript }));
const { ChatView } = await import("./ChatView");
const { ReadOnlyChatView } = await import("./ReadOnlyChatView");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const question: Item = { id: "question", seq: 1, role: "user", kind: "text", text: "First question" };
const fold: Item = { id: "work", seq: 2, role: "assistant", kind: "fold", fold: { items: 1, tools: 0, thoughts: 1, through_seq: 2 } };
const hidden: Item = { id: "thought", seq: 2, role: "assistant", kind: "thinking", text: "Folded thought" };
const latest: Item = { ...question, id: "latest", seq: 4, text: "Latest question" };
let host: HTMLDivElement;
let root: Root;
const scroller = () => host.querySelector<HTMLElement>("[data-chat-scroll]")!;
const render = (sid = "one", landAt?: string) => act(async () => root.render(<ChatView mid="m" sid={sid} landAt={landAt} inPage />));
const advance = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms); });
const imports = () => act(async () => { await vi.dynamicImportSettled(); });
const scrolled = vi.fn();
const hit = () => host.querySelector("[data-search-hit]");

beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear();
  transcript.expandFold.mockReset();
  transcript.loadEarlier.mockReset();
  transcript.reload.mockReset();
  rail.props = undefined;
  rail.toast.mockReset();
  pointer.fine = false;
  vi.stubGlobal("CSS", { escape: (s: string) => s });
  vi.stubGlobal("fetch", vi.fn(async (input) => Response.json(String(input).includes("from_seq=") ? { items: [hidden] } : {})));
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(500);
  vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(5000);
  Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: scrolled });
  applyCompleteSessions(["one", "two"].map((sid) => ({ machine_id: "m", session_id: sid, title: sid, state: "idle", status: "idle" }) as Session));
  Object.assign(transcript, { items: [question, fold, latest], loading: false, hasMore: false, history: "", replaceEpoch: 0 });
  host = document.body.appendChild(document.createElement("div"));
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  forgetExpanded();
  Reflect.deleteProperty(HTMLElement.prototype, "scrollIntoView");
  scrolled.mockClear();
  vi.restoreAllMocks();
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

async function pickFoldedHit() {
  await render();
  await imports();
  act(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-chatview.more_actions")}"]`)!.click());
  openAgentMore();
  const search = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((el) => el.textContent?.includes(t("chat-chatview.search_menu")))!;
  await act(async () => search.click());
  await imports();
  const input = document.querySelector<HTMLInputElement>("[data-chat-search] input")!;
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "Folded thought");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await advance(250);
  let release!: (ok: boolean) => void;
  transcript.expandFold.mockImplementationOnce(() => new Promise((resolve) => { release = resolve; }));
  await act(async () => document.querySelector<HTMLButtonElement>("[data-search-result]")!.click());
  await imports();
  expect(transcript.expandFold).toHaveBeenCalledExactlyOnceWith(fold);
  return () => act(async () => release(true));
}

it("keeps transcript history and in-chat search in the standalone read-only viewer without a composer or session writes", async () => {
  await act(async () => root.render(<ReadOnlyChatView mid="m" sid="one" onToggleSidebar={() => {}} />));
  await imports();
  expect(scroller().textContent).toContain(question.text);
  expect(host.querySelector("[data-chat-readonly]")).not.toBeNull();
  expect(host.querySelector("[data-composer-input]")).toBeNull();
  expect(host.querySelector(`button[aria-label="${t("chat-chatview.more_actions")}"]`)).toBeNull();
  const search = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-chatview.search_menu")}"]`)!;
  expect(search).not.toBeNull();
  await act(async () => search.click());
  await imports();
  expect(document.querySelector("[data-chat-search] input")).not.toBeNull();
});

it.each(["wheel", "touch", "scrollbar", "End key", "another chat", "another target"])(
  "a pending folded search hit yields to %s", async (action) => {
    const release = await pickFoldedHit();
    if (action === "another chat") await render("two");
    else if (action === "another target") {
      await render("one", latest.id);
      await imports();
      await advance(240);
      expect(hit()?.textContent).toContain(latest.text);
    } else if (action === "End key") {
      act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "End" })));
    } else if (action === "touch") {
      const swipe = (type: string, clientY: number) => new TouchEvent(type, { touches: [{ clientY } as Touch] });
      act(() => scroller().dispatchEvent(swipe("touchstart", 200)));
      act(() => scroller().dispatchEvent(swipe("touchmove", 160)));
    } else {
      const event = action === "wheel" ? new WheelEvent("wheel", { deltaY: 100 })
        : new Event("pointerdown");
      act(() => scroller().dispatchEvent(event));
    }
    transcript.items = [question, hidden, latest];
    await render(action === "another chat" ? "two" : "one", action === "another target" ? latest.id : undefined);
    await release();
    await advance(800);
    if (action === "another target") {
      expect(scrolled).toHaveBeenCalledTimes(1);
      expect(hit()?.textContent).toContain(latest.text);
    } else {
      expect(scrolled).not.toHaveBeenCalled();
      expect(hit()).toBeNull();
      expect(host.querySelector("[data-jump-pill]")).toBeNull();
    }
  },
);

it("a link still waiting for the first page yields to the reader", async () => {
  transcript.loading = true;
  await render("one", question.id);
  act(() => scroller().dispatchEvent(new WheelEvent("wheel", { deltaY: -100 })));
  transcript.loading = false;
  await render("one", question.id);
  await imports();
  await advance(250);
  expect(scrolled).not.toHaveBeenCalled();
  expect(hit()).toBeNull();
});

it("Jump to bottom cancels a link while its row is settling", async () => {
  await render("one", latest.id);
  await imports();
  await advance(20);
  const pill = host.querySelector<HTMLButtonElement>("[data-jump-pill]");
  expect(pill).toBeTruthy();
  act(() => pill!.click());
  await advance(800);
  expect(scrolled).not.toHaveBeenCalled();
  expect(hit()).toBeNull();
  expect(host.querySelector("[data-jump-pill]")).toBeNull();
});

it("a forwarded original still lands after the first page arrives under StrictMode", async () => {
  jumpWhenOpen("m", "one", latest.id);
  const chat = () => act(async () => root.render(<StrictMode><ChatView mid="m" sid="one" inPage /></StrictMode>));
  transcript.loading = true;
  await chat();
  transcript.loading = false;
  await chat();
  await imports();
  await advance(800);
  expect(scrolled).toHaveBeenCalledTimes(1);
  expect(hit()?.textContent).toContain(latest.text);
});

it("lands on a folded search hit when the reader keeps the selection", async () => {
  const release = await pickFoldedHit();
  transcript.items = [question, hidden, latest];
  await render();
  await release();
  await advance(800);
  expect(scrolled).toHaveBeenCalledTimes(1);
  expect(hit()?.textContent).toContain(hidden.text);
});

const historic: Item = { ...question, id: "historic#0", seq: 100, text: "Earlier question from the index" };
const live: Item = { ...latest, seq: 800 };
const generation = "history-generation";
const context = {
  items: [historic], history: generation, from_seq: 59, to_seq: 140, has_more: true, more_after: true,
};
const pageControl = (direction: "before" | "after") => host.querySelector<HTMLButtonElement>(`[data-context-page="${direction}"]`);

async function showRail(around: () => Response | Promise<Response> = () => Response.json(context)) {
  pointer.fine = true;
  const get = vi.fn(async (input: RequestInfo | URL) => {
    const query = new URL(String(input), "https://example.com").searchParams;
    if (query.has("user_inputs")) return Response.json({
      total: 70, history: transcript.history, inputs: Array.from({ length: 69 }, (_, i) => ({ id: `historic-${i}`, seq: i + 1 }))
        .concat({ id: historic.id, seq: historic.seq }),
    });
    if (query.has("around_seq")) return around();
    if (query.get("before_seq") === "60") return Response.json({
      items: [{ ...historic, id: "before", seq: 51, text: "An older page" }], history: generation, has_more: false,
    });
    if (query.get("after_seq") === "140") return Response.json({
      items: [{ ...historic, id: "after", seq: 151, text: "A later page" }], history: generation, to_seq: 160, more_after: false,
    });
    return Response.json({});
  });
  vi.stubGlobal("fetch", get);
  Object.assign(transcript, { items: [live], hasMore: true, history: generation });
  await render();
  await imports();
  await advance(32);
  expect(rail.props?.ticks.length).toBe(70);
  return {
    queries: () => get.mock.calls.map(([url]) => new URL(String(url), "https://example.com").searchParams),
    jump: async () => {
      await act(async () => { void rail.props!.onJump(`seg:${historic.id}`); });
      await imports();
      await advance(320);
    },
  };
}

it("opens an unloaded question directly, pages from both window cursors and returns to the live tail", async () => {
  const { jump, queries } = await showRail();
  await jump();
  expect(queries().find((q) => q.has("around_seq"))?.toString()).toBe("around_seq=100&radius=40&history=history-generation");
  expect(transcript.loadEarlier).not.toHaveBeenCalled();
  expect(scroller().hasAttribute("data-chat-context-window")).toBe(true);
  expect(scroller().textContent).toContain(historic.text);
  expect(scroller().textContent).not.toContain(live.text);
  expect(hit()).toBeTruthy();
  await act(async () => pageControl("before")!.click());
  await imports();
  expect(scroller().textContent).toContain("An older page");
  expect(queries().some((q) => q.get("before_seq") === "60")).toBe(true);
  await act(async () => pageControl("after")!.click());
  await imports();
  expect(scroller().textContent).toContain("A later page");
  expect(queries().some((q) => q.get("after_seq") === "140")).toBe(true);

  const newest = { ...live, id: "live-new", seq: 801, text: "A message arrived at the live tail" };
  transcript.items = [live, newest];
  await render();
  expect(scroller().textContent).not.toContain(newest.text);
  act(() => host.querySelector<HTMLButtonElement>("[data-jump-pill]")!.click());
  expect(scroller().hasAttribute("data-chat-context-window")).toBe(false);
  expect(scroller().textContent).toContain(newest.text);
  expect(scroller().textContent).not.toContain(historic.text);
});

it.each(["node_outdated", "unsupported", "ordinary page"])("keeps backward paging when the target read returns %s", async (outcome) => {
  const { jump } = await showRail(() => outcome === "ordinary page" ?
    Response.json({ items: [live], history: generation, has_more: true, to_seq: live.seq }) :
    Response.json({ code: outcome }, { status: 409 }));
  transcript.loadEarlier.mockImplementationOnce(async () => {
    transcript.items = [historic, live];
    transcript.hasMore = false;
    root.render(<ChatView mid="m" sid="one" inPage />);
    return Symbol();
  });
  await jump();
  await advance(320);
  expect(transcript.loadEarlier).toHaveBeenCalledOnce();
  expect(scroller().hasAttribute("data-chat-context-window")).toBe(false);
  expect(scroller().textContent).toContain(historic.text);
  expect(hit()).toBeTruthy();
});

it("refreshes both the index and transcript after history_changed instead of paging the stale target", async () => {
  const { jump, queries } = await showRail(() => Response.json({ code: "history_changed" }, { status: 409 }));
  await jump();
  expect(transcript.reload).toHaveBeenCalledOnce();
  expect(queries().filter((q) => q.has("user_inputs"))).toHaveLength(2);
  expect(transcript.loadEarlier).not.toHaveBeenCalled();
  expect(scroller().hasAttribute("data-chat-context-window")).toBe(false);
});

it("keeps the current view on a failed target read without a compatibility refusal", async () => {
  const { jump } = await showRail(() => Response.json({ code: "internal" }, { status: 500 }));
  await jump();
  expect(transcript.loadEarlier).not.toHaveBeenCalled();
  expect(scroller().hasAttribute("data-chat-context-window")).toBe(false);
  expect(scroller().textContent).toContain(live.text);
  expect(rail.toast).toHaveBeenCalledWith(t("chat-navrail.target_failed"), "error");
});

it("discards a late target window when the reader scrolls the chat first", async () => {
  let release!: (response: Response) => void;
  const { jump } = await showRail(() => new Promise((resolve) => { release = resolve; }));
  await jump();
  act(() => scroller().dispatchEvent(new WheelEvent("wheel", { deltaY: -100 })));
  await act(async () => release(Response.json(context)));
  await advance(320);
  expect(scroller().hasAttribute("data-chat-context-window")).toBe(false);
  expect(scroller().textContent).toContain(live.text);
  expect(hit()).toBeNull();
});

it("invalidates a detached window after a live transcript replacement", async () => {
  const { jump } = await showRail();
  await jump();
  expect(scroller().hasAttribute("data-chat-context-window")).toBe(true);
  Object.assign(transcript, { history: "rewritten-history", replaceEpoch: 1 });
  await render();
  await advance(64);
  expect(scroller().hasAttribute("data-chat-context-window")).toBe(false);
  expect(scroller().textContent).toContain(live.text);
  expect(scroller().textContent).not.toContain(historic.text);
});
