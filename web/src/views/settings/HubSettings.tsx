// Settings › hub (docs/operations/hub-update.md "In the app", the app's HubSettingsPage): the hub's version and channel, checking for
// and installing a release (waiting for the assistant and the agents to be idle, or now after a confirmation), a Docker
// hub's commands, auto-update, the personad on each computer, and the history. Follows the hub's `hub_update` pushes.
import { useCallback, useEffect, useId, useRef, useState } from "react";
import { ApiError, errorText } from "../../lib/api";
import { ago } from "../../lib/format";
import { formatDate, formatNumber, t } from "../../i18n";
import { hubUpdate, setHubUpdateSettings, startHubUpdate, type HubUpdateBackup, type HubUpdateRecord, type HubUpdateStatus } from "../../lib/hub-update";
import {
  applyHubUpdateBackup, canCheckHubUpdate, cancelHubUpdate, channelTitle, checkHubUpdate, dockerSteps, hubUpdateBackups, hubUpdateHistory,
  latestWarning, recordTitle, recordTone, runningChannelOf, shouldAutoCheckAfterSourceSwitch, startLatestHubUpdate, updateFinished,
  updateGuidance, updateView, updateWorking, type UpdateGuidance,
} from "../../lib/hub-update-settings";
import { machineDeployment } from "../../lib/machine-deploy";
import { managerOf, multiManager, useRoster } from "../../lib/managers";
import { navigate } from "../../lib/router";
import { isResumed } from "../../lib/reconnect";
import { onHubEvent, useStore } from "../../lib/store";
import { Button, copyText, cx, Dialog, IconButton, Loading, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Group, Pill, Row, Switch, SettingsHint, settingsInputClass } from "./SettingsParts";
import { ExtensionDownloadMirrors } from "./ExtensionDownloadMirrors";
import { RecoveryEntry } from "./RecoverySettings";
import { HubUpdatePipeline, observeHubUpdate } from "./HubUpdatePipeline";

const twoDigits = (value: number) => String(value).padStart(2, "0");
const HISTORY_PAGE_SIZE = 5;

function updateDate(value?: string): string {
  const iso = typeof value === "string" ? value : "";
  const stamp = iso.match(/^(\d{4}-\d{2}-(\d{2}))T\d{2}:\d{2}:\d{2}/i);
  const date = new Date(iso);
  // Date normalizes impossible days such as February 30.
  if (!stamp || Number.isNaN(date.getTime()) || new Date(`${stamp[1]}T00:00:00Z`).getUTCDate() !== Number(stamp[2])) {
    return t("settings-hubsettings.unknown");
  }
  const day = `${String(date.getFullYear()).padStart(4, "0")}-${twoDigits(date.getMonth() + 1)}-${twoDigits(date.getDate())}`;
  const time = `${twoDigits(date.getHours())}:${twoDigits(date.getMinutes())}:${twoDigits(date.getSeconds())}`;
  return `${day} ${time}`;
}

function datedCommit(commit?: string, date?: string): string {
  return commit ? `${updateDate(date)} · ${commit.slice(0, 12)}` : t("settings-hubsettings.unknown");
}

export function HubSettings({ onOpenRecovery }: { onOpenRecovery?: () => void }) {
  return <><HubUpdates /><RecoveryEntry onOpen={onOpenRecovery} /><ExtensionDownloadMirrors /></>;
}

function HubUpdates() {
  const [snapshot, setSnapshot] = useState<{ status: HubUpdateStatus; pipeline: ReturnType<typeof observeHubUpdate> } | null>(null);
  const status = snapshot?.status ?? null;
  const [history, setHistory] = useState<HubUpdateRecord[]>([]);
  const [unsupported, setUnsupported] = useState(false);
  const [error, setError] = useState("");
  const [checking, setChecking] = useState(false);
  const [starting, setStarting] = useState(false);
  const [savingSource, setSavingSource] = useState(false);
  const [savingAuto, setSavingAuto] = useState(false);
  const [confirmNow, setConfirmNow] = useState(false);
  const [backups, setBackups] = useState<HubUpdateBackup[]>([]);
  const [backupError, setBackupError] = useState("");
  const [loadingBackups, setLoadingBackups] = useState(false);
  const [selectedVersion, setSelectedVersion] = useState("");
  const [confirmBackup, setConfirmBackup] = useState(false);
  const last = useRef<string | undefined>(undefined);
  const activeSource = useRef<string>("release");
  const revision = useRef(0);
  const checkSeq = useRef(0);
  const opened = useRef(false);

  const apply = useCallback((s: HubUpdateStatus) => {
    revision.current++;
    activeSource.current = s.source ?? "release";
    const finished = updateFinished(last.current, s.state);
    last.current = s.state;
    const now = Date.now();
    setSnapshot(prev => ({ status: s, pipeline: observeHubUpdate(prev?.pipeline, s, now) }));
    if (finished) hubUpdateHistory().then(setHistory, () => {});
  }, []);

  const load = useCallback(() => {
    const before = revision.current;
    setError("");
    return hubUpdate()
      .then((s) => {
        if (revision.current === before) apply(s);
        setError("");
        hubUpdateHistory().then(setHistory, () => {});
      })
      .catch((e) => {
        if (revision.current !== before) return;
        if (e instanceof ApiError && e.status === 404) setUnsupported(true);
        else setError(errorText(e));
      });
  }, [apply]);

  const loadBackups = useCallback(async () => {
    setLoadingBackups(true);
    try {
      const versions = await hubUpdateBackups();
      setBackups(versions);
      setSelectedVersion(id => versions.some(v => v.id === id) ? id : "");
      setBackupError("");
    } catch (e) { setBackupError(errorText(e)); }
    finally { setLoadingBackups(false); }
  }, []);

  useEffect(() => {
    if (status?.source === "web-dev") {
      if (!updateWorking(status)) void loadBackups();
    }
    else { setBackups([]); setSelectedVersion(""); setConfirmBackup(false); }
    // The effective folder too: after a change the list must come from the new
    // one, or a selection would name a snapshot the hub cannot find there.
  }, [status?.source, status?.state, status?.backup?.id, status?.development?.backup_directory, loadBackups]);

  useEffect(() => {
    void load();
    const off = onHubEvent("hub_update", (m) => m.update && apply(m.update as HubUpdateStatus));
    // a reconnect the hub could not resume (a restart into the new version, often): the hub_update pushes missed
    // meanwhile (confirming, updated, rolled back) are not replayed (#4954)
    const offHello = onHubEvent("hello", (m) => !isResumed(m) && void load());
    return () => {
      checkSeq.current++;
      off();
      offHello();
    };
  }, [apply, load]);

  const runCheck = useCallback(async (preview: boolean, expectedSource?: string) => {
    const seq = ++checkSeq.current;
    setChecking(true);
    try {
      const next = await checkHubUpdate(preview);
      if (checkSeq.current !== seq) return;
      if (expectedSource && (activeSource.current !== expectedSource || (next.source ?? "release") !== expectedSource)) return;
      if (last.current && updateWorking({ state: last.current }) && !updateWorking(next)) return;
      apply(next);
    } catch (e) {
      if (checkSeq.current === seq && (!expectedSource || activeSource.current === expectedSource)) {
        toast(t("settings-hubsettings.check_failed", { error: errorText(e) }), "error");
      }
    } finally {
      if (checkSeq.current === seq) setChecking(false);
    }
  }, [apply]);

  useEffect(() => {
    if (!status || opened.current) return;
    opened.current = true;
    if (status.source !== "web-dev" || !status.supports_preview || status.state === "checking" || updateWorking(status)) return;
    void runCheck(true, "web-dev");
  }, [runCheck, status]);

  const phase = status?.state;
  const streaming = phase === "resolving" || phase === "building" || phase === "backing_up" || phase === "applying_backup";
  useEffect(() => {
    if (!streaming) return;
    let stopped = false;
    let timer: number;
    // Phase transitions arrive by push. Read the counters between pushes and
    // ignore any reply overtaken by a newer state or an unmount.
    async function poll() {
      const before = revision.current;
      try {
        const next = await hubUpdate();
        if (!stopped && before === revision.current) apply(next);
      } catch {
        // Keep the last confirmed progress during a brief connection loss.
      }
      if (!stopped) timer = window.setTimeout(poll, 1000);
    }
    timer = window.setTimeout(poll, 1000);
    return () => {
      stopped = true;
      window.clearTimeout(timer);
    };
  }, [apply, streaming]);

  const check = async () => {
    if (!status || !canCheckHubUpdate(status)) return;
    await runCheck(false, status.source ?? "release");
  };

  const update = async (now: boolean) => {
    checkSeq.current++;
    setChecking(false);
    setStarting(true);
    try {
      const before = revision.current;
      const next = !now && status?.source === "web-dev" && status.supports_preview
        ? await startLatestHubUpdate() : await startHubUpdate(now);
      if (revision.current === before) apply(next);
      setConfirmNow(false);
    } catch (e) {
      toast(t("settings-hubsettings.start_failed", { error: errorText(e) }), "error");
    } finally {
      setStarting(false);
    }
  };

  const cancel = async () => {
    const before = revision.current;
    try {
      const next = await cancelHubUpdate();
      if (before === revision.current) apply(next);
    } catch (e) {
      toast(errorText(e), "error");
      void load();
    }
  };

  const restore = async () => {
    checkSeq.current++;
    setChecking(false);
    setStarting(true);
    try {
      apply(await applyHubUpdateBackup(selectedVersion));
      setConfirmBackup(false);
    } catch (e) { toast(t("settings-hubsettings.start_failed", { error: errorText(e) }), "error"); }
    finally { setStarting(false); }
  };

  const setAuto = async (on: boolean, computers = false) => {
    setSavingAuto(true);
    try {
      apply(await setHubUpdateSettings(computers ? { auto_update_nodes: on } : { auto: on }));
    } catch (e) {
      toast(t("settings-hubsettings.save_failed", { error: errorText(e) }), "error");
    } finally { setSavingAuto(false); }
  };
  const setSource = async (nextSource: "release" | "beta" | "web-dev") => {
    const seq = ++checkSeq.current;
    setChecking(false);
    setSavingSource(true);
    let updated: HubUpdateStatus | undefined;
    try {
      updated = await setHubUpdateSettings({ source: nextSource });
      if (checkSeq.current !== seq) return;
      apply(updated);
    } catch (e) {
      if (checkSeq.current === seq) toast(t("settings-hubsettings.save_failed", { error: errorText(e) }), "error");
      return;
    } finally {
      if (checkSeq.current === seq) setSavingSource(false);
    }
    if (updated && shouldAutoCheckAfterSourceSwitch(updated)) {
      await runCheck(true, updated.source ?? "release");
    }
  };

  if (unsupported) {
    return (
      <Group>
        <p className="px-4 py-3 text-[12.5px] leading-relaxed text-ink-3">{t("settings-hubsettings.unsupported")}</p>
      </Group>
    );
  }
  if (!status) {
    return error ? (
      <div className="flex items-center gap-3 px-1">
        <p role="alert" className="min-w-0 flex-1 text-[13px] break-words text-red">{t("settings-hubsettings.version_error", { error })}</p>
        <Button icon="retry" onClick={() => void load()}>{t("settings-hubsettings.retry")}</Button>
      </div>
    ) : (
      <Loading />
    );
  }

  const working = updateWorking(status);
  const view = updateView(status);
  const canCheck = canCheckHubUpdate(status);
  const source = status.source ?? "release";
  const runningChannel = runningChannelOf(status);
  const showChannel = !status.development || runningChannel !== source;
  const showUpdateVersion = source === "web-dev" && status.can_update;
  const run = snapshot?.pipeline;
  const activeBackup = run?.backupId;
  const selectedID = run && !run.restoringBackup ? "" : activeBackup || selectedVersion;
  const selectedBackup = source === "web-dev" ? backups.find(v => v.id === selectedID) : undefined;
  const release = source !== "web-dev" && view.kind !== "docker" ? status.latest : undefined;
  const released = release?.released;
  const releaseTime = released && released !== "0001-01-01T00:00:00Z" ? formatDate(released, { dateStyle: "medium", timeStyle: "short" }) : "";
  const candidate = (!selectedBackup || activeBackup) && (view.kind === "newer" || showUpdateVersion) ? status.latest : undefined;
  const warning = candidate && latestWarning(status);
  const guidance = updateGuidance(status, view);

  return (
    <>
      {status.auto_notice && (
        <div className="mb-6 flex items-center gap-3 rounded-card bg-accent-tint px-4 py-2.5 text-[12.5px] text-accent-ink">
          <Icon name="info" size={14} className="shrink-0" />
          <span className="min-w-0 flex-1">{t("settings-hubsettings.auto_notice")}</span>
          <Button tone="quiet" onClick={() => void setAuto(false)} className="h-7">{t("settings-hubsettings.turn_off")}</Button>
          <Button tone="quiet" onClick={() => void setHubUpdateSettings({ auto_notice_seen: true }).then(apply, (e) => toast(errorText(e), "error"))} className="h-7">
            {t("settings-hubsettings.got_it")}
          </Button>
        </div>
      )}

      <Group title={t("settings-hubsettings.version")} anchor="hub-version">
        <Row label={t("settings-hubsettings.version")} mono stackOnNarrow>
          <span className="max-w-[320px] truncate" title={status.version}>{status.version}</span>
        </Row>
        {showChannel && <Row label={t("settings-hubsettings.channel")} anchor="hub-channel">{channelTitle(runningChannel)}</Row>}
        {status.development && <>
          <Row label={t("settings-hubsettings.source")} anchor={showChannel ? undefined : "hub-channel"} stackOnNarrow>
            <select aria-label={t("settings-hubsettings.source")} className={settingsInputClass}
              value={source} disabled={working || starting || savingSource || savingAuto}
              onChange={(e) => void setSource(e.target.value as "release" | "beta" | "web-dev")}>
              <option value="release">{t("lib-hub-update.channel_release")}</option>
              <option value="beta">{t("settings-hubsettings.beta_channel")}</option>
              <option value="web-dev" disabled={!status.development.configured}>{t("settings-hubsettings.web_dev")}</option>
            </select>
          </Row>
          {!status.development.configured && <SettingsHint>{status.development.why || t("settings-hubsettings.dev_setup")}</SettingsHint>}
        </>}
        {status.source === "beta" && <SettingsHint>{t("settings-hubsettings.beta_scope")}</SettingsHint>}
        {status.source === "web-dev" && <>
          <SettingsHint>{t("settings-hubsettings.dev_scope")}</SettingsHint>
          <Row label={t("settings-hubsettings.branch")} wrapValue>{status.development?.branch}</Row>
          <Row label={t("settings-hubsettings.current_commit")} mono wrapValue>
            <span className="break-normal tabular-nums">{datedCommit(status.development?.current_commit, status.development?.current_updated_at)}</span>
          </Row>
          <Row label={t("settings-hubsettings.latest_commit")} mono wrapValue>
            <span className="break-normal tabular-nums">{status.latest?.backup_id ? t("settings-hubsettings.unknown") : datedCommit(status.latest?.commit, status.latest?.updated_at)}</span>
          </Row>
        </>}
        {status.last_check && <Row label={t("settings-hubsettings.last_check")}>{ago(status.last_check)}</Row>}
        <div className="flex flex-wrap items-center justify-end gap-x-3 gap-y-2 px-4 py-2.5">
          {!canCheck && (
            <span className="mr-auto min-w-0 text-submeta leading-snug text-ink-3">
              {t("settings-hubsettings.check_unavailable")}
            </span>
          )}
          <Button icon="retry" busy={checking} disabled={!canCheck || checking || working || savingSource}
            title={!canCheck ? t("settings-hubsettings.check_unavailable") : undefined}
            onClick={() => void check()}>
            {checking ? t("settings-hubsettings.checking") : t("settings-hubsettings.check")}
          </Button>
        </div>
      </Group>

      <Group title={t("settings-hubsettings.update")} anchor="hub-update" footer={status.can_update ? t("settings-hubsettings.update_footer") : undefined}>
        {showUpdateVersion && <>
          <UpdateVersionRow latest={status.latest} backups={backups} activeBackup={activeBackup} selectedID={selectedID}
            disabled={working || checking || starting || savingSource || loadingBackups} onSelect={setSelectedVersion} />
          {backupError && <div className="flex items-center gap-3 px-4 py-2.5">
            <p role="alert" className="min-w-0 flex-1 text-label break-words text-red">{t("settings-hubsettings.backup_error", { error: backupError })}</p>
            <Button disabled={loadingBackups} onClick={() => void loadBackups()}>{t("settings-hubsettings.retry")}</Button>
          </div>}
          {selectedBackup && <>
            <Row label={t("settings-hubsettings.target_commit")} mono wrapValue>
              <span className="break-normal tabular-nums">{datedCommit(selectedBackup.commit, selectedBackup.updated_at)}</span>
            </Row>
            <Row label={t("settings-hubsettings.backup_created_at")} wrapValue>
              <span className="break-normal tabular-nums">{updateDate(selectedBackup.created_at)}</span>
            </Row>
            <SettingsHint>{t("settings-hubsettings.program_backup_hint")}</SettingsHint>
            {!working && <div className="flex justify-end px-4 py-2.5">
              <Button tone="accent" busy={starting} disabled={starting || checking || savingSource || loadingBackups} onClick={() => setConfirmBackup(true)}>{t("settings-hubsettings.apply_backup")}</Button>
            </div>}
          </>}
        </>}
        {view.kind === "docker" && <DockerRows s={status} />}
        {(release || candidate?.notes || warning) && (
          <div className="space-y-1 px-4 py-3">
            {release && (
              <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
                <span className="min-w-0 break-words text-meta font-medium text-ink">
                  {t(view.kind === "newer" ? "settings-hubsettings.new_version" : "settings-hubsettings.latest_release", { version: release.version })}
                </span>
                {releaseTime && <time dateTime={released} className="text-submeta text-ink-3">{t("settings-hubsettings.released", { time: releaseTime })}</time>}
              </div>
            )}
            {candidate?.notes && <p className="text-label leading-relaxed break-words text-ink-2">{candidate.notes}</p>}
            {warning && (
              <p className="flex items-start gap-1.5 text-label leading-snug text-orange">
                <Icon name="info" size={13} className="mt-px shrink-0" />
                <span className="min-w-0 break-words">{warning}</span>
              </p>
            )}
          </div>
        )}
        {!selectedBackup && view.kind === "newer" && !working && (
          <div className="flex justify-end px-4 py-2.5">
            <Button tone="accent" busy={starting} disabled={starting || checking || status.state === "checking"} onClick={() => void update(false)}>
              {t("settings-hubsettings.update_hub")}
            </Button>
          </div>
        )}
        {status.state === "resolving" && !run && <div role="status"><SettingsHint>{t("settings-hubsettings.resolving_latest")}</SettingsHint></div>}
        {run && view.kind !== "docker" && (
          <HubUpdatePipeline s={status} run={run} onCancel={cancel} onNow={() => setConfirmNow(true)} />
        )}
        {guidance && <UpdateGuidanceCallout guidance={guidance} />}
        {view.kind === "unchecked" && source !== "web-dev" && !status.error && (
          <div role="status"><SettingsHint>{t(checking || status.state === "checking" ? "settings-hubsettings.checking" : "settings-hubsettings.unchecked")}</SettingsHint></div>
        )}
        {!selectedBackup && view.kind === "current" && (source !== "web-dev" || status.last_check) && !status.error && (
          <div role="status" className="flex items-center gap-2 px-4 py-3 text-meta text-green">
            <Icon name="check" size={14} stroke={2.4} />
            {t("settings-hubsettings.up_to_date")}
          </div>
        )}
        {status.error && !working && <p role={status.state === "rolled_back" ? "status" : "alert"} className={cx("px-4 py-2.5 text-label break-words", status.state === "rolled_back" ? "text-orange" : "text-red")}>{status.error}</p>}
      </Group>

      {status.source === "web-dev" && status.development && <DevelopmentBackup s={status} onSaved={apply} />}

      {status.can_update && (
        <Group footer={t(status.source === "web-dev" ? "settings-hubsettings.dev_auto_footer" : "settings-hubsettings.auto_footer")} anchor="hub-auto-update">
          <Row label={t("settings-hubsettings.auto_update")}>
            <Switch on={status.auto} disabled={savingAuto || savingSource} onChange={(on) => void setAuto(on)} label={t("settings-hubsettings.auto_update")} />
          </Row>
        </Group>
      )}
      {status.source === "web-dev" && status.auto_update_nodes !== undefined && <Group footer={t("settings-hubsettings.computer_auto_footer")}>
        <Row label={t("settings-hubsettings.computer_auto")} wrapHint>
          <Switch on={status.auto_update_nodes} disabled={working || savingSource || savingAuto} label={t("settings-hubsettings.computer_auto")}
            onChange={(on) => void setAuto(on, true)} />
        </Row>
      </Group>}

      <Nodes development={status.source === "web-dev"} />

      <UpdateHistory records={history} />

      <Dialog open={confirmBackup} onClose={() => setConfirmBackup(false)} title={t("settings-hubsettings.apply_backup")} width={400}
        footer={<>
          <Button tone="quiet" onClick={() => setConfirmBackup(false)}>{t("common.cancel")}</Button>
          <Button tone="accent" busy={starting} disabled={starting || working || !selectedBackup} onClick={() => void restore()}>{t("settings-hubsettings.apply_backup")}</Button>
        </>}>
        <p className="text-meta leading-relaxed text-ink-2">{t("settings-hubsettings.apply_backup_confirm")}</p>
      </Dialog>

      <Dialog
        open={confirmNow}
        onClose={() => setConfirmNow(false)}
        title={t("settings-hubsettings.confirm_title")}
        width={400}
        footer={
          <>
            <Button tone="quiet" onClick={() => setConfirmNow(false)}>{t("common.cancel")}</Button>
            <Button tone="danger" busy={starting} onClick={() => void update(true)}>{t("settings-hubsettings.update_now")}</Button>
          </>
        }
      >
        <p className="text-[12.5px] leading-relaxed text-ink-2">
          {t("settings-hubsettings.confirm_body")}
        </p>
      </Dialog>
    </>
  );
}

function UpdateGuidanceCallout({ guidance }: { guidance: UpdateGuidance }) {
  const caution = guidance.tone === "caution";
  return (
    <div className="px-4 py-3">
      <div
        role="status"
        data-update-guidance={guidance.tone}
        className={cx(
          "flex items-start gap-2.5 rounded-control px-3 py-2.5",
          caution ? "bg-orange-tint text-ink" : "bg-inset text-ink shadow-hairline",
        )}
      >
        <Icon name="info" size={14} className={cx("mt-0.5 shrink-0", caution ? "text-orange" : "text-ink-3")} />
        <div className="min-w-0 flex-1 space-y-1">
          <p className={cx("text-meta font-medium leading-snug break-words", caution ? "text-orange" : "text-ink")}>{guidance.title}</p>
          <p className="text-label leading-relaxed break-words text-ink-2">{guidance.body}</p>
          {guidance.nextStep && <p className="text-label leading-relaxed break-words text-ink-3">{guidance.nextStep}</p>}
        </div>
      </div>
    </div>
  );
}

function UpdateHistory({ records }: { records: HubUpdateRecord[] }) {
  const [page, setPage] = useState(1);
  const pageCount = Math.max(1, Math.ceil(records.length / HISTORY_PAGE_SIZE));
  const currentPage = Math.min(page, pageCount);
  if (page !== currentPage) setPage(currentPage);
  if (records.length === 0) return null;

  const start = (currentPage - 1) * HISTORY_PAGE_SIZE;
  const title = t("settings-hubsettings.history");
  return (
    <Group title={title}>
      {records.slice(start, start + HISTORY_PAGE_SIZE).map((record, index) => {
        const time = record.ended || record.started;
        const commit = record.commit?.slice(0, 12);
        const showCommit = commit && !record.to_version.toLowerCase().includes(commit.toLowerCase());
        const tone = { neutral: "text-ink-2", green: "text-green", orange: "text-orange", red: "text-red" }[recordTone(record.result)];
        const label = record.result === "held" ? t("settings-hubsettings.history_held")
          : record.result === "failed" ? t("settings-hubsettings.history_failed")
            : recordTitle(record.result);
        return (
          <div key={`${record.to_build}/${record.started ?? start + index}`} className="px-2.5 py-2 sm:px-4">
            <div className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-1">
              <div className="flex min-h-5 flex-wrap items-center gap-x-1 gap-y-0.5">
                <span className={cx("text-label font-medium sm:text-meta", tone)}>{label}</span>
                {record.auto && <Pill size="sm">{t("settings-hubsettings.auto")}</Pill>}
                {record.source === "web-dev" && <Pill size="sm">{t("settings-hubsettings.web_dev")}</Pill>}
              </div>
              <time dateTime={time} className="whitespace-nowrap text-submeta leading-5 text-ink-3 tabular-nums max-sm:tracking-tight">{updateDate(time)}</time>
            </div>
            <div className="mt-0.5 flex gap-1 font-mono text-submeta text-ink-3">
              <span className="truncate" title={record.from_version}>{record.from_version}</span>
              {record.to_version && <span className="shrink-0">{" → "}</span>}
              <span className="truncate" title={record.to_version}>{record.to_version}</span>
            </div>
            {showCommit && <div className="mt-0.5 font-mono text-submeta text-ink-3">{commit}</div>}
            {record.reason && <p className="mt-0.5 text-submeta leading-snug text-ink-3 wrap-anywhere">{record.reason}</p>}
          </div>
        );
      })}
      {pageCount > 1 && (
        <nav aria-label={title} className="flex flex-wrap items-center justify-between gap-x-2 px-2.5 py-1 sm:px-4">
          <span role="status" className="text-submeta text-ink-3 tabular-nums">
            {t("settings-hubsettings.history_page", {
              page: formatNumber(currentPage), pages: formatNumber(pageCount), total: formatNumber(records.length),
            })}
          </span>
          <div className="ml-auto flex items-center gap-1">
            <Button tone="quiet" size="sm" className="pointer-coarse:min-h-11"
              disabled={currentPage === 1} onClick={() => setPage(currentPage - 1)}>{t("settings-hubsettings.history_previous")}</Button>
            <Button tone="quiet" size="sm" className="pointer-coarse:min-h-11"
              disabled={currentPage === pageCount} onClick={() => setPage(currentPage + 1)}>{t("settings-hubsettings.history_next")}</Button>
          </div>
        </nav>
      )}
    </Group>
  );
}

function UpdateVersionRow({ latest, backups, activeBackup, selectedID, disabled, onSelect }: {
  latest?: HubUpdateStatus["latest"];
  backups: HubUpdateBackup[];
  activeBackup?: string;
  selectedID: string;
  disabled: boolean;
  onSelect: (id: string) => void;
}) {
  const mainLabel = t("settings-hubsettings.latest_main");
  const backupLabel = t("settings-hubsettings.backup_version");
  const options = [
    { id: "", label: latest?.commit && !latest.backup_id ? `${mainLabel} · ${datedCommit(latest.commit, latest.updated_at)}` : mainLabel },
    ...backups.map(v => ({ id: v.id, label: `${backupLabel} · ${datedCommit(v.commit, v.updated_at)}` })),
  ];
  if (activeBackup && !backups.some(v => v.id === activeBackup)) {
    options.push({ id: activeBackup, label: `${backupLabel} · ${datedCommit(latest?.commit, latest?.updated_at)}` });
  }
  const selected = options.find(option => option.id === selectedID) ?? options[0];
  return (
    <Row label={t("settings-hubsettings.update_version")} stackOnNarrow>
      <div className="relative w-full min-w-0 max-w-full">
        <select aria-label={t("settings-hubsettings.update_version")}
          className={cx(settingsInputClass, "peer min-w-0 max-w-full tabular-nums max-sm:absolute max-sm:inset-0 max-sm:h-full max-sm:opacity-0 max-sm:disabled:opacity-0")}
          value={selected.id} disabled={disabled} onChange={e => onSelect(e.target.value)}>
          {options.map(option => <option key={option.id} value={option.id}>{option.label}</option>)}
        </select>
        {/* Native select text is single-line. Paint its selected option with wrapping on narrow screens. */}
        <span aria-hidden className="pointer-events-none flex min-h-11 items-center gap-2 rounded-control bg-field px-2.5 py-1.5 text-label text-ink shadow-inset-field tabular-nums peer-focus:shadow-[inset_0_0_0_1px_var(--accent)] peer-disabled:opacity-60 sm:hidden">
          <span className="min-w-0 flex-1 break-words">{selected.label.replaceAll(" · ", " ·\u00a0")}</span>
          <Icon name="chevron" size={12} className="shrink-0 text-ink-3" />
        </span>
      </div>
    </Row>
  );
}

/** Web development backups: where snapshots go and how many are kept. Only a
 *  folder and a count — the hub pauses its own writers; no command is accepted. */
function DevelopmentBackup({ s, onSaved }: { s: HubUpdateStatus; onSaved: (s: HubUpdateStatus) => void }) {
  const d = s.development!;
  const fieldId = useId();
  const folderInput = useRef<HTMLInputElement>(null);
  const recommended = d.backup_default_directory ?? "";
  const savedDir = savedBackupFolder(d);
  const savedRetain = d.backup_retain ?? 3;
  const [dir, setDir] = useState(savedDir);
  const [retain, setRetain] = useState(savedRetain);
  const [saving, setSaving] = useState(false);
  const trimmedDir = dir.trim();
  const customDir = trimmedDir !== "" && trimmedDir !== recommended;
  const nextDir = customDir ? trimmedDir : "";
  const dirty = nextDir !== savedDir || retain !== savedRetain;

  useEffect(() => {
    setDir(savedDir);
    setRetain(savedRetain);
  }, [savedDir, savedRetain]);

  const save = async () => {
    if (!dirty || saving) return;
    setSaving(true);
    try {
      const payload: Parameters<typeof setHubUpdateSettings>[0] = { development_backup_retain: retain };
      if (nextDir !== savedDir) payload.development_backup_directory = nextDir;
      const updated = await setHubUpdateSettings(payload);
      if (updated.development) {
        setDir(savedBackupFolder(updated.development));
        setRetain(updated.development.backup_retain ?? 3);
      }
      onSaved(updated);
      toast(t("settings-hubsettings.backup_saved"), "ok");
    } catch (e) {
      toast(t("settings-hubsettings.save_failed", { error: errorText(e) }), "error");
    } finally { setSaving(false); }
  };
  return (
    <form onSubmit={(event) => { event.preventDefault(); void save(); }}>
      <Group title={t("settings-hubsettings.dev_backup")} anchor="hub-dev-backup" footer={t("settings-hubsettings.dev_backup_footer")}>
        <div className="space-y-2 px-4 py-2.5">
          <div>
            <label htmlFor={fieldId} className="text-meta text-ink">{t("settings-hubsettings.backup_folder")}</label>
            <p id={`${fieldId}-folder-hint`} className="mt-0.5 text-submeta leading-snug text-ink-3">
              {t("settings-hubsettings.backup_folder_hint")}
            </p>
          </div>
          <div className="relative grid min-h-8 pointer-coarse:min-h-11">
            <input ref={folderInput} id={fieldId} aria-describedby={`${fieldId}-folder-hint`}
              spellCheck={false} autoCapitalize="none" autoComplete="off" disabled={saving}
              className={cx(settingsInputClass, "absolute inset-0 h-full! font-mono placeholder:text-transparent!")}
              placeholder={recommended} value={dir} onChange={(e) => setDir(e.target.value)} />
            {/* Give the empty field room to show the whole recommended path. */}
            {dir === "" && (
              <span aria-hidden="true" className="pointer-events-none relative self-center px-2.5 py-1.5 font-mono text-label leading-snug text-ink-3 wrap-anywhere">
                {recommended}
              </span>
            )}
          </div>
        </div>
        <div>
          <Row label={t("settings-hubsettings.backup_retain")} wrapHint
            hint={<span id={`${fieldId}-retain-hint`}>{t(d.builtin_quiesce ? "settings-hubsettings.backup_builtin" : "settings-hubsettings.backup_external")}</span>}>
            <select aria-label={t("settings-hubsettings.backup_retain")} aria-describedby={`${fieldId}-retain-hint`}
              className={cx(settingsInputClass, "min-w-11 pointer-coarse:min-h-11")} value={retain} disabled={saving}
              onChange={(e) => setRetain(Number(e.target.value))}>
              {[...new Set([1, 2, 3, 5, 10, 20, 50, savedRetain])].sort((a, b) => a - b).map((n) => <option key={n} value={n}>{n}</option>)}
            </select>
          </Row>
          <div className="flex flex-wrap justify-end gap-1.5 px-4 pb-2.5">
            {customDir && <Button tone="quiet" size="sm" className="pointer-coarse:min-h-11" disabled={saving}
              onClick={() => { setDir(""); folderInput.current?.focus(); }}>{t("settings-hubsettings.use_recommended")}</Button>}
            <Button type="submit" tone="accent" size="sm" className="pointer-coarse:min-h-11"
              busy={saving} disabled={!dirty || saving}>{t("settings-hubsettings.save_backup")}</Button>
          </div>
        </div>
      </Group>
    </form>
  );
}

function savedBackupFolder(d: NonNullable<HubUpdateStatus["development"]>): string {
  const recommended = d.backup_default_directory ?? "";
  const effective = d.backup_directory ?? recommended;
  const isDefault = d.backup_directory_is_default ?? (effective === recommended);
  return isDefault ? "" : effective;
}

function DockerRows({ s }: { s: HubUpdateStatus }) {
  const { headline, steps } = dockerSteps(s);
  return (
    <div className="px-4 py-3">
      {headline && <div className="mb-1.5 text-[13.5px] font-medium text-ink">{headline}</div>}
      {steps.map((st) => (
        <div key={st.copy} className="mb-2 last:mb-0">
          <p className="text-[12.5px] leading-relaxed text-ink-3">{st.text}</p>
          <div className="mt-1 flex items-center gap-2 rounded-control bg-inset px-2.5 py-1.5 shadow-hairline">
            <code className="min-w-0 flex-1 truncate font-mono text-[12px] text-ink select-all">{st.copy}</code>
            <IconButton icon="copy" label={t("settings-hubsettings.copy")} size={24} iconSize={12} onClick={() => void copyText(st.copy).then(() => toast(t("settings-hubsettings.copied"), "ok"))} />
          </div>
        </div>
      ))}
    </div>
  );
}

function Nodes({ development }: { development: boolean }) {
  const machines = useStore((s) => s.machines);
  const roster = useRoster();
  if (!machines.length) return null;
  const showOwners = multiManager(roster);
  return (
    <Group title={t("settings-hubsettings.nodes")} anchor="hub-nodes"
      footer={t(development ? "settings-hubsettings.dev_nodes_footer" : "settings-hubsettings.nodes_footer")}>
      {machines.map((m) => {
        const deployment = machineDeployment(m);
        const owner = showOwners && m.manager ? managerOf(roster, m.manager) : null;
        const source = m.deploy_source === "hub_package" ? t("settings-hubsettings.hub_package")
          : m.deploy_source === "local_build" ? t("settings-hubsettings.local_build") : "";
        const detail = [owner && t("settings-hubsettings.managed_by", { name: owner.name }), source].filter(Boolean).join(" · ");
        const reason = !deployment.busy && m.can_deploy !== true && m.deploy_reason !== m.deploy_error ? m.deploy_reason : undefined;
        const offline = !m.online && t("settings-hubsettings.offline");
        const version = m.node_version || offline || t("settings-hubsettings.unknown");
        const badge = deployment.busy ? <Pill tone="accent">{deployment.label}</Pill>
          : m.node_mismatch ? <Pill tone="orange">{t("settings-hubsettings.version_differs")}</Pill>
          : m.node_version && offline && <Pill>{offline}</Pill>;
        return (
          <Row key={m.id} onClick={() => navigate({ name: "machine", mid: m.id })} wrapHint
            label={<span className="flex flex-wrap items-center gap-x-2 gap-y-1" aria-live="polite">
              <span className="min-w-0 truncate">{m.name}</span>
              {badge}
              <span className="min-w-0 truncate font-mono text-meta text-ink-3 sm:ml-auto" title={m.node_version}>{version}</span>
            </span>}
            hint={(detail || deployment.error || reason) && <span className="block whitespace-normal break-words">
              {detail && <span className="block">{detail}</span>}
              {deployment.error && <span className="block text-red">{deployment.error}</span>}
              {reason && <span className="block">{reason}</span>}
            </span>} />
        );
      })}
    </Group>
  );
}
