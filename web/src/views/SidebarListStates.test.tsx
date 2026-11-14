// @vitest-environment jsdom
// The Agents block when it has nothing to list because a read failed or the hub is out of reach (the web audit's items
// 4, 27 and 36), against the store: the complete list's failed read says why with Retry instead of leaving the list
// blank; By to-do whose to-dos cannot be read lists by status, saying why with Retry, instead of putting every Agent
// under one name; a hub not reached since the page opened is said, not "Loading…" for good.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { t } from "../i18n";
import { connect, getState, reconnectNow, reset } from "../lib/store";
import type { Todo } from "../lib/todos";
import type { Machine, Session } from "../lib/types";
import { Sidebar } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

class FakeSocket {
  static OPEN = 1;
  static all: FakeSocket[] = [];
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  constructor() {
    FakeSocket.all.push(this);
  }
  send() {}
  close() {
    this.readyState = 3;
  }
  open() {
    this.readyState = 1;
    this.onopen?.();
  }
  push(msg: Record<string, unknown>) {
    this.onmessage?.({ data: JSON.stringify(msg) });
  }
  drop() {
    this.readyState = 3;
    this.onclose?.({ code: 1006 });
  }
}
const last = () => FakeSocket.all[FakeSocket.all.length - 1];

const mac: Machine = { id: "mac", name: "工作电脑", online: true };
const agent: Session = { machine_id: "mac", session_id: "s1", title: "改发布脚本", cwd: "/code/weather", status: "working", state: "working", updated_at: "2026-11-05T00:00:00Z" };
const todo: Todo = { id: "td1", title: "天气 App 发 1.3 版", created_at: "2026-11-05T00:00:00Z", updated_at: "2026-11-05T00:00:00Z", agents: [{ machine_id: "mac", session_id: "s1", linked_at: "2026-11-05T00:00:00Z" }] };

const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));
const failed = () => json({ error: "boom" }, 500);
/** The hub's answers: `/todos` and every `/sessions` read (the store's and the complete list's) as given. */
const hub = (o: { todos?: () => Promise<Response>; sessions?: () => Promise<Response>; pinned?: boolean }) =>
  vi.fn((url: string) => {
    const path = new URL(url, "http://hub").pathname;
    if (path === "/api/v1/todos") return (o.todos ?? (() => json({ todos: [todo] })))();
    if (path === "/api/v1/sessions") return (o.sessions ?? (() => json({ sessions: [agent], total: 1, has_more: false, revision: "r1" })))();
    if (path === "/api/v1/machines") return json({ machines: [mac] });
    // a pin keeps the complete list on a paging hub (lib/sidebar-api.ts pagedAgentList)
    if (path === "/api/v1/pins" && o.pinned) return json({ sessions: ["mac/s1"], spaces: [] });
    return json({});
  });

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); await new Promise((r) => setTimeout(r, 0)); });
};
const grouping = () => host.querySelector<HTMLElement>('[role="radiogroup"][aria-label="Agent 分组"]')!;
const checked = () => [...grouping().querySelectorAll('[role="radio"]')].find((b) => b.getAttribute("aria-checked") === "true")?.getAttribute("aria-label");
const agentsBlock = () => grouping().closest<HTMLElement>("[data-sidebar-agents]")!;
const alerts = () => [...agentsBlock().querySelectorAll('[role="alert"]')].map((el) => el.textContent);
const retryButton = () => [...agentsBlock().querySelectorAll("button")].find((b) => b.textContent === t("sidebar.retry"));
const render = async () => {
  act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
  await flush();
};
/** Connects, says hello with `features` and pushes the computer and its agent. */
const online = (features: string[] = []) => {
  connect();
  const sock = last();
  sock.open();
  sock.push({ type: "hello", server_version: "v1", features });
  sock.push({ type: "machines", machines: [mac] });
  sock.push({ type: "sessions", machine_id: "mac", sessions: [agent] });
};

beforeEach(() => {
  localStorage.clear();
  reset();
  FakeSocket.all = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  // the reads this test started answer before the next one starts
  await flush();
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
});

describe("a hub not reached since the page opened (audit item 36)", () => {
  it("says so in the Agents list instead of Loading… for good", async () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>(() => {})));
    connect();
    await render();
    // the first try: still loading, not offline yet
    expect(agentsBlock().textContent).toContain(t("sidebar.loading"));
    for (let i = 0; i < 3; i++) {
      await act(async () => last().drop());
      if (i < 2) act(() => reconnectNow());
    }
    await flush();
    expect(getState().conn).toBe("offline");
    expect(agentsBlock().textContent).toContain(t("sidebar.agents_offline"));
    expect(agentsBlock().textContent).not.toContain(t("sidebar.loading"));
  });
});

describe("the complete Agents list's read fails (audit item 4)", () => {
  it("says it could not read the list, why, and Retry reads it again, instead of a blank list", async () => {
    let sessions = failed;
    vi.stubGlobal("fetch", hub({ sessions: () => sessions(), pinned: true }));
    online(["sessions_list"]);
    await render();
    expect(getState().pins.sessions).toEqual(["mac/s1"]);
    const [said] = alerts();
    expect(said).toContain(t("sidebar.agents_read_failed", { error: "" }).replace(/[:：]\s*$/, ""));
    expect(said).toMatch(/500/);
    expect(agentsBlock().textContent).not.toContain(t("sidebar.loading"));
    sessions = () => json({ sessions: [agent], total: 1, has_more: false, revision: "r1" });
    await act(async () => retryButton()!.click());
    await flush();
    expect(alerts()).toEqual([]);
    expect(agentsBlock().textContent).toContain("改发布脚本");
  });
});

describe("By to-do whose to-dos cannot be read (audit item 27)", () => {
  it("lists by status for now, saying why with Retry, and goes back to By to-do once they are read", async () => {
    let todos = failed;
    vi.stubGlobal("fetch", hub({ todos: () => todos() }));
    online();
    await render();
    expect(getState().todosError).not.toBeNull();
    // not every Agent under one name: by status, the stored choice kept
    expect(checked()).toBe("按状态");
    expect(localStorage.getItem("persona.agentGrouping")).toBeNull();
    const [said] = alerts();
    expect(said).toContain(t("sidebar.todos_read_failed", { error: "" }).replace(/[:：]\s*$/, ""));
    expect(said).toMatch(/500/);
    expect(agentsBlock().textContent).toContain("改发布脚本");
    expect(agentsBlock().textContent).not.toContain("没挂待办的");
    todos = () => json({ todos: [todo] });
    await act(async () => retryButton()!.click());
    await flush();
    expect(alerts()).toEqual([]);
    expect(checked()).toBe("按待办");
    expect(agentsBlock().textContent).toContain("天气 App 发 1.3 版");
  });

  it("says the same on a hub that pages its Agents list by to-do", async () => {
    vi.stubGlobal("fetch", vi.fn((url: string) => {
      const path = new URL(url, "http://hub").pathname;
      if (path === "/api/v1/todos") return failed();
      if (path === "/api/v1/sessions") return json({ sessions: [agent], total: 1, has_more: false, revision: "r1", counts: { total: 1, revision: "r1", state: { working: 1 }, machine: { mac: 1 }, project: {}, group: {}, todo: { td1: 1 } } });
      if (path === "/api/v1/machines") return json({ machines: [mac] });
      return json({});
    }));
    online(["sessions_list", "sessions_todo"]);
    await render();
    expect(host.querySelector("[data-paged-sessions]")).not.toBeNull();
    expect(checked()).toBe("按状态");
    expect(alerts().some((a) => a?.includes(t("sidebar.todos_read_failed", { error: "" }).replace(/[:：]\s*$/, "")))).toBe(true);
  });
});

describe("a list drawn while the socket tries again after giving up (audit item 36)", () => {
  it("says the hub is out of reach, as the page's Offline does, not Loading…", async () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>(() => {})));
    connect();
    for (let i = 0; i < 3; i++) {
      await act(async () => last().drop());
      if (i < 2) act(() => reconnectNow());
    }
    expect(getState().conn).toBe("offline");
    // a socket that opens and closes before the hub says hello: connecting again, and the page stays Offline; a list
    // drawn now (the phone's drawer opened) reads the page's flag
    act(() => reconnectNow());
    await act(async () => { last().open(); last().drop(); });
    expect(getState().conn).toBe("connecting");
    await render();
    expect(agentsBlock().textContent).toContain(t("sidebar.agents_offline"));
    expect(agentsBlock().textContent).not.toContain(t("sidebar.loading"));
  });
});

describe("the paged Agents list's read fails (audit item 4)", () => {
  it("says it could not read the Agents list, as the complete list does, with Retry", async () => {
    let sessions = failed;
    vi.stubGlobal("fetch", hub({ sessions: () => sessions() }));
    online(["sessions_list", "sessions_todo"]);
    await render();
    expect(host.querySelector("[data-paged-sessions]")).not.toBeNull();
    const said = alerts();
    expect(said.length).toBeGreaterThan(0);
    for (const a of said) expect(a).toContain(t("sidebar.agents_read_failed", { error: "" }).replace(/[:：]\s*$/, ""));
    expect(said.some((a) => /500/.test(a ?? ""))).toBe(true);
    sessions = () => json({ sessions: [agent], total: 1, has_more: false, revision: "r1", counts: { total: 1, revision: "r1", state: { working: 1 }, machine: { mac: 1 }, project: {}, group: {}, todo: { td1: 1 } } });
    await act(async () => retryButton()!.click());
    await flush();
    expect(alerts()).toEqual([]);
  });
});

describe("the chat list needs no to-dos (the Filter menu's chat rows)", () => {
  const chatRows = () => host.querySelector('[data-testid="agent-chatlist-section"]');

  it("draws its rows at once, while GET /todos has not answered", async () => {
    localStorage.setItem("persona.agentChatList", "1");
    vi.stubGlobal("fetch", hub({ todos: () => new Promise<Response>(() => {}) }));
    online();
    await render();
    expect(getState().todosLoaded).toBe(false);
    expect(chatRows()?.textContent).toContain("改发布脚本");
    expect(agentsBlock().textContent).not.toContain(t("sidebar.loading"));
  });

  it("says nothing of to-dos it could not read: its rows are not listed by status", async () => {
    localStorage.setItem("persona.agentChatList", "1");
    vi.stubGlobal("fetch", hub({ todos: failed }));
    online();
    await render();
    expect(getState().todosError).not.toBeNull();
    expect(chatRows()?.textContent).toContain("改发布脚本");
    expect(alerts()).toEqual([]);
  });
});

describe("By to-do's to-dos read again from the grouping bar (audit item 27)", () => {
  it("reads them when 按待办 is tapped while they cannot be read, as the note's Retry does", async () => {
    let todos = failed;
    vi.stubGlobal("fetch", hub({ todos: () => todos() }));
    online();
    await render();
    expect(checked()).toBe("按状态");
    todos = () => json({ todos: [todo] });
    await act(async () => grouping().querySelector<HTMLElement>('[role="radio"][aria-label="按待办"]')!.click());
    await flush();
    expect(alerts()).toEqual([]);
    expect(checked()).toBe("按待办");
    expect(agentsBlock().textContent).toContain("天气 App 发 1.3 版");
  });
});

describe("By to-do's groups say what their rows say (audit item 48)", () => {
  it("says 未运行 beside Recent (not running) and 完成未看 beside agents on no to-do finished and not looked at", async () => {
    const stopped: Session = { machine_id: "mac", session_id: "s2", title: "旧的迁移", cwd: "/code/old", status: "offline", state: "offline", updated_at: "2026-11-04T00:00:00Z" };
    const unseen: Session = { machine_id: "mac", session_id: "s3", title: "写周报", cwd: "/code/notes", status: "idle", state: "unread", updated_at: "2026-11-05T00:00:00Z" };
    const every = [agent, stopped, unseen];
    vi.stubGlobal("fetch", hub({ sessions: () => json({ sessions: every, total: every.length, has_more: false, revision: "r1" }) }));
    connect();
    const sock = last();
    sock.open();
    sock.push({ type: "hello", server_version: "v1", features: [] });
    sock.push({ type: "machines", machines: [mac] });
    sock.push({ type: "sessions", machine_id: "mac", sessions: every });
    await render();
    expect(checked()).toBe("按待办");
    const end = (key: string) => host.querySelector(`[data-tree-branch="${key}"] [data-tree-branch-row]`)?.textContent;
    expect(end("recent")).toMatch(/1 个 Agent未运行$/);
    expect(end("todo:none")).toMatch(/1 个 Agent完成未看$/);
  });
});
