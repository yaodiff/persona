// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { followHinted, HINT_WINDOW_MS } from "./hinted-read";

const hub = vi.hoisted(() => ({ handlers: new Set<{ event: string; fn: () => void }>() }));
vi.mock("./api", async (original) => ({ ...await original<typeof import("./api")>(), api: vi.fn() }));
vi.mock("./store", () => ({
  onHubEvent: (event: string, fn: () => void) => {
    const entry = { event, fn };
    hub.handlers.add(entry);
    return () => { hub.handlers.delete(entry); };
  },
}));

/** The hub's state, moved by each change; a read answers with it as it is when the read arrives. */
let version = 0;
const signals: (AbortSignal | undefined)[] = [];
const change = (event = "sessions_list") => {
  version++;
  for (const entry of [...hub.handlers]) if (entry.event === event) entry.fn();
};
const reads = () => vi.mocked(api).mock.calls.length;
const wait = (ms: number) => vi.advanceTimersByTimeAsync(ms);
function setShown(shown: boolean) {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => shown ? "visible" : "hidden" });
  document.dispatchEvent(new Event("visibilitychange"));
}

const offs: (() => void)[] = [];
const follow = (path = "/sessions?counts=1", event = "sessions_list") => {
  const values: number[] = [];
  const errors: unknown[] = [];
  offs.push(followHinted<{ version: number }>(event, path, (v) => values.push(v.version), (e) => errors.push(e)));
  return { values, errors, off: offs.at(-1)! };
};

beforeEach(() => {
  vi.useFakeTimers();
  version = 0;
  signals.length = 0;
  vi.mocked(api).mockReset().mockImplementation((_path, init) => {
    signals.push(init?.signal ?? undefined);
    return new Promise((resolve) => setTimeout(() => resolve({ version }), 50)) as ReturnType<typeof api>;
  });
});

afterEach(() => {
  offs.splice(0).forEach((off) => off());
  setShown(true);
  hub.handlers.clear();
  vi.useRealTimers();
});

describe("followHinted", () => {
  it("reads once for every follower of a path, and each gets the answer", async () => {
    const a = follow(), b = follow(), c = follow();
    await wait(100);
    expect(reads()).toBe(1);
    expect([a.values, b.values, c.values]).toEqual([[0], [0], [0]]);
    // another path is another read
    follow("/sessions?counts=1&state=working");
    await wait(100);
    expect(reads()).toBe(2);
  });

  it("a hint after a quiet stretch reads at once; a burst's hints share one read per window", async () => {
    follow(); follow();
    await wait(HINT_WINDOW_MS + 100);
    const quiet = reads();
    change();
    await wait(0);
    expect(reads()).toBe(quiet + 1); // at once: feedback first
    for (let i = 0; i < 30; i++) { change(); await wait(100); }
    await wait(2 * HINT_WINDOW_MS);
    // 3 s of hints: about one read per 2 s window, plus the one after the last hint
    expect(reads() - quiet).toBeLessThanOrEqual(4);
  });

  it("is current after a burst: the last change is read, also one that came while a read was out", async () => {
    const a = follow(), b = follow();
    await wait(10); // the first read is out
    for (let i = 0; i < 12; i++) { change(); await wait(37); }
    await wait(2 * HINT_WINDOW_MS);
    expect(a.values.at(-1)).toBe(version);
    expect(b.values.at(-1)).toBe(version);
    // and nothing more is read while nothing changes
    const settled = reads();
    await wait(10 * HINT_WINDOW_MS);
    expect(reads()).toBe(settled);
  });

  it("reads once more when the only hint lands while its read is out", async () => {
    const replies: (() => void)[] = [];
    vi.mocked(api).mockImplementation((_path, init) => {
      signals.push(init?.signal ?? undefined);
      const seen = version;
      return new Promise((resolve) => {
        replies.push(() => resolve({ version: seen }));
      }) as ReturnType<typeof api>;
    });
    const a = follow(), b = follow();
    expect(reads()).toBe(1);
    change();
    replies.shift()!();
    await wait(0);
    expect([a.values, b.values]).toEqual([[0], [0]]);
    await wait(HINT_WINDOW_MS);
    expect(reads()).toBe(2);
    replies.shift()!();
    await wait(0);
    expect([a.values, b.values]).toEqual([[0, version], [0, version]]);
    await wait(10 * HINT_WINDOW_MS);
    expect(reads()).toBe(2);
  });

  it("a follower that joins gets the last answer at once and shares the next read", async () => {
    const a = follow();
    await wait(100);
    const b = follow();
    expect(b.values).toEqual([0]);
    change();
    await wait(HINT_WINDOW_MS + 100);
    expect(reads()).toBe(2);
    expect(a.values.at(-1)).toBe(1);
    expect(b.values.at(-1)).toBe(1);
  });

  it("holds the hints of a hidden page and reads once when it is shown again", async () => {
    const a = follow();
    await wait(100);
    setShown(false);
    for (let i = 0; i < 20; i++) { change(); await wait(100); }
    expect(reads()).toBe(1);
    setShown(true);
    await wait(100);
    expect(reads()).toBe(2);
    expect(a.values.at(-1)).toBe(version);
  });

  it("tells every follower of a failed read, and reads again on the next hint", async () => {
    vi.mocked(api).mockImplementationOnce(() => Promise.reject(new Error("offline")));
    const a = follow(), b = follow();
    await wait(100);
    expect(a.errors).toHaveLength(1);
    expect(b.errors).toHaveLength(1);
    change();
    await wait(HINT_WINDOW_MS + 100);
    expect(a.values.at(-1)).toBe(1);
  });

  it("the last follower to leave aborts the read out and stops following", async () => {
    const a = follow(), b = follow();
    await wait(10);
    a.off();
    expect(signals[0]?.aborted).toBe(false);
    b.off();
    expect(signals[0]?.aborted).toBe(true);
    change();
    await wait(HINT_WINDOW_MS + 100);
    expect(reads()).toBe(1);
    expect(hub.handlers.size).toBe(0);
  });
});
