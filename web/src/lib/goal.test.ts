import { describe, expect, it } from "vitest";
import { goalHeadline, goalStatusLabel, goalUsage } from "./goal";

describe("goal", () => {
  it("reads the status in words", () => {
    expect(goalStatusLabel({ objective: "x", status: "active" })).toBe("进行中");
    expect(goalStatusLabel({ objective: "x", status: "budget_limited" })).toBe("预算已用完");
    expect(goalStatusLabel({ objective: "x", status: "something_new" })).toBe("something_new");
  });
  it("takes the first non-empty line", () => {
    expect(goalHeadline({ objective: "\n  ship it  \nthen tell me", status: "active" })).toBe("ship it");
  });
  it("says what it spent", () => {
    expect(goalUsage({ objective: "x", status: "active" })).toBe("");
    expect(goalUsage({ objective: "x", status: "active", tokens_used: 12345, token_budget: 500000, seconds: 30 })).toBe("1.2万 / 50万 tokens · 1 分钟");
    expect(goalUsage({ objective: "x", status: "complete", seconds: 3600 + 120 })).toBe("1 小时 2 分");
  });
});
