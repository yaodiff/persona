import { useMailbox } from "../../lib/mail";
import { useEffect, useState } from "react";
import { api, enc, errorText, isAbort } from "../../lib/api";
import { clock, safeWebURL } from "../../lib/format";
import { docTypeName, isDone, withToken, type InboxItem, type MomentComment, type MomentPost } from "../../lib/inbox";
import { momentText } from "../../lib/inbox-page";
import { itemHeaders, useItemManager, withManager } from "../../lib/manager-choice";
import { openLink } from "../../lib/links";
import { t } from "../../i18n";
import { Button, cx, Loading } from "../../ui/atoms";
import { Glyph, Monogram, StatusMenu, Tag } from "./InboxParts";
import { openMedia, safeMediaURL } from "./LarkThread";
import { StoredMailBody } from "./StoredMailBody";

export function MailBody(props: { item: InboxItem; onLatest?: () => void }) {
  const { mailbox, loading } = useMailbox();
  if (mailbox) return <mailbox.Body {...props} />;
  if (loading) return <div className="p-5"><Loading label={t("inbox-inboxbodies.reading_mail")} /></div>;
  return <StoredMailBody {...props} />;
}

// ── Feishu document ──────────────────────────────────────

export function DocBody({ item }: { item: InboxItem }) {
  const doc = item.doc!;
  const rows = doc.rows ?? [];
  const tables = new Set(rows.map((r) => r.table).filter(Boolean));
  const kind = docTypeName(doc.type);
  return (
    <div className="mx-auto max-w-[760px] px-4 py-4 sm:px-6">
      <div className="flex flex-wrap items-center gap-2">
        <Button tone="secondary" icon="external" onClick={() => openLink(doc.url, item.title, item.manager)}>{t("inbox-inboxbodies.open_doc", { kind })}</Button>
        {doc.reply && (
          <span className="flex items-center gap-1.5 text-[12px] text-ink-3">
            <Glyph name="reply" size={12} />
            {t("inbox-inboxbodies.reply_goes", { how: doc.reply.label })}
          </span>
        )}
      </div>
      {doc.error && <p className="mt-3 text-[12.5px] text-red">{t("inbox-inboxbodies.doc_error", { error: doc.error })}</p>}
      {rows.length > 0 && (
        <section className="mt-5">
          <h3 className="mb-1.5 px-1 text-[12px] font-medium text-ink-3">{t("inbox-inboxbodies.rows_mentioning_you")}</h3>
          <div className="flex flex-col gap-2">
            {rows.map((row) => (
              <div key={row.ref} className="rounded-card bg-surface px-4 py-3 shadow-card">
                <div className="flex items-start gap-2">
                  <p className="min-w-0 flex-1 text-[13.5px] font-semibold text-ink">{row.title}</p>
                  {row.table && (tables.size > 1 || doc.type === "base") && <Tag>{row.table}</Tag>}
                </div>
                {!!row.fields?.length && (
                  <dl className="mt-1.5 grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-[12.5px]">
                    {row.fields.map((f) => (
                      <div key={f.name} className="contents">
                        <dt className="text-ink-3">{f.name}</dt>
                        <dd className="min-w-0 break-words text-ink">{f.value}</dd>
                      </div>
                    ))}
                  </dl>
                )}
                {!!row.status?.length && (
                  <div className="mt-2.5 flex flex-wrap gap-1.5">
                    {row.status.map((st) => <StatusMenu key={st.field} it={item} row={row} status={st} showName />)}
                  </div>
                )}
              </div>
            ))}
          </div>
          <p className="mt-1.5 px-1 text-[11.5px] text-ink-3">{isDone(item) ? t("inbox-inboxbodies.done_no_status") : t("inbox-inboxbodies.status_writes", { kind })}</p>
        </section>
      )}
      {!rows.length && !doc.error && <p className="mt-5 text-[12.5px] text-ink-3">{item.summary || item.preview}</p>}
    </div>
  );
}

// ── Moments ─────────────────────────────────────────────

export function MomentBody({ item }: { item: InboxItem }) {
  const id = item.moment!.post_id;
  const replyTo = item.moment!.reply_to;
  const [post, setPost] = useState<MomentPost | null>(null);
  const [comments, setComments] = useState<MomentComment[]>([]);
  const [error, setError] = useState("");
  const manager = useItemManager();

  useEffect(() => {
    let live = true;
    const abort = new AbortController();
    setPost(null);
    setError("");
    const base = `/lark/moments/posts/${enc(id)}`;
    const headers = itemHeaders(manager);
    api<MomentPost>(base, { headers, signal: abort.signal }).then((p) => live && setPost(p)).catch((e) => { if (live && !isAbort(e)) setError(errorText(e)); });
    api<{ comments?: MomentComment[] }>(`${base}/comments`, { headers, signal: abort.signal }).then((r) => live && setComments(r.comments ?? [])).catch(() => {});
    return () => {
      live = false;
      abort.abort();
    };
  }, [id, manager]);

  if (!post) return <div className="p-5">{error ? <p className="text-[12.5px] text-red">{error}</p> : <Loading label={t("inbox-inboxbodies.reading_post")} />}</div>;
  const images = (post.images ?? [])
    .map((i) => {
      const raw = i.url?.trim();
      if (!raw) return undefined;
      return raw.startsWith("/") ? safeMediaURL(withToken(withManager(raw, manager))) : safeWebURL(raw);
    })
    .filter(Boolean) as string[];
  return (
    <div className="mx-auto max-w-[760px] px-4 py-4 sm:px-6">
      <article className="rounded-card bg-surface p-4 shadow-card">
        <Author name={post.userName ?? ""} avatar={post.userAvatarUrl} time={post.createTime} />
        <p className="mt-3 text-[14px] leading-relaxed break-words whitespace-pre-wrap text-ink">{momentText(post.content)}</p>
        {images.length > 0 && (
          <div className={cx("mt-3 grid gap-1", images.length === 1 ? "max-w-[300px] grid-cols-1" : "max-w-[420px] grid-cols-3")}>
            {images.map((src, i) => (
              <button key={i} type="button" onClick={() => openMedia(src)} className={cx("overflow-hidden rounded-[8px] shadow-hairline", images.length > 1 && "aspect-square")}>
                <img src={src} alt="" loading="lazy" className="size-full object-cover" />
              </button>
            ))}
          </div>
        )}
        <p className="mt-3 flex flex-wrap gap-x-3 text-[11.5px] text-ink-3">
          {!!post.categoryNames?.length && <span>{post.categoryNames.join(" · ")}</span>}
          {(post.reactionCount ?? 0) > 0 && <span>{t("inbox-inboxbodies.reactions", { count: post.reactionCount ?? 0 })}</span>}
          <span>{t("inbox-inboxbodies.comments_public")}</span>
        </p>
      </article>
      <h3 className="mt-5 mb-1.5 px-1 text-[12px] font-medium text-ink-3">{t("inbox-inboxbodies.comments", { count: post.commentCount ?? comments.length })}</h3>
      <div className="flex flex-col gap-1">
        {comments.map((c) => (
          <div key={c.id} className={cx("rounded-[10px] px-3 py-2.5", c.id === replyTo ? "bg-accent-tint" : "bg-surface shadow-card")}>
            <Author name={c.userName ?? ""} avatar={c.userAvatarUrl} time={c.createTime} small extra={c.id === replyTo ? t("inbox-inboxbodies.send_replies_here") : undefined} />
            <p className="mt-1 pl-[38px] text-[13px] leading-relaxed break-words whitespace-pre-wrap text-ink">
              {c.replyUserName && <span className="text-ink-3">{t("inbox-inboxbodies.reply_to_user", { name: c.replyUserName })}</span>}
              {momentText(c.content)}
            </p>
          </div>
        ))}
      </div>
    </div>
  );
}

function Author({ name, avatar, time, small, extra }: { name: string; avatar?: string; time?: number; small?: boolean; extra?: string }) {
  return (
    <div className="flex items-center gap-2.5">
      <Monogram name={name || "?"} size={small ? 28 : 36} src={avatar} />
      <div className="min-w-0 flex-1">
        <p className={cx("truncate font-medium text-ink", small ? "text-[12.5px]" : "text-[13.5px]")}>{name}</p>
        {time ? <p className="text-[11px] text-ink-3">{clock(new Date(time).toISOString())}</p> : null}
      </div>
      {extra && <span className="shrink-0 text-[11px] font-medium text-accent-ink">{extra}</span>}
    </div>
  );
}
