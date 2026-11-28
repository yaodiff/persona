import { memo, useEffect, useId, useState } from "react";
import { handOffToMain, itemRoute, itemKey, getInbox, loadRoutedItem, suggestionAct, uncertainReplyKey, useInbox, type InboxItem } from "../../lib/inbox";
import { chainTime, suggestionButtons } from "../../lib/inbox-events";
import { handoffCardBody, handoffCardState, wrapUpItems, wrapUpWait, type HandoffCardBody, type HandoffCardState } from "../../lib/handoff-card";
import { later, useFeed, type FeedItem } from "../../lib/feed";
import { navigate } from "../../lib/router";
import { errorText } from "../../lib/api";
import { uuid } from "../../lib/format";
import { Button, cx, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { t } from "../../i18n";
import { useScope, scoped } from "../../lib/current-assistant";
import { sameManager } from "../../lib/manager-choice";
import { BoundMentions, ReplyRichText } from "../inbox/ReplyBody";

// A hand-off conversation's write-back as a card in the chat (#5932, docs/assistant/inbox.md "Item actions"): the
// item's summary, the reply or conclusion, and Send / OK, which confirm the item's suggestion. The item is the one home
// of the request: the card reads it for whether it can still be sent and how it ended, and is not counted in Needs you.

/** The item the card is about: the list's, else the history's, asked for once when neither has it. */
function useCardItem(id: string): { item?: InboxItem; asking: boolean } {
  const scope = useScope();
  const item = useInbox((s) => s.items.find((x) => x.id === id && scope.has(x.manager)) ?? s.history.find((x) => x.id === id && scope.has(x.manager)));
  const loaded = useInbox((s) => s.loaded);
  const asking = useInbox((s) => s.routing.some((k) => k.endsWith(`\u0000${id}`)));
  useEffect(() => {
    if (!item && loaded) loadRoutedItem(id, scope.several ? scope.id : undefined);
  }, [id, item, loaded, scope.several, scope.id]);
  return { item, asking: !item && (asking || !loaded) };
}

/** Reuse the card already in this page, or its existing Inbox home when it is outside the rendered history. */
function revealCurrentSuggestion(item: InboxItem) {
  const suggestion = item.suggestion;
  const card = suggestion && [...document.querySelectorAll<HTMLElement>('[data-testid="handoff-card"]')].find((el) =>
    el.dataset.inboxRef === item.id && el.dataset.suggestionVersion === String(suggestion.version) &&
    el.dataset.conversation === suggestion.conversation?.session_id && sameManager(el.dataset.manager || undefined, item.manager) &&
    el.dataset.state !== "later");
  if (card) {
    card.scrollIntoView?.({ block: "nearest" });
    card.focus({ preventScroll: true });
  } else navigate(itemRoute(item));
}

function sourceName(card: HandoffCardBody): string {
  if (card.source === "mail") return card.account || t("lib-inbox.kind_mail");
  if (card.source === "lark") return card.account || t("lib-inbox.lark");
  return card.account || card.source;
}

export const HandoffCard = memo(function HandoffCard({ item: feed }: { item: FeedItem }) {
  const card = handoffCardBody(feed);
  const id = feed.ref ?? "";
  const { item } = useCardItem(id);
  // only the newest card about the item in this conversation says how the item ended; older ones were replaced
  const newest = useFeed((s) => {
    const mine = (s.feeds[feed.conversation] ?? []).filter((x) => x.kind === "inbox_suggestion" && x.ref === feed.ref);
    return mine.length === 0 || mine[mine.length - 1].id === feed.id;
  });
  const sending = useInbox((s) => !!item && s.sending.includes(itemKey(item)));
  const deferred = useFeed((s) => !!s.later[feed.id]);
  const [text, setText] = useState<string | null>(null);
  const [open, setOpen] = useState(false);
  const headingId = useId();
  const uncertain = useInbox((s) => card && item ? s.uncertainReplies?.[uncertainReplyKey(item, card.version, feed.conversation)] : undefined);
  if (!card || !id) return null;
  const st: HandoffCardState = uncertain
    ? { state: "uncertain", attempt: uncertain } : handoffCardState(card, feed.conversation, item, newest);
  const attempt = "attempt" in st ? st.attempt : undefined;
  const live = st.state === "live" && !!item;
  const reply = card.kind === "reply";
  const draft = text ?? card.text;
  const bodyText = st.state === "sent" ? st.text : attempt?.text ?? card.text;
  const mentions = attempt?.mentions ?? (live ? item?.suggestion?.mentions : undefined) ?? card.mentions;
  const confirm = async () => {
    if (!item) return;
    await suggestionAct(item, "confirm", reply && draft !== card.text ? draft : undefined);
  };
  const retry = async () => {
    if (item) await suggestionAct(item, "confirm", undefined, undefined, card.version);
  };
  const heading = (() => {
    switch (st.state) {
      case "sent":
        return t("assistant-handoffcard.sent");
      case "self":
        return t("assistant-handoffcard.self_replied");
      case "filed":
        return reply ? t("assistant-handoffcard.filed_not_sent") : t("assistant-handoffcard.conclusion_filed");
      case "replaced":
        return t("assistant-handoffcard.replaced");
      case "failed": return t("assistant-handoffcard.failed");
      case "sending": return t("assistant-handoffcard.sending");
      case "uncertain": return t("assistant-handoffcard.uncertain");
      default:
        return reply ? t("assistant-handoffcard.reply_as_you", { title: card.title }) : t("assistant-handoffcard.conclusion");
    }
  })();
  const resolved = st.state !== "live" && st.state !== "unknown";
  if (live && deferred) {
    // Later: one line until it is opened again; the item waits in the Inbox meanwhile
    return (
      <button type="button" onClick={() => later(feed.id, false)} data-testid="handoff-card" data-state="later" className="flex min-w-0 items-center gap-1.5 self-start rounded-full bg-field px-3 py-1.5 text-xs text-ink-2 hover:bg-hover">
        <Icon name="inbox" size={12} className="shrink-0" />
        <span className="min-w-0 truncate">{t("assistant-handoffcard.later_line", { title: card.title })}</span>
      </button>
    );
  }
  const tone = st.state === "sent" ? "bg-green-tint" : resolved ? "bg-field" : "bg-accent-tint";
  const ink = st.state === "sent" ? "text-green" : st.state === "failed" ? "text-red" : resolved ? "text-ink-2" : "text-accent";
  const icon = st.state === "sent" || st.state === "self" ? "check" : st.state === "failed" || st.state === "uncertain" ? "alertCircle" : st.state === "sending" ? "clock" : resolved ? "archive" : reply ? "pencil" : "flag";
  const hasNewer = !!item?.suggestion && (item.suggestion.version !== card.version || item.suggestion.conversation?.session_id !== feed.conversation);
  return (
    <div className="flex flex-col gap-2.5 rounded-card bg-surface p-4 shadow-card focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
      tabIndex={-1} role="group" aria-labelledby={headingId} data-testid="handoff-card" data-state={st.state}
      data-inbox-ref={id} data-suggestion-version={card.version} data-conversation={feed.conversation} data-manager={item?.manager ?? ""}>
      <div className="flex min-w-0 items-center gap-1.5 text-xs font-medium text-ink-2">
        <Icon name={card.source === "mail" ? "mail" : "inbox"} size={13} className="shrink-0 text-accent" />
        <span className="min-w-0 truncate">{[card.title, sourceName(card)].filter(Boolean).join(" · ")}</span>
        <span className="flex-1" />
        {item && (
          <button type="button" onClick={() => navigate(itemRoute(item))} className="shrink-0 rounded-control px-1.5 py-0.5 hover:bg-hover hover:text-ink">
            {t("assistant-handoffcard.open_in_inbox")}
          </button>
        )}
      </div>
      {card.recap && (
        <button type="button" onClick={() => setOpen(!open)} aria-expanded={open} className="flex flex-col gap-0.5 text-left">
          <span className="text-xs font-semibold text-ink-2">{t("assistant-handoffcard.summary")}</span>
          <span className={cx("whitespace-pre-line text-sm [overflow-wrap:anywhere]", !open && "line-clamp-3")}>{card.recap}</span>
        </button>
      )}
      <div className={cx("flex flex-col gap-1 rounded-control p-3", tone)}>
        <span id={headingId} className={cx("flex min-w-0 items-center gap-1 text-xs font-semibold", ink)}>
          <Icon name={icon} size={12} className="shrink-0" />
          <span className="min-w-0 truncate">{heading}</span>
        </span>
        {live && reply ? (
          <>
            <textarea
              value={draft}
              onChange={(e) => setText(e.target.value)}
              aria-labelledby={headingId}
              rows={Math.min(6, Math.max(2, draft.split("\n").length))}
              className="-mx-1.5 resize-none rounded-control bg-transparent px-1.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-accent"
            />
            <BoundMentions text={draft} mentions={mentions} className="pt-1" />
          </>
        ) : (
          <>
            <ReplyRichText text={bodyText} />
            <BoundMentions text={bodyText} mentions={mentions} sent={st.state === "sent"} className="pt-1" />
          </>
        )}
      </div>
      {attempt && Number.isFinite(Date.parse(attempt.at)) && <time dateTime={attempt.at} className="text-xs text-ink-3" data-testid="handoff-attempt-time">{chainTime(attempt.at)}</time>}
      {st.state === "uncertain" && <div className="text-sm text-ink-2" role="status">{t("assistant-handoffcard.uncertain_hint")}</div>}
      {st.state === "sending" && <div className="text-sm text-ink-2" role="status">{t("assistant-handoffcard.sending_hint")}</div>}
      {st.state === "failed" && <div className="text-sm text-ink-2" role="status">
        {t(st.retryable ? "assistant-handoffcard.retry_hint" : "assistant-handoffcard.failed_hint")}
        {st.retryable && attempt?.retry_after ? " " + t("assistant-handoffcard.retry_after", { seconds: Math.ceil(attempt.retry_after) }) : ""}
      </div>}
      {(st.state === "failed" && st.retryable || hasNewer) && <div className="flex flex-col gap-2 sm:flex-row">
        {st.state === "failed" && st.retryable && <Button tone="accent" className="min-h-11" busy={sending} disabled={sending} onClick={() => void retry()} testId="handoff-card-retry">{t("assistant-handoffcard.retry")}</Button>}
        {hasNewer && item && <Button className="min-h-11" onClick={() => revealCurrentSuggestion(item)} testId="handoff-card-current">{t("assistant-handoffcard.current_suggestion")}</Button>}
      </div>}
      {card.note && !resolved && <div className="text-xs text-ink-2 [overflow-wrap:anywhere]">{card.note}</div>}
      {live && (
        <div className="flex flex-col gap-2 sm:flex-row">
          <Button tone="accent" busy={sending} disabled={reply && !draft.trim()} onClick={() => void confirm()} className="min-h-11 flex-1" testId="handoff-card-confirm">
            {suggestionButtons({ version: card.version, kind: card.kind, text: card.text }).confirm}
          </Button>
          <Button disabled={sending} onClick={() => later(feed.id)} className="min-h-11">
            {t("assistant-task-actions.later")}
          </Button>
        </div>
      )}
    </div>
  );
});

/** Over the composer of a conversation items were handed to: "Wrap up: <title>" for each still open, which asks the
 *  conversation for the final summary and reply through the same hand-off (the item's id goes with it). A chip whose
 *  item has a suggestion waiting for the owner says so ("To send" / "To confirm"); one handled with nothing waiting is
 *  gone (wrapUpItems). */
export const WrapUpChips = memo(function WrapUpChips({ conversation }: { conversation: string }) {
  const scope = useScope();
  const items = useInbox((s) => wrapUpItems(scoped(s.items, scope), conversation));
  const [busy, setBusy] = useState<string | null>(null);
  if (!items.length) return null;
  const wrapUp = async (it: InboxItem) => {
    if (it.suggestion) {
      revealCurrentSuggestion(it);
      return;
    }
    setBusy(it.id);
    try {
      await handOffToMain(it, t("assistant-handoffcard.wrap_up_ask"), uuid(), true);
      const current = getInbox().items.find((x) => itemKey(x) === itemKey(it));
      if (current?.suggestion) revealCurrentSuggestion(current);
    } catch (e) {
      toast(t("assistant-handoffcard.wrap_up_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(null);
    }
  };
  return (
    <div className="flex min-w-0 flex-wrap gap-1.5">
      {items.map((it) => (
        <button
          key={it.id}
          type="button"
          disabled={busy !== null}
          onClick={() => void wrapUp(it)}
          data-testid="wrap-up-chip"
          className="inline-flex h-7 min-w-0 max-w-full items-center gap-1.5 rounded-full bg-accent-tint px-3 text-xs font-semibold text-accent hover:brightness-95 disabled:opacity-60"
        >
          <Icon name={busy === it.id ? "clock" : "inbox"} size={12} className="shrink-0" />
          <span className="min-w-0 truncate whitespace-nowrap">{t("assistant-handoffcard.wrap_up", { title: it.title })}</span>
          <WrapUpBadge it={it} />
        </button>
      ))}
    </div>
  );
});

/** A Wrap up chip's note of the item's suggestion waiting for the owner; nothing while none waits. */
function WrapUpBadge({ it }: { it: InboxItem }) {
  const wait = wrapUpWait(it);
  if (!wait) return null;
  return (
    <span data-testid="wrap-up-wait" className="shrink-0 rounded-full bg-accent px-1.5 font-semibold whitespace-nowrap text-white">
      {t(wait === "send" ? "assistant-handoffcard.wrap_up_to_send" : "assistant-handoffcard.wrap_up_to_confirm")}
    </span>
  );
}
