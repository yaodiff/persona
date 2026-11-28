// Collapsible summary card and consecutive-run batching for multi-task hand-offs (#17766).
// Loaded lazily when 3+ hand-off cards or consecutive hand-off feed items appear so the cold first screen stays
// well within FIRST_SCREEN_BUDGET.
import { Fragment, memo, useContext, useId, useMemo, useState } from "react";
import { field, useFeed, type FeedItem, type TaskPlan } from "../../lib/feed";
import { interruption } from "../../lib/todos";
import { stateOf } from "../../lib/format";
import { stateCount } from "../../lib/state-labels";
import { useStore, type State } from "../../lib/store";
import { useStoreRows } from "../../lib/store-rows";
import { cx } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { t } from "../../i18n";
import {
  feedWake,
  HANDOFF_COLLAPSE_MIN,
  HandoffCard,
  HandoffGroupStateContext,
  itemPlan,
  planTaskTodo,
  sessionIn,
  TASK_LINE_SLACK_MS,
  taskHandoffs,
  type Handoff,
} from "./Handoff";

/** Maximum urgent attention cards surfaced directly beneath a collapsed summary bar. */
export const HANDOFF_MAX_SURFACED = 2;

/** Live summary bucket of a single hand-off card, mirroring HandoffCard's own precedence. */
export type HandoffBucket = "failed" | "interrupted" | "needs_you" | "starting" | "working" | "done";

export function handoffLiveBucket(h: Handoff, s: State): HandoffBucket {
  if (h.mid && h.sid) {
    const session = sessionIn(s, h.mid, h.sid);
    const closed = Boolean(h.todo && s.todos.find((x) => x.id === h.todo)?.done_at);
    if (!closed && interruption(session)) return "interrupted";
    if (!session) return closed ? "done" : "starting";
    const phase = stateOf(session);
    if (phase === "needs_you") return "needs_you";
    if (phase === "error") return "failed";
    if (phase === "working") return "working";
    return "done";
  }
  if (h.failure) return "failed";
  if (h.ended) return "done";
  if (h.todo && s.todos.find((x) => x.id === h.todo)?.done_at) return "done";
  return "starting";
}

export interface HandoffSummarySegment {
  bucket: HandoffBucket;
  count: number;
  label: string;
  tone: "danger" | "attention" | "muted";
}

const BUCKET_ORDER: readonly HandoffBucket[] = ["failed", "interrupted", "needs_you", "starting", "working", "done"];

function summarySegment(bucket: HandoffBucket, count: number): HandoffSummarySegment {
  switch (bucket) {
    case "failed":
      return { bucket, count, label: t("assistant-tasks.multi_handoff_failed", { count }), tone: "danger" };
    case "interrupted":
      return { bucket, count, label: t("assistant-tasks.multi_handoff_interrupted", { count }), tone: "danger" };
    case "needs_you":
      return { bucket, count, label: stateCount("needs_you", count), tone: "attention" };
    case "starting":
      return { bucket, count, label: t("assistant-tasks.multi_handoff_starting", { count }), tone: "muted" };
    case "working":
      return { bucket, count, label: stateCount("working", count), tone: "muted" };
    case "done":
      return { bucket, count, label: stateCount("done", count), tone: "muted" };
  }
}

/** Ordered non-zero live status breakdown segments for a collapsed hand-off group, urgent states first. */
export function handoffStatusSummary(buckets: readonly HandoffBucket[]): HandoffSummarySegment[] {
  const counts: Record<HandoffBucket, number> = { failed: 0, interrupted: 0, needs_you: 0, starting: 0, working: 0, done: 0 };
  for (const b of buckets) counts[b]++;
  const out: HandoffSummarySegment[] = [];
  for (const bucket of BUCKET_ORDER) {
    if (counts[bucket] > 0) out.push(summarySegment(bucket, counts[bucket]));
  }
  return out;
}

/** Up to HANDOFF_MAX_SURFACED urgent cards (failed / interrupted / needs_you) to keep visible while collapsed, with
 *  failures and interruptions prioritized ahead of needs_you and relative order preserved within each tier. */
export function surfacedAttentionHandoffs(handoffs: readonly Handoff[], buckets: readonly HandoffBucket[], max = HANDOFF_MAX_SURFACED): Handoff[] {
  const critical: Handoff[] = [];
  const waiting: Handoff[] = [];
  for (let i = 0; i < handoffs.length; i++) {
    const b = buckets[i];
    if (b === "failed" || b === "interrupted") critical.push(handoffs[i]!);
    else if (b === "needs_you") waiting.push(handoffs[i]!);
  }
  return [...critical, ...waiting].slice(0, max);
}

/** Compact collapsible summary card for 3+ hand-off cards with live status aggregation and urgent attention surfacing. */
export function HandoffGroup({ handoffs, groupKey }: { handoffs: Handoff[]; groupKey?: string }) {
  const groupState = useContext(HandoffGroupStateContext);
  const [expanded, setExpanded] = useState<boolean>(() => Boolean(groupKey && groupState?.get(groupKey)));
  const listId = useId();
  // Primitive string selector so unrelated store pushes never re-render the summary group.
  const bucketKey = useStore((s) => handoffs.map((h) => handoffLiveBucket(h, s)).join(","));
  const buckets = bucketKey ? (bucketKey.split(",") as HandoffBucket[]) : [];
  const segments = handoffStatusSummary(buckets);
  const surfaced = expanded ? [] : surfacedAttentionHandoffs(handoffs, buckets);
  const toggle = () => {
    setExpanded((prev) => {
      const next = !prev;
      if (groupKey && groupState) groupState.set(groupKey, next);
      return next;
    });
  };
  return (
    <div data-handoff-cards={handoffs.length} data-handoff-group={expanded ? "expanded" : "collapsed"} className="flex w-full min-w-0 flex-col gap-2">
      <button
        type="button"
        data-handoff-group-toggle
        aria-expanded={expanded}
        aria-controls={expanded ? listId : undefined}
        onClick={toggle}
        className="group card-interactive flex w-full min-w-0 items-center gap-2.5 rounded-card bg-surface p-3 text-left shadow-card focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ink focus-visible:ring-offset-2 focus-visible:ring-offset-canvas sm:gap-3.5 sm:p-4"
      >
        <span className="flex size-8 shrink-0 items-center justify-center rounded-full bg-field transition-colors duration-150 group-hover:bg-ink sm:size-9">
          <Icon name="layers" size={17} stroke={2} className="text-ink transition-colors duration-150 group-hover:text-surface" />
        </span>
        <span className="flex min-w-0 flex-1 flex-col gap-1">
          <span className="min-w-0 truncate text-sm leading-5 font-semibold text-ink">
            {t("assistant-tasks.multi_handoff_title", { count: handoffs.length })}
          </span>
          <span data-handoff-group-summary className="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5 text-xs leading-4">
            {segments.map((seg, i) => (
              <Fragment key={seg.bucket}>
                {i > 0 && <span aria-hidden className="text-ink-3">·</span>}
                <span
                  data-handoff-summary-state={seg.bucket}
                  className={cx(
                    "whitespace-nowrap",
                    seg.tone === "danger" ? "font-medium text-red" : seg.tone === "attention" ? "font-medium text-attention" : "text-ink-2",
                  )}
                >
                  {seg.label}
                </span>
              </Fragment>
            ))}
          </span>
        </span>
        <span className="inline-flex shrink-0 items-center gap-1 rounded-full bg-field px-2 py-1 text-xs leading-4 font-medium whitespace-nowrap text-ink-2 transition-colors duration-150 group-hover:bg-hover group-hover:text-ink sm:px-2.5">
          <span>{expanded ? t("chat-interactions.collapse") : t("assistant-tasks.multi_handoff_expand")}</span>
          <Icon name="chevron" size={13} stroke={2.2} className={cx("shrink-0 transition-transform duration-200", expanded && "rotate-180")} />
        </span>
      </button>
      {expanded ? (
        <div id={listId} data-handoff-group-list className="flex w-full min-w-0 flex-col gap-2 border-l-2 border-line pl-2.5">
          {handoffs.map((h) => <HandoffCard key={h.key} handoff={h} />)}
        </div>
      ) : surfaced.length > 0 ? (
        <div data-handoff-surfaced className="flex w-full min-w-0 flex-col gap-2 border-l-2 border-line pl-2.5">
          {surfaced.map((h) => <HandoffCard key={h.key} handoff={h} />)}
        </div>
      ) : null}
    </div>
  );
}

function FeedHandoffsBatch({ groupKey, items, plans }: { groupKey: string; items: readonly FeedItem[]; plans: Record<string, TaskPlan> }) {
  const handoffs = useStoreRows((s) => {
    const seen = new Set<string>();
    const out: Handoff[] = [];
    for (const item of items) {
      if (item.kind === "plan") {
        const plan = (item.ref ? plans[item.ref] : undefined) ?? itemPlan(item);
        if (!plan || plan.state !== "started") continue;
        for (let i = 0; i < plan.tasks.length; i++) {
          const task = plan.tasks[i]!;
          const id = planTaskTodo(plan, i, s.todos);
          const superseded = Boolean(id && !task.todo_id && Object.values(plans).some((p) => p.id !== plan.id && p.tasks.some((x) => x.todo_id === id)));
          for (const h of taskHandoffs(id ? s.todos.find((x) => x.id === id) : undefined, { key: `${plan.id}/${i}`, title: task.title, error: task.error, todoId: id, superseded }, task.continues ? "continuation" : "created", plans, s.todosLoaded, item.at)) {
            const key = seen.has(h.key) ? `${item.id}:${h.key}` : h.key;
            seen.add(key);
            out.push(key === h.key ? h : { ...h, key });
          }
        }
      } else if (item.kind === "task" && item.ref) {
        const planned = Object.values(plans).some((p) => p.conversation === item.conversation && p.tasks.some((task) => task.todo_id === item.ref));
        if (planned) continue;
        const wake = feedWake(field(item, "wake"));
        for (const h of taskHandoffs(s.todos.find((x) => x.id === item.ref), { key: item.id, title: "", todoId: item.ref }, wake, plans, s.todosLoaded, item.at, TASK_LINE_SLACK_MS)) {
          const key = seen.has(h.key) ? `${item.id}:${h.key}` : h.key;
          seen.add(key);
          out.push(key === h.key ? h : { ...h, key });
        }
      }
    }
    return out;
  });
  if (handoffs.length === 0) return null;
  if (handoffs.length >= HANDOFF_COLLAPSE_MIN) {
    return <HandoffGroup handoffs={handoffs} groupKey={groupKey} />;
  }
  return (
    <div data-handoff-cards={handoffs.length} className="flex w-full min-w-0 flex-col gap-2">
      {handoffs.map((h) => <HandoffCard key={h.key} handoff={h} />)}
    </div>
  );
}

/** Renders a contiguous run of `plan` / `task` feed items, splitting around any non-started plan (open or dismissed)
 *  while combining adjacent started-plan and standalone task hand-offs into one collapsible HandoffCards group. */
export const HandoffFeedRun = memo(function HandoffFeedRun({
  items,
  renderOther,
}: {
  items: readonly FeedItem[];
  renderOther: (item: FeedItem) => React.ReactNode;
}) {
  const plans = useFeed((s) => s.plans);
  const segments = useMemo(() => {
    const out: ({ kind: "other"; item: FeedItem } | { kind: "handoffs"; key: string; items: FeedItem[] })[] = [];
    let batch: FeedItem[] = [];
    const flush = () => {
      if (batch.length > 0) {
        out.push({ kind: "handoffs", key: batch[0]!.id, items: batch });
        batch = [];
      }
    };
    for (const item of items) {
      if (item.kind === "plan") {
        const shown = (item.ref ? plans[item.ref] : undefined) ?? itemPlan(item);
        if (!shown) continue;
        if (shown.state !== "started") {
          flush();
          out.push({ kind: "other", item });
        } else {
          batch.push(item);
        }
      } else if (item.kind === "task") {
        batch.push(item);
      }
    }
    flush();
    return out;
  }, [items, plans]);

  return (
    <>
      {segments.map((seg) =>
        seg.kind === "other"
          ? <Fragment key={seg.item.id}>{renderOther(seg.item)}</Fragment>
          : <FeedHandoffsBatch key={seg.key} groupKey={seg.key} items={seg.items} plans={plans} />,
      )}
    </>
  );
});
