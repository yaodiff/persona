// The hub's cases are the oracle for the shadow's mapping (#10226): for each case of spec/fixtures/todo-view, today's
// web triage of the same input against the view the hub must answer differs only where the redesign decided it.
import { afterEach, describe, expect, it, vi } from "vitest";
import fixture from "../../../../../spec/fixtures/todo-view/todo_view.json";
import type { TodoSummary } from "../../../lib/todo-view-wire";
import { openMergePRs, triage } from "../../../lib/todo-triage";
import { makeSessionLookup, type Todo } from "../../../lib/todos";
import type { Session } from "../../../lib/types";
import { classify, restoredAt, rowFacts, type ByDesign } from "./view-legacy";

interface Case {
  now: string;
  todo: Todo;
  sessions: Session[];
  restored_at?: string;
  someday_until?: string;
  loc?: string;
  superseded?: boolean;
}

// Every case the web places apart from the hub, and the decided difference that explains it; every other case reads the
// same. A new case lands here or in the same set, never as a mismatch.
const DECIDED: Record<string, ByDesign> = {
  failed_start_after_two_days: "failed_start",
  failed_start_edited_since: "failed_start",
  failed_start_due_ahead: "failed_start",
  awaits_reply: "awaits_reply",
  finished_seen_recently: "finished_recent",
  finished_seen_over_a_day: "finished_seen",
  accepted_reopened_unseen: "finished_unseen",
  usage_limit_until_reset: "usage_limit",
  usage_limit_before_run_waits: "usage_limit",
  finished_with_a_limited_run: "usage_limit",
  step_decision_rolls_up: "steps",
  steps_working_and_limited: "steps",
  retry_is_safe: "steps",
  run_waits_on_agent: "run_waits",
  run_waits_on_external: "run_waits",
  transient_retry_stays_background: "run_waits",
  stopped_unfinished_closeout_needs_followup: "stopped_unfinished",
  failed_start_supervised: "supervisor_holds",
  agent_asks_held_by_supervisor: "supervisor_holds",
  steps_with_superseded_child: "steps",
};

const zone = process.env.TZ;
afterEach(() => {
  vi.useRealTimers();
  if (zone === undefined) delete process.env.TZ;
  else process.env.TZ = zone;
});

/** The case as the shadow reads a live row: the hub's restore and Someday on the to-do, the page's clock and zone at
 *  the case's moment and in the owner's zone (the web reads both from the browser). */
function compare(input: Case) {
  process.env.TZ = input.loc ?? "UTC";
  const now = Date.parse(input.now);
  vi.useFakeTimers({ now, toFake: ["Date"] });
  const todo: Todo = { ...input.todo, restored_at: input.restored_at, someday_until: input.someday_until };
  const lookup = makeSessionLookup(input.sessions ?? []);
  const local = triage(todo, lookup, now, restoredAt(todo, 0, now), input.superseded ?? false);
  return { local, facts: rowFacts(todo, lookup, now, { prOpen: openMergePRs(todo, now).length > 0 }), now };
}

describe("the hub's view against today's web triage (spec/fixtures/todo-view)", () => {
  for (const c of fixture.cases) {
    it(c.name, () => {
      const { local, facts, now } = compare(c.input as unknown as Case);
      const got = classify(local, c.expected.view as TodoSummary, facts, now);
      const want = DECIDED[c.name];
      expect(got, `web ${local.section}/${local.rule}`).toEqual(want ? { kind: "by_design", why: want } : { kind: "same" });
    });
  }

  it("names only cases the fixture has", () => {
    const names = new Set(fixture.cases.map((c) => c.name));
    expect(Object.keys(DECIDED).filter((n) => !names.has(n))).toEqual([]);
  });
});
