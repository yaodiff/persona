// @vitest-environment jsdom
// A burst of `sessions_list` hints from a busy fleet (#15910): the paged lists that read their scope's counts on their
// own (the sidebar's, drawn as groups with hidden botmux sessions left out) share one read per hint window, however
// many of them follow the same counts, and still read the burst's last change.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../../lib/api";
import { HINT_WINDOW_MS } from "../../lib/hinted-read";
import { SessionList, type ListCounts } from "../../lib/session-list";
import { PagedSessionList, type PagedGrouping } from "./PagedSessionList";

vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
});
const hub = vi.hoisted(() => ({ handlers: new Set<{ event: string; fn: (msg?: unknown) => void }>() }));
vi.mock("../../lib/api", async (original) => ({ ...await original<typeof import("../../lib/api")>(), api: vi.fn() }));
vi.mock("../../lib/store", () => ({
  onHubEvent: (event: string, fn: () => void) => {
    const entry = { event, fn };
    hub.handlers.add(entry);
    return () => { hub.handlers.delete(entry); };
  },
  // the grouped list reads the computers' online state (GroupedSessions, #13522): none here
  useStore: (select: (state: { machines: [] }) => unknown) => select({ machines: [] }),
}));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type Row = { id: string; machine: string };
const identity = (row: Row) => row.id;
const compare = (a: Row, b: Row) => a.id.localeCompare(b.id);
const EXCLUDE = { exclude_pane_source: "feishu" };
const grouping: PagedGrouping<Row> = { dimension: "machine", bar: null, label: "Computer", quick: { select: {}, exclude: EXCLUDE, bar: () => null } };

/** The hub's state: each change moves the revision and the number of agents working. */
let revision = 1;
const countsNow = (): ListCounts => ({ total: 3, state: { working: revision, idle: 3 }, machine: { m: 3 }, project: {}, group: {}, revision: `r${revision}` });

let host: HTMLDivElement;
let root: Root;
const params = (path: string) => new URL(path, "https://example.com").searchParams;
/** The scope's own counts reads: hidden botmux left out, no group, no page. */
const facetReads = () => vi.mocked(api).mock.calls.filter(([path]) => {
  const p = params(path);
  return p.get("counts") === "1" && p.get("exclude_pane_source") === "feishu" && !p.has("machine_id") && !p.has("limit");
}).length;
const hint = () => {
  revision++;
  for (const entry of [...hub.handlers]) if (entry.event === "sessions_list") entry.fn({ type: "sessions_list", revision: `r${revision}` });
};

beforeEach(() => {
  vi.useFakeTimers();
  revision = 1;
  hub.handlers.clear();
  vi.mocked(api).mockReset().mockImplementation((path) => {
    const p = params(path);
    const answer = p.has("counts") ? countsNow()
      : { sessions: [{ id: "a", machine: "m" }], remove: [], added: [], has_more: false, total: 1, revision: `r${revision}` };
    // the hub answers after a moment, as a real one does
    return new Promise((resolve) => setTimeout(() => resolve(answer), 50)) as ReturnType<typeof api>;
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
});

async function showLists(n: number) {
  const list = new SessionList<Row>("/sessions", identity, compare);
  await act(async () => root.render(<>{Array.from({ length: n }, (_, i) => <PagedSessionList key={i} root={list} path="/sessions" identity={identity} compare={compare}
    event="sessions_list" grouping={grouping} renderRow={(row) => <span>{row.id}</span>} />)}</>));
  await act(async () => { await vi.advanceTimersByTimeAsync(3 * HINT_WINDOW_MS); });
}

/** `m` hints `gap` ms apart, then quiet until every read is done. */
async function burst(m: number, gap: number) {
  for (let i = 0; i < m; i++) {
    act(() => hint());
    await act(async () => { await vi.advanceTimersByTimeAsync(gap); });
  }
  await act(async () => { await vi.advanceTimersByTimeAsync(3 * HINT_WINDOW_MS); });
}

describe("paged lists in a hint burst (#15910)", () => {
  it("N lists and M hints make one counts read per window, not one per hint per list", async () => {
    const N = 3, M = 40, GAP = 100;
    await showLists(N);
    const before = facetReads();
    expect(before).toBe(1); // the N lists' first read is one read
    await burst(M, GAP);
    const during = facetReads() - before;
    // a read per window of the burst, plus the one after its last hint; before the fix: N × M = 120
    const bound = Math.ceil((M * GAP) / HINT_WINDOW_MS) + 1;
    expect(during).toBeGreaterThan(0);
    expect(during).toBeLessThanOrEqual(bound);
  });

  it("reads the burst's last change: the counts shown are the hub's after it", async () => {
    await showLists(2);
    await burst(25, 30);
    const shown = vi.mocked(api).mock.results.length;
    expect(shown).toBeGreaterThan(0);
    // the last counts read answered with the hub's state after the last hint
    const last = vi.mocked(api).mock.calls.map(([path], i) => ({ path, i })).filter(({ path }) => {
      const p = params(path);
      return p.get("counts") === "1" && p.get("exclude_pane_source") === "feishu" && !p.has("machine_id") && !p.has("limit");
    }).at(-1)!;
    const answer = await (vi.mocked(api).mock.results[last.i].value as Promise<ListCounts>);
    expect(answer.revision).toBe(`r${revision}`);
    expect(answer.state.working).toBe(revision);
  });
});
