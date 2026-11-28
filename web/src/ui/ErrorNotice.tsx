// A failure said quietly (cindy's ErrorNotice, at the owner's request, 2026-11-12; docs/design/DESIGN_SYSTEM.md 3.10): a
// neutral raised card with a hairline border, a small red mark, what happened in body text, what helps under it, the
// caller's actions, and the raw error folded under "Error details". No red fill and no red ring: red marks only the
// state. The caller keeps every decision about recovering and dismissing; this only lays it out.
import { useState, type HTMLAttributes, type ReactNode } from "react";
import { Button, cx } from "./atoms";
import { Icon } from "./icons";
import { t } from "../i18n";

export function ErrorNotice({
  message,
  description,
  actions,
  details,
  icon,
  onDismiss,
  dismissLabel,
  className,
  ...rest
}: {
  /** What happened, in plain words. */
  message: ReactNode;
  /** What helps, or what is safe. */
  description?: ReactNode;
  /** Buttons, in order; the row is not drawn when there are none and no details. */
  actions?: ReactNode;
  /** What only someone digging needs (the raw error, ids): folded until "Error details" is pressed. */
  details?: ReactNode;
  /** Replaces the red mark (a caller whose state is not a failure). */
  icon?: ReactNode;
  /** A ✕ at the top end that puts the notice away (cindy's banner); the caller decides what that means. */
  onDismiss?: () => void;
  /** The ✕'s spoken label and hover text; "Dismiss error" when not given. */
  dismissLabel?: string;
  role?: "status" | "alert";
  className?: string;
} & Omit<HTMLAttributes<HTMLDivElement>, "children" | "role" | "className">) {
  const [open, setOpen] = useState(false);
  const close = dismissLabel ?? t("ui-errornotice.dismiss");
  return (
    <div {...rest} data-error-notice className={cx("flex items-start gap-2 rounded-card bg-surface p-3 shadow-card", className)}>
      <span aria-hidden className="mt-0.5 inline-flex shrink-0 text-red">
        {icon ?? <Icon name="alertCircle" size={16} />}
      </span>
      <div className="min-w-0 flex-1">
        <div className="text-[13px] leading-5 [overflow-wrap:anywhere] text-ink">{message}</div>
        {description && <div className="mt-1 text-[12px] leading-5 text-ink-2">{description}</div>}
        <div className="mt-2 flex flex-wrap items-center gap-2 empty:hidden">
          {actions}
          {details && (
            <Button tone="quiet" size="sm" className="touch-hit" expanded={open} onClick={() => setOpen((o) => !o)}>
              <Icon name={open ? "chevron" : "chevronRight"} size={12} stroke={2.2} />
              {t(open ? "ui-errornotice.hide_details" : "ui-errornotice.details")}
            </Button>
          )}
        </div>
        {details && open && <div className="mt-2" data-error-details>{details}</div>}
      </div>
      {onDismiss && (
        // cindy's 28 px round ✕, pulled into the card's corner so it lines up with the first line of text
        <button
          type="button"
          onClick={onDismiss}
          aria-label={close}
          title={close}
          data-error-dismiss
          className="touch-hit -mt-1 -mr-1 flex size-7 shrink-0 items-center justify-center rounded-full text-ink-2 transition-colors hover:bg-hover hover:text-ink focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none"
        >
          <Icon name="x" size={14} />
        </button>
      )}
    </div>
  );
}

/** The raw error inside a notice's details: its own scrolling well, every character kept. */
export function ErrorDetailsText({ text }: { text: string }) {
  return <pre className="max-h-40 overflow-auto rounded-control bg-inset p-2 font-mono text-[12px] leading-5 break-all whitespace-pre-wrap text-ink-2">{text}</pre>;
}
