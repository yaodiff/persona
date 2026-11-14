// @vitest-environment jsdom
// The sidebar's paged Agents list by to-do (#6662): the complete list's grouping bar, a section per open to-do read by
// `todo=<id>`, open, then "Not on a to-do" folded; no complete list is read. A hub without `sessions_todo` gets the
// complete list for By to-do, and the same bar.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { loadPages } from "../lib/lazy-page";
import { connect, reset } from "../lib/store";
import type { Todo } from "../lib/todos";
import type { Session } from "../lib/types";
import { Sidebar } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
class FakeSocket {
  static OPEN = 1;
  static all: FakeSocket[] = [];
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  constructor() { FakeSocket.all.push(this); }
  send() {}
  close() { this.readyState = 3; }
  open() { this.readyState = 1; this.onopen?.(); }
  push(msg: Record<string, unknown>) { this.onmessage?.({ data: JSON.stringify(msg) }); }
}
const at = (minute: number) => `2026-01-01T00:${String(minute).padStart(2, "0")}:00Z`;
const row = (id: string, minute: number, todo?: { id: string; title: string }, state: Session["state"] = "idle", extra: Partial<Session> = {}): Session =>
  ({ machine_id: "mac", session_id: id, title: id, cwd: "/code/app", status: state === "working" ? "working" : "idle", state, updated_at: at(minute), pane_id: `w${minute}:p1`, ...(todo ? { todo } : {}), ...extra }) as Session;
const SHIP = { id: "todo_ship", title: "Ship 1.3" }, REVIEW = { id: "todo_review", title: "Submit for review" };
const ROWS = [row("Widget tests", 9, SHIP, "working"), row("Release notes", 8, REVIEW), row("Screenshots", 7, REVIEW), row("Sort downloads", 6),
  row("Waiting on CI", 5, undefined, "working", { background_waiting: true }), row("Weekly numbers", 4, undefined, "needs_you", { pane_id: "botmux:x" })];
let rowsNow = ROWS;
const origin = (r: Session) => (r.pane_id?.startsWith("botmux:") ? "feishu" : "terminal");
const TODOS: Todo[] = [
  { id: SHIP.id, title: SHIP.title, created_at: at(1), updated_at: at(9), agents: [] },
  { id: REVIEW.id, title: REVIEW.title, created_at: at(2), updated_at: at(8), agents: [] },
];
const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
let host: HTMLDivElement, root: Root;
const flush = async () => { for (let i = 0; i < 12; i++) await act(async () => { await Promise.resolve(); }); };

async function render(features: string[]) {
  const fetch = vi.fn((input: string) => {
    const url = new URL(input, "http://hub.example.com");
    const q = url.searchParams;
    if (url.pathname.endsWith("/sessions")) {
      const todo = q.get("todo"), state = q.get("state"), waiting = q.get("waiting"), only = q.get("pane_source"), not = q.get("exclude_pane_source");
      const rows = rowsNow.filter((r) => (!todo || (todo === "none" ? !r.todo : r.todo?.id === todo)) && (!state || r.state === state) &&
        (!waiting || Boolean(r.background_waiting) === (waiting === "1")) && (!only || origin(r) === only) && (!not || origin(r) !== not));
      if (q.has("counts")) {
        const count = (key: (r: Session) => string) => rows.reduce<Record<string, number>>((m, r) => ({ ...m, [key(r)]: (m[key(r)] ?? 0) + 1 }), {});
        return json({ total: rows.length, state: count((r) => r.state!), machine: count((r) => r.machine_id), group: {}, project: {}, todo: count((r) => r.todo?.id ?? ""),
          pane_source: count(origin), background_waiting: rows.filter((r) => r.background_waiting).length, revision: "r1" });
      }
      return json({ sessions: q.has("since") ? [] : rows, remove: [], has_more: false, revision: "r1", total: rows.length });
    }
    if (url.pathname.endsWith("/pins")) return json({ sessions: [], spaces: [] });
    if (url.pathname.endsWith("/machines")) return json({ machines: [{ id: "mac", name: "MacBook", online: true }, { id: "linux", name: "Linux box", online: true }] });
    if (url.pathname.endsWith("/todos")) return json({ todos: TODOS });
    return json({});
  });
  vi.stubGlobal("fetch", fetch);
  act(() => {
    connect();
    const socket = FakeSocket.all.at(-1)!;
    socket.open();
    socket.push({ type: "hello", features });
  });
  await flush();
  act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
  await flush();
  return fetch;
}
const bar = () => host.querySelector('[role="radiogroup"][aria-label="Agent 分组"]');
const segments = () => [...bar()!.querySelectorAll("button")].map((b) => b.textContent);
const sections = () => [...host.querySelectorAll<HTMLElement>("[data-session-group]")];
const heading = (section: HTMLElement) => section.querySelector<HTMLButtonElement>("button[aria-expanded]")!;
const rowTexts = (section: HTMLElement) => [...section.querySelectorAll<HTMLElement>("[data-session-row] a")].map((a) => a.textContent);
const paths = (fetch: ReturnType<typeof vi.fn>) => fetch.mock.calls.map(([input]) => new URL(input as string, "http://hub.example.com")).filter((u) => u.pathname.endsWith("/api/v1/sessions"));

beforeEach(() => {
  rowsNow = ROWS;
  localStorage.clear();
  reset();
  FakeSocket.all = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  history.replaceState(null, "", "/");
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount()); host.remove(); reset(); vi.unstubAllGlobals();
});

beforeAll(() => loadPages(), 60_000);

describe("the sidebar's paged Agents by to-do", () => {
  it("draws each open to-do as an open section of its Agents, then Not on a to-do folded, under the complete list's bar", async () => {
    const fetch = await render(["sessions_list", "sessions_lookup", "sessions_todo"]);
    expect(host.querySelector("[data-paged-sessions]")).not.toBeNull();
    expect(segments()).toEqual(["待办", "机器", "仓库", "项目", "状态"]);
    expect(bar()!.querySelector('[aria-checked="true"]')!.textContent).toBe("待办");
    // the To-dos page's order (sortTodos), its titles
    expect(sections().map((s) => [heading(s).textContent, heading(s).getAttribute("aria-expanded")])).toEqual([
      ["Submit for review", "true"], ["Ship 1.3", "true"], ["没挂待办的", "false"],
    ]);
    const [review, ship, none] = sections();
    expect(rowTexts(ship)).toEqual([expect.stringMatching(/^Widget tests/)]);
    expect(rowTexts(review)).toEqual([expect.stringMatching(/^Release notes/), expect.stringMatching(/^Screenshots/)]);
    // folded, it reads nothing
    expect(rowTexts(none)).toEqual([]);
    const reads = paths(fetch);
    expect(reads.map((u) => u.searchParams.get("todo")).filter(Boolean).sort()).toEqual(["todo_review", "todo_ship"]);
    expect(reads.some((u) => u.searchParams.get("summary") === "0" || !u.search)).toBe(false);
  });

  it("reads the complete list for By to-do on a hub that cannot list sessions by to-do, with the same bar", async () => {
    const fetch = await render(["sessions_list", "sessions_lookup"]);
    expect(host.querySelector("[data-paged-sessions]")).toBeNull();
    expect(segments()).toEqual(["待办", "机器", "仓库", "项目", "状态"]);
    expect(paths(fetch).some((u) => u.searchParams.get("summary") === "0" || !u.search)).toBe(true);
    expect(host.textContent).toContain("Ship 1.3");
  });

  it("shows the complete list's quick filters where the hub selects them, and reads each chip as the complete list matches it", async () => {
    localStorage.setItem("persona.agentFilter", "working");
    const fetch = await render(["sessions_list", "sessions_lookup", "sessions_todo", "sessions_quick_filters"]);
    const chips = () => [...host.querySelectorAll<HTMLButtonElement>('[role="radiogroup"][aria-label="快速筛选"] button')];
    expect(chips().map((b) => b.getAttribute("aria-label"))).toEqual(["全部", "需要你 1", "工作中 1", "后台等待中 1", "飞书群 1", "终端 5"]);
    // the choice the complete list keeps: Working, without the one waiting in the background
    expect(chips().find((b) => b.getAttribute("aria-checked") === "true")!.textContent).toMatch(/^工作中/);
    const reads = () => paths(fetch).map((u) => u.searchParams);
    expect(reads().some((q) => q.get("state") === "working" && q.get("waiting") === "0")).toBe(true);
    await act(async () => chips().find((b) => b.textContent!.startsWith("飞书群"))!.click());
    await flush();
    expect(localStorage.getItem("persona.agentFilter")).toBe("feishu");
    expect(reads().some((q) => q.get("pane_source") === "feishu" && !q.has("state"))).toBe(true);
    // hiding botmux leaves its origin out of every read and goes back to All
    const botmux = host.querySelector<HTMLButtonElement>('button[aria-pressed]')!;
    expect(botmux.textContent).toContain("botmux");
    fetch.mockClear();
    await act(async () => botmux.click());
    await flush();
    expect(localStorage.getItem("persona.agentFilter")).toBe("all");
    expect(reads().length).toBeGreaterThan(0);
    expect(reads().every((q) => q.get("exclude_pane_source") === "feishu")).toBe(true);
    expect(chips().map((b) => b.getAttribute("aria-label"))).not.toContain("飞书群 1");
  });

  it("keeps the paged list's own state chips on a hub that cannot select the quick filters", async () => {
    await render(["sessions_list", "sessions_lookup", "sessions_todo"]);
    const chips = [...host.querySelectorAll('[role="radiogroup"][aria-label="快速筛选"] button')].map((b) => b.textContent);
    expect(chips).toContain("全部");
    expect(chips.some((c) => c!.startsWith("已看"))).toBe(true);
  });

  it("has the complete list's title row: fold all acts on the paged sections, the ⋯ holds its sorts and the archived Agents", async () => {
    await render(["sessions_list", "sessions_lookup", "sessions_todo", "agent_session_archives"]);
    const foldAll = () => host.querySelector<HTMLButtonElement>("[data-testid=agents-fold-all]")!;
    const open = () => sections().map((s) => heading(s).getAttribute("aria-expanded"));
    expect(open()).toEqual(["true", "true", "false"]);
    expect(foldAll().getAttribute("aria-label")).toBe("全部折叠");
    await act(async () => foldAll().click());
    await flush();
    expect(open()).toEqual(["false", "false", "false"]);
    expect(foldAll().getAttribute("aria-label")).toBe("全部展开");
    await act(async () => foldAll().click());
    await flush();
    expect(open()).toEqual(["true", "true", "true"]);
    // no archive icon of its own: the archived Agents are in the ⋯, as in the complete list
    expect(host.querySelector('[aria-label="已归档的 Agent"]')).toBeNull();
    const menu = host.querySelector<HTMLButtonElement>('button[aria-label="Agent 列表选项"]')!;
    await act(async () => menu.click());
    await flush();
    expect(menuItems()).toEqual(["状态优先", "最近更新", "已归档的 Agent"]);
  });

  it("orders the computers by their newest Agent under Recently updated, and offers no sort By status, as the complete list", async () => {
    rowsNow = [row("Nightly build", 10, undefined, "idle", { machine_id: "linux" }), ...ROWS];
    await render(["sessions_list", "sessions_lookup", "sessions_todo", "agent_session_archives"]);
    const choose = async (label: string) => {
      await act(async () => [...bar()!.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label)!.click());
      await flush();
    };
    const keys = () => sections().map((s) => s.getAttribute("data-session-group"));
    const menu = () => host.querySelector<HTMLButtonElement>('button[aria-label^="Agent 列表选项"]');
    await choose("机器");
    // State first: the busiest computer first
    expect(keys()).toEqual(["mac", "linux"]);
    await act(async () => menu()!.click());
    await flush();
    await act(async () => [...document.querySelectorAll<HTMLElement>('[role^="menuitem"]')].find((el) => el.textContent === "最近更新")!.click());
    await flush();
    expect(keys()).toEqual(["linux", "mac"]);
    expect(localStorage.getItem("persona.agentSort")).toBe("updated");
    await choose("状态");
    await act(async () => menu()!.click());
    await flush();
    expect(menuItems()).toEqual(["已归档的 Agent"]);
  });
});
const menuItems = () => [...document.querySelectorAll('[role^="menuitem"]')].map((el) => el.textContent);
