// Wire types for the external source bindings and policies endpoints.
import { api, ApiError, enc } from "../../lib/api";
import { ownerHeaders, stampedHeaders } from "../../lib/manager-choice";
import type { LeaderInfo } from "../../lib/direction-names";

export interface ExternalNode {
  kind: string;
  id: string;
}

export interface ExternalRef {
  provider: string;
  channel?: string;
  container?: ExternalNode;
  item?: ExternalNode;
}

export type TargetKind = "direction" | "todo";
export type ForwardMode = "off" | "realtime" | "digest" | "on_demand";
export type ActionRole = "owner" | "fyi";
export type NewItemPolicy = "ask_owner" | "auto_todo";
export type PreFilterField = "keywords" | "mentions" | "senders";
export type PreFilter = Partial<Record<PreFilterField, string[]>>;

export interface ExternalPolicy {
  mode?: ForwardMode;
  digest_interval_minutes?: number;
  action_role?: ActionRole;
  new_item_policy?: NewItemPolicy;
  pre_filter?: PreFilter;
}

export interface ExternalTarget {
  target_kind: TargetKind;
  target_id: string;
}

export interface BindingInput extends ExternalTarget {
  external_ref: ExternalRef;
  policy: ExternalPolicy;
}

export interface ExternalBinding extends BindingInput {
  id: string;
}

export type BindingMatch = ExternalTarget & {
  external_ref: ExternalRef;
  target_title?: string;
  target_done?: boolean;
} & (
  | { source: "explicit"; binding_id: string }
  | { source: "repository_link"; repository_role: "delivery" | "reference" }
);

export interface ExternalBindingSummary {
  bindings: BindingMatch[];
  omitted_done_count?: number;
  total_count?: number;
}

export interface NormalizedBindingSummary {
  bindings: BindingMatch[];
  omittedDoneCount: number;
  totalCount: number;
  hasServerCounts: boolean;
}

type LookupMatch = BindingMatch | (ExternalTarget & { external_ref: ExternalRef; source: "lark_topic" });

export type PolicySource = "default" | "channel" | "container" | "item" | "binding";
export interface PolicyOrigin {
  source: PolicySource;
  external_ref?: ExternalRef;
  binding_id?: string;
}

export interface PolicyBaseline {
  policy: ExternalPolicy;
  provenance: Partial<Record<keyof ExternalPolicy, PolicyOrigin>>;
}

export interface EffectivePolicy extends PolicyBaseline {
  external_ref: ExternalRef;
}

interface ScopedPolicy {
  external_ref: ExternalRef;
  policy: ExternalPolicy;
}

const BINDINGS = "/assistant/external-source-bindings";
const POLICIES = "/assistant/external-source-policies";

function withQuery(path: string, params: Record<string, string | undefined>) {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined) query.set(key, value);
  }
  return query.size ? `${path}?${query}` : path;
}

export function isExternalSourcesUnsupported(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404 &&
    (!error.isJson || !error.code || error.code === "not_found");
}

export function listExternalBindings(
  manager: string,
  filter: Partial<ExternalTarget & { provider: string; external_ref: ExternalRef }>,
  signal?: AbortSignal,
) {
  return api<{ bindings: ExternalBinding[] }>(withQuery(BINDINGS, {
    ...filter, external_ref: filter.external_ref ? JSON.stringify(filter.external_ref) : undefined,
  }), { headers: stampedHeaders(manager), signal });
}

export function saveExternalBinding(manager: string, input: BindingInput, id?: string) {
  return api<ExternalBinding>(id ? `${BINDINGS}/${enc(id)}` : BINDINGS, {
    method: id ? "PUT" : "POST", headers: stampedHeaders(manager), json: input,
  });
}

export function deleteExternalBinding(manager: string, id: string) {
  return api<void>(`${BINDINGS}/${enc(id)}`, { method: "DELETE", headers: stampedHeaders(manager) });
}

export function lookupExternalBindings(manager: string, ref: ExternalRef, signal?: AbortSignal) {
  return api<{ path: ExternalRef[]; matches: LookupMatch[] }>(
    withQuery(`${BINDINGS}/lookup`, { external_ref: JSON.stringify(ref) }),
    { headers: stampedHeaders(manager), signal },
  );
}

function summaryRepoKey(binding: BindingMatch): string {
  const { external_ref: ref, target_kind: kind, target_id: id } = binding;
  return JSON.stringify([
    ref.provider,
    ref.channel ?? "",
    ref.container?.kind ?? "",
    ref.container?.id ?? "",
    ref.item?.kind ?? "",
    ref.item?.id ?? "",
    kind,
    id,
  ]);
}

/**
 * Deduplicates repository-link projections for the same `(external_ref, target_kind, target_id)` (preferring
 * `delivery` over `reference`, keeping distinct targets and explicit bindings separate) and normalizes optional
 * summary counters so older hubs without the new fields degrade cleanly.
 */
export function normalizeExternalBindingSummary(raw?: ExternalBindingSummary): NormalizedBindingSummary {
  const rawBindings = Array.isArray(raw?.bindings) ? raw.bindings : [];
  const bindings: BindingMatch[] = [];
  const seenExplicit = new Set<string>();
  const repoIndex = new Map<string, number>();

  for (const binding of rawBindings) {
    if (binding.source === "explicit") {
      if (seenExplicit.has(binding.binding_id)) continue;
      seenExplicit.add(binding.binding_id);
      bindings.push(binding);
      continue;
    }
    const key = summaryRepoKey(binding);
    const existingIndex = repoIndex.get(key);
    if (existingIndex === undefined) {
      repoIndex.set(key, bindings.length);
      bindings.push(binding);
      continue;
    }
    const existing = bindings[existingIndex];
    if (existing.source === "repository_link" && existing.repository_role !== "delivery" && binding.repository_role === "delivery") {
      bindings[existingIndex] = {
        ...binding,
        target_title: binding.target_title?.trim() ? binding.target_title : existing.target_title,
        target_done: binding.target_done ?? existing.target_done,
      };
    }
  }

  const omittedDoneCount = typeof raw?.omitted_done_count === "number" && Number.isFinite(raw.omitted_done_count) && raw.omitted_done_count > 0
    ? Math.floor(raw.omitted_done_count)
    : 0;
  const serverTotal = typeof raw?.total_count === "number" && Number.isFinite(raw.total_count) && raw.total_count >= 0
    ? Math.floor(raw.total_count)
    : undefined;
  const hasServerCounts = serverTotal !== undefined || raw?.omitted_done_count !== undefined;
  const totalCount = serverTotal !== undefined && bindings.length === rawBindings.length ? serverTotal : bindings.length;

  return { bindings, omittedDoneCount, totalCount, hasServerCounts };
}

export function externalBindingSummary(
  manager: string,
  provider?: string,
  channel?: string,
  signal?: AbortSignal,
  includeDone?: boolean,
) {
  return api<ExternalBindingSummary>(withQuery(`${BINDINGS}/summary`, {
    provider,
    channel,
    include_done: includeDone ? "true" : undefined,
  }), {
    headers: ownerHeaders(manager), signal,
  });
}

export function readExternalPolicy(manager: string, ref: ExternalRef, signal?: AbortSignal) {
  return api<ScopedPolicy>(withQuery(POLICIES, { external_ref: JSON.stringify(ref) }), {
    headers: ownerHeaders(manager), signal,
  });
}

export function saveExternalPolicy(manager: string, ref: ExternalRef, policy: ExternalPolicy) {
  return api<ScopedPolicy>(withQuery(POLICIES, { external_ref: JSON.stringify(ref) }), {
    method: "PUT", headers: stampedHeaders(manager), json: policy,
  });
}

export function effectiveExternalPolicy(manager: string, ref: ExternalRef, signal?: AbortSignal, bindingId?: string) {
  return api<EffectivePolicy>(withQuery(`${POLICIES}/effective`, {
    external_ref: JSON.stringify(ref), binding_id: bindingId,
  }), { headers: stampedHeaders(manager), signal });
}

export async function readExternalDirections(manager: string, signal?: AbortSignal) {
  const result = await api<{ leaders: LeaderInfo[] }>("/assistant/leaders", { headers: ownerHeaders(manager), signal });
  return result.leaders ?? [];
}
