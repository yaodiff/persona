// Model series & families in the model panel: which series and folding line a model ID names, how a provider's
// list is ordered and folded, and what never folds away (the model that runs, favorites, recent setups, a search).
import { describe, expect, it } from "vitest";
import { compareVersions, familyBlocks, modelFamily, type FamilyOptions } from "./model-families";
import { panelSections, type PanelModel, type PanelProvider, type PanelRow, type SectionsInput } from "./model-panel";

describe("modelFamily", () => {
  it("reads Claude's series, tier rank, and version from the concrete ID or resolved target", () => {
    expect(modelFamily({ id: "claude-opus-4-8" })).toMatchObject({ key: "claude:opus", series: "claude", label: "Claude", version: [4, 8], rank: 1 });
    expect(modelFamily({ id: "opus", resolved: "claude-opus-5-5" })).toMatchObject({ key: "claude:opus", series: "claude", label: "Claude", version: [5, 5] });
    expect(modelFamily({ id: "claude-fable-5" })).toMatchObject({ key: "claude:fable", series: "claude", version: [5], rank: 0 });
    expect(modelFamily({ id: "claude-haiku-4-5-20251001" })).toMatchObject({ key: "claude:haiku", series: "claude", version: [4, 5], rank: 3 });
    expect(modelFamily({ id: "claude-fable-5-2[1m]" })).toMatchObject({ key: "claude:fable", series: "claude", version: [5, 2] });
    expect(modelFamily({ id: "anthropic/claude-sonnet-4-6" })).toMatchObject({ key: "claude:sonnet", series: "claude", version: [4, 6] });
    expect(modelFamily({ id: "claude-3-5-sonnet-20241022" })).toMatchObject({ key: "claude:sonnet", series: "claude", version: [3, 5] });
  });

  it("assigns GPT and Gemini models to GPT and Gemini series while tracking internal folding line keys", () => {
    expect(modelFamily({ id: "gpt-6.1-sol" })).toMatchObject({ key: "gpt:sol", series: "gpt", label: "GPT", version: [6, 1] });
    expect(modelFamily({ id: "gpt-5.5" })).toMatchObject({ key: "gpt:", series: "gpt", label: "GPT", version: [5, 5] });
    expect(modelFamily({ id: "gpt-5.5-codex-mini" })).toMatchObject({ key: "gpt:codex-mini", series: "gpt", label: "GPT", version: [5, 5] });
    expect(modelFamily({ id: "gpt-4o" })).toMatchObject({ key: "gpt:4o", series: "gpt", label: "GPT", version: [], unversioned: true });
    expect(modelFamily({ id: "gemini-3.8-flash-medium" })).toMatchObject({ key: "gemini:flash", series: "gemini", label: "Gemini", version: [3, 8] });
    expect(modelFamily({ id: "gemini-3-pro-preview" })).toMatchObject({ key: "gemini:pro", series: "gemini", label: "Gemini", version: [3] });
    expect(modelFamily({ id: "gemini-3.8-flash-lite" })).toMatchObject({ key: "gemini:flash-lite", series: "gemini", label: "Gemini" });
    expect(modelFamily({ id: "google/gemini-4-argon" })).toMatchObject({ key: "gemini:argon", series: "gemini", label: "Gemini", version: [4] });
  });

  it("classifies DeepSeek, GLM, MiniMax, Seed, Qwen, Kimi, and Grok by series", () => {
    expect(modelFamily({ id: "deepseek-v4" })).toMatchObject({ key: "deepseek:v", series: "deepseek", label: "DeepSeek", version: [4] });
    expect(modelFamily({ id: "deepseek-ai/deepseek-v3-0324" })).toMatchObject({ key: "deepseek:v", series: "deepseek", label: "DeepSeek", version: [3] });
    expect(modelFamily({ id: "deepseek-r1-0528" })).toMatchObject({ key: "deepseek:r", series: "deepseek", label: "DeepSeek", version: [1] });
    expect(modelFamily({ id: "deepseek-chat" })).toMatchObject({ key: "deepseek:other", series: "deepseek", label: "DeepSeek", unversioned: true });

    expect(modelFamily({ id: "glm-5.3" })).toMatchObject({ key: "glm:", series: "glm", label: "GLM", version: [5, 3] });
    expect(modelFamily({ id: "zhipu/glm-4.5-air:high" })).toMatchObject({ key: "glm:air", series: "glm", label: "GLM", version: [4, 5] });

    expect(modelFamily({ id: "minimax-m2.5-highspeed" })).toMatchObject({ key: "minimax:m-highspeed", series: "minimax", label: "MiniMax", version: [2, 5] });
    expect(modelFamily({ id: "minimax-01" })).toMatchObject({ key: "minimax:other", series: "minimax", label: "MiniMax", unversioned: true });

    expect(modelFamily({ id: "doubao-seed-1.6-thinking" })).toMatchObject({ key: "seed:", series: "seed", label: "Seed", version: [1, 6] });
    expect(modelFamily({ id: "doubao-seed-1-6-250615" })).toMatchObject({ key: "seed:", series: "seed", label: "Seed", version: [1, 6] });
    expect(modelFamily({ id: "seed-coder" })).toMatchObject({ key: "seed:other", series: "seed", label: "Seed", unversioned: true });

    expect(modelFamily({ id: "qwen-4-coder" })).toMatchObject({ key: "qwen:coder", series: "qwen", label: "Qwen", version: [4] });
    expect(modelFamily({ id: "qwen3-max" })).toMatchObject({ key: "qwen:max", series: "qwen", label: "Qwen", version: [3] });
    expect(modelFamily({ id: "qwen-max" })).toMatchObject({ key: "qwen:other", series: "qwen", label: "Qwen", unversioned: true });

    expect(modelFamily({ id: "kimi-k2.5-thinking" })).toMatchObject({ key: "kimi:k", series: "kimi", label: "Kimi", version: [2, 5] });
    expect(modelFamily({ id: "grok-4-fast" })).toMatchObject({ key: "grok:fast", series: "grok", label: "Grok", version: [4] });
  });

  it("names no family for what it can't read plainly", () => {
    for (const id of ["default", "opus", "o3", "vendor/model-x", "默认", "gemini-3.8", "custom-alpha"]) expect(modelFamily({ id })).toBeUndefined();
  });

  it("orders versions newest first, a missing minor counting as 0", () => {
    expect([[4, 8], [5], [5, 5], [4, 10]].sort(compareVersions)).toEqual([[5, 5], [5], [4, 10], [4, 8]]);
    expect(compareVersions([5], [5, 0])).toBe(0);
  });
});

// claude-max as the hub lists it today (2026-11-17)
const claudeMax: PanelModel[] = [
  { id: "default", name: "Default (recommended)", resolved: "claude-opus-5-5", efforts: [] },
  { id: "opus", name: "Opus 5.5", resolved: "claude-opus-5-5", efforts: [] },
  { id: "fable", name: "Fable 5.2", resolved: "claude-fable-5-2", efforts: [] },
  { id: "sonnet", name: "Sonnet 5.5", resolved: "claude-sonnet-5-5", efforts: [] },
  { id: "haiku", name: "Haiku 5.5", resolved: "claude-haiku-5-5", efforts: [] },
  { id: "claude-fable-5-1", name: "Fable 5.1", efforts: [] },
  { id: "claude-haiku-4-5-20251001", name: "Haiku 4.5", efforts: [] },
  { id: "claude-sonnet-5", name: "Sonnet 5", efforts: [] },
  { id: "claude-opus-5", name: "Opus 5", efforts: [] },
  { id: "claude-fable-5", name: "Fable 5", efforts: [] },
  { id: "claude-opus-4-8", name: "Opus 4.8", efforts: [] },
  { id: "claude-opus-4-7", name: "Opus 4.7", efforts: [] },
  { id: "claude-opus-4-6", name: "Opus 4.6", efforts: [] },
  { id: "claude-sonnet-4-6", name: "Sonnet 4.6", efforts: [] },
];
const max: PanelProvider = { id: "claude-max", name: "claude-max", engine: "claude", models: claudeMax };
const codex: PanelProvider = {
  id: "codex",
  name: "codex",
  engine: "codex",
  models: ["gpt-6.1-sol", "gpt-6-astra", "gpt-6.1-terra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"].map((id) => ({ id, name: id, efforts: [] })),
};
const gateway: PanelProvider = {
  id: "aster",
  name: "aster",
  engine: "claude",
  models: [
    { id: "default", name: "Default (recommended)", resolved: "claude-opus-5-5", efforts: [] },
    ...["gemini-3.8-flash", "glm-5.3", "gpt-6-sol", "gemini-3.5-flash", "claude-opus-4-8", "gemini-3.8-pro", "gpt-6.1-sol", "opus", "deepseek-v4", "unknown-edge-model"].map((id) => ({ id, name: id, efforts: [], ...(id === "opus" ? { resolved: "claude-opus-5-5" } : {}) })),
    { id: "vendor/model-x", name: "vendor/model-x", efforts: [], custom: true },
  ],
};

const rowsOf = (p: PanelProvider): PanelRow[] =>
  (p.models ?? []).map((m) => ({ key: `model:${p.id}\n${m.id}`, kind: "model", provider: p, model: m, setup: { provider: p.id, model: m.id }, current: false, customized: false }));
const opts = (over: Partial<FamilyOptions> = {}): FamilyOptions => ({ fold: true, keep: () => false, open: new Set(), scope: "s", ...over });
const draw = (p: PanelProvider, over: Partial<FamilyOptions> = {}) =>
  familyBlocks(rowsOf(p), opts(over))?.map((b) => `${b.family?.label ?? "-"}: ${b.rows.map((r) => `${r.model.id}${r.latest ? "^" : ""}`).join(", ")}${b.older ? ` (+${b.older}${b.open ? " open" : ""})` : ""}`);

describe("familyBlocks", () => {
  it("Claude: default headerless first, then one Claude series block ordered Fable -> Opus -> Sonnet -> Haiku with older versions combined", () => {
    expect(draw(max)).toEqual([
      "-: default",
      "Claude: fable^, opus^, sonnet^, haiku^ (+9)",
    ]);
  });

  it("unfolded, the Claude series reveals older versions grouped under their own tier", () => {
    expect(draw(max, { open: new Set(["s\nclaude-max\nclaude"]) })?.[1]).toBe(
      "Claude: fable^, claude-fable-5-1, claude-fable-5, opus^, claude-opus-5, claude-opus-4-8, claude-opus-4-7, claude-opus-4-6, sonnet^, claude-sonnet-5, claude-sonnet-4-6, haiku^, claude-haiku-4-5-20251001 (+9 open)",
    );
  });

  it("GPT: keeps newest of each GPT line visible in one GPT series header and combines older versions", () => {
    expect(draw(codex)).toEqual(["GPT: gpt-6.1-sol^, gpt-6-astra, gpt-6.1-terra^, gpt-6-luna^ (+4)"]);
  });

  it("a gateway's mixed list: series in first-mention order, per-line folding inside each series, unknown/custom last as Other", () => {
    expect(draw(gateway)).toEqual([
      "-: default",
      "Gemini: gemini-3.8-flash^, gemini-3.8-pro (+1)",
      "GLM: glm-5.3",
      "GPT: gpt-6.1-sol^ (+1)",
      "Claude: opus^ (+1)",
      "DeepSeek: deepseek-v4",
      "-: unknown-edge-model, vendor/model-x",
    ]);
    const blocks = familyBlocks(rowsOf(gateway), opts())!;
    expect(blocks[0].other).toBeUndefined();
    expect(blocks.slice(1, -1).every((b) => b.other === undefined)).toBe(true);
    expect(blocks.at(-1)?.other).toBe(true);
  });

  it("an older version in use stays out of the fold, in tier & version order, and reduces the combined fold count", () => {
    const used = new Set(["claude-opus-4-7", "claude-haiku-4-5-20251001"]);
    const out = draw(max, { keep: (r) => used.has(r.model.id) });
    expect(out).toEqual([
      "-: default",
      "Claude: fable^, opus^, claude-opus-4-7, sonnet^, haiku^, claude-haiku-4-5-20251001 (+7)",
    ]);
  });

  it("not folding (a search), every row shows grouped under its series", () => {
    expect(draw(max, { fold: false })?.[1]).toBe(
      "Claude: fable^, claude-fable-5-1, claude-fable-5, opus^, claude-opus-5, claude-opus-4-8, claude-opus-4-7, claude-opus-4-6, sonnet^, claude-sonnet-5, claude-sonnet-4-6, haiku^, claude-haiku-4-5-20251001",
    );
  });

  it("keeps every row of the newest version (its 1M-context one too) and marks none latest when nothing is older", () => {
    const p: PanelProvider = { ...max, models: [{ id: "claude-opus-5-5", name: "Opus 5.5", efforts: [] }, { id: "claude-opus-5-5[1m]", name: "Opus 5.5 (1M)", efforts: [] }, { id: "claude-opus-5", name: "Opus 5", efforts: [] }] };
    expect(draw(p)).toEqual(["Claude: claude-opus-5-5^, claude-opus-5-5[1m]^ (+1)"]);
    // two aliases of one model come from the hub as one row, the other spelling among its aliases (#10365)
    const same: PanelProvider = { ...max, models: [{ id: "opus", name: "Sonnet 5.5", resolved: "claude-sonnet-5-5", aliases: ["sonnet"], efforts: [] }, { id: "claude-sonnet-5", name: "Sonnet 5", efforts: [] }] };
    expect(draw(same)).toEqual(["Claude: opus^ (+1)"]);
  });

  it("keeps single-series lists without older versions flat and ungrouped", () => {
    const aliases: PanelProvider = { ...max, models: claudeMax.slice(0, 5) };
    expect(draw(aliases)).toBeUndefined();
    const flatGpt: PanelProvider = {
      ...codex,
      models: [
        { id: "gpt-6.1-sol", name: "GPT 6.1 Sol", efforts: [] },
        { id: "gpt-6-astra", name: "GPT 6 Astra", efforts: [] },
        { id: "gpt-oss-120b", name: "GPT-OSS 120B", efforts: [] },
      ],
    };
    expect(draw(flatGpt)).toBeUndefined();
  });

  it("activates series headers when two or more series exist even with zero older versions", () => {
    const multiFlat: PanelProvider = {
      id: "agy-flat",
      name: "agy",
      engine: "agy",
      models: [
        { id: "gemini-4-neon-high", name: "Gemini 4 Neon (High)", efforts: [] },
        { id: "gemini-pro-orchestrator-neon", name: "Orchestrator (Neon)", efforts: [] },
        { id: "claude-opus-5-5-low", name: "Claude Opus 5.5 (Low)", efforts: [] },
        { id: "claude-sonnet-5-5-low", name: "Claude Sonnet 5.5 (Low)", efforts: [] },
        { id: "gpt-oss-120b-medium", name: "GPT-OSS 120B (Medium)", efforts: [] },
      ],
    };
    expect(draw(multiFlat)).toEqual([
      "Gemini: gemini-4-neon-high, gemini-pro-orchestrator-neon",
      "Claude: claude-opus-5-5-low, claude-sonnet-5-5-low",
      "GPT: gpt-oss-120b-medium",
    ]);
  });
});

const input = (over: Partial<SectionsInput> = {}): SectionsInput => ({
  providers: [max, codex],
  state: { current: { provider: "codex", model: "gpt-6.1-sol" }, recommended: [] },
  view: { kind: "all" },
  query: "",
  favorites: [],
  recent: [],
  overrides: {},
  ...over,
});
const section = (o: Partial<SectionsInput>, key: string) => panelSections(input(o)).find((s) => s.key === key);
const ids = (o: Partial<SectionsInput>, key: string) => section(o, key)?.rows.map((r) => r.model.id);

describe("families in the panel's sections", () => {
  it("a provider group shows the newest of each line inside its series; its rows are the blocks' rows; no Show all for folded rows", () => {
    const s = section({}, "provider:claude-max");
    expect(s?.rows.map((r) => r.model.id)).toEqual(["default", "fable", "opus", "sonnet", "haiku"]);
    expect(s?.blocks?.flatMap((b) => b.rows)).toEqual(s?.rows);
    expect(s?.total).toBeUndefined();
  });

  it("a favorite's, a recent setup's, a customized and the running older model stay in sight", () => {
    const o: Partial<SectionsInput> = {
      favorites: [{ uid: "f", provider: "claude-max", model: "claude-opus-4-6" }],
      recent: [{ provider: "claude-max", model: "claude-sonnet-4-6" }],
      overrides: { "claude-max\nclaude-fable-5": { effort: "low" } },
      state: { current: { provider: "claude-max", model: "claude-haiku-4-5-20251001" }, recommended: [], starting: true },
    };
    expect(ids(o, "provider:claude-max")).toEqual(["default", "fable", "claude-fable-5", "opus", "claude-opus-4-6", "sonnet", "claude-sonnet-4-6", "haiku", "claude-haiku-4-5-20251001"]);
  });

  it("an older model of another provider with the same ID is not kept by this one's favorite", () => {
    expect(ids({ favorites: [{ uid: "f", provider: "codex", model: "claude-opus-4-6" }] }, "provider:claude-max")).not.toContain("claude-opus-4-6");
  });

  it("a search finds folded versions, by name or ID", () => {
    expect(ids({ query: "opus 4" }, "provider:claude-max")).toEqual(["claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6"]);
    expect(ids({ query: "5.6" }, "provider:codex")).toEqual(["gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"]);
  });

  it("unfolding opens the series for that group", () => {
    const rows = ids({ view: { kind: "provider", provider: "claude-max" }, openFamilies: new Set(["provider:claude-max\nclaude-max\nclaude"]) }, "provider:claude-max");
    expect(rows).toContain("claude-haiku-4-5-20251001");
    expect(rows).toContain("claude-opus-4-8");
    expect(ids({ view: { kind: "provider", provider: "claude-max" } }, "provider:claude-max")).not.toContain("claude-opus-4-8");
  });

  it("Recommended: the model that runs first, then its provider's series", () => {
    const s = section({ state: { current: { provider: "claude-max", model: "claude-opus-5" }, recommended: ["claude-max"] } }, "recommended");
    expect(s?.blocks?.map((b) => b.family?.label ?? b.key)).toEqual(["recommended\nnow", "recommended\nlead", "Claude"]);
    expect(s?.rows.map((r) => r.model.id)).toEqual(["claude-opus-5", "default", "fable", "opus", "sonnet", "haiku"]);
  });
});

// A gateway provider's list as the hub folds it (#10365)
const gatewayApi: PanelProvider = {
  id: "gateway-api",
  name: "Gateway API",
  engine: "claude",
  models: [
    { id: "default", name: "Default (recommended)", resolved: "claude-opus-5-5", efforts: [] },
    { id: "claude-opus-5-5", name: "Opus 5.5", description: "Custom Opus model (claude-opus-5-5)", aliases: ["opus", "fable", "sonnet", "haiku"], efforts: [] },
    { id: "kiro/claude-opus-5-5", name: "Opus 5.5 (kiro)", description: "From gateway", efforts: [] },
  ],
};

describe("a gateway's folded list", () => {
  it("one Opus 5.5 row, the route's own beside it, Default apart; Latest once an older version is listed", () => {
    expect(draw(gatewayApi)).toEqual(["-: default", "Claude: claude-opus-5-5, kiro/claude-opus-5-5"]);
    const names = familyBlocks(rowsOf(gatewayApi), opts())!.flatMap((b) => b.rows).map((r) => r.model.name);
    expect(names.filter((n) => n === "Opus 5.5")).toHaveLength(1);
    const older: PanelProvider = { ...gatewayApi, models: [...gatewayApi.models!, { id: "claude-opus-5", name: "Opus 5", efforts: [] }] };
    expect(draw(older)).toEqual(["-: default", "Claude: claude-opus-5-5^, kiro/claude-opus-5-5^ (+1)"]);
  });

  it("a setup saved with an alias is that row's: it runs, never folds, and a search for the alias finds it without a typed row", () => {
    const providers = [{ ...gatewayApi, typable: true }];
    const all = (o: Partial<SectionsInput>) => panelSections({ ...input(o), providers });
    const lit = all({ state: { current: { provider: "gateway-api", model: "sonnet" }, recommended: [] } }).flatMap((s) => s.rows).filter((r) => r.current);
    expect(lit.map((r) => r.model.id)).toEqual(["claude-opus-5-5"]);
    const found = all({ query: "haiku" }).flatMap((s) => s.rows);
    expect(found.map((r) => `${r.kind}:${r.model.id}`)).toEqual(["model:claude-opus-5-5"]);
  });
});

describe("the newest version under a search", () => {
  it("is each line's newest in the whole list, not among the rows that answer", () => {
    const s = panelSections(input({ query: "opus" })).find((x) => x.key === "provider:claude-max");
    expect(s?.rows.filter((r) => r.latest).map((r) => r.model.id)).toEqual(["opus"]);
    const older = panelSections(input({ query: "opus 4" })).find((x) => x.key === "provider:claude-max");
    expect(older?.rows.map((r) => `${r.model.id}${r.latest ? "^" : ""}`)).toEqual(["claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6"]);
  });
});

describe("Antigravity thinking/effort variants and cross-engine qualifiers", () => {
  it("groups Claude thinking and effort variants under Claude series with internal tier lines while rejecting invalid suffixes", () => {
    expect(modelFamily({ id: "claude-opus-5-5-high" })).toMatchObject({ key: "claude:opus", series: "claude", label: "Claude", version: [5, 5], rank: 1 });
    expect(modelFamily({ id: "claude-opus-4-6-medium" })).toMatchObject({ key: "claude:opus", series: "claude", label: "Claude", version: [4, 6] });
    expect(modelFamily({ id: "claude-opus-4-6-xhigh" })).toMatchObject({ key: "claude:opus", series: "claude", version: [4, 6] });
    expect(modelFamily({ id: "claude-opus-4-6-max" })).toMatchObject({ key: "claude:opus", series: "claude", version: [4, 6] });
    expect(modelFamily({ id: "claude-sonnet-4-6-thinking" })).toMatchObject({ key: "claude:sonnet", series: "claude", label: "Claude", version: [4, 6], rank: 2 });
    expect(modelFamily({ id: "claude-opus-4-6-thinking-high" })).toMatchObject({ key: "claude:opus", series: "claude", version: [4, 6] });
    expect(modelFamily({ id: "claude-opus-4-6-high-thinking" })).toMatchObject({ key: "claude:opus", series: "claude", version: [4, 6] });
    expect(modelFamily({ id: "claude-fable-5-2-thinking-xhigh" })).toMatchObject({ key: "claude:fable", series: "claude", version: [5, 2], rank: 0 });
    expect(modelFamily({ id: "claude-3-7-sonnet-20250219-thinking" })).toMatchObject({ key: "claude:sonnet", series: "claude", version: [3, 7] });
    expect(modelFamily({ id: "claude-3-7-sonnet-20250219-high" })).toMatchObject({ key: "claude:sonnet", series: "claude", version: [3, 7] });
    expect(modelFamily({ id: "claude-opus-4-6-thinking-20260101" })).toMatchObject({ key: "claude:opus", series: "claude", version: [4, 6] });
    expect(modelFamily({ id: "claude-opus-4-20250514-thinking" })).toMatchObject({ key: "claude:opus", series: "claude", version: [4] });
    expect(modelFamily({ id: "claude-sonnet-4.6-thinking-high" })).toMatchObject({ key: "claude:sonnet", series: "claude", version: [4, 6] });
    expect(modelFamily({ id: "anthropic/claude-opus-5-5-high[1m]" })).toMatchObject({ key: "claude:opus", series: "claude", version: [5, 5] });
    for (const bad of ["claude-opus-4-6-custom", "claude-opus-4-6-high-low", "claude-opus-4-6-20251001-20251002"]) {
      expect(modelFamily({ id: bad })).toBeUndefined();
    }
  });

  it("strips Gemini max, ultra, and thinking qualifiers without dropping standalone Ultra tiers", () => {
    expect(modelFamily({ id: "gemini-4-hydrogen-max" })).toMatchObject({ key: "gemini:hydrogen", series: "gemini", label: "Gemini", version: [4] });
    expect(modelFamily({ id: "gemini-4-neon-ultra" })).toMatchObject({ key: "gemini:neon", series: "gemini", label: "Gemini", version: [4] });
    expect(modelFamily({ id: "gemini-3.8-pro-max-preview" })).toMatchObject({ key: "gemini:pro", series: "gemini", label: "Gemini", version: [3, 8] });
    expect(modelFamily({ id: "gemini-2.0-flash-thinking-exp" })).toMatchObject({ key: "gemini:flash", series: "gemini", label: "Gemini", version: [2, 0] });
    expect(modelFamily({ id: "gemini-2.0-flash-thinking-exp-01-21" })).toMatchObject({ key: "gemini:flash", series: "gemini", label: "Gemini", version: [2, 0] });
    expect(modelFamily({ id: "gemini-1.0-ultra" })).toMatchObject({ key: "gemini:ultra", series: "gemini", label: "Gemini", version: [1, 0] });
    expect(modelFamily({ id: "gemini-4-ultra-high" })).toMatchObject({ key: "gemini:ultra", series: "gemini", label: "Gemini", version: [4] });
    expect(modelFamily({ id: "gemini-4-ultra-max" })).toMatchObject({ key: "gemini:ultra", series: "gemini", label: "Gemini", version: [4] });
    expect(modelFamily({ id: "gemini-3.8-high" })).toBeUndefined();
    expect(modelFamily({ id: "gemini-3.8-thinking" })).toBeUndefined();
  });

  it("strips GPT effort suffixes and pre-suffix date stamps without breaking real GPT lines", () => {
    for (const effort of ["minimal", "low", "medium", "high", "xhigh"]) {
      expect(modelFamily({ id: `gpt-5.4-${effort}` })).toMatchObject({ key: "gpt:", series: "gpt", label: "GPT", version: [5, 4] });
    }
    expect(modelFamily({ id: "gpt-5.4-2025-04-14-high" })).toMatchObject({ key: "gpt:", series: "gpt", label: "GPT", version: [5, 4] });
    expect(modelFamily({ id: "gpt-5.4-20250414-high" })).toMatchObject({ key: "gpt:", series: "gpt", label: "GPT", version: [5, 4] });
    expect(modelFamily({ id: "gpt-5.4-codex-2025-04-14-high" })).toMatchObject({ key: "gpt:codex", series: "gpt", label: "GPT", version: [5, 4] });
    expect(modelFamily({ id: "gpt-5.5-codex-high" })).toMatchObject({ key: "gpt:codex", series: "gpt", label: "GPT", version: [5, 5] });
    expect(modelFamily({ id: "gpt-5.5-codex-mini-low" })).toMatchObject({ key: "gpt:codex-mini", series: "gpt", label: "GPT", version: [5, 5] });
    expect(modelFamily({ id: "gpt-6.1-sol-xhigh" })).toMatchObject({ key: "gpt:sol", series: "gpt", label: "GPT", version: [6, 1] });
    expect(modelFamily({ id: "gpt-5.1-codex-max" })).toMatchObject({ key: "gpt:codex-max", series: "gpt", label: "GPT", version: [5, 1] });
    expect(modelFamily({ id: "gpt-5.1-codex-max-high" })).toMatchObject({ key: "gpt:codex-max", series: "gpt", label: "GPT", version: [5, 1] });
    expect(modelFamily({ id: "gpt-5.5-ultra" })).toMatchObject({ key: "gpt:ultra", series: "gpt", label: "GPT", version: [5, 5] });
    expect(modelFamily({ id: "gpt-5.5-ultra-high" })).toMatchObject({ key: "gpt:ultra", series: "gpt", label: "GPT", version: [5, 5] });
    expect(modelFamily({ id: "gpt-5.5-high-low" })).toBeUndefined();
    expect(modelFamily({ id: "gpt-5.5-max", resolved: "openai/gpt-5.5-max", variants: { base: "gpt-5.5" } })).toMatchObject({ key: "gpt:", series: "gpt", label: "GPT", version: [5, 5] });
    expect(modelFamily({ id: "gpt-5.5-high", resolved: "   " })).toMatchObject({ key: "gpt:", series: "gpt", label: "GPT", version: [5, 5] });
  });

  it("handles colon-separated effort/thinking suffixes and rejects unknown or double colon suffixes", () => {
    expect(modelFamily({ id: "google-antigravity/gemini-4-argon:high" })).toMatchObject({ key: "gemini:argon", series: "gemini", label: "Gemini", version: [4] });
    expect(modelFamily({ id: "google-antigravity/claude-opus-4-6-thinking:high" })).toMatchObject({ key: "claude:opus", series: "claude", label: "Claude", version: [4, 6] });
    expect(modelFamily({ id: "claude-3-7-sonnet-20250219-thinking:high" })).toMatchObject({ key: "claude:sonnet", series: "claude", label: "Claude", version: [3, 7] });
    expect(modelFamily({ id: "claude-opus-4-6-thinking:high[1m]" })).toMatchObject({ key: "claude:opus", series: "claude", label: "Claude", version: [4, 6] });
    expect(modelFamily({ id: "anthropic/claude-opus-4-6:high[1m]" })).toMatchObject({ key: "claude:opus", series: "claude", label: "Claude", version: [4, 6] });
    expect(modelFamily({ id: "anthropic/claude-sonnet-4-6:thinking" })).toMatchObject({ key: "claude:sonnet", series: "claude", label: "Claude", version: [4, 6] });
    expect(modelFamily({ id: "claude-opus-4-6-20260101:high" })).toMatchObject({ key: "claude:opus", series: "claude", label: "Claude", version: [4, 6] });
    expect(modelFamily({ id: "openai/gpt-5.5:xhigh" })).toMatchObject({ key: "gpt:", series: "gpt", label: "GPT", version: [5, 5] });
    expect(modelFamily({ id: "openai/gpt-5.5:max" })).toMatchObject({ key: "gpt:", series: "gpt", label: "GPT", version: [5, 5] });
    expect(modelFamily({ id: "gpt-5.1-codex-max:high" })).toMatchObject({ key: "gpt:codex-max", series: "gpt", label: "GPT", version: [5, 1] });
    expect(modelFamily({ id: "gemini-4-ultra:high" })).toMatchObject({ key: "gemini:ultra", series: "gemini", label: "Gemini", version: [4] });
    for (const bad of [
      "openai/gpt-5.5:custom",
      "openai/gpt-5.5:0",
      "openai/gpt-5.5:free",
      "openai/gpt-5.5:high:low",
      "claude-opus-4-6-high:low",
      "claude-opus-4-6-thinking:thinking",
      "claude-opus-4-6-thinking:thinking-high",
      "claude-opus-4-6-high-thinking:low",
      "claude-opus-4-6-thinking-high:low",
      "gpt-5.4-high:low",
      "gemini-4-argon-high:low",
      "gemini-4-hydrogen-max:high",
      "gemini-3.8:high",
    ]) {
      expect(modelFamily({ id: bad })).toBeUndefined();
    }
  });

  it("folds each internal line separately inside the shared GPT and Gemini series in panelSections", () => {
    const level = (base: string, name: string, effort: string, efforts: string[]): PanelModel => ({
      id: `${base}-${effort}`,
      name: `${name} (${effort})`,
      efforts,
      defaultEffort: effort,
    });
    const agy: PanelProvider = {
      id: "agy",
      name: "agy",
      engine: "agy",
      models: [
        ...["low", "high"].map((e) => level("claude-opus-5-5", "Opus 5.5", e, ["low", "high"])),
        ...["low", "high"].map((e) => level("claude-opus-4-6-thinking", "Opus 4.6", e, ["low", "high"])),
        ...["low", "max"].map((e) => level("gemini-4-hydrogen", "Gemini 4 Hydrogen", e, ["low", "max"])),
        ...["low", "high"].map((e) => level("gemini-3.8-hydrogen", "Gemini 3.8 Hydrogen", e, ["low", "high"])),
        ...["low", "max"].map((e) => level("gpt-5.5-codex", "GPT 5.5 Codex", e, ["low", "max"])),
        ...["low", "high"].map((e) => level("gpt-5.4-codex", "GPT 5.4 Codex", e, ["low", "high"])),
        { id: "gpt-5.1-codex-max", name: "GPT 5.1 Codex Max", efforts: [] },
        { id: "gpt-5.0-codex-max", name: "GPT 5.0 Codex Max", efforts: [] },
      ],
    };
    const renderBlocks = (query = "", currentModel = "gemini-4-hydrogen-max") =>
      panelSections({
        providers: [agy],
        state: { current: { provider: "agy", model: currentModel }, recommended: [] },
        view: { kind: "provider", provider: "agy" },
        query,
        favorites: [],
        recent: [],
        overrides: {},
      })
        .find((s) => s.key === "provider:agy")
        ?.blocks?.map((b) => `${b.family?.label ?? "-"}: ${b.rows.map((r) => `${r.model.id}${r.latest ? "^" : ""}`).join(", ")}${b.older ? ` (+${b.older})` : ""}`);

    // Folded gpt-5.5-codex-low/max stays latest in its own line while sharing the single GPT header with GPT 5.1 Codex Max
    expect(renderBlocks()).toEqual([
      "Claude: claude-opus-5-5-low^ (+1)",
      "Gemini: gemini-4-hydrogen-max^ (+1)",
      "GPT: gpt-5.5-codex-low^, gpt-5.1-codex-max^ (+2)",
    ]);

    // Single-level search match ("max") classifies gpt-5.5-codex-max inside GPT without hiding gpt-5.0-codex-max
    expect(renderBlocks("max", "claude-opus-5-5-low")).toEqual([
      "Gemini: gemini-4-hydrogen-max^",
      "GPT: gpt-5.5-codex-max^, gpt-5.1-codex-max^, gpt-5.0-codex-max",
    ]);

    // Search ("low") knows about newer max/ultra-only folded version in full catalog, so gpt-5.5 is not falsely marked Latest
    const mixed: PanelProvider = {
      id: "mixed",
      name: "mixed",
      engine: "agy",
      models: [
        ...["max", "ultra"].map((e) => level("gpt-5.6", "GPT 5.6", e, ["max", "ultra"])),
        ...["low", "high"].map((e) => level("gpt-5.5", "GPT 5.5", e, ["low", "high"])),
        ...["low", "high"].map((e) => level("gpt-5.4", "GPT 5.4", e, ["low", "high"])),
      ],
    };
    const lowSearch = panelSections({
      providers: [mixed],
      state: { current: null, recommended: [] },
      view: { kind: "provider", provider: "mixed" },
      query: "low",
      favorites: [],
      recent: [],
      overrides: {},
    }).find((s) => s.key === "provider:mixed");
    expect(lowSearch?.blocks?.map((b) => `${b.family?.label ?? "-"}: ${b.rows.map((r) => `${r.model.id}${r.latest ? "^" : ""}`).join(", ")}`)).toEqual([
      "GPT: gpt-5.5-low, gpt-5.4-low",
    ]);
  });

  it("classifies Orchestrator under Gemini and GPT-OSS under GPT while rejecting malformed suffixes", () => {
    for (const id of [
      "gemini-pro-orchestrator-neon",
      "gemini-pro-orchestrator",
      "gemini-pro-orchestrator-argon",
      "gemini-orchestrator-neon",
      "google-antigravity/gemini-pro-orchestrator-neon:high",
      "gemini-pro-orchestrator-neon[1m]",
    ]) {
      expect(modelFamily({ id })).toEqual({
        key: "gemini:orchestrator",
        series: "gemini",
        label: "Gemini",
        vendor: "gemini",
        version: [],
        unversioned: true,
      });
    }
    for (const bad of [
      "gemini-pro-orchestrator:custom",
      "gemini-pro-orchestrator:high:low",
      "gemini-pro-orchestrator-neon-high:low",
      "gemini-pro-orchestrator-neon-high-low",
    ]) {
      expect(modelFamily({ id: bad })).toBeUndefined();
    }

    for (const model of [
      { id: "gpt-oss-120b-medium" },
      { id: "openai/gpt-oss-120b-medium" },
      { id: "gpt-oss-120b" },
      { id: "gpt-oss-20b" },
      { id: "openai/gpt-oss-120b:high" },
      { id: "gpt-oss-120b-20250805-medium" },
      { id: "gpt-oss-120b-high", variants: { base: "gpt-oss-120b" } },
    ]) {
      expect(modelFamily(model)).toEqual({
        key: "gpt:oss",
        series: "gpt",
        label: "GPT",
        vendor: "gpt",
        version: [],
        unversioned: true,
      });
    }
    for (const bad of ["gpt-oss-120b-high-low", "gpt-oss-120b-high:low", "gpt-oss-120b:custom"]) {
      expect(modelFamily({ id: bad })).toBeUndefined();
    }
  });

  it("groups multi-series Antigravity catalogs under Gemini, Claude, GPT, DeepSeek, and Other", () => {
    const agyCatalog: PanelProvider = {
      id: "agy-series",
      name: "agy",
      engine: "agy",
      models: [
        { id: "gemini-4-neon-high", name: "Gemini 4 Neon (High)", efforts: ["low", "high"], defaultEffort: "high" },
        { id: "gemini-4-neon-low", name: "Gemini 4 Neon (Low)", efforts: ["low", "high"], defaultEffort: "low" },
        { id: "gemini-pro-orchestrator-neon", name: "Orchestrator (Neon)", efforts: [] },
        { id: "gemini-pro-orchestrator", name: "Orchestrator (Carbon)", efforts: [] },
        { id: "gemini-pro-orchestrator-argon", name: "Orchestrator (Argon)", efforts: [] },
        { id: "gemini-3.8-flash-high", name: "Gemini 3.8 Flash (High)", efforts: ["low", "high"], defaultEffort: "high" },
        { id: "gemini-3.5-flash-low", name: "Gemini 3.5 Flash (Low)", efforts: ["low"], defaultEffort: "low" },
        { id: "claude-opus-5-5-low", name: "Claude Opus 5.5 (Low)", efforts: ["low", "high"], defaultEffort: "low" },
        { id: "claude-sonnet-5-5-low", name: "Claude Sonnet 5.5 (Low)", efforts: ["low", "high"], defaultEffort: "low" },
        { id: "gpt-oss-120b-medium", name: "GPT-OSS 120B (Medium)", efforts: ["medium"], defaultEffort: "medium" },
        { id: "gpt-oss-20b", name: "GPT-OSS 20B", efforts: [] },
        { id: "deepseek-v4", name: "DeepSeek V4", efforts: [] },
        { id: "acme-custom-1", name: "Acme Custom 1", efforts: [] },
      ],
    };
    const section = panelSections({
      providers: [agyCatalog],
      state: { current: null, recommended: [] },
      view: { kind: "provider", provider: "agy-series" },
      query: "",
      favorites: [],
      recent: [],
      overrides: {},
    }).find((s) => s.key === "provider:agy-series");
    expect(
      section?.blocks?.map((b) => ({
        key: b.family?.key ?? (b.other ? "other" : "lead"),
        label: b.family?.label ?? (b.other ? "OTHER" : "-"),
        other: b.other,
        older: b.older,
        rows: b.rows.map((r) => `${r.model.id}${r.latest ? "^" : ""}`),
      })),
    ).toEqual([
      {
        key: "gemini",
        label: "Gemini",
        other: undefined,
        older: 1,
        rows: ["gemini-4-neon-high", "gemini-pro-orchestrator-neon", "gemini-pro-orchestrator", "gemini-pro-orchestrator-argon", "gemini-3.8-flash-high^"],
      },
      {
        key: "claude",
        label: "Claude",
        other: undefined,
        older: 0,
        rows: ["claude-opus-5-5-low", "claude-sonnet-5-5-low"],
      },
      {
        key: "gpt",
        label: "GPT",
        other: undefined,
        older: 0,
        rows: ["gpt-oss-120b-medium", "gpt-oss-20b"],
      },
      {
        key: "deepseek",
        label: "DeepSeek",
        other: undefined,
        older: 0,
        rows: ["deepseek-v4"],
      },
      {
        key: "other",
        label: "OTHER",
        other: true,
        older: 0,
        rows: ["acme-custom-1"],
      },
    ]);

    // All-view truncation keeps `other: true` on a surviving trailing block
    const truncated = panelSections({
      providers: [
        {
          id: "mix",
          name: "mix",
          engine: "claude",
          models: [
            { id: "claude-opus-5-5", name: "Opus 5.5", efforts: [] },
            { id: "claude-opus-5", name: "Opus 5", efforts: [] },
            { id: "acme-custom-1", name: "Acme Custom 1", efforts: [] },
          ],
        },
        { id: "second", name: "second", engine: "codex", models: [{ id: "gpt-5.5", name: "GPT 5.5", efforts: [] }] },
      ],
      state: { current: null, recommended: [] },
      view: { kind: "all" },
      query: "",
      favorites: [],
      recent: [],
      overrides: {},
    }).find((s) => s.key === "provider:mix");
    expect(truncated?.blocks?.at(-1)?.other).toBe(true);
  });
});
