// Single-screen Decision Queue panel strictly gated behind `todos_v2 === true` (#15757).
// Lets the owner resolve multiple pending decisions in one batch via POST /api/v1/todos/decisions/batch:
// - Mode A: pick an explicit option per item (never pre-selected or auto-picked).
// - Mode B: check one or more items and apply or directly submit a shared custom reply.
// Surfaces per-item partial success/failure results returned by the server.
import { useEffect, useMemo, useState } from "react";
import { t } from "../../i18n";
import { errorText } from "../../lib/api";
import { safeWebURL } from "../../lib/format";
import {
  fetchTodoDecisionGroups,
  openAsk,
  submitTodoDecisionsBatch,
  type Todo,
  type TodoDecisionBatchItem,
  type TodoDecisionBatchResponse,
  type TodoDecisionBatchResult,
  type TodoDecisionGroup,
  type TodoDecisionGroupsResponse,
} from "../../lib/todos";
import { Button, cx, Dialog } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

export type DecisionDraftChoice =
  | { kind: "option"; value: string }
  | { kind: "reply"; value: string };

/** Pending decision to-dos eligible for the Decision Queue (open, with an active `decide` ask). */
export function decisionQueueCandidates(todos: readonly Todo[]): Todo[] {
  return todos.filter((td) => {
    if (td.done_at) return false;
    const ask = openAsk(td);
    return Boolean(ask && ask.outcome === "decide");
  });
}

/** Build the ordered batch payload from explicit per-item choices and checked items covered by the shared reply. */
export function buildDecisionBatchItems(
  items: readonly Todo[],
  choices: ReadonlyMap<string, DecisionDraftChoice>,
  checkedIds: ReadonlySet<string>,
  sharedReply: string,
  settledIds: ReadonlySet<string>,
): TodoDecisionBatchItem[] {
  const trimmedShared = sharedReply.trim();
  const payload: TodoDecisionBatchItem[] = [];
  for (const td of items) {
    if (settledIds.has(td.id)) continue;
    const explicit = choices.get(td.id);
    if (explicit) {
      if (explicit.kind === "option" && explicit.value.trim()) {
        payload.push({ todo_id: td.id, option: explicit.value.trim() });
      } else if (explicit.kind === "reply" && explicit.value.trim()) {
        payload.push({ todo_id: td.id, reply: explicit.value.trim() });
      }
      continue;
    }
    if (checkedIds.has(td.id) && trimmedShared) {
      payload.push({ todo_id: td.id, reply: trimmedShared });
    }
  }
  return payload;
}

export interface DecisionQueuePanelProps {
  open: boolean;
  items: readonly Todo[];
  onClose: () => void;
  onRefresh?: () => void | Promise<void>;
  onSubmitBatch?: (items: readonly TodoDecisionBatchItem[]) => Promise<TodoDecisionBatchResponse>;
  onFetchGroups?: (signal?: AbortSignal) => Promise<TodoDecisionGroupsResponse>;
}

interface ClusteredDecisionGroup {
  key: string;
  cause_kind: string;
  label: string;
  url?: string;
  todos: Todo[];
}

function safeExternalUrl(raw?: string): string | undefined {
  return safeWebURL(raw);
}

function causeKindLabel(kind: string): string | null {
  switch (kind) {
    case "shared_blocker":
      return t("todos.decision_queue_cause_shared_blocker");
    case "same_pr":
      return t("todos.decision_queue_cause_same_pr");
    case "same_direction":
      return t("todos.decision_queue_cause_same_direction");
    default:
      return null;
  }
}

export function DecisionQueuePanel({
  open,
  items,
  onClose,
  onRefresh,
  onSubmitBatch = submitTodoDecisionsBatch,
  onFetchGroups = fetchTodoDecisionGroups,
}: DecisionQueuePanelProps) {
  // Explicit per-item choice (never pre-populated with any recommendation).
  const [choices, setChoices] = useState<Map<string, DecisionDraftChoice>>(() => new Map());
  // Checked item IDs for shared custom reply (Mode B).
  const [checkedIds, setCheckedIds] = useState<Set<string>>(() => new Set());
  const [sharedReply, setSharedReply] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [requestError, setRequestError] = useState<string | null>(null);
  const [resultsById, setResultsById] = useState<Map<string, TodoDecisionBatchResult>>(() => new Map());
  const [lastSummary, setLastSummary] = useState<{ succeeded: number; failed: number } | null>(null);
  const [serverGroups, setServerGroups] = useState<TodoDecisionGroup[] | null>(null);

  // Reset transient state and fetch server-computed same-cause groups when the dialog opens fresh.
  useEffect(() => {
    if (!open) return;
    setChoices(new Map());
    setCheckedIds(new Set());
    setSharedReply("");
    setRequestError(null);
    setResultsById(new Map());
    setLastSummary(null);
    setServerGroups(null);

    const ac = new AbortController();
    void onFetchGroups(ac.signal)
      .then((res) => {
        if (ac.signal.aborted) return;
        const list = Array.isArray(res?.groups) ? res.groups : [];
        setServerGroups(list.length > 0 ? list : null);
      })
      .catch(() => {
        if (!ac.signal.aborted) setServerGroups(null);
      });
    return () => ac.abort();
  }, [open, onFetchGroups]);

  const settledIds = useMemo(() => {
    const out = new Set<string>();
    for (const [id, res] of resultsById) {
      if (res.ok) out.add(id);
    }
    return out;
  }, [resultsById]);

  const activeItems = useMemo(
    () => items.filter((td) => !settledIds.has(td.id)),
    [items, settledIds],
  );

  const batchPayload = useMemo(
    () => buildDecisionBatchItems(items, choices, checkedIds, sharedReply, settledIds),
    [items, choices, checkedIds, sharedReply, settledIds],
  );

  const allActiveChecked =
    activeItems.length > 0 && activeItems.every((td) => checkedIds.has(td.id));

  const toggleOption = (todoId: string, optionLabel: string) => {
    setRequestError(null);
    setChoices((prev) => {
      const next = new Map(prev);
      const current = next.get(todoId);
      if (current?.kind === "option" && current.value === optionLabel) {
        next.delete(todoId);
      } else {
        next.set(todoId, { kind: "option", value: optionLabel });
      }
      return next;
    });
    // Clear any previous error on this item once the user adjusts their choice.
    setResultsById((prev) => {
      if (!prev.has(todoId)) return prev;
      const next = new Map(prev);
      next.delete(todoId);
      return next;
    });
  };

  const clearItemChoice = (todoId: string) => {
    setChoices((prev) => {
      if (!prev.has(todoId)) return prev;
      const next = new Map(prev);
      next.delete(todoId);
      return next;
    });
  };

  const toggleChecked = (todoId: string) => {
    setCheckedIds((prev) => {
      const next = new Set(prev);
      if (next.has(todoId)) next.delete(todoId);
      else next.add(todoId);
      return next;
    });
  };

  const selectAllActive = () => {
    setCheckedIds(new Set(activeItems.map((td) => td.id)));
  };

  const clearChecked = () => {
    setCheckedIds(new Set());
  };

  const toggleGroupChecked = (groupTodos: readonly Todo[]) => {
    const activeGroupIds = groupTodos.filter((td) => !settledIds.has(td.id)).map((td) => td.id);
    if (activeGroupIds.length === 0) return;
    setCheckedIds((prev) => {
      const next = new Set(prev);
      const allSelected = activeGroupIds.every((id) => next.has(id));
      for (const id of activeGroupIds) {
        if (allSelected) next.delete(id);
        else next.add(id);
      }
      return next;
    });
  };

  const clusteredGroups = useMemo<ClusteredDecisionGroup[] | null>(() => {
    if (!serverGroups || serverGroups.length === 0 || items.length === 0) return null;
    const byId = new Map(items.map((td) => [td.id, td]));
    const assigned = new Set<string>();
    const out: ClusteredDecisionGroup[] = [];
    for (const g of serverGroups) {
      const todos: Todo[] = [];
      for (const id of g.todo_ids ?? []) {
        if (assigned.has(id)) continue;
        const td = byId.get(id);
        if (td) {
          assigned.add(id);
          todos.push(td);
        }
      }
      if (todos.length > 0) {
        out.push({
          key: g.key,
          cause_kind: g.cause_kind,
          label: g.label?.trim() || t("todos.decision_queue_group_other"),
          url: safeExternalUrl(g.url),
          todos,
        });
      }
    }
    if (out.length === 0) return null;
    const remaining = items.filter((td) => !assigned.has(td.id));
    if (remaining.length > 0) {
      out.push({
        key: "standalone:unlisted",
        cause_kind: "standalone",
        label: t("todos.decision_queue_group_other"),
        todos: remaining,
      });
    }
    return out;
  }, [serverGroups, items]);

  const applySharedReplyToChecked = () => {
    const text = sharedReply.trim();
    if (!text || checkedIds.size === 0) return;
    setRequestError(null);
    setChoices((prev) => {
      const next = new Map(prev);
      for (const id of checkedIds) {
        if (!settledIds.has(id)) {
          next.set(id, { kind: "reply", value: text });
        }
      }
      return next;
    });
    setResultsById((prev) => {
      const next = new Map(prev);
      for (const id of checkedIds) {
        if (next.get(id) && !next.get(id)!.ok) next.delete(id);
      }
      return next;
    });
    setSharedReply("");
  };

  const handleSubmit = async () => {
    if (submitting || batchPayload.length === 0) return;
    setSubmitting(true);
    setRequestError(null);
    try {
      const res = await onSubmitBatch(batchPayload);
      const nextResults = new Map(resultsById);
      const succeededIds = new Set<string>();
      for (const r of res.results) {
        nextResults.set(r.todo_id, r);
        if (r.ok) succeededIds.add(r.todo_id);
      }
      setResultsById(nextResults);
      setLastSummary({ succeeded: res.succeeded, failed: res.failed });

      // Clear staged choices and checkboxes for succeeded items; keep failed items intact for retry/edit.
      if (succeededIds.size > 0) {
        setChoices((prev) => {
          const next = new Map(prev);
          for (const id of succeededIds) next.delete(id);
          return next;
        });
        setCheckedIds((prev) => {
          const next = new Set(prev);
          for (const id of succeededIds) next.delete(id);
          return next;
        });
        if (res.failed === 0) {
          setSharedReply("");
        }
        if (onRefresh) {
          await Promise.resolve(onRefresh()).catch(() => {});
        }
      }
    } catch (e) {
      setRequestError(errorText(e));
    } finally {
      setSubmitting(false);
    }
  };

  const totalCount = activeItems.length;
  const readyCount = batchPayload.length;
  const checkedActiveCount = activeItems.filter((td) => checkedIds.has(td.id)).length;

  return (
    <Dialog
      open={open}
      onClose={() => {
        if (!submitting) onClose();
      }}
      title={t("todos.decision_queue_title")}
      width={640}
      footer={
        <div className="flex w-full flex-wrap items-center justify-between gap-2">
          <span
            data-testid="decision-queue-ready-count"
            className="text-submeta font-medium text-ink-2 tabular-nums"
          >
            {t("todos.decision_queue_ready_count", { ready: readyCount, total: totalCount })}
          </span>
          <div className="flex items-center gap-2">
            <Button tone="quiet" disabled={submitting} onClick={onClose}>
              {t("ui-atoms.close")}
            </Button>
            <Button
              tone="primary"
              busy={submitting}
              disabled={submitting || readyCount === 0}
              onClick={() => void handleSubmit()}
              testId="decision-queue-submit"
              className="whitespace-nowrap"
            >
              {t("todos.decision_queue_submit", { count: readyCount })}
            </Button>
          </div>
        </div>
      }
    >
      <div data-testid="decision-queue-panel" className="space-y-3">
        {/* Subtitle + multi-select controls */}
        <div className="flex flex-wrap items-center justify-between gap-2">
          <p className="text-submeta leading-snug text-ink-3">
            {t("todos.decision_queue_hint")}
          </p>
          {activeItems.length > 0 && (
            <div className="flex items-center gap-2 text-submeta">
              <button
                type="button"
                data-testid="decision-queue-select-all"
                disabled={submitting || allActiveChecked}
                onClick={selectAllActive}
                className="touch-hit rounded-sm px-1 font-medium whitespace-nowrap text-accent-ink transition-colors enabled:hover:underline disabled:opacity-40 focus-visible:outline-2 focus-visible:outline-accent"
              >
                {t("todos.reorganize_select_all")}
              </button>
              <button
                type="button"
                data-testid="decision-queue-clear-selection"
                disabled={submitting || checkedActiveCount === 0}
                onClick={clearChecked}
                className="touch-hit rounded-sm px-1 font-medium whitespace-nowrap text-ink-3 transition-colors enabled:hover:text-ink disabled:opacity-40 focus-visible:outline-2 focus-visible:outline-accent"
              >
                {t("todos.reorganize_clear_selection")}
              </button>
            </div>
          )}
        </div>

        {/* Batch feedback banner */}
        {requestError && (
          <p
            role="alert"
            data-testid="decision-queue-error"
            className="rounded-control border border-red/30 bg-inset px-3 py-2 text-submeta text-red"
          >
            {requestError}
          </p>
        )}
        {!requestError && lastSummary && (
          <p
            role="status"
            data-testid="decision-queue-summary"
            className={cx(
              "rounded-control border px-3 py-2 text-submeta",
              lastSummary.failed > 0
                ? "border-orange/40 bg-orange-tint text-ink"
                : "border-green/30 bg-inset text-green",
            )}
          >
            {lastSummary.failed > 0
              ? t("todos.decision_queue_partial", {
                  succeeded: lastSummary.succeeded,
                  failed: lastSummary.failed,
                })
              : t("todos.decision_queue_all_ok", { succeeded: lastSummary.succeeded })}
          </p>
        )}

        {/* Decision items list (clustered by server same-cause groups when available, else flat fallback) */}
        {items.length === 0 ? (
          <p data-testid="decision-queue-empty" className="py-6 text-center text-meta text-ink-3">
            {t("todos.decision_queue_empty")}
          </p>
        ) : (
          <div className="max-h-96 space-y-2.5 overflow-y-auto pr-0.5">
            {(() => {
              const renderItem = (td: Todo) => {
                const ask = openAsk(td);
                const result = resultsById.get(td.id);
                const settled = Boolean(result?.ok);
                const choice = choices.get(td.id);
                const checked = checkedIds.has(td.id);
                const options = (ask?.options ?? []).filter(
                  (o) => o && typeof o.label === "string" && o.label.trim().length > 0,
                );
                const questionText = ask?.question?.trim() || ask?.verdict || "";
                const recommendLabel = ask?.recommend?.trim() || "";
                const willUseSharedReply =
                  !settled && !choice && checked && sharedReply.trim().length > 0;

                return (
                  <div
                    key={td.id}
                    data-testid="decision-queue-item"
                    data-todo-id={td.id}
                    data-settled={settled ? "true" : undefined}
                    className={cx(
                      "rounded-control border p-3 transition-opacity",
                      settled
                        ? "border-green/30 bg-inset/60 opacity-60"
                        : result && !result.ok
                          ? "border-red/40 bg-surface"
                          : choice || willUseSharedReply
                            ? "border-accent/50 bg-accent-tint/20"
                            : "border-line bg-surface",
                    )}
                  >
                    <div className="flex items-start gap-2.5">
                      <input
                        type="checkbox"
                        data-testid="decision-queue-check"
                        data-todo-id={td.id}
                        aria-label={t("todos.decision_queue_select_item", { title: td.title })}
                        checked={checked}
                        disabled={submitting || settled}
                        onChange={() => toggleChecked(td.id)}
                        className="mt-0.5 size-4 shrink-0 cursor-pointer accent-accent disabled:opacity-40"
                      />
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center justify-between gap-2">
                          <span className="min-w-0 truncate text-meta font-semibold text-ink">
                            {td.title}
                          </span>
                          {settled && (
                            <span
                              data-testid="decision-queue-item-ok"
                              className="inline-flex shrink-0 items-center gap-1 text-submeta font-medium whitespace-nowrap text-green"
                            >
                              <Icon name="check" size={12} stroke={2.4} />
                              {t("todos.decision_queue_item_submitted")}
                            </span>
                          )}
                        </div>

                        {questionText && (
                          <p className="mt-1 text-submeta leading-snug text-ink-2">
                            {questionText}
                          </p>
                        )}

                        {/* Mode A: explicit option buttons (never pre-selected) */}
                        {!settled && options.length > 0 && (
                          <div className="mt-2 flex flex-wrap items-center gap-1.5" role="group" aria-label={td.title}>
                            {options.map((opt) => {
                              const selected =
                                choice?.kind === "option" && choice.value === opt.label;
                              const isRecommended =
                                Boolean(recommendLabel) && opt.label === recommendLabel;
                              return (
                                <button
                                  key={opt.label}
                                  type="button"
                                  data-testid="decision-queue-option"
                                  data-todo-id={td.id}
                                  data-option={opt.label}
                                  aria-pressed={selected}
                                  disabled={submitting}
                                  title={opt.leads_to || opt.label}
                                  onClick={() => toggleOption(td.id, opt.label)}
                                  className={cx(
                                    "touch-hit inline-flex max-w-full items-center gap-1.5 rounded-control border px-2.5 py-1 text-submeta font-medium whitespace-nowrap transition-colors focus-visible:outline-2 focus-visible:outline-accent disabled:opacity-50",
                                    selected
                                      ? "border-accent bg-accent text-white"
                                      : "border-line bg-inset text-ink hover:border-line-strong hover:bg-hover",
                                  )}
                                >
                                  <span className="truncate">{opt.label}</span>
                                  {isRecommended && (
                                    <span
                                      data-testid="decision-queue-recommended-badge"
                                      className={cx(
                                        "rounded-chip px-1 text-xs font-normal whitespace-nowrap",
                                        selected
                                          ? "bg-white/20 text-white"
                                          : "bg-orange-tint text-attention",
                                      )}
                                    >
                                      {t("todos.decision_queue_recommended")}
                                    </span>
                                  )}
                                </button>
                              );
                            })}
                          </div>
                        )}

                        {/* Recommendation hint when there are no structured option buttons */}
                        {!settled && options.length === 0 && recommendLabel && (
                          <p className="mt-1.5 text-submeta text-ink-3">
                            {t("assistant-brief.recommend", { option: recommendLabel })}
                          </p>
                        )}

                        {/* Staged custom reply pill (Mode B) */}
                        {!settled && choice?.kind === "reply" && (
                          <div
                            data-testid="decision-queue-item-reply"
                            className="mt-2 flex items-center justify-between gap-2 rounded-control border border-accent/30 bg-inset px-2.5 py-1 text-submeta text-ink"
                          >
                            <span className="min-w-0 truncate">
                              {t("todos.decision_queue_reply_preview", { reply: choice.value })}
                            </span>
                            <button
                              type="button"
                              aria-label={t("todos.decision_queue_clear_choice")}
                              disabled={submitting}
                              onClick={() => clearItemChoice(td.id)}
                              className="touch-hit shrink-0 text-ink-3 hover:text-ink"
                            >
                              <Icon name="x" size={12} />
                            </button>
                          </div>
                        )}

                        {/* Pending shared reply preview when checked + shared reply typed */}
                        {willUseSharedReply && (
                          <p
                            data-testid="decision-queue-item-shared-preview"
                            className="mt-1.5 truncate text-submeta text-accent-ink"
                          >
                            {t("todos.decision_queue_shared_preview", { reply: sharedReply.trim() })}
                          </p>
                        )}

                        {/* Per-item server error feedback */}
                        {result && !result.ok && (
                          <p
                            role="alert"
                            data-testid="decision-queue-item-error"
                            className="mt-1.5 text-submeta text-red"
                          >
                            {result.error || t("todos.decision_queue_item_failed")}
                          </p>
                        )}
                      </div>
                    </div>
                  </div>
                );
              };

              if (!clusteredGroups) {
                return items.map(renderItem);
              }

              return clusteredGroups.map((group) => {
                const activeGroupTodos = group.todos.filter((td) => !settledIds.has(td.id));
                const allGroupChecked =
                  activeGroupTodos.length > 0 && activeGroupTodos.every((td) => checkedIds.has(td.id));
                const kindTag = causeKindLabel(group.cause_kind);
                return (
                  <section
                    key={group.key}
                    data-testid="decision-queue-group"
                    data-group-key={group.key}
                    data-cause-kind={group.cause_kind}
                    className="space-y-1.5 rounded-control border border-line/80 bg-inset/40 p-2"
                  >
                    <div className="flex flex-wrap items-center justify-between gap-2 px-1 py-0.5">
                      <div className="flex min-w-0 flex-1 items-center gap-1.5">
                        <input
                          type="checkbox"
                          data-testid="decision-queue-group-select"
                          data-group-key={group.key}
                          aria-label={t("todos.decision_queue_select_group", { label: group.label })}
                          checked={allGroupChecked}
                          disabled={submitting || activeGroupTodos.length === 0}
                          onChange={() => toggleGroupChecked(group.todos)}
                          className="size-3.5 shrink-0 cursor-pointer accent-accent disabled:opacity-40"
                        />
                        {kindTag && (
                          <span className="shrink-0 rounded-chip bg-surface px-1.5 py-0.5 text-submeta font-medium whitespace-nowrap text-ink-3 shadow-hairline">
                            {kindTag}
                          </span>
                        )}
                        {group.url ? (
                          <a
                            href={group.url}
                            target="_blank"
                            rel="noreferrer noopener"
                            data-testid="decision-queue-group-link"
                            className="inline-flex min-w-0 items-center gap-1 truncate text-submeta font-semibold text-accent-ink hover:underline"
                          >
                            <span className="truncate">{group.label}</span>
                            <Icon name="external" size={11} stroke={2} className="shrink-0" />
                          </a>
                        ) : (
                          <span className="min-w-0 truncate text-submeta font-semibold text-ink-2">
                            {group.label}
                          </span>
                        )}
                      </div>
                      <span
                        data-testid="decision-queue-group-count"
                        className="shrink-0 rounded-chip bg-surface px-1.5 py-0.5 text-submeta font-medium text-ink-3 tabular-nums shadow-hairline"
                      >
                        {group.todos.length}
                      </span>
                    </div>
                    <div className="space-y-1.5">{group.todos.map(renderItem)}</div>
                  </section>
                );
              });
            })()}
          </div>
        )}

        {/* Mode B: shared custom reply composer for checked items */}
        {activeItems.length > 0 && (
          <div className="rounded-control border border-line bg-inset p-2.5">
            <label
              htmlFor="decision-queue-shared-reply"
              className="block text-submeta font-medium text-ink-2"
            >
              {t("todos.decision_queue_shared_label", { count: checkedActiveCount })}
            </label>
            <div className="mt-1.5 flex flex-wrap items-center gap-2">
              <input
                id="decision-queue-shared-reply"
                data-testid="decision-queue-shared-reply"
                type="text"
                value={sharedReply}
                disabled={submitting}
                placeholder={t("todos.decision_queue_shared_placeholder")}
                onChange={(e) => setSharedReply(e.target.value)}
                onKeyDown={(e) => {
                  if (e.nativeEvent.isComposing) return;
                  if (e.key === "Enter" && checkedActiveCount > 0 && sharedReply.trim()) {
                    e.preventDefault();
                    applySharedReplyToChecked();
                  }
                }}
                className="h-8 min-w-0 flex-1 rounded-control border border-line bg-surface px-2.5 text-meta text-ink placeholder:text-ink-3 focus-visible:outline-2 focus-visible:outline-accent disabled:opacity-50"
              />
              <Button
                tone="secondary"
                size="sm"
                disabled={submitting || checkedActiveCount === 0 || !sharedReply.trim()}
                onClick={applySharedReplyToChecked}
                testId="decision-queue-apply-reply"
                className="whitespace-nowrap"
              >
                {t("todos.decision_queue_apply_reply", { count: checkedActiveCount })}
              </Button>
            </div>
          </div>
        )}
      </div>
    </Dialog>
  );
}
