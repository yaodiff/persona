import { describe, expect, it } from "vitest";
import { assistantEntries } from "./assistant-entries";
import { boardSections } from "./task-board";
import { compareWaitAge, orderWaitGroups, waitSource, waitStart } from "./task-wait";
import { taskWait } from "./task-wait-info";
import { triageTodos } from "./todo-triage";
import type { ParkEvidence, TaskReview } from "./supervision";
import type { PRFacts, Todo } from "./todos";

const now = Date.parse("2026-11-17T12:00:00Z");
const iso = (hoursAgo: number) => new Date(now - hoursAgo * 3_600_000).toISOString();
const receipt = (hoursAgo: number) => ({
  channel: "Email", url: "https://example.test/messages/7", sender: "Example sender", sent_at: iso(hoursAgo),
  request: "Please confirm the schedule", status: "sent",
});
const evidence = (hoursAgo: number): ParkEvidence => ({ kind: "person_reply", target: "Schedule owner", wake: "The owner replies in the thread", contact: receipt(hoursAgo), recorded_by: "recording-conversation" });
const make = (id: string, hoursAgo?: number): Todo => {
  const e = hoursAgo === undefined ? undefined : evidence(hoursAgo);
  return {
    id, title: `Example schedule ${id}`, origin: "main", created_at: iso(240), updated_at: iso(0),
    supervision: {
      supervisor: "main", state: "parked",
      parked: { reason: "Waiting for the schedule owner", until: iso(-24), waiting_on: "someone_else", count: 1, evidence: e },
      reviews: e ? [{ at: iso(hoursAgo! - 1), verdict: "parked", parks: 1, evidence: e }] : [],
    },
  };
};
const review = (e: ParkEvidence, at: number, parks: number): TaskReview => ({ at: iso(at), verdict: "parked", parks, evidence: e });
const pr: PRFacts = { url: "https://github.com/example/demo/pull/7", state: "open", draft: false, review: "required", checks: "success", conflicts: false, checked_at: iso(0) };

describe("recorded external wait information", () => {
  it("uses the actual request, not task updates, polling or the recording conversation", () => {
    const todo = make("recorded", 48);
    const before = JSON.stringify(todo);
    expect(taskWait(todo, now)).toMatchObject({ kind: "person_reply", target: "Schedule owner", since: Date.parse(iso(48)), sinceRequest: true,
      contact: { sender: "Example sender", channel: "Email", sent_at: iso(48) }, wake: "The owner replies in the thread", overdue: true, canRemind: true });
    expect(JSON.stringify(todo)).toBe(before);
  });

  it("keeps the first request across re-parks and newer confirmed reminders", () => {
    const todo = make("repark", 25);
    const first = evidence(100), last = evidence(25);
    last.contact!.url = "https://example.test/messages/8";
    last.recorded_by = "another-conversation";
    todo.supervision!.parked!.count = 2;
    todo.supervision!.parked!.evidence = last;
    todo.supervision!.reviews = [review(first, 99, 1), review(last, 24, 2)];
    expect(waitStart(todo, now)).toEqual({ at: Date.parse(iso(100)), request: true });
    expect(taskWait(todo, now)?.contact?.url).toBe(last.contact!.url);
    expect(taskWait(todo, now)?.canRemind).toBe(true);
    // A recently sent reminder postpones follow-up, but does not reset the original age.
    last.contact!.sent_at = iso(1);
    expect(waitStart(todo, now).at).toBe(Date.parse(iso(100)));
    expect(taskWait(todo, now)?.canRemind).toBe(false);
  });

  it("stops a wait streak when an Agent turn reset the park count", () => {
    const todo = make("new-turn", 2), first = evidence(100), last = evidence(2);
    todo.supervision!.reviews = [review(first, 99, 1), review(last, 1, 1)];
    expect(waitStart(todo, now).at).toBe(Date.parse(iso(2)));
  });

  it("keeps a review wait's start when only the PR query, fragment or trailing slash changes", () => {
    const todo = make("same-review", 25);
    const first: ParkEvidence = { ...evidence(100), kind: "merge_review", ready: true, url: `${pr.url}?from=review#note` };
    const last: ParkEvidence = { ...evidence(25), kind: "merge_review", ready: true, url: `${pr.url}/` };
    todo.supervision!.parked!.count = 2;
    todo.supervision!.parked!.evidence = last;
    todo.supervision!.reviews = [review(first, 99, 1), review(last, 24, 2)];
    expect(waitStart(todo, now).at).toBe(Date.parse(iso(100)));
  });

  it.each(["target", "channel", "object", "verdict"])("does not carry an age across a changed %s", (change) => {
    const todo = make("changed", 2), first = evidence(100), last = evidence(2);
    if (change === "target") first.target = "Another owner";
    if (change === "channel") first.contact!.channel = "Chat";
    if (change === "object") {
      first.kind = last.kind = "merge_review";
      first.ready = last.ready = true;
      first.url = "https://github.com/example/demo/pull/6";
      last.url = pr.url;
    }
    todo.supervision!.parked!.count = 2;
    todo.supervision!.parked!.evidence = last;
    todo.supervision!.reviews = [change === "verdict" ? { at: iso(99), verdict: "rework" } : review(first, 99, 1), review(last, 1, 2)];
    expect(waitStart(todo, now).at).toBe(Date.parse(iso(2)));
  });

  it("uses a recorded park time for non-contact waits, not the task creation date", () => {
    const todo = make("event");
    const e: ParkEvidence = { kind: "external_event", target: "Published version", url: "https://example.test/releases/7", wake: "Version 7 is published" };
    todo.supervision!.parked!.evidence = e;
    todo.supervision!.reviews = [review(e, 30, 1)];
    expect(taskWait(todo, now)).toMatchObject({ kind: "external_event", since: Date.parse(iso(30)), sinceRequest: false, contact: undefined, canRemind: false });
  });

  it("leaves unrecorded legacy and observational merge starts unknown", () => {
    const todo = make("legacy");
    expect(taskWait(todo, now)).toMatchObject({ kind: "legacy", since: undefined, target: undefined, contact: undefined });
    const implicit = { ...todo, supervision: undefined, pr_url: pr.url, pr_status: pr };
    expect(taskWait(implicit, now, [pr])).toMatchObject({ kind: "merge_review", since: undefined, target: undefined, contact: undefined, sources: [pr.url] });
    expect(taskWait({ ...implicit, pr_status: { ...pr, checked_at: iso(10) } }, now, [pr])?.since).toBeUndefined();
  });

  it("uses the current park evidence rather than an unrelated task PR as its source", () => {
    const todo = make("bound-event");
    const bound = "https://github.com/example/demo/pull/73";
    todo.pr_url = pr.url;
    todo.pr_status = pr;
    todo.supervision!.parked!.evidence = { kind: "external_event", target: "Review of the new PR", url: bound, wake: "A review arrives" };
    expect(taskWait(todo, now, [pr])?.sources).toEqual([bound]);
  });

  it.each([undefined, "", "javascript:alert(1)"])("does not replace a park's missing or unsafe evidence URL with another PR: %s", (url) => {
    const todo = make("missing-source");
    todo.supervision!.parked!.evidence = { kind: "external_event", target: "Published version", url, wake: "The version changes" };
    expect(taskWait(todo, now, [pr])?.sources).toEqual([]);
  });

  it("keeps every eligible merge source when no explicit wait evidence is recorded", () => {
    const another = { ...pr, url: "https://github.com/example/demo/pull/8" };
    const todo = { ...make("merge-only"), supervision: undefined, pr_url: pr.url, pr_status: pr, other_prs: [another] };
    expect(taskWait(todo, now, [pr, another, pr])?.sources).toEqual([pr.url, another.url]);
    expect(taskWait(make("legacy"), now, [pr, another])?.sources).toEqual([pr.url, another.url]);
  });

  it("can display a legacy park time without calling it a sent request", () => {
    const todo = make("legacy-with-history");
    todo.supervision!.reviews = [{ at: iso(18), verdict: "parked", reason: todo.supervision!.parked!.reason, waiting_on: "someone_else", parks: 1 }];
    expect(waitStart(todo, now)).toEqual({ at: Date.parse(iso(18)), request: false });
  });

  it.each(["draft", "failed", "unknown", "future", "bad-time", "bad-url", "missing-sender", "missing-request", "missing-channel"])("does not report a %s contact as a confirmed request", (bad) => {
    const todo = make(bad, 48), c = todo.supervision!.parked!.evidence!.contact!;
    if (["draft", "failed", "unknown"].includes(bad)) c.status = bad;
    if (bad === "future") c.sent_at = iso(-1);
    if (bad === "bad-time") c.sent_at = "not-a-date";
    if (bad === "bad-url") c.url = "javascript:alert(1)";
    if (bad === "missing-sender") c.sender = "";
    if (bad === "missing-request") c.request = "";
    if (bad === "missing-channel") c.channel = "";
    expect(taskWait(todo, now)?.contact).toBeUndefined();
    expect(taskWait(todo, now)?.canRemind).toBe(false);
    expect(waitStart(todo, now).request).toBe(false);
  });

  it("marks 24-hour replies and 72-hour reviews due at the exact boundary", () => {
    const todo = make("reply", 24);
    expect(taskWait(todo, now - 1)?.overdue).toBe(false);
    expect(taskWait(todo, now)?.canRemind).toBe(true);
    todo.supervision!.parked!.evidence = { ...evidence(72), kind: "merge_review", ready: true, url: pr.url };
    expect(taskWait(todo, now - 1)?.canRemind).toBe(false);
    expect(taskWait(todo, now)?.canRemind).toBe(true);
  });

  it.each(["not-ready", "missing-object", "not-a-review"])("does not turn %s review evidence into a contact or reminder", (bad) => {
    const todo = make("invalid-review", 96);
    const e: ParkEvidence = { ...evidence(96), kind: "merge_review", ready: true, url: pr.url };
    if (bad === "not-ready") e.ready = false;
    if (bad === "missing-object") e.url = undefined;
    if (bad === "not-a-review") e.url = "https://example.test/releases/7";
    todo.supervision!.parked!.evidence = e;
    expect(taskWait(todo, now)?.contact).toBeUndefined();
    expect(taskWait(todo, now)?.canRemind).toBe(false);
  });

  it.each([`${pr.url}?from=review#comment`, "https://example.test/team/demo/-/merge_requests/7#note"])("keeps a ready review object with a query or fragment: %s", (url) => {
    const todo = make("review-link", 96);
    todo.supervision!.parked!.evidence = { ...evidence(96), kind: "merge_review", ready: true, url };
    expect(taskWait(todo, now)?.contact).toMatchObject({ status: "sent" });
    expect(taskWait(todo, now)?.canRemind).toBe(true);
  });

  it("marks a fallback date due without inventing a reminder contact", () => {
    const todo = make("date");
    todo.supervision!.parked!.until = iso(0);
    todo.supervision!.parked!.evidence = { kind: "date", target: "Scheduled opening", wake: "The opening date arrives" };
    expect(taskWait(todo, now - 1)?.overdue).toBe(false);
    expect(taskWait(todo, now)).toMatchObject({ kind: "date", overdue: true, canRemind: false });
  });

  it("hides completed work and owner/supervisor decisions rather than presenting another wait", () => {
    const todo = make("done", 48);
    expect(taskWait({ ...todo, done_at: iso(0) }, now, [pr])).toBeUndefined();
    for (const state of ["needs_user", "pending_review"] as const) {
      expect(taskWait({ ...todo, supervision: { ...todo.supervision!, state } }, now, [pr])).toBeUndefined();
    }
    expect(taskWait({ ...todo, ask: { outcome: "decide", verdict: "Choose a date", asked_at: iso(1) } }, now, [pr])).toBeUndefined();
  });

  it.each(["javascript:alert(1)", "data:text/html,example", "https://user:secret@example.test/thread/1", "https://example.test/", "not a URL"])("does not expose an unsafe or unrecorded source %s", (url) => {
    expect(waitSource(url)).toBeUndefined();
  });
});

describe("wait-age ordering on task surfaces", () => {
  const list = () => [make("unknown"), make("newer", 2), make("older", 100)];
  it("puts known starts oldest first and unknowns last, with stable identity ties", () => {
    expect(list().sort((a, b) => compareWaitAge(a, b, now)).map((td) => td.id)).toEqual(["older", "newer", "unknown"]);
    expect([make("b", 2), make("a", 2)].sort((a, b) => compareWaitAge(a, b, now)).map((td) => td.id)).toEqual(["a", "b"]);
  });
  it("keeps the existing conversation tray's membership and open count for lazy wait ordering", () => {
    const todos = list();
    const got = assistantEntries({ conversation: "main", main: "main", todos, sessions: [], interactions: [], plans: {}, feed: [], now });
    expect(got.parked.map((item) => item.kind === "task" ? item.todo.id : "").sort()).toEqual(["newer", "older", "unknown"]);
    expect(got.parked.filter((item) => item.kind === "task" && item.countsOpen)).toHaveLength(3);
    expect(got.needsYou).toHaveLength(0);
  });
  it("orders To-dos waiting sections using the same starts", () => {
    const got = orderWaitGroups(triageTodos(list(), undefined, now), now);
    expect(got.sections.parked.map((group) => group.lead.id)).toEqual(["older", "newer", "unknown"]);
    expect(got.open).toBe(3);
  });
  it("changes a waiting group's lead to its oldest wait without mutating canonical triage or counts", () => {
    const newer = { ...make("a-newer", 2), pr_url: pr.url };
    const older = { ...make("z-older", 100), pr_url: pr.url };
    const original = triageTodos([newer, older, make("middle", 50)], undefined, now);
    const before = JSON.stringify(original);
    const got = orderWaitGroups(original, now);
    expect(got.sections.parked.map((group) => group.lead.id)).toEqual(["z-older", "middle"]);
    expect(got.sections.parked[0].members.map((todo) => todo.id)).toEqual(["z-older", "a-newer"]);
    expect(got.sections.parked[0].key).toBe(original.sections.parked[0].key);
    expect(got.sections.parked[0].key).toBe("a-newer");
    expect(got.todoCounts).toBe(original.todoCounts);
    expect(got.sections.working).toBe(original.sections.working);
    expect(got.open).toBe(original.open);
    expect(JSON.stringify(original)).toBe(before);
  });
  it("orders All's waiting slots without changing their section", () => {
    const got = boardSections(list().map((todo) => ({ todo, status: "follow_up" as const, parked: true })), [], now);
    expect(got).toHaveLength(1);
    expect(got[0].key).toBe("parked");
    expect(got[0].rows.map((row) => row.todo.id)).toEqual(["older", "newer", "unknown"]);
  });
});
