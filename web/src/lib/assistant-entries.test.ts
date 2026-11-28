import { describe, expect, it } from "vitest";
import fixture from "../../../spec/fixtures/entries/cases.json";
import { assistantEntries, decisionLaterKey, needsYouBySupervisor, taskRequestRouting, type NeedsYouItem } from "./assistant-entries";
import { triageTodos } from "./todo-triage";
import { makeSessionLookup } from "./todos";
import type { FeedItem, TaskPlan } from "./feed";
import type { Todo } from "./todos";
import type { Interaction, Session } from "./types";

it("keeps an Agent interaction on a verifying task under its original conversation in Main", () => {
  const at = "2026-11-10T08:00:00Z";
  const todo: Todo = { id: "verify", title: "Review the result", origin: "dispatch-chat", created_at: at, updated_at: at,
    supervision: { state: "user_check", supervisor: "direction-lead" }, ask: { verdict: "Please verify", asked_at: at } };
  const card: Interaction = { id: "question", kind: "question", machine_id: "machine", session_id: "agent", created_at: at };
  const item: NeedsYouItem = { kind: "task", key: "task:verify", todo, status: "needs_you", asks: [card], countsOpen: true };
  expect(needsYouBySupervisor([item], "main", true)).toEqual([
    { supervisor: "main", items: [] }, { supervisor: "dispatch-chat", items: [item] },
  ]);
});

it("does not rehome a conversation request when a task agent shares its session id", () => {
  const at = "2026-11-10T08:00:00Z";
  const todo: Todo = {
    id: "task", title: "Task", origin: "conversation", created_at: at, updated_at: at,
    agents: [{ machine_id: "hub", session_id: "shared", linked_at: at }],
  };
  const request: Interaction = {
    id: "own", kind: "question", machine_id: "hub", session_id: "shared",
    folded_under: "shared", folded_under_machine_id: "other", created_at: at,
  };
  const { asksOf } = taskRequestRouting([todo], [request], makeSessionLookup([]), "shared", "hub");
  expect(asksOf.get("hub/shared")).toBeUndefined();
  expect(asksOf.get("other/shared")).toEqual([request]);
});

describe("steps carried by an open request", () => {
  const at = "2026-11-10T08:00:00Z";
  const view = { section: "needs_you" as const, phase: "needs_you" as const, rank: 0 };
  const parent: Todo = {
    id: "request", title: "Prepare the release", origin: "direction", created_at: at, updated_at: at, agents: [],
    view: { ...view, reason: "verify", steps: { done: 0, total: 1 } },
  };
  const verifyingParent: Todo = {
    ...parent, supervision: { state: "user_check", supervisor: "direction" },
    ask: { verdict: "Please verify the request", asked_at: at },
  };
  const verification: Todo = {
    ...parent, id: "verify", title: "Review the result", part_of: parent.id,
    supervision: { state: "user_check", supervisor: "worker-supervisor" }, ask: { verdict: "Please verify", asked_at: at },
    view: { ...view, reason: "verify" },
  };
  const interaction: Interaction = { id: "confirm", kind: "permission", machine_id: "box", session_id: "worker", created_at: at };
  const confirmation: Todo = {
    ...parent, id: "confirm", title: "Allow the change", part_of: parent.id,
    agents: [{ machine_id: "box", session_id: "worker", linked_at: at }], view: { ...view, reason: "agent_asks" },
  };
  const entries = (todos: Todo[], interactions: Interaction[] = [], deferred?: Record<string, unknown>) => assistantEntries({
    conversation: "direction", main: "main", todos, sessions: [], interactions, plans: {}, feed: [], now: Date.parse(at), deferred,
  });

  it("keeps a verification and a live confirmation under the same counted parent", () => {
    const result = entries([{ ...parent, view: { ...view, reason: "verify", steps: { done: 0, total: 2 } } }, verification, confirmation], [interaction]);
    expect(result.needsYou).toHaveLength(1);
    expect(result.needsYou[0]).toMatchObject({
      key: "task:request", countsOpen: true,
      steps: [{ todo: { id: "confirm" }, asks: [interaction] }, { todo: { id: "verify" } }],
    });
    expect(result.running).toEqual([]);
  });

  it.each(["deferred", "dismissed"])("keeps a step independently actionable when the parent's verification is %s", (hiddenBy) => {
    const visible = entries([verifyingParent, verification]);
    expect(visible.needsYou).toMatchObject([{ key: "task:request", steps: [{ key: "task:verify" }] }]);
    expect(visible.running).toEqual([]);

    const request = hiddenBy === "dismissed" ?
      { ...verifyingParent, decision_review: { review_at: at, dismissal_id: "dismissed" } } : verifyingParent;
    const deferred = hiddenBy === "deferred" ? { [decisionLaterKey(request)]: true } : undefined;
    const result = entries([request, verification], [], deferred);
    expect(result.needsYou).toMatchObject([{ key: "task:verify", todo: { ask: verification.ask } }]);
    expect(result.running).toEqual([]);
  });

  it("keeps a live permission independent of a quiet parent with a visible verification", () => {
    const request = { ...verifyingParent, quiet: true };
    const result = entries([request, confirmation], [interaction]);
    expect(result.needsYou).toMatchObject([{ key: "task:confirm", asks: [interaction] }, { key: "task:request" }]);
    expect(result.needsYou[1]).not.toHaveProperty("steps");
    expect(result.running).toEqual([]);
  });

  it("keeps an owner-decision to-do independent of its open parent", () => {
    const ownerDecision: Todo = {
      ...verification, id: "todo_od_release", title: "Choose the release",
      supervision: { state: "needs_user", supervisor: "direction" },
      ask: { verdict: "Please choose", asked_at: at, outcome: "decide" },
      view: { ...view, reason: "owner_decides" },
    };
    const result = entries([parent, verification, ownerDecision]);
    expect(result.needsYou).toHaveLength(2);
    expect(result.needsYou.find((item) => item.key === "task:request")).toMatchObject({ steps: [{ key: "task:verify" }] });
    expect(result.needsYou.find((item) => item.key === "task:todo_od_release")).toMatchObject({ todo: { ask: ownerDecision.ask } });
    expect(result.running).toEqual([]);
  });

  it("removes an empty parent row when all of its carried steps are hidden", () => {
    expect(entries([parent, verification]).needsYou).toMatchObject([{ key: "task:request", steps: [{ key: "task:verify" }] }]);
    const result = entries([parent, verification], [], { [decisionLaterKey(verification)]: true });
    expect(result.needsYou).toEqual([]);
    expect(result.running).toEqual([]);
  });

  it("keeps a carried verification visible in its supervisor's tray when its open parent belongs to another conversation", () => {
    const otherParent: Todo = { ...parent, origin: "other-conversation" };
    const step: Todo = {
      ...verification, origin: "direction",
      supervision: { state: "user_check", supervisor: "direction" },
    };
    const result = entries([otherParent, step]);
    expect(result.needsYou).toMatchObject([{ key: "task:verify", todo: { ask: step.ask } }]);
    expect(result.running).toEqual([]);
  });

  it("keeps a verification step visible when its open parent is quiet", () => {
    const quietParent: Todo = { ...parent, quiet: true };
    const result = entries([quietParent, verification]);
    expect(result.needsYou).toMatchObject([{ key: "task:verify", todo: { ask: verification.ask } }]);
    expect(result.running.map((item) => item.key)).toEqual(["task:request"]);
  });

  it("places a parent carrying a direction user_check step in Main's own Needs you group", () => {
    const mainEntries = assistantEntries({
      conversation: "main", main: "main", todos: [parent, verification],
      sessions: [], interactions: [], plans: {}, feed: [], now: Date.parse(at),
    });
    expect(mainEntries.needsYou).toMatchObject([{ key: "task:request", steps: [{ key: "task:verify" }] }]);
    expect(needsYouBySupervisor(mainEntries.needsYou, "main", true)).toEqual([
      { supervisor: "main", items: mainEntries.needsYou },
    ]);
  });
});

// Android reads the same fixture (AssistantEntriesFixtureTest): the two entries split the same input the same way.
describe("the Assistant's Needs you and In progress entries (spec/fixtures/entries)", () => {
  for (const c of fixture.cases) {
    it(c.name, () => {
      const input = c.input as unknown as { conversation: string; main: string; now: string; todos: Todo[]; sessions: Session[]; interactions: Interaction[]; plans: Record<string, TaskPlan>; feed: FeedItem[] };
      const got = assistantEntries({ ...input, now: Date.parse(input.now) });
      // the web lists every task waiting for its supervisor's review under Persona reviewing (#8678), out of the two
      // entries the other clients put it in: the rest of the split is theirs
      const review = (c.expected as { web_review?: string[] }).web_review ?? [];
      const parked = (c.expected as { web_parked?: string[] }).web_parked ?? [];
      const verify = (c.expected as { web_verify?: string[] }).web_verify ?? [];
      const shared = (keys: string[]) => keys.filter((key) => !review.includes(key) && !parked.includes(key) && !verify.includes(key));
      expect(got.needsYou.map((item) => item.key)).toEqual(shared(c.expected.needs_you));
      expect(got.verify.map((item) => item.key)).toEqual(verify);
      expect(got.running.map((item) => item.key)).toEqual(shared(c.expected.running));
      expect(got.shelved.map((item) => item.key)).toEqual(shared(c.expected.shelved));
      expect(got.review.map((item) => item.key)).toEqual(review);
      expect(got.parked.map((item) => item.key)).toEqual(parked);
      // Finished (#10922): what the conversation handed out that finished in the last 24 hours, newest first, and which
      // of them nobody has opened since (drawn as new)
      const finished = c.expected as { finished?: string[]; finished_new?: string[] };
      expect(got.finished.map((item) => item.key)).toEqual(finished.finished ?? []);
      expect(got.finished.filter((item) => item.fresh).map((item) => item.key)).toEqual(finished.finished_new ?? []);
      // what the other clients' In progress says its supervisor is reviewing is under Persona reviewing here
      expect(shared((c.expected as { supervisor_review?: string[] }).supervisor_review ?? [])).toEqual([]);
      const activity = (c.expected as { activity?: Record<string, unknown> }).activity;
      for (const [key, expected] of Object.entries(activity ?? {})) {
        const work = got.running.find((row) => row.key === key)?.activity;
        expect(work ? { tasks: work.tasks, subagents: work.subagents, workflows: work.workflows,
          ...(work.workflow?.phase ? { phase: work.workflow.phase, progress: `${work.workflow.agents_done}/${work.workflow.agents_started}+` } : {}) } : null).toEqual(expected);
      }
      // one entry each: nothing is in both, and no agent of a listed task shows on its own
      const keys = [...got.needsYou, ...got.verify, ...got.running, ...got.shelved, ...got.review, ...got.parked, ...got.finished].map((item) => item.key);
      expect(new Set(keys).size).toBe(keys.length);
      const taskAgents = new Set(input.todos.filter((todo) => !todo.done_at && keys.includes(`task:${todo.id}`))
        .flatMap((todo) => (todo.agents ?? []).filter((a) => !a.released_at).map((a) => `agent:${a.machine_id}/${a.session_id}`)));
      expect(keys.filter((key) => taskAgents.has(key))).toEqual([]);
    });
  }
});

// An explicit external wait has its own Parked group: not Needs you, active work, shelved or finished unseen.
describe("a parked task in the Assistant's entries", () => {
  const now = Date.parse("2026-11-10T09:00:00Z");
  const parked = { reason: "The design team has to pick the icon set first", until: "2026-11-14T09:00:00Z", waiting_on: "someone_else" as const, count: 1 };
  const todo = (state: "parked" | "needs_user" | "", at: string): Todo => ({
    id: "todo_1", title: "Redraw the toolbar icons", origin: "main", plan_id: "plan_1", created_at: at, updated_at: at,
    agents: [{ machine_id: "box", session_id: "s1", linked_at: at }],
    supervision: { supervisor: "main", state, ...(state === "parked" ? { parked } : {}), reviews: [], updated_at: at },
  } as unknown as Todo);
  const entries = (t: Todo, at: string, sessions: Session[] = []) => assistantEntries({
    conversation: "main", main: "main", todos: [t], sessions, interactions: [], plans: {}, feed: [], now,
    cards: [{ id: "plan:plan_1:0", todo_id: t.id, plan_id: "plan_1", index: 0, at, updated_at: at, state: "working" }],
  });
  const keys = (got: ReturnType<typeof assistantEntries>) => ({ needsYou: got.needsYou.map((i) => i.key), running: got.running.map((i) => i.key), parked: got.parked.map((i) => i.key), shelved: got.shelved.map((i) => i.key) });

  it("is listed separately from active work and Needs you", () => {
    const at = "2026-11-10T08:00:00Z";
    expect(keys(entries(todo("parked", at), at))).toEqual({ needsYou: [], running: [], parked: ["task:todo_1"], shelved: [] });
    // the same task waiting for the owner's decision is theirs
    expect(keys(entries(todo("needs_user", at), at)).needsYou).toEqual(["task:todo_1"]);
  });

  it("stays in Parked after days without activity, where an unparked task folds away", () => {
    const at = "2026-11-05T08:00:00Z";
    expect(keys(entries(todo("parked", at), at))).toEqual({ needsYou: [], running: [], parked: ["task:todo_1"], shelved: [] });
    expect(keys(entries(todo("", at), at)).running).toEqual([]);
  });

  it("keeps a timed recheck with background work in In progress", () => {
    const at = "2026-11-10T08:00:00Z";
    const task = todo("parked", at);
    task.supervision!.parked = { reason: "Check background work", until: "2026-11-10T09:00:00Z", waiting_on: "date", count: 1 };
    const session = { machine_id: "box", session_id: "s1", title: "Review", cwd: "/work", state: "idle", status: "idle", updated_at: at,
      background_working: true, background_tasks_count: 1 } as Session;
    expect(keys(entries(task, at, [session]))).toEqual({ needsYou: [], running: ["task:todo_1"], parked: [], shelved: [] });
  });
});

describe("a parked external event linked to an existing owner prompt (#10527)", () => {
  const at = "2026-11-10T08:00:00Z";
  const interactionID = "ix_read_only_permission";
  const prompt: Interaction = {
    id: interactionID, machine_id: "box", session_id: "s1", kind: "question", created_at: at,
    questions: [{ question: "Allow read-only access to the connector?", options: [{ label: "Allow read-only" }, { label: "Later", action: "defer" }] }],
    delegate: { what: "Read-only connector preflight", deadline: "2026-11-10T14:00:00Z" },
  } as Interaction;
  const idle: Session = { machine_id: "box", session_id: "s1", title: "Connector worker", state: "idle", status: "idle", updated_at: at } as Session;
  const todo = (id: string | undefined, released = true): Todo => ({
    id: "todo_1", title: "Check the connector", origin: "main", created_at: at, updated_at: at,
    agents: [{ machine_id: "box", session_id: "s1", linked_at: at, ...(released ? { released_at: "2026-11-10T08:30:00Z" } : {}) }],
    supervision: {
      supervisor: "main", state: "parked",
      parked: {
        reason: "The connector preflight needs the owner's read-only decision", until: "2026-11-14T09:00:00Z", waiting_on: "external_event", count: 1,
        evidence: { kind: "external_event", target: "Read-only connector preflight", url: "https://example.test/connector/permissions/4", wake: "The owner answers the existing prompt", ...(id ? { interaction_id: id } : {}) },
      },
    },
  } as unknown as Todo);
  const entries = (td: Todo, interactions: Interaction[]) => assistantEntries({
    conversation: "main", main: "main", todos: [td], sessions: [idle], interactions, plans: {}, feed: [], now: Date.parse("2026-11-10T09:00:00Z"),
    cards: [{ id: "plan:plan_1:0", todo_id: td.id, plan_id: "plan_1", index: 0, at, updated_at: at, state: "working" }],
  });

  it("shows the exact pending prompt once after its worker is released, without duplicating an active worker's card", () => {
    const got = entries(todo(interactionID), [prompt]);
    expect(got.needsYou).toHaveLength(1);
    expect(got.needsYou[0]).toMatchObject({ kind: "task", key: "task:todo_1", asks: [prompt] });
    expect(got.parked).toEqual([]);
    // The existing active-agent path and the explicit link must not put the same prompt on two rows or twice on one row.
    const active = entries(todo(interactionID, false), [prompt]);
    expect(active.needsYou).toHaveLength(1);
    expect(active.needsYou[0]).toMatchObject({ kind: "task", key: "task:todo_1", asks: [prompt] });
    expect(active.parked).toEqual([]);
  });

  it("keeps one home when a released linked task shares its worker with a tied active task", () => {
    const linked = todo(interactionID);
    const other = { ...todo(undefined, false), id: "todo_2", title: "Other task" };
    const tied = { ...idle, state: "needs_you", todo: { id: "todo_2", title: "Other task" } } as Session;
    const cards = [linked, other].map((td) => ({ id: `plan:${td.id}:0`, todo_id: td.id, plan_id: `plan_${td.id}`, index: 0, at, updated_at: at, state: "working" as const }));
    const got = assistantEntries({ conversation: "main", main: "main", todos: [linked, other], sessions: [tied], interactions: [prompt], plans: {}, feed: [],
      now: Date.parse("2026-11-10T09:00:00Z"), cards });
    expect(got.needsYou).toHaveLength(1);
    expect(got.needsYou[0]).toMatchObject({ kind: "task", key: "task:todo_2", asks: [prompt] });
    expect(got.parked.map((item) => item.key)).toEqual(["task:todo_1"]);
  });

  it("returns an active worker to Parked when its linked interaction resolved but its session is stale", () => {
    const stale = { ...idle, state: "needs_you", status: "blocked", work: { phase: "blocked", interaction_id: interactionID } } as Session;
    const input = { conversation: "main", main: "main", todos: [todo(interactionID, false)], sessions: [stale], interactions: [] as Interaction[], plans: {}, feed: [],
      now: Date.parse("2026-11-10T09:00:00Z"),
      cards: [{ id: "plan:plan_1:0", todo_id: "todo_1", plan_id: "plan_1", index: 0, at, updated_at: at, state: "working" as const }] };
    const got = assistantEntries(input);
    expect(got.needsYou).toEqual([]);
    expect(got.parked.map((item) => item.key)).toEqual(["task:todo_1"]);

    // A different current card is still real owner input, not the resolved one.
    const newer = { ...prompt, id: "ix_new" };
    const next = assistantEntries({ ...input, sessions: [{ ...stale, work: { phase: "blocked", interaction_id: "ix_new" } }], interactions: [newer] });
    expect(next.needsYou).toHaveLength(1);
    expect(next.needsYou[0]).toMatchObject({ kind: "task", key: "task:todo_1", asks: [newer] });
  });

  it.each([
    ["no interaction is linked", undefined, [prompt]],
    ["the referenced interaction is unknown", "ix_not_pending", []],
    ["the referenced interaction is resolved", interactionID, []],
    ["the pending interaction belongs to another worker", interactionID, [{ ...prompt, session_id: "another-worker" }]],
  ])("keeps the external wait Parked when %s", (_name, id, interactions) => {
    const got = entries(todo(id), interactions);
    expect(got.needsYou).toEqual([]);
    expect(got.parked.map((item) => item.key)).toEqual(["task:todo_1"]);
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
    [...got.needsYou, ...got.running, ...got.review, ...got.parked].filter((item) => item.kind === "task") as { key: string; countsOpen: boolean }[];

  it("keeps an unexpired parked task open even after days without activity, as the page does", () => {
    const t = base("parked-stale", stale, { supervision: { supervisor: "main", state: "parked", parked, reviews: [], updated_at: stale } });
    const got = run([t]);
    expect(got.parked.map((i) => i.key)).toEqual(["task:parked-stale"]);
    expect(openTaskItems(got)[0]!.countsOpen).toBe(true);
    // Explicit parking survives inactivity; fresh and stale parked tasks use the same count.
    const freshTodo = base("parked-fresh", fresh, { supervision: { supervisor: "main", state: "parked", parked: { ...parked, until: "2026-11-14T09:00:00Z" }, reviews: [], updated_at: fresh } });
    expect(openTaskItems(run([freshTodo]))[0]!.countsOpen).toBe(true);
  });

  it("keeps a seen review under Persona reviewing and counted until it is accepted", () => {
    const review = { at: stale, verdict: "needs_user" as const, note: "Choose the route." };
    const seen = base("review-seen", stale, { seen_at: stale, supervision: { supervisor: "main", state: "pending_review", reviews: [review], updated_at: stale } });
    const got = run([seen]);
    expect(got.needsYou).toEqual([]);
    expect(got.review.map((i) => i.key)).toEqual(["task:review-seen"]);
    // #8678 owns the neutral placement; #5732 keeps Seen a receipt, never acceptance or filing.
    expect(openTaskItems(got)[0]!.countsOpen).toBe(true);
    const unseen = base("review-unseen", stale, { supervision: { supervisor: "main", state: "pending_review", reviews: [review], updated_at: stale } });
    expect(openTaskItems(run([unseen]))[0]!.countsOpen).toBe(true);
  });

  it("counts across the entries exactly the to-dos the To-dos page header calls open, and never plans or hand-opened Agents", () => {
    const workingSession: Session = { machine_id: "box", session_id: "working", title: "Worker", cwd: "/w", state: "working", status: "working", updated_at: fresh } as Session;
    const todos: Todo[] = [
      base("working", fresh, { agents: [{ machine_id: "box", session_id: "working", linked_at: fresh }] }),
      base("stale", stale),
      base("done", fresh, { done_at: fresh }),
      base("parked-stale", stale, { supervision: { supervisor: "main", state: "parked", parked, reviews: [], updated_at: stale } }),
      base("review-seen", stale, { seen_at: stale, supervision: { supervisor: "main", state: "pending_review", reviews: [{ at: stale, verdict: "needs_user" as const }], updated_at: stale } }),
      // a review nobody has looked at: under Persona reviewing (#8678), and still open on the page
      base("review-unseen", fresh, { supervision: { supervisor: "main", state: "pending_review", reviews: [], updated_at: fresh, pending_since: fresh } }),
    ];
    // a plan waiting for Start has no to-do yet: a Needs you row that is not a to-do
    const plans: Record<string, TaskPlan> = { plan: { id: "plan", conversation: "main", state: "open", created_at: fresh, tasks: [{ title: "Future work", prompt: "p", cwd: "/w", machine: "box" }] } };
    // a hand-opened Agent that belongs to no to-do: a running row that is not a to-do
    const loneAgent: Session = { machine_id: "box", session_id: "lone", title: "Lone", cwd: "/w", state: "working", status: "working", updated_at: fresh } as Session;
    const got = run(todos, [workingSession, loneAgent], plans);
    const pageOpen = triageTodos(todos, makeSessionLookup([workingSession, loneAgent]), now).open;
    const chipOpen = openTaskItems(got).filter((i) => i.countsOpen).length;
    expect(chipOpen).toBe(pageOpen);
    expect(chipOpen).toBe(4); // Working, parked and both unaccepted reviews; stale shelves and done do not count.
    expect(got.review.map((i) => i.key)).toEqual(["task:review-unseen", "task:review-seen"]);
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

// How a task ended is told in the tray, not by a card in the chat (#8237): the handed-out tasks that finished in the last
// day are the Finished entry (#10922), newest first, seen or not; the unseen ones are new. None of it is open work.
describe("Finished in the Assistant's entries", () => {
  const now = Date.parse("2026-11-10T09:00:00Z");
  const done = (id: string, doneAt: string, extra: Partial<Todo> = {}): Todo => ({
    id, title: id, origin: "main", created_at: "2026-11-10T07:00:00Z", updated_at: doneAt, done_at: doneAt, agents: [], ...extra,
  } as unknown as Todo);
  const run = (todos: Todo[], conversation = "main", at = now) => assistantEntries({ conversation, main: "main", todos, sessions: [], interactions: [], plans: {}, feed: [], now: at });

  it("lists what finished in the last day, newest first, seen or not, and counts none of it", () => {
    const got = run([
      done("older", "2026-11-10T08:00:00Z"),
      done("newer", "2026-11-10T08:30:00Z"),
      done("seen", "2026-11-10T08:10:00Z", { seen_at: "2026-11-10T08:20:00Z" }),
      done("stale", "2026-11-08T08:00:00Z"),
      done("byHand", "2026-11-10T08:40:00Z", { origin: "" }),
    ]);
    expect(got.finished.map((i) => i.key)).toEqual(["task:newer", "task:seen", "task:older"]);
    expect(got.finished.map((i) => i.fresh)).toEqual([true, false, true]);
    expect(got.finished.map((i) => i.at)).toEqual(["2026-11-10T08:30:00Z", "2026-11-10T08:10:00Z", "2026-11-10T08:00:00Z"].map(Date.parse));
    expect([...got.needsYou, ...got.running, ...got.shelved, ...got.parked, ...got.review]).toEqual([]);
  });

  it("files closed pending-review tasks as finished only within the completion window", () => {
    const supervision = { supervisor: "main", state: "pending_review" as const };
    const got = run([
      done("recent-review", "2026-11-10T08:30:00Z", { supervision }),
      done("expired-review", "2026-11-08T08:00:00Z", { supervision }),
    ]);
    expect(got.finished.map((item) => item.key)).toEqual(["task:recent-review"]);
    expect([...got.needsYou, ...got.running, ...got.shelved, ...got.parked, ...got.review]).toEqual([]);
  });

  it("keeps a row once it is opened: seeing it changes only that it is new", () => {
    const task = done("opened", "2026-11-10T08:30:00Z");
    expect(run([task]).finished.map((i) => [i.key, i.fresh])).toEqual([["task:opened", true]]);
    expect(run([{ ...task, seen_at: "2026-11-10T08:50:00Z" }]).finished.map((i) => [i.key, i.fresh])).toEqual([["task:opened", false]]);
  });

  it("lets a row go once it finished 24 hours ago, seen or not", () => {
    const task = done("day", "2026-11-09T09:00:00Z");
    expect(run([task], "main", now - 1000).finished.map((i) => i.key)).toEqual(["task:day"]);
    expect(run([task]).finished).toEqual([]);
    expect(run([{ ...task, seen_at: "2026-11-09T10:00:00Z" }], "main", now - 1000).finished.map((i) => i.fresh)).toEqual([false]);
  });

  it("leaves another conversation's finished work to it", () => {
    expect(run([done("newer", "2026-11-10T08:30:00Z")], "side").finished).toEqual([]);
    expect(run([done("side", "2026-11-10T08:30:00Z", { origin: "side" })], "side").finished.map((i) => i.key)).toEqual(["task:side"]);
  });
});

// A supervisor's review is Persona's to do, not the owner's (#8678): the web lists it under Persona reviewing however
// long it has waited, and only what the owner has to act on is in Needs you.
describe("Persona reviewing in the Assistant's entries", () => {
  const now = Date.parse("2026-11-10T09:00:00Z");
  const at = "2026-11-10T06:00:00Z";
  const idle = (id: string): Session => ({ machine_id: "box", session_id: id, title: id, cwd: "/w", state: "idle", status: "idle", updated_at: at } as Session);
  const task = (id: string, supervision: Todo["supervision"], patch: Partial<Todo> = {}): Todo => ({
    id, title: id, origin: "main", created_at: at, updated_at: at, agents: [{ machine_id: "box", session_id: id, linked_at: at }], supervision, ...patch,
  }) as Todo;
  const run = (todos: Todo[], sessions: Session[]) => assistantEntries({ conversation: "main", main: "main", todos, sessions, interactions: [], plans: {}, feed: [], now });
  const keys = (items: { key: string }[]) => items.map((item) => item.key);

  it("keeps every review out of Needs you, fresh, overdue or from a hub that gives no start, and keeps the owner's decisions there", () => {
    const got = run([
      task("fresh", { supervisor: "main", state: "pending_review", pending_since: "2026-11-10T08:50:00Z" }),
      task("overdue", { supervisor: "main", state: "pending_review", pending_since: "2026-11-10T06:00:00Z" }),
      task("old-hub", { supervisor: "main", state: "pending_review" }),
      task("needs-user", { supervisor: "main", state: "needs_user" }),
    ], ["fresh", "overdue", "old-hub", "needs-user"].map(idle));
    expect(keys(got.needsYou)).toEqual(["task:needs-user"]);
    expect(keys(got.review).sort()).toEqual(["task:fresh", "task:old-hub", "task:overdue"]);
    expect(got.running).toEqual([]);
    expect(got.review.every((item) => item.kind === "task" && item.countsOpen)).toBe(true);
  });

  it("leaves a review whose Agent works again in progress, and a review is never Waiting for merge", () => {
    const working: Session = { ...idle("working"), state: "working", status: "working" };
    const pr = "https://example.com/acme/app/pull/7";
    const got = run([
      task("working", { supervisor: "main", state: "pending_review" }),
      task("with-pr", { supervisor: "main", state: "pending_review" }, { seen_at: "2026-11-10T08:40:00Z", pr_url: pr, pr_status: { url: pr, state: "open", draft: false, review: "required", checks: "success", conflicts: false, checked_at: "2026-11-10T08:55:00Z" } }),
    ], [working, idle("with-pr")]);
    expect(keys(got.running)).toEqual(["task:working"]);
    expect(keys(got.review)).toEqual(["task:with-pr"]);
    expect(got.parked).toEqual([]);
    expect(got.needsYou).toEqual([]);
  });
});


// What a task's supervising conversation handles is not the owner's: a supervised task's failed start (its supervisor
// retries or remedies it) and an Agent's card its supervisor holds (delegating) are in progress, not Needs you; the
// same card once passed up to the owner, and an unsupervised failed start, still are.
describe("what a supervisor holds in the Assistant's entries", () => {
  const now = Date.parse("2026-11-10T09:00:00Z");
  const at = "2026-11-10T08:00:00Z";
  const supervision = { supervisor: "lead", state: "", reviews: [], updated_at: at } as unknown as Todo["supervision"];
  const asking: Session = { machine_id: "box", session_id: "ask", title: "ask", cwd: "/w", state: "needs_you", status: "blocked", updated_at: at } as Session;
  const card = (delegating: boolean): Interaction => ({ id: "ix_1", machine_id: "box", session_id: "ask", kind: "question", created_at: at, ...(delegating ? { delegating } : {}) }) as Interaction;
  const failed = (patch: Partial<Todo> = {}): Todo => ({ id: "failed", title: "failed", origin: "main", created_at: at, updated_at: at, done_at: at, start_error: "Desk Mac is offline", agents: [], ...patch }) as Todo;
  const withAgent = (patch: Partial<Todo> = {}): Todo => ({ id: "asks", title: "asks", origin: "main", created_at: at, updated_at: at, agents: [{ machine_id: "box", session_id: "ask", linked_at: at }], ...patch }) as Todo;
  const run = (todos: Todo[], interactions?: Interaction[]) => assistantEntries({ conversation: "main", main: "main", todos, sessions: interactions ? [asking] : [], interactions: interactions ?? [], plans: {}, feed: [], now });
  const keys = (items: { key: string }[]) => items.map((item) => item.key);

  it("leaves a supervised failed start with its supervisor, and an unsupervised one with the owner", () => {
    expect(keys(run([failed({ supervision })]).needsYou)).toEqual([]);
    expect(keys(run([failed({ supervision })]).running)).toEqual(["task:failed"]);
    expect(keys(run([failed()]).needsYou)).toEqual(["task:failed"]);
  });

  it("leaves a card the supervisor holds with it, and shows the same card once it reaches the owner", () => {
    const held = run([withAgent({ supervision })], [card(true)]);
    expect(keys(held.needsYou)).toEqual([]);
    expect(keys(held.running)).toEqual(["task:asks"]);
    expect(keys(run([withAgent({ supervision })], [card(false)]).needsYou)).toEqual(["task:asks"]);
  });
});

describe("pending plans across Persona conversations", () => {
  const at = "2026-11-21T01:00:00Z";
  const plan = (id: string, conversation: string, state: TaskPlan["state"] = "open"): TaskPlan => ({
    id, conversation, state, created_at: at, tasks: [],
  });
  const plans = { local: plan("local", "main"), routine: plan("routine", "routine-chat"),
    side: plan("side", "side-chat"), done: plan("done", "routine-chat", "dismissed") };
  const entries = (conversation: string, main?: string) => assistantEntries({
    conversation, main, plans, todos: [], sessions: [], interactions: [], feed: [],
  }).needsYou;

  it("shows every open plan in Main, attributed to its source", () => {
    const items = entries("main", "main");
    expect(items.map((item) => item.key)).toEqual(["plan:local", "plan:routine", "plan:side"]);
    expect(needsYouBySupervisor(items, "main").map((group) => [group.supervisor, group.items.map((item) => item.key)]))
      .toEqual([["main", ["plan:local"]], ["routine-chat", ["plan:routine"]], ["side-chat", ["plan:side"]]]);
  });

  it("excludes another manager's pending plan from Main", () => {
    const result = assistantEntries({ conversation: "main", main: "main", manager: "home",
      plans: { home: { ...plan("home", "routine"), manager: "home" }, work: { ...plan("work", "work-chat"), manager: "work" } },
      todos: [], sessions: [], interactions: [], feed: [],
    });
    expect(result.needsYou.map((item) => item.key)).toEqual(["plan:home"]);
  });

  it("keeps a side conversation scoped, including before Main is known", () => {
    expect(entries("side-chat", "main").map((item) => item.key)).toEqual(["plan:side"]);
    expect(entries("side-chat").map((item) => item.key)).toEqual(["plan:side"]);
  });
});


it("keeps Health plans out of Main while retaining them in their own conversation", () => {
 const plans: Record<string, TaskPlan> = { health: {id:"health",conversation:"health-chat",space:"health",state:"open",tasks:[]} };
 const input = {main:"main",plans,todos:[],sessions:[],interactions:[],feed:[]};
 expect(assistantEntries({...input,conversation:"main"}).needsYou).toEqual([]);
 expect(assistantEntries({...input,conversation:"health-chat"}).needsYou.map((item)=>item.key)).toEqual(["plan:health"]);
});

describe("multi-manager scoping (#15645)", () => {
  const at = "2026-11-10T08:00:00Z";
  const now = Date.parse("2026-11-10T09:00:00Z");
  const makeTodo = (id: string, manager: string | undefined, extra: Partial<Todo> = {}): Todo => ({
    id,
    title: id,
    origin: manager ? `${manager}-main` : "default-main",
    created_at: at,
    updated_at: at,
    ...(manager ? { manager } : {}),
    ...extra,
  } as Todo);
  const makeSession = (machine: string, id: string, manager: string | undefined, state: "working" | "needs_you" = "working"): Session => ({
    machine_id: machine,
    session_id: id,
    ...(manager ? { manager } : {}),
    title: id,
    cwd: "/work",
    state,
    status: state === "needs_you" ? "blocked" : "working",
    updated_at: at,
  } as Session);

  it("keeps other managers' todos, standalone agents, interactions, and plans out of a secondary manager's Main", () => {
    const defaultTodo = makeTodo("todo-default", "assistant", {
      agents: [{ machine_id: "box-a", session_id: "s-def-task", linked_at: at }],
    });
    const unstampedDefaultTodo = makeTodo("todo-unstamped", undefined, {
      supervision: { supervisor: "default-main", state: "needs_user", reviews: [], updated_at: at },
    });
    const secondTodo = makeTodo("todo-second", "m-second", {
      agents: [{ machine_id: "box-b", session_id: "s-sec-task", linked_at: at }],
    });
    const sessions = [
      makeSession("box-a", "s-def-task", "assistant", "working"),
      makeSession("box-a", "s-def-solo", "assistant", "working"),
      makeSession("box-a", "s-unstamped-solo", undefined, "needs_you"),
      makeSession("box-b", "s-sec-task", "m-second", "working"),
      makeSession("box-b", "s-sec-solo", "m-second", "working"),
    ];
    const interactions: Interaction[] = [
      { id: "ix-def", manager: "assistant", machine_id: "box-a", session_id: "s-def-solo", kind: "question", created_at: at } as Interaction,
      { id: "ix-unstamped", machine_id: "box-a", session_id: "s-unstamped-solo", kind: "question", created_at: at } as Interaction,
      { id: "ix-sec", manager: "m-second", machine_id: "box-b", session_id: "s-sec-solo", kind: "question", created_at: at } as Interaction,
    ];
    const plans: Record<string, TaskPlan> = {
      pDef: { id: "pDef", conversation: "assistant-main", state: "open", created_at: at, tasks: [] },
      pSec: { id: "pSec", manager: "m-second", conversation: "m-second-main", state: "open", created_at: at, tasks: [] },
    };

    const second = assistantEntries({
      conversation: "m-second-main",
      main: "m-second-main",
      manager: "m-second",
      todos: [defaultTodo, unstampedDefaultTodo, secondTodo],
      sessions,
      interactions,
      plans,
      feed: [],
      now,
    });
    expect(second.running.map((item) => item.key)).toEqual(["task:todo-second", "agent:box-b/s-sec-solo"]);
    expect(second.needsYou.map((item) => item.key)).toEqual(["plan:pSec"]);

    // Default manager still includes both explicitly stamped "assistant" items and unstamped legacy items, excluding m-second.
    const primary = assistantEntries({
      conversation: "assistant-main",
      main: "assistant-main",
      manager: "assistant",
      todos: [defaultTodo, unstampedDefaultTodo, secondTodo],
      sessions,
      interactions,
      plans,
      feed: [],
      now,
    });
    expect(primary.running.map((item) => item.key)).toEqual(["task:todo-default", "agent:box-a/s-def-solo"]);
    expect(primary.needsYou.map((item) => item.key)).toEqual(["agent:box-a/s-unstamped-solo", "plan:pDef", "task:todo-unstamped"]);
  });
});

describe("delegating cards stay out of Needs you for both tasks and standalone agents", () => {
  const at = "2026-11-10T08:00:00Z";
  const now = Date.parse("2026-11-10T09:00:00Z");

  it("keeps non-supervised delegating task cards and standalone delegating dock agents in running", () => {
    const todo: Todo = {
      id: "todo-alpha",
      title: "Inspect dir-alpha logs",
      origin: "main",
      created_at: at,
      updated_at: at,
      agents: [{ machine_id: "box-a", session_id: "s-task-ro", linked_at: at }],
    } as Todo;
    const sessions: Session[] = [
      { machine_id: "box-a", session_id: "s-task-ro", title: "Task reader", cwd: "/work", state: "needs_you", status: "blocked", pending_interactions: 1, updated_at: at } as Session,
      { machine_id: "box-a", session_id: "s-solo-ro", title: "Solo reader", cwd: "/work", state: "needs_you", status: "blocked", pending_interactions: 1, updated_at: at } as Session,
      { machine_id: "box-a", session_id: "s-solo-mixed", title: "Solo mixed", cwd: "/work", state: "needs_you", status: "blocked", pending_interactions: 2, updated_at: at } as Session,
    ];
    const interactions: Interaction[] = [
      { id: "ix-task-ro", machine_id: "box-a", session_id: "s-task-ro", kind: "permission", tool_name: "Read", delegating: true, created_at: at } as Interaction,
      { id: "ix-solo-ro", machine_id: "box-a", session_id: "s-solo-ro", kind: "permission", tool_name: "Read", delegating: true, created_at: at } as Interaction,
      { id: "ix-solo-held", machine_id: "box-a", session_id: "s-solo-mixed", kind: "permission", tool_name: "Read", delegating: true, created_at: at } as Interaction,
      { id: "ix-solo-esc", machine_id: "box-a", session_id: "s-solo-mixed", kind: "question", questions: [{ question: "Confirm branch?" }], delegating: false, created_at: at } as Interaction,
    ];

    const got = assistantEntries({ conversation: "main", main: "main", todos: [todo], sessions, interactions, plans: {}, feed: [], now });
    expect(got.needsYou.map((item) => item.key)).toEqual(["agent:box-a/s-solo-mixed"]);
    expect(got.needsYou[0]).toMatchObject({ kind: "agent", asks: [interactions[3]] });
    expect(got.running.map((item) => item.key).sort()).toEqual(["agent:box-a/s-solo-ro", "task:todo-alpha"]);
  });
});
