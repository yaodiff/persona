// A launcher's models (#794): what its agent lists in its own /model menu (GET /machines/{mid}/launchers/{name}/models,
// node/launcher_models.go), read by New Agent's model panel (#5104), the chat's and the model settings' pickers; which
// launcher a picker asks, searching the list, the thinking efforts a pick allows, and what a start sends. A New Agent
// pick is for one start only: nothing about it is saved on the hub.
import { MANAGER_HEADER } from "./manager-choice";
import { t } from "../i18n";
import { api, ApiError, enc, pastHub } from "./api";
import { cleanModelId, validModelId } from "./model-id";
import type { ModelBinding } from "./model-config";
import type { PanelPrice } from "./model-panel";
import { CODEX_EFFORTS, EFFORTS } from "./session-actions";
import { launcherTitle, type Named } from "./provider-names";
import type { Launcher, Session } from "./types";

export interface AgentModel {
  /** What a start sends as `model` (Claude Code's aliases too: opus, sonnet…). */
  id: string;
  name: string;
  description?: string;
  /** The concrete model an alias stands for with this launcher's settings. */
  resolved?: string;
  /** What the agent runs when no model is given: picking it is the same as picking none. */
  default?: boolean;
  /** Thinking efforts it takes, weakest first; none: no effort setting. */
  efforts?: string[];
  default_effort?: string;
  /** Native speed choices (Codex serviceTiers), preserved rather than inferred from the model name. */
  service_tiers?: Session["service_tiers"];
  /** An ID the user typed before that ran here, remembered on the hub for this computer and launcher (#1644): listed
   *  after the agent's own, most recent first, with no efforts (not known). It can be forgotten. */
  custom?: boolean;
  /** Exact catalog reference price in USD per one million tokens, when the hub knows it. */
  price?: PanelPrice;
  /** The other spellings the hub folded into this row: they run the same model with the same context window here (Claude
   *  Code's tier aliases on a gateway whose tiers name one model, #10365). Each still picks this row (`listedModel`). */
  aliases?: string[];
}

export interface LauncherModels {
  /** What the launcher runs: claude, codex, traex, a runtime's agent. */
  agent: string;
  /** Empty: this agent has no list (no picker). */
  models: AgentModel[];
  /** Why a pick would not reach the agent (the launcher fixes its own --model); the start sends no model then. */
  locked?: string;
  /** The node accepts a model when starting the native omp launcher on the queried host (#11964). */
  supports_start_model?: boolean;
  /** This launcher's Codex start on the queried host accepts a native service tier. */
  supports_start_service_tier?: boolean;
}

/** What "Model configuration" runs when a launcher picker is left alone (GET /machines/{mid}/launchers's `default`, #2455):
 *  `provider` is the launcher's name. */
export interface LauncherDefault {
  provider?: string;
  model?: string;
  effort?: string;
  permission_mode?: string;
  /** Main chat only: cli or agentsdk. */
  runtime?: string;
}

/** One of "New agent"'s presets (#2225, docs/assistant/model-profiles.md "Agent presets"): a tier of the model profile in use
 *  whose launcher this computer starts. Picking it fills launcher, model, effort and permission mode as picking them by hand would. */
export interface PickerTier {
  name: string;
  launcher: string;
  /** "" or absent: the launcher's default (the model settings' when the launcher is theirs). */
  model?: string;
  effort?: string;
  permission_mode?: string;
  /** When to use it. */
  description?: string;
}

export interface MachineLaunchers {
  launchers: Launcher[];
  /** Absent from an older hub; null when the purpose is not a launcher use. */
  default?: LauncherDefault | null;
  /** "New agent" only: absent from an older hub, with no profile in use, or none whose launcher this computer has. */
  tiers?: PickerTier[];
  /** The profile the presets come from (they follow its switches). */
  tiers_profile?: { id: string; name: string };
  /** `?purpose=assistant` on a hub with `assistant_model_pick` (#5033): what can't run the main chat on this computer, with
   *  why in the request's language (shown off, not hidden). For "New agent" (#8109): the Persona-managed providers its
   *  own-service rule leaves out on this computer, with why; absent when there are none, or from an older hub. */
  unavailable?: UnavailableLauncher[];
  /** "New agent" only (#17650): the Orca chat a start that keeps the default opens as, while the computer's "Start new
   *  Agents as Orca chats" is on and its Orca offers that chat; absent otherwise and from an older hub (lib/orca-chat.ts). */
  default_opens?: import("./orca-chat").DefaultOpens;
}

/** One of MachineLaunchers.unavailable: a launcher of the computer or a Model provider that can't run assistant chats here
 *  (or, for "New agent", can't start Agents here). */
export interface UnavailableLauncher {
  name: string;
  display_name?: string;
  /** launcher | api */
  kind?: string;
  agent?: string;
  /** A direct API's protocol (responses, chat…). */
  api?: string;
  /** not_main_chat, api_protocol, launcher_out_of_scope, launcher_own_service, launcher_unhealthy */
  code?: string;
  reason: string;
}

/** The launchers on `mid` with the default of `purpose` (the hub's own when absent: "New agent"). */
export function machineLaunchers(mid: string, opts: { purpose?: string; manager?: string; signal?: AbortSignal } = {}): Promise<MachineLaunchers> {
  const q = opts.purpose ? `?purpose=${enc(opts.purpose)}` : "";
  return api<MachineLaunchers>(`/machines/${enc(mid)}/launchers${q}`, { signal: opts.signal, headers: opts.manager ? { [MANAGER_HEADER]: opts.manager } : undefined });
}

/** A picker's first entry: "Default: <provider> · <model>" from the hub's default, `fallback` naming the launcher when the hub
 *  says nothing (an older hub). The provider is a startup ID; `names` titles it (#5029). */
export function defaultLabel(d: LauncherDefault | null | undefined, fallback = "", names?: readonly Named[]): string {
  const provider = d?.provider || fallback;
  const parts = [provider && launcherTitle(provider, names), d?.model && modelName({ id: d.model.split("/").pop() || d.model })].filter(Boolean);
  return parts.length ? t("lib-launcher-models.default_named", { name: parts.join(" · ") }) : t("lib-launcher-models.default");
}

// The hub gives a model list 70 s (assistant/models_launchers.go): the node may have to start the CLI to ask its /model.
const MODELS_HUB_MS = 70_000;

async function readModelCatalog<T extends LauncherModels>(path: string, params: [string, string | undefined][], opts: { manager?: string; signal?: AbortSignal; dedupe?: boolean }): Promise<T | null> {
  const query = new URLSearchParams();
  for (const [k, v] of params) if (v) query.set(k, v);
  try {
    const r = await api<T>(`${path}${query.size ? `?${query}` : ""}`, { signal: opts.signal, timeout: pastHub(MODELS_HUB_MS), headers: opts.manager ? { [MANAGER_HEADER]: opts.manager } : undefined, dedupe: opts.dedupe });
    return { ...r, models: r.models ?? [] };
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null;
    throw e;
  }
}

/** The list for `launcher` on `mid` (seconds when the node has to start the CLI); null from an older hub (no picker). */
export function launcherModels(mid: string, launcher: string, opts: { refresh?: boolean; purpose?: "assistant"; host?: string; manager?: string; signal?: AbortSignal; dedupe?: boolean } = {}): Promise<LauncherModels | null> {
  return readModelCatalog(`/machines/${enc(mid)}/launchers/${enc(launcher)}/models`, [["refresh", opts.refresh ? "1" : ""], ["purpose", opts.purpose], ["host", opts.host]], opts);
}

/** "Model configuration"'s list for a launcher provider (`launcher.<name>`, GET /model-config/providers/{id}/models, #943): asked on a
 *  machine that has the launcher, which `machine` names. null: an older hub or a provider without a list (no picker). */
export function providerModels(provider: string, opts: { manager?: string; refresh?: boolean; machine?: string; signal?: AbortSignal } = {}): Promise<(LauncherModels & { machine?: string }) | null> {
  // machine: the purpose's own (a task, Lark /agent run there), so the list is that machine's launcher.
  return readModelCatalog(`/model-config/providers/${enc(provider)}/models`, [["refresh", opts.refresh ? "1" : ""], ["machine", opts.machine]], opts);
}

/** What is typed in the search, as an id to use when no model has exactly that id (or that alias) and the hub would take
 *  it (#1644, lib/model-id.ts): "Model configuration" and "New agent" take ids the list lacks. */
export function customModel(models: AgentModel[], query: string): string {
  const id = cleanModelId(query);
  return validModelId(id) && !listedModel(models, id) ? id : "";
}

/** The row of `models` that `id` picks: the one with that id, else the one the hub folded it into (`aliases`), so a model
 *  saved as `opus` still finds its row after the hub lists it under the concrete model's id (#10365). */
export function listedModel<M extends Pick<AgentModel, "id" | "aliases">>(models: readonly M[], id: string): M | undefined {
  return models.find((m) => m.id === id) ?? models.find((m) => m.aliases?.includes(id));
}

/** Engines whose pickers accept a manually entered model ID (#1644). */
export const takesTypedModel = (agent: string) =>
  agent === "claude" || agent === "codex" || agent === "pi" || agent === "cursor" || agent === "agy";

/** A runtime announces its own source and may provide a model catalog for its omp sessions. */
export const terminalOmp = (agent: string, launcher?: Pick<Launcher, "source">) =>
  agent === "omp" && (!launcher?.source || launcher.source === "zshrc" || launcher.source === "path" || launcher.source === "builtin");

/** Only the plain omp command on herdr or Orca can advertise a typed model for a new terminal session (#11964). */
export function nativeOmpStart(agent: string, launcher: Pick<Launcher, "name" | "source"> | undefined, host: string | undefined): boolean {
  return terminalOmp(agent, launcher) && launcher?.name === "omp" && (host === "herdr" || host === "orca");
}

/** omp's start capability belongs to the terminal host, including when another launcher's destination is a desktop app. */
export const startCatalogHost = (launcher: Pick<Launcher, "name" | "source"> | undefined, host: string | undefined, terminalHost: string) =>
  launcher?.name === "omp" && terminalOmp("omp", launcher) ? terminalHost || undefined : host;

/** A fixed launcher keeps its model. Live pickers do not opt into the native omp start capability. */
export const typedModelOffered = (agent: string, list: LauncherModels | null | undefined, nativeOmp = false) =>
  !list?.locked && (takesTypedModel(agent) || (nativeOmp && list?.supports_start_model === true));

/** Forgets one model ID typed for `launcher` on `mid` (#1644, the list's "Forget"): the hub stops listing it. */
export function forgetCustomModel(mid: string, launcher: string, model: string, opts: { manager?: string } = {}): Promise<unknown> {
  return api(`/machines/${enc(mid)}/launchers/${enc(launcher)}/models/custom?model=${enc(model)}`, { method: "DELETE", headers: opts.manager ? { [MANAGER_HEADER]: opts.manager } : undefined });
}

/** `list` without the typed ID `id` (the optimistic Forget), and how to put it back where it was when the hub says no. */
export function withoutCustom(list: LauncherModels, id: string): { list: LauncherModels; restore: (now: LauncherModels) => LauncherModels } {
  const at = list.models.findIndex((m) => m.id === id && m.custom);
  if (at < 0) return { list, restore: (now) => now };
  const row = list.models[at];
  return {
    list: { ...list, models: list.models.filter((_, i) => i !== at) },
    restore: (now) => (now.models.some((m) => m.id === id) ? now : { ...now, models: [...now.models.slice(0, at), row, ...now.models.slice(at)] }),
  };
}

/** The "Model configuration" binding of "New agent" that still applies with the chosen launcher: the hub keeps its model and effort
 *  unless another launcher is picked (hub applyModel). Not following "Model configuration": nothing. */
export function followedBinding(chosen: string, effective: ModelBinding | null | undefined): ModelBinding {
  if (!effective?.provider || (chosen && `launcher.${chosen}` !== effective.provider)) return {};
  return effective;
}

/** The launcher whose list the picker shows: the chosen one, else the one "Model configuration" runs (`launcher.<name>`); "": none. */
export function pickerLauncher(chosen: string, followed: ModelBinding): string {
  if (chosen) return chosen;
  const p = followed.provider ?? "";
  return p.startsWith("launcher.") ? p.slice("launcher.".length) : "";
}

/** What the launcher runs: the list's answer, else what the launcher list says (Claude Code when it says nothing). */
export function launcherAgent(name: string, launchers: Launcher[], list?: LauncherModels | null): string {
  if (list?.agent) return list.agent;
  const l = launchers.find((x) => x.name === name);
  return l?.agent_type || l?.agent || "claude";
}

/** A known terminal-compatible CLI takes text with creation. Pin its alias and host together with its kind;
 * attachments, unknown launchers and runtime/desktop starts keep their existing negotiation. */
export function inlineTextStart(launcher: Launcher | undefined, host: string, text: string, attachments: number) {
  if (!launcher || launcher.source || (launcher.agent_type || launcher.agent) !== "traex"
    || (host !== "herdr" && host !== "herdr-core") || !text.trim() || attachments !== 0) return undefined;
  return { launcher: launcher.name, host, agentType: "traex", inlinePrompt: true } as const;
}

/** The permission modes a start on `l` takes when they are not Claude Code's (#8261, the node's `permission_modes`): a
 *  runtime's provider of another agent, Paseo's Grok, OpenCode or Codex. Undefined for Claude Code's launchers, which take
 *  its five, and for every other launcher, which takes none. New Agent offers them as a choice only with two or more:
 *  the launcher's own ("Launcher default", sent as no mode) and Full access. */
export function listedModes(l: Pick<Launcher, "permission_modes"> | undefined): string[] | undefined {
  return l?.permission_modes?.length ? l.permission_modes : undefined;
}

/** How a start takes a thinking effort: `any` (Claude Code, Antigravity: its model's levels, all of them while its list
 *  doesn't say), `listed` (Codex, TRAE CLI and launchers that list permission modes: only the levels its model
 *  reports, #8261, #13764), `none` (the rest keep their own). */
export type EffortRule = "any" | "listed" | "none";

export function effortRule(agent: string, l?: Pick<Launcher, "permission_modes">): EffortRule {
  if (agent === "claude" || agent === "agy") return "any";
  return agent === "codex" || agent === "traex" || listedModes(l) ? "listed" : "none";
}

/** Claude Code and Antigravity start with a thinking effort, and Codex or a launcher that lists its modes starts with
 *  the effort its model reports (`effortRule`); the rest keep their own. */
export const takesEffort = (agent: string, l?: Pick<Launcher, "permission_modes">) => effortRule(agent, l) !== "none";

export function defaultModel(models: AgentModel[]): AgentModel | undefined {
  return models.find((m) => m.default);
}

/** Display only: explicit aliases win; raw GPT ids keep the GPT- prefix and separate their variant words. */
export function modelName(m: { id: string; name?: string }): string {
  const name = m.name?.trim() ? m.name : m.id;
  if (name.toLowerCase() !== m.id.toLowerCase()) return name;
  const gpt = /^gpt-([a-z0-9]+(?:\.[a-z0-9]+)*(?:-[a-z0-9]+(?:\.[a-z0-9]+)*)*)$/i.exec(name);
  if (!gpt) return name;
  return `GPT-${gpt[1].split("-").map((part, i) => i ? part[0].toUpperCase() + part.slice(1) : part).join(" ")}`;
}

/** A catalog variant encodes its default effort in its model ID. */
export function effortVariantBase(id: string, effort?: string): string | undefined {
  return effort && id.endsWith(`-${effort}`) ? id.slice(0, -effort.length - 1) || undefined : undefined;
}

/** Drop effort from the trailing qualifiers while keeping context size and other model qualifiers. */
export function withoutEffortQualifier(name: string): string {
  return name.replace(/\s*[(（](?:minimal|low|medium|high|xhigh|max|ultra)[)）](?=(?:\s*[(（[][^()（）[\]]*[)）\]])*\s*$)/giu, "").trimEnd() || name;
}

/** "Default"'s second line: what "Model configuration" sets, else the concrete model the default entry stands for. */
export function defaultDetail(models: AgentModel[], followed: ModelBinding): string {
  if (followed.model) return followed.model;
  const d = defaultModel(models);
  if (!d) return "";
  return d.resolved || (d.id === "default" ? "" : d.id);
}

/** A row's second line: the concrete model, else the id when it says more than the name. */
export function modelDetail(m: AgentModel): string {
  if (m.resolved && m.resolved !== m.name) return m.resolved;
  return m.id.toLowerCase() !== (m.name || m.id).toLowerCase() ? m.id : "";
}

/** Whether every word of `query` (any case) is in one of `fields`; an empty query matches. */
export function matches(fields: (string | undefined)[], query: string): boolean {
  const text = fields.filter(Boolean).join("\n").toLocaleLowerCase();
  return query.toLocaleLowerCase().split(/\s+/).filter(Boolean).every((word) => text.includes(word));
}

/** What the open list shows for `query` (name, id, resolved model, folded aliases, description; every word): "Default"
 *  first when it matches, then the rest in the agent's order. */
export function modelRows<M extends AgentModel>(models: M[], query: string, followed: ModelBinding = {}): { showDefault: boolean; rows: M[] } {
  const d = defaultModel(models);
  const showDefault = !query.trim() || matches([t("lib-launcher-models.default"), "default", defaultDetail(models, followed), d && modelName(d), d?.name, d?.id, d?.resolved, d?.description], query);
  const rows = models.filter((m) => m !== d && matches([modelName(m), m.name, m.id, m.resolved, ...(m.aliases ?? []), m.description], query));
  return { showDefault, rows };
}

/** Every thinking effort Claude Code takes, weakest first: what a model nobody lists the efforts of is offered. */
export const ALL_EFFORTS = EFFORTS.map((e) => e.id);
const CODEX_LEVELS = new Set(CODEX_EFFORTS.map((e) => e.id));

/** The efforts the control offers: the picked model's, with none picked the one that runs ("Model configuration"'s, else the
 *  default entry's). No list to go by (loading, failed, older hub), or "Model configuration"'s model not in it under that id: all
 *  of them, or none under the `listed` rule (only what its model lists, #8261). */
export function effortChoices(list: LauncherModels | null | undefined, model: string, followed: ModelBinding = {}, rule: EffortRule = "any"): string[] {
  if (rule === "none") return [];
  const unknown = rule === "listed" ? [] : ALL_EFFORTS;
  if (!list?.models.length) return unknown;
  const id = model || followed.model;
  const m = (id ? listedModel(list.models, id) : undefined) ?? (id ? undefined : defaultModel(list.models));
  // a typed ID (listed as typed before, or not at all): what it takes is not known
  const raw = m && !m.custom ? (m.efforts ?? []) : unknown;
  return list.agent === "codex" || list.agent === "traex" ? raw.filter((e) => CODEX_LEVELS.has(e)) : raw;
}

/** The effort kept when the choices change: one they no longer offer goes back to "Launcher default". */
export function fitEffort(effort: string, choices: string[]): string {
  return choices.includes(effort) ? effort : "";
}

interface StartModelOptions {
  agent: string;
  list: LauncherModels | null | undefined;
  followed?: ModelBinding;
  launcher?: Pick<Launcher, "permission_modes" | "source">;
  nativeOmp?: boolean;
}

/** What a start sends: the model only when it is not the default and the launcher lets it through, the effort only where
 *  the start takes one (`effortRule`, by the agent and the launcher that runs) and only one the model takes. A launcher
 *  that takes typed IDs can accept one when its list couldn't be read (`undefined`); an older hub's `null` sends none. */
export function startPicks(o: StartModelOptions & { model: string; effort: string }): { model?: string; effort?: string } {
  if (terminalOmp(o.agent, o.launcher)) {
    return typedModelOffered(o.agent, o.list, o.nativeOmp) && validModelId(o.model) ? { model: o.model } : {};
  }
  const out: { model?: string; effort?: string } = {};
  const m = o.list?.models.find((x) => x.id === o.model);
  if (m) {
    // the default entry goes too when the model settings' own model would run instead (a preset picked it, #2225; the
    // picker's "Default" row picks "")
    if ((!m.default || (o.followed?.model && o.followed.model !== m.id)) && !o.list?.locked) out.model = m.id;
  } else if (o.model && o.list !== null && typedModelOffered(o.agent, o.list) && validModelId(o.model)) {
    out.model = o.model;
  }
  const rule = effortRule(o.agent, o.launcher);
  if (o.effort && rule !== "none" && effortChoices(o.list, out.model ?? "", o.followed, rule).includes(o.effort)) out.effort = o.effort;
  return out;
}

/** What of a preset a start with this launcher's list would not pass on: its model when the launcher fixes its own --model
 *  or takes no typed ID, its effort when the model doesn't take it. Empty: all of it (or the list is not in yet). */
export function tierLeftOut(tier: PickerTier, o: StartModelOptions): string[] {
  const picks = startPicks({ ...o, model: tier.model ?? "", effort: tier.effort ?? "" });
  const out: string[] = [];
  // the list's default is what runs with no model sent (startPicks sends it when the model settings would run another)
  const implicitDefault = !terminalOmp(o.agent, o.launcher) && !o.list?.locked && o.list?.models.some((m) => m.id === tier.model && m.default);
  if (tier.model && picks.model !== tier.model && !implicitDefault) out.push(tier.model);
  if (tier.effort && picks.effort !== tier.effort) out.push(tier.effort);
  return out;
}
