import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { t } from "../../../i18n";
import { errorText, isAbort } from "../../../lib/api";
import { ConfigurationError } from "../../../lib/configuration/contract";
import { opOf, operations, operationsListed, watchOperations } from "../../../lib/extension-operations";
import {
  changed, checkError, clearedSecrets, compact, configError, draftEdits, extensionDetail, isDirty, isEntryList,
  fieldPath, numberProblems, prefillDraft, readNumber, rebaseDraft, same, setAt, setExtensionConfig,
  setExtensionConfigPreservingEntries, valueAt, type HubExtension, type HubExtensionDetail, type Json, type JsonObject,
} from "../../../lib/extensions";
import { defaultManager, pinManagerAt } from "../../../lib/manager-choice";
import { currentAssistant, ownedAssistant, type ScopedAssistantClient } from "../../../lib/scoped-assistant-api";
import { toast } from "../../../ui/atoms";
import { abortedConfiguration, ResourceConfiguration, resourceConfigurations } from "./resourceConfiguration";
import { useResourceConfiguration } from "./useResourceConfiguration";
import { useScopedAssistant } from "./useScopedAssistant";
import { useExtensionAuthSync } from "./useExtensionAuthSync";

export type Typed = Record<string, { text: string; integer: boolean }>;
type Draft = { base: JsonObject; draft: JsonObject; typed: Typed; dirty: boolean; editedDuringSave?: boolean };
type ExtensionResource = ResourceConfiguration<HubExtensionDetail, Draft>;
export type ExtensionMutation = (label: "management" | "secret", send: (client: ScopedAssistantClient) => Promise<HubExtensionDetail>) => Promise<HubExtensionDetail>;

const NO_CONFIG: JsonObject = {};
const NO_TYPED: Typed = {};
const configOf = (detail: HubExtensionDetail) => detail.config ?? NO_CONFIG;
const requiredLists = (detail: HubExtensionDetail) => new Set(detail.form?.fields?.filter((f) => isEntryList(f) && f.required).map((f) => f.name));
const canEdit = (d: HubExtensionDetail) => !d.read_only && d.source !== "dev" && d.source !== "hub.json";

function keepDraft(remote: HubExtensionDetail, draft: JsonObject, typed: Typed, editedDuringSave = false): Draft | undefined {
  const dirty = isDirty(draft, remote.config, requiredLists(remote)) || Object.keys(numberProblems(typed)).length > 0;
  if (dirty || editedDuringSave || Object.keys(typed).length) return { base: configOf(remote), draft, typed, dirty, editedDuringSave };
}

function reconcile(draft: Draft, remote: HubExtensionDetail) {
  if (same(draft.base, configOf(remote))) return { draft };
  const required = requiredLists(remote);
  const base = compact(draft.base, required);
  const fresh = compact(configOf(remote), required);
  const overlapping = draftEdits(draft.draft, base, required).some(({ path, value }) => moved(base, fresh, path, value));
  const next = rebaseDraft(draft, configOf(remote), required);
  return {
    draft: keepDraft(remote, next.draft, next.typed, draft.editedDuringSave),
    ...(overlapping && { conflict: saveConflict() }),
  };
}

// The list's unsaved badges and the browser's leave prompt follow the same drafts as both editors.
const unsaved = new Map<string, ExtensionResource>();
const listeners = new Set<() => void>();
let revision = 0;
const subscribe = (listener: () => void) => {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
};
const getRevision = () => revision;
export const extensionDraftKey = (id: string, manager = defaultManager()) => JSON.stringify([manager, id]);

function warn(event: BeforeUnloadEvent) {
  event.preventDefault();
  event.returnValue = true;
}

const resources = resourceConfigurations((id, client) => {
  const key = extensionDraftKey(id, client.managerId);
  const resource: ExtensionResource = new ResourceConfiguration(id, client, {
    read: () => extensionDetail(id, undefined, client),
    rebase: reconcile,
    isDirty: (draft) => draft.dirty,
    draftChanged: (dirty) => {
      if (dirty) unsaved.set(key, resource);
      else unsaved.delete(key);
      if (unsaved.size) window.addEventListener("beforeunload", warn);
      else window.removeEventListener("beforeunload", warn);
      revision++;
      listeners.forEach((listener) => listener());
    },
  });
  return resource;
});

export function useUnsavedExtensions(): ReadonlyMap<string, ExtensionResource> {
  useSyncExternalStore(subscribe, getRevision);
  return unsaved;
}

/** A merged list can omit an offline owner. Only discard drafts for owners whose list answered. */
export function dropGoneExtensionDrafts(list: HubExtension[]) {
  const owners = new Set(list.map((extension) => extension.manager ?? defaultManager()));
  const present = new Set(list.map((extension) => extensionDraftKey(extension.id, extension.manager)));
  for (const [key, resource] of unsaved) {
    if (owners.has(resource.client.managerId) && !present.has(key) && !resource.snapshot().saving) {
      resource.edit(() => undefined);
    }
  }
}

export function useExtensionBinding(id: string, manager?: string) {
  return useScopedAssistant(manager === undefined ? currentAssistant : ownedAssistant(manager), "extension", id);
}

export type ExtensionBinding = ReturnType<typeof useExtensionBinding>;
const prefills = new WeakMap<ExtensionResource, string>();

function saveConflict() {
  return new ConfigurationError("configuration_conflict", t("settings-configuration.error_configuration_conflict"));
}

function moved(base: JsonObject, fresh: JsonObject, path: string[], value: Json | undefined) {
  const current = valueAt(fresh, path);
  return !same(current, valueAt(base, path)) && !same(current, value);
}

function prepareWrite(resource: ExtensionResource, remote: HubExtensionDetail, submitted: Draft, immediate?: { path: string[]; previous: Json | undefined; value: Json | undefined }) {
  if (!canEdit(remote)) throw new ConfigurationError("configuration_forbidden", t("settings-configuration.error_configuration_forbidden"));
  const required = requiredLists(remote);
  const base = compact(submitted.base, required);
  const fresh = compact(configOf(remote), required);
  const edits = immediate ? [immediate] : draftEdits(submitted.draft, base, required);
  for (const { path, value } of edits) {
    // An earlier queued toggle can have applied the previous local value of this field.
    if (moved(base, fresh, path, value) && !(immediate && same(valueAt(fresh, path), immediate.previous))) {
      throw saveConflict();
    }
  }
  const applied = edits.reduce((config, { path, value }) => setAt(config, path, value), configOf(remote));
  const payload = compact(applied, required);
  const write = immediate ? setExtensionConfigPreservingEntries : setExtensionConfig;
  return {
    send: () => write(resource.id, payload, resource.client),
    matches: (d: HubExtensionDetail) => {
      const values = compact(configOf(d), required);
      return edits.every(({ path }) => same(valueAt(values, path), valueAt(payload, path)));
    },
    acknowledge: (latest: Draft, next: HubExtensionDetail) => {
      // Remove only what this save submitted. Keep edits made while it was in the queue or on the wire.
      const saved = edits.reduce((base, { path, value }) => setAt(base, path, value), latest.base);
      const kept = rebaseDraft({ ...latest, base: saved }, configOf(next), requiredLists(next));
      return keepDraft(next, kept.draft, kept.typed);
    },
  };
}

/** Shared configuration state for the legacy Extensions page and the unified integration detail. */
export function useExtensionConfiguration(id: string, binding: ExtensionBinding, prefill: JsonObject | undefined, onRemoved?: () => void) {
  const { client, routing } = binding;
  const resource = useMemo(() => resources.get(id, client), [id, client]);
  const active = useRef(false);
  const removed = useRef(onRemoved);
  removed.current = onRemoved;
  useLayoutEffect(() => {
    active.current = true;
    return () => { active.current = false; };
  }, [resource]);

  // Sign-in still has its own transport. Only these paths use the form host's legacy pin.
  useLayoutEffect(() => {
    const endpoint = `/${encodeURIComponent(id)}/-/auth`;
    return pinManagerAt([`/extensions${endpoint}`, `/plugins${endpoint}`], client.managerId);
  }, [id, client]);
  const reconnect = useCallback(() => {
    const state = resource.snapshot();
    if (!state.remote || state.readError || state.unconfirmed) void resource.refresh();
  }, [resource]);
  const state = useResourceConfiguration(resource, routing, reconnect);
  const detail = state.remote;
  const [connectorRefreshKey, setConnectorRefreshKey] = useState(0);
  const authChanged = useExtensionAuthSync({
    id, client, auth: detail?.auth,
    onAuth: (auth) => resource.patchRemote((remote) => ({ ...remote, auth })),
    onSync: () => setConnectorRefreshKey((key) => key + 1),
  });
  const draft = state.draft?.draft ?? (detail ? configOf(detail) : NO_CONFIG);
  const typed = state.draft?.typed ?? NO_TYPED;
  const dirty = state.draft?.dirty ?? false;
  const problems = useMemo(() => numberProblems(typed), [typed]);
  const edits = useMemo(() => detail && state.draft?.dirty
    ? draftEdits(state.draft.draft, state.draft.base, requiredLists(detail)) : [], [detail, state.draft]);
  const pendingCount = edits.filter(({ path }) => !resource.isSavingField(fieldPath(path))).length;
  const saveBarLabel = Object.keys(problems).length ? t("settings-extensions.not_a_number")
    : pendingCount > 0 ? t("settings-extensions.fields_to_save", { count: pendingCount }) : undefined;
  const editable = !!detail && canEdit(detail);
  const error = state.saveError?.label === "config" ? state.saveError.error : null;
  const refused = configError(error);
  const fieldErrors = refused ? { [refused.field]: refused.message } : {};
  const checkFailed = state.unconfirmed ? t("settings-configuration.error_unconfirmed") : error && !refused ? errorText(error) : "";

  const remove = useCallback(() => {
    if (!active.current) return;
    resources.remove(client.key);
    removed.current?.();
  }, [client]);
  useEffect(() => {
    const ended = () => operations().filter((op) => op.extension === id && op.state === "succeeded" && opOf(op, client.managerId));
    const seen = new Set(ended().map((op) => op.id));
    let listed = operationsListed();
    return watchOperations(() => {
      const fresh = ended().filter((op) => !seen.has(op.id));
      fresh.forEach((op) => seen.add(op.id));
      if (!listed && operationsListed()) {
        listed = true;
        return;
      }
      if (fresh[0]?.kind === "uninstall") remove();
      else if (fresh.length) void resource.refresh();
    });
  }, [id, client, resource, remove]);

  useEffect(() => {
    if (!prefill) {
      prefills.delete(resource);
      return;
    }
    const key = JSON.stringify(prefill);
    if (!editable || key === "{}" || prefills.get(resource) === key) return;
    prefills.set(resource, key);
    resource.edit((draft, remote) => keepDraft(remote, prefillDraft(draft?.draft ?? configOf(remote), prefill, remote.form?.fields ?? []), NO_TYPED));
  }, [editable, prefill, resource]);

  const edit = (path: string[], value: Json | undefined) =>
    resource.edit((draft, remote) => keepDraft(remote, setAt(draft?.draft ?? configOf(remote), path, value), draft?.typed ?? NO_TYPED, resource.snapshot().saving === "config"));
  const type = (path: string[], text: string, integer: boolean) => resource.edit((draft, remote) => {
    const parsed = readNumber(text, integer);
    const current = draft?.draft ?? configOf(remote);
    const raw = { ...draft?.typed, [fieldPath(path)]: { text, integer } };
    return keepDraft(remote, parsed.error ? current : setAt(current, path, parsed.value), raw, resource.snapshot().saving === "config");
  });

  const mutate: ExtensionMutation = async (label, send) => {
    if (!active.current) throw abortedConfiguration();
    try {
      const saved = await resource.save({ label, prepare: () => ({ send: () => send(client) }) });
      if (!active.current || !saved) throw abortedConfiguration();
      return saved;
    } catch (error) {
      throw active.current ? error : abortedConfiguration();
    }
  };
  const reportSaveFailure = (error: unknown) => {
    if (clearedSecrets(error).length) {
      changed(null);
      void resource.refresh();
    }
    if (active.current && !isAbort(error) && !configError(error) && !checkError(error) && !(error instanceof ConfigurationError)) {
      toast(t("settings-extensions.not_saved", { error: errorText(error) }), "error");
    }
  };
  const save = async () => {
    const current = resource.snapshot();
    const submitted = current.draft;
    if (!active.current || !current.remote || !canEdit(current.remote) || !submitted?.dirty || current.saving || current.unconfirmed ||
      Object.keys(numberProblems(submitted.typed)).length) return;
    try {
      await resource.save({ label: "config", refreshBefore: true, prepare: (remote) => prepareWrite(resource, remote, submitted) });
      if (active.current) toast(t("settings-extensions.saved_restart"), "ok");
    } catch (error) {
      reportSaveFailure(error);
    }
  };
  const saveNow = async (path: string[], value: Json | undefined) => {
    const { draft, remote, unconfirmed } = resource.snapshot();
    if (!active.current || !remote || !canEdit(remote) || unconfirmed) return;
    const config = configOf(remote);
    const before = draft?.draft ?? config;
    const previous = valueAt(before, path);
    if (same(previous, value)) return;
    edit(path, value);
    const submitted: Draft = { base: config, draft: before, typed: NO_TYPED, dirty: true };
    try {
      await resource.save({
        label: "config", immediateField: fieldPath(path), refreshBefore: true,
        prepare: (remote) => prepareWrite(resource, remote, submitted, { path, previous, value }),
      });
    } catch (error) {
      reportSaveFailure(error);
    }
  };
  const reload = async () => {
    await resource.refresh();
    const error = resource.snapshot().readError;
    if (error) throw error;
  };

  return {
    detail, draft, typed, saveBarLabel, problems, editable, fieldErrors, checkFailed,
    error: state.readError && !isAbort(state.readError) ? errorText(state.readError) : "",
    loading: state.loading, saving: state.saving === "config", toggling: state.saving !== null && state.saving !== "config",
    canSave: editable && dirty && !Object.keys(problems).length && !state.saving && !state.unconfirmed,
    load: resource.refresh, reload, authChanged, connectorRefreshKey, mutate, remove, edit, type, save, saveNow,
    revert: () => resource.edit(() => undefined),
  };
}
