import { useRef } from "react";
import { t } from "../../i18n";
import { blockedDecisionSkip, decisionSource } from "../../lib/decision-details";
import { decisionDots, type DecisionCard } from "../../lib/decision-pager";
import type { Item } from "../../lib/types";
import { cx, IconButton } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { InteractionCard } from "../chat/Interactions";
import { DecisionCardContext } from "../chat/decision-card";
import type { Pager } from "./decision-dock";

export function DecisionPager({ pager, id, openSource, items = [], inCall = false }: {
  pager: Pager; id: string; inCall?: boolean; items?: readonly Item[]; openSource: (card: DecisionCard, target?: string) => void;
}) {
  const start = useRef<{ x: number; y: number; id: number } | null>(null);
  const card = pager.card;
  if (!card) return null;
  const task = card.taskTitle !== undefined;
  const target = task ? undefined : decisionSource(card.interaction, items, false);
  const blocked = blockedDecisionSkip(card.interaction);
  const skipHint = blocked && t(`chat-interactions.decision_skip_${blocked}`);
  const navigation = <>
    {(task || target) && <button type="button" onClick={() => openSource(card, target)} className="touch-hit hover:text-ink">{t("chat-interactions.decision_view_source")}</button>}
    {!inCall && <button type="button" onClick={pager.collapse} className="touch-hit hover:text-ink">{t("chat-interactions.decision_collapse")}</button>}
  </>;
  return (
    <section id={id} aria-label={t("assistant-tasks.to_decide")} data-decision-pager data-decision-swipe={pager.count > 1 || undefined}
      className="flex min-h-0 shrink touch-pan-y flex-col gap-1"
      onTouchStart={(event) => {
        start.current = null;
        if (event.touches.length !== 1 || (event.target instanceof HTMLElement && event.target.closest("input,textarea,select,[contenteditable]"))) return;
        const touch = event.touches[0];
        start.current = { x: touch.clientX, y: touch.clientY, id: touch.identifier };
      }}
      onTouchCancel={() => { start.current = null; }}
      onTouchEnd={(event) => {
        const before = start.current;
        start.current = null;
        if (!before) return;
        const touch = Array.from(event.changedTouches).find(({ identifier }) => identifier === before.id);
        if (!touch) return;
        const x = touch.clientX - before.x;
        const y = touch.clientY - before.y;
        if (Math.abs(x) >= 48 && Math.abs(x) > 2 * Math.abs(y)) pager.go(pager.at + (x < 0 ? 1 : -1));
      }}>
      {pager.count > 1 && (
        <div className="flex shrink-0 items-center gap-2 px-1 text-label text-ink-2">
          <span aria-hidden className="flex shrink-0 items-center gap-1">
            {decisionDots(pager.count, pager.at).map((dot) => (
              <span key={dot} className={cx("h-1.5 rounded-full", dot === pager.at ? "w-3 bg-accent" : "w-1.5 bg-ink-3 opacity-40")} />
            ))}
          </span>
          <span className="tabular-nums" data-decision-position aria-live="polite">{pager.at + 1} / {pager.count}</span>
          <span className="flex-1" />
          <IconButton icon="chevronLeft" label={t("assistant-tasks.previous_decision")} disabled={pager.at === 0} onClick={() => pager.go(pager.at - 1)} />
          <IconButton icon="chevronRight" label={t("assistant-tasks.next_decision")} disabled={pager.at === pager.count - 1} onClick={() => pager.go(pager.at + 1)} />
        </div>
      )}
      {card.taskTitle && <p className="shrink-0 truncate px-1 text-label text-ink-3">{card.taskTitle}</p>}
      <div className="min-h-0 shrink overflow-y-auto overscroll-contain p-1" data-scroll-box>
        <DecisionCardContext.Provider key={pager.key} value={{ interaction: card.interaction, task, drafts: pager.drafts, navigation, skipHint }}>
          <InteractionCard ix={card.interaction} />
        </DecisionCardContext.Provider>
      </div>
    </section>
  );
}

export function DecisionEntry({ pager, expanded, controls, capsule, onToggle }: {
  pager: Pager; expanded: boolean; controls: string; capsule: string; onToggle: () => void;
}) {
  if (!pager.count) return null;
  return (
    <button type="button" data-decision-entry aria-expanded={expanded} aria-controls={expanded ? controls : undefined} onClick={onToggle}
      className={cx(capsule, "gap-2 font-medium", expanded ? "bg-ink text-surface" : "bg-field text-ink hover:bg-hover")}>
      <Icon name="hand" size={14} className={expanded ? "text-surface" : "text-attention"} />
      <span>{t("assistant-tasks.to_decide")}</span>
      <span className="tabular-nums">{pager.count}</span>
      {pager.arrived > 0 && <span className="tabular-nums">+{pager.arrived}</span>}
    </button>
  );
}
