import { type CSSProperties, Fragment, lazy, memo, type ReactNode, Suspense, useContext, useLayoutEffect, useMemo, useRef, useState } from "react";
import { flushSync } from "react-dom";
import { clock } from "../../lib/format";
import type { stripCloseout as stripCloseoutText } from "../../lib/closeout-text";
import { useCloseoutReader } from "./closeout-chunks";
import { getSession, getState, useStore } from "../../lib/store";
import { withChildSessions } from "../../lib/child-cards";
import { agentStatus, isNestedSubagentStep, nestedSubagentStatus, nestingKey, subagentFor, withNestedSubagents } from "../../lib/tasks";
import { turnImages } from "../../lib/turn-images";
import { turnToolViews } from "../../lib/turn-tool-views";
import { exchangeSteps, segmentIdentity, isLoopTick, isOwnMessage, toolChip, type Context, type Segment, type UserMessage } from "../../lib/turns";
import { usageKey } from "../../lib/turn-usage";
import type { Item } from "../../lib/types";
import { ENGINE_SWITCH_KIND } from "../../lib/engine-switch";
import { planCardStep, workBlocks, type ToolStep, type WorkNode } from "../../lib/work-groups";
import { NO_WORKFLOW_CALLS } from "../../lib/workflows";
import { channelOf, useChannels } from "../../lib/channel-source";
import { splitQuotes } from "../../lib/drafts";
import { EASE } from "../../ui/atoms";
import { ChannelMark, OriginMark } from "../../ui/ChannelMark";
import { openerFrom, type ExternalOrigin } from "../../lib/conversation-origins";
import { Markdown } from "../../ui/Markdown";
import { CardConversation, CardItem } from "../../ui/AssistantCard";
import { cardReply } from "../../lib/assistant-blocks";
import { InjectedHint, LoopTick, MergedBubble, PeerMessage } from "./Messages";
import { ModelChangeDivider } from "./ModelChangeDivider";
import { EngineSwitchDivider } from "./EngineSwitchDivider";
import { SourceMessage } from "./SourceMessage";
import { ContextGroup } from "./ContextGroup";
import type { InlineBubbleContent } from "./InlineUserBubble";
import { ForwardBlock, ForwardCards } from "./ForwardCard";
import { AttachedPhotos } from "./AttachedPhotos";
import { useFullText } from "./FullText";
import { LazyFileCards, LazyReplyFiles } from "./LazyFileCards";
import { LazyReplyPorts, LazyTurnChanges, LazyTurnImages } from "./LazyTurnExtras";
import { MessageActions } from "./MessageActions";
import { skillEditText } from "../../lib/skill-mentions";
import { contextXML } from "../../lib/context-xml";
import type { ContextRef } from "../../lib/links";
import { useIsLatestMessage } from "./latest-message-time";
import { QuoteStack } from "./Quote";
import { PathText } from "../../ui/PathLinks";
import { Thumb } from "./Steps";
import { useFoldScroll } from "./fold-scroll";
import { useChatContext } from "./chat-context";
import { useFreshWords } from "./fresh-words";
import { Icon } from "../../ui/icons";
import { WorkGroup, WorkNodes, type WorkProps } from "./WorkGroup";
import { turnChanges, turnChangesHeight } from "../../lib/turn-changes";
import type { Step } from "../../lib/turns";
import { useCallSeeker } from "./jump";
import { t } from "../../i18n";
import { useMessageFeedback } from "../../lib/message-send";
import { segmentItemIds } from "../../lib/chat-search";
import { useReveal } from "./search-reveal";

// a finished reply's Sources row (#9132): its chunk loads only for a turn that looked something up on the web
const TurnSources = lazy(() => import("./TurnSources").then((m) => ({ default: m.TurnSources })));
// botmux sessions as in Lark: only a botmux session's chat needs them
const BotmuxPart = lazy(() => import("./BotmuxMessages"));
// Out-of-band tool views (UI v12 route maps, etc.): lazy-loaded only when a turn carries Fold.tool_views or tool_result.tool_views
const ToolViews = lazy(() => import("./ToolViews").then((m) => ({ default: m.ToolViews })));
const InlineUserBubble = lazy(() => import("./InlineUserBubble").catch(() => ({ default: ({ render }: { render: () => ReactNode }) => render() })));

/** A message longer than this many lines folds to ten of them (FOLDED), with "Expand" under it (the reference client's UserMessage). */
export const BUBBLE_MAX_LINES = 14;
/** Before it is measured: about this many characters to a line of the 488 px bubble. */
const CHARS_PER_LINE = 30;
/** A folded message: ten lines high, scrolling inside (the chat scrolls on at its top and end). */
const FOLDED = "max-h-[10lh] overflow-y-auto";
/** Characters to a line of a bubble narrower than any drawn (a phone's holds about 17 wide ones): a message within
 *  BUBBLE_MAX_LINES even there is short in any bubble, and is not measured. */
const NARROWEST_CHARS = 10;

/** How many lines a message takes in its bubble, by its text (the bubble measures itself once drawn). */
export function bubbleLines(text: string, perLine = CHARS_PER_LINE): number {
  return text.split("\n").reduce((n, l) => n + Math.max(1, Math.ceil([...l].length / perLine)), 0);
}

/** ModelChangeDivider's height as drawn (Chrome, 2026-11-12): its 8 px above and below, the chip's 4 px padding, 1 px
 *  border and 16.5 px line (11 px text, the inherited 1.5). The exchange's 14 px gap comes on top. */
export const MODEL_DIVIDER_HEIGHT = 42.5;
/** EngineSwitchDivider's height folded: the model change divider's row (the same 8 px above and below, and a chip of the
 *  same padding, border and 16.5 px line; its 12 px icons are shorter than the line). The handoff opened adds its own
 *  once drawn. */
export const ENGINE_DIVIDER_HEIGHT = MODEL_DIVIDER_HEIGHT;
/** Compact tool view card height estimate before a deferred turn mounts. */
export const TOOL_VIEW_HEIGHT = 244;
/** A promoted relay's collapsed source row before it mounts: card padding, source title, disclosure and their gaps. */
const PROMOTED_RELAY_SOURCE_HEIGHT = 72;

/** A finished exchange's height in px before it is first drawn (content-visibility, docs/web/web.md "Long chats"), by
 *  its text the way bubbleLines counts it: its bubble (folded past BUBBLE_MAX_LINES), its words at 2.5 times the
 *  bubble's characters to a line (the column is wider, and Markdown's short and blank lines take less than a line), a
 *  row for each other block, and the model change divider when it has one (`modelChange`), the engine switch divider
 *  folded likewise (`engineSwitch`). Once drawn it keeps its own height. */
export function segmentHeight(user: UserMessage | undefined, nodes: WorkNode[], modelChange = false, engineSwitch = false, toolViews = 0): number {
  // gap: the exchange's blocks stand 14 px apart (`gap-3.5`, cindy's msg-stream-items)
  const line = 24, gap = 14, row = 28;
  const lines = user && !user.promotedRelay ? bubbleLines(user.text) : 0;
  let h = user ? (user.promotedRelay ? PROMOTED_RELAY_SOURCE_HEIGHT : (lines > BUBBLE_MAX_LINES ? 10 : lines) * line + 20 + (user.images.length ? 102 : 0)) : 0;
  for (const n of nodes) h += n.kind === "step" && n.step.kind === "text" ? bubbleLines(n.step.item.text ?? "", CHARS_PER_LINE * 2.5) * line : row;
  if (modelChange) h += MODEL_DIVIDER_HEIGHT;
  if (engineSwitch) h += ENGINE_DIVIDER_HEIGHT;
  if (toolViews > 0) h += toolViews * TOOL_VIEW_HEIGHT + Math.max(0, toolViews - 1) * 8;
  return Math.round(h + gap * Math.max(0, nodes.length + (user ? 1 : 0) + (modelChange ? 1 : 0) + (engineSwitch ? 1 : 0) + (toolViews > 0 ? 1 : 0) - 1));
}

/** `sendState`: a message not in the transcript yet (a sending bubble, the outbox's), marked for the sync scenarios
 *  (spec/fixtures/sync); the transcript's own bubbles have none. `starting`: the first message of a conversation still
 *  starting (the new-conversation page, #1697): drawn as it will be once sent, without actions (there is nothing yet to
 *  act on) and without "Sending…" (the page says it is preparing). */
/** `onDelete`: a message only on this device (a failed send): its menu ends in Delete, and it has no Rewind (it is not in
 *  the transcript to go back to). */
export function UserBubble({ msg, mid, sid, pending, sendState, starting, onDelete }: { msg: UserMessage; mid: string; sid: string; pending?: boolean; sendState?: "sending" | "sent" | "failed"; starting?: boolean; onDelete?: () => void }) {
  const own = isOwnMessage(msg);
  const feedback = useMessageFeedback(mid, sid, msg.client_msg_id ?? msg.id?.replace(/^(pending:|outbox:)/, "") ?? "");
  // the chat's last message keeps its time on screen under the bubble (#5285); a hover shows the others' beside it
  const pinned = useIsLatestMessage(msg.ts) && !pending && !starting;
  // the message an extension opened the conversation with (#3758)
  const { opening } = useChatContext();
  const openedBy = opening && opening.id === msg.id ? opening : undefined;
  // forwards (#3899) are cards at the top of the bubble (several at once, #3838: ForwardCards); the other contexts fold in
  // the row above it
  const forwards = msg.contexts.filter((c) => c.kind === "forward");
  const contexts = forwards.length ? msg.contexts.filter((c) => c.kind !== "forward") : msg.contexts;
  const fromCard = useContext(CardConversation) ? cardReply(msg.text) : undefined;
  const whole = fromCard?.text ?? msg.text;
  const split = useMemo(() => splitQuotes(whole), [whole]);
  const text = split ? split.rest : whole;
  const render = (inline?: InlineBubbleContent) => {
    const remaining = inline?.remaining ?? contexts;
    const bubble = <BubbleText text={text} quotes={split?.quotes} forwards={forwards} blocks={msg.contexts} fromCard={fromCard?.view}
      ts={pending || starting ? undefined : msg.ts} mid={mid} sid={sid} own={own} pending={pending && !starting} inline={inline} />;
    return (
      <div className="ml-auto flex max-w-[min(100%,488px)] min-w-0 flex-col items-end gap-1.5 pl-[12%]" style={pending ? { animation: `fade-up 300ms ${EASE} both` } : undefined} data-user-bubble data-source-fingerprint={msg.rawInput?.[0]?.source_fingerprint} data-send-state={sendState} data-items={pending || starting || sendState ? undefined : [msg.id, ...msg.images.map((i) => i.id)].join(" ")}>
        {msg.images.length > 0 && (
          <div className="flex flex-wrap justify-end gap-1.5">
            {msg.images.map((im) => (
              <Thumb key={im.id} item={im} mid={mid} sid={sid} size={96} />
            ))}
          </div>
        )}
        {msg.photos && <AttachedPhotos paths={msg.photos} mid={mid} sid={sid} />}
        {remaining.length > 0 && <ContextGroup contexts={remaining} />}
        {msg.files && <LazyFileCards files={msg.files} mid={mid} />}
        {(msg.text || forwards.length > 0) &&
          (starting || pending ? bubble : (
            <MessageActions text={msg.text} editText={msg.inlineTrailer
              ? `${msg.contexts.map((c) => contextXML({ ...c, kind: c.kind as ContextRef["kind"], title: c.title ?? "", url: c.url ?? "" })).join("\n")}\n\n${msg.text}${msg.inlineTrailer}`
              : skillEditText(msg.contexts, msg.text)} isUser ts={msg.ts} side messageId={onDelete ? undefined : msg.id} onDelete={onDelete}
              starSource={!onDelete && !sendState ? { mid, sid, item: msg.id, fingerprint: msg.rawInput?.[0]?.source_fingerprint, userInputs: msg.rawInput } : undefined}
              images={msg.images} photos={msg.photos} className="max-w-full">
              {bubble}
            </MessageActions>
          ))}
        {pinned && <span className="text-[11px] text-ink-3 tabular-nums" data-latest-time>{clock(msg.ts)}</span>}
        {/* a message waiting its turn says so under it; one on its way (Sending, Processing, delivery unknown) is said once,
            by the chat's status line under the last message (ChatView's data-message-feedback), not here as well */}
        {msg.queued && !pending && !starting && feedback !== "" && <span className="text-[11px] text-ink-3 whitespace-nowrap">{t("chat-segmentview.queued")}</span>}
        {!pending && !starting && <ChannelSource clientMsgId={msg.client_msg_id} openedBy={openedBy} />}
      </div>
    );
  };
  // parseUserText keeps the wire trailer separate from display text. Its offsets count back from the end,
  // so a leading quote can stay a separate card without shifting the anchors in the prose.
  return msg.inlineTrailer && contexts.length && text ? (
    <Suspense fallback={render()}><InlineUserBubble body={text + msg.inlineTrailer} contexts={contexts}
      mid={mid} cwd={getSession(mid, sid)?.cwd} render={render} /></Suspense>
  ) : render();
}

/** The bubble's words, folded to ten lines past fourteen; the fold scrolls (the wheel, a trackpad, a finger), with a
 *  fade at its bottom while there is more (fold-scroll.ts). `pending` (still being sent): dimmed, the same fold. A
 *  leading `> ` block ("Quote") is a folded quote card at its top instead of `>` lines (Quote.tsx). */
function BubbleText({ text, quotes, forwards, blocks, fromCard, ts, mid, sid, own, pending, inline }: {
  text: string; quotes?: string[]; forwards: Context[]; blocks: Context[]; fromCard?: string;
  ts?: string; mid: string; sid: string; own: boolean; pending?: boolean; inline?: InlineBubbleContent;
}) {
  const fill = own ? "bg-user-bubble" : "bg-neutral-bubble";
  const ref = useRef<HTMLDivElement>(null);
  const [long, setLong] = useState(() => bubbleLines(text) > BUBBLE_MAX_LINES);
  const [open, setOpen] = useState(false);
  const hasInline = Boolean(inline);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || open) return;
    // short however narrow: no layout read (opening a long chat read every bubble's, each a forced layout)
    if (!hasInline && bubbleLines(text, NARROWEST_CHARS) <= BUBBLE_MAX_LINES) {
      setLong(false);
      return;
    }
    // its full height in lines (a fold's scrollHeight is all of it)
    const line = parseFloat(getComputedStyle(el).lineHeight) || 24;
    setLong(el.scrollHeight / line > BUBBLE_MAX_LINES + 0.5);
  }, [text, open, hasInline]);
  const folded = long && !open;
  const fold = useFoldScroll(ref, folded, [text]);
  const words = text && (
    <div
      ref={ref}
      {...fold}
      className={`text-[length:var(--chat-body)] leading-[1.6] whitespace-pre-wrap [overflow-wrap:anywhere] ${folded ? `${FOLDED} user-bubble-fade` : ""}`}
      data-folded={folded || undefined}
      data-bubble-text
    >
      {inline?.words ?? <PathText text={text} mid={mid} cwd={getSession(mid, sid)?.cwd} />}
    </div>
  );
  return (
    <div className="flex max-w-full flex-col items-end gap-1" data-from-card={fromCard}>
      {fromCard && (
        <span className="flex items-center gap-1 text-[11px] font-medium whitespace-nowrap text-accent">
          <Icon name="check" size={11} />
          {t("ui-assistantcard.from_card")}
        </span>
      )}
      {quotes || forwards.length ? (
        <div className={`flex max-w-full flex-col gap-1.5 rounded-[12px] ${fill} px-4 py-2.5 text-ink ${pending ? "opacity-60" : ""}`} title={clock(ts)} data-hit-target>
          {forwards.length > 1 ? <ForwardCards blocks={blocks} count={forwards.length} /> : forwards.map((c, i) => <ForwardBlock key={i} c={c} />)}
          {quotes && <QuoteStack blocks={quotes} mid={mid} sid={sid} />}
          {words}
        </div>
      ) : (
        <div className={`max-w-full rounded-[12px] ${fill} px-4 py-2.5 text-ink ${pending ? "opacity-60" : ""}`} title={clock(ts)} data-hit-target>
          {words}
        </div>
      )}
      {long && (
        <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="rounded-[6px] px-1.5 py-0.5 text-[12px] font-medium whitespace-nowrap text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink-2">
          {open ? t("chat-segmentview.collapse") : t("chat-segmentview.expand")}
        </button>
      )}
      {inline?.details}
    </div>
  );
}

/** "From WeChat" under a message that came from a channel (its client_msg_id is `<channel>-…`, docs/extensions/channels.md);
 *  else, under the message an extension opened the conversation with, "From Lark /agent" / "From an extension" (#3758),
 *  its mark the opening extension's icon as on the conversation's rows and its words that extension's name while the icon
 *  is drawn (#10358). */
function ChannelSource({ clientMsgId, openedBy }: { clientMsgId?: string; openedBy?: { origin: ExternalOrigin; opened_by?: string; manager?: string } }) {
  const via = channelOf(clientMsgId, useChannels());
  // the extension whose own icon the mark draws, which the words then name (#10358)
  const [name, setName] = useState<string>();
  if (!via && openedBy) {
    return (
      <span data-opened-by={openedBy.origin} className="flex items-center gap-1.5 whitespace-nowrap text-[11px] text-ink-3">
        <OriginMark origin={openedBy.origin} size={14} decorative session={openedBy} onName={setName} />
        {openerFrom(openedBy.origin, name)}
      </span>
    );
  }
  if (!via) return null;
  return (
    <span className="flex items-center gap-1.5 whitespace-nowrap text-[11px] text-ink-3">
      <ChannelMark c={via} size={14} />
      {t("chat-segmentview.from", { name: via.name })}
    </span>
  );
}

/** An answer's prose at the chat's body size, 1.6 lines. Important: styles.css's `.prose-j` (14 / 1.7) is outside
 *  Tailwind's layers, so it would win over a plain utility. */
const ANSWER = "[&_.prose-j]:text-[length:var(--chat-body)]! [&_.prose-j]:leading-[1.6]!";

/** What the agent said, without a bubble, the chat's body size (15, 16 on a phone) / 1.6. `fade`: it just came in, its
 *  words fade in once. Still streaming, it has the same right-click / long-press menu (copy what is there so far, quote
 *  it); only a finished turn's last reply has the bar under it (`bar`, #5314).
 *  Memo'd: the chat context changes with every streamed item, so each SegmentView re-renders; a finished answer's
 *  props stay the same and it is skipped (#3024). */
export const Answer = memo(function Answer({ item, streaming, mid, sid, fade, stripCloseout, bar, cut, usage, usageMs, ttftMs }: {
  item?: Item;
  streaming?: string;
  mid?: string;
  sid?: string;
  fade?: boolean;
  /** The card above the transcript shows its closeout (#3612): drawn without the raw block. */
  stripCloseout?: typeof stripCloseoutText;
  /** The bar under it (ReplyBar): a finished turn's last reply, never narration in a work summary. */
  bar?: boolean;
  /** Its turn ended in a model error or an interruption right after these words (Segment.cut): cindy's line above them. */
  cut?: boolean;
  /** With `bar`: what its exchange used (lib/turn-usage.ts usageKey, a string so the memo holds), its work time, and streaming TTFT. */
  usage?: string;
  usageMs?: number;
  ttftMs?: number;
}) {
  // a long answer the hub cut for the page: its start, and "View full text" under it
  const live = streaming !== undefined;
  const full = useFullText(live ? undefined : item, mid, sid);
  const text = streaming ?? (stripCloseout ? stripCloseout(full.text, !item?.full_size || (Boolean(mid && sid) && !full.more)) : full.text);
  if (!text) return null;
  const cwd = mid && sid ? getSession(mid, sid)?.cwd : undefined;
  return (
    <MessageActions text={text} ts={item?.ts} itemId={item?.id} bar={bar} usage={bar ? usage : undefined} usageMs={bar ? usageMs : undefined} ttftMs={bar ? ttftMs : undefined}
      starSource={!live && mid && sid && item?.id ? { mid, sid, item: item.id, fingerprint: item.source_fingerprint } : undefined}
      translationSource={item?.full_size ? {
        key: JSON.stringify([mid, sid, item.id, item.text, item.full_size, item.source_fingerprint, Boolean(stripCloseout)]),
        load: async () => {
          const complete = await full.getText();
          return stripCloseout ? stripCloseout(complete) : complete;
        },
      } : undefined}>
      {cut && <p className="mb-1 text-[13px] leading-[1.384615] text-ink-2" data-cut-short>{t("chat-messageactions.cut_short")}</p>}
      <div className={ANSWER} data-answer={live ? "streaming" : true} data-source-fingerprint={item?.source_fingerprint} data-items={item?.id}>
        {/* the cards in it are this item's blocks (ui/AssistantCard.tsx): `<item id>/<n>` */}
        <CardItem.Provider value={item?.id ? { id: item.id, text } : null}>
          <Markdown text={text} mid={mid} cwd={cwd} streaming={live || undefined} fade={fade || undefined} lineBreaks />
        </CardItem.Provider>
      </div>
      {full.more}
      {!live && mid && sid && <LazyReplyFiles text={text} mid={mid} cwd={cwd} />}
      {!live && mid && sid && <LazyReplyPorts text={text} mid={mid} sid={sid} cwd={cwd} />}
    </MessageActions>
  );
});

/** Freshness reaches only the single visible persisted text, not every paragraph hidden in its execution summary: it
 *  reads its own id (fresh-words.ts), so a new reply renders these words, not every exchange. `done`: its turn is over,
 *  so the words have the bar under them (cindy: never while the turn runs). */
const LatestAnswer = memo(function LatestAnswer({ item, streaming, mid, sid, noCloseout, done, cut, usage, usageMs, ttftMs }: { item?: Item; streaming?: string; mid: string; sid: string; noCloseout?: boolean; done?: boolean; cut?: boolean; usage?: string; usageMs?: number; ttftMs?: number }) {
  const persisted = streaming === undefined ? item?.id : undefined;
  const fresh = useFreshWords(mid, sid, persisted);
  const bar = Boolean(done && persisted);
  const stripCloseout = useCloseoutReader(Boolean(noCloseout));
  if (stripCloseout && item?.text?.trim() && !item.full_size && !stripCloseout(item.text)) return null;
  return <div data-latest-text><Answer item={item} streaming={streaming} mid={mid} sid={sid} fade={persisted ? fresh : undefined} stripCloseout={stripCloseout} bar={bar || undefined} cut={cut} usage={bar ? usage : undefined} usageMs={bar ? usageMs : undefined} ttftMs={bar ? ttftMs : undefined} /></div>;
});

export const SegmentView = memo(function SegmentView({
  seg,
  live,
  mid,
  sid,
  streaming,
  batchedWords,
  expandFold,
  beforeWords,
  trace,
  offscreen,
  busy,
  dockTurnClock,
}: {
  seg: Segment;
  live: boolean;
  mid: string;
  sid: string;
  streaming?: string;
  /** The live words are an agent's as Claude shows them (docs/integrations/claude-integration.md "Live replies"): a batch
   *  of lines at a time, and still while a tool runs. The live work row stays above this text slot; the headless assistant's
   *  words stream as they are written and stand for it. */
  batchedWords?: boolean;
  expandFold: (f: Item) => Promise<boolean | void>;
  /** Drawn between the work and the agent's last words (the wakes chip, #3783); above the work while it is live. */
  beforeWords?: ReactNode;
  /** An opened supervision round shows its original input instead of the injected hint. */
  trace?: boolean;
  /** A finished exchange before the last two: off screen it is not drawn (the `seg-cv` class), and the chat watches its
   *  height through this (ChatView `watchSegment`, returning the unwatch) to keep the view still when it changes. */
  offscreen?: (section: HTMLElement) => () => void;
  /** The chat's last exchange while the chat is still at work (working, a message of yours on its way or being
   *  answered): drawn finished for a moment when a send drops the live line, its words get no reply bar yet, so the
   *  bar does not come and go (#5314). */
  busy?: boolean;
  /** The dock owns the live turn's elapsed clock; finished exchanges never receive this state. */
  dockTurnClock?: boolean;
}) {
  const { subagents, tasks, scope, workflowCalls = NO_WORKFLOW_CALLS, lineStyle, closeoutSegment, children, windowHistory, windowDrawn } = useChatContext();
  const own = useMemo(() => exchangeSteps(seg), [seg]);
  // A long chat creates every section and its estimate up front, but not every rich body. Once drawn (including while it
  // was live or among the final two) an exchange stays mounted: scrolling cannot throw away a menu, focus or selection.
  const defer = offscreen && windowHistory && typeof IntersectionObserver !== "undefined";
  const revealIds = useMemo(() => segmentItemIds(seg), [seg]);
  const revealed = useReveal(scope, revealIds, () => {});
  const [near, setNear] = useState(false);
  const kept = useRef(!defer);
  if (!defer || revealed) kept.current = true;
  const mounted = kept.current || near;
  // the subagents its subagents launched, each a card of its own by when it started (#5030); a trace shows the calls only.
  // Keyed by what places them, not the list: that changes with every subagent's tokens and steps.
  const nesting = trace ? "" : nestingKey(subagents);
  // the child sessions that started during this exchange, each a card among its steps (#5120, lib/child-cards.ts)
  const placed = trace ? undefined : children?.get(seg.id);
  const steps = useMemo(
    () => withChildSessions(nesting ? withNestedSubagents(own, subagents) : own, placed),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [own, nesting, placed],
  );
  const turnStart = seg.user?.ts ?? seg.start;
  // one plan card per exchange, where its latest list was written (cindy); the other updates are rows
  const plan = useMemo(() => planCardStep(steps), [steps]);
  const p = useMemo<WorkProps>(() => {
    const agent = (s: ToolStep) =>
      isNestedSubagentStep(s) ? nestedSubagentStatus(subagentFor(subagents, s.use)) : agentStatus(s.result, subagentFor(subagents, s.use), live, s.use.tool_use_id ? tasks.notes.get(s.use.tool_use_id) : undefined);
    return { mid, sid, scope, expandFold, agentStatus: agent, segment: seg.id, workflowCalls, trace, lineStyle, turnLive: live, plan, hideClock: dockTurnClock };
  }, [mid, sid, scope, expandFold, subagents, tasks, live, seg.id, workflowCalls, trace, lineStyle, plan, dockTurnClock]);
  const identity = segmentIdentity(seg);
  const last = steps.at(-1);
  const newWords = streaming !== undefined && (last?.kind !== "text" || last.item.text?.trimEnd() !== streaming.trimEnd());
  const nodes = useMemo(
    () => workBlocks(steps, { live, turnStart, streaming: newWords, identity }),
    [steps, live, turnStart, newWords, identity],
  );
  useCallSeeker(scope, nodes);
  // the pictures the agent looked at this turn, between its work and its last words
  const images = useMemo(() => turnImages(seg, getState().machines.find((m) => m.id === mid)?.name), [seg, mid]);
  // The text slot is fixed during the run and after completion. Streamed text replaces persisted narration.
  let cut = nodes.length;
  const tail = nodes[cut - 1];
  if (tail?.kind === "step" && tail.step.kind === "text") cut--;
  const latest = nodes.at(-1);
  const latestItem = latest?.kind === "step" && latest.step.kind === "text" ? latest.step.item : undefined;
  // The card above the transcript shows this exchange's closeout (#3612): its last words are drawn without the raw
  // block, and not at all when the block was all they said.
  const noCloseout = closeoutSegment === seg.id && streaming === undefined;
  const working = live && (streaming === undefined || batchedWords) && !nodes.some((n) => n.kind === "group" && n.live);
  // When the dock bar already shows the foreground turn's Working… status and elapsed timer, skip the empty non-expandable
  // stream placeholder unless batched live text is streaming above it (#12380).
  const liveRow = working && (!dockTurnClock || streaming !== undefined) && <WorkGroup g={{ kind: "group", id: `work-live-${seg.id}`, children: [], live: true, startedAt: Date.parse(turnStart ?? "") || undefined }} p={p} />;
  const imageStrip = images.length > 0 && <LazyTurnImages images={images} mid={mid} sid={sid} cwd={getSession(mid, sid)?.cwd} />;
  const box = useRef<HTMLElement>(null);
  useLayoutEffect(() => {
    const el = box.current;
    if (!defer || mounted || !el) return;
    const nearView = new IntersectionObserver((entries) => {
      if (!entries[0]?.isIntersecting) return;
      const was = el.getBoundingClientRect().height;
      // Commit and compensate with the observer's frame: a deferred update would move the rows below first and let
      // ResizeObserver correct them a frame later. ChatView advances its baseline, so this is not applied twice.
      flushSync(() => setNear(true));
      windowDrawn?.(el, was);
    }, { root: el.closest<HTMLElement>("[data-chat-scroll]"), rootMargin: "100% 0px" });
    nearView.observe(el);
    return () => nearView.disconnect();
  }, [defer, mounted, windowDrawn]);
  useLayoutEffect(() => {
    const el = box.current;
    return offscreen && el ? offscreen(el) : undefined;
  }, [offscreen]);
  const divided = Boolean(seg.modelChange);
  // the files this finished Agent exchange changed (cindy's TurnChangesCard): its own edits, never a Persona
  // conversation's (super rules 0 and 4), never while it runs, never in a supervision trace, and never when the page
  // starts inside it (no message of the user's): edits before the page would be missing from its counts
  const counted = !live && !trace && lineStyle === "agents" && Boolean(seg.user);
  // the session's folder from the store, so the card follows it when it comes in after the transcript
  const cwd = useStore(() => (counted ? getSession(mid, sid)?.cwd : undefined));
  const changes = useMemo(() => (counted ? turnChanges(own, cwd) : null), [counted, own, cwd]);
  // the web pages the finished exchange used, in a Persona conversation and an Agent's alike (#9132)
  const sourced = !live && !trace && Boolean(seg.user) && own.some(webStep);
  // what the finished exchange used, for its reply bar (#5426): the work summary's time when it has one summary
  const usage = !live && !trace ? usageKey(seg.usage) : undefined;
  const groups = usage ? nodes.filter((n) => n.kind === "group") : [];
  const usageMs = groups.length === 1 ? groups[0].durationMs : undefined;
  const toolViews = useMemo(() => (trace ? [] : turnToolViews(own, mid, sid)), [trace, own, mid, sid]);
  const switched = Boolean(seg.engineSwitch);
  const guess = useMemo(() => (offscreen ? ({ "--seg-est": `${segmentHeight(seg.user, nodes, divided, switched, toolViews.length) + (changes ? 12 + turnChangesHeight(changes) : 0) + (sourced ? 14 + SOURCES_HEIGHT : 0)}px` } as CSSProperties) : undefined), [offscreen, seg.user, nodes, divided, switched, toolViews.length, changes, sourced]);
  // Native browser find can still land on the raw words (content-visibility keeps them searchable); the observer then
  // mounts their rich representation. In-chat search uses `revealed` above and goes straight to the exact item instead.
  // The placeholder holds the section's estimate too: otherwise Chrome caches its shorter raw-text height in
  // `contain-intrinsic-block-size: auto`, and the later rich mount starts from a different height than the old body did.
  const placeholder = useMemo(() => {
    if (mounted) return "";
    const user = trace && seg.user?.rawInput ? seg.user.rawInput.map((i) => i.text) : seg.user?.promotedRelay ? [] : [seg.user?.text, ...(seg.user?.contexts.map((c) => c.text) ?? [])];
    return [...user, ...own.flatMap((s) => s.kind === "tool" ? [toolChip(s.use), s.result?.text] : [s.item.text])].filter(Boolean).join("\n");
  }, [mounted, own, seg.user, trace]);
  return (
    <section ref={box} className={offscreen ? "seg-cv flex flex-col gap-3.5" : "flex flex-col gap-3.5"} style={guess} data-segment>
      {mounted ? <Fragment>
      {/* the first message the conversation's new engine got (#5054): the switch divider above it, the user's words below */}
      {seg.engineSwitch && <EngineSwitchDivider sw={seg.engineSwitch} id={seg.id} item={switchItem(seg)} mid={mid} sid={sid} />}
      {trace && seg.user?.rawInput && !seg.user.promptHidden && !seg.user.sourceInTrace ? <RawInjectedInput items={seg.user.rawInput} mid={mid} sid={sid} /> : seg.user && (seg.user.source ? <SourceMessage msg={seg.user} mid={mid} sid={sid} /> : seg.user.injected ? <InjectedHint msg={seg.user} sid={sid} /> : seg.user.botmux ? <Suspense fallback={null}><BotmuxPart msg={seg.user} mid={mid} sid={sid} bubble={(m) => <UserBubble msg={m} mid={mid} sid={sid} />} /></Suspense> : seg.user.parts ? <MergedBubble msg={seg.user} mid={mid} sid={sid} /> : seg.user.from ? <PeerMessage msg={seg.user} /> : isLoopTick(seg.user) ? <LoopTick msg={seg.user} /> : <UserBubble msg={seg.user} mid={mid} sid={sid} />)}
      {/* the reply below came from another model than the one before it (cindy: after the message that started it) */}
      {seg.modelChange && <ModelChangeDivider from={seg.modelChange.from} to={seg.modelChange.to} />}
      {/* Work stays collapsed above the single latest-text slot. */}
      {live && <Fragment key="before-words">{beforeWords}</Fragment>}
      <WorkNodes nodes={nodes.slice(0, cut)} p={p} live={live} fade />
      {liveRow}
      {imageStrip}
      {!trace && toolViews.length > 0 && <Suspense key="tool-views" fallback={null}><ToolViews views={toolViews} /></Suspense>}
      {!live && <Fragment key="before-words">{beforeWords}</Fragment>}
      {(latestItem || streaming !== undefined) && <Suspense key="latest-text" fallback={null}><LatestAnswer item={streaming === undefined ? latestItem : undefined} streaming={streaming} mid={mid} sid={sid} noCloseout={noCloseout || undefined} done={(!live && !busy) || undefined} cut={(!live && seg.cut) || undefined} usage={usage} usageMs={usageMs} ttftMs={!live && !trace ? seg.ttftMs : undefined} /></Suspense>}
      {own.some(botmuxStep) && <Suspense key="botmux-replies" fallback={null}><BotmuxPart steps={own} mid={mid} /></Suspense>}
      {sourced && <Suspense key="turn-sources" fallback={null}><TurnSources steps={own} /></Suspense>}
      {changes && <LazyTurnChanges key="turn-changes" changes={changes} mid={mid} sid={sid} cwd={cwd} />}
      </Fragment> : <div className="whitespace-pre-wrap" style={{ blockSize: "var(--seg-est)" }} data-segment-placeholder>{placeholder}</div>}
    </section>
  );
});

/** The Sources row's height as drawn (TurnSources.tsx: one line of chips), for an exchange's estimate. */
const SOURCES_HEIGHT = 28;

/** A step that may name web pages: a web call, or folded work that carries some (lib/turn-sources.ts reads them). */
function webStep(s: Step): boolean {
  return s.kind === "tool" ? s.use.tool_name === "WebSearch" || s.use.tool_name === "WebFetch" : s.kind === "fold" && Boolean(s.item.fold?.sources?.length);
}

/** A step that sent a reply to Lark (completed, successful `botmux send`), or folded work that holds some. */
function botmuxStep(s: Step): boolean {
  return s.kind === "tool" ? s.use.botmux?.kind === "reply" && Boolean(s.result) && !s.result?.is_error : s.kind === "fold" && Boolean(s.item.fold?.botmux?.length);
}

/** The message's transcript item that carried the switch's block (its rest is fetched from it when the hub cut it). */
function switchItem(seg: Segment): Item | undefined {
  return seg.user?.rawInput?.find((i) => i.kind === "text" && i.text?.includes(`kind="${ENGINE_SWITCH_KIND}"`));
}

function RawInjectedInput({ items, mid, sid }: { items: Item[]; mid: string; sid: string }) {
  return (
    <div className="flex min-w-0 flex-col gap-1" data-supervision-input>
      <span className="text-[length:var(--chat-secondary)] text-ink-3">{t("chat-lines.supervision_input")}</span>
      {items.map((item) => item.kind === "image" ? <Thumb key={item.id} item={item} mid={mid} sid={sid} /> : <RawInjectedText key={item.id} item={item} mid={mid} sid={sid} />)}
    </div>
  );
}

function RawInjectedText({ item, mid, sid }: { item: Item; mid: string; sid: string }) {
  const full = useFullText(item, mid, sid);
  return (
    <div data-items={item.id}>
      <pre className="rounded-card bg-inset p-3 text-[length:var(--chat-secondary)] whitespace-pre-wrap text-ink-2 [overflow-wrap:anywhere]">{full.text}</pre>
      {full.more}
    </div>
  );
}
