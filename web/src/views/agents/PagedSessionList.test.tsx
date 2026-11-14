// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { api } from "../../lib/api";
import { loadPages } from "../../lib/lazy-page";
import { SessionList, type ListCounts } from "../../lib/session-list";
import { STATE_LABEL } from "../../lib/state-labels";
import type { AgentState } from "../../lib/types";
import { SESSION_LIST_METRICS } from "../../ui/list-metrics";
import { PagedSessionList } from "./PagedSessionList";

vi.mock("../../lib/api", async (original) => ({ ...await original<typeof import("../../lib/api")>(), api: vi.fn() }));
const hub = vi.hoisted(() => ({ handlers: new Set<{ event: string; fn: () => void }>() }));
vi.mock("../../lib/store", () => ({
  onHubEvent: (event: string, fn: () => void) => {
    const entry = { event, fn };
    hub.handlers.add(entry);
    return () => { hub.handlers.delete(entry); };
  },
}));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;

type Row = { id: string };
const identity = (row: Row) => row.id;
const compare = (a: Row, b: Row) => a.id.localeCompare(b.id);
const initialState = { needs_you: 1, working: 2, unread: 3, idle: 4, offline: 5, unknown: 6 };
const counts = (state = initialState, revision = "initial"): ListCounts => ({
  total: Object.values(state).reduce((sum, n) => sum + n, 0), state, machine: {}, project: {}, group: {}, revision,
});
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

let host: HTMLDivElement;
let root: Root;
let list: SessionList<Row>;
const rootCounts = vi.fn<() => Promise<ListCounts>>();
const scopedCounts = vi.fn<() => Promise<ListCounts>>();
const chips = () => [...host.querySelectorAll<HTMLButtonElement>(`[aria-label="${t("sidebar.quick_filters")}"] [role="radio"]`)];
const labels = () => chips().map((chip) => chip.getAttribute("aria-label"));
const expected = (state: Record<string, number>) => [t("paged-agents.all"), ...Object.entries(state)
  .filter(([, n]) => n > 0).map(([key, n]) => `${STATE_LABEL[key as AgentState]} ${n}`)];

beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear(); // the chosen Browse by is kept in this browser
  rootCounts.mockReset().mockResolvedValue(counts());
  scopedCounts.mockReset().mockResolvedValue(counts());
  vi.mocked(api).mockImplementation((path) => {
    const params = new URL(path, "https://example.com").searchParams;
    return (params.has("counts") ? (params.has("q") ? scopedCounts() : rootCounts())
      : Promise.resolve({ sessions: [], remove: [], has_more: false, total: 21, revision: "rows" })) as ReturnType<typeof api>;
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  list = new SessionList<Row>("/sessions", identity, compare);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
});

async function show() {
  await list.load();
  await act(async () => root.render(<PagedSessionList root={list} path="/sessions" identity={identity} compare={compare}
    event="sessions_list" renderRow={(row) => <span>{row.id}</span>} />));
  expect(labels()).toEqual(expected(initialState));
}

/** A `sessions_list` hint as the store delivers it: to every listener, and the root list refreshes. */
async function hint() {
  await act(async () => {
    for (const entry of [...hub.handlers]) if (entry.event === "sessions_list") entry.fn();
    await list.load();
    await vi.advanceTimersByTimeAsync(0);
  });
}

async function search() {
  const input = host.querySelector<HTMLInputElement>(`input[aria-label="${t("paged-agents.search")}"]`)!;
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "review");
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () => { await vi.advanceTimersByTimeAsync(SESSION_LIST_METRICS.searchDelay); });
}

beforeAll(() => loadPages(), 60_000);

describe("paged state chips during count refreshes", () => {
  it("retains every chip and count while a new scope loads, then applies its successful counts", async () => {
    await show();
    const before = chips();
    const pending = deferred<ListCounts>();
    scopedCounts.mockReturnValue(pending.promise);
    await search();
    expect(scopedCounts).toHaveBeenCalled();
    expect(labels()).toEqual(expected(initialState));
    expect(chips()).toEqual(before);

    const next = { ...initialState, working: 0, unread: 7 };
    await act(async () => pending.resolve(counts(next, "scoped")));
    expect(labels()).toEqual(expected(next));
  });

  it("keeps scoped chips through root-count refreshes and failures until replacement counts arrive", async () => {
    await show();
    const scoped = { ...initialState, working: 8 };
    scopedCounts.mockResolvedValue(counts(scoped, "scoped"));
    await search();
    expect(labels()).toEqual(expected(scoped));
    const before = chips();

    const pending = deferred<ListCounts>();
    scopedCounts.mockReturnValue(pending.promise);
    rootCounts.mockResolvedValue(counts(initialState, "refresh"));
    await hint();
    expect(labels()).toEqual(expected(scoped));
    expect(chips()).toEqual(before);

    await act(async () => pending.reject(new Error("Counts unavailable")));
    expect(labels()).toEqual(expected(scoped));
    expect(host.querySelector('[role="alert"]')?.textContent).toContain("Counts unavailable");

    const next = { ...scoped, working: 9 };
    scopedCounts.mockResolvedValue(counts(next, "recovered"));
    rootCounts.mockResolvedValue(counts(initialState, "retry"));
    await hint();
    expect(labels()).toEqual(expected(next));
    expect(host.querySelector('[role="alert"]')).toBeNull();
  });

  it("reads a search's counts once per hint: the list's own read serves the facet chips and the scope", async () => {
    vi.mocked(api).mockClear();
    await show();
    await search();
    const scopedReads = () => vi.mocked(api).mock.calls.filter(([path]) => {
      const params = new URL(path, "https://example.com").searchParams;
      return params.get("counts") === "1" && params.get("q") === "review";
    }).length;
    expect(scopedReads()).toBe(1);
    const before = scopedReads();
    for (let i = 0; i < 3; i++) {
      rootCounts.mockResolvedValue(counts(initialState, `hint-${i}`));
      await hint();
    }
    expect(scopedReads() - before).toBe(3); // 3 hints × one shared read
    // A root refresh with no hint (a retry, a rebaseline) reads nothing scoped.
    rootCounts.mockResolvedValue(counts(initialState, "retry"));
    await act(async () => { await list.load(); });
    expect(scopedReads() - before).toBe(3);
  });

  it("shares one counts read between the facet chips and the scope while a state tab is open", async () => {
    await show();
    await search();
    const working = chips().find((chip) => chip.getAttribute("aria-label")?.startsWith(STATE_LABEL.working))!;
    await act(async () => { working.click(); await vi.advanceTimersByTimeAsync(0); });
    vi.mocked(api).mockClear();
    const reads = (state: boolean) => vi.mocked(api).mock.calls.filter(([path]) => {
      const params = new URL(path, "https://example.com").searchParams;
      return params.get("counts") === "1" && params.get("q") === "review" && params.has("state") === state;
    }).length;
    for (let i = 0; i < 3; i++) await hint();
    expect(reads(false)).toBe(3); // facets and scope: the same query, once per hint
    expect(reads(true)).toBe(3); // the state-filtered list's own counts
  });

  it("keeps unfiltered chips while the root rebaselines and accepts a successful empty count", async () => {
    await show();
    const before = chips();
    const pending = deferred<ListCounts>();
    rootCounts.mockReturnValue(pending.promise);
    let loading!: Promise<void>;
    await act(async () => { list.reset(); loading = list.load(); });
    expect(list.getSnapshot().counts).toBeNull();
    expect(labels()).toEqual(expected(initialState));
    expect(chips()).toEqual(before);

    await act(async () => {
      pending.resolve(counts({ needs_you: 0, working: 0, unread: 0, idle: 0, offline: 0, unknown: 0 }, "empty"));
      await loading;
    });
    expect(labels()).toEqual([t("paged-agents.all")]);
  });
});

describe("paged grouping controls", () => {
  it("browses by project through a segmented control and the design system's menu, with no native select", async () => {
    rootCounts.mockResolvedValue({ ...counts(), project: { "/work/alpha": 2, "/work/beta": 5 } });
    await show();
    expect(host.querySelector("select, datalist")).toBeNull();
    const project = [...host.querySelectorAll<HTMLButtonElement>(`[aria-label="${t("paged-agents.group_by")}"] [role="radio"]`)]
      .find((b) => b.textContent === t("paged-agents.project"))!;
    vi.mocked(api).mockClear();
    await act(async () => project.click());
    expect(project.getAttribute("aria-checked")).toBe("true");
    // the list is drawn by project at once, each under its folder's name. The counts it already has name the groups:
    // what is read is the groups' sessions that need the user (one does) and a page for each open section
    expect([...host.querySelectorAll<HTMLElement>("[data-session-group]")].map((g) => g.dataset.sessionGroup)).toEqual(["/work/beta", "/work/alpha"]);
    expect(host.querySelector("[data-virtual-sessions]")).toBeNull();
    expect(vi.mocked(api).mock.calls.map(([path]) => path).sort()).toEqual([
      "/sessions?counts=1&state=needs_you", "/sessions?project=%2Fwork%2Falpha&summary=1&limit=50", "/sessions?project=%2Fwork%2Fbeta&summary=1&limit=50",
    ]);

    const pick = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("paged-agents.group_filter")}"]`)!;
    expect(pick.textContent).toBe(t("paged-agents.all_projects"));
    await act(async () => pick.click());
    const rows = [...document.querySelectorAll<HTMLElement>('[role="menuitemradio"]')];
    // the busiest project first, by its folder name
    expect(rows.map((r) => r.textContent?.includes("beta"))).toEqual([false, true, false]);
    vi.mocked(api).mockClear();
    await act(async () => rows[1].click());
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    expect(vi.mocked(api).mock.calls.some(([path]) => new URL(path as string, "https://example.com").searchParams.get("project") === "/work/beta")).toBe(true);
    expect(host.querySelector<HTMLButtonElement>(`button[aria-label="${t("paged-agents.group_filter")}"]`)!.textContent).toBe("beta");
    // one project picked: its sessions are the one list again
    expect(host.querySelector("[data-session-group]")).toBeNull();
    expect(host.querySelector("[data-virtual-sessions]")).not.toBeNull();
  });

  it("opens a value menu as wide as its pill, and never narrower than a menu is elsewhere", async () => {
    rootCounts.mockResolvedValue({ ...counts(), project: { "/work/alpha": 2 } });
    await show();
    const project = [...host.querySelectorAll<HTMLButtonElement>(`[aria-label="${t("paged-agents.group_by")}"] [role="radio"]`)]
      .find((b) => b.textContent === t("paged-agents.project"))!;
    await act(async () => project.click());
    const pick = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("paged-agents.group_filter")}"]`)!;
    const measured = (width: number) => ({ width, height: 24, left: 8, right: 8 + width, top: 100, bottom: 124, x: 8, y: 100, toJSON: () => ({}) }) as DOMRect;
    const menuWidth = () => document.querySelector<HTMLElement>('[role="menu"]')!.style.width;

    // the sidebar as the user dragged it: the menu takes the pill's width
    pick.getBoundingClientRect = () => measured(372);
    await act(async () => pick.click());
    expect(menuWidth()).toBe("372px");
    await act(async () => pick.click());

    // a pill narrower than any menu keeps the menu readable
    pick.getBoundingClientRect = () => measured(96);
    await act(async () => pick.click());
    expect(menuWidth()).toBe(`${SESSION_LIST_METRICS.pickMenuMin}px`);
  });
});
