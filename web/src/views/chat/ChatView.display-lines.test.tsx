// @vitest-environment jsdom
// Memoizing loadedFrom and displayLines in ChatView keeps streaming frames and scroll-state flips
// from walking the full historical feed on every render of a long Assistant conversation.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { FeedItem } from "../../lib/feed";
import type { Item, Session } from "../../lib/types";
import { t } from "../../i18n";
import type { Transcript } from "./useTranscript";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
  (g.CSS as { escape?: (s: string) => string } | undefined) ?? (g.CSS = {});
  (g.CSS as { escape?: (s: string) => string }).escape ??= (s: string) => s;
});

const transcriptState = vi.hoisted(() => {
  const initial: Transcript = {
    items: [],
    loading: false,
    firstRead: true,
    error: "",
    hasMore: true,
    loadingEarlier: false,
    streaming: "",
    streamEnded: false,
    notLive: false,
    loadEarlier: async () => undefined,
    expandFold: async () => true,
    reload: () => {},
  };
  let current: Transcript = initial;
  const listeners = new Set<() => void>();
  return {
    get: () => current,
    subscribe: (l: () => void) => {
      listeners.add(l);
      return () => { listeners.delete(l); };
    },
    reset(patch: Partial<Transcript> = {}) {
      current = { ...initial, ...patch };
    },
    update(patch: Partial<Transcript>) {
      current = { ...current, ...patch };
      listeners.forEach((l) => l());
    },
    clearListeners() {
      listeners.clear();
    },
  };
});

const feedSpies = vi.hoisted(() => ({
  displayLines: vi.fn(),
  loadedFrom: vi.fn(),
}));
const feedState = vi.hoisted(() => ({
  items: [] as FeedItem[],
  feeds: {} as Record<string, FeedItem[]>,
  listeners: new Set<() => void>(),
  set(next: FeedItem[]) {
    feedState.items = next;
    feedState.feeds = { ...feedState.feeds, main: next };
    feedState.listeners.forEach((l) => l());
  },
  setFor(conversation: string, next: FeedItem[]) {
    feedState.feeds = { ...feedState.feeds, [conversation]: next };
    feedState.listeners.forEach((l) => l());
  },
}));

vi.mock("./useTranscript", async () => {
  const { useSyncExternalStore } = await import("react");
  return {
    useTranscript: () => useSyncExternalStore(transcriptState.subscribe, transcriptState.get, transcriptState.get),
  };
});
vi.mock("../../lib/panel", async (orig) => ({ ...(await orig<typeof import("../../lib/panel")>()), usePanel: () => null }));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }) }));
vi.mock("../../lib/feed", async (orig) => {
  const real = await orig<typeof import("../../lib/feed")>();
  const { useSyncExternalStore } = await import("react");
  return {
    ...real,
    loadFeed: async () => {},
    loadToday: async () => {},
    useFeed: <T,>(select: (s: ReturnType<typeof real.getFeedState>) => T): T =>
      useSyncExternalStore(
        (l) => {
          feedState.listeners.add(l);
          return () => feedState.listeners.delete(l);
        },
        () => select({ ...real.getFeedState(), feeds: { main: feedState.items, ...feedState.feeds } }),
        () => select({ ...real.getFeedState(), feeds: { main: feedState.items, ...feedState.feeds } }),
      ),
    displayLines: (...args: Parameters<typeof real.displayLines>) => {
      feedSpies.displayLines(...args);
      return real.displayLines(...args);
    },
    loadedFrom: (...args: Parameters<typeof real.loadedFrom>) => {
      feedSpies.loadedFrom(...args);
      return real.loadedFrom(...args);
    },
  };
});

const { ChatView } = await import("./ChatView");
const { getState, reset, upsertSession } = await import("../../lib/store");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

const oldFeedItem: FeedItem = {
  id: "old-notice",
  conversation: "main",
  at: "2026-11-10T09:00:00Z",
  kind: "notice",
  body: { text: "Older historical notice" },
};
const currentFeedItem: FeedItem = {
  id: "current-notice",
  conversation: "main",
  at: "2026-11-15T10:05:00Z",
  kind: "notice",
  body: { text: "Current page notice" },
};
const baseItems: Item[] = [
  { id: "u1", seq: 100, role: "user", kind: "text", text: "Hello", ts: "2026-11-15T10:00:00Z" },
];

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-11-15T12:00:00Z"));
  localStorage.clear();
  feedSpies.displayLines.mockClear();
  feedSpies.loadedFrom.mockClear();
  transcriptState.reset({ items: baseItems, hasMore: true, streaming: "", streamEnded: false });
  feedState.items = [oldFeedItem, currentFeedItem];
  feedState.feeds = { main: [oldFeedItem, currentFeedItem] };
  Object.assign(getState(), {
    loaded: true,
    assistant: { machine_id: "m", main: "main", conversations: [] },
  });
  vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } })));
  upsertSession({ machine_id: "m", session_id: "main", title: "Persona", status: "working", state: "working", assistant: true } as Session);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  transcriptState.clearListeners();
  feedState.listeners.clear();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("ChatView assistant displayLines memoization", () => {
  it("skips displayLines and loadedFrom during streaming and scroll-state re-renders, and recomputes when lines, feed, or hasMore change", async () => {
    await act(async () => {
      root.render(<ChatView mid="m" sid="main" assistant />);
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(host.textContent).toContain("Current page notice");
    expect(host.textContent).not.toContain("Older historical notice");
    expect(feedSpies.displayLines).toHaveBeenCalledTimes(1);
    expect(feedSpies.loadedFrom).toHaveBeenCalledTimes(1);

    feedSpies.displayLines.mockClear();
    feedSpies.loadedFrom.mockClear();

    // Streaming deltas re-render ChatView repeatedly without changing lines, feed, or tr.items.
    for (let i = 1; i <= 10; i++) {
      await act(async () => {
        transcriptState.update({ streaming: `Streaming reply step ${i}` });
        await vi.advanceTimersByTimeAsync(100);
      });
      expect(host.textContent).toContain(`Streaming reply step ${i}`);
    }
    expect(feedSpies.displayLines).not.toHaveBeenCalled();
    expect(feedSpies.loadedFrom).not.toHaveBeenCalled();

    // Leaving the bottom flips `away` state (rendering the JumpPill) without recomputing displayLines.
    const scroller = host.querySelector<HTMLDivElement>("[data-chat-scroll]")!;
    Object.defineProperty(scroller, "scrollHeight", { value: 2000, configurable: true });
    Object.defineProperty(scroller, "clientHeight", { value: 500, configurable: true });
    await act(async () => {
      scroller.dispatchEvent(new WheelEvent("wheel", { deltaY: -120, bubbles: true }));
    });
    expect(host.querySelector("[data-jump-pill]")).not.toBeNull();
    expect(feedSpies.displayLines).not.toHaveBeenCalled();
    expect(feedSpies.loadedFrom).not.toHaveBeenCalled();

    // Appending a new transcript item updates lines and recomputes displayLines once.
    await act(async () => {
      transcriptState.update({
        streaming: "",
        items: [...baseItems, { id: "a1", seq: 101, role: "assistant", kind: "text", text: "Final reply", ts: "2026-11-15T10:02:00Z" }],
      });
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(host.textContent).toContain("Final reply");
    expect(feedSpies.displayLines).toHaveBeenCalledTimes(1);

    // Pushing a new feed item recomputes displayLines and renders the new card.
    feedSpies.displayLines.mockClear();
    const newerNotice: FeedItem = { id: "newer-notice", conversation: "main", at: "2026-11-15T10:10:00Z", kind: "notice", body: { text: "Fresh live notice" } };
    await act(async () => {
      feedState.set([oldFeedItem, currentFeedItem, newerNotice]);
    });
    expect(host.textContent).toContain("Fresh live notice");
    expect(feedSpies.displayLines).toHaveBeenCalledTimes(1);

    // Exhausting history (hasMore -> false) drops loadedFrom gating and reveals older feed items.
    feedSpies.displayLines.mockClear();
    await act(async () => {
      transcriptState.update({ hasMore: false });
    });
    expect(host.textContent).toContain("Older historical notice");
    expect(feedSpies.displayLines).toHaveBeenCalledTimes(1);
  });

  it("holds unanchored timelineFeed task cards while an Assistant conversation is loading, then interleaves by historyFrom once items arrive", async () => {
    const topicFeed: FeedItem[] = Array.from({ length: 24 }, (_, idx) => ({
      id: `topic-feed-${idx}`,
      conversation: "topic-1",
      at: new Date(Date.parse("2026-11-15T08:00:00Z") + idx * 10 * 60_000).toISOString(),
      kind: "notice",
      body: { text: `Historical task card #${idx}` },
    }));
    upsertSession({ machine_id: "m", session_id: "topic-1", title: "Topic", status: "idle", state: "idle", assistant: true } as Session);
    feedState.setFor("topic-1", topicFeed);
    transcriptState.reset({ items: [], loading: true, firstRead: false, hasMore: false, streaming: "", streamEnded: false });
    feedSpies.displayLines.mockClear();

    await act(async () => {
      root.render(<ChatView mid="m" sid="topic-1" assistant />);
      await vi.advanceTimersByTimeAsync(100);
    });

    // While /items has not returned, <Loading /> is shown and none of the 24 unanchored historical cards render.
    expect(host.textContent).toContain(t("chat-chatview.loading_chat"));
    expect(host.textContent).not.toContain("Historical task card #0");
    expect(host.textContent).not.toContain("Historical task card #23");
    expect(feedSpies.displayLines).not.toHaveBeenCalled();

    // Once /items arrives with hasMore=true anchored at 11:00Z, only cards >= 11:00Z (#18..#23) interleave.
    const arrived: Item[] = [
      { id: "u-topic-1", seq: 200, role: "user", kind: "text", text: "Topic prompt", ts: "2026-11-15T11:00:00Z" },
      { id: "a-topic-1", seq: 201, role: "assistant", kind: "text", text: "Topic reply", ts: "2026-11-15T11:25:00Z" },
    ];
    await act(async () => {
      transcriptState.update({ items: arrived, loading: false, firstRead: true, hasMore: true });
      await vi.advanceTimersByTimeAsync(100);
    });

    expect(host.textContent).not.toContain(t("chat-chatview.loading_chat"));
    expect(host.textContent).toContain("Topic prompt");
    expect(host.textContent).toContain("Topic reply");
    expect(host.textContent).not.toContain("Historical task card #0");
    expect(host.textContent).not.toContain("Historical task card #17");
    expect(host.textContent).toContain("Historical task card #18");
    expect(host.textContent).toContain("Historical task card #23");

    // Truly empty loaded conversation (!loading && !items.length) keeps rendering its feed items.
    await act(async () => {
      transcriptState.update({ items: [], loading: false, firstRead: true, hasMore: false });
      await vi.advanceTimersByTimeAsync(100);
    });
    expect(host.textContent).toContain("Historical task card #0");
    expect(host.textContent).toContain("Historical task card #23");
  });
});
