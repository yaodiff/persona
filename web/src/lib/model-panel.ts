// The unified model panel's data (#5032, after Cindy's model selector; docs/web/web.md "Model pill and model panel"):
// the providers a chat can run on and their models, the setup that runs now, and what the panel keeps in this browser
// (favorites, recent setups, per-model customizations). The panel (ui/model-panel/ModelPanel.tsx) draws them; each chat
// kind builds them from its own sources (an Agent: its computer's launchers; a Persona chat: the assistant's list).
import { t } from "../i18n";
import { effortVariantBase, matches } from "./launcher-models";
import { cleanModelId, validModelId } from "./model-id";
import { isCurrentModel } from "./model-switch";
import { familyBlocks, type PanelBlock } from "./model-families";
import type { SpeedOption } from "./session-speed";
import { defaultVariant, groupVariants, variantName } from "./model-variant-groups";

export function speedLabel({ id, name }: SpeedOption): string {
  if (id === "standard") return t("ui-modelpanel.speed_standard");
  if (id === "fast") return t("ui-modelpanel.fast_short");
  if (id === "ultrafast") return t("ui-modelpanel.speed_ultrafast");
  return name || id;
}

/** The catalog's reference price in USD per one million tokens; absent means unknown, not free. */
export interface PanelPrice {
  input: number;
  output: number;
  cache_read?: number;
  /** A 5-minute cache write; `cache_write_1h` a 1-hour one (absent: `cache_write`'s price). */
  cache_write?: number;
  cache_write_1h?: number;
}

/** One model of a provider, as the panel shows and picks it. */
export interface PanelModel {
  /** What a pick sends (Claude Code's aliases too: opus, sonnet…). */
  id: string;
  /** Display name (lib/launcher-models.ts modelName). */
  name: string;
  description?: string;
  /** The concrete model an alias stands for under this provider. */
  resolved?: string;
  /** The other spellings the hub folded into this row (lib/launcher-models.ts AgentModel.aliases): a saved setup naming
   *  one is this row's, and a search finds it by them. */
  aliases?: string[];
  /** Thinking efforts it takes, weakest first (lib/session-actions.ts EFFORTS / CODEX_EFFORTS ids); empty: no effort control. */
  efforts: string[];
  /** The running session cannot change effort here; keep its actual value visible and explain why. */
  effortUnavailable?: string;
  effortNote?: string;
  /** The effort the model runs with when none is set: the slider marks it "Recommended". */
  defaultEffort?: string;
  /** Ordered live speed choices, when offered for this model. */
  speeds?: readonly SpeedOption[];
  /** Context window in tokens, when known. */
  context?: number;
  /** Exact catalog reference price, when known. */
  price?: PanelPrice;
  /** A model ID typed before for this provider (#1644): listed after the provider's own models, can be forgotten. */
  custom?: boolean;
  /** The thinking levels folded into this row (#12315, foldVariants): Antigravity lists one model per level
   *  (`<base>-<level>`, named "<Name> (<Level>)"). The row stands for one of them (`id`, its level `defaultEffort`) and
   *  its effort control picks another level's ID; `efforts` are the levels. */
  variants?: EffortVariants;
  /** It stands for whatever its provider runs when no model is given (New Agent's "Default" row), not a model of its
   *  own: no other provider's row is the same model. */
  providerDefault?: boolean;
}

/** A model's levels listed as models of their own (PanelModel.variants). */
export interface EffortVariants {
  /** The ID without its level: what the row's key and its override go by, whichever level it stands for. */
  base: string;
  /** Each level and the model ID that runs it, weakest first. */
  levels: { effort: string; id: string }[];
  /** The level a pick takes when none was chosen: Medium where listed, else the one the list names first. */
  first: string;
}

/** One provider: a launcher (rc-file alias, PATH wrapper, built-in claude, Persona-managed) or a hosted provider. */
export interface PanelProvider {
  /** Launcher name: what a pick sends as the provider. */
  id: string;
  /** What the rail tooltip, group header and second lines call it. */
  name: string;
  /** The engine it runs (AgentMark ids): claude, codex, agy…; one of Paseo's providers is that provider (grok…, #8142). */
  engine: string;
  /** What the engine is called when the app has no name of its own for it: the session's own provider's label ("Grok",
   *  the row's agent_label); its mark and its name on the rows and in the flyout. */
  engineLabel?: string;
  /** Known launcher default for its collapsed summary; never inferred from list order. */
  defaultModel?: string;
  isDefault?: boolean;
  /** Existing launcher check result; descriptive only, never changes selection policy. */
  status?: string;
  /** null: not read yet. */
  models: PanelModel[] | null;
  loading?: boolean;
  /** Reading the list failed: the group says why and offers Retry (onReload). */
  error?: string;
  /** The launcher fixes its own --model: its rows can't be picked, and this says why. */
  locked?: string;
  /** None of its models can be picked here (another engine, out of the computer's scope…): the reason, on each row. */
  unavailable?: string;
  /** It takes a model ID typed by hand: a search that names no listed model offers "Use “…”" in this group. */
  typable?: boolean;
  /** The startup ID under a display name (#5029, lib/provider-names.ts providerIdNote): the group's header shows it small
   *  in monospace, the rail's tooltip after the name. */
  idNote?: string;
  /** Another harness that can use it too, said after the group's name ("pi can use it too"). */
  alsoNote?: string;
}

/** A setup: what a pick applies, what a favorite or a recent entry keeps. */
export interface PanelConfig {
  provider: string;
  model: string;
  effort?: string;
  speed?: string;
}

/** What the chat runs and may switch to right now. */
export interface PanelState {
  /** What runs now; null while unknown. */
  current: PanelConfig | null;
  /** The Model settings effort is display only for all picks or for AGY picks. */
  currentEffortDisplayOnly?: "all" | "agy";
  /** A switch accepted for the next message (#5035, #5033). */
  pending?: PanelConfig | null;
  /** Providers whose models switch without a restart: the Recommended group is the current model, then their other models. */
  recommended: string[];
  /** Why no pick can be applied right now (the Agent is working…): rows are off and this is the note on top. */
  busy?: string;
  /** A warning line on top of the list (a pending switch's consequence). */
  note?: string;
  /** The choice is for an Agent that hasn't started yet (New Agent): no Recommended group, as Cindy's new task has none;
   *  the model picked stays in its provider's group. */
  starting?: boolean;
}

/** Where favorites, recent setups and customizations are kept apart: an Agent's providers are not a Persona chat's. */
export type PanelScope = "agent" | "assistant";

// ── what the panel keeps in this browser ─────────────────────────────────────

/** A favorite: a copy of a whole setup (Cindy's favorites are setups, not models), told apart by `uid`. */
export interface PanelFavorite extends PanelConfig {
  uid: string;
}

/** A model's own setup, changed in its flyout: what a pick of its row sends instead of the recommended one. */
export interface PanelOverride {
  effort?: string;
  speed?: string;
}

/** Overrides by `overrideKey(provider, model)`. */
export type PanelOverrides = Record<string, PanelOverride>;

/** Recent keeps this many setups, most recent first. */
export const RECENT_MAX = 5;

export const overrideKey = (provider: string, model: string) => `${provider}\n${model}`;

/** A setup without empty effort or speed values. */
export function cleanSetup(c: PanelConfig): PanelConfig {
  return { provider: c.provider, model: c.model, ...(c.effort ? { effort: c.effort } : {}), ...(c.speed ? { speed: c.speed } : {}) };
}

/** The same setup: provider, model, effort and speed all alike. Legacy saved setups without speed are Standard. */
export function sameSetup(a: PanelConfig | null | undefined, b: PanelConfig | null | undefined): boolean {
  if (!a || !b) return false;
  return a.provider === b.provider && a.model === b.model && (a.effort || "") === (b.effort || "") && (a.speed || "standard") === (b.speed || "standard");
}

/** Favorites with `config` saved as a copy at the end; an identical setup already saved is not saved twice. */
export function addFavorite(list: PanelFavorite[], config: PanelConfig, uid: string): PanelFavorite[] {
  if (list.some((f) => sameSetup(f, config))) return list;
  return [...list, { uid, ...cleanSetup(config) }];
}

export function removeFavorite(list: PanelFavorite[], uid: string): PanelFavorite[] {
  return list.filter((f) => f.uid !== uid);
}

/** A favorite's flyout edit. An edit that makes it the same as another favorite drops the edited one (no twins). */
export function updateFavorite(list: PanelFavorite[], uid: string, patch: PanelOverride): PanelFavorite[] {
  const at = list.find((f) => f.uid === uid);
  if (!at) return list;
  const next: PanelFavorite = { uid, ...cleanSetup({ ...at, ...patch }) };
  if (list.some((f) => f.uid !== uid && sameSetup(f, next))) return removeFavorite(list, uid);
  return list.map((f) => (f.uid === uid ? next : f));
}

/** Recent after `config` was applied: it moves to the front (an identical setup is not listed twice), RECENT_MAX at most. */
export function pushRecent(list: PanelConfig[], config: PanelConfig): PanelConfig[] {
  return [cleanSetup(config), ...list.filter((c) => !sameSetup(c, config))].slice(0, RECENT_MAX);
}

/** The overrides after a flyout edit of a model's own setup (a part set to undefined is dropped; nothing left: no override). */
export function setOverride(all: PanelOverrides, provider: string, model: string, patch: PanelOverride): PanelOverrides {
  const k = overrideKey(provider, model);
  const merged: PanelOverride = { ...all[k], ...patch };
  const next: PanelOverride = {};
  if (merged.effort) next.effort = merged.effort;
  if (merged.speed) next.speed = merged.speed;
  const out = { ...all };
  if (Object.keys(next).length) out[k] = next;
  else delete out[k];
  return out;
}

/** "Reset to recommended": the model's override goes. */
export function resetOverride(all: PanelOverrides, provider: string, model: string): PanelOverrides {
  const k = overrideKey(provider, model);
  if (!(k in all)) return all;
  const out = { ...all };
  delete out[k];
  return out;
}

// ── rows ──────────────────────────────────────────────────────────────────────

/** Whether `model` of `provider` runs now: the row named exactly what runs when the list has one (so a typed ID and the
 *  default entry that stands for the same model are never both lit, as the pill names that one), else a row whose
 *  concrete model it is (lib/model-switch.ts isCurrentModel: an alias standing for it, or one folded into it). */
function runsNow(provider: Pick<PanelProvider, "id" | "models">, model: Pick<PanelModel, "id" | "resolved" | "aliases">, current: PanelConfig | null | undefined): boolean {
  if (!current || current.provider !== provider.id) return false;
  const want = current.model.toLowerCase();
  if (provider.models?.some((m) => m.id.toLowerCase() === want)) return model.id.toLowerCase() === want;
  return isCurrentModel(model, current.model);
}

const takes = (model: Pick<PanelModel, "efforts">, effort: string | undefined) => (effort && model.efforts.includes(effort) ? effort : undefined);

/**
 * The setup a model's row stands for, and what picking it sends. The model that runs now shows what runs, never a
 * browser override for an unknown session value. An Agent not started yet keeps only supported form values. Any
 * other: the effort last chosen for it here (its override), else none: the model's own default (#12315; the effort
 * running now no longer carries over to another model, so a model always opens at what it was left at). New agy starts
 * use their native effort unless the user picks one. Speed comes from the override when offered.
 */
export function rowSetup(provider: Pick<PanelProvider, "id" | "models"> & { engine?: string }, model: PanelModel, state: Pick<PanelState, "current" | "currentEffortDisplayOnly" | "starting">, override?: PanelOverride): PanelConfig {
  const current = state.current;
  const now = runsNow(provider, model, current);
  const displayOnly = state.currentEffortDisplayOnly === "all" || (state.currentEffortDisplayOnly === "agy" && provider.engine === "agy");
  const currentEffort = displayOnly ? undefined : current?.effort;
  const effort = now ? (state.starting ? takes(model, currentEffort) : currentEffort) : takes(model, override?.effort);
  const speed = now && !state.starting ? current?.speed : takesSpeed(model, now ? current?.speed : override?.speed);
  return cleanSetup({ provider: provider.id, model: model.id, effort, speed });
}

const takesSpeed = (model: PanelModel, speed: string | undefined) => model.speeds?.some((choice) => choice.id === speed) ? speed : undefined;

/** A saved copy as it applies to the model now, without choices the model no longer offers. */
export function copySetup(copy: PanelConfig, model: PanelModel, listed: boolean): PanelConfig {
  if (!listed) return cleanSetup(copy);
  return cleanSetup({ provider: copy.provider, model: model.id, effort: takes(model, copy.effort), speed: takesSpeed(model, copy.speed ?? "standard") });
}

/** The effort a setup runs at: its own, else the model's default (the triple and the slider show this). */
export const shownEffort = (setup: PanelConfig, model: Pick<PanelModel, "defaultEffort">) => setup.effort || model.defaultEffort || "";

/** Why a row of `provider` can't be picked now (undefined: it can): the chat is busy, the provider can't take a pick
 *  here, the launcher fixes its own model, or its list is still being read (no reason to say; a list shown from before
 *  and read again behind it does not wait). The model that runs now (`current`) can always be picked back while a switch
 *  waits, unless the chat is busy: that keeps it and drops the switch (#5035, #5054; an agent that can't change its model
 *  while it runs has no other way back). */
export function rowBlock(provider: PanelProvider, state: Pick<PanelState, "busy" | "pending">, current = false): { reason: string } | undefined {
  if (current && state.pending && !state.busy) return undefined;
  const reason = state.busy || provider.unavailable || provider.locked;
  if (reason) return { reason };
  if (provider.models === null) return { reason: "" };
  return undefined;
}

/** Whether a row's setup flyout opens. Recent rows and "Use “…”" have none. The model that runs now has one whenever the
 *  chat applies flyout edits at once (`live`), even while picks are off (busy, or a Codex session that changes only its
 *  effort and Fast); any other row only while it can be picked. */
export function rowCustomizable(row: Pick<PanelRow, "kind" | "current" | "provider">, state: Pick<PanelState, "busy" | "pending">, live: boolean): boolean {
  if (row.kind === "recent" || row.kind === "typed" || row.kind === "preset") return false;
  if (row.kind === "model" && row.current && live) return true;
  return !rowBlock(row.provider, state);
}

/** The search (every word, any case): the model's name, id, the model it stands for, the spellings folded into it, its
 *  description, the provider's name. */
export function matchesQuery(provider: Pick<PanelProvider, "name">, model: Pick<PanelModel, "id" | "name" | "resolved" | "aliases" | "description">, query: string): boolean {
  return matches([model.name, model.id, model.resolved, ...(model.aliases ?? []), model.description, provider.name], query);
}

// ── thinking levels listed as models ─────────────────────────────────────────

/** The ID a model would have without its level, when it is one level of a model listed per level (Antigravity's
 *  `gemini-4-neon-high`: its default effort is the ID's last word); undefined for any other model. */
function variantBase(m: PanelModel): string | undefined {
  if (m.custom || m.providerDefault || m.variants) return undefined;
  return effortVariantBase(m.id, m.defaultEffort);
}

/** A list's models in groups, in list order: the levels of one model together (where its first level is listed), every
 *  other model alone. Only levels the node told apart (`default_effort` naming the ID's suffix, server agyModelList)
 *  group; a model with one level listed stays as it is. */
export function variantGroups(models: PanelModel[]): PanelModel[][] {
  return groupVariants(models, variantBase);
}

/** How many models a list has, each model's levels counted once (a launcher's "N models"). */
export const modelCount = (models: PanelModel[]) => variantGroups(models.filter((m) => !m.providerDefault)).length;

const levelRank = (effort: string) => {
  const i = EFFORT_TONES.indexOf(effort);
  return i < 0 ? EFFORT_TONES.length : i;
};

const defaultLevel = (levels: PanelModel[]) => defaultVariant(levels, (m) => m.defaultEffort);

/** One row's model for the levels of one model (two or more, from variantGroups), standing for `chosen`: the model's
 *  name without the level ("Gemini 4 Neon (High)" → "Gemini 4 Neon"; the base ID when the names disagree), the levels
 *  as its efforts, and `chosen`'s level as its own. */
export function foldVariants(levels: PanelModel[], chosen: PanelModel): PanelModel {
  const base = variantBase(levels[0]) ?? chosen.id;
  const name = variantName(levels.map((m) => m.name), base);
  const sorted = levels.map((m) => ({ effort: m.defaultEffort ?? "", id: m.id })).sort((a, b) => levelRank(a.effort) - levelRank(b.effort));
  return { ...chosen, name, efforts: sorted.map((l) => l.effort), variants: { base, levels: sorted, first: defaultLevel(levels).defaultEffort ?? "" } };
}

/** The level a model's folded row stands for: the one that runs now, else the one a switch waits for, else the one
 *  last chosen for it (`override.effort`), else its default level (defaultLevel). */
function chosenLevel(provider: PanelProvider, levels: PanelModel[], state: Pick<PanelState, "current" | "pending">, override: PanelOverride | undefined): PanelModel {
  const now = levels.find((m) => runsNow(provider, m, state.current));
  const pending = state.pending;
  const waiting = pending && pending.provider === provider.id ? levels.find((m) => isCurrentModel(m, pending.model)) : undefined;
  const picked = override?.effort ? levels.find((m) => m.defaultEffort === override.effort) : undefined;
  return now ?? waiting ?? picked ?? defaultLevel(levels);
}

/** A provider's model rows (the search applied): one row per model, a model's levels folded into one (#12315). A
 *  folded row's pick sends its level's own ID and no effort beside it (the ID is the level, as an Agent's pick of one
 *  already does, lib/session-panel.ts pickPlan); the model that runs now keeps what runs. */
function modelRows(p: PanelProvider, models: PanelModel[], state: PanelState, overrides: PanelOverrides): PanelRow[] {
  return variantGroups(models).map((levels) => {
    if (levels.length === 1) {
      const m = levels[0];
      const override = overrides[overrideKey(p.id, m.id)];
      return { key: `model:${p.id}\n${m.id}`, kind: "model" as const, provider: p, model: m, setup: rowSetup(p, m, state, override), current: runsNow(p, m, state.current), customized: Boolean(override) };
    }
    const base = variantBase(levels[0])!;
    const override = overrides[overrideKey(p.id, base)];
    const chosen = chosenLevel(p, levels, state, override);
    const model = foldVariants(levels, chosen);
    const setup = rowSetup(p, { ...chosen, efforts: [] }, state, override);
    return { key: `model:${p.id}\n${base}`, kind: "model" as const, provider: p, model, setup, current: runsNow(p, chosen, state.current), customized: Boolean(override) };
  });
}

/** What a folded row's effort control applies for `effort`: that level's model, nothing else (Fast as the row has it). */
export function levelSetup(row: Pick<PanelRow, "provider" | "model" | "setup">, effort: string): PanelConfig | undefined {
  const level = row.model.variants?.levels.find((l) => l.effort === effort);
  if (!level) return undefined;
  return cleanSetup({ provider: row.provider.id, model: level.id, speed: row.setup.speed });
}

/** What the list shows: one rail cell's view. A search shows "all" whatever cell is on. */
export type PanelView = { kind: "recent" } | { kind: "favorites" } | { kind: "all" } | { kind: "provider"; provider: string };

export const sameView = (a: PanelView, b: PanelView) => a.kind === b.kind && (a.kind !== "provider" || (b.kind === "provider" && a.provider === b.provider));

/** A ready-made setup offered as one row of its own (New Agent's Agent presets, #2225): a name and what it is for. */
export interface PanelPreset {
  /** Unique among the presets; what the panel's `onPreset` gets. */
  key: string;
  name: string;
  description?: string;
  setup: PanelConfig;
}

export interface PanelRow {
  /** Unique in the list. */
  key: string;
  /** A model of a provider's list (also in Recommended), a favorite's copy, a recent setup, "Use “…”" for a typed ID, or
   *  a preset. */
  kind: "model" | "favorite" | "recent" | "typed" | "preset";
  provider: PanelProvider;
  model: PanelModel;
  /** What picking it sends. */
  setup: PanelConfig;
  /** A favorite row's favorite. */
  favorite?: PanelFavorite;
  /** The favorite with the same setup (a recent row's star). */
  starredUid?: string;
  /** It runs now: the selected fill. */
  current: boolean;
  /** A model row with an override of its own. */
  customized: boolean;
  /** A preset row's preset (its name is the row's name). */
  preset?: PanelPreset;
  /** The newest version of a family that has older ones listed (lib/model-families.ts): the row says "Latest". */
  latest?: boolean;
}

export interface PanelSection {
  key: string;
  kind: "favorites" | "recent" | "recommended" | "presets" | "provider";
  provider?: PanelProvider;
  rows: PanelRow[];
  /** The provider's list is still being read: its header and a spinner. */
  loading?: boolean;
  /** The All view shows only the group's first rows (ALL_GROUP_MAX): how many it has in all, for its "Show all N". */
  total?: number;
  /** The rows by model family (lib/model-families.ts), when a family has two rows or more: drawn in place of `rows`,
   *  which then holds the same rows (the ones shown) in the same order. */
  blocks?: PanelBlock[];
}

/** A provider group's rows in the All view, not searching, when several providers are listed: the rest are a click away
 *  in the provider's own view, so one long list does not bury the providers below it. */
export const ALL_GROUP_MAX = 8;

const NONE_OPEN: ReadonlySet<string> = new Set();

export interface SectionsInput {
  providers: PanelProvider[];
  state: PanelState;
  view: PanelView;
  query: string;
  favorites: PanelFavorite[];
  recent: PanelConfig[];
  overrides: PanelOverrides;
  /** Ready-made setups listed after the favorites (New Agent's presets). */
  presets?: PanelPreset[];
  /** Families the user unfolded (PanelBlock keys); the others keep their older versions folded. */
  openFamilies?: ReadonlySet<string>;
  /** Flat launcher search keeps the real session setup without a Recommended projection. */
  promote?: boolean;
}

/** A saved copy's model among the providers: undefined when its provider is gone, or its list is read and lacks it
 *  (unless the provider takes typed IDs: then it is one). A list not read yet stands in a placeholder. */
function resolveCopy(providers: PanelProvider[], copy: PanelConfig): { provider: PanelProvider; model: PanelModel; listed: boolean } | undefined {
  const provider = providers.find((p) => p.id === copy.provider);
  if (!provider) return undefined;
  if (provider.models === null) return { provider, model: { id: copy.model, name: copy.model, efforts: [] }, listed: false };
  const model = provider.models.find((m) => isCurrentModel(m, copy.model));
  if (model) return { provider, model, listed: true };
  if (provider.typable) return { provider, model: { id: copy.model, name: copy.model, efforts: [], custom: true }, listed: false };
  return undefined;
}

/**
 * The list for a view (Cindy's buildUnifiedListSections). All: Favorites, the presets, Recommended (the model that runs
 * now, then the other models of the providers in `state.recommended`; those leave their own group; none for an Agent not
 * started yet), then one group per provider (an emptied group is left out; one whose list is being read shows a spinner).
 * Not searching, each provider group of an All view that lists several shows its first ALL_GROUP_MAX rows (and the one
 * that runs now) with its `total`. A provider's view: its favorites and presets, then its models. Favorites: the
 * favorites only. Recent: the last setups applied. A search searches every provider, and a typed model ID none of whose
 * models answers is offered as "Use “…”" in the group of each provider that takes a pick here and takes typed IDs.
 * A provider group (and Recommended, after the model that runs) keeps each model family together, newest first, with
 * the older versions folded unless unfolded (`openFamilies`), searched for or in use (lib/model-families.ts); the
 * ALL_GROUP_MAX cut counts the rows shown.
 */
export function panelSections({ providers, state, view: chosen, query, favorites, recent, overrides, presets, openFamilies, promote = true }: SectionsInput): PanelSection[] {
  const q = query.trim();
  const open = openFamilies ?? NONE_OPEN;
  // an older version the user is using never folds away: the one that runs or waits, a favorite's, a recent setup's,
  // one customized
  const used = (c: PanelConfig | null | undefined, row: PanelRow) => Boolean(c) && c?.provider === row.provider.id && isCurrentModel(row.model, c.model);
  const listed = (id: string) => providers.find((p) => p.id === id)?.models;
  const keep = (row: PanelRow) =>
    row.current || row.customized || used(state.pending, row) || favorites.some((f) => used(f, row)) || recent.slice(0, RECENT_MAX).some((c) => used(c, row));
  const view: PanelView = q ? { kind: "all" } : chosen;
  const inView = (p: PanelProvider) => view.kind !== "provider" || p.id === view.provider;
  const out: PanelSection[] = [];

  if (view.kind === "recent") {
    const rows: PanelRow[] = [];
    for (const [i, c] of recent.entries()) {
      if (rows.length >= RECENT_MAX) break;
      const r = resolveCopy(providers, c);
      if (!r) continue;
      const setup = copySetup(c, r.model, r.listed);
      const starred = favorites.find((f) => f.provider === setup.provider && isCurrentModel(r.model, f.model) && sameSetup(copySetup(f, r.model, r.listed), setup));
      const now = state.current;
      const current = Boolean(now) && runsNow(r.provider, r.model, now) && (setup.effort || "") === (now?.effort || "") && (setup.speed || "standard") === (now?.speed || "standard");
      rows.push({ key: `recent:${i}`, kind: "recent", provider: r.provider, model: r.model, setup, starredUid: starred?.uid, current, customized: false });
    }
    return rows.length ? [{ key: "recent", kind: "recent", rows }] : [];
  }

  const favRows: PanelRow[] = [];
  for (const f of favorites) {
    const r = resolveCopy(providers, f);
    if (!r || !inView(r.provider) || !matchesQuery(r.provider, r.model, q)) continue;
    favRows.push({ key: `fav:${f.uid}`, kind: "favorite", provider: r.provider, model: r.model, setup: copySetup(f, r.model, r.listed), favorite: f, current: false, customized: false });
  }
  if (favRows.length) out.push({ key: "favorites", kind: "favorites", rows: favRows });
  if (view.kind === "favorites") return out;

  // the presets (a copy of a setup each, like a favorite: never lit, the model they run is lit in its own group)
  const presetRows: PanelRow[] = [];
  for (const x of presets ?? []) {
    const r = resolveCopy(providers, x.setup);
    if (!r || !inView(r.provider) || !matches([x.name, x.description, r.model.name, r.model.id, r.model.resolved, r.provider.name], q)) continue;
    presetRows.push({ key: `preset:${x.key}`, kind: "preset", provider: r.provider, model: r.model, setup: cleanSetup(x.setup), preset: x, current: false, customized: false });
  }
  if (presetRows.length) out.push({ key: "presets", kind: "presets", rows: presetRows });

  const shown = providers.filter(inView);
  const groups = new Map<string, PanelRow[]>();
  for (const p of shown) {
    if (p.models === null) continue;
    groups.set(p.id, modelRows(p, p.models.filter((m) => matchesQuery(p, m, q)), state, overrides));
  }

  if (view.kind === "all" && !state.starting && promote) {
    const promoted: PanelRow[] = [];
    const now = state.current ? groups.get(state.current.provider)?.find((r) => r.current) : undefined;
    if (now) promoted.push(now);
    for (const p of shown) if (state.recommended.includes(p.id)) for (const r of groups.get(p.id) ?? []) if (r !== now) promoted.push(r);
    if (promoted.length) {
      // the model that runs stays first; the rest keep their families together
      const blocks = familyBlocks(now ? promoted.slice(1) : promoted, { fold: !q, keep, open, scope: "recommended", listed });
      if (blocks && now) blocks.unshift({ key: "recommended\nnow", rows: [now], older: 0 });
      out.push({ key: "recommended", kind: "recommended", rows: blocks ? blocks.flatMap((b) => b.rows) : promoted, ...(blocks ? { blocks } : {}) });
      const moved = new Set(promoted);
      for (const [id, rows] of groups) groups.set(id, rows.filter((r) => !moved.has(r)));
    }
  }

  const typed = cleanModelId(q);
  const capped = view.kind === "all" && !q && shown.filter((p) => p.models !== null).length > 1;
  for (const p of shown) {
    if (p.models === null) {
      // a provider that can't take a pick here has no list to wait for; a list that failed shows why (not under a search)
      const waiting = !p.unavailable && !p.locked && (p.loading || !p.error);
      if (waiting) out.push({ key: `provider:${p.id}`, kind: "provider", provider: p, rows: [], loading: true });
      else if (!q) out.push({ key: `provider:${p.id}`, kind: "provider", provider: p, rows: [] });
      continue;
    }
    const rows = [...(groups.get(p.id) ?? [])];
    // only in a provider that takes a pick here (an Agent's other launchers too, with the in-session switch, #5035), and
    // only when none of its models answers the search (by the model's own words, not the provider's name): a word that
    // names a listed model is a search, not an ID
    if (typed && p.typable && !p.unavailable && !p.locked && validModelId(typed) && !p.models.some((m) => matchesQuery({ name: "" }, m, q))) {
      const model: PanelModel = { id: typed, name: typed, efforts: [], custom: true };
      rows.push({ key: `typed:${p.id}\n${typed}`, kind: "typed", provider: p, model, setup: { provider: p.id, model: typed }, current: false, customized: false });
    }
    // a list on its way under rows already shown (the running model's while its list is read): the spinner too
    if (!rows.length) continue;
    // families together, older versions folded (not under a search)
    const key = `provider:${p.id}`;
    const blocks = familyBlocks(rows, { fold: !q, keep, open, scope: key, listed });
    const shownRows = blocks ? blocks.flatMap((b) => b.rows) : rows;
    // the model that runs now stays, wherever it is in the list
    const cut = capped && shownRows.length > ALL_GROUP_MAX ? shownRows.filter((r, i) => i < ALL_GROUP_MAX || r.current) : shownRows;
    const left = cut.length < shownRows.length ? new Set(cut) : undefined;
    const cutBlocks = blocks && left ? blocks.map((b) => ({ ...b, rows: b.rows.filter((r) => left.has(r)) })).filter((b) => b.rows.length) : blocks;
    out.push({ key, kind: "provider", provider: p, rows: cut, ...(cutBlocks ? { blocks: cutBlocks } : {}), ...(p.loading ? { loading: true } : {}), ...(left ? { total: rows.length } : {}) });
  }
  return out;
}

// ── engines ───────────────────────────────────────────────────────────────────

/** One engine a model can run with, as a flyout lists it: the provider (launcher) that runs the model with it. */
export interface EngineChoice {
  engine: string;
  provider: PanelProvider;
  model: PanelModel;
  /** The row's own engine. */
  own: boolean;
  /** Why this provider can't take the pick here (its rows are off); undefined: it can. */
  blocked?: string;
}

/**
 * The engines `row`'s model can run with among `providers` (Cindy's engine pills): a provider runs one engine in
 * Persona, so another engine is another provider listing the same model ID (exactly, or the model an alias stands
 * for); its own first, then one per other engine, the first provider of that engine that can take the pick (else the
 * first of that engine). Only a model row's; a model no other engine lists has its own alone.
 */
export function engineChoices(providers: PanelProvider[], row: Pick<PanelRow, "kind" | "provider" | "model">, state: Pick<PanelState, "busy">): EngineChoice[] {
  const own: EngineChoice = { engine: row.provider.engine, provider: row.provider, model: row.model, own: true };
  if (row.kind !== "model" || row.model.custom || row.model.providerDefault) return [own];
  const ids = [row.model.id, row.model.resolved].filter((x): x is string => Boolean(x)).map((x) => x.toLowerCase());
  const byEngine = new Map<string, EngineChoice>();
  for (const p of providers) {
    if (p.id === row.provider.id || p.engine === row.provider.engine || !p.models) continue;
    const m = p.models.find((x) => !x.custom && !x.providerDefault && [x.id, x.resolved].some((v) => v && ids.includes(v.toLowerCase())));
    if (!m) continue;
    const blocked = rowBlock(p, state)?.reason;
    const had = byEngine.get(p.engine);
    if (!had || (had.blocked !== undefined && blocked === undefined)) byEngine.set(p.engine, { engine: p.engine, provider: p, model: m, own: false, ...(blocked !== undefined ? { blocked } : {}) });
  }
  return [own, ...byEngine.values()];
}

/** What choosing another engine in a row's flyout applies: that provider's model, with the effort `from` has when the
 *  model takes it (Cindy keeps the effort in use when only the engine changes). */
export function engineSetup(choice: Pick<EngineChoice, "provider" | "model">, from: PanelConfig): PanelConfig {
  return cleanSetup({ provider: choice.provider.id, model: choice.model.id, effort: takes(choice.model, from.effort) });
}

// ── geometry ──────────────────────────────────────────────────────────────────

/** Where the list scrolls on opening so the row that runs now has its middle at about 35% of what is visible (Cindy's
 *  computeSelectedRowScrollTop); a row taller than the list goes to the top. In the list's scroll coordinates. */
export function alignedScrollTop(o: { clientHeight: number; scrollHeight: number; rowTop: number; rowBottom: number }): number {
  const max = Math.max(0, o.scrollHeight - o.clientHeight);
  const clamp = (v: number) => Math.round(Math.min(Math.max(0, v), max));
  if (o.rowBottom - o.rowTop >= o.clientHeight) return clamp(o.rowTop);
  return clamp((o.rowTop + o.rowBottom) / 2 - o.clientHeight * 0.35);
}

interface Box {
  left: number;
  right: number;
  top: number;
  bottom: number;
}

/** The flyout's place (Cindy's computeFlyoutPlacement): outside the panel's left edge with a 4 px gap, on the right when
 *  the left has no room, kept 8 px inside the window; its top 12 px above the row's, held within the panel's height. */
export function flyoutPlace(o: { row: Box; panel: Box; width: number; height: number; screenW: number; screenH: number }): { left: number; top: number; side: "left" | "right" } {
  const gap = 4;
  const margin = 8;
  const leftAt = o.panel.left - gap - o.width;
  const rightAt = o.panel.right + gap;
  let side: "left" | "right";
  if (leftAt >= margin) side = "left";
  else if (rightAt + o.width <= o.screenW - margin) side = "right";
  else side = leftAt >= o.screenW - (rightAt + o.width) ? "left" : "right";
  const left = Math.min(Math.max(margin, side === "left" ? leftAt : rightAt), Math.max(margin, o.screenW - o.width - margin));
  const lowest = o.panel.bottom - o.height;
  let top = Math.min(o.row.top - 12, lowest);
  top = Math.max(top, Math.min(o.panel.top, lowest));
  top = Math.min(Math.max(margin, top), Math.max(margin, o.screenH - o.height - margin));
  return { left, top, side };
}

// ── effort colors ─────────────────────────────────────────────────────────────

const EFFORT_TONES = ["minimal", "low", "medium", "high", "xhigh", "max"];

/** The color of an effort level (styles.css --effort-*, the same in light and dark), by the level itself: a model that
 *  tops out at High is blue at its top, and purple is only for the true top (Codex's Ultra takes Max's). An effort the
 *  app doesn't know takes the middle one. */
export function effortColor(id: string | undefined): string {
  const tone = id === "ultra" ? "max" : id && EFFORT_TONES.includes(id) ? id : "medium";
  return `var(--effort-${tone})`;
}

/** The slider's color at a continuous position between its stops while it is dragged: the two levels around it mixed. */
export function effortColorAt(stops: string[], position: number): string {
  if (!stops.length) return effortColor(undefined);
  const p = Math.min(Math.max(0, position), stops.length - 1);
  const lo = Math.floor(p);
  const hi = Math.min(stops.length - 1, lo + 1);
  const share = Math.round((p - lo) * 100);
  if (share === 0 || lo === hi) return effortColor(stops[lo]);
  if (share === 100) return effortColor(stops[hi]);
  return `color-mix(in srgb, ${effortColor(stops[hi])} ${share}%, ${effortColor(stops[lo])})`;
}

/** One to three dollar marks from the standard output price; malformed values mean unknown, never a guessed tier. */
export function priceTier(price: PanelPrice | undefined): 1 | 2 | 3 | undefined {
  if (!price || ![price.input, price.output].every((v) => Number.isFinite(v) && v >= 0)) return undefined;
  if (price.cache_read !== undefined && (!Number.isFinite(price.cache_read) || price.cache_read < 0)) return undefined;
  if (price.output <= 3) return 1;
  if (price.output <= 15) return 2;
  return 3;
}

/** A USD amount per million tokens, retaining the small cache-read prices. */
export function priceAmount(value: number): string {
  return new Intl.NumberFormat("en-US", { maximumFractionDigits: value > 0 && value < 0.01 ? 4 : 2, useGrouping: false }).format(value);
}

/** A context window as the flyout writes it: 200K, 1M. */
export function contextSize(tokens: number): string {
  // 999,600 rounds to 1000K: that is 1M
  if (tokens >= 1_000_000 || Math.round(tokens / 1_000) >= 1_000) return `${+(tokens / 1_000_000).toFixed(1)}M`;
  if (tokens >= 1_000) return `${Math.round(tokens / 1_000)}K`;
  return String(tokens);
}
