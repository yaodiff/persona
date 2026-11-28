import { useEffect, useMemo, useState } from "react";
import { t } from "../i18n";
import {
  buildReorganizeRequest,
  canTransferSupervision,
  type ReorganizeDirectionOption,
  type ReorganizeTarget,
  type TodoReorganizeRequest,
} from "../lib/todo-reorganize";
import { Button, cx } from "../ui/atoms";

function valueToDirectionTarget(value: string): ReorganizeTarget {
  const trimmed = value.trim();
  if (!trimmed) return { mode: "clear" };
  return { mode: "set", id: trimmed };
}

/**
 * Compact single-item direction picker panel rendered inside `TodoDetail`
 * strictly when `todos_v2 === true` (#15653, #16138).
 *
 * Calls `POST /api/v1/todos/reorganize` with `include_children` defaulting to
 * `true` when the item has subtasks (so parent/child goals stay aligned unless
 * the user opts out) and `transfer_supervision` defaulting to `false` so
 * running Agents are never interrupted unless the user opts in.
 */
export function TodoDirectionPicker({
  todoId,
  currentDirectionId,
  currentDirectionLabel,
  currentDirectionInherited = false,
  childCount,
  directions,
  onApply,
  onClose,
}: {
  todoId: string;
  currentDirectionId?: string;
  currentDirectionLabel?: string;
  /** True when `currentDirectionId` is inherited (not explicitly set on `todo.direction_id`). */
  currentDirectionInherited?: boolean;
  childCount: number;
  directions: readonly ReorganizeDirectionOption[];
  onApply: (req: TodoReorganizeRequest) => Promise<void>;
  onClose: () => void;
}) {
  const normalizedCurrent = currentDirectionId?.trim() ?? "";
  const inherited = Boolean(currentDirectionInherited && normalizedCurrent);
  const [selectedValue, setSelectedValue] = useState(normalizedCurrent);
  const [includeChildren, setIncludeChildren] = useState(true);
  const [transferSupervision, setTransferSupervision] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const options = useMemo(() => {
    if (!normalizedCurrent || directions.some((d) => d.id === normalizedCurrent)) {
      return directions;
    }
    return [
      ...directions,
      {
        id: normalizedCurrent,
        label: currentDirectionLabel?.trim() || normalizedCurrent,
        active: false,
      },
    ];
  }, [directions, normalizedCurrent, currentDirectionLabel]);

  const activeDirectionIds = useMemo(
    () => new Set(directions.filter((d) => d.active).map((d) => d.id)),
    [directions],
  );

  const directionTarget = useMemo(() => valueToDirectionTarget(selectedValue), [selectedValue]);
  const transferAllowed = canTransferSupervision(directionTarget, activeDirectionIds);

  useEffect(() => {
    if (!transferAllowed && transferSupervision) {
      setTransferSupervision(false);
    }
  }, [transferAllowed, transferSupervision]);

  const changed =
    selectedValue.trim() !== normalizedCurrent ||
    (inherited && selectedValue.trim() !== "");
  const request = useMemo(() => {
    if (!changed) return null;
    return buildReorganizeRequest({
      ids: [todoId],
      partOf: { mode: "keep" },
      direction: directionTarget,
      includeChildren: childCount > 0 && includeChildren,
      transferSupervision,
      activeDirectionIds,
    });
  }, [changed, todoId, directionTarget, childCount, includeChildren, transferSupervision, activeDirectionIds]);

  const handleSubmit = async () => {
    if (!request || submitting) return;
    setSubmitting(true);
    setError(null);
    try {
      await onApply(request);
      onClose();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <section
      aria-label={t("todos.detail_change_direction_region")}
      data-testid="todo-detail-direction-panel"
      className="mb-3 rounded-control border border-line bg-surface p-3"
    >
      <div className="flex flex-col gap-2">
        {inherited && (
          <p data-testid="todo-detail-direction-inherited" className="text-submeta text-ink-3">
            {t("todos.detail_direction_inherited_picker_hint", {
              name: currentDirectionLabel?.trim() || normalizedCurrent,
            })}
          </p>
        )}
        <label className="flex flex-col gap-1">
          <span className="text-label font-medium text-ink-2">{t("todos.reorganize_direction_label")}</span>
          <select
            data-testid="todo-detail-direction-select"
            aria-label={t("todos.reorganize_direction_label")}
            value={selectedValue}
            disabled={submitting}
            onChange={(e) => {
              setSelectedValue(e.target.value);
              setError(null);
            }}
            className="h-8 w-full min-w-0 rounded-control border border-line bg-inset px-2 text-xs text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 disabled:opacity-50"
          >
            <option value="">{t("todos.reorganize_direction_clear")}</option>
            {options.map((d) => (
              <option key={d.id} value={d.id}>
                {d.label}
              </option>
            ))}
          </select>
        </label>

        {(childCount > 0 || transferAllowed) && (
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-ink-2">
            {childCount > 0 && (
              <label className="inline-flex cursor-pointer items-center gap-1.5 select-none">
                <input
                  type="checkbox"
                  data-testid="todo-direction-include-children"
                  checked={includeChildren}
                  disabled={submitting}
                  onChange={(e) => setIncludeChildren(e.target.checked)}
                  className="size-3.5 accent-accent"
                />
                <span>{t("todos.direction_include_children", { count: childCount })}</span>
              </label>
            )}
            {transferAllowed && (
              <label
                className={cx(
                  "inline-flex cursor-pointer items-center gap-1.5 select-none text-ink-2",
                )}
              >
                <input
                  type="checkbox"
                  data-testid="todo-detail-direction-transfer-supervision"
                  checked={transferSupervision}
                  disabled={submitting}
                  onChange={(e) => setTransferSupervision(e.target.checked)}
                  className="size-3.5 accent-accent"
                />
                <span>{t("todos.reorganize_transfer_supervision")}</span>
              </label>
            )}
          </div>
        )}

        <div className="flex flex-wrap items-center justify-between gap-2 pt-0.5">
          <span className="text-submeta text-ink-3">{t("todos.detail_change_direction_hint")}</span>
          <div className="flex items-center gap-1.5">
            <Button
              tone="quiet"
              size="sm"
              disabled={submitting}
              onClick={onClose}
              testId="todo-detail-direction-cancel"
              className="whitespace-nowrap text-xs"
            >
              {t("common.cancel")}
            </Button>
            <Button
              tone="accent"
              size="sm"
              busy={submitting}
              disabled={!request || submitting}
              onClick={() => void handleSubmit()}
              testId="todo-detail-direction-apply"
              className="whitespace-nowrap text-xs"
            >
              {t("todos.reorganize_apply")}
            </Button>
          </div>
        </div>

        {error && (
          <p role="alert" data-testid="todo-detail-direction-error" className="text-xs text-red">
            {error}
          </p>
        )}
      </div>
    </section>
  );
}
