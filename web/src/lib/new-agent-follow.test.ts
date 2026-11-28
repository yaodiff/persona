import { describe, expect, it } from "vitest";
import { formatFollowDetail, resolveFollowBinding, resolvedStartModel } from "./new-agent-follow";
import { launchDisplayEngine, launchModelForSession, type AgentLaunch } from "./new-agent";
import type { LauncherModels } from "./launcher-models";

const makeLaunch = (overrides: Partial<AgentLaunch> = {}): AgentLaunch => ({
  id: "local-1",
  machineId: "mac",
  cwd: "/code/app",
  launcher: "",
  text: "",
  attachments: [],
  phase: "creating",
  uploaded: {},
  startOpId: "op-1",
  startPromptOpId: "op-2",
  messageId: "msg-1",
  ...overrides,
});

describe("resolveFollowBinding", () => {
  it("uses hubDefault as the authoritative Follow-mode binding when it disagrees with purpose.effective", () => {
    const resolved = resolveFollowBinding(
      { provider: "claude", model: "deepseek-v4.1-flash", effort: "max" },
      { provider: "launcher.agy", model: "gemini-4-neon-high", effort: "high" },
    );
    expect(resolved).toEqual({
      provider: "launcher.claude",
      model: "deepseek-v4.1-flash",
      effort: "max",
    });
  });

  it("keeps an already-prefixed provider id and permission_mode intact", () => {
    expect(
      resolveFollowBinding(
        { provider: "launcher.claude", model: "deepseek-v4.1-flash", permission_mode: "plan" },
        null,
      ),
    ).toEqual({
      provider: "launcher.claude",
      model: "deepseek-v4.1-flash",
      permission_mode: "plan",
    });
  });

  it("falls back to configEffective when hubDefault is absent (older hubs)", () => {
    expect(
      resolveFollowBinding(undefined, { provider: "launcher.claude", model: "opus", effort: "high" }),
    ).toEqual({ provider: "launcher.claude", model: "opus", effort: "high" });
    expect(resolveFollowBinding(undefined, null)).toBeNull();
  });

  it("returns an empty binding when hubDefault is explicitly null on a unified hub", () => {
    expect(resolveFollowBinding(null, { provider: "launcher.agy" })).toEqual({});
    expect(resolveFollowBinding(null, null)).toBeNull();
  });
});

describe("formatFollowDetail", () => {
  it("formats provider, model, effort, and permission mode from the unified binding", () => {
    const list: LauncherModels = {
      agent: "claude",
      models: [{ id: "deepseek-v4.1-flash", name: "deepseek-v4.1-flash", efforts: ["low", "high", "max"] }],
    };
    const detail = formatFollowDetail(
      { provider: "launcher.claude", model: "deepseek-v4.1-flash", effort: "max" },
      "claude",
      list,
      [{ name: "claude" }],
    );
    expect(detail).toBe("Claude Code · deepseek-v4.1-flash · Max");
  });
});

describe("resolvedStartModel", () => {
  const list: LauncherModels = {
    agent: "claude",
    models: [
      { id: "default", name: "Default (recommended)", default: true, resolved: "deepseek-v4.1-flash" },
      { id: "sonnet", name: "Sonnet 5" },
    ],
  };

  it("prefers explicit pick, then followed model, then default entry resolved id, then launcher model", () => {
    expect(resolvedStartModel({ model: "sonnet", followed: { model: "deepseek-v4.1-flash" }, list })).toBe("sonnet");
    expect(resolvedStartModel({ followed: { model: "deepseek-v4.1-flash" }, list })).toBe("deepseek-v4.1-flash");
    expect(resolvedStartModel({ followed: {}, list })).toBe("deepseek-v4.1-flash");
    expect(resolvedStartModel({ followed: {}, launcher: { model: "glm-5" }, list: { agent: "claude", models: [] } })).toBe("glm-5");
    expect(resolvedStartModel({ followed: { model: "other" }, launcher: { model: "fixed-m" }, list: { ...list, locked: "fixed" } })).toBe("fixed-m");
  });
});

describe("launchDisplayEngine & launchModelForSession", () => {
  it("resolves the starting placeholder engine from displayEngine, agentType, or known launcher name", () => {
    expect(launchDisplayEngine(makeLaunch({ displayEngine: "claude", displayLauncher: "claude" }))).toBe("claude");
    expect(launchDisplayEngine(makeLaunch({ agentType: "codex" }))).toBe("codex");
    expect(launchDisplayEngine(makeLaunch({ launcher: "agy" }))).toBe("agy");
    expect(launchDisplayEngine(makeLaunch({ launcher: "" }))).toBeUndefined();
  });

  it("supplies the advertised follow model for a newly started session until session.model arrives", () => {
    const launches = {
      "local-1": makeLaunch({
        phase: "ready",
        launcher: "",
        displayLauncher: "claude",
        displayEngine: "claude",
        displayModel: "deepseek-v4.1-flash",
        session: { machine_id: "mac", session_id: "s-1", title: "New", cwd: "/code/app", status: "idle", launcher: "claude" },
      }),
    };
    expect(
      launchModelForSession(launches, { machine_id: "mac", session_id: "s-1", launcher: "claude", model: undefined }),
    ).toBe("deepseek-v4.1-flash");
    // Once the server/transcript reports a model, yield to it.
    expect(
      launchModelForSession(launches, { machine_id: "mac", session_id: "s-1", launcher: "claude", model: "sonnet" }),
    ).toBeUndefined();
    // If the launched session actually ran a different launcher, do not force the old model label.
    expect(
      launchModelForSession(launches, { machine_id: "mac", session_id: "s-1", launcher: "agy", model: undefined }),
    ).toBeUndefined();
  });
});
