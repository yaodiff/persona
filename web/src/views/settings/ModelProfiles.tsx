import { Suspense, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { ApiError, credentialOwner, errorText, getToken, subscribeCredentialOwner } from "../../lib/api";
import { lazyPage } from "../../lib/lazy-page";
import { onDefaultManager, onManagerChoice, shownManager } from "../../lib/manager-choice";
import { providerName, type ModelProvider, type ModelPurpose } from "../../lib/model-config";
import {
  captureProfileInput,
  clearModelProfiles,
  deactivateModelProfile,
  deleteModelProfile,
  LAUNCHER_MODEL_FIELDS,
  MAX_MODEL_PROFILES,
  MAX_PROFILE_DESCRIPTION,
  MAX_PROFILE_NAME,
  modelProfiles,
  parseProfileChange,
  PROFILE_CATEGORY_IDS,
  profileApplyDrift,
  rollbackModelProfile,
  saveModelProfile,
  switchModelProfile,
  type ModelProfile,
  type ModelProfileSaveInput,
  type ModelProfiles as Profiles,
} from "../../lib/model-profiles";
import { namedLauncher, namesFromProviders, providerIdNote, providerTitle, type Named } from "../../lib/provider-names";
import { effortTitle, modeTitle } from "../../lib/session-actions";
import { onHubEvent } from "../../lib/store";
import { t } from "../../i18n";
import { Button, Dialog, Loading, toast } from "../../ui/atoms";
import { suggestProfileId, validateNewProfile } from "./model-profile-create";
import { Group, Pill, Row, settingsInputClass } from "./SettingsParts";

// Load the interactive profile editor only when the user chooses Edit, keeping Settings' initial chunk light.
const ModelProfileEditor = lazyPage(() => import("./ModelProfileEditor").then((m) => m.ModelProfileEditor));

// A dispatched write can outlive its view and credential. Admit at most one per manager, including across remounts,
// so a late server commit cannot silently undo a later action from this browser.
const pendingWrites = new Set<string>();
const writeListeners = new Set<() => void>();
const subscribeWrites = (listener: () => void) => { writeListeners.add(listener); return () => { writeListeners.delete(listener); }; };
const writesChanged = () => writeListeners.forEach((listener) => listener());
let managerGeneration = 0;
onManagerChoice(() => { managerGeneration++; });
onDefaultManager(() => { managerGeneration++; });
const profileContext = () => JSON.stringify([credentialOwner(), managerGeneration]);
const subscribeProfileContext = (listener: () => void) => {
  const stops = [subscribeCredentialOwner(listener), onManagerChoice(listener), onDefaultManager(listener)];
  return () => stops.forEach((stop) => stop());
};

function purposeLabel(id: string, purposes?: ModelPurpose[]): string {
  if (id === "agent.new") return t("newagent.title");
  const found = purposes?.find((p) => p.id === id);
  if (found?.name) return found.name;
  const fallback: Record<string, string> = {
    assistant: t("settings-modelprofiles.cat_assistant"),
    "agent.new": t("settings-modelprofiles.cat_agent_new"),
    kb: t("settings-modelprofiles.cat_kb"),
    "inbox.triage": t("settings-modelprofiles.cat_inbox_triage"),
    "agent.delegate": t("settings-modelprofiles.cat_agent_delegate"),
    translate: t("settings-modelprofiles.cat_translate"),
  };
  return fallback[id] ?? id;
}

function describeChange(
  change: string,
  profile: ModelProfile | undefined,
  purposes: ModelPurpose[] | undefined,
  providers: ModelProvider[] | undefined,
  names: Named[],
): { key: string; label: string; target?: string } {
  const parsed = parseProfileChange(change, purposes?.map((p) => p.id));
  if (parsed.kind === "purpose") {
    const label = purposeLabel(parsed.id, purposes);
    const binding = profile?.purposes && Object.prototype.hasOwnProperty.call(profile.purposes, parsed.id)
      ? profile.purposes[parsed.id]
      : undefined;
    const target = binding
      ? [
          binding.provider ? (providerName(binding.provider, providers) ?? binding.provider) : t("settings-modelprofiles.purpose_follow_default"),
          binding.model,
          binding.effort && effortTitle(binding.effort),
          binding.permission_mode && modeTitle(binding.permission_mode),
        ].filter(Boolean).join(" · ")
      : undefined;
    return { key: change, label, target };
  }
  if (parsed.kind === "launcher") {
    const l = namedLauncher(parsed.launcher, names);
    const label = `${providerTitle(l)} · ${t(`settings-modelprofiles.field_${parsed.field}`)}`;
    const raw = profile?.launchers?.[parsed.launcher]?.[parsed.field];
    const target = raw === undefined
      ? undefined
      : raw === ""
        ? t("settings-modelprofiles.clear_on_switch")
        : parsed.field === "effort"
          ? effortTitle(raw)
          : raw;
    return { key: change, label, target };
  }
  return { key: change, label: parsed.raw };
}

/** `providers` and `purposes`: the model configuration's, to name launchers/categories cleanly and share picker filtering (#5029). */
export function ModelProfiles({
  manager,
  providers,
  purposes,
  onChanged,
}: {
  manager?: string;
  providers?: ModelProvider[];
  purposes?: ModelPurpose[];
  onChanged: () => void;
}) {
  const context = useSyncExternalStore(subscribeProfileContext, profileContext, profileContext);
  const owner = credentialOwner();
  const target = manager ?? shownManager();
  return (
    <ModelProfilesForOwner
      key={JSON.stringify([target, context])}
      manager={target}
      owner={owner}
      context={context}
      providers={providers}
      purposes={purposes}
      onChanged={onChanged}
    />
  );
}

function ModelProfilesForOwner({
  manager,
  owner,
  context,
  providers,
  purposes,
  onChanged,
}: {
  manager: string;
  owner: string;
  context: string;
  providers?: ModelProvider[];
  purposes?: ModelPurpose[];
  onChanged: () => void;
}) {
  const [data, setData] = useState<Profiles | null>(null);
  const [readError, setReadError] = useState("");
  const [actionError, setActionError] = useState("");
  const [conflict, setConflict] = useState(false);
  const [selected, setSelected] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const [confirmingApply, setConfirmingApply] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);
  const [creating, setCreating] = useState(false);
  const [createName, setCreateName] = useState("");
  const [createId, setCreateId] = useState("");
  const [createIdTouched, setCreateIdTouched] = useState(false);
  const [createDescription, setCreateDescription] = useState("");
  const [confirmingClearAll, setConfirmingClearAll] = useState(false);
  const [editRevision, setEditRevision] = useState(0);
  const [editSyncKey, setEditSyncKey] = useState(0);
  const [busyAction, setBusyAction] = useState<
    "apply" | "rollback" | "save" | "capture" | "capture-apply" | "create" | "deactivate" | "delete" | "clear" | null
  >(null);
  const busy = useSyncExternalStore(subscribeWrites, () => pendingWrites.has(manager), () => false);
  const token = useRef(getToken()).current;
  const mounted = useRef(false);
  const readNumber = useRef(0);
  const readAbort = useRef<AbortController | null>(null);
  const interaction = useRef(0);
  const changed = useRef(onChanged);
  changed.current = onChanged;
  const current = () => mounted.current && profileContext() === context && credentialOwner() === owner;
  const invalidateRead = () => { readNumber.current++; readAbort.current?.abort(); readAbort.current = null; };
  useLayoutEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; invalidateRead(); };
  }, []);
  const load = async () => {
    invalidateRead();
    const number = readNumber.current;
    const edit = interaction.current;
    const controller = new AbortController();
    readAbort.current = controller;
    const isCurrent = () => current() && number === readNumber.current;
    try {
      const result = await modelProfiles(manager, { token, tokenOwner: owner, isAuthContextCurrent: isCurrent, signal: controller.signal });
      if (isCurrent()) {
        setData(result);
        if (edit === interaction.current) setReadError("");
      }
      return result;
    } catch (e) {
      if (isCurrent() && edit === interaction.current && !(e instanceof ApiError && e.status === 404)) setReadError(errorText(e));
      return null;
    } finally {
      if (isCurrent()) readAbort.current = null;
    }
  };
  useEffect(() => {
    void load();
    let wasPending = pendingWrites.has(manager);
    const stopWrites = subscribeWrites(() => {
      const pending = pendingWrites.has(manager);
      // A write from this or a retired view may have committed after our last read.
      if (wasPending && !pending && current()) void load();
      wasPending = pending;
    });
    const stopEvents = onHubEvent("model_profile", () => {
      if (!current()) return;
      // Read after the pending write settles: an event read during it can precede its server commit.
      if (!pendingWrites.has(manager)) void load();
      changed.current();
    });
    return () => { stopWrites(); stopEvents(); invalidateRead(); };
  }, []);

  const apply = async (id?: string) => {
    if (!current() || pendingWrites.has(manager)) return;
    pendingWrites.add(manager);
    setBusyAction(id ? "apply" : "rollback");
    writesChanged();
    invalidateRead();
    const edit = ++interaction.current;
    setReadError(""); setActionError(""); setConflict(false);
    const reqContext = { token, tokenOwner: owner, isAuthContextCurrent: current };
    try {
      const result = await (id ? switchModelProfile(id, manager, reqContext) : rollbackModelProfile(manager, reqContext));
      if (current()) {
        setData(result);
        if (edit === interaction.current) { setConfirmingApply(false); setEditing(false); setSelected(null); }
        changed.current();
      }
    } catch (e) {
      if (current() && edit === interaction.current) setActionError(errorText(e));
    } finally {
      pendingWrites.delete(manager);
      if (current()) setBusyAction(null);
      writesChanged();
    }
  };

  const save = async (id: string, input: ModelProfileSaveInput) => {
    if (!current() || pendingWrites.has(manager)) return;
    pendingWrites.add(manager);
    setBusyAction("save");
    writesChanged();
    invalidateRead();
    const edit = ++interaction.current;
    setReadError(""); setActionError(""); setConflict(false);
    const reqContext = { token, tokenOwner: owner, isAuthContextCurrent: current };
    try {
      const result = await saveModelProfile(id, input, manager, reqContext);
      if (current()) {
        setData(result);
        if (edit === interaction.current) {
          setEditRevision(result.revision ?? 0);
          setEditing(false);
        }
        toast(t("settings-modelprofiles.saved"), "ok");
        changed.current();
      }
    } catch (e) {
      if (current() && edit === interaction.current) {
        const isConflict = e instanceof ApiError && e.status === 409;
        setConflict(isConflict);
        setActionError(isConflict ? t("settings-modelprofiles.conflict") : errorText(e));
      }
    } finally {
      pendingWrites.delete(manager);
      if (current()) setBusyAction(null);
      writesChanged();
    }
  };

  const capture = async (
    id: string,
    target: Pick<ModelProfile, "name" | "description">,
    options?: { keepEditing?: boolean; exitConfirm?: boolean },
  ) => {
    if (!current() || pendingWrites.has(manager)) return;
    pendingWrites.add(manager);
    setBusyAction("capture");
    writesChanged();
    invalidateRead();
    const edit = ++interaction.current;
    setReadError(""); setActionError(""); setConflict(false);
    const reqContext = { token, tokenOwner: owner, isAuthContextCurrent: current };
    const revision = options?.keepEditing ? editRevision : (data?.revision ?? 0);
    try {
      const result = await saveModelProfile(id, captureProfileInput(target, revision), manager, reqContext);
      if (current()) {
        setData(result);
        if (edit === interaction.current) {
          setEditRevision(result.revision ?? 0);
          if (options?.keepEditing) setEditSyncKey((n) => n + 1);
          if (options?.exitConfirm) setConfirmingApply(false);
        }
        toast(t("settings-modelprofiles.captured"), "ok");
        changed.current();
      }
    } catch (e) {
      if (current() && edit === interaction.current) {
        const isConflict = e instanceof ApiError && e.status === 409;
        setConflict(isConflict);
        setActionError(isConflict ? t("settings-modelprofiles.conflict_capture") : errorText(e));
      }
    } finally {
      pendingWrites.delete(manager);
      if (current()) setBusyAction(null);
      writesChanged();
    }
  };

  const captureActiveAndApply = async (activeTarget: ModelProfile, nextTarget: ModelProfile) => {
    if (!current() || pendingWrites.has(manager)) return;
    pendingWrites.add(manager);
    setBusyAction("capture-apply");
    writesChanged();
    invalidateRead();
    const edit = ++interaction.current;
    setReadError(""); setActionError(""); setConflict(false);
    const reqContext = { token, tokenOwner: owner, isAuthContextCurrent: current };
    try {
      let captured: Profiles;
      try {
        captured = await saveModelProfile(
          activeTarget.id,
          captureProfileInput(activeTarget, data?.revision ?? 0),
          manager,
          reqContext,
        );
      } catch (e) {
        if (current() && edit === interaction.current) {
          const isConflict = e instanceof ApiError && e.status === 409;
          setConflict(isConflict);
          setActionError(isConflict ? t("settings-modelprofiles.conflict_capture") : errorText(e));
        }
        return;
      }
      if (!current()) return;
      setData(captured);
      try {
        const applied = await switchModelProfile(nextTarget.id, manager, reqContext);
        if (current()) {
          setData(applied);
          if (edit === interaction.current) {
            setConfirmingApply(false);
            setEditing(false);
            setSelected(null);
          }
          toast(t("settings-modelprofiles.captured_and_applied", { active: activeTarget.name, target: nextTarget.name }), "ok");
          changed.current();
        }
      } catch (e) {
        if (current() && edit === interaction.current) {
          setActionError(t("settings-modelprofiles.apply_failed_after_capture", { active: activeTarget.name, error: errorText(e) }));
          changed.current();
        }
      }
    } finally {
      pendingWrites.delete(manager);
      if (current()) setBusyAction(null);
      writesChanged();
    }
  };

  const isUnsupportedRouteError = (e: unknown) =>
    e instanceof ApiError && (e.status === 404 || e.status === 405);

  const resetCreateForm = () => {
    setCreateName("");
    setCreateId("");
    setCreateIdTouched(false);
    setCreateDescription("");
  };

  const openCreate = () => {
    if (busy) return;
    clearErrors();
    setSelected(null);
    setEditing(false);
    setConfirmingApply(false);
    setConfirmingDelete(false);
    setConfirmingClearAll(false);
    resetCreateForm();
    setCreating(true);
  };

  const openClearAll = () => {
    if (busy) return;
    clearErrors();
    setSelected(null);
    setEditing(false);
    setConfirmingApply(false);
    setConfirmingDelete(false);
    setCreating(false);
    setConfirmingClearAll(true);
  };

  const createProfile = async () => {
    if (!current() || pendingWrites.has(manager)) return;
    const existingIds = (data?.profiles ?? []).map((p) => p.id);
    const check = validateNewProfile(createName, createId, createDescription, existingIds);
    if (!check.ok) return;
    pendingWrites.add(manager);
    setBusyAction("create");
    writesChanged();
    invalidateRead();
    const edit = ++interaction.current;
    setReadError(""); setActionError(""); setConflict(false);
    const reqContext = { token, tokenOwner: owner, isAuthContextCurrent: current };
    try {
      const result = await saveModelProfile(
        check.id,
        captureProfileInput({ name: check.name, description: check.description }, data?.revision ?? 0),
        manager,
        reqContext,
      );
      if (current()) {
        setData(result);
        if (edit === interaction.current) {
          setCreating(false);
          resetCreateForm();
        }
        toast(t("settings-modelprofiles.created", { name: check.name }), "ok");
        changed.current();
      }
    } catch (e) {
      if (current() && edit === interaction.current) {
        if (e instanceof ApiError && e.status === 409) {
          const fresh = await load();
          if (current() && edit === interaction.current) {
            setConflict(!fresh);
            setActionError(t("settings-modelprofiles.conflict_create"));
          }
        } else {
          setActionError(errorText(e));
        }
      }
    } finally {
      pendingWrites.delete(manager);
      if (current()) setBusyAction(null);
      writesChanged();
    }
  };

  const deactivate = async () => {
    if (!current() || pendingWrites.has(manager)) return;
    pendingWrites.add(manager);
    setBusyAction("deactivate");
    writesChanged();
    invalidateRead();
    const edit = ++interaction.current;
    setReadError(""); setActionError(""); setConflict(false);
    const reqContext = { token, tokenOwner: owner, isAuthContextCurrent: current };
    try {
      const result = await deactivateModelProfile(manager, reqContext);
      if (current()) {
        setData(result);
        if (edit === interaction.current) {
          setConfirmingApply(false);
          setConfirmingDelete(false);
          setEditing(false);
          setSelected(null);
        }
        toast(t("settings-modelprofiles.deactivated"), "ok");
        changed.current();
      }
    } catch (e) {
      if (current() && edit === interaction.current) {
        setActionError(isUnsupportedRouteError(e) ? t("settings-modelprofiles.unsupported_deactivate") : errorText(e));
      }
    } finally {
      pendingWrites.delete(manager);
      if (current()) setBusyAction(null);
      writesChanged();
    }
  };

  const removeProfile = async (target: ModelProfile) => {
    if (!current() || pendingWrites.has(manager)) return;
    pendingWrites.add(manager);
    setBusyAction("delete");
    writesChanged();
    invalidateRead();
    const edit = ++interaction.current;
    setReadError(""); setActionError(""); setConflict(false);
    const reqContext = { token, tokenOwner: owner, isAuthContextCurrent: current };
    try {
      const result = await deleteModelProfile(target.id, data?.revision, manager, reqContext);
      if (current()) {
        setData(result);
        if (edit === interaction.current) {
          setConfirmingDelete(false);
          setConfirmingApply(false);
          setEditing(false);
          setSelected(null);
        }
        toast(t("settings-modelprofiles.deleted", { name: target.name }), "ok");
        changed.current();
      }
    } catch (e) {
      if (current() && edit === interaction.current) {
        if (e instanceof ApiError && e.status === 409) {
          const fresh = await load();
          if (current() && edit === interaction.current) {
            setConflict(!fresh);
            setActionError(t("settings-modelprofiles.conflict_delete"));
          }
        } else if (e instanceof ApiError && e.status === 405) {
          setActionError(t("settings-modelprofiles.unsupported_delete"));
        } else if (e instanceof ApiError && e.status === 404) {
          const fresh = await load();
          if (current() && edit === interaction.current) {
            const stillExists = fresh?.profiles.some((p) => p.id === target.id) ?? true;
            setActionError(stillExists ? t("settings-modelprofiles.unsupported_delete") : errorText(e));
          }
        } else {
          setActionError(errorText(e));
        }
      }
    } finally {
      pendingWrites.delete(manager);
      if (current()) setBusyAction(null);
      writesChanged();
    }
  };

  const clearAll = async () => {
    if (!current() || pendingWrites.has(manager)) return;
    pendingWrites.add(manager);
    setBusyAction("clear");
    writesChanged();
    invalidateRead();
    const edit = ++interaction.current;
    setReadError(""); setActionError(""); setConflict(false);
    const reqContext = { token, tokenOwner: owner, isAuthContextCurrent: current };
    try {
      const result = await clearModelProfiles(data?.revision, manager, reqContext);
      if (current()) {
        setData(result);
        if (edit === interaction.current) {
          setConfirmingClearAll(false);
          setConfirmingDelete(false);
          setConfirmingApply(false);
          setEditing(false);
          setSelected(null);
        }
        toast(t("settings-modelprofiles.cleared_all"), "ok");
        changed.current();
      }
    } catch (e) {
      if (current() && edit === interaction.current) {
        if (e instanceof ApiError && e.status === 409) {
          const fresh = await load();
          if (current() && edit === interaction.current) {
            setConflict(!fresh);
            setActionError(t("settings-modelprofiles.conflict_clear"));
          }
        } else if (isUnsupportedRouteError(e)) {
          setActionError(t("settings-modelprofiles.unsupported_clear"));
        } else {
          setActionError(errorText(e));
        }
      }
    } finally {
      pendingWrites.delete(manager);
      if (current()) setBusyAction(null);
      writesChanged();
    }
  };

  const reloadForEdit = async () => {
    clearErrors();
    const fresh = await load();
    if (fresh && current()) {
      setEditRevision(fresh.revision ?? 0);
      setEditSyncKey((n) => n + 1);
    }
  };

  const reloadForDetail = async () => {
    clearErrors();
    await load();
  };

  const clearErrors = () => { interaction.current++; setReadError(""); setActionError(""); setConflict(false); };
  const closeDialog = () => {
    if (busy) return;
    setConfirmingApply(false);
    setConfirmingDelete(false);
    setEditing(false);
    setSelected(null);
    clearErrors();
  };
  const closeCreateDialog = () => {
    if (busy) return;
    setCreating(false);
    resetCreateForm();
    clearErrors();
  };
  const closeClearAllDialog = () => {
    if (busy) return;
    setConfirmingClearAll(false);
    clearErrors();
  };
  const startEditing = () => {
    clearErrors();
    setConfirmingApply(false);
    setConfirmingDelete(false);
    setEditRevision(data?.revision ?? 0);
    setEditSyncKey((n) => n + 1);
    setEditing(true);
  };

  const error = actionError || readError;
  const profiles = data?.profiles ?? [];
  const hasProfiles = profiles.length > 0;
  const hasRollbacks = Boolean(data?.rollback || (data?.rollbacks?.length ?? 0) > 0);
  const canClearAll = hasProfiles || hasRollbacks;
  const existingIds = profiles.map((p) => p.id);
  const createValidation = validateNewProfile(createName, createId, createDescription, existingIds);
  const profile = profiles.find((p) => p.id === selected);
  const activeProfile = data?.active ? profiles.find((p) => p.id === data.active) : undefined;
  const drift = profile ? profileApplyDrift(profile, activeProfile) : null;
  const dialogOpen = Boolean(profile || creating || confirmingClearAll);
  const tiers = Object.entries(profile?.tiers ?? {}).sort(([a], [b]) => a.localeCompare(b));
  const purposeEntries = Object.entries(profile?.purposes ?? {}).sort(([a], [b]) => {
    const ai = (PROFILE_CATEGORY_IDS as readonly string[]).indexOf(a);
    const bi = (PROFILE_CATEGORY_IDS as readonly string[]).indexOf(b);
    if (ai >= 0 && bi >= 0) return ai - bi;
    if (ai >= 0) return -1;
    if (bi >= 0) return 1;
    return a.localeCompare(b);
  });
  const launcherEntries = Object.entries(profile?.launchers ?? {}).sort(([a], [b]) => a.localeCompare(b));
  const names = namesFromProviders(providers);
  const describedTargetChanges = (drift?.targetChanges ?? []).map((change) => describeChange(change, profile, purposes, providers, names));
  const describedActiveDrift = (drift?.activeDrift ?? []).map((change) => describeChange(change, undefined, purposes, providers, names));

  const requestApply = () => {
    if (!profile || busy) return;
    if (drift?.hasDrift) {
      clearErrors();
      setConfirmingApply(true);
      return;
    }
    void apply(profile.id);
  };

  if (!data && !error) return null; // old hubs do not expose profiles

  const errorBanner = error ? (
    <div className="flex flex-wrap items-center justify-between gap-2">
      <p role="alert" data-testid="profile-action-error" className="min-w-0 flex-1 text-label text-red">{error}</p>
      {conflict && (
        <Button tone="secondary" size="sm" disabled={busy} onClick={() => void reloadForDetail()}>
          {t("settings-modelprofiles.reload")}
        </Button>
      )}
    </div>
  ) : null;

  const dialogTitle = !profile
    ? ""
    : editing
      ? t("settings-modelprofiles.edit_title", { name: profile.name })
      : confirmingDelete
        ? t("settings-modelprofiles.confirm_delete_title", { name: profile.name })
        : confirmingApply
          ? t("settings-modelprofiles.confirm_apply_title", { name: profile.name })
          : profile.name;

  const dialogFooter = !profile || editing ? undefined : confirmingDelete ? (
    <div data-testid="profile-delete-confirm-footer" className="flex w-full flex-col gap-2">
      {errorBanner}
      <div className="flex items-center justify-end gap-2">
        <Button
          tone="quiet"
          disabled={busy}
          onClick={() => { setConfirmingDelete(false); clearErrors(); }}
        >
          {t("common.cancel")}
        </Button>
        <Button
          tone="danger"
          testId="profile-delete-confirm"
          busy={busyAction === "delete"}
          disabled={busy}
          onClick={() => void removeProfile(profile)}
        >
          {t("settings-modelprofiles.delete_confirm")}
        </Button>
      </div>
    </div>
  ) : confirmingApply ? (
    <div data-testid="profile-confirm-footer" className="flex w-full flex-col gap-2">
      {errorBanner}
      <div className="flex flex-col gap-1.5">
        {drift?.canCaptureActiveFirst && activeProfile ? (
          drift.hasActiveDrift ? (
            <>
              <Button
                tone="primary"
                className="w-full"
                testId="profile-capture-and-apply"
                busy={busyAction === "capture-apply"}
                disabled={busy}
                onClick={() => void captureActiveAndApply(activeProfile, profile)}
              >
                <span className="truncate">{t("settings-modelprofiles.capture_active_first", { name: activeProfile.name })}</span>
              </Button>
              <Button
                tone="secondary"
                className="w-full"
                testId="profile-apply-direct"
                busy={busyAction === "apply"}
                disabled={busy}
                onClick={() => void apply(profile.id)}
              >
                {t("settings-modelprofiles.apply_directly")}
              </Button>
            </>
          ) : (
            <>
              <Button
                tone="primary"
                className="w-full"
                testId="profile-apply-direct"
                busy={busyAction === "apply"}
                disabled={busy}
                onClick={() => void apply(profile.id)}
              >
                {t("settings-modelprofiles.apply_directly")}
              </Button>
              <Button
                tone="secondary"
                className="w-full"
                testId="profile-capture-and-apply"
                busy={busyAction === "capture-apply"}
                disabled={busy}
                onClick={() => void captureActiveAndApply(activeProfile, profile)}
              >
                <span className="truncate">{t("settings-modelprofiles.capture_active_first", { name: activeProfile.name })}</span>
              </Button>
            </>
          )
        ) : profile.active ? (
          <>
            <Button
              tone="primary"
              className="w-full"
              testId="profile-confirm-capture"
              busy={busyAction === "capture"}
              disabled={busy}
              onClick={() => void capture(profile.id, profile, { exitConfirm: true })}
            >
              {t("settings-modelprofiles.capture_from_current")}
            </Button>
            <Button
              tone="secondary"
              className="w-full"
              testId="profile-apply-direct"
              busy={busyAction === "apply"}
              disabled={busy}
              onClick={() => void apply(profile.id)}
            >
              {t("settings-modelprofiles.apply_directly")}
            </Button>
          </>
        ) : (
          <Button
            tone="primary"
            className="w-full"
            testId="profile-apply-direct"
            busy={busyAction === "apply"}
            disabled={busy}
            onClick={() => void apply(profile.id)}
          >
            {t("settings-modelprofiles.apply_directly")}
          </Button>
        )}
        <Button
          tone="quiet"
          className="w-full"
          disabled={busy}
          onClick={() => { setConfirmingApply(false); clearErrors(); }}
        >
          {t("common.cancel")}
        </Button>
      </div>
    </div>
  ) : (
    <div className="flex w-full flex-col gap-2">
      {errorBanner}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <Button
          tone="quiet"
          className="text-red enabled:hover:text-red"
          testId="profile-delete-button"
          disabled={busy}
          onClick={() => { clearErrors(); setConfirmingDelete(true); }}
        >
          {t("settings-modelprofiles.delete")}
        </Button>
        <div className="flex items-center gap-2">
          <Button tone="quiet" disabled={busy} onClick={closeDialog}>{t("common.cancel")}</Button>
          <Button tone="secondary" disabled={busy} onClick={startEditing}>{t("settings-modelprofiles.edit")}</Button>
          <Button tone="primary" busy={busyAction === "apply"} disabled={busy} onClick={requestApply}>{t("settings-modelprofiles.apply")}</Button>
        </div>
      </div>
    </div>
  );

  const groupRight = (canClearAll || hasProfiles) ? (
    <div className="flex items-center gap-1">
      {canClearAll && (
        <Button
          tone="quiet"
          size="sm"
          testId="profile-clear-all-button"
          disabled={busy}
          onClick={openClearAll}
        >
          {t("settings-modelprofiles.clear_all")}
        </Button>
      )}
      {hasProfiles && (
        <Button
          tone="secondary"
          size="sm"
          icon="plus"
          testId="profile-create-button"
          disabled={busy || profiles.length >= MAX_MODEL_PROFILES}
          onClick={openCreate}
        >
          {t("settings-modelprofiles.new_profile")}
        </Button>
      )}
    </div>
  ) : undefined;

  const groupFooter = hasProfiles
    ? (data && !data.active ? `${t("settings-modelprofiles.footer")} ${t("settings-modelprofiles.none_active")}` : t("settings-modelprofiles.footer"))
    : undefined;

  return <>
    <Group anchor="model-profiles" title={t("settings-modelprofiles.title")} right={groupRight} footer={groupFooter}>
      {data && !hasProfiles ? (
        <Row
          label={t("settings-modelprofiles.empty_title")}
          hint={t("settings-modelprofiles.empty_hint")}
          wrapHint
          stackOnNarrow
        >
          <Button
            tone="primary"
            size="sm"
            icon="plus"
            testId="profile-create-button"
            disabled={busy}
            onClick={openCreate}
          >
            {t("settings-modelprofiles.create")}
          </Button>
        </Row>
      ) : (
        profiles.map((p) => (
          <Row key={p.id} label={p.name} hint={p.description} onClick={() => { setSelected(p.id); setEditing(false); setConfirmingApply(false); setConfirmingDelete(false); clearErrors(); }}>
            {p.active && <Pill>{t("settings-modelprofiles.active")}</Pill>}
            {!p.active && p.trial && (
              <span className="shrink-0 whitespace-nowrap text-submeta text-ink-3">
                {t("settings-modelprofiles.trial_summary", {
                  days: p.trial.days,
                  dispatches: p.trial.dispatches,
                  would_switch: p.trial.would_switch,
                })}
              </span>
            )}
          </Row>
        ))
      )}
      {data?.active && (
        <Row
          label={t("settings-modelprofiles.deactivate_row_title", { name: activeProfile?.name ?? data.active })}
          hint={t("settings-modelprofiles.deactivate_hint")}
          wrapHint
          stackOnNarrow
        >
          <Button
            tone="secondary"
            size="sm"
            testId="profile-deactivate-row-button"
            busy={busyAction === "deactivate"}
            disabled={busy}
            onClick={() => void deactivate()}
          >
            {t("settings-modelprofiles.deactivate")}
          </Button>
        </Row>
      )}
      {data?.rollback && (
        <Row label={t("settings-modelprofiles.rollback")} hint={data.rollback.to || t("settings-modelprofiles.custom")}>
          <Button tone="quiet" busy={busyAction === "rollback"} disabled={busy} onClick={() => void apply()}>{t("settings-modelprofiles.rollback")}</Button>
        </Row>
      )}
    </Group>
    {!dialogOpen && error && <p role="alert" className="mb-4 text-meta text-red">{error}</p>}
    <Dialog
      open={!!profile}
      width={editing ? 560 : 480}
      title={dialogTitle}
      onClose={closeDialog}
      footer={dialogFooter}
    >
      {profile && editing ? (
        <Suspense fallback={<Loading />}>
          <ModelProfileEditor
            manager={manager}
            profile={profile}
            revision={editRevision}
            syncKey={editSyncKey}
            purposes={purposes}
            providers={providers}
            busy={busy}
            saving={busyAction === "save"}
            capturing={busyAction === "capture"}
            error={error}
            conflict={conflict}
            onSave={(input) => save(profile.id, input)}
            onCapture={() => capture(profile.id, profile, { keepEditing: true })}
            onDelete={() => { clearErrors(); setEditing(false); setConfirmingDelete(true); }}
            onReload={reloadForEdit}
            onCancel={() => { setEditing(false); clearErrors(); }}
          />
        </Suspense>
      ) : profile && confirmingDelete ? (
        <div data-testid="model-profile-delete-confirm" className="flex flex-col gap-3">
          <p className="text-label leading-relaxed text-ink-2">
            {profile.active
              ? t("settings-modelprofiles.confirm_delete_active", { name: profile.name })
              : t("settings-modelprofiles.confirm_delete_inactive", { name: profile.name })}
          </p>
        </div>
      ) : profile && confirmingApply ? (
        <div data-testid="model-profile-apply-confirm" className="flex flex-col gap-3">
          {describedTargetChanges.length > 0 ? (
            <>
              <p className="text-label leading-relaxed text-ink-2">
                {profile.active
                  ? t("settings-modelprofiles.confirm_reapply_intro", { name: profile.name })
                  : t("settings-modelprofiles.confirm_apply_intro", { name: profile.name })}
              </p>
              <ul data-testid="profile-apply-drift-list" className="flex flex-col gap-1.5">
                {describedTargetChanges.map(({ key, label, target }) => (
                  <li key={key} className="flex items-baseline justify-between gap-2 rounded-control bg-field px-3 py-2 text-label">
                    <span className="shrink-0 font-medium text-ink">{label}</span>
                    {target && <span className="min-w-0 truncate text-right text-ink-3">{target}</span>}
                  </li>
                ))}
              </ul>
            </>
          ) : activeProfile && describedActiveDrift.length > 0 ? (
            <p className="text-label leading-relaxed text-ink-2">
              {t("settings-modelprofiles.confirm_switch_active_drift_only", { active: activeProfile.name, target: profile.name })}
            </p>
          ) : null}

          {drift?.canCaptureActiveFirst && activeProfile && (
            drift.hasActiveDrift ? (
              <div data-testid="profile-active-drift-notice" className="flex flex-col gap-2 rounded-control bg-inset px-3 py-2.5 text-label text-ink-2">
                <p className="leading-relaxed">
                  {t("settings-modelprofiles.confirm_active_drift", { active: activeProfile.name })}
                </p>
                <ul className="flex flex-wrap gap-1.5">
                  {describedActiveDrift.map(({ key, label }) => (
                    <li key={key} className="rounded-control bg-field px-2 py-0.5 text-submeta text-ink-2">{label}</li>
                  ))}
                </ul>
              </div>
            ) : (
              <p className="text-submeta text-ink-3">
                {t("settings-modelprofiles.confirm_capture_active_hint", { active: activeProfile.name })}
              </p>
            )
          )}
        </div>
      ) : profile ? (
        <>
          {profile.description && <p className="mb-3 text-label text-ink-3">{profile.description}</p>}
          {profile.active && (
            <div data-testid="model-profile-active-banner" className="mb-3 flex flex-wrap items-center justify-between gap-2 rounded-control bg-inset px-3 py-2.5 text-label text-ink-2">
              <div className="min-w-0 flex-1">
                <div className="mb-1 flex items-center gap-2">
                  <Pill>{t("settings-modelprofiles.active")}</Pill>
                </div>
                <p className="leading-relaxed">{t("settings-modelprofiles.deactivate_hint")}</p>
              </div>
              <Button
                tone="secondary"
                size="sm"
                testId="profile-deactivate-button"
                busy={busyAction === "deactivate"}
                disabled={busy}
                onClick={() => void deactivate()}
              >
                {t("settings-modelprofiles.deactivate")}
              </Button>
            </div>
          )}
          <p className="mb-2 text-label text-ink-3">{t("settings-modelprofiles.changes", { count: profile.changes.length })}</p>
          {describedTargetChanges.length > 0 && (
            <ul data-testid="model-profile-changes" className="mb-3 flex flex-wrap gap-1.5">
              {describedTargetChanges.map(({ key, label }) => (
                <li key={key} className="rounded-control bg-field px-2 py-0.5 text-submeta text-ink-2">{label}</li>
              ))}
            </ul>
          )}
          {!profile.active && profile.trial && (
            <div data-testid="model-profile-trial" className="mb-4 rounded-control bg-inset px-3 py-2 text-label">
              <p className="font-medium text-ink">
                {t("settings-modelprofiles.trial_summary", {
                  days: profile.trial.days,
                  dispatches: profile.trial.dispatches,
                  would_switch: profile.trial.would_switch,
                })}
              </p>
              <p className="mt-0.5 text-submeta text-ink-3">{t("settings-modelprofiles.trial_definition")}</p>
            </div>
          )}
          <div className="mb-4 flex flex-col gap-2 rounded-control bg-inset px-3 py-2.5 text-label text-ink-2">
            <p className="leading-relaxed">
              {profile.active && profile.changes.length > 0
                ? t("settings-modelprofiles.active_drift")
                : t("settings-modelprofiles.capture_hint")}
            </p>
            <div>
              <Button
                tone="secondary"
                size="sm"
                testId="profile-capture-button"
                busy={busyAction === "capture"}
                disabled={busy}
                onClick={() => void capture(profile.id, profile)}
              >
                {t("settings-modelprofiles.capture_from_current")}
              </Button>
            </div>
          </div>

          <p className="mb-1.5 text-label font-semibold text-ink-2">{t("settings-modelprofiles.tiers")}</p>
          {tiers.length ? (
            <ul data-testid="model-profile-tiers" className="mb-4 flex flex-col gap-1.5">
              {tiers.map(([name, x]) => {
                // the launcher by its display name, its startup ID after it in small mono (#5029); an rc alias by its ID
                const multiCandidates = x.candidates && x.candidates.length > 1 ? x.candidates : null;
                const renderCandidate = (c: { launcher: string; model?: string; effort?: string; permission_mode?: string; speed?: string }) => {
                  const l = namedLauncher(c.launcher, names);
                  const id = providerIdNote(l);
                  const speed = c.speed === "fast" || c.speed === "ultrafast" ? c.speed : "";
                  return (
                    <>
                      {providerTitle(l)}
                      {id && <> <span className="font-mono text-submeta">{id}</span></>}
                      {[c.model, c.effort, c.permission_mode && modeTitle(c.permission_mode), speed].filter(Boolean).map((v) => ` · ${v}`).join("")}
                    </>
                  );
                };
                return (
                  <li key={name} className="rounded-control bg-field px-3 py-2 text-label">
                    {multiCandidates ? (
                      <>
                        <div className="flex min-w-0 flex-wrap items-baseline gap-2">
                          <span className="shrink-0 font-mono text-ink">{name}</span>
                          {x.aliases && x.aliases.length > 0 && (
                            <span data-testid="model-profile-tier-aliases" className="text-submeta text-ink-3">
                              {t("settings-modelprofiles.tier_aliases_inline", { aliases: x.aliases.join(", ") })}
                            </span>
                          )}
                        </div>
                        <ol className="mt-1 flex flex-col gap-0.5 text-ink-3">
                          {multiCandidates.map((cand, ci) => (
                            <li key={ci} className="min-w-0 [overflow-wrap:anywhere]">
                              <span className="mr-1.5 font-mono text-submeta text-ink-2">{ci + 1}.</span>
                              {renderCandidate(cand)}
                            </li>
                          ))}
                        </ol>
                      </>
                    ) : (
                      <>
                        <div className="flex min-w-0 items-baseline gap-2">
                          <span className="shrink-0 font-mono text-ink">{name}</span>
                          <span className="min-w-0 [overflow-wrap:anywhere] text-ink-3">
                            {renderCandidate(x)}
                          </span>
                        </div>
                        {x.aliases && x.aliases.length > 0 && (
                          <p data-testid="model-profile-tier-aliases" className="mt-0.5 text-submeta text-ink-3">
                            {t("settings-modelprofiles.tier_aliases_inline", { aliases: x.aliases.join(", ") })}
                          </p>
                        )}
                      </>
                    )}
                    {x.description && <p className="mt-0.5 text-ink-2">{x.description}</p>}
                  </li>
                );
              })}
            </ul>
          ) : (
            <p className="mb-4 text-label text-ink-3">{t("settings-modelprofiles.no_tiers")}</p>
          )}

          <p className="mb-1.5 text-label font-semibold text-ink-2">{t("settings-modelprofiles.purposes_section")}</p>
          {purposeEntries.length ? (
            <ul data-testid="model-profile-purposes" className="mb-4 flex flex-col gap-1.5">
              {purposeEntries.map(([id, b]) => {
                const summary = [
                  b.provider ? (providerName(b.provider, providers) ?? b.provider) : t("settings-modelprofiles.purpose_follow_default"),
                  b.model,
                  b.effort && effortTitle(b.effort),
                  b.permission_mode && modeTitle(b.permission_mode),
                ].filter(Boolean).join(" · ");
                return (
                  <li key={id} className="flex items-baseline justify-between gap-2 rounded-control bg-field px-3 py-2 text-label">
                    <span className="shrink-0 font-medium text-ink">{purposeLabel(id, purposes)}</span>
                    <span className="min-w-0 truncate text-right text-ink-3">{summary}</span>
                  </li>
                );
              })}
            </ul>
          ) : (
            <p className="mb-4 text-label text-ink-3">{t("settings-modelprofiles.no_purposes")}</p>
          )}

          <p className="mb-1.5 text-label font-semibold text-ink-2">{t("settings-modelprofiles.launchers_section")}</p>
          {launcherEntries.length ? (
            <ul data-testid="model-profile-launchers" className="mb-2 flex flex-col gap-1.5">
              {launcherEntries.map(([name, fields]) => {
                const l = namedLauncher(name, names);
                const id = providerIdNote(l);
                const items = LAUNCHER_MODEL_FIELDS.filter((f) => fields[f] !== undefined).map((f) => ({
                  field: f,
                  value: fields[f]!,
                }));
                return (
                  <li key={name} className="rounded-control bg-field px-3 py-2 text-label">
                    <div className="font-medium text-ink">
                      {providerTitle(l)}
                      {id && <> <span className="font-mono text-submeta text-ink-3">{id}</span></>}
                    </div>
                    <div className="mt-1 flex flex-col gap-0.5 text-submeta text-ink-3">
                      {items.map(({ field, value }) => (
                        <div key={field} className="flex items-baseline justify-between gap-2">
                          <span>{t(`settings-modelprofiles.field_${field}`)}</span>
                          <span className="min-w-0 truncate font-mono text-ink-2">
                            {value === "" ? t("settings-modelprofiles.clear_on_switch") : field === "effort" ? effortTitle(value) : value}
                          </span>
                        </div>
                      ))}
                    </div>
                  </li>
                );
              })}
            </ul>
          ) : (
            <p className="mb-2 text-label text-ink-3">{t("settings-modelprofiles.no_launchers")}</p>
          )}
        </>
      ) : null}
    </Dialog>
    <Dialog
      open={creating}
      width={480}
      title={t("settings-modelprofiles.create_title")}
      onClose={closeCreateDialog}
      footer={
        <div data-testid="profile-create-footer" className="flex w-full flex-col gap-2">
          {errorBanner}
          <div className="flex items-center justify-end gap-2">
            <Button tone="quiet" disabled={busy} onClick={closeCreateDialog}>
              {t("common.cancel")}
            </Button>
            <Button
              tone="primary"
              testId="profile-create-submit"
              busy={busyAction === "create"}
              disabled={busy || !createValidation.ok}
              onClick={() => void createProfile()}
            >
              {t("settings-modelprofiles.create")}
            </Button>
          </div>
        </div>
      }
    >
      <div data-testid="model-profile-create-dialog" className="flex flex-col gap-3">
        <p className="rounded-control bg-inset px-3 py-2.5 text-label leading-relaxed text-ink-2">
          {t("settings-modelprofiles.create_note")}
        </p>
        <label className="block">
          <span className="mb-1 block text-label font-medium text-ink-2">{t("settings-modelprofiles.name")}</span>
          <input
            aria-label={t("settings-modelprofiles.name")}
            className={settingsInputClass}
            value={createName}
            maxLength={MAX_PROFILE_NAME}
            disabled={busy}
            placeholder={t("settings-modelprofiles.name_placeholder")}
            onChange={(e) => {
              const nextName = e.target.value;
              setCreateName(nextName);
              if (!createIdTouched) {
                setCreateId(nextName.trim() ? suggestProfileId(nextName, existingIds) : "");
              }
            }}
          />
        </label>
        <label className="block">
          <span className="mb-1 block text-label font-medium text-ink-2">{t("settings-modelprofiles.profile_id")}</span>
          <input
            aria-label={t("settings-modelprofiles.profile_id")}
            className={`${settingsInputClass} font-mono`}
            value={createId}
            maxLength={32}
            disabled={busy}
            spellCheck={false}
            autoComplete="off"
            placeholder={t("settings-modelprofiles.profile_id_placeholder")}
            onChange={(e) => {
              setCreateId(e.target.value);
              setCreateIdTouched(true);
            }}
          />
          {!createValidation.ok && createValidation.idError && (createId.trim().length > 0 || createIdTouched) ? (
            <span role="alert" data-testid="profile-create-id-error" className="mt-1 block text-submeta text-red">
              {createValidation.idError}
            </span>
          ) : (
            <span className="mt-1 block text-submeta text-ink-3">{t("settings-modelprofiles.profile_id_hint")}</span>
          )}
        </label>
        <label className="block">
          <span className="mb-1 block text-label font-medium text-ink-2">{t("settings-modelprofiles.description")}</span>
          <input
            aria-label={t("settings-modelprofiles.description")}
            className={settingsInputClass}
            value={createDescription}
            maxLength={MAX_PROFILE_DESCRIPTION}
            disabled={busy}
            placeholder={t("settings-modelprofiles.description_placeholder")}
            onChange={(e) => setCreateDescription(e.target.value)}
          />
        </label>
      </div>
    </Dialog>
    <Dialog
      open={confirmingClearAll}
      width={480}
      title={t("settings-modelprofiles.confirm_clear_all_title")}
      onClose={closeClearAllDialog}
      footer={
        <div data-testid="profile-clear-all-footer" className="flex w-full flex-col gap-2">
          {errorBanner}
          <div className="flex items-center justify-end gap-2">
            <Button tone="quiet" disabled={busy} onClick={closeClearAllDialog}>
              {t("common.cancel")}
            </Button>
            <Button
              tone="danger"
              testId="profile-clear-all-confirm"
              busy={busyAction === "clear"}
              disabled={busy}
              onClick={() => void clearAll()}
            >
              {t("settings-modelprofiles.clear_all_confirm")}
            </Button>
          </div>
        </div>
      }
    >
      <div data-testid="model-profile-clear-all-confirm" className="flex flex-col gap-3">
        <p className="text-label leading-relaxed text-ink-2">
          {hasProfiles
            ? t("settings-modelprofiles.confirm_clear_all_body", { count: profiles.length })
            : t("settings-modelprofiles.confirm_clear_all_rollbacks_only")}
        </p>
      </div>
    </Dialog>
  </>;
}
