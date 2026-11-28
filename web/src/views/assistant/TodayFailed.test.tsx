// The "Today" card's row for what did not go through overnight (#4124): one by its title, how it ended and why; several by
// how many (failed_total, past the hub's newest 8) and the first titles. A hub before it sends nothing: the card is as it was.
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { formatDate } from "../../i18n";
import { todayFailedRow, type BriefFailed, type FeedItem, type TodayBody } from "../../lib/feed";
import { FeedLineView } from "./Tasks";

const now = new Date().toISOString();
const at = "2026-11-03T03:12:00+08:00";
const time = formatDate(at, { hour: "2-digit", minute: "2-digit" });
const task: BriefFailed = { kind: "task", id: "todo_task_align", title: "设置页按钮对齐", at, why: "rpc: connection closed" };
const run: BriefFailed = { kind: "routine", id: "rt_weekly", title: "周报", at, why: " " };
const skipped: BriefFailed = { kind: "routine", id: "evening", title: "晚间回顾", at, skipped: true };
const today = (body: TodayBody): FeedItem => ({ id: "f1", conversation: "c1", at: now, kind: "today", body: body as unknown as Record<string, unknown> });
const card = (body: TodayBody) => renderToStaticMarkup(<FeedLineView item={today(body)} />);

describe("the 今天 card's row for what did not go through", () => {
  it("is not there when nothing failed, or from a hub before it", () => {
    const plain: TodayBody = { date: now, working: 1 };
    expect(todayFailedRow(plain)).toBeUndefined();
    expect(todayFailedRow({ ...plain, failed: [], failed_total: 0 })).toBeUndefined();
    expect(card({ ...plain, failed: [], failed_total: 0 })).toBe(card(plain));
    expect(card(plain)).not.toContain("未成功");
  });

  it("says one by its title, how it ended and why", () => {
    expect(todayFailedRow({ date: now, failed: [task], failed_total: 1 })).toMatchObject({ title: "未成功：设置页按钮对齐", detail: `未能启动 · ${time} — rpc: connection closed` });
    expect(todayFailedRow({ date: now, failed: [run] })?.detail).toBe(`运行失败 · ${time}`);
    expect(todayFailedRow({ date: now, failed: [skipped] })?.detail).toBe(`已跳过 · ${time}`);
    const html = card({ date: now, failed: [task], failed_total: 1 });
    expect(html).toContain("未成功：设置页按钮对齐");
    expect(html).toContain(`未能启动 · ${time} — rpc: connection closed`);
    expect(html).toContain("查看");
  });

  it("says several by how many in all and the first titles", () => {
    const row = todayFailedRow({ date: now, failed: [run, skipped, task], failed_total: 5 });
    expect(row).toMatchObject({ title: "5 项未成功", detail: "周报、晚间回顾、设置页按钮对齐 · 还有 2 项" });
    expect(row?.first).toBe(run);
    expect(todayFailedRow({ date: now, failed: [run, task] })).toMatchObject({ title: "2 项未成功", detail: "周报、设置页按钮对齐" });
    expect(card({ date: now, failed: [run, skipped, task], failed_total: 5 })).toContain("5 项未成功");
  });
});
