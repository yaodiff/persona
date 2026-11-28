import { afterEach, describe, expect, it } from "vitest";
import { initI18n } from "../i18n";
import { evidenceAuthorLabel } from "./evidence-author";
import { acceptanceStands, checkCount, checklist, latestReview, parkedLine, progressLine, reportLine, reportText, reviewBadge, reviewState, reviewVerdictLabel, type Supervision } from "./supervision";
import { makeSessionLookup, todoStatusLabel, type Todo } from "./todos";
import type { Session } from "./types";

const base: Todo = { id: "todo_1", title: "部署 hub", created_at: "2026-11-05T08:00:00Z", updated_at: "2026-11-05T08:00:00Z" };
const sup = (s: Partial<Supervision>): Supervision => ({ supervisor: "main", done_when: ["PR 合并到 master", "截图贴进 PR"], ...s });

describe("supervision", () => {
  it("clears pending review after completion and restores it when reopened", () => {
    const supervision = sup({ state: "pending_review" });
    expect(reviewState({ ...base, supervision })).toBe("pending_review");
    expect(reviewState({ ...base, supervision, done_at: "2026-11-12T09:00:00Z" })).toBe("");
    expect(reviewState({ ...base, supervision, done_at: null })).toBe("pending_review");
  });

  it("uses live per-criterion state ahead of stale reviews, in done_when order", () => {
    const s = sup({
      criteria: [
        { criterion: "截图贴进 PR", state: "not_met", evidence: "dark screenshot missing", at: "2026-11-10T10:00:00Z", by: "supervisor:main" },
        { criterion: "PR 合并到 master", state: "met", evidence: "pull/7 merged", at: "2026-11-10T09:00:00Z", by: "agent:box/s1" },
      ],
      reviews: [{ at: "old", verdict: "rework", checks: [{ criterion: "PR 合并到 master", met: false }] }],
    });
    expect(checklist(s).map((c) => [c.criterion, c.mark])).toEqual([["PR 合并到 master", "met"], ["截图贴进 PR", "missing"]]);
    expect(checklist(s)[0]).toMatchObject({ evidence: "pull/7 merged", at: "2026-11-10T09:00:00Z", by: "agent:box/s1" });
    expect(checkCount(s)).toBe("1/2");
    s.criteria![1].state = "pending";
    expect(checkCount(s)).toBe("0/2");
    expect(checklist(sup({ criteria: [] })).map((c) => c.mark)).toEqual(["open", "open"]);
    // Reviews may describe what a task was for when no done_when was defined.
    expect(checkCount(sup({ done_when: [], criteria: [], reviews: [
      { at: "old", verdict: "accepted", checks: [{ criterion: "answer delivered", met: true, evidence: "posted" }] },
    ] }))).toBe("1/1");
  });
  it("says a parked task is parked, until when and why (#3804)", () => {
    const parked = { reason: "设计组要先定图标", until: "2026-11-14T09:00:00Z", waiting_on: "someone_else" as const, count: 1 };
    const todo = { ...base, supervision: sup({ state: "parked", parked }) };
    expect(reviewState(todo)).toBe("parked");
    expect(reviewBadge(todo)).toEqual({ label: "已搁置", tone: "muted" });
    expect(parkedLine(todo)).toMatch(/^搁置至 .*2026.* · 设计组要先定图标$/);
    expect(reviewVerdictLabel("parked")).toBe("已搁置");
    // closed, or without what it waits for (its agent works again): not parked
    expect(reviewState({ ...todo, done_at: "2026-11-12T09:00:00Z" })).toBe("");
    expect(parkedLine({ ...todo, done_at: "2026-11-12T09:00:00Z" })).toBeUndefined();
    expect(reviewState({ ...base, supervision: sup({ state: "parked" }) })).toBe("");
    expect(parkedLine({ ...base, supervision: sup({}) })).toBeUndefined();
    expect(parkedLine(undefined)).toBeUndefined();
    // a time that cannot be read: the reason alone
    expect(parkedLine({ ...base, supervision: sup({ state: "parked", parked: { ...parked, until: "" } }) })).toBe("已搁置 · 设计组要先定图标");
  });

  it("says the agent's latest report in a line", () => {
    expect(progressLine(undefined)).toBeUndefined();
    expect(progressLine(sup({}))).toBeUndefined();
    expect(progressLine(sup({ progress: { kind: "progress", step: 3, steps: 5, text: "在跑迁移" } }))).toBe("第 3/5 步 · 在跑迁移");
    expect(progressLine(sup({ progress: { kind: "progress", step: 2, text: "在跑迁移" } }))).toBe("第 2 步 · 在跑迁移");
    expect(progressLine(sup({ progress: { kind: "progress", text: "在跑迁移" } }))).toBe("在跑迁移");
    expect(progressLine(sup({ progress: { kind: "question", step: 2, steps: 5, text: "要不要停写？" } }))).toBe("向 Persona 提问：要不要停写？");
    expect(reportLine("blocked", "等 CI")).toBe("遇到阻塞：等 CI");
    expect(reportLine("done", "PR 已合并")).toBe("回报已完成：PR 已合并");
  });

  it("ignores legacy Codex terminal startup notices stored in supervision progress (#14822)", () => {
    for (const text of [
      "Codex will start in the terminal to use the requested reasoning effort. Approvals and questions will use the terminal screen.",
      "为使用指定的推理强度，Codex 将通过终端启动。审批和提问将通过终端画面处理。",
      "指定された推論レベルを使うため、Codex はターミナルで起動します。承認や質問はターミナル画面を通じて行います。",
    ]) {
      const legacy = sup({ progress: { kind: "progress", text, line: "终端启动说明" } });
      expect(progressLine(legacy)).toBeUndefined();
      expect(reportText(legacy)).toBeUndefined();
    }
  });

  it("words the report in the hub's paraphrase when it has one, and keeps the original for the details (#3181)", () => {
    const progress = { kind: "progress" as const, step: 3, steps: 5, text: "Ran the **migration** on staging: 41 tables moved", line: "迁移跑完，41 张表已搬" };
    expect(progressLine(sup({ progress }))).toBe("第 3/5 步 · 迁移跑完，41 张表已搬");
    expect(progressLine(sup({ progress: { ...progress, kind: "question", line: "先灰度还是直接全量" } }))).toBe("向 Persona 提问：先灰度还是直接全量");
    // no line yet, no model, or an older hub: the agent's words, as before
    expect(progressLine(sup({ progress: { ...progress, line: undefined } }))).toBe("第 3/5 步 · Ran the **migration** on staging: 41 tables moved");
    expect(progressLine(sup({ progress: { ...progress, line: "  " } }))).toBe("第 3/5 步 · Ran the **migration** on staging: 41 tables moved");
    // the details: the whole report when the hub kept it apart, else the text
    expect(reportText(sup({ progress: { ...progress, detail: "Ran the **migration**:\n\n- 41 tables moved" } }))).toBe("Ran the **migration**:\n\n- 41 tables moved");
    expect(reportText(sup({ progress }))).toBe("Ran the **migration** on staging: 41 tables moved");
    expect(reportText(sup({}))).toBeUndefined();
  });

  it("has no badge for a task that is not supervised (an older hub)", () => {
    expect(reviewBadge(base)).toBeUndefined();
    expect(checklist(undefined)).toEqual([]);
  });

  it("names each state", () => {
    expect(reviewBadge({ ...base, supervision: sup({}) })?.label).toBe("督办中");
    expect(reviewBadge({ ...base, supervision: sup({ state: "pending_review" }) })).toEqual({ label: "待验收", tone: "accent" });
    expect(reviewBadge({ ...base, supervision: sup({ state: "rework", round: 2 }) })?.label).toBe("第 2 轮返工");
    expect(reviewBadge({ ...base, supervision: sup({ state: "needs_user" }) })?.tone).toBe("orange");
    expect(reviewBadge({ ...base, done_at: "2026-11-05T09:00:00Z", supervision: sup({ state: "accepted" }) })?.label).toBe("已验收");
  });

  it("goes on again when the user reopens an accepted task", () => {
    expect(reviewState({ done_at: null, supervision: sup({ state: "accepted" }) })).toBe("");
    expect(reviewState({ done_at: "2026-11-05T09:00:00Z", supervision: sup({ state: "accepted" }) })).toBe("accepted");
  });

  it("names a continuation's new round, not the acceptance in its history (#11553)", () => {
    const earlier = { at: "2026-11-20T02:23:57Z", verdict: "accepted" as const, checks: [{ criterion: "PR 合并到 master", met: true }] };
    // task_plan continues reopened the accepted to-do with a new Agent: the hub keeps the acceptance in the reviews and
    // puts the ledger back to going on, so the history alone never makes it accepted
    const continued = { ...base, done_at: null, reopened_at: "2026-11-20T02:35:24Z", supervision: sup({ state: "", round: 0, reviews: [earlier] }) };
    expect(acceptanceStands(continued)).toBe(false);
    expect(reviewState(continued)).toBe("");
    expect(reviewBadge(continued)).toEqual({ label: "督办中", tone: "muted" });
    // more work sent to the accepted task's Agent (#3156): the ledger still says accepted, and so does the badge
    const standing = { ...continued, supervision: sup({ state: "accepted", reviews: [earlier] }) };
    expect(acceptanceStands(standing)).toBe(true);
    expect(reviewBadge(standing)).toEqual({ label: "已验收", tone: "green" });
  });

  it("marks each criterion as its latest review found it", () => {
    const s = sup({
      reviews: [
        { at: "1", verdict: "rework", round: 1, checks: [{ criterion: "PR 合并到 master", met: true }, { criterion: "截图贴进 PR", met: false, evidence: "只有浅色" }] },
        { at: "2", verdict: "rework", round: 2, checks: [{ criterion: "截图贴进 PR", met: false, evidence: "深色的也糊了" }, { criterion: "另外的", met: true }] },
      ],
    });
    expect(checklist(s)).toEqual([
      { criterion: "PR 合并到 master", mark: "met", evidence: undefined },
      { criterion: "截图贴进 PR", mark: "missing", evidence: "深色的也糊了" },
      { criterion: "另外的", mark: "met", evidence: undefined },
    ]);
    expect(checkCount(s)).toBe("2/3");
    expect(latestReview(s)?.round).toBe(2);
    expect(checklist(sup({}))[0].mark).toBe("open");
    expect(checkCount(sup({ done_when: [] }))).toBe("");
  });

  it("labels a todo its supervisor closed", () => {
    const done = { ...base, done_at: "2026-11-05T09:00:00Z", done_by: "review" };
    expect(todoStatusLabel({ ...done, supervision: sup({ state: "accepted" }) }, "done")).toBe("已验收");
    expect(todoStatusLabel({ ...done, supervision: sup({ state: "cancelled" }) }, "done")).toBe("已取消");
    expect(todoStatusLabel({ ...base, done_at: "2026-11-05T09:00:00Z", done_by: "auto" }, "done")).toBe("自动完成");
  });
});

describe("evidenceAuthorLabel", () => {
  afterEach(async () => {
    await initI18n("zh-Hans");
  });

  const makeSession = (machine_id: string, session_id: string, extra: Partial<Session> = {}): Session =>
    ({ machine_id, session_id, title: "", cwd: "", status: "idle", ...extra }) as Session;

  it("resolves supervisor:<id> by direction name, then conversation title, then localized role label", () => {
    const directionNames = new Map([["dir-1", "发布方向"]]);
    const conversations = [
      makeSession("hub", "dir-1", { title: "旧对话标题" }),
      makeSession("hub", "conv-1", { title: "1105 | fix | 登录修复" }),
      makeSession("hub", "main-conv", { title: "随便什么首条消息" }),
      makeSession("hub", "deleted-conv", { title: "已删对话", deleted: true }),
      makeSession("hub", "other-mgr-conv", { title: "别的助手对话", manager: "mgr-b" }),
    ];
    const ctx = { directionNames, conversations, mainConversationId: "main-conv" };

    // Direction name takes precedence over conversation title.
    expect(evidenceAuthorLabel("supervisor:dir-1", ctx)).toBe("发布方向");
    // Falls back to conversation title when no direction name is cached.
    expect(evidenceAuthorLabel("supervisor:conv-1", ctx)).toBe("1105 | fix | 登录修复");
    // Main conversation resolves to MAIN_TITLE ("Persona").
    expect(evidenceAuthorLabel("supervisor:main-conv", ctx)).toBe("Persona");
    // Deleted, missing, or cross-manager conversations fall back to localized supervisor role.
    expect(evidenceAuthorLabel("supervisor:deleted-conv", ctx)).toBe("督办");
    expect(evidenceAuthorLabel("supervisor:other-mgr-conv", ctx)).toBe("督办");
    expect(evidenceAuthorLabel("supervisor:agy-231f5e13-deadbeef", ctx)).toBe("督办");
    expect(evidenceAuthorLabel("supervisor:", ctx)).toBe("督办");
  });

  it("resolves agent:<machine>/<session> via session lookup and sessionTitle, scoped to manager", () => {
    const lookup = makeSessionLookup([
      makeSession("m1", "s1", { title: "修复登录单测", cwd: "/work/app" }),
      makeSession("m1", "s2", { title: "", cwd: "/work/persona-web" }),
      makeSession("m1", "s-del", { title: "已删会话", deleted: true }),
      makeSession("m1", "s-other", { title: "另一个助手的会话", manager: "mgr-b" }),
    ]);

    expect(evidenceAuthorLabel("agent:m1/s1", { lookup })).toBe("修复登录单测");
    expect(evidenceAuthorLabel("agent:m1/s2", { lookup })).toBe("persona-web");
    // Deleted, missing, malformed, or out-of-scope sessions fall back to localized Agent role.
    expect(evidenceAuthorLabel("agent:m1/s-del", { lookup })).toBe("Agent");
    expect(evidenceAuthorLabel("agent:m1/s-other", { lookup })).toBe("Agent");
    expect(evidenceAuthorLabel("agent:m1/missing", { lookup })).toBe("Agent");
    expect(evidenceAuthorLabel("agent:malformed", { lookup })).toBe("Agent");
    expect(evidenceAuthorLabel("agent:/s1", { lookup })).toBe("Agent");
    expect(evidenceAuthorLabel("agent:m1/", { lookup })).toBe("Agent");
    // Same manager succeeds when todo.manager matches.
    expect(evidenceAuthorLabel("agent:m1/s-other", { lookup, manager: "mgr-b" })).toBe("另一个助手的会话");
  });

  it("falls back cleanly for judge, unknown formats, and empty values across en/zh-Hans/ja", async () => {
    expect(evidenceAuthorLabel(undefined)).toBeUndefined();
    expect(evidenceAuthorLabel("   ")).toBeUndefined();

    const expectations: Array<["en" | "zh-Hans" | "ja", string, string, string]> = [
      ["en", "Supervisor", "Agent", "Reviewer"],
      ["zh-Hans", "督办", "Agent", "评审"],
      ["ja", "監督", "エージェント", "レビュアー"],
    ];

    for (const [lang, supervisor, agent, reviewer] of expectations) {
      await initI18n(lang);
      expect(evidenceAuthorLabel("supervisor:agy-unknown-id")).toBe(supervisor);
      expect(evidenceAuthorLabel("agent:m1/unknown")).toBe(agent);
      expect(evidenceAuthorLabel("judge")).toBe(reviewer);
      expect(evidenceAuthorLabel("raw-internal-uuid-1234")).toBe(reviewer);
    }
  });
});
