// Going between hubs (#1014, lib/hubs.ts): the list in Settings › General, the other hubs in the account menu and on the sign-in
// page. Switching opens the other hub's own page; with no other hub in the list, the menu and the sign-in page look as
// before (only Settings shows the list, to add one from).
import { useCallback, useLayoutEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { errorText } from "../lib/api";
import { addHub, here, hostOf, hubTitle, normalizeHubURL, removeHub, renameHub, type SavedHub } from "../lib/hubs";
import { Button, Dialog, IconButton, toast } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { Group, Pill, Row } from "./settings/SettingsParts";
import { goToHub, noopSubscribe, useHubs, type UseHubsDeps } from "./hub-menu";
import { t } from "../i18n";

export { here };
export { goToHub, hubMenuItems, useHubs } from "./hub-menu";
export type { GoToHubDeps, HubMenuItemsDeps, UseHubsDeps } from "./hub-menu";

export type HubNoticeTone = "ok" | "error" | "info";
export type AddHubResult = { list?: readonly SavedHub[]; added?: SavedHub } | { error: string } | void;

export interface AddHubDialogViewProps {
  open: boolean;
  url: string;
  label: string;
  error?: string;
  busy?: boolean;
  normalizeURL?: (input: string) => { url: string } | { error: string };
  onUrlChange: (value: string) => void;
  onLabelChange: (value: string) => void;
  onSave: () => void;
  onClose: () => void;
}

export interface RenameHubDialogViewProps {
  hub: SavedHub | null;
  label: string;
  error?: string;
  busy?: boolean;
  onLabelChange: (value: string) => void;
  onSave: () => void;
  onClose: () => void;
}

export interface HubListViewProps {
  rows: readonly SavedHub[];
  current: string;
  actionError?: string;
  switchingUrl?: string | null;
  removingUrls?: readonly string[];
  addOpen: boolean;
  addUrl: string;
  addLabel: string;
  addError?: string;
  addBusy?: boolean;
  renameHub: SavedHub | null;
  renameLabel: string;
  renameError?: string;
  renameBusy?: boolean;
  normalizeURL?: (input: string) => { url: string } | { error: string };
  onOpenAdd: () => void;
  onCloseAdd: () => void;
  onAddUrlChange: (value: string) => void;
  onAddLabelChange: (value: string) => void;
  onSaveAdd: () => void;
  onOpenRename: (hub: SavedHub) => void;
  onCloseRename: () => void;
  onRenameLabelChange: (value: string) => void;
  onSaveRename: () => void;
  onRemove: (hub: SavedHub) => void;
  onSwitch: (hub: SavedHub) => void;
}

export interface HubListDeps extends UseHubsDeps {
  /** Optional scope partition (value or getter); switching it resets dialogs, drafts, errors, and busy states and fences stale callbacks. */
  scopeKey?: string | (() => string);
  /** Subscribe to scope changes when `scopeKey` is a getter. */
  subscribeScopeKey?: (listener: () => void) => () => void;
  /** Controlled current hub URL (value or getter; bypasses `here()` when supplied). */
  current?: string | (() => string);
  /** Subscribe to current hub updates when `current` is a getter. */
  subscribeCurrent?: (listener: () => void) => () => void;
  /** Custom URL validator/normalizer (defaults to `lib/hubs.normalizeHubURL`). */
  normalizeURL?: (input: string) => { url: string } | { error: string };
  /** Add a hub (sync or async; defaults to `lib/hubs.addHub`). */
  addHub?: (url: string, label: string, signal: AbortSignal) => AddHubResult | Promise<AddHubResult>;
  /** Rename a hub (sync or async; defaults to `lib/hubs.renameHub`). */
  renameHub?: (url: string, label: string, signal: AbortSignal) => SavedHub[] | void | Promise<SavedHub[] | void>;
  /** Remove a hub (sync or async; defaults to `lib/hubs.removeHub`). */
  removeHub?: (url: string, signal: AbortSignal) => SavedHub[] | void | Promise<SavedHub[] | void>;
  /** Switch to another hub (sync or async; defaults to `goToHub`). */
  switchHub?: (hub: SavedHub, signal: AbortSignal) => void | Promise<unknown>;
  /** Show toast notifications (defaults to `atoms.toast`). */
  notify?: (message: string, tone?: HubNoticeTone) => void;
  /** Format thrown action errors (defaults to `api.errorText`). */
  formatError?: (error: unknown) => string;
  /** Error observer invoked when add/rename/remove/switch throws or rejects in the active scope. */
  onError?: (message: string) => void;
  /** Optional custom view renderer (defaults to `<HubListView>`). */
  renderView?: (props: HubListViewProps) => ReactNode;
}

export interface HubListProps {
  deps?: HubListDeps;
}

export interface OtherHubsViewProps {
  hubs: readonly SavedHub[];
  switchingUrl?: string | null;
  error?: string;
  onSwitch: (hub: SavedHub) => void;
}

export interface OtherHubsDeps extends UseHubsDeps {
  /** Optional scope partition (value or getter); switching it resets error/busy state and fences stale callbacks. */
  scopeKey?: string | (() => string);
  /** Subscribe to scope changes when `scopeKey` is a getter. */
  subscribeScopeKey?: (listener: () => void) => () => void;
  /** Controlled current hub URL (value or getter; bypasses `here()` when supplied). */
  current?: string | (() => string);
  /** Subscribe to current hub updates when `current` is a getter. */
  subscribeCurrent?: (listener: () => void) => () => void;
  /** Switch to another hub (sync or async; defaults to `goToHub`). */
  switchHub?: (hub: SavedHub, signal: AbortSignal) => void | Promise<unknown>;
  /** Show error notifications (optional; defaults to inline error without extra toast in `OtherHubs`). */
  notify?: (message: string, tone?: HubNoticeTone) => void;
  /** Format thrown switch errors (defaults to `api.errorText`). */
  formatError?: (error: unknown) => string;
  /** Error observer invoked when switching throws or rejects in the active scope. */
  onError?: (message: string) => void;
  /** Optional custom view renderer (defaults to `<OtherHubsView>`). */
  renderView?: (props: OtherHubsViewProps) => ReactNode;
}

export interface OtherHubsProps {
  deps?: OtherHubsDeps;
}

function isThenable(value: unknown): value is PromiseLike<unknown> {
  return value !== null && (typeof value === "object" || typeof value === "function") && typeof (value as PromiseLike<unknown>).then === "function";
}

function safeFormatError(fmt: ((error: unknown) => string) | undefined, error: unknown): string {
  if (!fmt) return errorText(error);
  try {
    return fmt(error);
  } catch {
    return errorText(error);
  }
}

function safeNotify(notify: ((message: string, tone?: HubNoticeTone) => void) | undefined, message: string, tone?: HubNoticeTone) {
  try {
    if (notify) notify(message, tone);
    else toast(message, tone);
  } catch {
    // Notification failures stay non-blocking so recovery never leaves busy stuck.
  }
}

function useTrackedScope(
  source: string | (() => string) | undefined,
  subscribe: ((listener: () => void) => () => void) | undefined,
) {
  const isGetter = typeof source === "function";
  const read = useCallback(() => (typeof source === "function" ? source() : (source ?? "")), [source]);
  const readRef = useRef(read);
  const committedRef = useRef<{ value: string; version: number } | null>(null);
  const snapRef = useRef<{ value: string; version: number } | null>(null);
  const sub = useCallback((listener: () => void) => {
    if (!subscribe || !isGetter) return noopSubscribe();
    return subscribe(() => {
      const next = readRef.current();
      const cur = committedRef.current;
      if (!cur || next !== cur.value) {
        committedRef.current = { value: next, version: (cur?.version ?? 0) + 1 };
      }
      listener();
    });
  }, [subscribe, isGetter]);
  const getSnapshot = useCallback(() => {
    const next = read();
    const cur = committedRef.current;
    if (cur && next === cur.value) return cur;
    const nextVersion = cur ? cur.version + 1 : 0;
    if (snapRef.current && snapRef.current.value === next && snapRef.current.version === nextVersion) {
      return snapRef.current;
    }
    snapRef.current = { value: next, version: nextVersion };
    return snapRef.current;
  }, [read]);
  const snap = useSyncExternalStore(sub, getSnapshot, getSnapshot);
  useLayoutEffect(() => {
    readRef.current = read;
    committedRef.current = snap;
  }, [read, snap]);
  const readToken = useCallback(() => {
    const cur = readRef.current();
    const committed = committedRef.current;
    return committed && committed.value === cur ? `${committed.version}:${cur}` : `drift:${cur}`;
  }, []);
  return { value: snap.value, token: `${snap.version}:${snap.value}`, readToken };
}

// ── Settings › General ─────────────────────────────────────

function Field({ label, value, onChange, placeholder, hint, error, mono, autoFocus }: { label: string; value: string; onChange: (v: string) => void; placeholder?: string; hint?: React.ReactNode; error?: string; mono?: boolean; autoFocus?: boolean }) {
  return (
    <label className="block">
      <span className="mb-1 block text-[12px] font-medium text-ink-2">{label}</span>
      <input
        autoFocus={autoFocus}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        autoComplete="off"
        spellCheck={false}
        aria-invalid={error ? true : undefined}
        className={`h-9 w-full rounded-control bg-field px-3 text-[13px] text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] ${mono ? "font-mono" : ""}`}
      />
      {error ? <span className="mt-1 block text-[11px] text-red" role="alert">{error}</span> : hint && <span className="mt-1 block text-[11px] text-ink-3">{hint}</span>}
    </label>
  );
}

/** Pure presentational dialog view for adding a hub. */
export function AddHubDialogView({
  open,
  url,
  label,
  error,
  busy = false,
  normalizeURL = normalizeHubURL,
  onUrlChange,
  onLabelChange,
  onSave,
  onClose,
}: AddHubDialogViewProps) {
  const parsed = url.trim() ? normalizeURL(url) : null;
  return (
    <Dialog
      open={open}
      onClose={onClose}
      width={440}
      title={t("hubs.add_hub")}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" busy={busy} disabled={!parsed || "error" in parsed} onClick={onSave}>
            {t("hubs.add")}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (!busy) onSave();
        }}
      >
        <Field
          label={t("hubs.address")}
          autoFocus
          mono
          value={url}
          onChange={onUrlChange}
          placeholder={t("hubs.address_placeholder")}
          error={error || (parsed && "error" in parsed ? parsed.error : "")}
          hint={parsed && "url" in parsed ? t("hubs.will_open", { url: parsed.url }) : t("hubs.address_hint")}
        />
        <Field label={t("hubs.name_optional")} value={label} onChange={onLabelChange} placeholder={t("hubs.name_placeholder")} />
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}

/** Pure presentational dialog view for renaming a hub. */
export function RenameHubDialogView({
  hub,
  label,
  error,
  busy = false,
  onLabelChange,
  onSave,
  onClose,
}: RenameHubDialogViewProps) {
  return (
    <Dialog
      open={hub !== null}
      onClose={onClose}
      width={400}
      title={t("hubs.rename")}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button tone="primary" busy={busy} onClick={onSave}>
            {t("common.save")}
          </Button>
        </>
      }
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (!busy) onSave();
        }}
      >
        <Field label={t("hubs.name")} autoFocus value={label} onChange={onLabelChange} placeholder={hub?.name || (hub ? hostOf(hub.url) : "")} error={error} hint={t("hubs.rename_hint")} />
        <button type="submit" hidden />
      </form>
    </Dialog>
  );
}

/** Pure presentational view for Settings › General › Hubs and its add/rename dialogs. */
export function HubListView({
  rows,
  current,
  actionError,
  switchingUrl = null,
  removingUrls = [],
  addOpen,
  addUrl,
  addLabel,
  addError,
  addBusy = false,
  renameHub: renameTarget,
  renameLabel,
  renameError,
  renameBusy = false,
  normalizeURL = normalizeHubURL,
  onOpenAdd,
  onCloseAdd,
  onAddUrlChange,
  onAddLabelChange,
  onSaveAdd,
  onOpenRename,
  onCloseRename,
  onRenameLabelChange,
  onSaveRename,
  onRemove,
  onSwitch,
}: HubListViewProps) {
  return (
    <>
      <Group
        anchor="hubs"
        title={t("hubs.group_title")}
        right={
          <Button tone="quiet" icon="plus" onClick={onOpenAdd} className="-mb-1 h-7 px-2">
            {t("hubs.add_hub")}
          </Button>
        }
        footer={t("hubs.group_footer")}
      >
        {actionError ? <p role="alert" className="px-4 py-2 text-submeta text-red">{actionError}</p> : null}
        {rows.length === 0 && <Row label={hostOf(current)} hint={t("hubs.this_hub")} />}
        {rows.map((h) => {
          const removing = removingUrls.includes(h.url);
          const switching = switchingUrl === h.url;
          return (
            <Row key={h.url} label={hubTitle(h)} hint={<span className="font-mono">{hostOf(h.url)}</span>}>
              {h.url === current ? (
                <Pill tone="green">{t("hubs.current")}</Pill>
              ) : (
                <Button tone="secondary" busy={switching} disabled={removing || (switchingUrl !== null && !switching)} onClick={() => onSwitch(h)} className="h-7 px-2.5">
                  {t("hubs.switch")}
                </Button>
              )}
              <IconButton icon="pencil" disabled={removing} label={t("hubs.rename_label", { name: hubTitle(h) })} onClick={() => onOpenRename(h)} />
              {h.url !== current && (
                <IconButton icon="trash" disabled={removing || switching} label={t("hubs.remove_label", { name: hubTitle(h) })} onClick={() => onRemove(h)} />
              )}
            </Row>
          );
        })}
      </Group>
      <AddHubDialogView
        open={addOpen}
        url={addUrl}
        label={addLabel}
        error={addError}
        busy={addBusy}
        normalizeURL={normalizeURL}
        onUrlChange={onAddUrlChange}
        onLabelChange={onAddLabelChange}
        onSave={onSaveAdd}
        onClose={onCloseAdd}
      />
      <RenameHubDialogView
        hub={renameTarget}
        label={renameLabel}
        error={renameError}
        busy={renameBusy}
        onLabelChange={onRenameLabelChange}
        onSave={onSaveRename}
        onClose={onCloseRename}
      />
    </>
  );
}

interface PendingOp {
  epoch: number;
  seq: number;
  controller: AbortController;
}

export function HubList({ deps }: HubListProps = {}) {
  const trackedScope = useTrackedScope(deps?.scopeKey, deps?.subscribeScopeKey);
  const currentSource = deps?.current !== undefined ? deps.current : here;
  const currentSub = deps?.current !== undefined ? deps.subscribeCurrent : undefined;
  const trackedCurrent = useTrackedScope(currentSource, currentSub);
  const list = useHubs(deps);
  const current = trackedCurrent.value;

  const hubScope = JSON.stringify([trackedScope.token, trackedCurrent.token]);
  const [scope, setScope] = useState<{ key: string; epoch: number }>(() => ({ key: hubScope, epoch: 1 }));
  const effectiveEpoch = scope.key === hubScope ? scope.epoch : scope.epoch + 1;
  if (scope.key !== hubScope) {
    setScope({ key: hubScope, epoch: effectiveEpoch });
  }

  const [addState, setAddState] = useState<{ epoch: number; seq: number; open: boolean; url: string; label: string; error: string }>({
    epoch: 0,
    seq: 0,
    open: false,
    url: "",
    label: "",
    error: "",
  });
  const [renameState, setRenameState] = useState<{ epoch: number; seq: number; url: string | null; label: string; error: string }>({
    epoch: 0,
    seq: 0,
    url: null,
    label: "",
    error: "",
  });
  const [addBusyState, setAddBusyState] = useState<{ epoch: number; seq: number; busy: boolean }>({ epoch: 0, seq: 0, busy: false });
  const [renameBusyState, setRenameBusyState] = useState<{ epoch: number; seq: number; busy: boolean }>({ epoch: 0, seq: 0, busy: false });
  const [removingState, setRemovingState] = useState<{ epoch: number; urls: string[] }>({ epoch: 0, urls: [] });
  const [switchingState, setSwitchingState] = useState<{ epoch: number; url: string | null }>({ epoch: 0, url: null });
  const [actionErrorState, setActionErrorState] = useState<{ epoch: number; message: string }>({ epoch: 0, message: "" });

  const liveRenameHub = renameState.epoch === effectiveEpoch && renameState.url !== null
    ? (list.find((h) => h.url === renameState.url) ?? null)
    : null;
  if (renameState.epoch === effectiveEpoch && renameState.url !== null && liveRenameHub === null) {
    setRenameState({ epoch: effectiveEpoch, seq: renameState.seq + 1, url: null, label: "", error: "" });
  }

  const addOpen = addState.epoch === effectiveEpoch && addState.open;
  const addSeq = addOpen ? addState.seq : 0;
  const addUrl = addOpen ? addState.url : "";
  const addLabel = addOpen ? addState.label : "";
  const addError = addOpen ? addState.error : "";
  const addBusy = addOpen && addBusyState.epoch === effectiveEpoch && addBusyState.seq === addSeq && addBusyState.busy;

  const renameSeq = liveRenameHub ? renameState.seq : 0;
  const renameLabel = liveRenameHub ? renameState.label : "";
  const renameError = liveRenameHub ? renameState.error : "";
  const renameBusy = Boolean(liveRenameHub) && renameBusyState.epoch === effectiveEpoch && renameBusyState.seq === renameSeq && renameBusyState.busy;
  const renameOpenKey = liveRenameHub ? `${effectiveEpoch}:${renameSeq}:${liveRenameHub.url}` : "";

  const removingUrls = removingState.epoch === effectiveEpoch ? removingState.urls : [];
  const switchingUrl = switchingState.epoch === effectiveEpoch ? switchingState.url : null;
  const actionError = actionErrorState.epoch === effectiveEpoch ? actionErrorState.message : "";

  const activeEpochRef = useRef(0);
  const activeAddSeqRef = useRef(0);
  const activeRenameKeyRef = useRef("");
  const dialogSeqRef = useRef(0);
  const opSeqRef = useRef(0);
  const pendingAddRef = useRef<PendingOp | null>(null);
  const pendingRenameRef = useRef<PendingOp | null>(null);
  const pendingRemoveRef = useRef<Map<string, PendingOp>>(new Map());
  const pendingSwitchRef = useRef<PendingOp | null>(null);
  const depsRef = useRef(deps);
  const listRef = useRef(list);
  const currentRef = useRef(current);
  const readScopeTokenRef = useRef(trackedScope.readToken);
  const readCurrentTokenRef = useRef(trackedCurrent.readToken);

  useLayoutEffect(() => {
    depsRef.current = deps;
    listRef.current = list;
    currentRef.current = current;
    readScopeTokenRef.current = trackedScope.readToken;
    readCurrentTokenRef.current = trackedCurrent.readToken;
  });

  useLayoutEffect(() => {
    activeEpochRef.current = effectiveEpoch;
    return () => {
      if (activeEpochRef.current === effectiveEpoch) activeEpochRef.current = 0;
      pendingAddRef.current?.controller.abort();
      pendingAddRef.current = null;
      pendingRenameRef.current?.controller.abort();
      pendingRenameRef.current = null;
      pendingRemoveRef.current.forEach((op) => op.controller.abort());
      pendingRemoveRef.current.clear();
      pendingSwitchRef.current?.controller.abort();
      pendingSwitchRef.current = null;
      opSeqRef.current += 1;
    };
  }, [effectiveEpoch]);

  useLayoutEffect(() => {
    activeAddSeqRef.current = addSeq;
    return () => {
      if (activeAddSeqRef.current === addSeq) activeAddSeqRef.current = 0;
      pendingAddRef.current?.controller.abort();
      pendingAddRef.current = null;
    };
  }, [effectiveEpoch, addSeq]);

  useLayoutEffect(() => {
    activeRenameKeyRef.current = renameOpenKey;
    return () => {
      if (activeRenameKeyRef.current === renameOpenKey) activeRenameKeyRef.current = "";
      pendingRenameRef.current?.controller.abort();
      pendingRenameRef.current = null;
    };
  }, [renameOpenKey]);

  const capturedEpoch = effectiveEpoch;
  const capturedScopeToken = trackedScope.token;
  const capturedCurrentToken = trackedCurrent.token;

  const isScopeLive = () =>
    activeEpochRef.current === capturedEpoch
    && readScopeTokenRef.current() === capturedScopeToken
    && readCurrentTokenRef.current() === capturedCurrentToken;

  const reportError = (msg: string) => {
    try {
      depsRef.current?.onError?.(msg);
    } catch {
      // Error observer failures stay non-blocking.
    }
  };

  const openAdd = () => {
    if (!isScopeLive()) return;
    pendingAddRef.current?.controller.abort();
    pendingAddRef.current = null;
    const seq = ++dialogSeqRef.current;
    setAddState({ epoch: capturedEpoch, seq, open: true, url: "", label: "", error: "" });
    setAddBusyState({ epoch: capturedEpoch, seq, busy: false });
  };

  const closeAdd = () => {
    if (!isScopeLive()) return;
    pendingAddRef.current?.controller.abort();
    pendingAddRef.current = null;
    const seq = ++dialogSeqRef.current;
    setAddState({ epoch: capturedEpoch, seq, open: false, url: "", label: "", error: "" });
    setAddBusyState({ epoch: capturedEpoch, seq, busy: false });
  };

  const handleAddUrlChange = (value: string) => {
    if (!isScopeLive()) return;
    setAddState((prev) => (prev.epoch === capturedEpoch && prev.open ? { ...prev, url: value, error: "" } : prev));
  };

  const handleAddLabelChange = (value: string) => {
    if (!isScopeLive()) return;
    setAddState((prev) => (prev.epoch === capturedEpoch && prev.open ? { ...prev, label: value, error: "" } : prev));
  };

  const saveAdd = () => {
    if (!isScopeLive() || !addOpen || pendingAddRef.current !== null) return;
    const runAddSeq = addSeq;
    const typedUrl = addUrl;
    const typedLabel = addLabel;
    const controller = new AbortController();
    const runSeq = ++opSeqRef.current;
    const isRunCurrent = () =>
      !controller.signal.aborted
      && isScopeLive()
      && activeAddSeqRef.current === runAddSeq;

    const finishSuccess = (result: AddHubResult) => {
      if (!isRunCurrent()) return;
      if (result && typeof result === "object" && "error" in result && typeof result.error === "string") {
        setAddState((prev) => (prev.epoch === capturedEpoch && prev.seq === runAddSeq ? { ...prev, error: result.error } : prev));
        return;
      }
      const normalize = depsRef.current?.normalizeURL ?? normalizeHubURL;
      const parsed = typedUrl.trim() ? normalize(typedUrl) : null;
      let addedName: string;
      if (result && typeof result === "object" && "added" in result && result.added) {
        addedName = hubTitle(result.added);
      } else if (result && typeof result === "object" && "list" in result && result.list && result.list.length > 0) {
        addedName = hubTitle(result.list[result.list.length - 1]!);
      } else if (parsed && "url" in parsed) {
        addedName = hubTitle({ url: parsed.url, ...(typedLabel.trim() ? { label: typedLabel.trim() } : {}) });
      } else {
        addedName = typedLabel.trim() || hostOf(typedUrl.trim());
      }
      safeNotify(depsRef.current?.notify, t("hubs.added", { name: addedName }));
      const nextSeq = ++dialogSeqRef.current;
      setAddState({ epoch: capturedEpoch, seq: nextSeq, open: false, url: "", label: "", error: "" });
      setAddBusyState({ epoch: capturedEpoch, seq: nextSeq, busy: false });
    };

    const finishError = (err: unknown) => {
      if (!isRunCurrent()) return;
      const msg = safeFormatError(depsRef.current?.formatError, err);
      setAddState((prev) => (prev.epoch === capturedEpoch && prev.seq === runAddSeq ? { ...prev, error: msg } : prev));
      reportError(msg);
    };

    const normalize = depsRef.current?.normalizeURL ?? normalizeHubURL;
    const validated = normalize(typedUrl);
    if ("error" in validated) {
      setAddState((prev) => (prev.epoch === capturedEpoch && prev.seq === runAddSeq ? { ...prev, error: validated.error } : prev));
      return;
    }

    setAddState((prev) => (prev.epoch === capturedEpoch && prev.seq === runAddSeq ? { ...prev, error: "" } : prev));
    let res: AddHubResult | Promise<AddHubResult>;
    try {
      res = depsRef.current?.addHub
        ? depsRef.current.addHub(typedUrl, typedLabel, controller.signal)
        : addHub(typedUrl, typedLabel);
    } catch (err) {
      finishError(err);
      return;
    }

    if (!isThenable(res)) {
      finishSuccess(res);
      return;
    }

    pendingAddRef.current = { epoch: capturedEpoch, seq: runSeq, controller };
    setAddBusyState({ epoch: capturedEpoch, seq: runAddSeq, busy: true });
    void (async () => {
      try {
        const awaited = await res;
        finishSuccess(awaited);
      } catch (err) {
        finishError(err);
      } finally {
        if (pendingAddRef.current?.seq === runSeq) pendingAddRef.current = null;
        if (activeEpochRef.current === capturedEpoch && activeAddSeqRef.current === runAddSeq) {
          setAddBusyState({ epoch: capturedEpoch, seq: runAddSeq, busy: false });
        }
      }
    })();
  };

  const openRename = (hub: SavedHub) => {
    if (!isScopeLive()) return;
    pendingRenameRef.current?.controller.abort();
    pendingRenameRef.current = null;
    const seq = ++dialogSeqRef.current;
    setRenameState({ epoch: capturedEpoch, seq, url: hub.url, label: hub.label ?? "", error: "" });
    setRenameBusyState({ epoch: capturedEpoch, seq, busy: false });
  };

  const closeRename = () => {
    if (!isScopeLive()) return;
    pendingRenameRef.current?.controller.abort();
    pendingRenameRef.current = null;
    const seq = ++dialogSeqRef.current;
    setRenameState({ epoch: capturedEpoch, seq, url: null, label: "", error: "" });
    setRenameBusyState({ epoch: capturedEpoch, seq, busy: false });
  };

  const handleRenameLabelChange = (value: string) => {
    if (!isScopeLive()) return;
    setRenameState((prev) => (prev.epoch === capturedEpoch && prev.url !== null ? { ...prev, label: value, error: "" } : prev));
  };

  const saveRename = () => {
    if (!isScopeLive() || !liveRenameHub || pendingRenameRef.current !== null) return;
    const runRenameSeq = renameSeq;
    const runKey = renameOpenKey;
    const targetUrl = liveRenameHub.url;
    const nextLabel = renameLabel;
    const controller = new AbortController();
    const runSeq = ++opSeqRef.current;
    const isRunCurrent = () =>
      !controller.signal.aborted
      && isScopeLive()
      && activeRenameKeyRef.current === runKey;

    const finishSuccess = () => {
      if (!isRunCurrent()) return;
      const nextSeq = ++dialogSeqRef.current;
      setRenameState({ epoch: capturedEpoch, seq: nextSeq, url: null, label: "", error: "" });
      setRenameBusyState({ epoch: capturedEpoch, seq: nextSeq, busy: false });
    };

    const finishError = (err: unknown) => {
      if (!isRunCurrent()) return;
      const msg = safeFormatError(depsRef.current?.formatError, err);
      setRenameState((prev) => (prev.epoch === capturedEpoch && prev.seq === runRenameSeq ? { ...prev, error: msg } : prev));
      reportError(msg);
    };

    setRenameState((prev) => (prev.epoch === capturedEpoch && prev.seq === runRenameSeq ? { ...prev, error: "" } : prev));
    let res: SavedHub[] | void | Promise<SavedHub[] | void>;
    try {
      res = depsRef.current?.renameHub
        ? depsRef.current.renameHub(targetUrl, nextLabel, controller.signal)
        : renameHub(targetUrl, nextLabel);
    } catch (err) {
      finishError(err);
      return;
    }

    if (!isThenable(res)) {
      finishSuccess();
      return;
    }

    pendingRenameRef.current = { epoch: capturedEpoch, seq: runSeq, controller };
    setRenameBusyState({ epoch: capturedEpoch, seq: runRenameSeq, busy: true });
    void (async () => {
      try {
        await res;
        finishSuccess();
      } catch (err) {
        finishError(err);
      } finally {
        if (pendingRenameRef.current?.seq === runSeq) pendingRenameRef.current = null;
        if (activeEpochRef.current === capturedEpoch && activeRenameKeyRef.current === runKey) {
          setRenameBusyState({ epoch: capturedEpoch, seq: runRenameSeq, busy: false });
        }
      }
    })();
  };

  const handleRemove = (hub: SavedHub) => {
    if (!isScopeLive() || hub.url === currentRef.current || pendingRemoveRef.current.has(hub.url)) return;
    const controller = new AbortController();
    const runSeq = ++opSeqRef.current;
    const title = hubTitle(hub);
    const isRunCurrent = () =>
      !controller.signal.aborted
      && isScopeLive()
      && pendingRemoveRef.current.get(hub.url)?.seq === runSeq;

    const finishError = (err: unknown) => {
      const msg = safeFormatError(depsRef.current?.formatError, err);
      setActionErrorState({ epoch: capturedEpoch, message: msg });
      safeNotify(depsRef.current?.notify, msg, "error");
      reportError(msg);
    };

    setActionErrorState({ epoch: capturedEpoch, message: "" });
    let res: SavedHub[] | void | Promise<SavedHub[] | void>;
    try {
      res = depsRef.current?.removeHub
        ? depsRef.current.removeHub(hub.url, controller.signal)
        : removeHub(hub.url);
    } catch (err) {
      if (isScopeLive()) finishError(err);
      return;
    }

    if (!isThenable(res)) {
      if (isScopeLive()) safeNotify(depsRef.current?.notify, t("hubs.removed", { name: title }));
      return;
    }

    pendingRemoveRef.current.set(hub.url, { epoch: capturedEpoch, seq: runSeq, controller });
    setRemovingState((prev) => ({
      epoch: capturedEpoch,
      urls: prev.epoch === capturedEpoch ? (prev.urls.includes(hub.url) ? prev.urls : [...prev.urls, hub.url]) : [hub.url],
    }));
    void (async () => {
      try {
        await res;
        if (isRunCurrent()) safeNotify(depsRef.current?.notify, t("hubs.removed", { name: title }));
      } catch (err) {
        if (isRunCurrent()) finishError(err);
      } finally {
        if (pendingRemoveRef.current.get(hub.url)?.seq === runSeq) {
          pendingRemoveRef.current.delete(hub.url);
        }
        if (activeEpochRef.current === capturedEpoch) {
          setRemovingState((prev) => (prev.epoch === capturedEpoch ? { epoch: capturedEpoch, urls: prev.urls.filter((u) => u !== hub.url) } : prev));
        }
      }
    })();
  };

  const handleSwitch = (hub: SavedHub) => {
    if (!isScopeLive() || hub.url === currentRef.current || pendingSwitchRef.current !== null) return;
    const controller = new AbortController();
    const runSeq = ++opSeqRef.current;
    const isRunCurrent = () =>
      !controller.signal.aborted
      && isScopeLive()
      && pendingSwitchRef.current?.seq === runSeq;

    const finishError = (err: unknown) => {
      const msg = safeFormatError(depsRef.current?.formatError, err);
      setActionErrorState({ epoch: capturedEpoch, message: msg });
      safeNotify(depsRef.current?.notify, msg, "error");
      reportError(msg);
    };

    setActionErrorState({ epoch: capturedEpoch, message: "" });
    let res: unknown;
    try {
      if (depsRef.current?.switchHub) {
        res = depsRef.current.switchHub(hub, controller.signal);
      } else {
        res = goToHub(hub, depsRef.current?.hubs !== undefined ? { hubs: listRef.current } : undefined);
      }
    } catch (err) {
      if (isScopeLive()) finishError(err);
      return;
    }

    if (!isThenable(res)) return;

    pendingSwitchRef.current = { epoch: capturedEpoch, seq: runSeq, controller };
    setSwitchingState({ epoch: capturedEpoch, url: hub.url });
    void (async () => {
      try {
        await res;
      } catch (err) {
        if (isRunCurrent()) finishError(err);
      } finally {
        if (pendingSwitchRef.current?.seq === runSeq) pendingSwitchRef.current = null;
        if (activeEpochRef.current === capturedEpoch) {
          setSwitchingState((prev) => (prev.epoch === capturedEpoch && prev.url === hub.url ? { epoch: capturedEpoch, url: null } : prev));
        }
      }
    })();
  };

  // the page's own hub first, the rest as the user added them
  const rows = [...list.filter((h) => h.url === current), ...list.filter((h) => h.url !== current)];
  const viewProps: HubListViewProps = {
    rows,
    current,
    actionError,
    switchingUrl,
    removingUrls,
    addOpen,
    addUrl,
    addLabel,
    addError,
    addBusy,
    renameHub: liveRenameHub,
    renameLabel,
    renameError,
    renameBusy,
    normalizeURL: deps?.normalizeURL ?? normalizeHubURL,
    onOpenAdd: openAdd,
    onCloseAdd: closeAdd,
    onAddUrlChange: handleAddUrlChange,
    onAddLabelChange: handleAddLabelChange,
    onSaveAdd: saveAdd,
    onOpenRename: openRename,
    onCloseRename: closeRename,
    onRenameLabelChange: handleRenameLabelChange,
    onSaveRename: saveRename,
    onRemove: handleRemove,
    onSwitch: handleSwitch,
  };
  if (deps?.renderView) return <>{deps.renderView(viewProps)}</>;
  return <HubListView {...viewProps} />;
}

// ── Sign-in page ───────────────────────────────────────────

/** Pure presentational view for the sign-in page's other-hubs list. */
export function OtherHubsView({ hubs, switchingUrl = null, error, onSwitch }: OtherHubsViewProps) {
  if (hubs.length === 0) return null;
  return (
    <div className="mt-6">
      <div className="mb-1.5 px-1 text-[11.5px] font-medium text-ink-3">{t("hubs.other_hubs")}</div>
      <div className="divide-y divide-line overflow-hidden rounded-card bg-surface shadow-card">
        {hubs.map((h) => {
          const switching = switchingUrl === h.url;
          return (
            <button
              key={h.url}
              type="button"
              disabled={switchingUrl !== null}
              aria-busy={switching || undefined}
              onClick={() => onSwitch(h)}
              className="flex w-full items-center gap-2.5 whitespace-nowrap px-3 py-2.5 text-left transition-colors hover:bg-hover disabled:opacity-50"
            >
              <Icon name="hub" size={14} className="shrink-0 text-ink-3" />
              <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{hubTitle(h)}</span>
              <span className="shrink-0 truncate font-mono text-[11px] text-ink-3">{hostOf(h.url)}</span>
              <Icon name="chevronRight" size={12} stroke={2} className="shrink-0 text-ink-3" />
            </button>
          );
        })}
      </div>
      {error ? <p role="alert" className="mt-1.5 px-1 text-submeta text-red">{error}</p> : null}
    </div>
  );
}

/** On the sign-in page: the other hubs, so a hub not signed in on yet is not a dead end. */
export function OtherHubs({ deps }: OtherHubsProps = {}) {
  const trackedScope = useTrackedScope(deps?.scopeKey, deps?.subscribeScopeKey);
  const currentSource = deps?.current !== undefined ? deps.current : here;
  const currentSub = deps?.current !== undefined ? deps.subscribeCurrent : undefined;
  const trackedCurrent = useTrackedScope(currentSource, currentSub);
  const list = useHubs(deps);
  const current = trackedCurrent.value;

  const hubScope = JSON.stringify([trackedScope.token, trackedCurrent.token]);
  const [scope, setScope] = useState<{ key: string; epoch: number }>(() => ({ key: hubScope, epoch: 1 }));
  const effectiveEpoch = scope.key === hubScope ? scope.epoch : scope.epoch + 1;
  if (scope.key !== hubScope) {
    setScope({ key: hubScope, epoch: effectiveEpoch });
  }

  const [switchingState, setSwitchingState] = useState<{ epoch: number; url: string | null }>({ epoch: 0, url: null });
  const [errorState, setErrorState] = useState<{ epoch: number; message: string }>({ epoch: 0, message: "" });
  const switchingUrl = switchingState.epoch === effectiveEpoch ? switchingState.url : null;
  const error = errorState.epoch === effectiveEpoch ? errorState.message : "";

  const activeEpochRef = useRef(0);
  const opSeqRef = useRef(0);
  const pendingSwitchRef = useRef<PendingOp | null>(null);
  const depsRef = useRef(deps);
  const listRef = useRef(list);
  const currentRef = useRef(current);
  const readScopeTokenRef = useRef(trackedScope.readToken);
  const readCurrentTokenRef = useRef(trackedCurrent.readToken);

  useLayoutEffect(() => {
    depsRef.current = deps;
    listRef.current = list;
    currentRef.current = current;
    readScopeTokenRef.current = trackedScope.readToken;
    readCurrentTokenRef.current = trackedCurrent.readToken;
  });

  useLayoutEffect(() => {
    activeEpochRef.current = effectiveEpoch;
    return () => {
      if (activeEpochRef.current === effectiveEpoch) activeEpochRef.current = 0;
      pendingSwitchRef.current?.controller.abort();
      pendingSwitchRef.current = null;
      opSeqRef.current += 1;
    };
  }, [effectiveEpoch]);

  const capturedEpoch = effectiveEpoch;
  const capturedScopeToken = trackedScope.token;
  const capturedCurrentToken = trackedCurrent.token;

  const isScopeLive = () =>
    activeEpochRef.current === capturedEpoch
    && readScopeTokenRef.current() === capturedScopeToken
    && readCurrentTokenRef.current() === capturedCurrentToken;

  const handleSwitch = (hub: SavedHub) => {
    if (!isScopeLive() || hub.url === currentRef.current || pendingSwitchRef.current !== null) return;
    const controller = new AbortController();
    const runSeq = ++opSeqRef.current;
    const isRunCurrent = () =>
      !controller.signal.aborted
      && isScopeLive()
      && pendingSwitchRef.current?.seq === runSeq;

    const finishError = (err: unknown) => {
      const msg = safeFormatError(depsRef.current?.formatError, err);
      setErrorState({ epoch: capturedEpoch, message: msg });
      if (depsRef.current?.notify) safeNotify(depsRef.current.notify, msg, "error");
      try {
        depsRef.current?.onError?.(msg);
      } catch {
        // Error observer failures stay non-blocking.
      }
    };

    setErrorState({ epoch: capturedEpoch, message: "" });
    let res: unknown;
    try {
      if (depsRef.current?.switchHub) {
        res = depsRef.current.switchHub(hub, controller.signal);
      } else {
        res = goToHub(hub, depsRef.current?.hubs !== undefined ? { hubs: listRef.current } : undefined);
      }
    } catch (err) {
      if (isScopeLive()) finishError(err);
      return;
    }

    if (!isThenable(res)) return;

    pendingSwitchRef.current = { epoch: capturedEpoch, seq: runSeq, controller };
    setSwitchingState({ epoch: capturedEpoch, url: hub.url });
    void (async () => {
      try {
        await res;
      } catch (err) {
        if (isRunCurrent()) finishError(err);
      } finally {
        if (pendingSwitchRef.current?.seq === runSeq) pendingSwitchRef.current = null;
        if (activeEpochRef.current === capturedEpoch) {
          setSwitchingState((prev) => (prev.epoch === capturedEpoch && prev.url === hub.url ? { epoch: capturedEpoch, url: null } : prev));
        }
      }
    })();
  };

  const others = list.filter((h) => h.url !== current);
  const viewProps: OtherHubsViewProps = {
    hubs: others,
    switchingUrl,
    error,
    onSwitch: handleSwitch,
  };
  if (deps?.renderView) return <>{deps.renderView(viewProps)}</>;
  return <OtherHubsView {...viewProps} />;
}
