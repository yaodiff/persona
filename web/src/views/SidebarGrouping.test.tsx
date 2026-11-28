// @vitest-environment jsdom
// The Agents block's grouping (#2812) against the store: By to-do waits for the first GET /todos instead of drawing every
// agent under No to-do and moving it, and a hub without todos hides To-dos and shows By machine while keeping the choice.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { seedAgentsDisplay } from "../lib/agents-display";
const v2TreeLoaded = vi.hoisted(() => vi.fn());
vi.mock("../features/todos/board-sidebar-tree", async (load) => {
  v2TreeLoaded();
  return load();
});
// jsdom has no matchMedia (the sidebar's modules read it when they load) and no ResizeObserver (its highlight)
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { t } from "../i18n";
import { filterAgentList, resetAgentListFilters } from "../lib/agent-list-filters";
import { connect, getState, patchAssistant, refresh, reset, upsertSession } from "../lib/store";
import type { Todo } from "../lib/todos";
import type { Machine, Session } from "../lib/types";
import { Icon, type IconName } from "../ui/icons";
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
}

const mac: Machine = { id: "mac", name: "工作电脑", online: true };
const agent: Session = { machine_id: "mac", session_id: "s1", title: "改发布脚本", cwd: "/code/weather", status: "working", state: "working" };
const todo: Todo = { id: "td1", title: "天气 App 发 1.3 版", created_at: "2026-11-05T00:00:00Z", updated_at: "2026-11-05T00:00:00Z", agents: [{ machine_id: "mac", session_id: "s1", linked_at: "2026-11-05T00:00:00Z" }] };

const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));
/** fetch for a hub with `mac` and its one agent; GET /todos answers with `todos`. */
const hub = (todos: () => Promise<Response>) =>
  vi.fn((url: string) => {
    if (new URL(url, "http://hub").pathname.endsWith("/api/v1/todos")) return todos();
    if (url.endsWith("/api/v1/sessions")) return json({ sessions: [agent] });
    if (url.endsWith("/api/v1/machines")) return json({ machines: [mac] });
    return json({});
  });

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  await act(async () => { await vi.dynamicImportSettled(); });
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const grouping = () => host.querySelector<HTMLElement>('[role="radiogroup"][aria-label="Agent 分组"]')!;
const segments = () => [...grouping().querySelectorAll<HTMLButtonElement>('[role="radio"]')];
const checked = () => segments().find((b) => b.getAttribute("aria-checked") === "true")?.getAttribute("aria-label");
// the Agents block's body: the segments, the quick filter and the groups (the personal agents' Loading... is elsewhere)
const agentsBlock = () => grouping().closest<HTMLElement>("[data-sidebar-agents]")!;
const render = async () => {
  act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
  await flush();
};

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  reset();
  // Settings › Agents: the Filter is on for these tests (it is off until the hub says so)
  seedAgentsDisplay({ show_botmux: true, configured: true, show_filter: true });
  resetAgentListFilters();
  FakeSocket.all = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  history.replaceState(null, "", "/");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  resetAgentListFilters();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("按待办 before the todos have loaded", () => {
  it("loads the v2 sidebar chunk only when the Lab and By to-do are both active", async () => {
    vi.stubGlobal("fetch", hub(() => json({ todos: [todo] })));
    await refresh();
    Object.assign(getState(), { assistant: { machine_id: "mac", conversations: [], todos_v2: false } });
    await render();
    const choose = (label: string) => segments().find((b) => b.getAttribute("aria-label") === label)!.click();
    expect(checked()).toBe("按待办");
    expect(v2TreeLoaded).not.toHaveBeenCalled();

    await act(async () => choose("按机器"));
    await act(async () => patchAssistant({ todos_v2: true }));
    await flush();
    expect(v2TreeLoaded).not.toHaveBeenCalled();

    await act(async () => choose("按待办"));
    await flush();
    expect(v2TreeLoaded).toHaveBeenCalledTimes(1);
    expect(agentsBlock().querySelector('[data-tree-branch="todo:td1"]')).not.toBeNull();
  });

  it("says 加载中… until GET /todos answers, then draws the agent under its todo, never under 没挂待办的 first", async () => {
    let answer!: (r: Response) => void;
    const todos = new Promise<Response>((r) => (answer = r));
    vi.stubGlobal("fetch", hub(() => todos));
    // the socket pushes the machines and sessions while GET /todos (asked for on hello) is still on its way
    connect();
    const sock = FakeSocket.all[FakeSocket.all.length - 1];
    sock.open();
    sock.push({ type: "hello", server_version: "v1" });
    sock.push({ type: "machines", machines: [mac] });
    sock.push({ type: "sessions", machine_id: "mac", sessions: [agent] });
    await render();
    expect(checked()).toBe("按待办");
    expect(agentsBlock().textContent).toContain("加载中…");
    expect(agentsBlock().textContent).not.toContain("未关联待办");
    expect(agentsBlock().textContent).not.toContain("改发布脚本");

    await act(async () => answer(new Response(JSON.stringify({ todos: [todo] }), { status: 200 })));
    await flush();
    expect(agentsBlock().textContent).not.toContain("加载中…");
    expect(agentsBlock().textContent).toContain("天气 App 发 1.3 版");
    expect(agentsBlock().textContent).toContain("改发布脚本");
    // the agent sits under its to-do; 没挂待办的 is listed (#6458) and holds none
    expect(agentsBlock().querySelector('[data-tree-branch="todo:td1"]')?.textContent).toContain("改发布脚本");
    expect(agentsBlock().querySelector('[data-tree-branch="todo:none"] [data-tree-branch-row]')?.textContent).toMatch(/^未关联待办.*0 个 Agent$/);
    expect(agentsBlock().querySelector('[data-tree-branch="todo:none"]')?.textContent).not.toContain("改发布脚本");
  });

  it("draws the todos as soon as GET /todos answers, while GET /sessions is still on its way", async () => {
    const slow = vi.fn((url: string) => {
      if (new URL(url, "http://hub").pathname.endsWith("/api/v1/todos")) return json({ todos: [todo] });
      if (url.endsWith("/api/v1/sessions")) return new Promise<Response>(() => {});
      if (url.endsWith("/api/v1/machines")) return json({ machines: [mac] });
      return json({});
    });
    vi.stubGlobal("fetch", slow);
    connect();
    const sock = FakeSocket.all[FakeSocket.all.length - 1];
    sock.open();
    sock.push({ type: "hello", server_version: "v1" });
    sock.push({ type: "machines", machines: [mac] });
    sock.push({ type: "sessions", machine_id: "mac", sessions: [agent] });
    await render();
    expect(agentsBlock().textContent).not.toContain("加载中…");
    expect(agentsBlock().textContent).toContain("天气 App 发 1.3 版");
    expect(agentsBlock().textContent).toContain("改发布脚本");
  });

  it("does not wait under another grouping", async () => {
    localStorage.setItem("persona.agentGrouping", "machine");
    vi.stubGlobal("fetch", hub(() => new Promise<Response>(() => {})));
    connect();
    const sock = FakeSocket.all[FakeSocket.all.length - 1];
    sock.open();
    sock.push({ type: "hello", server_version: "v1" });
    sock.push({ type: "machines", machines: [mac] });
    sock.push({ type: "sessions", machine_id: "mac", sessions: [agent] });
    await render();
    expect(checked()).toBe("按机器");
    expect(agentsBlock().textContent).not.toContain("加载中…");
    expect(agentsBlock().textContent).toContain("工作电脑");
  });
});

describe("group controls in every Agents view", () => {
  it.each(["todo", "machine", "repo", "space", "state"])("opens and folds all levels in %s without folding the block", async (mode) => {
    localStorage.setItem("persona.agentGrouping", mode);
    vi.stubGlobal("fetch", hub(() => json({ todos: [todo] })));
    await refresh();
    await render();
    const action = (label: string) => host.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`)!;
    const disclosures = () => [...agentsBlock().querySelectorAll<HTMLButtonElement>("[data-sidebar-group-heading] button[aria-expanded]")].filter((b) => !b.closest("[data-sidebar-agents-controls]"));
    expect(disclosures().length).toBeGreaterThan(0);
    expect(host.querySelectorAll("[data-sidebar-agents-controls] [data-testid=agents-fold-all]")).toHaveLength(1);
    await act(async () => { action("全部折叠").click(); await Promise.resolve(); });
    expect(disclosures().every((b) => b.getAttribute("aria-expanded") === "false")).toBe(true);
    expect(grouping().closest("[inert]")).toBeNull();
    await flush();
    await act(async () => { action("全部展开").click(); await Promise.resolve(); });
    expect(disclosures().every((b) => b.getAttribute("aria-expanded") === "true")).toBe(true);
    await flush();
    const first = disclosures()[0]!;
    await act(async () => { action("全部折叠").click(); await Promise.resolve(); });
    await flush();
    await act(async () => { first.click(); await Promise.resolve(); });
    await flush();
    expect(first.getAttribute("aria-expanded")).toBe("true");
    expect(action("全部折叠")).not.toBeNull();
    await act(async () => { action("全部折叠").click(); await Promise.resolve(); });
    expect(first.getAttribute("aria-expanded")).toBe("false");
  });
});

// Collapse all / Expand all act on the list under the controls only (#6447): the grouping tabs, the quick filters and
// the title row's Filter (#6589: the list has no search field of its own) stay on screen while every group folds to its
// heading; the chat-list view has no groups to fold to, so it has no button (a first fix folded the whole block there,
// the controls with it).
describe("the fold-all button keeps the Agents controls", () => {
  /** On screen: not inside an inert box or a closed Collapse (0fr), the two ways the sidebar folds. */
  const onScreen = (el: Element | null | undefined) => {
    for (let e: Element | null = el ?? null; e; e = e.parentElement) {
      if (e.hasAttribute("inert") || (e as HTMLElement).style?.gridTemplateRows === "0fr") return false;
    }
    return Boolean(el);
  };
  const controls = () => ({
    tabs: onScreen(grouping()),
    filters: onScreen(agentsBlock().querySelector(`[role="radiogroup"][aria-label="${t("sidebar.quick_filters")}"]`)),
    filter: onScreen(agentsBlock().querySelector("[data-sidebar-agents-controls] [data-testid=agent-filter-button]")),
  });
  const everyControl = { tabs: true, filters: true, filter: true };
  /** The agent's title, wherever the grouping draws it. */
  const agentShown = () => {
    const walk = document.createTreeWalker(agentsBlock(), NodeFilter.SHOW_TEXT);
    for (let n = walk.nextNode(); n; n = walk.nextNode()) if (n.textContent?.includes(agent.title!) && onScreen(n.parentElement)) return true;
    return false;
  };
  const headings = () => [...agentsBlock().querySelectorAll<HTMLButtonElement>("[data-sidebar-group-heading] button[aria-expanded]")]
    .filter((b) => !b.closest("[data-sidebar-agents-controls]") && onScreen(b));
  const foldAll = () => host.querySelector<HTMLButtonElement>("[data-sidebar-agents-controls] [data-testid=agents-fold-all]");
  /** The glyph an icon draws, and the one the button shows: corners in (Collapse all) or out (Expand all). */
  const glyph = (name: IconName) => renderToStaticMarkup(<Icon name={name} />).match(/ d="([^"]+)"/)?.[1];
  const shows = () => foldAll()?.querySelector("path")?.getAttribute("d");
  const press = async () => {
    await act(async () => { foldAll()!.click(); await Promise.resolve(); });
    await flush();
  };

  it.each(["todo", "machine", "repo", "space", "state"])("folds %s to its group headings and opens it again, the controls staying", async (mode) => {
    localStorage.setItem("persona.agentGrouping", mode);
    vi.stubGlobal("fetch", hub(() => json({ todos: [todo] })));
    await refresh();
    await render();
    expect(controls()).toEqual(everyControl);
    expect(agentShown()).toBe(true);
    expect(foldAll()!.getAttribute("aria-label")).toBe("全部折叠");
    expect(shows()).toBe(glyph("foldToggle"));

    await press();
    expect(controls()).toEqual(everyControl);
    expect(headings().length).toBeGreaterThan(0);
    expect(headings().every((b) => b.getAttribute("aria-expanded") === "false")).toBe(true);
    expect(agentShown()).toBe(false);
    // by to-do, each to-do is one row, its agents folded under it (no project or repository level above it, #6458)
    if (mode === "todo") expect(agentsBlock().querySelector('[data-tree-branch="todo:td1"] [data-tree-branch-row]')?.textContent).toContain(todo.title);
    if (mode === "todo") expect(onScreen(agentsBlock().querySelector('[data-tree-branch="todo:td1"] [data-tree-branch-row]'))).toBe(true);
    expect(foldAll()!.getAttribute("aria-label")).toBe("全部展开");
    expect(shows()).toBe(glyph("unfoldToggle"));

    await press();
    expect(controls()).toEqual(everyControl);
    expect(headings().every((b) => b.getAttribute("aria-expanded") === "true")).toBe(true);
    expect(agentShown()).toBe(true);
    expect(shows()).toBe(glyph("foldToggle"));
  });

  it("has no button in the chat-list view, whose rows stay under the controls", async () => {
    localStorage.setItem("persona.agentGrouping", "machine");
    localStorage.setItem("persona.agentChatList", "1");
    vi.stubGlobal("fetch", hub(() => json({ todos: [todo] })));
    await refresh();
    await render();
    expect(host.querySelector("[data-testid=agent-chatlist-section]")).not.toBeNull();
    expect(foldAll()).toBeNull();
    expect(controls()).toEqual(everyControl);
    expect(agentShown()).toBe(true);
  });
});

describe("按待办's 最近（未运行）", () => {
  it("has no ⓘ, its name saying what it holds, and is listed only with something in it (#6447)", async () => {
    localStorage.setItem("persona.agentGrouping", "todo");
    const gone: Session = { ...agent, session_id: "s2", title: "旧的发布检查", status: "offline", state: "offline" };
    let sessions = [agent, gone];
    vi.stubGlobal("fetch", vi.fn((url: string) => {
      if (new URL(url, "http://hub").pathname.endsWith("/api/v1/todos")) return json({ todos: [todo] });
      if (url.endsWith("/api/v1/sessions")) return json({ sessions });
      if (url.endsWith("/api/v1/machines")) return json({ machines: [mac] });
      return json({});
    }));
    await refresh();
    await render();
    const about = (branch: string) => agentsBlock().querySelector(`[data-tree-branch="${branch}"] [data-tree-branch-row] [aria-label^="${t("ui-atoms.about", { name: "" }).slice(0, 2)}"]`);
    const recent = () => agentsBlock().querySelector('[data-tree-branch="recent"] [data-tree-branch-row]');
    expect(recent()?.textContent).toContain(t("sidebar.recent"));
    expect(about("recent")).toBeNull();
    // the groups whose names do not say it keep theirs
    expect(about("todo:none")).not.toBeNull();

    // the same hub with nothing stopped: no 最近（未运行） row at all
    act(() => root.unmount());
    reset();
    root = createRoot(host);
    sessions = [agent];
    await refresh();
    await render();
    expect(recent()).toBeNull();
    expect(agentsBlock().querySelector('[data-tree-branch="todo:none"]')).not.toBeNull();
  });
});

describe("a hub without todos (GET /todos 404)", () => {
  it("hides 待办 and shows 按机器 for a browser that never chose", async () => {
    vi.stubGlobal("fetch", hub(() => json({ error: "not found" }, 404)));
    await refresh();
    await render();
    expect(segments().map((b) => b.textContent)).toEqual(["机器", "仓库", "项目", "状态"]);
    expect(checked()).toBe("按机器");
    expect(agentsBlock().textContent).toContain("工作电脑");
    expect(agentsBlock().textContent).not.toContain("加载中…");
    expect(localStorage.getItem("persona.agentGrouping")).toBeNull();
  });

  it("keeps a 按待办 chosen before and shows it again once the hub has todos", async () => {
    localStorage.setItem("persona.agentGrouping", "todo");
    vi.stubGlobal("fetch", hub(() => json({ error: "not found" }, 404)));
    await refresh();
    await render();
    expect(checked()).toBe("按机器");
    expect(localStorage.getItem("persona.agentGrouping")).toBe("todo");

    vi.stubGlobal("fetch", hub(() => json({ todos: [todo] })));
    await act(async () => { await refresh(); });
    await flush();
    expect(segments()[0].getAttribute("aria-label")).toBe("按待办");
    expect(checked()).toBe("按待办");
    expect(agentsBlock().textContent).toContain("天气 App 发 1.3 版");
  });
});

describe("relative-time Agents filters", () => {
  it("expires a session as the since window passes", async () => {
    vi.useFakeTimers();
    const start = new Date("2026-11-15T12:00:00Z");
    vi.setSystemTime(start);
    const aging = { ...agent, updated_at: new Date(start.getTime() - 24 * 60 * 60 * 1000 + 30_000).toISOString() };
    vi.stubGlobal("fetch", vi.fn((url: string) => {
      if (new URL(url, "http://hub").pathname.endsWith("/api/v1/todos")) return json({ todos: [todo] });
      if (url.endsWith("/api/v1/sessions")) return json({ sessions: [aging] });
      if (url.endsWith("/api/v1/machines")) return json({ machines: [mac] });
      return json({});
    }));
    filterAgentList("since:24h");
    await refresh();
    await render();
    expect(agentsBlock().textContent).toContain(agent.title);
    expect(agentsBlock().textContent).not.toContain(t("sidebar.no_match"));

    await act(async () => { vi.advanceTimersByTime(61_000); await Promise.resolve(); });
    await flush();
    expect(agentsBlock().textContent).not.toContain(agent.title);
    expect(agentsBlock().textContent).toContain(t("sidebar.no_match"));
  });
});

describe("stable machine, workspace and tab order (#11801)", () => {
  it("ignores activity during hover, focus, background updates and reloads", async () => {
    localStorage.setItem("persona.agentGrouping", "machine");
    // A browser that used the removed activity sort gets the same stable placement.
    localStorage.setItem("persona.agentSort", "updated");
    const placed = (id: string, machine_id: string, space: string, slot_index: number, hour: number): Session => ({
      machine_id, session_id: id, title: id, cwd: `/${space}`, status: "idle", state: "idle", updated_at: `2026-01-01T0${hour}:00:00Z`,
      space_id: space, space_name: space, space_index: space === "alpha" ? 0 : 1, slot_id: id, slot_index,
    });
    const sessions = [
      placed("a1", "mac", "alpha", 0, 1), placed("a2", "mac", "alpha", 1, 2),
      placed("b1", "mac", "beta", 0, 3), placed("c1", "pc", "gamma", 0, 4),
    ];
    const computers: Machine[] = [{ id: "mac", name: "Mac", online: true }, { id: "pc", name: "PC", online: true }];
    vi.stubGlobal("fetch", vi.fn((url: string) => {
      const path = new URL(url, "http://hub.example.com").pathname;
      const body = path.endsWith("/sessions") ? { sessions } : path.endsWith("/machines") ? { machines: computers } : {};
      return Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
    }));
    await refresh();
    await render();
    const order = () => [...agentsBlock().querySelectorAll<HTMLAnchorElement>('a[href^="/t/"]')].map((a) => a.getAttribute("href"));
    const expected = ["/t/mac/alpha/a1", "/t/mac/alpha/a2", "/t/mac/beta/b1", "/t/pc/gamma/c1"];
    expect(order()).toEqual(expected);

    const hovered = agentsBlock().querySelector<HTMLAnchorElement>('a[href="/t/mac/alpha/a1"]')!;
    act(() => {
      hovered.dispatchEvent(new MouseEvent("mouseover", { bubbles: true }));
      hovered.focus();
      upsertSession({ ...sessions[0]!, state: "needs_you", status: "idle", updated_at: "2026-01-01T10:00:00Z" });
    });
    await flush();
    expect(order()).toEqual(expected);
    expect(document.activeElement).toBe(hovered);

    act(() => root.unmount());
    reset();
    root = createRoot(host);
    await refresh();
    await render();
    expect(order()).toEqual(expected);
  });

  it("keeps quieted workspaces active across focus and reload without hiding new activity in Idle", async () => {
    localStorage.setItem("persona.agentGrouping", "space");
    const placed = (id: string, space: string, state: "working" | "idle" | "needs_you"): Session => ({
      machine_id: "mac", session_id: id, title: id, cwd: `/${space}`, status: state === "needs_you" ? "idle" : state, state,
      updated_at: "2026-01-01T01:00:00Z", space_id: space, space_name: space, space_index: space === "alpha" ? 0 : 1,
      slot_id: `${space}-tab`, slot_index: 0,
    });
    let sessions = [placed("a1", "alpha", "working"), placed("b1", "beta", "idle")];
    vi.stubGlobal("fetch", vi.fn((url: string) => {
      const path = new URL(url, "http://hub.example.com").pathname;
      const body = path.endsWith("/sessions") ? { sessions } : path.endsWith("/machines") ? { machines: [mac] } : {};
      return Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
    }));
    await refresh();
    await render();
    const headings = () => [...agentsBlock().querySelectorAll<HTMLButtonElement>("[data-sidebar-group-heading] button[aria-expanded]")];
    const heading = (name: string) => headings().find((button) => button.textContent?.includes(name));
    const idleName = () => t("sidebar.idle_spaces", { count: 1 });
    expect(heading("alpha")).toBeDefined();
    expect(heading("beta")).toBeUndefined();
    expect(heading(idleName())).toBeDefined();

    // A dimension filter temporarily unfolds every match. Clearing it restores the membership, not the filtered display.
    act(() => filterAgentList("machine:mac"));
    await flush();
    expect(heading("beta")).toBeDefined();
    expect(heading(idleName())).toBeUndefined();
    act(() => filterAgentList(""));
    await flush();
    expect(heading("alpha")).toBeDefined();
    expect(heading("beta")).toBeUndefined();
    expect(heading(idleName())).toBeDefined();

    await act(async () => heading(idleName())!.click());
    await flush();
    expect(agentsBlock().querySelector('a[href="/s/mac/b1"]')).not.toBeNull();

    // Stored membership does not follow an ordinary Idle → working transition.
    sessions = [sessions[0]!, placed("b1", "beta", "working")];
    act(() => upsertSession(sessions[1]!));
    await flush();
    expect(heading("beta")).toBeUndefined();
    expect(heading(idleName())).toBeDefined();
    expect(agentsBlock().querySelector('a[href="/s/mac/b1"] [data-state="working"]')).not.toBeNull();

    const alpha = agentsBlock().querySelector<HTMLAnchorElement>('a[href="/s/mac/a1"]')!;
    act(() => {
      alpha.dispatchEvent(new MouseEvent("mouseover", { bubbles: true }));
      alpha.focus();
      sessions = [placed("a1", "alpha", "idle"), placed("b1", "beta", "needs_you")];
      upsertSession(sessions[0]!);
      upsertSession(sessions[1]!);
    });
    await flush();
    expect(heading("alpha")).toBeDefined();
    expect(heading("beta")).toBeDefined();
    expect(heading(idleName())).toBeUndefined();
    expect(agentsBlock().querySelector<HTMLAnchorElement>('a[href="/s/mac/a1"]')).toBe(alpha);
    expect(alpha.querySelector('[data-state="idle"]')).not.toBeNull();
    expect(agentsBlock().querySelector('a[href="/s/mac/b1"] [data-state="needs_you"]')).not.toBeNull();
    expect(document.activeElement).toBe(alpha);

    // Reload reconstructs the same membership: alpha stays active although quiet, beta stays visible above Idle.
    act(() => root.unmount());
    reset();
    root = createRoot(host);
    await render(); // reload draws the empty store before GET /sessions answers
    await act(async () => { await refresh(); });
    await flush();
    expect(heading("alpha")).toBeDefined();
    expect(heading("beta")).toBeDefined();
    expect(heading(idleName())).toBeUndefined();
    expect(agentsBlock().querySelector('a[href="/s/mac/b1"] [data-state="needs_you"]')).not.toBeNull();
  });

  it("distinguishes stored empty membership from a new tab and admits only attention from Idle", async () => {
    localStorage.setItem("persona.agentGrouping", "space");
    sessionStorage.setItem("persona.agentActiveSpaces", "[]");
    const names = ["alpha", "beta", "gamma", "delta"];
    const placed = (id: string, space: string, patch: Partial<Session> = {}): Session => ({
      machine_id: "mac", session_id: id, title: id, cwd: `/${space}`, status: "idle", state: "idle",
      updated_at: "2026-01-01T01:00:00Z", space_id: space, space_name: space, space_index: names.indexOf(space),
      slot_id: `${space}-tab`, slot_index: 0, ...patch,
    });
    let sessions = names.map((space) => placed(`${space[0]}1`, space));
    vi.stubGlobal("fetch", vi.fn((url: string) => {
      const path = new URL(url, "http://hub.example.com").pathname;
      const body = path.endsWith("/sessions") ? { sessions } : path.endsWith("/machines") ? { machines: [mac] } : {};
      return Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
    }));
    await refresh();
    await render();
    const headings = () => [...agentsBlock().querySelectorAll<HTMLButtonElement>("[data-sidebar-group-heading] button[aria-expanded]")];
    const heading = (name: string) => headings().find((button) => button.textContent?.includes(name));
    for (const name of names) expect(heading(name)).toBeUndefined();
    expect(heading(t("sidebar.idle_spaces", { count: 4 }))).toBeDefined();

    sessions = [placed("a1", "alpha", { status: "working", state: "working" }), ...sessions.slice(1)];
    act(() => upsertSession(sessions[0]!));
    await flush();
    expect(heading("alpha")).toBeUndefined();
    expect(heading(t("sidebar.idle_spaces", { count: 4 }))).toBeDefined();

    sessions = [
      sessions[0]!,
      placed("b1", "beta", { state: "needs_you" }),
      placed("c1", "gamma", { work: { phase: "done", just_finished: true } }),
      placed("d1", "delta", { status: "working", state: "working", background_working: true, background_waiting: true }),
    ];
    act(() => sessions.slice(1).forEach((session) => upsertSession(session)));
    await flush();
    expect(heading("alpha")).toBeUndefined();
    for (const name of names.slice(1)) expect(heading(name)).toBeDefined();
    expect(heading(t("sidebar.idle_spaces", { count: 1 }))).toBeDefined();

    // A reload in this tab keeps the attention-admitted rows active and ordinary work in Idle.
    act(() => root.unmount());
    reset();
    root = createRoot(host);
    await render();
    await act(async () => { await refresh(); });
    await flush();
    expect(heading("alpha")).toBeUndefined();
    for (const name of names.slice(1)) expect(heading(name)).toBeDefined();
    expect(heading(t("sidebar.idle_spaces", { count: 1 }))).toBeDefined();

    // A new tab has no stored set. It derives the current layout, so ordinary work starts active.
    act(() => root.unmount());
    reset();
    sessionStorage.removeItem("persona.agentActiveSpaces");
    sessions = [sessions[0]!, ...names.slice(1).map((space) => placed(`${space[0]}1`, space))];
    root = createRoot(host);
    await render();
    await act(async () => { await refresh(); });
    await flush();
    expect(heading("alpha")).toBeDefined();
    for (const name of names.slice(1)) expect(heading(name)).toBeUndefined();
    expect(heading(t("sidebar.idle_spaces", { count: 3 }))).toBeDefined();
  });
});
