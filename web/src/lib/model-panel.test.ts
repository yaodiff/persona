// The unified model panel's pure parts (#5032): which rows each view lists and in which groups, the search, the setup a
// row's pick sends, what is kept (favorites, recent setups, overrides), when a row can be picked or customized, and
// where the list scrolls and the flyout sits.
import { describe, expect, it } from "vitest";
import {
  addFavorite,
  ALL_GROUP_MAX,
  alignedScrollTop,
  contextSize,
  copySetup,
  effortColor,
  effortColorAt,
  flyoutPlace,
  levelSetup,
  matchesQuery,
  modelCount,
  overrideKey,
  panelSections,
  priceAmount,
  priceTier,
  pushRecent,
  RECENT_MAX,
  removeFavorite,
  resetOverride,
  rowBlock,
  rowCustomizable,
  rowSetup,
  sameSetup,
  setOverride,
  updateFavorite,
  variantGroups,
  type PanelFavorite,
  type PanelModel,
  type PanelProvider,
  type PanelState,
  type SectionsInput,
} from "./model-panel";

const CLAUDE_EFFORTS = ["low", "medium", "high", "xhigh", "max"];
const opus: PanelModel = { id: "opus", name: "Opus 5.5", resolved: "claude-opus-5-5", description: "Most capable", efforts: CLAUDE_EFFORTS, defaultEffort: "high", context: 1_000_000 };
const sonnet: PanelModel = { id: "sonnet", name: "Sonnet 5", resolved: "claude-sonnet-5", description: "Fast and smart", efforts: CLAUDE_EFFORTS, defaultEffort: "medium" };
const haiku: PanelModel = { id: "haiku", name: "Haiku 4.5", efforts: [] };
const typedBefore: PanelModel = { id: "vendor/model-x", name: "vendor/model-x", efforts: [], custom: true };
const gpt: PanelModel = { id: "gpt-5.5", name: "GPT 5.5", efforts: ["minimal", "low", "medium", "high", "xhigh"], defaultEffort: "medium", speeds: [{ id: "standard" }, { id: "fast" }] };

const claude = (over: Partial<PanelProvider> = {}): PanelProvider => ({ id: "claude", name: "Claude", engine: "claude", models: [opus, sonnet, haiku, typedBefore], typable: true, ...over });
const gateway = (over: Partial<PanelProvider> = {}): PanelProvider => ({ id: "gw", name: "Gateway", engine: "claude", models: [{ ...opus, id: "gw-opus", resolved: undefined, name: "Gateway Opus" }], ...over });
const codex = (over: Partial<PanelProvider> = {}): PanelProvider => ({ id: "codex", name: "Codex", engine: "codex", models: [gpt], ...over });

const state = (over: Partial<PanelState> = {}): PanelState => ({ current: { provider: "claude", model: "claude-opus-5-5", effort: "xhigh" }, recommended: ["claude"], ...over });

const input = (over: Partial<SectionsInput> = {}): SectionsInput => ({
  providers: [claude(), gateway(), codex()],
  state: state(),
  view: { kind: "all" },
  query: "",
  favorites: [],
  recent: [],
  overrides: {},
  ...over,
});

const shape = (o: Partial<SectionsInput> = {}) => panelSections(input(o)).map((s) => `${s.kind}${s.provider ? `:${s.provider.id}` : ""}${s.loading ? " (loading)" : ""} [${s.rows.map((r) => `${r.kind === "model" ? "" : `${r.kind}:`}${r.model.id}${r.current ? "*" : ""}`).join(", ")}]`);

describe("the sections of a view", () => {
  it("All: Recommended is the model that runs now, then the recommended providers' other models, which leave their own group", () => {
    expect(shape()).toEqual([
      "recommended [opus*, sonnet, haiku, vendor/model-x]",
      "provider:gw [gw-opus]",
      "provider:codex [gpt-5.5]",
    ]);
  });

  it("All: favorites come first and stay in their providers' groups too", () => {
    const favorites: PanelFavorite[] = [{ uid: "f1", provider: "gw", model: "gw-opus", effort: "low" }];
    expect(shape({ favorites })).toEqual([
      "favorites [favorite:gw-opus]",
      "recommended [opus*, sonnet, haiku, vendor/model-x]",
      "provider:gw [gw-opus]",
      "provider:codex [gpt-5.5]",
    ]);
  });

  it("All with nothing recommended: the model that runs now still leads, and only it leaves its group", () => {
    expect(shape({ state: state({ recommended: [] }) })).toEqual([
      "recommended [opus*]",
      "provider:claude [sonnet, haiku, vendor/model-x]",
      "provider:gw [gw-opus]",
      "provider:codex [gpt-5.5]",
    ]);
  });

  it("a provider's view lists its favorites, then all its models, the current one in place", () => {
    const favorites: PanelFavorite[] = [{ uid: "f1", provider: "gw", model: "gw-opus" }, { uid: "f2", provider: "claude", model: "sonnet", effort: "max" }];
    expect(shape({ view: { kind: "provider", provider: "claude" }, favorites })).toEqual([
      "favorites [favorite:sonnet]",
      "provider:claude [opus*, sonnet, haiku, vendor/model-x]",
    ]);
  });

  it("Favorites lists the saved setups only; Recent the last ones applied, the one running now marked", () => {
    const favorites: PanelFavorite[] = [{ uid: "f1", provider: "codex", model: "gpt-5.5", speed: "fast" }];
    expect(shape({ view: { kind: "favorites" }, favorites })).toEqual(["favorites [favorite:gpt-5.5]"]);
    const recent = [{ provider: "claude", model: "opus", effort: "xhigh" }, { provider: "claude", model: "opus", effort: "low" }, { provider: "gone", model: "x" }];
    expect(shape({ view: { kind: "recent" }, recent })).toEqual(["recent [recent:opus*, recent:opus]"]);
    expect(shape({ view: { kind: "recent" } })).toEqual([]);
    expect(shape({ view: { kind: "favorites" } })).toEqual([]);
  });

  it("a recent setup saved as a favorite carries that favorite (its star removes it)", () => {
    const favorites: PanelFavorite[] = [{ uid: "f9", provider: "claude", model: "opus", effort: "low" }];
    const [s] = panelSections(input({ view: { kind: "recent" }, favorites, recent: [{ provider: "claude", model: "opus", effort: "low" }, { provider: "claude", model: "opus", effort: "high" }] }));
    expect(s.rows.map((r) => r.starredUid)).toEqual(["f9", undefined]);
  });

  it("a provider whose list is being read shows its header and a spinner; one that failed or can't be used shows its header", () => {
    const providers = [claude(), gateway({ models: null, loading: true }), codex({ models: null, error: "offline" }), { ...codex(), id: "cx2", models: null, unavailable: "Needs a new conversation" }];
    expect(shape({ providers })).toEqual([
      "recommended [opus*, sonnet, haiku, vendor/model-x]",
      "provider:gw (loading) []",
      "provider:codex []",
      "provider:cx2 []",
    ]);
    // under a search, only the waiting one stays (and Claude Code's offer to use what is typed as a model ID)
    expect(shape({ providers, query: "zzz" })).toEqual(["provider:claude [typed:zzz]", "provider:gw (loading) []"]);
  });

  it("an emptied group leaves no header", () => {
    expect(shape({ providers: [claude({ models: [opus] }), codex()] })).toEqual(["recommended [opus*]", "provider:codex [gpt-5.5]"]);
  });
});

describe("a long list in the All view", () => {
  const many = (n: number, prefix: string): PanelModel[] => Array.from({ length: n }, (_, i) => ({ id: `${prefix}-${i}`, name: `${prefix} ${i}`, efforts: [] }));
  const big = (over: Partial<PanelProvider> = {}) => gateway({ models: many(20, "gw"), ...over });

  it("each provider group shows its first rows and how many it has, while several providers are listed", () => {
    const groupOf = (o: Partial<SectionsInput>) => panelSections(input(o)).find((s) => s.provider?.id === "gw")!;
    const g = groupOf({ providers: [claude(), big(), codex()] });
    expect(g.rows.map((r) => r.model.id)).toEqual(many(ALL_GROUP_MAX, "gw").map((m) => m.id));
    expect(g.total).toBe(20);
    // a short group is whole and has no total
    expect(panelSections(input({ providers: [claude(), big(), codex()] })).find((s) => s.provider?.id === "codex")!.total).toBeUndefined();
  });

  it("the model that runs now stays in its cut group; a provider's view, a search and a single provider show everything", () => {
    const running = state({ current: { provider: "gw", model: "gw-15" }, recommended: [], starting: true });
    const cut = panelSections(input({ providers: [claude(), big(), codex()], state: running })).find((s) => s.provider?.id === "gw")!;
    expect(cut.rows.map((r) => `${r.model.id}${r.current ? "*" : ""}`)).toEqual([...many(ALL_GROUP_MAX, "gw").map((m) => m.id), "gw-15*"]);
    expect(cut.total).toBe(20);
    const whole = (o: Partial<SectionsInput>) => panelSections(input({ providers: [claude(), big(), codex()], ...o })).find((s) => s.provider?.id === "gw");
    expect(whole({ view: { kind: "provider", provider: "gw" } })?.rows).toHaveLength(20);
    expect(whole({ query: "gw" })?.rows).toHaveLength(20);
    expect(panelSections(input({ providers: [big()], state: state({ current: null, recommended: [] }) }))[0].rows).toHaveLength(20);
  });
});

describe("the search", () => {
  it("matches every word against the name, id, concrete model, description and provider", () => {
    const p = claude();
    expect(matchesQuery(p, opus, "opus")).toBe(true);
    expect(matchesQuery(p, opus, "claude-opus-5-5")).toBe(true);
    expect(matchesQuery(p, opus, "CAPABLE most")).toBe(true);
    expect(matchesQuery(p, opus, "claude opus")).toBe(true);
    expect(matchesQuery(p, opus, "opus sonnet")).toBe(false);
    expect(matchesQuery(gateway(), gateway().models![0], "gateway")).toBe(true);
  });

  it("searches every provider whatever cell is on", () => {
    expect(shape({ view: { kind: "provider", provider: "codex" }, query: "opus" })).toEqual(["recommended [opus*]", "provider:gw [gw-opus]"]);
    expect(shape({ view: { kind: "favorites" }, query: "gpt" })).toEqual(["provider:claude [typed:gpt]", "provider:codex [gpt-5.5]"]);
  });

  it("offers a typed model ID as “Use …” in each provider that takes typed IDs and a pick here", () => {
    expect(shape({ query: "vendor/new-model" })).toEqual(["provider:claude [typed:vendor/new-model]"]);
    // not a valid ID, already listed, or a provider that can't take a pick here: nothing to offer
    expect(shape({ query: "two words" })).toEqual([]);
    expect(shape({ query: "vendor/model-x" })).toEqual(["recommended [vendor/model-x]"]);
    expect(shape({ query: "vendor/new-model", providers: [claude({ unavailable: "another engine" })] })).toEqual([]);
    expect(shape({ query: "vendor/new-model", providers: [claude({ locked: "--model" })] })).toEqual([]);
    // a provider that isn't recommended (an Agent's other launcher, switched to with the next message) offers it too
    expect(shape({ query: "vendor/new-model", state: state({ recommended: [] }) })).toEqual(["provider:claude [typed:vendor/new-model]"]);
    const [s] = panelSections(input({ query: "vendor/new-model" }));
    expect(s.rows[0].setup).toEqual({ provider: "claude", model: "vendor/new-model" });
    // a word that names a listed model is a search: no "Use" row beside its match
    expect(shape({ query: "son" })).not.toContain("provider:claude [typed:son]");
    expect(shape({ query: "son" }).join(" ")).not.toContain("typed:son");
  });
});

describe("the setup a row sends", () => {
  it("keeps actual session effort even when a stale model catalog omits it", () => {
    const current = { provider: "codex", model: "gpt-5.5", effort: "max" };
    expect(rowSetup(codex(), { ...gpt, defaultEffort: "low" }, state({ current })).effort).toBe("max");
    expect(rowSetup(codex(), { ...gpt, efforts: [] }, state({ current })).effort).toBe("max");
  });

  it("does not invent an unknown session effort from a browser customization", () => {
    expect(rowSetup(codex(), gpt, state({ current: { provider: "codex", model: "gpt-5.5" } }), { effort: "low" }).effort).toBeUndefined();
  });

  it("the model that runs now: what runs", () => {
    expect(rowSetup(claude(), opus, state())).toEqual({ provider: "claude", model: "opus", effort: "xhigh" });
    expect(rowSetup(codex(), gpt, state({ current: { provider: "codex", model: "gpt-5.5", effort: "low", speed: "fast" } }))).toEqual({ provider: "codex", model: "gpt-5.5", effort: "low", speed: "fast" });
  });

  it("another model opens at its own default, not at the effort running now (#12315)", () => {
    expect(rowSetup(claude(), sonnet, state())).toEqual({ provider: "claude", model: "sonnet" });
    expect(rowSetup(claude(), haiku, state())).toEqual({ provider: "claude", model: "haiku" });
    expect(rowSetup(codex(), gpt, state({ current: { provider: "claude", model: "opus", effort: "max" } }))).toEqual({ provider: "codex", model: "gpt-5.5" });
  });

  it("an override wins for its effort, and is the only source of Fast", () => {
    expect(rowSetup(claude(), sonnet, state(), { effort: "low" })).toEqual({ provider: "claude", model: "sonnet", effort: "low" });
    expect(rowSetup(codex(), gpt, state(), { effort: "high", speed: "fast" })).toEqual({ provider: "codex", model: "gpt-5.5", effort: "high", speed: "fast" });
    // Fast on a model that has none, an effort it doesn't take: left out
    expect(rowSetup(claude(), sonnet, state(), { effort: "ultra", speed: "fast" })).toEqual({ provider: "claude", model: "sonnet" });
  });

  it("a saved copy drops what the model no longer takes; one whose list isn't read stays as it is", () => {
    expect(copySetup({ provider: "claude", model: "opus", effort: "ultra", speed: "fast" }, opus, true)).toEqual({ provider: "claude", model: "opus" });
    expect(copySetup({ provider: "codex", model: "gpt-5.5", effort: "low", speed: "fast" }, gpt, true)).toEqual({ provider: "codex", model: "gpt-5.5", effort: "low", speed: "fast" });
    expect(copySetup({ provider: "x", model: "m", effort: "low" }, { id: "m", name: "m", efforts: [] }, false)).toEqual({ provider: "x", model: "m", effort: "low" });
  });

  it("rows of the current model carry its override flag; favorites their copy", () => {
    const favorites: PanelFavorite[] = [{ uid: "f1", provider: "claude", model: "sonnet", effort: "low" }];
    const sections = panelSections(input({ favorites, overrides: { [overrideKey("claude", "sonnet")]: { effort: "max" } } }));
    const fav = sections[0].rows[0];
    expect(fav.setup).toEqual({ provider: "claude", model: "sonnet", effort: "low" });
    const row = sections[1].rows.find((r) => r.model.id === "sonnet")!;
    expect(row.customized).toBe(true);
    expect(row.setup.effort).toBe("max");
  });
});

describe("what is kept", () => {
  it("favorites: a copy each, an identical setup once, removed by uid, edited without twins", () => {
    let f = addFavorite([], { provider: "claude", model: "opus", effort: "high" }, "a");
    expect(f).toEqual([{ uid: "a", provider: "claude", model: "opus", effort: "high" }]);
    expect(addFavorite(f, { provider: "claude", model: "opus", effort: "high" }, "b")).toBe(f);
    f = addFavorite(f, { provider: "claude", model: "opus", effort: "low" }, "b");
    expect(f.map((x) => x.uid)).toEqual(["a", "b"]);
    expect(updateFavorite(f, "b", { effort: "max" })).toEqual([f[0], { uid: "b", provider: "claude", model: "opus", effort: "max" }]);
    // made the same as "a": "b" goes
    expect(updateFavorite(f, "b", { effort: "high" })).toEqual([f[0]]);
    expect(removeFavorite(f, "a").map((x) => x.uid)).toEqual(["b"]);
  });

  it("recent: most recent first, an identical setup moves up, at most five", () => {
    let r: { provider: string; model: string; effort?: string }[] = [];
    for (const m of ["a", "b", "c", "d", "e", "f"]) r = pushRecent(r, { provider: "p", model: m });
    expect(r.map((c) => c.model)).toEqual(["f", "e", "d", "c", "b"]);
    expect(r).toHaveLength(RECENT_MAX);
    r = pushRecent(r, { provider: "p", model: "c" });
    expect(r.map((c) => c.model)).toEqual(["c", "f", "e", "d", "b"]);
    r = pushRecent(r, { provider: "p", model: "c", effort: "low" });
    expect(r.map((c) => `${c.model}${c.effort ?? ""}`)).toEqual(["clow", "c", "f", "e", "d"]);
  });

  it("overrides: merged per model, an emptied one goes, reset removes it", () => {
    let o = setOverride({}, "claude", "opus", { effort: "low" });
    o = setOverride(o, "claude", "opus", { speed: "fast" });
    expect(o).toEqual({ [overrideKey("claude", "opus")]: { effort: "low", speed: "fast" } });
    expect(setOverride(o, "claude", "opus", { effort: undefined, speed: undefined })).toEqual({});
    expect(resetOverride(o, "claude", "opus")).toEqual({});
    expect(resetOverride(o, "claude", "sonnet")).toBe(o);
  });

  it("the same setup ignores empty parts", () => {
    expect(sameSetup({ provider: "p", model: "m", effort: "", speed: "standard" }, { provider: "p", model: "m" })).toBe(true);
    expect(sameSetup({ provider: "p", model: "m", speed: "fast" }, { provider: "p", model: "m" })).toBe(false);
  });
});

describe("what can be picked and customized", () => {
  it("not while busy, on a provider that can't take a pick, a locked launcher or a list not read; each says why", () => {
    expect(rowBlock(claude(), {})).toBeUndefined();
    expect(rowBlock(claude(), { busy: "Working" })).toEqual({ reason: "Working" });
    expect(rowBlock(codex({ unavailable: "Needs a new conversation" }), {})).toEqual({ reason: "Needs a new conversation" });
    expect(rowBlock(claude({ locked: "Fixed by the launcher" }), {})).toEqual({ reason: "Fixed by the launcher" });
    expect(rowBlock(claude({ models: null }), {})).toEqual({ reason: "" });
    // a list shown from before and read again behind it can be picked
    expect(rowBlock(claude({ loading: true }), {})).toBeUndefined();
  });

  it("the model that runs now can be picked back while a switch waits (it drops the switch), unless the chat is busy", () => {
    const fixed = codex({ unavailable: "Codex can't change the model" });
    const waiting = { pending: { provider: "claude", model: "opus" } };
    expect(rowBlock(fixed, waiting, true)).toBeUndefined();
    // its other rows stay off, and so does it with no switch waiting
    expect(rowBlock(fixed, waiting)).toEqual({ reason: "Codex can't change the model" });
    expect(rowBlock(fixed, {}, true)).toEqual({ reason: "Codex can't change the model" });
    expect(rowBlock(fixed, { ...waiting, busy: "Working" }, true)).toEqual({ reason: "Working" });
  });

  it("the model that runs now keeps its flyout when edits apply live, even when picks are off", () => {
    const sections = panelSections(input({ providers: [codex({ unavailable: "Codex can't change the model" })], state: state({ current: { provider: "codex", model: "gpt-5.5" }, recommended: [] }) }));
    const row = sections[0].rows[0];
    expect(row.current).toBe(true);
    expect(rowCustomizable(row, {}, true)).toBe(true);
    expect(rowCustomizable(row, {}, false)).toBe(false);
    expect(rowCustomizable(row, { busy: "Working" }, true)).toBe(true);
    const other = panelSections(input())[1].rows[0];
    expect(rowCustomizable(other, {}, true)).toBe(true);
    expect(rowCustomizable(other, { busy: "Working" }, true)).toBe(false);
    expect(rowCustomizable({ ...other, kind: "recent" }, {}, true)).toBe(false);
  });
});

describe("geometry and colors", () => {
  it("puts the current row's middle at about 35% of the list, within its scroll range", () => {
    expect(alignedScrollTop({ clientHeight: 400, scrollHeight: 2000, rowTop: 1000, rowBottom: 1054 })).toBe(Math.round(1027 - 140));
    expect(alignedScrollTop({ clientHeight: 400, scrollHeight: 2000, rowTop: 20, rowBottom: 74 })).toBe(0);
    expect(alignedScrollTop({ clientHeight: 400, scrollHeight: 2000, rowTop: 1960, rowBottom: 2000 })).toBe(1600);
    expect(alignedScrollTop({ clientHeight: 40, scrollHeight: 2000, rowTop: 500, rowBottom: 560 })).toBe(500);
  });

  it("places the flyout left of the panel, right without room, its top 12 px above the row's within the panel", () => {
    const panel = { left: 600, right: 1000, top: 200, bottom: 700 };
    expect(flyoutPlace({ row: { left: 660, right: 990, top: 400, bottom: 454 }, panel, width: 264, height: 240, screenW: 1200, screenH: 900 })).toEqual({ left: 332, top: 388, side: "left" });
    // near the panel's bottom: held so its bottom meets the panel's
    expect(flyoutPlace({ row: { left: 660, right: 990, top: 680, bottom: 700 }, panel, width: 264, height: 240, screenW: 1200, screenH: 900 }).top).toBe(460);
    // no room on the left
    expect(flyoutPlace({ row: { left: 60, right: 390, top: 300, bottom: 354 }, panel: { ...panel, left: 50, right: 450 }, width: 264, height: 240, screenW: 1200, screenH: 900 })).toEqual({ left: 454, top: 288, side: "right" });
    // room on neither side: kept in the window
    const tight = flyoutPlace({ row: { left: 20, right: 300, top: 300, bottom: 354 }, panel: { left: 10, right: 310, top: 200, bottom: 700 }, width: 264, height: 240, screenW: 320, screenH: 900 });
    expect(tight.left).toBeGreaterThanOrEqual(8);
    expect(tight.left + 264).toBeLessThanOrEqual(320 - 8 + 0.5);
  });

  it("colors an effort by the level itself, mixing two while dragging", () => {
    expect(effortColor("high")).toBe("var(--effort-high)");
    expect(effortColor("ultra")).toBe("var(--effort-max)");
    expect(effortColor("minimal")).toBe("var(--effort-minimal)");
    expect(effortColor("someday")).toBe("var(--effort-medium)");
    expect(effortColorAt(["low", "medium", "high"], 1)).toBe("var(--effort-medium)");
    expect(effortColorAt(["low", "medium", "high"], 1.25)).toBe("color-mix(in srgb, var(--effort-high) 25%, var(--effort-medium))");
  });

  it("maps known output prices to tiers and writes small prices without losing them", () => {
    expect(priceTier({ input: 1, output: 3 })).toBe(1);
    expect(priceTier({ input: 2, output: 10 })).toBe(2);
    expect(priceTier({ input: 4, output: 20 })).toBe(3);
    expect(priceTier({ input: 4, output: Number.NaN })).toBeUndefined();
    expect(priceTier({ input: -1, output: 20 })).toBeUndefined();
    expect(priceTier({ input: 4, output: 20, cache_read: -0.1 })).toBeUndefined();
    expect(priceAmount(20)).toBe("20");
    expect(priceAmount(0.2)).toBe("0.2");
    expect(priceAmount(0.0036)).toBe("0.0036");
    expect(priceAmount(1.23456)).toBe("1.23");
  });

  it("writes context windows short", () => {
    expect(contextSize(200_000)).toBe("200K");
    expect(contextSize(1_000_000)).toBe("1M");
    expect(contextSize(999_600)).toBe("1M");
    expect(contextSize(272_000)).toBe("272K");
    expect(contextSize(1_500_000)).toBe("1.5M");
  });
});

describe("the running model's row", () => {
  it("is the row named exactly what runs, not also an alias standing for it", () => {
    const dflt: PanelModel = { id: "default", name: "Default", efforts: [], resolved: "gw-model" };
    const typed: PanelModel = { id: "gw-model", name: "gw-model", efforts: [], custom: true };
    const p: PanelProvider = { id: "gw", name: "gw", engine: "claude", models: [dflt, typed] };
    const lit = panelSections({ providers: [p], state: { current: { provider: "gw", model: "gw-model" }, recommended: ["gw"] }, view: { kind: "provider", provider: "gw" }, query: "", favorites: [], recent: [], overrides: {} })
      .flatMap((s) => s.rows).filter((r) => r.current).map((r) => r.model.id);
    expect(lit).toEqual(["gw-model"]);
    // an alias still lights when no row is named what runs
    const alias = panelSections({ providers: [{ ...p, models: [dflt] }], state: { current: { provider: "gw", model: "gw-model" }, recommended: ["gw"] }, view: { kind: "provider", provider: "gw" }, query: "", favorites: [], recent: [], overrides: {} })
      .flatMap((s) => s.rows).filter((r) => r.current).map((r) => r.model.id);
    expect(alias).toEqual(["default"]);
  });

  it("shows its Fast as it runs, not a saved override", () => {
    const m: PanelModel = { id: "gpt-x", name: "x", efforts: ["low", "high"], speeds: [{ id: "standard" }, { id: "fast" }] };
    const p = { id: "codex", models: [m] };
    expect(rowSetup(p, m, { current: { provider: "codex", model: "gpt-x" } }, { speed: "fast" }).speed).toBeUndefined();
    expect(rowSetup(p, m, { current: { provider: "codex", model: "gpt-x", speed: "fast" } }, {}).speed).toBe("fast");
    // another model's row still takes its override
    expect(rowSetup(p, m, { current: { provider: "codex", model: "other" } }, { speed: "fast" }).speed).toBe("fast");
  });
});


// Antigravity lists one model per thinking level, in its own order (`agy models`); the node marks each level
// (server agyModelList: `default_effort` is the ID's suffix, `efforts` the levels listed)
describe("a model listed per thinking level (#12315)", () => {
  const LEVELS = ["low", "medium", "high", "xhigh"];
  const level = (base: string, name: string, effort: string, efforts = LEVELS): PanelModel => ({ id: `${base}-${effort}`, name: `${name} (${effort === "xhigh" ? "XHigh" : effort.charAt(0).toUpperCase() + effort.slice(1)})`, efforts, defaultEffort: effort });
  const neon = ["high", "xhigh", "medium", "low"].map((e) => level("gemini-4-neon", "Gemini 4 Neon", e));
  const flash = ["high", "medium", "low"].map((e) => level("gemini-3.8-flash", "Gemini 3.8 Flash", e, ["low", "medium", "high"]));
  const orchestrator: PanelModel = { id: "gemini-pro-orchestrator-neon", name: "Orchestrator (Neon)", efforts: [] };
  const single = level("gpt-oss-120b", "GPT-OSS 120B", "medium", ["medium"]);
  const agy = (over: Partial<PanelProvider> = {}): PanelProvider => ({ id: "agy", name: "agy", engine: "agy", models: [...neon, orchestrator, ...flash, single], ...over });
  const rowsOf = (o: Partial<SectionsInput> = {}) => panelSections(input({ providers: [agy()], state: { current: null, recommended: [] }, view: { kind: "provider", provider: "agy" }, ...o })).flatMap((s) => s.rows);

  it("folds a model's levels into one row named for the model, the levels weakest first", () => {
    const rows = rowsOf();
    expect(rows.map((r) => r.model.name)).toEqual(["Gemini 4 Neon", "Orchestrator (Neon)", "Gemini 3.8 Flash", "GPT-OSS 120B (Medium)"]);
    expect(rows[0].key).toBe("model:agy\ngemini-4-neon");
    expect(rows[0].model.efforts).toEqual(LEVELS);
    expect(rows[0].model.variants).toEqual({
      base: "gemini-4-neon",
      first: "medium",
      levels: LEVELS.map((e) => ({ effort: e, id: `gemini-4-neon-${e}` })),
    });
    // a model with one level listed, and a model whose name only looks like one, stay as they are
    expect(rows[1].model.variants).toBeUndefined();
    expect(rows[3].model).toEqual(single);
  });

  it("counts each model once", () => {
    expect(modelCount(agy().models!)).toBe(4);
    expect(variantGroups(agy().models!).map((g) => g.length)).toEqual([4, 1, 3, 1]);
    // a Default row is no model of its own
    expect(modelCount([{ id: "default", name: "Default", efforts: [], providerDefault: true }, ...neon])).toBe(1);
  });

  it("keeps all advertised Neon and Hydrogen levels attached to their own row", () => {
    const hydrogenLevels = [...LEVELS, "max"];
    const hydrogen = [...hydrogenLevels].reverse().map((e) => level("gemini-4-hydrogen", "Gemini 4 Hydrogen", e, hydrogenLevels));
    const rows = rowsOf({ providers: [agy({ models: [...neon, ...hydrogen] })] });
    expect(rows.map((row) => [row.model.name, row.model.efforts])).toEqual([
      ["Gemini 4 Neon", LEVELS],
      ["Gemini 4 Hydrogen", hydrogenLevels],
    ]);
    expect(levelSetup(rows[0], "xhigh")?.model).toBe("gemini-4-neon-xhigh");
    expect(levelSetup(rows[1], "xhigh")?.model).toBe("gemini-4-hydrogen-xhigh");
    expect(levelSetup(rows[1], "max")?.model).toBe("gemini-4-hydrogen-max");
  });

  it("keeps context qualifiers on a folded row", () => {
    const models = [level("m", "Model (1M context)", "low"), { ...level("m", "Model", "high"), name: "Model (High) (1M context)" }];
    expect(rowsOf({ providers: [agy({ models })] })[0].model.name).toBe("Model (1M context)");
  });

  it("leaves models alone whose default effort is not their ID's last word", () => {
    expect(variantGroups([opus, sonnet, gpt]).map((g) => g.map((m) => m.id))).toEqual([["opus"], ["sonnet"], ["gpt-5.5"]]);
    const typed = { ...neon[0], custom: true };
    expect(variantGroups([typed, neon[1]]).map((g) => g.length)).toEqual([1, 1]);
  });

  it("stands for Medium (agy names no default) and sends that level's ID alone; without Medium, the list's first", () => {
    const [row] = rowsOf();
    expect(row.model.id).toBe("gemini-4-neon-medium");
    expect(row.model.defaultEffort).toBe("medium");
    expect(row.setup).toEqual({ provider: "agy", model: "gemini-4-neon-medium" });
    const noMedium = ["high", "xhigh", "low"].map((e) => level("m", "M", e));
    expect(rowsOf({ providers: [agy({ models: noMedium })] })[0].model.id).toBe("m-high");
    expect(row.current).toBe(false);
  });

  it("stands for the level that runs, keeping what runs", () => {
    const [row] = rowsOf({ state: { current: { provider: "agy", model: "gemini-4-neon-low", effort: "low" }, recommended: [] } });
    expect(row.current).toBe(true);
    expect(row.model.id).toBe("gemini-4-neon-low");
    expect(row.setup).toEqual({ provider: "agy", model: "gemini-4-neon-low", effort: "low" });
  });

  it("does not carry the running effort into another level's pick", () => {
    const rows = rowsOf({ state: { current: { provider: "agy", model: "gemini-4-neon-xhigh", effort: "xhigh" }, recommended: [] } });
    const flashRow = rows.find((r) => r.model.variants?.base === "gemini-3.8-flash")!;
    expect(flashRow.setup).toEqual({ provider: "agy", model: "gemini-3.8-flash-medium" });
  });

  it("stands for the level a switch waits for, else the one chosen in its flyout", () => {
    const waiting = rowsOf({ state: { current: null, pending: { provider: "agy", model: "gemini-4-neon-medium" }, recommended: [] } });
    expect(waiting[0].model.id).toBe("gemini-4-neon-medium");
    const chosen = rowsOf({ overrides: { [overrideKey("agy", "gemini-4-neon")]: { effort: "xhigh" } } });
    expect(chosen[0].model.id).toBe("gemini-4-neon-xhigh");
    expect(chosen[0].setup).toEqual({ provider: "agy", model: "gemini-4-neon-xhigh" });
    expect(chosen[0].customized).toBe(true);
    // a level the list no longer has: the first again
    const gone = rowsOf({ overrides: { [overrideKey("agy", "gemini-4-neon")]: { effort: "max" } } });
    expect(gone[0].model.id).toBe("gemini-4-neon-medium");
  });

  it("a level picked in the flyout applies that level's model", () => {
    const [row] = rowsOf({ state: { current: { provider: "agy", model: "gemini-4-neon-high" }, recommended: [] } });
    expect(levelSetup(row, "low")).toEqual({ provider: "agy", model: "gemini-4-neon-low" });
    expect(levelSetup(row, "max")).toBeUndefined();
    expect(levelSetup(rowsOf()[1], "low")).toBeUndefined();
  });

  it("a search folds the levels it finds; one level found is that level's own row", () => {
    expect(rowsOf({ query: "neon" }).map((r) => r.model.name)).toEqual(["Gemini 4 Neon", "Orchestrator (Neon)"]);
    expect(rowsOf({ query: "neon low" }).map((r) => `${r.model.name} ${r.model.id}`)).toEqual(["Gemini 4 Neon (Low) gemini-4-neon-low"]);
  });

  it("names the row by its base ID when the levels' names disagree", () => {
    const odd = [level("m", "One", "low"), { ...level("m", "Two", "high") }];
    const [row] = rowsOf({ providers: [agy({ models: odd })] });
    expect(row.model.name).toBe("m");
  });
});
