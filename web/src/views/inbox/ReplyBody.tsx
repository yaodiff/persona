import { activeMentions, type InboxMention } from "../../lib/inbox-events";
import { t } from "../../i18n";
import { cx } from "../../ui/atoms";
import { Markdown } from "../../ui/Markdown";

export function BoundMentions({ text, mentions, sent, className }: { text: string; mentions?: InboxMention[]; sent?: boolean; className?: string }) {
  const active = activeMentions(text, mentions);
  if (active.length === 0) return null;
  return (
    <div data-testid="bound-mentions" className={cx("flex flex-wrap items-center gap-1 text-submeta text-ink-2", className)}>
      <span className="text-ink-3">{t(sent ? "assistant-handoffcard.mentions_sent" : "assistant-handoffcard.mentions_will")}</span>
      {active.map((m) => (
        <span
          key={m.id}
          data-mention-id={m.id}
          className={cx("inline-flex items-center rounded-full px-1.5 py-0.5 font-medium", sent ? "bg-surface/70 text-green" : "bg-surface/80 text-accent")}
        >
          @{m.name}
        </span>
      ))}
    </div>
  );
}

export function ReplyRichText({ text, className, dataSuggestionText }: { text: string; className?: string; dataSuggestionText?: boolean }) {
  return (
    <div
      className={cx("text-sm [overflow-wrap:anywhere]", className)}
      {...(dataSuggestionText ? { "data-suggestion-text": "" } : undefined)}
    >
      <Markdown text={text} small lineBreaks />
    </div>
  );
}
