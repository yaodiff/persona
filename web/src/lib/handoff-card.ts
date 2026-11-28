// A conversation an Inbox item was handed to writes back to it (inbox_suggest with item): the reply or conclusion is
// also a card in that conversation (#5932, docs/assistant/inbox.md "Item actions"). The card is the suggestion as it was
// handed in; the item says whether it can still be sent and how it ended, so nothing here is stored on the card.

import { isDone, type InboxItem } from "./inbox";
import type { FeedItem } from "./feed";
import type { InboxMention, InboxSuggestionAttempt } from "./inbox-events";

/** The card's body (assistant/inbox_handoff_card.go handoffCard). */
export interface HandoffCardBody {
  version: number;
  kind: "reply" | "conclusion" | string;
  /** A conclusion's: "fyi" (worth knowing; OK files it) or "done" (filed at once). */
  outcome?: string;
  text: string;
  mentions?: InboxMention[];
  note?: string;
  recap?: string;
  title: string;
  source: string;
  account?: string;
  sender?: string;
}

export type HandoffCardState =
  /** The item's suggestion is still this one from this conversation: Send / OK works. */
  | { state: "live" }
  /** Persona sent a reply on the user's confirmation; text: what went out. */
  | { state: "sent"; text: string; attempt?: InboxSuggestionAttempt }
  /** Only the current version's confirmed non-delivery may be retried. */
  | { state: "failed"; attempt: InboxSuggestionAttempt; retryable: boolean }
  | { state: "sending" | "uncertain"; attempt: InboxSuggestionAttempt }
  /** The user answered at the source themselves. */
  | { state: "self" }
  /** Handled without this reply going out (OK on a conclusion, Done, read elsewhere, filed by the AI). */
  | { state: "filed" }
  /** The conversation handed in a newer suggestion, or the user turned this one down. */
  | { state: "replaced" }
  /** The item is not here (yet). */
  | { state: "unknown" };

export function handoffCardBody(it: FeedItem): HandoffCardBody | undefined {
  const b = it.body as Partial<HandoffCardBody> | undefined;
  if (!b || typeof b.version !== "number" || typeof b.text !== "string") return undefined;
  return { ...b, version: b.version, text: b.text, kind: b.kind ?? "reply", title: b.title ?? "", source: b.source ?? "" };
}

/** Where the card stands, from the item as the inbox has it now. `newest`: no later card about the item in this
 *  conversation (only the newest one says how the item ended). */
export function handoffCardState(card: HandoffCardBody, conversation: string, item: InboxItem | undefined, newest: boolean): HandoffCardState {
  if (!item) return { state: "unknown" };
  const attempt = handoffCardAttempt(card, conversation, item);
  if (attempt) {
    switch (attempt.status) {
      case "sent": return { state: "sent", text: attempt.text, attempt };
      case "failed": return { state: "failed", attempt, retryable: attempt.retryable && !isDone(item) &&
        item.suggestion?.version === card.version && item.suggestion?.conversation?.session_id === conversation };
      case "sending": return { state: "sending", attempt };
      default: return { state: "uncertain", attempt }; // unknown or a future outcome cannot authorize another send
    }
  }
  if (isDone(item)) {
    if (!newest) return { state: "replaced" };
    if (item.done_by === "source_replied") return { state: "self" };
    if (item.done_by === "replied") {
      const last = [...(item.chain ?? [])].reverse().find((e) => e.kind === "confirmed" || e.kind === "self_replied");
      if (last?.kind === "self_replied") return { state: "self" };
      return { state: "sent", text: last?.detail || card.text };
    }
    return { state: "filed" };
  }
  const s = item.suggestion;
  if (s && s.version === card.version && s.conversation?.session_id === conversation) return { state: "live" };
  return { state: "replaced" };
}

/** Historical cards use their own version's receipt, never the current suggestion's text or delivery. */
function handoffCardAttempt(card: HandoffCardBody, conversation: string, item: InboxItem | undefined): InboxSuggestionAttempt | undefined {
  return item?.suggestion_attempts?.find((a) => a.version === card.version && (!a.conversation || a.conversation.session_id === conversation));
}

/** How many "Wrap up" chips a conversation shows at most, newest hand-off first. */
export const WRAP_UP_MAX = 2;

/** What a "Wrap up" chip says besides its title: a reply waiting to be sent, another suggestion (a conclusion, an
 *  Agent to start) waiting to be confirmed, or nothing while the item is still being worked on. */
export type WrapUpWait = "send" | "confirm" | undefined;

/** The suggestion of item it waiting for the owner, as a Wrap up chip shows it. */
export function wrapUpWait(it: InboxItem): WrapUpWait {
  const s = it.suggestion;
  if (!s) return undefined;
  if (it.suggestion_attempts?.some((a) => a.version === s.version)) return undefined;
  return s.kind === "reply" ? "send" : "confirm";
}

/** The open items handed to conversation sid, newest hand-off first: each gets a "Wrap up" chip over the composer.
 *  An item whose handling is done with no suggestion waiting has nothing left to wrap up: it stays in the Inbox,
 *  the one home of the request, and gets no chip. The items themselves, so the list compares equal while they do. */
export function wrapUpItems(items: InboxItem[], sid: string): InboxItem[] {
  const at = (it: InboxItem) => {
    const h = [...(it.handoffs ?? [])].reverse().find((x) => x.session_id === sid);
    return h ? Date.parse(h.at ?? "") || 0 : -1;
  };
  return items
    .filter((it) => !isDone(it) && at(it) >= 0 && !(it.handling?.state === "done" && !it.suggestion))
    .sort((a, b) => at(b) - at(a))
    .slice(0, WRAP_UP_MAX);
}
