// Remote files share the composer's attachment strip with local photos (#15408). The slot in FileTiles.tsx loads
// these tiles the first time a draft has a remote file.
import { removeFile, retryFile, type ComposerFile } from "../../lib/composer-files";
import { bytes } from "../../lib/format";
import { AttachmentRemoveButton } from "../../ui/AttachmentTray";
import { cx, EASE } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { t } from "../../i18n";
import { FileGlyph } from "../session/files/parts";

function status(f: ComposerFile): string {
  switch (f.state) {
    case "waiting":
      return t("chat-composeroverlays.file_waiting");
    case "uploading":
      return f.size ? `${bytes(f.sent)} / ${bytes(f.size)}` : bytes(f.sent);
    case "ready":
      return bytes(f.size);
    case "failed":
      return t("chat-composeroverlays.file_failed");
  }
}

export function FileTiles({ draftKey, files }: { draftKey: string; files: ComposerFile[] }) {
  return files.map((f) => {
    const failed = f.state === "failed";
    const busy = f.state === "waiting" || f.state === "uploading";
    const detail = status(f);
    const progress = f.size ? Math.min(100, (f.sent / f.size) * 100) : 0;
    const body = (
      <>
        <span className="flex size-7 shrink-0 items-center justify-center rounded-control bg-surface ring-1 ring-inset ring-line">
          {failed ? <Icon name="warning" size={16} className="text-red" /> : <FileGlyph name={f.name} />}
        </span>
        <span className="flex min-w-0 flex-col">
          <span className="truncate text-label font-medium text-ink">{f.name}</span>
          <span className={cx("truncate text-submeta tabular-nums", failed ? "text-red" : "text-ink-3")}>{detail}</span>
        </span>
      </>
    );
    return (
      <span
        key={f.id}
        title={failed ? f.error || f.name : f.name}
        data-state={f.state}
        className="group/att relative flex h-[52px] w-48 max-w-full shrink-0 items-center gap-2 overflow-hidden rounded-card bg-field/80 px-2 pr-7 ring-1 ring-inset ring-line"
        style={{ animation: `pop-in 200ms ${EASE} both` }}
      >
        {failed ? (
          <button type="button" onClick={() => retryFile(draftKey, f.id)} aria-label={t("chat-composeroverlays.file_retry", { name: f.name })} className="flex h-full min-w-0 flex-1 items-center gap-2 text-left hover:underline focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-accent">
            {body}
          </button>
        ) : body}
        {busy && (
          <span
            role="progressbar"
            aria-label={f.name}
            aria-valuenow={f.size ? progress : undefined}
            aria-valuetext={detail}
            className="absolute inset-x-2 bottom-1 h-0.5 overflow-hidden rounded-full bg-line"
          >
            <span className="block h-full rounded-full bg-accent" style={{ width: `${progress}%` }} />
          </span>
        )}
        <AttachmentRemoveButton
          label={t(busy ? "chat-composeroverlays.file_cancel" : "ui-attachmenttray.remove", { name: f.name })}
          onRemove={() => removeFile(draftKey, f.id)}
          file
        />
      </span>
    );
  });
}
