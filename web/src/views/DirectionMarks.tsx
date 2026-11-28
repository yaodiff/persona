// The direction tree's two marks (#4352) in the sidebar's "By pinned" tree and on the To-dos page: what a direction has
// going, as the sidebar's 常驻 rows show it, and a task's state word. Text where a state needs words, so it never rides
// on color alone.
import { memo } from "react";
import { t } from "../i18n";
import { countsLabel, userCheckLabel } from "../lib/direction-names";
import { DIRECTION_STATE_TONE, directionStateName, type Direction, type DirectionTaskState } from "../lib/direction-tree";
import { cx } from "../ui/atoms";
import { StatusMark } from "../ui/StatusMark";

/** A direction's end, as its sidebar pinned row's (#6626): the working arc while anything under it runs (the hub's
 *  `running`: an Agent of its tasks at work, the one rule on every client), then "N need you" in the needs-you tone,
 *  and optionally "M to verify" in calm secondary ink for completed tasks awaiting owner verification; nothing when none. */
export const DirectionCountsText = memo(function DirectionCountsText({ direction, className }: { direction: Pick<Direction, "counts" | "running">; className?: string }) {
  const needsYou = countsLabel(direction.counts, t);
  const toVerify = userCheckLabel(direction.counts, t);
  if (!direction.running && !needsYou && !toVerify) return null;
  const title = [needsYou, toVerify].filter(Boolean).join(" · ") || undefined;
  return (
    <span title={title} data-direction-counts data-running={direction.running || undefined} className={cx("flex shrink-0 items-center gap-1 whitespace-nowrap text-[11px] text-ink-3 tabular-nums", className)}>
      {direction.running && <StatusMark state="working" size="sm" />}
      {needsYou && <span className="font-semibold text-attention">{needsYou}</span>}
      {needsYou && toVerify && <span aria-hidden>·</span>}
      {toVerify && <span data-direction-user-check className="text-ink-3">{toVerify}</span>}
    </span>
  );
});

/** A task's state word in its tone: 等你 / 待验收 / 卡住 / 在做. */
export const DirectionStateText = memo(function DirectionStateText({ state }: { state: DirectionTaskState }) {
  return <span data-task-state={state} className={cx("shrink-0 text-[11px] font-semibold whitespace-nowrap", DIRECTION_STATE_TONE[state])}>{directionStateName(state)}</span>;
});
