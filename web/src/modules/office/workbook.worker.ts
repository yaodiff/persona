import * as XLSX from "xlsx";
import { workbookSheets } from "../../lib/sheets";
import type { SheetReply } from "../../lib/sheet-worker";

self.onmessage = ({ data }: MessageEvent<{ bytes: ArrayBuffer }>) => {
  let reply: SheetReply;
  try {
    const wb = XLSX.read(new Uint8Array(data.bytes), { type: "array", dense: true, cellDates: true, cellStyles: true });
    reply = { sheets: workbookSheets(XLSX, wb) };
  } catch (err) {
    reply = { error: err instanceof Error ? err.message : String(err) };
  }
  self.postMessage(reply);
};
