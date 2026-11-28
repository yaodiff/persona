// Settings-only presentation stays out of the shared API module and first-screen catalog.
import { t } from "../i18n";
import { SUPERVISE_PURPOSE } from "./background-supervisor";
import { api, enc } from "./api";
import { MANAGER_HEADER } from "./manager-choice";
import type { CandidateUpdate } from "./model-candidates";
import { modelSummary, providerAgent, providerName, RUNTIMES, runtimeLabel, type ModelBinding, type ModelConfigurationBody, type ModelDraft, type ModelProvider, type ModelPurpose, type ModelPurposeBody } from "./model-config";
import { listedModeOption, PERMISSION_MODES, type ModeOption } from "./session-actions";
import type { Machine } from "./types";

/** Older hubs omit launch; keep their known launcher purposes readable without using inheritance-group names. */
export function purposeLaunch(p: ModelPurpose): ModelPurpose["launch"] {
  if (p.kind !== "launcher") return undefined;
  if (p.launch) return p.launch;
  if (p.id === "inbox.agent" || p.id === "lark.agent") return "headless";
  return [p.id, p.parent].some((id) => id === "assistant" || id === "schedule" || id?.startsWith("task.")) ? "headless" : "engine";
}

function executionLabel(form: string): string {
  return form === "api" ? t("settings.direct_api") : form === "plugin" ? t("settings-nav.tab_extensions") : t(`settings-modelconfiguration.${form}`);
}

/** Whether a launcher provider on an API purpose routes via model.run (CLI) rather than an HTTP Messages gateway.
 *  A Persona-managed launcher with an empty or "messages" API always uses HTTP Messages regardless of has_key; older
 *  hubs that omit `source` fall back to `has_key` only when `api` is omitted. */
export function isModelRunLauncher(provider?: ModelProvider): boolean {
  if (provider?.kind !== "launcher") return false;
  if (provider.api === "messages") return false;
  if (provider.source === "persona") return !!provider.api;
  return provider.source !== undefined || !!provider.api || !provider.has_key;
}

export function executionForm(p: ModelPurpose, providers: ModelProvider[] = [], machines: Machine[] = []): { label: string; backend: string } {
  const provider = providers.find((v) => v.id === p.effective?.provider);
  const launch = purposeLaunch(p);
  const agent = providerAgent(provider, p.effective?.provider);
  let backend: string;
  if (p.kind === "plugin") backend = providerName(p.effective?.provider, providers) ?? "";
  // A launcher picked for an API purpose runs its CLI once per request on the main chat's computer (model.run), unless
  // it is a managed one with a Messages gateway, called over HTTP (#5823, #12027).
  else if (p.kind === "api" && isModelRunLauncher(provider)) {
    backend = agent === "codex" ? "codex exec" : agent === "agy" ? "agy -p" : "claude -p";
  }
  else if (p.kind === "api") backend = { messages: "Messages", responses: "Responses", chat: "Chat" }[provider?.api || "messages"];
  else if (launch === "headless") {
    backend = agent === "codex" ? "codex app-server"
      : agent === "claude" ? (p.effective?.runtime === "agentsdk" ? "Agent SDK" : "claude -p")
      : runtimeLabel("cli", agent);
  } else {
    const machine = p.machine_id ? machines.find((m) => m.id === p.machine_id) : undefined;
    backend = `${machine ? machine.engine_default || machine.engine || "herdr" : t("lib-agent-groups.by_machine")} · ${agent}`;
  }
  return { label: executionLabel(launch || p.kind), backend };
}

/** What a purpose is for, in a sentence under its name: the hub's (`description`, #5086, in the request's language),
 *  else this page's own for the background supervisor (#5082), which a hub before #5086 sends without one. Nothing for
 *  any other purpose the hub describes nothing for. */
export function purposeDescription(p: Pick<ModelPurpose, "id" | "description">): string {
  return p.description?.trim() || (p.id === SUPERVISE_PURPOSE ? t("settings-modelconfiguration.supervise_description") : "");
}

/** Whether the hub's catalog lists purpose `id` itself (the background supervisor's since #5806): then the page asks
 *  nothing more for it. */
export function catalogLists(catalog: ModelConfigurationBody, id: string): boolean {
  return catalog.purposes.some((p) => p.id === id);
}

/** The catalog with purpose `id` right after `after` (the background supervisor's after Main chat, which it follows;
 *  #5082, #5806, docs/assistant/model-config.md "The background supervisor in the catalog"). A hub since #5806 lists
 *  the row last among its built-in purposes, so the apps that append their own copy keep their order: here it is
 *  moved up to its parent. A hub before that leaves it out and answers it by its own id: `fetched` is that answer, put
 *  in. Unchanged when there is no such row (an older hub still), when it is in place already, or without an `after` row. */
export function withPurposeAfter(catalog: ModelConfigurationBody, id: string, after: string, fetched?: ModelPurpose): ModelConfigurationBody {
  const at = catalog.purposes.findIndex((p) => p.id === id);
  const row = at >= 0 ? catalog.purposes[at] : fetched?.id === id ? fetched : undefined;
  const anchor = catalog.purposes.findIndex((p) => p.id === after);
  if (!row || anchor < 0 || at === anchor + 1) return catalog;
  const rest = catalog.purposes.filter((p) => p.id !== id);
  const to = rest.findIndex((p) => p.id === after) + 1;
  return { ...catalog, purposes: [...rest.slice(0, to), row, ...rest.slice(to)] };
}

/** The group of this page a purpose is listed in: by how it runs, with the extensions' own purposes under Extensions. */
function groupKey(p: ModelPurpose): string {
  return p.id.startsWith("extension.") || p.kind === "plugin" ? "plugin" : purposeLaunch(p) || "api";
}

export function purposeGroups(purposes: ModelPurpose[], query: string, providers: ModelProvider[] = []): [string, ModelPurpose[]][] {
  const q = query.trim().toLocaleLowerCase();
  return [[t("sidebar.persona_chats"), "headless"], [t("settings-modelconfiguration.agents"), "engine"], [t("settings-modelconfiguration.processing"), "api"], ["", "plugin"]].map(([name, key]): [string, ModelPurpose[]] => {
    const group = [name, executionLabel(key)].filter(Boolean).join(" · ");
    return [group, purposes.filter((p) =>
      groupKey(p) === key
      && (!q || [p.name, p.group, p.id, group, modelSummary(p, providers)].some((s) => s.toLocaleLowerCase().includes(q))))];
  }).filter(([, rows]) => rows.length);
}

/** The purpose has a field of its own to write (an older hub sends a read-only use with `fields: []`). */
function ownFields(p: ModelPurpose): boolean {
  return (p.fields ?? []).some((field) => field === "provider" || field === "model" || field === "effort" || field === "permission_mode");
}

/** The category `p` is a use set apart from, told by the hub's data, not by id (#7980, docs/assistant/model-config.md
 *  "Meeting minutes and the in-meeting conversation"): a purpose with fields of its own, no `uses` (it is a use itself),
 *  a `category_id` other than its id, and a `parent` that is a category of `purposes` (its `category_id` its own id)
 *  listed in the same group of this page. Main chat for the background supervisor and the In-meeting conversation,
 *  Information processing for Meeting minutes. None for the categories and their read-only uses, nor for the uses of
 *  Agents (AI twin, Lark /agent: they run in another group than Agents, with words of their own, `agentsOverride`) or
 *  an extension's purpose (under Extensions). */
export function setApartFrom(p: ModelPurpose, purposes: ModelPurpose[]): ModelPurpose | undefined {
  if (!ownFields(p) || p.uses?.length || !p.category_id || p.category_id === p.id || !p.parent) return undefined;
  const parent = purposes.find((c) => c.id === p.parent && c.category_id === c.id);
  return parent && groupKey(parent) === groupKey(p) ? parent : undefined;
}

/** The category a use set apart says it follows, by that category's name in the catalog: **Follows Main chat** /
 *  **Follow Information processing** (#7980). Not the background supervisor, whose row and editor keep the words they
 *  had before (the hub's `inherited_from`, and Inherit). */
export function followedCategory(p: ModelPurpose, purposes: ModelPurpose[]): ModelPurpose | undefined {
  return p.id === SUPERVISE_PURPOSE ? undefined : setApartFrom(p, purposes);
}

/** The catalog with each use set apart right after its category, behind those already there (#7980): Main chat, the
 *  background supervisor, the In-meeting conversation; Information processing, Meeting minutes. The hub lists them
 *  last among its own purposes so the rows installed apps draw keep their order (the reason the background supervisor
 *  went last, #5806); here each moves up to what it follows, in the hub's order. The catalog itself when nothing moves,
 *  as against a hub before #7980. */
export function withUsesAfterCategories(catalog: ModelConfigurationBody): ModelConfigurationBody {
  const uses = new Map<ModelPurpose, ModelPurpose[]>(); // each category's uses set apart, in the hub's order
  for (const p of catalog.purposes) {
    const parent = setApartFrom(p, catalog.purposes);
    if (parent) uses.set(parent, [...(uses.get(parent) ?? []), p]);
  }
  if (!uses.size) return catalog;
  const moved = new Set([...uses.values()].flat());
  const purposes = catalog.purposes.flatMap((p) => moved.has(p) ? [] : [p, ...(uses.get(p) ?? [])]);
  return purposes.every((p, i) => p === catalog.purposes[i]) ? catalog : { ...catalog, purposes };
}

/** The In-meeting conversation runs only on Claude Code (#7980): its line in the editor says so. A fact about that
 *  purpose, so this page knows it by the id; the hub sends no field for it (its `provider_ids` hold only Claude Code
 *  launchers and Messages connections, without saying why). */
export const MEETING_ROOM_PURPOSE = "meeting.room";
export const claudeCodeOnly = (p: Pick<ModelPurpose, "id">): boolean => p.id === MEETING_ROOM_PURPOSE;

// What only Model settings does with a purpose (its editor's draft, what it sends, the catalog): here, so the first
// screen's lib/model-config carries none of it (startup-preload's budget).

/** "Runtime"'s options: only the ones the hub offers for this purpose (and this client knows). */
export function runtimeChoices(p: ModelPurpose, agent?: string): { id: string; label: string }[] {
  if (!p.runtimes?.length) return [];
  if (agent && agent !== "claude") return [{ id: "cli", label: runtimeLabel("cli", agent) }];
  return RUNTIMES.filter((r) => p.runtimes?.includes(r.id));
}

/** The profiles a main chat's new conversations can start in (#4633), in the order the choice offers them. */
const CONFIG_DIRS = ["private", "legacy"] as const;

/** "New chats' Claude profile"'s options: the ones the hub offers (and this client knows), for Claude Code only. */
export function configDirChoices(p: ModelPurpose, agent?: string): { id: string; label: string }[] {
  if (!p.config_dirs?.length || (agent && agent !== "claude")) return [];
  return CONFIG_DIRS.filter((id) => p.config_dirs?.includes(id)).map((id) => ({ id, label: configDirLabel(id) }));
}

export function configDirLabel(id: string | undefined): string {
  return id === "legacy" ? t("settings-modelconfiguration.config_dir_legacy") : t("settings-modelconfiguration.config_dir_private");
}

const NO_PERMISSION_MODE_AGENTS = new Set(["omp", "traex", "pi", "cursor"]);

/** Permission mode options supported by a launcher (`chosen` / `agent`); empty for agents without permission modes. */
export function permissionModesForLauncher(chosen: ModelProvider | undefined, agent: string): ModeOption[] {
  if (chosen?.permission_modes?.length) {
    if (chosen.permission_modes.length < 2) return [];
    return agent === "agy"
      ? PERMISSION_MODES.filter((m) => chosen.permission_modes!.includes(m.id))
      : chosen.permission_modes.map(listedModeOption);
  }
  if (NO_PERMISSION_MODE_AGENTS.has(agent)) return [];
  return PERMISSION_MODES;
}

/** Permission options for launcher-backed purposes. Inbox TRAE uses the app-server policies. */
export function permissionModesForProvider(p: ModelPurpose, chosen: ModelProvider | undefined, agent: string): ModeOption[] {
  if (p.kind !== "launcher" || !supportsField(p, "permission_mode")) return [];
  if (p.id === "inbox.agent" && agent === "traex") {
    return PERMISSION_MODES.filter((mode) => mode.id !== "plan");
  }
  return permissionModesForLauncher(chosen, agent);
}

export const modelConfiguration = (manager?: string) => api<ModelConfigurationBody>("/model-config", { headers: manager ? { [MANAGER_HEADER]: manager } : undefined });
export function draftFor(p: ModelPurpose): ModelDraft {
  const runtime = runtimeChoices(p).length ? p.override?.runtime || p.effective?.runtime || "cli" : "";
  const draft: ModelDraft = {
    provider: p.override?.provider ?? "",
    model: p.override?.model ?? "",
    effort: p.override?.effort ?? "",
    runtime,
  };
  if (p.kind === "launcher" && (supportsField(p, "permission_mode") || p.override?.permission_mode)) {
    draft.permission_mode = p.override?.permission_mode ?? "";
  }
  // Asked without the agent on purpose: seeded whatever the purpose runs on, so an editor opened on one without profiles
  // (Codex), which hides the choice, keeps the saved one for Claude Code and for its save (#4633).
  if (configDirChoices(p).length) draft.config_dir = p.override?.config_dir || p.effective?.config_dir || "private";
  return draft;
}

export function chooseProvider(draft: ModelDraft, provider: string): ModelDraft {
  if (draft.provider === provider) return draft;
  const next: ModelDraft = { ...draft, provider, model: "", effort: "" };
  if ("permission_mode" in draft) next.permission_mode = "";
  return next;
}

export function supportsField(p: ModelPurpose, field: Exclude<keyof ModelDraft, "runtime" | "config_dir">): boolean {
  return (p.fields ?? ["provider", "model"]).includes(field);
}

/** A use of Agents the hub lets the user set apart (Lark /agent, the AI twin): it says so with fields of its own, and
 * follows Agents while they are empty. Other uses of a category have no fields and stay read-only. */
export function agentsOverride(p: ModelPurpose): boolean {
  return p.parent === "agent.new" && ownFields(p);
}

/** The binding says something of its own: the row says **Separately configured**. */
export function overridden(b: ModelBinding): boolean {
  return !!(b.provider || b.model || b.effort || b.permission_mode);
}

/** A use with a record of its own under a category, told by its own data, whatever group this page lists it in and
 *  whether the catalog loaded at all: fields to write and a `category_id` other than its id (the AI twin, Lark /agent,
 *  the background supervisor, a task's own, the two meeting uses of #7980, an extension's purpose). Empty, it follows
 *  that category. */
export function ownRecord(p: ModelPurpose): boolean {
  return ownFields(p) && !!p.category_id && p.category_id !== p.id;
}

/** The category such a use follows (`ownRecord`), as the catalog has it: its `parent` when that is a category there.
 *  Its binding is what the use runs on once its record is empty again. */
export function parentCategory(p: ModelPurpose, purposes: ModelPurpose[]): ModelPurpose | undefined {
  return ownRecord(p) && p.parent ? purposes.find((c) => c.id === p.parent && c.category_id === c.id) : undefined;
}

/** A provider that took a thinking effort before Chat support (#8779): a launcher, or a direct API connection on Messages
 *  or Responses (one that says no protocol is a Messages one). Unknown: the editor does not hold it back. */
function takesEffortProvider(v?: ModelProvider): boolean {
  return v?.kind !== "api" || v.api === "responses" || v.api === "messages" || !v.api;
}

/** Whether the editor offers a thinking effort for a draft that runs on `chosen`. A current hub lists `effort` for Chat
 *  and marks the provider `chat_effort`; without that mark, an older hub's field for Messages or Responses does not
 *  admit a different Chat provider. An effort field already supplied for any effective Chat provider remains valid.
 *  `chatEffortKnown` is another listed provider's current-hub mark: it restores Messages or Responses when the previous
 *  effective provider is missing. On an older hub that omits effort for Chat, picking Messages or Responses offers it
 *  again (#7980). */
export function effortOffered(p: ModelPurpose, chosen: ModelProvider | undefined, effective: ModelProvider | undefined, followsAgents = false, chatEffortKnown = false): boolean {
  const explicit = supportsField(p, "effort");
  const markedChat = chosen?.kind === "api" && chosen.api === "chat" && chosen.chat_effort === true;
  const field = explicit || (p.kind === "api" && !!p.fields?.includes("provider")
    && (markedChat || chatEffortKnown || (!!effective && !takesEffortProvider(effective))));
  const chat = chosen?.kind === "api" && chosen.api === "chat"
    && (chosen.chat_effort === true || (explicit && effective?.kind === "api" && effective.api === "chat"));
  return field && (followsAgents || chat || takesEffortProvider(chosen));
}

/** What Save sends: the revision and each field the purpose takes, explicit `null` for an empty one. `effort` says
 *  whether effort is among them, by default when the hub lists it; the editor passes `effortOffered` too. */
export function modelPatch(p: ModelPurpose, draft: ModelDraft, effort = supportsField(p, "effort"), permissionMode = supportsField(p, "permission_mode")): Record<string, string | number | null> {
  const patch: Record<string, string | number | null> = { revision: p.revision };
  for (const field of ["provider", "model", "effort"] as const) {
    if (field === "effort" ? effort : supportsField(p, field)) patch[field] = draft[field].trim() || null;
  }
  if (p.kind === "launcher" && (permissionMode || (supportsField(p, "permission_mode") && p.override?.permission_mode))) {
    patch.permission_mode = (draft.permission_mode ?? "").trim() || null;
  }
  if (runtimeChoices(p).length && draft.runtime) patch.runtime = draft.runtime;
  if (configDirChoices(p).length && draft.config_dir) patch.config_dir = draft.config_dir;
  return patch;
}

/** What the editor's reset sends (Inherit, Follow Agents, Follow <the category>): the revision, `null` for each field
 *  the purpose takes, and the runtime and the main chat's profile it keeps (#4633). A use with a record of its own
 *  (`ownRecord`) goes back to following its category with explicit nulls for all fields, whatever fields the provider
 *  in effect takes now (#7980), so an effort or permission mode saved on an earlier provider goes too. */
export function resetPatch(p: ModelPurpose, runtime = "", configDir?: string): Record<string, string | number | null> {
  const patch = modelPatch(p, { provider: "", model: "", effort: "", permission_mode: "", runtime, config_dir: configDir });
  if (!ownRecord(p)) return patch;
  const cleared: Record<string, string | number | null> = { ...patch, provider: null, model: null, effort: null };
  if (p.kind === "launcher" && supportsField(p, "permission_mode")) cleared.permission_mode = null;
  return cleared;
}

export function saveModelPurpose(p: ModelPurpose, patch: Record<string, string | number | null | CandidateUpdate>, manager?: string): Promise<ModelPurposeBody> {
  return api(`/model-config/purposes/${enc(p.id)}`, { method: "PATCH", json: patch, headers: manager ? { [MANAGER_HEADER]: manager } : undefined });
}

export function providersFor(p: ModelPurpose, providers: ModelProvider[]): ModelProvider[] {
  const candidates = providers.filter((provider) =>
    p.provider_ids ? p.provider_ids.includes(provider.id) : provider.kind === p.kind,
  );
  return candidates.filter((provider) => !provider.hidden || provider.id === p.override?.provider || provider.id === p.effective?.provider);
}
