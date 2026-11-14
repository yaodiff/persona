// Settings › Assistant › Skills: existing discovery plus bounded, explicitly confirmed ZIP imports.
import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { t } from "../../i18n";
import { credentialOwner, errorText, subscribeCredentialOwner } from "../../lib/api";
import type { Skill } from "../../lib/hub-update";
import { useShownManager } from "../../lib/manager-choice";
import { importSkill, readSkillZIP, skillImportError, skillImportList, type SkillImportResult } from "../../lib/skill-import";
import { Button, Dialog, Loading } from "../../ui/atoms";
import { Group, Pill, Row, RowFailed, SettingsHint } from "./SettingsParts";

type ImportView = {
  phase: "select" | "checking" | "preview" | "installing" | "success" | "undoing" | "undone";
  archive?: string;
  result?: SkillImportResult;
  error?: string;
};

export function SkillsGroup({ manager }: { manager?: string } = {}) {
  const shown = useShownManager();
  const owner = useSyncExternalStore(subscribeCredentialOwner, credentialOwner);
  return <SkillsForManager key={JSON.stringify([manager ?? shown, owner])} manager={manager ?? shown} owner={owner} />;
}

function SkillsForManager({ manager, owner }: { manager: string; owner: string }) {
  const [list, setList] = useState<Skill[] | null>(null);
  const [machine, setMachine] = useState<string>();
  const [error, setError] = useState("");
  const [view, setView] = useState<ImportView | null>(null);
  const [lastInstall, setLastInstall] = useState<SkillImportResult>();
  const active = useRef(true);
  const version = useRef(0);
  const request = useRef<AbortController | null>(null);
  const reading = useRef<AbortController | null>(null);
  const admitted = useRef(false);
  const uploadButton = useRef<HTMLDivElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const previewBody = useRef<HTMLDivElement>(null);
  const current = () => active.current && credentialOwner() === owner;

  async function refresh() {
    reading.current?.abort();
    const controller = new AbortController();
    reading.current = controller;
    try {
      const r = await skillImportList(manager, controller.signal, current);
      if (!current() || controller.signal.aborted) return;
      setList(r.skills ?? []); setMachine(r.machine || r.machine_id); setError("");
    } catch (e) {
      if (!current() || controller.signal.aborted) return;
      setError(errorText(e)); setList([]);
    }
  }
  useEffect(() => {
    active.current = true;
    void refresh();
    return () => { active.current = false; version.current++; request.current?.abort(); reading.current?.abort(); };
  }, [manager, owner]);
  useEffect(() => {
    if (view?.phase === "preview") previewBody.current?.querySelector<HTMLButtonElement>("button")?.focus();
  }, [view?.phase]);

  function close() {
    if (admitted.current) return;
    const result = view?.phase === "preview" ? view.result : undefined;
    version.current++;
    request.current?.abort();
    setView(null);
    if (result && current()) {
      void importSkill(manager, { op: "cancel", reference: result.reference, environment: result.environment }, undefined, current).catch(() => {});
    }
    uploadButton.current?.querySelector("button")?.focus();
  }
  async function check(file?: File) {
    if (!file || admitted.current) return;
    const n = ++version.current;
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setView({ phase: "checking" });
    try {
      const archive = await readSkillZIP(file, controller.signal);
      const result = await importSkill(manager, { op: "preview", archive }, controller.signal, current);
      if (current() && n === version.current) setView({ phase: "preview", archive, result });
    } catch (e) {
      if (current() && n === version.current && !controller.signal.aborted) setView({ phase: "select", error: skillImportError(e) });
    }
  }
  async function write(op: "install" | "undo", result: SkillImportResult, archive?: string) {
    if (admitted.current) return;
    admitted.current = true;
    const n = ++version.current;
    const controller = new AbortController();
    request.current = controller;
    setView({ phase: op === "install" ? "installing" : "undoing", result });
    try {
      const out = await importSkill(manager, { op, archive, reference: result.reference, environment: result.environment }, controller.signal, current);
      if (!current() || n !== version.current) return;
      if (op === "install") setLastInstall(out);
      else setLastInstall(undefined);
      setView({ phase: op === "install" ? "success" : "undone", result: out });
    } catch (e) {
      if (current() && n === version.current && !controller.signal.aborted) {
        setView({ phase: "select", error: skillImportError(e) });
      }
    } finally {
      admitted.current = false;
      // A lost write response is not evidence that nothing happened. Reconcile through the existing list.
      if (current()) void refresh();
    }
  }
  const busy = view?.phase === "checking" || view?.phase === "installing" || view?.phase === "undoing";
  const committing = view?.phase === "installing" || view?.phase === "undoing";
  const result = view?.result;

  return (
    <>
      <Group
        anchor="skills"
        title={<span className="flex items-center gap-2">{t("settings-skills.title")}{list && list.length > 0 && <span className="font-normal tabular-nums">{t("settings-skills.count", { count: list.length })}</span>}</span>}
        right={<div ref={uploadButton}><Button className="min-h-11" onClick={() => setView({ phase: "select" })}>{t("settings-skills.upload")}</Button></div>}
        footer={t("settings-skills.discovery")}
      >
        {list === null ? <div className="px-4 py-4"><Loading /></div> :
          error ? <RowFailed text={t("settings-skills.load_failed", { error })} retry={t("settings-skills.retry")} onRetry={() => void refresh()} /> :
          list.length === 0 ? <SettingsHint>{t("settings-skills.empty")}</SettingsHint> :
          list.map((s) => (
            <Row key={s.name} label={s.name} hint={s.description} wrapHint>
              <Pill tone={s.source === "builtin" ? undefined : "accent"}>{t(s.source === "builtin" ? "settings-skills.source_builtin" : s.source === "plugin" ? "settings-skills.source_plugin" : "settings-skills.source_mine")}</Pill>
            </Row>
          ))}
        {machine && <SettingsHint>{t("settings-skills.computer", { machine })}</SettingsHint>}
        <SettingsHint>{t("settings-skills.shared")}</SettingsHint>
        {lastInstall && !view && <Row label={t("settings-skills.installed", { name: lastInstall.name })} hint={t("settings-skills.undo_hint")} wrapHint stackOnNarrow>
          <Button className="min-h-11" onClick={() => void write("undo", lastInstall)}>{t("settings-skills.undo")}</Button>
        </Row>}
      </Group>
      <Dialog open={view !== null} onClose={close} title={t("settings-skills.upload")}>
        <div ref={previewBody} className="space-y-4" aria-busy={busy || undefined}>
          {view?.error && <p role="alert" className="text-red">{view.error}</p>}
          {view?.phase === "select" && <>
            <p>{t("settings-skills.package_hint")}</p>
            <Button className="min-h-11" onClick={() => fileInput.current?.click()}>{t("settings-skills.file_label")}</Button>
            <input ref={fileInput} type="file" accept=".zip,application/zip" className="hidden" aria-label={t("settings-skills.file_label")}
              onChange={(e) => { void check(e.currentTarget.files?.[0]); e.currentTarget.value = ""; }} />
          </>}
          {busy && <div role="status" className="flex items-center gap-3"><Loading />{t(view?.phase === "checking" ? "settings-skills.checking" : view?.phase === "undoing" ? "settings-skills.undoing" : "settings-skills.installing")}</div>}
          {view?.phase === "preview" && result && <>
            <p role="status" className="text-green">{t("settings-skills.valid")}</p>
            <dl className="space-y-3">
              <div><dt className="text-ink-3">{t("settings-skills.name")}</dt><dd className="break-words font-medium text-ink">{result.name}</dd></div>
              <div><dt className="text-ink-3">{t("settings-skills.description")}</dt><dd className="break-words whitespace-pre-wrap text-ink">{result.description}</dd></div>
              <div><dt className="text-ink-3">{t("settings-skills.destination")}</dt><dd className="break-all font-mono text-ink">{result.destination}</dd></div>
            </dl>
            <p>{t("settings-skills.preview_computer", { machine: result.machine })}</p>
            <p>{t("settings-skills.package_size", { count: result.files, bytes: result.bytes })}</p>
            <p>{t("settings-skills.shared")}</p>
            <p>{t("settings-skills.trust")}</p>
          </>}
          {view?.phase === "success" && result && <div role="status" className="space-y-3">
            <p className="text-green">{t("settings-skills.installed", { name: result.name })}</p>
            <p>{t("settings-skills.discovery")}</p>
            <p>{t("settings-skills.undo_hint")}</p>
          </div>}
          {view?.phase === "undone" && <div role="status" className="space-y-3">
            <p>{t("settings-skills.undone")}</p>
            {result?.recovery && <p className="break-all font-mono">{result.recovery}</p>}
          </div>}
          <div className="flex flex-wrap justify-end gap-2">
            <Button className="min-h-11" disabled={committing} onClick={close}>{view?.phase === "success" || view?.phase === "undone" ? t("settings-skills.close") : t("common.cancel")}</Button>
            {view?.phase === "preview" && result && <Button className="min-h-11" tone="primary" onClick={() => void write("install", result, view.archive)}>{t("settings-skills.install")}</Button>}
            {view?.phase === "success" && result && <Button className="min-h-11" onClick={() => void write("undo", result)}>{t("settings-skills.undo")}</Button>}
          </div>
        </div>
      </Dialog>
    </>
  );
}
