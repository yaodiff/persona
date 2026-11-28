// @vitest-environment jsdom
// What a waiting to-do waits on (#11090, lib/todo-wait-reason.ts): "Waiting on you" in the attention color only when it
// waits on its owner by the sidebar badge's predicate (#11085), so the label and the badge agree; a neutral word for
// each other wait, read from the fields the to-do and its Agents already carry; and "Paused (nothing needed from you)",
// never the attention color, when nothing says. The data is made up.
import { beforeAll, describe, expect, it } from "vitest";
import { initI18n } from "../i18n";
import type { Session, WorkState } from "./types";
import { makeSessionLookup, TODO_STATUS_INFO, TODO_TONE_CLASS, todoStatus, type Todo } from "./todos";
import { ownerBadge, triage } from "./todo-triage";
import { statusWord, todoLook, waitOf, waitReason, waitTone, type WaitReason, type LegacyWaitReason } from "./todo-wait-reason";
import { agentRowLabel, agentRowTone } from "./task-row-status";
import { stateName, waitName } from "./state-labels";
import { todoEntry } from "./todo-calendar";

const NOW = Date.parse("2026-11-10T12:00:00Z");
const minutesAgo = (m: number) => new Date(NOW - m * 60_000).toISOString();
const soon = new Date(NOW + 10 * 60_000).toISOString();

const session = (sid: string, patch: Partial<Session> = {}): Session =>
  ({ machine_id: "m", session_id: sid, state: "idle", status: "idle", updated_at: minutesAgo(5), ...patch }) as Session;
const blocked = (sid: string, work: Omit<WorkState, "phase">): Session => session(sid, { work: { phase: "blocked", ...work } });

const sessions = {
  ci: blocked("ci", { on: "external", line: "Wait for CI", wait: "CI on main", wake_at: soon }),
  other: blocked("other", { on: "agent", line: "Wait for the other Agent to merge", wait: "the other Agent's merge" }),
  named: blocked("named", { on: "external", line: "Rebase after it lands", agent: { machine_id: "m", session_id: "ci" } }),
  retry: blocked("retry", { on: "transient", line: "The model service failed for a moment", wake_at: soon }),
  // its turn ended while its background commands run: they report back (agentWaits)
  bg: session("bg", { state: "working", status: "idle", background_working: true }),
  // an external wait that names nothing and has no wake: one that ends by itself (a /loop, lib/work.ts hubWaits)
  loop: blocked("loop", { on: "external", line: "Scheduled loop, next at 16:20" }),
  // the hub checks back on it without naming what it waits for
  unnamed: blocked("unnamed", { on: "external", line: "", wake_at: soon }),
  run: session("run", { state: "working", status: "working" }),
};
const lookup = makeSessionLookup(Object.values(sessions));
const on = (sid: keyof typeof sessions) => ({ machine_id: "m", session_id: sid, linked_at: minutesAgo(30) });
const todo = (id: string, sid: keyof typeof sessions, patch: Partial<Todo> = {}): Todo =>
  ({ id, title: `Task ${id}`, created_at: minutesAgo(60), updated_at: minutesAgo(5), done_at: null, origin: "conv", agents: [on(sid)], ...patch });
const reason = (td: Todo) => waitReason(td, lookup, undefined, NOW);
const tone = (r: WaitReason) => TODO_TONE_CLASS[todoLook({} as Todo, "waiting", r).tone];

beforeAll(() => initI18n("zh-Hans"));

describe("a waiting to-do's reason (#11090)", () => {
  it("uses a projected kind even when local session and delivery data disagree", () => {
    const td = todo("wait", "bg", { wait: { kind: "usage_limit" }, delivery: { state: "pr", unblock: "user" } });
    expect(reason(td)).toBe("usage_limit");
    expect(waitOf(td, "not_started", undefined)).toBe("usage_limit");
    expect(todoLook(td, "not_started")).toEqual({ label: waitName("usage_limit"), tone: "idle" });
  });

  it("every case here is a Waiting to-do", () => {
    for (const sid of Object.keys(sessions).filter((k) => k !== "run")) expect(todoStatus(todo("t", sid as keyof typeof sessions), lookup), sid).toBe("waiting");
  });

  it("waits on the owner: a decision only they can make, or work held for their OK", () => {
    const decide = todo("decide", "ci", { ask: { outcome: "decide", verdict: "Pick a cover", asked_at: minutesAgo(3) } });
    const held = todo("held", "ci", { delivery: { state: "merged", gate: "hub update", unblock: "user" } });
    const review = todo("review", "other", { supervision: { supervisor: "conv", state: "needs_user" } });
    for (const td of [decide, held, review]) expect(reason(td), td.id).toBe("owner");
  });

  it("names what its Agent waits on: background work, the hub's retry, another Agent", () => {
    expect(reason(todo("bg", "bg"))).toBe("background");
    expect(reason(todo("loop", "loop"))).toBe("background");
    expect(reason(todo("retry", "retry"))).toBe("retry");
    expect(reason(todo("other", "other"))).toBe("prerequisite");
    expect(reason(todo("named", "named"))).toBe("prerequisite");
  });

  it("an outside wait its Agent names: its prerequisites, a merge or deploy, else outside Persona", () => {
    expect(reason(todo("ci", "ci"))).toBe("external");
    expect(reason(todo("pre", "ci", { prerequisite_blockers: ["t1", "t2"] }))).toBe("prerequisite");
    // finished work held short of live by others, an open PR of it, a park on a merge review, an undelivered check
    expect(reason(todo("pr", "ci", { delivery: { state: "pr", gate: "CI", unblock: "other" } }))).toBe("delivery");
    expect(reason(todo("merged", "ci", { delivery: { state: "merged", gate: "deploy", unblock: "agent" } }))).toBe("delivery");
    const facts = { url: "https://example.com/o/r/pull/1", state: "open" as const, draft: false, review: "approved" as const, checks: "pending" as const, conflicts: false, checked_at: minutesAgo(1) };
    expect(reason(todo("open", "ci", { pr_url: facts.url, pr_status: facts }))).toBe("delivery");
    // stale PR facts say nothing
    expect(reason(todo("stale", "ci", { pr_url: facts.url, pr_status: { ...facts, checked_at: minutesAgo(60) } }))).toBe("external");
    expect(reason(todo("undelivered", "ci", { check: { verdict: "undelivered", at: minutesAgo(2) } }))).toBe("delivery");
  });

  it("a wait nothing names reads the to-do's facts: its park, its check", () => {
    const park = (evidence: string | undefined, waiting_on: string) => todo(`park-${evidence}-${waiting_on}`, "unnamed", {
      supervision: { supervisor: "conv", state: "parked", parked: { reason: "r", until: soon, waiting_on, count: 1, ...(evidence && { evidence: { kind: evidence, target: "x", wake: "y" } }) } as never },
    });
    expect(reason(park("merge_review", "someone_else"))).toBe("delivery");
    expect(reason(park("person_reply", "someone_else"))).toBe("external");
    expect(reason(park("external_event", "external_event"))).toBe("external");
    expect(reason(park("date", "date"))).toBe("external");
    // an older park without evidence, by what it waits on
    expect(reason(park(undefined, "someone_else"))).toBe("external");
    // a legacy owner_priority park is the hub's to turn into a decision: not the owner's here
    expect(reason(park(undefined, "owner_priority"))).toBe("unknown");
    expect(reason(todo("check", "unnamed", { check: { verdict: "waiting", at: minutesAgo(2) } }))).toBe("external");
    // a check from before its latest reopen says nothing
    expect(reason(todo("reopened", "unnamed", { check: { verdict: "waiting", at: minutesAgo(20) }, reopened_at: minutesAgo(10) }))).toBe("unknown");
  });

  it("nothing says: unknown, neutral", () => {
    expect(reason(todo("unnamed", "unnamed"))).toBe("unknown");
    // its Agent's session is not at hand (a tray card the hub said waits): unknown, never the owner's
    expect(todoLook(todo("gone", "ci"), "waiting")).toEqual({ label: "暂停中（不需要你）", tone: "idle" });
  });

  it("a judge's needs_user or an Agent's words never make it the owner's: only waitsOnOwner does", () => {
    expect(reason(todo("judge", "ci", { check: { verdict: "needs_user", at: minutesAgo(2) } }))).toBe("external");
    const asks = blocked("asks", { on: "external", line: "Needs you to approve the deploy", wait: "you" });
    expect(waitReason(todo("words", "ci", { agents: [{ machine_id: "m", session_id: "asks", linked_at: minutesAgo(30) }] }), makeSessionLookup([asks]), undefined, NOW)).toBe("external");
  });

  it("the page's placement, when given, is the one read", () => {
    const td = todo("ci", "ci");
    const placed = triage(td, lookup, NOW);
    expect(waitReason(td, lookup, placed, NOW)).toBe("external");
    expect(waitReason(td, lookup, { ...placed, section: "waiting", rule: "owner_decides" }, NOW)).toBe("owner");
  });
});

describe("its words and color", () => {
  it("Waiting on you in the attention color; every other wait in the neutral one", () => {
    const words: Record<LegacyWaitReason, string> = {
      owner: "等你", prerequisite: "等前置任务", delivery: "等合入 / 部署", external: "等外部", background: "等后台任务", retry: "等自动重试", unknown: "暂停中（不需要你）",
    };
    for (const [r, word] of Object.entries(words) as [LegacyWaitReason, string][]) {
      expect(todoLook({} as Todo, "waiting", r).label, r).toBe(word);
      expect(tone(r), r).toBe(r === "owner" ? "text-attention" : "text-ink-3");
      expect(waitTone(r), r).toBe(r === "owner" ? "attention" : "idle");
    }
  });

  it("only Waiting changes: other statuses keep their words and tones", () => {
    const td = todo("run", "run");
    expect(todoLook(td, "working")).toEqual({ label: stateName("working"), tone: "amber" });
    expect(todoLook(td, "needs_you")).toEqual({ label: stateName("needs_you"), tone: "amber" });
    expect(todoLook(td, "follow_up")).toEqual({ label: stateName("follow_up"), tone: "idle" });
    expect(waitOf(td, "working", lookup)).toBeUndefined();
    expect(statusWord("working")).toBe(stateName("working"));
  });

  it("Waiting with nothing at hand is the neutral fallback wherever TODO_STATUS_INFO is read", () => {
    expect(TODO_STATUS_INFO.waiting).toEqual({ label: waitName("unknown"), tone: "idle" });
    expect(statusWord("waiting")).toBe("暂停中（不需要你）");
    expect(statusWord("waiting", "owner")).toBe("等你");
  });

  it("the tray's status column says the same, Waiting on you in attention", () => {
    expect([agentRowLabel("waiting", "owner"), agentRowTone("waiting", "owner")]).toEqual(["等你", "text-attention"]);
    expect([agentRowLabel("waiting", "delivery"), agentRowTone("waiting", "delivery")]).toEqual(["等合入 / 部署", "text-ink-2"]);
    expect([agentRowLabel("waiting"), agentRowTone("waiting")]).toEqual(["暂停中（不需要你）", "text-ink-2"]);
    expect(agentRowTone("needs_you")).toBe("text-attention");
  });
});

describe("the calendar's entries (#2029)", () => {
  it("a waiting to-do's entry is named and colored by what it waits on", () => {
    const due = { due_date: "2026-11-10" };
    expect(todoEntry(todo("pr", "ci", { ...due, delivery: { state: "pr", unblock: "other" } }), lookup, NOW)).toMatchObject({ statusLabel: "等合入 / 部署", tone: "idle" });
    expect(todoEntry(todo("held", "ci", { ...due, delivery: { state: "merged", unblock: "user" } }), lookup, NOW)).toMatchObject({ statusLabel: "等你", tone: "attention" });
    expect(todoEntry(todo("run", "run", due), lookup, NOW)).toMatchObject({ statusLabel: stateName("working"), tone: "amber" });
  });
});

describe("the label and the sidebar's badge agree (#11085)", () => {
  it("a waiting to-do says Waiting on you exactly when the badge counts it", () => {
    const list = [
      todo("decide", "ci", { ask: { outcome: "decide", verdict: "Pick a cover", asked_at: minutesAgo(3) } }),
      todo("held", "bg", { delivery: { state: "merged", unblock: "user" } }),
      todo("ci", "ci"),
      todo("pr", "ci", { delivery: { state: "pr", unblock: "other" } }),
      todo("other", "other"),
      todo("retry", "retry"),
      todo("bg", "bg"),
      todo("unnamed", "unnamed"),
      todo("judge", "ci", { check: { verdict: "needs_user", at: minutesAgo(2) } }),
    ];
    const badge = ownerBadge(list, lookup, NOW);
    for (const td of list) expect(reason(td) === "owner", td.id).toBe(badge.has(td.id));
    expect([...badge].sort()).toEqual(["decide", "held"]);
  });
});

describe("todoLook for open PRs and concerns without active agents", () => {
  it("resolves projected pr_merge blockers, fresh PR facts, and concern Tracking with accurate tones", () => {
    const fresh = new Date(NOW - 60_000).toISOString();
    const prWait: Todo = {
      id: "pr-wait", title: "PR wait", created_at: fresh, updated_at: fresh,
      wait: { kind: "pr_merge", waiting_on: "PR #55", pr_blockers: ["conflicts", "checks_failing"] },
    };
    expect(todoLook(prWait, "not_started", undefined, undefined, NOW)).toEqual({ label: "存在合并冲突", tone: "red" });
    expect(todoLook({ ...prWait, wait: { kind: "pr_merge", waiting_on: "PR #55", pr_blockers: ["changes_requested"] } }, "not_started", undefined, undefined, NOW))
      .toEqual({ label: "需要修改", tone: "amber" });
    expect(todoLook({ ...prWait, wait: { kind: "pr_merge", waiting_on: "PR #55" } }, "not_started", undefined, undefined, NOW))
      .toEqual({ label: "待合入", tone: "idle" });
    const concern: Todo = { id: "c1", title: "Concern", kind: "concern", created_at: fresh, updated_at: fresh };
    expect(todoLook(concern, "not_started", undefined, undefined, NOW)).toEqual({ label: "跟进中", tone: "idle" });
  });
});
