// The queued messages (Session.queued) at the top of the composer's card, as cindy's pending queue (#5327; the owner
// asked this part of the chat to follow cindy): rows in send order, the top one next, no numbers (the labels carry
// them). A row is one line, the marks of what it carries before its words; on hover or focus it shows a drag handle
// and round buttons: Send now (the tooltip explains whether it sends the next message or the whole queue), Edit
// (pulled back into the box below) and Delete. Dragging the handle, or
// ↑ ↓ Home End on it, re-queues them all in the new order. Four rows or fewer show in full; five or more show three
// and "N more".
// Messages from other conversations come last and are only shown; since #6394 they collapse into one line
// with a delivery note when all rows share one. Opened, each row names its sender once and opens in place to its whole
// text. A mixed group shows the delivery note on each row. A hairline separates the user's rows from the relayed ones.
// Every change goes through the hub (POST …/queue {keep}, …/queue/send-now) and the rows follow its next push; a
// reorder keeps its new order on screen until then. Its own chunk: drawn only while something is queued
// (QueueStrip.tsx loads it).
import { COMPOSER_INPUT } from "../../lib/dock-focus";
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type RefObject } from "react";
import { api, errorText, fileDownloadURL, fileThumbURL, sessionPath } from "../../lib/api";
import { agentLabel } from "../../lib/mentions";
import { useMessageFeedback, type SendFeedback } from "../../lib/message-send";
import { inSendOrder, outboxKey } from "../../lib/outbox";
import { keepAllBut, moved, movedTo, parseQueued, queuedMarks, type QueuedParts } from "../../lib/queue";
import { relayedFrom } from "../../lib/queue-relayed";
import { useStore } from "../../lib/store";
import type { Session } from "../../lib/types";
import { hasFinePointer } from "../../lib/viewport";
import { Collapse, cx, Spinner, toast } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { t } from "../../i18n";
import { useQueueEntries, type QueueCandidate, type QueueEntry, type QueueSnapshot } from "./queue-entries";
import { restoreQueued, type QueuedImage } from "./queue-restore";

export interface QueueEditResult {
  queue?: string[];
  images?: QueuedImage[][];
}

export interface QueuePanelProps {
  session: Session;
  draftKey: string;
  canEdit: boolean;
  /** The persona's name in a Persona chat (#4342). */
  persona?: string;
  pending?: QueueCandidate[];
  optimistic?: QueueEntry[];
  memory?: RefObject<QueueSnapshot | undefined>;
  onAccepted?: (ids: string[]) => void;
  cancelQueued?: (id: string) => boolean;
  /** A phone on its side: one line, "N queued", in the line of the run's status (outside the composer), that opens the
   *  same rows under it. */
  compact?: boolean;
  /** Controlled queue update handler (`keep` indexes into the own queue), replacing default `inSendOrder` + `api(".../queue")`. */
  onEditQueue?: (keep: number[]) => Promise<QueueEditResult | void> | QueueEditResult | void;
  /** Controlled Send now handler, replacing default `api(".../queue/send-now")`. */
  onSendNow?: () => Promise<void> | void;
  /** Controlled draft restoration handler when Edit pulls a message back, replacing default `restoreQueued`. */
  onRestoreQueued?: (draftKey: string, session: Session, parts: QueuedParts, images: QueuedImage[]) => Promise<void> | void;
  /** Controlled notification handler replacing default `toast`. */
  onNotify?: (message: string, tone?: "ok" | "error") => void;
  /** Controlled photo thumbnail URL resolver replacing default `fileThumbURL`. */
  resolveThumbURL?: (path: string, size: number) => string;
  /** Controlled inline photo download URL resolver replacing default `fileDownloadURL`. */
  resolveDownloadURL?: (path: string, options: { inline?: boolean }) => string;
  /** Controlled per-message transport feedback resolver replacing default `useMessageFeedback`. */
  getSendFeedback?: (id: string) => SendFeedback | undefined;
  /** Explicit offline connection state replacing default `useStore((s) => s.conn === "offline")`. */
  offline?: boolean;
}

/** cindy's: four rows or fewer show in full; past that the first three and "N more". */
const FOLD_OVER = 4;
const FOLDED_ROWS = 3;
/** Maximum inline photo thumbnails rendered per queued row before collapsing extras into a +N badge (#10871). */
const MAX_PHOTO_THUMBS = 3;
/** Thumbnail request size in px (crisp on 2x-3x displays for a 28 px tile). */
const PHOTO_THUMB_SIZE = 96;
/** How long a queue change keeps its optimistic view on screen after the hub said yes, waiting for the push that shows it. */
const HOLD_MS = 4000;
/** How long a keyboard reorder's handle takes the focus back when its row is drawn again (the agent re-queues them). */
const REFOCUS_MS = 5000;

interface Row extends QueueEntry {
  /** Its place in the hub's queue, or -1 while the local send is unconfirmed. */
  i: number;
  /** The user's own (not from another conversation). */
  own: boolean;
}

interface Drag {
  id: string;
  from: number;
  to: number;
}

function relayNote(noWake: boolean) {
  return t(noWake ? "chat-queuepanel.relayed_note_no_wake" : "chat-queuepanel.relayed_note");
}

export function QueuePanel(props: QueuePanelProps) {
  const sessionKey = JSON.stringify([props.session.machine_id, props.session.session_id, props.draftKey]);
  const prevSessionKey = useRef(sessionKey);
  const sessionGen = useRef(0);
  if (prevSessionKey.current !== sessionKey) {
    prevSessionKey.current = sessionKey;
    sessionGen.current += 1;
  }
  return <QueuePanelBody key={sessionKey} {...props} sessionGen={sessionGen} />;
}

interface RunContext {
  /** True while this exact QueuePanelBody instance is mounted and owns the active request token. */
  isMountedRun: () => boolean;
  /** True while the enclosing QueuePanel has not switched to another session (remains true on same-session queue-clear unmount). */
  isSameSession: () => boolean;
  /** Emits a notification through the action's captured handler only when the panel has not switched sessions. */
  notifyIfSameSession: (message: string, tone?: "ok" | "error") => void;
}

type QueueEditOutcome =
  | { sent: true; result: QueueEditResult | undefined }
  | { sent: false };

function QueuePanelBody({
  session,
  draftKey,
  canEdit,
  persona,
  pending = [],
  optimistic: local = [],
  memory,
  onAccepted,
  cancelQueued,
  compact,
  onEditQueue,
  onSendNow,
  onRestoreQueued,
  onNotify,
  resolveThumbURL,
  resolveDownloadURL,
  getSendFeedback,
  offline,
  sessionGen,
}: QueuePanelProps & { sessionGen: RefObject<number> }) {
  const queued = useMemo(() => session.queued ?? [], [session.queued]);
  const { rows: confirmed, accepted, planEdit } = useQueueEntries(session, pending, memory, onAccepted);
  const optimistic = local.filter((p) => !accepted.includes(p.id));
  // the user's own come first; the rest came from other conversations and are only shown (#4342)
  const own = confirmed.length;
  const agent = persona ?? agentLabel(session);
  const runtimeSource = session.source_kind === "runtime";
  const runtime = persona === undefined && runtimeSource;
  const editable = canEdit && (session.capabilities?.queue_edit ?? (!runtime && session.agent !== "agy"));
  // a Claude or agy takes queued messages at its next step, and can be given them now (#6088, #14871); so does a Persona chat's (#4342)
  const takesNow = canEdit && (session.capabilities?.queue_send_now ?? (!runtime && (session.agent === "claude" || session.agent === "agy" || (persona !== undefined && own > 0))));
  const listId = useId();
  const explainId = useId();
  const region = useRef<HTMLDivElement>(null);
  const composerEl = useRef<HTMLElement | null>(null);
  const list = useRef<HTMLDivElement>(null);
  const rowEls = useRef(new Map<string, HTMLElement>());
  const handles = useRef(new Map<string, HTMLButtonElement>());

  const [busy, setBusy] = useState<string | null>(null);
  const [withdraw, setWithdraw] = useState<{ id: string; del: boolean } | null>(null);
  const mounted = useRef(true);
  const nextToken = useRef(0);
  const inFlight = useRef<number | null>(null);
  const holdTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const sessionRef = useRef({ mid: session.machine_id, sid: session.session_id, draftKey });
  sessionRef.current = { mid: session.machine_id, sid: session.session_id, draftKey };
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
      return { ...(confirmed[i] ?? { raw, id: `${nth}:${raw}` }), i, own: i < own };
    });
  }, [queued, own, confirmed]);
  const queueKey = `${own}\u0000${queued.join("\u0000")}`;
  const holdOrder = held && held.queue === queueKey ? held.order : null;
  const locked = busy !== null || withdraw !== null || holdOrder !== null || drag !== null;

  const identity = Array.from({ length: own }, (_, i) => i);
  const order = drag ? (movedTo(own, drag.from, drag.to) ?? identity) : (holdOrder ?? identity);
  // Only the owner's own rows reorder and fold; the relayed ones live behind their own collapsed line (#6394).
  const ownRows = rows.filter((r) => r.own);
  const relayedRows = useMemo(() => rows.slice(own).map((row) => ({ row, source: relayedFrom(row.raw) })), [rows, own]);
  const noWakeCount = relayedRows.filter(({ source }) => source?.noWake).length;
  const mixedRelays = noWakeCount > 0 && noWakeCount < relayedRows.length;
  const visibleOptimistic = withdraw?.del ? optimistic.filter((p) => p.id !== withdraw.id) : optimistic;
  const shown = [...order.map((i) => ownRows[i]), ...visibleOptimistic.map((p): Row => ({ ...p, i: -1, own: true }))];
  const folds = shown.length > FOLD_OVER;
  const visible = folds && !expanded ? shown.slice(0, FOLDED_ROWS) : shown;
  const current = useRef(ownRows);
  useLayoutEffect(() => {
    current.current = ownRows;
    const box = region.current?.closest("[data-composer-top]")?.parentElement;
    if (box) composerEl.current = box;
  }, [ownRows]);
  const newest = visibleOptimistic.at(-1)?.id;
  useLayoutEffect(() => {
    if (!newest || compact) return;
    if (folds) setExpanded(true);
    const frame = requestAnimationFrame(() => {
      if (list.current) list.current.scrollTop = list.current.scrollHeight;
    });
    return () => cancelAnimationFrame(frame);
  }, [newest, compact, folds]);

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
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      inFlight.current = null;
      if (holdTimer.current !== null) {
        clearTimeout(holdTimer.current);
        holdTimer.current = null;
      }
      dragging.current?.stop();
    };
  }, []);

  const run = async (id: string, work: (ctx: RunContext) => Promise<boolean | void>): Promise<boolean> => {
    if (inFlight.current !== null) return false;
    const token = ++nextToken.current;
    const targetGen = sessionGen.current;
    const targetMid = session.machine_id;
    const targetSid = session.session_id;
    const targetDraftKey = draftKey;
    const targetNotify = onNotify;
    const isSameSession = () => sessionGen.current === targetGen;
    const isMountedRun = () =>
      mounted.current &&
      isSameSession() &&
      sessionRef.current.mid === targetMid &&
      sessionRef.current.sid === targetSid &&
      sessionRef.current.draftKey === targetDraftKey &&
      inFlight.current === token;
    const notifyIfSameSession = (message: string, tone?: "ok" | "error") => {
      if (!isSameSession()) return;
      if (targetNotify) targetNotify(message, tone);
      else toast(message, tone);
    };
    inFlight.current = token;
    setBusy(id);
    try {
      const outcome = await work({ isMountedRun, isSameSession, notifyIfSameSession });
      if (!isMountedRun()) return false;
      return outcome !== false;
    } catch (e) {
      notifyIfSameSession(t("chat-queuepanel.failed", { error: errorText(e) }), "error");
      return false;
    } finally {
      if (inFlight.current === token) {
        inFlight.current = null;
        if (mounted.current) setBusy(null);
      }
    }
  };
  // back to the box below once the row is gone (a precise pointer only: a touch screen would raise its keyboard)
  const toBox = (always: boolean) => {
    if (!mounted.current) return;
    const at = document.activeElement;
    if (!hasFinePointer() || !(always || !at || at === document.body || region.current?.contains(at))) return;
    const box = region.current?.closest("[data-composer-top]")?.parentElement ?? composerEl.current;
    box?.querySelector<HTMLElement>(COMPOSER_INPUT)?.focus({ preventScroll: true });
  };
  const holdFor = (mine: { order: number[]; queue: string }) => {
    if (holdTimer.current !== null) clearTimeout(holdTimer.current);
    holdTimer.current = setTimeout(() => {
      holdTimer.current = null;
      if (mounted.current) setHeld((h) => (h === mine ? null : h));
    }, HOLD_MS);
  };
  const editQueue = (keep: number[], isMountedRun: () => boolean): Promise<QueueEditOutcome> => {
    const targetMid = session.machine_id;
    const targetSid = session.session_id;
    const targetEdit = onEditQueue;
    const execute = async (): Promise<QueueEditOutcome> => {
      if (!isMountedRun()) return { sent: false };
      // A send whose queue row arrived before its HTTP reply can still hold the turn. Recheck indexes when the
      // edit starts, and keep later sends behind it so the keep list cannot drop one of them.
      if (current.current.length !== own || ownRows.some((r, i) => r.id !== current.current[i].id)) {
        throw new Error(t("chat-queuepanel.changed"));
      }
      const finish = planEdit(keep);
      let matched = false;
      try {
        const result = targetEdit
          ? ((await targetEdit(keep)) ?? undefined)
          : await api<QueueEditResult>(`${sessionPath(targetMid, targetSid)}/queue`, { method: "POST", json: { keep } });
        // Return the accepted edit result even if a queue-emptying push unmounted QueuePanel before HTTP resolved.
        matched = result?.queue !== undefined
          ? result.queue.length === own && result.queue.every((raw, i) => raw === ownRows[i].raw)
          : Boolean(targetEdit);
        return { sent: true, result };
      } finally {
        if (mounted.current) finish(matched);
      }
    };
    if (targetEdit) return execute();
    return inSendOrder(outboxKey(targetMid, targetSid), execute);
  };
  const act = (row: Row, del: boolean) => {
    const local = row.i < 0;
    if (local && !cancelQueued?.(row.id)) {
      // The POST has started. Wait for the authoritative row and use the normal queue edit.
      setWithdraw({ id: row.id, del });
      return;
    }
    if (!local && inFlight.current !== null) return;
    // Snapshot the target session, draft key, row, and handlers when the action starts so pull-back
    // restoration still writes back to the originating draft after queue-clear unmount and never writes to a newer session.
    const targetSession = session;
    const targetDraftKey = draftKey;
    const targetRestore = onRestoreQueued;
    const targetEdit = onEditQueue;
    const targetIndex = row.i;
    const targetRaw = row.raw;
    const targetImages = row.images;
    const keep = !local ? keepAllBut(own, targetIndex) : null;
    const mine = keep ? { order: keep, queue: queueKey } : null;
    if (del && mine) setHeld(mine);
    void run(row.id, async (ctx) => {
      let r: QueueEditResult | undefined;
      if (!local && keep) {
        const edited = await editQueue(keep, ctx.isMountedRun);
        if (!edited.sent) return false;
        r = edited.result;
        if (!del && mine && ctx.isMountedRun()) {
          setHeld(mine);
          holdFor(mine);
        }
      }
      const raw = local
        ? targetRaw
        : r?.queue !== undefined
          ? r.queue[targetIndex]
          : (targetEdit ? targetRaw : undefined);
      const images = (local
        ? targetImages
        : r?.images?.[targetIndex] ?? (targetEdit && r?.queue === undefined ? targetImages : undefined)) ?? [];
      ctx.notifyIfSameSession(del ? t("chat-queuepanel.deleted") : t("chat-queuepanel.pulled_back"), "ok");
      if (!del && raw !== undefined) {
        const parsed = parseQueued(raw, images.length);
        if (targetRestore) await targetRestore(targetDraftKey, targetSession, parsed, images);
        else await restoreQueued(targetDraftKey, targetSession, parsed, images);
      }
      return ctx.isMountedRun();
    }).then((ok) => {
      if (mounted.current && mine && del) {
        if (ok) holdFor(mine);
        else setHeld((h) => (h === mine ? null : h));
      }
      if (ok) toBox(!del);
    });
  };
  useEffect(() => {
    if (!withdraw || optimistic.length > 0) return;
    setWithdraw(null);
    const row = ownRows.find((r) => r.id === withdraw.id);
    if (row && editable) act(row, withdraw.del);
  });
  const reorder = (keep: number[] | null, id: string) => {
    if (!keep) return;
    const mine = { order: keep, queue: queueKey };
    setHeld(mine);
    void run(id, async (ctx) => {
      const edited = await editQueue(keep, ctx.isMountedRun);
      return edited.sent && ctx.isMountedRun();
    }).then((ok) => {
      if (!mounted.current) return;
      if (!ok || keep.every((i, at) => ownRows[i].raw === ownRows[at].raw)) {
        setHeld((h) => (h === mine ? null : h));
      } else {
        holdFor(mine);
      }
    });
  };
  // at one press, with no dialog: it loses nothing (the messages go now rather than at the next step), and Stop, which
  // cuts the whole turn, and Delete beside it act at one press too (DESIGN_SYSTEM.md 4.4)
  const sendNow = (row: Row) => {
    if (inFlight.current !== null) return;
    const targetMid = session.machine_id;
    const targetSid = session.session_id;
    const targetSendNow = onSendNow;
    const targetAgent = agent;
    const mine = { order: runtimeSource ? identity.slice(1) : [], queue: queueKey };
    void run(row.id, async (ctx) => {
      if (targetSendNow) await targetSendNow();
      else await api(`${sessionPath(targetMid, targetSid)}/queue/send-now`, { method: "POST", json: {} });
      if (ctx.isMountedRun()) {
        setHeld(mine);
        holdFor(mine);
      }
      ctx.notifyIfSameSession(t("chat-queuepanel.sent_now", { agent: targetAgent }), "ok");
      return ctx.isMountedRun();
    }).then((ok) => ok && toBox(false));
  };
  // what the dialog used to say, now the button's tooltip and description
  const sendNowTip = t(runtimeSource ? "chat-queuepanel.send_now_tip_runtime" : "chat-queuepanel.send_now_tip", { agent });
  const explain = t(runtimeSource ? "chat-queuepanel.send_now_body_runtime" :
    persona !== undefined ? "chat-queuepanel.send_now_body_persona" : "chat-queuepanel.send_now_body", { agent });

  const keyMove = (e: React.KeyboardEvent, row: Row) => {
    const to = e.key === "ArrowUp" ? row.i - 1 : e.key === "ArrowDown" ? row.i + 1 : e.key === "Home" ? 0 : e.key === "End" ? own - 1 : null;
    if (to === null) return;
    e.preventDefault();
    if (locked || optimistic.length > 0) return;
    const keep = e.key.startsWith("Arrow") ? moved(own, row.i, to - row.i) : movedTo(own, row.i, to);
    if (!keep) return;
    // a row moved past the fold stays in view, and its handle keeps the focus as the rows are drawn again
    if (folds && to >= FOLDED_ROWS) setExpanded(true);
    refocus.current = { id: row.id, until: Date.now() + REFOCUS_MS };
    reorder(keep, row.id);
  };
  const startDrag = (e: React.PointerEvent<HTMLButtonElement>, row: Row) => {
    if (e.button !== 0 || locked || dragging.current || optimistic.length > 0) return;
    refocus.current = null;
    // the shown own rows' middles, top to bottom: the dragged one goes after every other one above the pointer
    const mids = visible.map((r) => {
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

  const total = queued.length + optimistic.length;
  const shownTotal = shown.length + relayedRows.length;
  if (!total || !shownTotal) return null;
  const hidden = shown.length - visible.length;
  const rowFor = (row: Row, pos: number) => (
    <QueueRow
      key={row.id}
      row={row}
      n={pos + 1}
      mid={session.machine_id}
      sid={session.session_id}
      cwd={session.cwd}
      editable={editable}
      movable={editable && own > 1 && row.i >= 0}
      takesNow={takesNow && row.i >= 0}
      sendNowTip={sendNowTip}
      explain={explain}
      explainId={explainId}
      busy={busy === row.id || withdraw?.id === row.id}
      locked={locked || (row.i >= 0 && optimistic.length > 0)}
      dragging={drag?.id === row.id}
      resolveThumbURL={resolveThumbURL}
      resolveDownloadURL={resolveDownloadURL}
      getSendFeedback={getSendFeedback}
      offline={offline}
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
  // Its hairline only parts it from the owner's rows above: with none of those it is the card's first thing, and a line
  // over it (and the empty list's padding) would stand alone at the card's top.
  const relayedLine = relayedRows.length > 0 && (
    <div data-queue-relayed-group className={cx(shown.length > 0 && "border-t border-line")}>
      <button
        type="button"
        aria-expanded={relayedOpen}
        data-queue-relayed-summary
        onClick={() => setRelayedOpen((o) => !o)}
        className="touch-hit flex h-8 w-full items-center gap-1.5 px-3 text-label leading-none whitespace-nowrap text-ink-3 transition-colors hover:bg-hover hover:text-ink"
      >
        {/* in the rows' 20 px handle column, so the summary's words line up with every row's */}
        <span aria-hidden className="flex size-5 shrink-0 items-center justify-center">
          <Icon name="chat" size={13} stroke={2} />
        </span>
        <span className="min-w-0 flex-1 truncate text-left">
          <span className="font-medium text-ink-2">{t("chat-queuepanel.relayed_summary", { count: relayedRows.length })}</span>
          {!mixedRelays && <span data-queue-relayed-when> · {relayNote(noWakeCount > 0)}</span>}
        </span>
        <Icon name="chevron" size={13} className={cx("shrink-0 transition-transform duration-200", relayedOpen && "rotate-180")} />
      </button>
      {relayedOpen && (
        <div role="list" data-queue-relayed-list className="flex max-h-[196px] flex-col overflow-y-auto overscroll-contain px-1 pb-1">
          {relayedRows.map(({ row, source }, pos) => (
            <RelayedRow
              key={row.id}
              raw={source?.text ?? row.raw}
              from={source?.from}
              note={relayNote(source?.noWake ?? false)}
              showNote={mixedRelays}
              n={own + pos + 1}
              mid={session.machine_id}
              cwd={session.cwd}
              resolveThumbURL={resolveThumbURL}
              resolveDownloadURL={resolveDownloadURL}
            />
          ))}
        </div>
      )}
    </div>
  );
  const rowsAndFold = (
    <>
      {!compact && shown.length > 0 && (
        <div data-queue-header className="flex items-center gap-1.5 px-3 pt-2 pb-0.5 text-submeta leading-none text-ink-2">
          <Icon name="clock" size={12} stroke={2} className="shrink-0 text-ink-3" />
          <span className="font-medium">{t("chat-queuepanel.status")}</span>
        </div>
      )}
      {shown.length > 0 && (
        <div ref={list} id={listId} role="list" className="flex max-h-[196px] flex-col overflow-y-auto overscroll-contain px-1 pt-1.5 pb-1">
          {visible.map((row, pos) => rowFor(row, pos))}
        </div>
      )}
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
            aria-controls={shown.length > 0 ? listId : undefined}
            onClick={() => setOpen((o) => !o)}
            className="flex h-7 w-full min-w-0 items-center gap-1.5 rounded-[8px] px-1.5 text-left text-[11.5px] font-medium text-ink-3 transition-colors hover:bg-hover"
          >
            <Icon name="inbox" size={13} />
            <span className="min-w-0 flex-1 truncate">{t("chat-queuepanel.summary", { count: shownTotal })}</span>
            {optimistic.length > 0 ? (
              <QueueSendStatus
                mid={session.machine_id}
                sid={session.session_id}
                id={optimistic[0].id}
                getSendFeedback={getSendFeedback}
                offline={offline}
              />
            ) : busy !== null && <Spinner />}
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

// cindy's row buttons: 24 px, round, a hairline ring on the card's own fill
const ROUND = "touch-hit flex size-6 shrink-0 items-center justify-center rounded-full border border-line bg-surface text-ink-2 transition-colors enabled:hover:border-line-strong enabled:hover:bg-hover-2 enabled:hover:text-ink disabled:opacity-50";
// always-visible Send now pill on the first row: single line at every width (DESIGN_SYSTEM.md 1.2 rule 3)
const SEND_NOW_PILL = "touch-hit inline-flex h-6 shrink-0 items-center gap-1 rounded-full border border-line-strong bg-surface px-2 text-submeta font-medium leading-none whitespace-nowrap text-ink transition-colors enabled:hover:bg-hover-2 disabled:opacity-50";
// shown while the row is hovered or has the focus; always on a screen without hover
const ON_ACTIVE = "opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100 [@media(hover:none)]:opacity-100";

/** Small 28 px thumbnails for photos attached to a queued message (#10871). Uses local preview data URLs when freshly
 *  queued from this page, otherwise loads from the session's machine (file/thumb -> inline file/download -> icon fallback). */
function QueuedPhotoThumbs({ paths, previews, count, mid, cwd, resolveThumbURL, resolveDownloadURL }: {
  paths: string[];
  previews?: string[];
  count: number;
  mid: string;
  cwd?: string;
  resolveThumbURL?: (path: string, size: number) => string;
  resolveDownloadURL?: (path: string, options: { inline?: boolean }) => string;
}) {
  const [stage, setStage] = useState<Record<string, number>>({});
  if (count <= 0) return null;
  const shownCount = Math.min(count, MAX_PHOTO_THUMBS);
  const extra = count - shownCount;
  return (
    <span data-queue-mark="photo" aria-hidden className="inline-flex shrink-0 items-center gap-1">
      {Array.from({ length: shownCount }, (_, i) => {
        const local = previews?.[i];
        const path = paths[i];
        const key = local ? `preview-${i}` : path || `preview-${i}`;
        const candidates: string[] = [];
        if (local) candidates.push(local);
        if (path) {
          if (resolveThumbURL) {
            const thumb = resolveThumbURL(path, PHOTO_THUMB_SIZE);
            if (thumb) candidates.push(thumb);
          } else if (mid && !resolveDownloadURL) {
            try {
              candidates.push(fileThumbURL(mid, path, cwd, PHOTO_THUMB_SIZE));
            } catch {
              // environments without storage (e.g. Node static markup) fall back to the icon tile
            }
          }
          if (resolveDownloadURL) {
            const download = resolveDownloadURL(path, { inline: true });
            if (download) candidates.push(download);
          } else if (mid && !resolveThumbURL) {
            try {
              candidates.push(fileDownloadURL(mid, path, cwd, { inline: true }));
            } catch {
              // environments without storage (e.g. Node static markup) fall back to the icon tile
            }
          }
        }
        const cur = stage[key] ?? 0;
        const src = candidates[cur];
        return (
          <span
            key={key}
            className="relative inline-flex size-7 shrink-0 items-center justify-center overflow-hidden rounded-[6px] border border-line bg-field text-ink-3"
          >
            {src ? (
              <img
                src={src}
                alt=""
                loading="lazy"
                decoding="async"
                data-queue-photo-thumb
                onError={() => setStage((s) => ({ ...s, [key]: (s[key] ?? 0) + 1 }))}
                className="size-full object-cover"
              />
            ) : (
              <span data-queue-photo-fallback className="flex size-full items-center justify-center text-ink-3">
                <Icon name="image" size={13} />
              </span>
            )}
          </span>
        );
      })}
      {extra > 0 && (
        <span
          data-queue-photo-overflow
          className="inline-flex h-7 shrink-0 items-center justify-center rounded-[6px] border border-line bg-field px-1.5 text-submeta font-medium tabular-nums text-ink-2"
        >
          +{extra}
        </span>
      )}
    </span>
  );
}

function QueueSendStatus(props: {
  mid: string;
  sid: string;
  id: string;
  getSendFeedback?: (id: string) => SendFeedback | undefined;
  offline?: boolean;
}) {
  if (props.getSendFeedback) {
    return <QueueSendStatusOffline feedback={props.getSendFeedback(props.id)} offline={props.offline} />;
  }
  return <DefaultQueueSendFeedback mid={props.mid} sid={props.sid} id={props.id} offline={props.offline} />;
}

function DefaultQueueSendFeedback({ mid, sid, id, offline }: { mid: string; sid: string; id: string; offline?: boolean }) {
  const feedback = useMessageFeedback(mid, sid, id);
  return <QueueSendStatusOffline feedback={feedback} offline={offline} />;
}

function QueueSendStatusOffline({ feedback, offline }: { feedback: SendFeedback | undefined; offline?: boolean }) {
  if (offline !== undefined) {
    return <QueueSendStatusView feedback={feedback} offline={offline} />;
  }
  return <DefaultQueueOfflineStatus feedback={feedback} />;
}

function DefaultQueueOfflineStatus({ feedback }: { feedback: SendFeedback | undefined }) {
  const offline = useStore((s) => s.conn === "offline");
  return <QueueSendStatusView feedback={feedback} offline={offline} />;
}

function QueueSendStatusView({ feedback, offline }: { feedback: SendFeedback | undefined; offline: boolean }) {
  const label = feedback === "unknown" ? t("lib-outbox.delivery_unknown") :
    feedback === "processing" ? t("chat-segmentview.processing") :
    offline ? t("chat-chatview.waiting_for_hub") : t("chat-segmentview.sending");
  return (
    <span role="status" title={label} className="flex size-5 shrink-0 items-center justify-center">
      <Spinner />
      <span className="sr-only">{label}</span>
    </span>
  );
}

/** One of the owner's queued messages, including a send that is still on its way. */
function QueueRow({
  row,
  n,
  mid,
  sid,
  cwd,
  editable,
  movable,
  takesNow,
  sendNowTip,
  explain,
  explainId,
  busy,
  locked,
  dragging,
  resolveThumbURL,
  resolveDownloadURL,
  getSendFeedback,
  offline,
  rowRef,
  handleRef,
  onKey,
  onPointerDown,
  onSendNow,
  onEdit,
  onDelete,
}: {
  row: Row;
  n: number;
  mid: string;
  sid: string;
  cwd?: string;
  editable: boolean;
  movable: boolean;
  takesNow: boolean;
  sendNowTip: string;
  /** What sending now does, as Send now's tooltip and description. */
  explain: string;
  explainId: string;
  busy: boolean;
  locked: boolean;
  dragging: boolean;
  resolveThumbURL?: (path: string, size: number) => string;
  resolveDownloadURL?: (path: string, options: { inline?: boolean }) => string;
  getSendFeedback?: (id: string) => SendFeedback | undefined;
  offline?: boolean;
  rowRef: (el: HTMLDivElement | null) => void;
  handleRef: (el: HTMLButtonElement | null) => void;
  onKey: (e: React.KeyboardEvent) => void;
  onPointerDown: (e: React.PointerEvent<HTMLButtonElement>) => void;
  onSendNow: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const photoPreviews = useMemo(() => row.images?.map(({ mime, data }) => `data:${mime};base64,${data}`), [row.images]);
  const localPhotos = photoPreviews?.length ?? 0;
  const parts = useMemo(() => parseQueued(row.raw, localPhotos), [row.raw, localPhotos]);
  const photos = Math.max(0, localPhotos - parts.photos.length);
  const marks = queuedMarks(parts, photos);
  const photoCount = parts.photos.length + photos;
  const nonPhotoMarks = marks.filter((m) => m.kind !== "photo");
  // the words, then what came with them in words (for the label and the tooltip: "2 photos, 1 context")
  const carried = marks.map((m) => markWords(m.kind, m.count)).join(t("chat-queuepanel.mark_join"));
  const words = parts.text;
  const said = [words, carried].filter(Boolean).join(" · ") || t("chat-queuepanel.no_text");
  const label = t("chat-queuepanel.row_label", { n, text: said });
  // the whole message as a tooltip, only when its line is cut
  const fit = (e: React.SyntheticEvent<HTMLElement>) => {
    const el = e.currentTarget;
    if (el.scrollWidth > el.clientWidth) el.title = said;
    else el.removeAttribute("title");
  };
  const buttons = (editable ? 2 : 0) + (takesNow ? 1 : 0);
  const alwaysShowSendNow = n === 1 && takesNow;
  const editButtons = editable && (
    <>
      <button type="button" data-queue-edit disabled={locked} onClick={onEdit} aria-label={t("chat-queuepanel.edit_label", { n })} title={t("chat-queuepanel.edit")} className={ROUND}>
        <Icon name="pencil" size={11} stroke={2.25} />
      </button>
      <button type="button" data-queue-delete disabled={locked} onClick={onDelete} aria-label={t("chat-queuepanel.delete_label", { n })} title={t("chat-queuepanel.delete")} className={ROUND}>
        <Icon name="trash" size={11} stroke={2.25} />
      </button>
    </>
  );
  return (
    <div
      ref={rowRef}
      role="listitem"
      aria-label={label}
      data-queue-row
      className={cx(
        "group flex items-center gap-1.5 rounded-[8px] border border-transparent px-2 py-1 transition-colors hover:bg-hover focus-within:bg-hover",
        dragging && "relative z-10 bg-hover",
      )}
    >
      {row.i < 0 ? (
        <QueueSendStatus mid={mid} sid={sid} id={row.id} getSendFeedback={getSendFeedback} offline={offline} />
      ) : movable ? (
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
      {photoCount > 0 && (
        <QueuedPhotoThumbs
          paths={parts.photos}
          previews={photoPreviews}
          count={photoCount}
          mid={mid}
          cwd={cwd}
          resolveThumbURL={resolveThumbURL}
          resolveDownloadURL={resolveDownloadURL}
        />
      )}
      <p
        data-queue-row-body
        onPointerEnter={fit}
        className="relative top-px min-w-0 flex-1 truncate text-[13px] leading-[1.25] text-ink-2 transition-colors group-hover:text-ink group-focus-within:text-ink"
      >
        {nonPhotoMarks.map((m) => (
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
          <span className={cx("flex h-6 shrink-0 items-center justify-end", alwaysShowSendNow ? "w-[120px]" : buttons === 3 ? "w-[80px]" : "w-[52px]")}>
            <Spinner size={12} />
          </span>
        ) : alwaysShowSendNow ? (
          <div data-queue-actions className="flex shrink-0 items-center gap-1">
            {editable && <div className={cx("flex shrink-0 items-center gap-1", ON_ACTIVE)}>{editButtons}</div>}
            <button
              type="button"
              data-send-now
              disabled={locked}
              onClick={onSendNow}
              aria-label={sendNowTip}
              aria-describedby={explainId}
              title={`${sendNowTip}\n${explain}`}
              className={SEND_NOW_PILL}
            >
              <Icon name="arrowUp" size={11} stroke={2.25} />
              <span>{t("chat-queuepanel.send_now")}</span>
            </button>
          </div>
        ) : (
          <div data-queue-actions className={cx("flex shrink-0 items-center gap-1", ON_ACTIVE)}>
            {takesNow && (
              <button
                type="button"
                data-send-now
                disabled={locked}
                onClick={onSendNow}
                aria-label={sendNowTip}
                aria-describedby={explainId}
                title={`${sendNowTip}\n${explain}`}
                className={ROUND}
              >
                <Icon name="arrowUp" size={12} stroke={2.25} />
              </button>
            )}
            {editButtons}
          </div>
        ))}
    </div>
  );
}

/** One message another conversation (or the system) sent in: only shown. It names the chat it came from once, then its
 *  words on one line; a line cut short opens in place to its whole text (the chevron, or a click on the row that selects
 *  nothing), so nothing it says is left to a hover tooltip, and the opened text can be selected and copied. */
function RelayedRow({ raw, from, note, showNote, n, mid, cwd, resolveThumbURL, resolveDownloadURL }: {
  raw: string;
  from?: string;
  note: string;
  showNote: boolean;
  n: number;
  mid: string;
  cwd?: string;
  resolveThumbURL?: (path: string, size: number) => string;
  resolveDownloadURL?: (path: string, options: { inline?: boolean }) => string;
}) {
  const parts = useMemo(() => parseQueued(raw), [raw]);
  const marks = queuedMarks(parts);
  const nonPhotoMarks = marks.filter((m) => m.kind !== "photo");
  const carried = marks.map((m) => markWords(m.kind, m.count)).join(t("chat-queuepanel.mark_join"));
  const words = parts.text || carried || t("chat-queuepanel.no_text");
  const label = t("chat-queuepanel.row_label", {
    n,
    text: [from ? t("chat-queuepanel.relayed_from_chat", { chat: from }) : t("chat-queuepanel.relayed_from"), words, note].join(" · "),
  });
  const body = useRef<HTMLSpanElement>(null);
  const [open, setOpen] = useState(false);
  const [cut, setCut] = useState(false);
  // whether its one line is cut: measured while closed, and again as the composer's width changes
  useLayoutEffect(() => {
    const el = body.current;
    if (!el || open) return;
    const measure = () => setCut(el.scrollWidth > el.clientWidth);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const watch = new ResizeObserver(measure);
    watch.observe(el);
    return () => watch.disconnect();
  }, [open, words, from]);
  const opens = cut || open;
  const toggle = () => setOpen((o) => !o);
  return (
    <div
      role="listitem"
      aria-label={label}
      data-queue-row
      data-queue-relayed
      onClick={() => opens && !window.getSelection()?.toString() && toggle()}
      className={cx(
        "group flex gap-1.5 rounded-[8px] px-2 py-0.5 text-meta leading-[1.25] transition-colors",
        open || showNote ? "items-start" : "items-center",
        opens && "cursor-pointer hover:bg-hover focus-within:bg-hover",
      )}
    >
      <span aria-hidden className="size-5 shrink-0" />
      <div className="min-w-0 flex-1">
        <div className={cx("flex min-w-0 gap-1.5", open ? "items-start pt-0.5" : "relative top-px items-center")}>
          {from && (
            <span data-queue-from className="max-w-[40%] min-w-0 shrink-0 truncate font-medium text-ink">
              {from}
            </span>
          )}
          {parts.photos.length > 0 && (
            <QueuedPhotoThumbs
              paths={parts.photos}
              count={parts.photos.length}
              mid={mid}
              cwd={cwd}
              resolveThumbURL={resolveThumbURL}
              resolveDownloadURL={resolveDownloadURL}
            />
          )}
          <span
            ref={body}
            data-queue-row-body
            className={cx(
              "min-w-0 flex-1 text-ink-2 transition-colors",
              opens && "group-hover:text-ink",
              open ? "max-h-[132px] cursor-text overflow-y-auto overscroll-contain break-words whitespace-pre-wrap" : "truncate",
            )}
          >
            {nonPhotoMarks.map((m) => (
              <span key={m.kind} data-queue-mark={m.kind} aria-hidden className="mr-1.5 inline-flex items-center gap-0.5 align-[-2px] text-ink-3">
                <Icon name={MARK_ICON[m.kind]} size={13} />
                {m.count > 1 && <span className="text-submeta font-medium tabular-nums">{m.count}</span>}
              </span>
            ))}
            {parts.text || <span className="text-ink-3">{words}</span>}
          </span>
        </div>
        {showNote && <div data-queue-note className="pt-0.5 text-submeta text-ink-3">{note}</div>}
      </div>
      {opens && (
        <button
          type="button"
          aria-expanded={open}
          aria-label={open ? t("chat-queuepanel.less") : t("chat-queuepanel.show_all")}
          title={open ? t("chat-queuepanel.less") : t("chat-queuepanel.show_all")}
          data-queue-relayed-toggle
          onClick={(e) => {
            e.stopPropagation();
            toggle();
          }}
          className="touch-hit flex size-5 shrink-0 items-center justify-center rounded-full text-ink-3 transition-colors hover:text-ink"
        >
          <Icon name="chevron" size={13} className={cx("transition-transform duration-200", open && "rotate-180")} />
        </button>
      )}
    </div>
  );
}
