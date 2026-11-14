// @vitest-environment jsdom
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { getFeedState, loadFeed, type FeedItem, type TaskPlan } from "../../lib/feed";
import { setAgentOpenMode } from "../../lib/agent-open";
import { closePanel, getPanel } from "../../lib/panel";
import { putTodo, refresh, refreshTodos, reset, upsertSession } from "../../lib/store";
import type { Todo } from "../../lib/todos";
import { usageLimitLabel } from "../../lib/usage-limit";
import type { Interaction, Session } from "../../lib/types";
import { AssistantActionBar, DOCK_CAPSULE, FeedLineView, TaskDock } from "./Tasks";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const at = "2026-11-10T08:00:00Z";
// "Now" for every case: an hour after the fixtures, not the real clock, by which they would be long shelved
// (SHELVE_AFTER_MS, lib/todo-triage.ts) and the trays would leave them out (#5531). Only Date is faked: timers stay real.
const now = new Date("2026-11-10T09:00:00Z");
const conversation = "tray-demo";
const plan: TaskPlan = { id: "tray-plan", conversation, state: "started", created_at: at, tasks: [{ title: "Demo task", prompt: "Demo checks", cwd: "/demo", machine: "demo", todo_id: "tray-task" }] };
let todo: Todo;
let host: HTMLDivElement;
let root: Root;
let writes: string[];
/** More tasks, agents and cards a test adds to what the hub answers; `mainChat`: the main conversation's id. */
let moreTodos: Todo[];
let moreSessions: Session[];
let moreInteractions: Interaction[];
let mainChat: string | undefined;
const feed: FeedItem[] = [
  { id: "tray-plan-event", conversation, at, kind: "plan", ref: plan.id },
  { id: "tray-task-event", conversation, at, kind: "task", ref: "tray-task" },
  { id: "tray-review-event", conversation, at, kind: "review", ref: "tray-task", body: { verdict: "accepted" } },
  { id: "tray-receipt", conversation, at, kind: "receipt", ref: "tray-task", body: { state: "done", summary: "Demo result" } },
];

beforeEach(async () => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(now);
  writes = [];
  moreTodos = [];
  moreSessions = [];
  moreInteractions = [];
  mainChat = undefined;
  todo = { id: "tray-task", title: "Demo task", origin: conversation, plan_id: plan.id, created_at: at, updated_at: at, pr_url: "https://github.com/example/demo/pull/42", agents: [{ machine_id: "demo", session_id: "worker", linked_at: at }], supervision: { supervisor: conversation, progress: { kind: "progress", step: 2, steps: 5, text: "Run the checks", at } } };
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const path = String(url);
    if (init?.method && init.method !== "GET") writes.push(path);
    const body = path.includes("/feed") ? { items: feed, plans: { [plan.id]: plan }, task_cards: [{ id: `plan:${plan.id}:0`, plan_id: plan.id, index: 0, todo_id: todo.id, at, updated_at: at, state: "accepted" }] } : path.includes("/todos") ? { todos: [todo, ...moreTodos] } : path.includes("/sessions") ? { sessions: [{ machine_id: "demo", session_id: "worker", title: "Demo agent", state: "working", status: "working", updated_at: at }, ...moreSessions] } : path.includes("/interactions") ? { interactions: moreInteractions } : /\/assistant(\?|$)/.test(path) && mainChat ? { machine_id: "demo", main: mainChat, conversations: [] } : {};
    return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
  }));
  await act(async () => {
    await refresh();
    await loadFeed(conversation);
    await refreshTodos();
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<StrictMode><AssistantActionBar conversation={conversation} />{feed.map((item) => <FeedLineView key={item.id} item={item} simplified />)}</StrictMode>));
});
afterEach(() => {
  vi.useRealTimers();
  act(() => root.unmount());
  host.remove();
  history.replaceState(null, "", "/");
  setAgentOpenMode("panel");
  closePanel();
  reset();
  delete getFeedState().plans[plan.id];
  delete getFeedState().feeds[conversation];
  delete getFeedState().taskCards[conversation];
  vi.unstubAllGlobals();
});
const entries = () => [...host.querySelectorAll<HTMLButtonElement>("[data-assistant-actionbar] [data-tray-entry]")];
const entry = (key: string) => host.querySelector<HTMLButtonElement>(`[data-assistant-actionbar] [data-tray-entry="${key}"]`)!;

/** Opens the Needs you rows the tray folds to a line (#5569); a row with its key opens that one. */
const openRows = (key?: string) => act(async () => host.querySelectorAll<HTMLButtonElement>(`button[data-needs-row${key ? `="${key}"` : ""}][aria-expanded="false"]`).forEach((b) => b.click()));

const label = { needs: t("assistant-tasks.tray_needs_you"), running: t("assistant-tasks.tray_running") };
// where a click went as a page (set to Full page, #5096); the side panel, the default, says so itself (getPanel)
const opened = () => location.pathname + location.search;
const trays = () => host.querySelector("[data-assistant-actionbar]")!.textContent;

describe("Assistant Needs you / In progress trays", () => {
  it("uses the Working mark and live subtitle for background work, with a clock only after all work ends", () => {
    act(() => entry("running").click());
    const row = host.querySelector("[data-task-slot]")!;
    const session: Session = { machine_id: "demo", session_id: "worker", title: "Demo agent", cwd: "/demo", status: "working", state: "working", updated_at: at,
      waiting: true, background_working: true, background_count: 2, background_tasks_count: 2 };
    act(() => upsertSession(session));
    expect(host.querySelector("[data-task-slot]")).toBe(row);
    expect(row.querySelector('[data-task-mark="working"]')).not.toBeNull();
    expect(row.querySelector('[data-task-mark="waiting"]')).toBeNull();
    expect(row.textContent).toContain(t("assistant-tasks.background_tasks_running", { count: 2 }));
    const workflow = { task_id: "wf", status: "running", running: true, phase: "Adversarial review", agents_done: 5, agents_started: 6 };
    act(() => upsertSession({ ...session, background_tasks_count: 0, background_count: 1, running_workflows: 1, workflow_progress: workflow }));
    expect(row.textContent).toContain("Adversarial review · 5/6+");
    expect(row.querySelector('[data-task-mark="working"]')).not.toBeNull();
    act(() => upsertSession({ ...session, background_tasks_count: 0, background_count: 0, running_subagents: 3 }));
    expect(row.textContent).toContain(t("assistant-tasks.subagents_count_running", { count: 3 }));
    expect(row.textContent).not.toContain("Adversarial review");
    act(() => upsertSession({ ...session, state: "idle", status: "idle", background_working: false, background_count: 0,
      background_tasks_count: 0, work: { phase: "blocked", on: "external", wait: "Wait for deployment", line: "Wait for deployment" } }));
    expect(row.querySelector('[data-task-mark="waiting"]')).not.toBeNull();
    expect(row.querySelector('[data-task-mark="working"]')).toBeNull();
    expect(row.textContent).toContain("Wait for deployment");
    expect(row.textContent).not.toContain(t("assistant-tasks.background_tasks_running", { count: 2 }));
    expect(writes).toEqual([]);
  });

  it("puts a row's status in its own column, with › where it opens and the reset time under a usage limit (#5569)", () => {
    act(() => entry("running").click());
    const row = () => host.querySelector("[data-task-slot]")!;
    const status = () => row().querySelector<HTMLElement>("[data-task-status]")!;
    expect(status().textContent).toBe(t("lib-state-labels.working"));
    expect(row().querySelector(".font-semibold")!.textContent).toBe("Demo task"); // the title no longer carries the word
    const limited: Session = { machine_id: "demo", session_id: "worker", title: "Demo agent", cwd: "/demo", status: "idle", state: "idle", updated_at: at,
      turn_error: "You've hit your usage limit.", turn_error_kind: "usage_limit", turn_error_resets_at: new Date(now.getTime() + 3 * 3_600_000).toISOString() };
    act(() => upsertSession(limited));
    expect(status().dataset.taskStatus).toBe("usage_limit");
    expect(status().className).toContain("text-orange");
    expect(row().textContent).toContain(usageLimitLabel(limited, now.getTime())!);
    act(() => upsertSession({ ...limited, turn_error: undefined, turn_error_kind: undefined, turn_error_resets_at: undefined, state: "working", status: "working" }));
    expect(status().className).toContain("text-ink-2");
  });

  it("says Starting, not Not started, on a task whose Agent is not up yet", async () => {
    todo.agents = [];
    await act(async () => {
      await refreshTodos();
    });
    act(() => entry("running").click());
    expect(host.querySelector("[data-task-status]")!.textContent).toBe(t("assistant-tasks.status_starting"));
  });

  it("keeps two entries in order, collapses by default and replaces conversation cards with the dispatch's hand-off card", () => {
    expect(entries().map((button) => button.getAttribute("data-tray-entry"))).toEqual(["needs", "running"]);
    expect(entries().map((button) => button.textContent)).toEqual([label.needs, `${label.running}1`]);
    expect(host.querySelector("section")).toBeNull();
    // the plan's one Agent; the task line of a planned task adds none; the task's conclusion is its own card (#5569)
    expect(host.querySelectorAll("[data-handoff-cards]")).toHaveLength(1);
    expect(host.querySelectorAll("[data-handoff-card]")).toHaveLength(1);
    expect(host.querySelectorAll("[data-receipt]")).toHaveLength(1);
    expect(host.textContent).toContain("Demo result");
    act(() => entry("running").click());
    expect(entry("running").getAttribute("aria-expanded")).toBe("true");
    expect(host.querySelectorAll("[data-task-slot]")).toHaveLength(1);
    expect(host.querySelector('[role="progressbar"]')?.getAttribute("aria-valuenow")).toBe("2");
    expect(host.textContent).toContain("PR #42");
    // the task's agent is in its row, not a row of its own
    expect(host.querySelector("[data-running-agent]")).toBeNull();
    expect(trays()).not.toContain("Demo agent");
  });

  it("draws the dispatched Agent as a hand-off card that opens it in the side panel, or its page set to Full page, not the In progress tray (#3700, #5096, #5424)", () => {
    const card = host.querySelector<HTMLButtonElement>('[data-handoff-card="demo/worker"]')!;
    expect(card.tagName).toBe("BUTTON");
    // the Agent's own title, New (the plan started it), and its state
    expect(card.textContent).toContain("Demo agent");
    expect(card.querySelector('[data-handoff-wake="created"]')?.textContent).toBe(t("assistant-tasks.handoff_wake_created"));
    expect(card.querySelector('[data-handoff-state="working"]')).not.toBeNull();
    act(() => card.click());
    expect(getPanel()).toEqual({ kind: "agent", mid: "demo", sid: "worker" });
    expect(opened()).toBe("/");
    act(() => closePanel());
    setAgentOpenMode("page");
    act(() => card.click());
    expect(opened()).toBe("/s/demo/worker");
    expect(getPanel()).toBeNull();
    expect(host.querySelector("section")).toBeNull();
    expect(entry("running").getAttribute("aria-expanded")).toBe("false");
  });

  it("draws a card for each Agent of several dispatched tasks, and the one that did not start says why", async () => {
    moreTodos = [{ id: "second-task", title: "Second task", origin: conversation, created_at: at, updated_at: at, agents: [{ machine_id: "demo", session_id: "second", linked_at: at }, { machine_id: "demo", session_id: "third", linked_at: at }] }];
    moreSessions = [
      { machine_id: "demo", session_id: "second", title: "Second agent", cwd: "/demo", state: "needs_you", status: "blocked", updated_at: at } as Session,
      { machine_id: "demo", session_id: "third", title: "", cwd: "/demo", state: "idle", status: "idle", updated_at: at } as Session,
    ];
    await act(async () => {
      await refresh();
      await refreshTodos();
      for (const x of moreSessions) upsertSession(x);
    });
    const many: TaskPlan = { id: "tray-plan-many", conversation, state: "started", created_at: at, tasks: [
      { title: "First task", prompt: "a", cwd: "/demo", machine: "demo", todo_id: "tray-task" },
      { title: "Second task", prompt: "b", cwd: "/demo", machine: "demo", todo_id: "second-task" },
      { title: "Failed task", prompt: "c", cwd: "/demo", machine: "demo", error: "machine offline" },
    ] };
    getFeedState().plans[many.id] = many;
    try {
      act(() => root.render(<StrictMode><AssistantActionBar conversation={conversation} /><FeedLineView item={{ id: "many-event", conversation, at, kind: "plan", ref: many.id }} simplified /></StrictMode>));
      // one card per Agent in plan order, no heading over them (cindy stacks cards)
      const group = host.querySelector("[data-handoff-cards]")!;
      expect(group.getAttribute("data-handoff-cards")).toBe("4");
      const cards = [...group.querySelectorAll<HTMLElement>("[data-handoff-card]")];
      expect(cards.map((el) => [el.tagName, el.getAttribute("data-handoff-card")])).toEqual([["BUTTON", "demo/worker"], ["BUTTON", "demo/second"], ["BUTTON", "demo/third"], ["DIV", "tray-plan-many/2"]]);
      // an Agent that needs you says so on its card; one without a title of its own takes its task's
      expect(cards[1]!.querySelector('[data-handoff-state="needs_you"]')).not.toBeNull();
      expect(cards[1]!.textContent).toContain("Second agent");
      expect(cards[2]!.textContent).toContain("Second task");
      expect(cards[3]!.textContent).toContain("Failed task");
      expect(cards[3]!.textContent).toContain(t("assistant-tasks.start_failed", { error: "machine offline" }));
      act(() => cards[1]!.click());
      expect(getPanel()).toEqual({ kind: "agent", mid: "demo", sid: "second" });
      act(() => cards[0]!.click());
      expect(getPanel()).toEqual({ kind: "agent", mid: "demo", sid: "worker" });
      expect(host.querySelector("section")).toBeNull();
    } finally {
      delete getFeedState().plans[many.id];
    }
  });

  it("counts an unexpired parked task in In progress even after days without activity, as the To-dos page does", async () => {
    const stale = "2026-11-05T08:00:00Z";
    moreSessions = [{ machine_id: "demo", session_id: "parked-worker", title: "Parked agent", cwd: "/demo", state: "idle", status: "idle", updated_at: stale } as Session];
    moreTodos = [{
      ...todo, id: "tray-parked", pr_url: undefined, plan_id: undefined, created_at: stale, updated_at: stale,
      agents: [{ machine_id: "demo", session_id: "parked-worker", linked_at: stale }],
      supervision: { supervisor: conversation, state: "parked", updated_at: stale, reviews: [], parked: { reason: "Waiting on another team", until: "2026-11-14T09:00:00Z", waiting_on: "someone_else", count: 1 } },
    }];
    await act(async () => { await refresh(); await refreshTodos(); });
    // Both tasks remain unfinished; explicit parking is not the inactivity fold.
    expect(entry("running").textContent).toBe(`${label.running}2`);
    act(() => entry("running").click());
    const slots = host.querySelectorAll("[data-task-slot]");
    expect(slots).toHaveLength(2);
    // Its parked line and the shared open count survive the inactivity window.
    expect(host.textContent).toContain("Waiting on another team");
  });

  it("updates the same row while it runs, opens the task, and drops it once it is accepted", () => {
    act(() => entry("running").click());
    const row = host.querySelector("[data-task-slot]");
    act(() => putTodo({ ...todo, supervision: { ...todo.supervision!, progress: { kind: "blocked", step: 3, steps: 5, text: "Waiting for a decision" } } }));
    expect(host.querySelector("[data-task-slot]")).toBe(row);
    expect(row?.textContent).toContain("Waiting for a decision");
    // the hub's paraphrase in the user's language replaces the agent's words in the row (#3181)
    act(() => putTodo({ ...todo, supervision: { ...todo.supervision!, progress: { kind: "blocked", step: 3, steps: 5, text: "Waiting for a decision", line: "Needs a call on the rollout" } } }));
    expect(row?.textContent).toContain("Needs a call on the rollout");
    expect(row?.textContent).not.toContain("Waiting for a decision");
    expect(host.querySelector('[role="progressbar"] .bg-orange')).not.toBeNull();
    act(() => (host.querySelector("[data-task-slot]") as HTMLButtonElement).click());
    // the task's side panel (#5424); set to Full page, its Agent's page (#5096)
    expect(getPanel()).toEqual({ kind: "task", todo: todo.id });
    act(() => closePanel());
    setAgentOpenMode("page");
    act(() => (host.querySelector("[data-task-slot]") as HTMLButtonElement).click());
    expect(opened()).toBe("/s/demo/worker");
    act(() => putTodo({ ...todo, done_at: at, supervision: { ...todo.supervision!, state: "accepted" } }));
    // finished: in neither entry (the board has it), no fold for it either
    expect(entry("running").textContent).toBe(label.running);
    expect(entry("needs").textContent).toBe(label.needs);
    expect(host.querySelector("[data-task-slot]")).toBeNull();
    expect(host.textContent).toContain(t("assistant-tasks.tray_empty_progress"));
  });

  it("reuses round-bound owner acceptance and deduplicates its supervision decision", async () => {
    const firstRound = { outcome: "decide" as const, verdict: "Choose the rollout", recommend: "Use the demo rollout", asked_at: at };
    todo = { ...todo, ask: firstRound, supervision: { ...todo.supervision!, state: "needs_user" } };
    await act(async () => putTodo(todo));
    await act(async () => { entry("needs").click(); await import("./TaskDecision"); });
    await openRows();
    // A lazy card can commit after its module import resolves.
    await expect.poll(() => host.querySelectorAll('[data-brief-row="accept"]')).toHaveLength(1);
    expect(host.textContent).not.toContain(t("assistant-tasks.review_needs_user"));
    const first = [...host.querySelectorAll("button")].find((button) => button.textContent?.trim() === t("assistant-brief.take"))!;
    const nextRound = "2026-11-10T08:20:00Z";
    await act(async () => {
      first.click();
      todo = { ...todo, ask: { ...firstRound, asked_at: nextRound } };
      putTodo(todo);
    });
    await openRows(); // the next round asks folded again
    await expect.poll(() => {
      const button = [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === t("assistant-brief.take"));
      return button !== undefined && button !== first;
    }).toBe(true);
    const second = [...host.querySelectorAll("button")].find((button) => button.textContent?.trim() === t("assistant-brief.take"))!;
    expect(second).not.toBe(first);
    await act(async () => second.click());
    const rounds = vi.mocked(fetch).mock.calls.filter(([url]) => String(url).endsWith("/accept")).map(([, init]) => JSON.parse(String(init?.body)).asked_at);
    expect(rounds).toEqual([at, nextRound]);
    expect(writes.filter((path) => path.includes("message"))).toEqual([]);
  });

  it("removes a directly finished decision from Needs you without sending or accepting anything", async () => {
    const ask = { outcome: "decide" as const, verdict: "Choose the demo rollout", recommend: "Use the demo rollout", asked_at: at };
    todo = { ...todo, ask, supervision: { ...todo.supervision!, state: "needs_user" } };
    await act(async () => putTodo(todo));
    await act(async () => { entry("needs").click(); await import("./TaskDecision"); });
    await openRows();
    expect(host.textContent!.split(todo.title).length - 1, "an opened row names its task once").toBe(1);
    const row = host.querySelector('[data-brief-row="accept"]')!;
    expect([...row.querySelectorAll("button")].map((b) => b.textContent?.trim())).toEqual([t("assistant-brief.open"), t("assistant-brief.finish"), t("assistant-tasks.later"), t("assistant-tasks.dismiss_draft"), t("assistant-brief.take")]);
    const read = vi.mocked(fetch).getMockImplementation()!;
    vi.mocked(fetch).mockImplementation(async (url, init) => {
      if (init?.method === "PATCH") {
        writes.push(String(url));
        todo = { ...todo, ask: undefined, done_at: at };
        return new Response(JSON.stringify({ todo }), { headers: { "Content-Type": "application/json" } });
      }
      return read(url, init);
    });
    await act(async () => [...row.querySelectorAll("button")][1].click());
    expect(host.querySelector('[data-brief-row="accept"]')).toBeNull();
    expect(host.textContent).toContain(t("assistant-tasks.tray_empty_pending"));
    expect(writes).toHaveLength(1);
    expect(writes[0]).toMatch(/\/todos\/tray-task$/);
    const request = vi.mocked(fetch).mock.calls.find(([, init]) => init?.method === "PATCH")!;
    expect(JSON.parse(String(request[1]?.body))).toEqual({ done: true });
    expect(writes.filter((path) => /accept|message|resume/.test(path))).toEqual([]);
  });

  it("puts a task waiting on the user and a draft in Needs you only, without sending anything", async () => {
    const draft: FeedItem = { id: "tray-draft", conversation, at, kind: "message", ref: "draft-ref", body: { draft: "Demo reply", text: "Demo message" } };
    getFeedState().feeds[conversation] = [...feed, draft];
    act(() => putTodo({ ...todo, supervision: { ...todo.supervision!, state: "needs_user" } }));
    expect(entries().map((button) => button.textContent)).toEqual([`${label.needs}2`, label.running]);
    await act(async () => {
      entry("needs").click();
      await import("./MessageDraft");
      await import("./TaskDecision");
    });
    expect(host.querySelector('[data-testid="message-card"]')).toBeNull(); // folded until opened
    await openRows();
    await expect.poll(() => host.querySelector('[data-testid="message-card"]')).not.toBeNull();
    expect(host.textContent).toContain(t("assistant-tasks.review_needs_user"));
    const draftCard = host.querySelector('[data-testid="message-card"]')!;
    const actions = [...draftCard.querySelectorAll("button")];
    expect(actions.map((button) => button.textContent)).toEqual([t("assistant-tasks.send"), t("assistant-tasks.later"), t("assistant-tasks.dismiss_draft")]);
    act(() => actions[1].click());
    expect(host.querySelector('[data-testid="message-card"]')).toBeNull();
    expect(entries().map((button) => button.textContent)).toEqual([`${label.needs}1`, label.running]);
    act(() => entry("running").click());
    expect(host.querySelector("[data-task-slot]")).toBeNull();
    expect(writes).toEqual([]);
  });

  it("moves a task whose agent asks to Needs you, with the card under its name, and back once answered", async () => {
    moreSessions = [{ machine_id: "demo", session_id: "worker", title: "Demo agent", cwd: "/demo", state: "needs_you", status: "blocked", updated_at: at, pending_interactions: 1 } as Session];
    moreInteractions = [{ id: "ix-1", machine_id: "demo", session_id: "worker", kind: "question", created_at: at, questions: [{ question: "Which port?", options: [{ label: "8080" }, { label: "9090" }] }] } as Interaction];
    await act(async () => {
      await refresh();
      upsertSession(moreSessions[0]);
    });
    expect(entries().map((button) => button.textContent)).toEqual([`${label.needs}1`, label.running]);
    act(() => entry("needs").click());
    const row = host.querySelector('[data-needs-row="task:tray-task"]')!;
    expect(row.textContent).toContain("Demo task");
    expect(row.textContent).toContain("Which port?");
    // a line until opened (#5569): the question under the title, no card, no answer buttons
    const head = row as HTMLButtonElement;
    expect(head.tagName).toBe("BUTTON");
    expect(head.getAttribute("aria-expanded")).toBe("false");
    expect(host.querySelector("[data-card]")).toBeNull();
    // it opens where it is (a real button: Enter and Space do the same as the click); its separate chevron folds it again
    act(() => head.click());
    const opened = host.querySelector('[data-needs-row="task:tray-task"]')!;
    expect(opened.hasAttribute("data-needs-open")).toBe(true);
    expect(opened.querySelector("button[aria-expanded=true]")).not.toBeNull();
    expect(opened.textContent).toContain("8080");
    // the additional Open entry still leads to the task (#6255); the title opens the asking Agent
    expect(opened.querySelectorAll("[data-needs-open-entry]")).toHaveLength(1);
    expect(opened.querySelector("[data-needs-open-entry]")!.getAttribute("title")).toContain("Demo task");
    act(() => opened.querySelector<HTMLButtonElement>("button[aria-expanded=true]")!.click());
    expect(host.querySelector('[data-needs-row="task:tray-task"]')!.tagName).toBe("BUTTON");
    // every row is folded again when the tray is closed and opened
    act(() => host.querySelector<HTMLButtonElement>('button[data-needs-row="task:tray-task"]')!.click());
    act(() => entry("needs").click());
    act(() => entry("needs").click());
    expect(host.querySelector('[data-needs-row="task:tray-task"]')!.tagName).toBe("BUTTON");
    // the agent is not a row of its own in either tray (the chat's hand-off card names it)
    expect(trays()).not.toContain("Demo agent");
    moreInteractions = [];
    await act(async () => {
      await refresh();
      upsertSession({ machine_id: "demo", session_id: "worker", title: "Demo agent", cwd: "/demo", state: "working", status: "working", updated_at: "2026-11-10T08:05:00Z" });
    });
    expect(entries().map((button) => button.textContent)).toEqual([label.needs, `${label.running}1`]);
  });

  it("counts and lists an Agent's question once when two to-dos list that Agent, under the one the hub ties it to", async () => {
    moreTodos = [{ id: "also-lists", title: "Also lists the agent", origin: conversation, created_at: at, updated_at: at, agents: [{ machine_id: "demo", session_id: "worker", linked_at: at }] }];
    moreSessions = [{ machine_id: "demo", session_id: "worker", title: "Demo agent", cwd: "/demo", state: "needs_you", status: "blocked", updated_at: at, pending_interactions: 1, todo: { id: "tray-task", title: "Demo task" } } as Session];
    moreInteractions = [{ id: "ix-1", machine_id: "demo", session_id: "worker", kind: "question", created_at: at, questions: [{ question: "Which port?", options: [{ label: "8080" }, { label: "9090" }] }] } as Interaction];
    await act(async () => {
      await refresh();
      await refreshTodos();
      upsertSession(moreSessions[0]);
    });
    expect(entries().map((button) => button.textContent)).toEqual([`${label.needs}1`, `${label.running}1`]);
    act(() => entry("needs").click());
    const rows = [...host.querySelectorAll("[data-needs-row]")];
    expect(rows.map((row) => row.getAttribute("data-needs-row"))).toEqual(["task:tray-task"]);
    expect(trays().split("Which port?")).toHaveLength(2);
    act(() => entry("running").click());
    expect(trays()).toContain("Also lists the agent");
  });

  it("lists Agents that belong to no task in the main conversation only, by what they need", async () => {
    mainChat = conversation;
    moreSessions = [
      { machine_id: "demo", session_id: "by-hand-asks", title: "Hand asks", cwd: "/demo", state: "needs_you", status: "blocked", updated_at: at, pending_interactions: 1 } as Session,
      { machine_id: "demo", session_id: "by-hand-works", title: "Hand works", cwd: "/demo", state: "working", status: "working", updated_at: at } as Session,
    ];
    moreInteractions = [{ id: "ix-hand", machine_id: "demo", session_id: "by-hand-asks", kind: "question", created_at: at, questions: [{ question: "Keep the old file?", options: [{ label: "Yes" }, { label: "No" }] }] } as Interaction];
    await act(async () => {
      await refresh();
      for (const s of moreSessions) upsertSession(s);
    });
    expect(entries().map((button) => button.textContent)).toEqual([`${label.needs}1`, `${label.running}2`]);
    act(() => entry("needs").click());
    expect(host.querySelector('[data-needs-row="agent:demo/by-hand-asks"]')?.textContent).toContain("Keep the old file?");
    expect(host.textContent).not.toContain("Hand works");
    act(() => entry("running").click());
    expect(host.querySelector('[data-running-agent="demo/by-hand-works"]')?.textContent).toContain("Hand works");
    expect(host.querySelector('[data-running-agent="demo/by-hand-asks"]')).toBeNull();
    // the task's own agent is still not listed by itself
    expect(host.querySelector('[data-running-agent="demo/worker"]')).toBeNull();
    act(() => (host.querySelector('[data-running-agent="demo/by-hand-works"]') as HTMLButtonElement).click());
    expect(getPanel()).toEqual({ kind: "agent", mid: "demo", sid: "by-hand-works" });
  });

  it.each(["task", "agent"] as const)("opens the asking Agent from an expanded %s title without sending an answer", async (kind) => {
    const sid = kind === "task" ? "worker" : "by-hand-asks";
    const title = kind === "task" ? todo.title : "Hand asks";
    mainChat = conversation;
    moreSessions = [{ machine_id: "demo", session_id: sid, title, cwd: "/demo", state: "needs_you", status: "blocked", updated_at: at } as Session];
    moreInteractions = [{ id: "ix-title", machine_id: "demo", session_id: sid, kind: "question", questions: [{ question: "Which port?", options: null }] } as Interaction];
    await act(async () => {
      await refresh();
      upsertSession(moreSessions[0]);
    });
    act(() => entry("needs").click());
    await openRows();
    const row = host.querySelector<HTMLElement>(`[data-needs-row="${kind === "task" ? "task:tray-task" : "agent:demo/by-hand-asks"}"]`)!;
    const titleButton = [...row.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === title)!;
    act(() => titleButton.click());
    expect(getPanel()).toEqual({ kind: "agent", mid: "demo", sid });
    expect(row.hasAttribute("data-needs-open")).toBe(true);
    closePanel();
    setAgentOpenMode("page");
    act(() => titleButton.click());
    expect(opened()).toBe(`/s/demo/${sid}`);
    expect(writes).toEqual([]);
  });

  it("keeps an expanded task question's free-text draft editable without sending it", async () => {
    moreSessions = [{ machine_id: "demo", session_id: "worker", title: "Demo agent", cwd: "/demo", state: "needs_you", status: "blocked", updated_at: at } as Session];
    moreInteractions = [{ id: "ix-draft", machine_id: "demo", session_id: "worker", kind: "question", questions: [{ question: "Which port?", options: null }] } as Interaction];
    await act(async () => {
      await refresh();
      upsertSession(moreSessions[0]);
    });
    act(() => entry("needs").click());
    await openRows();
    const input = host.querySelector<HTMLInputElement>('[data-card="ix-draft"] input')!;
    expect(input.matches(":disabled")).toBe(false);
    act(() => {
      input.focus();
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "Draft only; do not send");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(input.value).toBe("Draft only; do not send");
    expect(document.activeElement).toBe(input);
    expect(writes).toEqual([]);
  });

  it("draws the entries as the dock's old All / task capsules, with the open tray selected", () => {
    const dockHost = document.createElement("div");
    document.body.append(dockHost);
    const dockRoot = createRoot(dockHost);
    act(() => dockRoot.render(<TaskDock conversation={conversation} />));
    const all = dockHost.querySelector<HTMLButtonElement>('[data-testid="task-dock-board"]')!;
    const capsule = DOCK_CAPSULE.split(" ");
    for (const button of [all, ...entries()]) for (const name of capsule) expect(button.classList).toContain(name);
    act(() => dockRoot.unmount());
    dockHost.remove();
    // no fixed-width grid or tall rounded rectangle left over from #2920
    expect(host.querySelector("[data-assistant-actionbar] .grid")).toBeNull();
    for (const button of entries()) expect(button.className).not.toMatch(/rounded-control|py-2\b|flex-1/);
    // the open tray is the selected capsule; aria-expanded / aria-controls still say which one
    expect(entries().map((button) => button.getAttribute("aria-expanded"))).toEqual(["false", "false"]);
    act(() => entry("running").click());
    expect(entry("running").getAttribute("aria-expanded")).toBe("true");
    expect(entry("running").classList).toContain("bg-ink");
    expect(document.getElementById(entry("running").getAttribute("aria-controls")!)?.tagName).toBe("SECTION");
    expect(entry("needs").classList).not.toContain("bg-ink");
    act(() => entry("running").click());
    expect(entry("running").getAttribute("aria-expanded")).toBe("false");
    expect(host.querySelector("section")).toBeNull();
  });

  it("keeps an empty Needs you quiet (grey) but still opens it", () => {
    const needs = entry("needs");
    expect(needs.disabled).toBe(false);
    expect(needs.classList).toContain("text-ink-3");
    expect(needs.querySelector("[aria-hidden]")?.classList).toContain("bg-ink-3");
    expect(entry("running").querySelector("[aria-hidden]")?.classList).toContain("bg-accent");
    act(() => needs.click());
    expect(host.textContent).toContain(t("assistant-tasks.tray_empty_pending"));
  });

  it("draws an empty In progress quiet (grey) as well, like an empty Needs you, and still opens it", () => {
    const emptyHost = document.createElement("div");
    document.body.append(emptyHost);
    const emptyRoot = createRoot(emptyHost);
    act(() => emptyRoot.render(<AssistantActionBar conversation="nothing-here" />));
    for (const key of ["needs", "running"]) {
      const button = emptyHost.querySelector<HTMLButtonElement>(`[data-tray-entry="${key}"]`)!;
      expect(button.classList).toContain("text-ink-3");
      expect(button.classList).not.toContain("text-ink");
      expect(button.querySelector("[aria-hidden]")?.classList).toContain("bg-ink-3");
    }
    act(() => emptyHost.querySelector<HTMLButtonElement>('[data-tray-entry="running"]')!.click());
    expect(emptyHost.textContent).toContain(t("assistant-tasks.tray_empty_progress"));
    act(() => emptyRoot.unmount());
    emptyHost.remove();
    // one with work going on is drawn as before
    expect(entry("running").classList).toContain("text-ink");
    expect(entry("running").querySelector("[aria-hidden]")?.classList).toContain("bg-accent");
  });

  it("closes an open tray on Esc, from its entry or from inside it, with the focus back on the entry and the chat's Esc untouched", () => {
    const windowEsc = vi.fn();
    window.addEventListener("keydown", windowEsc);
    const esc = (el: Element) => act(() => void el.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true })));
    try {
      entry("needs").focus();
      act(() => entry("needs").click());
      expect(host.querySelector("section")).not.toBeNull();
      esc(entry("needs"));
      expect(host.querySelector("section")).toBeNull();
      expect(document.activeElement).toBe(entry("needs"));

      act(() => entry("running").click());
      const inside = host.querySelector<HTMLButtonElement>(`section button[aria-label="${t("ui-atoms.close")}"]`)!;
      inside.focus();
      esc(inside);
      expect(host.querySelector("section")).toBeNull();
      expect(document.activeElement).toBe(entry("running"));
      expect(windowEsc).not.toHaveBeenCalled();

      // with no tray open, Esc is not the bar's
      esc(entry("running"));
      expect(windowEsc).toHaveBeenCalledTimes(1);
    } finally {
      window.removeEventListener("keydown", windowEsc);
    }
  });

  it("leaves Esc in a tray card to the card: the tray stays open and an answer being typed is kept", async () => {
    moreSessions = [{ machine_id: "demo", session_id: "worker", title: "Demo agent", cwd: "/demo", state: "needs_you", status: "blocked", updated_at: at, pending_interactions: 1 } as Session];
    moreInteractions = [{ id: "ix-1", machine_id: "demo", session_id: "worker", kind: "question", created_at: at, questions: [{ question: "Which port?", options: [{ label: "8080" }, { label: "9090" }] }] } as Interaction];
    await act(async () => {
      await refresh();
      await refreshTodos();
      upsertSession(moreSessions[0]);
    });
    const windowEsc = vi.fn();
    window.addEventListener("keydown", windowEsc);
    const esc = (el: Element) => act(() => void el.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true })));
    const field = () => host.querySelector<HTMLInputElement>(`section input[aria-label="${t("chat-interactions.custom_answer")}"]`);
    try {
      act(() => entry("needs").click());
      await openRows();
      const typed = field()!;
      act(() => {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(typed, "8081");
        typed.dispatchEvent(new Event("input", { bubbles: true }));
      });
      act(() => typed.focus());
      esc(typed);
      expect(host.querySelector("section")).not.toBeNull();
      expect(field()?.value).toBe("8081");
      // the card's own Esc took it: on to neither the tray nor the window (the chat's interrupt)
      expect(windowEsc).not.toHaveBeenCalled();

      // from a waiting card's own buttons as well: the card's Esc, not the tray's
      const pick = host.querySelector<HTMLElement>("section [data-card-keys] button")!;
      act(() => pick.focus());
      esc(pick);
      expect(host.querySelector("section")).not.toBeNull();
      expect(field()?.value).toBe("8081");

      // the tray's own controls still close it
      const close = host.querySelector<HTMLButtonElement>(`section button[aria-label="${t("ui-atoms.close")}"]`)!;
      close.focus();
      esc(close);
      expect(host.querySelector("section")).toBeNull();
      expect(document.activeElement).toBe(entry("needs"));
      expect(windowEsc).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener("keydown", windowEsc);
    }
  });

  it("lists every open task that does not wait on the user in In progress, and the one that does in Needs you", async () => {
    const mk = (id: string, title: string, sid: string | undefined, extra: Partial<Todo> = {}): Todo => ({ id, title, origin: conversation, created_at: at, updated_at: at, agents: sid ? [{ machine_id: "demo", session_id: sid, linked_at: at }] : [], ...extra });
    moreTodos = [
      mk("open-stopped", "Stopped between rounds", "idle-worker"),
      mk("open-unread", "Finished, not seen", "unread-worker"),
      mk("open-asks", "Needs a decision", "asking-worker"),
      mk("closed-old", "Closed long ago", "idle-worker", { done_at: at }),
      mk("closed-elsewhere", "Another chat's task", "idle-worker", { origin: "another-chat" }),
    ];
    await act(async () => {
      await refreshTodos();
      upsertSession({ machine_id: "demo", session_id: "idle-worker", title: "Idle", cwd: "/demo", state: "idle", status: "idle", updated_at: at });
      upsertSession({ machine_id: "demo", session_id: "unread-worker", title: "Unread", cwd: "/demo", state: "unread", status: "unread", updated_at: at });
      upsertSession({ machine_id: "demo", session_id: "asking-worker", title: "Asking", cwd: "/demo", state: "needs_you", status: "needs_you", updated_at: at });
    });
    expect(entries().map((button) => button.textContent)).toEqual([`${label.needs}1`, `${label.running}3`]);
    act(() => entry("running").click());
    const rows = [...host.querySelectorAll("[data-task-slot]")];
    expect(rows.map((row) => row.querySelector(".font-semibold")?.textContent)).toEqual(["Demo task", "Finished, not seen", "Stopped between rounds"]);
    expect(rows.map((row) => row.querySelector("[data-task-mark]")?.getAttribute("data-task-mark"))).toEqual(["working", "unread", "follow_up"]);
    expect(host.textContent).not.toContain("Another chat's task");
    expect(host.textContent).not.toContain("Closed long ago");
    expect(host.textContent).not.toContain("Needs a decision");
    act(() => entry("needs").click());
    expect(host.querySelector('[data-needs-row="task:open-asks"]')?.textContent).toContain("Needs a decision");
  });

  it("says why a stopped task is not done, from the hub's completion check (#3180)", async () => {
    const stoppedTodo: Todo = { id: "open-checked", title: "Waiting on CI", origin: conversation, created_at: at, updated_at: at,
      agents: [{ machine_id: "demo", session_id: "idle-worker", linked_at: at }], check: { verdict: "waiting", reason: "Waiting for CI to finish", at, work_since: at } };
    moreTodos = [stoppedTodo];
    await act(async () => {
      await refreshTodos();
      upsertSession({ machine_id: "demo", session_id: "idle-worker", title: "Idle", cwd: "/demo", state: "idle", status: "idle", updated_at: at, last_text: "The checks are queued" });
    });
    act(() => entry("running").click());
    const row = () => [...host.querySelectorAll("[data-task-slot]")].find((r) => r.querySelector(".font-semibold")?.textContent === "Waiting on CI");
    expect(row()?.textContent).toContain(t("assistant-tasks.progress_stopped_reason", { reason: "Waiting for CI to finish" }));
    expect(row()?.textContent).not.toContain("The checks are queued");
    // no reason: the line alone; done or unsure: the row as before
    act(() => putTodo({ ...stoppedTodo, check: { verdict: "not_done", at } }));
    expect(row()?.textContent).toContain(t("assistant-tasks.progress_stopped"));
    expect(row()?.textContent).not.toContain(" · ");
    act(() => putTodo({ ...stoppedTodo, check: { verdict: "unsure", reason: "Can't tell", at } }));
    expect(row()?.textContent).not.toContain(t("assistant-tasks.progress_stopped"));
    expect(row()?.textContent).toContain("The checks are queued");
    // working again: the agent's words, not the old check
    act(() => putTodo(stoppedTodo));
    await act(async () => upsertSession({ machine_id: "demo", session_id: "idle-worker", title: "Idle", cwd: "/demo", state: "working", status: "working", updated_at: at, last_text: "Rerunning the checks" }));
    expect(row()?.textContent).not.toContain(t("assistant-tasks.progress_stopped"));
  });

  it("counts a plan waiting for Start as one Needs you row, where Start is one click away", () => {
    const waiting: TaskPlan = { id: "tray-open-plan", conversation, state: "open", created_at: at, tasks: [{ title: "Diet card", prompt: "Build it", cwd: "/demo", machine: "demo" }, { title: "Fix the supervisor", prompt: "Fix it", cwd: "/demo", machine: "demo" }] };
    act(() => {
      getFeedState().plans[waiting.id] = waiting;
      putTodo({ ...todo });
    });
    try {
      expect(entries().map((button) => button.getAttribute("data-tray-entry"))).toEqual(["needs", "running"]);
      expect(entry("needs").textContent).toBe(`${label.needs}1`);
      expect(entry("needs").querySelector("[aria-hidden]")?.classList).toContain("bg-attention");
      // In progress counts what the To-dos page counts (#3331): the plan's tasks are no to-dos yet
      expect(entry("running").textContent).toBe(`${label.running}1`);
      act(() => entry("running").click());
      expect(host.querySelector('[data-task-slot="plan:tray-open-plan:0"]')).toBeNull();
      act(() => entry("needs").click());
      expect(entry("needs").getAttribute("aria-expanded")).toBe("true");
      expect(host.querySelector("section")?.textContent).toContain("Diet card");
      // folded: the plan's first task and how many more; Start is not on the screen until the row is opened
      expect(host.querySelector("section")?.textContent).toContain(t("assistant-tasks.needs_more", { title: "Diet card", count: 1 }));
      const plan = host.querySelector<HTMLButtonElement>("button[data-needs-row]")!;
      expect(plan.getAttribute("aria-expanded")).toBe("false");
      expect(host.querySelector("[data-plan-actions]")).toBeNull();
      act(() => plan.click());
      expect(host.querySelector("[data-needs-open]")).not.toBeNull();
      expect(host.querySelector("section")?.textContent).toContain("Fix the supervisor");
      expect([...host.querySelectorAll("section button")].some((button) => button.textContent?.includes(t("assistant-tasks.plan_start_n", { count: 2 })))).toBe(true);
      // once it is started or dismissed it leaves Needs you
      act(() => {
        getFeedState().plans[waiting.id] = { ...waiting, state: "dismissed" };
        putTodo({ ...todo, updated_at: "2026-11-10T08:01:00Z" });
      });
      expect(entry("needs").textContent).toBe(label.needs);
      expect(host.textContent).toContain(t("assistant-tasks.tray_empty_pending"));
    } finally {
      delete getFeedState().plans[waiting.id];
    }
  });
});
