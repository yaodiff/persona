import { useCallback } from "react";
import type { ModuleFilePreviewProps } from "../../core/module-host";
import { t } from "../../i18n";
import { extOf, viewKind } from "../../lib/files";
import { bytes } from "../../lib/format";
import { fileBytes } from "../../views/session/files/file-data";
import { workerSheets } from "../../lib/sheet-worker";
import { SheetLoader } from "../../views/session/files/SheetLoader";
import { OfficeView } from "./OfficeView";

const MAX_TABLE_BYTES = 200 << 20;

export function OfficePreview(props: ModuleFilePreviewProps) {
  const { mid, cwd, file } = props;
  const workbook = useCallback(async (signal: AbortSignal) => {
    if (file.size > MAX_TABLE_BYTES) throw new Error(t("session-files-fileview.sheet_too_large", { size: bytes(file.size) }));
    const data = await fileBytes(mid, file, cwd, signal);
    signal.throwIfAborted();
    return workerSheets(new Worker(new URL("./workbook.worker.ts", import.meta.url), { type: "module" }), { bytes: data }, signal);
  }, [mid, cwd, file]);
  if (viewKind(file) === "workbook") {
    return <SheetLoader file={file} workbook={workbook} fallback={extOf(file.name) === "numbers" && <OfficeView {...props} />} />;
  }
  return <OfficeView {...props} />;
}
