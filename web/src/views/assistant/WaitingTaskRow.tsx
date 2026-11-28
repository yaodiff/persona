import { useState } from "react";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import { field, useFeed } from "../../lib/feed";
import { safeWebURL } from "../../lib/format";
import { openTask } from "../../lib/handoff";
import { plainMarkdown } from "../../lib/plain-markdown";
import { putTodo, refreshTodos } from "../../lib/store";
import { heldDelivery, interruption, updateTodo, type Todo, type TodoStatus } from "../../lib/todos";
import { cx, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { progressOf, retryTaskNow, ROW_ACTIONS, useRetrying, useTaskNow, waitsLabel } from "./Tasks";

/** A task in Needs you that only waits (#8237): cut off by the model service, with why and Continue (its receipt card's
 *  until then); waiting for review, with what it handed in; finished work held for the owner's OK, with what holds it
 *  and Open task, the pull request when there is one and a two-step Mark done with Undo (nothing to approve on the row
 *  itself, #13191, #13541); else what it waits for. The title opens the task. */
export default function WaitingTaskRow({ rowKey, todo, status, countsOpenAttr }: { rowKey?: string; todo: Todo; status: TodoStatus; countsOpenAttr: { "data-counts-open"?: string } }) {
  const task = useTaskNow(todo);
  const retrying = useRetrying(todo.id);
  const summary = useFeed((s) => {
    const done = todo.origin ? s.feeds[todo.origin]?.findLast((it) => it.kind === "receipt" && it.ref === todo.id && field(it, "state") !== "failed") : undefined;
    return done ? field(done, "summary") : undefined;
  });
  const receiptPr = useFeed((s) => {
    const done = todo.origin ? s.feeds[todo.origin]?.findLast((it) => it.kind === "receipt" && it.ref === todo.id && field(it, "state") !== "failed") : undefined;
    return done ? field(done, "pr") : undefined;
  });
  const cutOff = status === "interrupted";
  const cut = cutOff ? interruption(task?.lead) : undefined;
  const canContinue = cutOff && (cut ? cut.canContinue : true);
  const hold = heldDelivery(todo);
  const approval = !cutOff && hold?.unblock === "user";
  const line = cutOff ? cut?.reason
    : approval ? hold.gate
      : todo.supervision?.state === "pending_review" ? (summary && plainMarkdown(summary)) || progressOf(task?.lead)?.text : undefined;
  const label = cut?.invalid ? t("lib-api-errors.invalid_label")
    : approval ? t("assistant-tasks.delivery_ok") // its own words: no permission card waits (#13191)
      : waitsLabel(todo, status, task?.status === status ? task.wait : undefined);
  const rawPr = todo.pr_url ?? receiptPr;
  const prUrl = safeWebURL(rawPr);
  const prNumber = prUrl ? /\/(?:pull|pulls|merge_requests)\/(\d+)(?:\/|$)/i.exec(new URL(prUrl).pathname)?.[1] : undefined;
  // Mark done takes two presses: the first arms it, the second closes the task and leaves an Undo on the toast.
  const [closing, setClosing] = useState<"idle" | "armed" | "busy" | "done">("idle");
  const close = async () => {
    setClosing("busy");
    try {
      const updated = await updateTodo(todo.id, { done: true });
      if (!updated) throw new Error("?");
      putTodo(updated);
      setClosing("done");
      toast(t("assistant-tasks.closed_task", { title: todo.title }), "ok", { label: t("assistant-tasks.undo_close"), run: () => void reopen() });
      void refreshTodos();
    } catch (e) {
      setClosing("armed");
      toast(t("assistant-tasks.close_failed", { error: errorText(e) }), "error");
    }
  };
  const reopen = async () => {
    try {
      const reopened = await updateTodo(todo.id, { done: false });
      if (reopened) putTodo(reopened);
      toast(t("assistant-tasks.close_undone"), "ok");
    } catch (e) {
      toast(t("assistant-tasks.close_undone_failed", { error: errorText(e) }), "error");
    } finally {
      void refreshTodos();
    }
  };
  return (
    <div data-needs-row={rowKey} {...countsOpenAttr} data-waiting-task={todo.id} className={cx("flex flex-col gap-1.5 rounded-control px-3 py-2 text-sm", cutOff ? "bg-danger-wash" : "bg-field")}>
      <div className="flex min-w-0 items-center gap-2">
        <Icon name={cutOff ? "info" : "hand"} className={cx("shrink-0", cutOff ? "text-red" : "text-attention")} />
        <button type="button" onClick={() => openTask(todo.id)} className="min-w-0 flex-1 truncate text-left hover:underline">{todo.title}</button>
        <span className={cx("shrink-0 text-xs", cutOff ? "text-ink" : "text-ink-2")}>{label}</span>
      </div>
      {line && <div className={cx("pl-6 text-xs", cut && !cut.canContinue ? "whitespace-normal" : "truncate", cutOff ? "text-ink" : "text-ink-2")}>{line}</div>}
      {approval && (
        <div data-card-keys className={cx(ROW_ACTIONS, "flex-wrap")}>
          <button type="button" data-open-task={todo.id} onClick={() => openTask(todo.id)} className="touch-hit inline-flex items-center gap-1 whitespace-nowrap font-semibold text-ink hover:underline">
            {t("assistant-tasks.open_task")}
          </button>
          {prUrl && prNumber && (
            <a data-pr-link={todo.id} href={prUrl} target="_blank" rel="noreferrer noopener" className="touch-hit inline-flex items-center gap-1 whitespace-nowrap font-semibold text-ink hover:underline">
              {t("assistant-tasks.progress_pr", { number: prNumber })}
            </a>
          )}
          <button
            type="button"
            data-close-task={todo.id}
            data-close-armed={closing === "armed" ? "" : undefined}
            aria-busy={closing === "busy" || undefined}
            disabled={closing === "busy" || closing === "done"}
            onClick={() => (closing === "armed" ? void close() : setClosing("armed"))}
            onKeyDown={(e) => {
              if (e.key !== "Escape" || closing !== "armed") return;
              e.preventDefault();
              setClosing("idle");
            }}
            className={cx("touch-hit inline-flex items-center gap-1 whitespace-nowrap font-semibold hover:underline disabled:opacity-50 disabled:hover:no-underline", closing === "armed" ? "text-attention" : "text-ink")}
          >
            {closing === "armed" ? (hold?.state === "pr" ? t("assistant-tasks.close_confirm_pr") : t("assistant-tasks.close_confirm"))
              : closing === "busy" ? t("assistant-tasks.closing")
                : <><Icon name="check" size={12} />{t("assistant-tasks.close_task")}</>}
          </button>
        </div>
      )}
      {canContinue && (
        <div className={ROW_ACTIONS}>
          <button type="button" data-continue-task={todo.id} disabled={retrying} onClick={() => retryTaskNow(todo, "continue")} className="touch-hit inline-flex items-center gap-1 whitespace-nowrap font-semibold text-ink hover:underline disabled:opacity-50 disabled:hover:no-underline">
            <Icon name="retry" size={12} />
            {retrying ? t("assistant-tasks.retrying") : t("assistant-tasks.continue")}
          </button>
        </div>
      )}
    </div>
  );
}
