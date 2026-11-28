import { describe, expect, it } from "vitest";
import { hasLoop, isLoopDraft, loopHint } from "./loop-command";

describe("/loop in the composer", () => {
  it("knows a /loop draft from other words", () => {
    expect(isLoopDraft("/loop")).toBe(true);
    expect(isLoopDraft("/loop ")).toBe(true);
    expect(isLoopDraft("/LOOP 5m x")).toBe(true);
    expect(isLoopDraft("/looping")).toBe(false);
    expect(isLoopDraft("看下 /loop")).toBe(false);
  });

  it("says what is missing", () => {
    expect(loopHint("/loop ")).toEqual({ missing: true, text: expect.stringContaining("/loop 10m 检查 CI") });
    expect(loopHint("/loop 10m")).toEqual({ missing: true, text: "每 10 分钟：尚未填写任务内容" });
  });

  it("says what it will repeat and how often", () => {
    expect(loopHint("/loop 10m 看下 CI")).toEqual({ missing: false, text: "每 10 分钟重复：看下 CI" });
    expect(loopHint("/loop 2H  跑一遍测试")).toEqual({ missing: false, text: "每 2 小时重复：跑一遍测试" });
    expect(loopHint("/loop 30s ping")?.text).toBe("每 30 秒重复：ping");
    expect(loopHint("/loop 看下 CI 有没有过")).toEqual({ missing: false, text: "由 Claude 自动决定节奏重复：看下 CI 有没有过" });
  });

  it("leaves other drafts alone", () => {
    expect(loopHint("继续")).toBeNull();
    expect(loopHint("/compact")).toBeNull();
  });

  it("is offered to Claude agents only", () => {
    expect(hasLoop({})).toBe(true);
    expect(hasLoop({ agent: "claude" })).toBe(true);
    expect(hasLoop({ agent: "codex" })).toBe(false);
    expect(hasLoop({ agent: "traex" })).toBe(false);
    expect(hasLoop({ source_kind: "codex_app_server" })).toBe(false);
    expect(hasLoop(undefined)).toBe(false);
  });
});
