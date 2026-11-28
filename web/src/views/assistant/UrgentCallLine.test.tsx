import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { displayLines, type FeedItem } from "../../lib/feed";
import { callSummary, UrgentCallLine } from "./UrgentCallLine";
import { callItems, type UrgentCall } from "./urgent-calls";

// Urgent calls in a conversation (#8734, docs/assistant/urgent-call.md): a row per call at its time, folded to how it
// went, opening to why Persona called, what came of it and what was said. Made-up values; the tests run in zh-Hans.
describe("urgent call rows", () => {
  const ended: UrgentCall = {
    id: "call_1", title: "云账单扣费失控", why: "rule", rule: { id: "money_loss", text: "资金或资源正在损失" },
    happening: "8 台 GPU 实例近 1 小时扣了 2300 元", state: "ended", created_at: "2026-11-16T08:00:00Z", rang_at: "2026-11-16T08:00:01Z",
    seconds: 125, outcome: "现在停掉", transcript: "Persona：要不要现在停掉？\n你：现在停掉",
  };

  it("says how each call went", () => {
    expect(callSummary(ended)).toMatch(/^已接听 · \S/); // the duration as lib/format writes it
    expect(callSummary({ ...ended, state: "missed", attempt: 2 })).toBe("未接听（响铃 2 次）");
    expect(callSummary({ ...ended, state: "missed", attempt: 1 })).toBe("未接听");
    expect(callSummary({ ...ended, state: "declined" })).toBe("已拒接");
    expect(callSummary({ ...ended, state: "cancelled" })).toBe("已在别处处理");
    expect(callSummary({ ...ended, state: "answered", seconds: 0 })).toBe("已接听");
  });

  it("starts folded, and opens to the matter, the outcome and the transcript", () => {
    const [item] = callItems("conv", [ended]);
    const html = renderToStaticMarkup(<UrgentCallLine item={item} />);
    expect(html).toContain("加急来电：云账单扣费失控");
    expect(html).toContain('aria-expanded="false"');
    expect(html).not.toContain("你：现在停掉");
  });

  it("is a feed item at the time it rang, one that could not ring left out, placed among the lines by time", () => {
    const items = callItems("conv", [ended, { ...ended, id: "call_2", state: "failed" }]);
    expect(items.map((i) => [i.id, i.at, i.kind])).toEqual([["call-call_1", "2026-11-16T08:00:01Z", "urgent_call"]]);
    const lines = displayLines([], items as FeedItem[]);
    expect(lines.some((l) => l.kind === "feed" && l.item.id === "call-call_1")).toBe(true);
  });
});
