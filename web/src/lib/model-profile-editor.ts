// Pure draft state, validation, and payload serialization for editing a ModelProfile
// (server/internal/assistant/model_profiles_edit.go). Kept separate from lib/model-profiles.ts
// so only the lazy-loaded editor chunk carries it.
import { t } from "../i18n";
import { EFFORT_LEVELS } from "./providers";
import { PERMISSION_MODES } from "./session-actions";
import { validModelId } from "./model-id";
import type { ModelBinding } from "./model-config";
import {
  LAUNCHER_MODEL_FIELDS,
  MAX_MODEL_TIERS,
  MAX_PROFILE_DESCRIPTION,
  MAX_PROFILE_NAME,
  MAX_PROFILE_VALUE,
  MAX_TIER_CANDIDATES,
  MAX_TIER_DESCRIPTION,
  PROFILE_CATEGORY_IDS,
  TIER_NAME_RE,
  type LauncherModelField,
  type ModelProfile,
  type ModelProfileLauncherModels,
  type ModelProfileSaveInput,
  type ModelProfileTier,
  type ModelProfileTierCandidate,
} from "./model-profiles";

export interface PurposeDraftEntry {
  included: boolean;
  provider: string;
  model: string;
  effort: string;
  permission_mode: string;
}

/** Tri-state per launcher field: undefined = leave unchanged on switch; "" = clear to launcher default; non-empty = set. */
export type LauncherDraftEntry = Partial<Record<LauncherModelField, string>>;

export interface CandidateDraftEntry {
  /** Stable React list key so reordering keeps focus and local picker state. */
  key: string;
  /** Original model ID when loaded from the profile, for preserving untouched legacy unlisted model IDs. */
  originalModel?: string;
  launcher: string;
  model: string;
  effort: string;
  permission_mode: string;
  speed: string;
  /** The four dimensions' runtime and channel (PUT /model-profiles/{id} keeps them beside the launcher alias). */
  runtime?: string;
  channel?: string;
}

export interface TierDraftEntry {
  /** Stable React list key so renaming `name` keeps focus. */
  key: string;
  /** Original tier name when loaded from the profile, for preserving legacy unlisted model IDs unchanged. */
  originalName?: string;
  name: string;
  description: string;
  /** Read-only aliases preserved across round-trips. */
  aliases?: string[];
  candidates: CandidateDraftEntry[];
}

export interface ProfileDraft {
  name: string;
  description: string;
  purposes: Record<string, PurposeDraftEntry>;
  launchers: Record<string, LauncherDraftEntry>;
  tiers: TierDraftEntry[];
  /** Tier names the hub returned with a `candidates` list when this profile was loaded. */
  listedTierNames?: string[];
}

export function categoryIdsForProfile(profile: Pick<ModelProfile, "purposes">, catalogPurposeIds?: readonly string[]): string[] {
  const ordered: string[] = [...PROFILE_CATEGORY_IDS];
  for (const id of catalogPurposeIds ?? []) {
    if ((PROFILE_CATEGORY_IDS as readonly string[]).includes(id) && !ordered.includes(id)) ordered.push(id);
  }
  for (const id of Object.keys(profile.purposes ?? {})) {
    if (!ordered.includes(id)) ordered.push(id);
  }
  return ordered;
}

export function profileToDraft(profile: ModelProfile, catalogPurposeIds?: readonly string[]): ProfileDraft {
  const purposes: Record<string, PurposeDraftEntry> = {};
  for (const id of categoryIdsForProfile(profile, catalogPurposeIds)) {
    const has = !!profile.purposes && Object.prototype.hasOwnProperty.call(profile.purposes, id);
    const b = has ? profile.purposes![id] : undefined;
    purposes[id] = {
      included: has,
      provider: b?.provider ?? "",
      model: b?.model ?? "",
      effort: b?.effort ?? "",
      permission_mode: b?.permission_mode ?? "",
    };
  }
  const launchers: Record<string, LauncherDraftEntry> = {};
  for (const [name, fields] of Object.entries(profile.launchers ?? {})) {
    const entry: LauncherDraftEntry = {};
    for (const field of LAUNCHER_MODEL_FIELDS) {
      if (Object.prototype.hasOwnProperty.call(fields, field) && fields[field] !== undefined) {
        entry[field] = fields[field];
      }
    }
    launchers[name] = entry;
  }
  const listedTierNames: string[] = [];
  const tiers: TierDraftEntry[] = Object.entries(profile.tiers ?? {})
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([name, tier], index) => {
      const hasListedCandidates = !!tier.candidates && tier.candidates.length > 0;
      if (hasListedCandidates) listedTierNames.push(name);
      const sourceCandidates: ModelProfileTierCandidate[] =
        hasListedCandidates
          ? tier.candidates!
          : [
              {
                launcher: tier.launcher ?? "",
                ...(tier.runtime ? { runtime: tier.runtime } : {}),
                ...(tier.channel ? { channel: tier.channel } : {}),
                model: tier.model,
                effort: tier.effort,
                permission_mode: tier.permission_mode,
                speed: tier.speed,
              },
            ];
      return {
        key: `tier-${index}-${name}`,
        originalName: name,
        name,
        description: tier.description ?? "",
        aliases: tier.aliases?.length ? [...tier.aliases] : undefined,
        candidates: sourceCandidates.map((c, ci) => ({
          key: `tier-${index}-${name}-cand-${ci}`,
          originalModel: c.model ?? "",
          launcher: c.launcher ?? "",
          model: c.model ?? "",
          effort: c.effort ?? "",
          permission_mode: c.permission_mode ?? "",
          speed: c.speed ?? "",
          ...(c.runtime ? { runtime: c.runtime } : {}),
          ...(c.channel ? { channel: c.channel } : {}),
        })),
      };
    });
  return {
    name: profile.name ?? "",
    description: profile.description ?? "",
    purposes,
    launchers,
    tiers,
    listedTierNames,
  };
}

function runeLength(value: string): number {
  return [...value].length;
}

function validProfileValue(value: string): boolean {
  return value === value.trim() && !/[\r\n\t\0]/.test(value) && new TextEncoder().encode(value).length <= MAX_PROFILE_VALUE;
}

const VALID_SPEEDS = new Set(["", "standard", "fast", "ultrafast"]);
const VALID_PERMISSION_MODES = new Set<string>(PERMISSION_MODES.map((m) => m.id));

/** Client-side checks mirroring server/internal/assistant/model_profiles_edit.go so users see clear inline reasons before saving. */
export function profileDraftProblems(draft: ProfileDraft, launcherKinds?: Readonly<Record<string, "launcher" | "api" | undefined>>): string[] {
  const problems: string[] = [];
  const name = draft.name.trim();
  if (!name || runeLength(name) > MAX_PROFILE_NAME || /[\r\n\0]/.test(name)) {
    problems.push(t("settings-modelprofiles.err_name", { max: MAX_PROFILE_NAME }));
  }
  const desc = draft.description.trim();
  if (runeLength(desc) > MAX_PROFILE_DESCRIPTION || desc.includes("\0")) {
    problems.push(t("settings-modelprofiles.err_description", { max: MAX_PROFILE_DESCRIPTION }));
  }
  for (const [id, p] of Object.entries(draft.purposes)) {
    if (!p.included) continue;
    for (const [field, raw] of [["provider", p.provider], ["model", p.model]] as const) {
      const value = raw.trim();
      if (value && !validProfileValue(value)) {
        problems.push(t("settings-modelprofiles.err_value", { where: id, field }));
      }
    }
    const permissionMode = p.permission_mode.trim();
    if (permissionMode && (!validProfileValue(permissionMode) || !VALID_PERMISSION_MODES.has(permissionMode))) {
      problems.push(t("settings-modelprofiles.err_value", { where: id, field: "permission_mode" }));
    }
  }
  for (const [launcherName, fields] of Object.entries(draft.launchers)) {
    const isApi = launcherKinds?.[launcherName] === "api";
    for (const field of LAUNCHER_MODEL_FIELDS) {
      const raw = fields[field];
      if (raw === undefined) continue;
      const value = raw.trim();
      if (field === "model" && !value) {
        problems.push(t("settings-modelprofiles.err_launcher_model", { launcher: launcherName }));
        continue;
      }
      if (!validProfileValue(value)) {
        problems.push(t("settings-modelprofiles.err_value", { where: launcherName, field }));
        continue;
      }
      if (field === "effort" && value && !EFFORT_LEVELS.includes(value)) {
        problems.push(t("settings-modelprofiles.err_effort", { where: launcherName, effort: value }));
      }
      if (isApi && field !== "model" && value) {
        problems.push(t("settings-modelprofiles.err_launcher_api", { launcher: launcherName, field }));
      }
    }
  }
  if (draft.tiers.length > MAX_MODEL_TIERS) {
    problems.push(t("settings-modelprofiles.err_tiers_limit", { max: MAX_MODEL_TIERS }));
  }
  const seenTiers = new Set<string>();
  for (const tier of draft.tiers) {
    const tierName = tier.name.trim();
    const tierLabel = tierName || "?";
    if (!TIER_NAME_RE.test(tierName)) {
      problems.push(t("settings-modelprofiles.err_tier_name", { tier: tierLabel }));
    } else if (seenTiers.has(tierName)) {
      problems.push(t("settings-modelprofiles.err_tier_duplicate", { tier: tierName }));
    } else {
      seenTiers.add(tierName);
    }
    if (tier.candidates.length === 0) {
      problems.push(t("settings-modelprofiles.err_tier_candidates_empty", { tier: tierLabel }));
    } else if (tier.candidates.length > MAX_TIER_CANDIDATES) {
      problems.push(t("settings-modelprofiles.err_tier_candidates_limit", { tier: tierLabel, max: MAX_TIER_CANDIDATES }));
    }
    tier.candidates.forEach((cand, index) => {
      const where = t("settings-modelprofiles.tier_candidate_where", { tier: tierLabel, index: index + 1 });
      const launcher = cand.launcher.trim();
      // a candidate given by its runtime and channel alone gets its launcher alias from the hub on save
      if (!launcher && !cand.runtime?.trim() && !cand.channel?.trim()) {
        problems.push(t("settings-modelprofiles.err_tier_candidate_launcher", { tier: tierLabel, index: index + 1 }));
      } else if (launcher && (!validProfileValue(launcher) || launcher.includes(" "))) {
        problems.push(t("settings-modelprofiles.err_value", { where, field: "launcher" }));
      }
      const model = cand.model.trim();
      if (model) {
        const unchangedLegacy = tier.originalName === tierName && cand.originalModel === model;
        if (!validProfileValue(model) || model.includes(" ") || (!unchangedLegacy && !validModelId(model))) {
          problems.push(t("settings-modelprofiles.err_tier_candidate_model", { tier: tierLabel, index: index + 1, model }));
        }
      }
      const effort = cand.effort.trim();
      if (effort && !EFFORT_LEVELS.includes(effort)) {
        problems.push(t("settings-modelprofiles.err_effort", { where, effort }));
      }
      const permissionMode = cand.permission_mode.trim();
      if (permissionMode && (!validProfileValue(permissionMode) || !VALID_PERMISSION_MODES.has(permissionMode))) {
        problems.push(t("settings-modelprofiles.err_value", { where, field: "permission_mode" }));
      }
      const speed = cand.speed.trim();
      if (!VALID_SPEEDS.has(speed)) {
        problems.push(t("settings-modelprofiles.err_tier_candidate_speed", { tier: tierLabel, index: index + 1, speed }));
      }
    });
    const tierDesc = tier.description.trim();
    if (runeLength(tierDesc) > MAX_TIER_DESCRIPTION || /[\r\n\0]/.test(tierDesc)) {
      problems.push(t("settings-modelprofiles.err_tier_description", { tier: tierLabel, max: MAX_TIER_DESCRIPTION }));
    }
  }
  return problems;
}

/** Converts the editor draft into the PUT /api/v1/model-profiles/{id} body. Never emits credentials or config_dir. */
export function draftToProfileInput(draft: ProfileDraft, revision: number): ModelProfileSaveInput {
  const purposes: Record<string, ModelBinding> = {};
  for (const [id, entry] of Object.entries(draft.purposes)) {
    if (!entry.included) continue;
    const binding: ModelBinding = {};
    const provider = entry.provider.trim();
    const model = entry.model.trim();
    const effort = entry.effort.trim();
    const permissionMode = entry.permission_mode.trim();
    if (provider) binding.provider = provider;
    if (model) binding.model = model;
    if (effort) binding.effort = effort;
    if (permissionMode) binding.permission_mode = permissionMode;
    purposes[id] = binding;
  }
  const launchers: Record<string, ModelProfileLauncherModels> = {};
  for (const [name, fields] of Object.entries(draft.launchers)) {
    const out: ModelProfileLauncherModels = {};
    let any = false;
    for (const field of LAUNCHER_MODEL_FIELDS) {
      if (fields[field] === undefined) continue;
      const value = fields[field]!.trim();
      if (field === "model" && !value) continue;
      out[field] = value;
      any = true;
    }
    if (any) launchers[name] = out;
  }
  const listedTiers = new Set(draft.listedTierNames ?? []);
  const tiers: Record<string, ModelProfileTier> = {};
  for (const entry of draft.tiers) {
    const tierName = entry.name.trim();
    if (!tierName) continue;
    const candidates: ModelProfileTierCandidate[] = entry.candidates.map((cand) => {
      const item: ModelProfileTierCandidate = { launcher: cand.launcher.trim() };
      const runtime = cand.runtime?.trim();
      const channel = cand.channel?.trim();
      if (runtime) item.runtime = runtime;
      if (channel) item.channel = channel;
      const model = cand.model.trim();
      const effort = cand.effort.trim();
      const permissionMode = cand.permission_mode.trim();
      const speed = cand.speed.trim();
      if (model) item.model = model;
      if (effort) item.effort = effort;
      if (permissionMode) item.permission_mode = permissionMode;
      // Omit default/standard speed so older hubs using strict JSON decoding accept saves without newer optional fields.
      if (speed && speed !== "standard") item.speed = speed;
      return item;
    });
    const primary = candidates[0] ?? { launcher: "" };
    const out: ModelProfileTier = { launcher: primary.launcher };
    if (primary.runtime) out.runtime = primary.runtime;
    if (primary.channel) out.channel = primary.channel;
    if (primary.model) out.model = primary.model;
    if (primary.effort) out.effort = primary.effort;
    if (primary.permission_mode) out.permission_mode = primary.permission_mode;
    if (primary.speed) out.speed = primary.speed;
    const description = entry.description.trim();
    if (description) out.description = description;
    // State `candidates` when there is more than one or the submitted tier name was listed with candidates by the hub,
    // so cutting a tier down to one (including delete-and-recreate or name swaps) sends `[the one]` rather than the
    // flat-only shape that causes the hub to keep stored candidates. Never emit `[]`.
    if (candidates.length > 1 || (candidates.length === 1 && listedTiers.has(tierName))) {
      out.candidates = candidates;
    }
    if (entry.aliases && entry.aliases.length > 0) out.aliases = [...entry.aliases];
    tiers[tierName] = out;
  }
  const out: ModelProfileSaveInput = {
    revision,
    name: draft.name.trim(),
    description: draft.description.trim(),
  };
  if (Object.keys(purposes).length > 0) out.purposes = purposes;
  if (Object.keys(launchers).length > 0) out.launchers = launchers;
  if (Object.keys(tiers).length > 0) out.tiers = tiers;
  return out;
}

export function isProfileDraftDirty(draft: ProfileDraft, original: ProfileDraft): boolean {
  return JSON.stringify(draftToProfileInput(draft, 0)) !== JSON.stringify(draftToProfileInput(original, 0));
}
