// The four dimensions a run is made of (docs/assistant/execution-spec.md): runtime × channel × model × effort, as the
// hub declares them. Kept apart from lib/api.ts: the wire types of /api/v1/execution/*, the requests, and the pure
// rules the pickers share (the main chat's model + effort, the "Advanced / route" fields, the three-level check of
// POST /execution/validate). Every option comes from the hub's declarations; nothing here lists models or channels.
import { useSyncExternalStore } from "react";
import { t } from "../i18n";
import { api, ApiError, enc } from "./api";
import type { ModelPick, Session } from "./types";

// ── wire types (server/internal/proto/execspec.go) ───────────────────────────

/** ExecutionSpec: an empty dimension is inherited (the hub fills it in). */
export interface ExecutionSpec {
  runtime?: string;
  channel?: string;
  model?: string;
  effort?: string;
}

export type ChannelKind = "native" | "managed" | "external_shell";
export type CombinationLevel = "allowed" | "notice" | "blocked";
export type EdgeTier = "native" | "bridged_stable" | "bridged_experimental";

export interface RuntimeCapability {
  runtime: string;
  protocols?: string[];
  effort_via?: string;
  efforts?: string[];
  live_switch?: string[];
  managed?: boolean;
}

export interface ChannelEdge {
  protocol: string;
  tier: EdgeTier;
  wire_model?: string;
  effort_via?: string;
  warnings?: string[];
}

export interface ChannelModel {
  canonical: string;
  name?: string;
  efforts?: string[];
  default_effort?: string;
  context_window?: number;
  max_output_tokens?: number;
  quota_pool?: string;
  edges?: ChannelEdge[];
}

export interface ChannelQuota {
  source: string;
  provider: string;
}

export interface ChannelSupply {
  name: string;
  display_name?: string;
  kind: ChannelKind;
  runtimes?: string[];
  endpoints?: string[];
  models?: ChannelModel[];
  quota?: ChannelQuota;
  aliases?: string[];
  rev?: string;
}

export interface MachineReadiness {
  machine: string;
  name?: string;
  online: boolean;
  known: boolean;
  runtimes?: string[];
  channels?: string[];
}

export interface CombinationReason {
  code: string;
  dimension?: string;
  text: string;
}

export interface CombinationVerdict {
  spec: ExecutionSpec;
  level: CombinationLevel;
  tier?: EdgeTier;
  protocol?: string;
  wire_model?: string;
  wire_effort?: string;
  launcher?: string;
  recommended: boolean;
  reasons?: CombinationReason[];
  alternatives?: ExecutionSpec[];
}

export interface LauncherAlias {
  launcher: string;
  spec: ExecutionSpec;
}

export interface ExecutionCatalog {
  runtimes: RuntimeCapability[];
  channels: ChannelSupply[];
  machines: MachineReadiness[];
  aliases: LauncherAlias[];
  combinations?: CombinationVerdict[];
}

export interface ChannelBinding {
  runtime: string;
  alias: string;
  api?: string;
  base_url?: string;
  model?: string;
  effort?: string;
}

/** ChannelConfig as Settings reads it: the key and headers are write-only, a view says only has_key / has_headers. */
export interface ChannelConfig {
  name: string;
  display_name?: string;
  base_url: string;
  api_key?: string;
  has_key?: boolean;
  headers?: string;
  has_headers?: boolean;
  auth?: string;
  proxy?: string;
  no_proxy?: string;
  share?: string;
  scope_machines?: string[];
  delivery?: string;
  endpoints: string[];
  models?: ChannelModel[];
  quota?: ChannelQuota;
  bindings: ChannelBinding[];
  replace_launchers?: string[];
  rev?: string;
  updated?: string;
}

export interface ChannelRevision {
  revision: number;
  at: string;
  action: "save" | "delete" | "rollback" | string;
  channel?: string;
  channels: string[];
}

export interface ChannelCatalogView {
  revision: number;
  channels: ChannelConfig[];
  rollbacks: ChannelRevision[];
}

export interface LastGoodSpec {
  spec: ExecutionSpec;
  launcher: string;
  machine: string;
  at: string;
}

/** A ModelPick with the two dimensions the hub takes besides the launcher (#17368). */
export type SpecPick = ModelPick & { runtime?: string; channel?: string };

// ── requests ─────────────────────────────────────────────────────────────────

/** GET /execution/catalog (?machine= adds every alias's verdict on that computer). */
export const executionCatalog = (machine?: string) =>
  api<ExecutionCatalog>("/execution/catalog", machine ? { query: { machine } } : {});

/** POST /execution/validate: one combination's verdict, its text in the request's language. */
export const validateExecution = (spec: ExecutionSpec, machine?: string) =>
  api<CombinationVerdict>("/execution/validate", { method: "POST", json: { spec: cleanSpec(spec), ...(machine ? { machine } : {}) } });

export const executionChannels = () => api<ChannelCatalogView>("/execution/channels");

/** PUT /execution/channels/{name}; `revision`: the catalog's revision the change was made on (409 when it moved). */
export const saveExecutionChannel = (config: ChannelConfig, revision?: number) =>
  api<{ ok: boolean; revision: number; channel?: ChannelConfig }>(`/execution/channels/${enc(config.name)}`, {
    method: "PUT",
    json: { ...writableChannel(config), ...(revision !== undefined ? { revision } : {}) },
  });

export const deleteExecutionChannel = (name: string, revision?: number) =>
  api<{ ok: boolean; revision: number }>(`/execution/channels/${enc(name)}`, {
    method: "DELETE",
    ...(revision !== undefined ? { query: { revision: String(revision) } } : {}),
  });

/** POST /execution/channels/rollback: `revision` none is the newest point. */
export const rollbackExecutionChannels = (revision?: number) =>
  api<{ ok: boolean; revision: number }>("/execution/channels/rollback", { method: "POST", json: revision !== undefined ? { revision } : {} });

const conversationPath = (s: Pick<Session, "session_id">) => `/assistant/conversations/${enc(s.session_id)}/model`;

/** GET …/model/last-good: the setup the chat last finished a turn on without an error; null when none. */
export async function conversationLastGood(s: Pick<Session, "session_id">): Promise<LastGoodSpec | null> {
  try {
    return (await api<{ last_good: LastGoodSpec | null }>(`${conversationPath(s)}/last-good`)).last_good ?? null;
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null;
    throw e;
  }
}

/** POST …/model/restore: back to the last working setup; null when the hub keeps none (404). */
export async function restoreConversationLastGood(s: Pick<Session, "session_id">): Promise<{ model_pick?: SpecPick; last_good?: LastGoodSpec } | null> {
  try {
    return await api<{ ok: boolean; model_pick?: SpecPick; last_good?: LastGoodSpec }>(`${conversationPath(s)}/restore`, { method: "POST" });
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null;
    throw e;
  }
}

// ── rules ────────────────────────────────────────────────────────────────────

/** The spec without its empty dimensions (they are inherited). */
export function cleanSpec(spec: ExecutionSpec): ExecutionSpec {
  const out: ExecutionSpec = {};
  for (const k of ["runtime", "channel", "model", "effort"] as const) {
    const v = spec[k]?.trim();
    if (v) out[k] = v;
  }
  return out;
}

export const sameSpec = (a: ExecutionSpec, b: ExecutionSpec) => JSON.stringify(cleanSpec(a)) === JSON.stringify(cleanSpec(b));

/** What a PUT writes: never the read-only fields (has_key, has_headers, rev, updated); empty secrets are left out so the
 *  hub keeps the stored ones. */
export function writableChannel(c: ChannelConfig): ChannelConfig {
  const { has_key: _k, has_headers: _h, rev: _r, updated: _u, api_key, headers, ...rest } = c;
  void _k; void _h; void _r; void _u;
  return { ...rest, ...(api_key?.trim() ? { api_key: api_key.trim() } : {}), ...(headers?.trim() ? { headers: headers.trim() } : {}) };
}

/** One choice of the main chat's model picker: a model and an effort, and the combination behind it. */
export interface RecommendedPick {
  model: string;
  effort: string;
  spec: ExecutionSpec;
  launcher?: string;
  verdict: CombinationVerdict;
}

/** The main chat's default choices: the catalog's combinations (?machine=) that are not blocked and recommended, one per
 *  model + effort, in the hub's order. An experimental bridge is never recommended, so it never shows here. */
export function recommendedPicks(catalog: ExecutionCatalog | null | undefined): RecommendedPick[] {
  const out: RecommendedPick[] = [];
  const seen = new Set<string>();
  for (const v of catalog?.combinations ?? []) {
    if (v.level === "blocked" || !v.recommended || v.tier === "bridged_experimental") continue;
    const model = v.spec.model ?? "";
    const effort = v.spec.effort ?? "";
    const key = `${model}\u0000${effort}`;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push({ model, effort, spec: cleanSpec(v.spec), launcher: v.launcher, verdict: v });
  }
  return out;
}

/** The distinct models of `picks`, in order. */
export const pickModels = (picks: readonly RecommendedPick[]) => [...new Set(picks.map((p) => p.model))];

/** The efforts `picks` offer for `model`, in order. */
export const pickEfforts = (picks: readonly RecommendedPick[], model: string) => [...new Set(picks.filter((p) => p.model === model).map((p) => p.effort))];

/** The pick for model + effort: that one, else the model's first (its effort then is the closest the hub offers). */
export function choosePick(picks: readonly RecommendedPick[], model: string, effort: string): RecommendedPick | undefined {
  return picks.find((p) => p.model === model && p.effort === effort) ?? picks.find((p) => p.model === model);
}

/** "Advanced / route": the runtime choices are the catalog's runtimes. */
export const runtimeOptions = (catalog: ExecutionCatalog | null | undefined) => (catalog?.runtimes ?? []).map((r) => r.runtime);

/** The channel choices: the computer's channels (machines[m].channels) that run `runtime`; every channel that runs it
 *  when the hub has not seen the computer. */
export function channelOptions(catalog: ExecutionCatalog | null | undefined, machine: string | undefined, runtime: string | undefined): ChannelSupply[] {
  const m = machine ? catalog?.machines.find((x) => x.machine === machine) : undefined;
  const onMachine = m?.channels ? new Set(m.channels) : null;
  return (catalog?.channels ?? []).filter((c) => (!runtime || (c.runtimes ?? []).includes(runtime)) && (!onMachine || onMachine.has(c.name)));
}

/** The model choices: the channel's models (none listed: any, typed). */
export function modelOptions(catalog: ExecutionCatalog | null | undefined, channel: string | undefined): ChannelModel[] {
  return channel ? (catalog?.channels.find((c) => c.name === channel)?.models ?? []) : [];
}

/** The effort choices: the model's, else the runtime's. */
export function effortOptions(catalog: ExecutionCatalog | null | undefined, channel: string | undefined, model: string | undefined, runtime: string | undefined): string[] {
  const m = modelOptions(catalog, channel).find((x) => x.canonical === model);
  if (m?.efforts?.length) return m.efforts;
  return catalog?.runtimes.find((r) => r.runtime === runtime)?.efforts ?? [];
}

/** A launcher name's four dimensions (catalog.aliases); undefined when it is no alias the hub knows. */
export const aliasSpec = (catalog: ExecutionCatalog | null | undefined, launcher: string | undefined): ExecutionSpec | undefined =>
  launcher ? catalog?.aliases.find((a) => a.launcher === launcher)?.spec : undefined;

/** The launcher alias that runs `spec`'s runtime on its channel (catalog.aliases); undefined when none does. */
export function aliasFor(catalog: ExecutionCatalog | null | undefined, spec: ExecutionSpec): string | undefined {
  if (!spec.runtime || !spec.channel) return undefined;
  return catalog?.aliases.find((a) => a.spec.runtime === spec.runtime && a.spec.channel === spec.channel)?.launcher;
}

/** A change in one dimension, the dependent ones cleared when they no longer fit (runtime → channel → model → effort). */
export function narrowSpec(catalog: ExecutionCatalog | null | undefined, machine: string | undefined, spec: ExecutionSpec, patch: ExecutionSpec): ExecutionSpec {
  const next = { ...spec, ...patch };
  if (patch.runtime !== undefined && next.channel && !channelOptions(catalog, machine, next.runtime).some((c) => c.name === next.channel)) next.channel = "";
  const models = modelOptions(catalog, next.channel);
  if ((patch.runtime !== undefined || patch.channel !== undefined) && next.model && models.length && !models.some((m) => m.canonical === next.model)) next.model = "";
  const efforts = effortOptions(catalog, next.channel, next.model, next.runtime);
  if (patch.effort === undefined && next.effort && efforts.length && !efforts.includes(next.effort)) next.effort = "";
  return next;
}

const RUNTIME_NAMES: Record<string, string> = { claude: "Claude Code", codex: "Codex", agy: "Antigravity", omp: "Oh My Pi", pi: "Pi", grok: "Grok" };

/** A runtime's product name (not translated). */
export const runtimeLabel = (runtime: string | undefined) => (runtime ? (RUNTIME_NAMES[runtime] ?? runtime) : "");

/** A channel's name as people read it: a native sign-in and a computer's own launcher in words, a managed one by its
 *  display name. */
export function channelLabel(catalog: ExecutionCatalog | null | undefined, name: string | undefined): string {
  if (!name) return "";
  const c = catalog?.channels.find((x) => x.name === name);
  if (c?.display_name) return c.display_name;
  if (name.startsWith("native:")) return t("execution.channel_native", { runtime: runtimeLabel(name.slice("native:".length)) });
  if (name.startsWith("shell:")) return t("execution.channel_shell", { name: name.slice("shell:".length) });
  return name;
}

/** A model's name: the channel's word for it, else its id. */
export function modelLabel(catalog: ExecutionCatalog | null | undefined, model: string): string {
  if (!model) return t("execution.model_default");
  for (const c of catalog?.channels ?? []) {
    const m = c.models?.find((x) => x.canonical === model);
    if (m?.name) return m.name;
  }
  return model;
}

/** Whether a verdict carries the experimental bridge mark ("Compatibility bridge (experimental)"). */
export const bridgedExperimental = (v: CombinationVerdict | null | undefined) =>
  Boolean(v && (v.tier === "bridged_experimental" || v.reasons?.some((r) => r.code === "bridged_experimental")));

/** What a verdict asks of the form: blocked ones can't be confirmed. */
export const verdictBlocks = (v: CombinationVerdict | null | undefined) => v?.level === "blocked";

/** The pick a verdict (or an alternative of it) makes: its launcher alias when the hub named one, the four dimensions. */
export function pickOfSpec(spec: ExecutionSpec, launcher: string | undefined, fallbackLauncher: string): SpecPick {
  const s = cleanSpec(spec);
  return {
    launcher: launcher || fallbackLauncher,
    ...(s.model ? { model: s.model } : {}),
    ...(s.effort ? { effort: s.effort } : {}),
    ...(s.runtime ? { runtime: s.runtime } : {}),
    ...(s.channel ? { channel: s.channel } : {}),
  };
}

/** A machine's readiness for a runtime: how many of the computers that have it are online. */
export function runtimeReadiness(catalog: ExecutionCatalog | null | undefined, runtime: string): { online: MachineReadiness[]; offline: MachineReadiness[] } {
  const with_ = (catalog?.machines ?? []).filter((m) => (m.runtimes ?? []).includes(runtime));
  return { online: with_.filter((m) => m.online), offline: with_.filter((m) => !m.online) };
}

/** A channel save or delete refused because another change came first (409). */
export const isRevisionConflict = (e: unknown) => e instanceof ApiError && e.status === 409;

// ── forms that hold a blocked combination ────────────────────────────────────

const blocked = new Map<string, Set<string>>();
const listeners = new Set<() => void>();
const versions = new Map<string, number>();

/** A form's part (`key`, e.g. one candidate of a preset) says whether its combination is blocked; the form (`scope`)
 *  disables its confirm button while any part is. */
export function reportBlocked(scope: string, key: string, on: boolean) {
  const set = blocked.get(scope) ?? new Set<string>();
  if (set.has(key) === on) return;
  if (on) set.add(key);
  else set.delete(key);
  blocked.set(scope, set);
  versions.set(scope, (versions.get(scope) ?? 0) + 1);
  for (const l of listeners) l();
}

export const anyBlocked = (scope: string) => (blocked.get(scope)?.size ?? 0) > 0;

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => void listeners.delete(l);
};

/** Whether any part of form `scope` holds a blocked combination. */
export function useAnyBlocked(scope: string): boolean {
  return useSyncExternalStore(subscribe, () => anyBlocked(scope), () => false);
}
