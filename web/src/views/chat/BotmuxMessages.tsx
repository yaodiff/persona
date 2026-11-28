// BotMux sessions as in Lark (lib/botmux.ts): a message as written there with its tags and its original on request,
// and the replies the agent sent to Lark under an exchange. Drawn while the sidebar's botmux chip decodes
// (lib/botmux-mode.ts); loaded with the first botmux session opened, not with the first screen.
import { memo, type ReactNode, useMemo, useState } from "react";
import { t } from "../../i18n";
import { botmuxReplies, botmuxTags, decodedUser } from "../../lib/botmux";
import { useBotmuxMode } from "../../lib/botmux-mode";
import { alsoOpenText } from "../../lib/engines";
import { attentionReason, externalLink } from "../../lib/format";
import type { Step, UserMessage } from "../../lib/turns";
import type { Botmux, Item, Session } from "../../lib/types";
import { StatusMark } from "../../ui/StatusMark";
import { Icon } from "../../ui/icons";
import { Markdown } from "../../ui/Markdown";
import { FailureNote } from "./FailureNote";
import { useFullText } from "./FullText";

function BotmuxTags({ b, end }: { b: Botmux; end?: boolean }) {
  const tags = botmuxTags(b);
  if (!tags.length) return null;
  return (
    <div className={end ? "flex flex-wrap justify-end gap-1" : "flex flex-wrap gap-1"} data-botmux-tags>
      {tags.map((tag, i) => (
        <span key={`${tag}-${i}`} className="rounded-full bg-hover px-1.5 text-[length:var(--chat-secondary)] leading-[18px] text-ink-3 whitespace-nowrap">{tag}</span>
      ))}
    </div>
  );
}

/** "Original": the transcript's words, as the agent read them. */
function BotmuxOriginal({ items, mid, sid }: { items?: Item[]; mid: string; sid: string }) {
  const [open, setOpen] = useState(false);
  const textItems = items?.filter((item) => item.kind === "text" && (item.text?.trim() || item.full_size));
  if (!textItems?.length) return null;
  return (
    <div className="flex w-full flex-col items-end gap-1">
      <button type="button" className="text-[length:var(--chat-secondary)] text-ink-3 hover:text-ink-2" aria-expanded={open} onClick={() => setOpen((v) => !v)} data-botmux-original>
        {t(open ? "chat-botmux.hide_original" : "chat-botmux.original")}
      </button>
      {open && (
        <div className="max-h-80 w-full overflow-auto rounded-lg bg-hover p-2 font-mono text-[length:var(--chat-secondary)] text-ink-2">
          {textItems.map((item) => <BotmuxOriginalText key={item.id} item={item} mid={mid} sid={sid} />)}
        </div>
      )}
    </div>
  );
}

function BotmuxOriginalText({ item, mid, sid }: { item: Item; mid: string; sid: string }) {
  const full = useFullText(item, mid, sid);
  return (
    <div>
      <pre className="whitespace-pre-wrap break-words">{full.text}</pre>
      {full.more}
    </div>
  );
}

/** What the exchange sent to Lark, each reply as the agent's message; its call stays in the work above. */
export const BotmuxReplies = memo(function BotmuxReplies({ steps, mid }: { steps: readonly Step[]; mid: string }) {
  const replies = useMemo(() => botmuxReplies(steps), [steps]);
  if (!replies.length) return null;
  return (
    <div className="flex flex-col gap-3" data-botmux-replies>
      {replies.map(({ id, reply }) => (
        <div key={id} className="flex min-w-0 flex-col gap-1" data-botmux-reply>
          <BotmuxTags b={reply} />
          <Markdown text={reply.text} mid={mid} lineBreaks />
        </div>
      ))}
    </div>
  );
});

/** A message botmux handed the agent, as written in Lark: its tags, its words in `bubble` (the chat's user bubble), and
 *  the original on request. */
export function BotmuxUser({ msg, mid, sid, bubble }: { msg: UserMessage; mid: string; sid: string; bubble: (decoded: UserMessage) => ReactNode }) {
  const decoded = decodedUser(msg);
  if (!decoded) return null;
  return (
    <div className="ml-auto flex max-w-[min(100%,488px)] min-w-0 flex-col items-end gap-1" data-botmux-message={msg.botmux!.kind}>
      <BotmuxTags b={msg.botmux!} end />
      {(decoded.text || decoded.images.length > 0) && bubble(decoded)}
      <BotmuxOriginal items={msg.rawInput} mid={mid} sid={sid} />
    </div>
  );
}

/** Above the composer while a session needs you and its host says why (a botmux attention, iOS AttentionNote):
 *  the reason and "Open in Lark" its topic. */
function AttentionNote({ session }: { session: Session }) {
  const reason = attentionReason(session);
  if (!reason) return null;
  const link = externalLink(session);
  return (
    <div role="status" data-testid="chat-attention" className="flex items-start gap-2 rounded-[12px] border-l-2 border-attention bg-inset px-3 py-2 text-meta" style={{ animation: "fade-up 200ms ease-out both" }}>
      <span className="flex h-[18px] shrink-0 items-center"><StatusMark state="needs_you" /></span>
      <span className="line-clamp-3 min-w-0 flex-1 text-ink-2">{reason}</span>
      {link && (
        <a href={link} target="_blank" rel="noopener noreferrer" className="flex shrink-0 items-center gap-1 whitespace-nowrap font-medium text-accent hover:underline">
          <Icon name="external" size={12} />
          {t("chat-chatview.open_in_lark")}
        </a>
      )}
    </div>
  );
}

/** The session is live in another engine host too (#2576): this one owns it, and messages go here only. */
function ConflictNote({ session }: { session: Session }) {
  const text = alsoOpenText(session);
  if (!text) return null;
  return (
    <div role="status" data-testid="chat-also-open" className="flex items-start gap-2 rounded-[12px] bg-orange-tint px-3 py-2 text-meta" style={{ animation: "fade-up 200ms ease-out both" }}>
      <span className="flex h-[18px] shrink-0 items-center text-orange"><Icon name="warning" size={13} /></span>
      <span className="line-clamp-2 min-w-0 flex-1 text-ink-2">{text}</span>
    </div>
  );
}

export function BotmuxComposerNotes({ session, retry }: { session: Session; retry?: () => void }) {
  return (
    <>
      <AttentionNote session={session} />
      <ConflictNote session={session} />
      {retry && <FailureNote session={session} onRetry={retry} />}
    </>
  );
}

/** Default export for single-chunk lazy loading from SegmentView. */
export default function BotmuxPart(props: { msg: UserMessage; mid: string; sid: string; bubble: (decoded: UserMessage) => ReactNode } | { steps: readonly Step[]; mid: string }) {
  if (useBotmuxMode() === "all") return "msg" in props ? props.bubble(props.msg) : null;
  return "msg" in props ? <BotmuxUser {...props} /> : <BotmuxReplies steps={props.steps} mid={props.mid} />;
}
