// The Feishu chat's composer: text, the quoted message, and pictures / files picked, pasted or dropped
// (docs/extensions/lark-attachments.md, iOS LarkComposer). Text and each attachment go as messages of their own.
// In a group, typing @ looks the members up (lib/lark-people.ts, ui/LarkPeoplePicker.tsx) and the message @s whom it
// still names. With a `draft` key, what is typed and who is @-ed stay as the chat's draft until sent (lib/lark-chat.ts;
// the quote is the page's and is kept with it; the box is drawn again for each chat).
import { forwardRef, useCallback, useEffect, useImperativeHandle, useLayoutEffect, useRef, useState } from "react";
import { useComposition } from "../../lib/ime";
import { fitPick, releaseLarkAttachments, stageLarkAttachment, type LarkAttachment } from "../../lib/lark-attachments";
import { chatMembers, loadDraft, saveDraft, withEveryone } from "../../lib/lark-chat";
import { useItemManager } from "../../lib/manager-choice";
import { mentionsIn, type LarkPerson } from "../../lib/lark-people";
import { PHONE, useMedia } from "../../lib/viewport";
import { t } from "../../i18n";
import { AttachmentTray } from "../../ui/AttachmentTray";
import { Button, cx, Spinner, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { LarkPeopleMenu, useLarkPeoplePicker } from "../../ui/LarkPeoplePicker";

export interface LarkReplyTarget {
  id: string;
  sender_name?: string;
  text?: string;
}

export interface LarkComposerHandle {
  addFiles: (files: FileList | File[]) => void;
  focus: () => void;
}

export interface LarkComposerProps {
  replyTo?: LarkReplyTarget | null;
  onClearReply?: () => void;
  /** Sends what is in the box (with whom its text still @s); resolves false to keep it all there (nothing was sent). */
  onSend: (text: string, attachments: LarkAttachment[], replyTo?: LarkReplyTarget, mentions?: LarkPerson[]) => Promise<boolean | void>;
  /** The chat's draft key (lib/lark-chat.ts draftKey): the text, the quote and the @-ed are kept under it. */
  draft?: string;
  /** A group's chat: typing @ offers its members ("Everyone" first). */
  group?: boolean;
  feed?: string;
  account?: number;
  /** Pictures and files can be added (the hub's extension sends them, and can route them where this box sends). */
  attachments?: boolean;
  placeholder?: string;
  /** Smaller, under a thread. */
  compact?: boolean;
}

export const LarkComposer = forwardRef<LarkComposerHandle, LarkComposerProps>(function LarkComposer(
  { replyTo = null, onClearReply, onSend, attachments: canAttach = false, placeholder, compact = false, draft, group = false, feed, account }, ref,
) {
  const [stored] = useState(() => (draft ? loadDraft(draft) : { text: "", mentions: undefined }));
  const [text, setText] = useState(stored.text);
  const [picked, setPicked] = useState<LarkPerson[]>(stored.mentions ?? []);
  const [caret, setCaret] = useState(stored.text.length);
  const [focused, setFocused] = useState(false);
  const phone = useMedia(PHONE);
  const place = useRef<number | null>(null);
  const [sending, setSending] = useState(false);
  const [files, setFiles] = useState<LarkAttachment[]>([]);
  const [staging, setStaging] = useState(0);
  const [dragging, setDragging] = useState(false);
  const input = useRef<HTMLTextAreaElement>(null);
  const picker = useRef<HTMLInputElement>(null);
  const ime = useComposition();
  const mountedRef = useRef(true);
  const sendingRef = useRef(false);
  const draftRevRef = useRef(0);
  const liveAttachmentsRef = useRef<Map<string, LarkAttachment>>(new Map());
  const inFlightFileIdsRef = useRef<Set<string>>(new Set());
  const stagingRef = useRef(0);

  // previews left in the box go with it; StrictMode re-runs mount after cleanup so reset mountedRef
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      const toRelease: LarkAttachment[] = [];
      for (const [id, att] of liveAttachmentsRef.current) {
        if (!inFlightFileIdsRef.current.has(id)) {
          toRelease.push(att);
          liveAttachmentsRef.current.delete(id);
        }
      }
      if (toRelease.length) releaseLarkAttachments(toRelease);
    };
  }, []);

  const addFiles = (list: FileList | File[]) => {
    if (!canAttach || !mountedRef.current) return;
    const { take, dropped } = fitPick(liveAttachmentsRef.current.size + stagingRef.current, Array.from(list));
    if (dropped) toast(t("inbox-larkcomposer.too_many", { count: dropped }), "error");
    if (!take.length) return;
    stagingRef.current += take.length;
    setStaging(stagingRef.current);
    for (const f of take) {
      stageLarkAttachment(f)
        .then((a) => {
          if (!mountedRef.current) {
            releaseLarkAttachments([a]);
            return;
          }
          liveAttachmentsRef.current.set(a.id, a);
          setFiles((cur) => [...cur, a]);
        })
        .catch((e: unknown) => {
          if (mountedRef.current) toast(e instanceof Error ? e.message : String(e), "error");
        })
        .finally(() => {
          stagingRef.current = Math.max(0, stagingRef.current - 1);
          if (mountedRef.current) setStaging(stagingRef.current);
        });
    }
  };

  useImperativeHandle(ref, () => ({ addFiles, focus: () => input.current?.focus() }));

  // The draft: kept a moment after the last change, and right away when the box goes (another chat, the page closing).
  const clearedReplyIdRef = useRef<string | undefined>(undefined);
  if (replyTo?.id !== clearedReplyIdRef.current) clearedReplyIdRef.current = undefined;
  const effectiveReplyTo = replyTo && replyTo.id !== clearedReplyIdRef.current ? replyTo : undefined;
  const latest = useRef({ text, replyTo: effectiveReplyTo as LarkReplyTarget | null | undefined, picked });
  latest.current = { text, replyTo: effectiveReplyTo, picked };
  const save = useCallback(() => {
    if (!draft) return;
    const d = latest.current;
    saveDraft(draft, { text: d.text, replyTo: d.replyTo ?? undefined, mentions: mentionsIn(d.text, d.picked) });
  }, [draft]);
  useEffect(() => {
    const id = setTimeout(save, 300);
    return () => clearTimeout(id);
  }, [text, replyTo, picked, save]);
  useEffect(() => {
    addEventListener("pagehide", save);
    return () => {
      removeEventListener("pagehide", save);
      save();
    };
  }, [save]);

  // A drawn-back draft or a picked name: as tall as its text.
  useLayoutEffect(() => {
    const el = input.current;
    if (!el) return;
    el.style.height = "auto";
    if (el.value) el.style.height = `${Math.min(el.scrollHeight, 120)}px`;
  }, [text]);

  // The caret after a picked name, set before the next key.
  useLayoutEffect(() => {
    const at = place.current;
    if (at === null || !input.current) return;
    place.current = null;
    input.current.focus();
    input.current.setSelectionRange(at, at);
  });

  const everyone = t("inbox-larkthread.everyone");
  const manager = useItemManager();
  const find = useCallback(
    (q: string, signal: AbortSignal) => chatMembers(feed ?? "", q, account, signal, manager).then((people) => withEveryone(people, q, everyone)),
    [feed, account, manager, everyone],
  );
  const people = useLarkPeoplePicker({
    text,
    caret,
    open: focused && group && Boolean(feed),
    find,
    onPicked: (p) => {
      draftRevRef.current += 1;
      setPicked((cur) => [...cur, p]);
    },
    apply: (next, at) => {
      draftRevRef.current += 1;
      place.current = at;
      setText(next);
      setCaret(at);
    },
  });

  const remove = (id: string) => {
    if (inFlightFileIdsRef.current.has(id)) return;
    const doomed = liveAttachmentsRef.current.get(id);
    if (doomed) {
      liveAttachmentsRef.current.delete(id);
      releaseLarkAttachments([doomed]);
    }
    setFiles((cur) => cur.filter((a) => a.id !== id));
  };

  const ready = (text.trim() !== "" || files.length > 0) && staging === 0;
  const submit = async () => {
    if (sendingRef.current || stagingRef.current > 0) return;
    const chosen = Array.from(liveAttachmentsRef.current.values());
    const trimmed = latest.current.text.trim();
    if (!trimmed && chosen.length === 0) return;
    const sentRev = draftRevRef.current;
    const sentIds = new Set(chosen.map((a) => a.id));
    const sentReply = latest.current.replyTo ?? undefined;
    const sentMentions = mentionsIn(trimmed, latest.current.picked);
    sendingRef.current = true;
    inFlightFileIdsRef.current = sentIds;
    setSending(true);
    let handedOff = false;
    try {
      const sent = await onSend(trimmed, chosen, sentReply, sentMentions);
      if (sent === false) return;
      handedOff = true;
      // Ownership of submitted attachments transfers to the sent message bubble; keep any newly staged files.
      for (const id of sentIds) liveAttachmentsRef.current.delete(id);
      const unchanged = draftRevRef.current === sentRev;
      if (!mountedRef.current) {
        if (unchanged && draft) {
          const cur = loadDraft(draft);
          const curMentions = cur.mentions ?? [];
          const sameDraft = cur.text.trim() === trimmed
            && (cur.replyTo?.id ?? undefined) === sentReply?.id
            && curMentions.length === sentMentions.length
            && curMentions.every((m, i) => m.user_id === sentMentions[i].user_id);
          if (sameDraft) saveDraft(draft, { text: "", replyTo: undefined, mentions: [] });
        }
        return;
      }
      setFiles((cur) => cur.filter((a) => !sentIds.has(a.id)));
      if (unchanged) {
        clearedReplyIdRef.current = sentReply?.id;
        if (draft) saveDraft(draft, { text: "", replyTo: undefined, mentions: [] });
        latest.current = { text: "", replyTo: undefined, picked: [] };
        setText("");
        setPicked([]);
        setCaret(0);
        if (input.current) input.current.style.height = "auto";
      }
    } catch (e: unknown) {
      if (mountedRef.current) toast(e instanceof Error ? e.message : String(e), "error");
    } finally {
      if (!handedOff && !mountedRef.current && chosen.length) {
        for (const id of sentIds) liveAttachmentsRef.current.delete(id);
        releaseLarkAttachments(chosen);
      }
      inFlightFileIdsRef.current = new Set();
      sendingRef.current = false;
      if (mountedRef.current) setSending(false);
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (ime.busy(e.nativeEvent)) return;
    if (people.onKeyDown(e)) return;
    if (e.key === "Enter" && !e.shiftKey && !e.altKey) {
      e.preventDefault();
      void submit();
    }
  };

  const handleChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    draftRevRef.current += 1;
    setText(e.target.value);
    setCaret(e.target.selectionStart);
  };

  return (
    <div
      className={cx("relative", compact ? "pt-1.5" : "shrink-0 border-t border-line bg-surface px-3 py-2 sm:px-4")}
      onDragOver={(e) => {
        if (canAttach && e.dataTransfer.types.includes("Files")) {
          e.preventDefault();
          setDragging(true);
        }
      }}
      onDragLeave={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node)) setDragging(false);
      }}
      onDrop={(e) => {
        if (!canAttach || !e.dataTransfer.files.length) return;
        e.preventDefault();
        e.stopPropagation();
        setDragging(false);
        addFiles(e.dataTransfer.files);
      }}
    >
      {dragging && (
        <div className="pointer-events-none absolute inset-1 z-10 flex items-center justify-center rounded-[10px] border-2 border-dashed border-accent bg-accent-tint/70 text-[12.5px] font-medium text-accent-ink">
          {t("inbox-larkcomposer.drop")}
        </div>
      )}
      <div className={cx(!compact && "mx-auto max-w-[760px]")}>
        {replyTo && (
          <div className="mb-2 flex items-center justify-between rounded-[8px] bg-field/60 px-3 py-1.5 text-[12px] text-ink-2">
            <div className="flex min-w-0 items-center gap-1.5">
              <Icon name="quote" size={12} className="shrink-0 text-accent-ink" />
              <span className="shrink-0 font-medium text-ink">{t("inbox-larkthread.quoted_sender", { name: replyTo.sender_name ?? "" })}</span>
              <span className="truncate text-ink-3">{replyTo.text || t("inbox-larkthread.quoted_message")}</span>
            </div>
            <button
              type="button"
              onClick={onClearReply}
              className="ml-2 shrink-0 rounded p-0.5 text-ink-3 hover:bg-surface hover:text-ink"
              title={t("inbox-larkthread.cancel_quote")}
              aria-label={t("inbox-larkthread.cancel_quote")}
            >
              <Icon name="x" size={12} />
            </button>
          </div>
        )}
        {(files.length > 0 || staging > 0) && (
          <div className="mb-2 flex items-center gap-2">
            <AttachmentTray attachments={files} onRemove={sending ? undefined : remove} />
            {staging > 0 && <Spinner size={14} />}
          </div>
        )}
        <div className="relative flex items-end gap-2">
          {group && <LarkPeopleMenu picker={people} account={account} label={t("inbox-larkthread.mention_label")} />}
          {canAttach && (
            <>
              <input
                ref={picker}
                type="file"
                multiple
                hidden
                onChange={(e) => {
                  if (e.target.files) addFiles(e.target.files);
                  e.target.value = "";
                }}
              />
              <button
                type="button"
                onClick={() => picker.current?.click()}
                disabled={sending}
                aria-label={t("inbox-larkcomposer.add")}
                title={t("inbox-larkcomposer.add_hint")}
                className={cx("touch-hit flex shrink-0 items-center justify-center rounded-[10px] text-ink-3 transition-colors hover:bg-hover hover:text-ink disabled:opacity-50", compact ? "size-8" : "size-9")}
              >
                <Icon name="clip" size={16} />
              </button>
            </>
          )}
          <textarea
            ref={input}
            value={text}
            {...ime.handlers}
            onChange={handleChange}
            onSelect={(e) => setCaret(e.currentTarget.selectionStart)}
            onFocus={() => setFocused(true)}
            onBlur={() => {
              ime.onBlur();
              setFocused(false);
            }}
            onKeyDown={handleKeyDown}
            onPaste={(e) => {
              const pasted = Array.from(e.clipboardData.files);
              if (canAttach && pasted.length) {
                e.preventDefault();
                addFiles(pasted);
              }
            }}
            placeholder={placeholder ?? (replyTo ? t("inbox-larkthread.reply_to_placeholder", { name: replyTo.sender_name ?? "" }) : group ? t(phone ? "inbox-larkthread.group_placeholder_short" : "inbox-larkthread.group_placeholder") : t("inbox-larkthread.reply_placeholder"))}
            aria-label={t("inbox-larkcomposer.message")}
            rows={1}
            className={cx(
              "max-h-[120px] flex-1 resize-none rounded-[10px] border border-line bg-field px-3 py-1.5 text-[13px] leading-relaxed text-ink placeholder:truncate placeholder:text-ink-3 focus:border-accent focus:outline-none",
              compact ? "min-h-[32px]" : "min-h-[36px]",
            )}
          />
          <Button
            tone="accent"
            busy={sending}
            disabled={!ready || sending}
            onClick={() => void submit()}
            className={cx("shrink-0 whitespace-nowrap", compact ? "h-[32px]" : "h-[36px]")}
          >
            {t("inbox-larkthread.send")}
          </Button>
        </div>
      </div>
    </div>
  );
});
