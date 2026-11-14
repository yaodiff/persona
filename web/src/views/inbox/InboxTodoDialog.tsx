// "Add to to-dos" (#6480): the due time asked once for the rows (1 hour later · today, all day · pick a date and time · no due
// date), then one to-do per row. Opened from a row's menu, the item's page and the selecting bar; the Inbox page's
// TodoDialogHost draws it.
import { useState, useSyncExternalStore } from "react";
import { addTodos, fromLocalMoment, pickerStart, type DueChoice } from "../../lib/inbox-actions";
import type { InboxItem } from "../../lib/inbox";
import { t } from "../../i18n";
import { Button, cx, Dialog } from "../../ui/atoms";

type Opening = { items: InboxItem[]; done?: () => void };
let opening: Opening | null = null;
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());

/** Opens "Add to to-dos" for rows; `done` runs once they are added (the selecting bar ends selecting). */
export function openTodoDialog(items: InboxItem[], done?: () => void) {
  if (!items.length) return;
  opening = { items, done };
  emit();
}

function closeTodoDialog() {
  opening = null;
  emit();
}

/** Mounted once on the Inbox page. */
export function TodoDialogHost() {
  const o = useSyncExternalStore((l) => (listeners.add(l), () => void listeners.delete(l)), () => opening, () => opening);
  return o ? <TodoDialog key={o.items.map((it) => it.id).join()} items={o.items} onDone={o.done} onClose={closeTodoDialog} /> : null;
}

type Pick = "hour" | "today" | "at" | "none";

export function TodoDialog({ items, onDone, onClose }: { items: InboxItem[]; onDone?: () => void; onClose: () => void }) {
  const [pick, setPick] = useState<Pick>("today");
  const [at, setAt] = useState(pickerStart);
  const [busy, setBusy] = useState(false);
  const moment = fromLocalMoment(at);
  const due: DueChoice | null = pick === "at" ? (moment ? { kind: "at", at: moment } : null) : { kind: pick };
  const options: { key: Pick; label: string }[] = [
    { key: "hour", label: t("inbox-inboxlist.todo_hour") },
    { key: "today", label: t("inbox-inboxlist.todo_today") },
    { key: "at", label: t("inbox-inboxlist.todo_pick") },
    { key: "none", label: t("inbox-inboxlist.todo_none") },
  ];
  const add = async () => {
    if (!due) return;
    setBusy(true);
    const n = await addTodos(items, due);
    setBusy(false);
    if (n) onDone?.();
    onClose();
  };
  return (
    <Dialog open onClose={onClose} title={t("inbox-inboxlist.todo_title", { count: items.length })} width={380}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>{t("common.cancel")}</Button>
          <Button tone="primary" icon="check" busy={busy} disabled={!due} onClick={() => void add()}>{t("inbox-inboxlist.todo_add")}</Button>
        </>
      }>
      <div role="radiogroup" aria-label={t("inbox-inboxlist.todo_due")} className="flex flex-col gap-1" data-todo-due>
        {options.map((o) => (
          <button key={o.key} type="button" role="radio" aria-checked={pick === o.key} onClick={() => setPick(o.key)}
            className={cx("flex h-9 items-center gap-2.5 rounded-control px-2.5 text-left text-meta whitespace-nowrap transition-colors", pick === o.key ? "bg-accent-tint font-medium text-ink" : "text-ink-2 hover:bg-hover")}>
            <span aria-hidden className={cx("size-3.5 shrink-0 rounded-full", pick === o.key ? "bg-accent shadow-[inset_0_0_0_3px_var(--accent-tint)]" : "shadow-[inset_0_0_0_1.5px_var(--line)]")} />
            {o.label}
          </button>
        ))}
      </div>
      {pick === "at" && (
        <input type="datetime-local" value={at} onChange={(e) => setAt(e.target.value)} aria-label={t("inbox-inboxlist.todo_pick")} data-todo-at
          className="mt-2 h-9 w-full rounded-control bg-field px-2.5 text-meta text-ink outline-none focus:shadow-[inset_0_0_0_1px_var(--accent)]" />
      )}
    </Dialog>
  );
}
