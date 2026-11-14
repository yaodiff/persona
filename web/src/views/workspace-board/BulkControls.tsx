import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { t } from "../../i18n";
import type { BoardCard, BoardCardChange, BoardMutation, BoardState } from "../../lib/workspace-board";
import { Button } from "../../ui/atoms";

/** Selection belongs to the cards actually visible in this board, not a previous filter or a hidden column. */
export function useBoardSelection({ board, shown, activeColumn, enabled, busy }: {
  board: BoardState | null;
  shown: ReadonlyMap<string, BoardCard[]>;
  activeColumn: string;
  enabled: boolean;
  busy: boolean;
}) {
  const [container, containerRef] = useState<HTMLDivElement | null>(null);
  const [wide, setWide] = useState(false);
  const [selecting, setSelecting] = useState(false);
  const [chosen, setChosen] = useState<ReadonlySet<string>>(() => new Set());
  const active = selecting && enabled;
  // Read the same container-query rule that draws the columns. The sidebar and panel can resize the board without
  // resizing the window; duplicating a viewport breakpoint would select cards the user cannot see.
  useEffect(() => {
    if (!container) return;
    const marker = container.querySelector<HTMLElement>("[data-board-wide]");
    if (!marker) return;
    let last: boolean | undefined;
    const measure = () => {
      const next = getComputedStyle(marker).display !== "none";
      if (next !== last) { last = next; setWide(next); }
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(container);
    return () => observer.disconnect();
  }, [container]);
  const visibleIDs = useMemo(() => new Set((wide ? [...shown.values()].flat() : shown.get(activeColumn) ?? []).map((c) => c.id)), [shown, wide, activeColumn]);
  const selectedIDs = useMemo(() => new Set(active ? [...chosen].filter((id) => visibleIDs.has(id)) : []), [active, chosen, visibleIDs]);
  const wasActive = useRef(false);
  useLayoutEffect(() => {
    if (active === wasActive.current || !container) return;
    wasActive.current = active;
    // The entry/exit buttons are replaced by the mode change. Keep keyboard focus in the board instead of leaving
    // it on the body; on a narrow screen the first card in DOM order may belong to a hidden column.
    if (active) {
      const input = [...container.querySelectorAll<HTMLInputElement>("article[data-card-id] input[type=checkbox]")]
        .find((item) => visibleIDs.has(item.closest("article")?.getAttribute("data-card-id") ?? ""));
      input?.focus();
    } else {
      container.querySelector<HTMLButtonElement>('[data-testid="board-select-cards"]')?.focus();
    }
  }, [active, container, visibleIDs]);
  // Skip temporary optimistic projections: an archive/move may hide selected cards while the request is pending.
  // If it fails, their selection must return with them. Confirmed refreshes and filter changes prune permanently.
  useEffect(() => {
    if (busy) return;
    if (!enabled) setSelecting(false);
    setChosen((previous) => {
      const next = new Set(enabled ? [...previous].filter((id) => visibleIDs.has(id)) : []);
      return next.size === previous.size ? previous : next;
    });
  }, [busy, enabled, visibleIDs]);
  const current = useRef({ active, busy, visibleIDs });
  current.current = { active, busy, visibleIDs };
  const toggle = useCallback((card: BoardCard) => {
    const { active, busy, visibleIDs } = current.current;
    if (!active || busy || !visibleIDs.has(card.id)) return;
    setChosen((previous) => {
      const next = new Set(previous);
      if (next.has(card.id)) next.delete(card.id); else next.add(card.id);
      return next;
    });
  }, []);
  const clear = () => setChosen(new Set());
  return {
    active, containerRef, selectedIDs, visibleIDs, toggle, clear,
    start: () => { clear(); setSelecting(true); },
    finish: () => { clear(); setSelecting(false); },
    selectAll: () => setChosen(new Set(visibleIDs)),
    // Persisted order is the order moves append in; pinned display order must not rewrite workflow order.
    cards: board?.cards.filter((card) => selectedIDs.has(card.id)) ?? [],
  };
}

type Selection = ReturnType<typeof useBoardSelection>;

/** Reversible metadata only. Selecting a workspace never opens it, starts an Agent, or sends one a message. */
export function BulkControls({ board, selection, busy, archived, onApply }: {
  board: BoardState;
  selection: Selection;
  busy: boolean;
  archived: boolean;
  onApply: (mutation: BoardMutation) => Promise<boolean>;
}) {
  const [target, setTarget] = useState("");
  const validTarget = board.columns.some((column) => column.id === target) ? target : "";
  const pending = useRef(false);
  const count = selection.cards.length;
  const allPinned = count > 0 && selection.cards.every((card) => card.pinned);
  const apply = async (fields: Omit<BoardCardChange, "id">) => {
    if (busy || pending.current || !count) return;
    pending.current = true;
    try {
      const changes = selection.cards.map((card) => ({ id: card.id, ...fields }));
      if (await onApply({ action: "batch_cards", changes })) selection.clear();
    } finally {
      pending.current = false;
    }
  };
  if (!selection.active) return <div className="shrink-0 border-b border-line bg-surface px-4 py-2">
    <Button testId="board-select-cards" icon="check" disabled={busy || selection.visibleIDs.size === 0} expanded={false} onClick={selection.start}>{t("workspaceboard.select_cards")}</Button>
  </div>;
  return <div role="group" aria-label={t("workspaceboard.selection_actions")} className="shrink-0 space-y-2 border-b border-line bg-surface px-4 py-3">
    <div className="flex flex-wrap items-center gap-2">
      <p role="status" aria-live="polite" className="min-w-max flex-1 whitespace-nowrap text-sm font-medium text-ink">{busy ? t("workspaceboard.bulk_saving") : t("workspaceboard.selected_count", { count })}</p>
      <Button tone="quiet" disabled={busy || !selection.visibleIDs.size || count === selection.visibleIDs.size} onClick={selection.selectAll}>{t("workspaceboard.select_all_visible")}</Button>
      <Button tone="quiet" disabled={busy || !count} onClick={selection.clear}>{t("workspaceboard.clear_selection")}</Button>
      <Button disabled={busy} onClick={selection.finish}>{t("workspaceboard.selection_done")}</Button>
    </div>
    <div className="flex flex-wrap items-center gap-2">
      <select aria-label={t("workspaceboard.bulk_move_to")} className="min-w-0 max-w-full basis-full flex-1 rounded-control @lg:basis-0 bg-field px-3 py-2 text-sm text-ink outline-none focus:ring-2 focus:ring-accent" disabled={busy || !count} value={validTarget} onChange={(event) => setTarget(event.target.value)}>
        <option value="">{t("workspaceboard.bulk_move_placeholder")}</option>
        {board.columns.map((column) => <option key={column.id} value={column.id}>{column.title}</option>)}
      </select>
      <Button icon="layers" disabled={busy || !count || !validTarget} onClick={() => void apply({ column_id: validTarget })}>{t("workspaceboard.bulk_move")}</Button>
      <Button icon="pin" disabled={busy || !count} onClick={() => void apply({ pinned: !allPinned })}>{allPinned ? t("workspaceboard.unpin") : t("workspaceboard.pin")}</Button>
      <Button icon={archived ? "undo" : "check"} disabled={busy || !count} onClick={() => void apply({ archived: !archived })}>{archived ? t("workspaceboard.restore_card") : t("workspaceboard.bulk_archive")}</Button>
    </div>
    <p className="text-xs text-ink-3">{t("workspaceboard.selection_hint")}</p>
  </div>;
}
