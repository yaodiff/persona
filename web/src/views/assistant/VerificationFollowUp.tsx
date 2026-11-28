import { useEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { api, credentialOwner, errorText } from "../../lib/api";
import { conversationPage } from "../../lib/conversation-page";
import { conversationDeleted } from "../../lib/conversation-deleted";
import { addQuote } from "../../lib/drafts";
import { setRoute } from "../../lib/feed";
import { mainConversationMachine } from "../../lib/feed-cache";
import { whenComposer } from "../../lib/links";
import { ownerHeaders, sameManager, shownManager } from "../../lib/manager-choice";
import { closePanel } from "../../lib/panel";
import { navigate } from "../../lib/router";
import { getState } from "../../lib/store";
import { personalVerificationAsk, type Todo } from "../../lib/todos";
import type { AssistantInfo } from "../../lib/types";
import { Button, toast } from "../../ui/atoms";

export function VerificationFollowUp({ todo, disabled }: { todo: Todo; disabled?: boolean }) {
  const [busy, setBusy] = useState(false);
  const live = useRef(true);
  const ask = personalVerificationAsk(todo);
  const origin = todo.origin;
  useEffect(() => {
    live.current = true;
    return () => { live.current = false; };
  }, []);
  if (!origin || !ask) return null;

  async function followUp(sid: string, text: string) {
    const owner = credentialOwner();
    const current = getState().assistant;
    setBusy(true);
    try {
      const assistant = current && sameManager(current.manager ?? shownManager(), todo.manager)
        ? current : await api<AssistantInfo>("/assistant", { headers: ownerHeaders(todo.manager) });
      if (!live.current || credentialOwner() !== owner) return;
      const mid = assistant.main === sid ? mainConversationMachine(assistant)
        : assistant.conversations?.find((c) => c.session_id === sid)?.machine_id || assistant.machine_id;
      if (!mid || conversationDeleted(mid, sid)) throw new Error(t("chat-chatview.session_gone"));
      const key = `${mid}/${sid}`;
      addQuote(key, { task: { title: todo.title, id: todo.id }, text }, false);
      setRoute(sid, null);
      closePanel();
      window.dispatchEvent(new CustomEvent("persona:verification-follow-up", { detail: sid }));
      navigate(conversationPage(sid, mid, todo.manager));
      whenComposer(key, (composer) => composer.focus());
    } catch (error) {
      if (live.current && credentialOwner() === owner) toast(errorText(error), "error");
    } finally {
      if (live.current) setBusy(false);
    }
  }
  return <Button icon="chat" disabled={disabled || busy} busy={busy} onClick={() => void followUp(origin, ask.summary || ask.verdict)}>
    {t("assistant-taskpanel.follow_up")}
  </Button>;
}
