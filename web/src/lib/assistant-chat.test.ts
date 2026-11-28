import { describe, expect, it } from "vitest";
import launcherFixture from "../../../spec/fixtures/assistant-launchers/cases.json";
import { assistantInfoText, conversationKey, effectiveLauncher, launcherOffered, NEW_CONVERSATION, pickedLauncher, pickLauncher, runsAssistant, shortModel } from "./assistant-chat";

describe("the assistant's launchers", () => {
  it("runs on Claude Code or Codex, not TRAE CLI or other agents", () => {
    expect(runsAssistant({ name: "claude" })).toBe(true);
    expect(runsAssistant({ name: "claude-fast", agent: "claude" })).toBe(true);
    expect(runsAssistant({ name: "codex", agent: "codex" })).toBe(true);
    expect(runsAssistant({ name: "agy", agent: "agy", assistant: true })).toBe(true);
    expect(runsAssistant({ name: "agy", source: "zshrc", agent: "agy" })).toBe(false);
    expect(runsAssistant({ name: "omp", source: "zshrc", agent: "omp" })).toBe(false);
    expect(runsAssistant({ name: "traex", base_url: "(traex)" })).toBe(false);
    expect(runsAssistant({ name: "dsh", agent: "dsh" })).toBe(false);
  });

  it("keeps managed native engines and requires an assistant contract from runtime providers", () => {
    expect(runsAssistant({ name: "cc-relay", source: "paseo" })).toBe(false);
    expect(runsAssistant({ name: "agy-rt", source: "agy", agent: "agy", assistant: true })).toBe(true);
    expect(runsAssistant({ name: "codex-gw", source: "jarvis", agent: "codex" })).toBe(true);
    expect(runsAssistant({ name: "codex-gw", source: "persona", agent: "codex" })).toBe(true);
    expect(runsAssistant({ name: "gw-claude", source: "jarvis" })).toBe(true);
    expect(runsAssistant({ name: "codex", source: "zshrc", agent: "codex" })).toBe(true);
    expect(runsAssistant({ name: "claude", source: "builtin" })).toBe(true);
  });

  it("matches the shared assistant catalog without dropping managed Codex", () => {
    for (const fixture of launcherFixture.cases) {
      expect(fixture.candidates.filter(runsAssistant).map((l) => l.name)).toEqual(fixture.assistant);
    }
    expect(runsAssistant({ name: "provider", source: "persona", agent: "claude", agent_type: "unknown" })).toBe(false);
    expect(runsAssistant({ name: "provider", source: "persona", agent_type: "codex" })).toBe(true);
  });

  it("drops picks only against a loaded list, an empty one included", () => {
    expect(launcherOffered(null, "codex-gw")).toBe(true);
    expect(launcherOffered([], "codex-gw")).toBe(false);
    expect(launcherOffered([], "claude")).toBe(true);
    expect(launcherOffered([{ name: "gw" }], "gw")).toBe(true);
    expect(launcherOffered([{ name: "gw" }], "codex-gw")).toBe(false);
  });

  it("follows 设置 on the main conversation, the conversation's own elsewhere, a pick over both", () => {
    expect(effectiveLauncher({ isMain: true, session: "old", config: "claude-fast" })).toBe("claude-fast");
    expect(effectiveLauncher({ isMain: true, picked: "codex", session: "old", config: "claude-fast" })).toBe("codex");
    expect(effectiveLauncher({ session: "old", config: "claude-fast" })).toBe("old");
    expect(effectiveLauncher({ picked: "codex", session: "old", config: "claude-fast" })).toBe("codex");
    expect(effectiveLauncher({ config: "claude-fast" })).toBe("claude-fast");
    expect(effectiveLauncher({})).toBe("claude");
    expect(effectiveLauncher({ isNew: true, session: "x", config: "c" })).toBe("c");
    expect(effectiveLauncher({ isNew: true, picked: "p", config: "c" })).toBe("p");
  });

  it("keeps a pick per conversation until it is cleared", () => {
    const k = conversationKey("hub", "s1");
    expect(pickedLauncher(k)).toBeUndefined();
    pickLauncher(k, "codex");
    pickLauncher(NEW_CONVERSATION, "claude-fast");
    expect(pickedLauncher(k)).toBe("codex");
    expect(pickedLauncher(conversationKey("hub", "s2"))).toBeUndefined();
    pickLauncher(k, undefined);
    expect(pickedLauncher(k)).toBeUndefined();
    expect(pickedLauncher(NEW_CONVERSATION)).toBe("claude-fast");
  });

  it("shortens a model to its last part and makes GPT variants readable", () => {
    expect(shortModel("vendor/model-x")).toBe("model-x");
    expect(shortModel("vendor/gpt-6.1-sol")).toBe("GPT-6.1 Sol");
    expect(shortModel("gpt-6.1")).toBe("GPT-6.1");
    expect(shortModel("gpt-4o-mini")).toBe("GPT-4o Mini");
    expect(shortModel("claude-opus-5-5")).toBe("claude-opus-5-5");
    expect(shortModel("unknown-name")).toBe("unknown-name");
    expect(shortModel("gpt-")).toBe("gpt-");
    expect(shortModel("")).toBe("");
    expect(shortModel(undefined)).toBe("");
    expect(shortModel("默认")).toBe(""); // i18n-ignore: legacy wire value, never displayed
  });
});

describe("复制信息", () => {
  it("says which conversation, where and how to go on with it", () => {
    const t = assistantInfoText({ title: "周报", isMain: true, sessionId: "s1", machine: "服务器", machineId: "srv", cwd: "~/.persona/assistant", launcher: "claude", model: "model-x" });
    expect(t.split("\n")).toEqual([
      "Persona 助手的对话 主对话 「周报」",
      "会话 ID：s1",
      "所在机器：服务器（srv）",
      "工作目录：~/.persona/assistant",
      "启动器：claude · 模型：model-x",
      "在该机器上继续对话：cd ~/.persona/assistant && claude --resume s1",
    ]);
    const plain = assistantInfoText({ isMain: false, sessionId: "s2", machine: "srv", machineId: "srv", cwd: "/a", launcher: "codex" });
    expect(plain.split("\n").slice(0, 3)).toEqual(["Persona 助手的对话", "会话 ID：s2", "所在机器：srv"]);
    expect(plain).toContain("启动器：codex\n");
    const spaced = assistantInfoText({ isMain: false, sessionId: "s3", machine: "srv", machineId: "srv", cwd: "/Users/me/My Assistant; x", launcher: "claude" });
    expect(spaced.split("\n").pop()).toBe("在该机器上继续对话：cd '/Users/me/My Assistant; x' && claude --resume s3");
  });
});
