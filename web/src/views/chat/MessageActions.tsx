// What you can do with a message (iOS MessageMenu.swift: "Copy" / "Select" / "Quote" / "Forward" / "Edit"): every item on
// right-click; on a touch screen, a long press opens them (and "Select" shows the text to select, since the press no longer
// selects). With a mouse, a small bar on hover too: beside your own bubbles (under one that leaves it no room), and
// under a turn's last reply cindy's bar
// (ReplyBar, #5314: Copy message, Start a new session, ⋯, the time); the other replies (narration in a work summary, words
// still streaming) have the menus only, and on a touch screen the last reply has a copy button and a ⋯ under it. "Quote" puts
// it as a quote in front of the draft, "Edit" (your own messages) puts it back in the composer to change and send again.
// "Forward" (#3899) opens Forward to: Persona or any Agent, the message over that chat's box with a note to type
// (lib/forward.ts); it took the place of "Hand to Persona". "Use as context" attaches it to this chat's next message. Sharing
// as a card is the phone's. "Start a new session" (#2137: a new conversation with what was said up to this reply) is there
// when the session can branch. Beside your own bubbles, Forward and Use as context live in ⋯ to keep the hover bar compact.
// While the chat picks messages to forward at once (#3838, Forward to's Select messages…), a message with a Forward has a
// checkbox over it (Picking.tsx PickRow, which comes with Forward to): a click anywhere on it picks it or unpicks it, the
// tick before it; its menus and bars are off. Not picking, the row is drawn as it always was.
import { lazy, Suspense, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { t } from "../../i18n";
import { clock } from "../../lib/format";
import { sendDraft, type QuotedTask } from "../../lib/drafts";
import type { ForwardPick } from "../../lib/forward";
import { addContext, newContext } from "../../lib/links";
import type { Item } from "../../lib/types";
import { Button, copyText, cx, Dialog, Menu, toast, type MenuItem } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import type { RewindRestore } from "../../lib/rewind";
import { isNoHover, NO_HOVER, useMedia } from "../../ui/media-query";
import { useChatActions, type ChatActions } from "./chat-actions";
import { pickParts } from "./picking-parts";
import { useIsLatestMessage } from "./latest-message-time";

// Hidden until the pointer comes, so it is not part of the first screen (startup-preload.ts): its row is kept while it
// loads, so nothing moves when it comes.
const replyBar = () => import("./ReplyBar");
const ReplyBar = lazy(() => replyBar().then((m) => ({ default: m.ReplyBar })));
// what the exchange cost, at the end of a touch screen's row (the reply bar has it on hover)
const TurnUsage = lazy(() => replyBar().then((m) => ({ default: m.TurnUsage })));


/** The left edge of what shows `el`: the nearest box that clips it sideways (the chat's scroller), or the window's. */
function shownLeft(el: HTMLElement): number {
  for (let box = el.parentElement; box; box = box.parentElement) {
    const style = getComputedStyle(box);
    if (style.overflowX !== "visible" || /paint|strict|content/.test(style.contain)) return Math.max(0, box.getBoundingClientRect().left);
  }
  return 0;
}

/** Whether the hover bar beside your bubble (`side`: to its left, 0.375rem away) fits inside what shows the chat. */
export function sideBarFits(row: HTMLElement, bar: HTMLElement): boolean {
  const gap = 0.375 * (parseFloat(getComputedStyle(document.documentElement).fontSize) || 16);
  return row.getBoundingClientRect().left - gap - bar.offsetWidth >= shownLeft(row);
}

function pointAnchor(x: number, y: number): HTMLElement {
  const el = document.createElement("span");
  el.style.cssText = `position:fixed;left:${x}px;top:${y}px;width:0;height:0;pointer-events:none`;
  document.body.appendChild(el);
  return el;
}

/** The actions of one message; empty when there is nothing to act on. */
export interface Action extends Pick<MenuItem, "hint" | "disabled" | "danger" | "confirm" | "inline" | "children"> {
  key: string;
  label: string;
  icon: IconName;
  onSelect: () => void | Promise<unknown>;
}

function useActions(text: string, isUser: boolean, ts: string | undefined, task: QuotedTask | undefined, select: () => void, forward?: ForwardPick, editText?: string): Action[] {
  const chat = useChatActions();
  if (!text.trim()) return [];
  const out: Action[] = [{ key: "copy", label: t("chat-messageactions.copy"), icon: "copy", onSelect: () => void copyText(text) }];
  if (isNoHover()) out.push({ key: "select", label: t("chat-messageactions.select_text"), icon: "read", onSelect: select });
  if (chat?.canCompose) {
    out.push({ key: "quote", label: t("chat-messageactions.quote"), icon: "quote", onSelect: () => void sendDraft(chat.draftKey, { type: "quote", text, from: isUser ? t("chat-messageactions.me") : chat.speaker, ts, task }) });
  }
  out.push(...messageContextActions(text, chat, forward));
  if (chat?.canCompose && isUser) out.push({ key: "edit", label: t("chat-messageactions.edit"), icon: "pencil", onSelect: () => void sendDraft(chat.draftKey, { type: "replace", text: editText ?? text }) });
  return out;
}

/** The message goes on to another chat (Forward, a message in the transcript: `forward` names it), or to its own
 *  conversation's next message (Android MessageMenu). */
export function messageContextActions(text: string, chat: ChatActions | null, forward?: ForwardPick): Action[] {
  if (!chat || !text.trim()) return [];
  const title = t("chat-messageactions.context_title", { title: chat.title });
  const out: Action[] = [];
  const send = chat.forward;
  if (send && forward) out.push({ key: "forward", label: t("chat-forward.forward"), icon: "forward", onSelect: () => send(forward) });
  if (chat.canCompose) out.push({
    key: "context", label: t("chat-messageactions.use_as_context"), icon: "clip",
    onSelect: () => addContext(chat.draftKey, newContext("context", title, "", text)),
  });
  return out;
}

/** "Start a new session" (cindy's words; "Start a new chat" in a Persona conversation, `chat`), on the reply bar and at the
 *  end of a reply's right-click menu, when the session can fork and the reply is a transcript line of its own
 *  (`<uuid>#<n>`; lines without a uuid are `line<seq>`, which the node cannot find). */
export function moreActions(itemId: string | undefined, fork: ((itemId: string) => void) | undefined, chat?: boolean): Action[] {
  if (!itemId || !fork || itemId.startsWith("line") || !itemId.includes("#")) return [];
  return [{ key: "fork", label: t(chat ? "chat-messageactions.fork_chat" : "chat-messageactions.fork"), icon: "fork", onSelect: () => fork(itemId) }];
}

/** "Save as Prompt…" on a message of yours (#7329): the saved prompt editor over the page, filled with its words (a chunk of
 *  its own, settings/SavedPromptDialog). */
export function savePromptActions(text: string): Action[] {
  if (!text.trim()) return [];
  return [{
    key: "save-prompt", label: t("chat-messageactions.save_as_prompt"), icon: "book",
    onSelect: () => import("../settings/SavedPromptDialog").then(
      (m) => m.openSavedPromptDialog(text),
      (e: unknown) => toast(e instanceof Error ? e.message : String(e), "error"), // the chunk would not load
    ),
  }];
}

/** "Delete", last and in red, on a message only on this device (#4626, the apps' long press): in the right-click / long-press
 *  menu, and for your own bubbles in ⋯. */
export function deleteActions(onDelete: (() => void) | undefined): Action[] {
  return onDelete ? [{ key: "delete", label: t("chat-outboxbubbles.delete"), icon: "trash", danger: true, onSelect: onDelete }] : [];
}

type Rewind = NonNullable<ChatActions["rewind"]>;

/** "Rewind to here" (#2436) on a message of yours: "Conversation only" / "Code and conversation", the second asking again (it puts files back too). Off
 *  with the reason as its hint while the session cannot ("Claude only", "Working"…). Each choice waits for the node inside the
 *  menu (it drives Claude's own /rewind, up to a minute) and shows the failure there. */
export function rewindActions(messageId: string | undefined, text: string, rewind: { block: string; code?: boolean; run: Rewind["run"] } | undefined): Action[] {
  if (!messageId || !rewind || !text.trim()) return [];
  const go = (restore: RewindRestore) => () => rewind.run(messageId, text, restore);
  return [
    {
      key: "rewind",
      label: t("rewind.menu"),
      icon: "undo",
      hint: rewind.block || undefined,
      disabled: Boolean(rewind.block),
      onSelect: () => {},
      children: [
        { key: "rewind-conversation", label: t("rewind.conversation"), inline: true, onSelect: go("conversation") },
        ...(rewind.code === false ? [] : [{ key: "rewind-code", label: t("rewind.code_and_conversation"), danger: true, confirm: t("rewind.confirm_code"), inline: true, onSelect: go("code_and_conversation") }]),
      ],
    },
  ];
}

const NO_STORE = () => () => {};

/** `live`'s value while `open` (a menu that shows it), `closed` otherwise: a closed menu does not render again when it
 *  changes (why "Rewind to here" is off changes each time the session starts or stops working). */
function useWhileOpen(live: Rewind["block"] | undefined, open: boolean, closed: string): string {
  const on = open ? live : undefined;
  const get = on ? on.get : () => closed;
  return useSyncExternalStore(on ? on.subscribe : NO_STORE, get, get);
}

/** Wraps a message: right-click / long press opens its menu. A hover bar goes beside it (`side`, your own bubbles: they had
 *  no row under them, so the layout does not move; the time leads that bar, #5285), over its top right corner (`corner`, a
 *  task's card: no row under it either), or under it (`bar`, a finished turn's last reply: ReplyBar). Without any of those
 *  it has the menus only. `task`: "Quote" quotes it as that task. The chat's last message keeps its time on screen
 *  (#5285): a last reply's in its bar (ReplyBar `pinned`) and the touch row, your own bubble's under it (UserBubble). */
export function MessageActions({
  text,
  editText,
  isUser,
  ts,
  side,
  corner,
  bar,
  task,
  itemId,
  messageId,
  images,
  photos,
  onDelete,
  usage,
  usageMs,
  children,
  className,
}: {
  text: string;
  /** Explicit selections kept when editing or rewinding; Copy, Quote and Forward use visible words only. */
  editText?: string;
  isUser?: boolean;
  ts?: string;
  side?: boolean;
  corner?: boolean;
  bar?: boolean;
  task?: QuotedTask;
  /** The reply's transcript item: where "Start a new session" branches off, and what Forward forwards. */
  itemId?: string;
  /** Your message (UserMessage.id): where "Rewind to here" goes back to, and what Forward forwards. */
  messageId?: string;
  /** Your message's pictures, which go with a forward of it. */
  images?: Item[];
  photos?: string[];
  /** A message only on this device (a failed send): "Delete" ends its menus (#4626). */
  onDelete?: () => void;
  /** With `bar`: what the exchange used (lib/turn-usage.ts usageKey) and its work time, after the time (#5426). */
  usage?: string;
  usageMs?: number;
  children: React.ReactNode;
  className?: string;
}) {
  const [selecting, setSelecting] = useState(false);
  const touch = useMedia(NO_HOVER);
  const latest = useIsLatestMessage(ts);
  const forwarded = itemId ?? messageId;
  const pick: ForwardPick | undefined = forwarded ? { itemId: forwarded, text, isUser: Boolean(isUser), ts, images, photos } : undefined;
  const actions = useActions(text, Boolean(isUser), ts, task, () => setSelecting(true), pick, editText);
  const chat = useChatActions();
  // the chat picks messages to forward (#3838): the checkbox over this one, when it is one to forward
  const picker = chat?.forward ? chat.pick : undefined;
  const picking = Boolean(picker);
  const PickRow = picker && pick && text.trim() ? pickParts.Row : null;
  const [moreAnchor, setMoreAnchor] = useState<HTMLElement | null>(null);
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const rewind = isUser ? chat?.rewind : undefined;
  const rewindBlock = useWhileOpen(rewind?.block, Boolean(anchor || moreAnchor), "");
  const extras = [
    ...moreActions(itemId, chat?.fork, chat?.assistant),
    ...rewindActions(messageId, editText ?? text, rewind && { block: rewindBlock, code: rewind.code, run: rewind.run }),
    ...(isUser && chat?.canCompose ? savePromptActions(text) : []), // where you write messages: not a read-only chat
    ...deleteActions(onDelete),
  ];
  const contextActions = side ? actions.filter((a) => a.key === "forward" || a.key === "context") : [];
  const more: MenuItem[] = [...contextActions, ...extras].map((a, i) => ({ ...a, divider: i === contextActions.length && i > 0 }));
  const anchorRef = useRef<HTMLElement | null>(null);
  // Your bubble's hover bar sits to its left; in a narrow window a wide bubble leaves no room there and the bar fell
  // off the screen, its buttons still reached by Tab (audit item 73). Measured when the pointer comes or focus moves in
  // (not for every bubble as the chat opens): with no room, the same buttons sit under the bubble instead, so the one
  // focused stays focused and in sight.
  const barRef = useRef<HTMLDivElement>(null);
  const [below, setBelow] = useState(false);
  const placeBar = (row: HTMLElement) => {
    const bar = barRef.current;
    if (!side || !bar) return;
    const fits = sideBarFits(row, bar);
    if (fits === below) setBelow(!fits);
  };
  const press = useRef<{ timer: number; x: number; y: number } | null>(null);
  const opened = useRef(0);

  const openAt = (x: number, y: number) => {
    anchorRef.current?.remove();
    const el = pointAnchor(x, y);
    anchorRef.current = el;
    setAnchor(el);
  };
  const close = () => {
    anchorRef.current?.remove();
    anchorRef.current = null;
    setAnchor(null);
  };
  useEffect(() => () => anchorRef.current?.remove(), []);

  const cancelPress = () => {
    if (press.current) clearTimeout(press.current.timer);
    press.current = null;
  };

  if (!actions.length) return <div className={className}>{children}</div>;

  const items: MenuItem[] = [...actions, ...extras].map((a, i) => ({ ...a, divider: i === actions.length && i > 0 }));

  return (
    <div
      className={cx("group/msg", (side || corner || PickRow) && "relative", PickRow && (side ? "isolate" : "isolate pl-7"), className)}
      onPointerEnter={side ? (e) => e.pointerType !== "touch" && placeBar(e.currentTarget) : undefined}
      onFocus={side ? (e) => placeBar(e.currentTarget) : undefined}
    >
      {PickRow && picker && pick && <PickRow pick={picker} message={pick} side={side} />}
      <div
        inert={picking || undefined}
        className="[@media(hover:none)]:select-none [@media(hover:none)]:[-webkit-touch-callout:none]"
        onContextMenu={(e) => {
          if (Date.now() - opened.current < 1000) {
            e.preventDefault(); // the long press already opened ours
            return;
          }
          // a selection inside the message (the browser's menu copies it), a link, a picture or code: the browser's own
          const sel = getSelection();
          if (sel && !sel.isCollapsed && sel.anchorNode && e.currentTarget.contains(sel.anchorNode)) return;
          if ((e.target as Element).closest?.("a, img, pre")) return;
          e.preventDefault();
          openAt(e.clientX, e.clientY);
        }}
        onPointerDown={(e) => {
          if (e.pointerType !== "touch") return;
          cancelPress();
          const x = e.clientX,
            y = e.clientY;
          press.current = {
            x,
            y,
            timer: window.setTimeout(() => {
              press.current = null;
              opened.current = Date.now();
              openAt(x, y);
            }, 450),
          };
        }}
        onPointerMove={(e) => {
          const p = press.current;
          if (p && Math.hypot(e.clientX - p.x, e.clientY - p.y) > 8) cancelPress();
        }}
        onPointerUp={cancelPress}
        onPointerCancel={cancelPress}
        // the finger lifting after the press opened the menu: no mouse events after it, whose mousedown would land
        // outside the menu and close it at once
        onTouchEnd={(e) => {
          if (Date.now() - opened.current < 1000) e.preventDefault();
        }}
      >
        {children}
      </div>
      {picking ? null : bar ? (
        <Suspense fallback={<div data-reply-bar="loading" className="mt-1.5 hidden h-6 [@media(hover:hover)]:block" />}>
          <ReplyBar text={text} ts={ts} pinned={latest} fork={extras.find((a) => a.key === "fork")} more={actions.filter((a) => a.key !== "copy" && a.key !== "select")} usage={usage} usageMs={usageMs} persona={chat?.assistant} />
        </Suspense>
      ) : (side || corner) && (
      <div
        ref={barRef}
        data-side-bar={side ? (below ? "below" : "beside") : undefined}
        className={cx(
          "hidden h-6 items-center gap-0.5 opacity-0 transition-opacity duration-200 group-hover/msg:opacity-100 focus-within:opacity-100 [@media(hover:hover)]:flex",
          moreAnchor && "opacity-100", // the ⋯ menu is open: its bar stays while the pointer is in the menu
          side
            ? below
              ? // over what comes next while shown only: hidden, it takes no clicks meant for that; a bridge over the gap
                // keeps the pointer on its way down from the bubble in the hover
                cx("absolute top-full right-0 z-10 mt-1 rounded-control bg-surface px-0.5 shadow-hairline before:absolute before:inset-x-0 before:-top-1 before:h-1",
                  moreAnchor ? "pointer-events-auto" : "pointer-events-none group-hover/msg:pointer-events-auto focus-within:pointer-events-auto")
              : "absolute right-full bottom-0.5 mr-1.5"
            : "absolute top-2 right-2 rounded-[8px] bg-surface px-0.5 shadow-hairline",
        )}
      >
        {side && ts && !latest && <span className="mr-1 text-[12px] whitespace-nowrap text-ink-3 tabular-nums">{clock(ts)}</span>}
        {actions
          .filter((a) => a.key !== "select" && !contextActions.includes(a))
          .map((a) => (
            <button
              key={a.key}
              type="button"
              aria-label={a.key === "copy" ? (isUser ? t("chat-messageactions.copy_message") : t("chat-messageactions.copy_reply")) : a.label}
              title={a.label}
              onClick={a.onSelect}
              className="flex size-6 items-center justify-center rounded-[6px] text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink-2"
            >
              <Icon name={a.icon} size={14} />
            </button>
          ))}
        {more.length > 0 && (
          <button
            type="button"
            aria-label={t("chat-messageactions.more")}
            title={t("chat-messageactions.more")}
            aria-haspopup="menu"
            aria-expanded={Boolean(moreAnchor)}
            onClick={(e) => setMoreAnchor(moreAnchor ? null : e.currentTarget)}
            className={cx("flex size-6 items-center justify-center rounded-[6px] text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink-2", moreAnchor && "bg-hover-2 text-ink-2")}
          >
            <Icon name="more" size={14} />
          </button>
        )}
      </div>
      )}
      {touch && bar && !picking && (
        <div className="mt-1 flex h-6 items-center gap-5">
          <button type="button" aria-label={t("chat-messageactions.copy_reply")} title={t("chat-messageactions.copy")} onClick={() => void copyText(text)} className="touch-hit flex size-6 items-center justify-center rounded-[6px] text-ink-3">
            <Icon name="copy" size={14} />
          </button>
          <button
            type="button"
            aria-label={t("chat-messageactions.more")}
            title={t("chat-messageactions.more")}
            aria-haspopup="menu"
            aria-expanded={Boolean(anchor)}
            onClick={(e) => (anchor ? close() : setAnchor(e.currentTarget))}
            className="touch-hit flex size-6 items-center justify-center rounded-[6px] text-ink-3"
          >
            <Icon name="more" size={14} />
          </button>
          {latest && ts && <span data-latest-time className="-ml-3 text-[12px] whitespace-nowrap text-ink-3 tabular-nums">{clock(ts)}</span>}
          {usage && (
            <Suspense fallback={null}>
              <TurnUsage usage={usage} ms={usageMs} persona={chat?.assistant} touch />
            </Suspense>
          )}
        </div>
      )}
      {anchor && !picking && <Menu anchor={anchor} onClose={close} items={items} width={items.some((i) => i.hint) ? 200 : 168} />}
      {moreAnchor && !picking && <Menu anchor={moreAnchor} onClose={() => setMoreAnchor(null)} items={more} side="top" width={more.some((a) => a.hint) ? 200 : 168} />}
      <Dialog
        open={selecting}
        onClose={() => setSelecting(false)}
        title={t("chat-messageactions.select_text")}
        footer={
          <>
            <Button tone="quiet" onClick={() => setSelecting(false)}>
              {t("chat-messageactions.done")}
            </Button>
            <Button
              tone="primary"
              onClick={() => {
                void copyText(text);
                setSelecting(false);
              }}
            >
              {t("chat-messageactions.copy_all")}
            </Button>
          </>
        }
      >
        <div className="max-h-[50vh] overflow-y-auto text-[14px] leading-relaxed whitespace-pre-wrap text-ink select-text [overflow-wrap:anywhere]">{text}</div>
      </Dialog>
    </div>
  );
}
