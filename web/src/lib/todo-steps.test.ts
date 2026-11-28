// A to-do's steps on its status line (#11858, lib/todo-steps.ts): the hub's count on the row's view, said in place of
// Not started when no Agent of its own is on it, with what is next once every step is closed, or after its Agent count;
// nothing without the view. The hub's own cases (spec/fixtures/todo-view) are drawn with their count as sent. The data
// is made up.
import { beforeAll, describe, expect, it } from "vitest";
import fixture from "../../../spec/fixtures/todo-view/todo_view.json";
import { initI18n, t } from "../i18n";
import type { TodoSummary } from "./todo-view-wire";
import { stepsWords, todoSteps } from "./todo-steps";
import { makeSessionLookup, todoStatus, type Todo } from "./todos";
import type { Session } from "./types";

const at = "2026-11-10T08:00:00Z";
const view = (steps?: { done: number; total: number }): TodoSummary => ({ phase: "todo", section: "todo", reason: "rest", rank: 0, ...(steps && { steps }) });
const goal = (patch: Partial<Todo> = {}): Todo => ({ id: "goal", title: "Plan the trip", created_at: at, updated_at: at, ...patch });

beforeAll(() => initI18n("en"));

describe("a to-do's steps on its status line (#11858)", () => {
  it("says nothing without the hub's count: no view, no steps, none counted, or closed", () => {
    expect(stepsWords(goal(), "not_started")).toBeUndefined();
    expect(stepsWords(goal({ view: view() }), "not_started")).toBeUndefined();
    expect(stepsWords(goal({ view: view({ done: 0, total: 0 }) }), "not_started")).toBeUndefined();
    expect(stepsWords(goal({ view: view({ done: 2, total: 2 }), done_at: at }), "done")).toBeUndefined();
    // a failed start keeps done_at: its line is the failure's, as before
    expect(todoSteps(goal({ view: view({ done: 1, total: 2 }), done_at: at, start_error: "offline" }))).toBeUndefined();
  });

  it("puts the steps in place of Not started, and nothing about handing it over, while some are open", () => {
    expect(stepsWords(goal({ view: view({ done: 3, total: 9 }) }), "not_started")).toEqual({ label: "Steps 3/9", tail: "" });
  });

  it("says every step is done and the next is not arranged once all are closed", () => {
    expect(stepsWords(goal({ view: view({ done: 9, total: 9 }) }), "not_started")).toEqual({ label: "9/9 steps done", tail: "Next step not arranged yet" });
  });

  it("keeps Accepted on a to-do its supervisor accepted, at rest, the steps after it", () => {
    const supervision = { state: "accepted", reviews: [{ at, verdict: "accepted" as const }] } as unknown as Todo["supervision"];
    expect(stepsWords(goal({ view: view({ done: 3, total: 3 }), supervision }), "not_started")).toEqual({ after: "Steps 3/3", tail: "" });
    expect(stepsWords(goal({ view: view({ done: 1, total: 3 }), supervision }), "not_started")).toEqual({ after: "Steps 1/3", tail: "" });
  });

  it("keeps the status of a to-do whose own result is finished or awaits review, as the hub's finish reads it", () => {
    const all = view({ done: 3, total: 3 });
    const pending = { state: "pending_review" } as unknown as Todo["supervision"];
    expect(stepsWords(goal({ view: all, supervision: pending }), "not_started")).toEqual({ after: "Steps 3/3", tail: "" });
    expect(stepsWords(goal({ view: all, delivery: { state: "live" } }), "not_started")).toEqual({ after: "Steps 3/3", tail: "" });
    expect(stepsWords(goal({ view: all, ask: { verdict: "done", outcome: "done", asked_at: at } }), "not_started")).toEqual({ after: "Steps 3/3", tail: "" });
    expect(stepsWords(goal({ view: all, ask: { verdict: "check it", asked_at: at } }), "not_started")).toEqual({ after: "Steps 3/3", tail: "" });
    // not finished: an ask to decide, a concern deployed, a pr delivery
    expect(stepsWords(goal({ view: all, ask: { verdict: "which", outcome: "decide", asked_at: at } }), "not_started")?.label).toBe("3/3 steps done");
    expect(stepsWords(goal({ view: all, kind: "concern", delivery: { state: "live" } }), "not_started")?.label).toBe("3/3 steps done");
    expect(stepsWords(goal({ view: all, delivery: { state: "pr" } }), "not_started")?.label).toBe("3/3 steps done");
  });

  it("keeps the status of a to-do its own Agent is on, the steps after its Agent count", () => {
    expect(stepsWords(goal({ view: view({ done: 1, total: 2 }) }), "working")).toEqual({ after: "Steps 1/2" });
    expect(stepsWords(goal({ view: view({ done: 2, total: 2 }) }), "follow_up")).toEqual({ after: "Steps 2/2" });
  });

  it.each([
    { lang: "zh-Hans", count: "步骤 3/9", done: "9/9 步已完成", next: "下一步尚未安排" },
    { lang: "ja", count: "ステップ 3/9", done: "9/9 ステップ完了", next: "次のステップはまだ決まっていません" },
    { lang: "en", count: "Steps 3/9", done: "9/9 steps done", next: "Next step not arranged yet" },
  ] as const)("words it as iOS does in $lang", async ({ lang, count, done, next }) => {
    await initI18n(lang);
    expect(t("todos.steps_count", { done: 3, total: 9 })).toBe(count);
    expect(t("todos.steps_all_done", { done: 9, total: 9 })).toBe(done);
    expect(t("todos.steps_next_unplanned")).toBe(next);
  });
});

interface Case {
  name: string;
  input: { todo: Todo; sessions?: Session[] };
  expected: { view?: TodoSummary };
}

describe("the hub's cases with steps (spec/fixtures/todo-view)", () => {
  const cases = (fixture as unknown as { cases: Case[] }).cases.filter((c) => c.expected.view?.steps);

  it("has some", () => expect(cases.length).toBeGreaterThan(0));

  it.each(cases.map((c) => [c.name, c] as const))("%s: draws the count the hub sends", (_name, c) => {
    const todo: Todo = { ...c.input.todo, view: c.expected.view };
    const { done, total } = c.expected.view!.steps!;
    const words = stepsWords(todo, todoStatus(todo, makeSessionLookup(c.input.sessions ?? [])));
    const said = words?.label ?? words?.after ?? "";
    expect(said).toContain(`${done}/${total}`);
  });
});
