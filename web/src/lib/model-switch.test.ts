import { afterEach, describe, expect, it, vi } from "vitest";
import type { AgentModel } from "./launcher-models";
import { canSwitchModel, isCurrentModel, isModelCommand, modelBusy, onModelPicker, requestModelPicker, sessionLauncher } from "./model-switch";
import type { Session } from "./types";

const session = (extra: Partial<Session> = {}): Session => ({ machine_id: "m", session_id: "s", status: "idle", agent: "claude", ...extra }) as Session;
const all = ["low", "medium", "high", "xhigh", "max"];
const models: AgentModel[] = [
  { id: "default", name: "Default", default: true, resolved: "claude-opus-5-5", efforts: all },
  { id: "opus", name: "Opus 5.5", resolved: "claude-opus-5-5", efforts: all },
  { id: "haiku", name: "Haiku 5.5", resolved: "claude-haiku-5-5" },
  { id: "gateway/glm", name: "glm", efforts: ["low", "high"] },
];

describe("which sessions can switch model", () => {
  it("is a Claude Code terminal session that takes messages", () => {
    expect(canSwitchModel(session())).toBe(true);
    expect(canSwitchModel(session({ agent: undefined }))).toBe(true);
  });

  it("is not Codex, a runtime's, a Persona conversation, an app-server thread or an offline session", () => {
    expect(canSwitchModel(undefined)).toBe(false);
    expect(canSwitchModel(session({ agent: "codex" }))).toBe(false);
    expect(canSwitchModel(session({ agent: "traex" }))).toBe(false);
    expect(canSwitchModel(session({ assistant: true }))).toBe(false);
    expect(canSwitchModel(session({ source_kind: "codex_app_server", capabilities: { send: true } as Session["capabilities"] }))).toBe(false);
    expect(canSwitchModel(session({ status: "offline" }))).toBe(false);
  });

  it("honors explicit runtime and Codex model capability and its launcher", () => {
    const agy = session({ agent: "agy", source_kind: "runtime", launcher: "agy", capabilities: { send: true, model: true } as Session["capabilities"] });
    expect(canSwitchModel(agy)).toBe(true);
    expect(sessionLauncher(agy)).toBe("agy");
    expect(canSwitchModel({ ...agy, capabilities: { ...agy.capabilities!, model: false } })).toBe(false);
    expect(canSwitchModel({ ...agy, status: "offline" })).toBe(false);
    expect(canSwitchModel({ ...agy, assistant: true })).toBe(false);

    const cx = session({ agent: "codex", source_kind: "codex_app_server", capabilities: { send: true, model: true } as Session["capabilities"] });
    expect(canSwitchModel(cx)).toBe(true);
    expect(canSwitchModel(session({ agent: "codex", space_kind: "codex_app", capabilities: { send: true, model: true } as Session["capabilities"] }))).toBe(true);

    // Live terminal agy session (no source_kind, no explicit launcher): honors capabilities.model and resolves launcher to "agy".
    const agyTerm = session({ agent: "agy", pane_id: "herdr:w1:p1", capabilities: { send: true, modes: false, model: true } as Session["capabilities"] });
    expect(canSwitchModel(agyTerm)).toBe(true);
    expect(sessionLauncher(agyTerm)).toBe("agy");
    expect(modelBusy({ ...agyTerm, status: "working" })).toContain("正在处理中");
    expect(modelBusy({ ...agyTerm, status: "working", background_waiting: true })).toBe("");
    expect(modelBusy({ ...agyTerm, state: "needs_you" })).toContain("正在处理中");
  });

  it("is busy while Claude works or waits on you, but Codex and plugin-backed Claude Desktop queue for the next step", () => {
    expect(modelBusy(session())).toBe("");
    expect(modelBusy(session({ status: "working" }))).toContain("正在处理中");
    expect(modelBusy(session({ state: "needs_you" }))).toContain("正在处理中");
    expect(modelBusy(session({ agent: "codex", status: "working" }))).toBe("");
    expect(modelBusy(session({ agent: "codex", state: "needs_you" }))).toBe("");
    expect(
      modelBusy(
        session({
          source_kind: "claude_desktop",
          status: "working",
          capabilities: { send: true, model: true, plugin_input: true } as Session["capabilities"],
        }),
      ),
    ).toBe("");
  });

  it("asks the launcher the session runs with, claude-desktop on desktop, plain claude without one", () => {
    expect(sessionLauncher({ launcher: "gateway" })).toBe("gateway");
    expect(sessionLauncher({})).toBe("claude");
    expect(sessionLauncher({ agent: "claude" })).toBe("claude");
    expect(sessionLauncher({ agent: "claude", agent_surface: "desktop" })).toBe("claude-desktop");
    expect(sessionLauncher({ agent: "claude", source_kind: "claude_desktop" })).toBe("claude-desktop");
    expect(sessionLauncher({ agent: "claude", space_kind: "claude_desktop" })).toBe("claude-desktop");
    expect(sessionLauncher({ agent: "claude", agent_surface: "desktop", launcher: "gateway" })).toBe("gateway");
  });

  it("asks a Codex session without a launcher its engine's, never Claude Code's (#6501)", () => {
    // a Codex App chat (the node names its space codex_app): the App's own launcher, whose list is empty and locked
    expect(sessionLauncher({ agent: "codex", source_kind: "codex_history", space_kind: "codex_app", agent_surface: "desktop" })).toBe("codex-app");
    // another Codex session: plain codex, whether the node names the agent or only its source
    expect(sessionLauncher({ agent: "codex" })).toBe("codex");
    expect(sessionLauncher({ source_kind: "codex_app_server" })).toBe("codex");
    expect(sessionLauncher({ source_kind: "codex_history", space_kind: "herdr" })).toBe("codex");
    // a launcher it names still wins
    expect(sessionLauncher({ agent: "codex", space_kind: "codex_app", launcher: "codex-gw" })).toBe("codex-gw");
  });
});

describe("the current model", () => {
  it("matches the list's id or what it stands for, ignoring case", () => {
    expect(isCurrentModel(models[1], "opus")).toBe(true);
    expect(isCurrentModel(models[1], "Claude-Opus-5-5")).toBe(true);
    expect(isCurrentModel(models[2], "claude-opus-5-5")).toBe(false);
    expect(isCurrentModel(models[1], undefined)).toBe(false);
    expect(isCurrentModel(models[1], "")).toBe(false);
  });
  it("folds a gateway's routed echo and the 1M marker", () => {
    const carbon = [{ id: "gemini-4-carbon", name: "Gemini 4 Carbon" }];
    expect(isCurrentModel(carbon[0], "passthrough/route-gemini-4-carbon/gemini-4-carbon")).toBe(true);
    expect(isCurrentModel(carbon[0], "gemini-4-carbon[1m]")).toBe(true);
    expect(isCurrentModel({ id: "gemini-4-carbon[1m]", name: "Gemini 4 Carbon" }, "gemini-4-carbon")).toBe(true);
    expect(isCurrentModel(carbon[0], "passthrough/x/kimi-k3.1-pro")).toBe(false);
  });
});

describe("the /model command", () => {
  it("is /model alone", () => {
    expect(isModelCommand("/model")).toBe(true);
    expect(isModelCommand("/model ")).toBe(true);
    expect(isModelCommand("  /model  ")).toBe(true);
    expect(isModelCommand("/model opus")).toBe(false);
    expect(isModelCommand("/models")).toBe(false);
    expect(isModelCommand("use /model")).toBe(false);
  });
});

describe("opening the panel from the composer", () => {
  afterEach(() => vi.restoreAllMocks());

  it("reaches the conversation's pill and nobody else's", () => {
    const open = vi.fn();
    const off = onModelPicker("m/s", open);
    expect(requestModelPicker("m/other")).toBe(false);
    expect(requestModelPicker("m/s")).toBe(true);
    expect(open).toHaveBeenCalledTimes(1);
    off();
    expect(requestModelPicker("m/s")).toBe(false); // an agent without a switch: the composer sends as before
  });

  it("keeps the newer pill when an older one unsubscribes late", () => {
    const a = vi.fn(), b = vi.fn();
    const offA = onModelPicker("m/s", a);
    const offB = onModelPicker("m/s", b);
    offA();
    expect(requestModelPicker("m/s")).toBe(true);
    expect(b).toHaveBeenCalled();
    offB();
  });
});
