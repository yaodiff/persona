// The queued messages (Session.queued) at the top of the composer's card, as cindy's pending queue (#5327; the owner
// asked this part of the chat to follow cindy): rows in send order, the top one next, no numbers (the labels carry
// them). A row is one line, the marks of what it carries before its words; on hover or focus it shows a drag handle
// and round buttons: Send now (where the agent takes queued messages now; it hands over the whole queue at one press,
// its tooltip saying so and what happens), Edit (pulled back into the box below) and Delete. Dragging the handle, or
// ↑ ↓ Home End on it, re-queues them all in the new order. Four rows or fewer show in full; five or more show three
// and "N more".
// Messages from other conversations come last and are only shown; since #6394 they collapse into one line
// ("N messages from other conversations are waiting"), opened on demand, so a backlog no longer spreads over the
// composer. Every change goes through the hub (POST …/queue {keep}, …/queue/send-now) and the rows follow its next
// push; a reorder keeps its new order on screen until then. Its own chunk: drawn only while something is queued
// (QueueStrip.tsx loads it).
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { api, errorText, sessionPath } from "../../lib/api";
import { agentLabel } from "../../lib/mentions";
import { keepAllBut, moved, movedTo, parseQueued, queuedMarks } from "../../lib/queue";
import type { Session } from "../../lib/types";
import { hasFinePointer } from "../../lib/viewport";
import { Collapse, cx, Spinner, toast } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { t } from "../../i18n";
import { restoreQueued, type QueuedImage } from "./QueueStrip";

export interface QueuePanelProps {
  session: Session;
  draftKey: string;
  canEdit: boolean;
  /** The persona's name in a Persona chat (#4342). */
  persona?: string;
  /** Hears a message taken out of the queue, so its sending bubble goes too. */
  onWithdrawn?: (text: string) => void;
  /** How many photos a queued message went with when this browser sent it (a Persona chat's queue holds only words). */
  photosOf?: (raw: string) => number;
  /** A phone on its side: one line, "N queued", in the line of the run's status (outside the composer), that opens the
   *  same rows under it. */
  compact?: boolean;
}

/** cindy's: four rows or fewer show in full; past that the first three and "N more". */
const FOLD_OVER = 4;
const FOLDED_ROWS = 3;
/** How long a reorder keeps its order on screen after the hub said yes, waiting for the push that shows it. */
const HOLD_MS = 1500;
/** How long a keyboard reorder's handle takes the focus back when its row is drawn again (the agent re-queues them). */
const REFOCUS_MS = 5000;

interface Row {
  raw: string;
  /** Its place in the hub's queue: what keep and send-now count by. */
  i: number;
  /** Stable while it stays queued, moved or not: its words, and which of the same words it is. */
  id: string;
  /** The user's own (not from another conversation). */
  own: boolean;
}

interface Drag {
  id: string;
  from: number;
  to: number;
}

export function QueuePanel({ session, draftKey, canEdit, persona, onWithdrawn, photosOf, compact }: QueuePanelProps) {
  const queued = useMemo(() => session.queued ?? [], [session.queued]);
  // the user's own come first; the rest came from other conversations and are only shown (#4342)
  const own = queued.length - Math.min(Math.max(session.queued_relayed ?? 0, 0), queued.length);
  const agent = persona ?? agentLabel(session);
  // a Claude takes queued messages at its next step, and can be given them now (#6088); so does a Persona chat's (#4342)
  const takesNow = session.agent === "claude" || (persona !== undefined && own > 0);
  const path = sessionPath(session.machine_id, session.session_id);
  const listId = useId();
  const explainId = useId();
  const region = useRef<HTMLDivElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const rowEls = useRef(new Map<string, HTMLElement>());
  const handles = useRef(new Map<string, HTMLButtonElement>());

  const [busy, setBusy] = useState<string | null>(null);
  const inFlight = useRef(false);
  const [expanded, setExpanded] = useState(false);
  // relayed messages from other conversations are one collapsed line until the owner asks to inspect them (#6394)
  const [relayedOpen, setRelayedOpen] = useState(false);
  // compact: the rows under its one line, opened
  const [open, setOpen] = useState(false);
  const [drag, setDrag] = useState<Drag | null>(null);
  // the drag as the window's pointer listeners see it, and how to stop them (the row moves in the page as it is
  // dragged down, which would take a pointer capture away from its handle)
  const dragging = useRef<{ drag: Drag; stop: () => void } | null>(null);
  // a reorder's order, shown until the queue changes or a little after the hub took it
  const [held, setHeld] = useState<{ order: number[]; queue: string } | null>(null);
  // the handle a keyboard reorder came from: it gets the focus back when its row is drawn again
  const refocus = useRef<{ id: string; until: number } | null>(null);

  const rows = useMemo(() => {
    const seen = new Map<string, number>();
    return queued.map((raw, i): Row => {
      const nth = seen.get(raw) ?? 0;
      seen.set(raw, nth + 1);
      return { raw, i, id: `${nth}:${raw}`, own: i < own };
    });
  }, [queued, own]);
  const queueKey = queued.join("\u0000");
  const holdOrder = held && held.queue === queueKey ? held.order : null;
  const locked = busy !== null || holdOrder !== null || drag !== null;

  const identity = Array.from({ length: own }, (_, i) => i);
  const order = drag ? (movedTo(own, drag.from, drag.to) ?? identity) : (holdOrder ?? identity);
  // Only the owner's own rows reorder and fold; the relayed ones live behind their own collapsed line (#6394).
  const ownRows = rows.filter((r) => r.own);
  const relayedRows = rows.filter((r) => !r.own);
  const shown = order.map((i) => ownRows[i]).filter(Boolean);
  const folds = shown.length > FOLD_OVER;
  const visible = folds && !expanded ? shown.slice(0, FOLDED_ROWS) : shown;
  const shownOwn = visible.length;

  useLayoutEffect(() => {
    const back = refocus.current;
    if (!back || Date.now() > back.until) return;
    const at = document.activeElement;
    if (at && at !== document.body) return;
    handles.current.get(back.id)?.focus({ preventScroll: true });
  });
  // a drag ends with the queue it started on: once the queue changed (the agent took one), its places mean nothing
  useEffect(() => {
    dragging.current?.stop();
  }, [queueKey]);
  useEffect(() => () => dragging.current?.stop(), []);

  const run = async (id: string, work: () => Promise<void>): Promise<boolean> => {
    if (inFlight.current) return false;
    inFlight.current = true;
    setBusy(id);
    try {
      await work();
      return true;
    } catch (e) {
      toast(t("chat-queuepanel.failed", { error: errorText(e) }), "error");
      return false;
    } finally {
      inFlight.current = false;
      setBusy(null);
    }
  };
  // back to the box below once the row is gone (a precise pointer only: a touch screen would raise its keyboard)
  const toBox = (always: boolean) => {
    const at = document.activeElement;
    if (!hasFinePointer() || !(always || !at || at === document.body || region.current?.contains(at))) return;
    region.current?.closest("[data-composer-top]")?.parentElement?.querySelector("textarea")?.focus({ preventScroll: true });
  };
  const act = (row: Row, del: boolean) =>
    void run(row.id, async () => {
      const r = await api<{ queue?: string[]; images?: QueuedImage[][] }>(`${path}/queue`, { method: "POST", json: { keep: keepAllBut(own, row.i) } });
      const pulled = r.queue ?? [];
      toast(del ? t("chat-queuepanel.deleted") : t("chat-queuepanel.pulled_back"), "ok");
      if (pulled[row.i] !== undefined) onWithdrawn?.(pulled[row.i]);
      const images = r.images?.[row.i] ?? [];
      if (!del && pulled[row.i] !== undefined) await restoreQueued(draftKey, session, parseQueued(pulled[row.i], images.length), images);
    }).then((ok) => ok && toBox(!del));
  const reorder = (keep: number[] | null, id: string) => {
    if (!keep) return;
    const mine = { order: keep, queue: queueKey };
    setHeld(mine);
    void run(id, () => api(`${path}/queue`, { method: "POST", json: { keep } }).then(() => undefined)).then((ok) => {
      if (!ok) setHeld(null);
      else setTimeout(() => setHeld((h) => (h === mine ? null : h)), HOLD_MS);
    });
  };
  // at one press, with no dialog: it loses nothing (the messages go now rather than at the next step), and Stop, which
  // cuts the whole turn, and Delete beside it act at one press too (DESIGN_SYSTEM.md 4.4)
  const sendNow = (row: Row) =>
    void run(row.id, async () => {
      await api(`${path}/queue/send-now`, { method: "POST", json: {} });
      toast(t("chat-queuepanel.sent_now", { agent }), "ok");
    }).then((ok) => ok && toBox(false));
  // what the dialog used to say, now the button's tooltip and description
  const explain = t(persona !== undefined ? "chat-queuepanel.send_now_body_persona" : "chat-queuepanel.send_now_body", { agent });

  const keyMove = (e: React.KeyboardEvent, row: Row) => {
    const to = e.key === "ArrowUp" ? row.i - 1 : e.key === "ArrowDown" ? row.i + 1 : e.key === "Home" ? 0 : e.key === "End" ? own - 1 : null;
    if (to === null) return;
    e.preventDefault();
    if (locked) return;
    const keep = e.key.startsWith("Arrow") ? moved(own, row.i, to - row.i) : movedTo(own, row.i, to);
    if (!keep) return;
    // a row moved past the fold stays in view, and its handle keeps the focus as the rows are drawn again
    if (folds && to >= FOLDED_ROWS) setExpanded(true);
    refocus.current = { id: row.id, until: Date.now() + REFOCUS_MS };
    reorder(keep, row.id);
  };
  const startDrag = (e: React.PointerEvent<HTMLButtonElement>, row: Row) => {
    if (e.button !== 0 || locked || dragging.current) return;
    refocus.current = null;
    // the shown own rows' middles, top to bottom: the dragged one goes after every other one above the pointer
    const mids = visible.slice(0, shownOwn).map((r) => {
      const b = rowEls.current.get(r.id)?.getBoundingClientRect();
      return b ? b.top + b.height / 2 : 0;
    });
    const scroll = list.current?.scrollTop ?? 0;
    const move = (ev: PointerEvent) => {
      const cur = dragging.current;
      if (!cur) return;
      const y = ev.clientY + (list.current?.scrollTop ?? 0) - scroll;
      const to = mids.filter((m, j) => j !== cur.drag.from && m < y).length;
      if (to === cur.drag.to) return;
      cur.drag = { ...cur.drag, to };
      setDrag(cur.drag);
    };
    const end = (ev: PointerEvent) => {
      const cur = dragging.current;
      stop();
      if (cur && ev.type === "pointerup") reorder(movedTo(own, cur.drag.from, cur.drag.to), cur.drag.id);
    };
    const stop = () => {
      removeEventListener("pointermove", move);
      removeEventListener("pointerup", end);
      removeEventListener("pointercancel", end);
      dragging.current = null;
      setDrag(null);
    };
    addEventListener("pointermove", move);
    addEventListener("pointerup", end);
    addEventListener("pointercancel", end);
    dragging.current = { drag: { id: row.id, from: row.i, to: row.i }, stop };
    setDrag(dragging.current.drag);
  };

  if (!queued.length) return null;
  const hidden = shown.length - visible.length;
  const rowFor = (row: Row, pos: number) => (
    <QueueRow
      key={row.id}
      row={row}
      n={pos + 1}
      photos={row.own ? (photosOf?.(row.raw) ?? 0) : 0}
      editable={canEdit && row.own}
      movable={canEdit && row.own && own > 1}
      takesNow={takesNow}
      agent={agent}
      explain={explain}
      explainId={explainId}
      busy={busy === row.id}
      locked={locked}
      dragging={drag?.id === row.id}
      rowRef={(el) => {
        if (el) rowEls.current.set(row.id, el);
        else rowEls.current.delete(row.id);
      }}
      handleRef={(el) => {
        if (el) handles.current.set(row.id, el);
        else handles.current.delete(row.id);
      }}
      onKey={(e) => keyMove(e, row)}
      onPointerDown={(e) => startDrag(e, row)}
      onSendNow={() => sendNow(row)}
      onEdit={() => act(row, false)}
      onDelete={() => act(row, true)}
    />
  );
  // The messages other conversations sent in: one line while collapsed, their read-only rows when opened (#6394).
  const relayedLine = relayedRows.length > 0 && (
    <div className="border-t border-line">
      <button
        type="button"
        aria-expanded={relayedOpen}
        data-queue-relayed-summary
        onClick={() => setRelayedOpen((o) => !o)}
        className="touch-hit flex h-8 w-full items-center gap-1.5 px-2 text-label leading-none whitespace-nowrap text-ink-3 transition-colors hover:bg-hover hover:text-ink"
      >
        <Icon name="send" size={13} stroke={2} className="shrink-0" />
        <span className="min-w-0 flex-1 truncate text-left">{t("chat-queuepanel.relayed_summary", { count: relayedRows.length })}</span>
        <Icon name="chevron" size={13} className={cx("shrink-0 transition-transform duration-200", relayedOpen && "rotate-180")} />
      </button>
      {relayedOpen && (
        <div role="list" data-queue-relayed-list>
          {relayedRows.map((row, pos) => rowFor(row, own + pos))}
        </div>
      )}
    </div>
  );
  const rowsAndFold = (
    <>
      <div ref={list} id={listId} role="list" className="flex max-h-[196px] flex-col overflow-y-auto overscroll-contain px-1 pt-1.5 pb-0.5">
        {visible.map((row, pos) => rowFor(row, pos))}
      </div>
      {folds && (
        <div className="flex items-center justify-center border-t border-line px-2 py-1.5 text-[12px] leading-none">
          <button
            type="button"
            aria-expanded={expanded}
            aria-controls={listId}
            data-queue-fold
            onClick={() => setExpanded((o) => !o)}
            className="touch-hit flex h-5 items-center gap-1 rounded-full px-2 whitespace-nowrap text-ink-3 transition-colors hover:bg-hover hover:text-ink"
          >
            {expanded ? t("chat-queuepanel.less") : t("chat-queuepanel.more", { count: hidden })}
            <Icon name="chevron" size={13} className={cx("transition-transform duration-200", expanded && "rotate-180")} />
          </button>
        </div>
      )}
      {relayedLine}
    </>
  );
  return (
    <div
      ref={region}
      role="region"
      aria-label={t("chat-queuepanel.region")}
      data-queue-panel
      data-queue-strip={compact || undefined}
      className={cx("w-full", compact ? "rounded-[12px] bg-surface p-1 shadow-card" : "border-b border-line")}
      style={{ animation: "fade-in 160ms ease-out both" }}
    >
      {compact ? (
        <>
          <button
            type="button"
            aria-expanded={open}
            aria-controls={listId}
            onClick={() => setOpen((o) => !o)}
            className="flex h-7 w-full min-w-0 items-center gap-1.5 rounded-[8px] px-1.5 text-left text-[11.5px] font-medium text-ink-3 transition-colors hover:bg-hover"
          >
            <Icon name="inbox" size={13} />
            <span className="min-w-0 flex-1 truncate">{t("chat-queuepanel.summary", { count: queued.length })}</span>
            {busy !== null && <Spinner />}
            <Icon name="chevron" size={12} stroke={2.2} className={cx("shrink-0 transition-transform duration-200", open && "rotate-180")} />
          </button>
          <Collapse open={open}>{rowsAndFold}</Collapse>
        </>
      ) : (
        rowsAndFold
      )}
      {takesNow && (
        <span id={explainId} hidden data-queue-explain>
          {explain}
        </span>
      )}
    </div>
  );
}

type MarkKind = ReturnType<typeof queuedMarks>[number]["kind"];
const MARK_ICON: Record<MarkKind, IconName> = { photo: "image", video: "video", file: "file", context: "quote" };
const markWords = (kind: MarkKind, count: number) =>
  kind === "photo"
    ? t("chat-queuepanel.mark_photo", { count })
    : kind === "video"
      ? t("chat-queuepanel.mark_video", { count })
      : kind === "file"
        ? t("chat-queuepanel.mark_file", { count })
        : t("chat-queuepanel.mark_context", { count });

// cindy's row buttons: 20 px, round, a hairline ring on the card's own fill
const ROUND = "touch-hit flex size-5 shrink-0 items-center justify-center rounded-full border border-line bg-surface text-ink-2 transition-colors enabled:hover:text-ink disabled:opacity-50";
// shown while the row is hovered or has the focus; always on a screen without hover
const ON_ACTIVE = "opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 [@media(hover:none)]:opacity-100";

/** One queued message. `photos`: ones it went with beside its words (a Persona chat's, when this browser sent it). */
function QueueRow({ row, n, photos, editable, movable, takesNow, agent, explain, explainId, busy, locked, dragging, rowRef, handleRef, onKey, onPointerDown, onSendNow, onEdit, onDelete }: {
  row: Row;
  n: number;
  photos: number;
  editable: boolean;
  movable: boolean;
  takesNow: boolean;
  agent: string;
  /** What sending now does, as Send now's tooltip and description. */
  explain: string;
  explainId: string;
  busy: boolean;
  locked: boolean;
  dragging: boolean;
  rowRef: (el: HTMLDivElement | null) => void;
  handleRef: (el: HTMLButtonElement | null) => void;
  onKey: (e: React.KeyboardEvent) => void;
  onPointerDown: (e: React.PointerEvent<HTMLButtonElement>) => void;
  onSendNow: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const parts = useMemo(() => parseQueued(row.raw, photos), [row.raw, photos]);
  const marks = queuedMarks(parts, photos);
  // the words, then what came with them in words (for the label and the tooltip: "2 photos, 1 context")
  const carried = marks.map((m) => markWords(m.kind, m.count)).join(t("chat-queuepanel.mark_join"));
  const words = parts.text;
  const said = [words, carried].filter(Boolean).join(" · ") || t("chat-queuepanel.no_text");
  const relayed = !row.own;
  const label = t("chat-queuepanel.row_label", { n, text: relayed ? [t("chat-queuepanel.relayed_from"), said, t("chat-queuepanel.relayed_note")].join(" · ") : said });
  // the whole message as a tooltip, only when its line is cut
  const fit = (e: React.SyntheticEvent<HTMLElement>) => {
    const el = e.currentTarget;
    if (el.scrollWidth > el.clientWidth) el.title = said;
    else el.removeAttribute("title");
  };
  const buttons = editable ? (takesNow ? 3 : 2) : 0;
  const tip = t("chat-queuepanel.send_now_tip", { agent });
  return (
    <div
      ref={rowRef}
      role="listitem"
      aria-label={label}
      data-queue-row
      data-queue-relayed={relayed || undefined}
      className={cx(
        "group flex items-center gap-1.5 rounded-[8px] border border-transparent px-2 py-0.5 transition-colors hover:bg-hover focus-within:bg-hover",
        dragging && "relative z-10 bg-hover",
      )}
    >
      {movable ? (
        <button
          ref={handleRef}
          type="button"
          aria-label={t("chat-queuepanel.drag", { n })}
          aria-keyshortcuts="ArrowUp ArrowDown Home End"
          aria-disabled={locked || undefined}
          data-queue-handle
          onKeyDown={onKey}
          onPointerDown={onPointerDown}
          className={cx(
            "touch-hit flex size-5 shrink-0 touch-none items-center justify-center rounded-[6px] text-ink-3 select-none",
            dragging ? "cursor-grabbing opacity-100" : cx("cursor-grab focus-visible:opacity-100", ON_ACTIVE),
          )}
        >
          <Icon name="grip" size={12} />
        </button>
      ) : (
        <span aria-hidden className="size-5 shrink-0" />
      )}
      {relayed && (
        <span data-queue-note title={t("chat-queuepanel.relayed_note")} className="relative top-px flex max-w-[120px] min-w-0 shrink-0 items-center gap-1.5 text-[13px] leading-[1.25] font-semibold text-ink">
          <Icon name="send" size={13} stroke={2} className="shrink-0" />
          <span className="truncate">{t("chat-queuepanel.relayed_from")}</span>
        </span>
      )}
      <p
        data-queue-row-body
        onPointerEnter={fit}
        className="relative top-px min-w-0 flex-1 truncate text-[13px] leading-[1.25] text-ink-2 transition-colors group-hover:text-ink group-focus-within:text-ink"
      >
        {marks.map((m) => (
          <span key={m.kind} data-queue-mark={m.kind} aria-hidden className="mr-1.5 inline-flex items-center gap-0.5 align-[-2px] text-ink-3">
            <Icon name={MARK_ICON[m.kind]} size={13} />
            {m.count > 1 && <span className="text-[11px] font-medium tabular-nums">{m.count}</span>}
          </span>
        ))}
        {/* a message with nothing typed (photos, a context) says what it carries */}
        {words || <span className="text-ink-3">{carried || t("chat-queuepanel.no_text")}</span>}
      </p>
      {buttons > 0 &&
        (busy ? (
          <span className={cx("flex h-5 shrink-0 items-center justify-end", buttons === 3 ? "w-[68px]" : "w-[44px]")}>
            <Spinner size={12} />
          </span>
        ) : (
          <div data-queue-actions className={cx("flex shrink-0 items-center gap-1", ON_ACTIVE)}>
            {takesNow && (
              <button
                type="button"
                data-send-now
                disabled={locked}
                onClick={onSendNow}
                aria-label={tip}
                aria-describedby={explainId}
                title={`${tip}\n${explain}`}
                className={ROUND}
              >
                <Icon name="arrowUp" size={12} stroke={2.25} />
              </button>
            )}
            <button type="button" data-queue-edit disabled={locked} onClick={onEdit} aria-label={t("chat-queuepanel.edit_label", { n })} title={t("chat-queuepanel.edit")} className={ROUND}>
              <Icon name="pencil" size={11} stroke={2.25} />
            </button>
            <button type="button" data-queue-delete disabled={locked} onClick={onDelete} aria-label={t("chat-queuepanel.delete_label", { n })} title={t("chat-queuepanel.delete")} className={ROUND}>
              <Icon name="trash" size={11} stroke={2.25} />
            </button>
          </div>
        ))}
    </div>
  );
}
