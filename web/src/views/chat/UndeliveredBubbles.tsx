// The owner's messages the assistant keeps after they could not go in (lib/undelivered.ts, #10194): the owner's own
// bubble, as a failed outbox one looks (OutboxBubbles), with a line under it. Waiting: why, and that it goes by itself
// (the caution color, Delete only). Given up: why in red, Retry and Delete. On every device, from the row. A message
// held for an Agent while its computer was away (#10661, `computer` set) looks the same, its line the manager's words
// as they come ("Will send when … is back", "Waits behind the message above", why it was not sent). A chunk of its own
// with its strings outside the startup catalog (#10357): only a conversation holding a kept message draws it, and
// ChatView shows the bare bubble until it is in. ChatView also takes the box's words for an Agent whose computer is away
// from here (heldPlaceholder), for the same reason.
//
// A logical conversation's input that has not gone in (`reason` set, #17431) is drawn the same way: queued or waiting
// behind an unconfirmed one (no Delete: it goes by itself), awaiting its own receipt with the seconds left, or expired
// with Resend (sent once more) and Ignore. DeliveryBanner sums them up over the composer.
import { useEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import type { UndeliveredMessage } from "../../lib/types";
import { deliverySummary, keptBubble, secondsLeft } from "../../lib/undelivered";
import { deleteUndelivered, retryUndelivered } from "../../lib/undelivered-actions";
import { toast } from "../../ui/atoms";
import { UserBubble } from "./SegmentView";

/** The clock a countdown draws with: ticks each second while `on`. */
function useNow(on: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!on) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [on]);
  return now;
}

/** Runs one action at a time: a second tap while the first is on its way does nothing (it never sends twice). */
function useOnce(): [boolean, (f: () => Promise<void>) => Promise<void>] {
  const [busy, setBusy] = useState(false);
  const running = useRef(false);
  const run = async (f: () => Promise<void>) => {
    if (running.current) return;
    running.current = true;
    setBusy(true);
    try {
      await f();
    } catch (e) {
      toast(t("chat-undeliveredbubbles.action_failed", { error: errorText(e) }), "error");
    } finally {
      running.current = false;
      setBusy(false);
    }
  };
  return [busy, run];
}

/** The line under a kept message's bubble. */
function keptLine(m: UndeliveredMessage, failed: boolean, now: number): string {
  if (m.reason) {
    const line = m.error || t("lib-outbox.delivery_unknown");
    const left = m.reason === "awaiting_receipt" ? secondsLeft(m.deadline, now) : undefined;
    return left === undefined ? line : `${line} · ${t("chat-undeliveredbubbles.time_left", { seconds: left })}`;
  }
  if (m.delete_only && m.error) return m.error;
  if (m.computer) return m.error || t(failed ? "chat-undeliveredbubbles.held_failed" : "chat-undeliveredbubbles.held_waiting", { computer: m.computer });
  return t(failed ? "chat-undeliveredbubbles.failed" : "chat-undeliveredbubbles.waiting", { reason: m.error || t("lib-outbox.delivery_unknown") });
}

/** What an Agent's message box says while its computer is away, on a hub that holds the messages sent meanwhile. */
export const heldPlaceholder = (computer: string): string => t("chat-undeliveredbubbles.held_placeholder", { computer });

export function UndeliveredBubble({ mid, sid, m }: { mid: string; sid: string; m: UndeliveredMessage }) {
  const [busy, run] = useOnce();
  const failed = m.state === "failed";
  const expired = m.reason === "receipt_expired";
  // a logical input still on its way goes by itself: nothing to delete until it expires
  const canDelete = !(m.reason && !failed);
  const now = useNow(m.reason === "awaiting_receipt");
  return (
    <div data-undelivered={m.id} data-undelivered-state={m.state} data-undelivered-reason={m.reason} data-undelivered-held={m.computer ? "" : undefined} className="flex flex-col gap-1.5">
      <UserBubble msg={keptBubble(m)} mid={mid} sid={sid} sendState="failed"
        onDelete={canDelete ? () => void run(() => deleteUndelivered(mid, sid, m)) : undefined} />
      <div role={failed ? "alert" : "status"} className="flex flex-wrap items-center justify-end gap-x-2 gap-y-1 pl-[12%] text-submeta">
        <span className={`min-w-0 break-words ${failed ? "text-red" : "text-orange"}`}>
          {keptLine(m, failed, now)}
          {m.images ? ` · ${t("chat-undeliveredbubbles.images", { count: m.images })}` : ""}
        </span>
        {failed && !m.delete_only && (
          <button type="button" disabled={busy} aria-busy={busy} onClick={() => void run(() => retryUndelivered(mid, sid, m))} className="whitespace-nowrap font-medium text-accent-ink hover:underline [@media(pointer:coarse)]:min-h-11">
            {expired ? t("chat-undeliveredbubbles.resend") : t("chat-outboxbubbles.retry")}
          </button>
        )}
        {canDelete && (
          <button type="button" disabled={busy} onClick={() => void run(() => deleteUndelivered(mid, sid, m))} className="whitespace-nowrap text-ink-3 hover:text-ink-2 [@media(pointer:coarse)]:min-h-11">
            {expired ? t("chat-undeliveredbubbles.ignore") : t("chat-outboxbubbles.delete")}
          </button>
        )}
      </div>
    </div>
  );
}

/** One expired input in the banner: its words, Resend and Ignore. */
function ExpiredLine({ mid, sid, m }: { mid: string; sid: string; m: UndeliveredMessage }) {
  const [busy, run] = useOnce();
  const words = m.text.trim() || (m.images ? t("chat-undeliveredbubbles.images", { count: m.images }) : "");
  return (
    <li data-delivery-expired={m.id} className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
      <span className="min-w-0 flex-1 truncate text-ink-2" title={m.text}>{words}</span>
      <button type="button" disabled={busy} aria-busy={busy} onClick={() => void run(() => retryUndelivered(mid, sid, m))} className="whitespace-nowrap font-medium text-accent-ink hover:underline [@media(pointer:coarse)]:min-h-11">
        {t("chat-undeliveredbubbles.resend")}
      </button>
      <button type="button" disabled={busy} onClick={() => void run(() => deleteUndelivered(mid, sid, m))} className="whitespace-nowrap text-ink-3 hover:text-ink-2 [@media(pointer:coarse)]:min-h-11">
        {t("chat-undeliveredbubbles.ignore")}
      </button>
    </li>
  );
}

/** At most this many expired inputs get their own line in the banner; the rest are counted (their bubbles have it all). */
const BANNER_EXPIRED_LINES = 3;

/** Over the composer while a logical conversation holds input that has not gone in (#17431): which one waits for its
 *  receipt and how long it still waits, how many wait behind it, and the expired ones with Resend / Ignore. Never an
 *  idle-looking chat that swallowed the message. Nothing for kept or held messages: their bubbles say it. */
export function DeliveryBanner({ mid, sid, list }: { mid: string; sid: string; list: readonly UndeliveredMessage[] }) {
  const summary = deliverySummary(list);
  const now = useNow(Boolean(summary.head));
  if (!summary.waiting && !summary.expired.length) return null;
  const head = summary.head;
  const left = head ? secondsLeft(head.deadline, now) : undefined;
  const behind = head?.behind ?? 0;
  const status = head
    ? [t(left ? "chat-undeliveredbubbles.banner_awaiting" : "chat-undeliveredbubbles.banner_awaiting_now", { seconds: left ?? 0 }),
      behind > 0 ? t("chat-undeliveredbubbles.banner_behind", { count: behind }) : ""].filter(Boolean).join(" ")
    : summary.waiting ? t("chat-undeliveredbubbles.banner_queued", { count: summary.waiting }) : "";
  const shown = summary.expired.slice(0, BANNER_EXPIRED_LINES);
  const more = summary.expired.length - shown.length;
  return (
    <div data-delivery-banner="" className="flex flex-col gap-1.5 rounded-card border border-line bg-inset px-3 py-2 text-xs">
      {status && (
        <p role="status" data-delivery-status={head ? "awaiting" : "queued"} className="min-w-0 break-words text-orange">
          {status}
        </p>
      )}
      {summary.expired.length > 0 && (
        <div role="alert" className="flex min-w-0 flex-col gap-1">
          <p className="min-w-0 break-words font-medium text-red">{t("chat-undeliveredbubbles.banner_expired", { count: summary.expired.length })}</p>
          <ul className="flex min-w-0 flex-col gap-1">
            {shown.map((m) => <ExpiredLine key={m.id} mid={mid} sid={sid} m={m} />)}
          </ul>
          {more > 0 && <p className="text-ink-3">{t("chat-undeliveredbubbles.banner_expired_more", { count: more })}</p>}
        </div>
      )}
    </div>
  );
}
