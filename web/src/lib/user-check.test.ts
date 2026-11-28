import { describe, expect, it } from "vitest";
import { assistantEntries } from "./assistant-entries";
import { reviewBadge, reviewState } from "./supervision";
import { todoChipState } from "./task-row-status";
import { countsAsOpen, triage } from "./todo-triage";
import { shownStatus, todoStatus, todoStatusLabel, type Todo } from "./todos";

const now = Date.parse("2026-11-10T12:00:00Z");
const pending: Todo = {
  id: "personal-check", title: "Inspect the updated layout", origin: "main",
  created_at: "2026-11-01T08:00:00Z", updated_at: "2026-11-01T08:00:00Z", seen_at: "2026-11-02T08:00:00Z",
  delivery: { state: "live", at: "2026-11-01T08:00:00Z" },
  supervision: { supervisor: "main", state: "user_check", reviews: [{ at: "2026-11-01T08:00:00Z", verdict: "accepted" }] },
};

describe("personal verification is open work, not completion", () => {
  it("labels the task awaiting its owner without completed styling or a second home", () => {
    const status = todoStatus(pending);
    expect(reviewState(pending)).toBe("user_check");
    expect(reviewBadge(pending)).toEqual({ label: "待你验收", tone: "orange" });
    expect(todoStatusLabel(pending, status)).toBe("待你验收");
    expect(shownStatus(pending, status)).not.toBe("done");
    expect(todoChipState({ toStart: false, status, done: false, todo: pending })).toBe("待你验收");
    expect(triage(pending, undefined, now)).toMatchObject({ section: "waiting", rule: "owner_decides" });
    expect(countsAsOpen(pending, triage(pending, undefined, now))).toBe(true);
    const entries = assistantEntries({ conversation: "main", main: "main", todos: [pending], sessions: [], interactions: [], plans: {}, feed: [], now });
    expect(entries.needsYou).toEqual([]);
    expect(entries.verify.map((item) => item.key)).toEqual(["task:personal-check"]);
    expect(entries.running).toEqual([]);
    expect(entries.shelved).toEqual([]);
  });

  it("reads as accepted after Pass even if the final ledger update must be retried", () => {
    const passed = { ...pending, done_at: "2026-11-10T11:00:00Z", done_by: "review" };
    expect(reviewState(passed)).toBe("accepted");
    expect(reviewBadge(passed)).toEqual({ label: "已验收", tone: "green" });
    expect(todoStatusLabel(passed, todoStatus(passed))).toBe("已验收");
    expect(countsAsOpen(passed, triage(passed, undefined, now))).toBe(false);
  });

  it("leaves ordinary supervisor acceptance unchanged", () => {
    const accepted: Todo = { ...pending, supervision: { ...pending.supervision!, state: "accepted" } };
    expect(shownStatus(accepted, "follow_up")).toBe("done");
    expect(todoStatusLabel(accepted, "follow_up")).toBe("已验收");
  });
});
