// A chat the hub has no such conversation for, and nothing of it is here to read (a stale or mistyped link): said in
// plain words, with the way back and Retry, never as cached content that can still be read. Its own chunk, drawn only
// then: its words are off the first screen (namespace chat-notfoundnotice).
import { t } from "../../i18n";
import { navigate } from "../../lib/router";
import { Button } from "../../ui/atoms";

/** `assistant`: a Persona conversation, whose way back is the main chat; an Agent's goes back to Overview. */
export function NotFoundNotice({ assistant, retry }: { assistant: boolean; retry: () => void }) {
  return (
    <div role="alert" data-chat-not-found className="flex shrink-0 items-center gap-2 border-b border-line bg-inset px-3 py-2 text-xs text-ink-2">
      <span className="min-w-0 flex-1 break-words">{t("chat-notfoundnotice.body")}</span>
      <Button tone="quiet" onClick={retry}>{t("sidebar.retry")}</Button>
      <Button onClick={() => navigate(assistant ? { name: "persona" } : { name: "home" })}>{assistant ? t("assistant-assistantchat.back_to_main") : t("chat-notfoundnotice.back_home")}</Button>
    </div>
  );
}
