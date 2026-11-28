import { describe, expect, it } from "vitest";
import { buildAgentGroups } from "./agent-groups";
import { sessionPreview } from "./format";
import type { Session, WorkState } from "./types";
import { hubWaits, workLine } from "./work";

const now = Date.parse("2026-11-05T10:00:00Z");
const at = (min: number) => new Date(now + min * 60_000).toISOString();
const session = (o: Partial<Session>): Session => ({ machine_id: "m", session_id: "s", cwd: "/x", title: "修登录", status: "done", state: "idle", ...o }) as Session;

describe("workLine", () => {
  it("says what a done turn concluded", () => {
    expect(workLine(session({ work: { phase: "done", line: "已合并 PR #12" } }), now)).toBe("✓ 已合并 PR #12");
  });
  it("says what it waits for, and when the hub acts", () => {
    expect(workLine(session({ work: { phase: "blocked", on: "user", line: "等你选 D2 / D4" } }), now)).toBe("等你选 D2 / D4");
    expect(workLine(session({ work: { phase: "blocked", on: "external", line: "等部署跑完", wait: "部署", wake_at: at(12) } }), now)).toBe("等部署跑完（12 分钟后唤醒）");
    expect(workLine(session({ work: { phase: "blocked", on: "transient", line: "模型服务暂时出错", wake_at: at(0.5) } }), now)).toBe("模型服务暂时出错（即将重试）");
    expect(workLine(session({ work: { phase: "blocked", on: "external", line: "等", wake_at: at(65) } }), now)).toBe("等（1 小时 5 分后唤醒）");
  });
  it("is nothing while it works or before the hub triaged it", () => {
    expect(workLine(session({ state: "working", status: "working", work: { phase: "working" } }), now)).toBeUndefined();
    expect(workLine(session({}), now)).toBeUndefined();
    expect(workLine(session({ work: { phase: "done" } }), now)).toBeUndefined();
  });
});

describe("sessionPreview with a work state", () => {
  it("prefers the work line over the last words", () => {
    expect(sessionPreview(session({ last_text: "很长的最后一段话", work: { phase: "done", line: "做完了" } }))).toBe("✓ 做完了");
    expect(sessionPreview(session({ last_text: "很长的最后一段话" }))).toBe("很长的最后一段话");
  });
});

describe("hubWaits", () => {
  const w = (o: Partial<WorkState>): WorkState => ({ phase: "blocked", ...o });
  it("is a wait that ends without the user", () => {
    expect(hubWaits(w({ on: "external", wait: "部署", wake_at: at(5) }))).toBe(true);
    expect(hubWaits(w({ on: "external", line: "后台 2 个任务在跑" }))).toBe(true);
    expect(hubWaits(w({ on: "external", wait: "部署" }))).toBe(false); // nobody will wake it
    expect(hubWaits(w({ on: "user", wake_at: at(5) }))).toBe(false);
    expect(hubWaits({ phase: "done" })).toBe(false);
    expect(hubWaits(undefined)).toBe(false);
  });
  it("keeps the waiting detail within Idle", () => {
    const groups = buildAgentGroups([session({ work: { phase: "blocked", on: "external", wait: "部署", wake_at: at(5), line: "等部署" } })], [{ id: "m", name: "m", online: true } as never], "state");
    expect(groups.map((g) => g.name)).toEqual(["空闲"]);
  });
});
