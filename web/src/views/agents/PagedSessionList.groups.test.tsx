/// <reference types="node" />
// @vitest-environment jsdom
// Browse by a computer, a project or a space draws the paged list in that structure: one folding section per group.
import { readFileSync } from "node:fs";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { api } from "../../lib/api";
import { loadPages } from "../../lib/lazy-page";
import { listPair, SessionList, type ListCounts } from "../../lib/session-list";
import type { SessionCatalog } from "../../lib/session-groups";
import { stateCount } from "../../lib/state-labels";
import { SESSION_LIST_METRICS } from "../../ui/list-metrics";
import { PagedSessionList } from "./PagedSessionList";

vi.mock("../../lib/api", async (original) => ({ ...await original<typeof import("../../lib/api")>(), api: vi.fn() }));
const hub = vi.hoisted(() => ({
  handlers: new Set<{ event: string; fn: () => void }>(),
  // what a space's + asked New agent for (#13523)
  opened: [] as unknown[],
  // the computers the store knows: their names are the list's label, their online state is the store's (#13522)
  machines: [{ id: "mac", name: "MacBook", online: true }, { id: "pc", name: "Windows PC", online: true },
    { id: "nas", name: "NAS", online: false, last_seen: "2026-01-01T00:00:00Z" }, { id: "lab", name: "Lab", online: true }],
}));
vi.mock("../../lib/new-agent", () => ({ openNewAgent: (preset: unknown) => void hub.opened.push(preset) }));
vi.mock("../../lib/store", () => ({
  onHubEvent: (event: string, fn: () => void) => {
    const entry = { event, fn };
    hub.handlers.add(entry);
    return () => { hub.handlers.delete(entry); };
  },
  useStore: <S,>(select: (state: { machines: typeof hub.machines }) => S) => select({ machines: hub.machines }),
}));
// jsdom lays nothing out, so the flat list's virtualizer would draw no row: draw every item, in order.
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count, getItemKey }: { count: number; getItemKey: (i: number) => string }) => ({
    getVirtualItems: () => Array.from({ length: count }, (_, index) => ({ index, key: getItemKey(index), start: index * 32 })),
    getTotalSize: () => count * 32,
    measureElement: () => {},
  }),
}));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;

type Row = { id: string; machine: string; project: string; group: string; state: string };
const identity = (row: Row) => row.id;
const compare = (a: Row, b: Row) => a.id.localeCompare(b.id);
const row = (id: string, machine: string, project: string, group = "", state = "idle"): Row => ({ id, machine, project, group, state });
const ROWS = [
  row("a1", "mac", "/Users/me/code/app", "w1", "needs_you"), row("a2", "mac", "/Users/me/code/app", "w1"), row("a3", "mac", "/Users/me/notes"),
  row("b1", "pc", "C:\\code\\App", "w1"), row("b2", "pc", "D:\\Notes", "w2"), row("c1", "nas", ""), row("d1", "lab", "/srv/tool"),
];
const NAMES: Record<string, string> = { mac: "MacBook", pc: "Windows PC", nas: "NAS", lab: "Lab" };
const ORIGIN: Record<string, string> = { [listPair("mac", "/Users/me/code/app")]: "git.example.com/team/app", [listPair("pc", "C:\\code\\App")]: "git.example.com/team/app" };
const folder = (path: string) => path.replace(/\\/g, "/").split("/").pop() ?? "";
/** A space's name where it is not `Space <id>`: two of one computer's can share one. */
const SPACE_NAMES: Record<string, string> = { "w-app": "app", "codex-project:/Users/me/Desktop/app": "app" };

const params = (path: string) => new URL(path, "https://example.com").searchParams;
/** The rows a read selects: the state, the search, a machine, and the place and space pairs. */
function matching(q: URLSearchParams): Row[] {
  const places = q.getAll("place"), spaces = q.getAll("space");
  return ROWS.filter((r) => (!q.get("state") || q.get("state") === r.state) && (!q.get("machine_id") || q.get("machine_id") === r.machine) &&
    (!q.get("project") || q.get("project") === r.project) && (!q.get("group") || q.get("group") === r.group) && (!q.get("q") || r.id.includes(q.get("q")!)) &&
    (!places.length || places.includes(listPair(r.machine, r.project))) && (!spaces.length || spaces.includes(listPair(r.machine, r.group))));
}
function tally(rows: Row[], key: (r: Row) => string): Record<string, number> {
  const out: Record<string, number> = {};
  for (const r of rows) out[key(r)] = (out[key(r)] ?? 0) + 1;
  return out;
}
const countsOf = (rows: Row[]): ListCounts => ({ total: rows.length, revision: "r", machine: tally(rows, (r) => r.machine), project: tally(rows, (r) => r.project),
  group: tally(rows, (r) => r.group), state: tally(rows, (r) => r.state) });
/** The hub's catalog of rows: a repository's directories as one project, the rest by folder, spaces by computer. */
function catalogOf(rows: Row[]): SessionCatalog {
  const projects = new Map<string, SessionCatalog["projects"][number]>();
  const spaces = new Map<string, SessionCatalog["spaces"][number]>();
  for (const r of rows) {
    const pair = listPair(r.machine, r.project);
    const id = !r.project ? "" : ORIGIN[pair] ? `git:${ORIGIN[pair]}` : `dir:${folder(r.project).toLowerCase()}`;
    const needs = r.state === "needs_you" ? 1 : 0;
    const p = projects.get(id) ?? { id, name: id ? folder(r.project).toLowerCase() : "", origin: ORIGIN[pair], total: 0, needs_you: 0, places: [] };
    p.total++;
    p.needs_you! += needs;
    const place = p.places.find((x) => x.machine_id === r.machine && x.path === r.project);
    if (place) place.total++; else p.places.push({ machine_id: r.machine, path: r.project, total: 1 });
    projects.set(id, p);
    const s = spaces.get(listPair(r.machine, r.group)) ?? { machine_id: r.machine, id: r.group, name: r.group ? SPACE_NAMES[r.group] ?? `Space ${r.group}` : "",
      ...(r.group && r.project ? { path: r.project } : {}), total: 0, needs_you: 0 };
    s.total++;
    s.needs_you! += needs;
    spaces.set(listPair(r.machine, r.group), s);
  }
  const last = (id: string) => (id ? 0 : 1);
  return { revision: "r", total: rows.length,
    projects: [...projects.values()].sort((a, b) => last(a.id) - last(b.id) || b.total - a.total || a.id.localeCompare(b.id)),
    spaces: [...spaces.values()].sort((a, b) => last(a.id) - last(b.id) || b.total - a.total || a.machine_id.localeCompare(b.machine_id)) };
}

let host: HTMLDivElement;
let root: Root;
let list: SessionList<Row>;
let pending = 0; // how many more catalog reads say they are pending
const calls = () => vi.mocked(api).mock.calls.map(([path]) => path as string);
const reads = (kind: "catalog" | "counts" | "page") => calls().filter((path) => {
  const q = params(path);
  return kind === "page" ? !q.has("catalog") && !q.has("counts") : q.has(kind);
});

beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear();
  pending = 0;
  vi.mocked(api).mockReset().mockImplementation((path) => {
    const q = params(path as string);
    const rows = matching(q);
    const answer = q.has("catalog") ? { ...catalogOf(rows), ...(pending-- > 0 ? { pending: true } : {}) }
      : q.has("counts") ? countsOf(rows)
        : { sessions: rows, remove: [], has_more: false, total: rows.length, revision: "r" };
    return Promise.resolve(answer) as ReturnType<typeof api>;
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  list = new SessionList<Row>("/sessions", identity, compare);
});

afterEach(() => {
  setShown(true);
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
});

async function show({ catalog = true, open = "" }: { catalog?: boolean; open?: string } = {}) {
  await list.load();
  await act(async () => root.render(<PagedSessionList root={list} path="/sessions" identity={identity} compare={compare} event="sessions_list" catalog={catalog}
    label={(dimension, id) => dimension === "machine" ? NAMES[id] ?? id : id} isOpen={(r) => r.id === open}
    placeOf={(r) => ({ machine: r.machine, project: r.project, group: r.group })}
    renderRow={(r, within, group) => <span data-row={r.id} data-within={within ?? ""} data-group={group?.name ?? ""}>{r.id}</span>} />));
}
const settle = () => act(async () => { await vi.advanceTimersByTimeAsync(0); });
async function browseBy(key: "recent" | "machine" | "project" | "group") {
  const tab = [...host.querySelectorAll<HTMLButtonElement>(`[aria-label="${t("paged-agents.group_by")}"] [role="radio"]`)].find((b) => b.textContent === t(`paged-agents.${key}`))!;
  await act(async () => tab.click());
  await settle();
}
const sections = () => [...host.querySelectorAll<HTMLElement>("[data-session-group]")];
const heading = (section: HTMLElement) => section.querySelector<HTMLButtonElement>("button[aria-expanded]")!;
/** A section as its heading reads: its name, then how many of its sessions need the user (when any do), then its count. */
const summary = (section: HTMLElement) => {
  const top = section.querySelector<HTMLElement>("[data-sidebar-group-heading]")!;
  const needs = top.querySelector(".text-attention")?.firstChild?.textContent;
  return [heading(section).textContent, needs, top.querySelector("span[aria-hidden]")!.textContent].filter(Boolean).join(" ");
};
const rowsIn = (section: HTMLElement) => [...section.querySelectorAll<HTMLElement>("[data-row]")].map((el) => el.dataset.row);
async function toggle(section: HTMLElement) {
  await act(async () => heading(section).click());
  await settle();
}
const hubHint = () => { for (const entry of [...hub.handlers]) if (entry.event === "sessions_list") entry.fn(); };
function setShown(shown: boolean) {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => shown ? "visible" : "hidden" });
  document.dispatchEvent(new Event("visibilitychange"));
}
async function hint() {
  await act(async () => {
    for (const entry of [...hub.handlers]) if (entry.event === "sessions_list") entry.fn();
    await list.load();
    // past the invalidation throttle window (lib/session-list.ts): a section's trailing read has gone out
    await vi.advanceTimersByTimeAsync(2_100);
  });
}

beforeAll(() => loadPages(), 60_000);

describe("the sections' own chunk", () => {
  // The default list, newest first, is what a first screen draws; the list by group loads when a Browse by is chosen.
  it("keeps the sections out of the list's static imports", () => {
    const source = readFileSync("src/views/agents/PagedSessionList.tsx", "utf8");
    expect(source).not.toMatch(/^import (?!type )[^;]*from\s+["']\.\/GroupedSessions["']/m);
    expect(source).toMatch(/lazyPage\(\(\) => import\("\.\/GroupedSessions"\)/);
    expect(source).toMatch(/<Suspense fallback=\{loadingLine\}>\s*<GroupedSessions /);
  });
});

describe("the list by computer", () => {
  it("draws one section per computer in a fixed order: the first three open on their sessions, the rest folded and unread", async () => {
    await show();
    vi.mocked(api).mockClear();
    await browseBy("machine");
    expect(sections().map(summary)).toEqual(["Lab 1", "MacBook 1 3", "NAS 1", "Windows PC 2"]); // name, needs you, count
    expect(sections().map((s) => heading(s).getAttribute("aria-expanded"))).toEqual(["true", "true", "true", "false"]);
    expect(sections().map(rowsIn)).toEqual([["d1"], ["a1", "a2", "a3"], ["c1"], []]);
    expect(sections()[1].querySelector("[data-row]")!.getAttribute("data-within")).toBe("machine");
    expect(host.querySelector("[data-virtual-sessions]")).toBeNull();
    expect(host.querySelector("[data-session-total]")!.textContent).toBe(t("paged-agents.count", { count: 7 }));
    // one page for each open section, none for the folded one; and a section reads rows, never counts: its number is
    // on its heading already
    expect(reads("page").map((path) => params(path).get("machine_id")).sort()).toEqual(["lab", "mac", "nas"]);
    expect(reads("counts").every((path) => !params(path).has("machine_id"))).toBe(true);

    await toggle(sections()[3]);
    expect(rowsIn(sections()[3])).toEqual(["b1", "b2"]);
    expect(Object.fromEntries(params(reads("page").at(-1)!))).toEqual({ machine_id: "pc", summary: "1", limit: "50" });
  });

  it("stops reading a section once it is folded, and keeps it folded", async () => {
    await show();
    await browseBy("machine");
    vi.mocked(api).mockClear();
    await hint();
    expect(reads("page").some((path) => params(path).get("machine_id") === "mac")).toBe(true);
    await toggle(sections()[0]);
    expect(rowsIn(sections()[0])).toEqual([]);
    vi.mocked(api).mockClear();
    await hint();
    expect(reads("page").some((path) => params(path).get("machine_id") === "lab")).toBe(false);
    expect(reads("page").some((path) => params(path).get("machine_id") === "mac")).toBe(true);
    // what someone folded stays folded the next time the list is drawn
    act(() => root.unmount());
    root = createRoot(host);
    await show();
    await settle();
    expect(sections().map((s) => heading(s).getAttribute("aria-expanded"))).toEqual(["false", "true", "true", "false"]);
  });

  it("shows a section's newest sessions first, then what it has read, then reads on, under a quiet Load more line", async () => {
    const many = Array.from({ length: 9 }, (_, i) => row(`m${i}`, "mac", "/big"));
    vi.mocked(api).mockImplementation((path) => {
      const q = params(path as string);
      const answer = q.has("counts") ? countsOf(many)
        : q.has("cursor") ? { sessions: [row("m9", "mac", "/big")], remove: [], has_more: false, total: 10, revision: "r" }
          : { sessions: many, remove: [], has_more: q.has("machine_id"), next_cursor: q.has("machine_id") ? "next" : undefined, total: q.has("machine_id") ? 10 : 9, revision: "r" };
      return Promise.resolve(answer) as ReturnType<typeof api>;
    });
    await show();
    await browseBy("machine");
    const mac = () => sections().find((s) => s.getAttribute("data-session-group") === "mac")!;
    const more = () => [...mac().querySelectorAll("button")].find((b) => b.textContent === t("paged-agents.more"))!;
    // the sidebar's quiet line under a list's rows, not a button pill (#6662)
    expect(more().className).toContain("text-ink-3");
    expect(rowsIn(mac())).toEqual(["m0", "m1", "m2", "m3", "m4", "m5"]);
    vi.mocked(api).mockClear();
    await act(async () => more().click());
    await settle();
    expect(rowsIn(mac())).toHaveLength(9); // already read: shown without another request
    expect(reads("page")).toEqual([]);
    await act(async () => more().click());
    await settle();
    expect(rowsIn(mac())).toHaveLength(10);
    expect(reads("page").map((path) => params(path).get("cursor"))).toEqual(["next"]);
    expect(more()).toBeUndefined();
  });

  it("counts a state tab's sessions on the headings, and lists only them", async () => {
    await show();
    await browseBy("machine");
    const needs = [...host.querySelectorAll<HTMLButtonElement>(`[aria-label="${t("sidebar.quick_filters")}"] [role="radio"]`)][1];
    await act(async () => needs.click());
    await settle();
    expect(sections().map(summary)).toEqual(["MacBook 1"]);
    expect(host.querySelector("[data-session-total]")!.textContent).toBe(t("paged-agents.count", { count: 1 }));
    expect(rowsIn(sections()[0])).toEqual(["a1"]);
  });

  it("opens by itself the section the open conversation is in", async () => {
    await show({ open: "b1" });
    await browseBy("machine");
    expect(sections().map((s) => heading(s).getAttribute("aria-expanded"))).toEqual(["true", "true", "true", "true"]);
    expect(rowsIn(sections()[3])).toEqual(["b1", "b2"]);
  });

  it("opens the section of an open conversation on a later page", async () => {
    const filler = Array.from({ length: 50 }, (_, i) => row(`f${i}`, "mac", "/bulk"));
    const all = [...filler, ...ROWS];
    const selected = (q: URLSearchParams) => {
      const places = q.getAll("place"), spaces = q.getAll("space");
      return all.filter((r) => (!q.get("state") || q.get("state") === r.state) && (!q.get("machine_id") || q.get("machine_id") === r.machine) &&
        (!q.get("project") || q.get("project") === r.project) && (!q.get("group") || q.get("group") === r.group) && (!q.get("q") || r.id.includes(q.get("q")!)) &&
        (!places.length || places.includes(listPair(r.machine, r.project))) && (!spaces.length || spaces.includes(listPair(r.machine, r.group))));
    };
    vi.mocked(api).mockImplementation((path) => {
      const q = params(path as string);
      const rows = selected(q);
      const answer = q.has("catalog") ? catalogOf(rows) : q.has("counts") ? countsOf(rows)
        : q.has("cursor") ? { sessions: rows, remove: [], has_more: false, total: rows.length, revision: "r" }
          : { sessions: rows.slice(0, 50), remove: [], has_more: true, next_cursor: "second", total: rows.length, revision: "r" };
      return Promise.resolve(answer) as ReturnType<typeof api>;
    });
    await show({ open: "b1" });
    await browseBy("machine");
    expect(sections().map((s) => heading(s).getAttribute("aria-expanded"))).toEqual(["true", "true", "true", "true"]);
    expect(rowsIn(sections()[3])).toEqual(["b1", "b2"]);
    expect(reads("page").filter((path) => params(path).get("cursor") === "second")).toHaveLength(1);
  });
});

describe("stable presentation over paged transport (#11801)", () => {
  it("sorts the loaded prefix before the preview, preserving its rows through refreshes and Load more", async () => {
    type Ranked = Row & { rank: number; place: number };
    let many: Ranked[] = Array.from({ length: 9 }, (_, i) => ({ ...row(`r${i}`, "mac", "/big"), rank: i, place: i }));
    vi.mocked(api).mockImplementation((path) => {
      const q = params(path as string);
      const answer = q.has("counts") ? countsOf(many) : { sessions: many, remove: [], has_more: false, total: many.length, revision: "r" };
      return Promise.resolve(answer) as ReturnType<typeof api>;
    });
    const identity = (r: Ranked) => r.id;
    const transport = (a: Ranked, b: Ranked) => b.rank - a.rank || a.id.localeCompare(b.id);
    const stable = (a: Ranked, b: Ranked) => a.place - b.place;
    const rankedList = new SessionList<Ranked>("/sessions", identity, transport);
    await rankedList.load();
    await act(async () => root.render(<PagedSessionList root={rankedList} path="/sessions" identity={identity} compare={transport} event="sessions_list"
      grouping={{ dimension: "machine", bar: null, label: "Machine", rowOrder: { compare: stable } }}
      placeOf={(r) => ({ machine: r.machine, project: r.project, group: r.group })}
      renderRow={(r) => <span data-row={r.id}>{r.id}</span>} />));
    await settle();
    const shown = () => [...host.querySelectorAll<HTMLElement>("[data-session-row]")].map((el) => el.textContent);
    expect(shown()).toEqual(["r0", "r1", "r2", "r3", "r4", "r5"]);

    // The last row in herdr order becomes the first in transport order. The preview must not gain or lose a row.
    many = many.map((r) => r.id === "r0" ? { ...r, rank: 100 } : r);
    await act(async () => {
      hubHint();
      await rankedList.load();
      await vi.advanceTimersByTimeAsync(2_100);
    });
    expect(shown()).toEqual(["r0", "r1", "r2", "r3", "r4", "r5"]);
    await act(async () => [...host.querySelectorAll("button")].find((b) => b.textContent === t("paged-agents.more"))!.click());
    await settle();
    expect(shown()).toEqual(["r0", "r1", "r2", "r3", "r4", "r5", "r6", "r7", "r8"]);
  });

  type Ranked = Row & { rank: number; place: number };
  const transport = (a: Ranked, b: Ranked) => b.rank - a.rank || a.id.localeCompare(b.id);
  const stable = (a: Ranked, b: Ranked) => a.place - b.place;
  const range = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => `r${from + i}`);
  const shown = () => [...host.querySelectorAll<HTMLElement>("[data-session-row]")].map((el) => el.textContent);
  const more = () => [...host.querySelectorAll("button")].find((b) => b.textContent === t("paged-agents.more"))!;

  /** A 120-row hub whose newest-first transport is the reverse of its stable tab order. */
  async function showRankedPages(query = "") {
    const fixture = {
      rows: Array.from({ length: 120 }, (_, i) => ({ ...row(`r${i + 1}`, "mac", "/big"), rank: i + 1, place: i + 1 })),
      delta: [] as Ranked[],
      revision: "r0",
    };
    vi.mocked(api).mockImplementation((path) => {
      const q = params(path as string);
      if (q.has("counts")) return Promise.resolve(countsOf(fixture.rows)) as ReturnType<typeof api>;
      if (q.has("since")) {
        return Promise.resolve({ sessions: q.get("since") === fixture.revision ? [] : fixture.delta, remove: [], added: [], has_more: false,
          total: fixture.rows.length, revision: fixture.revision }) as ReturnType<typeof api>;
      }
      const cursor = q.get("cursor");
      const offset = cursor ? Number(cursor.split(":")[1]) : 0;
      const revision = cursor?.split(":")[0] ?? fixture.revision;
      const ordered = [...fixture.rows].sort(transport);
      const end = offset + 50;
      return Promise.resolve({ sessions: ordered.slice(offset, end), remove: [], has_more: end < ordered.length,
        next_cursor: end < ordered.length ? `${revision}:${end}` : undefined, total: ordered.length, revision }) as ReturnType<typeof api>;
    });
    const identity = (r: Ranked) => r.id;
    const rankedList = new SessionList<Ranked>("/sessions", identity, transport);
    await rankedList.load();
    await act(async () => root.render(<PagedSessionList root={rankedList} path="/sessions" identity={identity} compare={transport} event="sessions_list"
      grouping={{ dimension: "machine", bar: null, label: "Machine", query, rowOrder: { compare: stable } }}
      placeOf={(r) => ({ machine: r.machine, project: r.project, group: r.group })}
      renderRow={(r) => <span tabIndex={0} data-row={r.id}>{r.id}</span>} />));
    await settle();
    return { fixture, rankedList };
  }

  it("does not displace the preview when activity brings an unloaded row into the loaded prefix", async () => {
    const { fixture, rankedList } = await showRankedPages();
    expect(shown()).toEqual(range(71, 76));

    const active = { ...fixture.rows[0]!, rank: 121 };
    fixture.rows = fixture.rows.map((r) => r.id === active.id ? active : r);
    fixture.delta = [active];
    fixture.revision = "r1";
    await act(async () => {
      hubHint();
      await rankedList.load();
      await vi.advanceTimersByTimeAsync(2_100);
    });

    expect(shown()).toEqual(range(71, 76));
    await act(async () => more().click());
    await settle();
    expect(shown()).toEqual(range(71, 120));
  });

  it("retains flat-search rows and focus when activity brings an unloaded row into the prefix", async () => {
    const { fixture } = await showRankedPages("r");
    const before = shown();
    expect(before[0]).toBe("r71");
    const focused = host.querySelector<HTMLElement>('[data-row="r71"]')!;
    act(() => focused.focus());
    expect(document.activeElement).toBe(focused);

    const active = { ...fixture.rows[0]!, rank: 121 };
    fixture.rows = fixture.rows.map((r) => r.id === active.id ? active : r);
    fixture.delta = [active];
    fixture.revision = "r1";
    await act(async () => {
      hubHint();
      await vi.advanceTimersByTimeAsync(2_100);
    });

    expect(shown()).toEqual(before); // newly active, but an old row: the current hub's empty `added` does not admit it
    expect(document.activeElement).toBe(focused);
  });

  it("reveals the first page, then sorts a second network page into the structural union", async () => {
    await showRankedPages();
    expect(shown()).toEqual(range(71, 76));
    await act(async () => more().click());
    await settle();
    expect(shown()).toEqual(range(71, 120));

    await act(async () => more().click());
    await settle();
    expect(shown()).toEqual(range(21, 120));
  });

  it("applies activity, swapped slots and a new first tab, then reloads to the same placement", async () => {
    const fixture = {
      rows: [
        { ...row("r1", "mac", "/big"), rank: 1, place: 1 },
        { ...row("r2", "mac", "/big"), rank: 2, place: 2 },
        { ...row("r3", "mac", "/big"), rank: 3, place: 3 },
      ] as Ranked[],
      delta: [] as Ranked[],
      added: [] as string[],
      revision: "r0",
    };
    vi.mocked(api).mockImplementation((path) => {
      const q = params(path as string);
      if (q.has("counts")) return Promise.resolve(countsOf(fixture.rows)) as ReturnType<typeof api>;
      if (q.has("since")) {
        return Promise.resolve({ sessions: q.get("since") === fixture.revision ? [] : fixture.delta, remove: [], added: fixture.added, has_more: false,
          total: fixture.rows.length, revision: fixture.revision }) as ReturnType<typeof api>;
      }
      return Promise.resolve({ sessions: [...fixture.rows].sort(transport), remove: [], has_more: false,
        total: fixture.rows.length, revision: fixture.revision }) as ReturnType<typeof api>;
    });
    const identity = (r: Ranked) => r.id;
    const renderList = async (rankedList: SessionList<Ranked>) => {
      await rankedList.load();
      await act(async () => root.render(<PagedSessionList root={rankedList} path="/sessions" identity={identity} compare={transport} event="sessions_list"
        grouping={{ dimension: "machine", bar: null, label: "Machine", rowOrder: { compare: stable } }}
        placeOf={(r) => ({ machine: r.machine, project: r.project, group: r.group })}
        renderRow={(r) => <span data-row={r.id} data-rank={r.rank} data-place={r.place}>{r.id}</span>} />));
      await settle();
    };
    const refresh = async (revision: string) => {
      fixture.revision = revision;
      await act(async () => {
        hubHint();
        await vi.advanceTimersByTimeAsync(2_100);
      });
    };
    let rankedList = new SessionList<Ranked>("/sessions", identity, transport);
    await renderList(rankedList);
    expect(shown()).toEqual(["r1", "r2", "r3"]);

    const active = { ...fixture.rows[0]!, rank: 10 };
    fixture.rows = fixture.rows.map((r) => r.id === active.id ? active : r);
    fixture.delta = [active];
    await refresh("r1");
    expect(shown()).toEqual(["r1", "r2", "r3"]);
    expect(host.querySelector('[data-row="r1"]')!.getAttribute("data-rank")).toBe("10");

    const swapped = fixture.rows.map((r) => r.id === "r1" ? { ...r, place: 2 } : r.id === "r2" ? { ...r, place: 1 } : r);
    fixture.rows = swapped;
    fixture.delta = swapped.filter((r) => r.id !== "r3");
    await refresh("r2");
    expect(shown()).toEqual(["r2", "r1", "r3"]);

    const first = { ...row("r0", "mac", "/big"), rank: 20, place: 0 };
    fixture.rows = [first, ...fixture.rows];
    fixture.delta = fixture.rows;
    fixture.added = ["r0"];
    await refresh("r3");
    expect(shown()).toEqual(["r0", "r2", "r1", "r3"]);

    act(() => root.unmount());
    root = createRoot(host);
    rankedList = new SessionList<Ranked>("/sessions", identity, transport);
    await renderList(rankedList);
    expect(shown()).toEqual(["r0", "r2", "r1", "r3"]);
  });
});

describe("a list by group whose first read fails", () => {
  // audit item 4: the groups come from the root list's counts, and its failed read left them on "Loading…" for good
  it("says why with Retry instead of loading for good, and draws the groups once Retry reads them", async () => {
    localStorage.setItem("persona.agentBrowseBy", "machine");
    const answer = vi.mocked(api).getMockImplementation()!;
    vi.mocked(api).mockImplementation(() => Promise.reject(new Error("HTTP 500")));
    await list.load().catch(() => {});
    await act(async () => root.render(<PagedSessionList root={list} path="/sessions" identity={identity} compare={compare} event="sessions_list"
      label={(_, id) => NAMES[id] ?? id} renderRow={(r) => <span data-row={r.id}>{r.id}</span>} />));
    await settle();
    expect(host.querySelector('[role="status"]')).toBeNull();
    const alerts = [...host.querySelectorAll('[role="alert"]')].map((el) => el.textContent);
    expect(alerts).toEqual(["HTTP 500"]);
    vi.mocked(api).mockImplementation(answer);
    const retry = [...host.querySelectorAll("button")].find((b) => b.textContent === t("sidebar.retry"))!;
    await act(async () => retry.click());
    await settle();
    expect(host.querySelector('[role="alert"]')).toBeNull();
    expect(sections().map(summary)).toEqual(["Lab 1", "MacBook 1 3", "NAS 1", "Windows PC 2"]);
  });
});

describe("the list by project, on a hub with the catalog", () => {
  it("makes a repository on two computers one project that lists both directories and their sessions together", async () => {
    await show();
    vi.mocked(api).mockClear();
    await browseBy("project");
    // name, how many need the user, count
    // a project on two computers names neither; one only on Lab names it, the list being on several (#16877)
    expect(sections().map(summary)).toEqual(["app 1 3", "notes 2", "tool · Lab 1", `${t("paged-agents.no_folder")} 1`]);
    // one read names the projects and counts the ones that need the user
    expect(reads("catalog").map((path) => Object.fromEntries(params(path)))).toEqual([{ catalog: "1" }]);

    const app = sections()[0];
    expect(rowsIn(app)).toEqual(["a1", "a2", "b1"]);
    // a row is told the group it is under: the sidebar names the computer on a row of a project that has several
    expect([...app.querySelectorAll<HTMLElement>("[data-row]")].map((el) => `${el.dataset.within}:${el.dataset.group}`)).toEqual(["project:app", "project:app", "project:app"]);
    // its remote, then each directory with its computer and how many of the sessions are there
    const where = [...app.querySelectorAll(`[aria-label="${t("agents-groupedsessions.places")}"] li`)]
      .map((li) => [li.querySelector("span[title]")!.textContent, li.querySelector("span[aria-hidden]")?.textContent].filter(Boolean).join(" = "));
    expect(where).toEqual(["git.example.com/team/app", "MacBook · ~/code/app = 2", "Windows PC · C:\\code\\App = 1"]);
    expect(app.querySelector(`[aria-label="${t("agents-groupedsessions.places")}"] span[title="MacBook · /Users/me/code/app"]`)).not.toBeNull(); // the whole path on hover
    const read = reads("page").find((path) => params(path).getAll("place").length === 2)!;
    expect(params(read).getAll("place")).toEqual([listPair("mac", "/Users/me/code/app"), listPair("pc", "C:\\code\\App")]);
    expect(params(read).has("project")).toBe(false);

    // the sessions in no folder: the computers' empty paths
    await toggle(sections()[3]);
    expect(rowsIn(sections()[3])).toEqual(["c1"]);
    expect(params(reads("page").at(-1)!).getAll("place")).toEqual([listPair("nas", "")]);
  });

  it("lists a project picked in the menu as the one list, with its directories under its name there", async () => {
    await show();
    await browseBy("project");
    const pick = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("paged-agents.group_filter")}"]`)!;
    await act(async () => pick.click());
    const items = [...document.querySelectorAll<HTMLElement>('[role="menuitemradio"]')];
    expect(items.map((item) => item.textContent)).toEqual([
      t("paged-agents.all_projects"),
      "appgit.example.com/team/app\nMacBook · ~/code/app · 2\nWindows PC · C:\\code\\App · 13",
      "notesMacBook · ~/notes · 1\nWindows PC · D:\\Notes · 12",
      "toolLab · /srv/tool1",
      `${t("paged-agents.no_folder")}1`,
    ]);
    vi.mocked(api).mockClear();
    await act(async () => items[1].click());
    await settle();
    expect(pick.textContent).toBe("app");
    expect(sections()).toEqual([]);
    expect([...host.querySelectorAll<HTMLElement>("[data-virtual-sessions] [data-row]")].length + host.querySelectorAll("[data-virtual-sessions]").length).toBeGreaterThan(0);
    const both = [listPair("mac", "/Users/me/code/app"), listPair("pc", "C:\\code\\App")];
    expect(reads("page").every((path) => String(params(path).getAll("place")) === String(both))).toBe(true);
    expect(reads("page").length).toBeGreaterThan(0);
    // the state tabs count the picked project's sessions
    expect(reads("counts").some((path) => String(params(path).getAll("place")) === String(both))).toBe(true);
    expect([...host.querySelectorAll<HTMLButtonElement>(`[aria-label="${t("sidebar.quick_filters")}"] [role="radio"]`)].map((c) => c.getAttribute("aria-label")).slice(1))
      .toEqual([`${t("lib-state-labels.needs_you")} 1`, `${t("lib-state-labels.idle")} 2`]);
  });

  it("reads the groups once per hint window, and the open sections' rows follow every one of them", async () => {
    await show();
    await browseBy("project");
    const groupReads = () => reads("catalog").length;
    const rowReads = () => reads("page").filter((path) => params(path).getAll("place").includes(listPair("pc", "C:\\code\\App"))).length;
    vi.mocked(api).mockClear();
    // A burst of hints within one window (the fleet broadcasts one per working-agent update) reads the section's
    // rows once, not once per hint; the groups refresh (their own 1.5 s gap) ride that same window.
    await act(async () => {
      for (let i = 0; i < 5; i++) {
        for (const entry of [...hub.handlers]) if (entry.event === "sessions_list") entry.fn();
      }
      await list.load();
      await vi.advanceTimersByTimeAsync(2_100);
    });
    expect(rowReads()).toBe(1);
    expect(groupReads()).toBe(1);
    // A hint after a quiet stretch is read at once
    await hint();
    expect(rowReads()).toBe(2);
    expect(groupReads()).toBe(2);
  });

  it("reads nothing for the hints a hidden tab gets, and once for them all when it is shown again", async () => {
    await show();
    await browseBy("machine");
    await act(async () => { await vi.advanceTimersByTimeAsync(SESSION_LIST_METRICS.groupsRefresh); });
    vi.mocked(api).mockClear();
    await act(async () => setShown(false));
    for (let i = 0; i < 10; i++) await act(async () => { hubHint(); await vi.advanceTimersByTimeAsync(0); });
    await act(async () => { await vi.advanceTimersByTimeAsync(2 * SESSION_LIST_METRICS.groupsRefresh); });
    expect(calls()).toEqual([]);
    await act(async () => setShown(true));
    await settle();
    // each open section reads once, the groups once; the folded section still reads nothing
    expect(reads("page").map((path) => params(path).get("machine_id")).sort()).toEqual(["lab", "mac", "nas"]);
    expect(reads("counts")).toHaveLength(1);
  });

  it("reads a catalog again while its computers are still answering, less often as it goes on", async () => {
    pending = 3;
    await show();
    vi.mocked(api).mockClear();
    await browseBy("project");
    expect(reads("catalog")).toHaveLength(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(reads("catalog")).toHaveLength(2);
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(reads("catalog")).toHaveLength(2); // the second wait is half as long again
    await act(async () => { await vi.advanceTimersByTimeAsync(500); });
    expect(reads("catalog")).toHaveLength(3);
    await act(async () => { await vi.advanceTimersByTimeAsync(2250); });
    expect(reads("catalog")).toHaveLength(4);
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
    expect(reads("catalog")).toHaveLength(4); // settled: no more
  });
});

describe("the list by space", () => {
  const computers = () => [...host.querySelectorAll<HTMLElement>("[data-session-computer]")];
  const computer = (id: string) => computers().find((c) => c.dataset.sessionComputer === id)!;
  /** A computer's heading as it reads: its name with its state, then what its numbers say. */
  const computerHeading = (c: HTMLElement) => {
    const top = c.querySelector<HTMLElement>("[data-sidebar-group-heading]")!;
    return [heading(c).textContent, ...[...top.querySelectorAll<HTMLElement>(":scope > div > span[title]")].map((el) => el.title)];
  };
  const spacesIn = (c: HTMLElement) => [...c.querySelectorAll<HTMLElement>("[data-session-group]")];
  const spaceReads = () => reads("page").map((path) => params(path).getAll("space")).filter((pairs) => pairs.length);

  it("puts each computer's spaces under its heading, with its online state and numbers, its sessions in no space last (#13522)", async () => {
    await show();
    vi.mocked(api).mockClear();
    await browseBy("group");
    // the computers in the hub's order (none given here: by id), each named as the list names it, with its state
    expect(computers().map((c) => c.dataset.sessionComputer)).toEqual(["lab", "mac", "nas", "pc"]);
    expect(computerHeading(computer("mac"))).toEqual([`MacBook${t("sidebar.online")}`, stateCount("needs_you", 1), t("paged-agents.count", { count: 3 })]);
    expect(computerHeading(computer("pc"))).toEqual([`Windows PC${t("sidebar.online")}`, t("paged-agents.count", { count: 2 })]);
    // a computer that is away starts folded, and says so beside its name
    expect(computerHeading(computer("nas"))).toEqual([`NAS${t("sidebar.offline")}`, t("paged-agents.count", { count: 1 })]);
    expect(computer("nas").querySelector("[data-sidebar-group-heading]")!.textContent).toContain(t("sidebar.offline"));
    expect(computers().map((c) => heading(c).getAttribute("aria-expanded"))).toEqual(["true", "true", "false", "true"]);
    // inside each, its spaces by name, then its sessions in no space; the space headings no longer name the computer
    expect(spacesIn(computer("mac")).map((s) => heading(s).textContent)).toEqual(["Space w1", t("paged-agents.no_space")]);
    expect(spacesIn(computer("pc")).map((s) => heading(s).textContent)).toEqual(["Space w1", "Space w2"]);
    expect(spacesIn(computer("lab")).map((s) => heading(s).textContent)).toEqual([t("paged-agents.no_space")]);
    expect(spacesIn(computer("nas"))).toEqual([]); // folded: nothing drawn, nothing read
    expect(spacesIn(computer("mac")).map(rowsIn)).toEqual([["a1", "a2"], ["a3"]]);
    expect(spacesIn(computer("mac"))[0].querySelector("[data-row]")!.getAttribute("data-within")).toBe("group");
    // one read for each open space, by its computer's pair; none for the folded computer's
    expect(spaceReads().map(String).sort()).toEqual([listPair("lab", ""), listPair("mac", ""), listPair("mac", "w1"), listPair("pc", "w1"), listPair("pc", "w2")].sort());
    // no second pill for the computer: a space already is one computer's
    expect(host.querySelector(`button[aria-label="${t("paged-agents.machine")}"]`)).toBeNull();

    await toggle(computer("nas"));
    expect(spacesIn(computer("nas")).map(rowsIn)).toEqual([["c1"]]);
    expect(params(reads("page").at(-1)!).getAll("space")).toEqual([listPair("nas", "")]);
    // what someone opened or folded is kept
    act(() => root.unmount());
    root = createRoot(host);
    await show();
    await settle();
    expect(computers().map((c) => heading(c).getAttribute("aria-expanded"))).toEqual(["true", "true", "true", "true"]);
  });

  it("keeps the computer level when a chip leaves one computer's sessions, its numbers the chip's", async () => {
    await show();
    await browseBy("group");
    const needs = [...host.querySelectorAll<HTMLButtonElement>(`[aria-label="${t("sidebar.quick_filters")}"] [role="radio"]`)][1];
    await act(async () => needs.click());
    await settle();
    expect(computers().map((c) => c.dataset.sessionComputer)).toEqual(["mac"]);
    expect(computerHeading(computer("mac"))).toEqual([`MacBook${t("sidebar.online")}`, t("paged-agents.count", { count: 1 })]);
    expect(spacesIn(computer("mac")).map(rowsIn)).toEqual([["a1"]]);
  });

  it("opens by itself the computer and the space the open conversation is in", async () => {
    await show({ open: "c1" });
    await browseBy("group");
    expect(heading(computer("nas")).getAttribute("aria-expanded")).toBe("true");
    expect(spacesIn(computer("nas")).map(rowsIn)).toEqual([["c1"]]);
  });

  it("tells apart a computer's spaces of one name by their parent folder (#13522)", async () => {
    ROWS.push(row("e1", "mac", "/Users/me/Desktop/app", "codex-project:/Users/me/Desktop/app"), row("e2", "mac", "/Users/me/code/app", "w-app"));
    try {
      await show();
      await browseBy("group");
      expect(spacesIn(computer("mac")).map((s) => heading(s).textContent)).toEqual(["app · ~/Desktop", "app · ~/code", "Space w1", t("paged-agents.no_space")]);
      expect(spacesIn(computer("mac")).slice(0, 2).map(rowsIn)).toEqual([["e1"], ["e2"]]);
    } finally {
      ROWS.splice(-2);
    }
  });

  describe("a space's + (#13523)", () => {
    const plus = (name: string) => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("agents-groupedsessions.new_agent_in", { name })}"]`);
    const pluses = () => [...host.querySelectorAll<HTMLButtonElement>("[data-space-add] button")];
    beforeEach(() => { hub.opened.length = 0; });

    it("opens New agent on the space's computer and folder, and only where there is a folder on a computer that is up", async () => {
      await show();
      await browseBy("group");
      // a space of each online computer has one; No project has no folder, and the computer that is away is folded
      expect(spacesIn(computer("mac")).map((s) => Boolean(s.querySelector(`[data-space-add]`)))).toEqual([true, false]);
      expect(spacesIn(computer("pc")).map((s) => Boolean(s.querySelector(`[data-space-add]`)))).toEqual([true, true]);
      expect(spacesIn(computer("lab")).map((s) => Boolean(s.querySelector(`[data-space-add]`)))).toEqual([false]);
      expect(pluses()).toHaveLength(3);
      await act(async () => plus("Space w2")!.click());
      expect(hub.opened).toEqual([{ machineId: "pc", cwd: "D:\\Notes" }]);
      // it starts nothing and folds nothing: the section stays as it was
      expect(heading(spacesIn(computer("pc"))[1]).getAttribute("aria-expanded")).toBe("true");
      // shown under the pointer, on keyboard focus and always on a touch screen; the numbers step aside for it
      expect(plus("Space w1")!.className).toContain("opacity-0");
      expect(plus("Space w1")!.className).toContain("group-hover/sec:opacity-100");
      expect(plus("Space w1")!.className).toContain("focus-visible:opacity-100");
      expect(plus("Space w1")!.className).toContain("[@media(hover:none)]:opacity-100");
      const numbers = spacesIn(computer("mac"))[0].querySelector("[data-sidebar-group-heading] span[title]")!.parentElement!;
      expect(numbers.className).toContain("group-hover/sec:-translate-x-6");
      expect(numbers.className).toContain("[@media(hover:none)]:mr-6");
    });

    it("has none on a computer that is away", async () => {
      const before = hub.machines;
      // a new list, as the store makes one for a push: the sidebar's projection of it is kept by the list's identity
      hub.machines = before.map((m) => (m.id === "pc" ? { ...m, online: false } : m));
      try {
        await show();
        await browseBy("group");
        await toggle(computer("pc"));
        expect(spacesIn(computer("pc")).map((s) => heading(s).textContent)).toEqual(["Space w1", "Space w2"]);
        expect(spacesIn(computer("pc")).some((s) => s.querySelector("[data-space-add]"))).toBe(false);
      } finally {
        hub.machines = before;
      }
    });

    it("names the space it starts in, its parent folder too where another of its computer's has its name", async () => {
      ROWS.push(row("e1", "mac", "/Users/me/Desktop/app", "codex-project:/Users/me/Desktop/app"), row("e2", "mac", "/Users/me/code/app", "w-app"));
      try {
        await show();
        await browseBy("group");
        await act(async () => plus("app · ~/code")!.click());
        expect(hub.opened).toEqual([{ machineId: "mac", cwd: "/Users/me/code/app" }]);
      } finally {
        ROWS.splice(-2);
      }
    });
  });
  it("lists one computer's spaces as before, with no computer level", async () => {
    const one = ROWS.filter((r) => r.machine === "mac");
    vi.mocked(api).mockImplementation((path) => {
      const q = params(path as string);
      const rows = matching(q).filter((r) => one.includes(r));
      const answer = q.has("catalog") ? catalogOf(rows) : q.has("counts") ? countsOf(rows) : { sessions: rows, remove: [], has_more: false, total: rows.length, revision: "r" };
      return Promise.resolve(answer) as ReturnType<typeof api>;
    });
    await show();
    await browseBy("group");
    expect(computers()).toEqual([]);
    expect(sections().map((s) => heading(s).textContent)).toEqual(["Space w1", t("paged-agents.no_space")]);
  });

  it("on a hub without the catalog groups by the ids it counts, with the computer pill beside", async () => {
    await show({ catalog: false });
    vi.mocked(api).mockClear();
    await browseBy("group");
    expect(reads("catalog")).toEqual([]);
    expect(sections().map((s) => heading(s).textContent)).toEqual(["w1", "w2"]);
    expect(host.querySelector(`button[aria-label="${t("paged-agents.machine")}"]`)).not.toBeNull();
    expect(rowsIn(sections()[0])).toEqual(["a1", "a2", "b1"]);
    expect(reads("page").map((path) => Object.fromEntries(params(path)))).toContainEqual({ group: "w1", summary: "1", limit: "50" });
  });
});

describe("back to the one list", () => {
  it("draws Recently updated flat again, as it was", async () => {
    await show();
    await browseBy("machine");
    await browseBy("recent");
    expect(sections()).toEqual([]);
    expect(host.querySelector("[data-virtual-sessions]")).not.toBeNull();
    expect(host.querySelector("[data-session-total]")!.textContent).toBe(t("paged-agents.count", { count: 7 }));
  });
});

describe("the chosen Browse by", () => {
  const chosen = () => [...host.querySelectorAll<HTMLButtonElement>(`[aria-label="${t("paged-agents.group_by")}"] [role="radio"]`)].find((b) => b.getAttribute("aria-checked") === "true")!.textContent;

  it("is kept in this browser: the list opens the way it was last read, and Recently updated forgets it", async () => {
    await show();
    await browseBy("project");
    act(() => root.unmount());
    root = createRoot(host);
    await show();
    await settle();
    expect(chosen()).toBe(t("paged-agents.project"));
    expect(sections().map((s) => heading(s).textContent)).toEqual(["app", "notes", "tool · Lab", t("paged-agents.no_folder")]);

    await browseBy("recent");
    act(() => root.unmount());
    root = createRoot(host);
    await show();
    expect(chosen()).toBe(t("paged-agents.recent"));
    expect(sections()).toEqual([]);
  });

  it("ignores a value that is not one of the list's own", async () => {
    localStorage.setItem("persona.agentBrowseBy", "repo");
    await show();
    expect(chosen()).toBe(t("paged-agents.recent"));
  });
});

describe("a section passes over hints about other sessions (#9176)", () => {
  const sectionReads = () => calls().filter((path) => { const q = params(path); return q.has("machine_id") && !q.has("counts") && !q.has("catalog"); }).length;
  async function changeHint(since: string, revision: string, changed: string[][]) {
    await act(async () => {
      for (const entry of [...hub.handlers]) if (entry.event === "sessions_list") (entry.fn as (msg: unknown) => void)({ since, revision, changed });
      await list.load(); // the root list reads on every hint (the store's), and with it the groups' numbers
      await vi.advanceTimersByTimeAsync(SESSION_LIST_METRICS.groupsRefresh + 2_100);
    });
  }

  it("reads an open section only for a hint that can concern its sessions, or when its heading's numbers change", async () => {
    await show();
    await browseBy("machine");
    const mac = sections().find((s) => summary(s).startsWith("MacBook"))!;
    expect(rowsIn(mac)).toEqual(["a1", "a2", "a3"]);
    const before = sectionReads();
    await changeHint("r", "r2", [["elsewhere", "zz"]]);
    expect(sectionReads()).toBe(before);
    // a session joins the section: no hint it could check names it, the heading's count does
    ROWS.push(row("a4", "mac", "/Users/me/notes"));
    try {
      await changeHint("r2", "r3", [["mac", "a4"]]);
      expect(sectionReads()).toBeGreaterThan(before);
      expect(rowsIn(sections().find((s) => summary(s).startsWith("MacBook"))!)).toEqual(["a1", "a2", "a3", "a4"]);
    } finally {
      ROWS.pop();
    }
  });
});
