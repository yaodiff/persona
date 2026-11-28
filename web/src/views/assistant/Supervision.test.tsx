import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { Todo } from "../../lib/todos";
import { makeSessionLookup } from "../../lib/todos";
import type { Session } from "../../lib/types";
import { Icon } from "../../ui/icons";
import { FeedLineView } from "./Tasks";
import { LatestReport, ReviewBadge } from "./Supervision";
import { ReviewChecklist } from "../Todos";

const todo: Todo = {
  id: "todo_1",
  title: "部署 hub",
  created_at: "2026-11-05T08:00:00Z",
  updated_at: "2026-11-05T08:00:00Z",
  supervision: {
    supervisor: "main",
    done_when: ["PR 合并到 master", "浅色 / 深色截图贴进 PR"],
    expect_minutes: 30,
    state: "rework",
    round: 1,
    reviews: [{ at: "2026-11-05T09:00:00Z", verdict: "rework", round: 1, note: "补深色截图",
      checks: [{ criterion: "PR 合并到 master", met: true, evidence: "pull/901 merged" }, { criterion: "浅色 / 深色截图贴进 PR", met: false, evidence: "只有浅色" }] }],
  },
};

describe("a supervised task", () => {
  it("updates the card count before a review and shows evidence, timestamp and reporter", () => {
    const live: Todo = { ...todo, supervision: {
      supervisor: "main", done_when: todo.supervision!.done_when,
      criteria: [
        { criterion: "PR 合并到 master", state: "met", evidence: "pull/7 merged", at: "2026-11-10T09:00:00Z", by: "agent:box/s1" },
        { criterion: "浅色 / 深色截图贴进 PR", state: "pending" },
      ],
    } };
    const html = renderToStaticMarkup(<ReviewChecklist todo={live} />);
    expect(html).toContain("督办中");
    expect(html).toContain("1/2");
    expect(html).toContain("pull/7 merged");
    expect(html).toContain('dateTime="2026-11-10T09:00:00Z"');
    expect(html).toContain('data-testid="review-evidence-author">Agent<');
    expect(html).not.toContain("agent:box/s1");
    expect(html).toContain('aria-label="尚未验收"');
  });
  it.each([
    ["agent:box/s1", "Agent"],
    ["supervisor:main", "督办"],
    ["judge", "评审"],
  ])("maps %s evidence attribution to readable label %s in the task detail", (by, expected) => {
    const detail: Todo = { ...todo, supervision: {
      supervisor: "main", done_when: ["Tests pass"],
      criteria: [{ criterion: "Tests pass", state: "met", evidence: "go test: PASS", at: "2026-11-11T01:00:00Z", by }],
    } };
    const html = renderToStaticMarkup(<ReviewChecklist todo={detail} />);
    expect(html).toContain(`data-testid="review-evidence-author">${expected}<`);
    expect(html).not.toContain(by);
    expect(html).toContain("go test: PASS");
    expect(html).toContain('dateTime="2026-11-11T01:00:00Z"');
  });
  it("keeps the upstream parked explanation beside the persisted criterion", () => {
    const html = renderToStaticMarkup(<ReviewChecklist todo={{ ...todo, supervision: {
      supervisor: "main", done_when: ["Tests pass"], state: "parked",
      parked: { reason: "Waiting for the external release", until: "2026-11-14T09:00:00Z", waiting_on: "external_event", count: 1 },
      criteria: [{ criterion: "Tests pass", state: "met", evidence: "go test: PASS", by: "agent:box/s1", at: "2026-11-11T01:00:00Z" }],
    } }} />);
    expect(html).toContain('data-testid="review-parked"');
    expect(html).toContain("Waiting for the external release");
    expect(html).toContain("go test: PASS");
    expect(html).toContain('data-testid="review-evidence-author">Agent<');
    expect(html).not.toContain("agent:box/s1");
    expect(html).toContain('dateTime="2026-11-11T01:00:00Z"');
  });
  it("does not display stale parked metadata while the task's Agent is working", () => {
    const live: Todo = { ...todo, agents: [{ machine_id: "demo", session_id: "worker", linked_at: "2026-11-11T00:00:00Z" }],
      supervision: { supervisor: "main", state: "parked", done_when: ["Tests pass"],
        parked: { reason: "Earlier external wait", until: "2026-11-18T09:00:00Z", waiting_on: "someone_else", count: 1 } } };
    const lookup = makeSessionLookup([{ machine_id: "demo", session_id: "worker", state: "working", status: "working" } as Session]);
    const html = renderToStaticMarkup(<ReviewChecklist todo={live} lookup={lookup} />);
    expect(html).toContain('data-testid="review-checklist"');
    expect(html).not.toContain('data-task-wait-info=');
    expect(html).not.toContain("Earlier external wait");
  });
  it("tints accepted work green but keeps reviews requiring user input neutral", () => {
    const accepted = renderToStaticMarkup(<ReviewBadge todo={{ ...todo, done_at: "2026-11-05T10:00:00Z", supervision: { ...todo.supervision!, state: "accepted" } }} />);
    expect(accepted).toContain("bg-green-tint text-green");
    const pending = renderToStaticMarkup(<ReviewBadge todo={{ ...todo, supervision: { ...todo.supervision!, state: "needs_user" } }} />);
    expect(pending).toContain("bg-field text-orange");
    expect(pending).not.toContain("bg-orange-tint");
  });

  it("shows its review state and the criteria met on its card", () => {
    const html = renderToStaticMarkup(<ReviewBadge todo={todo} />);
    expect(html).toContain("第 1 轮返工");
    expect(html).toContain("1/2");
    expect(renderToStaticMarkup(<ReviewBadge todo={{ ...todo, supervision: undefined }} />)).toBe("");
  });

  it("lists each criterion with its evidence or gap on its page", () => {
    const html = renderToStaticMarkup(<ReviewChecklist todo={todo} />);
    expect(html).toContain("验收标准");
    expect(html).toContain("预计 30 分钟");
    expect(html).toContain('aria-label="已达成"');
    expect(html).toContain('aria-label="未达成"');
    expect(html).toContain("只有浅色");
    expect(html).toContain("督办说明：补深色截图");
  });

  it("keeps the existing unchecked minus for a legacy criterion added after acceptance", () => {
    const html = renderToStaticMarkup(<ReviewChecklist todo={{ ...todo, supervision: {
      ...todo.supervision!, state: "accepted",
      reviews: [{ at: "2026-11-05T09:00:00Z", verdict: "accepted", checks: [{ criterion: todo.supervision!.done_when![0], met: true }] }],
    } }} />);
    expect(html).toContain('aria-label="验收后新增，未单独核对"');
    expect(html).toContain(renderToStaticMarkup(<Icon name="minus" size={13} stroke={2.4} />));
    expect(html).toContain("1/1");
  });

  it("shows the agent's latest report as it wrote it, Markdown rendered, on its page (#3181)", () => {
    const progress = { kind: "progress" as const, text: "Ran the **migration**: - 41 tables moved", line: "迁移跑完", detail: "Ran the **migration**:\n\n- 41 tables moved\n- `orders` needs a backfill", at: "2026-11-05T09:00:00Z" };
    const html = renderToStaticMarkup(<LatestReport todo={{ ...todo, supervision: { ...todo.supervision!, progress } }} />);
    expect(html).toContain('data-testid="latest-report"');
    expect(html).toContain("最新汇报");
    expect(html).toContain("<strong>migration</strong>");
    expect(html).toContain("<li>");
    expect(html).toContain("<code");
    expect(html).not.toContain("迁移跑完");
    // without a detail, the text; without a report, nothing
    const plain = renderToStaticMarkup(<LatestReport todo={{ ...todo, supervision: { ...todo.supervision!, progress: { ...progress, detail: undefined } } }} />);
    expect(plain).toContain("<strong>migration</strong>");
    expect(renderToStaticMarkup(<LatestReport todo={todo} />)).toBe("");
    // over the agent's chat in the task panel: open, with a toggle that folds it to its title line
    const banner = renderToStaticMarkup(<LatestReport todo={{ ...todo, supervision: { ...todo.supervision!, progress } }} banner />);
    expect(banner).toContain('aria-expanded="true"');
    expect(banner).toContain("<strong>migration</strong>");
    expect(renderToStaticMarkup(<LatestReport todo={todo} banner />)).toBe("");
  });

  it("says when no criteria were given", () => {
    const html = renderToStaticMarkup(<ReviewChecklist todo={{ ...todo, supervision: { supervisor: "main", done_when: [] } }} />);
    expect(html).toContain("未设置验收标准");
  });

  it("marks a line to a task that cut its turn short", () => {
    const say = (body: Record<string, unknown>): string =>
      renderToStaticMarkup(<FeedLineView item={{ id: "f3", conversation: "main", at: "2026-11-05T09:00:00Z", kind: "routed", ref: "todo_1", body }} />);
    expect(say({ text: "先修测试", interrupted: true })).toContain("已中断「");
    expect(say({ text: "先修测试" })).not.toContain("已中断「");
    expect(say({ text: "先修测试" })).toContain("对「");
  });

  it("shows the independent review's result under the criteria, and nothing when none ran", () => {
    expect(renderToStaticMarkup(<ReviewChecklist todo={todo} />)).not.toContain("auto-review");
    const reviewed = (auto_review: NonNullable<Todo["supervision"]>["auto_review"]): string =>
      renderToStaticMarkup(<ReviewChecklist todo={{ ...todo, supervision: { ...todo.supervision!, state: "pending_review", auto_review } }} />);
    const clean = reviewed({ id: "rv_1", state: "clean", count: 2, blocking: 0, findings: "P2 app.ts:3 命名" });
    expect(clean).toContain("独立评审");
    expect(clean).toContain("无阻断问题 · 2 条发现");
    expect(clean).toContain("展开报告");
    expect(clean).not.toContain("P2 app.ts:3"); // the report opens on a click
    expect(reviewed({ id: "rv_1", state: "blocking", count: 3, blocking: 1, findings: "P0 x" })).toContain("仍有 1 条阻断问题 · 3 条发现");
    expect(reviewed({ id: "rv_1", state: "running" })).toContain("进行中");
    expect(reviewed({ id: "rv_1", state: "failed" })).toContain("未完成评审");
    expect(reviewed({ id: "rv_1", state: "cancelled" })).not.toContain("auto-review");
  });

});
