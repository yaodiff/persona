import { describe, expect, it } from "vitest";
import { TODO_LIST_SECTIONS, TODO_PHASES, type TodoSummary } from "../../../lib/todo-view-wire";
import { TRIAGE_SECTIONS, type Triage, type TriageRule, type TriageSection } from "../../../lib/todo-triage";
import { makeSessionLookup, type Todo } from "../../../lib/todos";
import type { Session } from "../../../lib/types";
import { BY_DESIGN, BOUNDARY_MS, classify, KNOWN_PHASES, knownView, legacyOf, PAGE_ORDER, restoredAt, rowFacts, type ByDesign, type RowFacts } from "./view-legacy";

const NOW = Date.parse("2026-11-10T12:00:00Z");
const HOUR = 3_600_000;
const local = (section: TriageSection, rule: TriageRule, last = NOW - HOUR): Triage => ({ section, rule, last });
// The view as the wire carries it: raw strings, which may be a newer hub's values the closed types do not know.
const view = (section: string, reason: string, more: object = {}): TodoSummary => ({ phase: "todo", section, reason, rank: 0, ...more }) as TodoSummary;
const facts = (more: Partial<RowFacts> = {}): RowFacts => ({
  missing: false,
  usageLimit: false,
  waits: false,
  transient: false,
  awaitsReply: false,
  failedLong: false,
  due: false,
  restoredHere: false,
  prOpen: false,
  ...more,
});
const step = (key: string) => ({ line: { key, args: { step: "Book the ryokan" } } });
const inactive = { inactive: "2026-11-08T00:00:00Z" };

describe("legacyOf", () => {
  it("files every view section where today's page draws it", () => {
    const got = Object.fromEntries(TODO_LIST_SECTIONS.map((s) => [s, legacyOf(view(s, "rest"))]));
    expect(got).toEqual({
      needs_you: "waiting",
      can_wait: "waiting",
      working: "working",
      waiting: "parked",
      todo: "other",
      just_finished: "just_done",
      someday: "other",
      done: "done",
    });
    expect(legacyOf(view("todo", "rest", inactive))).toBe("shelved");
  });

  it("keeps #5732 closes and cancellations in their own sections, and live delivery awaiting review", () => {
    const closed = view("just_finished", "closed_unseen", { phase: "done" });
    expect(legacyOf(closed)).toBe("done");
    expect(legacyOf(closed, true)).toBe("just_done"); // a fresh closed concern
    expect(legacyOf(view("done", "canceled", { phase: "done" }))).toBe("cancelled");
    const live = view("just_finished", "finished_recent", { line: { key: "deployed" } });
    expect(classify(local("just_done", "finished_unseen"), live, facts(), NOW)).toEqual({ kind: "same" });
  });

  it("reads only phases, sections and reasons this web knows", () => {
    expect(KNOWN_PHASES).toEqual(TODO_PHASES);
    expect(knownView(view("todo", "rest"))).toBe(true);
    expect(knownView(view("todo", "rest", { phase: "a_newer_phase" }))).toBe(false);
    expect(knownView(view("later", "rest"))).toBe(false);
    expect(knownView(view("todo", "a_newer_reason"))).toBe(false);
    expect(knownView(undefined)).toBe(false);
    expect(classify(local("other", "rest"), view("todo", "rest", { phase: "a_newer_phase" }), facts(), NOW)).toEqual({ kind: "unknown" });
    expect(classify(local("other", "rest"), view("todo", "a_newer_reason"), facts(), NOW)).toEqual({ kind: "unknown" });
  });

  it("orders today's sections as the page does", () => {
    expect(PAGE_ORDER).toEqual(TRIAGE_SECTIONS);
  });
});

describe("the decided differences: each case, and look-alikes that are not one", () => {
  type Pair = { local: Triage; view: TodoSummary; facts?: RowFacts };
  const cases: { why: ByDesign; is: Pair; not: Pair[] }[] = [
    // row 1: the web files a failed start in Done after 48 h of no activity; the hub keeps it open, To do 48 h after the
    // failure; filed there minutes after a failure, it is the hub's error
    {
      why: "failed_start",
      is: { local: local("done", "failed_start"), view: view("todo", "rest", inactive), facts: facts({ failedLong: true }) },
      not: [
        { local: local("done", "closed"), view: view("todo", "rest"), facts: facts({ failedLong: true }) },
        { local: local("waiting", "failed_start"), view: view("todo", "rest") },
        { local: local("done", "failed_start"), view: view("needs_you", "failed_start", { phase: "needs_you" }), facts: facts({ failedLong: true }) },
      ],
    },
    // ... edited since the failure, so the web still asks; put off to Someday; an open concern the web keeps open
    { why: "failed_start", is: { local: local("waiting", "failed_start"), view: view("todo", "rest"), facts: facts({ failedLong: true }) }, not: [] },
    { why: "failed_start", is: { local: local("done", "failed_start"), view: view("someday", "deferred", { phase: "someday" }), facts: facts({ failedLong: true }) }, not: [] },
    { why: "failed_start", is: { local: local("other", "rest"), view: view("todo", "rest", inactive), facts: facts({ failedLong: true }) }, not: [] },
    // 3.4: the row carries its step's state when that is ahead of its own; never a step behind what its own run does,
    // and a step's Can wait never over work that runs
    {
      why: "steps",
      is: { local: local("shelved", "stale"), view: view("needs_you", "owner_decides", step("decision")) },
      not: [
        { local: local("shelved", "stale"), view: view("needs_you", "owner_decides", { line: { key: "decision" } }) },
        { local: local("waiting", "agent_asks"), view: view("working", "agent_running", step("working")) },
        { local: local("working", "agent_running"), view: view("can_wait", "awaits_reply", step("awaits_reply")) },
        { local: local("other", "rest"), view: view("just_finished", "finished_unseen", step("finished")) },
      ],
    },
    { why: "steps", is: { local: local("other", "rest"), view: view("can_wait", "can_wait", step("decision")) }, not: [] },
    // D11: a run that waits on another Agent is Waiting (or Just finished, row 9 first), never To do or Needs you
    {
      why: "run_waits",
      is: { local: local("working", "agent_running"), view: view("waiting", "someone_else"), facts: facts({ waits: true }) },
      not: [
        { local: local("working", "agent_running"), view: view("waiting", "someone_else") },
        { local: local("working", "agent_running"), view: view("todo", "rest"), facts: facts({ waits: true }) },
        { local: local("working", "agent_running"), view: view("needs_you", "agent_asks"), facts: facts({ waits: true }) },
      ],
    },
    { why: "run_waits", is: { local: local("working", "agent_running"), view: view("just_finished", "finished_unseen"), facts: facts({ waits: true }) }, not: [] },
    // D11: a model failure the hub retries is Working in the background, not cut off
    {
      why: "run_waits",
      is: { local: local("waiting", "agent_asks"), view: view("working", "background"), facts: facts({ transient: true }) },
      not: [
        { local: local("waiting", "agent_asks"), view: view("working", "background") },
        { local: local("waiting", "agent_asks"), view: view("working", "agent_running"), facts: facts({ transient: true }) },
      ],
    },
    // row 10 and the web's one usage-limit status: the hub places the to-do by its other runs, or waits for the reset
    {
      why: "usage_limit",
      is: { local: local("other", "rest"), view: view("waiting", "usage_limit"), facts: facts({ usageLimit: true }) },
      not: [
        { local: local("other", "rest"), view: view("waiting", "usage_limit") },
        { local: local("other", "rest"), view: view("needs_you", "agent_asks"), facts: facts({ usageLimit: true }) },
        { local: local("other", "rest"), view: view("waiting", "pr_open"), facts: facts({ usageLimit: true }) },
      ],
    },
    { why: "usage_limit", is: { local: local("other", "rest"), view: view("working", "agent_running"), facts: facts({ usageLimit: true }) }, not: [] },
    { why: "usage_limit", is: { local: local("other", "rest"), view: view("working", "starting"), facts: facts({ usageLimit: true }) }, not: [] },
    { why: "usage_limit", is: { local: local("shelved", "stale"), view: view("just_finished", "finished_unseen"), facts: facts({ usageLimit: true }) }, not: [] },
    // row 4: a run that ended on a question in its text can wait, ahead of work, a park and an open PR (rows 5 to 14),
    // never ahead of what asks or closed work
    {
      why: "awaits_reply",
      is: { local: local("just_done", "finished_unseen"), view: view("can_wait", "awaits_reply"), facts: facts({ awaitsReply: true }) },
      not: [
        { local: local("just_done", "finished_unseen"), view: view("can_wait", "can_wait"), facts: facts({ awaitsReply: true }) },
        { local: local("just_done", "finished_unseen"), view: view("can_wait", "awaits_reply") },
        { local: local("done", "closed"), view: view("can_wait", "awaits_reply"), facts: facts({ awaitsReply: true }) },
      ],
    },
    { why: "awaits_reply", is: { local: local("working", "agent_running"), view: view("can_wait", "awaits_reply"), facts: facts({ awaitsReply: true }) }, not: [] },
    { why: "awaits_reply", is: { local: local("parked", "pr_open"), view: view("can_wait", "awaits_reply"), facts: facts({ awaitsReply: true }) }, not: [] },
    { why: "awaits_reply", is: { local: local("parked", "parked"), view: view("can_wait", "awaits_reply"), facts: facts({ awaitsReply: true }) }, not: [] },
    {
      why: "stopped_unfinished",
      is: { local: local("just_done", "finished_unseen"), view: view("needs_you", "stopped_unfinished") },
      not: [
        { local: local("just_done", "finished_unseen"), view: view("needs_you", "agent_asks") },
        { local: local("just_done", "closed"), view: view("needs_you", "stopped_unfinished") },
        { local: local("working", "agent_running"), view: view("needs_you", "stopped_unfinished") },
        { local: local("just_done", "finished_unseen"), view: view("todo", "stopped_unfinished") },
      ],
    },
    // row 9: accepted work nobody has seen stays Just finished
    {
      why: "finished_unseen",
      is: { local: local("done", "accepted"), view: view("just_finished", "finished_unseen") },
      not: [{ local: local("done", "closed"), view: view("just_finished", "finished_unseen") }],
    },
    // row 9: 24 h in Just finished after it was seen, ahead of an open PR; a review not done yet with an open PR, which
    // the web reads as no wait at all
    {
      why: "finished_recent",
      is: { local: local("done", "seen"), view: view("just_finished", "finished_recent") },
      not: [
        { local: local("done", "closed"), view: view("just_finished", "finished_recent") },
        { local: local("other", "rest"), view: view("just_finished", "finished_recent") },
      ],
    },
    { why: "finished_recent", is: { local: local("parked", "pr_open"), view: view("just_finished", "finished_recent") }, not: [] },
    { why: "finished_recent", is: { local: local("other", "rest"), view: view("just_finished", "finished_recent"), facts: facts({ prOpen: true }) }, not: [] },
    // row 11, rule 9a: seen over a day ago and open is a To do; an open PR comes first
    {
      why: "finished_seen",
      is: { local: local("done", "seen"), view: view("todo", "finished_seen") },
      not: [
        { local: local("done", "closed"), view: view("todo", "finished_seen") },
        { local: local("parked", "pr_open"), view: view("todo", "finished_seen") },
      ],
    },
    // row 12: Someday, which the web may shelve
    {
      why: "someday",
      is: { local: local("shelved", "stale"), view: view("someday", "deferred") },
      not: [{ local: local("just_done", "finished_unseen"), view: view("someday", "deferred") }],
    },
    // row 13: the hub reads activity and who may shelve as the web does, so the Inactive mark differs only where it knows
    // otherwise; a task idle for days without it, or an errand with it, is the hub's error
    {
      why: "inactive",
      is: { local: local("shelved", "stale"), view: view("todo", "rest"), facts: facts({ due: true }) },
      not: [
        { local: local("shelved", "stale"), view: view("todo", "rest") },
        { local: local("other", "rest"), view: view("todo", "rest", inactive) },
        { local: local("working", "agent_running"), view: view("todo", "rest"), facts: facts({ due: true }) },
      ],
    },
    { why: "inactive", is: { local: local("other", "rest"), view: view("todo", "rest", inactive), facts: facts({ restoredHere: true }) }, not: [] },
    { why: "inactive", is: { local: local("shelved", "stale"), view: view("todo", "rest"), facts: facts({ missing: true }) }, not: [] },
    // rows 1, 2: a supervised failed start and a card its supervisor holds are Persona's work, not the owner's; any other
    // Working, or a decision only the owner can make, is not this difference
    {
      why: "supervisor_holds",
      is: { local: local("waiting", "agent_asks"), view: view("working", "persona_working") },
      not: [
        { local: local("waiting", "agent_asks"), view: view("working", "agent_running") },
        { local: local("waiting", "owner_decides"), view: view("working", "persona_working") },
      ],
    },
    { why: "supervisor_holds", is: { local: local("waiting", "failed_start"), view: view("working", "persona_working") }, not: [] },
  ];

  for (const c of cases) {
    it(`${c.why}: ${c.is.local.section}/${c.is.local.rule} → ${c.is.view.section}/${c.is.view.reason}`, () => {
      expect(classify(c.is.local, c.is.view, c.is.facts ?? facts(), NOW)).toEqual({ kind: "by_design", why: c.why });
      for (const n of c.not) expect(classify(n.local, n.view, n.facts ?? facts(), NOW), `${n.local.section}/${n.local.rule} → ${n.view.section}/${n.view.reason}`).toEqual({ kind: "mismatch" });
    });
  }

  it("covers every entry of the table, each with a look-alike", () => {
    expect([...new Set(cases.map((c) => c.why))].sort()).toEqual(BY_DESIGN.map((d) => d.why).sort());
    expect([...new Set(cases.filter((c) => c.not.length).map((c) => c.why))].sort()).toEqual(BY_DESIGN.map((d) => d.why).sort());
  });

  it("reads Parked in Waiting and Can wait under Needs you as the same", () => {
    expect(classify(local("parked", "parked"), view("waiting", "parked"), facts(), NOW)).toEqual({ kind: "same" });
    expect(classify(local("parked", "pr_open"), view("waiting", "pr_open"), facts(), NOW)).toEqual({ kind: "same" });
    expect(classify(local("waiting", "owner_decides"), view("can_wait", "can_wait"), facts(), NOW)).toEqual({ kind: "same" });
  });
});

describe("what explains a difference outside the table", () => {
  const working = view("working", "agent_running", { next_change_at: new Date(NOW + 10 * HOUR).toISOString() });

  it("a run the web has no session of", () => {
    expect(classify(local("other", "rest"), working, facts({ missing: true }), NOW)).toEqual({ kind: "client_gap" });
  });

  it("a view that time alone changes within two minutes, or already has", () => {
    const at = (ms: number) => view("working", "agent_running", { next_change_at: new Date(NOW + ms).toISOString() });
    expect(classify(local("other", "rest"), at(BOUNDARY_MS - 1000), facts(), NOW)).toEqual({ kind: "boundary" });
    expect(classify(local("other", "rest"), at(-HOUR), facts(), NOW)).toEqual({ kind: "boundary" });
    expect(classify(local("other", "rest"), at(BOUNDARY_MS + 1000), facts(), NOW)).toEqual({ kind: "mismatch" });
    expect(classify(local("other", "rest"), working, facts(), NOW)).toEqual({ kind: "mismatch" });
  });
});

describe("rowFacts", () => {
  const todo = (sessionIds: string[], more: Partial<Todo> = {}): Todo => ({
    id: "todo_r1",
    title: "Compare the hotel prices",
    created_at: "2026-11-10T08:00:00Z",
    updated_at: "2026-11-10T08:00:00Z",
    agents: sessionIds.map((sid) => ({ machine_id: "m1", session_id: sid, linked_at: "2026-11-10T08:00:00Z" })),
    ...more,
  });
  const session = (sid: string, more: Partial<Session>): Session => ({ machine_id: "m1", session_id: sid, state: "idle", status: "idle", ...more }) as Session;

  it("reads each linked run's session as the hub reads it", () => {
    const lookup = makeSessionLookup([
      session("s-wait", { work: { phase: "blocked", on: "agent" } }),
      session("s-retry", { work: { phase: "blocked", on: "transient", wake_at: "2026-11-10T12:00:10Z" } }),
      session("s-limit", { turn_error: "limit", turn_error_kind: "usage_limit", turn_error_resets_at: "2026-11-10T13:00:00Z" }),
      session("s-reply", { state: "unread", awaits_reply: true }),
    ]);
    expect(rowFacts(todo(["s-wait", "s-retry", "s-limit", "s-reply"]), lookup, NOW)).toEqual(facts({ usageLimit: true, waits: true, transient: true, awaitsReply: true }));
    expect(rowFacts(todo(["s-gone"]), lookup, NOW)).toEqual(facts({ missing: true }));
  });

  it("counts as a wait only another Agent, or an outside event the run names (the hub's waitsOn)", () => {
    const waits = (work: Session["work"], more: Partial<Session> = {}) => rowFacts(todo(["s"]), makeSessionLookup([session("s", { work, ...more })]), NOW).waits;
    expect(waits({ phase: "blocked", on: "external", wait: "the deploy" })).toBe(true);
    expect(waits({ phase: "blocked", on: "external", agent: { machine_id: "m1", session_id: "s2" } })).toBe(true);
    // background commands or a /loop between its ticks, and a usage limit's own wait
    expect(waits({ phase: "blocked", on: "external" })).toBe(false);
    expect(waits({ phase: "blocked", on: "external", wait: "the reset", resets_at: "2026-11-10T13:00:00Z" })).toBe(false);
    // a turn not over yet, or on a card
    expect(waits({ phase: "blocked", on: "agent" }, { state: "working", status: "working" })).toBe(false);
    expect(waits({ phase: "blocked", on: "agent" }, { state: "needs_you" })).toBe(false);
  });

  it("reads a retry only while the hub's Continue is on its way", () => {
    const transient = (wake: string | undefined) => rowFacts(todo(["s"]), makeSessionLookup([session("s", { work: { phase: "blocked", on: "transient", wake_at: wake } })]), NOW).transient;
    expect(transient("2026-11-10T11:56:00Z")).toBe(true);
    expect(transient("2026-11-10T11:55:00Z")).toBe(false);
    expect(transient(undefined)).toBe(false);
  });

  it("leaves out released runs and a run that waits on the owner", () => {
    const lookup = makeSessionLookup([session("s-ask", { state: "needs_you", work: { phase: "blocked", on: "agent" } })]);
    const released = { ...todo(["s-gone"]), agents: [{ machine_id: "m1", session_id: "s-gone", linked_at: "2026-11-10T08:00:00Z", released_at: "2026-11-10T09:00:00Z" }] };
    expect(rowFacts(released, lookup, NOW).missing).toBe(false);
    expect(rowFacts(todo(["s-ask"]), lookup, NOW).waits).toBe(false);
  });

  it("reads the row's own facts", () => {
    const failed = (done_at: string, updated_at = done_at) => rowFacts(todo([], { start_error: "Desk Mac is offline", done_at, updated_at }), undefined, NOW).failedLong;
    expect(failed("2026-11-08T12:00:00Z")).toBe(true);
    // the 48 h run from the failure, not from an edit since
    expect(failed("2026-11-08T12:00:01Z", "2026-11-10T11:00:00Z")).toBe(false);
    expect(failed("2026-11-08T11:00:00Z", "2026-11-10T11:00:00Z")).toBe(true);
    expect(rowFacts(todo([], { done_at: "2026-11-01T12:00:00Z" }), undefined, NOW).failedLong).toBe(false);
    expect(rowFacts(todo([], { due_date: "2026-11-09" }), undefined, NOW).due).toBe(true);
    expect(rowFacts(todo([], { restored_at: "2026-11-10T09:00:00Z" }), undefined, NOW, { restored: NOW - HOUR, prOpen: true })).toMatchObject({ restoredHere: true, prOpen: true });
    expect(rowFacts(todo([], { restored_at: "2026-11-10T11:30:00Z" }), undefined, NOW, { restored: NOW - HOUR }).restoredHere).toBe(false);
  });
});

describe("restoredAt", () => {
  const todo = (more: Partial<Todo>): Todo => ({ id: "todo_x1", title: "Renew the library card", created_at: "2026-10-01T08:00:00Z", updated_at: "2026-10-01T08:00:00Z", ...more });

  it("takes the latest of this browser's restore, the hub's and a Someday that has passed", () => {
    expect(restoredAt(todo({}), 0, NOW)).toBe(0);
    expect(restoredAt(todo({ restored_at: "2026-11-09T08:00:00Z" }), NOW - 2 * HOUR, NOW)).toBe(NOW - 2 * HOUR);
    expect(restoredAt(todo({ restored_at: "2026-11-10T11:30:00Z" }), NOW - 2 * HOUR, NOW)).toBe(Date.parse("2026-11-10T11:30:00Z"));
    expect(restoredAt(todo({ someday_until: "2026-11-10T10:00:00Z" }), 0, NOW)).toBe(Date.parse("2026-11-10T10:00:00Z"));
    expect(restoredAt(todo({ someday_until: "2026-11-11T10:00:00Z" }), 0, NOW)).toBe(0);
    expect(restoredAt(todo({ someday_until: "9999-12-31T00:00:00Z" }), 0, NOW)).toBe(0);
  });
});
