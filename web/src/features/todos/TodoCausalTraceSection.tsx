// On-demand read-only causal audit trace disclosure for TodoDetail, strictly gated behind `todos_v2 === true` (#16433).
// Starts collapsed by default and fetches GET /api/v1/todos/{id}/trace lazily only when expanded or manually refreshed.
import { useCallback, useEffect, useRef, useState } from "react";
import { formatDate, t } from "../../i18n";
import { errorText } from "../../lib/api";
import { safeWebURL } from "../../lib/format";
import { fetchTodoCausalTrace, type TodoCausalTraceEvent } from "../../lib/todos";
import { Button, cx, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

function safeTraceUrl(raw?: string): string | undefined {
  return safeWebURL(raw);
}

function categoryLabel(category: string): string {
  switch (category) {
    case "lifecycle":
      return t("todos.causal_trace_cat_lifecycle");
    case "queue":
      return t("todos.causal_trace_cat_queue");
    case "blocker":
      return t("todos.causal_trace_cat_blocker");
    case "recovery":
      return t("todos.causal_trace_cat_recovery");
    case "review":
      return t("todos.causal_trace_cat_review");
    case "pr":
      return t("todos.causal_trace_cat_pr");
    default:
      return category;
  }
}

function formatTraceTime(at: string): string {
  const ms = Date.parse(at);
  if (Number.isNaN(ms)) return at;
  return formatDate(ms, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function TodoCausalTraceSection({
  todoId,
  enabled,
  compact = false,
}: {
  todoId: string;
  enabled: boolean;
  compact?: boolean;
}) {
  const [expanded, setExpanded] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [events, setEvents] = useState<TodoCausalTraceEvent[] | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  // Reset state and abort any in-flight request when switching to-dos or turning off todos_v2.
  useEffect(() => {
    abortRef.current?.abort();
    abortRef.current = null;
    setExpanded(false);
    setLoading(false);
    setError(null);
    setEvents(null);
  }, [todoId, enabled]);

  useEffect(() => {
    return () => {
      abortRef.current?.abort();
    };
  }, []);

  const loadTrace = useCallback(async () => {
    if (!enabled) return;
    abortRef.current?.abort();
    const ac = new AbortController();
    abortRef.current = ac;
    setLoading(true);
    setError(null);
    try {
      const res = await fetchTodoCausalTrace(todoId, ac.signal);
      if (ac.signal.aborted) return;
      setEvents(res.events);
    } catch (e) {
      if (ac.signal.aborted) return;
      setError(errorText(e));
    } finally {
      if (!ac.signal.aborted) {
        setLoading(false);
      }
    }
  }, [enabled, todoId]);

  if (!enabled) return null;

  const handleToggle = () => {
    const next = !expanded;
    setExpanded(next);
    if (next && events === null && !loading) {
      void loadTrace();
    }
  };

  return (
    <section
      data-testid="todo-causal-trace"
      aria-label={t("todos.causal_trace_title")}
      className={cx(compact ? "" : "mt-6", "rounded-control border border-line bg-canvas")}
    >
      <div className="flex items-center justify-between gap-2 px-3 py-2">
        <button
          type="button"
          data-testid="todo-causal-trace-toggle"
          aria-expanded={expanded}
          onClick={handleToggle}
          className="flex min-w-0 flex-1 items-center gap-1.5 text-left text-label font-medium text-ink-2 transition-colors hover:text-ink focus-visible:outline-2 focus-visible:outline-accent"
        >
          <Icon
            name="chevron"
            size={11}
            stroke={2.2}
            className={cx("shrink-0 text-ink-3 transition-transform", expanded ? "" : "-rotate-90")}
          />
          <span className="truncate">{t("todos.causal_trace_title")}</span>
          {events !== null && (
            <span
              data-testid="todo-causal-trace-count"
              className="shrink-0 rounded-chip bg-field px-1.5 py-0.5 text-submeta text-ink-3 tabular-nums"
            >
              {events.length}
            </span>
          )}
        </button>
        {expanded && (
          <Button
            tone="quiet"
            size="sm"
            testId="todo-causal-trace-refresh"
            disabled={loading}
            onClick={() => void loadTrace()}
            className="h-6 shrink-0 px-2 text-submeta whitespace-nowrap text-ink-3 hover:text-ink"
          >
            {t("todos.causal_trace_refresh")}
          </Button>
        )}
      </div>

      {expanded && (
        <div className="border-t border-line px-3 py-2.5">
          {loading && events === null ? (
            <div data-testid="todo-causal-trace-loading" className="flex items-center gap-2 py-2 text-submeta text-ink-3">
              <Spinner size={12} />
              <span>{t("todos.causal_trace_loading")}</span>
            </div>
          ) : error ? (
            <div data-testid="todo-causal-trace-error" className="flex flex-wrap items-center justify-between gap-2 py-1 text-submeta text-red">
              <span role="alert">{t("todos.causal_trace_failed", { error })}</span>
              <Button tone="secondary" size="sm" onClick={() => void loadTrace()} className="h-6 px-2 text-submeta whitespace-nowrap">
                {t("todos.retry")}
              </Button>
            </div>
          ) : events && events.length === 0 ? (
            <p data-testid="todo-causal-trace-empty" className="py-1 text-submeta text-ink-3">
              {t("todos.causal_trace_empty")}
            </p>
          ) : events ? (
            <ol data-testid="todo-causal-trace-list" className="space-y-2">
              {events.map((ev) => {
                const url = safeTraceUrl(ev.url);
                return (
                  <li
                    key={ev.id}
                    data-testid="todo-causal-trace-event"
                    data-category={ev.category}
                    className="flex items-start gap-2 text-submeta"
                  >
                    <span className="w-24 shrink-0 text-ink-3 tabular-nums">
                      {formatTraceTime(ev.at)}
                    </span>
                    <span className="shrink-0 rounded-chip bg-field px-1.5 py-0.5 text-xs font-medium whitespace-nowrap text-ink-2">
                      {categoryLabel(ev.category)}
                    </span>
                    <div className="min-w-0 flex-1 text-ink">
                      <span>{ev.summary}</span>
                      {ev.ref && !url && (
                        <span className="ml-1.5 text-ink-3">{ev.ref}</span>
                      )}
                      {url && (
                        <a
                          href={url}
                          target="_blank"
                          rel="noreferrer noopener"
                          data-testid="todo-causal-trace-link"
                          className="ml-1.5 inline-flex items-center gap-0.5 text-accent-ink hover:underline"
                        >
                          <span>{ev.ref || t("todos.causal_trace_open_link")}</span>
                          <Icon name="external" size={10} stroke={2} className="shrink-0" />
                        </a>
                      )}
                    </div>
                  </li>
                );
              })}
            </ol>
          ) : null}
        </div>
      )}
    </section>
  );
}
