import { useEffect, useMemo, useRef, useState } from "react";
import { decisionKey, decisionPosition, type DecisionCard } from "../../lib/decision-pager";
import type { QuestionDraft } from "../chat/decision-card";

export function useDecisionPager(cards: readonly DecisionCard[]) {
  const keys = useMemo(() => cards.map(({ interaction }) => decisionKey(interaction)), [cards]);
  const [selection, setSelection] = useState<{ key?: string; index: number }>({ index: 0 });
  const [collapsedKey, setCollapsedKey] = useState<string | undefined>(undefined);
  const drafts = useRef(new Map<string, QuestionDraft>()).current;
  const at = decisionPosition(keys, selection.key, selection.index);
  const rememberedIndex = Math.max(at, 0);
  const key = keys[at];
  const collapsed = collapsedKey !== undefined && keys.includes(collapsedKey);
  const collapsedAt = collapsedKey === undefined ? -1 : keys.indexOf(collapsedKey);

  useEffect(() => {
    if (selection.key !== key || selection.index !== rememberedIndex) setSelection({ key, index: rememberedIndex });
    if (!collapsed && collapsedKey !== undefined) setCollapsedKey(undefined);
    for (const draft of drafts.keys()) if (!keys.includes(draft)) drafts.delete(draft);
  }, [collapsed, collapsedKey, drafts, key, keys, rememberedIndex, selection.index, selection.key]);

  function go(index: number) {
    if (keys[index] !== undefined) setSelection({ key: keys[index], index });
  }

  function reveal(id: string) {
    const index = cards.findIndex(({ interaction }) => interaction.id === id);
    if (index < 0) return false;
    go(index);
    setCollapsedKey(undefined);
    return true;
  }

  return {
    at, key, card: cards[at], count: keys.length, collapsed, drafts, go, reveal,
    arrived: collapsed ? keys.length - collapsedAt - 1 : 0,
    open: () => setCollapsedKey(undefined),
    collapse: () => { if (key !== undefined) setCollapsedKey(key); },
  };
}

export type Pager = ReturnType<typeof useDecisionPager>;
