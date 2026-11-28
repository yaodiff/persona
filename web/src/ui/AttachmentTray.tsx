import { cx, EASE } from "./atoms";
import { Icon } from "./icons";
import { t } from "../i18n";

type AttachmentTrayProps = {
  attachments: { id: string; name: string; url?: string }[];
  onRemove?: (id: string) => void;
  /** Click a picture to open it (the composer expands it above the input and offers markup). */
  onOpen?: (id: string) => void;
  /** A read-only opener's spoken label; without it, opening retains the composer's Edit wording. */
  openLabel?: (name: string) => string;
  /** Currently expanded image tile id, highlighted with an accent ring. */
  activeId?: string;
  /** Preserve focus on the active input when clicking tiles or remove buttons with a pointer. */
  keepFocus?: boolean;
};

export function AttachmentTray(props: AttachmentTrayProps) {
  if (!props.attachments.length) return null;
  return (
    <div className="flex flex-wrap gap-1.5">
      <AttachmentTiles {...props} />
    </div>
  );
}

const prevent = (e: { preventDefault(): void }) => e.preventDefault();

/** The composer places these directly beside its remote file tiles. */
export function AttachmentTiles({ attachments, onRemove, onOpen, openLabel, activeId, keepFocus }: AttachmentTrayProps) {
  return attachments.map((a) => {
    const active = Boolean(activeId && activeId === a.id);
    return (
      <span
        key={a.id}
        data-attachment-id={a.id}
        title={a.name}
        className={cx(
          "group/att relative rounded-card bg-field transition-shadow duration-150",
          active ? "ring-2 ring-accent" : "ring-1 ring-inset ring-line",
          a.url ? "size-[52px] shrink-0 p-px" : "flex h-[52px] max-w-full items-center gap-2 px-3 pr-7",
        )}
        style={{ animation: `pop-in 200ms ${EASE} both` }}
      >
        {a.url ? (
          onOpen ? (
            <button
              type="button"
              aria-label={openLabel?.(a.name) ?? t("ui-attachmenttray.edit", { name: a.name })}
              title={openLabel?.(a.name) ?? t("ui-attachmenttray.edit_hint")}
              aria-pressed={activeId ? active : undefined}
              onMouseDown={keepFocus ? prevent : undefined}
              onClick={() => onOpen(a.id)}
              className="block size-full cursor-pointer rounded-card"
            >
              <img src={a.url} alt={a.name} className="size-full rounded-card object-cover" />
            </button>
          ) : (
            <img src={a.url} alt={a.name} className="size-full rounded-card object-cover" />
          )
        ) : <><Icon name="read" size={16} className="shrink-0 text-ink-3" /><span className="min-w-0 truncate text-label text-ink-2">{a.name}</span></>}
        {onRemove && <AttachmentRemoveButton label={t("ui-attachmenttray.remove", { name: a.name })} onRemove={() => onRemove(a.id)} file={!a.url} keepFocus={keepFocus} />}
      </span>
    );
  });
}

export function AttachmentRemoveButton({ label, onRemove, file = false, keepFocus = false }: {
  label: string;
  onRemove: () => void;
  file?: boolean;
  keepFocus?: boolean;
}) {
  // Reach down the file's reserved column. Keep the smaller photo target clear of the editor button at its center.
  return (
    <span className={cx("absolute top-0.5 flex", file ? "right-0.5" : "right-px")}>
      <button
        type="button"
        aria-label={label}
        onMouseDown={keepFocus ? prevent : undefined}
        onClick={onRemove}
        data-remove
        className={cx(
          "touch-hit-corner flex size-4.5 items-center justify-center rounded-full bg-surface text-ink opacity-0 shadow-btn transition-opacity",
          "group-hover/att:opacity-100 group-focus-within/att:opacity-100 focus-visible:rounded-full!",
          "[@media(hover:none)]:opacity-100 [@media(any-pointer:coarse)]:opacity-100",
          file && "[--hit-in-x:8px] [--hit-in-y:24px]",
        )}
      >
        <Icon name="x" size={10} stroke={2.6} />
      </button>
    </span>
  );
}
