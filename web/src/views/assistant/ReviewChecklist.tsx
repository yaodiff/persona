import { lazy, Suspense, useState } from "react";
import { t } from "../../i18n";
import { resultExtras } from "../../lib/brief";
import { useCachedDirectionNames } from "../../lib/direction-names";
import { evidenceAuthorLabel, type EvidenceAuthorContext } from "../../lib/evidence-author";
import { defaultManager, sameManager, useShownManager } from "../../lib/manager-choice";
import { useStore } from "../../lib/store";
import { autoReviewLine, checklist, latestReview, type CheckMark, type Supervision } from "../../lib/supervision";
import { openMergePRs, triage } from "../../lib/todo-triage";
import type { Todo, SessionLookup } from "../../lib/todos";
import { cx } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { ReviewBadge } from "./Supervision";
import { TaskWaitInfo } from "./TaskWaitInfo";

const BriefResultExtras = lazy(() => import("./TaskBrief").then((m) => ({ default: m.BriefResultExtras })));

const MARK: Record<CheckMark, { icon: "check" | "x" | "clock" | "minus"; className: string; label: () => string }> = {
  met: { icon: "check", className: "text-green", label: () => t("assistant-tasks.review_met") },
  missing: { icon: "x", className: "text-red", label: () => t("assistant-tasks.review_missing") },
  open: { icon: "clock", className: "text-ink-3", label: () => t("assistant-tasks.review_open") },
  // added after the acceptance, which still stands (#3156): nothing waits on it
  unchecked: { icon: "minus", className: "text-ink-3", label: () => t("assistant-tasks.review_unchecked") },
};

/** The task's criteria, each as its latest report or review found it, and what the supervisor said last. */
export function ReviewChecklist({ todo, lookup, compact = false }: { todo: Todo; lookup?: SessionLookup; compact?: boolean }) {
  const cachedDirectionNames = useCachedDirectionNames();
  const assistant = useStore((s) => s.assistant);
  const shown = useShownManager();
  const sup = todo.supervision;
  if (!sup) return null;
  const list = checklist(sup);
  const review = latestReview(sup);
  const parked = todo.wait == null && triage(todo, lookup, Date.now()).section === "parked";
  // What this list cannot say of finished work (#3822): the criteria a scope change dropped, and what was done beyond the brief.
  const extras = todo.done_at ? resultExtras(todo) : undefined;
  const hasReviewNote = Boolean(review?.note && review.note !== todo.ask?.summary);
  const hasAutoReview = Boolean(autoReviewLine(sup));
  if (compact && list.length === 0 && !extras && !parked && !hasReviewNote && !hasAutoReview) {
    return (
      <section
        data-testid="review-checklist"
        data-compact-empty="true"
        className="mt-3 flex flex-wrap items-center gap-x-2 gap-y-1 text-label text-ink-3"
      >
        <span className="font-medium text-ink-2">{t("assistant-tasks.review_title")}</span>
        <span aria-hidden>·</span>
        <span>{t("assistant-tasks.review_none")}</span>
        <ReviewBadge todo={todo} />
        {sup.expect_minutes ? (
          <span className="ml-auto text-submeta text-ink-3">
            {t("assistant-tasks.review_expect", { minutes: sup.expect_minutes })}
          </span>
        ) : null}
      </section>
    );
  }
  const assistantMatchesTodo = Boolean(assistant && sameManager(assistant.manager ?? shown, todo.manager));
  const authorCtx: EvidenceAuthorContext = {
    manager: todo.manager,
    directionNames: sameManager(todo.manager, defaultManager()) ? cachedDirectionNames : undefined,
    conversations: assistantMatchesTodo ? assistant?.conversations : undefined,
    mainConversationId: assistantMatchesTodo ? assistant?.main : undefined,
    lookup,
  };
  return (
    <section data-testid="review-checklist" className="mt-4 rounded-control bg-inset p-3.5 shadow-hairline">
      <div className="flex items-center gap-2">
        <span className="text-[12.5px] font-semibold text-ink-2">{t("assistant-tasks.review_title")}</span>
        <ReviewBadge todo={todo} />
        <span className="flex-1" />
        {sup.expect_minutes ? <span className="text-[12px] text-ink-3">{t("assistant-tasks.review_expect", { minutes: sup.expect_minutes })}</span> : null}
      </div>
      {list.length === 0 ? (
        <p className="mt-2 text-[12.5px] text-ink-3">{t("assistant-tasks.review_none")}</p>
      ) : (
        <ul className="mt-2 flex flex-col gap-1.5">
          {list.map((c) => {
            const m = MARK[c.mark];
            const author = evidenceAuthorLabel(c.by, authorCtx);
            return (
              <li key={c.criterion} className="flex items-start gap-2 text-[13px]">
                <span role="img" aria-label={m.label()} title={m.label()} className={cx("mt-0.5 shrink-0", m.className)}>
                  <Icon name={m.icon} size={13} stroke={2.4} />
                </span>
                <span className="flex min-w-0 flex-col">
                  <span className="text-ink">{c.criterion}</span>
                  {c.evidence && <span className="text-[12px] text-ink-3 select-text">{c.evidence}</span>}
                  {(c.at || author) && (
                    <span className="flex flex-wrap gap-x-1.5 text-[11px] text-ink-3">
                      {author && <span data-testid="review-evidence-author">{author}</span>}
                      {c.at && <time dateTime={c.at} title={c.at}>{new Date(c.at).toLocaleString()}</time>}
                    </span>
                  )}
                </span>
              </li>
            );
          })}
        </ul>
      )}
      {extras && (
        <Suspense fallback={null}>
          <BriefResultExtras dropped={extras.dropped} alsoDone={extras.alsoDone} />
        </Suspense>
      )}
      {parked && <div data-testid="review-parked" className="mt-2 select-text"><TaskWaitInfo todo={todo} merges={openMergePRs(todo, Date.now())} expanded /></div>}
      {review?.note && review.note !== todo.ask?.summary && <p className="mt-2 text-[12.5px] leading-[1.6] text-ink-2 select-text">{t("assistant-tasks.review_note", { note: review.note })}</p>}
      <AutoReviewResult sup={sup} />
    </section>
  );
}

/** The independent reviewer's result (#2173) under the criteria: a line, and its report on a click. Nothing when no
 *  review ran, so a task without one looks as it did. */
function AutoReviewResult({ sup }: { sup: Supervision }) {
  const [open, setOpen] = useState(false);
  const line = autoReviewLine(sup);
  if (!line) return null;
  const findings = sup.auto_review?.findings;
  return (
    <div data-testid="auto-review" className="mt-2 flex flex-col gap-1 text-[12.5px] text-ink-2">
      <div className="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5">
        <Icon name="target" size={12} className="shrink-0" />
        <span className="shrink-0 whitespace-nowrap font-semibold">{t("assistant-task-actions.auto_review_title")}</span>
        <span className="min-w-0">· {line}</span>
        {findings && (
          <button type="button" aria-expanded={open} onClick={() => setOpen(!open)} className="touch-hit ml-auto shrink-0 whitespace-nowrap text-ink-3 hover:text-ink-2">
            {t(open ? "assistant-task-actions.auto_review_hide" : "assistant-task-actions.auto_review_show")}
          </button>
        )}
      </div>
      {open && findings && (
        <div data-testid="auto-review-findings" className="leading-[1.55] whitespace-pre-wrap text-ink select-text [overflow-wrap:anywhere]">
          {findings}
        </div>
      )}
    </div>
  );
}
