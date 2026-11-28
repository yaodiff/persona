import { describe, expect, it } from "vitest";
import { contextLabel, contextTier, contextTooltip, contextUsage, contextWarn, modelShort, rowDetail } from "./context-usage";
import { contextNotes } from "./context-notes";
import type { Session } from "./types";

const base: Session = { machine_id: "m", session_id: "s", title: "t", cwd: "/code/weather", status: "idle" };

describe("contextUsage", () => {
  it("is null while the node cannot tell", () => {
    expect(contextUsage(undefined)).toBeNull();
    expect(contextUsage(base)).toBeNull();
    expect(contextUsage({ context_tokens: 5000 })).toBeNull(); // a model whose window is unknown
    expect(contextUsage({ context_window: 200000 })).toBeNull();
  });
  it("rounds to whole percents and clamps", () => {
    expect(contextUsage({ context_tokens: 78000, context_window: 200000 })).toMatchObject({ used: 39, left: 61 });
    expect(contextUsage({ context_tokens: 250000, context_window: 200000 })).toMatchObject({ used: 100, left: 0 });
  });
});

describe("labels", () => {
  const u = contextUsage({ context_tokens: 78000, context_window: 200000 })!;
  it("labels the used share and explains both used and remaining shares", () => {
    expect(contextLabel(u)).toBe("上下文 39%");
    expect(contextTooltip(u)).toBe("上下文已用 39%，剩余 61%");
  });
  it("warns from 80% used", () => {
    expect(contextWarn(contextUsage({ context_tokens: 158000, context_window: 200000 })!)).toBe(false);
    expect(contextWarn(contextUsage({ context_tokens: 160000, context_window: 200000 })!)).toBe(true);
  });
  it("has three tiers: ok below 80% used, warn from 80%, danger from 90% (10% or less left)", () => {
    const tier = (tokens: number) => contextTier(contextUsage({ context_tokens: tokens, context_window: 200000 })!);
    expect(tier(158000)).toBe("ok"); // 79%
    expect(tier(160000)).toBe("warn"); // 80%
    expect(tier(178000)).toBe("warn"); // 89%
    expect(tier(180000)).toBe("danger"); // 90%, 10% left
    expect(tier(200000)).toBe("danger");
  });
});

describe("modelShort", () => {
  it("shortens Claude ids and keeps the rest", () => {
    expect(modelShort("claude-opus-5-5")).toBe("opus 5.5");
    expect(modelShort("claude-sonnet-4-5-20250929")).toBe("sonnet 4.5");
    expect(modelShort("claude-opus-5-5[1m]")).toBe("opus 5.5 1M");
    expect(modelShort("opus")).toBe("opus");
    expect(modelShort("gpt-5.5")).toBe("gpt-5.5");
    expect(modelShort(undefined)).toBe("");
  });
  it("keeps a gateway's routing prefix intact", () => {
    expect(modelShort("passthrough/route-gemini-4-carbon/gemini-4-carbon")).toBe("passthrough/route-gemini-4-carbon/gemini-4-carbon");
    expect(modelShort("route/kimi-k3.1-pro")).toBe("route/kimi-k3.1-pro");
    expect(modelShort("passthrough/claude-opus-5-5")).toBe("passthrough/claude-opus-5-5");
    expect(modelShort("passthrough/claude-opus-5-5[1m]")).toBe("passthrough/claude-opus-5-5[1m]");
  });
});

describe("rowDetail", () => {
  const now = Date.parse("2026-11-05T12:00:00Z");
  it("lists folder, model, context and age, leaving out what is unknown", () => {
    const s = { ...base, model: "claude-opus-5-5", context_tokens: 78000, context_window: 200000, updated_at: "2026-11-05T11:55:00Z" };
    expect(rowDetail(s, now).map((p) => p.text)).toEqual(["上下文 39%", "opus 5.5", "5分钟前", "weather"]);
    expect(rowDetail(s, now)[0]).toMatchObject({ text: "上下文 39%", warn: false, title: "上下文已用 39%，剩余 61%" });
    expect(rowDetail(base, now).map((p) => p.text)).toEqual(["weather"]);
  });
  it("marks a nearly full window", () => {
    const s = { ...base, context_tokens: 190000, context_window: 200000 };
    expect(rowDetail(s, now).find((p) => p.warn)?.text).toBe("上下文 95%");
    expect(rowDetail(s, now).find((p) => p.warn)?.danger).toBe(true);
  });
  it("marks 80–89% used as a warning but not a danger", () => {
    const s = { ...base, context_tokens: 170000, context_window: 200000 };
    expect(rowDetail(s, now).find((p) => p.warn)).toMatchObject({ text: "上下文 85%", warn: true, danger: false });
  });
});

describe("the header's popover (#4054)", () => {
  it("says the share with the tokens, then what happens when it fills; only notes, nothing to choose", () => {
    const notes = contextNotes(contextUsage({ context_tokens: 78000, context_window: 200000 })!);
    expect(notes.map((n) => [n.label, n.hint])).toEqual([
      ["上下文已用 39%，剩余 61%", "7.8 万 / 20 万 token"], // the token count as the compaction divider writes it
      ["上下文表示 Agent 在当前对话中能够记住的内容容量。用满后会自动压缩，较早的对话内容将仅保留摘要。", undefined],
    ]);
    expect(notes.every((n) => n.note)).toBe(true);
    const near = contextNotes(contextUsage({ context_tokens: 170000, context_window: 200000 })!);
    expect(near[0].label).toBe("上下文已用 85%，剩余 15%");
    expect(near[1].label).toContain("/compact");
  });
});
