// Health reports (docs/assistant/health.md"Health reports", #4998): what was uploaded to the health space's reports/, on the home page. Opens
// the record the health conversation wrote and the original, and deletes both.
import { useCallback, useEffect, useState } from "react";
import { api, enc, errorText, fileDownloadURL } from "../../lib/api";
import { bytes } from "../../lib/format";
import { onHubEvent, useStore } from "../../lib/store";
import { Button, Dialog, Loading, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Markdown } from "../../ui/Markdown";
import { SectionTitle } from "../assistant/parts";
import { t } from "../../i18n";
import { reportsPath, type HealthReport } from "./reports";

export function HealthReports({ version }: { version: number }) {
  const mid = useStore((s) => s.assistant?.machine_id);
  const [list, setList] = useState<HealthReport[] | null>(null);
  const [error, setError] = useState("");
  const [open, setOpen] = useState<HealthReport | null>(null);
  const load = useCallback(async () => {
    if (!mid) return;
    try {
      const r = await api<{ reports: HealthReport[] }>(reportsPath(mid));
      setList(r.reports ?? []);
      setError("");
    } catch (e) {
      setError(errorText(e));
    }
  }, [mid]);
  useEffect(() => {
    void load();
  }, [load, version]);
  // read again on every hello, as the page's conversations are: one that failed while the hub was away, or a report
  // added or deleted from another device meanwhile
  useEffect(() => onHubEvent("hello", () => void load()), [load]);

  return (
    <section className="mt-7" data-testid="health-reports">
      <SectionTitle right={list && list.length > 0 ? <span className="text-[11.5px] text-ink-3 tabular-nums">{t("health.reports_count", { count: list.length })}</span> : undefined}>{t("health.reports")}</SectionTitle>
      <div className="overflow-hidden rounded-card bg-surface shadow-card">
        {list === null && !error ? (
          <div className="flex justify-center py-6"><Loading /></div>
        ) : !list || list.length === 0 ? (
          <p className="px-4 py-4 text-[12.5px] leading-relaxed text-ink-3">{error ? t("health.reports_load_failed", { error }) : t("health.reports_empty")}</p>
        ) : (
          <div className="divide-y divide-line">
            {list.map((r) => (
              <button key={r.name} type="button" onClick={() => setOpen(r)} className="flex w-full min-w-0 items-center gap-3 px-4 py-2.5 text-left transition-colors hover:bg-hover">
                <span className="flex size-8 shrink-0 items-center justify-center rounded-full bg-inset text-ink-2"><Icon name="file" size={13} /></span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[13.5px] font-medium text-ink">{r.title}</span>
                  <span className="block truncate text-[12px] text-ink-3">{r.record_path ? t("health.report_recorded") : t("health.report_unrecorded")} · {bytes(r.size)}</span>
                </span>
                <span className="shrink-0 text-[11.5px] text-ink-3 tabular-nums">{new Date(r.modified_at).toLocaleDateString(undefined, { year: "numeric", month: "numeric", day: "numeric" })}</span>
              </button>
            ))}
          </div>
        )}
      </div>
      {open && mid && <ReportDialog key={open.name} mid={mid} report={open} onClose={() => setOpen(null)} onDeleted={() => { setOpen(null); void load(); }} />}
    </section>
  );
}

function ReportDialog({ mid, report, onClose, onDeleted }: { mid: string; report: HealthReport; onClose: () => void; onDeleted: () => void }) {
  const [record, setRecord] = useState<string | null>(report.record_path ? null : "");
  const [readError, setReadError] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!report.record_path) return;
    let live = true;
    api<{ text?: string }>(`/machines/${enc(mid)}/file?path=${enc(report.record_path)}`)
      .then((f) => { if (live) setRecord(f.text ?? ""); })
      .catch((e) => { if (live) setReadError(errorText(e)); });
    return () => { live = false; };
  }, [mid, report.record_path]);

  const remove = async () => {
    setBusy(true);
    try {
      await api(`${reportsPath(mid)}/${enc(report.name)}`, { method: "DELETE" });
      toast(t("health.report_deleted", { title: report.title }), "ok");
      onDeleted();
    } catch (e) {
      toast(errorText(e), "error");
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      width={640}
      onClose={onClose}
      title={report.title}
      footer={confirming ? (
        <>
          <Button tone="quiet" onClick={() => setConfirming(false)}>{t("health.report_keep")}</Button>
          <Button tone="danger" icon="trash" busy={busy} onClick={() => void remove()}>{t("health.report_delete_confirm")}</Button>
        </>
      ) : (
        <>
          <Button tone="danger" icon="trash" onClick={() => setConfirming(true)}>{t("health.report_delete")}</Button>
          <span className="flex-1" />
          <Button tone="quiet" onClick={onClose}>{t("health.close")}</Button>
          {report.path && <Button tone="primary" href={fileDownloadURL(mid, report.path, undefined, { inline: true })}>{t("health.report_open")}</Button>}
        </>
      )}
    >
      {confirming ? (
        <p className="text-[13px] leading-relaxed text-ink-2">{t("health.report_delete_body")}</p>
      ) : readError ? (
        <p className="text-[13px] leading-relaxed text-ink-3">{t("health.reports_load_failed", { error: readError })}</p>
      ) : record === null ? (
        <div className="flex justify-center py-6"><Loading /></div>
      ) : record ? (
        <div className="max-h-[60vh] overflow-y-auto"><Markdown text={record} small mid={mid} /></div>
      ) : (
        <p className="text-[13px] leading-relaxed text-ink-3">{t("health.report_no_record")}</p>
      )}
    </Dialog>
  );
}
