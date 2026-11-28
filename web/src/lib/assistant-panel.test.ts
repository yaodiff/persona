// A Persona chat's model pill and panel (#5033, docs/web/web.md "Model pill and model panel"): where the setup the next
// message runs on comes from (its kept pick, the default, what a side chat started with, this page's pick on an older hub
// or the new-conversation page), what the pill says, the panel's providers (what can run Persona chats, with their
// lists; what can't, off with the hub's reason; another engine than a running chat's, off) and what a pick keeps.
import { afterEach, describe, expect, it } from "vitest";
import { t } from "../i18n";
import { agentName } from "./agent-kind";
import { NEW_CONVERSATION, pickEffort, pickLauncher, pickModel } from "./assistant-chat";
import { assistantPick, assistantPillView, assistantSetup, type AssistantChat } from "./assistant-models";
import { assistantPanel, assistantPickOf } from "./assistant-panel";
import type { LauncherModels } from "./launcher-models";
import { panelSections } from "./model-panel";
import type { AssistantLaunchers, ModelList } from "./session-models";
import type { Session } from "./types";

const ALL = ["low", "medium", "high", "xhigh", "max"];
const claudeList: LauncherModels = {
  agent: "claude",
  models: [
    { id: "default", name: "Default (recommended)", default: true, resolved: "claude-opus-5-5", efforts: ALL },
    { id: "opus", name: "Opus 5.5", resolved: "claude-opus-5-5", efforts: ALL },
    { id: "haiku", name: "Haiku 5.5", resolved: "claude-haiku-5-5" },
    { id: "vendor/model-x", name: "vendor/model-x", description: "之前用过的模型 ID", custom: true },
  ],
};
const relayList: LauncherModels = { ...claudeList, locked: "--model" };
const info: AssistantLaunchers = {
  launchers: [
    { name: "claude", source: "builtin" },
    { name: "relay", source: "zshrc", model: "sonnet" },
    { name: "team-api", source: "persona", display_name: "Team API", model: "claude-sonnet-5-5" },
    { name: "codex", source: "builtin", agent: "codex" },
  ],
  def: { provider: "claude", model: "opus", effort: "high" },
  unavailable: [
    { name: "gpt-direct", display_name: "GPT direct", kind: "api", api: "responses", code: "api_protocol", reason: "直连 API 用的是 Responses 协议，跑不了主对话" },
    { name: "far-gw", kind: "launcher", code: "launcher_out_of_scope", reason: "这个服务商不对这台电脑开放" },
  ],
};
const read: Record<string, ModelList> = { claude: { list: claudeList }, relay: { list: relayList }, "team-api": { list: { agent: "claude", models: [] } } };
const listOf = (name: string) => read[name] ?? {};
const session = (x: Partial<Session> = {}): Session =>
  ({ machine_id: "server", session_id: "c1", title: "t", cwd: "/a", status: "idle", state: "idle", assistant: true, agent: "claude", launcher: "relay", model: "claude-sonnet-5-5", effort: "medium", ...x }) as Session;
const chat = (x: Partial<AssistantChat> = {}): AssistantChat => ({ session: session(), isMain: false, kept: true, config: "claude", ...x });

afterEach(() => {
  pickLauncher("server/c1", undefined);
  pickLauncher(NEW_CONVERSATION, undefined);
  pickEffort(NEW_CONVERSATION, undefined);
});

describe("the setup a Persona chat's next message runs on", () => {
  it("its kept pick, on every device; the main chat follows the default without one, another chat keeps what it started with", () => {
    expect(assistantSetup(chat({ session: session({ model_pick: { launcher: "team-api", effort: "low" } }) }), info)).toEqual({ provider: "team-api", model: undefined, effort: "low", source: "pick" });
    expect(assistantSetup(chat({ isMain: true }), info)).toEqual({ provider: "claude", model: "opus", effort: "high", source: "default" });
    expect(assistantSetup(chat(), info)).toEqual({ provider: "relay", model: "claude-sonnet-5-5", effort: "medium", source: "own" });
    // a launcher the list no longer offers is not what it runs on: the assistant's
    expect(assistantSetup(chat({ session: session({ launcher: "gone", model: "kimi" }) }), info)).toEqual({ provider: "claude", model: undefined, effort: "medium", source: "own" });
  });

  it("a Main chat default of another agent kind keeps what the conversation runs; same-kind and unknown defaults stay unchanged", () => {
    const running = chat({ isMain: true, session: session({ agent: "codex", launcher: "codex", model: "gpt-5.5" }) });
    expect(assistantSetup(running, info)).toEqual({ provider: "codex", model: "gpt-5.5", effort: "medium", source: "own" });
    expect(assistantPillView(running, assistantSetup(running, info), info, listOf("codex"))).toMatchObject({ engine: "codex", provider: "Codex", model: "GPT-5.5" });
    const fastRunning = chat({ isMain: true, session: session({ agent: "codex", launcher: "codex", model: "gpt-5.5", service_tier: "priority" }) });
    expect(assistantSetup(fastRunning, info)).toEqual({ provider: "codex", model: "gpt-5.5", effort: "medium", speed: "fast", source: "own" });
    expect(assistantPillView(fastRunning, assistantSetup(fastRunning, info), info, listOf("codex"))).toMatchObject({ engine: "codex", model: "GPT-5.5", speed: "fast" });
    const sameKind = { ...info, def: { provider: "codex" } };
    expect(assistantSetup(running, sameKind)).toEqual({ provider: "codex", model: "gpt-5.5", effort: "medium", source: "default" });
    const sameProviderID = { ...info, launchers: [{ name: "launcher.codex", source: "builtin" }], def: { provider: "launcher.codex" } };
    expect(assistantSetup(running, sameProviderID)).toEqual({ provider: "launcher.codex", model: undefined, effort: "medium", source: "default" });
    const unknown = { ...info, def: { provider: "gone" } };
    expect(assistantSetup(running, unknown)).toEqual({ provider: "gone", model: undefined, effort: "medium", source: "default" });
  });

  it("keeps a running non-Claude runtime main or leader conversation on its actual engine even while launchers load or default is Claude", () => {
    const agySession = session({
      agent: "agy",
      launcher: undefined,
      source_kind: "runtime",
      source_instance_id: "agy",
      model: "gemini-3-pro-high",
      effort: "high",
    });
    const mainAgy = chat({ isMain: true, config: "claude", def: { provider: "claude", model: "", effort: "high" }, session: agySession });
    // Before /launchers?purpose=assistant resolves (info === undefined), never fall back to Claude + Default (recommended).
    const loadingSetup = assistantSetup(mainAgy, undefined);
    expect(loadingSetup).toEqual({ provider: "agy", model: "gemini-3-pro-high", effort: "high", source: "own" });
    expect(assistantPillView(mainAgy, loadingSetup, undefined, {})).toMatchObject({
      engine: "agy",
      provider: "Antigravity CLI",
      model: "gemini-3-pro-high",
      effort: "high",
    });

    // Even when Claude is absent from the assistant catalog (e.g. unavailable), keep the running AGY session.
    const agyOnlyInfo: AssistantLaunchers = {
      launchers: [{ name: "agy", source: "agy", assistant: true }],
      def: { provider: "claude", model: "", effort: "high" },
      unavailable: [{ name: "claude", kind: "launcher", agent: "claude", reason: "unavailable" }],
    };
    const agyCatalog: ModelList = {
      list: { agent: "agy", models: [{ id: "gemini-3-pro-high", name: "Gemini 3 Pro (High)", default_effort: "high" }] },
    };
    const loadedSetup = assistantSetup(mainAgy, agyOnlyInfo);
    expect(loadedSetup).toEqual({ provider: "agy", model: "gemini-3-pro-high", effort: "high", source: "own" });
    expect(assistantPillView(mainAgy, loadedSetup, agyOnlyInfo, agyCatalog)).toMatchObject({
      engine: "agy",
      provider: "Antigravity CLI",
      model: "Gemini 3 Pro",
      effort: "high",
    });

    // A leader/direction chat running on AGY also keeps its own engine when Main chat defaults to Claude.
    const leaderAgy = chat({ isMain: false, followsDefault: true, config: "claude", session: agySession });
    expect(assistantSetup(leaderAgy, info)).toEqual({ provider: "agy", model: "gemini-3-pro-high", effort: "high", source: "own" });
  });

  it("resolves a default-following main chat to its concrete model instead of vague Default (recommended)", () => {
    const emptyModelDefault: AssistantLaunchers = {
      launchers: [{ name: "claude", source: "builtin" }, { name: "agy", source: "agy", agent: "agy", assistant: true }],
      def: { provider: "claude", model: "", effort: "high" },
      unavailable: [],
    };
    // No running session yet: resolve the catalog's default row to its concrete model name.
    const freshMain = chat({ isMain: true, session: undefined });
    const freshSetup = assistantSetup(freshMain, emptyModelDefault);
    expect(assistantPillView(freshMain, freshSetup, emptyModelDefault, { list: claudeList })).toMatchObject({
      engine: "claude",
      model: "Opus 5.5",
      effort: "high",
    });
    // Running Gemini main chat following launcher.gemini default with empty model keeps Gemini icon and running model.
    const geminiInfo: AssistantLaunchers = {
      launchers: [{ name: "gemini", source: "builtin", agent: "gemini" }],
      def: { provider: "launcher.gemini", model: "", effort: "high" },
      unavailable: [],
    };
    const geminiChat = chat({ isMain: true, session: session({ agent: "gemini", launcher: "gemini", model: "gemini-2.5-pro", effort: "high" }) });
    const geminiSetup = assistantSetup(geminiChat, geminiInfo);
    expect(assistantPillView(geminiChat, geminiSetup, geminiInfo, {
      list: { agent: "gemini", models: [{ id: "gemini-2.5-pro", name: "Gemini 2.5 Pro" }] },
    })).toMatchObject({
      engine: "gemini",
      provider: "Gemini CLI",
      model: "Gemini 2.5 Pro",
      effort: "high",
    });
  });

  it("an older hub keeps this page's pick; the new-conversation page has its own, the default otherwise", () => {
    pickLauncher("server/c1", "team-api");
    pickModel("server/c1", "claude-haiku-5-5");
    expect(assistantPick(chat({ kept: false }))).toEqual({ launcher: "team-api", model: "claude-haiku-5-5" });
    expect(assistantPick(chat())).toBeUndefined(); // a hub that keeps picks: only model_pick counts
    expect(assistantSetup(chat({ session: undefined }), info)).toEqual({ provider: "claude", model: "opus", effort: "high", source: "default" });
    pickLauncher(NEW_CONVERSATION, "relay");
    pickEffort(NEW_CONVERSATION, "max");
    expect(assistantSetup(chat({ session: undefined }), info)).toEqual({ provider: "relay", model: undefined, effort: "max", source: "pick" });
  });

  it("the pill: the provider's name for people, its list's name for the model (none set: the default entry, else the model it is set up with)", () => {
    const view = (c: AssistantChat) => assistantPillView(c, assistantSetup(c, info), info, listOf(assistantSetup(c, info).provider));
    expect(view(chat({ isMain: true }))).toMatchObject({ engine: "claude", provider: "Claude Code", model: "Opus 5.5", effort: "high" });
    expect(view(chat({ session: session({ model_pick: { launcher: "team-api" } }) }))).toMatchObject({ provider: "Team API", model: "sonnet 5.5" });
    expect(view(chat({ session: session({ model_pick: { launcher: "claude" } }) })).model).toBe("Default (recommended)");
    expect(view(chat({ session: session({ model_pick: { launcher: "codex", model: "gpt-5.5" } }) }))).toMatchObject({ engine: "codex", model: "GPT-5.5" });
    // A side conversation whose session model carries a gateway route prefix keeps the route on the pill while matching the panel row.
    const gwInfo: AssistantLaunchers = { launchers: [{ name: "gw", source: "persona" }], def: { provider: "claude", model: "opus" }, unavailable: [] };
    const gwList: ModelList = { list: { agent: "claude", models: [{ id: "gemini-4-carbon", name: "Gemini 4 Carbon" }] } };
    const side = chat({ isMain: false, session: session({ launcher: "gw", model: "route-a/gemini-4-carbon" }) });
    const sideSetup = assistantSetup(side, gwInfo);
    expect(assistantPillView(side, sideSetup, gwInfo, gwList).model).toBe("route-a/gemini-4-carbon");
    expect(assistantPanel(sideSetup, gwInfo, () => gwList).state.current?.model).toBe("gemini-4-carbon");
  });

  it("reads an Antigravity launcher's configured variant when the catalog marks no default", () => {
    const model = "gemini-4-hydrogen-xhigh";
    const launchers: AssistantLaunchers = { launchers: [{ name: "agy", agent: "agy", source: "persona", model }], unavailable: [] };
    const catalog: ModelList = { list: { agent: "agy", models: [{ id: model, name: "Gemini 4 Hydrogen (XHigh)", default_effort: "xhigh" }] } };
    const setup = { provider: "agy", source: "pick" } as const;
    expect(assistantPillView(chat({ session: undefined }), setup, launchers, catalog)).toMatchObject({ model: "Gemini 4 Hydrogen", effort: "xhigh" });
  });

  it("shows the picked variant's effort while the running session still reports the previous model", () => {
    const model = "gemini-4-neon-xhigh";
    const catalog: ModelList = { list: { agent: "agy", models: [{ id: model, name: "Gemini 4 Neon (XHigh)", default_effort: "xhigh" }] } };
    const c = chat({ session: session({ agent: "agy", launcher: "agy", model: "gemini-4-neon-medium", effort: "medium", model_pick: { launcher: "agy", model } }) });
    const setup = assistantSetup(c, undefined);
    expect(setup.effort).toBeUndefined();
    expect(assistantPillView(c, setup, undefined, catalog)).toMatchObject({ model: "Gemini 4 Neon", effort: "xhigh" });
  });
  it("treats an older node's Chinese default placeholder exactly like an empty launcher model", () => {
    const withModel = (model?: string): AssistantLaunchers => ({
      launchers: [{ name: "claude", source: "builtin", ...(model ? { model } : {}) }],
      def: { provider: "claude" },
      unavailable: [],
    });
    const c = chat({ isMain: true, session: session({ model: undefined }) });
    const emptyCatalog = (): ModelList => ({ list: { agent: "claude", models: [] } });
    const view = (launchers: AssistantLaunchers) => {
      const setup = assistantSetup(c, launchers);
      return {
        setup,
        pill: assistantPillView(c, setup, launchers, emptyCatalog()),
        panel: assistantPanel(setup, launchers, emptyCatalog),
      };
    };
    expect(view(withModel("默认"))).toEqual(view(withModel()));
  });
});

describe("the panel's providers", () => {
  it("names the configured default launcher exactly as the hub does, with a readable default model", () => {
    const { providers } = assistantPanel(assistantSetup(chat({ isMain: true }), info), info, listOf);
    expect(providers.find((p) => p.id === "claude")).toMatchObject({ isDefault: true, defaultModel: "opus 5.5" });
    expect(providers.find((p) => p.id === "relay")?.isDefault).toBe(false);
  });

  it("what can run Persona chats, each with its list; what can't, off with the hub's reason (not hidden)", () => {
    const { providers, state } = assistantPanel(assistantSetup(chat({ isMain: true }), info), info, listOf, { typed: true });
    expect(providers.map((p) => [p.id, p.name, p.engine, p.unavailable ?? ""])).toEqual([
      ["claude", "Claude Code", "claude", ""],
      ["relay", "relay", "claude", ""],
      ["team-api", "Team API", "claude", ""],
      ["codex", "Codex", "codex", ""],
      ["gpt-direct", "GPT direct", "claude", "直连 API 用的是 Responses 协议，跑不了主对话"],
      ["far-gw", "far-gw", "claude", "这个服务商不对这台电脑开放"],
    ]);
    expect(providers.find((p) => p.id === "gpt-direct")!.models).toBeNull();
    // the provider it runs on is the one whose models switch "at once" (Recommended); its row is the default's model
    expect(state).toEqual({ current: { provider: "claude", model: "opus", effort: "high" }, recommended: ["claude"] });
    expect(providers.find((p) => p.id === "codex")).toMatchObject({ loading: true, models: null });
  });

  it("a Paseo provider that can't run it is off as that provider, never 'paseo' (#8142)", () => {
    const paseo: AssistantLaunchers = { ...info, unavailable: [{ name: "grok", kind: "launcher", agent: "paseo", code: "not_main_chat", reason: "跑不了主对话" }, { name: "cursor", kind: "launcher", agent: "paseo", code: "not_main_chat", reason: "跑不了主对话" }] };
    const { providers } = assistantPanel(assistantSetup(chat({ isMain: true }), paseo), paseo, listOf, {});
    expect(providers.filter((p) => p.unavailable).map((p) => [p.id, p.engine])).toEqual([["grok", "grok"], ["cursor", "cursor"]]);
  });

  it("an ID typed before is listed (and can be forgotten); typed IDs only where the hub keeps them; a launcher that fixes its model offers its default only", () => {
    const { providers } = assistantPanel(assistantSetup(chat({ isMain: true }), info), info, listOf, { typed: true });
    const claude = providers.find((p) => p.id === "claude")!;
    expect(claude.models!.find((m) => m.id === "vendor/model-x")).toMatchObject({ custom: true, efforts: ALL });
    expect(claude.typable).toBe(true);
    expect(assistantPanel(assistantSetup(chat({ isMain: true }), info), info, listOf, { typed: false }).providers[0].typable).toBeUndefined();
    const relay = providers.find((p) => p.id === "relay")!;
    expect(relay.models!.map((m) => m.id)).toEqual(["default"]);
    expect(relay.typable).toBeUndefined();
    // a direct API whose list is empty: the model it is set up with
    expect(providers.find((p) => p.id === "team-api")!.models).toEqual([{ id: "claude-sonnet-5-5", name: "sonnet 5.5", efforts: ALL }]);
  });

  it("a chat that runs keeps its engine: another engine's providers are off with why, as in an Agent's panel; the new-chat page offers every kind", () => {
    const pickable = (ps: { id: string; unavailable?: string }[]) => ps.filter((p) => !p.unavailable).map((p) => p.id);
    const claudeChat = assistantPanel(assistantSetup(chat({ isMain: true }), info), info, listOf, { typed: true, engine: "claude", running: true }).providers;
    expect(claudeChat.find((p) => p.id === "codex")).toMatchObject({ unavailable: t("chat-modelpanel.other_engine", { from: agentName("claude"), to: agentName("codex") }) });
    expect(claudeChat.find((p) => p.id === "codex")!.typable).toBeUndefined();
    expect(pickable(claudeChat)).toEqual(["claude", "relay", "team-api"]);
    // the hub's own reasons stay theirs
    expect(claudeChat.find((p) => p.id === "far-gw")!.unavailable).toBe("这个服务商不对这台电脑开放");
    // a Codex chat: the Claude Code providers are the other engine; the one it runs on stays
    const c = chat({ session: session({ agent: "codex", launcher: "codex", model: "gpt-5.5" }) });
    const codexChat = assistantPanel(assistantSetup(c, info), info, listOf, { engine: "codex", running: true }).providers;
    expect(pickable(codexChat)).toEqual(["codex"]);
    expect(codexChat.find((p) => p.id === "relay")!.unavailable).toBe(t("chat-modelpanel.other_engine", { from: agentName("codex"), to: agentName("claude") }));
    // the new-chat page runs nothing yet
    expect(pickable(assistantPanel(assistantSetup(chat({ session: undefined }), info), info, listOf, { typed: true }).providers)).toEqual(["claude", "relay", "team-api", "codex"]);
  });

  it("a pick whose launcher is gone stays shown, off, with why", () => {
    const c = chat({ session: session({ model_pick: { launcher: "old-gw", model: "glm-5" } }) });
    const { providers, state } = assistantPanel(assistantSetup(c, info), info, listOf, {});
    expect(providers[0]).toMatchObject({ id: "old-gw", unavailable: t("chat-modelpill.provider_gone"), models: [{ id: "glm-5" }] });
    expect(state.current).toEqual({ provider: "old-gw", model: "glm-5", effort: "medium" });
    expect(state.recommended).toEqual([]);
  });

  it("Codex's Fast on the model it runs, as it runs", () => {
    const c = chat({ session: session({ launcher: "codex", model: "gpt-5.5", service_tier: "priority" }) });
    const withList = (name: string) => (name === "codex" ? { list: { agent: "codex", models: [{ id: "gpt-5.5", name: "GPT-5.5", efforts: ["low", "high"] }] } } : listOf(name));
    const { providers, state } = assistantPanel(assistantSetup(c, info), info, withList, { speeds: [{ id: "standard" }, { id: "fast" }] });
    expect(providers.find((p) => p.id === "codex")!.models![0].speeds?.map((tier) => tier.id)).toEqual(["standard", "fast"]);
    expect(state.current).toMatchObject({ provider: "codex", model: "gpt-5.5", speed: "fast" });
  });
});

describe("a gateway provider's folded list (#10365)", () => {
  // what the hub sends for the captured catalog: opus, fable, sonnet and haiku all ran the gateway's one model and all read
  // "Opus 5.5"; the concrete model's row carries them, and the gateway's other route of it says its route
  const gateway: LauncherModels = {
    agent: "claude",
    models: [
      { id: "default", name: "Default (recommended)", default: true, resolved: "claude-opus-5-5", efforts: ALL },
      { id: "claude-opus-5-5", name: "Opus 5.5", description: "Custom Opus model (claude-opus-5-5)", aliases: ["opus", "fable", "sonnet", "haiku"], efforts: ALL },
      { id: "kiro/claude-opus-5-5", name: "Opus 5.5 (kiro)", description: "From gateway" },
    ],
  };
  const gw: AssistantLaunchers = { launchers: [{ name: "gateway-api", source: "persona", display_name: "Gateway API" }], def: { provider: "gateway-api" }, unavailable: [] };
  const lists = (name: string): ModelList => (name === "gateway-api" ? { list: gateway } : {});

  it("one Opus 5.5 row in All, a pick saved as an alias on it, and picking it keeps the concrete model", () => {
    const c = chat({ session: session({ launcher: "gateway-api", model_pick: { launcher: "gateway-api", model: "sonnet" } }) });
    const setup = assistantSetup(c, gw);
    const { providers, state } = assistantPanel(setup, gw, lists, { typed: true });
    expect(state.current).toEqual({ provider: "gateway-api", model: "claude-opus-5-5", effort: "medium" });
    const sections = panelSections({ providers, state, view: { kind: "all" }, query: "", favorites: [], recent: [], overrides: {} });
    const rows = sections.flatMap((s) => s.rows);
    expect(rows.map((r) => [r.model.id, r.model.name, r.current])).toEqual([
      ["claude-opus-5-5", "Opus 5.5", true],
      ["default", "Default (recommended)", false],
      ["kiro/claude-opus-5-5", "Opus 5.5 (kiro)", false],
    ]);
    expect(assistantPickOf({ provider: "gateway-api", model: "kiro/claude-opus-5-5" }, gateway, state.current)).toEqual({ launcher: "gateway-api", model: "kiro/claude-opus-5-5" });
    expect(assistantPickOf({ provider: "gateway-api", model: "claude-opus-5-5", effort: "medium" }, gateway, state.current)).toBeNull();
  });
});

describe("what a pick keeps", () => {
  const now = { provider: "claude", model: "opus", effort: "medium" };
  it("the launcher, the model unless it is the default entry, the effort the setup has", () => {
    expect(assistantPickOf({ provider: "claude", model: "haiku", effort: "low" }, claudeList, now)).toEqual({ launcher: "claude", model: "haiku", effort: "low" });
    expect(assistantPickOf({ provider: "claude", model: "default" }, claudeList, now)).toEqual({ launcher: "claude" });
    expect(assistantPickOf({ provider: "team-api", model: "claude-sonnet-5-5" }, { agent: "claude", models: [] }, now)).toEqual({ launcher: "team-api", model: "claude-sonnet-5-5" });
    expect(assistantPickOf({ provider: "claude", model: "~vendor/model-latest[1m]" }, claudeList, now)).toEqual({ launcher: "claude", model: "~vendor/model-latest[1m]" });
  });

  it("nothing for an ID the hub would refuse, or the setup the chat runs now", () => {
    expect(assistantPickOf({ provider: "claude", model: "vendor model" }, claudeList, now)).toBeNull();
    expect(assistantPickOf({ provider: "claude", model: "opus", effort: "medium" }, claudeList, now)).toBeNull();
    expect(assistantPickOf({ provider: "claude", model: "opus", effort: "max" }, claudeList, now)).toEqual({ launcher: "claude", model: "opus", effort: "max" });
  });

  it("keeps a concrete model ID even when marked default, while 'default' and locked launchers still follow launcher default (#13567)", () => {
    const concreteDefault: LauncherModels = {
      agent: "claude",
      models: [
        { id: "default", name: "Default (recommended)", default: false, resolved: "claude-opus-5-5", efforts: ALL },
        { id: "opus", name: "Opus 5.5", default: true, resolved: "claude-opus-5-5", efforts: ALL },
        { id: "claude-opus-5-5", name: "Opus 5.5 exact", default: true, efforts: ALL },
      ],
    };
    // Concrete models marked default: true keep their model ID so picking them pins the model.
    expect(assistantPickOf({ provider: "claude", model: "opus", effort: "high" }, concreteDefault, null)).toEqual({ launcher: "claude", model: "opus", effort: "high" });
    expect(assistantPickOf({ provider: "claude", model: "claude-opus-5-5" }, concreteDefault, null)).toEqual({ launcher: "claude", model: "claude-opus-5-5" });
    // Explicit "default" row omits model even when launcher settings moved default: true to a concrete row.
    expect(assistantPickOf({ provider: "claude", model: "default" }, concreteDefault, null)).toEqual({ launcher: "claude" });
    // Locked launchers fix their own --model, so picking their sole default row still omits model.
    const lockedConcrete: LauncherModels = { agent: "claude", locked: "--model", models: [{ id: "sonnet", name: "Sonnet", default: true }] };
    expect(assistantPickOf({ provider: "relay", model: "sonnet" }, lockedConcrete, null)).toEqual({ launcher: "relay" });
    // Fresh draft (current = null) pins the displayed setup instead of returning null.
    expect(assistantPickOf(now, claudeList, null)).toEqual({ launcher: "claude", model: "opus", effort: "medium" });
  });
});

it("keeps unsupported supervision connections visible with a reason", () => {
  const catalog: AssistantLaunchers = { ...info, launchers: [
    { name: "claude", source: "builtin" },
    { name: "opencode", source: "opencode", agent: "opencode", assistant: true, supervision_policy: true },
    { name: "plugin", source: "opencode", agent: "opencode", assistant: true },
    { name: "grok", source: "grok", agent: "grok", assistant: true },
  ] };
  const setup = assistantSetup(chat({ isMain: true }), catalog);
  const supervised = assistantPanel(setup, catalog, listOf, { running: true, switching: true, supervision: true }).providers;
  expect(supervised.find((p) => p.id === "opencode")?.unavailable).toBeUndefined();
  for (const name of ["plugin", "grok"]) expect(supervised.find((p) => p.id === name)?.unavailable).toBe(t("chat-modelpanel.supervision_unavailable"));
  const ordinary = assistantPanel(setup, catalog, listOf, { running: true, switching: true }).providers;
  expect(ordinary.find((p) => p.id === "plugin")?.unavailable).toBeUndefined();
});
