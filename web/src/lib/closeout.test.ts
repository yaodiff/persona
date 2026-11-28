import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { closeoutClaim, closeoutContradicted, closeoutLine, closeoutSuffix, shownCloseout } from "./closeout";
import { stripCloseout } from "./closeout-text";
import { sessionPreview } from "./format";
import { orphanedLine } from "./todos";
import type { Closeout, Session } from "./types";

const session = (o: Partial<Session>): Session => ({ machine_id: "m", session_id: "s", cwd: "/x", title: "修登录", status: "done", state: "idle", ...o }) as Session;
const done = (o: Partial<Closeout> = {}): Closeout => ({ status: "done", ...o });

describe("shownCloseout", () => {
  it("is the closeout of a stopped session", () => {
    expect(shownCloseout(session({ work: { phase: "done", closeout: done() } }))?.status).toBe("done");
    // a turn the hub could not call finished carries it with an empty phase
    expect(shownCloseout(session({ work: { phase: "", closeout: done({ merged: "yes", checked: { merged: "no" } }) } }))?.status).toBe("done");
  });
  it("is nothing while it works, without one, or with a status this page does not know", () => {
    expect(shownCloseout(session({ state: "working", work: { phase: "done", closeout: done() } }))).toBeUndefined();
    expect(shownCloseout(session({ work: { phase: "working", closeout: done() } }))).toBeUndefined();
    expect(shownCloseout(session({ work: { phase: "done", line: "做完了" } }))).toBeUndefined();
    expect(shownCloseout(session({ work: { phase: "done", closeout: { status: "paused" } } }))).toBeUndefined();
    expect(shownCloseout(undefined)).toBeUndefined();
  });
});

describe("closeoutClaim", () => {
  it("sets the agent's word against the hub's finding", () => {
    expect(closeoutClaim(done({ merged: "yes", checked: { merged: "yes" } }), "merged")).toEqual({ said: true, found: true, state: "confirmed" });
    expect(closeoutClaim(done({ merged: "yes", checked: { merged: "no" } }), "merged")).toEqual({ said: true, found: false, state: "contradicted" });
    expect(closeoutClaim(done({ merged: "no", checked: { merged: "yes" } }), "merged")?.state).toBe("contradicted");
    expect(closeoutClaim(done({ deployed: "no", checked: { merged: "yes" } }), "deployed")).toEqual({ said: false, state: "unchecked" });
    expect(closeoutClaim(done({ checked: { deployed: "no" } }), "deployed")).toEqual({ found: false, state: "found" });
  });
  it("is nothing when neither said nor found", () => {
    expect(closeoutClaim(done(), "merged")).toBeUndefined();
    expect(closeoutClaim(done({ merged: "n/a" }), "merged")).toBeUndefined();
    expect(closeoutContradicted(done({ merged: "yes" }))).toBe(false);
    expect(closeoutContradicted(done({ merged: "yes", deployed: "yes", checked: { merged: "yes", deployed: "no" } }))).toBe(true);
  });
});

describe("closeoutSuffix", () => {
  it("says where it stands on merged and deployed", () => {
    expect(closeoutSuffix(done({ merged: "yes", deployed: "no" }))).toBe("已合并 · 未部署");
    expect(closeoutSuffix(done({ merged: "yes", deployed: "yes", checked: { merged: "yes", deployed: "yes" } }))).toBe("已合并 ✓ · 已部署 ✓");
    expect(closeoutSuffix(done({ checked: { merged: "no" } }))).toBe("未合并");
    expect(closeoutSuffix(done())).toBe("");
  });
  it("marks a claim the hub found untrue", () => {
    expect(closeoutSuffix(done({ merged: "yes", deployed: "no", checked: { merged: "no" } }))).toBe("✗ 报告已合并，实际未合并 · 未部署");
    expect(closeoutSuffix(done({ merged: "yes", deployed: "yes", checked: { merged: "yes", deployed: "no" } }))).toBe("已合并 ✓ · ✗ 报告已部署，实际未部署");
    // it said no and the hub found yes: the finding
    expect(closeoutSuffix(done({ merged: "no", checked: { merged: "yes" } }))).toBe("已合并 ✓");
  });
});

describe("sessionPreview with a closeout", () => {
  it("keeps the hub's line and adds where the closeout stands", () => {
    const work = { phase: "done" as const, line: "报表导出改成流式", closeout: done({ merged: "yes", deployed: "no" }) };
    expect(sessionPreview(session({ last_text: "<closeout …>", work }))).toBe("✓ 报表导出改成流式 · 已合并 · 未部署");
    expect(sessionPreview(session({ work: { phase: "done", line: "只读，没有可发的", closeout: done() } }))).toBe("✓ 只读，没有可发的");
  });
  it("speaks from the closeout when the hub gave no line, never the raw block", () => {
    const raw = '<closeout status="done" merged="yes">\ndid: x\n</closeout>';
    expect(sessionPreview(session({ last_text: raw, work: { phase: "", closeout: done({ merged: "yes", checked: { merged: "no" } }) } }))).toBe("已报告完成 · ✗ 报告已合并，实际未合并");
    expect(closeoutLine({ status: "blocked", waiting: "DBA 批准" })).toBe("阻塞于：DBA 批准");
    expect(closeoutLine({ status: "blocked" })).toBe("已阻塞");
    expect(closeoutLine({ status: "not_done", missing: "分词库" })).toBe("未完成：分词库");
    expect(closeoutLine({ status: "not_done" })).toBe("未完成");
  });
  it("leaves a working session and what is more urgent alone", () => {
    const closeout = done({ merged: "yes" });
    expect(sessionPreview(session({ state: "working", status: "working", last_text: "在改", work: { phase: "working", closeout } }))).toBe("在改");
    expect(sessionPreview(session({ failure_reason: "本轮出错", work: { phase: "done", line: "做完了", closeout } }))).toBe("本轮出错");
  });
});

describe("stripCloseout", () => {
  const fixture = JSON.parse(readFileSync(new URL("../../../spec/fixtures/work-reports/terminal.json", import.meta.url), "utf8")) as {
    cases: { name: string; text: string; display: string }[];
  };
  it.each(fixture.cases)("shows the same reply above the card: $name", ({ text, display }) => {
    expect(stripCloseout(text)).toBe(display);
  });
  it("keeps a report from an incomplete reply visible", () => {
    const done = fixture.cases.find((c) => c.name === "done")!;
    expect(stripCloseout(done.text, false)).toBe(done.text);
  });
  const block = '<closeout status="done" merged="yes" deployed="n/a">\ndid: 改了导出\nverified: untested\n</closeout>';
  it("takes out exactly the block", () => {
    expect(stripCloseout(`修好了。\n\n${block}`)).toBe("修好了。");
    expect(stripCloseout(`${block}\n\n还有一件事。`)).toBe("还有一件事。");
    expect(stripCloseout(`前面。\n\n${block}\n\n后面。`)).toBe("前面。\n\n后面。");
    expect(stripCloseout(block)).toBe("");
    expect(stripCloseout(`  \n${block}\n`)).toBe("");
  });
  it("takes only the last block, the one the hub read", () => {
    const first = '<closeout status="not_done">\ndid: 草稿\n</closeout>';
    expect(stripCloseout(`${first}\n\n改完了。\n\n${block}`)).toBe(`${first}\n\n改完了。`);
  });
  it("takes a code fence that held only the block", () => {
    expect(stripCloseout(`修好了。\n\n\`\`\`xml\n${block}\n\`\`\``)).toBe("修好了。");
    expect(stripCloseout(`修好了。\n\n\`\`\`\n${block}\n\`\`\`\n\n完。`)).toBe("修好了。\n\n完。");
    // a fence that holds more keeps the rest
    expect(stripCloseout(`\`\`\`\n示例：\n${block}\n\`\`\``)).toBe("```\n示例：\n\n```");
  });
  it("leaves text without a block as it is", () => {
    expect(stripCloseout("没有收尾记录。  ")).toBe("没有收尾记录。  ");
    expect(stripCloseout('<closeout status="done"> 没有结尾')).toBe('<closeout status="done"> 没有结尾');
    expect(stripCloseout("")).toBe("");
  });
});

describe("orphanedLine", () => {
  it("says nobody is working on an open to-do, and why", () => {
    expect(orphanedLine({ orphaned: { reason: "error", since: "2026-11-10T00:00:00Z" } })).toEqual({ lead: "当前无人处理", reason: "出错后关闭" });
    expect(orphanedLine({ orphaned: { reason: "closed" } })?.reason).toBe("已关闭");
    expect(orphanedLine({ orphaned: { reason: "offline" } })?.reason).toBe("电脑离线");
    expect(orphanedLine({ orphaned: { reason: "moved" } })).toEqual({ lead: "当前无人处理", reason: "" });
  });
  it("is nothing for a done to-do or one the hub did not flag", () => {
    expect(orphanedLine({ orphaned: { reason: "error" }, done_at: "2026-11-10T00:00:00Z" })).toBeUndefined();
    expect(orphanedLine({})).toBeUndefined();
  });
});
