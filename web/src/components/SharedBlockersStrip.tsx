import { t } from "../i18n";
import type { SharedBlocker, SharedBlockerKind } from "../lib/todo-shared-blockers";
import { cx } from "../ui/atoms";
import { Icon, type IconName } from "../ui/icons";

function blockerIcon(kind: SharedBlockerKind): IconName {
  if (kind === "pull_request") return "pullRequest";
  if (kind === "issue") return "alertCircle";
  if (kind === "todo") return "todo";
  return "link";
}

/**
 * Compact shared blockers summary strip shown at the top of the Web To-dos list
 * when `todos_v2 === true` and at least one shared blocker (`count >= 2`) is present (#14728, #17938).
 * Covers both external blockers (PR/issue/target) and internal prerequisite to-dos.
 */
export function SharedBlockersStrip({
  blockers,
  selectedKey,
  onSelect,
  onOpenTodo,
  openableTodoIds,
}: {
  blockers: readonly SharedBlocker[];
  selectedKey: string | null;
  onSelect: (key: string | null) => void;
  onOpenTodo?: (todoId: string) => void;
  openableTodoIds?: ReadonlySet<string>;
}) {
  if (blockers.length === 0) return null;

  return (
    <section
      aria-label={t("todos.shared_blockers_region")}
      data-testid="todos-shared-blockers"
      className="px-3 pb-2"
    >
      <div className="rounded-control border border-line bg-elevated/60 px-2.5 py-2">
        <div className="flex items-center justify-between gap-2">
          <div className="flex min-w-0 items-center gap-1.5 text-xs font-medium text-ink-3">
            <Icon name="alertCircle" size={12} className="shrink-0 text-orange" />
            <span className="truncate">{t("todos.shared_blockers_title", { count: blockers.length })}</span>
          </div>
          {selectedKey !== null && (
            <button
              type="button"
              data-testid="shared-blockers-clear"
              onClick={() => onSelect(null)}
              className="touch-hit shrink-0 rounded-sm px-1 text-xs font-medium whitespace-nowrap text-accent transition-colors hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
            >
              {t("todos.shared_blockers_clear")}
            </button>
          )}
        </div>

        <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
          {blockers.map((blocker) => {
            const selected = selectedKey === blocker.key;
            const canOpenTodo = Boolean(
              blocker.kind === "todo" &&
                blocker.todo_id &&
                onOpenTodo &&
                (!openableTodoIds || openableTodoIds.has(blocker.todo_id)),
            );
            const hasTrailingAction = Boolean(blocker.url) || canOpenTodo;
            return (
              <div
                key={blocker.key}
                className={cx(
                  "inline-flex max-w-full items-center rounded-control border text-xs transition-colors",
                  selected
                    ? "border-accent/50 bg-accent/10 text-ink"
                    : "border-line bg-surface text-ink-2 hover:border-line-strong hover:text-ink",
                )}
              >
                <button
                  type="button"
                  aria-pressed={selected}
                  data-testid="shared-blocker-chip"
                  data-blocker-key={blocker.key}
                  data-blocker-kind={blocker.kind}
                  onClick={() => onSelect(selected ? null : blocker.key)}
                  className={cx(
                    "flex min-w-0 items-center gap-1.5 py-1 pl-2 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40",
                    hasTrailingAction ? "rounded-l-control pr-1.5" : "rounded-control pr-2",
                  )}
                >
                  <Icon
                    name={blockerIcon(blocker.kind)}
                    size={12}
                    className={cx("shrink-0", selected ? "text-accent" : "text-ink-3")}
                  />
                  <span className="max-w-[180px] truncate font-medium" title={blocker.label}>
                    {blocker.label}
                  </span>
                  <span
                    data-testid="shared-blocker-count"
                    className={cx(
                      "shrink-0 text-xs whitespace-nowrap tabular-nums",
                      selected ? "text-accent" : "text-ink-3",
                    )}
                  >
                    {t("todos.shared_blockers_count", { count: blocker.count })}
                  </span>
                </button>

                {blocker.url ? (
                  <a
                    href={blocker.url}
                    target="_blank"
                    rel="noopener noreferrer"
                    data-testid="shared-blocker-link"
                    aria-label={t("todos.shared_blockers_open_link", { label: blocker.label })}
                    title={blocker.url}
                    className={cx(
                      "touch-hit flex shrink-0 items-center justify-center rounded-r-control border-l px-1.5 py-1 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40",
                      selected
                        ? "border-accent/30 text-accent hover:bg-accent/15"
                        : "border-line text-ink-3 hover:bg-hover hover:text-ink",
                    )}
                  >
                    <Icon name="external" size={11} />
                  </a>
                ) : canOpenTodo && blocker.todo_id && onOpenTodo ? (
                  <button
                    type="button"
                    data-testid="shared-blocker-open-todo"
                    data-todo-id={blocker.todo_id}
                    aria-label={t("todos.shared_blockers_open_link", { label: blocker.label })}
                    title={t("todos.shared_blockers_open_link", { label: blocker.label })}
                    onClick={() => onOpenTodo(blocker.todo_id!)}
                    className={cx(
                      "touch-hit flex shrink-0 items-center justify-center rounded-r-control border-l px-1.5 py-1 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40",
                      selected
                        ? "border-accent/30 text-accent hover:bg-accent/15"
                        : "border-line text-ink-3 hover:bg-hover hover:text-ink",
                    )}
                  >
                    <Icon name="arrowUpRight" size={11} />
                  </button>
                ) : null}
              </div>
            );
          })}
        </div>
      </div>
    </section>
  );
}
