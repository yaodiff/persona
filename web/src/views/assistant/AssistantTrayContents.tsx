import { Suspense, useEffect, useRef } from "react";
import { t } from "../../i18n";
import { needsYouBySupervisor, type AssistantEntries, type NeedsYouItem } from "../../lib/assistant-entries";
import { backgroundWorkLine, type BackgroundWork } from "../../lib/background-work";
import { field } from "../../lib/feed";
import { sessionTitle } from "../../lib/format";
import { openAgent, openTask } from "../../lib/handoff";
import { interactionKicker } from "../../lib/row-line";
import { stateName } from "../../lib/state-labels";
import { useStore } from "../../lib/store";
import { openAsk, personalVerificationAsk } from "../../lib/todos";
import { reviewState } from "../../lib/supervision";
import type { Interaction, Session } from "../../lib/types";
import { Button, cx, IconButton, Loading } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { StatusMark } from "../../ui/StatusMark";
import { cardLine } from "../chat/dock";
import { TaskActivityDetail } from "./TaskActivityDetail";
import { FinishedTasks, isDecision, NeedsYouBody, OpenEntry, ParkedTasks, ProgressTaskRow, type AssistantTray } from "./Tasks";
import { conversationTitle } from "./conversations";

/** What a Needs you row says folded (#5569): its mark, the title and, under it, what it waits for, with the kind at the
 *  end. None for the rows that are one line already (an Agent with no question, a task that only waits, a failed start). */
function foldedRow(item: NeedsYouItem, calmVerify = false): { title: string; line?: string; kicker: string } | undefined {
  if (item.kind === "plan") {
    const first = item.plan.tasks[0]?.title ?? t("assistant-tasks.plan_single");
    return { title: item.plan.tasks.length > 1 ? t("assistant-tasks.needs_more", { title: first, count: item.plan.tasks.length - 1 }) : first, kicker: t("assistant-tasks.plan_waiting") };
  }
  if (item.kind === "message") return { title: [field(item.item, "sender"), field(item.item, "chat")].filter(Boolean).join(" · ") || t("assistant-tasks.task_fallback"), line: field(item.item, "text"), kicker: stateName("needs_you") };
  if (item.kind === "agent") return item.asks.length ? { title: sessionTitle(item.session), line: cardLine(item.asks[0]), kicker: interactionKicker(item.asks[0]) } : undefined;
  if (item.asks.length) return { title: item.todo.title, line: cardLine(item.asks[0]), kicker: interactionKicker(item.asks[0]) };
  // In the urgent Needs-you tray a verification whose Pass round has not arrived is already one line (the body's
  // open-task button), so it has no folded form. The calm To verify tray keeps its folded note + one-click Pass.
  if (!calmVerify && reviewState(item.todo) === "user_check" && !personalVerificationAsk(item.todo)) return undefined;
  const ask = openAsk(item.todo);
  // a decision held for the briefing says so folded too (#5803): why it did not ring
  if (isDecision(item.todo)) return { title: item.todo.title, line: ask?.verdict, kicker: ask?.for_briefing === true ? t("todos.ask_held") : stateName("needs_you") };
  if (reviewState(item.todo) === "user_check") return { title: item.todo.title, line: ask?.verdict || item.todo.supervision?.reviews?.at(-1)?.note, kicker: t("assistant-tasks.review_user_check") };
  if (ask) return { title: item.todo.title, line: ask.verdict, kicker: ask.for_briefing === true ? t("todos.ask_held") : stateName("needs_you") };
  return undefined;
}

/** What a Needs you row's open state is kept under: its key and the round it asks, so the same task or Agent asking
 *  again comes back folded while a row that only blinks out (hidden for a moment, then restored) keeps its state. */
function openKey(item: NeedsYouItem): string {
  if (item.kind === "task") {
    const round = item.todo.supervision?.reviews?.at(-1)?.at || item.todo.decision_review?.review_at || openAsk(item.todo)?.asked_at || item.asks.map((ix) => ix.id).join(",");
    return `${item.key}@${round}${item.steps?.map(openKey).join(",") ?? ""}`;
  }
  if (item.kind === "agent") return `${item.key}@${item.asks.map((ix) => ix.id).join(",")}`;
  return item.key;
}

/** A Needs you row. Open the folded line to answer in place; its expanded title opens the asking Agent (or the task
 *  for a task-level decision), while the chevron folds it again. `verify` draws calm non-attention styling for
 *  completed tasks awaiting optional owner verification (`user_check`). */
function NeedsYouRow({ item, open, onToggle, onOpenChat, verify = false }: { item: NeedsYouItem; open: boolean; onToggle: () => void; onOpenChat?: () => void; verify?: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  const opened = useRef(open);
  useEffect(() => {
    const row = ref.current;
    const first = open && !opened.current;
    opened.current = open;
    if (!first || !row) return;
    // A card taller than the list shows its top: "nearest" would leave its question and buttons below the fold.
    const reveal = () => {
      const list = row.closest("[data-tray-list]");
      row.scrollIntoView?.({ block: list && row.offsetHeight > list.clientHeight ? "start" : "nearest" });
    };
    reveal();
    // The card's body loads after the row opens (a lazy chunk): align again as the row grows, for a moment, so the
    // finished card is what ends up in view and not the placeholder.
    if (typeof ResizeObserver === "undefined") return;
    const growing = new ResizeObserver(reveal);
    growing.observe(row);
    const stop = window.setTimeout(() => growing.disconnect(), 1000);
    return () => { window.clearTimeout(stop); growing.disconnect(); };
  }, [open]);
  const steps = item.kind === "task" ? item.steps : undefined;
  const firstStep = steps?.[0];
  const showStep = firstStep && item.kind === "task" && !item.asks.length && !openAsk(item.todo) && !isDecision(item.todo);
  const shown = showStep ? firstStep : item;
  const folded = foldedRow(shown, verify) ?? (steps?.length && shown.kind === "task" ?
    { title: shown.todo.title, kicker: stateName("needs_you") } : undefined);
  // Whether the To-dos page counts this to-do as open (#3331): no attribute on rows that are no to-do. The chip badge
  // is summed from this same flag, so the trays and the header tell one number.
  const countsOpenAttr = item.kind === "task" ? { "data-counts-open": item.countsOpen ? "true" : "false" } : {};
  if (!folded) return <NeedsYouBody item={item} calmVerify={verify} onOpenChat={onOpenChat} />;
  const openTitle = item.kind === "agent" ? () => openAgent(item.session.machine_id, item.session.session_id)
    : shown.kind === "task" ? () => {
      const ask = shown.asks[0];
      if (ask && !showStep && reviewState(shown.todo) !== "user_check") openAgent(ask.machine_id, ask.session_id);
      else openTask(shown.todo.id);
    } : undefined;
  const label = <>
    {verify ? <span aria-hidden className="size-2 shrink-0 rounded-full bg-orange" /> : <StatusMark state="needs_you" />}
    <span className="min-w-0 flex-1">
      <span className="block truncate font-semibold">{folded.title}</span>
      {!open && folded.line && folded.line !== folded.kicker && <span className="block truncate text-xs text-ink-2">{folded.line}</span>}
    </span>
  </>;
  const chevron = <Icon name="chevronRight" size={14} className="shrink-0 text-ink-3 transition-transform duration-150 motion-reduce:transition-none" style={{ transform: open ? "rotate(90deg)" : "none" }} />;
  const head = open && openTitle ? (
    <div className="flex min-w-0 items-center rounded-control px-3 py-2 text-sm">
      <button type="button" data-needs-title onClick={openTitle} className="flex min-w-0 flex-1 items-center gap-2 text-left hover:underline">
        {label}
      </button>
      <button type="button" aria-expanded={open} aria-label={t("assistant-tasks.summary_collapse")} onClick={onToggle} className="touch-hit ml-2 flex size-6 shrink-0 items-center justify-center rounded-control text-ink-3 hover:bg-hover">
        {chevron}
      </button>
    </div>
  ) : (
    <button
      type="button"
      data-needs-row={open ? undefined : item.key}
      {...countsOpenAttr}
      aria-expanded={open}
      onClick={onToggle}
      className={cx("flex w-full min-w-0 items-center gap-2 rounded-control px-3 py-2 text-left text-sm hover:bg-hover", open ? "" : "bg-field")}
    >
      {label}
      {!open && <span className={cx("max-w-[40%] shrink-0 truncate text-right text-xs", verify ? "text-ink-2" : "text-attention")}>{folded.kicker}</span>}
      {chevron}
    </button>
  );
  if (!open) return head;
  return (
    <div ref={ref} data-needs-row={item.key} data-needs-open {...countsOpenAttr} className="flex scroll-m-2 flex-col gap-1">
      {head}
      <NeedsYouBody item={shown} nested calmVerify={verify} onOpenChat={onOpenChat} />
      {steps?.filter((step) => step !== shown).map((step) => (
        <div key={openKey(step)} className="flex min-w-0 flex-col gap-1">
          <OpenEntry title={step.todo.title} onOpen={() => openTask(step.todo.id)} />
          <NeedsYouBody item={step} nested onOpenChat={onOpenChat} />
        </div>
      ))}
    </div>
  );
}

/** An Agent that belongs to no task, working: one row of In progress that opens its page. */
function RunningAgentRow({ session, activity }: { session: Session; activity?: BackgroundWork }) {
  const detail = backgroundWorkLine(activity) ?? session.activity?.trim();
  return (
    <button type="button" data-running-agent={`${session.machine_id}/${session.session_id}`} onClick={() => openAgent(session.machine_id, session.session_id)} className="flex w-full min-w-0 items-center gap-3 px-3 py-2.5 text-left hover:bg-hover">
      <span className="flex w-4 shrink-0 justify-center"><StatusMark state="working" /></span>
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm font-semibold">{sessionTitle(session)}</div>
        {detail && <TaskActivityDetail activity={activity} detail={detail} />}
      </div>
      <span className="max-w-[40%] shrink-0 truncate text-right text-xs text-ink-2">{stateName("working")}</span>
      <span className="flex w-4 shrink-0 justify-center"><Icon name="chevronRight" className="text-ink-3" /></span>
    </button>
  );
}

/** One of the conversation's own cards in Needs you (#4883): the card itself is in the chat above the composer, as on
 *  iOS (ActionTrayContent's asks); its row says what it waits for and closes the tray onto it. */
function OwnAskRow({ ix, onOpen }: { ix: Interaction; onOpen: () => void }) {
  return (
    <button type="button" data-own-ask={ix.id} onClick={onOpen} className="flex items-center gap-2 rounded-control bg-field px-3 py-2 text-left text-sm">
      <StatusMark state="needs_you" />
      <span className="min-w-0 flex-1 truncate">{cardLine(ix)}</span>
      <span className="shrink-0 whitespace-nowrap text-xs text-ink-2">{interactionKicker(ix)}</span>
    </button>
  );
}

/** The tray loads when one of the assistant's capsules is opened. */
export function AssistantTrayContents({ tray, id, label, expanded, toggleExpanded, openBoard, close,
                                       conversation, entries, waiting, directWaiting, needsCount, ownAsks, openRows,
                                       toggleRow, openOwn, showShelved, toggleShelved }: {
  tray: AssistantTray;
  id: string;
  label: string;
  expanded: boolean;
  toggleExpanded: () => void;
  openBoard: () => void;
  close: () => void;
  conversation: string;
  entries: AssistantEntries;
  waiting: readonly NeedsYouItem[];
  directWaiting: boolean;
  needsCount: number;
  ownAsks: readonly Interaction[];
  openRows: ReadonlySet<string>;
  toggleRow: (key: string) => void;
  openOwn: (id: string) => void;
  showShelved: boolean;
  toggleShelved: () => void;
}) {
  const isAsking = (item: NeedsYouItem) => (item.kind === "task" || item.kind === "agent") && item.asks.length > 0;
  // While background rounds fold, rows other conversations supervise are grouped under them (#4314); with the
  // switch off, or every row this conversation's, the tray is as it was.
  const foldBackground = useStore((s) => s.assistant?.main_background_folded) !== false;
  const conversations = useStore((s) => s.assistant?.conversations);
  const groups = foldBackground ? needsYouBySupervisor(waiting, conversation, directWaiting) : [{ supervisor: conversation, items: waiting }];
  const own = groups[0].items;
  const asking = own.filter(isAsking);
  const rest = own.filter((item) => !asking.includes(item));
  const supervisorTitle = (id: string) => {
    const row = conversations?.find((c) => c.session_id === id);
    return row ? conversationTitle(row) : t("assistant-tasks.needs_group_another");
  };
  const needsRow = (item: NeedsYouItem) => {
    const key = openKey(item);
    return <NeedsYouRow key={item.key} item={item} open={openRows.has(key)} onToggle={() => toggleRow(key)} onOpenChat={close} />;
  };
  return (
    // The header stays visible while the list and its sticky actions scroll below it.
    <section id={id} aria-label={label} data-expanded={expanded || undefined} className="assistant-action-tray flex min-h-0 flex-col overflow-hidden rounded-card bg-surface shadow-card">
      <div className="flex shrink-0 items-center gap-2 bg-surface px-3 py-2 text-sm">
        <h2 className="min-w-0 flex-1 truncate font-semibold">{label}</h2>
        <Button onClick={openBoard}>{t("assistant-tasks.all")}</Button>
        <IconButton icon={expanded ? "collapse" : "expand"} label={t(expanded ? "assistant-tasks.tray_restore" : "assistant-tasks.tray_expand")} onClick={toggleExpanded} />
        <IconButton icon="x" label={t("ui-atoms.close")} onClick={close} />
      </div>
      <div data-tray-list className="min-h-0 grow overflow-y-auto overscroll-contain">
        {tray === "running" && <div className="divide-y divide-line">
          {entries.running.map((item) => item.kind === "task" ? <ProgressTaskRow key={item.key} card={item.card} conversation={conversation} activity={item.activity} countsOpen={item.countsOpen} /> : <RunningAgentRow key={item.key} session={item.session} activity={item.activity} />)}
          {!entries.running.length && <p className="px-3 py-3 text-sm text-ink-2">{t("assistant-tasks.tray_empty_progress")}</p>}
          {entries.shelved.length > 0 && (
            <button type="button" data-progress-shelved aria-expanded={showShelved} onClick={toggleShelved} title={t("assistant-tasks.tray_shelved_hint")} className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs text-ink-2 hover:bg-hover">
              <Icon name={showShelved ? "chevron" : "chevronRight"} size={12} className="shrink-0" />
              <span className="whitespace-nowrap">{t("assistant-tasks.tray_shelved", { count: entries.shelved.length })}</span>
            </button>
          )}
          {showShelved && entries.shelved.map((item) => item.kind === "task" && <ProgressTaskRow key={item.key} card={item.card} conversation={conversation} activity={item.activity} countsOpen={item.countsOpen} />)}
        </div>}
        {/* how a task ended, instead of a card in the chat (#8237): what finished in the last 24 hours, newest first, seen
            or not; one nobody opened yet says Just finished (#10922) */}
        {tray === "finished" && <div data-tray-finished className="divide-y divide-line">
          <Suspense fallback={<Loading />}><FinishedTasks items={entries.finished} conversation={conversation} /></Suspense>
        </div>}
        {tray === "verify" && <div data-tray-verify className="flex flex-col gap-2 p-2">
          {entries.verify.map((item) => {
            const key = openKey(item);
            return <NeedsYouRow key={item.key} item={item} open={openRows.has(key)} onToggle={() => toggleRow(key)} verify />;
          })}
          {!entries.verify.length && <p className="px-1 py-2 text-sm text-ink-2">{t("assistant-tasks.tray_empty_verify")}</p>}
        </div>}
        {tray === "review" && <div className="divide-y divide-line">
          {entries.review.map((item) => item.kind === "task" && <ProgressTaskRow key={item.key} card={item.card} conversation={conversation} countsOpen={item.countsOpen} supervisorReview />)}
          {!entries.review.length && <p className="px-3 py-3 text-sm text-ink-2">{t("assistant-tasks.tray_empty_review")}</p>}
        </div>}
        {tray === "parked" && <div className="divide-y divide-line">
          <Suspense fallback={<Loading />}><ParkedTasks items={entries.parked} /></Suspense>
          {!entries.parked.length && <p className="px-3 py-3 text-sm text-ink-2">{t("assistant-tasks.tray_empty_parked")}</p>}
        </div>}
        {tray === "needs" && <div className="flex flex-col gap-2 p-2">
          {asking.map(needsRow)}
          {ownAsks.map((ix) => <OwnAskRow key={ix.id} ix={ix} onOpen={() => openOwn(ix.id)} />)}
          {rest.map(needsRow)}
          {groups.slice(1).map((group) => (
            <section key={group.supervisor} data-needs-supervisor={group.supervisor} aria-label={supervisorTitle(group.supervisor)} className="flex flex-col gap-2">
              <h3 className="truncate px-1 pt-1 text-xs text-ink-2">{supervisorTitle(group.supervisor)}</h3>
              {[...group.items.filter(isAsking), ...group.items.filter((item) => !isAsking(item))].map(needsRow)}
            </section>
          ))}
          {!needsCount && <p className="px-1 py-2 text-sm text-ink-2">{t("assistant-tasks.tray_empty_pending")}</p>}
        </div>}
      </div>
    </section>
  );
}
