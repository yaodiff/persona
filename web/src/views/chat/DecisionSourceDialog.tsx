import { useCallback } from "react";
import { t } from "../../i18n";
import { decisionSource } from "../../lib/decision-details";
import type { Interaction, Item } from "../../lib/types";
import { Dialog, IconButton } from "../../ui/atoms";
import { ChatView } from "./ChatView";

export function DecisionSourceDialog({ open, card, onClose }: { open: boolean; card: Interaction; onClose: () => void }) {
  const findSource = useCallback((items: readonly Item[]) => decisionSource(card, items, true), [card]);
  return (
    <Dialog open={open} closeOnBack width={960} onClose={onClose} title={
      <span className="flex items-center gap-2">
        <IconButton icon="arrowLeft" label={t("sidepanel.back")} onClick={onClose} />
        <span>{card.session_title || t("chat-interactions.decision_view_source")}</span>
      </span>
    }>
      <div className="flex h-[min(65dvh,640px)] min-h-0 flex-col" data-decision-source-chat>
        <ChatView mid={card.machine_id} sid={card.session_id} embedded findSource={findSource} />
      </div>
    </Dialog>
  );
}
