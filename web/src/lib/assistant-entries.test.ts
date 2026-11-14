import { describe, expect, it } from "vitest";
import fixture from "../../../spec/fixtures/entries/cases.json";
import { assistantEntries, decisionLaterKey } from "./assistant-entries";
import { triageTodos } from "./todo-triage";
import { makeSessionLookup } from "./todos";
import type { FeedItem, TaskPlan } from "./feed";
import type { Todo } from "./todos";
import type { Interaction, Session } from "./types";

// Android reads the same fixture (AssistantEntriesFixtureTest): the two entries split the same input the same way.
describe("the Assistant's Needs you and In progress entries (spec/fixtures/entries)", () => {
  for (const c of fixture.cases) {
    it(c.name, () => {
      const input = c.input as unknown as { conversation: string; main: string; now: string; todos: Todo[]; sessions: Session[]; interactions: Interaction[]; plans: Record<string, TaskPlan>; feed: FeedItem[] };
      const got = assistantEntries({ ...input, now: Date.parse(input.now) });
      expect(got.needsYou.map((item) => item.key)).toEqual(c.expected.needs_you);
      expect(got.running.map((item) => item.key)).toEqual(c.expected.running);
      expect(got.shelved.map((item) => item.key)).toEqual(c.expected.shelved);
      const activity = (c.expected as { activity?: Record<string, unknown> }).activity;
      for (const [key, expected] of Object.entries(activity ?? {})) {
        const work = got.running.find((row) => row.key === key)?.activity;
        expect(work ? { tasks: work.tasks, subagents: work.subagents, workflows: work.workflows,
          ...(work.workflow?.phase ? { phase: work.workflow.phase, progress: `${work.workflow.agents_done}/${work.workflow.agents_started}+` } : {}) } : null).toEqual(expected);
      }
      // one entry each: nothing is in both, and no agent of a listed task shows on its own
      const keys = [...got.needsYou, ...got.running, ...got.shelved].map((item) => item.key);
      expect(new Set(keys).size).toBe(keys.length);
      const taskAgents = new Set(input.todos.filter((todo) => !todo.done_at && keys.includes(`task:${todo.id}`))
        .flatMap((todo) => (todo.agents ?? []).filter((a) => !a.released_at).map((a) => `agent:${a.machine_id}/${a.session_id}`)));
      expect(keys.filter((key) => taskAgents.has(key))).toEqual([]);
    });
  }
});

// A task its supervisor parked (#3804) is In progress, however it would be filed otherwise: not Needs you, not folded
// under shelved after two days still, not dropped as finished unseen.
describe("a parked task in the Assistant's entries", () => {
  const now = Date.parse("2026-11-10T09:00:00Z");
  const parked = { reason: "The design team has to pick the icon set first", until: "2026-11-14T09:00:00Z", waiting_on: "someone_else" as const, count: 1 };
  const todo = (state: "parked" | "needs_user" | "", at: string): Todo => ({
    id: "todo_1", title: "Redraw the toolbar icons", origin: "main", plan_id: "plan_1", created_at: at, updated_at: at,
    agents: [{ machine_id: "box", session_id: "s1", linked_at: at }],
    supervision: { supervisor: "main", state, ...(state === "parked" ? { parked } : {}), reviews: [], updated_at: at },
  } as unknown as Todo);
  const entries = (t: Todo, at: string) => assistantEntries({
    conversation: "main", main: "main", todos: [t], sessions: [], interactions: [], plans: {}, feed: [], now,
    cards: [{ id: "plan:plan_1:0", todo_id: t.id, plan_id: "plan_1", index: 0, at, updated_at: at, state: "working" }],
  });
  const keys = (got: ReturnType<typeof assistantEntries>) => ({ needsYou: got.needsYou.map((i) => i.key), running: got.running.map((i) => i.key), shelved: got.shelved.map((i) => i.key) });

  it("is listed with the tasks in progress, not under Needs you", () => {
    const at = "2026-11-10T08:00:00Z";
    expect(keys(entries(todo("parked", at), at))).toEqual({ needsYou: [], running: ["task:todo_1"], shelved: [] });
    // the same task waiting for the owner's decision is theirs
    expect(keys(entries(todo("needs_user", at), at)).needsYou).toEqual(["task:todo_1"]);
  });

  it("stays in progress after days without activity, where an unparked task folds away", () => {
    const at = "2026-11-05T08:00:00Z";
    expect(keys(entries(todo("parked", at), at))).toEqual({ needsYou: [], running: ["task:todo_1"], shelved: [] });
    expect(keys(entries(todo("", at), at)).running).toEqual([]);
  });
});

describe("review dismissal with an interrupted Agent", () => {
  const at = "2026-11-10T08:00:00.123456789Z";
  const todo: Todo = {
    id: "task", title: "Example rollout", origin: "main", created_at: at, updated_at: at,
    agents: [{ machine_id: "box", session_id: "worker", linked_at: at }],
    supervision: { supervisor: "main", state: "needs_user", reviews: [{ at, verdict: "needs_user", note: "Choose the rollout route." }] },
    decision_review: { review_at: at, dismissal_id: "dismiss-demo" },
  };
  const session: Session = { machine_id: "box", session_id: "worker", title: "Example Agent", cwd: "/example", state: "idle", status: "idle", updated_at: at, turn_error: "Synthetic transient error", turn_error_kind: "transient" };
  const input = { conversation: "main", main: "main", todos: [todo], sessions: [session], interactions: [], plans: {}, feed: [],
    cards: [{ id: "task:task", todo_id: todo.id, at, updated_at: at, state: "working" as const }] };

  it("honors a same-round dismissal despite an old runtime error", () => {
    expect(assistantEntries(input).needsYou).toEqual([]);
  });

  it("honors Later for the same review despite an old runtime error", () => {
    const deferredTodo = { ...todo, decision_review: { review_at: at } };
    expect(assistantEntries({ ...input, todos: [deferredTodo], deferred: { [decisionLaterKey(deferredTodo)]: true } }).needsYou).toEqual([]);
  });

  it("does not hide a new review one nanosecond later", () => {
    const next = "2026-11-10T08:00:00.123456790Z";
    const newer = { ...todo, supervision: { ...todo.supervision!, reviews: [{ at: next, verdict: "needs_user" as const }] } };
    expect(assistantEntries({ ...input, todos: [newer], deferred: { [decisionLaterKey(todo)]: true } }).needsYou.map((r) => r.key)).toEqual(["task:task"]);
  });

  it("keeps a pending Agent question visible with its own home", () => {
    const interaction = { id: "question", machine_id: "box", session_id: "worker", kind: "question", created_at: at } as Interaction;
    const got = assistantEntries({ ...input, interactions: [interaction], deferred: { [decisionLaterKey(todo)]: true } });
    expect(got.needsYou.map((r) => r.key)).toEqual(["task:task"]);
    expect(got.needsYou[0]).toMatchObject({ asks: [interaction] });
  });

  it("keeps Agent input visible even while its interaction has not loaded", () => {
    const got = assistantEntries({ ...input, sessions: [{ ...session, state: "needs_you" }], deferred: { [decisionLaterKey(todo)]: true } });
    expect(got.needsYou.map((r) => r.key)).toEqual(["task:task"]);
  });

  it("keeps an interruption without a review decision visible", () => {
    const interrupted = { ...todo, supervision: undefined, decision_review: undefined };
    expect(assistantEntries({ ...input, todos: [interrupted] }).needsYou.map((r) => r.key)).toEqual(["task:task"]);
  });
});

// #3331: the chip bar's numbers use the To-dos page's one rule (lib/todo-triage countsAsOpen). Rows stay visible
// whatever the bucket; only the count follows whether the page's "Not done" counts the to-do.
describe("the entries' open-to-do count follows the To-dos page's rule", () => {
  const now = Date.parse("2026-11-10T09:00:00Z");
  const fresh = "2026-11-10T08:00:00Z";
  const stale = "2026-11-05T08:00:00Z";
  const parked = { reason: "The design team has to pick the icon set first", until: "2026-11-14T09:00:00Z", waiting_on: "someone_else" as const, count: 1 };
  const base = (id: string, at: string, patch: Partial<Todo> = {}): Todo => ({
    id, title: id, origin: "main", created_at: at, updated_at: at,
    agents: [{ machine_id: "box", session_id: id, linked_at: at }], ...patch,
  }) as Todo;
  const run = (todos: Todo[], sessions: Session[] = [], plans: Record<string, TaskPlan> = {}) =>
    assistantEntries({ conversation: "main", main: "main", todos, sessions, interactions: [], plans, feed: [], now });
  const openTaskItems = (got: ReturnType<typeof assistantEntries>) =>
    [...got.needsYou, ...got.running].filter((item) => item.kind === "task") as { key: string; countsOpen: boolean }[];

  it("keeps an unexpired parked task open even after days without activity, as the page does", () => {
    const t = base("parked-stale", stale, { supervision: { supervisor: "main", state: "parked", parked, reviews: [], updated_at: stale } });
    const got = run([t]);
    expect(got.running.map((i) => i.key)).toEqual(["task:parked-stale"]);
    expect(openTaskItems(got)[0]!.countsOpen).toBe(true);
    // Explicit parking survives inactivity; fresh and stale parked tasks use the same count.
    const freshTodo = base("parked-fresh", fresh, { supervision: { supervisor: "main", state: "parked", parked: { ...parked, until: "2026-11-14T09:00:00Z" }, reviews: [], updated_at: fresh } });
    expect(openTaskItems(run([freshTodo]))[0]!.countsOpen).toBe(true);
  });

  it("keeps a seen pending-review decision visible in Needs you but counts it only while the page still has it open", () => {
    const review = { at: stale, verdict: "needs_user" as const, note: "Choose the route." };
    const seen = base("review-seen", stale, { seen_at: stale, supervision: { supervisor: "main", state: "pending_review", reviews: [review], updated_at: stale } });
    const got = run([seen]);
    expect(got.needsYou.map((i) => i.key)).toEqual(["task:review-seen"]);
    expect(openTaskItems(got)[0]!.countsOpen).toBe(false);
    // nobody looked at the finished turn yet: the page keeps it in Just finished, so it still counts
    const unseen = base("review-unseen", stale, { supervision: { supervisor: "main", state: "pending_review", reviews: [review], updated_at: stale } });
    expect(openTaskItems(run([unseen]))[0]!.countsOpen).toBe(true);
  });

  it("counts across both entries exactly the to-dos the To-dos page header calls open, and never plans or hand-opened Agents", () => {
    const workingSession: Session = { machine_id: "box", session_id: "working", title: "Worker", cwd: "/w", state: "working", status: "working", updated_at: fresh } as Session;
    const todos: Todo[] = [
      base("working", fresh, { agents: [{ machine_id: "box", session_id: "working", linked_at: fresh }] }),
      base("stale", stale),
      base("done", fresh, { done_at: fresh }),
      base("parked-stale", stale, { supervision: { supervisor: "main", state: "parked", parked, reviews: [], updated_at: stale } }),
      base("review-seen", stale, { seen_at: stale, supervision: { supervisor: "main", state: "pending_review", reviews: [{ at: stale, verdict: "needs_user" as const }], updated_at: stale } }),
    ];
    // a plan waiting for Start has no to-do yet: a Needs you row that is not a to-do
    const plans: Record<string, TaskPlan> = { plan: { id: "plan", conversation: "main", state: "open", created_at: fresh, tasks: [{ title: "Future work", prompt: "p", cwd: "/w", machine: "box" }] } };
    // a hand-opened Agent that belongs to no to-do: a running row that is not a to-do
    const loneAgent: Session = { machine_id: "box", session_id: "lone", title: "Lone", cwd: "/w", state: "working", status: "working", updated_at: fresh } as Session;
    const got = run(todos, [workingSession, loneAgent], plans);
    const pageOpen = triageTodos(todos, makeSessionLookup([workingSession, loneAgent]), now).open;
    const chipOpen = openTaskItems(got).filter((i) => i.countsOpen).length;
    expect(chipOpen).toBe(pageOpen);
    expect(chipOpen).toBe(2); // Working and unexpired parked; inactivity alone does not end an explicit wait.
    expect(got.needsYou.some((i) => i.kind === "plan")).toBe(true);
    expect(got.running.some((i) => i.kind === "agent")).toBe(true);
  });
});

// One home per request (docs/design/DESIGN_SYSTEM.md 4.8): an Agent that several to-dos list asks under one of them, so
// one approval is one Needs you row and is counted once, not once for every to-do that lists the Agent.
describe("an Agent several to-dos list", () => {
  const at = "2026-11-10T08:00:00Z";
  const agent = { machine_id: "box", session_id: "tests", linked_at: at };
  const todo = (id: string, patch: Partial<Todo> = {}): Todo => ({ id, title: id, origin: "main", created_at: at, updated_at: at, agents: [agent], ...patch }) as Todo;
  const asking: Session = { machine_id: "box", session_id: "tests", title: "Network tests", cwd: "/w", state: "needs_you", status: "blocked", updated_at: at } as Session;
  const approval = { id: "perm", machine_id: "box", session_id: "tests", kind: "permission", tool_name: "Bash", created_at: at } as Interaction;
  const run = (todos: Todo[], session = asking) => assistantEntries({ conversation: "main", main: "main", todos, sessions: [session], interactions: [approval], plans: {}, feed: [], now: Date.parse(at) });
  const withAsks = (got: ReturnType<typeof assistantEntries>) => got.needsYou.filter((i) => (i.kind === "task" || i.kind === "agent") && i.asks.length).map((i) => i.key);

  it("asks under the to-do the hub ties its session to; the others are in progress", () => {
    const got = run([todo("cache"), todo("release"), todo("tests")], { ...asking, todo: { id: "release", title: "release" } });
    expect(withAsks(got)).toEqual(["task:release"]);
    expect(got.needsYou.map((i) => i.key)).toEqual(["task:release"]);
    expect(got.running.map((i) => i.key).sort()).toEqual(["task:cache", "task:tests"]);
  });

  it("asks under the first to-do that lists it when the hub ties it to none of them", () => {
    const got = run([todo("cache"), todo("tests")]);
    expect(withAsks(got)).toEqual(["task:cache"]);
    expect(got.running.map((i) => i.key)).toEqual(["task:tests"]);
  });

  it("keeps another to-do's own decision in Needs you, without the Agent's card", () => {
    const decide = todo("tests", { ask: { asked_at: at, outcome: "decide", verdict: "needs_user", question: "Which framework?" } });
    const got = run([todo("release"), decide], { ...asking, todo: { id: "release", title: "release" } });
    expect(got.needsYou.map((i) => i.key).sort()).toEqual(["task:release", "task:tests"]);
    expect(got.needsYou.find((i) => i.key === "task:tests")).toMatchObject({ asks: [] });
  });
});
