// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { seedAgentsDisplay } from "../lib/agents-display";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { connect, putLaunch, reset, savePins } from "../lib/store";
import { cycleGrouping } from "../lib/sidebar-api";
import { SESSIONS_AGENTS_ONLY_FEATURE, runtimeAgents, runtimeSessions } from "../lib/runtime-list";
import { filterAgentList, getAgentListFilters, resetAgentListFilters } from "../lib/agent-list-filters";
import type { AgentPins } from "../lib/pins";
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
const row = (id: string): Session => ({ machine_id: "computer", session_id: id, title: id, cwd: "/code/demo", status: "working", state: "working", updated_at: "2026-01-01T00:00:00Z", space_id: "project", space_name: "Demo project" });
const first = { ...row("First page agent"), cwd: "/home/demo/.persona/health-bin" }, tail = row("Beyond first page");
const assistant: Session = { ...row("Persona conversation"), assistant: true, state: "needs_you" };
const health: Session = { ...row("Health conversation"), space: "health", state: "needs_you" };
const healthDir = { ...health, session_id: "health-dir", title: "Legacy private directory", space: undefined, cwd: "/home/demo/.persona/health/notes" };
const managerHealthDir = { ...health, session_id: "manager-health-dir", title: "Legacy manager private directory", space: undefined, cwd: "C:\\Users\\demo\\.jarvis\\health-m-0b0b0b\\notes" };
const emptyPins: AgentPins = { sessions: [], spaces: [] };
const json = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }));
let host: HTMLDivElement, root: Root;
const flush = async () => {
  await act(async () => { await vi.dynamicImportSettled(); });
  for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); });
};
const grouping = () => host.querySelector('[role="radiogroup"][aria-label="Agent 分组"]');
const chosen = () => grouping()?.querySelector('[aria-checked="true"]')?.textContent;
const pagedList = () => host.querySelector("[data-paged-sessions]");
const completeReads = (fetch: ReturnType<typeof vi.fn>) => fetch.mock.calls.filter(([url]) => (url as string).endsWith("/api/v1/sessions") || (url as string).includes("summary=0"));

async function render(paged: boolean, pins = emptyPins, saved?: string, agentsOnly = paged, delays: { tail?: Promise<void>; todos?: Promise<void> } = {}) {
  if (saved) localStorage.setItem("persona.agentGrouping", saved);
  let moved = false;
  const movedRow = { ...first, assistant: true };
  const fetch = vi.fn((input: string) => {
    const url = new URL(input, "http://hub.example.com");
    if (url.pathname.endsWith("/sessions") && !url.pathname.includes("/agents/")) {
      const q = url.searchParams;
      const scoped = q.get("agents_only") === "1";
      const firstPage = scoped ? [first] : [first, assistant, health, healthDir, managerHealthDir];
      const total = firstPage.length + 1 - (scoped && moved ? 1 : 0);
      if (q.has("counts")) return json({ total, state: scoped ? { working: total } : { working: 2, needs_you: total - 2 }, machine: { computer: total }, group: {}, project: {}, revision: moved ? "r2" : "r1" });
      if (q.has("since")) return json({
        sessions: moved && !scoped ? [movedRow] : [],
        remove: moved && scoped ? [JSON.stringify([first.machine_id, first.session_id])] : [],
        has_more: false,
        revision: moved ? "r2" : "r1",
        total,
      });
      // The complete fallback's pages (#3759): every row, in two frozen pages; the bounded list's summary pages.
      if (url.searchParams.get("summary") === "0" && !url.searchParams.has("session_id")) {
        if (url.searchParams.get("cursor") === "next") {
          const answer = () => json({ sessions: [tail], remove: [], has_more: false, revision: "r1", total });
          return delays.tail ? delays.tail.then(answer) : answer();
        }
        return json({ sessions: firstPage, remove: [], has_more: true, next_cursor: "next", revision: "r1", total });
      }
      if (url.search) return json({ sessions: firstPage, remove: [], has_more: true, next_cursor: "next", revision: "r1", total });
      return json({ sessions: [...firstPage, tail] });
    }
    if (url.pathname.endsWith("/pins")) return json(pins);
    if (url.pathname.endsWith("/machines")) return json({ machines: [{ id: "computer", name: "Demo computer", online: true }] });
    if (url.pathname.endsWith("/todos")) {
      const answer = () => json({ todos: [] });
      return delays.todos ? delays.todos.then(answer) : answer();
    }
    return json({});
  });
  vi.stubGlobal("fetch", fetch);
  act(() => {
    connect();
    const socket = FakeSocket.all.at(-1)!;
    socket.open();
    socket.push({ type: "hello", features: paged ? ["sessions_list", "sessions_lookup", "sessions_todo", ...(agentsOnly ? [SESSIONS_AGENTS_ONLY_FEATURE] : [])] : [] });
  });
  await flush();
  act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
  await flush();
  return { fetch, moveToAssistant: () => { moved = true; FakeSocket.all.at(-1)!.push({ type: "sessions_list", revision: "r2" }); } };
}

beforeEach(() => {
  localStorage.clear();
  reset();
  // Settings › Agents: the Filter is on for these tests (it is off until the hub says so)
  seedAgentsDisplay({ show_botmux: true, configured: true, show_filter: true });
  FakeSocket.all = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  history.replaceState(null, "", "/");
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount()); host.remove(); reset(); resetAgentListFilters(); vi.unstubAllGlobals();
});

for (const paged of [false, true]) {
  describe(`sidebar with paging ${paged ? "supported" : "unavailable"}`, () => {
    it("keeps session pins beyond the first page in their own section", async () => {
      await render(paged, { sessions: ["computer/Beyond first page"], spaces: [] });
      expect(grouping()).not.toBeNull();
      expect(host.textContent).toContain("置顶");
      expect(host.textContent).toContain(tail.title);
      expect(host.querySelectorAll('a[href*="Beyond%20first%20page"]')).toHaveLength(1);
    });
    it("keeps pinned workspaces", async () => {
      await render(paged, { sessions: [], spaces: ["computer/Demo project"] });
      expect(grouping()).not.toBeNull();
      expect(host.textContent).toContain(tail.title);
    });
    it.each(["machine", "space", "repo", "state", "todo"])("honors saved %s grouping under the same bar", async (saved) => {
      const { fetch } = await render(paged, emptyPins, saved);
      expect(chosen()).toBe({ todo: "待办", machine: "机器", repo: "仓库", space: "项目", state: "状态" }[saved]);
      expect(localStorage.getItem("persona.agentGrouping")).toBe(saved);
      // The hub pages its machine, space and to-do groups. Status needs each AgentView.
      const complete = !paged || saved === "repo" || saved === "state";
      expect(Boolean(pagedList())).toBe(!complete);
      expect(completeReads(fetch).length > 0).toBe(complete);
      if (complete) expect(host.textContent).toContain(tail.title);
    });
    it("opens on By to-do, paged where the hub pages, with the same bar either way", async () => {
      const { fetch } = await render(paged);
      expect(chosen()).toBe("待办");
      expect(Boolean(pagedList())).toBe(paged);
      expect(completeReads(fetch).length > 0).toBe(!paged);
    });
  });
}

it("reacts to pins saved while the paged list is mounted, then releases the fallback, the bar unchanged", async () => {
  await render(true);
  expect(pagedList()).not.toBeNull();
  expect(chosen()).toBe("待办");
  await act(async () => savePins({ sessions: ["computer/Beyond first page"], spaces: [] }));
  await flush();
  expect(pagedList()).toBeNull();
  expect(chosen()).toBe("待办");
  expect(host.textContent).toContain(tail.title);
  await act(async () => savePins(emptyPins));
  await flush();
  expect(pagedList()).not.toBeNull();
  expect(chosen()).toBe("待办");
});

it("honors the grouping shortcut while the paged list is mounted", async () => {
  await render(true);
  act(() => { cycleGrouping(); });
  await flush();
  expect(localStorage.getItem("persona.agentGrouping")).toBe("machine");
  expect(chosen()).toBe("机器");
  expect(pagedList()).not.toBeNull();
});

it("has no search field of its own: ⌘K's words are the paged list's search, a chip whose × takes them off (#6589)", async () => {
  const { fetch } = await render(true);
  expect(pagedList()).not.toBeNull();
  expect(host.querySelector("[data-sidebar-agents] input")).toBeNull();
  act(() => filterAgentList("review"));
  await flush();
  // the hub reads them as its text search, the matches listed flat
  expect(fetch.mock.calls.some(([url]) => new URL(url as string, "http://hub.example.com").searchParams.get("q") === "review")).toBe(true);
  const chip = host.querySelector('[data-sidebar-agents] [data-chip="query"]')!;
  expect(chip.textContent).toContain("review");
  await act(async () => chip.querySelector<HTMLButtonElement>("button[aria-label]")!.click());
  await flush();
  expect(getAgentListFilters().query).toBe("");
  expect(host.querySelector('[data-chip="query"]')).toBeNull();
});

it("scopes sidebar pages, counts and deltas while keeping the shared session list complete", async () => {
  const { fetch, moveToAssistant } = await render(true);
  expect(runtimeAgents.getSnapshot().rows.map((s) => s.title)).toEqual([first.title]);
  expect(runtimeAgents.getSnapshot().total).toBe(2);
  expect(runtimeAgents.getSnapshot().counts?.state).toEqual({ working: 2 });
  expect(runtimeSessions.getSnapshot().rows).toContainEqual(assistant);
  expect(runtimeSessions.getSnapshot().rows).toContainEqual(health);

  await act(async () => moveToAssistant());
  await flush();
  const requests = fetch.mock.calls.map(([input]) => new URL(input, "http://hub.example.com").searchParams);
  const scoped = requests.filter((q) => q.get("agents_only") === "1");
  expect(scoped.some((q) => q.get("since") === "r1")).toBe(true);
  expect(scoped.some((q) => q.get("counts") === "1")).toBe(true);
  expect(runtimeAgents.getSnapshot().rows).not.toContainEqual(first);
  expect(runtimeAgents.getSnapshot().total).toBe(1);
  expect(runtimeAgents.getSnapshot().counts?.state).toEqual({ working: 1 });
  expect(runtimeSessions.getSnapshot().rows).toContainEqual({ ...first, assistant: true });
});

it("uses the complete Agents list when the hub supports paging without the Agents filter", async () => {
  const { fetch } = await render(true, emptyPins, undefined, false);
  expect(grouping()).not.toBeNull();
  expect(host.textContent).toContain(tail.title);
  expect(host.textContent).toContain(first.title);
  expect(host.textContent).not.toContain(assistant.title);
  expect(host.textContent).not.toContain(health.title);
  expect(host.textContent).not.toContain(healthDir.title);
  expect(host.textContent).not.toContain(managerHealthDir.title);
  expect(fetch.mock.calls.some(([input]) => input.includes("agents_only"))).toBe(false);
});

it("shows useful complete-list Agents before the delayed last page without settling their partial count (#12231)", async () => {
  const rows = (session: Session) => [...host.querySelectorAll<HTMLAnchorElement>("[data-sidebar-agents] a[href]")]
    .filter((a) => a.textContent?.includes(session.title!));
  const block = () => grouping()!.closest<HTMLElement>("[data-sidebar-agents]")!;
  // A warm visit gives an implementation the opportunity to persist a validated complete snapshot.
  await render(true, emptyPins, undefined, false);
  expect(rows(first)).toHaveLength(1);
  expect(rows(tail)).toHaveLength(1);
  act(() => { root.unmount(); reset(); });
  root = createRoot(host);

  let answerTodos!: () => void, answerTail!: () => void;
  const todos = new Promise<void>((resolve) => { answerTodos = resolve; });
  const tailPage = new Promise<void>((resolve) => { answerTail = resolve; });
  await render(true, emptyPins, undefined, false, { todos, tail: tailPage });
  expect(chosen()).toBe("待办");
  // Early session data must not put every Agent under No to-do before the grouping input arrives.
  expect(rows(first)).toHaveLength(0);
  expect(block().textContent).toContain("加载中…");

  await act(async () => { answerTodos(); });
  await flush();
  // Either the first authoritative page or a validated complete snapshot can supply these rows.
  expect(rows(first)).toHaveLength(1);
  if (rows(tail).length === 0) {
    expect(block().textContent).toContain("加载中…");
    expect(block().querySelector('[data-tree-branch="todo:none"]')).toBeNull();
  }

  await act(async () => { answerTail(); });
  await flush();
  expect(rows(first)).toHaveLength(1);
  expect(rows(tail)).toHaveLength(1);
  const noTodo = block().querySelector('[data-tree-branch="todo:none"]')!;
  expect(noTodo.textContent).toContain(first.title!);
  expect(noTodo.textContent).toContain(tail.title!);
  expect(noTodo.textContent).toContain("2 个 Agent");
  expect(block().textContent).not.toContain("加载中…");
  expect(chosen()).toBe("待办");
});

it("keeps an in-flight sending launch in Starting only without duplicating it in paged groups, then moves it once ready", async () => {
  await render(true, emptyPins, "machine");
  await act(async () => vi.dynamicImportSettled());
  await flush();

  // Baseline: "First page agent" is rendered once inside the open machine group.
  const group = () => host.querySelector('[data-session-group="computer"]')!;
  expect(group()).not.toBeNull();
  expect(group().querySelectorAll("[data-session-row]")).toHaveLength(1);
  expect(group().textContent).toContain(first.title);
  expect(host.querySelectorAll('a[href*="/computer/First%20page%20agent"]')).toHaveLength(1);

  // While the launch is in flight ("sending") with its created session already present in the hub's paged list,
  // it renders only under Starting (with the matching engine mark) and is excluded from the machine group.
  act(() => {
    putLaunch({
      id: "local-sending-1",
      machineId: first.machine_id,
      cwd: first.cwd,
      launcher: "",
      displayLauncher: "claude",
      displayEngine: "claude",
      displayModel: "deepseek-v4.1-flash",
      text: "Starting agent task",
      attachments: [],
      phase: "sending",
      session: first,
      uploaded: {},
      startOpId: "op-1",
      startPromptOpId: "op-2",
      messageId: "msg-1",
    });
  });
  await flush();

  expect(host.textContent).toContain("正在启动");
  expect(host.querySelector('a[href*="/computer/local-sending-1"] [data-agent-mark="claude"]')).not.toBeNull();
  expect(group().querySelectorAll("[data-session-row]")).toHaveLength(0);
  expect(host.querySelectorAll('a[href*="/computer/First%20page%20agent"]')).toHaveLength(0);

  // Once handoff reaches "ready", Starting disappears and the session renders in its machine group again.
  act(() => {
    putLaunch({
      id: "local-sending-1",
      machineId: first.machine_id,
      cwd: first.cwd,
      launcher: "",
      displayLauncher: "claude",
      displayEngine: "claude",
      displayModel: "deepseek-v4.1-flash",
      text: "Starting agent task",
      attachments: [],
      phase: "ready",
      session: first,
      uploaded: {},
      startOpId: "op-1",
      startPromptOpId: "op-2",
      messageId: "msg-1",
    });
  });
  await flush();

  expect(host.textContent).not.toContain("正在启动");
  expect(group().querySelectorAll("[data-session-row]")).toHaveLength(1);
  expect(host.querySelectorAll('a[href*="/computer/First%20page%20agent"]')).toHaveLength(1);
});
