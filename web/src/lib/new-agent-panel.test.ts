// New Agent's model choice in the unified model panel (#5104; the tests run in zh-Hans): the panel's data for a computer's launchers (Default rows,
// blocked and locked launchers, typed IDs, presets, the row lit), what a pick puts in the form, what the pill says, and
// when a choice goes along to another computer; plus the shared panel's parts New Agent added (presets, no Recommended
// group, the row lit shows the form's setup, engine choices).
import { describe, expect, it } from "vitest";
import type { LauncherModels, PickerTier } from "./launcher-models";
import { engineChoices, engineSetup, panelSections, shownEffort, type PanelProvider, type SectionsInput } from "./model-panel";
import { choiceCarries, formChoice, newAgentPanel, newAgentPill, type ModelChoice } from "./new-agent-panel";
import type { ModelList } from "./session-models";
import type { Launcher } from "./types";

const EFFORTS = ["low", "medium", "high", "xhigh", "max"];
const claudeList: LauncherModels = {
  agent: "claude",
  models: [
    { id: "default", name: "Default (recommended)", resolved: "claude-opus-5-5", default: true, efforts: EFFORTS, description: "Use the default model" },
    { id: "opus", name: "Opus 5.5", resolved: "claude-opus-5-5", efforts: EFFORTS },
    { id: "sonnet", name: "Sonnet 5.5", resolved: "claude-sonnet-5-5", efforts: ["low", "medium", "high"] },
    { id: "gateway/glm-5.3", name: "glm-5.3", efforts: ["low", "medium", "high"] },
    { id: "vendor/model-x", name: "vendor/model-x", custom: true },
  ],
};
const codexList: LauncherModels = {
  agent: "codex",
  models: [
    { id: "gpt-5.5", name: "GPT-5.5", default: true, efforts: ["low", "medium", "high"], default_effort: "medium" },
    { id: "gateway/glm-5.3", name: "glm-5.3", efforts: ["low", "medium", "high"], default_effort: "medium" },
  ],
};
const launchers: Launcher[] = [
  { name: "claude", model: "opus" },
  { name: "work_gw", model: "glm-5.3", health: { state: "blocked", kind: "not_in_terminal" } },
  { name: "sonnetcc", model: "sonnet" },
  { name: "codex", agent: "codex", agent_type: "codex", model: "gpt-5.5" },
  { name: "p-relay", display_name: "Team relay" },
];
const lists: Record<string, ModelList> = {
  claude: { list: claudeList },
  work_gw: { list: { agent: "claude", models: [] } },
  sonnetcc: { list: { ...claudeList, locked: "This launcher fixes its own model" } },
  codex: { list: codexList },
  "p-relay": { loading: true },
};
const listOf = (name: string): ModelList => lists[name] ?? {};
const none: ModelChoice = { launcher: "", model: "", effort: "" };
const configured = { provider: "launcher.claude", model: "sonnet", effort: "high" };
const tiers: PickerTier[] = [
  { name: "quick", launcher: "sonnetcc", description: "Small, quick fixes" },
  { name: "review", launcher: "claude", model: "opus", effort: "high" },
];
const panel = (o: Partial<Parameters<typeof newAgentPanel>[0]> = {}) =>
  newAgentPanel({ launchers, listOf, choice: none, configured, runs: "claude", tiers, ...o });
const provider = (p: ReturnType<typeof panel>, id: string) => p.providers.find((x) => x.id === id)!;

describe("the panel's providers", () => {
  it("says which Persona providers pi can use too (#8194)", () => {
    const p = panel({ launchers: [...launchers.slice(0, 4), { name: "p-relay", display_name: "Team relay", source: "persona", pi: true }] });
    expect(provider(p, "p-relay").alsoNote).toBe("pi 也可使用");
    expect(provider(p, "claude").alsoNote).toBeUndefined();
  });
  it("one per launcher in the hub's order, named by display name, each led by its Default row", () => {
    const p = panel();
    expect(p.providers.map((x) => [x.id, x.name, x.engine])).toEqual([
      ["claude", "Claude Code", "claude"],
      ["work_gw", "work_gw", "claude"],
      ["sonnetcc", "sonnetcc", "claude"],
      ["codex", "Codex", "codex"],
      ["p-relay", "Team relay", "claude"],
    ]);
    // a display name keeps its startup ID as secondary text (#5029); an rc alias has none
    expect(p.providers.map((x) => x.idNote)).toEqual([undefined, undefined, undefined, undefined, "p-relay"]);
    const claude = provider(p, "claude");
    expect(claude.defaultModel).toBe("Sonnet 5.5");
    expect(claude.isDefault).toBe(true);
    expect(provider(p, "codex").isDefault).toBe(false);
    expect(claude.models?.map((m) => m.id)).toEqual(["default", "opus", "sonnet", "gateway/glm-5.3", "vendor/model-x"]);
    // the model settings run sonnet on claude: its Default row says so, and takes their effort as its default
    expect(claude.models?.[0]).toMatchObject({ name: "默认：Sonnet 5.5", providerDefault: true, defaultEffort: "high", efforts: ["low", "medium", "high"] });
    expect(claude.typable).toBe(true);
    // another launcher's Default row names the model its default entry stands for, by the list's own name for it
    expect(provider(panel({ configured: null }), "claude").models?.[0].name).toBe("默认：Opus 5.5");
  });

  it("a launcher that failed its check here is listed, its rows off with why and a fix to show (#1643)", () => {
    const blocked = provider(panel(), "work_gw");
    expect(blocked.unavailable).toBeTruthy();
    // no list: its Default row alone, named by the model the launcher runs
    expect(blocked.models?.map((m) => m.name)).toEqual(["默认：glm-5.3"]);
  });

  it("a provider left out on this computer comes after the launchers, off with the hub's reason (#8109)", () => {
    const reason = "Mac 已经有自己的 Claude Code 服务，而 DeepSeek 设置为跳过这样的电脑";
    const p = panel({ unavailable: [
      { name: "deepseek", display_name: "DeepSeek", kind: "launcher", code: "launcher_own_service", reason },
      { name: "claude", kind: "launcher", code: "launcher_own_service", reason: "not this one" }, // listed already: stays as listed
      { name: "cx-gw", kind: "launcher", agent: "codex", code: "launcher_own_service", reason },
    ] });
    expect(p.providers.map((x) => x.id)).toEqual(["claude", "work_gw", "sonnetcc", "codex", "p-relay", "deepseek", "cx-gw"]);
    expect(provider(p, "deepseek")).toEqual({ id: "deepseek", name: "DeepSeek", engine: "claude", models: null, unavailable: reason });
    expect(provider(p, "cx-gw").engine).toBe("codex");
    expect(provider(p, "claude").unavailable).toBeUndefined();
    // the row lit stays the launcher that runs
    expect(p.state.current?.provider).toBe("claude");
  });

  it("a launcher that fixes its own --model offers its Default row alone, with why, and no typed ID", () => {
    const locked = provider(panel(), "sonnetcc");
    expect(locked.models).toHaveLength(1);
    expect(locked.models?.[0]).toMatchObject({ id: "default", name: "默认：sonnet", description: "This launcher fixes its own model" });
    expect(locked.typable).toBeUndefined();
    expect(locked.locked).toBeUndefined();
  });

  it.each(["claude_desktop", "codex_app"])("%s retains its supported desktop model catalog and selection", (source) => {
    const desktop: Launcher = { name: "desktop", agent: source === "codex_app" ? "codex" : "claude", source };
    const list = source === "codex_app" ? codexList : claudeList;
    const model = source === "codex_app" ? "gpt-5.5" : "sonnet";
    const p = panel({ launchers: [desktop], listOf: () => ({ list }),
      choice: { launcher: desktop.name, model, effort: "high" }, configured: null, runs: desktop.name, tiers: [] });
    const item = provider(p, desktop.name);
    expect(item.models?.some((row) => row.id === model)).toBe(true);
    expect(item.typable).toBe(true);
    expect(p.state.current).toEqual({ provider: desktop.name, model, effort: "high" });
  });

  it.each(["claude_desktop", "codex_app"])("%s respects a model catalog's explicit lock", (source) => {
    const desktop: Launcher = { name: "desktop", agent: source === "codex_app" ? "codex" : "claude", source };
    const p = panel({ launchers: [desktop], listOf: () => ({ list: { agent: desktop.agent!, models: [], locked: "Settings follow the desktop app." } }),
      choice: none, configured: null, runs: desktop.name, tiers: [] });
    const item = provider(p, desktop.name);
    expect(item.models?.map((row) => row.id)).toEqual(["default"]);
    expect(item.models?.[0].description).toBe("Settings follow the desktop app.");
    expect(item.typable).toBeUndefined();
  });

  it("a list that couldn't be read: why and Retry, the launcher's Default row, and a typed ID for Claude Code and Codex", () => {
    const p = newAgentPanel({ launchers, listOf: (name) => (name === "claude" || name === "work_gw" ? { error: "no answer" } : listOf(name)), choice: none, configured: null, runs: "claude", tiers: [] });
    expect(provider(p, "claude")).toMatchObject({ error: "no answer", typable: true });
    expect(provider(p, "claude").models?.map((m) => [m.id, m.providerDefault])).toEqual([["default", true]]);
    expect(provider(p, "claude").models?.[0].efforts).toEqual(EFFORTS);
    const typed = panelSections({ providers: p.providers, state: p.state, view: { kind: "all" }, query: "acme/coder-9", favorites: [], recent: [], overrides: {} });
    expect(typed.flatMap((s) => s.rows).filter((r) => r.kind === "typed").map((r) => r.provider.id)).toEqual(["claude", "codex"]);
  });

  it("a real model its list marks default (Codex's) keeps a row of its own; the Default row names it", () => {
    const codex = provider(panel({ configured: null }), "codex");
    expect(codex.models?.map((m) => [m.id, m.name])).toEqual([["default", "默认：GPT-5.5"], ["gpt-5.5", "GPT-5.5"], ["gateway/glm-5.3", "glm-5.3"]]);
  });

  it("an ID typed for this start is not one the hub remembers: no Typed before, nothing to forget", () => {
    const row = provider(panel({ choice: { launcher: "claude", model: "vendor/new", effort: "" }, configured: null }), "claude").models?.at(-1);
    expect(row?.custom).toBeUndefined();
    expect(row?.description).toBe("本次手动输入的模型 ID");
  });

  it("Codex offers only each model's reported efforts and keeps unset effort at Codex's default; a list still read is a spinner", () => {
    const p = panel({
      listOf: (name) => name === "codex" ? { list: {
        agent: "codex",
        models: [
          ...codexList.models,
          { id: "gpt-5.5-none", name: "GPT-5.5 None", efforts: ["none", "low", "high"] },
          { id: "gpt-5.5-plain", name: "GPT-5.5 Plain" },
        ],
      } } : listOf(name),
      choice: { launcher: "codex", model: "unlisted-codex", effort: "" },
      configured: null,
      runs: "codex",
    });
    const codex = provider(p, "codex");
    expect(codex.models?.map((m) => [m.id, m.efforts, m.defaultEffort])).toEqual([
      ["default", ["low", "medium", "high"], undefined],
      ["gpt-5.5", ["low", "medium", "high"], undefined],
      ["gateway/glm-5.3", ["low", "medium", "high"], undefined],
      ["gpt-5.5-none", ["low", "high"], undefined],
      ["gpt-5.5-plain", [], undefined],
      ["unlisted-codex", [], undefined],
    ]);
    expect(provider(p, "p-relay")).toMatchObject({ models: null, loading: true });
  });

  it("supplies the tier encoded by the Antigravity variant to the start pill", () => {
    const model = "gemini-4-neon-xhigh";
    const list: LauncherModels = { agent: "agy", models: [{ id: model, name: "Gemini 4 Neon (XHigh)", default_effort: "xhigh" }] };
    const choice = { launcher: "agy", model, effort: "" };
    const view = newAgentPill({ launchers: [{ name: "agy", agent: "agy" }], runs: "agy", list: { list }, choice, configured: null, fallback: "" });
    expect(view.effort).toBe("xhigh");
  });

  // A launcher that lists its permission modes (#8261, Paseo's Grok and its Codex providers) takes the efforts its models
  // list: each row its model's own, the Default row the default model's, none for a model nobody lists the levels of.
  it("a launcher that lists its permission modes offers each model's own efforts, and none where nobody says", () => {
    const grok: Launcher = { name: "grok", agent: "paseo", source: "paseo", display_name: "Grok", permission_modes: ["default", "bypassPermissions"] };
    const paseoCodex: Launcher = { name: "my-codex", agent: "codex", source: "paseo", permission_modes: ["default", "bypassPermissions"] };
    const grokList: LauncherModels = { agent: "paseo", models: [{ id: "grok-4", name: "Grok 4", default: true, efforts: ["low", "high"] }, { id: "grok-mini", name: "Grok mini" }] };
    const p = newAgentPanel({ launchers: [grok, paseoCodex], listOf: (n) => (n === "grok" ? { list: grokList } : { list: codexList }), choice: none, configured: null, runs: "grok", tiers: [] });
    expect(provider(p, "grok").name).toBe("Grok");
    expect(provider(p, "grok").models?.map((m) => [m.id, m.efforts])).toEqual([["default", ["low", "high"]], ["grok-4", ["low", "high"]], ["grok-mini", []]]);
    expect(provider(p, "my-codex").models?.map((m) => m.efforts)).toEqual([["low", "medium", "high"], ["low", "medium", "high"], ["low", "medium", "high"]]);
    expect(newAgentPill({ launchers: [grok], runs: "grok", list: { list: grokList }, choice: { launcher: "grok", model: "", effort: "high" }, configured: null, fallback: "" }).effort)
      .toBe("high");
  });

  it("an ID typed for this start that the list lacks gets its own row, so it is lit", () => {
    const p = panel({ choice: { launcher: "claude", model: "vendor/new", effort: "" }, configured: null });
    expect(provider(p, "claude").models?.at(-1)).toMatchObject({ id: "vendor/new", efforts: EFFORTS });
    expect(p.state.current).toEqual({ provider: "claude", model: "vendor/new" });
  });
});

describe("the row lit and the state", () => {
  it("following the model settings: their launcher's Default row, with their effort; no Recommended group", () => {
    const p = panel();
    expect(p.state).toEqual({ current: { provider: "claude", model: "default", effort: "high" }, currentEffortDisplayOnly: "agy", recommended: [], starting: true });
    const sections = panelSections({ providers: p.providers, state: p.state, view: { kind: "all" }, query: "", favorites: [], recent: [], overrides: {}, presets: p.presets });
    expect(sections.map((s) => s.kind)).not.toContain("recommended");
    expect(sections.find((s) => s.provider?.id === "claude")?.rows.find((r) => r.current)?.model.id).toBe("default");
  });

  it.each([
    { launcher: "", effort: "", currentEffort: "xhigh", defaultEffort: "xhigh", shown: "xhigh" },
    { launcher: "agy", effort: "", currentEffort: undefined, defaultEffort: "low", shown: "low" },
    { launcher: "agy", effort: "high", currentEffort: "high", defaultEffort: "low", shown: "high" },
  ])("shows agy effort for launcher=$launcher and effort=$effort without inheriting it in picks", ({ launcher, effort, currentEffort, defaultEffort, shown }) => {
    const agyList: LauncherModels = { agent: "agy", models: [{ id: "example", name: "Example", default: true, default_effort: "low", efforts: EFFORTS }] };
    const p = panel({
      launchers: [{ name: "agy", agent: "agy" }],
      listOf: () => ({ list: agyList }),
      configured: { provider: "launcher.agy", model: "example", effort: "xhigh" },
      choice: { ...none, launcher, effort },
      runs: "agy",
    });
    const sections = panelSections({ providers: p.providers, state: p.state, view: { kind: "all" }, query: "", favorites: [], recent: [], overrides: {} });
    const row = sections.flatMap((s) => s.rows).find((r) => r.current)!;
    expect(p.state.current?.effort).toBe(currentEffort);
    expect(row.model.defaultEffort).toBe(defaultEffort);
    expect(shownEffort(row.setup, row.model)).toBe(shown);
    expect(formChoice(row.setup)).toEqual({ launcher: "agy", model: "", effort });
  });

  it.each([
    { source: "Model settings", choice: none, effort: "" },
    // another model opens at its own default, whatever the pick before had (#12315)
    { source: "an explicit pick", choice: { launcher: "agy", model: "example", effort: "xhigh" }, effort: "" },
  ])("picks another agy model with the correct effort from $source", ({ choice, effort }) => {
    const agyList: LauncherModels = {
      agent: "agy",
      models: [
        { id: "example", name: "Example", efforts: ["high", "xhigh"] },
        { id: "other", name: "Other", efforts: ["high", "xhigh"] },
        { id: "limited", name: "Limited", efforts: ["high"] },
      ],
    };
    const p = panel({
      launchers: [{ name: "agy", agent: "agy" }],
      listOf: () => ({ list: agyList }),
      configured: { provider: "launcher.agy", model: "example", effort: "xhigh" },
      choice,
      runs: "agy",
    });
    const sections = panelSections({ providers: p.providers, state: p.state, view: { kind: "all" }, query: "", favorites: [], recent: [], overrides: {} });
    const rows = sections.flatMap((s) => s.rows);
    expect(formChoice(rows.find((r) => r.model.id === "other")!.setup)).toEqual({ launcher: "agy", model: "other", effort });
    expect(formChoice(rows.find((r) => r.model.id === "limited")!.setup)).toEqual({ launcher: "agy", model: "limited", effort: "" });
  });

  it("a model picked: its row, with the effort picked", () => {
    expect(panel({ choice: { launcher: "claude", model: "opus", effort: "max" } }).state.current).toEqual({ provider: "claude", model: "opus", effort: "max" });
  });

  it("no launcher known to run: nothing lit", () => {
    expect(panel({ runs: "" }).state.current).toBeNull();
  });

  it("the lit row shows what the form holds, not a customization saved for that model", () => {
    const p = panel({ choice: { launcher: "claude", model: "opus", effort: "" }, configured: null });
    const input: SectionsInput = { providers: p.providers, state: p.state, view: { kind: "all" }, query: "", favorites: [], recent: [], overrides: { "claude\nopus": { effort: "low" }, "claude\nsonnet": { effort: "medium" } } };
    const rows = panelSections(input).flatMap((s) => s.rows);
    expect(rows.find((r) => r.model.id === "opus")?.setup).toEqual({ provider: "claude", model: "opus" });
    // another row's saved setup is what picking it sends
    expect(rows.find((r) => r.model.id === "sonnet")?.setup).toEqual({ provider: "claude", model: "sonnet", effort: "medium" });
  });
});

describe("presets", () => {
  it("one row each, after the favorites, named by the preset and described; never lit", () => {
    const p = panel();
    expect(p.presets).toEqual([
      { key: "quick", name: "quick", description: "Small, quick fixes", setup: { provider: "sonnetcc", model: "default" } },
      { key: "review", name: "review", description: "Claude Code · Opus 5.5 · High", setup: { provider: "claude", model: "opus", effort: "high" } },
    ]);
    const sections = panelSections({ providers: p.providers, state: p.state, view: { kind: "all" }, query: "", favorites: [{ uid: "f", provider: "claude", model: "sonnet" }], recent: [], overrides: {}, presets: p.presets });
    expect(sections.slice(0, 2).map((s) => s.kind)).toEqual(["favorites", "presets"]);
    expect(sections[1].rows.map((r) => [r.kind, r.preset?.name, r.current])).toEqual([["preset", "quick", false], ["preset", "review", false]]);
  });

  it("stand on a row their launcher's group has: a model a fixed or untyped launcher lacks gives way to its Default row", () => {
    const p = panel({ tiers: [{ name: "fast", launcher: "sonnetcc", model: "opus", effort: "high" }, { name: "x", launcher: "claude", model: "vendor/other" }, { name: "y", launcher: "p-relay", model: "m-1" }] });
    expect(p.presets.map((x) => x.setup)).toEqual([
      { provider: "sonnetcc", model: "default", effort: "high" },
      // Claude Code takes a typed ID, and a list not read yet is not judged
      { provider: "claude", model: "vendor/other" },
      { provider: "p-relay", model: "m-1" },
    ]);
    const rows = panelSections({ providers: p.providers, state: p.state, view: { kind: "all" }, query: "", favorites: [], recent: [], overrides: {}, presets: p.presets }).find((s) => s.kind === "presets")?.rows;
    expect(rows?.map((r) => r.preset?.key)).toEqual(["fast", "x", "y"]);
  });

  it("searched by their name and description, and listed in their launcher's view", () => {
    const p = panel();
    const base = { providers: p.providers, state: p.state, favorites: [], recent: [], overrides: {}, presets: p.presets };
    expect(panelSections({ ...base, view: { kind: "all" }, query: "quick fixes" }).find((s) => s.kind === "presets")?.rows.map((r) => r.preset?.key)).toEqual(["quick"]);
    expect(panelSections({ ...base, view: { kind: "provider", provider: "claude" }, query: "" }).find((s) => s.kind === "presets")?.rows.map((r) => r.preset?.key)).toEqual(["review"]);
  });
});

describe("what a pick puts in the form", () => {
  it("a model: its launcher, model and effort; a Default row: no model", () => {
    expect(formChoice({ provider: "claude", model: "opus", effort: "high" })).toEqual({ launcher: "claude", model: "opus", effort: "high" });
    expect(formChoice({ provider: "claude", model: "default" })).toEqual({ launcher: "claude", model: "", effort: "" });
    // a model its list marks default (Codex's) is that model, not "no model"
    expect(formChoice({ provider: "codex", model: "gpt-5.5" })).toEqual({ launcher: "codex", model: "gpt-5.5", effort: "" });
  });
});

describe("engines", () => {
  it("another engine is another launcher listing the same model; Default rows and typed IDs have their own alone", () => {
    const p = panel();
    const rows = panelSections({ providers: p.providers, state: p.state, view: { kind: "all" }, query: "glm", favorites: [], recent: [], overrides: {} }).flatMap((s) => s.rows);
    const glm = rows.find((r) => r.provider.id === "claude" && r.model.id === "gateway/glm-5.3")!;
    const choices = engineChoices(p.providers, glm, p.state);
    expect(choices.map((c) => [c.engine, c.provider.id, c.own])).toEqual([["claude", "claude", true], ["codex", "codex", false]]);
    expect(engineSetup(choices[1], { provider: "claude", model: "gateway/glm-5.3", effort: "high" })).toEqual({ provider: "codex", model: "gateway/glm-5.3", effort: "high" });
    const def = rows.length ? panelSections({ providers: p.providers, state: p.state, view: { kind: "all" }, query: "", favorites: [], recent: [], overrides: {} }).flatMap((s) => s.rows).find((r) => r.provider.id === "claude" && r.model.providerDefault)! : glm;
    expect(engineChoices(p.providers, def, p.state)).toHaveLength(1);
  });

  it("each of Paseo's providers is an engine of its own, by its provider, never 'paseo' (#8142)", () => {
    const shared = { id: "anthropic/claude-sonnet-5-5", name: "Sonnet 5.5" };
    const paseo: Launcher[] = [
      { name: "opencode", agent: "paseo", agent_type: "paseo", source: "paseo" },
      { name: "pi", agent: "paseo", agent_type: "paseo", source: "paseo" },
      { name: "grok", agent: "paseo", agent_type: "paseo", source: "paseo" },
    ];
    const paseoLists: Record<string, ModelList> = {
      opencode: { list: { agent: "paseo", models: [{ ...shared, default: true }] } },
      pi: { list: { agent: "paseo", models: [{ ...shared, default: true }] } },
      grok: { list: { agent: "paseo", models: [{ id: "grok-4", name: "Grok 4", default: true }] } },
    };
    const p = newAgentPanel({ launchers: paseo, listOf: (n) => paseoLists[n] ?? {}, choice: none, configured: null, runs: "opencode", tiers: [] });
    expect(p.providers.map((x) => x.engine)).toEqual(["opencode", "pi", "grok"]);
    // the same model through OpenCode and Pi: two engines, as the flyout's engine pills offer them
    const row = { kind: "model" as const, provider: p.providers[0], model: p.providers[0].models!.find((m) => m.id === shared.id)! };
    expect(engineChoices(p.providers, row, p.state).map((c) => [c.engine, c.provider.id])).toEqual([["opencode", "opencode"], ["pi", "pi"]]);
    expect(newAgentPill({ launchers: paseo, runs: "grok", list: paseoLists.grok, choice: none, configured: null, fallback: "" }).engine).toBe("grok");
  });

  it("of one engine, a launcher that can take the pick is offered over one that can't", () => {
    const off: PanelProvider = { id: "codex-old", name: "old", engine: "codex", models: [{ id: "gateway/glm-5.3", name: "glm-5.3", efforts: [] }], unavailable: "blocked" };
    const ok: PanelProvider = { id: "codex", name: "codex", engine: "codex", models: [{ id: "gateway/glm-5.3", name: "glm-5.3", efforts: ["low", "high"] }] };
    const own: PanelProvider = { id: "claude", name: "claude", engine: "claude", models: [{ id: "gateway/glm-5.3", name: "glm-5.3", efforts: ["low", "high"] }] };
    const row = { kind: "model" as const, provider: own, model: own.models![0] };
    expect(engineChoices([own, off], row, {}).map((c) => [c.provider.id, c.blocked])).toEqual([["claude", undefined], ["codex-old", "blocked"]]);
    expect(engineChoices([own, off, ok], row, {}).map((c) => c.provider.id)).toEqual(["claude", "codex"]);
    // the effort in use goes along where the other engine's model takes it
    expect(engineSetup({ provider: ok, model: ok.models![0] }, { provider: "claude", model: "gateway/glm-5.3", effort: "high" })).toEqual({ provider: "codex", model: "gateway/glm-5.3", effort: "high" });
  });
});

describe("the pill", () => {
  it("names what runs: the model settings' model with their effort, the engine and the launcher's title", () => {
    expect(newAgentPill({ launchers, runs: "claude", list: lists.claude, choice: none, configured, fallback: "Default model" })).toEqual({ engine: "claude", provider: "Claude Code", model: "Sonnet 5.5", effort: "high", loading: false, failed: false });
  });

  it("a model picked, a launcher's own default, a fixed model, a Codex launcher (reported effort only)", () => {
    expect(newAgentPill({ launchers, runs: "claude", list: lists.claude, choice: { launcher: "claude", model: "opus", effort: "max" }, configured: null, fallback: "" })).toMatchObject({ model: "Opus 5.5", effort: "max" });
    expect(newAgentPill({ launchers, runs: "claude", list: lists.claude, choice: { launcher: "claude", model: "", effort: "" }, configured: null, fallback: "" }).model).toBe("Opus 5.5");
    expect(newAgentPill({ launchers, runs: "sonnetcc", list: lists.sonnetcc, choice: { launcher: "sonnetcc", model: "", effort: "" }, configured: null, fallback: "" }).model).toBe("sonnet");
    expect(newAgentPill({ launchers, runs: "codex", list: lists.codex, choice: { launcher: "codex", model: "gpt-5.5", effort: "" }, configured: null, fallback: "" })).toMatchObject({ engine: "codex", model: "GPT-5.5" });
    expect(newAgentPill({ launchers, runs: "codex", list: lists.codex, choice: { launcher: "codex", model: "gpt-5.5", effort: "" }, configured: null, fallback: "" }).effort).toBeUndefined();
    expect(newAgentPill({ launchers, runs: "codex", list: lists.codex, choice: { launcher: "codex", model: "gpt-5.5", effort: "high" }, configured: null, fallback: "" })).toMatchObject({ engine: "codex", model: "GPT-5.5", effort: "high" });
    expect(newAgentPill({ launchers, runs: "codex", list: lists.codex, choice: { launcher: "codex", model: "", effort: "high" }, configured: null, fallback: "" }).effort).toBe("high");
    expect(newAgentPill({ launchers, runs: "codex", list: lists.codex, choice: { launcher: "codex", model: "gpt-5.5", effort: "max" }, configured: null, fallback: "" }).effort).toBeUndefined();
    expect(newAgentPill({ launchers, runs: "codex", list: lists.codex, choice: { launcher: "codex", model: "unlisted", effort: "high" }, configured: null, fallback: "" }).effort).toBeUndefined();
  });

  it.each(["claude_desktop", "codex_app"])("%s pills retain supported selected models and efforts", (source) => {
    const desktop: Launcher = { name: "desktop", agent: source === "codex_app" ? "codex" : "claude", source };
    const list = source === "codex_app" ? codexList : claudeList;
    const model = source === "codex_app" ? "gpt-5.5" : "sonnet";
    const pill = newAgentPill({ launchers: [desktop], runs: desktop.name, list: { list },
      choice: { launcher: desktop.name, model, effort: "high" }, configured: null, fallback: "" });
    expect(pill.model).toBe(source === "codex_app" ? "GPT-5.5" : "Sonnet 5.5");
    expect(pill.effort).toBe("high");
  });

  it("while the list is read the first time, when that failed, and with no launcher known", () => {
    expect(newAgentPill({ launchers, runs: "p-relay", list: { loading: true }, choice: { launcher: "p-relay", model: "", effort: "" }, configured: null, fallback: "" })).toMatchObject({ provider: "Team relay", loading: true, failed: false });
    expect(newAgentPill({ launchers, runs: "p-relay", list: { error: "no" }, choice: { launcher: "p-relay", model: "", effort: "" }, configured: null, fallback: "" })).toMatchObject({ failed: true });
    expect(newAgentPill({ launchers, runs: "", list: {}, choice: none, configured, fallback: "acme · glm" }).model).toBe("acme · glm");
  });
});

describe("another computer", () => {
  it("keeps following the model settings; a launcher picked goes along only where it is and can start", () => {
    expect(choiceCarries(none, [])).toBe(true);
    expect(choiceCarries({ launcher: "codex", model: "gpt-5.5", effort: "" }, launchers)).toBe(true);
    expect(choiceCarries({ launcher: "codex", model: "gpt-5.5", effort: "" }, launchers.filter((l) => l.name !== "codex"))).toBe(false);
    expect(choiceCarries({ launcher: "work_gw", model: "", effort: "" }, launchers)).toBe(false);
  });
});
