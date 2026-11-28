import { describe, expect, it } from "vitest";
import { groupByProvider } from "./usage-popover";
import { type UsageAccount, type UsageWindow } from "./usage-windows";
import { unifiedTimeline } from "./usage-timeline";
import {
  buildUsageTable,
  detectLocalExternalOverlaps,
  groupAccountWindows,
  nextTimelineReset,
  shortPoolLabel,
  shouldMergeSingleAccountHeader,
  tableWindowSubLabel,
  windowRowDisplay,
} from "./usage-popover-model";

const NOW = new Date(2026, 10, 5, 10, 0, 0).getTime();
const H = 3600_000;
const at = (ms: number) => new Date(NOW + ms).toISOString();
const win = (label: string, minutes: number, used: number, resetsIn?: number, extra: Partial<UsageWindow> = {}): UsageWindow => ({
  label,
  window_minutes: minutes,
  used_percent: used,
  ...(resetsIn === undefined ? {} : { resets_at: at(resetsIn) }),
  ...extra,
});
const acct = (id: string, provider: string, name: string, windows: UsageWindow[], extra: Partial<UsageAccount> = {}): UsageAccount => ({
  id,
  provider,
  name,
  windows,
  sampled_at: at(-30_000),
  ...extra,
});

describe("groupAccountWindows", () => {
  it("groups Antigravity multi-window model pools under one heading per pool without repeating", () => {
    const agy = acct("agy", "antigravity", "Antigravity", [
      win("5h", 300, 20, 2 * H, { model: "Claude and GPT models" }),
      win("5h", 300, 35, 3 * H, { model: "Gemini Models" }),
      win("7d", 10080, 45, 48 * H, { model: "Claude and GPT models" }),
      win("7d", 10080, 60, 72 * H, { model: "Gemini Models" }),
    ]);
    const sections = groupAccountWindows(agy);
    expect(sections).toHaveLength(2);
    expect(sections.map((s) => (s.kind === "group" ? [s.source, s.title, s.windows.map((w) => w.label)] : s.kind))).toEqual([
      ["model", "Claude and GPT models", ["5h", "7d"]],
      ["model", "Gemini Models", ["5h", "7d"]],
    ]);
  });

  it("renders a single-window per-model allowance alongside plan windows as a compact inline row", () => {
    const claude = acct("cl", "claude", "Claude #alpha", [
      win("5h", 300, 25, 2 * H),
      win("7d", 10080, 45, 48 * H),
      win("7d", 10080, 82, 48 * H, { model: "Fable" }),
    ]);
    const sections = groupAccountWindows(claude);
    expect(sections).toEqual([
      { kind: "plain", windows: [claude.windows[0], claude.windows[1]] },
      { kind: "inline-model", model: "Fable", window: claude.windows[2] },
    ]);
  });

  it("keeps explicit extension groups under one heading in extension order", () => {
    const ext = acct("ext", "claude", "Gateway pool", [
      win("5h", 300, 20, 2 * H, { group: "Coding Pool", title: "Burst", note: "Shared across IDE and CLI" }),
      win("7d", 10080, 40, 48 * H, { group: "Coding Pool", title: "Weekly" }),
      win("7d", 10080, 75, 72 * H, { group: "Reasoning Pool", note: "Reserves 10% headroom" }),
    ]);
    const sections = groupAccountWindows(ext);
    expect(sections.map((s) => (s.kind === "group" ? [s.source, s.title, s.windows.map((w) => w.title ?? w.label)] : s.kind))).toEqual([
      ["group", "Coding Pool", ["Burst", "Weekly"]],
      ["group", "Reasoning Pool", ["7d"]],
    ]);
  });

  it("uses consistent group headings across sibling model pools even if one pool currently has only one window", () => {
    const mixed = acct("agy-partial", "antigravity", "Antigravity", [
      win("5h", 300, 15, 2 * H, { model: "Claude and GPT models" }),
      win("7d", 10080, 40, 48 * H, { model: "Claude and GPT models" }),
      win("7d", 10080, 25, 72 * H, { model: "Gemini Models" }),
    ]);
    const sections = groupAccountWindows(mixed);
    expect(sections.every((s) => s.kind === "group")).toBe(true);
    expect(sections.map((s) => (s.kind === "group" ? s.title : ""))).toEqual(["Claude and GPT models", "Gemini Models"]);
  });
});

describe("windowRowDisplay", () => {
  it("always keeps window span in column 1 and deduplicates extension title against model, label, and group", () => {
    expect(windowRowDisplay(win("5h", 300, 10, 2 * H, { title: "Burst", note: "Fast bucket" }))).toEqual({
      span: "5h",
      caption: "Burst",
      isModelCaption: false,
      hasTitle: true,
      note: "Fast bucket",
    });
    expect(windowRowDisplay(win("5h", 300, 10, 2 * H, { title: "5h" }))).toEqual({
      span: "5h",
      caption: "",
      isModelCaption: false,
      hasTitle: false,
      note: "",
    });
    // Regression: { label: "7d", model: "Fable", title: "Fable" } or title "7 days · Fable" must NEVER duplicate Fable or lose "7d"
    expect(windowRowDisplay(win("7d", 10080, 80, 48 * H, { model: "Fable", title: "Fable" }), "Fable")).toEqual({
      span: "7d",
      caption: "Fable",
      isModelCaption: true,
      hasTitle: false,
      note: "",
    });
    expect(windowRowDisplay(win("7d", 10080, 80, 48 * H, { model: "Fable", title: "7 days · Fable" }), "Fable")).toEqual({
      span: "7d",
      caption: "Fable",
      isModelCaption: true,
      hasTitle: false,
      note: "",
    });
    expect(windowRowDisplay(win("7d", 10080, 80, 48 * H, { model: "Fable", title: "fable" }), "Fable")).toEqual({
      span: "7d",
      caption: "Fable",
      isModelCaption: true,
      hasTitle: false,
      note: "",
    });
    // Distinct extra title alongside model is kept without repeating model
    expect(windowRowDisplay(win("7d", 10080, 80, 48 * H, { model: "Fable", title: "Extra" }), "Fable")).toEqual({
      span: "7d",
      caption: "Fable · Extra",
      isModelCaption: true,
      hasTitle: true,
      note: "",
    });
    // Title repeating group context is suppressed
    expect(windowRowDisplay(win("5h", 300, 20, 2 * H, { title: "5h · Gemini Models" }), undefined, "Gemini Models")).toEqual({
      span: "5h",
      caption: "",
      isModelCaption: false,
      hasTitle: false,
      note: "",
    });
  });

  it("merges single-account header only when the sole account name matches provider title", () => {
    const agy = acct("a", "antigravity", "Antigravity", [win("5h", 300, 10, 2 * H)]);
    const named = acct("b", "antigravity", "Antigravity · Main", [win("5h", 300, 10, 2 * H)]);
    expect(shouldMergeSingleAccountHeader({ accounts: [agy] }, "Antigravity")).toBe(true);
    expect(shouldMergeSingleAccountHeader({ accounts: [named] }, "Antigravity")).toBe(false);
    expect(shouldMergeSingleAccountHeader({ accounts: [agy, named] }, "Antigravity")).toBe(false);
  });
});

describe("detectLocalExternalOverlaps", () => {
  it("flags local-only accounts whose active reset timestamp matches an external account under the same provider", () => {
    const localClaude = acct("local-1", "claude", "Claude", [win("7d", 10080, 30, 48 * H)], { local: true, machines: ["Workstation"] });
    const extClaude = acct("ext-1", "claude", "Claude #alpha", [win("7d", 10080, 32, 48 * H + 15_000)], { sources: ["Gateway"] });
    const otherClaude = acct("ext-2", "claude", "Claude #beta", [win("7d", 10080, 10, 96 * H)], { sources: ["Gateway"] });
    const mergedClaude = acct("merged-1", "claude", "Claude #merged", [win("7d", 10080, 30, 48 * H)], { local: true, machines: ["Workstation"], sources: ["Gateway"] });

    const overlaps = detectLocalExternalOverlaps([localClaude, extClaude, otherClaude, mergedClaude], NOW);
    expect(overlaps.get("local-1")).toBe("Claude #alpha");
    expect(overlaps.has("ext-1")).toBe(false);
    expect(overlaps.has("merged-1")).toBe(false);
  });

  it("matches agy/antigravity alias scopes and ignores different providers or distant reset times", () => {
    const localAgy = acct("loc-agy", "agy", "Antigravity", [win("5h", 300, 20, 2 * H, { model: "Gemini Models" })], { local: true, machines: ["Laptop"] });
    const extAgy = acct("ext-agy", "antigravity", "Antigravity · Gemini", [win("5h", 300, 20, 2 * H)], { sources: ["Relay"] });
    const extCodex = acct("ext-codex", "codex", "Codex #1", [win("5h", 300, 20, 2 * H)], { sources: ["Relay"] });
    const localFar = acct("loc-far", "claude", "Claude", [win("7d", 10080, 20, 48 * H)], { local: true });
    const extFar = acct("ext-far", "claude", "Claude #other", [win("7d", 10080, 20, 48 * H + 120_000)], { sources: ["Relay"] });

    const overlaps = detectLocalExternalOverlaps([localAgy, extAgy, extCodex, localFar, extFar], NOW);
    expect(overlaps.get("loc-agy")).toBe("Antigravity · Gemini");
    expect(overlaps.has("loc-far")).toBe(false);
  });

  it("matches a second machine's local row against a reconciled external row and folds stale local sibling duplicates", () => {
    const reconciled = acct("ext-merged", "codex", "Work Codex", [win("7d", 10080, 17, 48 * H)], {
      local: true,
      machines: ["host-a"],
      sources: ["Extension"],
    });
    const unmergedMachineB = acct("loc-b", "codex", "Codex Plus", [win("7d", 10080, 14, 48 * H)], {
      local: true,
      stale: true,
      machines: ["host-b"],
    });
    expect(detectLocalExternalOverlaps([reconciled, unmergedMachineB], NOW).get("loc-b")).toBe("Work Codex");

    const liveLocal = acct("codex-live", "codex", "Codex Plus", [win("5h", 300, 1, 2 * H), win("7d", 10080, 17, 48 * H)], {
      local: true,
      machines: ["host-a"],
    });
    const staleRollout = acct("codex-stale", "codex", "Codex Plus", [win("7d", 10080, 14, 48 * H)], {
      local: true,
      stale: true,
      machines: ["host-b"],
    });
    const freshOther = acct("codex-fresh-2", "codex", "Codex Plus 2", [win("7d", 10080, 10, 48 * H)], {
      local: true,
      machines: ["host-c"],
    });
    const siblingOverlaps = detectLocalExternalOverlaps([liveLocal, staleRollout, freshOther], NOW);
    expect(siblingOverlaps.get("codex-stale")).toBe("Codex Plus");
    expect(siblingOverlaps.has("codex-live")).toBe(false);
    expect(siblingOverlaps.has("codex-fresh-2")).toBe(false);

    // Distinct signed-in email accounts never fold into each other even when reset timestamps match and one is stale.
    const agyHostA = acct("agy-a", "antigravity", "alice@example.com", [win("7d", 10080, 18, 48 * H, { model: "Gemini Models" })], {
      local: true,
      machines: ["host-a"],
    });
    const agyHostB = acct("agy-b", "antigravity", "bob@example.com", [win("7d", 10080, 18, 48 * H, { model: "Gemini Models" })], {
      local: true,
      stale: true,
      machines: ["host-b"],
    });
    expect(detectLocalExternalOverlaps([agyHostA, agyHostB], NOW).size).toBe(0);
  });
});

describe("buildUsageTable and nextTimelineReset", () => {
  const sampleAccounts = (): UsageAccount[] => [
    acct("codex-1", "codex", "Codex #alpha", [win("5h", 300, 15, 1.5 * H), win("7d", 10080, 38, 52 * H)], { plan: "plus" }),
    acct(
      "claude-1",
      "claude",
      "Claude #work",
      [
        win("5h", 300, 32, 3 * H),
        win("7d", 10080, 58, 30 * H, { protect_percent: 90 }),
        win("7d", 10080, 82, 30 * H, { model: "Fable" }),
      ],
      { plan: "max" },
    ),
    acct(
      "agy-1",
      "antigravity",
      "Antigravity · Main",
      [
        win("5h", 300, 20, 2 * H, { model: "Claude and GPT models" }),
        win("5h", 300, 35, 2.5 * H, { model: "Gemini Models" }),
        win("7d", 10080, 45, 60 * H, { model: "Claude and GPT models" }),
        win("7d", 10080, 60, 72 * H, { model: "Gemini Models" }),
      ],
      { plan: "ultra" },
    ),
    acct("ds-1", "deepseek", "DeepSeek", [], { balance: { amount: "18.50", currency: "CNY" } }),
  ];

  it("aligns 5h and 7d columns across providers and stacks multi-model windows inside matching span cells", () => {
    const groups = groupByProvider(sampleAccounts());
    const table = buildUsageTable(groups, null);
    expect(table.columns.map((c) => [c.key, c.head])).toEqual([
      ["m:300", "5h"],
      ["m:10080", "7d"],
    ]);
    expect(table.hasOther).toBe(false);
    expect(table.rows.map((r) => [r.account.id, r.plan, r.bareBalance])).toEqual([
      ["codex-1", "Plus", false],
      ["claude-1", "Max", false],
      ["agy-1", "Ultra", false],
      ["ds-1", "", true],
    ]);

    const agyRow = table.rows.find((r) => r.account.id === "agy-1")!;
    expect(agyRow.cells["m:300"].map((w) => w.model)).toEqual(["Claude and GPT models", "Gemini Models"]);
    expect(agyRow.cells["m:10080"].map((w) => w.model)).toEqual(["Claude and GPT models", "Gemini Models"]);
    expect(shortPoolLabel("Claude and GPT models")).toBe("Claude & GPT");
    expect(shortPoolLabel("Gemini Models")).toBe("Gemini");

    const claudeRow = table.rows.find((r) => r.account.id === "claude-1")!;
    expect(claudeRow.cells["m:10080"]).toHaveLength(2);
    expect(tableWindowSubLabel(claudeRow.cells["m:10080"][0], "7d", 2, "Plan")).toEqual({ text: "Plan", full: "Plan" });
    expect(tableWindowSubLabel(claudeRow.cells["m:10080"][1], "7d", 2, "Plan")).toEqual({ text: "Fable", full: "Fable" });
    // Regression: extension title duplicating model or span ("Fable", "7 days · Fable") stays cleanly "Fable" in 7d column
    expect(tableWindowSubLabel(win("7d", 10080, 82, 30 * H, { model: "Fable", title: "Fable" }), "7d", 2, "Plan")).toEqual({
      text: "Fable",
      full: "Fable",
    });
    expect(tableWindowSubLabel(win("7d", 10080, 82, 30 * H, { model: "Fable", title: "7 days · Fable" }), "7d", 2, "Plan")).toEqual({
      text: "Fable",
      full: "Fable",
    });
  });

  it("sorts by headroom (tightest first ascending, healthiest first descending) with empty cells anchored at the bottom", () => {
    const groups = groupByProvider(sampleAccounts());
    // 7d tightest headroom: claude-1 has Fable 18% (R=0 -> h=18), agy-1 has Gemini 40% (h=40), codex-1 has 62% (h=62), ds-1 has none
    const asc = buildUsageTable(groups, { key: "m:10080", dir: "asc" });
    expect(asc.rows.map((r) => r.account.id)).toEqual(["claude-1", "agy-1", "codex-1", "ds-1"]);

    const desc = buildUsageTable(groups, { key: "m:10080", dir: "desc" });
    expect(desc.rows.map((r) => r.account.id)).toEqual(["codex-1", "agy-1", "claude-1", "ds-1"]);

    const byName = buildUsageTable(groups, { key: "account", dir: "asc" });
    expect(byName.rows.map((r) => r.account.id)).toEqual(["agy-1", "claude-1", "codex-1", "ds-1"]);
  });

  it("picks the earliest upcoming reset across live timeline rows, skipping stale/excluded duplicates unless all are stale", () => {
    const groups = groupByProvider(sampleAccounts());
    const rows = unifiedTimeline(groups, NOW);
    const next = nextTimelineReset(rows);
    expect(next).toEqual({
      accountId: "codex-1",
      accountName: "#alpha",
      provider: "codex",
      label: "5h",
      resetsAt: at(1.5 * H),
      remainingMs: 1.5 * H,
      paused: false,
    });

    // Stale overlapping local account resetting 10s earlier does not hijack summary ahead of live external account
    const staleLocal = acct("codex-stale", "codex", "Codex Promax", [win("5h", 300, 20, 1.4 * H)], { stale: true, local: true });
    const liveExt = acct("codex-live", "codex", "Magpie #1", [win("5h", 300, 10, 1.5 * H)], { sources: ["Magpie"] });
    const mixedRows = unifiedTimeline(groupByProvider([staleLocal, liveExt]), NOW);
    expect(nextTimelineReset(mixedRows, new Set(["codex-stale"]))?.accountId).toBe("codex-live");
    // Falls back to stale row when only stale rows exist
    const staleOnlyRows = unifiedTimeline(groupByProvider([staleLocal]), NOW);
    expect(nextTimelineReset(staleOnlyRows)?.accountId).toBe("codex-stale");
  });
});
