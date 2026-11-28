// The Finished tray's rows (#10922): drawn when the tray opens, in the chunk the Parked tray's rows load in
// (ParkedTaskRow.tsx re-exports them, so they add no lazy entry of their own), so neither the rows' list, seeing a task
// on opening it, nor the tray's empty line is part of the first screen (startup-preload's FIRST_SCREEN_BUDGET).
import type { FinishedItem } from "../../lib/assistant-entries";
import { getState } from "../../lib/store";
import { setTodoSeen, todoSeenOn } from "../../lib/todo-seen";
import { t } from "../../i18n";
import { ProgressTaskRow } from "./Tasks";

/** Opening a Finished row the owner has not seen counts as seeing it (#5707), where the hub keeps seen marks: it stays in
 *  Finished without its Just finished mark (#10922); a failure leaves the mark. */
function seeFinished(item: FinishedItem) {
  const s = getState();
  if (!item.fresh || !todoSeenOn(s.features)) return;
  void setTodoSeen(item.todo.id, true, item.at).catch(() => undefined);
}

/** How a task ended, instead of a card in the chat (#8237): what finished in the last 24 hours, newest first, seen or not;
 *  one nobody opened yet says Just finished (#10922). Its line when nothing is left, the tray kept open past the last. */
export function FinishedTaskRows({ items, conversation }: { items: readonly FinishedItem[]; conversation: string }) {
  if (!items.length) return <p className="px-3 py-3 text-sm text-ink-2">{t("assistant-finishedtaskrows.empty")}</p>;
  return <>{items.map((item) => <ProgressTaskRow key={item.key} card={item.card} conversation={conversation} countsOpen={false} inFinished fresh={item.fresh} onOpen={() => seeFinished(item)} />)}</>;
}
