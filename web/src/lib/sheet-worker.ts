import { t } from "../i18n";
import type { Sheet } from "./sheets";

type SheetRequest = { name: string; text: string } | { bytes: ArrayBuffer };
export type SheetReply = { sheets: Sheet[] } | { error: string };

/** Parses a table file in a worker: workbook bytes or delimited text. */
export function workerSheets(w: Worker, req: SheetRequest, signal: AbortSignal): Promise<Sheet[]> {
  return new Promise((resolve, reject) => {
    const abort = () => {
      done();
      reject(new DOMException("aborted", "AbortError"));
    };
    const done = () => {
      w.terminate();
      signal.removeEventListener("abort", abort);
    };
    if (signal.aborted) return abort();
    signal.addEventListener("abort", abort);
    w.onmessage = (e: MessageEvent<SheetReply>) => {
      done();
      if ("error" in e.data) reject(new Error(e.data.error));
      else resolve(e.data.sheets);
    };
    w.onerror = (e) => {
      done();
      reject(new Error(e.message || t("session-files-fileview.sheet_parse_failed")));
    };
    w.postMessage(req, "bytes" in req ? [req.bytes] : []);
  });
}
