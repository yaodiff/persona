import { useId, useState } from "react";
import { formatDate, t } from "../../i18n";
import { waitSummary } from "../../lib/state-labels";
import { parseTodoWaitPRBlocker } from "../../lib/todo-wait-wire";
import { taskWait } from "../../lib/task-wait-info";
import { waitSource } from "../../lib/task-wait";
import { normalizedPR, prBlockers } from "../../lib/todo-triage";
import { href, onLinkClick } from "../../lib/router";
import type { PRFacts, Todo } from "../../lib/todos";
import { Button, copyText, cx, Dialog, useNow } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

function elapsed(since: number | undefined, now: number, request: boolean): string {
  if (since === undefined) return t("task-wait.age_unknown");
  const minutes = Math.max(0, Math.floor((now - since) / 60_000));
  const age = minutes < 1 ? t("task-wait.just_now") : minutes < 60 ? t("task-wait.minutes", { count: minutes }) :
    minutes < 1440 ? t("task-wait.hours", { count: Math.floor(minutes / 60) }) : t("task-wait.days", { count: Math.floor(minutes / 1440) });
  return t(request ? "task-wait.waiting_age" : "task-wait.parked_age", { age });
}

function sourceLabel(source: string, todo: Todo): string {
  const url = new URL(source);
  const github = url.hostname === "github.com" && url.pathname.match(/^\/[^/]+\/[^/]+\/(pull|issues)\/([1-9]\d*)(?:\/files)?\/?$/);
  if (github) {
    const comment = /^#(?:issuecomment-|discussion_r|pullrequestreview-|r)\d+$/.test(url.hash);
    return t(`task-wait.source_${comment ? "comment" : github[1] === "pull" ? "pr" : "issue"}`, { number: github[2] });
  }
  if (url.hostname === "applink.feishu.cn" || url.hostname === "applink.larksuite.com") {
    const thread = todo.thread;
    if (url.pathname === "/client/thread/open" && thread?.channel === "feishu" && thread.title.trim() &&
      url.searchParams.get("open_chat_id") === thread.chat_id && url.searchParams.get("open_thread_id") === thread.thread_id) return thread.title.trim();
    const contact = todo.wait != null ? todo.wait.contact : todo.supervision?.parked?.evidence?.contact;
    if (waitSource(contact?.url) === source && contact?.channel.trim()) return contact.channel.trim();
    return t("task-wait.source_lark");
  }
  return url.host + url.pathname;
}

/** One projection for rows and task details. This reads recorded evidence; it does not watch events or send messages. */
export function TaskWaitInfo({ todo, merges: legacyMerges = [], expanded = false }: { todo: Todo; merges?: readonly PRFacts[]; expanded?: boolean }) {
  const now = useNow(60_000);
  const merges = todo.wait == null ? legacyMerges : [];
  const info = taskWait(todo, now, merges);
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState<string>();
  const draftId = useId();
  const detailsId = useId();
  if (!info) return null;
  const isExpanded = expanded || open;
  const contact = info.contact;
  const contactTime = contact?.sent_at && formatDate(contact.sent_at, { dateStyle: "medium", timeStyle: "short" });
  const age = elapsed(info.since, now, info.sinceRequest);
  const stopControls = (event: React.MouseEvent<HTMLDivElement>) => {
    const el = event.target;
    // Plain waiting text still opens its task row; links, disclosure, actions and the portalled editor do not.
    if (!(el instanceof Element) || !event.currentTarget.contains(el) || el.closest("a, button, summary, input, textarea")) event.stopPropagation();
  };
  const toggleButton = !expanded && (
    <button
      type="button"
      data-wait-toggle
      aria-expanded={open}
      aria-controls={detailsId}
      onClick={() => setOpen((was) => !was)}
      className="touch-hit ml-auto inline-flex shrink-0 items-center gap-1 whitespace-nowrap text-ink-3 hover:text-ink-2 pointer-coarse:min-h-11 pointer-coarse:min-w-11"
    >
      <span>{t(open ? "task-wait.hide_details" : "task-wait.details")}</span>
      <Icon name="chevron" size={12} className={cx("transition-transform", open && "rotate-180")} />
    </button>
  );
  const target = info.target ?? t("task-wait.target_unknown");
  const contactURL = waitSource(contact?.url);
  const contactLabel = contact && (contact.sender && contactTime
    ? t("task-wait.last_contact", { sender: contact.sender, time: contactTime, channel: contact.channel })
    : [contact.channel, contact.sender, contactTime].filter(Boolean).join(" · "));
  const sourceLinks = info.sources.filter((url) => url !== contactURL || merges.some((pr) => normalizedPR(waitSource(pr.url)) === normalizedPR(url)));
  const blockers = todo.wait?.pr_blockers?.map(parseTodoWaitPRBlocker).flatMap((key) =>
    typeof key === "string" ? [t(`assistant-tasks.pr_blocker_${key === "checks_failing" ? "checks_failed" : key}`)] : []);
  const handoff = info.handoff;
  const route = handoff && { name: "todos", id: handoff.todo_id } as const;
  const targetLine = route ? <>
    {t("task-wait.kind_todo_handoff")} <a data-wait-target href={href(route)} className={cx("text-accent-ink hover:underline", isExpanded && "inline-flex items-center pointer-coarse:min-h-11 pointer-coarse:min-w-11")}
      onClick={(event) => { event.stopPropagation(); onLinkClick(event, route); }}>{handoff?.title || target}</a>
  </> : <span data-wait-target>{todo.wait != null ? waitSummary(todo.wait) : t(`task-wait.kind_${info.kind}`) + t("task-wait.target", { target })}</span>;
  return <div data-task-wait-info={todo.id} data-wait-kind={info.kind} data-wait-overdue={info.overdue ? "true" : "false"}
    data-wait-collapsed={isExpanded ? "false" : "true"}
    className="min-w-0 space-y-1 text-xs text-ink-2" onClick={stopControls}>
    <div className={cx("flex min-w-0 gap-x-2 gap-y-1", isExpanded ? "flex-wrap items-baseline" : "items-center", info.overdue && "text-red")}>
      {isExpanded ? (
        <>
          <span className="min-w-0 break-words">{targetLine}</span>
          <span data-wait-age className="min-w-0 break-words tabular-nums" title={info.since ? new Date(info.since).toISOString() : undefined}>{age}</span>
          {info.overdue && <span data-wait-due className="font-medium">{t("task-wait.follow_up_due")}</span>}
        </>
      ) : (
        <span className="min-w-0 flex-1 truncate">
          {targetLine}
          {" · "}
          <span data-wait-age className="tabular-nums" title={info.since ? new Date(info.since).toISOString() : undefined}>{age}</span>
          {info.overdue && <>{" · "}<span data-wait-due className="font-medium">{t("task-wait.follow_up_due")}</span></>}
        </span>
      )}
      {toggleButton}
    </div>
    {isExpanded && (
      <div id={detailsId} data-wait-details className="space-y-1 break-words">
        {contactURL ? <a data-wait-contact href={contactURL} target="_blank" rel="noreferrer noopener" className="block break-words text-accent-ink hover:underline pointer-coarse:min-h-11">
          {contactLabel}
        </a> : contact ? <p data-wait-contact>{contactLabel}</p> : info.contactExpected && <p data-wait-contact-missing className="text-ink-3">{t("task-wait.contact_unknown")}</p>}
        {handoff ? <p data-wait-wake>{handoff.check}</p> : (info.wake || todo.wait == null) && <p data-wait-wake>{info.wake ? t("task-wait.wake", { wake: info.wake }) : t("task-wait.wake_unknown")}</p>}
        {info.wakeAt !== undefined && <p>{t(info.kind === "usage_limit" ? "lib-usage-limit.resets" : "task-wait.recheck", { time: formatDate(info.wakeAt, { dateStyle: "medium", timeStyle: "short" }) })}</p>}
        {blockers?.length ? <p>{blockers.join(" · ")}</p> : null}
        {todo.wait?.step && <p>{todo.wait.step}</p>}
        {sourceLinks.length > 0 && <div className="space-y-1">{sourceLinks.map((url) => {
          const pr = merges.find((facts) => normalizedPR(waitSource(facts.url)) === normalizedPR(url));
          return <div key={url} data-pr-waiting-url={pr ? url : undefined}>
            {url !== contactURL && <a data-wait-source href={url} target="_blank" rel="noreferrer noopener" title={url} className="block break-all text-accent-ink hover:underline pointer-coarse:min-h-11">{sourceLabel(url, todo)}</a>}
            {pr && <p>{prBlockers(todo, pr).map((key) => t(`assistant-tasks.pr_blocker_${key}`)).join(" · ")}</p>}
          </div>;
        })}</div>}
        {info.reason && <p data-wait-reason>{info.reason}</p>}
        {contact?.request && <p className="whitespace-pre-wrap" data-wait-request>{t("task-wait.request", { request: contact.request })}</p>}
        {info.until !== undefined && <p>{t(handoff ? "task-wait.handoff_deadline" : todo.wait != null ? "task-wait.deadline" : "task-wait.fallback", { time: formatDate(info.until, { dateStyle: "medium", timeStyle: "short" }) })}</p>}
        {todo.wait == null && !handoff && <p className="text-ink-3">{t(todo.supervision?.parked?.evidence ? "task-wait.evidence_note" : "task-wait.missing_evidence_note")}</p>}
        {info.canRemind && <Button testId="wait-reminder" className="pointer-coarse:min-h-11" onClick={() => setDraft((previous) => previous ?? t("task-wait.reminder", { target, title: todo.title, request: contact?.request ?? "" }))}>{t("task-wait.draft_reminder")}</Button>}
      </div>
    )}
    {draft !== undefined && <Dialog open onClose={() => setDraft(undefined)} title={t("task-wait.draft_reminder")} footer={<>
      <Button className="pointer-coarse:min-h-11" onClick={() => setDraft(undefined)}>{t("common.cancel")}</Button>
      <Button className="pointer-coarse:min-h-11" tone="primary" onClick={() => {
        const text = draft!;
        void copyText(text).then((copied) => {
          // Dismiss after success, but keep a failed copy or a draft edited while clipboard permission was pending.
          if (copied) setDraft((current) => current === text ? undefined : current);
        });
      }}>{t("task-wait.copy_draft")}</Button>
    </>}>
      <p className="mb-3 text-sm text-ink-2">{t("task-wait.draft_note")}</p>
      <label htmlFor={draftId} className="mb-1 block text-xs text-ink-2">{t("task-wait.message")}</label>
      <textarea id={draftId} value={draft} rows={5} onChange={(event) => setDraft(event.target.value)}
        className="w-full resize-y rounded-control border border-line bg-canvas px-3 py-2 text-sm text-ink focus:border-accent" />
      {contact && contactURL && <a data-reminder-source href={contactURL} target="_blank" rel="noreferrer noopener" className="mt-3 inline-block text-sm text-accent-ink hover:underline pointer-coarse:min-h-11">{t("task-wait.open_channel", { channel: contact.channel })}</a>}
    </Dialog>}
  </div>;
}
