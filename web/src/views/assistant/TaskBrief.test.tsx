// A task's brief (#3822): on the plan card before "Start", on the task's page above its criteria, each criterion's
// result where finished work is shown, the scope changes and who worked on it. The tests read zh-Hans, as the page does.
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { formatDate } from "../../i18n";
import { briefAsked, resultExtras, resultOf, type BriefCriterion, type TaskBrief } from "../../lib/brief";
import type { Todo } from "../../lib/todos";
import { ReviewChecklist } from "../Todos";
import { BriefResultExtras, briefSummary, expectText, PlanBrief, TaskBriefHistory, TaskBriefPanel } from "./TaskBrief";

const at = "2026-11-03T12:00:00Z";
const brief: TaskBrief = {
  id: "brief_1",
  created_at: at,
  started_by: "owner",
  request: { text: "Please fix the sign-in crash before Friday", at, conversation: "main", message_id: "m1" },
  goal: "The sign-in page no longer crashes in landscape",
  done_when: ["The fix is merged", "A test covers landscape"],
  criteria: ["The fix is merged", "A test covers landscape"],
  expect_minutes: 30,
  out_of_bounds: ["Do not touch the payment flow"],
  log: [],
};
const todo: Todo = { id: "todo_3", title: "Retry the sign-in fix", created_at: at, updated_at: at, brief };
const criteria: BriefCriterion[] = [
  { criterion: "The fix is merged", state: "met", evidence: "merged in the example change" },
  { criterion: "A test covers landscape", state: "not_met" },
  { criterion: "Screenshots attached", state: "dropped", change: "No screen changed after all", changed_at: at },
  { criterion: "The changelog has a line", state: "unchecked" },
];

describe("who asked (#3822)", () => {
  it("is the owner's own words when the hub recorded them", () => {
    expect(briefAsked(brief)).toEqual({ kind: "owner", at, text: "Please fix the sign-in crash before Friday", truncated: false });
  });
  it("names who started work the owner did not ask for, with why", () => {
    expect(briefAsked({ ...brief, request: undefined, started_by: "assistant", reason: "Left over from the deploy" })).toEqual({ kind: "started", by: "assistant", at, why: "Left over from the deploy" });
    expect(briefAsked({ ...brief, request: undefined, started_by: "routine", trigger_title: "Morning check" })).toMatchObject({ kind: "started", by: "routine", why: "Morning check" });
  });
  it("says a brief made later is one, and invents nobody", () => {
    expect(briefAsked({ ...brief, request: undefined, started_by: "unknown", reconstructed: true })).toEqual({ kind: "reconstructed" });
    expect(briefAsked({ ...brief, request: undefined, started_by: "unknown" })).toEqual({ kind: "unknown", at });
    expect(briefAsked({ ...brief, request: undefined, started_by: "someone-new" })).toEqual({ kind: "unknown", at });
  });
});

describe("the brief on the plan card (#3822)", () => {
  it("folded, shows the owner's words with the date and one line that counts the rest", () => {
    const html = renderToStaticMarkup(<PlanBrief brief={brief} title="Fix the sign-in crash" />);
    expect(html).toContain('data-brief-asked="owner"');
    expect(html).toContain(`你于 ${formatDate(at)} 提出：`);
    expect(html).toContain("Please fix the sign-in crash before Friday");
    expect(html).toContain("line-clamp-2"); // long words are held to two lines, the rest on demand
    expect(html).toContain("2 条验收标准 · 约 30 分钟 · 1 条禁止事项");
    // the goal, the criteria and what is out of bounds are behind the one control
    expect(html).not.toContain("The sign-in page no longer crashes in landscape");
    expect(html).not.toContain("The fix is merged");
    expect(html).not.toContain("Do not touch the payment flow");
    expect(html.match(/aria-expanded="false"/g)).toHaveLength(1);
  });
  it("counts only what the brief has", () => {
    expect(briefSummary(brief)).toBe("2 条验收标准 · 约 30 分钟 · 1 条禁止事项");
    expect(briefSummary({ ...brief, out_of_bounds: undefined, expect_minutes: 150 })).toBe("2 条验收标准 · 约 2.5 小时");
    expect(briefSummary({ ...brief, done_when: [], criteria: [], out_of_bounds: [], expect_minutes: 0 })).toBe("");
    // nothing to open: no control
    const bare = renderToStaticMarkup(<PlanBrief brief={{ ...brief, goal: "x", done_when: [], criteria: [], out_of_bounds: [], expect_minutes: 0 }} title="x" />);
    expect(bare).not.toContain("aria-expanded");
  });
  it("says who started it when the owner did not ask", () => {
    const html = renderToStaticMarkup(<PlanBrief brief={{ ...brief, request: undefined, started_by: "leader", reason: "The earlier agent stopped" }} title="x" />);
    expect(html).toContain('data-brief-asked="leader"');
    expect(html).toContain(`${formatDate(at)} 由负责人对话发起：The earlier agent stopped`);
    expect(html).not.toContain("你于");
  });
  it("says hours for long work", () => {
    expect(expectText(45)).toBe("约 45 分钟");
    expect(expectText(150)).toBe("约 2.5 小时");
    expect(expectText(240)).toBe("约 4 小时");
  });
});

describe("the brief on the task's page (#3822)", () => {
  it("says a reconstructed brief is one and labels the hand-over prompt", () => {
    const old: Todo = { ...todo, brief: { ...brief, request: undefined, started_by: "unknown", reconstructed: true, handover: "You are taking over a session that stopped: carry on from its branch." } };
    const html = renderToStaticMarkup(<TaskBriefPanel todo={old} />);
    expect(html).toContain("事后补建：未记录原始需求内容");
    expect(html).toContain("交接提示词");
    expect(html).toContain("You are taking over a session that stopped");
    expect(html).not.toContain("你于");
  });
  it("marks work past its clock while it is open, in words and a mark", () => {
    const late: Todo = { ...todo, brief: { ...brief, overdue_at: "2026-11-05T12:00:00Z" } };
    const html = renderToStaticMarkup(<TaskBriefPanel todo={late} />);
    expect(html).toContain('data-testid="brief-overdue"');
    expect(html).toContain("已超时");
    expect(html).toContain(`自 ${formatDate("2026-11-05T12:00:00Z")} 起已超出预计用时`);
    expect(renderToStaticMarkup(<TaskBriefPanel todo={{ ...late, done_at: at }} />)).not.toContain("brief-overdue");
  });
  it("leaves the criteria and the time to the review box when the task has one", () => {
    const supervised: Todo = { ...todo, supervision: { supervisor: "main", done_when: brief.done_when, expect_minutes: 30 } };
    const html = renderToStaticMarkup(<TaskBriefPanel todo={supervised} />);
    expect(html).not.toContain("The fix is merged");
    expect(html).not.toContain("约 30 分钟");
    expect(renderToStaticMarkup(<TaskBriefPanel todo={todo} />)).toContain("The fix is merged");
  });
  it("draws nothing for a to-do without a brief", () => {
    expect(renderToStaticMarkup(<TaskBriefPanel todo={{ ...todo, brief: undefined }} />)).toBe("");
    expect(renderToStaticMarkup(<TaskBriefHistory todo={{ ...todo, brief: undefined }} />)).toBe("");
    expect(renderToStaticMarkup(<TaskBriefHistory todo={todo} />)).toBe(""); // nothing changed, nobody took over
  });
  it("lists the scope changes and who worked on it, with why each stopped", () => {
    const worked: Todo = {
      ...todo,
      brief: { ...brief, log: [{ at, kind: "scope", change: "No screenshots needed", why: "No screen changed", dropped: ["Screenshots attached"], added: ["The changelog has a line"], expect_minutes: 180, approval: { text: "ok", at } }, { at, kind: "overdue" }] },
      lineage: [
        { todo_id: "todo_1", title: "Fix the sign-in crash", agent: "claude", machine: "box", started: at, ended: "2026-11-04T12:00:00Z", why: "Its computer went offline", superseded_by: "todo_2" },
        { todo_id: "todo_2", title: "Take over the sign-in fix", agent: "codex", started: "2026-11-04T12:00:00Z", ended: "2026-11-05T12:00:00Z", why: "The model service kept failing", continues: "todo_1", superseded_by: "todo_3" },
        { todo_id: "todo_3", title: "Retry the sign-in fix", started: "2026-11-05T12:00:00Z", continues: "todo_2", current: true },
      ],
    };
    const html = renderToStaticMarkup(<TaskBriefHistory todo={worked} onOpenTodo={() => {}} />);
    expect(html).toContain("范围变更");
    expect(html).toContain("No screenshots needed");
    expect(html).toContain("原因：No screen changed");
    expect(html).toContain("移除：Screenshots attached");
    expect(html).toContain("新增：The changelog has a line");
    expect(html).toContain("预计用时更新为：约 3 小时");
    expect(html).toContain("已获你批准");
    expect(html.match(/data-testid="brief-scope"/g)).toHaveLength(1);
    expect(html).toContain("经手记录");
    expect(html).toContain("停止原因：Its computer went offline");
    expect(html).toContain("停止原因：The model service kept failing");
    expect(html).toContain("当前任务");
    expect(html).toContain("仍在进行中");
    expect(html.match(/<li data-lineage-current/g)).toHaveLength(1);
    expect(html).toContain('data-open-todo="todo_1"');
    expect(html).toContain('data-open-todo="todo_2"');
    expect(html).toContain("承接自");
    expect(html).toContain("后续由");
  });
});

describe("what a finished task's result adds to its criteria (#3822)", () => {
  it("lists only the dropped criteria, with the change, and what else was done", () => {
    const extras = resultExtras({ ...todo, brief: { ...brief, result: { at, todo_id: "todo_3", criteria, also_done: ["Removed a dead flag"] } } })!;
    expect(extras.dropped.map((c) => c.criterion)).toEqual(["Screenshots attached"]);
    const html = renderToStaticMarkup(<BriefResultExtras dropped={extras.dropped} alsoDone={extras.alsoDone} />);
    expect(html.match(/data-criterion-state=/g)).toHaveLength(1);
    expect(html).toContain('data-criterion-state="dropped"');
    expect(html).toContain("已移除：No screen changed after all");
    expect(html).toContain("额外完成");
    expect(html).toContain("Removed a dead flag");
    // met, not met and not checked are the criteria list's own (the supervision ledger's): not said a second time
    expect(html).not.toContain("The fix is merged");
    expect(html).not.toContain("The changelog has a line");
  });
  it("is nothing when no criterion was dropped and nothing else was done", () => {
    const kept = criteria.filter((c) => c.state !== "dropped");
    expect(resultExtras({ ...todo, brief: { ...brief, result: { at, todo_id: "todo_3", criteria: kept } } })).toBeUndefined();
    expect(resultExtras(todo)).toBeUndefined();
    expect(renderToStaticMarkup(<BriefResultExtras dropped={[]} alsoDone={[]} />)).toBe("");
  });
  it("leaves the review box's own list as the one list of criteria", () => {
    const supervision = { supervisor: "main", done_when: brief.done_when, state: "accepted" as const };
    const result = { at, todo_id: "todo_3", criteria, also_done: ["Removed a dead flag"] };
    const done: Todo = { ...todo, done_at: at, supervision, brief: { ...brief, result } };
    const html = renderToStaticMarkup(<ReviewChecklist todo={done} />);
    expect(html).toContain("验收标准");
    for (const c of brief.done_when!) expect(html.split(c).length - 1).toBe(1); // each criterion once
    // while the task is open, or without a result, the box is as it was
    const open = renderToStaticMarkup(<ReviewChecklist todo={{ ...done, done_at: undefined }} />);
    expect(open).toBe(renderToStaticMarkup(<ReviewChecklist todo={{ ...done, done_at: undefined, brief: undefined }} />));
    expect(renderToStaticMarkup(<ReviewChecklist todo={{ ...done, brief }} />)).toBe(renderToStaticMarkup(<ReviewChecklist todo={{ ...done, brief: undefined }} />));
  });
  it("is the to-do's own result only", () => {
    const result = { at, todo_id: "todo_3", criteria };
    expect(resultOf({ ...todo, brief: { ...brief, result } })).toBe(result);
    expect(resultOf({ ...todo, id: "todo_1", brief: { ...brief, result } })).toBeUndefined(); // another to-do took the work over
    expect(resultOf(todo)).toBeUndefined();
  });
});
