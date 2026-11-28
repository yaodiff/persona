// Model series & families in the model panel (docs/web/web.md "Model pill and model panel", "Families and earlier versions"):
// visually groups a provider's list by model series (Claude, GPT, Gemini, DeepSeek, GLM, MiniMax, Seed, Qwen, Kimi,
// Grok — with Orchestrator under Gemini and GPT-OSS under GPT), while keeping version comparison and "Earlier versions"
// folding per internal line (e.g. Claude Opus folds older Opus versions without hiding Claude Sonnet).
// Series headers activate when at least two series appear in the section or any versioned line has two or more rows;
// single-series flat lists stay ungrouped. Unrecognized models and custom IDs sit at the end under a localized "Other" header.
import type { PanelRow } from "./model-panel";

export type ModelSeries =
  | "claude"
  | "gpt"
  | "gemini"
  | "deepseek"
  | "glm"
  | "minimax"
  | "seed"
  | "qwen"
  | "kimi"
  | "grok";

export const SERIES_LABELS: Record<ModelSeries, string> = {
  claude: "Claude",
  gpt: "GPT",
  gemini: "Gemini",
  deepseek: "DeepSeek",
  glm: "GLM",
  minimax: "MiniMax",
  seed: "Seed",
  qwen: "Qwen",
  kimi: "Kimi",
  grok: "Grok",
};

export interface ModelFamily {
  /** Internal version-folding line key: "claude:opus", "gpt:sol", "gpt:oss", "gemini:flash", "gemini:orchestrator", ... */
  key: string;
  /** Visual series group key: "claude", "gpt", "gemini", "deepseek", "glm", "minimax", "seed", "qwen", "kimi", "grok". */
  series: ModelSeries;
  /** Visual series header label (brand name, never translated): "Claude", "GPT", "Gemini", etc. */
  label: string;
  vendor: ModelSeries;
  /** Claude's tiers by capability (Fable, Opus, Sonnet, Haiku); other lines keep list order within their series. */
  rank?: number;
  /** The version's numbers, major first: Opus 4.8 → [4, 8]; empty for unversioned lines. */
  version: number[];
  /** Unversioned line inside a series (e.g. Orchestrator under Gemini, GPT-OSS under GPT): never folds or shows Latest. */
  unversioned?: boolean;
}

export interface FamilyModelRef {
  id: string;
  resolved?: string;
  defaultEffort?: string;
  custom?: boolean;
  providerDefault?: boolean;
  variants?: { base: string };
}

const CLAUDE_TIERS = ["fable", "opus", "sonnet", "haiku"];
// Effort words that never name a model tier of their own.
const PURE_EFFORTS = new Set(["minimal", "low", "medium", "high", "xhigh"]);
// Release-channel and thinking words that name a variant of a line, not a separate line.
const QUALIFIERS = new Set(["thinking", "preview", "exp", "experimental", "latest"]);
// Effort words that can also be standalone tier names (e.g. gemini-1.0-ultra): strip only after another tier word.
const TRAILING_EFFORTS = new Set(["max", "ultra"]);
const EFFORT_WORDS = "minimal|low|medium|high|xhigh|max|ultra";
// Trailing colon effort/thinking qualifier used by gateway/omp IDs (e.g. google-antigravity/gemini-4-argon:high).
const COLON_QUALIFIER = new RegExp(`:(?:thinking(?:-(?:${EFFORT_WORDS}))?|(?:${EFFORT_WORDS})(?:-thinking)?)$`);
const DATE_STAMP = /(?:-\d{8}|-\d{4}-\d{2}-\d{2}|-\d{6})$/;
const CLAUDE_QUALIFIER = new RegExp(`(?:(?:-thinking)?-(?:${EFFORT_WORDS})|-thinking|-(?:${EFFORT_WORDS})-thinking)$`);
const CLAUDE_TIER_FIRST = /^claude-(fable|opus|sonnet|haiku)-(\d{1,2}(?:[.-]\d{1,2})?)$/;
const CLAUDE_VERSION_FIRST = /^claude-(\d{1,2}(?:[.-]\d{1,2})?)-(opus|sonnet|haiku)$/;
const PURE_EFFORT_SUFFIX = /-(?:minimal|low|medium|high|xhigh)$/;
const GPT_OSS = /^gpt-oss(?:-([a-z0-9]+(?:-[a-z0-9]+)*))?$/;
const GPT_4O = /^gpt-4o(?:-([a-z0-9]+(?:-[a-z0-9]+)*))?$/;
const GEMINI_ORCHESTRATOR = /^gemini-(?:pro-)?orchestrator(?:-([a-z][a-z0-9]*(?:-[a-z0-9]+)*))?$/;

const numbers = (v: string) => v.split(/[.-]/).filter(Boolean).map(Number);

function stripPureEffort(id: string, hadColon: boolean): string | undefined {
  const out = !hadColon && PURE_EFFORT_SUFFIX.test(id) ? id.replace(PURE_EFFORT_SUFFIX, "").replace(DATE_STAMP, "") : id;
  return PURE_EFFORT_SUFFIX.test(out) ? undefined : out;
}

function seriesLine(series: ModelSeries, sub: string, version: number[], opts?: { rank?: number; unversioned?: boolean }): ModelFamily {
  return {
    key: sub ? `${series}:${sub}` : `${series}:`,
    series,
    label: SERIES_LABELS[series],
    vendor: series,
    version,
    ...(opts?.rank !== undefined ? { rank: opts.rank } : {}),
    ...(opts?.unversioned ? { unversioned: true } : {}),
  };
}

/** The series and internal version line a model belongs to; undefined for unrecognized IDs. */
export function modelFamily(model: Pick<FamilyModelRef, "id" | "resolved" | "variants">): ModelFamily | undefined {
  const raw = (model.variants?.base?.trim() || model.resolved?.trim() || model.id.trim()).toLowerCase();
  // a gateway's vendor path, a context-size mark, a colon effort/thinking qualifier, a date stamp: none makes a series
  const withoutContext = (raw.split("/").pop() ?? raw).replace(/\[[^\]]*\]$/, "");
  const colonMatch = COLON_QUALIFIER.exec(withoutContext);
  const hadColon = Boolean(colonMatch);
  const colonHasThinking = Boolean(colonMatch?.[0].includes("thinking"));
  const withoutColon = hadColon ? withoutContext.replace(COLON_QUALIFIER, "") : withoutContext;
  const id = withoutColon.replace(/(?:-\d{8}|-\d{4}-\d{2}-\d{2}|@[\w.-]+)$/, "");

  if (id.startsWith("claude-")) {
    let claudeId = id;
    if (!hadColon && CLAUDE_QUALIFIER.test(claudeId)) {
      claudeId = claudeId.replace(CLAUDE_QUALIFIER, "").replace(DATE_STAMP, "");
    } else if (hadColon && !colonHasThinking && claudeId.endsWith("-thinking")) {
      claudeId = claudeId.slice(0, -"-thinking".length).replace(DATE_STAMP, "");
    }
    let m = CLAUDE_TIER_FIRST.exec(claudeId);
    if (m) return seriesLine("claude", m[1], numbers(m[2]), { rank: CLAUDE_TIERS.indexOf(m[1]) });
    m = CLAUDE_VERSION_FIRST.exec(claudeId);
    if (m) return seriesLine("claude", m[2], numbers(m[1]), { rank: CLAUDE_TIERS.indexOf(m[2]) });
    return undefined;
  }

  const base = stripPureEffort(id, hadColon);
  if (!base) return undefined;

  if (base.startsWith("gpt-")) {
    let m = GPT_OSS.exec(base);
    if (m) return seriesLine("gpt", "oss", [], { unversioned: true });
    m = GPT_4O.exec(base);
    if (m) return seriesLine("gpt", "4o", [], { unversioned: true });
    m = /^gpt-(\d+(?:\.\d+)*)(?:-([a-z][a-z0-9]*(?:-[a-z][a-z0-9]*)*))?$/.exec(base);
    if (!m) return undefined;
    const words = m[2] ? m[2].split("-") : [];
    if (words.length && PURE_EFFORTS.has(words[words.length - 1])) return undefined;
    return seriesLine("gpt", words.join("-"), numbers(m[1]));
  }

  if (base.startsWith("gemini-")) {
    const om = GEMINI_ORCHESTRATOR.exec(id);
    if (om) {
      const words = om[1] ? om[1].split("-") : [];
      if (hadColon && words.some((w) => PURE_EFFORTS.has(w))) return undefined;
      if (!hadColon && words.length > 1 && PURE_EFFORTS.has(words[words.length - 1]) && PURE_EFFORTS.has(words[words.length - 2])) return undefined;
      return seriesLine("gemini", "orchestrator", [], { unversioned: true });
    }
    const m = /^gemini-(\d+(?:\.\d+)*)-([a-z][a-z0-9]*(?:-[a-z0-9]+)*)$/.exec(id);
    if (!m) return undefined;
    const tokens = m[2].split("-").filter((w) => !QUALIFIERS.has(w) && !/^\d+$/.test(w));
    if (hadColon) {
      if (tokens.some((w) => PURE_EFFORTS.has(w))) return undefined;
      if (tokens.length > 1 && TRAILING_EFFORTS.has(tokens[tokens.length - 1])) return undefined;
    }
    const words = tokens.filter((w) => !PURE_EFFORTS.has(w));
    if (words.length > 1 && TRAILING_EFFORTS.has(words[words.length - 1])) words.pop();
    if (!words.length) return undefined;
    return seriesLine("gemini", words.join("-"), numbers(m[1]));
  }

  if (base.startsWith("deepseek-")) {
    const clean = base.replace(DATE_STAMP, "");
    const m = /^deepseek-(v|r)(\d+(?:\.\d+)*)(?:-([a-z0-9][a-z0-9-]*))?$/.exec(clean);
    if (m) {
      const words = (m[3] ? m[3].split("-") : []).filter((w) => !QUALIFIERS.has(w) && !/^\d{4}$/.test(w));
      return seriesLine("deepseek", [m[1], ...words].join("-"), numbers(m[2]));
    }
    if (/^deepseek-[a-z0-9][a-z0-9.-]*$/.test(clean)) return seriesLine("deepseek", "other", [], { unversioned: true });
    return undefined;
  }

  if (base.startsWith("glm-")) {
    const clean = base.replace(DATE_STAMP, "");
    const m = /^glm-(\d+(?:\.\d+)*)(?:-([a-z0-9][a-z0-9.-]*))?$/.exec(clean);
    if (m) {
      const words = (m[2] ? m[2].split("-") : []).filter((w) => !QUALIFIERS.has(w) && !/^\d{4}$/.test(w));
      return seriesLine("glm", words.join("-"), numbers(m[1]));
    }
    if (/^glm-[a-z0-9][a-z0-9.-]*$/.test(clean)) return seriesLine("glm", "other", [], { unversioned: true });
    return undefined;
  }

  if (base.startsWith("minimax-")) {
    const clean = base.replace(DATE_STAMP, "");
    const m = /^minimax-m(\d+(?:\.\d+)*)(?:-([a-z0-9][a-z0-9.-]*))?$/.exec(clean);
    if (m) {
      const words = (m[2] ? m[2].split("-") : []).filter((w) => !QUALIFIERS.has(w) && !/^\d{4}$/.test(w));
      return seriesLine("minimax", ["m", ...words].join("-"), numbers(m[1]));
    }
    if (/^minimax-[a-z0-9][a-z0-9.-]*$/.test(clean)) return seriesLine("minimax", "other", [], { unversioned: true });
    return undefined;
  }

  if (base.startsWith("seed-") || base.startsWith("doubao-seed-")) {
    const clean = base.replace(DATE_STAMP, "");
    const m = /^(?:doubao-)?seed-(\d{1,2}(?:[.-]\d{1,2})*)(?:-([a-z][a-z0-9-]*))?$/.exec(clean);
    if (m) {
      const words = (m[2] ? m[2].split("-") : []).filter((w) => !QUALIFIERS.has(w) && !/^\d+$/.test(w));
      return seriesLine("seed", words.join("-"), numbers(m[1]));
    }
    if (/^(?:doubao-)?seed-[a-z0-9][a-z0-9.-]*$/.test(clean)) return seriesLine("seed", "other", [], { unversioned: true });
    return undefined;
  }

  if (base.startsWith("qwen")) {
    const clean = base.replace(DATE_STAMP, "");
    const m = /^qwen-?(\d{1,2}(?:\.\d+)*)(?:-([a-z0-9][a-z0-9.-]*))?$/.exec(clean);
    if (m) {
      const words = (m[2] ? m[2].split("-") : []).filter((w) => !QUALIFIERS.has(w));
      return seriesLine("qwen", words.join("-"), numbers(m[1]));
    }
    if (/^qwen-[a-z][a-z0-9.-]*$/.test(clean)) return seriesLine("qwen", "other", [], { unversioned: true });
    return undefined;
  }

  if (base.startsWith("kimi-")) {
    const clean = base.replace(DATE_STAMP, "");
    const m = /^kimi-k(\d+(?:\.\d+)*)(?:-([a-z0-9][a-z0-9-]*))?$/.exec(clean);
    if (m) {
      const words = (m[2] ? m[2].split("-") : []).filter((w) => !QUALIFIERS.has(w));
      return seriesLine("kimi", ["k", ...words].join("-"), numbers(m[1]));
    }
    if (/^kimi-[a-z0-9][a-z0-9.-]*$/.test(clean)) return seriesLine("kimi", "other", [], { unversioned: true });
    return undefined;
  }

  if (base.startsWith("grok-")) {
    const clean = base.replace(DATE_STAMP, "");
    const m = /^grok-(\d+(?:\.\d+)*)(?:-([a-z0-9][a-z0-9.-]*))?$/.exec(clean);
    if (m) {
      const words = (m[2] ? m[2].split("-") : []).filter((w) => !QUALIFIERS.has(w));
      return seriesLine("grok", words.join("-"), numbers(m[1]));
    }
    if (/^grok-[a-z0-9][a-z0-9.-]*$/.test(clean)) return seriesLine("grok", "other", [], { unversioned: true });
    return undefined;
  }

  return undefined;
}

/** Newer first: [5, 5] before [5] before [4, 8]; a missing minor counts as 0. */
export function compareVersions(a: number[], b: number[]): number {
  for (let i = 0; i < Math.max(a.length, b.length); i++) {
    const d = (b[i] ?? 0) - (a[i] ?? 0);
    if (d) return d;
  }
  return 0;
}

/** One stretch of a section's list: a series group (header, internal line folds), trailing unrecognized/custom rows (`other`), or headerless leading rows. */
export interface PanelBlock {
  /** Unique in the section; a series block's key is what `open` holds (with the section's key). */
  key: string;
  family?: Pick<ModelFamily, "key" | "label">;
  /** Trailing unrecognized or custom rows after recognized series: drawn under a localized "Other" header. */
  other?: boolean;
  /** The rows drawn, ordered by intra-series line rank/appearance and newest first per line. */
  rows: PanelRow[];
  /** How many older rows the series fold holds (folded) or would hold (unfolded); 0: no fold. */
  older: number;
  /** The series is unfolded. */
  open?: boolean;
}

export interface FamilyOptions {
  /** Not searching: older versions fold. A search shows every row that answers it, series kept together. */
  fold: boolean;
  /** Rows that never fold: the one that runs now, a favorite's or a recent setup's model, one customized. */
  keep: (row: PanelRow) => boolean;
  /** Series unfolded, by block key. */
  open: ReadonlySet<string>;
  /** A prefix for the block keys (the section's key). */
  scope: string;
  /** A provider's whole list, when `rows` may be a part of it (a search): which version is a line's newest is read
   *  from it, so a search for "opus 4" does not call Opus 4.8 the latest. */
  listed?: (provider: string) => readonly FamilyModelRef[] | null | undefined;
}

/** A provider's own "no model given" entry (Claude Code's `default`, New Agent's Default): first, outside every series. */
const listDefault = (row: PanelRow) => row.model.providerDefault || row.model.id === "default";

/** Fold multi-level effort variants in a provider's full catalog so `listed` and single-level search matches classify
 *  under the exact same variant base as folded rows (e.g. `gpt-5.5-codex-low` + `gpt-5.5-codex-max` -> `gpt-5.5-codex`). */
function catalogVariantInfo(models: readonly FamilyModelRef[] | null | undefined): {
  listed: Pick<FamilyModelRef, "id" | "resolved" | "variants">[];
  baseOfId: Map<string, string>;
} {
  const baseOfId = new Map<string, string>();
  if (!models?.length) return { listed: [], baseOfId };
  const groups = new Map<string, FamilyModelRef[]>();
  const ordered: (FamilyModelRef | { base: string; levels: FamilyModelRef[] })[] = [];
  for (const m of models) {
    const effort = !m.custom && !m.providerDefault && !m.variants ? m.defaultEffort : undefined;
    const base = effort && m.id.endsWith(`-${effort}`) ? m.id.slice(0, -effort.length - 1) || undefined : undefined;
    if (!base) {
      ordered.push(m);
      continue;
    }
    const existing = groups.get(base);
    if (existing) {
      existing.push(m);
    } else {
      const levels = [m];
      groups.set(base, levels);
      ordered.push({ base, levels });
    }
  }
  const listed: Pick<FamilyModelRef, "id" | "resolved" | "variants">[] = [];
  for (const entry of ordered) {
    if ("levels" in entry) {
      if (entry.levels.length > 1) {
        for (const m of entry.levels) baseOfId.set(m.id, entry.base);
        listed.push({ id: entry.levels[0].id, resolved: entry.levels[0].resolved, variants: { base: entry.base } });
      } else {
        listed.push(entry.levels[0]);
      }
    } else {
      listed.push(entry);
    }
  }
  return { listed, baseOfId };
}

/**
 * A section's rows grouped by model series (`Claude`, `Gemini`, `GPT`, `DeepSeek`, `GLM`, `MiniMax`, `Seed`, `Qwen`,
 * `Kimi`, `Grok`), or undefined when fewer than two series appear AND no versioned line has two rows (single-series flat
 * lists stay as given). Inside each series, rows stay ordered by intra-series line (Claude tiers by capability; other
 * lines by first mention), with older versions folded per line and combined into one "Earlier versions · N" toggle per
 * series header. Trailing unrecognized/custom rows sit under a localized "Other" header (`other: true`).
 */
export function familyBlocks(rows: PanelRow[], o: FamilyOptions): PanelBlock[] | undefined {
  const lead: PanelRow[] = [];
  const rest: PanelRow[] = [];
  const typed: PanelRow[] = [];
  interface LineGroup {
    family: ModelFamily;
    first: number;
    rows: { row: PanelRow; family: ModelFamily; at: number }[];
  }
  interface SeriesGroup {
    providerId: string;
    series: ModelSeries;
    label: string;
    first: number;
    lines: Map<string, LineGroup>;
  }
  const seriesMap = new Map<string, SeriesGroup>();
  const catalogCache = new Map<string, ReturnType<typeof catalogVariantInfo>>();
  const catalogFor = (providerId: string) => {
    let info = catalogCache.get(providerId);
    if (!info) {
      info = catalogVariantInfo(o.listed?.(providerId));
      catalogCache.set(providerId, info);
    }
    return info;
  };
  for (const [at, row] of rows.entries()) {
    if (row.kind === "typed" || row.model.custom) {
      typed.push(row);
      continue;
    }
    if (listDefault(row)) {
      lead.push(row);
      continue;
    }
    const catalogBase = row.kind === "model" && !row.model.variants ? catalogFor(row.provider.id).baseOfId.get(row.model.id) : undefined;
    const family = row.kind === "model" ? modelFamily(catalogBase ? { ...row.model, variants: { base: catalogBase } } : row.model) : undefined;
    if (!family) {
      rest.push(row);
      continue;
    }
    const seriesKey = `${row.provider.id}\n${family.series}`;
    let seriesGroup = seriesMap.get(seriesKey);
    if (!seriesGroup) {
      seriesGroup = { providerId: row.provider.id, series: family.series, label: family.label, first: at, lines: new Map() };
      seriesMap.set(seriesKey, seriesGroup);
    }
    const lineGroup = seriesGroup.lines.get(family.key) ?? { family, first: at, rows: [] };
    lineGroup.rows.push({ row, family, at });
    seriesGroup.lines.set(family.key, lineGroup);
  }

  const hasMultipleSeries = seriesMap.size >= 2;
  const hasFoldableLine = [...seriesMap.values()].some((s) =>
    [...s.lines.values()].some((line) => !line.family.unversioned && line.rows.length > 1),
  );
  if (!hasMultipleSeries && !hasFoldableLine) return undefined;

  const orderedSeries = [...seriesMap.values()].sort((a, b) => a.first - b.first);
  const blocks: PanelBlock[] = [];
  if (lead.length) blocks.push({ key: `${o.scope}\nlead`, rows: lead, older: 0 });

  for (const s of orderedSeries) {
    const blockKey = `${o.scope}\n${s.providerId}\n${s.series}`;
    const open = o.open.has(blockKey);
    const orderedLines = [...s.lines.values()].sort((a, b) => {
      if (a.family.rank !== undefined && b.family.rank !== undefined && a.family.rank !== b.family.rank) {
        return a.family.rank - b.family.rank;
      }
      return a.first - b.first;
    });

    const blockRows: PanelRow[] = [];
    let totalOlder = 0;

    for (const line of orderedLines) {
      if (line.family.unversioned) {
        const lineSorted = [...line.rows].sort((a, b) => a.at - b.at);
        for (const x of lineSorted) blockRows.push(x.row);
        continue;
      }
      const sorted = [...line.rows].sort((a, b) => compareVersions(a.family.version, b.family.version) || a.at - b.at);
      // the line's versions in the provider's whole list (the rows given, when it isn't known)
      const versions = catalogFor(s.providerId)
        .listed.map((m) => modelFamily(m))
        .filter((x) => x?.key === line.family.key)
        .map((x) => x!.version);
      for (const x of sorted) versions.push(x.family.version);
      versions.sort(compareVersions);
      const newest = versions[0];
      const anyOlder = versions.some((v) => compareVersions(v, newest) !== 0);
      const isNewest = (x: (typeof sorted)[number]) => compareVersions(x.family.version, newest) === 0;
      totalOlder += sorted.filter((x) => !isNewest(x) && !o.keep(x.row)).length;
      const shown = !o.fold || open ? sorted : sorted.filter((x) => isNewest(x) || o.keep(x.row));
      for (const x of shown) {
        blockRows.push(isNewest(x) && anyOlder ? { ...x.row, latest: true } : x.row);
      }
    }

    blocks.push({
      key: blockKey,
      family: { key: s.series, label: s.label },
      rows: blockRows,
      older: o.fold ? totalOlder : 0,
      ...(o.fold && open ? { open: true } : {}),
    });
  }

  if (rest.length || typed.length) blocks.push({ key: `${o.scope}\nrest`, other: true, rows: [...rest, ...typed], older: 0 });
  return blocks;
}
