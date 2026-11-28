// Messages another page sent for this conversation (lib/outbox.ts): sending ones like the composer's own bubbles,
// sent ones as plain bubbles until the transcript has them, failed ones with why and "Retry" / "Delete" ("Clear and send" when the
// computer's input box holds text it can clear), uncertain ones with their reason (waiting for approval on the computer, when
// the computer said so) and read-only "Check" instead. A failed one's message menu ends in "Delete" too (#4626).
import { useRef, useState } from "react";
import { checkOutbox, discardOutbox, entryReason, heldOnComputer, outboxMessage, retryOutbox, type OutboxEntry } from "../../lib/outbox";
import { navigate } from "../../lib/router";
import { toast } from "../../ui/atoms";
import { UserBubble } from "./SegmentView";
import { matchesQueuedEntry } from "./queue-entries";
import { t } from "../../i18n";

/** Drops non-failed outbox entries already represented in Session.queued (1-to-1) so a queued follow-up never shows as
 *  both a QueuePanel row and a duplicate faded bubble in the chat stream (#14871). Failed entries always stay visible. */
export function dedupeQueuedOutbox(entries: OutboxEntry[], queued?: string[]): OutboxEntry[] {
  if (!entries.length || !queued?.length) return entries;
  const remaining = queued.slice();
  return entries.filter((entry) => {
    if (entry.state === "failed") return true;
    const i = remaining.findIndex((raw) => matchesQueuedEntry(raw, { raw: entry.text, images: entry.images }));
    if (i === -1) return true;
    remaining.splice(i, 1);
    return false;
  });
}

/** Delete on a failed message (the row under it, its message menu): it was only on this device, so it goes at once and
 *  nothing is sent; Undo in the toast puts it back (#4626, as the apps do). */
export function deleteFailed(mid: string, sid: string, id: string) {
  const undo = discardOutbox(mid, sid, id);
  if (undo) toast(t("chat-outboxbubbles.deleted"), "ok", { label: t("chat-outboxbubbles.undo"), run: undo });
}

export function OutboxBubbles({ mid, sid, entries, queued, canRetry = true, hasCurrentTerminal }: { mid: string; sid: string; entries: OutboxEntry[]; queued?: string[]; canRetry?: boolean; hasCurrentTerminal?: boolean }) {
  const [checking, setChecking] = useState<string>();
  const checkLock = useRef(false);
  const check = async (e: OutboxEntry) => {
    if (checkLock.current) return;
    checkLock.current = true;
    setChecking(e.id);
    try {
      // A Check that found nothing new says so: a button that seems to do nothing leaves the owner unsure it ran
      if (await checkOutbox(mid, sid, e.id) === "unknown") toast(t(heldOnComputer(e) ? "lib-outbox.still_held" : "lib-outbox.still_unknown"), "info");
    } finally {
      checkLock.current = false;
      setChecking(undefined);
    }
  };
  const visible = dedupeQueuedOutbox(entries, queued);
  if (!visible.length) return null;
  return (
    <>
      {visible.map((e) => {
        const canUseCurrent = hasCurrentTerminal && (!e.dialogMachine || e.dialogMachine === mid) && (!e.dialogSession || e.dialogSession === sid);
        const target = e.dialogMachine && e.dialogSession
          ? { mid: e.dialogMachine, sid: e.dialogSession }
          : canUseCurrent ? { mid, sid } : undefined;
        return (
          <div key={e.id} data-outbox={e.id} className="flex flex-col gap-1.5">
            <UserBubble msg={outboxMessage(e)} mid={mid} sid={sid} pending={e.state === "sending"} sendState={e.state}
              onDelete={e.state === "failed" ? () => deleteFailed(mid, sid, e.id) : undefined} />
            {e.state === "failed" && (
              // An uncertain one is no failure: its reason alone, in the caution color ("not sent: not sure it arrived"
              // contradicted itself); a definite one says it did not go, in red
              <div role={e.inDoubt ? "status" : "alert"} data-in-doubt={e.inDoubt ? (heldOnComputer(e) ? "held" : "unknown") : undefined} className="flex flex-wrap items-center justify-end gap-x-2 gap-y-1 pl-[12%] text-[11.5px]">
                <span className={`min-w-0 break-words ${e.inDoubt ? "text-orange" : "text-red"}`}>{e.inDoubt ? entryReason(e) : t("chat-outboxbubbles.failed", { error: entryReason(e) })}</span>
                {!e.inDoubt && e.code === "dialog_open" && target && (
                  <button type="button" onClick={() => navigate({ name: "session", ...target, view: "term", standalone: true })} className="touch-hit whitespace-nowrap font-medium text-accent-ink hover:underline">
                    {t("chat-outboxbubbles.open_terminal")}
                  </button>
                )}
                {canRetry && !e.inDoubt && e.clearable !== undefined && (
                  <button type="button" onClick={() => void retryOutbox(mid, sid, e.id, e.clearable)} className="whitespace-nowrap font-medium text-accent-ink hover:underline">
                    {t("chat-outboxbubbles.clear_and_send")}
                  </button>
                )}
                {e.inDoubt ? (
                  <button type="button" disabled={checking === e.id} aria-busy={checking === e.id} onClick={() => void check(e)} className="whitespace-nowrap font-medium text-accent-ink hover:underline">
                    {t(checking === e.id ? "chat-outboxbubbles.checking" : "chat-outboxbubbles.check")}
                  </button>
                ) : canRetry && (
                  <button type="button" onClick={() => void retryOutbox(mid, sid, e.id)} className="whitespace-nowrap font-medium text-accent-ink hover:underline">
                    {t("chat-outboxbubbles.retry")}
                  </button>
                )}
                <button type="button" onClick={() => deleteFailed(mid, sid, e.id)} className="whitespace-nowrap text-ink-3 hover:text-ink-2">
                  {t("chat-outboxbubbles.delete")}
                </button>
              </div>
            )}
          </div>
        );
      })}
    </>
  );
}
