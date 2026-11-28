import { describe, expect, it } from "vitest";
import { continuationLaunch, takeoverBody, type CodexTakeoverPlan } from "./codex-takeover";

const plan: CodexTakeoverPlan = {
  running: true, can_resume: true, token: "opaque-token", cwd: "/work", transcript_path: "/private/transcript",
  continuation_prompt: "encrypted continuation payload",
};

describe("Codex takeover", () => {
  it("does not produce a POST body for an unconfirmed running turn or a cancelled dialog", () => {
    expect(takeoverBody(plan, false)).toBeNull();
  });

  it("marks an explicitly confirmed running turn", () => {
    expect(takeoverBody(plan, true)).toEqual({ token: "opaque-token", confirm_stop: true });
  });

  it.each([false, true])("requires confirmation for an app-owned session (running=%s)", (running) => {
    const appPlan = { ...plan, running, app_owned: true };
    expect(takeoverBody(appPlan, false)).toBeNull();
    expect(takeoverBody(appPlan, true)).toEqual({ token: "opaque-token", confirm_stop: true });
  });

  it("allows an exited session to take over without confirmation", () => {
    expect(takeoverBody({ ...plan, running: false }, false)).toEqual({ token: "opaque-token", confirm_stop: false });
  });

  it("passes the continuation payload unchanged and keeps the original Todo", () => {
    expect(continuationLaunch(plan, "machine", "todo-original")).toEqual({
      machineId: "machine", cwd: "/work", launcher: "codex", agentType: "codex", inlinePrompt: true,
      text: "encrypted continuation payload", attachments: [], todoId: "todo-original",
    });
  });
});
