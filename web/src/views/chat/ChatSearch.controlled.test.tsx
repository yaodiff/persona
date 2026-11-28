// @vitest-environment jsdom
// Controlled ChatSearch panels can supply precomputed results, custom matchers and earlier-loading state;
// default callers retain the debounced searchChat and internal earlier-loading lifecycle.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import type { ChatSearchHit, ChatSearchResults } from "../../lib/chat-search";
import type { Item } from "../../lib/types";
import type { ChatSearchProps, LoadOutcome } from "./ChatSearch";

const mocks = vi.hoisted(() => ({
  searchChat: vi.fn(),
}));

vi.mock("../../lib/chat-search-match", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/chat-search-match")>();
  mocks.searchChat.mockImplementation(actual.searchChat);
  return {
    ...actual,
    searchChat: mocks.searchChat,
  };
});

const { ChatSearch } = await import("./ChatSearch");
const { searchChat: realSearchChat } = await vi.importActual<typeof import("../../lib/chat-search-match")>("../../lib/chat-search-match");
const { t } = await import("../../i18n");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const defaultItems: Item[] = [
  { id: "u#1", seq: 1, role: "user", kind: "text", text: "inspect parser token" },
  { id: "tr#1", seq: 2, role: "user", kind: "tool_result", tool_use_id: "call-1", text: "PASS parser suite" },
];

const items: Item[] = [
  { id: "u#0", seq: 1, role: "user", kind: "text", text: "look at the parser" },
  { id: "a#0", seq: 2, role: "assistant", kind: "text", text: "the parser is fixed" },
];
const hit: ChatSearchHit = { itemId: "a#0", category: "text", snippet: "the parser is fixed", ranges: [[4, 10]] };
const found: ChatSearchResults = { hits: [hit], truncated: false };

const controlledHit: ChatSearchHit = {
  itemId: "remote#1",
  category: "text",
  snippet: "remote parser hit",
  ranges: [[7, 13]],
};

let host: HTMLDivElement;
let root: Root;
let onQuery: Mock<(q: string) => void>;
let onPick: Mock<(hit: ChatSearchHit) => void>;
let onClose: Mock<() => void>;

const base = (over: Partial<ChatSearchProps> = {}): ChatSearchProps => ({
  open: true,
  query: "",
  onQuery,
  items,
  rounds: 1,
  hasMore: false,
  onLoadEarlier: async () => null,
  onPick,
  onClose,
  ...over,
});

const mount = (props: Partial<ChatSearchProps> = {}) => {
  const full: ChatSearchProps = {
    open: true,
    query: "",
    onQuery,
    rounds: 2,
    hasMore: false,
    onPick,
    onClose,
    ...props,
  };
  act(() => root.render(<ChatSearch {...full} />));
  return full;
};

const mountBase = async (props: ChatSearchProps) => {
  await act(async () => root.render(<ChatSearch {...props} />));
};

const button = (label: string) => {
  const found = [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((item) => item.textContent === label);
  if (!found) throw new Error(`button not found: ${label}`);
  return found;
};

const searchInput = () => document.querySelector<HTMLInputElement>("[data-chat-search] input")!;
const buttonByText = (label: string) =>
  [...document.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent?.trim() === label);

beforeEach(() => {
  vi.clearAllMocks();
  mocks.searchChat.mockImplementation(realSearchChat);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  onQuery = vi.fn();
  onPick = vi.fn();
  onClose = vi.fn();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  vi.useRealTimers();
});

describe("ChatSearch with controlled results and matching", () => {
  it("renders supplied results without calling searchChat or a supplied matcher", async () => {
    const searchItems = vi.fn(() => found);
    await mountBase(base({ query: "parser", results: found, searching: true, searchItems }));

    expect(mocks.searchChat).not.toHaveBeenCalled();
    expect(searchItems).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("the parser is fixed");
    expect(document.querySelectorAll("[data-search-result]")).toHaveLength(1);
    act(() => document.querySelector<HTMLButtonElement>("[data-search-result]")!.click());
    expect(onPick).toHaveBeenCalledWith(hit);
  });

  it("invalidates old controlled results while a new query is pending", async () => {
    await mountBase(base({ query: "parser", results: found }));
    expect(document.querySelectorAll("[data-search-result]")).toHaveLength(1);

    // The query moved, but the parent has not supplied a new result object yet: the old hit is neither drawn nor
    // accepted by Enter.
    await mountBase(base({ query: "next", results: found }));
    expect(document.body.textContent).not.toContain("the parser is fixed");
    expect(document.querySelectorAll("[data-search-result]")).toHaveLength(0);
    const input = document.querySelector<HTMLInputElement>("input")!;
    await act(async () => input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    expect(onPick).not.toHaveBeenCalled();

    const empty: ChatSearchResults = { hits: [], truncated: false };
    await mountBase(base({ query: "next", results: empty }));
    expect(document.body.textContent).toContain(t("chat-chatsearch.no_hits"));
    expect(document.body.textContent).not.toContain(t("chat-chatsearch.enter_keyword"));
  });

  it("uses a custom matcher after the configured settle time, not the default matcher", async () => {
    vi.useFakeTimers();
    const searchItems = vi.fn(() => found);
    await mountBase(base({ searchItems, settleMs: 75 }));
    searchItems.mockClear();
    await mountBase(base({ query: "parser", searchItems, settleMs: 75 }));
    act(() => vi.advanceTimersByTime(74));
    expect(searchItems).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(1));
    expect(searchItems).toHaveBeenCalledWith(items, "parser");
    expect(mocks.searchChat).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("the parser is fixed");
  });

  it("uses controlled earlier-load state and stop callback", async () => {
    let stopped: (() => boolean) | undefined;
    const onLoadEarlier = vi.fn(async (_onPage: (n: number) => void, isStopped: () => boolean): Promise<LoadOutcome> => {
      stopped = isStopped;
      return null;
    });
    const onStopLoadEarlier = vi.fn();
    const controlled = {
      query: "parser",
      results: found,
      hasMore: true,
      onLoadEarlier,
      onStopLoadEarlier,
      loadingEarlier: false,
      loadedPages: 4,
      notice: "External stop reason",
    };
    await mountBase(base(controlled));
    expect(document.body.textContent).toContain("External stop reason");
    await act(async () => button(t("chat-chatsearch.load_earlier")).click());
    expect(onLoadEarlier).toHaveBeenCalledTimes(1);
    expect(stopped?.()).toBe(false);

    await mountBase(base({ ...controlled, loadingEarlier: true, loadedPages: 6 }));
    expect(document.body.textContent).toContain(t("chat-chatsearch.loading_pages", { count: 6 }));
    act(() => button(t("chat-chatsearch.stop_loading")).click());
    expect(stopped?.()).toBe(true);
    expect(onStopLoadEarlier).toHaveBeenCalledTimes(1);
  });

  it("fences an earlier-load run across close/reopen and a newer run", async () => {
    const runs: Array<{
      onPage: (n: number) => void;
      stopped: () => boolean;
      resolve: (outcome: LoadOutcome) => void;
    }> = [];
    const onLoadEarlier = vi.fn((onPage: (n: number) => void, stopped: () => boolean) =>
      new Promise<LoadOutcome>((resolve) => {
        runs.push({ onPage, stopped, resolve });
      }));
    const onStopLoadEarlier = vi.fn();
    const props = base({ query: "parser", results: found, hasMore: true, onLoadEarlier, onStopLoadEarlier });

    await mountBase(props);
    await act(async () => button(t("chat-chatsearch.load_earlier")).click());
    expect(runs).toHaveLength(1);
    act(() => runs[0].onPage(1));
    expect(document.body.textContent).toContain(t("chat-chatsearch.loading_pages", { count: 1 }));

    await mountBase({ ...props, open: false });
    expect(runs[0].stopped()).toBe(true);
    expect(onStopLoadEarlier).toHaveBeenCalledTimes(1);
    await mountBase({ ...props, open: true });
    expect(button(t("chat-chatsearch.load_earlier"))).toBeDefined();

    await act(async () => button(t("chat-chatsearch.load_earlier")).click());
    expect(runs).toHaveLength(2);
    expect(runs[0].stopped()).toBe(true);
    expect(runs[1].stopped()).toBe(false);
    act(() => runs[1].onPage(2));
    expect(document.body.textContent).toContain(t("chat-chatsearch.loading_pages", { count: 2 }));

    act(() => runs[0].onPage(9));
    await act(async () => {
      runs[0].resolve("Stale run");
      await Promise.resolve();
    });
    expect(document.body.textContent).toContain(t("chat-chatsearch.loading_pages", { count: 2 }));
    expect(document.body.textContent).not.toContain("Stale run");
    expect(button(t("chat-chatsearch.stop_loading"))).toBeDefined();

    await act(async () => {
      runs[1].resolve("New run finished");
      await Promise.resolve();
    });
    expect(document.body.textContent).toContain("New run finished");
    expect(button(t("chat-chatsearch.load_earlier"))).toBeDefined();
  });
});

describe("ChatSearch with controlled search and loading props", () => {
  it("renders controlled results without items or searchChat and supports immediate Enter selection", () => {
    const controlledResults: ChatSearchResults = { hits: [controlledHit], truncated: true };
    mount({ query: "parser", results: controlledResults, rounds: 4 });

    expect(mocks.searchChat).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain(t("chat-chatsearch.summary", { hits: "1+", rounds: 4 }));
    expect(document.body.textContent).toContain(t("chat-chatsearch.truncated"));
    expect([...document.querySelectorAll("mark")].map((mark) => mark.textContent)).toEqual(["parser"]);

    act(() => {
      searchInput().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    });
    expect(onPick).toHaveBeenCalledWith(controlledHit);
  });

  it("blocks Enter selection while controlled searching is active and updates empty state immediately once done", () => {
    const empty: ChatSearchResults = { hits: [], truncated: false };
    mount({ query: "parser", results: { hits: [controlledHit], truncated: false }, searching: true });

    act(() => {
      searchInput().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    });
    expect(onPick).not.toHaveBeenCalled();

    mount({ query: "missing", results: empty, searching: false });
    expect(document.body.textContent).toContain(t("chat-chatsearch.no_hits"));

    mount({ query: "   ", results: empty, searching: false });
    expect(document.body.textContent).toContain(t("chat-chatsearch.enter_keyword"));
    expect(mocks.searchChat).not.toHaveBeenCalled();
  });

  it("uses a custom searchItems matcher and skips searchChat while closed or open", () => {
    const searchItems = vi.fn<NonNullable<ChatSearchProps["searchItems"]>>(() => ({
      hits: [controlledHit],
      truncated: false,
    }));

    mount({ open: false, query: "parser", items: defaultItems, searchItems, settleMs: 0 });
    expect(searchItems).not.toHaveBeenCalled();
    expect(mocks.searchChat).not.toHaveBeenCalled();

    mount({ open: true, query: "parser", items: defaultItems, searchItems, settleMs: 0 });
    expect(searchItems).toHaveBeenCalledWith(defaultItems, "parser");
    expect(mocks.searchChat).not.toHaveBeenCalled();
    expect(document.querySelectorAll("[data-search-result]")).toHaveLength(1);
  });

  it("honors controlled earlier-loading state, notice, stop callback and missing onLoadEarlier", () => {
    const onStopLoadEarlier = vi.fn();
    mount({
      hasMore: true,
      loadingEarlier: true,
      loadedPages: 3,
      onStopLoadEarlier,
    });

    expect(document.body.textContent).toContain(t("chat-chatsearch.loading_pages", { count: 3 }));
    act(() => buttonByText(t("chat-chatsearch.stop_loading"))!.click());
    expect(onStopLoadEarlier).toHaveBeenCalledTimes(1);

    // Closing the dialog while loading notifies the controlled stop callback once.
    mount({
      open: false,
      hasMore: true,
      loadingEarlier: true,
      loadedPages: 3,
      onStopLoadEarlier,
    });
    expect(onStopLoadEarlier).toHaveBeenCalledTimes(2);

    // Without onLoadEarlier, hasMore renders no load button while still showing a controlled notice.
    mount({
      open: true,
      hasMore: true,
      loadingEarlier: false,
      notice: "Custom status notice",
      onStopLoadEarlier,
    });
    expect(document.body.textContent).toContain("Custom status notice");
    expect(buttonByText(t("chat-chatsearch.load_earlier"))).toBeUndefined();
  });

  it("fences overlapping and partially controlled earlier-loading runs against stale page, outcome and failure updates", async () => {
    const runs: Array<{
      onPage: (n: number) => void;
      stopped: () => boolean;
      resolve: (outcome: string | null) => void;
      reject: (error: Error) => void;
    }> = [];
    const onLoadEarlier = vi.fn((onPage: (n: number) => void, stopped: () => boolean) =>
      new Promise<string | null>((resolve, reject) => {
        runs.push({ onPage, stopped, resolve, reject });
      }));

    // Partially controlled: loadingEarlier is held false while pages and notice remain internal,
    // allowing a second run to start before the first settles.
    mount({ hasMore: true, loadingEarlier: false, onLoadEarlier });
    act(() => buttonByText(t("chat-chatsearch.load_earlier"))!.click());
    expect(runs).toHaveLength(1);
    act(() => runs[0].onPage(1));
    expect(runs[0].stopped()).toBe(false);

    act(() => buttonByText(t("chat-chatsearch.load_earlier"))!.click());
    expect(runs).toHaveLength(2);
    expect(runs[0].stopped()).toBe(true);
    expect(runs[1].stopped()).toBe(false);
    act(() => runs[1].onPage(2));

    // Releasing controlled loadingEarlier exposes internal loading and page count for the active run.
    mount({ hasMore: true, onLoadEarlier });
    expect(document.body.textContent).toContain(t("chat-chatsearch.loading_pages", { count: 2 }));
    expect(buttonByText(t("chat-chatsearch.stop_loading"))).toBeDefined();

    // Stale page progress and settlement from run 1 cannot overwrite run 2 or end loading early.
    act(() => runs[0].onPage(9));
    expect(document.body.textContent).toContain(t("chat-chatsearch.loading_pages", { count: 2 }));
    await act(async () => {
      runs[0].resolve("Stale run 1 notice");
      await Promise.resolve();
    });
    expect(document.body.textContent).toContain(t("chat-chatsearch.loading_pages", { count: 2 }));
    expect(document.body.textContent).not.toContain("Stale run 1 notice");
    expect(buttonByText(t("chat-chatsearch.stop_loading"))).toBeDefined();

    await act(async () => {
      runs[1].resolve("Run 2 finished");
      await Promise.resolve();
    });
    expect(document.body.textContent).toContain("Run 2 finished");
    expect(buttonByText(t("chat-chatsearch.load_earlier"))).toBeDefined();

    // A stale rejection from an overlapped run also cannot clobber the newer run.
    mount({ hasMore: true, loadingEarlier: false, onLoadEarlier });
    act(() => buttonByText(t("chat-chatsearch.load_earlier"))!.click());
    act(() => buttonByText(t("chat-chatsearch.load_earlier"))!.click());
    expect(runs).toHaveLength(4);
    expect(runs[2].stopped()).toBe(true);
    expect(runs[3].stopped()).toBe(false);
    act(() => runs[3].onPage(4));

    mount({ hasMore: true, onLoadEarlier });
    await act(async () => {
      runs[2].reject(new Error("Stale run 3 failure"));
      await Promise.resolve();
    });
    expect(document.body.textContent).toContain(t("chat-chatsearch.loading_pages", { count: 4 }));
    expect(document.body.textContent).not.toContain(t("chat-chatview.search_load_failed"));
    expect(buttonByText(t("chat-chatsearch.stop_loading"))).toBeDefined();

    await act(async () => {
      runs[3].resolve("Run 4 recovered");
      await Promise.resolve();
    });
    expect(document.body.textContent).toContain("Run 4 recovered");
  });
});

describe("ChatSearch default compatibility", () => {
  it("keeps the 200 ms debounce and default searchChat matcher", async () => {
    vi.useFakeTimers();
    mocks.searchChat.mockImplementation((_items: Item[], query: string) => query ? found : { hits: [], truncated: false });
    await mountBase(base());
    mocks.searchChat.mockClear();
    await mountBase(base({ query: "parser" }));
    act(() => vi.advanceTimersByTime(199));
    expect(mocks.searchChat).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(1));
    expect(mocks.searchChat).toHaveBeenCalledWith(items, "parser");
    expect(document.body.textContent).toContain("the parser is fixed");
  });

  it("debounces query changes and runs default searchChat when controlled search props are absent", () => {
    vi.useFakeTimers();
    mount({ query: "", items: defaultItems, onLoadEarlier: async () => null });
    expect(mocks.searchChat).toHaveBeenCalledWith(defaultItems, "");

    mount({ query: "parser", items: defaultItems, onLoadEarlier: async () => null });
    expect(document.querySelectorAll("[data-search-result]")).toHaveLength(0);

    act(() => vi.advanceTimersByTime(200));
    expect(mocks.searchChat).toHaveBeenCalledWith(defaultItems, "parser");
    expect(document.querySelectorAll("[data-search-result]")).toHaveLength(2);

    act(() => {
      searchInput().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    });
    expect(onPick).toHaveBeenCalledWith(expect.objectContaining({ itemId: "tr#1", category: "tool_output" }));
  });

  it("keeps the earlier-load page and stop lifecycle", async () => {
    mocks.searchChat.mockReturnValue(found);
    let finish: (outcome: LoadOutcome) => void = () => {};
    const onLoadEarlier = vi.fn((onPage: (n: number) => void, _stopped: () => boolean) => {
      onPage(2);
      return new Promise<LoadOutcome>((resolve) => {
        finish = resolve;
      });
    });
    await mountBase(base({ query: "parser", hasMore: true, onLoadEarlier }));
    await act(async () => button(t("chat-chatsearch.load_earlier")).click());
    expect(document.body.textContent).toContain(t("chat-chatsearch.loading_pages", { count: 2 }));
    act(() => button(t("chat-chatsearch.stop_loading")).click());
    expect(onLoadEarlier.mock.calls[0][1]()).toBe(true);
    await act(async () => finish("Stopped by the reader"));
    expect(document.body.textContent).toContain("Stopped by the reader");
  });

  it("keeps the default async onLoadEarlier lifecycle, stop flag and failure notice", async () => {
    let finish!: (outcome: string | null) => void;
    let checkStopped!: () => boolean;
    const onLoadEarlier = vi.fn((onPage: (n: number) => void, stopped: () => boolean) => {
      checkStopped = stopped;
      onPage(2);
      return new Promise<string | null>((resolve) => { finish = resolve; });
    });

    mount({ items: defaultItems, hasMore: true, onLoadEarlier });
    act(() => buttonByText(t("chat-chatsearch.load_earlier"))!.click());
    expect(document.body.textContent).toContain(t("chat-chatsearch.loading_pages", { count: 2 }));
    expect(checkStopped()).toBe(false);

    act(() => buttonByText(t("chat-chatsearch.stop_loading"))!.click());
    expect(checkStopped()).toBe(true);

    await act(async () => {
      finish("Stopped at page 2");
      await Promise.resolve();
    });
    expect(document.body.textContent).toContain("Stopped at page 2");

    const failingLoad = vi.fn(async () => {
      throw new Error("Synthetic failure");
    });
    mount({ items: defaultItems, hasMore: true, onLoadEarlier: failingLoad });
    await act(async () => buttonByText(t("chat-chatsearch.load_earlier"))!.click());
    expect(document.body.textContent).toContain(t("chat-chatview.search_load_failed"));
  });
});
