// @vitest-environment jsdom
// What a dispatch draws in the conversation (#3700, #5096: cindy's hand-off card): a card for each Agent it handed work
// to, which opens that Agent's own page, never a side panel and never the whole In progress tray.
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  globalThis.IntersectionObserver ??= class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } } as unknown as typeof IntersectionObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

import { t } from "../../i18n";
import { getFeedState, type DisplayLine, type FeedItem, type TaskPlan } from "../../lib/feed";
import { handoffFrom } from "../../lib/handoff";
import { setAgentOpenMode } from "../../lib/agent-open";
import { closePanel, getPanel } from "../../lib/panel";
import { stateCount } from "../../lib/state-labels";
import { applyCompleteSessions, refreshTodos, reset } from "../../lib/store";
import type { Todo, TodoAgent } from "../../lib/todos";
import type { Session } from "../../lib/types";
import {
  groupAssistantLines,
  handoffAgents,
  HandoffFeedRun,
  HandoffGroupStateContext,
  lastActive,
  shortSessionId,
  TASK_LINE_SLACK_MS,
} from "./Handoff";
import { AssistantActionBar, FeedLineView } from "./Tasks";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const at = "2026-11-10T08:00:00Z";
const conversation = "dispatch-demo";
const agent: Session = { machine_id: "example-computer", session_id: "0f3c2a91-5d7e-4b8a-9c11-2e4f6a8b7ed0", title: "Example Agent", cwd: "/example", state: "working", status: "working", updated_at: at } as Session;
const linked = (id: string, title: string, sessions: Session[], extra: Partial<Todo> = {}): Todo => ({
  id, title, origin: conversation, created_at: at, updated_at: at,
  agents: sessions.map((s) => ({ machine_id: s.machine_id, session_id: s.session_id, title: s.title, linked_at: at })), ...extra,
});
let served: Todo[];
let host: HTMLDivElement;
let root: Root;
const settle = () => act(async () => {
  await vi.dynamicImportSettled();
  await new Promise((r) => setTimeout(r, 0));
});
const opened = () => location.pathname + location.search;
const tray = () => host.querySelector<HTMLButtonElement>('[data-tray-entry="running"]')!;
const cards = () => [...host.querySelectorAll<HTMLElement>("[data-handoff-card]")];

beforeEach(() => {
  vi.stubGlobal("fetch", vi.fn(async (url: string) => new Response(JSON.stringify(String(url).includes("/todos") ? { todos: served } : String(url).includes("/items") ? { items: [], to_seq: 0 } : {}), { headers: { "Content-Type": "application/json" } })));
  history.replaceState(null, "", "/persona");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  setAgentOpenMode("panel");
  closePanel();
  reset();
  history.replaceState(null, "", "/");
  for (const id of Object.keys(getFeedState().plans)) delete getFeedState().plans[id];
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function show(item: FeedItem, todos: Todo[], sessions: Session[]) {
  served = todos;
  await act(async () => {
    applyCompleteSessions(sessions);
    await refreshTodos();
  });
  act(() => root.render(<StrictMode><AssistantActionBar conversation={conversation} /><div data-chat-scroll><FeedLineView item={item} simplified /></div></StrictMode>));
  await settle();
}

describe("hand-off cards", () => {
  it("one dispatched task: its Agent's card, New, opening the Agent in the side panel, or its page with the way back set to Full page, not the In progress tray", async () => {
    await show({ id: "task-event", conversation, at, kind: "task", ref: "example-task" }, [linked("example-task", "Example task", [agent])], [agent]);
    const [card] = cards();
    expect(cards()).toHaveLength(1);
    expect(card!.tagName).toBe("BUTTON");
    expect(card!.textContent).toContain("Example Agent");
    expect(card!.querySelector('[data-handoff-wake="created"]')?.textContent).toBe(t("assistant-tasks.handoff_wake_created"));
    expect(card!.querySelector('[data-handoff-state="working"]')?.textContent).toContain(t("lib-state-labels.working"));
    // the id is only in the tooltip, cindy's short form
    expect(card!.textContent).not.toContain("0f3c2a91");
    expect(card!.getAttribute("title")).toBe("Example Agent · 0f3c2a91…7ed0");
    // the default (#5424): beside the chat, which stays where it is
    act(() => card!.click());
    expect(getPanel()).toEqual({ kind: "agent", mid: "example-computer", sid: agent.session_id });
    expect(opened()).toBe("/persona");
    act(() => closePanel());
    // set to Full page (#5096): the Agent's page, which says where it came from
    setAgentOpenMode("page");
    act(() => card!.click());
    expect(opened()).toBe(`/s/example-computer/${agent.session_id}`);
    expect(handoffFrom()).toEqual({ path: "/persona" });
    expect(getPanel()).toBeNull();
    expect(tray().getAttribute("aria-expanded")).toBe("false");
  });

  it("a task made of an Agent that was running already says Already active", async () => {
    await show({ id: "task-event", conversation, at, kind: "task", ref: "example-task", body: { wake: "active" } }, [linked("example-task", "Example task", [agent])], [agent]);
    expect(cards()[0]!.querySelector('[data-handoff-wake="active"]')?.textContent).toBe(t("assistant-tasks.handoff_wake_active"));
  });

  it("a closed Agent the hand-off resumed says Resumed", async () => {
    await show({ id: "task-event", conversation, at, kind: "task", ref: "example-task", body: { wake: "resumed" } }, [linked("example-task", "Example task", [agent])], [agent]);
    expect(cards()[0]!.querySelector('[data-handoff-wake="resumed"]')?.textContent).toBe(t("assistant-tasks.handoff_wake_resumed"));
  });

  // Anchored at its line's time (#11854): the first Agents linked from then on. Before #11854 this held because the
  // card kept the to-do's earliest Agents whatever line it was under; now it holds because the line is the earlier one.
  it("draws the Agents the hand-off went to, not one a later message started (that one has its own card)", async () => {
    const next: Session = { ...agent, session_id: "next-agent", title: "Next Agent" };
    const todo = linked("example-task", "Example task", [agent, next]);
    todo.agents![1]!.linked_at = "2026-11-10T09:00:00Z";
    await show({ id: "task-event", conversation, at, kind: "task", ref: "example-task", body: { wake: "active" } }, [todo], [agent, next]);
    expect(cards().map((c) => c.getAttribute("data-handoff-card"))).toEqual([`example-computer/${agent.session_id}`]);
  });

  it("before its Agent is up the card is the task's: starting, and it opens the to-do", async () => {
    await show({ id: "task-event", conversation, at, kind: "task", ref: "example-task" }, [linked("example-task", "Example task", [])], []);
    const [card] = cards();
    expect(card!.textContent).toContain("Example task");
    expect(card!.textContent).toContain(t("assistant-tasks.starting_agent"));
    act(() => card!.click());
    expect(getPanel()).toEqual({ kind: "task", todo: "example-task" });
    act(() => closePanel());
    setAgentOpenMode("page");
    act(() => card!.click());
    expect(opened()).toBe("/todos?id=example-task");
  });

  it("a task that did not start says why; one removed since says so and opens nothing", async () => {
    const plan: TaskPlan = { id: "plan-two", conversation, state: "started", created_at: at, tasks: [
      { title: "Broken task", prompt: "a", cwd: "/example", machine: "example-computer", todo_id: "broken-task" },
      { title: "Removed task", prompt: "b", cwd: "/example", machine: "example-computer", todo_id: "removed-task" },
    ] };
    getFeedState().plans[plan.id] = plan;
    await show({ id: "plan-event", conversation, at, kind: "plan", ref: plan.id }, [linked("broken-task", "Broken task", [], { start_error: "launcher missing", done_at: at })], []);
    const [broken, removed] = cards();
    act(() => broken!.click());
    expect(getPanel()).toEqual({ kind: "task", todo: "broken-task" });
    // a raw error that names no known cause says to open the task, where it is (#8237)
    expect(broken!.textContent).toContain(t("assistant-tasks.start_failed_details"));
    expect(broken!.textContent).not.toContain("launcher missing");
    expect(removed!.tagName).toBe("DIV");
    expect(removed!.textContent).toContain("Removed task");
    expect(removed!.textContent).toContain(t("assistant-tasks.handoff_gone"));
  });

  it("shows a failed continuation without making its previous to-do the destination", async () => {
    const plan: TaskPlan = { id: "plan-failed", conversation, state: "started", created_at: at, tasks: [
      { title: "Carry on the task", prompt: "a", cwd: "/example", machine: "example-computer", continues: "previous-task", error: "continues: cannot stop agent example-computer/session on to-do previous-task: exit status 1" },
    ] };
    getFeedState().plans[plan.id] = plan;
    await show({ id: "plan-event", conversation, at, kind: "plan", ref: plan.id }, [linked("previous-task", "Previous task", [agent])], [agent]);
    const [card] = cards();
    expect(card!.tagName).toBe("DIV");
    expect(card!.querySelector('[data-handoff-wake="continuation"]')).not.toBeNull();
    expect(card!.querySelector("[data-handoff-failure]")?.textContent).toBe(t("assistant-tasks.start_failed_short"));
    act(() => card!.click());
    expect(getPanel()).toBeNull();
  });

  it("wraps the recovery instructions and filenames on an interactive failed launch", async () => {
    const failure = 'The previous workspace has uncommitted changes. Commit or stash them, then retry (task_retry). Changed files include "timeout.ts".';
    await show({ id: "task-event", conversation, at, kind: "task", ref: "example-task" }, [linked("example-task", "Example task", [], { start_error: failure, done_at: at })], []);
    const [card] = cards();
    const reason = card!.querySelector("[data-handoff-failure]");
    expect(card!.tagName).toBe("BUTTON");
    expect(reason?.textContent).toContain(failure);
    expect(reason?.classList.contains("break-words")).toBe(true);
    expect(reason?.classList.contains("truncate")).toBe(false);
  });

  describe("a to-do continued by later plans (#11854)", () => {
    // three plans on one to-do: the first made it, the next two continued it; each continuation released the Agent
    // before and cleared the older plan's todo_id, as the hub does (tasks_start.go)
    const hour = (h: number, m = 0) => `2026-11-10T${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}:00Z`;
    const session = (id: string, title: string, state: "working" | "idle"): Session => ({ ...agent, session_id: id, title, state, status: state } as Session);
    const first = session("first-agent", "First Agent", "idle");
    const second = session("second-agent", "Second Agent", "idle");
    const third = session("third-agent", "Third Agent", "working");
    const row = (s: Session, linkedAt: string, releasedAt?: string): TodoAgent => ({ machine_id: s.machine_id, session_id: s.session_id, title: s.title, linked_at: linkedAt, released_at: releasedAt });
    const todoWith = (agents: TodoAgent[]): Todo => ({ id: "continued-task", title: "Continued task", origin: conversation, plan_id: "plan-a", created_at: hour(8), updated_at: hour(12), agents });
    const plan = (id: string, at: string, task: Partial<TaskPlan["tasks"][number]>): FeedItem => {
      getFeedState().plans[id] = { id, conversation, state: "started", created_at: at, tasks: [{ title: "Continued task", prompt: "p", cwd: "/example", machine: "example-computer", ...task }] };
      return { id: `${id}-event`, conversation, at, kind: "plan", ref: id };
    };
    const plans = () => ({
      a: plan("plan-a", hour(8), {}),
      b: plan("plan-b", hour(9), { continues: "continued-task" }),
      c: plan("plan-c", hour(12), { continues: "continued-task", todo_id: "continued-task" }),
    });
    const ids = () => cards().map((c) => c.getAttribute("data-handoff-card"));

    it("the newest plan's card is the running Agent it started, and opens it, not the first stopped one", async () => {
      const { c } = plans();
      const todo = todoWith([row(third, hour(12, 1)), row(second, hour(9, 1), hour(12)), row(first, hour(8, 1), hour(9))]);
      await show(c, [todo], [first, second, third]);
      expect(ids()).toEqual([`example-computer/third-agent`]);
      const [card] = cards();
      expect(card!.textContent).toContain("Third Agent");
      expect(card!.querySelector('[data-handoff-wake="continuation"]')).not.toBeNull();
      expect(card!.querySelector('[data-handoff-state="working"]')).not.toBeNull();
      act(() => card!.click());
      expect(getPanel()).toEqual({ kind: "agent", mid: "example-computer", sid: "third-agent" });
    });

    it("cards under the earlier plans keep the Agents those plans started, the first one included (its todo_id was cleared)", async () => {
      const { a, b } = plans();
      const todo = todoWith([row(third, hour(12, 1)), row(second, hour(9, 1), hour(12)), row(first, hour(8, 1), hour(9))]);
      await show(b, [todo], [first, second, third]);
      expect(ids()).toEqual([`example-computer/second-agent`]);
      act(() => root.render(<StrictMode><div data-chat-scroll><FeedLineView item={a} simplified /></div></StrictMode>));
      await settle();
      expect(ids()).toEqual([`example-computer/first-agent`]);
      expect(cards()[0]!.querySelector('[data-handoff-wake="created"]')).not.toBeNull();
    });

    it("before the newest plan's Agent is linked its card says starting, never an older Agent", async () => {
      const { c } = plans();
      await show(c, [todoWith([row(second, hour(9, 1), hour(12)), row(first, hour(8, 1), hour(9))])], [first, second]);
      expect(ids()).toEqual(["todo:continued-task"]);
      expect(cards()[0]!.textContent).toContain(t("assistant-tasks.starting_agent"));
      expect(cards()[0]!.textContent).not.toContain("Second Agent");
    });

    it("with every Agent stopped, the newest plan's card is the Agent it started, the latest", async () => {
      const { c } = plans();
      const todo = todoWith([row(third, hour(12, 1), hour(13)), row(second, hour(9, 1), hour(12)), row(first, hour(8, 1), hour(9))]);
      await show(c, [todo], [first, second, { ...third, state: "idle", status: "idle" } as Session]);
      expect(ids()).toEqual([`example-computer/third-agent`]);
    });

    it("several Agents the newest plan started together each get a card; the released older ones none", async () => {
      const { c } = plans();
      const fourth = session("fourth-agent", "Fourth Agent", "working");
      const todo = todoWith([row(fourth, hour(12, 2)), row(third, hour(12, 1)), row(second, hour(9, 1), hour(12)), row(first, hour(8, 1), hour(9))]);
      await show(c, [todo], [first, second, third, fourth]);
      // in the hub's order (formatTodo: the newest linked first)
      expect(ids()).toEqual([`example-computer/fourth-agent`, `example-computer/third-agent`]);
    });

    // continued in quick succession: the previous Agent was linked 10 seconds before the newest plan's line, within a
    // task line's look-behind (TASK_LINE_SLACK_MS); a plan line takes none, so it is never the plan's own
    describe("when the previous Agent was linked just before the newest plan's line", () => {
      const justBefore = (releasedAt?: string) => row(second, "2026-11-10T11:59:50Z", releasedAt);

      it("its card says starting until the plan's Agent is linked, not the previous one", async () => {
        const { c } = plans();
        await show(c, [todoWith([justBefore("2026-11-10T12:00:20Z"), row(first, hour(8, 1), hour(9))])], [first, second]);
        expect(ids()).toEqual(["todo:continued-task"]);
        expect(cards()[0]!.textContent).toContain(t("assistant-tasks.starting_agent"));
      });

      it("then it is only the plan's Agent, linked soon after", async () => {
        const { c } = plans();
        await show(c, [todoWith([row(third, hour(12, 1)), justBefore("2026-11-10T12:00:20Z"), row(first, hour(8, 1), hour(9))])], [first, second, third]);
        expect(ids()).toEqual([`example-computer/third-agent`]);
      });

      it("or linked more than two minutes later", async () => {
        const { c } = plans();
        await show(c, [todoWith([row(third, hour(12, 5)), justBefore("2026-11-10T12:00:20Z"), row(first, hour(8, 1), hour(9))])], [first, second, third]);
        expect(ids()).toEqual([`example-computer/third-agent`]);
      });
    });
  });

  it("a message that started a new Agent for its task has that Agent's card under it", async () => {
    const next: Session = { ...agent, session_id: "next-agent", title: "Next Agent" };
    await show({ id: "routed-event", conversation, at, kind: "routed", ref: "example-task", body: { text: "Go on", new_agent: "next-agent", new_agent_machine: "example-computer" } },
      [linked("example-task", "Example task", [agent, next])], [agent, next]);
    const [card] = cards();
    expect(cards()).toHaveLength(1);
    expect(card!.textContent).toContain("Next Agent");
    act(() => card!.click());
    expect(getPanel()).toEqual({ kind: "agent", mid: "example-computer", sid: "next-agent" });
    act(() => closePanel());
    setAgentOpenMode("page");
    act(() => card!.click());
    expect(opened()).toBe("/s/example-computer/next-agent");
  });
});

describe("the card's words", () => {
  it("shortens an id as cindy does", () => {
    expect(shortSessionId("0f3c2a91-5d7e-4b8a-9c11-2e4f6a8b7ed0")).toBe("0f3c2a91…7ed0");
    expect(shortSessionId("short-id")).toBe("short-id");
  });

  it("says when the Agent was last active", () => {
    const now = Date.parse(at);
    expect(lastActive({ activity_at: new Date(now - 10_000).toISOString() }, now)).toBe(t("assistant-tasks.handoff_active_now"));
    expect(lastActive({ updated_at: new Date(now - 5 * 60_000).toISOString() }, now)).toMatch(/5/);
    expect(lastActive({}, now)).toBeUndefined();
  });
});

describe("which Agents a card stands for (#11854)", () => {
  const row = (id: string, linkedAt: string, extra: Partial<TodoAgent> = {}): TodoAgent => ({ machine_id: "example-computer", session_id: id, linked_at: linkedAt, ...extra });
  const names = (agents: TodoAgent[]) => agents.map((a) => a.session_id);

  it("without an anchor: the newest batch still linked, not the first", () => {
    const agents = [row("old", "2026-11-10T08:00:00Z", { released_at: "2026-11-10T09:00:00Z" }), row("new", "2026-11-10T12:00:00Z"), row("new-too", "2026-11-10T12:01:00Z")];
    expect(names(handoffAgents(agents))).toEqual(["new", "new-too"]);
  });

  it("without an anchor and with every Agent released: the one released last", () => {
    const agents = [row("first", "2026-11-10T08:00:00Z", { released_at: "2026-11-10T09:00:00Z" }), row("last", "2026-11-10T09:00:00Z", { released_at: "2026-11-10T12:00:00Z" })];
    expect(names(handoffAgents(agents))).toEqual(["last"]);
  });

  it("anchored: the first Agents linked since, skipping ones that failed to start; none yet unless it may fall back", () => {
    const agents = [row("old", "2026-11-10T08:00:00Z", { released_at: "2026-11-10T09:00:00Z" }), row("failed", "2026-11-10T09:00:30Z", { start_error: "x" }), row("retry", "2026-11-10T09:05:00Z")];
    expect(names(handoffAgents(agents, "2026-11-10T09:00:00Z"))).toEqual(["retry"]);
    expect(names(handoffAgents(agents.slice(0, 1), "2026-11-10T09:00:00Z", false))).toEqual([]);
    expect(names(handoffAgents(agents.slice(0, 1), "2026-11-10T09:00:00Z"))).toEqual(["old"]);
  });

  it("looks behind its anchor only as far as asked: a task line's 30 seconds, a plan line's none", () => {
    const agents = [row("previous", "2026-11-10T11:59:50Z"), row("planned", "2026-11-10T12:03:00Z")];
    expect(names(handoffAgents(agents, "2026-11-10T12:00:00Z", false, TASK_LINE_SLACK_MS))).toEqual(["previous"]);
    expect(names(handoffAgents(agents, "2026-11-10T12:00:00Z", false))).toEqual(["planned"]);
    expect(names(handoffAgents(agents.slice(0, 1), "2026-11-10T12:00:00Z", false))).toEqual([]);
  });
});

describe("collapsible multi-task hand-off groups (#17766)", () => {
  const mkSession = (id: string, title: string, state: Session["state"], extra: Partial<Session> = {}): Session => ({
    ...agent,
    session_id: id,
    title,
    state,
    status: state === "needs_you" ? "blocked" : state === "working" ? "working" : "idle",
    ...extra,
  } as Session);

  const toggleBtn = () => host.querySelector<HTMLButtonElement>("[data-handoff-group-toggle]");
  const summaryEl = () => host.querySelector<HTMLElement>("[data-handoff-group-summary]");

  it("keeps 1-2 hand-off cards directly expanded with zero extra collapse chrome", async () => {
    const s1 = mkSession("s1", "Agent One", "working");
    const s2 = mkSession("s2", "Agent Two", "working");
    const plan: TaskPlan = {
      id: "plan-two-direct",
      conversation,
      state: "started",
      created_at: at,
      tasks: [
        { title: "Task One", prompt: "a", cwd: "/example", machine: "example-computer", todo_id: "t1" },
        { title: "Task Two", prompt: "b", cwd: "/example", machine: "example-computer", todo_id: "t2" },
      ],
    };
    getFeedState().plans[plan.id] = plan;
    await show(
      { id: "plan-two-ev", conversation, at, kind: "plan", ref: plan.id },
      [linked("t1", "Task One", [s1]), linked("t2", "Task Two", [s2])],
      [s1, s2],
    );
    expect(host.querySelector("[data-handoff-group]")).toBeNull();
    expect(toggleBtn()).toBeNull();
    expect(cards()).toHaveLength(2);
  });

  it("collapses 3+ cards by default into a compact summary card with live status aggregation and expand/collapse toggle", async () => {
    const s1 = mkSession("s1", "Agent 1", "working");
    const s2 = mkSession("s2", "Agent 2", "working");
    const s3 = mkSession("s3", "Agent 3", "working");
    const plan: TaskPlan = {
      id: "plan-five",
      conversation,
      state: "started",
      created_at: at,
      tasks: [
        { title: "Task 1", prompt: "a", cwd: "/example", machine: "example-computer", todo_id: "t1" },
        { title: "Task 2", prompt: "b", cwd: "/example", machine: "example-computer", todo_id: "t2" },
        { title: "Task 3", prompt: "c", cwd: "/example", machine: "example-computer", todo_id: "t3" },
        { title: "Task 4", prompt: "d", cwd: "/example", machine: "example-computer", todo_id: "t4" },
        { title: "Task 5", prompt: "e", cwd: "/example", machine: "example-computer", todo_id: "t5" },
      ],
    };
    getFeedState().plans[plan.id] = plan;
    await show(
      { id: "plan-five-ev", conversation, at, kind: "plan", ref: plan.id },
      [
        linked("t1", "Task 1", [s1]),
        linked("t2", "Task 2", [s2]),
        linked("t3", "Task 3", [s3]),
        linked("t4", "Task 4", []),
        linked("t5", "Task 5", []),
      ],
      [s1, s2, s3],
    );

    const group = host.querySelector("[data-handoff-cards]")!;
    expect(group.getAttribute("data-handoff-cards")).toBe("5");
    expect(group.getAttribute("data-handoff-group")).toBe("collapsed");
    expect(toggleBtn()!.getAttribute("aria-expanded")).toBe("false");
    expect(toggleBtn()!.textContent).toContain(t("assistant-tasks.multi_handoff_title", { count: 5 }));
    expect(toggleBtn()!.textContent).toContain(t("assistant-tasks.multi_handoff_expand"));
    expect(summaryEl()!.querySelector('[data-handoff-summary-state="starting"]')?.textContent).toBe(t("assistant-tasks.multi_handoff_starting", { count: 2 }));
    expect(summaryEl()!.querySelector('[data-handoff-summary-state="working"]')?.textContent).toBe(stateCount("working", 3));
    expect(cards()).toHaveLength(0);

    // Expanding shows all 5 cards in original order and preserves card click behavior
    act(() => toggleBtn()!.click());
    expect(group.getAttribute("data-handoff-group")).toBe("expanded");
    expect(toggleBtn()!.getAttribute("aria-expanded")).toBe("true");
    expect(toggleBtn()!.textContent).toContain(t("chat-interactions.collapse"));
    expect(cards().map((c) => c.getAttribute("data-handoff-card"))).toEqual([
      "example-computer/s1",
      "example-computer/s2",
      "example-computer/s3",
      "todo:t4",
      "todo:t5",
    ]);
    act(() => cards()[1]!.click());
    expect(getPanel()).toEqual({ kind: "agent", mid: "example-computer", sid: "s2" });

    // Collapsing folds cards again
    act(() => toggleBtn()!.click());
    expect(group.getAttribute("data-handoff-group")).toBe("collapsed");
    expect(cards()).toHaveLength(0);
  });

  it("highlights urgent attention counts and surfaces up to 2 urgent cards while collapsed without duplicating when expanded", async () => {
    const sWorking1 = mkSession("w1", "Working 1", "working");
    const sWorking2 = mkSession("w2", "Working 2", "working");
    const sNeeds = mkSession("n1", "Waiting Agent", "needs_you");
    const sCut = mkSession("c1", "Cut Agent", "idle", { turn_error: "upstream 502" });
    const plan: TaskPlan = {
      id: "plan-urgent",
      conversation,
      state: "started",
      created_at: at,
      tasks: [
        { title: "Working 1", prompt: "a", cwd: "/example", machine: "example-computer", todo_id: "t1" },
        { title: "Needs you task", prompt: "b", cwd: "/example", machine: "example-computer", todo_id: "t2" },
        { title: "Cut task", prompt: "c", cwd: "/example", machine: "example-computer", todo_id: "t3" },
        { title: "Failed task", prompt: "d", cwd: "/example", machine: "example-computer", todo_id: "t4" },
        { title: "Working 2", prompt: "e", cwd: "/example", machine: "example-computer", todo_id: "t5" },
      ],
    };
    getFeedState().plans[plan.id] = plan;
    await show(
      { id: "plan-urgent-ev", conversation, at, kind: "plan", ref: plan.id },
      [
        linked("t1", "Working 1", [sWorking1]),
        linked("t2", "Needs you task", [sNeeds]),
        linked("t3", "Cut task", [sCut]),
        linked("t4", "Failed task", [], { start_error: "machine offline", done_at: at }),
        linked("t5", "Working 2", [sWorking2]),
      ],
      [sWorking1, sWorking2, sNeeds, sCut],
    );

    // Summary shows semantic tones for all urgent states
    const failedSeg = summaryEl()!.querySelector('[data-handoff-summary-state="failed"]')!;
    const cutSeg = summaryEl()!.querySelector('[data-handoff-summary-state="interrupted"]')!;
    const needsSeg = summaryEl()!.querySelector('[data-handoff-summary-state="needs_you"]')!;
    expect(failedSeg.className).toContain("text-red");
    expect(cutSeg.className).toContain("text-red");
    expect(needsSeg.className).toContain("text-attention");

    // Collapsed: surfaces at most 2 urgent cards, prioritizing failed/interrupted ahead of needs_you
    expect(host.querySelector("[data-handoff-surfaced]")).not.toBeNull();
    expect(cards().map((c) => c.getAttribute("data-handoff-card"))).toEqual([
      "example-computer/c1",
      "todo:t4",
    ]);

    // Expanded: shows all 5 cards in original order with zero duplicates
    act(() => toggleBtn()!.click());
    expect(host.querySelector("[data-handoff-surfaced]")).toBeNull();
    expect(cards().map((c) => c.getAttribute("data-handoff-card"))).toEqual([
      "example-computer/w1",
      "example-computer/n1",
      "example-computer/c1",
      "todo:t4",
      "example-computer/w2",
    ]);
  });

  it("keeps expand/collapse state stable across live status transitions (starting -> working -> done)", async () => {
    const plan: TaskPlan = {
      id: "plan-live",
      conversation,
      state: "started",
      created_at: at,
      tasks: [
        { title: "Task 1", prompt: "a", cwd: "/example", machine: "example-computer", todo_id: "t1" },
        { title: "Task 2", prompt: "b", cwd: "/example", machine: "example-computer", todo_id: "t2" },
        { title: "Task 3", prompt: "c", cwd: "/example", machine: "example-computer", todo_id: "t3" },
      ],
    };
    getFeedState().plans[plan.id] = plan;
    await show(
      { id: "plan-live-ev", conversation, at, kind: "plan", ref: plan.id },
      [linked("t1", "Task 1", []), linked("t2", "Task 2", []), linked("t3", "Task 3", [])],
      [],
    );
    expect(summaryEl()!.textContent).toContain(t("assistant-tasks.multi_handoff_starting", { count: 3 }));

    // User expands while tasks are starting
    act(() => toggleBtn()!.click());
    expect(toggleBtn()!.getAttribute("aria-expanded")).toBe("true");
    expect(cards()).toHaveLength(3);

    // Agents start and move to working
    const s1 = mkSession("s1", "Agent 1", "working");
    const s2 = mkSession("s2", "Agent 2", "working");
    const s3 = mkSession("s3", "Agent 3", "unread");
    served = [linked("t1", "Task 1", [s1]), linked("t2", "Task 2", [s2]), linked("t3", "Task 3", [s3])];
    await act(async () => {
      applyCompleteSessions([s1, s2, s3]);
      await refreshTodos();
    });
    expect(toggleBtn()!.getAttribute("aria-expanded")).toBe("true");
    expect(summaryEl()!.querySelector('[data-handoff-summary-state="working"]')?.textContent).toBe(stateCount("working", 2));
    expect(summaryEl()!.querySelector('[data-handoff-summary-state="done"]')?.textContent).toBe(stateCount("done", 1));
    expect(cards()).toHaveLength(3);

    // All finish -> still expanded
    const d1 = mkSession("s1", "Agent 1", "idle");
    const d2 = mkSession("s2", "Agent 2", "idle");
    const d3 = mkSession("s3", "Agent 3", "idle");
    await act(async () => {
      applyCompleteSessions([d1, d2, d3]);
    });
    expect(toggleBtn()!.getAttribute("aria-expanded")).toBe("true");
    expect(summaryEl()!.querySelector('[data-handoff-summary-state="done"]')?.textContent).toBe(stateCount("done", 3));
  });

  it("groups consecutive standalone task feed items into one collapsible summary and splits on intervening messages or open plans", async () => {
    const s1 = mkSession("s1", "Agent 1", "working");
    const s2 = mkSession("s2", "Agent 2", "working");
    const s3 = mkSession("s3", "Agent 3", "needs_you");
    const f1: FeedItem = { id: "f1", conversation, at, kind: "task", ref: "t1" };
    const f2: FeedItem = { id: "f2", conversation, at, kind: "task", ref: "t2" };
    const f3: FeedItem = { id: "f3", conversation, at, kind: "task", ref: "t3" };
    const notice: FeedItem = { id: "n1", conversation, at, kind: "notice", body: { text: "Between runs" } };
    const f4: FeedItem = { id: "f4", conversation, at, kind: "task", ref: "t4" };
    const f5: FeedItem = { id: "f5", conversation, at, kind: "task", ref: "t5" };

    const lines: DisplayLine[] = [
      { kind: "feed", id: "feed-f1", item: f1 },
      { kind: "feed", id: "feed-f2", item: f2 },
      { kind: "feed", id: "feed-f3", item: f3 },
      { kind: "feed", id: "feed-n1", item: notice },
      { kind: "feed", id: "feed-f4", item: f4 },
      { kind: "feed", id: "feed-f5", item: f5 },
    ];
    const grouped = groupAssistantLines(lines);
    expect(grouped.map((g) => g.kind)).toEqual(["handoff_run", "feed", "handoff_run"]);

    served = [
      linked("t1", "Task 1", [s1]),
      linked("t2", "Task 2", [s2]),
      linked("t3", "Task 3", [s3]),
      linked("t4", "Task 4", []),
      linked("t5", "Task 5", []),
    ];
    await act(async () => {
      applyCompleteSessions([s1, s2, s3]);
      await refreshTodos();
    });

    const stateMap = new Map<string, boolean>();
    const renderOther = (it: FeedItem) => <FeedLineView item={it} simplified />;
    act(() => {
      root.render(
        <StrictMode>
          <HandoffGroupStateContext.Provider value={stateMap}>
            {grouped.map((g) =>
              g.kind === "handoff_run"
                ? <HandoffFeedRun key={g.id} items={g.items} renderOther={renderOther} />
                : <FeedLineView key={g.id} item={(g as Extract<DisplayLine, { kind: "feed" }>).item} simplified />,
            )}
          </HandoffGroupStateContext.Provider>
        </StrictMode>,
      );
    });
    await settle();

    // First run (3 tasks) is collapsed with the 1 needs_you card surfaced; second run (2 tasks) stays expanded
    const groups = [...host.querySelectorAll<HTMLElement>("[data-handoff-cards]")];
    expect(groups).toHaveLength(2);
    expect(groups[0]!.getAttribute("data-handoff-cards")).toBe("3");
    expect(groups[0]!.getAttribute("data-handoff-group")).toBe("collapsed");
    expect([...groups[0]!.querySelectorAll("[data-handoff-card]")].map((c) => c.getAttribute("data-handoff-card"))).toEqual(["example-computer/s3"]);
    expect(groups[1]!.getAttribute("data-handoff-cards")).toBe("2");
    expect(groups[1]!.getAttribute("data-handoff-group")).toBeNull();
    expect(groups[1]!.querySelectorAll("[data-handoff-card]")).toHaveLength(2);

    // Expanding first run persists via HandoffGroupStateContext even if a new task joins the run
    act(() => groups[0]!.querySelector<HTMLButtonElement>("[data-handoff-group-toggle]")!.click());
    expect(stateMap.get("f1")).toBe(true);
    expect(groups[0]!.querySelectorAll("[data-handoff-card]")).toHaveLength(3);
  });

  it("splits around an open plan inside a consecutive plan/task run", async () => {
    const openPlan: TaskPlan = {
      id: "plan-open",
      conversation,
      state: "open",
      created_at: at,
      tasks: [{ title: "Needs approval", prompt: "a", cwd: "/example", machine: "example-computer" }],
    };
    getFeedState().plans[openPlan.id] = openPlan;
    served = [linked("t1", "Standalone Task", [agent])];
    await act(async () => {
      applyCompleteSessions([agent]);
      await refreshTodos();
    });
    const runItems: FeedItem[] = [
      { id: "open-ev", conversation, at, kind: "plan", ref: openPlan.id },
      { id: "task-ev", conversation, at, kind: "task", ref: "t1" },
    ];
    act(() => {
      root.render(
        <StrictMode>
          <HandoffFeedRun items={runItems} renderOther={(it) => <FeedLineView item={it} simplified />} />
        </StrictMode>,
      );
    });
    await settle();
    expect(host.querySelector("[data-plan-actions]")).not.toBeNull();
    expect(cards()).toHaveLength(1);
    expect(cards()[0]!.getAttribute("data-handoff-card")).toBe(`example-computer/${agent.session_id}`);
  });
});
