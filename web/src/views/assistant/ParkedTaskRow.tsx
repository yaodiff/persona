import { openTask } from "../../lib/handoff";
import type { RunningItem } from "../../lib/assistant-entries";
import { compareWaitAge } from "../../lib/task-wait";
import { openMergePRs } from "../../lib/todo-triage";
import type { Todo } from "../../lib/todos";
import { Icon } from "../../ui/icons";
import { useNow } from "../../ui/atoms";
import { TaskWaitInfo } from "./TaskWaitInfo";

// The Finished tray's rows (#10922) load in this chunk too: one lazy entry for the trays drawn on intent, not one each.
export { FinishedTaskRows } from "./FinishedTaskRows";

/** The tray already loads waiting rows on demand; keep their ordering on that same lazy boundary. */
export function ParkedTaskRows({ items }: { items: readonly RunningItem[] }) {
  const now = useNow(60_000);
  return <>{items.filter((item) => item.kind === "task").sort((a, b) => compareWaitAge(a.todo, b.todo, now))
    .map((item) => <ParkedTaskRow key={item.key} todo={item.todo} countsOpen={item.countsOpen} />)}</>;
}

/** Preserve the dedicated waiting tray's row contract and group membership. */
export function ParkedTaskRow({ todo, countsOpen }: { todo: Todo; countsOpen: boolean }) {
  const prs = todo.wait == null ? openMergePRs(todo, Date.now()) : [];
  const isPR = todo.wait?.kind === "pr_merge" || prs.length > 0;
  return <div data-parked-task-row={todo.id} data-counts-open={countsOpen ? "true" : "false"}
    data-pr-waiting-row={isPR ? todo.id : undefined} className="flex items-start gap-2 px-3 py-2.5">
    <span data-task-mark="waiting" className="mt-0.5 shrink-0 text-ink-3"><Icon name={isPR ? "pullRequest" : "clock"} /></span>
    <div className="min-w-0 flex-1">
      <button type="button" data-pr-task-link={isPR ? todo.id : undefined} onClick={() => openTask(todo.id)}
        className="block min-w-11 max-w-full truncate text-left text-sm font-medium hover:underline pointer-coarse:min-h-11">{todo.title}</button>
      <TaskWaitInfo todo={todo} merges={prs} />
    </div>
  </div>;
}
