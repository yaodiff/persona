import { ApiError, errorText } from "../../lib/api";
import { safeWebURL } from "../../lib/format";
import { larkMessagePreview } from "../../lib/lark-inbox-card";
import type { ThreadPage } from "../../lib/connectors";
import type { InboxItem } from "../../lib/inbox";
import { t } from "../../i18n";
import { Button } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

export interface ConversationFailure { kind: "unsupported" | "unavailable" | "rate_limited" | "failed"; detail: string }
export function conversationFailure(error: unknown): ConversationFailure {
  const detail = errorText(error);
  if (error instanceof ApiError) {
    if (error.code === "conversations_unsupported" || error.status === 409 && / has no conversations to open$/.test(detail)) return { kind: "unsupported", detail };
    if (error.status === 429) return { kind: "rate_limited", detail };
    if (error.status === 401 || error.status === 403 || error.code === "unavailable") return { kind: "unavailable", detail };
  }
  return { kind: "failed", detail };
}

export function ConnectorReadNotice({ failure, onRetry }: { failure: ConversationFailure; onRetry: () => void }) {
  return <div className="text-xs text-ink-3">
    <details>
      <summary className="touch-hit flex cursor-pointer list-none items-start gap-2 [&::-webkit-details-marker]:hidden focus-visible:outline-2 focus-visible:outline-accent">
        <Icon name="info" size={14} className="mt-0.5 shrink-0" /><span>{t({ unsupported: "inbox-connectorthread.read_unsupported", unavailable: "inbox-connectorthread.read_unavailable", rate_limited: "inbox-connectorthread.read_rate_limited", failed: "inbox-connectorthread.read_failed" }[failure.kind])}</span>
      </summary>
      <p className="mt-2 break-words rounded-control bg-inset px-3 py-2">{failure.detail}</p>
    </details>
    {failure.kind === "failed" && <Button tone="quiet" className="mt-1" onClick={onRetry}>{t("inbox-connectorthread.retry_conversation")}</Button>}
  </div>;
}

export function ConnectorReplyTarget({ item, page }: { item: InboxItem; page?: ThreadPage | null }) {
  const target = item.connector?.reply_to;
  if (item.source !== "larkcli") return null;
  const message = page?.messages.find((m) => m.id === target);
  const preview = message?.text ? larkMessagePreview(message.text) : "";
  const source = item.connector?.open_url ? undefined : safeWebURL(message?.url);
  const reply = target && item.connector?.can_reply;
  if (!reply && !source) return null;
  return <div className="flex shrink-0 items-center gap-2 border-b border-line px-4 py-2 text-xs text-ink-3">
    {reply && <details className="min-w-0 flex-1">
    <summary className="touch-hit flex cursor-pointer list-none flex-wrap items-center gap-x-2 gap-y-1 [&::-webkit-details-marker]:hidden focus-visible:outline-2 focus-visible:outline-accent">
      <span className="text-ink-2">{t("inbox-connectorthread.reply_to_name", { name: item.title })}</span>
      <span className="min-w-0 truncate">{preview || t("inbox-connectorthread.reply_unknown")}</span><Icon name="chevron" size={12} />
    </summary>
    <p className="mt-2 break-words font-mono select-text">{target}</p>
    </details>}
    {source && <Button tone="secondary" icon="external" onClick={() => window.open(source, "_blank", "noopener,noreferrer")}>
      {source.startsWith("https://applink.larksuite.com/") ? t("inbox-inboxdetail.open_in_lark") : t("inbox-inboxdetail.open_in_feishu")}
    </Button>}
  </div>;
}
