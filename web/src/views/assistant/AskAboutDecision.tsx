import { useRef, useState } from "react";
import { t } from "../../i18n";
import { api, credentialOwner, errorText } from "../../lib/api";
import { conversationPage } from "../../lib/conversation-page";
import { addQuote } from "../../lib/drafts";
import { mainConversationMachine } from "../../lib/feed-cache";
import { setRoute } from "../../lib/feed";
import { whenComposer } from "../../lib/links";
import { ownerHeaders, sameManager, shownManager } from "../../lib/manager-choice";
import { adoptConversation } from "../../lib/outbox";
import { navigate } from "../../lib/router";
import { askOptions, taskReviewDecisionAsk, type Todo, type TodoAsk } from "../../lib/todos";
import type { AssistantInfo } from "../../lib/types";
import { hasFinePointer } from "../../lib/viewport";
import { Button, toast } from "../../ui/atoms";

export function AskAboutDecision({ todo, ask, disabled, onOpenChat }: { todo: Todo; ask: TodoAsk; disabled: boolean; onOpenChat?: () => void }) {
  const [busy, setBusy] = useState(false);
  const opening = useRef(false);
  const open = async () => {
    if (disabled || opening.current) return;
    opening.current = true;
    setBusy(true);
    const owner = credentialOwner();
    try {
      const assistant = await api<AssistantInfo>("/assistant", { headers: ownerHeaders(todo.manager) });
      if (owner !== credentialOwner()) return;
      const sid = assistant.main;
      const mid = mainConversationMachine(assistant);
      if (sid ? !mid : !sameManager(todo.manager, shownManager())) throw new Error(t("chat-chatview.session_gone"));

      const options = askOptions(ask);
      const key = sid ? `${mid}/${sid}` : "persona-new";
      addQuote(key, {
        task: { id: todo.id, title: todo.title.replace(/[\r\n]+/g, " ") },
        text: [
          ask.question || ask.verdict,
          "",
          ...options.map(({ label, leads_to }) => leads_to ? `- ${label}: ${leads_to}` : `- ${label}`),
          "",
          t("assistant-brief.recommend", { option: ask.recommend || options[0]?.label }),
          "",
          t("assistant-decisionrow.ask_discussion_context"),
        ].join("\n"),
      });
      if (sid && mid) {
        adoptConversation(todo.manager, sid, mid);
        // The owner is switching the composer from an earlier task reply to a main-chat discussion.
        setRoute(sid, null);
        navigate(conversationPage(sid, mid, todo.manager));
      } else {
        navigate({ name: "persona" });
      }
      onOpenChat?.();
      if (hasFinePointer()) whenComposer(key, (composer) => requestAnimationFrame(() => composer.focus()));
    } catch (e) {
      if (owner === credentialOwner()) toast(t("assistant-decisionrow.ask_in_main_failed", { error: errorText(e) }), "error");
    } finally {
      opening.current = false;
      setBusy(false);
    }
  };

  if (!taskReviewDecisionAsk(todo)) return null;
  return (
    <Button tone="quiet" className="min-h-11" disabled={disabled} busy={busy} onClick={() => void open()}>
      {t("assistant-decisionrow.ask_in_main_chat")}
    </Button>
  );
}
