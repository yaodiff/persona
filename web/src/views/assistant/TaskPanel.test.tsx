// @vitest-environment jsdom
// Exercise the real side panel, chat header and menu: a task is an entry point, not the destination of its Agent chat.
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi, type MockInstance } from "vitest";

vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  globalThis.IntersectionObserver ??= class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } } as unknown as typeof IntersectionObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

import { t } from "../../i18n";
import { closePanel, getPanel, openPanel, popPanel, type Panel } from "../../lib/panel";
import { href } from "../../lib/router";
import { handoffFrom } from "../../lib/handoff";
import { applyCompleteSessions, getState, refreshTodos, reset } from "../../lib/store";
import { TODO_STATUS_INFO, type Todo } from "../../lib/todos";
import type { Session } from "../../lib/types";
import { later, loadFeed, type TaskPlan } from "../../lib/feed";
import { decisionLaterKey } from "../../lib/assistant-entries";
import { SidePanel } from "../SidePanel";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const at = "2026-11-10T08:00:00Z";
const first: Session = { machine_id: "example-computer", session_id: "example-agent", title: "Example Agent", cwd: "/example", state: "working", status: "working", todo: { id: "example-task", title: "Example task" } } as Session;
const second: Session = { ...first, machine_id: "example-other", session_id: "other-agent", title: "Other Agent" };
const task = (sessions = [first]): Todo => ({
  id: "example-task", title: "Example task", origin: "example-conversation", created_at: at, updated_at: at,
  agents: sessions.map((s) => ({ machine_id: s.machine_id, session_id: s.session_id, title: s.title, linked_at: at })),
});
let servedTodo: Todo;
let servedPlans: Record<string, TaskPlan> = {};
let host: HTMLDivElement;
let root: Root;
let newWindow: MockInstance<typeof window.open>;
const settle = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); });

// SidePanel loads the task and Agent panels on first use: loaded here once, so each test's lazy panel resolves at once
beforeAll(async () => {
  await import("./TaskPanel");
}, 60_000);

beforeEach(() => {
  servedPlans = {};
  vi.stubGlobal("fetch", vi.fn(async (url: string) => new Response(JSON.stringify(
    String(url).includes("/todos") ? { todos: [servedTodo] }
      : String(url).includes("/feed") ? { items: [], plans: servedPlans }
        : String(url).includes("/items") ? { items: [], to_seq: 0 } : {},
  ), { headers: { "Content-Type": "application/json" } })));
  newWindow = vi.spyOn(window, "open").mockReturnValue(null);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  closePanel();
  reset();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function open(panel: Panel, todo = task(), sessions = [first], plans?: Record<string, TaskPlan>) {
  servedTodo = todo;
  servedPlans = plans ?? {};
  await act(async () => {
    applyCompleteSessions(sessions);
    await refreshTodos();
    if (plans) await loadFeed("example-conversation");
  });
  act(() => {
    openPanel(panel);
    root.render(<StrictMode><SidePanel /></StrictMode>);
  });
  // the task and Agent panels load with the first one opened (SidePanel's lazy chunk): React.lazy suspends once on its first
  // render even when the module is in, as long as the machine is slow. Each poll runs in act so React's retry can flush.
  await vi.waitFor(async () => {
    await act(async () => { await new Promise((r) => setTimeout(r, 20)); });
    expect(host.querySelector("aside header")).not.toBeNull();
  }, { timeout: 5000, interval: 20 });
  for (let i = 0; i < 6; i++) await settle();
  expect(host.querySelector("aside header")).not.toBeNull();
}
async function clickMore() {
  const button = host.querySelector<HTMLButtonElement>(`aside header button[aria-label="${t("chat-chatview.more_actions")}"]`)
    ?? host.querySelector<HTMLButtonElement>(`aside button[aria-label="${t("assistant-taskpanel.more")}"]`);
  expect(button).not.toBeNull();
  act(() => button!.click());
  await settle();
}
async function clickMenu(label: string) {
  const item = [...document.querySelectorAll<HTMLElement>('[role="menuitem"], [role="menuitemradio"]')].find((el) => [...el.querySelectorAll("span")].some((span) => span.textContent?.trim() === label));
  expect(item, label).toBeDefined();
  act(() => item!.click());
  await settle();
}
async function expectSessionWindow(s = first) {
  await clickMore();
  await clickMenu(t("assistant-taskpanel.open_in_new_window"));
  expect(newWindow).toHaveBeenLastCalledWith(href({ name: "session", mid: s.machine_id, sid: s.session_id }), "_blank", "noopener");
  expect(newWindow.mock.calls.at(-1)?.[0]).not.toContain("/todos");
}

it("answers a rolled-up step's decision from its task panel", async () => {
  const step: Todo = {
    ...task(), part_of: "example-parent",
    supervision: { state: "needs_user", supervisor: "example-conversation" },
    ask: {
      asked_at: at, outcome: "decide", verdict: "Choose a release", question: "Which release should go out?",
      recommend: "Preview", options: [{ label: "Preview" }, { label: "Stable" }],
    },
    view: { phase: "needs_you", section: "needs_you", reason: "owner_decides", rank: 0 },
  };
  await open({ kind: "task", todo: step.id }, step);
  await expect.poll(() => host.querySelector("aside [data-decision-question]")?.textContent).toBe(step.ask?.question);
  const laterButton = [...host.querySelectorAll<HTMLButtonElement>("aside button")].find((button) => button.textContent === t("assistant-task-actions.later"));
  expect(laterButton).toBeDefined();
  await act(async () => laterButton!.click());
  expect(host.querySelector("aside [data-decision-question]")).toBeNull();
  await act(async () => later(decisionLaterKey(step), false));
  const option = [...host.querySelectorAll<HTMLButtonElement>("aside button")].find((button) => button.textContent === "Stable");
  expect(option).toBeDefined();
  await act(async () => option!.click());
  const writes = vi.mocked(fetch).mock.calls.filter(([url, init]) => String(url).endsWith("/accept") && init?.method === "POST");
  expect(writes.map(([url, init]) => [url, JSON.parse(String(init?.body))])).toEqual([
    [`/api/v1/todos/${step.id}/accept`, { asked_at: at, option: "Stable" }],
  ]);
});

describe("Agent panel new-window destination", () => {
  it("opens the verification report first and restores it after inspecting a released worker", async () => {
    const todo = task();
    todo.agents![0].released_at = at;
    todo.supervision = { supervisor: "owner", state: "user_check", done_when: ["Check the route"], criteria: [{ criterion: "Check the route", state: "met", evidence: "Both transfers checked" }] };
    todo.ask = { verdict: "Ready to verify", summary: "The itinerary is ready.", asked_at: at, link: "https://example.com/itinerary" };
    await open({ kind: "task", todo: todo.id }, todo);
    expect(host.textContent).toContain(todo.ask.summary);
    expect(host.textContent).toContain("Both transfers checked");
    expect(host.querySelector("a")?.href).toBe(todo.ask.link);
    const button = [...host.querySelectorAll("button")].find((b) => b.textContent === t("assistant-taskpanel.raw_session"));
    act(() => button!.click());
    expect(getPanel()).toEqual({ kind: "agent", mid: first.machine_id, sid: first.session_id });
    act(() => { popPanel(); });
    expect(host.textContent).toContain(todo.ask.summary);
  });

  it("opens the displayed Agent session from a task card or task-backed In progress tray entry", async () => {
    await open({ kind: "task", todo: "example-task" });
    expect(host.querySelector("aside header h1")?.textContent).toBe("Example task");
    await expectSessionWindow();
  });

  it("opens a directly opened Agent session, even when that Agent is linked to a task", async () => {
    await open({ kind: "agent", mid: first.machine_id, sid: first.session_id });
    expect(host.querySelector("aside header h1")?.textContent).toBe(first.title);
    await expectSessionWindow();
  });

  it("follows the Agent selected in a multi-Agent task, including its machine", async () => {
    await open({ kind: "task", todo: "example-task" }, task([first, second]), [first, second]);
    await expectSessionWindow();
    act(() => host.querySelector<HTMLButtonElement>(`aside button[aria-label="${t("assistant-taskpanel.switch_agent")}"]`)!.click());
    await settle();
    await clickMenu(second.title);
    await expectSessionWindow(second);
  });

  it("uses linked Agent identity before the session list arrives", async () => {
    await open({ kind: "task", todo: "example-task" }, task(), []);
    await expectSessionWindow();
  });

  it("opens the task only while the panel has no unreleased Agent to show", async () => {
    await open({ kind: "task", todo: "example-task" }, task([]), []);
    await clickMore();
    await clickMenu(t("assistant-taskpanel.open_in_new_window"));
    expect(newWindow).toHaveBeenCalledExactlyOnceWith(href({ name: "todos", id: "example-task" }), "_blank", "noopener");
  });

  it("keeps the separate linked-task menu item pointing to the task", async () => {
    const before = location.href;
    await open({ kind: "task", todo: "example-task" });
    try {
      await clickMore();
      await clickMenu(t("chat-chatview.todo_label", { title: "Example task" }));
      expect(location.pathname + location.search).toBe("/todos?id=example-task");
      expect(newWindow).not.toHaveBeenCalled();
    } finally {
      history.replaceState({}, "", before);
    }
  });
});

describe("Open Agent page button", () => {
  const button = () => host.querySelector<HTMLButtonElement>(`aside header button[aria-label="${t("chat-chatview.open_agent_page")}"]`);
  async function expectJump(s: Session) {
    const before = location.href;
    try {
      const b = button();
      expect(b).not.toBeNull();
      expect(b!.title).toBe(t("chat-chatview.open_agent_page"));
      // it sits just before ×, after the header's other buttons
      expect(b!.nextElementSibling?.getAttribute("aria-label")).toBe(t("sidepanel.close"));
      act(() => b!.click());
      await settle();
      expect(location.pathname).toBe(href({ name: "session", mid: s.machine_id, sid: s.session_id }));
      expect(getPanel()).toBeNull();
      expect(newWindow).not.toHaveBeenCalled();
    } finally {
      history.replaceState({}, "", before);
    }
  }

  it("opens the task's Agent as its own page and closes the panel", async () => {
    await open({ kind: "task", todo: "example-task" });
    await expectJump(first);
  });

  it("follows the Agent picked in a multi-Agent task", async () => {
    await open({ kind: "task", todo: "example-task" }, task([first, second]), [first, second]);
    act(() => host.querySelector<HTMLButtonElement>(`aside button[aria-label="${t("assistant-taskpanel.switch_agent")}"]`)!.click());
    await settle();
    await clickMenu(second.title);
    await expectJump(second);
  });

  it("from a conversation, says where it came from and Back opens the task's panel again", async () => {
    const before = location.href;
    history.replaceState(null, "", "/persona");
    try {
      await open({ kind: "task", todo: "example-task" });
      act(() => button()!.click());
      await settle();
      expect(location.pathname).toBe(href({ name: "session", mid: first.machine_id, sid: first.session_id }));
      expect(handoffFrom()).toEqual({ path: "/persona" });
      expect(getPanel()).toBeNull();
      await act(async () => {
        await new Promise<void>((done) => {
          addEventListener("popstate", () => setTimeout(done, 0), { once: true });
          history.back();
        });
      });
      expect(location.pathname).toBe("/persona");
      expect(getPanel()).toEqual({ kind: "task", todo: "example-task" });
    } finally {
      history.replaceState({}, "", before);
    }
  });

  it("opens a directly opened Agent's page", async () => {
    await open({ kind: "agent", mid: first.machine_id, sid: first.session_id });
    await expectJump(first);
  });

  it.each([
    ["not started", task([])],
    ["failed to start", { ...task([]), start_error: "Synthetic start failure" }],
    ["released", { ...task(), id: "released-agent", agents: [{ ...task().agents![0], released_at: at }] }],
  ])("is absent while the task has no Agent to show (%s)", async (_, todo) => {
    await open({ kind: "task", todo: todo.id }, todo, []);
    expect(host.querySelector("aside header")).not.toBeNull();
    expect(button()).toBeNull();
  });
});

describe("task panel dispatch feedback (#7552)", () => {
  const emptyTask = (id: string, patch: Partial<Todo> = {}): Todo => ({ ...task([]), id, ...patch });
  const planFor = (todo: Todo, state: TaskPlan["state"], error?: string): TaskPlan => ({
    id: `${todo.id}-plan`, conversation: "example-conversation", state,
    tasks: [{ title: todo.title, prompt: "Synthetic task", machine: "example-computer", cwd: "/example", todo_id: todo.id, error }],
  });
  const panelText = () => host.querySelector("aside")?.textContent ?? "";
  const expectNeutral = () => {
    expect(panelText()).toContain(t("assistant-taskpanel.no_agent"));
    expect(panelText()).not.toContain(t("assistant-taskpanel.starting"));
  };

  it.each([undefined, "example-conversation"])("does not claim a never-dispatched to-do is starting (origin %s)", async (origin) => {
    const todo = emptyTask(`unstarted-${origin ? "conversation" : "personal"}`, { origin });
    await open({ kind: "task", todo: todo.id }, todo, []);
    expect(host.querySelector("aside header")?.textContent).toContain(TODO_STATUS_INFO.not_started.label);
    expectNeutral();
  });

  it("keeps Starting while a plan-created task has not received its plan yet", async () => {
    const todo = emptyTask("unloaded-plan-task", { plan_id: "not-loaded-plan" });
    await open({ kind: "task", todo: todo.id }, todo, []);
    expect(panelText()).toContain(t("assistant-taskpanel.starting"));
    expect(panelText()).not.toContain(t("assistant-taskpanel.no_agent"));
    expect(host.querySelector("aside header")?.textContent).toContain(t("assistant-tasks.status_starting"));
  });

  it.each(["open", "started"] as const)("keeps Starting when a loaded %s plan takes over the to-do", async (state) => {
    const todo = emptyTask(`takeover-${state}`);
    const plan = planFor(todo, state);
    await open({ kind: "task", todo: todo.id }, todo, [], { [plan.id]: plan });
    expect(panelText()).toContain(t("assistant-taskpanel.starting"));
    expect(panelText()).not.toContain(t("assistant-taskpanel.no_agent"));
    expect(host.querySelector("aside header")?.textContent).toContain(t("assistant-tasks.status_starting"));
  });

  it("drops the pending explanation when a takeover plan is dismissed", async () => {
    const todo = emptyTask("dismissed-takeover");
    const plan = planFor(todo, "started");
    await open({ kind: "task", todo: todo.id }, todo, [], { [plan.id]: plan });
    expect(panelText()).toContain(t("assistant-taskpanel.starting"));
    servedPlans = { [plan.id]: { ...plan, state: "dismissed" } };
    await act(async () => { await loadFeed("example-conversation"); });
    expect(host.querySelector("aside header")?.textContent).toContain(TODO_STATUS_INFO.not_started.label);
    expectNeutral();
  });

  it("updates a neutral panel when a plan later takes over the to-do", async () => {
    const todo = emptyTask("later-takeover", { origin: undefined });
    await open({ kind: "task", todo: todo.id }, todo, []);
    expectNeutral();
    const plan = planFor(todo, "open");
    servedPlans = { [plan.id]: plan };
    await act(async () => { await loadFeed("example-conversation"); });
    expect(panelText()).toContain(t("assistant-taskpanel.starting"));
    expect(panelText()).not.toContain(t("assistant-taskpanel.no_agent"));
    expect(host.querySelector("aside header")?.textContent).toContain(t("assistant-tasks.status_starting"));
  });

  it.each(["todo", "legacy-plan"] as const)("keeps the %s start failure and retry action", async (source) => {
    const todo = emptyTask(`failed-${source}`);
    const plan = planFor(todo, "started", "Synthetic start failure");
    if (source === "todo") todo.start_error = "Synthetic start failure";
    else todo.plan_id = plan.id;
    await open({ kind: "task", todo: todo.id }, todo, [], source === "legacy-plan" ? { [plan.id]: plan } : undefined);
    expect(panelText()).toContain(t("assistant-taskpanel.start_failed"));
    expect(panelText()).toContain("Synthetic start failure");
    expect(panelText()).toContain(t("assistant-taskpanel.retry"));
    expect(panelText()).not.toContain(t("assistant-taskpanel.starting"));
    expect(panelText()).not.toContain(t("assistant-taskpanel.no_agent"));
  });

  it("offers the computer's other launcher before Retry when its own can't start there, where the hub takes it (#10365)", async () => {
    const todo = emptyTask("failed-launcher", { start_error: "Synthetic terminal refusal", done_at: at });
    const plan = planFor(todo, "started", "Synthetic terminal refusal");
    plan.tasks[0] = { ...plan.tasks[0], error_code: "launcher_missing", error_args: { where: "terminal", launcher: "work" }, retry_with: ["claude"] };
    const features = getState().features;
    getState().features = ["task_retry_launcher"];
    try {
      await open({ kind: "task", todo: todo.id }, todo, [], { [plan.id]: plan });
      const instead = host.querySelector<HTMLButtonElement>("[data-testid=task-retry-launcher]");
      expect(instead?.textContent).toBe(t("assistant-task-actions.use_launcher_instead", { launcher: "claude" }));
      expect(panelText()).toContain(t("assistant-taskpanel.retry"));
      await act(async () => instead!.click());
      await settle();
      const post = vi.mocked(fetch).mock.calls.find(([url, init]) => String(url).endsWith("/todos/failed-launcher/retry") && init?.method === "POST");
      expect(post?.[1]?.body).toBe(JSON.stringify({ launcher: "claude" }));
    } finally {
      getState().features = features;
    }
  });

  it("answers the question its tab's shell stopped on beside Retry, where the hub takes it (#10365)", async () => {
    const todo = emptyTask("failed-question", { start_error: "Synthetic shell question", done_at: at });
    const plan = planFor(todo, "started", "Synthetic shell question");
    plan.tasks[0] = { ...plan.tasks[0], error_code: "shell_waiting", error_args: { line: "Press Enter to continue", pane: "w1:p2", kind: "press_key",
      choices: "enter", kept_until: new Date(Date.now() + 5 * 60_000).toISOString() } };
    const features = getState().features;
    getState().features = ["todo_start_answer"];
    try {
      await open({ kind: "task", todo: todo.id }, todo, [], { [plan.id]: plan });
      const choice = () => host.querySelector<HTMLButtonElement>("[data-testid=shell-choice-enter]");
      await vi.waitFor(async () => {
        await settle();
        expect(choice()).not.toBeNull();
      }, { timeout: 5000, interval: 20 });
      expect(panelText()).toContain(t("assistant-taskpanel.retry"));
      await act(async () => choice()!.click());
      await settle();
      const post = vi.mocked(fetch).mock.calls.find(([url, init]) => String(url).endsWith("/todos/failed-question/start/answer") && init?.method === "POST");
      expect(post?.[1]?.body).toBe(JSON.stringify({ choice: "enter" }));
    } finally {
      getState().features = features;
    }
  });

  it("does not describe released or completed work as a new Agent start", async () => {
    const released = emptyTask("released-task", { agents: [{ ...task().agents![0], released_at: at }] });
    await open({ kind: "task", todo: released.id }, released, []);
    expectNeutral();
    servedTodo = emptyTask("completed-task", { done_at: at, plan_id: "completed-plan" });
    await act(async () => { await refreshTodos(); openPanel({ kind: "task", todo: servedTodo.id }); });
    await settle();
    expect(host.querySelector("aside header")?.textContent).toContain(TODO_STATUS_INFO.done.label);
    expectNeutral();
  });
});
