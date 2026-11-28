// @vitest-environment jsdom
// Settings › General's assistant line with no computer (#4480): not the sentence with an empty computer and folder ("every chat runs in the background in  of  ."), and no folder path.
import { describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} });
});
import { assistantFooter } from "./Settings";

describe("assistantFooter", () => {
  it("says there is no computer yet when the hub has none, with no gaps", () => {
    const none = assistantFooter("", true)!;
    expect(none).toContain("尚未连接电脑");
    expect(none).not.toMatch(/在\s+的\s+里/);
  });

  it("names the computer, and never a folder path", () => {
    const on = assistantFooter("我的 MacBook", true)!;
    expect(on).toContain("我的 MacBook");
    expect(on).not.toContain("~/");
  });

  it("says nothing before the assistant is read", () => {
    expect(assistantFooter(undefined, false)).toBeUndefined();
  });
});
