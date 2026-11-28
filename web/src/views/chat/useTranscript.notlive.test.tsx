// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ItemsEvent, listenSession } from "../../lib/store";
import type { Item, ItemsPage } from "../../lib/types";
import { STALE_PULL_EVERY_MS, STALE_QUIET_MS, STALE_RECOVERY_WINDOW_MS } from "../../lib/stale-page";

type Listener = Parameters<typeof listenSession>[2];
let listener: Listener | undefined;
const sent: Record<string, unknown>[] = [];
const calls: string[] = [];

type ApiHandler = (path: string) => Promise<ItemsPage>;
let apiHandler: ApiHandler;

vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: vi.fn((path: string) => {
    calls.push(path);
    return apiHandler(path);
  }),
}));
vi.mock("../../lib/store", async (orig) => ({
  ...(await orig<typeof import("../../lib/store")>()),
  send: vi.fn((msg: Record<string, unknown>) => {
    sent.push(msg);
  }),
  listenSession: (_mid: string, _sid: string, l: Listener) => {
    listener = l;
    return () => {
      listener = undefined;
    };
  },
}));

const { useTranscript, resetStalePullsForTests } = await import("./useTranscript");
import type { Transcript } from "./useTranscript";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function item(seq: number, ts = "2026-11-06T00:00:00Z"): Item {
  return { id: `i${seq}`, seq, role: "assistant", kind: "text", text: `row ${seq}`, ts } as Item;
}

let tr: Transcript;
function Probe({ sid, rev, updatedAt }: { sid: string; rev?: number; updatedAt?: string }) {
  tr = useTranscript("m1", sid, { rev, updatedAt });
  return null;
}

let root: Root;
let visibility: DocumentVisibilityState = "visible";

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-11-06T00:01:00Z"));
  calls.length = 0;
  sent.length = 0;
  listener = undefined;
  visibility = "visible";
  resetStalePullsForTests();
  vi.spyOn(document, "visibilityState", "get").mockImplementation(() => visibility);
  root = createRoot(document.createElement("div"));
});

afterEach(() => {
  act(() => root.unmount());
  vi.restoreAllMocks();
  vi.useRealTimers();
});

const wait = async (ms: number) => {
  for (let t = 0; t < ms; t += 1000) {
    await act(async () => void vi.advanceTimersByTime(1000));
  }
};

describe("useTranscript notLive recovery and reload", () => {
  it("triggers notLive on repeated missed pushes, and reload() resets state, deduplicates clicks, and force-resubscribes", async () => {
    let nextSeq = 100;
    let holdReload: ((page: ItemsPage) => void) | undefined;
    apiHandler = async (path) => {
      if (path.includes("after_seq=")) {
        nextSeq++;
        return { items: [item(nextSeq)], to_seq: nextSeq } as ItemsPage;
      }
      if (holdReload !== undefined) {
        return new Promise<ItemsPage>((resolve) => {
          holdReload = resolve;
        });
      }
      return { items: [item(100)], to_seq: 100 } as ItemsPage;
    };

    act(() => root.render(<Probe sid="dead-stream" rev={100} />));
    await act(async () => {});
    expect(tr.notLive).toBe(false);
    expect(sent.filter((m) => m.type === "subscribe")).toHaveLength(1);

    // First missed push -> recovered = 1 (silent, still not showing notLive).
    act(() => root.render(<Probe sid="dead-stream" rev={110} />));
    await wait(STALE_QUIET_MS + 2000);
    expect(tr.notLive).toBe(false);

    // Second missed push within window -> recovered = 2 -> notLive = true.
    act(() => root.render(<Probe sid="dead-stream" rev={120} />));
    await wait(STALE_PULL_EVERY_MS + 2000);
    expect(tr.notLive).toBe(true);

    // Trigger reload with a deferred response to verify loading state & concurrent click guard.
    sent.length = 0;
    const callsBeforeReload = calls.length;
    holdReload = () => {};
    act(() => {
      tr.reload();
      tr.reload(); // concurrent click must be ignored
    });
    expect(tr.reloading).toBe(true);
    expect(calls.length).toBe(callsBeforeReload + 1);

    // Complete reload.
    await act(async () => {
      holdReload!({ items: [item(100), item(101), item(102)], to_seq: 102 } as ItemsPage);
    });
    expect(tr.reloading).toBe(false);
    expect(tr.notLive).toBe(false);
    expect(tr.error).toBe("");

    // Force-resubscribe sent unsubscribe before subscribe so the hub re-establishes the node watch.
    const types = sent.map((m) => m.type);
    expect(types).toEqual(["unsubscribe", "subscribe"]);
    expect(sent[1]).toEqual(expect.objectContaining({ type: "subscribe", session_id: "dead-stream", since_seq: 102 }));

    // Tally was reset: one more recovery does not immediately flip notLive back on.
    act(() => root.render(<Probe sid="dead-stream" rev={130} />));
    await wait(STALE_PULL_EVERY_MS + 2000);
    expect(tr.notLive).toBe(false);
  });

  it("keeps notLive and surfaces error when reload() fails, and allows retrying", async () => {
    let nextSeq = 100;
    let failPage = false;
    apiHandler = async (path) => {
      if (path.includes("after_seq=")) {
        nextSeq++;
        return { items: [item(nextSeq)], to_seq: nextSeq } as ItemsPage;
      }
      if (failPage) throw new Error("network down");
      return { items: [item(100)], to_seq: 100 } as ItemsPage;
    };

    act(() => root.render(<Probe sid="reload-fail" rev={100} />));
    await act(async () => {});

    act(() => root.render(<Probe sid="reload-fail" rev={110} />));
    await wait(STALE_QUIET_MS + 2000);
    act(() => root.render(<Probe sid="reload-fail" rev={120} />));
    await wait(STALE_PULL_EVERY_MS + 2000);
    expect(tr.notLive).toBe(true);

    // Reload fails -> notLive stays true, error is set, reloading returns to false.
    failPage = true;
    await act(async () => {
      tr.reload();
      await Promise.resolve();
    });
    expect(tr.reloading).toBe(false);
    expect(tr.notLive).toBe(true);
    expect(tr.error).toBeTruthy();

    // Retry succeeds -> clears both notLive and error.
    failPage = false;
    await act(async () => {
      tr.reload();
      await Promise.resolve();
    });
    expect(tr.reloading).toBe(false);
    expect(tr.notLive).toBe(false);
    expect(tr.error).toBe("");
  });

  it("treats active WS deltas as live stream activity and prevents false stale pulls", async () => {
    let nextSeq = 100;
    apiHandler = async (path) => {
      if (path.includes("after_seq=")) {
        nextSeq++;
        return { items: [item(nextSeq)], to_seq: nextSeq } as ItemsPage;
      }
      return { items: [item(100)], to_seq: 100 } as ItemsPage;
    };

    act(() => root.render(<Probe sid="delta-live" rev={100} />));
    await act(async () => {});

    // rev moves, but WS is actively streaming deltas every 2s.
    act(() => root.render(<Probe sid="delta-live" rev={110} />));
    for (let i = 0; i < 4; i++) {
      act(() => listener!.delta!(`chunk ${i}`));
      await wait(2000);
    }
    expect(calls.filter((c) => c.includes("after_seq="))).toHaveLength(0);
    expect(tr.notLive).toBe(false);
  });

  it("waits STALE_QUIET_MS after tab becomes visible instead of pulling immediately on wake", async () => {
    let nextSeq = 100;
    apiHandler = async (path) => {
      if (path.includes("after_seq=")) {
        nextSeq++;
        return { items: [item(nextSeq)], to_seq: nextSeq } as ItemsPage;
      }
      return { items: [item(100)], to_seq: 100 } as ItemsPage;
    };

    act(() => root.render(<Probe sid="tab-wake" rev={100} />));
    await act(async () => {});

    // Tab goes hidden and rev moves while hidden.
    visibility = "hidden";
    act(() => document.dispatchEvent(new Event("visibilitychange")));
    act(() => root.render(<Probe sid="tab-wake" rev={110} />));
    await wait(10_000);
    expect(calls.filter((c) => c.includes("after_seq="))).toHaveLength(0);

    // Tab becomes visible -> must NOT pull immediately on wake.
    visibility = "visible";
    act(() => document.dispatchEvent(new Event("visibilitychange")));
    await act(async () => {});
    expect(calls.filter((c) => c.includes("after_seq="))).toHaveLength(0);

    // If a WS push arrives within the grace window, no pull is ever needed.
    await wait(2000);
    expect(calls.filter((c) => c.includes("after_seq="))).toHaveLength(0);
    act(() => listener!.items!({ items: [item(101)], from_seq: 100, to_seq: 101 } as ItemsEvent));
    await wait(2000);
    expect(calls.filter((c) => c.includes("after_seq="))).toHaveLength(0);
  });

  it("does not trip notLive for isolated single-item recoveries separated by more than STALE_RECOVERY_WINDOW_MS", async () => {
    let nextSeq = 100;
    apiHandler = async (path) => {
      if (path.includes("after_seq=")) {
        nextSeq++;
        return { items: [item(nextSeq)], to_seq: nextSeq } as ItemsPage;
      }
      return { items: [item(100)], to_seq: 100 } as ItemsPage;
    };

    act(() => root.render(<Probe sid="isolated-blips" rev={100} />));
    await act(async () => {});

    // First isolated recovery.
    act(() => root.render(<Probe sid="isolated-blips" rev={110} />));
    await wait(STALE_QUIET_MS + 2000);
    expect(calls.filter((c) => c.includes("after_seq="))).toHaveLength(1);
    expect(tr.notLive).toBe(false);

    // More than STALE_RECOVERY_WINDOW_MS later, another isolated recovery.
    await act(async () => void vi.advanceTimersByTime(STALE_RECOVERY_WINDOW_MS + 5000));
    act(() => root.render(<Probe sid="isolated-blips" rev={120} />));
    await wait(STALE_QUIET_MS + 2000);
    expect(calls.filter((c) => c.includes("after_seq="))).toHaveLength(2);
    expect(tr.notLive).toBe(false);
  });

  it("does not count items already delivered via WS push as missed pulls", async () => {
    apiHandler = async (path) => {
      if (path.includes("after_seq=")) {
        return { items: [item(102)], to_seq: 102 } as ItemsPage;
      }
      return { items: [item(100)], to_seq: 100 } as ItemsPage;
    };

    act(() => root.render(<Probe sid="already-held" rev={100} />));
    await act(async () => {});

    // Gapped WS push delivers i102 without advancing covered past 100, and rev moves.
    act(() => listener!.items!({ items: [item(102)], from_seq: 101, to_seq: 102 } as ItemsEvent));
    act(() => root.render(<Probe sid="already-held" rev={110} />));
    await wait(STALE_QUIET_MS + 2000);
    // Pull returned i102 which was already in latest -> must not increment recovered tally.
    act(() => root.render(<Probe sid="already-held" rev={120} />));
    await wait(STALE_PULL_EVERY_MS + 2000);
    expect(tr.notLive).toBe(false);
  });
});
