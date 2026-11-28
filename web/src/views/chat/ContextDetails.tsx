// Context bodies (including selected-skill metadata) load only when the existing disclosure opens.
import { useState, type MouseEvent as ReactMouseEvent } from "react";
import { clickLink, linkHref } from "../../lib/links";
import { useLinkManager } from "../../lib/manager-choice";
import type { Context } from "../../lib/turns";
import { Collapse, cx } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { t } from "../../i18n";
import { contextLabel, CONTEXT_ICONS } from "./ContextGroup";

export type ContextLinkClick = (
  event: ReactMouseEvent<HTMLAnchorElement>,
  url: string,
  title: string | undefined,
  manager: string | undefined,
) => void;

export interface ContextLinkProps {
  /** Explicit manager skips reading LinkManager/ItemManager contexts; null selects the default manager. */
  linkManager?: string | null;
  /** Builds a link address without calling the default link router. */
  resolveLinkHref?: (url: string) => string | undefined;
  /** Handles link activation without calling the default panel's clickLink helper. */
  onLinkClick?: ContextLinkClick;
}

export interface ContextDetailsProps extends ContextLinkProps {
  contexts: Context[];
}

export function ContextDetails({ contexts, linkManager, resolveLinkHref, onLinkClick }: ContextDetailsProps) {
  const linkProps: ContextLinkProps = { linkManager, resolveLinkHref, onLinkClick };
  return (
    <div className="mt-1 flex flex-col overflow-hidden rounded-[10px] bg-inset shadow-hairline">
      {contexts.length === 1
        ? <ContextBody c={contexts[0]} {...linkProps} />
        : contexts.map((c, i) => <ContextRow key={i} c={c} first={i === 0} {...linkProps} />)}
    </div>
  );
}

function ContextRow({ c, first, linkManager, resolveLinkHref, onLinkClick }: { c: Context; first: boolean } & ContextLinkProps) {
  const [open, setOpen] = useState(false);
  return (
    <div className={cx(!first && "border-t border-line")}>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className="flex w-full min-w-0 items-center gap-2 px-3 py-2 text-left text-[12.5px] whitespace-nowrap text-ink-2 transition-colors duration-100 hover:bg-hover-2"
      >
        <Icon name={CONTEXT_ICONS[c.kind] ?? "quote"} size={13} className="shrink-0 text-ink-3" />
        <span className="min-w-0 flex-1 truncate">{contextLabel(c)}</span>
        {!c.text && <span className="shrink-0 text-[11.5px] text-ink-3">{t("chat-contextgroup.empty")}</span>}
        <Icon name="chevronRight" size={13} stroke={2.2} className="shrink-0 text-ink-3 transition-transform duration-200" style={{ transform: open ? "rotate(90deg)" : "none" }} />
      </button>
      <Collapse open={open} lazy>
        <ContextBody c={c} linkManager={linkManager} resolveLinkHref={resolveLinkHref} onLinkClick={onLinkClick} />
      </Collapse>
    </div>
  );
}

function ContextBody({ c, linkManager, resolveLinkHref, onLinkClick }: { c: Context } & ContextLinkProps) {
  if (onLinkClick || linkManager !== undefined) {
    return (
      <ContextBodyView
        c={c}
        manager={linkManager || undefined}
        resolveLinkHref={resolveLinkHref ?? linkHref}
        onLinkClick={onLinkClick ?? clickLink}
      />
    );
  }
  return <DefaultContextBody c={c} resolveLinkHref={resolveLinkHref} />;
}

function DefaultContextBody({ c, resolveLinkHref }: { c: Context; resolveLinkHref?: (url: string) => string | undefined }) {
  const manager = useLinkManager();
  return (
    <ContextBodyView
      c={c}
      manager={manager}
      resolveLinkHref={resolveLinkHref ?? linkHref}
      onLinkClick={clickLink}
    />
  );
}

function ContextBodyView({ c, manager, resolveLinkHref, onLinkClick }: {
  c: Context;
  manager: string | undefined;
  resolveLinkHref: (url: string) => string | undefined;
  onLinkClick: ContextLinkClick;
}) {
  const url = c.url;
  const href = url ? resolveLinkHref(url) : undefined;
  return (
    <div className="flex flex-col gap-1.5 px-3 pb-2.5 pt-0.5">
      {c.text ? (
        <div className="max-h-[320px] overflow-y-auto text-[12.5px] leading-[1.6] whitespace-pre-wrap text-ink-3 [overflow-wrap:anywhere]">{c.text}</div>
      ) : (
        <span className="text-[12px] text-ink-3">{t("chat-contextgroup.no_content")}</span>
      )}
      {url && href && (
        <a
          href={href}
          target="_blank"
          rel="noreferrer"
          onClick={(e) => onLinkClick(e, url, c.title, manager)}
          className="flex w-fit max-w-full min-w-0 items-center gap-1 text-[12px] whitespace-nowrap text-accent hover:underline"
        >
          <Icon name="external" size={12} className="shrink-0" />
          <span className="min-w-0 truncate">{t("chat-contextgroup.open_url", { url })}</span>
        </a>
      )}
    </div>
  );
}
