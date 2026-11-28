import { useMemo, useState } from "react";
import { formatDate, t } from "../i18n";
import { foldedState } from "../lib/child-sessions";
import { plainMarkdown } from "../lib/plain-markdown";
import { waitName } from "../lib/state-labels";
import { progressLine } from "../lib/supervision";
import type { EffectiveDirection } from "../lib/todo-effective-direction";
import { waitsOnOwner, type OwnerNeed, type Triage } from "../lib/todo-triage";
import { mostUrgentAgent, openAsk, responsibleOf, taskNow, type SessionLookup, type Todo, type TodoBoardProjection, type TodoChildSummary } from "../lib/todos";
import { pref, setPref, useTodoPreference } from "../features/todos/preferences";
import { cx } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { fieldClass, Tag } from "./assistant/parts";

const HOUR_MS = 3_600_000;
export const TODO_BOARD_HOURS_KEY = "persona.todos.board.staleHours";
const GROUPING_KEY = "persona.todos.board.grouping";

type BoardGrouping = "project" | "direction";
type Progress = { at: string; text: string };
type ParentGoalRef = { id: string; title: string; loaded: boolean };
type BoardRow = {
  todo: Todo;
  progress?: Progress;
  waiting: boolean;
  stale: boolean;
  responsible: string;
  direction?: EffectiveDirection;
  parentGoal?: ParentGoalRef;
  childSummary?: TodoChildSummary;
};
type BoardGroup = { key: string; name: string; rows: BoardRow[]; open: number; waiting: number; stale: number };

function isFallbackChildDone(todo: Todo): boolean {
  if (todo.done_at || todo.cancelled_at) return true;
  return todo.view?.phase === "done" || todo.view?.section === "done";
}

function isFallbackChildNeedsYou(todo: Todo, triage: Triage | undefined): boolean {
  if (todo.view?.section === "needs_you" || todo.view?.phase === "needs_you") return true;
  if (openAsk(todo)) return true;
  if (triage && waitsOnOwner(todo, triage) === "owner_decides") return true;
  return false;
}

function isFallbackChildWorking(todo: Todo, triage: Triage | undefined, lookup: SessionLookup): boolean {
  if (todo.view?.section === "working" || todo.view?.phase === "working") return true;
  if (triage?.section === "working") return true;
  for (const agent of todo.agents ?? []) {
    if (agent.released_at) continue;
    const session = lookup(agent.machine_id, agent.session_id);
    if (session && foldedState(session) === "working") return true;
  }
  return false;
}

function resolveChildSummary(
  todo: Todo,
  directChildren: readonly Todo[] | undefined,
  triages: ReadonlyMap<string, Triage>,
  lookup: SessionLookup,
): TodoChildSummary | undefined {
  if (todo.child_summary && todo.child_summary.total > 0) return todo.child_summary;
  if (!directChildren || directChildren.length === 0) return undefined;
  let done = 0;
  let open = 0;
  let needs_you = 0;
  let working = 0;
  for (const child of directChildren) {
    if (isFallbackChildDone(child)) {
      done += 1;
      continue;
    }
    open += 1;
    const childTriage = triages.get(child.id);
    if (isFallbackChildNeedsYou(child, childTriage)) {
      needs_you += 1;
    } else if (isFallbackChildWorking(child, childTriage, lookup)) {
      working += 1;
    }
  }
  return { total: directChildren.length, open, needs_you, working, done };
}

function timestamp(value: string | undefined): string | undefined {
  return value && Number.isFinite(Date.parse(value)) ? value : undefined;
}

function oneLine(value: string | undefined): string {
  return (value ?? "").replace(/\s+/g, " ").trim();
}

function firstNoteLine(value: string): string {
  const line = value.split(/\r?\n/).find((part) => part.trim()) ?? "";
  return oneLine(plainMarkdown(line));
}

/** The latest report and its own moment, never a summary paired with an unrelated status change. */
function latestProgress(todo: Todo, lookup: SessionLookup): Progress | undefined {
  const candidates: Progress[] = [];
  const reportAt = timestamp(todo.supervision?.progress?.at);
  const report = oneLine(progressLine(todo.supervision));
  if (reportAt && report) candidates.push({ at: reportAt, text: report });
  let linkedProgress = false;
  for (const agent of todo.agents ?? []) {
    if (agent.released_at) continue;
    const session = lookup(agent.machine_id, agent.session_id);
    const current = taskNow(session, session ? foldedState(session) === "working" : false);
    if (!session || !current) continue;
    const text = oneLine(current.kind === "words" ? plainMarkdown(current.text) : current.text);
    const at = timestamp(current.kind === "step" ? current.since : undefined) ?? timestamp(session.activity_at) ?? timestamp(session.updated_at) ?? timestamp(session.state_since);
    if (at && text) {
      candidates.push({ at, text });
      linkedProgress = true;
    }
  }
  // #12712: with no linked-Agent report, an external responsible party's notes are its progress note.
  const external = responsibleOf(todo);
  const externalAt = timestamp(todo.updated_at);
  const externalNote = firstNoteLine(todo.notes ?? "");
  if (external && !linkedProgress && !(reportAt && report) && externalAt && externalNote) candidates.push({ at: externalAt, text: externalNote });
  return candidates.reduce<Progress | undefined>((latest, candidate) => !latest || Date.parse(candidate.at) > Date.parse(latest.at) ? candidate : latest, undefined);
}

function responsible(todo: Todo, lookup: SessionLookup): string {
  const stored = responsibleOf(todo)?.name;
  if (stored) return stored;
  const urgent = mostUrgentAgent(todo, lookup);
  return oneLine(urgent?.agent.title) || oneLine(urgent?.session?.title) || t("todos.board_unassigned");
}

function hoursValue(raw: string): number | undefined {
  if (!raw.trim()) return undefined;
  const value = Number(raw);
  return Number.isFinite(value) && value > 0 ? value : undefined;
}

function storedHours(): number {
  return hoursValue(pref(TODO_BOARD_HOURS_KEY) ?? "") ?? 72;
}

/** A read-only projection of the To-dos already read by the page. It starts no requests of its own. */
export function TodoBoard({
  todos,
  lookup,
  triages,
  serverBoard,
  projectedNeeds,
  projectName,
  tick,
  onOpen,
  todosV2,
  allTodos,
  effectiveDirectionOf,
}: {
  todos: readonly Todo[];
  lookup: SessionLookup;
  triages?: ReadonlyMap<string, Triage>;
  serverBoard?: TodoBoardProjection | null;
  projectedNeeds?: ReadonlyMap<string, OwnerNeed>;
  projectName: (slug: string) => string;
  tick: number;
  onOpen: (id: string) => void;
  todosV2?: boolean;
  allTodos?: readonly Todo[];
  effectiveDirectionOf?: (todo: Todo) => EffectiveDirection | undefined;
}) {
  const isV2 = todosV2 === true;
  const [hours, setHours] = useState(storedHours);
  const [draft, setDraft] = useState(() => String(storedHours()));
  const [invalid, setInvalid] = useState(false);
  const [boardGrouping, setBoardGrouping] = useTodoPreference<BoardGrouping>(GROUPING_KEY, (value) => value === "direction" ? "direction" : "project");
  const activeGrouping: BoardGrouping = isV2 ? boardGrouping : "project";

  const groups = useMemo(() => {
    const now = Date.now();
    const byId = new Map<string, Todo>();
    const directChildren = new Map<string, Todo[]>();
    if (isV2) {
      const pool = allTodos ?? todos;
      for (const td of pool) {
        if (!byId.has(td.id)) byId.set(td.id, td);
      }
      for (const td of todos) {
        if (!byId.has(td.id)) byId.set(td.id, td);
      }
      for (const td of serverBoard ? [] : byId.values()) {
        const parentId = td.part_of?.trim();
        if (!parentId || parentId === td.id) continue;
        const list = directChildren.get(parentId);
        if (list) list.push(td);
        else directChildren.set(parentId, [td]);
      }
    }

    const rows: BoardRow[] = todos.map((todo) => {
      const progress = latestProgress(todo, lookup);
      const baseline = Date.parse(progress?.at ?? todo.created_at);
      const triage = triages?.get(todo.id);
      let direction: EffectiveDirection | undefined;
      let parentGoal: ParentGoalRef | undefined;
      let childSummary: TodoChildSummary | undefined;
      if (isV2) {
        direction = effectiveDirectionOf?.(todo);
        const parentId = todo.part_of?.trim();
        if (parentId && parentId !== todo.id) {
          const parent = byId.get(parentId);
          parentGoal = {
            id: parentId,
            title: parent?.title ?? parentId,
            loaded: Boolean(parent),
          };
        }
        childSummary = triages ? resolveChildSummary(todo, directChildren.get(todo.id), triages, lookup) : todo.child_summary;
      }
      return {
        todo,
        progress,
        waiting: projectedNeeds ? projectedNeeds.has(todo.id) : Boolean(triage && waitsOnOwner(todo, triage) === "owner_decides"),
        stale: Number.isFinite(baseline) && now - baseline > hours * HOUR_MS,
        responsible: responsible(todo, lookup),
        ...(direction ? { direction } : {}),
        ...(parentGoal ? { parentGoal } : {}),
        ...(childSummary ? { childSummary } : {}),
      };
    });

    const group = (key: string, name: string, rows: BoardRow[], waiting?: number): BoardGroup => ({
      key, name, rows, waiting: waiting ?? rows.filter((row) => row.waiting).length,
      open: rows.length, stale: rows.filter((row) => row.stale).length,
    });
    let groups: BoardGroup[];
    if (activeGrouping === "direction" && serverBoard) {
      const rowsById = new Map(rows.map((row) => [row.todo.id, row]));
      groups = serverBoard.direction_groups.flatMap(({ direction_id: id, label, todo_ids, counts }) => {
        const members = todo_ids.flatMap((id) => {
          const row = rowsById.get(id);
          return row ? [row] : [];
        });
        if (!members.length) return [];
        const name = id ? label || members[0].direction?.label || id : t("todos.direction_uncategorized");
        return [group(id, name, members, counts.needs_you)];
      });
    } else {
      const buckets = new Map<string, BoardRow[]>();
      for (const row of rows) {
        const key = (activeGrouping === "direction" ? row.direction?.id : row.todo.project?.trim()) ?? "";
        const list = buckets.get(key);
        if (list) list.push(row);
        else buckets.set(key, [row]);
      }
      groups = [...buckets].map(([key, bucketRows]) => {
        const name = activeGrouping === "direction"
          ? bucketRows[0].direction?.label || t("todos.direction_uncategorized")
          : key ? projectName(key) : t("todos.board_no_project");
        return group(key, name, bucketRows);
      }).sort((a, b) => !a.key ? 1 : !b.key ? -1 : a.name.localeCompare(b.name));
    }

    if (serverBoard && groups.length === 1 && groups[0].rows.length === rows.length) {
      groups[0].open = serverBoard.counts.open_total;
      groups[0].waiting = serverBoard.counts.needs_you;
    }
    return groups;
  }, [todos, allTodos, lookup, triages, serverBoard, projectedNeeds, projectName, effectiveDirectionOf, isV2, activeGrouping, hours, tick]);

  const changeHours = (raw: string) => {
    setDraft(raw);
    const value = hoursValue(raw);
    if (value === undefined) {
      setInvalid(true);
      return;
    }
    setInvalid(false);
    setHours(value);
    setPref(TODO_BOARD_HOURS_KEY, String(value));
  };

  const staleHoursControl = (
    <label className="flex items-center gap-2 whitespace-nowrap text-label text-ink-2">
      <span>{t("todos.board_stale_hours")}</span>
      <input
        type="number"
        step="any"
        inputMode="decimal"
        data-testid="todo-board-stale-hours"
        aria-label={t("todos.board_stale_hours")}
        aria-invalid={invalid || undefined}
        aria-describedby={invalid ? "todo-board-hours-error" : undefined}
        className={cx(fieldClass, "w-24 max-md:h-11")}
        value={draft}
        onChange={(event) => changeHours(event.target.value)}
      />
    </label>
  );

  return (
    <div data-testid="todos-board" data-open-count={serverBoard?.counts.open_total} data-needs-you-count={serverBoard?.counts.needs_you} className="flex flex-col gap-3 p-1">
      <div className="flex flex-wrap items-end justify-between gap-2 px-1">
        <p className="max-w-prose text-meta text-ink-3">{t("todos.board_stale_explanation")}</p>
        {isV2 ? (
          <div className="flex flex-wrap items-center gap-2">
            <div
              role="radiogroup"
              aria-label={t("todos.grouping_label")}
              data-testid="todo-board-grouping"
              className="inline-flex h-8 shrink-0 items-center gap-0.5 rounded-control bg-inset p-0.5 shadow-hairline"
            >
              {(
                [
                  { value: "project" as const, label: t("todos.grouping_v2_folder") },
                  { value: "direction" as const, label: t("todos.grouping_v2_direction") },
                ]
              ).map((opt) => {
                const active = activeGrouping === opt.value;
                return (
                  <button
                    key={opt.value}
                    type="button"
                    role="radio"
                    aria-checked={active}
                    data-selected={active}
                    data-board-grouping={opt.value}
                    onClick={() => setBoardGrouping(opt.value)}
                    className={cx(
                      "touch-hit h-7 rounded-chip px-2.5 text-submeta whitespace-nowrap transition-colors focus-visible:outline-2 focus-visible:outline-accent",
                      active ? "bg-surface font-medium text-ink shadow-btn" : "text-ink-3 hover:text-ink-2",
                    )}
                  >
                    {opt.label}
                  </button>
                );
              })}
            </div>
            {staleHoursControl}
          </div>
        ) : staleHoursControl}
      </div>
      {invalid && <p id="todo-board-hours-error" role="alert" className="px-1 text-label text-red">{t("todos.board_invalid_hours")}</p>}
      {groups.length === 0 ? (
        <p className="px-1 py-8 text-center text-meta text-ink-3">{t("todos.board_empty")}</p>
      ) : groups.map((group) => (
        <section
          key={group.key}
          data-testid="todo-board-group"
          {...(activeGrouping === "direction" ? { "data-direction": group.key } : { "data-project": group.key })}
          data-open-count={group.open}
          data-waiting-count={group.waiting}
          data-stale-count={group.stale}
          className="overflow-hidden rounded-card border border-line bg-surface"
        >
          <header className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-line px-3 py-2">
            <span className="flex min-w-0 items-center gap-1.5 text-card-title font-semibold text-ink">
              <Icon
                name={activeGrouping === "direction" ? (group.key ? "target" : "inbox") : "folder"}
                size={13}
                className="shrink-0 text-ink-3"
              />
              <span className="truncate">{group.name}</span>
            </span>
            <span className="flex flex-wrap gap-x-2 text-label text-ink-3 tabular-nums">
              <span>{t("todos.board_open_count", { count: group.open })}</span>
              <span>{t("todos.board_waiting_count", { count: group.waiting, state: waitName("owner") })}</span>
              <span>{t("todos.board_stale_count", { count: group.stale })}</span>
            </span>
          </header>
          <div className="divide-y divide-line">
            {group.rows.map((row) => {
              const hasInteractiveParent = isV2 && Boolean(row.parentGoal?.loaded);
              return (
                <div
                  key={row.todo.id}
                  role={hasInteractiveParent ? undefined : "button"}
                  tabIndex={hasInteractiveParent ? undefined : 0}
                  data-todo={row.todo.id}
                  onClick={(event) => {
                    if (hasInteractiveParent && (event.target as HTMLElement).closest("button, a")) return;
                    onOpen(row.todo.id);
                  }}
                  onKeyDown={
                    hasInteractiveParent
                      ? undefined
                      : (event) => {
                          if (event.target !== event.currentTarget || (event.key !== "Enter" && event.key !== " ")) return;
                          event.preventDefault();
                          onOpen(row.todo.id);
                        }
                  }
                  className={cx(
                    "relative flex cursor-pointer flex-col gap-1.5 px-3 py-2.5 transition-colors hover:bg-hover",
                    !hasInteractiveParent && "focus-visible:outline-2 focus-visible:outline-accent",
                  )}
                >
                  {hasInteractiveParent && (
                    <button
                      type="button"
                      data-testid="board-row-open"
                      aria-label={row.todo.title}
                      onClick={() => onOpen(row.todo.id)}
                      className="absolute inset-0 cursor-pointer focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-accent"
                    />
                  )}
                  <span className="flex flex-wrap items-center gap-1.5">
                    <span className="min-w-0 flex-1 truncate text-card-title font-semibold text-ink">{row.todo.title}</span>
                    {isV2 && row.childSummary && (
                      <span
                        data-testid="board-child-summary"
                        className="inline-flex shrink-0 items-center gap-1.5 rounded-chip bg-field px-1.5 py-0.5 text-submeta font-medium whitespace-nowrap text-ink-2 tabular-nums"
                      >
                        <span>{t("todos.goal_progress", { done: row.childSummary.done, total: row.childSummary.total })}</span>
                        {row.childSummary.working > 0 && (
                          <span data-testid="board-child-summary-working" className="font-normal text-ink-3">
                            {t("todos.child_summary_working", { count: row.childSummary.working })}
                          </span>
                        )}
                        {row.childSummary.needs_you > 0 && (
                          <span data-testid="board-child-summary-blocked" className="font-medium text-attention">
                            {t("todos.goal_blocked", { count: row.childSummary.needs_you })}
                          </span>
                        )}
                      </span>
                    )}
                    {isV2 && row.direction && (activeGrouping !== "direction" || row.direction.id !== group.key) && (
                      <span
                        data-testid="board-direction-tag"
                        data-direction-id={row.direction.id}
                        data-direction-inherited={row.direction.inherited ? "true" : "false"}
                        title={row.direction.inherited ? t("todos.detail_direction_inherited_hint", { name: row.direction.label }) : row.direction.label}
                        className="max-w-36 shrink-0 truncate rounded-chip bg-field px-1.5 py-0.5 text-submeta text-ink-2 whitespace-nowrap"
                      >
                        {row.direction.label}
                      </span>
                    )}
                    {row.waiting && <Tag tone="accent"><span data-testid="board-owner">{waitName("owner")}</span></Tag>}
                    {row.stale && <Tag tone="orange"><span data-testid="board-stale">{t("todos.board_stale")}</span></Tag>}
                  </span>
                  {isV2 && row.parentGoal && (
                    <div className="flex min-w-0 items-center">
                      {row.parentGoal.loaded ? (
                        <button
                          type="button"
                          data-testid="board-parent-goal"
                          data-parent-id={row.parentGoal.id}
                          onClick={(event) => {
                            event.stopPropagation();
                            onOpen(row.parentGoal!.id);
                          }}
                          className="touch-hit relative z-10 inline-flex min-w-0 max-w-full items-center gap-1 rounded-chip text-submeta font-medium text-accent-ink whitespace-nowrap hover:underline focus-visible:outline-2 focus-visible:outline-accent"
                        >
                          <Icon name="target" size={11} stroke={2} className="shrink-0" />
                          <span className="truncate">{t("todos.goal_parent_hint", { title: row.parentGoal.title })}</span>
                        </button>
                      ) : (
                        <span
                          data-testid="board-parent-goal"
                          data-parent-id={row.parentGoal.id}
                          className="inline-flex min-w-0 max-w-full items-center gap-1 text-submeta text-ink-3 whitespace-nowrap"
                        >
                          <Icon name="target" size={11} stroke={2} className="shrink-0" />
                          <span className="truncate">{t("todos.goal_parent_hint", { title: row.parentGoal.title })}</span>
                        </span>
                      )}
                    </div>
                  )}
                  <span className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-label text-ink-2">
                    <span data-testid="board-responsible">{row.responsible}</span>
                    {row.progress ? (
                      <time className="text-ink-3" dateTime={row.progress.at}>{formatDate(row.progress.at, { dateStyle: "medium", timeStyle: "short" })}</time>
                    ) : null}
                  </span>
                  <span data-testid="board-progress" className="truncate text-meta text-ink-3">
                    {row.progress?.text ?? t("todos.board_no_progress")}
                  </span>
                </div>
              );
            })}
          </div>
        </section>
      ))}
    </div>
  );
}
