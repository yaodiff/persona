// @vitest-environment jsdom
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { formatDate, t } from "../../i18n";
import { getFeedState, loadFeed, type FeedItem, type TaskPlan } from "../../lib/feed";
import { chooseManager, resetManagerChoice } from "../../lib/manager-choice";
import { setAgentOpenMode } from "../../lib/agent-open";
import { closePanel, getPanel } from "../../lib/panel";
import { loadPages } from "../../lib/lazy-page";
import { getState, putTodo, refresh, refreshTodos, reset, upsertSession } from "../../lib/store";
import { interactionID } from "../../lib/ids-wire";
import type { Todo } from "../../lib/todos";
import { stateName } from "../../lib/state-labels";
import { usageLimitLabel } from "../../lib/usage-limit";
import type { Interaction, Session } from "../../lib/types";
import { Toasts } from "../../ui/atoms";
import { AssistantActionBar, DOCK_CAPSULE, FeedLineView, TaskDock } from "./Tasks";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
beforeAll(() => loadPages(), 60_000);
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
    const body = path.includes("/push/ln-mode") ? { enabled: false } : path.includes("/feed") ? { items: feed, plans: { [plan.id]: plan }, task_cards: [{ id: `plan:${plan.id}:0`, plan_id: plan.id, index: 0, todo_id: todo.id, at, updated_at: at, state: "accepted" }] } : path.includes("/todos") ? { todos: [todo, ...moreTodos] } : path.includes("/sessions") ? { sessions: [{ machine_id: "demo", session_id: "worker", title: "Demo agent", state: "working", status: "working", updated_at: at }, ...moreSessions] } : path.includes("/interactions") ? { interactions: moreInteractions } : /\/assistant(\?|$)/.test(path) && mainChat ? { machine_id: "demo", main: mainChat, conversations: [] } : {};
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

const label = { needs: t("assistant-tasks.tray_needs_you"), running: t("assistant-tasks.tray_running"), review: t("assistant-tasks.tray_review"), parked: t("todos.section_parked"), finished: t("assistant-tasks.tray_finished") };
// where a click went as a page (set to Full page, #5096); the side panel, the default, says so itself (getPanel)
const opened = () => location.pathname + location.search;
const trays = () => host.querySelector("[data-assistant-actionbar]")!.textContent;

describe("Assistant Needs you / In progress trays", () => {
  it.each([false, true])("puts a quiet verification with Pass in its current supervisor's Needs you (main=%s)", async (main) => {
    const title = "Check the new layout";
    const verification: Todo = {
      id: "verify-task", title, quiet: true, origin: "previous-supervisor", created_at: at, updated_at: at,
      supervision: { state: "user_check", supervisor: conversation },
      ask: { verdict: "Please verify the result", asked_at: at },
      view: { phase: "needs_you", section: "needs_you", reason: "verify", rank: 0 },
    };
    moreTodos = [verification, { ...verification, id: "other-verify", supervision: { state: "user_check", supervisor: "another-supervisor" } }];
    mainChat = main ? conversation : undefined;
    await act(async () => refresh());
    expect(entry("needs")).not.toBeNull();
    await act(async () => entry("needs").click());
    await openRows("task:verify-task");
    const row = host.querySelector('[data-needs-row="task:verify-task"][data-needs-open]');
    expect(row).not.toBeNull();
    await expect.poll(() => row?.textContent).toContain("Please verify the result");
    const other = host.querySelector('[data-needs-row="task:other-verify"]');
    if (main) {
      expect(other).not.toBeNull();
      expect(other?.closest("[data-needs-supervisor]")).toBeNull();
    } else {
      expect(other).toBeNull();
    }
    const pass = row?.querySelector<HTMLButtonElement>(`button[title="${t("assistant-brief.accept_row", { title })}"]`);
    expect(pass).toBeDefined();
    await act(async () => pass!.click());
    expect(writes).toContain("/api/v1/todos/verify-task/accept");
  });

  it("shows and answers the step behind a direction's rolled-up Needs you row", async () => {
    const ask = {
      outcome: "decide" as const, asked_at: at, verdict: "Release approval needed", question: "Which release should go out?",
      recommend: "Preview", options: [{ label: "Preview" }, { label: "Stable" }],
    };
    const projectedAsk = { id: `todo:decision@${at}`, home: "todo" as const, kind: "decision" as const, round: at };
    const view = { phase: "needs_you" as const, section: "needs_you" as const, reason: "owner_decides" as const, rank: 0 };
    todo = {
      ...todo, title: "Publish the update", kind: "task", agents: [], supervision: undefined,
      view: { ...view, steps: { total: 1, done: 0 }, asks: [{ ...projectedAsk, id: `step:release-step/${projectedAsk.id}` }] },
    };
    const step: Todo = {
      id: "release-step", title: "Choose the release", kind: "task", origin: "worker-supervisor", part_of: todo.id,
      created_at: at, updated_at: at, agents: [{ machine_id: "demo", session_id: "worker", linked_at: at }],
      supervision: { state: "needs_user", supervisor: "worker-supervisor" }, ask, view: { ...view, asks: [projectedAsk] },
    };
    moreTodos = [step];
    mainChat = "main-chat";
    await act(async () => refresh());
    expect(entry("needs").textContent).toBe(`${label.needs}1`);
    await act(async () => { entry("needs").click(); await import("./TaskDecision"); });
    const selector = '[data-needs-row="task:tray-task"]';
    const collapsed = host.querySelector(selector);
    expect(collapsed?.textContent).toContain(step.title);
    expect(collapsed?.textContent).toContain(stateName("needs_you"));
    expect(collapsed?.textContent).not.toContain(t("lib-state-labels.not_started"));
    expect(host.querySelectorAll("[data-needs-row]")).toHaveLength(1);
    await openRows("task:tray-task");
    await expect.poll(() => host.querySelector(`${selector} [data-decision-question]`)?.textContent).toBe(ask.question);
    const row = host.querySelector(selector)!;
    await act(async () => row.querySelector<HTMLButtonElement>("button[data-needs-title]")!.click());
    expect(getPanel()).toEqual({ kind: "task", todo: step.id });

    const option = [...row.querySelectorAll("button")].find((button) => button.textContent === "Stable");
    expect(option).toBeDefined();
    await act(async () => option!.click());
    const answers = vi.mocked(fetch).mock.calls.filter(([url, init]) => String(url).endsWith("/accept") && init?.method === "POST");
    expect(answers.map(([url, init]) => [url, JSON.parse(String(init?.body))])).toEqual([
      [`/api/v1/todos/${step.id}/accept`, { asked_at: at, option: "Stable" }],
    ]);
  });

  it.each([false, true])("opens a quiet verification whose Pass round has not arrived (main=%s)", async (main) => {
    const verification: Todo = {
      id: "verify-without-pass", title: "Inspect the finished work", quiet: true, origin: "dispatch-chat", created_at: at, updated_at: at,
      supervision: { state: "user_check", supervisor: main ? "direction-lead" : conversation },
      view: { phase: "needs_you", section: "needs_you", reason: "verify", rank: 0 },
    };
    moreTodos = [verification];
    mainChat = main ? conversation : "main-chat";
    await act(async () => refresh());
    expect(entry("needs")).not.toBeNull();
    await act(async () => entry("needs").click());
    const row = host.querySelector<HTMLButtonElement>('button[data-needs-row="task:verify-without-pass"]');
    expect(row).not.toBeNull();
    expect(row?.textContent).toContain(t("assistant-tasks.review_user_check"));
    expect(row?.closest("[data-needs-supervisor]")).toBeNull();
    await act(async () => row!.click());
    expect(getPanel()).toEqual({ kind: "task", todo: verification.id });
    expect(writes.filter((path) => path.endsWith("/accept"))).toEqual([]);
  });

  it.each([
    { main: conversation, supervisor: conversation },
    { main: conversation, supervisor: "direction-lead" },
    { main: "main-chat", supervisor: conversation },
  ])("shows a quiet task review decision in Needs you ($main, $supervisor)", async ({ main, supervisor }) => {
    const release: Todo = {
      id: "release-review", title: "Release review", origin: "dispatch-chat", quiet: true, created_at: at, updated_at: at,
      supervision: { state: "needs_user", supervisor },
      ask: { outcome: "decide", asked_at: at, verdict: "Needs your decision", question: "How should the fix be released?",
        recommend: "Release and verify", options: [{ label: "Release and verify" }, { label: "Release only" }, { label: "Wait" }] },
      view: { phase: "needs_you", section: "needs_you", reason: "owner_decides", rank: 0 },
    };
    moreTodos = [release, { ...release, id: "dismissed-review", decision_review: { review_at: at, dismissal_id: "dismissed" } },
      { ...release, id: "held-review", ask: { ...release.ask!, for_briefing: true }, view: { ...release.view!, section: "can_wait", reason: "can_wait" } }];
    mainChat = main;
    await act(async () => refresh());
    expect(entry("needs")).not.toBeNull();
    await act(async () => { entry("needs").click(); await import("./TaskDecision"); });
    await openRows("task:release-review");
    const opened = () => host.querySelector('[data-needs-row="task:release-review"][data-needs-open]');
    await expect.poll(() => opened()?.querySelector("[data-decision-question]")?.textContent).toBe(release.ask?.question);
    const row = opened()!;
    expect(row.closest("[data-needs-supervisor]")?.getAttribute("data-needs-supervisor")).toBe(supervisor === conversation ? undefined : supervisor);
    expect(host.querySelector('[data-needs-row="task:dismissed-review"]')).toBeNull();
    expect(host.querySelector('[data-needs-row="task:held-review"]')).toBeNull();
    const option = [...row.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent === "Release only");
    expect(option).toBeDefined();
    await act(async () => option!.click());
    const sends = vi.mocked(fetch).mock.calls.filter(([url, init]) => String(url).endsWith("/todos/release-review/accept") && init?.method === "POST");
    expect(sends).toHaveLength(1);
    expect(JSON.parse(String(sends[0][1]?.body))).toEqual({ asked_at: at, option: "Release only" });
  });

  it("renders the projected PR and blockers in the waiting tray ahead of conflicting park evidence", async () => {
    moreTodos = [{
      id: "held", title: "Review the export", origin: conversation, created_at: at, updated_at: at,
      supervision: { supervisor: conversation, state: "parked", parked: {
        waiting_on: "date", reason: "Earlier plan", until: at, count: 1,
      } },
      wait: { kind: "pr_merge", waiting_on: "PR #77", pr_blockers: ["checks_failing"] },
    }];
    await act(async () => refreshTodos());
    await act(async () => entry("parked").click());
    const row = host.querySelector('[data-parked-task-row="held"]')!;
    expect(row.querySelector("[data-wait-target]")?.textContent).toContain("PR #77");
    await act(async () => row.querySelector<HTMLButtonElement>("[data-wait-toggle]")!.click());
    expect(row.textContent).toContain(t("assistant-tasks.pr_blocker_checks_failed"));
    expect(row.textContent).not.toContain("Earlier plan");
  });

  it("Parked grouping: shows explicit waits and multi-PR waits once, outside active work, including on All", async () => {
    const pr = "https://github.com/example/demo/pull/52";
    const checked_at = new Date(now.getTime() - 60_000).toISOString();
    const facts = { url: pr, state: "open" as const, draft: false, review: "required" as const, checks: "success" as const, conflicts: false, checked_at };
    const held: Todo = {
      id: "supplier-wait", title: "Example supplier reply", origin: conversation, created_at: at, updated_at: at,
      supervision: { supervisor: conversation, state: "parked", parked: { reason: "The supplier has been asked", until: "2026-11-09T09:00:00Z", waiting_on: "someone_else", count: 1 } },
    };
    const merging: Todo = { id: "merge-wait", title: "Example schedule review", origin: conversation, created_at: at, updated_at: at, pr_url: pr, pr_status: facts, other_prs: [{ ...facts, url: "https://github.com/example/demo/pull/51" }] };
    moreTodos = [held, merging];
    await act(async () => refreshTodos());
    expect(entry("parked")).not.toBeNull();
    expect(entries().map((button) => button.getAttribute("data-tray-entry"))).toEqual(["needs", "running", "parked"]);
    expect(entry("parked").textContent).toBe(`${label.parked}2`);
    expect(entry("running").textContent).toBe(`${label.running}1`);
    expect(entry("pr")).toBeNull();
    const reveal = vi.fn();
    entry("parked").scrollIntoView = reveal;
    act(() => entry("parked").click());
    expect(reveal).toHaveBeenCalledWith({ block: "nearest", inline: "nearest" });
    const rows = [...host.querySelectorAll<HTMLElement>("[data-parked-task-row]")];
    expect(rows.map((row) => row.dataset.parkedTaskRow).sort()).toEqual(["merge-wait", "supplier-wait"]);
    expect(rows.every((row) => row.dataset.countsOpen === "true")).toBe(true);
    expect(rows.some((row) => row.querySelector(".text-green"))).toBe(false);
    expect(host.querySelectorAll("[data-pr-waiting-row]")).toHaveLength(1);
    expect(host.querySelectorAll('[data-parked-task-row] [data-wait-collapsed="true"]')).toHaveLength(2);
    act(() => host.querySelector<HTMLButtonElement>("[data-pr-waiting-row] [data-wait-toggle]")!.click());
    expect([...host.querySelectorAll<HTMLAnchorElement>("[data-pr-waiting-row] a")].map((a) => a.href)).toEqual([pr, "https://github.com/example/demo/pull/51"]);
    act(() => entry("running").click());
    expect(host.querySelectorAll("[data-task-slot]")).toHaveLength(1);
    expect(host.querySelector("[data-parked-task-row]")).toBeNull();
    const all = [...host.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent === t("assistant-tasks.all"))!;
    await act(async () => all.click());
    const board = document.querySelector('[data-testid="task-board"]');
    expect(board).not.toBeNull();
    const section = [...board!.querySelectorAll("section")].find((row) => row.getAttribute("aria-label") === label.parked);
    expect(section).toBeDefined();
    expect(section!.textContent).toContain(held.title);
    expect(section!.textContent).toContain(merging.title);
    expect(section!.querySelectorAll('[data-wait-collapsed="true"]')).toHaveLength(2);
    expect(section!.querySelectorAll("button:not([data-wait-toggle])")).toHaveLength(4); // Each task's open and existing remove actions.
    expect(section!.querySelectorAll("[data-wait-toggle]")).toHaveLength(2);
    expect(section!.querySelector(".text-green")).toBeNull();
  });

  it("Parked grouping: keeps an emptied wait tray stable while live work moves back to In progress", () => {
    const held: Todo = {
      ...todo, agents: [{ machine_id: "demo", session_id: "idle-worker", linked_at: at }],
      supervision: { supervisor: conversation, state: "parked", parked: { reason: "Awaiting the supplier", until: at, waiting_on: "someone_else", count: 1 } },
    };
    act(() => {
      putTodo(held);
      upsertSession({ machine_id: "demo", session_id: "idle-worker", title: "Example worker", cwd: "/demo", status: "idle", state: "unread", updated_at: at });
    });
    expect(entry("parked")).not.toBeNull();
    act(() => entry("parked").click());
    const row = host.querySelector('[data-parked-task-row="tray-task"]');
    expect(row).not.toBeNull();
    expect(row!.querySelector(".text-green")).toBeNull();
    act(() => upsertSession({ machine_id: "demo", session_id: "idle-worker", title: "Example worker", cwd: "/demo", status: "working", state: "working", updated_at: at }));
    expect(host.querySelector("[data-parked-task-row]")).toBeNull();
    expect(host.textContent).toContain(t("assistant-tasks.tray_empty_parked"));
    expect(entry("parked").getAttribute("aria-expanded")).toBe("true");
    expect(entry("running").textContent).toBe(`${label.running}1`);
  });

  it("counts and shows a waiting PR in its own tray with its link and blockers", () => {
    const pr = "https://github.com/example/demo/pull/42";
    act(() => putTodo({ ...todo, agents: [], pr_url: pr, delivery: { state: "pr" }, pr_status: { url: pr, state: "open", draft: false, review: "changes_requested", checks: "pending", conflicts: true, checked_at: new Date(now.getTime() - 60_000).toISOString() } }));
    expect(entry("parked").textContent).toContain(label.parked);
    expect(entry("parked").textContent).toContain("1");
    expect(entry("running").textContent).not.toContain("1");
    act(() => entry("parked").click());
    const row = host.querySelector<HTMLElement>("[data-pr-waiting-row='tray-task']")!;
    expect(row.textContent).toContain("Demo task");
    expect(row.querySelector('[data-wait-collapsed="true"]')).not.toBeNull();
    act(() => row.querySelector<HTMLButtonElement>("[data-wait-toggle]")!.click());
    expect(row.querySelector("a")?.getAttribute("href")).toBe(pr);
    expect(row.textContent).toContain(t("assistant-tasks.pr_blocker_changes_requested"));
    expect(row.textContent).toContain(t("assistant-tasks.pr_blocker_checks_pending"));
    expect(row.textContent).toContain(t("assistant-tasks.pr_blocker_conflicts"));
    expect(row.querySelector("a")?.classList).toContain("break-all");
    act(() => row.querySelector<HTMLButtonElement>("[data-pr-task-link]")!.click());
    expect(getPanel()).toEqual({ kind: "task", todo: "tray-task" });
  });

  it("keeps a task whose last PR merged in the waiting tray while an earlier PR of it is open, each PR with its own blockers", () => {
    const merged = "https://github.com/example/demo/pull/43", open = "https://github.com/example/demo/pull/42";
    const checked = new Date(now.getTime() - 60_000).toISOString();
    act(() => putTodo({ ...todo, agents: [], pr_url: merged,
      pr_status: { url: merged, state: "merged", draft: false, review: "approved", checks: "success", conflicts: false, checked_at: checked },
      other_prs: [{ url: open, state: "open", draft: false, review: "required", checks: "success", conflicts: false, checked_at: checked }] }));
    expect(entry("parked").textContent).toContain("1");
    act(() => entry("parked").click());
    const row = host.querySelector<HTMLElement>("[data-pr-waiting-row='tray-task']")!;
    act(() => row.querySelector<HTMLButtonElement>("[data-wait-toggle]")!.click());
    expect([...row.querySelectorAll("a")].map((a) => a.getAttribute("href"))).toEqual([open]);
    expect(row.textContent).toContain(t("assistant-tasks.pr_blocker_review_required"));
  });

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

  it("presents a mixed usage-limit and foreground-working recheck as active, not Parked", () => {
    const until = new Date(now.getTime() + 4 * 3_600_000).toISOString();
    const limited: Session = { machine_id: "demo", session_id: "limited-worker", title: "Limited agent", cwd: "/demo", status: "idle", state: "idle", updated_at: at,
      turn_error: "You've hit your usage limit.", turn_error_kind: "usage_limit", turn_error_resets_at: until };
    act(() => {
      upsertSession(limited);
      putTodo({ ...todo,
        agents: [...todo.agents!, { machine_id: "demo", session_id: limited.session_id, linked_at: at }],
        supervision: { ...todo.supervision!, state: "parked", parked: { reason: "Check the active work", until, waiting_on: "date", count: 1 } },
      });
    });
    act(() => entry("running").click());
    const row = host.querySelector("[data-task-slot]")!;
    expect(row.querySelector("[data-task-status]")!.getAttribute("data-task-status")).toBe("usage_limit");
    expect(row.querySelector("[data-task-todo-state]")).toBeNull();
    expect(row.textContent).toContain(t("task-wait.recheck", { time: formatDate(until, { dateStyle: "medium", timeStyle: "short" }) }));
    // Once the foreground turn ends, the quiet usage-limited Agent alone does not keep the task active.
    act(() => upsertSession({ machine_id: "demo", session_id: "worker", title: "Demo agent", cwd: "/demo", status: "idle", state: "idle", updated_at: at }));
    expect(entry("running").textContent).toBe(label.running);
    expect(entry("parked").textContent).toBe(`${label.parked}1`);
  });

  it("names what a waiting task waits on in its status column, in the neutral color (#11090)", () => {
    act(() => entry("running").click());
    const status = () => host.querySelector("[data-task-slot]")!.querySelector<HTMLElement>("[data-task-status]")!;
    const worker: Session = { machine_id: "demo", session_id: "worker", title: "Demo agent", cwd: "/demo", status: "idle", state: "idle", updated_at: at };
    act(() => upsertSession({ ...worker, work: { phase: "blocked", on: "external", wait: "CI on main", line: "Wait for CI on main" } }));
    expect(status().dataset.taskStatus).toBe("waiting");
    expect(status().dataset.taskWait).toBe("external");
    expect(status().textContent).toBe(t("lib-state-labels.waits_external"));
    expect(status().className).toContain("text-ink-2");
    expect(status().className).not.toContain("text-attention");
    act(() => upsertSession({ ...worker, work: { phase: "blocked", on: "agent", wait: "the other Agent's merge", line: "Wait for the other Agent" } }));
    expect(status().textContent).toBe(t("lib-state-labels.waits_prerequisite"));
  });

  it("says Starting, not Not started, on a task whose Agent is not up yet", async () => {
    todo.agents = [];
    await act(async () => {
      await refreshTodos();
    });
    act(() => entry("running").click());
    expect(host.querySelector("[data-task-status]")!.textContent).toBe(t("assistant-tasks.status_starting"));
    expect(host.querySelector("[data-task-slot] .status-mark")!.getAttribute("data-state")).toBe("working");
  });

  /** The In progress row titled title, and the dock's pill titles. */
  const trayRow = (title: string) => [...host.querySelectorAll<HTMLElement>("[data-task-slot]")].find((row) => row.querySelector(".font-semibold")?.textContent === title);
  const dockTitles = () => {
    const dockHost = document.createElement("div");
    document.body.append(dockHost);
    const dockRoot = createRoot(dockHost);
    act(() => dockRoot.render(<TaskDock conversation={conversation} />));
    const titles = [...dockHost.querySelectorAll("button .truncate")].map((el) => el.textContent);
    act(() => dockRoot.unmount());
    dockHost.remove();
    return titles;
  };

  it("says Not started, with a still mark and no dock pill, on a to-do nobody dispatched (#7552)", async () => {
    // a to-do of this conversation (a leader's piece, a todo_create) with no Agent, no plan and no start: nothing is starting
    moreTodos = [{ id: "own-todo", title: "Decide the release", origin: conversation, created_at: at, updated_at: at, agents: [] }];
    await act(async () => {
      await refreshTodos();
    });
    act(() => entry("running").click());
    const row = trayRow("Decide the release")!;
    expect(row.querySelector("[data-task-status]")!.textContent).toBe(stateName("not_started"));
    expect(row.querySelector(".status-mark")!.getAttribute("data-state")).toBe("offline");
    // still a row of In progress, counted open as the To-dos page counts it (#3331)
    expect(row.getAttribute("data-counts-open")).toBe("true");
    expect(dockTitles()).not.toContain("Decide the release");
  });

  it("says Starting while a plan that took over a to-do starts its Agent, and moves it to Needs you once that start failed (#7552)", async () => {
    // a to-do a plan reuses keeps no plan_id (tasks_recover.go): the plan's task names it
    const reuse: TaskPlan = { id: "reuse-plan", conversation, state: "started", created_at: at, tasks: [{ title: "Reused", prompt: "Go on", cwd: "/demo", machine: "demo", todo_id: "reused-todo" }] };
    getFeedState().plans[reuse.id] = reuse;
    moreTodos = [{ id: "reused-todo", title: "Carry on the work", origin: conversation, created_at: at, updated_at: at, agents: [] }];
    await act(async () => {
      await refreshTodos();
    });
    act(() => entry("running").click());
    expect(trayRow("Carry on the work")!.querySelector("[data-task-status]")!.textContent).toBe(t("assistant-tasks.status_starting"));
    expect(trayRow("Carry on the work")!.querySelector(".status-mark")!.getAttribute("data-state")).toBe("working");
    expect(dockTitles()).toContain("Carry on the work");
    // the hub ends a failed start with done_at and start_error (#1077): the owner's to retry, in Needs you
    moreTodos = [{ ...moreTodos[0], done_at: at, start_error: "the computer is offline" }];
    await act(async () => {
      await refreshTodos();
    });
    expect(trayRow("Carry on the work")).toBeUndefined();
    act(() => entry("needs").click());
    expect(host.querySelector('[data-needs-row="task:reused-todo"]')?.textContent).toContain("Carry on the work");
    delete getFeedState().plans[reuse.id];
  });

  it("offers a failed start's other launcher first in Needs you, with its code's short line, where the hub takes it (#10365)", async () => {
    const reuse: TaskPlan = { id: "launcher-plan", conversation, state: "started", created_at: at, tasks: [{ title: "Reused", prompt: "Go on", cwd: "/demo", machine: "demo",
      todo_id: "launcher-todo", error: "terminal", error_code: "launcher_missing", error_args: { where: "terminal", launcher: "work" }, retry_with: ["claude"] }] };
    getFeedState().plans[reuse.id] = reuse;
    const features = getState().features;
    getState().features = [...features, "task_retry_launcher"];
    moreTodos = [{ id: "launcher-todo", title: "Carry on the work", origin: conversation, created_at: at, updated_at: at, agents: [], done_at: at,
      start_error: "Demo's terminal can't run the launcher's program; start agents where it can, or pick another launcher." }];
    try {
      await act(async () => {
        await refreshTodos();
      });
      // the failed start's row comes with the tray's first one (TaskDecision's chunk), not with the first screen
      await act(async () => { entry("needs").click(); await import("./TaskDecision"); });
      const row = host.querySelector<HTMLElement>("[data-failed-task=launcher-todo]")!;
      expect(row.textContent).toContain(t("lib-start-retry.start_code_launcher_terminal", { launcher: "work" }));
      const buttons = [...row.querySelectorAll("button")].map((b) => b.textContent);
      expect(buttons.slice(1)).toEqual([t("assistant-task-actions.use_launcher_instead", { launcher: "claude" }), t("assistant-tasks.retry"), t("assistant-tasks.remove")]);
      await act(async () => row.querySelector<HTMLButtonElement>("[data-retry-launcher=claude]")!.click());
      expect(writes).toContain("/api/v1/todos/launcher-todo/retry");
    } finally {
      getState().features = features;
      delete getFeedState().plans[reuse.id];
    }
  });

  it("says Not started on the owner's own to-do in the main conversation's In progress (#7552)", async () => {
    mainChat = conversation;
    moreTodos = [{ id: "owner-todo", title: "Book the dentist", created_at: at, updated_at: at, agents: [] }];
    await act(async () => {
      await refresh();
      await refreshTodos();
    });
    act(() => entry("running").click());
    const row = trayRow("Book the dentist")!;
    expect(row.querySelector("[data-task-status]")!.textContent).toBe(stateName("not_started"));
    expect(row.querySelector(".status-mark")!.getAttribute("data-state")).not.toBe("working");
  });

  it("keeps the two populated entries in order, collapses by default and replaces conversation cards with the dispatch's hand-off card", () => {
    expect(entries().map((button) => button.getAttribute("data-tray-entry"))).toEqual(["needs", "running"]);
    expect(entries().map((button) => button.textContent)).toEqual([label.needs, `${label.running}1`]);
    expect(host.querySelector("section")).toBeNull();
    // the plan's one Agent; the task line of a planned task adds none; its receipt and review are no cards (#8237)
    expect(host.querySelectorAll("[data-handoff-cards]")).toHaveLength(1);
    expect(host.querySelectorAll("[data-handoff-card]")).toHaveLength(1);
    expect(host.querySelectorAll("[data-receipt]")).toHaveLength(0);
    expect(host.textContent).not.toContain("Demo result");
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
      await act(async () => {
        await vi.dynamicImportSettled();
        await new Promise((r) => setTimeout(r, 0));
      });
      // 3+ cards default to a compact summary group (#17766) while surfacing up to 2 urgent attention cards (failed + needs_you)
      const group = host.querySelector("[data-handoff-cards]")!;
      expect(group.getAttribute("data-handoff-cards")).toBe("4");
      expect(group.getAttribute("data-handoff-group")).toBe("collapsed");
      const toggle = group.querySelector<HTMLButtonElement>("[data-handoff-group-toggle]")!;
      expect(toggle.getAttribute("aria-expanded")).toBe("false");
      const surfaced = [...group.querySelectorAll<HTMLElement>("[data-handoff-card]")];
      expect(surfaced.map((el) => el.getAttribute("data-handoff-card"))).toEqual(["tray-plan-many/2", "demo/second"]);
      // expanding reveals all 4 cards in original plan order with no duplicates
      act(() => toggle.click());
      expect(toggle.getAttribute("aria-expanded")).toBe("true");
      const cards = [...group.querySelectorAll<HTMLElement>("[data-handoff-card]")];
      expect(cards.map((el) => [el.tagName, el.getAttribute("data-handoff-card")])).toEqual([["BUTTON", "demo/worker"], ["BUTTON", "demo/second"], ["BUTTON", "demo/third"], ["DIV", "tray-plan-many/2"]]);
      // an Agent that needs you says so on its card; one without a title of its own takes its task's
      expect(cards[1]!.querySelector('[data-handoff-state="needs_you"]')).not.toBeNull();
      expect(cards[1]!.textContent).toContain("Second agent");
      expect(cards[2]!.textContent).toContain("Second task");
      expect(cards[3]!.textContent).toContain("Failed task");
      // in plain words (#8237): the raw "machine offline" is the task's details'
      expect(cards[3]!.textContent).toContain(t("assistant-tasks.start_failed", { error: t("assistant-tasks.start_cause_offline") }));
      expect(cards[3]!.textContent).not.toContain("machine offline");
      act(() => cards[1]!.click());
      expect(getPanel()).toEqual({ kind: "agent", mid: "demo", sid: "second" });
      act(() => cards[0]!.click());
      expect(getPanel()).toEqual({ kind: "agent", mid: "demo", sid: "worker" });
      expect(host.querySelector("section")).toBeNull();
    } finally {
      delete getFeedState().plans[many.id];
    }
  });

  it("counts an old external wait in Parked, not In progress, as the To-dos page does", async () => {
    const stale = "2026-11-05T08:00:00Z";
    moreSessions = [{ machine_id: "demo", session_id: "parked-worker", title: "Parked agent", cwd: "/demo", state: "idle", status: "idle", updated_at: stale } as Session];
    moreTodos = [{
      ...todo, id: "tray-parked", pr_url: undefined, plan_id: undefined, created_at: stale, updated_at: stale,
      agents: [{ machine_id: "demo", session_id: "parked-worker", linked_at: stale }],
      supervision: { supervisor: conversation, state: "parked", updated_at: stale, reviews: [], parked: { reason: "Waiting on another team", until: "2026-11-14T09:00:00Z", waiting_on: "someone_else", count: 1 } },
    }];
    await act(async () => { await refresh(); await refreshTodos(); });
    // Both tasks remain unfinished; explicit parking is not the inactivity fold.
    expect(entry("running").textContent).toBe(`${label.running}1`);
    expect(entry("parked").textContent).toBe(`${label.parked}1`);
    act(() => entry("running").click());
    const slots = host.querySelectorAll("[data-task-slot]");
    expect(slots).toHaveLength(1);
    expect(host.textContent).not.toContain("Waiting on another team");
    act(() => entry("parked").click());
    // Its parked line and the shared open count survive the inactivity window.
    expect(host.querySelector('[data-parked-task-row="tray-parked"] [data-wait-collapsed="true"]')).not.toBeNull();
    act(() => host.querySelector<HTMLButtonElement>('[data-parked-task-row="tray-parked"] [data-wait-toggle]')!.click());
    expect(host.textContent).toContain("Waiting on another team");
    expect(host.querySelector('[data-parked-task-row="tray-parked"] .text-green')).toBeNull();
  });

  it("shows the recorded wait contact in the parked tray", async () => {
    const evidence = { kind: "person_reply" as const, target: "Schedule owner", wake: "The owner replies in the thread",
      recorded_by: "recording-conversation",
      contact: { sender: "Example sender", channel: "Email", url: "https://example.test/messages/7",
        sent_at: "2026-11-10T08:00:00Z", request: "Please confirm the schedule", status: "sent" } };
    moreTodos = [{
      ...todo, id: "tray-wait-evidence", title: "Example schedule wait", pr_url: undefined, plan_id: undefined, agents: [],
      supervision: { supervisor: conversation, state: "parked", parked: {
        reason: "Waiting for the schedule owner", until: "2026-11-14T09:00:00Z", waiting_on: "someone_else", count: 1, evidence,
      }, reviews: [{ at: "2026-11-10T09:00:00Z", verdict: "parked", parks: 1, evidence }] },
    }];
    const older = moreTodos[0];
    const newerEvidence = { ...evidence, contact: { ...evidence.contact, sent_at: "2026-11-10T08:30:00Z" } };
    moreTodos = [{ ...older, id: "tray-wait-newer", title: "Newer example wait", updated_at: "2026-11-10T08:40:00Z",
      supervision: { ...older.supervision!, parked: { ...older.supervision!.parked!, evidence: newerEvidence },
        reviews: [{ at: "2026-11-10T08:40:00Z", verdict: "parked", parks: 1, evidence: newerEvidence }] } }, older];
    await act(async () => { await refreshTodos(); });
    await act(async () => entry("parked").click());
    expect([...host.querySelectorAll("[data-parked-task-row]")].map((row) => row.getAttribute("data-parked-task-row")))
      .toEqual(["tray-wait-evidence", "tray-wait-newer"]);
    const info = host.querySelector('[data-task-wait-info="tray-wait-evidence"]');
    expect(info?.getAttribute("data-wait-collapsed")).toBe("true");
    expect(info?.querySelector("[data-wait-target]")?.textContent).toContain("Schedule owner");
    expect(info?.querySelector("[data-wait-age]")?.textContent).not.toContain(t("task-wait.age_unknown"));
    expect(info?.querySelector("[data-wait-contact]")).toBeNull();
    await act(async () => info?.querySelector<HTMLButtonElement>("[data-wait-toggle]")!.click());
    expect(info?.getAttribute("data-wait-collapsed")).toBe("false");
    expect(info?.querySelector("[data-wait-contact]")?.textContent).toContain("Example sender");
    expect(info?.querySelector("[data-wait-contact]")?.getAttribute("href")).toBe(evidence.contact.url);
    expect(info?.textContent).not.toContain("recording-conversation");
  });

  it("says only the Agent's own state in the status column, with the to-do's state as its own chip (#6492)", async () => {
    moreTodos = [{
      ...todo, id: "tray-rework-stopped", title: "Needs rework, Agent gone", pr_url: undefined, plan_id: undefined,
      agents: [{ machine_id: "demo", session_id: "gone-worker", linked_at: at }],
      supervision: { supervisor: conversation, state: "rework", updated_at: at, reviews: [] },
    }, {
      ...todo, id: "tray-merge", title: "PR waiting to merge", plan_id: undefined, stage: "merge",
      agents: [{ machine_id: "demo", session_id: "quiet-worker", linked_at: at }], supervision: undefined,
    }];
    await act(async () => {
      await refreshTodos();
      upsertSession({ machine_id: "demo", session_id: "gone-worker", title: "Gone agent", cwd: "/demo", state: "offline", status: "offline", updated_at: at });
      upsertSession({ machine_id: "demo", session_id: "quiet-worker", title: "Quiet agent", cwd: "/demo", state: "idle", status: "idle", updated_at: at });
    });
    act(() => entry("running").click());
    const row = (title: string) => [...host.querySelectorAll("[data-task-slot]")].find((r) => r.querySelector(".font-semibold")?.textContent === title)!;
    const rework = row("Needs rework, Agent gone");
    // Explicit holds now have their own Parked tray; unfinished rework still has an Agent-only In progress row.
    expect(rework.querySelector("[data-task-status]")!.textContent).toBe(t("assistant-tasks.agent_stopped"));
    expect(rework.querySelector("[data-task-status]")!.getAttribute("data-task-status")).toBe("stopped");
    expect(rework.querySelector("[data-task-todo-state]")!.textContent).toBe(t("assistant-tasks.todo_chip", { state: t("assistant-tasks.review_rework", { round: 1 }) }));
    // one mark set for Agent states: the stopped Agent's hollow ring, as the sidebar draws it
    expect(rework.querySelector('[data-task-mark="stopped"] [data-state="offline"]')).not.toBeNull();
    const merge = row("PR waiting to merge");
    expect(merge.querySelector("[data-task-status]")!.textContent).toBe(t("assistant-tasks.agent_idle"));
    expect(merge.querySelector("[data-task-todo-state]")!.textContent).toBe(t("assistant-tasks.todo_chip", { state: t("assistant-tasks.todo_waiting_merge") }));
    // a to-do with nothing to add beyond its Agent has no chip
    expect(row("Demo task").querySelector("[data-task-todo-state]")).toBeNull();
    expect(row("Demo task").querySelector("[data-task-status]")!.textContent).toBe(t("lib-state-labels.working"));
  });

  it.each(["pending_review", "rework"] as const)("keeps a stale %s chip off a working Agent's rendered row (#6492)", (state) => {
    act(() => putTodo({ ...todo, supervision: { supervisor: conversation, state, reviews: [], round: 2 } }));
    expect(entry("review")).toBeNull();
    act(() => entry("running").click());
    const row = host.querySelector("[data-task-slot]")!;
    expect(row.querySelector("[data-task-status]")!.textContent).toBe(t("lib-state-labels.working"));
    expect(row.querySelector('[data-task-mark="working"] [data-state="working"]')).not.toBeNull();
    expect(row.querySelector("[data-task-todo-state]")).toBeNull();
  });

  it("updates the same row while it runs, opens the task, and moves it to Finished once it is accepted", () => {
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
    // finished: counted in neither entry and no longer work going on; the Finished entry lists it, new till it is opened
    // (#8237, #10922)
    expect(entry("running").textContent).toBe(label.running);
    expect(entry("needs").textContent).toBe(label.needs);
    expect(host.textContent).toContain(t("assistant-tasks.tray_empty_progress"));
    expect(host.querySelector("[data-task-slot]")).toBeNull();
    expect(entry("finished").textContent).toBe(`${label.finished}1`);
    act(() => entry("finished").click());
    const finished = host.querySelector("[data-tray-finished] [data-task-slot]")!;
    expect(finished.getAttribute("data-counts-open")).toBe("false");
    expect(finished.querySelector("[data-task-mark]")?.getAttribute("data-task-mark")).toBe("just_finished");
    expect(finished.querySelector("[data-task-status]")?.textContent).toBe(stateName("just_finished"));
  });

  it("lists what finished in the last day under Finished, seen or not, newest first; opening a new one marks it seen and keeps it (#10922)", async () => {
    const done = (id: string, title: string, doneAt: string, extra: Partial<Todo> = {}): Todo => ({ id, title, origin: conversation, created_at: "2026-11-09T07:00:00Z", updated_at: doneAt, done_at: doneAt, agents: [], ...extra });
    moreTodos = [
      done("f-seen", "Fix the export button", "2026-11-10T08:10:00Z", { seen_at: "2026-11-10T08:20:00Z" }),
      done("f-new", "Rename the settings tab", "2026-11-10T08:30:00Z"),
      done("f-old", "Move the docs", "2026-11-09T08:00:00Z"),
    ];
    await act(async () => refreshTodos());
    expect(entries().map((b) => b.dataset.trayEntry)).toEqual(["needs", "running", "finished"]);
    expect(entry("finished").textContent).toBe(`${label.finished}2`);
    expect(entry("finished").className).toContain("text-ink-2");
    expect(entry("finished").querySelector(".bg-green")).not.toBeNull();
    expect(entry("running").textContent).toBe(`${label.running}1`);
    act(() => entry("finished").click());
    const rows = () => [...host.querySelectorAll<HTMLButtonElement>("[data-tray-finished] [data-task-slot]")];
    expect(rows().map((row) => row.dataset.taskSlot)).toEqual(["task:f-new", "task:f-seen"]);
    expect(rows().map((row) => row.querySelector("[data-task-status]")!.getAttribute("data-task-status"))).toEqual(["just_finished", "done"]);
    expect(rows()[0].querySelector('[data-state="just_finished"]')).not.toBeNull();
    Object.assign(getState() as unknown as Record<string, unknown>, { features: ["todo_seen"] });
    const answer = vi.mocked(fetch).getMockImplementation()!;
    vi.mocked(fetch).mockImplementation(async (url, init) => {
      if (!String(url).includes("/todos/f-new/seen")) return answer(url, init);
      writes.push(`${init?.method} ${String(url)}`);
      return new Response(JSON.stringify({ todo: { ...moreTodos[1], seen_at: now.toISOString() } }), { headers: { "Content-Type": "application/json" } });
    });
    await act(async () => rows()[0].click());
    expect(getPanel()).toEqual({ kind: "task", todo: "f-new" });
    expect(writes.filter((path) => path.includes("/seen"))).toEqual([expect.stringMatching(/^PUT .*\/todos\/f-new\/seen$/)]);
    expect(rows().map((row) => row.dataset.taskSlot)).toEqual(["task:f-new", "task:f-seen"]);
    expect(rows().map((row) => row.querySelector("[data-task-status]")!.getAttribute("data-task-status"))).toEqual(["done", "done"]);
    expect(entry("finished").textContent).toBe(`${label.finished}2`);
  });

  it("shows the final Codex reply instead of startup progress, ignores legacy startup progress while active, and keeps newer real progress ahead of stale replies", async () => {
    const startup = "Codex will start in the terminal to use the requested reasoning effort. Approvals and questions will use the terminal screen.";
    const earlierReply = "Earlier turn draft.";
    const newerProgress = "Rechecking the export checksums.";
    const result = "The export now includes all rows.";
    const t0 = "2026-11-10T08:00:00Z";
    const t1 = "2026-11-10T08:01:00Z";
    const t2 = "2026-11-10T08:02:00Z";
    const t3 = "2026-11-10T08:03:00Z";
    const delivered: Todo = {
      id: "delivered", title: "Fix the export", origin: conversation, created_at: t0, updated_at: t0,
      agents: [{ machine_id: "demo", session_id: "export-worker", linked_at: t0 }],
      supervision: {
        supervisor: conversation,
        progress: { kind: "progress", text: startup, at: t1 },
      },
    };
    moreTodos = [delivered];
    await act(async () => refreshTodos());
    // Active task with empty last_text and legacy Codex startup notice in supervision.progress: never show the notice.
    act(() => upsertSession({ machine_id: "demo", session_id: "export-worker", title: "Export worker", cwd: "/demo",
      status: "working", state: "working", updated_at: t0 }));
    act(() => entry("running").click());
    const row = () => host.querySelector('[data-task-slot="task:delivered"]')?.textContent;
    expect(row()).not.toContain(startup);

    // Delivered task whose real progress arrived after an earlier reply shows the newer progress, not the stale reply.
    act(() => upsertSession({ machine_id: "demo", session_id: "export-worker", title: "Export worker", cwd: "/demo",
      status: "idle", state: "unread", updated_at: t1, last_text: earlierReply }));
    act(() => putTodo({
      ...delivered, delivery: { state: "live", at: t2 },
      supervision: { supervisor: conversation, progress: { kind: "progress", text: newerProgress, at: t2 } },
    }));
    expect(row()).toContain(newerProgress);
    expect(row()).not.toContain(earlierReply);

    // Once a newer final reply lands after that progress report, the reply wins over the older progress.
    act(() => upsertSession({ machine_id: "demo", session_id: "export-worker", title: "Export worker", cwd: "/demo",
      status: "idle", state: "unread", updated_at: t3, last_text: result }));
    expect(row()).toContain(result);
    expect(row()).not.toContain(newerProgress);

    // Even if a legacy startup notice had a newer timestamp than the reply, it is still ignored and the reply wins.
    act(() => putTodo({
      ...delivered, delivery: { state: "live", at: t3 },
      supervision: { supervisor: conversation, progress: { kind: "progress", text: startup, at: "2026-11-10T08:04:00Z" } },
    }));
    expect(row()).toContain(result);
    expect(row()).not.toContain(startup);

    act(() => putTodo({
      ...delivered, done_at: t3, delivery: { state: "live", at: t3 }, supervision: { ...delivered.supervision!, state: "user_check",
        reviews: [{ at: t3, verdict: "accepted" }], progress: { kind: "done", text: "Verified against the source.", at: t3 } },
    }));
    await act(async () => entry("finished").click());
    expect(row()).toContain("Verified against the source.");
  });

  it("keeps reopened accepted work Working while its Agent runs again", () => {
    act(() => entry("running").click());
    act(() => putTodo({ ...todo, done_at: undefined, supervision: { ...todo.supervision!, state: "accepted", reviews: [{ at, verdict: "accepted" }] } }));
    const running = host.querySelector("[data-task-slot]")!;
    expect(host.querySelector("[data-progress-just-finished]")).toBeNull();
    expect(running.querySelector("[data-task-status]")?.getAttribute("data-task-status")).toBe("working");
    expect(running.querySelector("[data-task-mark]")?.getAttribute("data-task-mark")).toBe("working");
  });

  it("keeps an accepted concern open instead of drawing it as Done", () => {
    act(() => entry("running").click());
    act(() => putTodo({ ...todo, kind: "concern", agents: [], done_at: undefined, supervision: { ...todo.supervision!, state: "accepted", reviews: [{ at, verdict: "accepted" }] } }));
    const concern = host.querySelector("[data-task-slot]")!;
    expect(concern.querySelector("[data-task-status]")?.getAttribute("data-task-status")).toBe("not_started");
    expect(concern.querySelector("[data-task-mark]")?.getAttribute("data-task-mark")).toBe("not_started");
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
    expect([...row.querySelectorAll("button")].map((b) => b.textContent?.trim())).toEqual([t("assistant-brief.open"), t("assistant-brief.finish"), t("assistant-task-actions.later"), t("assistant-task-actions.dismiss_draft"), t("assistant-brief.take")]);
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

  it("lists a task waiting for its supervisor's review under Persona reviewing, never Needs you, however long it waits (#8678)", async () => {
    moreSessions = [{ machine_id: "demo", session_id: "review-worker", title: "Review agent", cwd: "/demo", state: "idle", status: "idle", updated_at: at } as Session];
    const reviewed = (pending_since?: string, patch: Partial<Todo> = {}): Todo => ({
      ...todo, id: "tray-review", title: "Reviewed task", pr_url: undefined, plan_id: undefined,
      agents: [{ machine_id: "demo", session_id: "review-worker", linked_at: at }],
      supervision: { supervisor: conversation, state: "pending_review", updated_at: at, reviews: [], ...(pending_since ? { pending_since } : {}) },
      ...patch,
    });
    // no review waiting: no Persona reviewing entry at all
    expect(entry("review")).toBeNull();
    moreTodos = [reviewed("2026-11-10T08:50:00Z")]; // ten minutes before `now`
    await act(async () => { await refresh(); await refreshTodos(); });
    // the supervisor's review is not the owner's: Needs you stays empty and uncounted, In progress keeps only the task at work
    expect(entries().map((button) => button.textContent)).toEqual([label.needs, `${label.running}1`, `${label.review}1`]);
    // a neutral entry: a grey dot and secondary ink, neither the attention nor the working colour
    const dot = entry("review").querySelector("span[aria-hidden]")!;
    expect(dot.className).toContain("bg-ink-3");
    expect(dot.className).not.toMatch(/bg-(attention|accent)/);
    expect(entry("review").className).toContain("text-ink-2");
    act(() => entry("running").click());
    expect(trayRow("Reviewed task")).toBeUndefined();
    act(() => entry("review").click());
    expect(host.querySelector("section h2")!.textContent).toBe(label.review);
    const row = trayRow("Reviewed task")!;
    expect(row.dataset.countsOpen).toBe("true");
    expect(row.querySelector("[data-task-status]")!.textContent).toBe(t("assistant-tasks.review_supervisor"));
    expect(row.querySelector("[data-task-status]")!.getAttribute("data-task-status")).toBe("review");
    expect(row.querySelector('[data-task-mark="follow_up"]')).not.toBeNull();
    // A released Agent does not change whose turn it is: this tray is waiting for Persona, not the Agent.
    act(() => upsertSession({ ...moreSessions[0], state: "offline", status: "offline" }));
    expect(trayRow("Reviewed task")!.querySelector("[data-task-status]")!.textContent).toBe(t("assistant-tasks.review_supervisor"));
    expect(trayRow("Reviewed task")!.querySelector('[data-task-mark="stopped"]')).toBeNull();
    act(() => row.click());
    expect(getPanel()).toEqual({ kind: "task", todo: "tray-review" });
    // an hour on, nobody reviewed it yet: still Persona's, not the owner's
    act(() => putTodo(reviewed("2026-11-10T07:00:00Z")));
    expect(entries().map((button) => button.textContent)).toEqual([label.needs, `${label.running}1`, `${label.review}1`]);
    // a hub that does not say since when: the same
    act(() => putTodo(reviewed()));
    expect(entries().map((button) => button.textContent)).toEqual([label.needs, `${label.running}1`, `${label.review}1`]);
    // the review ends on a decision only the owner can make: Needs you; the open tray stays to say it is empty, and
    // once closed Persona reviewing goes away
    act(() => putTodo(reviewed(undefined, { supervision: { supervisor: conversation, state: "needs_user", updated_at: at, reviews: [] } })));
    expect(entries().map((button) => button.textContent)).toEqual([`${label.needs}1`, `${label.running}1`, label.review]);
    expect(trays()).toContain(t("assistant-tasks.tray_empty_review"));
    act(() => entry("review").click());
    expect(entries().map((button) => button.textContent)).toEqual([`${label.needs}1`, `${label.running}1`]);
    // an open ask (please verify) during a review is the owner's too
    act(() => putTodo(reviewed("2026-11-10T08:50:00Z", { ask: { outcome: "done", verdict: "Please check that the login page loads.", asked_at: at } })));
    expect(entries().map((button) => button.textContent)).toEqual([`${label.needs}1`, `${label.running}1`]);
    // accepted: closed, uncounted and under Finished, though its please-verify ask is still on it (#12974)
    act(() => putTodo(reviewed(undefined, { done_at: at, supervision: { supervisor: conversation, state: "accepted", updated_at: at, reviews: [] } })));
    expect(entries().map((button) => button.textContent)).toEqual([label.needs, `${label.running}1`, `${label.finished}1`]);
  });

  it("puts a task waiting on the user and a draft in Needs you only, without sending anything", async () => {
    const draft: FeedItem = { id: "tray-draft", conversation, at, kind: "message", ref: "draft-ref", body: { draft: "Demo reply", text: "Demo message" } };
    getFeedState().feeds[conversation] = [...feed, draft];
    // Keep the mocked hub in the same state: putTodo's lazy push setup may refresh it.
    todo = { ...todo, supervision: { ...todo.supervision!, state: "needs_user" } };
    act(() => putTodo(todo));
    expect(entries().map((button) => button.textContent)).toEqual([`${label.needs}2`, label.running]);
    await act(async () => {
      entry("needs").click();
      await import("./MessageDraft");
      await import("./TaskDecision");
    });
    expect(host.querySelector('[data-testid="message-card"]')).toBeNull(); // folded until opened
    await openRows();
    await expect.poll(() => host.querySelector('[data-testid="message-card"]')).not.toBeNull();
    await expect.poll(() => host.textContent).toContain(t("assistant-tasks.review_needs_user"));
    expect(host.textContent).toContain(t("assistant-tasks.review_needs_user"));
    const draftCard = host.querySelector('[data-testid="message-card"]')!;
    const actions = [...draftCard.querySelectorAll("button")];
    expect(actions.map((button) => button.textContent)).toEqual([t("assistant-task-actions.send"), t("assistant-task-actions.later"), t("assistant-task-actions.dismiss_draft")]);
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

  it("shows a released worker's verified pending prompt once and answers its exact card with its existing defer action", async () => {
    const interactionID = "ix_read_only_permission";
    const question = "Allow read-only access to the connector?";
    const deadline = "2026-11-10T15:00:00Z";
    todo = {
      ...todo,
      agents: [{ machine_id: "demo", session_id: "worker", linked_at: at, released_at: "2026-11-10T08:30:00Z" }],
      supervision: {
        supervisor: conversation, state: "parked",
        parked: {
          reason: "The connector preflight needs the owner's read-only decision", until: "2026-11-14T09:00:00Z", waiting_on: "external_event", count: 1,
          evidence: { kind: "external_event", target: "Read-only connector preflight", url: "https://example.test/connector/permissions/4", wake: "The owner answers the existing prompt", interaction_id: interactionID },
        },
      },
    } as unknown as Todo;
    moreSessions = [{ machine_id: "demo", session_id: "worker", title: "Demo agent", cwd: "/demo", state: "idle", status: "idle", updated_at: at } as Session];
    moreInteractions = [{
      id: interactionID, machine_id: "demo", session_id: "worker", kind: "question", created_at: at,
      questions: [{ question, options: [{ label: "Allow read-only" }, { label: "Later", action: "defer" }] }],
      delegate: { what: "Read-only connector preflight", deadline },
    } as Interaction];
    await act(async () => {
      await refresh();
      upsertSession(moreSessions[0]);
    });

    expect(entry("needs").textContent).toBe(`${label.needs}1`);
    act(() => entry("needs").click());
    await openRows("task:tray-task");
    expect(host.querySelectorAll('[data-needs-row="task:tray-task"]')).toHaveLength(1);
    expect(host.querySelectorAll(`[data-card="${interactionID}"]`)).toHaveLength(1);
    expect(host.querySelector("[data-parked-task-row]")).toBeNull();
    const card = host.querySelector<HTMLElement>(`[data-card="${interactionID}"]`)!;
    expect(card.textContent).toContain(question);
    expect(card.querySelector("[data-delegate-context]")!.textContent).toContain(formatDate(deadline, { dateStyle: "medium", timeStyle: "short" }));
    const defer = [...card.querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent === "Later")!;
    const beforeAnswer = writes.length;
    await act(async () => {
      defer.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(writes.slice(beforeAnswer)).toEqual([`/api/v1/interactions/${interactionID}/resolve`]);
    const answer = vi.mocked(fetch).mock.calls.find(([url, init]) => String(url) === `/api/v1/interactions/${interactionID}/resolve` && init?.method === "POST");
    expect(JSON.parse(String(answer?.[1]?.body))).toEqual({ answers: { [question]: "Later" } });
    expect(todo.done_at).toBeUndefined();
    expect(todo.supervision?.state).toBe("parked");
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
      mk("closed-old", "Closed long ago", "idle-worker", { done_at: "2026-11-08T08:00:00Z" }),
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
    expect(rows.map((row) => row.querySelector("[data-task-mark]")?.getAttribute("data-task-mark"))).toEqual(["working", "unread", "idle"]);
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

  it("marks an owner-held task in another conversation done from Needs you, with Undo and the count going down (#13541)", async () => {
    mainChat = conversation;
    let heldTodo: Todo = {
      id: "held-elsewhere", title: "Release abroad", origin: "another-chat", created_at: at, updated_at: at,
      pr_url: "https://github.com/example/demo/pull/7",
      delivery: { state: "pr", gate: "Approve the release", unblock: "user", at },
    };
    moreTodos = [heldTodo];
    const read = vi.mocked(fetch).getMockImplementation()!;
    vi.mocked(fetch).mockImplementation(async (url, init) => {
      if (init?.method === "PATCH") {
        writes.push(String(url));
        const body = JSON.parse(String(init.body));
        heldTodo = { ...heldTodo, done_at: body.done ? at : undefined };
        moreTodos = moreTodos.map((x) => (x.id === heldTodo.id ? heldTodo : x));
        return new Response(JSON.stringify({ todo: heldTodo }), { headers: { "Content-Type": "application/json" } });
      }
      return read(url, init);
    });
    const toastsHost = document.createElement("div");
    document.body.append(toastsHost);
    const toastsRoot = createRoot(toastsHost);
    act(() => toastsRoot.render(<Toasts />));
    try {
      await act(async () => { await refresh(); await refreshTodos(); });
      act(() => entry("needs").click());
      expect(entry("needs").textContent).toBe(`${label.needs}1`);
      const group = host.querySelector('[data-needs-supervisor="another-chat"]')!;
      expect(group).not.toBeNull();
      expect(group.getAttribute("aria-label")).toBe(t("assistant-tasks.needs_group_another"));
      // WaitingTaskRow is a lazy chunk: its supervisor group can commit before its action buttons do.
      await vi.waitFor(async () => {
        await act(async () => {});
        expect(group.querySelector("[data-open-task=held-elsewhere]")).not.toBeNull();
      });
      const close = group.querySelector<HTMLButtonElement>("[data-close-task=held-elsewhere]")!;
      expect(close).not.toBeNull();
      act(() => close.click());
      expect(close.hasAttribute("data-close-armed")).toBe(true);
      expect(writes).toHaveLength(0);
      await act(async () => close.click());
      expect(writes).toEqual(["/api/v1/todos/held-elsewhere"]);
      expect(JSON.parse(String(vi.mocked(fetch).mock.calls.find(([, init]) => init?.method === "PATCH")![1]?.body))).toEqual({ done: true });
      // the row leaves Needs you at once and the badge drops with it
      expect(host.querySelector('[data-needs-row="task:held-elsewhere"]')).toBeNull();
      expect(entry("needs").textContent).toBe(label.needs);
      const undo = [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("assistant-tasks.undo_close"));
      expect(undo).toBeDefined();
      await act(async () => undo!.click());
      expect(writes).toHaveLength(2);
      await act(async () => refreshTodos());
      expect(host.querySelector('[data-needs-row="task:held-elsewhere"]')).not.toBeNull();
    } finally {
      act(() => toastsRoot.unmount());
      toastsHost.remove();
    }
  });

  it("shows an escalated terminal confirmation (screen) card in main chat Needs you and sends the chosen option, while keeping delegating cards hidden", async () => {
    mainChat = conversation;
    const screenID = "scr_demo_worker_rev1";
    const command = "rm -rf dist && npm run build";
    const baseScreen: Interaction = {
      id: screenID,
      machine_id: "demo",
      session_id: "worker",
      kind: "screen",
      created_at: at,
      dialog: {
        question: "Do you want to proceed?",
        command,
        description: "Clean and rebuild the output directory",
        options: [{ key: "1", label: "Yes" }, { key: "2", label: "No" }],
        rev: "rev1",
      },
    };
    moreSessions = [{ machine_id: "demo", session_id: "worker", title: "Demo agent", cwd: "/demo", state: "needs_you", status: "blocked", updated_at: at, pending_interactions: 1 } as Session];

    // Held by supervisor (`delegating: true`): stays in In progress and out of main chat Needs you.
    todo = { ...todo, quiet: true, view: { phase: "working", section: "working", reason: "persona_working", rank: 0 } };
    moreInteractions = [{ ...baseScreen, delegating: true }];
    await act(async () => {
      await refresh();
      await refreshTodos();
      upsertSession(moreSessions[0]);
    });
    expect(entry("needs").textContent).toBe(label.needs);
    act(() => entry("needs").click());
    expect(host.querySelector('[data-needs-row="task:tray-task"]')).toBeNull();
    act(() => entry("needs").click());
    expect(entry("running").textContent).toBe(`${label.running}1`);
    await act(async () => entry("running").click());
    const heldStatus = host.querySelector('[data-task-slot] [data-task-status="working"]');
    expect(heldStatus?.textContent).toBe("督办处理中");
    await act(async () => entry("running").click());

    // A separate owner question takes priority while the supervisor still holds the screen card.
    todo = { ...todo, quiet: false, view: { phase: "needs_you", section: "needs_you", reason: "agent_asks", rank: 0 } };
    moreInteractions = [{ ...baseScreen, delegating: true }, {
      id: "ix-owner-question", machine_id: "demo", session_id: "worker", kind: "question", created_at: at,
      questions: [{ question: "Which report should we use?" }],
    }];
    await act(async () => {
      await refresh();
      await refreshTodos();
    });
    expect(entry("needs").textContent).toBe(`${label.needs}1`);
    await act(async () => entry("needs").click());
    const mixed = host.querySelector('[data-needs-row="task:tray-task"]');
    expect(mixed?.textContent).toContain("Which report should we use?");
    expect(mixed?.textContent).not.toContain("Do you want to proceed?");
    await act(async () => entry("needs").click());

    // Plain unescalated screen card on a quiet task (even with view needs_you / agent_asks): stays out of main chat
    // Needs you per D19 until explicitly escalated via ask_escalate.
    todo = { ...todo, quiet: true, view: { phase: "needs_you", section: "needs_you", reason: "agent_asks", rank: 0, asks: [{ id: "run:demo/worker", kind: "question", home: "run", interaction: interactionID(screenID) }] } };
    moreInteractions = [{ ...baseScreen, delegating: false }];
    await act(async () => {
      await refresh();
      await refreshTodos();
    });
    expect(entry("needs").textContent).toBe(label.needs);

    // Escalated via ask_escalate (`!delegating`, `delegate` set, hub view `needs_you` / `agent_asks`):
    // visible, counted, renders CommandScreenCard, and Allow resolves the exact screen card.
    moreInteractions = [{ ...baseScreen, delegating: false, delegate: { reason: "Deleting build output needs owner approval", recommend: "Allow" } }];
    await act(async () => {
      await refresh();
      await refreshTodos();
    });
    expect(entry("needs").textContent).toBe(`${label.needs}1`);
    expect(entry("running").textContent).toBe(label.running);
    act(() => entry("needs").click());
    const folded = host.querySelector<HTMLButtonElement>('button[data-needs-row="task:tray-task"]')!;
    expect(folded).not.toBeNull();
    expect(folded.textContent).toContain("Demo task");
    expect(folded.textContent).toContain("Do you want to proceed?");
    await openRows("task:tray-task");
    await act(async () => {
      await vi.dynamicImportSettled();
      await Promise.resolve();
    });
    const card = host.querySelector<HTMLElement>(`[data-card="${screenID}"]`)!;
    expect(card).not.toBeNull();
    expect(card.querySelector("[data-screen-command]")?.textContent).toBe(command);
    const allow = [...card.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === t("chat-interactions.allow"))!;
    expect(allow).toBeDefined();
    const before = writes.length;
    await act(async () => {
      allow.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(writes.slice(before)).toEqual([`/api/v1/interactions/${screenID}/resolve`]);
    const call = vi.mocked(fetch).mock.calls.find(([url, init]) => String(url) === `/api/v1/interactions/${screenID}/resolve` && init?.method === "POST");
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ option: "1" });
  });

  it("shows an escalated ordinary permission card in main chat Needs you and allows approving it in place, while keeping unescalated standalone permissions out", async () => {
    mainChat = conversation;
    const permID = "ix_perm_escalated";
    const escalatedPerm: Interaction = {
      id: permID,
      machine_id: "demo",
      session_id: "worker",
      kind: "permission",
      tool_name: "Bash",
      tool_input: { command: "gh pr comment 12 --body done" },
      created_at: at,
      delegate: { reason: "Commenting on the PR acts in your name", recommend: "Allow" },
    };
    const routinePerm: Interaction = {
      id: "ix_perm_standalone_noise",
      machine_id: "demo",
      session_id: "by-hand-perm",
      kind: "permission",
      tool_name: "Bash",
      tool_input: { command: "npm test" },
      created_at: at,
    };
    moreSessions = [
      { machine_id: "demo", session_id: "worker", title: "Demo agent", cwd: "/demo", state: "needs_you", status: "blocked", updated_at: at, pending_interactions: 1 } as Session,
      { machine_id: "demo", session_id: "by-hand-perm", title: "Standalone agent", cwd: "/demo", state: "needs_you", status: "blocked", updated_at: at, pending_interactions: 1 } as Session,
    ];
    moreInteractions = [escalatedPerm, routinePerm];
    await act(async () => {
      await refresh();
      await refreshTodos();
      for (const s of moreSessions) upsertSession(s);
    });

    // Only the escalated task permission counts; the unescalated standalone Agent permission stays out of main chat Needs you.
    expect(entry("needs").textContent).toBe(`${label.needs}1`);
    // Held pushes stay out of the primary main chat composer bar (surfaced in secondary views instead).
    expect(host.querySelector("[data-held-pushes-entry]")).toBeNull();
    expect(vi.mocked(fetch).mock.calls.some(([url]) => String(url).includes("/push/intercepts") || String(url).includes("/push/ln-mode"))).toBe(false);
    act(() => entry("needs").click());
    expect(host.querySelector('[data-needs-row="agent:demo/by-hand-perm"]')).toBeNull();
    await openRows("task:tray-task");
    await act(async () => {
      await vi.dynamicImportSettled();
      await Promise.resolve();
    });
    const card = host.querySelector<HTMLElement>(`[data-card="${permID}"]`)!;
    expect(card).not.toBeNull();
    expect(card.textContent).toContain("gh pr comment 12 --body done");
    const allow = [...card.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === t("chat-interactions.allow"))!;
    expect(allow).toBeDefined();
    const before = writes.length;
    await act(async () => {
      allow.click();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(writes.slice(before)).toEqual([`/api/v1/interactions/${permID}/resolve`]);
    const call = vi.mocked(fetch).mock.calls.find(([url, init]) => String(url) === `/api/v1/interactions/${permID}/resolve` && init?.method === "POST");
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ allow: true });
  });

  it("excludes another manager's tasks and standalone agents from ActionBar trays and TaskDock (#15645)", async () => {
    mainChat = conversation;
    todo = { ...todo, manager: "assistant" };
    const secondTodo: Todo = {
      id: "second-task",
      manager: "m-second",
      title: "Second assistant task",
      origin: conversation,
      created_at: at,
      updated_at: at,
      agents: [{ machine_id: "box-second", session_id: "sec-worker", linked_at: at }],
    };
    const defaultDecision: Todo = {
      id: "default-decision",
      manager: "assistant",
      title: "Default assistant decision",
      origin: "default-main",
      created_at: at,
      updated_at: at,
      supervision: { supervisor: "default-main", state: "needs_user", reviews: [], updated_at: at },
    };
    moreTodos = [secondTodo, defaultDecision];
    moreSessions = [
      { machine_id: "demo", session_id: "default-solo", manager: "assistant", title: "Default solo agent", cwd: "/demo", state: "working", status: "working", updated_at: at } as Session,
      { machine_id: "box-second", session_id: "sec-worker", manager: "m-second", title: "Second task agent", cwd: "/sec", state: "working", status: "working", updated_at: at } as Session,
      { machine_id: "box-second", session_id: "sec-solo", manager: "m-second", title: "Second solo agent", cwd: "/sec", state: "working", status: "working", updated_at: at } as Session,
    ];
    const dockHost = document.createElement("div");
    document.body.append(dockHost);
    const dockRoot = createRoot(dockHost);
    try {
      act(() => chooseManager("m-second"));
      await act(async () => {
        await refresh();
        await refreshTodos();
        for (const s of moreSessions) upsertSession(s);
      });
      act(() => dockRoot.render(<StrictMode><TaskDock conversation={conversation} /></StrictMode>));
      // Only m-second's task and standalone agent count; assistant's task, decision, and solo agent stay out.
      expect(entry("needs").textContent).toBe(label.needs);
      expect(entry("running").textContent).toBe(`${label.running}2`);
      expect(dockHost.textContent).toContain("Second assistant task");
      expect(dockHost.textContent).toContain("Second solo agent");
      expect(dockHost.textContent).not.toContain("Default solo agent");
      expect(dockHost.textContent).not.toContain("Demo task");
    } finally {
      act(() => dockRoot.unmount());
      dockHost.remove();
      act(() => resetManagerChoice());
    }
  });
});
