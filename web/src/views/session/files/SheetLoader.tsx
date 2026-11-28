import { useEffect, useReducer, useState, type ReactNode } from "react";
import { t } from "../../../i18n";
import { errorText, isAbort } from "../../../lib/api";
import type { FileContent } from "../../../lib/files";
import { bytes } from "../../../lib/format";
import { workerSheets } from "../../../lib/sheet-worker";
import { delimitedSheet, type Sheet } from "../../../lib/sheets";
import { Button, Loading } from "../../../ui/atoms";
import { Empty } from "./parts";
import { SheetView } from "./SheetView";

const MAX_TABLE_TEXT = 64 << 20; // a CSV is read whole up to this, then parsed

/** Reads a bounded CSV/TSV preview or a workbook's whole bytes and parses it off the main thread. */
export function SheetLoader({ file, source, workbook, fallback }: { file: FileContent; fallback?: ReactNode } & (
  | { source: (cap: number) => Promise<{ text: string; complete: boolean }>; workbook?: never }
  | { source?: never; workbook: (signal: AbortSignal) => Promise<Sheet[]> }
)) {
  const [state, setState] = useState<{ sheets?: Sheet[]; error?: string; note?: string }>({});
  const [attempt, retry] = useReducer((n: number) => n + 1, 0);
  useEffect(() => {
    const ac = new AbortController();
    setState({});
    (async () => {
      if (workbook) {
        const sheets = await workbook(ac.signal);
        if (!ac.signal.aborted) setState({ sheets });
        return;
      }
      const { text, complete } = await source(MAX_TABLE_TEXT);
      if (ac.signal.aborted) return;
      const note = complete ? "" : t("session-files-fileview.sheet_partial", { size: bytes(text.length) });
      const sheets = text.length < 256 << 10 ? [delimitedSheet(file.name, text)] : await workerSheets(
        new Worker(new URL("../../../lib/sheets.worker.ts", import.meta.url), { type: "module" }), { name: file.name, text }, ac.signal,
      );
      if (!ac.signal.aborted) setState({ sheets, note });
    })().catch((e) => {
      if (!isAbort(e) && !ac.signal.aborted) setState({ error: errorText(e) });
    });
    return () => ac.abort();
  }, [file, source, workbook, attempt]);
  if (state.error && fallback) return fallback;
  if (state.error) return <Empty icon="info" title={t("session-files-fileview.sheet_open_failed")} detail={state.error}
    action={<Button icon="retry" onClick={retry}>{t("session-files-fileview.retry")}</Button>} />;
  if (!state.sheets)
    return (
      <div className="flex flex-1 items-center justify-center">
        <Loading label={typeof file.text === "string" ? t("session-files-fileview.reading_sheet") : t("session-files-fileview.reading_sheet_size", { size: bytes(file.size) })} />
      </div>
    );
  if (!state.sheets.length) return <Empty icon="info" title={t("session-files-fileview.no_sheets")} />;
  return <SheetView sheets={state.sheets} note={state.note} />;
}
