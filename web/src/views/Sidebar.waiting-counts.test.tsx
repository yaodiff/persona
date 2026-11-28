// @vitest-environment jsdom
// Regression tests for waiting-count alignment across Overview, Main Chat, To-dos badge, visible direction rows,
// and the sidebar "Persona chats" section header using synthetic direction fixtures.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { initI18n, t } from "../i18n";
import { assistantEntries } from "../lib/assistant-entries";
import { directUserWaiting, directWaitingCount } from "../lib/direct-waiting";
import { resetDirectionNames } from "../lib/direction-names";
import { getFeedState, type TaskPlan } from "../lib/feed";
import { resetManagerChoice } from "../lib/manager-choice";
import { setRecentCount } from "../lib/sidebar-recent";
import { refresh, refreshTodos, reset } from "../lib/store";
import type { Todo } from "../lib/todos";
import type { OwnerAsk } from "../lib/todo-view-wire";
import type { Interaction, Session } from "../lib/types";
import { Sidebar } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const AT = "2026-11-25T10:00:00Z";
const conv = (session_id: string, title: string, updated_at = AT): Session => ({
  machine_id: "hub",
  session_id,
  title,
  cwd: "/persona",
  status: "idle",
  state: "idle",
  assistant: true,
  origin: "user",
  updated_at,
});

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  await act(async () => { await vi.dynamicImportSettled(); });
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(async () => {
  localStorage.clear();
  await initI18n("en");
  history.replaceState(null, "", "/overview");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  act(() => root.unmount());
  await act(async () => { await vi.dynamicImportSettled(); });
  host.remove();
  reset();
  resetDirectionNames();
  resetManagerChoice();
  getFeedState().plans = {};
  delete getFeedState().feeds["main"];
  delete getFeedState().taskCards["main"];
  vi.unstubAllGlobals();
});

describe("sidebar waiting counts and cross-surface agreement", () => {
  it("sums only visible rows in the Persona chats header, excluding hidden directions, main plans, and folded recent rows", async () => {
    setRecentCount(5);
    const conversations: Session[] = [
      conv("main", "Persona", "2026-11-25T10:09:00Z"),
      conv("dir-alpha", "Alpha Direction", "2026-11-25T10:08:00Z"),
      conv("dir-beta", "Beta Direction", "2026-11-25T10:07:00Z"),
      conv("bg-sup", "Background Supervisor", "2026-11-25T10:06:00Z"),
      conv("recent-visible", "Visible Recent Chat", "2026-11-25T10:05:00Z"),
      conv("recent-2", "Recent Two", "2026-11-25T10:04:00Z"),
      conv("recent-3", "Recent Three", "2026-11-25T10:03:00Z"),
      conv("recent-4", "Recent Four", "2026-11-25T10:02:00Z"),
      conv("recent-5", "Recent Five", "2026-11-25T10:01:00Z"),
      conv("recent-truncated", "Truncated Recent Chat", "2026-11-25T10:00:00Z"),
    ];
    const plans: Record<string, TaskPlan> = {
      pMain: { id: "pMain", conversation: "main", state: "open", created_at: AT, tasks: [] },
      pVisibleRecent: { id: "pVisibleRecent", conversation: "recent-visible", state: "open", created_at: AT, tasks: [] },
      pTruncatedRecent: { id: "pTruncatedRecent", conversation: "recent-truncated", state: "open", created_at: AT, tasks: [] },
    };
    const tree = {
      directions: [
        { id: "dir-alpha", name: "Alpha", counts: { working: 1, needs_you: 2, stuck: 0, review: 0, planned: 0 }, running: true, tasks: [] },
        { id: "dir-beta", name: "Beta", counts: { working: 0, needs_you: 1, stuck: 0, review: 0, planned: 0 }, running: false, tasks: [] },
        // Present in tree.directions but omitted from assistant.conversations and not external -> hidden from visible list.
        { id: "dir-hidden", name: "Hidden", counts: { working: 0, needs_you: 5, stuck: 0, review: 0, planned: 0 }, running: true, tasks: [] },
        { id: "bg-sup", name: "Background", counts: { working: 0, needs_you: 3, stuck: 0, review: 0, planned: 0 }, running: false, tasks: [] },
        { id: "main", name: "", secretary: true, counts: { working: 0, needs_you: 4, stuck: 0, review: 0, planned: 0 }, running: false, tasks: [] },
      ],
    };
    vi.stubGlobal("fetch", vi.fn((url: string) => {
      const path = new URL(url, "http://hub.example.com").pathname;
      if (path === "/api/v1/assistant") {
        return Promise.resolve(Response.json({
          machine_id: "hub",
          main: "main",
          background_supervisor: "bg-sup",
          leaders: ["dir-alpha", "dir-beta", "dir-hidden", "bg-sup"],
          conversations,
        }));
      }
      if (path === "/api/v1/assistant/directions/tree") return Promise.resolve(Response.json(tree));
      if (path === "/api/v1/assistant/conversations/main/feed") return Promise.resolve(Response.json({ items: [], plans, task_cards: [] }));
      if (path === "/api/v1/machines") return Promise.resolve(Response.json({ machines: [{ id: "hub", name: "Hub", online: true }] }));
      if (path === "/api/v1/todos") return Promise.resolve(Response.json({ todos: [] }));
      if (path === "/api/v1/sessions") return Promise.resolve(Response.json({ sessions: [] }));
      if (path === "/api/v1/interactions") return Promise.resolve(Response.json({ interactions: [] }));
      return Promise.resolve(Response.json({}));
    }));

    await act(() => refresh());
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();

    // Visible rows under Persona chats: dir-alpha (2), dir-beta (1), recent-visible (1) => header must be 4, never 12+.
    const alphaRow = host.querySelector<HTMLAnchorElement>('a[href="/persona/dir-alpha"]')!;
    const betaRow = host.querySelector<HTMLAnchorElement>('a[href="/persona/dir-beta"]')!;
    const recentRow = host.querySelector<HTMLAnchorElement>('a[href="/persona/recent-visible"]')!;
    expect(alphaRow.textContent).toContain(t("sidebar.leader_needs_you", { count: 2 }));
    expect(betaRow.textContent).toContain(t("sidebar.leader_needs_you", { count: 1 }));
    expect(recentRow.textContent).toContain(t("sidebar.leader_needs_you", { count: 1 }));
    expect(host.querySelector('a[href="/persona/dir-hidden"]')).toBeNull();
    expect(host.querySelector('a[href="/persona/recent-truncated"]')).toBeNull();

    const headerTally = () => host.querySelector<HTMLElement>("[data-sidebar-rows] [data-direction-counts]")?.textContent ?? "";
    expect(headerTally()).toBe(t("sidebar.leader_needs_you", { count: 4 }));

    // Folding Recent removes recent-visible from visible rows -> header matches visible directions sum (2 + 1 = 3).
    const recentToggle = [...host.querySelectorAll<HTMLButtonElement>('[role="heading"] button')].find((b) => b.textContent?.includes(t("sidebar.recent_chats")))!;
    act(() => recentToggle.click());
    await flush();
    expect(headerTally()).toBe(t("sidebar.leader_needs_you", { count: 3 }));
  });

  it("keeps Overview, Main Chat, To-dos, visible directions, and section header aligned at 1 when 4 internal read-only permission cards are supervisor-held", async () => {
    const conversations: Session[] = [
      conv("main", "Persona", "2026-11-25T10:09:00Z"),
      conv("dir-alpha", "Alpha Direction", "2026-11-25T10:08:00Z"),
      conv("dir-beta", "Beta Direction", "2026-11-25T10:07:00Z"),
    ];
    const sessions: Session[] = [
      // Four agents waiting only on supervisor-held read-only permission cards (including one folded child).
      { machine_id: "m1", session_id: "s-held-1", title: "Alpha reader 1", cwd: "/work/a", status: "blocked", state: "needs_you", pending_interactions: 1, updated_at: AT },
      { machine_id: "m1", session_id: "s-held-2", title: "Alpha parent", cwd: "/work/a", status: "working", state: "working", children: { total: 1, needs_you: 1 }, updated_at: AT },
      { machine_id: "m1", session_id: "s-held-2-sub", folded_under: "s-held-2", title: "Alpha child", cwd: "/work/a", status: "blocked", state: "needs_you", pending_interactions: 1, updated_at: AT },
      { machine_id: "m1", session_id: "s-held-3", title: "Beta reader 1", cwd: "/work/b", status: "blocked", state: "needs_you", pending_interactions: 1, updated_at: AT },
      { machine_id: "m1", session_id: "s-held-4", title: "Beta reader 2", cwd: "/work/b", status: "blocked", state: "needs_you", pending_interactions: 1, updated_at: AT },
      // One genuinely escalated agent card for the owner.
      { machine_id: "m1", session_id: "s-owner-esc", title: "Alpha deploy gate", cwd: "/work/a", status: "blocked", state: "needs_you", pending_interactions: 1, updated_at: AT },
    ];
    const interactions: Interaction[] = [
      { id: "ix-ro-1", machine_id: "m1", session_id: "s-held-1", kind: "permission", tool_name: "Read", delegating: true, created_at: AT },
      { id: "ix-ro-2", machine_id: "m1", session_id: "s-held-2-sub", folded_under: "s-held-2", kind: "permission", tool_name: "Read", delegating: true, created_at: AT },
      { id: "ix-ro-3", machine_id: "m1", session_id: "s-held-3", kind: "permission", tool_name: "Read", delegating: true, created_at: AT },
      { id: "ix-ro-4", machine_id: "m1", session_id: "s-held-4", kind: "permission", tool_name: "Read", delegating: true, created_at: AT },
      { id: "ix-owner-1", machine_id: "m1", session_id: "s-owner-esc", kind: "permission", tool_name: "Bash", delegating: false, delegate: { reason: "Needs owner approval to publish release" }, created_at: AT },
    ];
    const todos: Todo[] = [
      {
        id: "t-held-1", title: "Alpha read check 1", origin: "main", created_at: AT, updated_at: AT,
        agents: [{ machine_id: "m1", session_id: "s-held-1", linked_at: AT }],
        supervision: { supervisor: "dir-alpha", state: "", reviews: [] },
        view: { phase: "working", section: "working", reason: "persona_working", rank: 0 },
      },
      {
        id: "t-held-2", title: "Alpha read check 2", origin: "main", created_at: AT, updated_at: AT,
        agents: [{ machine_id: "m1", session_id: "s-held-2", linked_at: AT }],
        supervision: { supervisor: "dir-alpha", state: "", reviews: [] },
        view: { phase: "working", section: "working", reason: "persona_working", rank: 0 },
      },
      {
        id: "t-held-3", title: "Beta read check 1", origin: "main", created_at: AT, updated_at: AT,
        agents: [{ machine_id: "m1", session_id: "s-held-3", linked_at: AT }],
        supervision: { supervisor: "dir-beta", state: "", reviews: [] },
        view: { phase: "working", section: "working", reason: "persona_working", rank: 0 },
      },
      {
        id: "t-held-4", title: "Beta read check 2", origin: "main", created_at: AT, updated_at: AT,
        agents: [{ machine_id: "m1", session_id: "s-held-4", linked_at: AT }],
        supervision: { supervisor: "dir-beta", state: "", reviews: [] },
        view: { phase: "working", section: "working", reason: "persona_working", rank: 0 },
      },
      {
        id: "t-owner-1", title: "Alpha release approval", origin: "main", created_at: AT, updated_at: AT,
        agents: [{ machine_id: "m1", session_id: "s-owner-esc", linked_at: AT }],
        supervision: { supervisor: "dir-alpha", state: "", reviews: [] },
        view: { phase: "needs_you", section: "needs_you", reason: "agent_asks", rank: 0, asks: [{ id: "run:m1/s-owner-esc", kind: "tool", home: "run", risk: "open", lockscreen: "open_only", interaction: "ix-owner-1" as OwnerAsk["interaction"] }] },
      },
    ];
    const tree = {
      directions: [
        { id: "dir-alpha", name: "Alpha", counts: { working: 2, needs_you: 1, stuck: 0, review: 0, planned: 0 }, running: true, tasks: [] },
        { id: "dir-beta", name: "Beta", counts: { working: 2, needs_you: 0, stuck: 0, review: 0, planned: 0 }, running: true, tasks: [] },
        { id: "main", name: "", secretary: true, counts: { working: 0, needs_you: 0, stuck: 0, review: 0, planned: 0 }, running: false, tasks: [] },
      ],
    };

    vi.stubGlobal("fetch", vi.fn((url: string) => {
      const path = new URL(url, "http://hub.example.com").pathname;
      if (path === "/api/v1/assistant") {
        return Promise.resolve(Response.json({
          machine_id: "hub",
          main: "main",
          leaders: ["dir-alpha", "dir-beta"],
          conversations,
        }));
      }
      if (path === "/api/v1/assistant/directions/tree") return Promise.resolve(Response.json(tree));
      if (path === "/api/v1/assistant/conversations/main/feed") return Promise.resolve(Response.json({ items: [], plans: {}, task_cards: [] }));
      if (path === "/api/v1/machines") return Promise.resolve(Response.json({ machines: [{ id: "hub", name: "Hub", online: true }, { id: "m1", name: "Worker", online: true }] }));
      if (path === "/api/v1/todos") return Promise.resolve(Response.json({ todos }));
      if (path === "/api/v1/sessions") return Promise.resolve(Response.json({ sessions }));
      if (path === "/api/v1/interactions") return Promise.resolve(Response.json({ interactions }));
      return Promise.resolve(Response.json({}));
    }));

    await act(async () => {
      await refresh();
      await refreshTodos();
    });
    act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
    await flush();

    // 1. Overview badge: 1 (excludes the 4 supervisor-held read-only cards)
    const overviewRow = host.querySelector<HTMLAnchorElement>('a[href="/overview"]')!;
    expect(overviewRow.textContent).toContain("1");

    // 2. Main Chat sidebar row (useMainWaiting) and bottom chip projection: 1
    const mainWaitingBadge = host.querySelector<HTMLElement>("[data-main-waiting]")!;
    expect(mainWaitingBadge?.getAttribute("data-main-waiting")).toBe("1");
    expect(mainWaitingBadge?.textContent).toBe("1");
    const entries = assistantEntries({ conversation: "main", main: "main", todos, sessions, interactions, plans: {}, feed: [], now: Date.parse(AT) });
    expect(directWaitingCount(directUserWaiting(entries.needsYou), [])).toBe(1);

    // 3. To-dos sidebar badge: 1
    const todosRow = host.querySelector<HTMLAnchorElement>('a[href="/todos"]')!;
    expect(todosRow.textContent).toContain("1");

    // 4. Visible directions & Persona chats header: dir-alpha=1, dir-beta=0, header=1
    const alphaRow = host.querySelector<HTMLAnchorElement>('a[href="/persona/dir-alpha"]')!;
    const betaRow = host.querySelector<HTMLAnchorElement>('a[href="/persona/dir-beta"]')!;
    expect(alphaRow.textContent).toContain(t("sidebar.leader_needs_you", { count: 1 }));
    expect(betaRow.textContent).not.toContain("need you");
    const headerTally = host.querySelector<HTMLElement>("[data-sidebar-rows] [data-direction-counts]")!;
    expect(headerTally.textContent).toBe(t("sidebar.leader_needs_you", { count: 1 }));
  });
});
