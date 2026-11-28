import { useId, useState } from "react";
import { cardDismissed, cardFiled, cardSent, dismissMessage, draftPlaceholder, field, later, sendMessage, undoDismissMessage, useFeed, type FeedItem } from "../../lib/feed";
import { statusWord, type WaitReason } from "../../lib/todo-wait-reason";
import { clock } from "../../lib/format";
import { Button, cx, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { t } from "../../i18n";
import type { ComponentType } from "react";
import type { Todo, TodoStatus } from "../../lib/todos";
import type { Session } from "../../lib/types";

export interface DraftTaskHelpers {
  useTodo: (id: string | undefined) => Todo | undefined;
  useTaskNow: (todo: Todo | undefined) => { status: TodoStatus; lead?: Session; wait?: WaitReason } | undefined;
  progressOf: (lead: Session | undefined) => { text: string; for?: string } | null;
  TaskMark: ComponentType<{ status: TodoStatus }>;
  openTask: (id: string) => void;
}

// Loaded only when a draft/history card is opened, not by the collapsed first-screen tray.
export default function MessageDraft({ item, tasks }: { item: FeedItem; tasks: DraftTaskHelpers }) {
  const { useTodo, useTaskNow, progressOf, TaskMark, openTask } = tasks;
  const todo = useTodo(field(item, "todo_id"));
  const now = useTaskNow(todo);
  const sent = cardSent(item);
  // handled without a reply from here: nothing left to edit or send
  const filed = cardFiled(item);
  const dismissed = cardDismissed(item);
  const resolved = !!(sent || filed || dismissed);
  const busy = useFeed((s) => !!s.busy[item.id]);
  const [text, setText] = useState<string | null>(null);
  const headingId = useId();
  const draft = text ?? field(item, "draft") ?? "";
  const displayed = sent?.text ?? draft;
  const blank = resolved ? undefined : draftPlaceholder(draft);
  const dismiss = async () => {
    try {
      const receipt = await dismissMessage(item);
      if (!receipt) return;
      toast(t("assistant-task-actions.dismissed_not_sent"), "ok", {
        label: t("assistant-task-actions.undo_dismiss"),
        run: () => {
          void undoDismissMessage(item, receipt).then(
            (restored) => restored && toast(t("assistant-task-actions.dismiss_undone"), "ok"),
            (e) => toast(t("assistant-task-actions.undo_dismiss_failed", { error: String(e instanceof Error ? e.message : e) }), "error"),
          );
        },
      });
    } catch (e) {
      toast(t("assistant-task-actions.dismiss_failed", { error: String(e instanceof Error ? e.message : e) }), "error");
    }
  };
  const send = async () => {
    try {
      if (await sendMessage(item, draft)) toast(t("assistant-task-actions.sent_done"), "ok");
    } catch (e) {
      toast(t("assistant-task-actions.send_failed", { error: String(e instanceof Error ? e.message : e) }), "error");
    }
  };
  const status = now?.status;
  return (
    <div className="flex flex-col gap-2.5 rounded-card bg-surface p-4 shadow-card" data-testid="message-card">
      <div className="flex items-center gap-1.5 text-[12px] font-medium text-ink-2">
        <Icon name={field(item, "source") === "mail" ? "mail" : "chat"} size={13} className="text-accent" />
        {[field(item, "sender"), field(item, "chat")].filter(Boolean).join(" · ")}
      </div>
      <div className="border-l-[3px] border-accent pl-3 text-[14.5px]">{field(item, "text")}</div>
      {todo && status && (
        <button type="button" onClick={() => openTask(todo.id)} className="flex items-center gap-2 rounded-control bg-field px-3 py-2 text-left text-[12.5px] hover:bg-hover">
          <TaskMark status={status} />
          <span className="font-semibold">{todo.title}</span>
          <span className="min-w-0 flex-1 truncate text-ink-2">{progressOf(now?.lead)?.text || statusWord(status, now?.wait)}</span>
          <Icon name="chevronRight" size={13} className="text-ink-3" />
        </button>
      )}
      {/* handled elsewhere: no longer a draft waiting on you, so not drawn like one (#5095) */}
      <div className={cx("flex flex-col gap-1 rounded-control p-3", sent ? "bg-green-tint" : filed || dismissed ? "bg-field" : "bg-accent-tint")}>
        <span id={headingId} className={cx("flex items-center gap-1 text-[12px] font-semibold", sent ? "text-green" : filed || dismissed ? "text-ink-2" : "text-accent")}>
          <Icon name={sent ? "check" : filed || dismissed ? "archive" : "pencil"} size={12} />
          {sent ? t("assistant-task-actions.replied_as_you") : dismissed ? t("assistant-task-actions.dismiss_draft") : filed ? t("assistant-task-actions.filed_title") : t("assistant-task-actions.draft_from_progress")}
          {sent && <span className="ml-auto font-normal text-ink-3">{clock(sent.at)}</span>}
        </span>
        {!resolved ? (
          // Named by the visible heading above it; the global focus ring covers only buttons and links, so it draws its own.
          <textarea
            value={draft}
            onChange={(e) => setText(e.target.value)}
            aria-labelledby={headingId}
            rows={Math.min(6, Math.max(2, draft.split("\n").length))}
            className="-mx-1.5 resize-none rounded-control bg-transparent px-1.5 text-[14.5px] outline-none focus-visible:ring-2 focus-visible:ring-accent"
          />
        ) : displayed && (
          <div className="whitespace-pre-line text-[14.5px]">{displayed}</div>
        )}
      </div>
      {blank && <div className="text-[12px] text-red">{t("assistant-task-actions.draft_blank", { blank })}</div>}
      {filed && <div className="text-[12px] text-ink-2">{t("assistant-task-actions.filed_not_sent")}</div>}
      {dismissed && <div className="text-[12px] text-ink-2">{t("assistant-task-actions.dismissed_not_sent")}</div>}
      {!resolved && (
        <div className="flex flex-col gap-2 sm:flex-row">
          <Button tone="accent" busy={busy} disabled={!!blank || !draft.trim()} onClick={send} className="min-h-11 flex-1">
            {t("assistant-task-actions.send")}
          </Button>
          <Button disabled={busy} onClick={() => later(item.id)} className="min-h-11">
            {t("assistant-task-actions.later")}
          </Button>
          <Button disabled={busy} onClick={dismiss} className="min-h-11">
            {t("assistant-task-actions.dismiss_draft")}
          </Button>
        </div>
      )}
    </div>
  );
}
