// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, invalidateInFlightGets, sessionPath } from "../../lib/api";
import { feedCacheScope, readMemoryPage } from "../../lib/feed-cache";
import type { listenSession, ItemsEvent } from "../../lib/store";
import type { Item, ItemsPage } from "../../lib/types";
import { resetStalePullsForTests, useTranscript, type Transcript } from "./useTranscript";

interface Pending {
  url: string;
  settled: boolean;
  resolve: (response: Response) => void;
  reject: (reason: unknown) => void;
}

type Listener = Parameters<typeof listenSession>[2];
const calls: Pending[] = [];
const looseReads: Promise<unknown>[] = [];
let listener: Listener | undefined;
let root: Root;
let shown: Transcript;
const shownViews = new Map<string, Transcript>();

vi.mock("../../lib/store", async (original) => ({
  ...(await original<typeof import("../../lib/store")>()),
  listenSession: (_mid: string, _sid: string, next: Listener) => {
    listener = next;
    return () => { listener = undefined; };
  },
  send: () => {},
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item = (id: string, seq: number): Item => ({ id, seq, role: "assistant", kind: "text", text: id });
const response = (page: ItemsPage) => new Response(JSON.stringify(page), { status: 200, headers: { "Content-Type": "application/json" } });
const pagePath = (sid: string) => `${sessionPath("m", sid)}/items?turns=2&fold_work=1&lazy_images=1`;
const pullPath = (sid: string) => `${sessionPath("m", sid)}/items?after_seq=1&lazy_images=1`;
const matching = (path: string) => calls.filter((call) => {
  const url = new URL(call.url, "http://hub.example.com");
  return `${url.pathname}${url.search}` === `/api/v1${path}`;
});

function Probe({ sid, view }: { sid: string; view?: string }) {
  shown = useTranscript("m", sid, { readOnly: true });
  if (view) shownViews.set(view, shown);
  return null;
}

async function answer(call: Pending, page: ItemsPage) {
  call.settled = true;
  await act(async () => { call.resolve(response(page)); });
}

beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear();
  localStorage.setItem("persona.token", "test-credential");
  resetStalePullsForTests();
  calls.length = 0;
  looseReads.length = 0;
  listener = undefined;
  shownViews.clear();
  vi.stubGlobal("fetch", vi.fn((url: string, init?: RequestInit) => {
    let resolve!: (value: Response) => void;
    let reject!: (reason: unknown) => void;
    const promise = new Promise<Response>((res, rej) => { resolve = res; reject = rej; });
    const pending: Pending = { url, settled: false, resolve, reject };
    calls.push(pending);
    init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
    return promise;
  }));
  root = createRoot(document.createElement("div"));
});

afterEach(async () => {
  act(() => root.unmount());
  for (const call of calls.splice(0)) {
    if (!call.settled) call.resolve(response({ items: [] }));
  }
  await Promise.allSettled(looseReads);
  await act(async () => {});
  invalidateInFlightGets();
  localStorage.clear();
  resetStalePullsForTests();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("useTranscript freshness-sensitive reads", () => {
  it("reloads the newest page instead of joining an older identical GET", async () => {
    act(() => root.render(<Probe sid="reload" />));
    await answer(matching(pagePath("reload"))[0]!, { items: [item("initial", 1)], history: "v1", to_seq: 1 });

    const older = api<ItemsPage>(pagePath("reload"));
    looseReads.push(older);
    const oldCall = matching(pagePath("reload"))[1]!;
    act(() => shown.reload());

    const requests = matching(pagePath("reload"));
    expect(requests).toHaveLength(3);
    await answer(requests[2]!, { items: [item("initial", 1), item("fresh", 2)], history: "v1", to_seq: 2 });
    expect(shown.items.map((row) => row.id)).toEqual(["initial", "fresh"]);

    await answer(oldCall, { items: [item("stale", 1)], history: "v1", to_seq: 1 });
    await expect(older).resolves.toMatchObject({ items: [{ id: "stale" }] });
    expect(shown.items.map((row) => row.id)).toEqual(["initial", "fresh"]);
  });

  it("pulls missed items instead of joining an older identical GET", async () => {
    act(() => root.render(<Probe sid="pull" />));
    await answer(matching(pagePath("pull"))[0]!, { items: [item("initial", 1)], history: "v1", to_seq: 1 });

    const older = api<ItemsPage>(pullPath("pull"));
    looseReads.push(older);
    const oldCall = matching(pullPath("pull"))[0]!;
    act(() => listener!.items!({ machine_id: "m", session_id: "pull", items: [], history: "v1", more_after: true } as ItemsEvent));
    await act(async () => { await vi.advanceTimersByTimeAsync(1500); });

    const requests = matching(pullPath("pull"));
    expect(requests).toHaveLength(2);
    await answer(requests[1]!, { items: [item("missed", 2)], history: "v1", to_seq: 2 });
    expect(shown.items.map((row) => row.id)).toEqual(["initial", "missed"]);

    await answer(oldCall, { items: [item("stale", 2)], history: "v1", to_seq: 2 });
    await expect(older).resolves.toMatchObject({ items: [{ id: "stale" }] });
    expect(shown.items.map((row) => row.id)).toEqual(["initial", "missed"]);
  });

  it("keeps every view and the shared cache on the newest readPage response", async () => {
    act(() => root.render(<><Probe sid="shared" view="a" /><Probe sid="shared" view="b" /></>));
    const requests = matching(pagePath("shared"));
    expect(requests).toHaveLength(2);

    await answer(requests[1]!, { items: [item("fresh", 2)], history: "v1", to_seq: 2 });
    await answer(requests[0]!, { items: [item("stale", 1)], history: "v1", to_seq: 1 });

    expect(shownViews.get("a")?.items.map((row) => row.id)).toEqual(["fresh"]);
    expect(shownViews.get("b")?.items.map((row) => row.id)).toEqual(["fresh"]);
    expect(readMemoryPage(feedCacheScope(), "m", "shared")?.items.map((row) => row.id)).toEqual(["fresh"]);
  });
});
