// @vitest-environment jsdom
import { beforeEach, describe, expect, it } from "vitest";
import type { Session } from "./types";
import { makeSessionLookup, shownStatus, todoStatus, todoStatusLabel, type Todo } from "./todos";
import { checkCount, checklist, reviewBadge, type Supervision } from "./supervision";
import { FOLDED_SECTIONS, lastActivity, relatedKeys, SHELVE_AFTER_MS, supersededFailedStarts, todoBucket, TRIAGE_SECTIONS, triage, triageTodos, waitingForMerge } from "./todo-triage";
import { resetRestoredCache, restoreTodo } from "./todo-restore";

const NOW = Date.parse("2026-11-10T12:00:00Z");
const hoursAgo = (h: number) => new Date(NOW - h * 3600_000).toISOString();

const session = (sid: string, state: Session["state"], updatedHoursAgo = 1, patch: Partial<Session> = {}): Session =>
  ({ machine_id: "m", session_id: sid, state, status: state === "working" ? "working" : "idle", updated_at: hoursAgo(updatedHoursAgo), ...patch }) as Session;

const agent = (sid: string) => ({ machine_id: "m", session_id: sid, linked_at: hoursAgo(200) });

const todo = (id: string, patch: Partial<Todo> = {}): Todo => ({
  id,
  title: id,
  created_at: hoursAgo(300),
  updated_at: hoursAgo(1),
  done_at: null,
  agents: [],
  origin: "conv",
  ...patch,
});

/** An accepted task the hub reopened when its agent was sent more work: eight criteria, the review judged five. */
const acceptedReopened = (): Supervision => {
  const when = ["c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8"];
  return {
    supervisor: "conv",
    done_when: when,
    state: "accepted",
    round: 1,
    reviews: [{ at: hoursAgo(5), verdict: "accepted", checks: when.slice(0, 5).map((criterion) => ({ criterion, met: true })) }],
  };
};

describe("triage (docs/assistant/todo-status-sop.md, decision table)", () => {
  const lookup = makeSessionLookup([
    session("ask", "needs_you"),
    session("run", "working"),
    session("unseen", "unread"),
    session("idle", "idle", 2),
    session("old", "idle", 100),
  ]);
  const at = (td: Todo) => triage(td, lookup, NOW);

  it("an agent asking is Waiting on you, whatever else holds", () => {
    expect(at(todo("t", { agents: [agent("ask"), agent("run")] }))).toMatchObject({ section: "waiting", rule: "agent_asks" });
  });

  it("a decision for the owner is Waiting on you", () => {
    expect(at(todo("t", { agents: [agent("idle")], supervision: { supervisor: "c", state: "needs_user" } })).rule).toBe("owner_decides");
    expect(at(todo("t", { ask: { outcome: "decide", verdict: "v", asked_at: hoursAgo(1) } })).section).toBe("waiting");
  });

  it.each([false, true])("keeps a stopped parked task unfinished (seen: %s)", (seen) => {
    const td = todo("parked", {
      agents: [agent("unseen")],
      seen_at: seen ? hoursAgo(0) : undefined,
      supervision: {
        supervisor: "conv",
        state: "parked",
        parked: { reason: "Waiting for another team", waiting_on: "someone_else", until: hoursAgo(-12), count: 1 },
      },
    });
    expect(at(td)).toMatchObject({ section: "parked", rule: "parked" });
    expect(todoBucket(td, lookup, NOW)).toBe("open");
    const result = triageTodos([td], lookup, NOW);
    expect(result.todoCounts.parked).toBe(1);
    expect(result.open).toBe(1);
    expect(result.shelved).toBe(0);
    expect(result.finished).toBe(0);
    expect(result.sections.done).toEqual([]);
  });

  it("an expired park stays open for review rather than filing a seen turn as done", () => {
    const td = todo("expired", {
      agents: [agent("unseen")], seen_at: hoursAgo(0),
      supervision: { supervisor: "conv", state: "parked", parked: { reason: "Check again", waiting_on: "date", until: hoursAgo(1), count: 1 } },
    });
    expect(at(td)).toMatchObject({ section: "other", rule: "parked" });
    expect(todoBucket(td, lookup, NOW)).toBe("open");
  });

  it("real activity or an explicit close takes precedence over a parked review", () => {
    const supervision: Supervision = {
      supervisor: "conv", state: "parked",
      parked: { reason: "Waiting for another team", waiting_on: "someone_else", until: hoursAgo(-12), count: 1 },
    };
    expect(at(todo("running", { supervision, agents: [agent("run")] })).section).toBe("working");
    expect(at(todo("asking", { supervision, agents: [agent("ask")] })).section).toBe("waiting");
    expect(at(todo("closed", { supervision, done_at: hoursAgo(30) })).section).toBe("done");
    expect(at(todo("old", { supervision, updated_at: hoursAgo(100), seen_at: hoursAgo(0) })).section).toBe("parked");
  });

  it("a deployed to-do's Please verify, or a done conclusion, is Just finished: the supervisor accepts it, not the owner", () => {
    const idle = { agents: [agent("idle")] };
    expect(at(todo("t", { ...idle, ask: { verdict: "done, deployed: verify below", asked_at: hoursAgo(1) } }))).toMatchObject({ section: "just_done", rule: "finished_unseen" });
    expect(at(todo("t", { ...idle, ask: { outcome: "done", verdict: "done", asked_at: hoursAgo(1) } })).section).toBe("just_done");
  });

  it("an agent running is Working, even on an old to-do", () => {
    expect(at(todo("t", { updated_at: hoursAgo(400), agents: [agent("run")] }))).toMatchObject({ section: "working", rule: "agent_running" });
  });

  it("a closed to-do is Just finished for a day, then Done", () => {
    expect(at(todo("t", { done_at: hoursAgo(3) })).section).toBe("just_done");
    expect(at(todo("t", { done_at: hoursAgo(30), updated_at: hoursAgo(30) })).section).toBe("done");
  });

  it("an accepted task reopened by more work is finished, not Follow up", () => {
    const td = todo("t", { agents: [agent("idle")], reopened_at: hoursAgo(4), supervision: acceptedReopened() });
    expect(at(td)).toMatchObject({ section: "just_done", rule: "accepted" });
    expect(todoStatusLabel(td, todoStatus(td, lookup))).toBe("已验收");
    expect(reviewBadge(td)?.tone).toBe("green");
  });

  it("an accepted concern stays open in its status words and tone too", () => {
    const td = todo("concern", { kind: "concern", supervision: acceptedReopened() });
    const status = todoStatus(td, lookup);
    expect(at(td)).toMatchObject({ section: "other", rule: "rest" });
    expect(status).toBe("not_started");
    expect(shownStatus(td, status)).toBe("not_started");
    expect(todoStatusLabel(td, status)).toBe("未开始");
  });

  it("an accepted task whose agent runs again is Working", () => {
    expect(at(todo("t", { agents: [agent("run")], supervision: acceptedReopened() })).section).toBe("working");
  });

  it("a deployed delivery is finished", () => {
    expect(at(todo("t", { delivery: { state: "live", at: hoursAgo(2) } })).rule).toBe("deployed");
  });

  it("a finished turn nobody saw is Just finished", () => {
    expect(at(todo("t", { agents: [agent("unseen")] }))).toMatchObject({ section: "just_done", rule: "finished_unseen" });
  });

  it("a seen to-do goes to Done at once; one seen before it got there stays", () => {
    // closed, accepted, deployed: seen at or after that moment, it is Done long before the 24 hours
    expect(at(todo("t", { done_at: hoursAgo(3), seen_at: hoursAgo(2) }))).toMatchObject({ section: "done", rule: "closed" });
    expect(at(todo("t", { done_at: hoursAgo(3), seen_at: hoursAgo(3) })).section).toBe("done");
    expect(at(todo("t", { done_at: hoursAgo(3), seen_at: hoursAgo(4) }))).toMatchObject({ section: "just_done", rule: "closed" });
    expect(at(todo("t", { agents: [agent("idle")], supervision: acceptedReopened(), seen_at: hoursAgo(4) }))).toMatchObject({ section: "done", rule: "accepted" });
    expect(at(todo("t", { delivery: { state: "live", at: hoursAgo(2) }, seen_at: hoursAgo(1) }))).toMatchObject({ section: "done", rule: "deployed" });
    // a failed start superseded by its row's finished work, too
    expect(triage(todo("f", { start_error: "x", done_at: hoursAgo(2), seen_at: hoursAgo(1) }), lookup, NOW, 0, true)).toMatchObject({ section: "done", rule: "superseded" });
    // a mark that does not parse is no mark
    expect(at(todo("t", { done_at: hoursAgo(3), seen_at: "not a time" })).section).toBe("just_done");
  });

  it("a finished turn seen since it last moved is Done, unless its PR is open", () => {
    // the unseen session last moved an hour ago
    expect(at(todo("t", { agents: [agent("unseen")], seen_at: hoursAgo(0.5) }))).toMatchObject({ section: "done", rule: "seen" });
    expect(at(todo("t", { agents: [agent("unseen")], seen_at: hoursAgo(2) }))).toMatchObject({ section: "just_done", rule: "finished_unseen" });
    const url = "https://example.com/pull/1";
    expect(at(todo("t", { agents: [agent("unseen")], seen_at: hoursAgo(0.5), pr_url: url, stage: "review", pr_status: { url, state: "open", draft: false, review: "pending", checks: "none", conflicts: false, checked_at: new Date(NOW).toISOString() } }))).toMatchObject({ section: "pr", rule: "pr_open" });
    // seen, it is no longer open work: every count reads it the same way
    expect(todoBucket(todo("t", { agents: [agent("unseen")], seen_at: hoursAgo(0.5) }), lookup, NOW)).toBe("closed");
    expect(todoBucket(todo("t", { agents: [agent("unseen")] }), lookup, NOW)).toBe("open");
  });

  it("legacy PR hints do not claim waiting for merge without fresh PR facts", () => {
    const old = { updated_at: hoursAgo(300), agents: [agent("old")] };
    expect(at(todo("t", { ...old, delivery: { state: "pr" } })).section).toBe("shelved");
    expect(at(todo("t", { ...old, pr_url: "https://example.com/pull/1" })).section).toBe("shelved");
    expect(at(todo("t", { ...old, pr_url: "https://example.com/pull/1", delivery: { state: "merged" } })).section).toBe("shelved");
  });

  it("fresh open PR facts take precedence over stage and delivery hints", () => {
    const fresh = { updated_at: hoursAgo(1), agents: [agent("old")] };
    const url = "https://example.com/pull/1";
    const pr_status = { url, state: "open" as const, draft: false, review: "approved" as const, checks: "success" as const, conflicts: false, checked_at: new Date(NOW).toISOString() };
    for (const stage of ["merge", "deploy", "verify"]) {
      expect(at(todo("t", { ...fresh, pr_url: url, pr_status, stage })).rule).toBe("pr_open");
    }
    expect(at(todo("t", { ...fresh, pr_url: url, pr_status, delivery: { state: "live", at: hoursAgo(1) } })).rule).toBe("pr_open");
    for (const state of ["merged", "closed"] as const) {
      expect(at(todo("t", { ...fresh, pr_url: url, pr_status: { ...pr_status, state } })).rule).not.toBe("pr_open");
    }
  });

  it("puts a fresh matching open non-draft PR in its own open bucket ahead of parked hints", () => {
    const facts = { url: "https://example.com/pull/7", state: "open" as const, draft: false, review: "required" as const, checks: "pending" as const, conflicts: false, checked_at: new Date(NOW - 60_000).toISOString() };
    const td = todo("waiting-pr", { pr_url: `${facts.url}/`, pr_status: facts, delivery: { state: "pr" }, supervision: { supervisor: "c", state: "parked", parked: { reason: "Wait", waiting_on: "someone_else", until: hoursAgo(-4), count: 1 } } });
    expect(waitingForMerge(td, lookup, NOW)).toBe(true);
    expect(triage(td, lookup, NOW)).toMatchObject({ section: "pr", rule: "pr_open" });
    expect(triageTodos([td], lookup, NOW)).toMatchObject({ open: 1, todoCounts: { pr: 1 } });
  });

  it("does not infer waiting for merge from stale, unknown, mismatched, draft, merged or closed facts", () => {
    const facts = { url: "https://example.com/pull/7", state: "open" as const, draft: false, review: "approved" as const, checks: "success" as const, conflicts: false, checked_at: new Date(NOW - 16 * 60_000).toISOString() };
    expect(triage(todo("legacy", { pr_url: facts.url, delivery: { state: "pr" } }), lookup, NOW).section).not.toBe("pr");
    expect(waitingForMerge(todo("stale", { pr_url: facts.url, pr_status: facts }), lookup, NOW)).toBe(false);
    expect(waitingForMerge(todo("mismatch", { pr_url: facts.url, pr_status: { ...facts, url: "https://example.com/pull/8", checked_at: new Date(NOW).toISOString() } }), lookup, NOW)).toBe(false);
    for (const patch of [{ draft: true }, { state: "merged" as const }, { state: "closed" as const }]) {
      const td = todo("terminal", { pr_url: facts.url, delivery: { state: "pr" }, pr_status: { ...facts, checked_at: new Date(NOW).toISOString(), ...patch } });
      expect(waitingForMerge(td, lookup, NOW)).toBe(false);
      expect(triage(td, lookup, NOW).section).not.toBe("pr");
    }
  });

  it("keeps genuine owner decisions and active agents ahead of a fresh PR", () => {
    const pr_status = { url: "https://example.com/pull/7", state: "open" as const, draft: false, review: "required" as const, checks: "none" as const, conflicts: false, checked_at: new Date(NOW).toISOString() };
    expect(triage(todo("decision", { pr_url: pr_status.url, pr_status, supervision: { supervisor: "c", state: "needs_user" } }), lookup, NOW).section).toBe("waiting");
    expect(triage(todo("active", { pr_url: pr_status.url, pr_status, agents: [agent("run")] }), lookup, NOW).section).toBe("working");
  });

  it("a task with no move for 48 hours is Shelved; a newer move of its agent keeps it", () => {
    expect(at(todo("t", { updated_at: hoursAgo(100), agents: [agent("old")] }))).toMatchObject({ section: "shelved", rule: "stale" });
    expect(at(todo("t", { updated_at: hoursAgo(100), agents: [agent("idle")] })).section).toBe("other");
  });

  it("the user's own to-do without agents never shelves itself, nor one due ahead", () => {
    expect(at(todo("t", { updated_at: hoursAgo(100), origin: undefined })).section).toBe("other");
    expect(at(todo("t", { updated_at: hoursAgo(100), agents: [agent("old")], due_date: "2026-12-01" })).section).toBe("other");
  });

  it("a restore counts as a move", () => {
    const td = todo("t", { updated_at: hoursAgo(100), agents: [agent("old")] });
    expect(triage(td, lookup, NOW, NOW - 3600_000).section).toBe("other");
    expect(triage(td, lookup, NOW, NOW - SHELVE_AFTER_MS - 1).section).toBe("shelved");
  });

  it("last activity takes the latest of the to-do, its supervision and its agents", () => {
    const td = todo("t", { updated_at: hoursAgo(50), agents: [agent("idle")], supervision: { supervisor: "c", progress: { kind: "progress", text: "x", at: hoursAgo(1) } } });
    expect(lastActivity(td, lookup)).toBe(NOW - 3600_000);
  });
});

describe("accepted criteria (#3156)", () => {
  it("criteria added after a standing acceptance are unchecked, not open, and not counted", () => {
    const sup = acceptedReopened();
    expect(checklist(sup).map((c) => c.mark)).toEqual(["met", "met", "met", "met", "met", "unchecked", "unchecked", "unchecked"]);
    expect(checkCount(sup)).toBe("5/5");
  });

  it("before any acceptance they stay open and count", () => {
    const sup = { ...acceptedReopened(), state: "rework" as const, reviews: [{ at: hoursAgo(5), verdict: "rework" as const, checks: [{ criterion: "c1", met: true }] }] };
    expect(checklist(sup).filter((c) => c.mark === "open")).toHaveLength(7);
    expect(checkCount(sup)).toBe("1/8");
  });
});

describe("triageTodos", () => {
  const lookup = makeSessionLookup([session("run", "working"), session("old", "idle", 100), session("old2", "idle", 120)]);

  it("merges the same issue, PR, title or plan into one row, led by its most urgent member", () => {
    const prStatus = { url: "https://example.com/pull/9", state: "open" as const, draft: false, review: "pending" as const, checks: "none" as const, conflicts: false, checked_at: new Date(NOW).toISOString() };
    const list = [
      todo("fix", { title: "Fix #412 lost pushes", agents: [agent("run")] }),
      todo("test", { title: "#412 lost pushes: regression test", updated_at: hoursAgo(100), agents: [agent("old")] }),
      todo("pr-a", { title: "A", pr_url: "https://example.com/pull/9", pr_status: prStatus, agents: [agent("old2")], updated_at: hoursAgo(120) }),
      todo("pr-b", { title: "B", pr_url: "https://example.com/pull/9/", pr_status: { ...prStatus, url: "https://example.com/pull/9/" }, agents: [agent("old2")], updated_at: hoursAgo(121) }),
      todo("lone", { title: "Something else", updated_at: hoursAgo(1) }),
    ];
    const r = triageTodos(list, lookup, NOW);
    expect(r.sections.working.map((g) => [g.lead.id, g.members.map((m) => m.id)])).toEqual([["fix", ["fix", "test"]]]);
    expect(r.sections.pr.map((g) => g.members.map((m) => m.id))).toEqual([["pr-a", "pr-b"]]);
    expect(r.sections.other.map((g) => g.lead.id)).toEqual(["lone"]);
  });

  it("lists the unfinished sections open above Just finished, Shelved and Done folded with Done last (#5707)", () => {
    expect(TRIAGE_SECTIONS).toEqual(["waiting", "working", "pr", "parked", "other", "just_done", "shelved", "done"]);
    expect([...FOLDED_SECTIONS]).toEqual(["shelved", "done"]);
  });

  it("unfinished to-dos lead a row and come before Just finished; seen ones are Done", () => {
    const look = makeSessionLookup([session("unseen", "unread"), session("unseen2", "unread"), session("idle", "idle", 2)]);
    const list = [
      todo("fix", { title: "Fix #512 crash on launch", agents: [agent("unseen")] }),
      todo("test", { title: "#512 crash: add a regression test", updated_at: hoursAgo(2), agents: [agent("idle")] }),
      todo("notes", { title: "Write the release notes", updated_at: hoursAgo(3), done_at: hoursAgo(3), seen_at: hoursAgo(2.5) }),
      todo("speed", { title: "Speed up the web page", agents: [agent("unseen2")], seen_at: hoursAgo(0.5) }),
      todo("logs", { title: "Rotate the logs", updated_at: hoursAgo(3), done_at: hoursAgo(3) }),
    ];
    const r = triageTodos(list, look, NOW);
    expect(r.sections.other.map((g) => g.members.map((m) => m.id))).toEqual([["test", "fix"]]);
    expect(r.sections.just_done.map((g) => g.lead.id)).toEqual(["logs"]);
    expect(r.sections.done.map((g) => g.lead.id)).toEqual(["speed", "notes"]);
    expect([r.open, r.shelved, r.finished]).toEqual([2, 0, 3]);
  });

  it("never merges a closed to-do into an open one's row", () => {
    const r = triageTodos([todo("a", { title: "Same title" }), todo("b", { title: "Same title", done_at: hoursAgo(1) })], lookup, NOW);
    expect(r.sections.other).toHaveLength(1);
    expect(r.sections.just_done).toHaveLength(1);
  });

  it("counts a failed start waiting on the owner as open, though the hub set done_at", () => {
    const r = triageTodos([todo("f", { start_error: "no launcher", done_at: hoursAgo(1) })], lookup, NOW);
    expect(r.sections.waiting.map((g) => g.lead.id)).toEqual(["f"]);
    expect(r.open).toBe(1);
  });

  it("moves a failed start out of Waiting on you once its row's work really finished (#3713)", () => {
    const failed = todo("f", { title: "Deploy the panel", plan_id: "p", start_error: "no launcher", done_at: hoursAgo(1) });
    const done = todo("d", { title: "Verify the panel", plan_id: "p", done_at: hoursAgo(2), done_by: "review" });
    const other = todo("g", { title: "Rotate keys", start_error: "no launcher", done_at: hoursAgo(3) });
    const twin = todo("h", { title: "Rotate keys", start_error: "no launcher", done_at: hoursAgo(4) });
    const list = [failed, done, other, twin];
    // another failed start is not finished work: those two still wait on the owner
    expect([...supersededFailedStarts(list)]).toEqual(["f"]);
    const r = triageTodos(list, lookup, NOW);
    expect(r.sections.just_done.map((g) => g.members.map((m) => m.id))).toEqual([["d", "f"]]);
    expect(r.sections.waiting.map((g) => g.members.map((m) => m.id))).toEqual([["g", "h"]]);
    expect(r.open).toBe(2);
    expect(r.sections.just_done[0].triage.get("f")?.rule).toBe("superseded");
    // every count of open work reads it the same way (Progress, #3331)
    expect(todoBucket(failed, lookup, NOW, 0, true)).toBe("closed");
    expect(todoBucket(failed, lookup, NOW)).toBe("open");
    // like the work it went with: Just finished for 24 hours, then Done
    expect(triage(failed, lookup, NOW + 30 * 3600_000, 0, true).section).toBe("done");
  });

  it("lists quiet tasks on the page, leaves them out for the sidebar", () => {
    const list = [todo("q", { quiet: true })];
    expect(triageTodos(list, lookup, NOW).open).toBe(1);
    expect(triageTodos(list, lookup, NOW, new Map(), { quiet: false }).open).toBe(0);
  });

  it("counts shelved and finished apart from open, and leaves quiet tasks out", () => {
    const r = triageTodos(
      [
        todo("run", { agents: [agent("run")] }),
        todo("stale", { updated_at: hoursAgo(100), agents: [agent("old")] }),
        todo("closed", { done_at: hoursAgo(2) }),
        todo("accepted", { supervision: acceptedReopened(), agents: [agent("old")] }),
        todo("quiet", { quiet: true }),
      ],
      lookup,
      NOW,
      new Map(),
      { quiet: false },
    );
    expect([r.open, r.shelved, r.finished]).toEqual([1, 1, 2]);
  });

  it("related keys ignore case, spaces, punctuation and a PR link's trailing slash", () => {
    expect(relatedKeys(todo("a", { title: "Fix: the  Widget!", pr_url: "https://Example.com/pull/3/" }))).toEqual(["pr:https://example.com/pull/3", "title:fixthewidget"]);
  });
});

describe("restoreTodo", () => {
  beforeEach(() => {
    localStorage.clear();
    resetRestoredCache();
  });

  it("keeps the restore in this browser, dropping ones that would have shelved again", () => {
    restoreTodo("old", NOW - SHELVE_AFTER_MS - 1);
    restoreTodo("new", NOW);
    expect(JSON.parse(localStorage.getItem("persona.todos.restored")!)).toEqual({ new: NOW });
  });
});
