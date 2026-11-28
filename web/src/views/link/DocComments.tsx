// A Feishu document's comments in the reader panel (#1290; the phone's FeishuDocsComments): "Open" / "Resolved", each thread
// with its quote and replies. With the plugin's `lark_doc_comments` the user writes too: "Reply" to a thread, "Resolve" / "Reopen",
// a comment on the whole document from the box at the bottom, or on a passage picked in the document ("Comment" on the
// selection bar), @ someone. Only comments: the document's text is not edited here. lib/doc-comments.ts has the rest.
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { ApiError } from "../../lib/api";
import { clientIdFor, ON_DOCUMENT, readComments, replyText, sendComment, sendKey, setResolved, writeErrorText, findDocPeople, type CommentTarget, type DocReply, type DocThread } from "../../lib/doc-comments";
import { ago } from "../../lib/format";
import { mentionsIn, type LarkPerson } from "../../lib/lark-people";
import { clickLink, linkHref } from "../../lib/links";
import { defaultManager, useLinkManager } from "../../lib/manager-choice";
import { Button, cx, EASE, IconButton, Loading, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { LarkPeopleMenu, useLarkPeoplePicker } from "../../ui/LarkPeoplePicker";
import { Empty, Segmented } from "../session/files/parts";
import { docErrorFromBody, docErrorText } from "./feishu-doc";
import { t } from "../../i18n";
import { composing } from "../../lib/send-history";

export interface DocComments {
  threads: DocThread[] | null;
  loadError: string;
  reload: () => void;
  target: CommentTarget;
  setTarget: (t: CommentTarget) => void;
  draft: string;
  setDraft: (s: string) => void;
  pickPerson: (p: LarkPerson) => void;
  sending: boolean;
  resolving: Set<string>;
  send: () => Promise<void>;
  resolve: (t: DocThread, resolved: boolean) => Promise<void>;
}

function readError(e: unknown): string {
  if (e instanceof ApiError) return docErrorFromBody(e.status, e.body).message;
  return docErrorText(0);
}

/** The threads of `url` (read as soon as the reader opens, so the count and the list are there at once) and the box. */
export function useDocComments(url: string, on: boolean): DocComments {
  const manager = useLinkManager();
  const [threads, setThreads] = useState<DocThread[] | null>(null);
  const [loadError, setLoadError] = useState("");
  const [round, setRound] = useState(0);
  const [target, setTarget] = useState<CommentTarget>(ON_DOCUMENT);
  const [draft, setDraft] = useState("");
  const [picked, setPicked] = useState<LarkPerson[]>([]);
  const [sending, setSending] = useState(false);
  const [resolving, setResolving] = useState<Set<string>>(() => new Set());
  // the id of the send being tried: a retry of the same words to the same place reuses it (no second comment)
  const pending = useRef<{ key: string; id: string } | null>(null);
  // the panel moved to another document: nothing of this one's stays
  const [of, setOf] = useState(url);
  if (of !== url) {
    setOf(url);
    setThreads(null);
    setLoadError("");
    setTarget(ON_DOCUMENT);
    setDraft("");
    setPicked([]);
  }

  useEffect(() => {
    if (!on) return;
    let live = true;
    readComments(url, manager)
      .then((t) => {
        if (!live) return;
        setThreads(t);
        setLoadError("");
      })
      .catch((e: unknown) => live && setLoadError(readError(e)));
    return () => {
      live = false;
    };
  }, [url, manager, on, round]);

  const reload = useCallback(() => {
    setLoadError("");
    setRound((n) => n + 1);
  }, []);

  /** After a write: the threads its answer brought, else read again (the write went through either way). */
  const settle = async (t: DocThread[] | null) => {
    const fresh = t ?? (await readComments(url, manager).catch(() => null));
    if (fresh) setThreads(fresh);
  };

  const send = async () => {
    const text = draft.trim();
    if (!text || sending) return;
    pending.current = clientIdFor(pending.current, sendKey(url, target, text));
    setSending(true);
    try {
      const sent = await sendComment(url, target, text, mentionsIn(text, picked), pending.current.id, manager);
      pending.current = null;
      setDraft("");
      setPicked([]);
      setTarget(ON_DOCUMENT);
      await settle(sent);
    } catch (e) {
      toast(t("link-doccomments.send_failed", { error: writeErrorText(e) }), "error");
    } finally {
      setSending(false);
    }
  };

  const resolve = async (th: DocThread, resolved: boolean) => {
    if (resolving.has(th.id)) return;
    setResolving((s) => new Set(s).add(th.id));
    try {
      await settle(await setResolved(url, th.id, resolved, manager));
      if (target.kind === "thread" && target.id === th.id && resolved) setTarget(ON_DOCUMENT);
    } catch (e) {
      toast(resolved ? t("link-doccomments.resolve_failed", { error: writeErrorText(e) }) : t("link-doccomments.reopen_failed", { error: writeErrorText(e) }), "error");
    } finally {
      setResolving((s) => {
        const n = new Set(s);
        n.delete(th.id);
        return n;
      });
    }
  };

  const pickPerson = useCallback((p: LarkPerson) => setPicked((cur) => [...cur, p]), []);

  return { threads, loadError, reload, target, setTarget, draft, setDraft, pickPerson, sending, resolving, send, resolve };
}

/** What to answer in a thread: its last reply (FeishuDocsCommentsModel.reply(to:)). */
export function replyTarget(th: DocThread): CommentTarget {
  const last = th.replies[th.replies.length - 1];
  return { kind: "thread", id: th.id, name: last?.author.name ?? t("link-doccomments.comment"), text: last ? replyText(last) : th.quote };
}

export function DocCommentsView({ url, model, writes, resolved, onResolved, onBack, focus }: { url: string; model: DocComments; writes: boolean; resolved: boolean; onResolved: (v: boolean) => void; onBack: () => void; focus: number }) {
  const manager = useLinkManager();
  const { threads, loadError } = model;
  const open = (threads ?? []).filter((t) => !t.resolved);
  const done = (threads ?? []).filter((t) => t.resolved);
  const shown = resolved ? done : open;
  const replyingTo = model.target.kind === "thread" ? model.target.id : null;
  const box = useRef<HTMLTextAreaElement>(null);

  const answer = (t: DocThread) => {
    model.setTarget(replyTarget(t));
    box.current?.focus();
  };

  return (
    // Esc steps back here (the @ menu, then "Reply" / "Comment on this passage", then the document) and never reaches the chat's "Interrupt".
    // The surface is there at once (the document must not show through it); what is on it fades in.
    <div
      className="flex h-full flex-col bg-surface"
      onKeyDown={(e) => {
        if (e.key !== "Escape" || composing(e.nativeEvent)) return;
        e.preventDefault();
        e.stopPropagation();
        onBack();
      }}
    >
      <div className="flex min-h-0 flex-1 flex-col" style={{ animation: `fade-in 160ms ease-out both` }}>
        <div className="flex h-11 shrink-0 items-center gap-2 border-b border-line px-3">
          <IconButton icon="arrowLeft" label={t("link-doccomments.back")} onClick={onBack} />
          <span className="text-[13px] font-semibold text-ink">{t("link-doccomments.title")}</span>
          <span className="flex-1" />
          <Segmented
            label={t("link-doccomments.status")}
            value={resolved ? "done" : "open"}
            onChange={(v) => onResolved(v === "done")}
            options={[
              ["open", t("link-doccomments.open_count", { count: open.length })],
              ["done", t("link-doccomments.done_count", { count: done.length })],
            ]}
          />
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="mx-auto flex w-full max-w-[760px] flex-col gap-3 px-4 pt-4 pb-6 sm:px-5">
            {threads === null ? (
              loadError ? (
                <Empty icon="info" title={t("link-doccomments.load_failed")} detail={loadError} action={<Button icon="retry" onClick={model.reload}>{t("link-doccomments.retry")}</Button>} />
              ) : (
                <div className="py-10">
                  <Loading label={t("link-doccomments.loading")} />
                </div>
              )
            ) : shown.length === 0 ? (
              <Empty
                icon="chat"
                title={resolved ? t("link-doccomments.empty_done") : t("link-doccomments.empty_open")}
                detail={resolved ? t("link-doccomments.empty_done_detail") : writes ? t("link-doccomments.empty_open_detail_write") : t("link-doccomments.empty_open_detail")}
              />
            ) : (
              shown.map((t) => <ThreadCard key={t.id} thread={t} writes={writes} replying={replyingTo === t.id} resolving={model.resolving.has(t.id)} onReply={() => answer(t)} onResolve={(v) => void model.resolve(t, v)} />)
            )}
          </div>
        </div>
        {writes && <CommentBox key={JSON.stringify([manager ?? defaultManager(), url])} url={url} model={model} box={box} focus={focus} />}
      </div>
    </div>
  );
}

function ThreadCard({ thread, writes, replying, resolving, onReply, onResolve }: { thread: DocThread; writes: boolean; replying: boolean; resolving: boolean; onReply: () => void; onResolve: (resolved: boolean) => void }) {
  return (
    <article
      aria-label={thread.whole ? t("link-doccomments.whole") : t("link-doccomments.thread_label", { quote: thread.quote })}
      className={cx("rounded-card bg-surface p-3 shadow-raised transition-shadow duration-200", replying && "ring-2 ring-accent")}
    >
      {thread.whole || !thread.quote ? (
        <div className="mb-2 inline-flex items-center gap-1 rounded-chip bg-field px-1.5 py-0.5 text-[11px] font-medium text-ink-3">
          <Icon name="read" size={11} /> {t("link-doccomments.whole")}
        </div>
      ) : (
        <blockquote className="mb-2 line-clamp-2 border-l-2 border-line-strong pl-2 text-[12px] leading-[1.55] text-ink-3">{thread.quote}</blockquote>
      )}
      <div className="flex flex-col gap-2.5">
        {thread.replies.map((r) => (
          <Reply key={r.id} reply={r} />
        ))}
      </div>
      {writes && (
        <div className="mt-2.5 flex items-center gap-1 border-t border-line pt-2">
          {!thread.resolved && (
            <Button tone="quiet" icon="chat" onClick={onReply} className="h-7 px-2">
              {t("link-doccomments.reply")}
            </Button>
          )}
          <Button tone="quiet" icon={thread.resolved ? "undo" : "check"} busy={resolving} onClick={() => onResolve(!thread.resolved)} className="h-7 px-2">
            {thread.resolved ? t("link-doccomments.reopen") : t("link-doccomments.resolve")}
          </Button>
        </div>
      )}
    </article>
  );
}

function Reply({ reply }: { reply: DocReply }) {
  const when = ago(reply.createdAt);
  const manager = useLinkManager();
  return (
    <div className="flex gap-2">
      <span aria-hidden className="mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full bg-field text-[11px] font-semibold text-ink-2">
        {[...reply.author.name][0] ?? "?"}
      </span>
      <div className="min-w-0 flex-1">
        <div className="text-[12px] text-ink-3">
          <span className="font-medium text-ink-2">{reply.author.name}</span>
          {when && <> · {when}</>}
          {reply.edited && <> · {t("link-doccomments.edited")}</>}
        </div>
        <div className="text-[13px] leading-[1.6] whitespace-pre-wrap text-ink [overflow-wrap:anywhere]">
          {reply.content.map((s, i) => {
            if (s.kind === "mention") {
              return (
                <span key={i} className="font-medium text-accent">
                  {s.text}
                </span>
              );
            }
            const safeHref = s.href && !s.href.trim().startsWith("/") ? linkHref(s.href) : undefined;
            return safeHref ? (
              <a key={i} href={safeHref} onClick={(e) => clickLink(e, s.href, undefined, manager)} target="_blank" rel="noreferrer noopener" className="text-accent underline-offset-2 hover:underline">
                {s.text}
              </a>
            ) : (
              <span key={i}>{s.text}</span>
            );
          })}
          {Boolean(reply.imageCount) && <span className="text-ink-3"> {t("link-doccomments.images", { count: reply.imageCount ?? 0 })}</span>}
        </div>
      </div>
    </div>
  );
}

function targetLine(tg: CommentTarget): { label: string; text: string } | null {
  if (tg.kind === "thread") return { label: t("link-doccomments.reply_to", { name: tg.name }), text: tg.text };
  if (tg.kind === "passage") return { label: t("link-doccomments.comment_passage"), text: tg.quote };
  return null;
}

/** The box at the bottom: where it goes (over the box, × back to the whole document), the words, @, "Send". */
function CommentBox({ url, model, box, focus }: { url: string; model: DocComments; box: React.RefObject<HTMLTextAreaElement | null>; focus: number }) {
  const [caret, setCaret] = useState(0);
  const [focused, setFocused] = useState(false);
  // where the caret goes once a picked name is in the text: set as soon as React has put it there, before the next
  // key (a frame later, the first letter typed after a pick would be left behind the caret)
  const place = useRef<number | null>(null);
  useLayoutEffect(() => {
    const at = place.current;
    if (at === null || !box.current) return;
    place.current = null;
    box.current.focus();
    box.current.setSelectionRange(at, at);
  });
  const { draft, setDraft, target } = model;
  const manager = useLinkManager();
  const find = useCallback((q: string, signal: AbortSignal) => findDocPeople(url, q, signal, manager), [url, manager]);
  const picker = useLarkPeoplePicker({
    text: draft,
    caret,
    open: focused,
    find,
    onPicked: model.pickPerson,
    apply: (text, at) => {
      place.current = at;
      setDraft(text);
      setCaret(at);
    },
  });

  // "Comment" on the selection bar (or "Reply") puts the caret here
  useEffect(() => {
    if (focus) box.current?.focus();
  }, [focus, box]);

  // grows with its text up to six lines
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 140)}px`;
  }, [draft, box]);

  const line = targetLine(target);
  const can = draft.trim().length > 0 && !model.sending;
  const placeholder = target.kind === "thread" ? t("link-doccomments.placeholder_reply", { name: target.name }) : target.kind === "passage" ? t("link-doccomments.placeholder_passage") : t("link-doccomments.placeholder_whole");

  return (
    <div className="shrink-0 border-t border-line bg-surface px-3 pt-2 pb-3">
      {line && (
        <div className="mb-2 flex items-center gap-2 rounded-[8px] bg-field py-1 pr-1 pl-2.5" style={{ animation: `fade-up 180ms ${EASE} both` }}>
          <Icon name={target.kind === "thread" ? "chat" : "quote"} size={13} className="shrink-0 text-ink-3" />
          <div className="min-w-0 flex-1 truncate text-[12px] text-ink-2">
            <span className="font-medium text-ink">{line.label}</span>
            {line.text && <span className="text-ink-3">{t("link-doccomments.target_text", { text: line.text })}</span>}
          </div>
          <IconButton icon="x" label={t("link-doccomments.comment_whole_instead")} size={24} iconSize={13} onClick={() => model.setTarget(ON_DOCUMENT)} />
        </div>
      )}
      <div className="relative flex items-end gap-2">
        <LarkPeopleMenu picker={picker} manager={manager} />
        <textarea
          ref={box}
          rows={1}
          value={draft}
          aria-label={t("link-doccomments.write")}
          placeholder={placeholder}
          onChange={(e) => {
            setDraft(e.target.value);
            setCaret(e.target.selectionStart);
          }}
          onSelect={(e) => setCaret(e.currentTarget.selectionStart)}
          onFocus={() => setFocused(true)}
          onBlur={() => setFocused(false)}
          onKeyDown={(e) => {
            if (picker.onKeyDown(e)) return;
            if (e.nativeEvent.isComposing || e.keyCode === 229) return;
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              void model.send();
            } else if (e.key === "Escape" && target.kind !== "document") {
              e.preventDefault();
              e.stopPropagation();
              model.setTarget(ON_DOCUMENT);
            }
          }}
          className="max-h-[140px] min-h-9 flex-1 resize-none rounded-[10px] bg-field px-3 py-2 text-[13.5px] leading-[1.45] text-ink outline-none placeholder:text-ink-3 focus:shadow-hairline"
        />
        <Button tone="primary" icon="send" busy={model.sending} disabled={!can} onClick={() => void model.send()} className="h-9">
          {t("link-doccomments.send")}
        </Button>
      </div>
    </div>
  );
}
