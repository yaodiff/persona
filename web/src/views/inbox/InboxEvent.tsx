// An inbox item as an event (docs/assistant/inbox.md "Events and handling chain", docs/web/web.md "Inbox"): the row's mark (the
// manager's current state, or an older hub's handling / suggestion line) and the detail's body — ✦ summary, the suggestion card
// with its buttons right under it, any recap, then the handling chain folded to its three newest entries, each entry going where its link says.
// Drawn only for an item that carries this phase's fields (lib/inbox-events.ts `hasEvent`).
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import {
  actOnSuggestion, canExpand, chainRows, chainTap, chainTime, firstLine, handlingFailed, handlingWord, preparing, suggestionButtons, suggestionTitle,
  type ChainEntry, type ChainTap, type InboxSuggestion,
} from "../../lib/inbox-events";
import { getInbox, isDone, itemKey, itemPath, itemRequest, itemRoute, reloadInbox, replaceItem, sameItem, suggestionAct, uncertainReplyKey, useInbox, type InboxItem } from "../../lib/inbox";
import { conversationRoute } from "../../lib/conversation-page";
import { api, ApiError, errorText } from "../../lib/api";
import { markdownBody, markdownInSummary, withoutMarks } from "../../lib/inbox-markdown";
import { docChangeLine, inboxSessionRoute, openInboxConversation } from "../../lib/inbox-page";
import { mainConversationMachine } from "../../lib/feed-cache";
import { otherManager, sameManager } from "../../lib/manager-choice";
import { nowText } from "../../lib/inbox-now";
import { connectorBrand } from "../../lib/connectors";
import { activityOf } from "../../lib/format";
import { stateName } from "../../lib/state-labels";
import { openLink } from "../../lib/links";
import { type Route } from "../../lib/router";
import { openFromInbox } from "../../lib/inbox-nav";
import { useComposition } from "../../lib/ime";
import { useStore } from "../../lib/store";
import { t } from "../../i18n";
import { Button, cx, Shimmer, Spinner, Switch, toast } from "../../ui/atoms";
import { BrandMark } from "../../ui/BrandMark";
import { BoundMentions, ReplyRichText } from "./ReplyBody";
import { Icon, type IconName } from "../../ui/icons";
import { Markdown } from "../../ui/Markdown";
import { StatusMark } from "../../ui/StatusMark";
import { useFoldScroll } from "../chat/fold-scroll";
import { useInboxView } from "../../lib/inbox-view-memory";
import { InboxLine } from "./InboxLine";
import { Glyph, Spark, Tag } from "./InboxParts";
import { useItemReadOnly } from "./item-write-gate";

/** Where a tapped `source` entry (or "Original") opens the item's own page: at a Feishu position, a mail's uid, or a URL. */
export type SourceAt = { position?: string; uid?: number; url?: string };

/** An untitled main chat can be named from this manager's loaded snapshot, without looking up the target. */
function SuggestionLabel({ item, suggestion }: { item: InboxItem; suggestion: InboxSuggestion }) {
  const main = useStore(({ assistant }) => suggestion.kind === "handoff" && !suggestion.target_title && !!assistant?.main
    && sameManager(item.manager, assistant.manager) && suggestion.session_id === assistant.main
    && suggestion.machine_id === mainConversationMachine(assistant));
  return suggestionTitle(suggestion, main);
}

// ── the row ──────────────────────────────────────────────

/** Under a row: a known manager state always has its own line; an older hub keeps the handling or suggestion line. */
export function EventMark({ it }: { it: InboxItem }) {
  const now = nowText(it);
  if (now !== undefined) {
    const key = it.now!;
    const needsYou = key === "confirm_reply" || key === "confirm_agent" || key === "confirm_handoff" || key === "decide" || key === "follow_up_ask" || key === "check_due" || key === "failed" || key === "needs_you";
    const handoff = (key === "confirm_handoff" || key === "decide") && it.suggestion?.kind === "handoff" ? it.suggestion : undefined;
    const working = key === "queued" || key === "looking" || key === "agent_working";
    const icon = key === "waiting" ? "clock" : working ? "spinner" : needsYou ? "needs-you" : key.startsWith("done_") ? "check" : "none";
    return (
      <p className={cx("flex min-w-0 items-center gap-1.5 text-submeta whitespace-nowrap", key === "failed" ? "text-red" : isDone(it) ? "text-ink-3" : "text-ink-2")} data-event-mark="now" data-event-icon={icon}>
        {icon === "clock" && <Icon name="clock" size={12} className="shrink-0 text-ink-3" />}
        {icon === "spinner" && <Spinner size={11} />}
        {/* failed keeps the Needs-you shape, but both its mark and its words use the danger color */}
        {icon === "needs-you" && (
          <span className={cx("inline-flex shrink-0", key === "failed" && "[&_.status-mark]:![--mark-color:var(--red)]")}>
            <StatusMark state="needs_you" size="sm" label={false} />
          </span>
        )}
        {icon === "check" && <Icon name="check" size={12} stroke={2.4} className="shrink-0" />}
        {key === "looking" ? <Shimmer className="truncate">{now}</Shimmer> : <span className="truncate">
          {handoff ? <>{key === "decide" && `${now} · `}<SuggestionLabel item={it} suggestion={handoff} /></> : now}
        </span>}
      </p>
    );
  }
  if (isDone(it)) {
    // An older hub keeps the manager-done line alone.
    if (it.done_by !== "manager") return null;
    return (
      <p className="flex min-w-0 items-center gap-1.5 text-submeta whitespace-nowrap text-ink-3" data-event-mark="manager-done">
        <Icon name="check" size={12} stroke={2.4} className="shrink-0" />
        <span className="truncate">{t("inbox-inboxevent.manager_done")}</span>
      </p>
    );
  }
  const s = it.suggestion;
  if (s) {
    return (
      <p className="flex min-w-0 items-center gap-1.5 text-submeta whitespace-nowrap" data-event-mark="suggestion">
        <span className="max-w-full shrink-0 truncate font-medium text-accent-ink"><Spark /><SuggestionLabel item={it} suggestion={s} /></span>
        <span className="min-w-0 truncate text-ink-2">{firstLine(s.text)}</span>
      </p>
    );
  }
  if (preparing(it)) {
    return (
      <p className="flex min-w-0 items-center gap-1.5 text-submeta whitespace-nowrap" data-event-mark="preparing">
        <Spinner size={11} />
        <Shimmer className="truncate">{t("inbox-inboxevent.preparing")}</Shimmer>
      </p>
    );
  }
  if (handlingFailed(it)) {
    return (
      <p className="flex min-w-0 items-center gap-1.5 text-submeta whitespace-nowrap text-red" data-event-mark="failed">
        <Icon name="warning" size={12} className="shrink-0" />
        <span className="truncate">{t("inbox-inboxevent.failed_short")}</span>
      </p>
    );
  }
  const oldWord = handlingWord(it);
  if (!oldWord) return null;
  const waiting = it.handling?.state === "waiting";
  return (
    <p className="flex min-w-0 items-center gap-1.5 text-submeta whitespace-nowrap text-ink-2" data-event-mark="working">
      {waiting ? <Icon name="clock" size={12} className="shrink-0 text-ink-3" /> : <Spinner size={11} />}
      <span className="truncate">{oldWord}</span>
    </p>
  );
}

// ── the detail ───────────────────────────────────────────

/**
 * The detail's body for an event: the active state and its actions, the summary, any suggestion and recap, the handling chain, "Related" and "Trail".
 * `embedded`: above the item's own page (a Feishu chat or mail, #3262 / #5471), as tall as it needs up to a share of the pane, then scrolling.
 */
export function EventBody({ item, onSource, embedded = false }: { item: InboxItem; onSource: (at: SourceAt) => void; embedded?: boolean }) {
  const done = isDone(item);
  const now = nowText(item);
  // capped above the source: its bottom fades while there is more below, so a cut-off row reads as more to scroll to
  const box = useRef<HTMLDivElement>(null);
  const fold = useFoldScroll(box, embedded, [item]);
  const hasConversation = Boolean(item.handling?.machine_id && item.handling.session_id);
  const body = (
    <div ref={box} {...fold} className={embedded ? "min-h-0 overflow-y-auto" : "min-h-0 flex-1 overflow-y-auto"} data-event-body={embedded ? "embedded" : ""}>
      <div className={cx("mx-auto flex max-w-[760px] flex-col px-4 sm:px-6", embedded ? "gap-3 py-3" : "gap-4 py-4")}>
        {(!done || now !== undefined || hasConversation) && <HandlingLine key={itemKey(item)} item={item} now={now} />}
        {done && now === undefined && item.done_by === "manager" && !hasConversation && (
          <p className="flex items-center gap-2 text-[12.5px] text-ink-2" data-event-manager-done>
            <Icon name="check" size={13} stroke={2.4} className="shrink-0 text-green" />
            {t("inbox-inboxevent.manager_done")}
          </p>
        )}
        {!done && item.suggestion && item.summary && <EventSummary text={item.summary} marked />}
        {!done && item.suggestion && <SuggestionCard key={`${itemKey(item)}@${item.suggestion.version}`} item={item} suggestion={item.suggestion} />}
        {!done && item.suggestion && item.recap && (
          <section className="flex flex-col gap-2">
            <div>
              <p className="text-submeta font-medium text-ink-3">{t("inbox-inboxevent.recap")}</p>
              <p className="mt-0.5 text-meta leading-relaxed whitespace-pre-wrap text-ink-2" data-event-recap>{item.recap}</p>
            </div>
          </section>
        )}
        {(done || !item.suggestion) && (item.summary || item.recap) && (
          <section className="flex flex-col gap-2">
            {item.summary && (markdownBody(item) ? <AssistantBody key={itemKey(item)} text={markdownBody(item)!} /> : <EventSummary text={item.summary} />)}
            {item.recap && (
              <div>
                <p className="text-submeta font-medium text-ink-3">{t("inbox-inboxevent.recap")}</p>
                <p className="mt-0.5 text-meta leading-relaxed whitespace-pre-wrap text-ink-2" data-event-recap>{item.recap}</p>
              </div>
            )}
          </section>
        )}
        <Chain item={item} onSource={onSource} />
        <Related item={item} />
        <InboxLine item={item} />
      </div>
    </div>
  );
  // the line under it outside the fade, which would wash it out
  return embedded ? <div className="flex max-h-[40%] shrink-0 flex-col border-b border-line">{body}</div> : body;
}

/** Lines an assistant row's text shows folded: a briefing's conclusion and first section. */
const BODY_LINES = 10;
/** Folded: ten lines high, scrolling inside, its bottom fading while there is more (fold-scroll), as a long message in the chat. */
const BODY_FOLDED = "max-h-[10lh] overflow-y-auto";

/** The assistant's own row's text (#9041): the chat's Markdown, folded to BODY_LINES with Expand under it when it runs
 *  longer, never cut with "…". */
function AssistantBody({ text }: { text: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const [long, setLong] = useState(() => text.split("\n").length > BODY_LINES);
  const [open, setOpen] = useState(false);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || open) return;
    // its whole height in lines (a fold's scrollHeight is all of it)
    const line = parseFloat(getComputedStyle(el).lineHeight) || 22;
    setLong(el.scrollHeight / line > BODY_LINES + 0.5);
  }, [text, open]);
  const folded = long && !open;
  const fold = useFoldScroll(ref, folded, [text]);
  return (
    <div className="flex flex-col items-start gap-1" data-event-text>
      <div ref={ref} {...fold} className={cx("w-full text-meta leading-relaxed text-ink", folded && BODY_FOLDED)} data-folded={folded || undefined}>
        {/* a reply, read line by line as it was written, as the chat draws one */}
        <Markdown text={text} small lineBreaks />
      </div>
      {long && (
        <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="rounded-[6px] px-1.5 py-0.5 text-label font-medium whitespace-nowrap text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink-2">
          {open ? t("inbox-inboxevent.entry_collapse") : t("inbox-inboxevent.entry_expand")}
        </button>
      )}
    </div>
  );
}

/** The active state, its processing conversation, and a way to instruct an open item without a suggestion. */
/** The ✦ summary, before the suggestion (`marked`, #7473) or with the recap: one paragraph style for both. */
function EventSummary({ text, marked = false }: { text: string; marked?: boolean }) {
  return (
    <p className="text-[13.5px] leading-relaxed font-medium text-ink" data-event-summary={marked || undefined}>
      <Spark />
      {text}
    </p>
  );
}

/** Agent state can also mean the Assistant's handling chat has a child Agent. Check which session the chain links. */
function assistantHandlingFallback(item: InboxItem, mid: string, sid: string): boolean {
  const h = item.handling;
  let assistant = h?.state !== "agent" || h.machine_id !== mid || h.session_id !== sid;
  for (const e of item.chain ?? []) {
    const link = e.link;
    if (link?.type !== "session" || !("machine_id" in link) || link.machine_id !== mid || link.session_id !== sid) continue;
    if (e.kind === "agent" || e.kind === "agent_stopped") return false;
    if (e.kind === "handling" && !e.detail) assistant = true;
  }
  return assistant;
}

function HandlingLine({ item, now }: { item: InboxItem; now?: string }) {
  const [sayOpen, setSayOpen] = useState(false);
  const [say, setSay] = useState("");
  const [acting, setActing] = useState(false);
  const ime = useComposition();
  const { onBlur: resetIme } = ime;
  const inFlightRef = useRef(false);
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);
  const h = item.handling;
  const mid = h?.machine_id;
  const sid = h?.session_id;
  const done = isDone(item);
  const canSay = !done && !item.suggestion;
  const failed = canSay && (h ? handlingFailed(item) : item.now === "failed" || item.agent?.state === "failed");
  const showSay = sayOpen && canSay && !failed;
  useEffect(() => {
    if (!showSay) resetIme();
  }, [showSay, resetIme]);
  const showPreparing = !done && preparing(item);
  if (!h && !canSay && now === undefined) return null;
  const word = now ?? (done ? (item.done_by === "manager" ? t("inbox-inboxevent.manager_done") : undefined) : handlingWord(item));
  const working = h?.state === "looking" || h?.state === "agent";
  const instruct = async (words: string) => {
    const text = words.trim();
    if (!text || acting || inFlightRef.current) return;
    inFlightRef.current = true;
    setActing(true);
    try {
      const r = await actOnSuggestion(itemPath(item.id), 0, "instruct", text, undefined, item.manager);
      if (r.ok) {
        if (r.item?.id === item.id) replaceItem(r.item, item.manager);
        if (mountedRef.current) {
          setSay("");
          setSayOpen(false);
        }
        toast(t("lib-inbox-events.instructed"), "ok");
      } else if (r.stale) {
        if (r.item) replaceItem(r.item, item.manager);
        else void reloadInbox();
        toast(r.error, "info");
      } else {
        toast(t("lib-inbox.suggestion_failed", { error: r.error }), "error");
      }
    } finally {
      inFlightRef.current = false;
      if (mountedRef.current) setActing(false);
    }
  };
  const retry = async () => {
    if (acting || inFlightRef.current) return;
    inFlightRef.current = true;
    setActing(true);
    const current = () => getInbox().items.find((row) => sameItem(row, item));
    const before = current();
    const reconcile = (fresh?: InboxItem) => {
      if (fresh?.id !== item.id || (fresh.manager && !sameItem(fresh, item))) return false;
      // A push can report newer handling progress before this request's response arrives.
      if (current() === before) replaceItem(fresh, item.manager);
      return true;
    };
    try {
      const fresh = await api<InboxItem>(`${itemPath(item.id)}/retry`, { method: "POST", headers: itemRequest(item) });
      reconcile(fresh);
      toast(t("inbox-inboxdetail.retry_requested"), "ok");
    } catch (err) {
      if (err instanceof ApiError && err.status === 409 && err.body?.code === "inbox_retry_stale") {
        if (!reconcile(err.body.item as InboxItem | undefined)) void reloadInbox();
        toast(errorText(err), "info");
      } else {
        void reloadInbox();
        toast(t("inbox-inboxdetail.retry_failed", { error: errorText(err) }), "error");
      }
    } finally {
      inFlightRef.current = false;
      if (mountedRef.current) setActing(false);
    }
  };
  const open = mid && sid && (
    <button type="button" onClick={() => openInboxConversation(mid, sid, item.manager, assistantHandlingFallback(item, mid, sid))} className="-mx-1 inline-flex h-6 shrink-0 items-center gap-1 rounded-[6px] px-1 text-label font-medium whitespace-nowrap text-accent-ink transition-colors hover:bg-hover">
      {t("inbox-inboxevent.open_conversation")}
      <Icon name="chevronRight" size={11} stroke={2.4} />
    </button>
  );
  const action = canSay && (failed ? (
    <Button tone="quiet" size="sm" icon="retry" busy={acting} onClick={() => void retry()} testId="inbox-retry">{t("inbox-inboxdetail.retry")}</Button>
  ) : !sayOpen ? (
    <Button tone="quiet" size="sm" icon="chat" onClick={() => setSayOpen(true)} testId="inbox-say">{t("inbox-inboxdetail.say_something")}</Button>
  ) : null);
  const actions = (open || action) ? <div className="flex shrink-0 items-center gap-2" data-event-status-actions>{open}{action}</div> : null;
  if (!showPreparing && !failed && !word && !(done && open) && !action && !showSay) return null;
  return (
    <div className="flex flex-col gap-2">
      {showPreparing ? (
        <div className="flex min-w-0 flex-col gap-2 rounded-[10px] bg-inset p-3 text-[12.5px] shadow-hairline sm:flex-row sm:items-center" data-event-preparing>
          <div className="flex min-w-0 items-center gap-2 sm:flex-1">
            <Spinner size={12} />
            <Shimmer className="min-w-0 flex-1 truncate">{now ?? t("inbox-inboxevent.preparing")}</Shimmer>
          </div>
          {actions}
        </div>
      ) : failed ? (
        <div className="flex min-w-0 flex-col gap-2 rounded-[10px] bg-red-tint p-3 text-[12.5px] text-red sm:flex-row sm:items-start" role="status" data-event-failed>
          <div className="flex min-w-0 items-start gap-2 sm:flex-1">
            <Icon name="warning" size={14} className="mt-px shrink-0" />
            <span className="min-w-0 flex-1 leading-relaxed">{now ?? (h?.error ? t("inbox-inboxevent.failed_with", { error: h.error }) : t("inbox-inboxevent.failed"))}</span>
          </div>
          {actions}
        </div>
      ) : word ? (
        <div className="flex min-w-0 flex-col gap-2 text-[12.5px] text-ink-2 sm:flex-row sm:items-center" data-event-handling={h?.state ?? (done ? "done" : undefined)} data-event-now={now === undefined ? undefined : item.now}>
          <div className="flex min-w-0 items-center gap-2 sm:flex-1">
            {done ? <Icon name="check" size={13} stroke={2.4} className="shrink-0 text-green" /> : h?.state === "waiting" ? <Icon name="clock" size={13} className="shrink-0 text-ink-3" /> : working ? <Spinner size={12} /> : null}
            <span className={cx("min-w-0 flex-1", now === undefined && "truncate")}>{word}</span>
          </div>
          {actions}
        </div>
      ) : (done && open) || action ? <div className="flex justify-end gap-2">{open}{action}</div> : null}
      {showSay && (
        <div className="flex flex-col gap-2" data-event-say>
          <label className="flex h-8 items-center gap-2 rounded-control bg-field px-2.5 focus-within:shadow-[inset_0_0_0_1px_var(--accent)]">
            <Icon name="chat" size={13} className="shrink-0 text-ink-3" />
            <input
              value={say}
              onChange={(e) => setSay(e.target.value)}
              disabled={acting}
              placeholder={t("inbox-inboxevent.say_placeholder")}
              aria-label={t("inbox-inboxdetail.say_something")}
              className="min-w-0 flex-1 bg-transparent text-[12.5px] text-ink outline-none placeholder:text-ink-3"
              data-event-say-input
              {...ime.handlers}
              onBlur={ime.onBlur}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !ime.busy(e.nativeEvent) && say.trim() && !acting && !inFlightRef.current) {
                  e.preventDefault();
                  void instruct(say);
                }
              }}
            />
          </label>
          <div className="flex flex-wrap justify-end gap-2">
            <Button tone="quiet" size="sm" disabled={acting} onClick={() => { if (inFlightRef.current) return; setSayOpen(false); setSay(""); }}>{t("common.cancel")}</Button>
            <Button tone="accent" size="sm" icon="spark" disabled={!say.trim() || acting} busy={acting} onClick={() => void instruct(say)}>{t("inbox-inboxdetail.say_something")}</Button>
          </div>
        </div>
      )}
    </div>
  );
}

/**
 * The suggestion and its buttons: reply "Send" · "Edit" · "Reject"; agent "Start" · "Edit" · "Reject"; conclusion
 * "Got it" · "Reject"; one that needs judgment (handback) "Confirm" · "Edit" · "Reject". "Edit" opens the text to edit
 * ("Confirm" runs the edited one) and "Say something", which goes
 * back to the processing conversation for a new suggestion instead.
 */
export function SuggestionCard({ item, suggestion: s }: { item: InboxItem; suggestion: InboxSuggestion }) {
  const cardKey = `${itemKey(item)}@${s.version}`;
  if (s.kind === "todo") return <TodoSuggestion key={cardKey} item={item} suggestion={s} />;
  return <TextSuggestion key={cardKey} item={item} suggestion={s} />;
}

/**
 * A to-do suggestion (#6480): the title and the due day (and time) are fields right in the card, so one tap on "Add to to-dos" takes
 * them as they stand; a day alone is all day.
 */
function TodoSuggestion({ item, suggestion: s }: { item: InboxItem; suggestion: InboxSuggestion }) {
  const busy = useInbox((st) => st.sending.includes(itemKey(item)));
  const [title, setTitle] = useState(s.text);
  const [{ day: d0, time: t0 }] = useState(() => dueParts(s.due));
  const [day, setDay] = useState(d0);
  // All day is a switch: on, no time is asked; off, a time is (9:00 to start with)
  const [allDay, setAllDay] = useState(!t0);
  const [time, setTime] = useState(t0 || "09:00");
  const [acting, setActing] = useState<"" | "confirm" | "reject">("");
  const inFlightRef = useRef(false);
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);
  const act = async (action: "confirm" | "reject") => {
    if (busy || inFlightRef.current || (action === "confirm" && !title.trim())) return;
    inFlightRef.current = true;
    setActing(action);
    try {
      await suggestionAct(item, action, action === "confirm" && title.trim() !== s.text ? title.trim() : undefined, action === "confirm" ? dueOf(day, allDay ? "" : time) : undefined);
    } finally {
      inFlightRef.current = false;
      if (mountedRef.current) setActing("");
    }
  };
  const field = "h-8 rounded-control bg-field px-2.5 text-meta text-ink outline-none focus:shadow-[inset_0_0_0_1px_var(--accent)]";
  return (
    <section className="rounded-[14px] bg-surface p-3.5 shadow-card" data-suggestion="todo">
      <p className="flex items-center gap-1.5 text-submeta font-medium whitespace-nowrap text-accent-ink"><span><Spark />{suggestionTitle(s)}</span></p>
      <input value={title} onChange={(e) => setTitle(e.target.value)} aria-label={t("inbox-inboxevent.todo_title")} className={cx(field, "mt-2 w-full")} data-todo-title />
      <div className="mt-2 flex flex-wrap items-center gap-2">
        <input type="date" value={day} onChange={(e) => setDay(e.target.value)} aria-label={t("inbox-inboxevent.todo_day")} className={field} data-todo-day />
        {day && !allDay && <input type="time" value={time} onChange={(e) => setTime(e.target.value)} aria-label={t("inbox-inboxevent.todo_time")} className={field} data-todo-time />}
        {day && (
          <label className="flex items-center gap-2 text-meta whitespace-nowrap text-ink-2">
            <Switch on={allDay} onChange={setAllDay} label={t("inbox-inboxevent.todo_all_day")} />
            {t("inbox-inboxevent.todo_all_day")}
          </label>
        )}
      </div>
      {s.note && <p className="mt-1.5 text-label leading-relaxed text-ink-3">{s.note}</p>}
      <div className="mt-3 flex flex-wrap justify-end gap-2">
        <Button tone="quiet" disabled={busy && acting !== "reject"} busy={acting === "reject"} onClick={() => void act("reject")}>{t("inbox-inboxevent.reject")}</Button>
        <Button tone="accent" icon="check" disabled={!title.trim() || (busy && acting !== "confirm")} busy={acting === "confirm"} onClick={() => void act("confirm")}>{t("lib-inbox-events.add_todo")}</Button>
      </div>
    </section>
  );
}

/** A suggestion's due as the card's fields: the local day and, for a moment, its time ("" for all day or none). */
export function dueParts(due?: string): { day: string; time: string } {
  if (!due) return { day: "", time: "" };
  if (/^\d{4}-\d{2}-\d{2}$/.test(due)) return { day: due, time: "" };
  const d = new Date(due);
  if (Number.isNaN(d.getTime())) return { day: "", time: "" };
  const p = (n: number) => String(n).padStart(2, "0");
  return { day: `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`, time: `${p(d.getHours())}:${p(d.getMinutes())}` };
}

/** The request's due from the card's fields: a day alone is all day (2006-01-02), with a time a moment (RFC 3339); no day: none. */
export function dueOf(day: string, time: string): string | undefined {
  if (!day) return undefined;
  if (!time) return day;
  const d = new Date(`${day}T${time}`);
  return Number.isNaN(d.getTime()) ? day : d.toISOString();
}

function TextSuggestion({ item, suggestion: s }: { item: InboxItem; suggestion: InboxSuggestion }) {
  const busy = useInbox((st) => st.sending.includes(itemKey(item)));
  const uncertain = useInbox((st) => st.uncertainReplies[uncertainReplyKey(item, s.version)]);
  const attempt = s.kind === "reply" ? uncertain ?? item.suggestion_attempts?.find((a) => a.version === s.version) : undefined;
  const readOnly = useItemReadOnly(item);
  const replyReadOnly = s.kind === "reply" && readOnly;
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState(s.text);
  const [say, setSay] = useState("");
  const [acting, setActing] = useState<"" | "confirm" | "reject" | "instruct">("");
  const ime = useComposition();
  const { onBlur: resetIme } = ime;
  const inFlightRef = useRef(false);
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);
  useEffect(() => {
    if (!editing) {
      setText(s.text);
      resetIme();
    }
  }, [s.text, editing, resetIme]);
  const b = suggestionButtons(s);
  const act = async (action: "confirm" | "reject" | "instruct", body?: string) => {
    if ((replyReadOnly && action !== "reject") || busy || inFlightRef.current) return;
    if (action === "instruct" && !body?.trim()) return;
    inFlightRef.current = true;
    setActing(action);
    try {
      const ok = await suggestionAct(item, action, body);
      if (ok && mountedRef.current) {
        setEditing(false);
        if (action === "instruct") setSay("");
      }
    } finally {
      inFlightRef.current = false;
      if (mountedRef.current) setActing("");
    }
  };
  const edited = text.trim() && text !== s.text ? text : undefined;
  if (attempt) {
    const retryable = !replyReadOnly && attempt.status === "failed" && attempt.retryable && !isDone(item) && item.suggestion?.version === s.version;
    const label = attempt.status === "failed" ? "failed" : attempt.status === "sending" ? "sending" : attempt.status === "sent" ? "sent" : "uncertain";
    return (
      <section className="rounded-[14px] bg-surface p-3.5 shadow-card" data-suggestion={s.kind} data-delivery={attempt.status}>
        <p className={cx("text-submeta font-medium", label === "failed" ? "text-red" : "text-ink-2")}>{t(`assistant-handoffcard.${label}`)}</p>
        <ReplyRichText text={attempt.text} className="mt-1.5 text-meta leading-relaxed text-ink" dataSuggestionText />
        <BoundMentions text={attempt.text} mentions={attempt.mentions ?? s.mentions} sent={attempt.status === "sent"} className="mt-1.5" />
        {Number.isFinite(Date.parse(attempt.at)) && <time dateTime={attempt.at} className="mt-1 block text-submeta text-ink-3">{chainTime(attempt.at)}</time>}
        {label !== "sent" && <p role="status" className="mt-2 text-meta text-ink-2">
          {t(`assistant-handoffcard.${label === "failed" ? retryable ? "retry_hint" : "failed_hint" : label + "_hint"}`)}
          {retryable && attempt.retry_after ? " " + t("assistant-handoffcard.retry_after", { seconds: Math.ceil(attempt.retry_after) }) : ""}
        </p>}
        {retryable && <div className="mt-3 flex justify-end">
          <Button tone="accent" className="min-h-11" disabled={busy} busy={acting === "confirm"} onClick={() => void act("confirm")} testId="suggestion-retry">{t("assistant-handoffcard.retry")}</Button>
        </div>}
      </section>
    );
  }
  return (
    <section className="rounded-[14px] bg-surface p-3.5 shadow-card" data-suggestion={s.kind} data-handback={s.handback ? "" : undefined}>
      <p className="flex items-center gap-1.5 text-submeta font-medium text-accent-ink">
        <span className="min-w-0 wrap-anywhere"><Spark /><SuggestionLabel item={item} suggestion={s} /></span>
        {s.handback && <span className="text-ink-3">· {t("inbox-inboxevent.handback")}</span>}
      </p>
      {editing ? (
        <div className="mt-2 flex flex-col gap-2">
          <textarea
            value={text}
            onChange={(e) => setText(e.target.value)}
            disabled={busy}
            aria-label={t("inbox-inboxevent.edit_text")}
            rows={Math.min(8, Math.max(3, text.split("\n").length + 1))}
            className="w-full resize-y rounded-control bg-field px-2.5 py-2 text-meta leading-relaxed text-ink outline-none focus:shadow-[inset_0_0_0_1px_var(--accent)]"
            data-suggestion-editor
          />
          <BoundMentions text={text} mentions={s.mentions} />
          {!replyReadOnly && (
            <label className="flex h-8 items-center gap-2 rounded-control bg-field px-2.5 focus-within:shadow-[inset_0_0_0_1px_var(--accent)]">
              <Icon name="chat" size={13} className="shrink-0 text-ink-3" />
              <input
                value={say}
                onChange={(e) => setSay(e.target.value)}
                disabled={busy}
                placeholder={t("inbox-inboxevent.say_placeholder")}
                aria-label={t("inbox-inboxevent.say")}
                className="min-w-0 flex-1 bg-transparent text-[12.5px] text-ink outline-none placeholder:text-ink-3"
                data-suggestion-say
                {...ime.handlers}
                onBlur={ime.onBlur}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && !ime.busy(e.nativeEvent) && say.trim() && !replyReadOnly && !busy && !inFlightRef.current) {
                    e.preventDefault();
                    void act("instruct", say.trim());
                  }
                }}
              />
            </label>
          )}
          <div className="flex flex-wrap justify-end gap-2">
            <Button tone="quiet" disabled={busy} onClick={() => { if (inFlightRef.current) return; setEditing(false); setText(s.text); setSay(""); }}>{t("common.cancel")}</Button>
            {!replyReadOnly && (
              <>
                <Button tone={say.trim() ? "accent" : "secondary"} icon="spark" disabled={!say.trim() || (busy && acting !== "instruct")} busy={acting === "instruct"} onClick={() => void act("instruct", say.trim())}>
                  {t("inbox-inboxevent.say")}
                </Button>
                <Button tone={say.trim() ? "secondary" : "accent"} icon={s.kind === "reply" && !s.handback ? "send" : "check"} disabled={!text.trim() || (busy && acting !== "confirm")} busy={acting === "confirm"} onClick={() => void act("confirm", edited)}>
                  {b.confirm}
                </Button>
              </>
            )}
          </div>
        </div>
      ) : (
        <>
          <ReplyRichText text={s.text} className="mt-1.5 text-meta leading-relaxed text-ink" dataSuggestionText />
          <BoundMentions text={s.text} mentions={s.mentions} className="mt-1.5" />
          {(s.changes ?? []).map((c) => (
            <p key={`${c.ref}|${c.field}`} className="mt-1 flex items-center gap-1.5 truncate text-submeta text-ink-2">
              <Icon name="chevronRight" size={11} stroke={2.4} className="shrink-0 text-ink-3" />
              <span className="truncate">{docChangeLine(c, item.doc)}</span>
            </p>
          ))}
          {s.note && <p className="mt-1.5 text-label leading-relaxed text-ink-3">{s.note}</p>}
          <div className="mt-3 flex flex-wrap justify-end gap-2">
            <Button tone="quiet" disabled={busy && acting !== "reject"} busy={acting === "reject"} onClick={() => void act("reject")}>{t("inbox-inboxevent.reject")}</Button>
            {!replyReadOnly && b.edit && <Button tone="secondary" icon="pencil" disabled={busy} onClick={() => { if (inFlightRef.current) return; setText(s.text); setEditing(true); }}>{t("inbox-inboxevent.edit")}</Button>}
            {!replyReadOnly && (
              <Button tone="accent" icon={s.kind === "reply" && !s.handback ? "send" : "check"} disabled={busy && acting !== "confirm"} busy={acting === "confirm"} onClick={() => void act("confirm")}>
                {b.confirm}
              </Button>
            )}
          </div>
        </>
      )}
    </section>
  );
}

/** The shared icon meaning of a chain step (spec/fixtures/inbox/timeline.json), kept in the lazy detail chunk. */
export function chainIconMeaning(e: Pick<ChainEntry, "kind" | "via">): string {
  if (e.kind === "reported") return "reported";
  if (e.via === "assistant") return "persona";
  if (e.via === "agent") return "agents";
  if (e.via === "user") return "person";
  if (e.via) return "source";
  const kinds: Record<string, string> = {
    received: "received",
    triaged: "triaged",
    handling: "persona",
    sent: "sendMail",
    agent: "agents",
    agent_stopped: "agents",
    confirmed: "success",
    rejected: "rejected",
    instructed: "instructed",
    self_replied: "reply",
    failed: "error",
    concluded: "concluded",
    done: "done",
  };
  return kinds[e.kind] ?? "info";
}

const CHAIN_ICON: Record<string, { icon?: IconName; glyph?: "reply" | "question" }> = {
  received: { icon: "inbox" },
  triaged: { icon: "filter" },
  persona: { icon: "spark" },
  sendMail: { icon: "send" },
  agents: { icon: "code" },
  success: { icon: "check" },
  rejected: { icon: "x" },
  instructed: { icon: "chat" },
  reply: { glyph: "reply" },
  error: { icon: "warning" },
  done: { icon: "check" },
  concluded: { icon: "read" },
  person: { icon: "person" },
  reported: { icon: "chat" },
  info: { icon: "info" },
};

function ChainIcon({ item, e }: { item: InboxItem; e: ChainEntry }) {
  const meaning = chainIconMeaning(e);
  const source = e.via || item.source;
  const brand = meaning === "source" ? connectorBrand(source) : undefined;
  const c = CHAIN_ICON[meaning] ?? CHAIN_ICON.info;
  // Reported owns the chat-circle glyph; an unbranded source keeps the generic link so the two are not the same bubble.
  const glyph = brand ? `brand:${brand}` : meaning === "source" ? (source === "mail" ? "mail" : "link") : (c.glyph ?? c.icon);
  const tone = meaning === "error" ? "text-red" : meaning === "persona" || meaning === "concluded" ? "text-accent" : "text-ink-3";
  return (
    <span className={cx("flex size-5 shrink-0 items-center justify-center rounded-full bg-field", tone)} aria-hidden data-chain-icon={meaning} data-chain-glyph={glyph}>
      {brand ? <BrandMark brand={brand} size={18} fit="tile" /> : meaning === "source" ? <Icon name={source === "mail" ? "mail" : "link"} size={11} stroke={2.2} /> : c.glyph ? <Glyph name={c.glyph} size={11} stroke={2.2} /> : <Icon name={c.icon!} size={11} stroke={2.2} />}
    </span>
  );
}

/** The Agent session a step links, or none for the processing conversation and non-Agent steps. */
function agentSession(e: ChainEntry): { key: string; machine: string; session: string } | undefined {
  if (e.kind !== "agent" && e.kind !== "agent_stopped") return undefined;
  const link = e.link as Record<string, unknown> | undefined;
  if (link?.type !== "session" || typeof link.machine_id !== "string" || typeof link.session_id !== "string") return undefined;
  return { key: `${link.machine_id}\u0000${link.session_id}`, machine: link.machine_id, session: link.session_id };
}

/** The handling chain: folded to the three newest entries, "Show all (N)" shows them all, oldest first. */
export function Chain({ item, onSource }: { item: InboxItem; onSource: (at: SourceAt) => void }) {
  const view = useInboxView();
  const key = itemKey(item);
  const revision = view.revision;
  const [open, setOpen] = useState(() => view.chainOpen(key));
  const [seen, setSeen] = useState({ key, revision });
  if (seen.key !== key || seen.revision !== revision) {
    setSeen({ key, revision });
    setOpen(view.chainOpen(key));
  }
  useEffect(() => view.setChainOpen(key, open), [view, key, open]);
  const assistant = useStore((s) => s.assistant);
  const sessions = useStore((s) => s.sessions);
  const summaries = useStore((s) => s.sessionSummaries);
  const all = item.chain ?? [];
  if (!all.length) return null;
  const rows = chainRows(all, open);
  const latestAgent = new Map<string, string>();
  for (const e of all) {
    const link = agentSession(e);
    if (link) latestAgent.set(link.key, e.id);
  }
  const conversation = (e: ChainEntry, mid: string, sid: string): Route => {
    const h = item.handling;
    // A handling entry without detail records an Assistant taking over. A suggestion with detail can come from an Agent.
    if (e.kind === "handling" && (!e.detail || (h?.machine_id === mid && h.session_id === sid))) {
      return inboxSessionRoute(mid, sid, item.manager, assistantHandlingFallback(item, mid, sid));
    }
    const row = sessions[mid]?.find((s) => s.session_id === sid) ?? summaries.find((s) => s.machine_id === mid && s.session_id === sid);
    return conversationRoute(mid, sid, sessions, assistant, row);
  };
  const tap = (x: ChainTap) => {
    if (x.kind === "source") {
      if (x.url) {
        openLink(x.url, undefined, item.manager);
        return;
      }
      onSource({ position: x.position, uid: x.uid });
    } else if (x.kind === "route") openFromInbox(x.route);
  };
  const agentState = (e: ChainEntry) => {
    const link = agentSession(e);
    if (!link || latestAgent.get(link.key) !== e.id) return undefined;
    const session = sessions[link.machine]?.find((s) => s.session_id === link.session);
    return session ? stateName(activityOf(session)) : undefined;
  };
  return (
    <section data-event-chain>
      <div className="flex items-center gap-2 pb-1">
        <p className="min-w-0 flex-1 text-submeta font-medium whitespace-nowrap text-ink-3">{t("inbox-inboxevent.chain")}</p>
        {all.length > 3 && (
          <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="h-6 shrink-0 rounded-[6px] px-1.5 text-submeta font-medium whitespace-nowrap text-ink-3 transition-colors hover:bg-hover hover:text-ink" data-chain-toggle>
            {open ? t("inbox-inboxevent.chain_collapse") : t("inbox-inboxevent.chain_expand", { count: all.length })}
          </button>
        )}
      </div>
      <ol className="flex flex-col">
        {rows.map((e) => <ChainRow key={e.id} item={item} e={e} tap={chainTap(e, (mid, sid) => conversation(e, mid, sid), otherManager(item.manager))} onTap={tap} agentState={agentState(e)} />)}
      </ol>
    </section>
  );
}

function ChainRow({ item, e, tap, onTap, agentState }: { item: InboxItem; e: ChainEntry; tap: ChainTap; onTap: (x: ChainTap) => void; agentState?: string }) {
  const [open, setOpen] = useState(false);
  const expandable = canExpand(e);
  const goes = tap.kind === "source" || tap.kind === "route";
  const reported = e.kind === "reported";
  const text = reported ? (e.detail?.trim() || e.text) : e.text;
  const press = () => (goes ? onTap(tap) : expandable && setOpen((o) => !o));
  return (
    <li data-chain-entry={e.kind} data-chain-id={e.id} data-chain-reported={reported ? "" : undefined}>
      <div className="flex min-w-0 items-center gap-1">
        <button
          type="button"
          onClick={press}
          disabled={!goes && !expandable}
          aria-expanded={!goes && expandable ? open : undefined}
          className={cx("flex min-w-0 flex-1 items-center gap-2 rounded-md px-1.5 py-1.5 text-left text-[12.5px]", goes || expandable ? cx("hover:bg-field", reported ? "text-ink-2" : "text-ink") : cx("cursor-default", reported ? "text-ink-2" : "text-ink"))}
        >
          <span className="min-w-[34px] shrink-0 text-submeta whitespace-nowrap text-ink-3 tabular-nums">{chainTime(e.at)}</span>
          <ChainIcon item={item} e={e} />
          {reported && <Tag>{t("inbox-inboxdetail.assistant_says")}</Tag>}
          <span className="min-w-0 flex-1 truncate">{text}</span>
          {agentState && <span className="shrink-0 text-submeta whitespace-nowrap text-ink-3" data-chain-agent-state>{agentState}</span>}
          {goes && <Icon name="chevronRight" size={11} stroke={2.4} className="shrink-0 text-ink-3" />}
          {!goes && expandable && <Icon name="chevronRight" size={11} stroke={2.4} className="shrink-0 text-ink-3 transition-transform duration-200" style={{ transform: open ? "rotate(90deg)" : undefined }} />}
        </button>
        {/* an entry that goes somewhere and also has words to read: those open beside it; an assistant row's received
            message is its text, drawn in full above (#9041): that entry only goes to the conversation, one arrow (#9930) */}
        {goes && expandable && !(item.source === "assistant" && e.kind === "received") && (
          <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} aria-label={open ? t("inbox-inboxevent.entry_collapse") : t("inbox-inboxevent.entry_expand")} title={open ? t("inbox-inboxevent.entry_collapse") : t("inbox-inboxevent.entry_expand")} className="touch-hit flex size-7 shrink-0 items-center justify-center rounded-[8px] text-ink-3 transition-colors hover:bg-hover hover:text-ink">
            <Icon name="chevron" size={12} stroke={2.4} className="transition-transform duration-200" style={{ transform: open ? "rotate(180deg)" : undefined }} />
          </button>
        )}
      </div>
      {open && (
        <div className="mx-1.5 mb-1 flex flex-col gap-1 rounded-[10px] bg-inset px-3 py-2 text-[12.5px] leading-relaxed text-ink-2" data-chain-detail>
          {e.detail && <p className="whitespace-pre-wrap">{e.detail}</p>}
          {(e.messages ?? []).map((m, i) => (
            // an assistant row's message: its whole text is above (#9041), so three lines say which
            <p key={i} className={cx("min-w-0", item.source === "assistant" && "line-clamp-3")}>
              {m.sender && <span className="font-medium text-ink">{t("inbox-inboxevent.message_sender", { sender: m.sender })}</span>}
              {/* an older assistant row's message is its Markdown run into one line: said without the marks (#9041) */}
              <span className="whitespace-pre-wrap">{markdownInSummary(item) ? withoutMarks(m.text) : m.text}</span>
              {m.at && <span className="ml-1.5 text-[11px] text-ink-3 tabular-nums">{chainTime(m.at)}</span>}
            </p>
          ))}
        </div>
      )}
    </li>
  );
}

/** "Related": other items this one touches; each goes its own way, a tap opens it. */
function Related({ item }: { item: InboxItem }) {
  const list = item.related ?? [];
  if (!list.length) return null;
  return (
    <section data-event-related>
      <p className="pb-1 text-submeta font-medium text-ink-3">{t("inbox-inboxevent.related")}</p>
      <ul className="flex flex-col gap-0.5">
        {list.map((r) => (
          <li key={r.id}>
            <button type="button" onClick={() => openFromInbox(itemRoute({ id: r.id, manager: item.manager }))} className="flex w-full min-w-0 items-center gap-2 rounded-md px-1.5 py-1 text-left text-[12.5px] whitespace-nowrap text-ink hover:bg-field">
              <Icon name={r.source === "mail" ? "mail" : "link"} size={12} className="shrink-0 text-ink-3" />
              <span className="truncate">{r.title}</span>
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}
