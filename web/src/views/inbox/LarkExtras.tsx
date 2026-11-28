// The Feishu chat page's parts beyond text (lib/lark-chat.ts holds the logic): files that download, voice and video
// that play in the page, a card's buttons, the read mark beside my messages, reactions and their picker, and the
// profile card behind an avatar (iOS LarkFileCard / LarkVoicePart / LarkVideoPart / LarkCardButton / LarkReadMarkView /
// LarkReactionPicker / LarkProfileSheet).
import { useEffect, useRef, useState } from "react";
import { errorText } from "../../lib/api";
import { safeWebURL } from "../../lib/format";
import { withToken, type LarkPart } from "../../lib/inbox";
import { emojiLabel } from "../../lib/inbox-page";
import { useItemManager, withManager } from "../../lib/manager-choice";
import { safeApiPath } from "../../lib/safe-media-url";
import {
  cardIconName, directChat, duration, emojiCatalog, fileSize, pressCardAction, profile, profileRows, quickBar, quickReactions,
  type CardButton, type EmojiCatalog, type LarkProfile, type LarkProfileRef, type LarkReaction, type ReadMark,
} from "../../lib/lark-chat";
import { t } from "../../i18n";
import { Button, cx, Dialog, Loading, Spinner, toast } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { LarkAvatar } from "./InboxParts";

// ── files, voice, video ──────────────────────────────────

/** A file: type, name, size; a click downloads it under its name. */
export function FilePart({ p }: { p: LarkPart }) {
  const manager = useItemManager();
  const meta = p.metadata ?? {};
  const name = p.text || (typeof meta.name === "string" ? meta.name : "") || t("inbox-larkthread.file");
  const ext = String(meta.extension ?? (name.includes(".") ? name.split(".").pop() : "") ?? "").toUpperCase().slice(0, 4);
  const safeHrefPath = p.url ? safeApiPath(p.url) : undefined;
  const href = safeHrefPath ? withToken(withManager(safeHrefPath, manager)) : undefined;
  const size = fileSize(meta.size);
  const gone = meta.destroyed === true;
  const body = (
    <>
      <span aria-hidden className="flex size-9 shrink-0 items-center justify-center rounded-control bg-accent-tint text-[10px] font-semibold text-accent-ink">{ext || <Icon name="clip" size={15} />}</span>
      <span className="min-w-0 flex-1">
        <span className="line-clamp-2 block text-[12.5px] font-medium break-all text-ink">{name}</span>
        <span className="block text-[11px] text-ink-3">{gone ? t("inbox-larkthread.file_gone") : size}</span>
      </span>
      {href && !gone && <Icon name="download" size={14} className="shrink-0 text-ink-3" />}
    </>
  );
  const box = "flex w-[260px] max-w-full items-center gap-2.5 rounded-card bg-surface px-3 py-2.5 shadow-card";
  if (!href || gone) return <span className={cx(box, "opacity-70")}>{body}</span>;
  return (
    <a href={href} download={name} title={t("inbox-larkthread.download_file", { name })} className={cx(box, "transition-colors hover:bg-hover")}>
      {body}
    </a>
  );
}

/** A voice message: play / pause, a few bars, how long; plays in the page. */
export function VoicePart({ p }: { p: LarkPart }) {
  const manager = useItemManager();
  const safeAudioPath = p.url ? safeApiPath(p.url) : undefined;
  const src = safeAudioPath ? withToken(withManager(safeAudioPath, manager)) : undefined;
  const ms = Number(p.metadata?.duration ?? 0);
  const audio = useRef<HTMLAudioElement | null>(null);
  const [playing, setPlaying] = useState(false);
  const [loading, setLoading] = useState(false);
  useEffect(() => () => audio.current?.pause(), []);
  const toggle = () => {
    if (!src) return;
    if (!audio.current) {
      const a = new Audio(src);
      a.onplaying = () => {
        setLoading(false);
        setPlaying(true);
      };
      a.onpause = () => setPlaying(false);
      a.onended = () => setPlaying(false);
      a.onerror = () => {
        setLoading(false);
        setPlaying(false);
        audio.current = null;
        toast(t("inbox-larkthread.play_failed"), "error");
      };
      audio.current = a;
    }
    const a = audio.current;
    if (a.paused) {
      setLoading(true);
      a.play().catch(() => setLoading(false));
    } else a.pause();
  };
  const bars = Math.min(18, Math.max(5, Math.floor(ms / 1500)));
  return (
    <button
      type="button"
      disabled={!src}
      onClick={toggle}
      aria-label={playing ? t("inbox-larkthread.pause_voice") : t("inbox-larkthread.play_voice")}
      className="flex w-fit items-center gap-2 rounded-card bg-surface px-3 py-2 whitespace-nowrap shadow-card transition-colors enabled:hover:bg-hover disabled:opacity-60"
    >
      {loading ? <Spinner size={14} /> : <Icon name={playing ? "stop" : "play"} size={14} className="text-accent-ink" />}
      <span aria-hidden className="flex items-center gap-0.5">
        {Array.from({ length: bars }, (_, i) => (
          <span key={i} className={cx("w-[3px] rounded-full", playing ? "bg-accent" : "bg-ink-3/50")} style={{ height: [8, 14, 10, 18, 12, 6][i % 6] }} />
        ))}
      </span>
      <span className="text-[12px] text-ink-3 tabular-nums">{duration(ms) || t("inbox-larkthread.voice")}</span>
    </button>
  );
}

/** A video: its poster with a play button and how long; a click plays it in place with the browser's controls. */
export function VideoPart({ p }: { p: LarkPart }) {
  const manager = useItemManager();
  const meta = p.metadata ?? {};
  const safeVideoPath = p.url ? safeApiPath(p.url) : undefined;
  const src = safeVideoPath ? withToken(withManager(safeVideoPath, manager)) : undefined;
  const safePosterPath = typeof meta.posterUrl === "string" ? safeApiPath(meta.posterUrl) : undefined;
  const poster = safePosterPath ? withToken(withManager(safePosterPath, manager)) : undefined;
  const [playing, setPlaying] = useState(false);
  const w = Number(meta.posterWidth) || 16;
  const h = Number(meta.posterHeight) || 9;
  const ratio = { aspectRatio: `${w} / ${h}` };
  if (playing && src) {
    return (
      <video
        src={src}
        poster={poster}
        controls
        autoPlay
        onError={() => toast(t("inbox-larkthread.play_failed"), "error")}
        className="max-h-80 w-[320px] max-w-full rounded-control bg-black shadow-hairline"
        style={ratio}
      />
    );
  }
  const len = duration(meta.duration);
  return (
    <button
      type="button"
      disabled={!src}
      onClick={() => setPlaying(true)}
      aria-label={t("inbox-larkthread.play_video", { name: p.text ?? "" })}
      className="relative w-[320px] max-w-full overflow-hidden rounded-control bg-black shadow-hairline"
      style={ratio}
    >
      {poster && <img src={poster} alt="" loading="lazy" className="absolute inset-0 size-full object-cover" />}
      <span className="absolute inset-0 flex items-center justify-center">
        <span className="flex size-12 items-center justify-center rounded-full bg-black/45 text-white">
          <Icon name="play" size={20} />
        </span>
      </span>
      {len && <span className="absolute right-2 bottom-2 rounded-full bg-black/50 px-1.5 py-px text-[11px] font-medium text-white tabular-nums">{len}</span>}
    </button>
  );
}

// ── card buttons ─────────────────────────────────────────

const BUTTON_TONE: Record<string, string> = {
  primary: "bg-accent-ink text-surface enabled:hover:brightness-105",
  primary_filled: "bg-accent-ink text-surface enabled:hover:brightness-105",
  danger_filled: "bg-danger-fill text-white enabled:hover:brightness-105",
  danger: "bg-surface text-red shadow-btn enabled:hover:bg-hover",
  danger_text: "text-red enabled:hover:bg-red-tint",
  text: "text-accent-ink enabled:hover:bg-accent-tint",
  primary_text: "text-accent-ink enabled:hover:bg-accent-tint",
  laser: "text-accent-ink enabled:hover:bg-accent-tint",
};

/** The icon a card button leads with: the page's closest icon for a UD token, else the card's picture, else a dot
 * (iOS LarkCardIcon). */
function CardButtonIcon({ icon }: { icon: NonNullable<CardButton["icon"]> }) {
  const manager = useItemManager();
  const name = cardIconName(icon.token);
  if (name) return <Icon name={name as IconName} size={13} className="shrink-0" />;
  const src = icon.image?.startsWith("/api/") ? withToken(withManager(icon.image, manager)) : icon.image;
  if (src) return <img src={src} alt="" className="size-3.5 shrink-0 object-contain" />;
  return <span aria-hidden className="size-1.5 shrink-0 rounded-full bg-current opacity-60" />;
}

function safeCardURL(url?: string): string | null {
  const trimmed = url?.trim();
  if (!trimmed || /\s/.test(trimmed) || !/^https?:\/\//i.test(trimmed)) return null;
  for (let i = 0; i < trimmed.length; i++) {
    const c = trimmed.charCodeAt(i);
    if (c <= 0x1f || c === 0x7f) return null;
  }
  const auth = trimmed.replace(/^https?:\/\//i, "").split(/[/?#]/, 1)[0];
  if (!auth || auth.includes("@") || auth.includes("\\")) return null;
  try {
    const parsed = new URL(trimmed);
    return (parsed.protocol === "https:" || parsed.protocol === "http:") && parsed.hostname && !parsed.username && !parsed.password ? parsed.href : null;
  } catch {
    return null;
  }
}

/** A card's buttons under it: a link opens in a tab, a callback is pressed as the user, then the message is read again
 * 1.2 s later with the spinner still on (iOS LarkCardButton). `onDone` gets the pressed message's id. */
export function CardButtons({ buttons, messageID, account, onDone }: { buttons: CardButton[]; messageID?: string; account?: number; onDone?: (messageID: string) => void | Promise<void> }) {
  const manager = useItemManager();
  const [pending, setPending] = useState<number | null>(null);
  // Unmounted (another chat, the message gone): the delayed re-read and the spinner stop there.
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  if (!buttons.length) return null;
  const press = async (b: CardButton, i: number) => {
    if (b.url) {
      const href = safeCardURL(b.url);
      if (href) window.open(href, "_blank", "noopener,noreferrer");
      return;
    }
    if (!b.action || !messageID) return;
    const name = b.label || b.tooltip || t("inbox-larkthread.card_button");
    setPending(i);
    try {
      await pressCardAction(messageID, b.action, account, manager);
      toast(t("inbox-larkthread.card_pressed", { label: name }), "ok");
      await new Promise((r) => setTimeout(r, 1200));
      if (alive.current) await onDone?.(messageID);
    } catch (e) {
      if (alive.current) toast(t("inbox-larkthread.card_failed", { error: errorText(e) }), "error");
    } finally {
      if (alive.current) setPending(null);
    }
  };
  return (
    <div className="mt-2.5 flex flex-wrap gap-1.5">
      {buttons.map((b, i) => {
        const usable = !b.disabled && (b.url ? Boolean(safeCardURL(b.url)) : Boolean(b.action) && Boolean(messageID));
        const name = b.label || b.tooltip || t("inbox-larkthread.card_button");
        return (
          <button
            key={i}
            type="button"
            disabled={!usable || pending !== null}
            onClick={() => void press(b, i)}
            aria-label={b.label ? undefined : name}
            title={b.label ? undefined : name}
            className={cx(
              "inline-flex h-8 max-w-full items-center gap-1 rounded-chip text-[12.5px] font-medium whitespace-nowrap transition-[background-color,filter] disabled:opacity-50",
              b.label ? "px-3" : "px-2.5",
              BUTTON_TONE[b.kind] ?? "bg-surface text-ink shadow-btn enabled:hover:bg-hover",
            )}
          >
            {pending === i ? <Spinner size={11} /> : b.icon ? <CardButtonIcon icon={b.icon} /> : null}
            {b.url && <Icon name="external" size={11} className="shrink-0" />}
            {b.label && <span className="truncate">{b.label}</span>}
          </button>
        );
      })}
    </div>
  );
}

// ── read mark ────────────────────────────────────────────

export function readMarkLabel(m: ReadMark): string {
  switch (m.kind) {
    case "read":
      return t("inbox-larkthread.read");
    case "unread":
      return t("inbox-larkthread.unread");
    case "all":
      return t("inbox-larkthread.all_read");
    case "progress":
      return t("inbox-larkthread.unread_count", { count: m.total - m.read });
  }
}

/** Beside my message: "Read" / "Unread" in a one-to-one chat; a ring that fills as a group reads it, a tick once all have. */
export function ReadMarkView({ mark }: { mark: ReadMark }) {
  const label = readMarkLabel(mark);
  if (mark.kind === "read" || mark.kind === "unread") {
    return <span className={cx("shrink-0 text-[11px] whitespace-nowrap", mark.kind === "unread" ? "text-accent-ink" : "text-ink-3")} title={label}>{label}</span>;
  }
  if (mark.kind === "all") {
    return (
      <span className="shrink-0 text-ink-3" title={label} role="img" aria-label={label}>
        <Icon name="check" size={12} stroke={2.4} />
      </span>
    );
  }
  const r = 5;
  const c = 2 * Math.PI * r;
  return (
    <span className="shrink-0 text-accent" title={label} role="img" aria-label={label}>
      <svg width={12} height={12} viewBox="0 0 12 12" aria-hidden>
        <circle cx={6} cy={6} r={r} fill="none" stroke="currentColor" strokeOpacity={0.25} strokeWidth={1.6} />
        <circle cx={6} cy={6} r={r} fill="none" stroke="currentColor" strokeWidth={1.6} strokeLinecap="round" strokeDasharray={`${(c * mark.read) / mark.total} ${c}`} transform="rotate(-90 6 6)" />
      </svg>
    </span>
  );
}

// ── reactions ────────────────────────────────────────────

function useCatalog(): EmojiCatalog | null {
  const manager = useItemManager();
  const [cat, setCat] = useState<EmojiCatalog | null>(null);
  useEffect(() => {
    let live = true;
    emojiCatalog(manager).then((c) => live && setCat(c)).catch(() => {});
    return () => {
      live = false;
    };
  }, [manager]);
  return cat;
}

/** One emoji of Feishu's catalogue as its picture, else `[key]`. */
export function EmojiImage({ name, size, catalog }: { name: string; size: number; catalog: EmojiCatalog | null }) {
  const manager = useItemManager();
  const e = catalog?.emojis[name.toLowerCase()];
  const safeEmojiPath = e?.url ? safeApiPath(e.url) : undefined;
  const src = safeEmojiPath ? withToken(withManager(safeEmojiPath, manager)) : safeWebURL(e?.url, true);
  if (src) return <img src={src} alt={e?.label || name} width={size} height={size} className="shrink-0 object-contain" style={{ width: size, height: size }} />;
  // The catalogue is still loading (its fetch only starts once reactions draw): reserve a neutral dot instead of
  // flashing the raw "[key]" text, which made the reaction row jump when the picture replaced it.
  if (!catalog) return <span aria-hidden className="inline-block shrink-0 rounded-full bg-field" style={{ width: size, height: size }} />;
  return <span className="text-[11px] text-ink-2">{emojiLabel(name)}</span>;
}

/** The reactions under a message: emoji and count, mine tinted; a click puts mine on or takes it off. */
export function Reactions({ reactions, onToggle, readOnly = false }: { reactions: LarkReaction[]; onToggle?: (key: string) => void; readOnly?: boolean }) {
  const catalog = useCatalog();
  if (!reactions.length) return null;
  return (
    <div className="mt-1 flex flex-wrap gap-1">
      {reactions.map((r) => {
        const who = (r.users ?? []).join(t("inbox-larkthread.name_separator"));
        const label = catalog?.emojis[r.key.toLowerCase()]?.label || r.key;
        const title = who ? t("inbox-larkthread.reacted_by", { names: who, emoji: label }) : label;
        const className = cx(
          "flex h-6 items-center gap-1 rounded-full px-2 text-[11.5px] font-medium whitespace-nowrap tabular-nums transition-colors",
          r.mine ? "bg-accent-tint text-accent-ink shadow-[inset_0_0_0_1px_var(--color-accent)]" : "bg-field text-ink-2 enabled:hover:bg-hover",
        );
        const content = <><EmojiImage name={r.key} size={16} catalog={catalog} />{r.count}</>;
        return readOnly ? (
          <span key={r.key} title={title} className={className}>{content}</span>
        ) : (
          <button
            key={r.key}
            type="button"
            disabled={!onToggle}
            onClick={() => onToggle?.(r.key)}
            title={title}
            aria-pressed={Boolean(r.mine)}
            className={className}
          >
            {content}
          </button>
        );
      })}
    </div>
  );
}

/** "Reactions": the quick row on top, then every group of the catalogue; the ones I put on are tinted. */
export function ReactionPicker({ mine, onPick, onClose }: { mine: Set<string>; onPick: (key: string) => void; onClose: () => void }) {
  const manager = useItemManager();
  const catalog = useCatalog();
  const [keys, setKeys] = useState<string[] | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let live = true;
    quickReactions(manager).then((k) => live && setKeys(quickBar(k, 7))).catch(() => live && setKeys([]));
    emojiCatalog(manager).catch(() => live && setFailed(true));
    return () => {
      live = false;
    };
  }, [manager]);
  const cell = (key: string, size: number) => {
    const label = catalog?.emojis[key.toLowerCase()]?.label || key;
    return (
      <button
        key={key}
        type="button"
        onClick={() => {
          onPick(key);
          onClose();
        }}
        title={label}
        aria-label={label}
        aria-pressed={mine.has(key.toLowerCase())}
        className={cx("flex items-center justify-center rounded-control p-1.5 transition-colors hover:bg-hover", mine.has(key.toLowerCase()) && "bg-accent-tint")}
      >
        <EmojiImage name={key} size={size} catalog={catalog} />
      </button>
    );
  };
  return (
    <Dialog open onClose={onClose} title={t("inbox-larkthread.reactions_title")} width={380}>
      {keys === null || (!catalog && !failed) ? (
        <Loading />
      ) : (
        <div className="flex max-h-[min(60vh,420px)] flex-col gap-3 overflow-y-auto">
          {keys.length > 0 && <div className="grid grid-cols-7 gap-1">{keys.map((k) => cell(k, 28))}</div>}
          {(catalog?.groups ?? []).map((g) => (
            <div key={g.title}>
              <p className="mb-1 text-[11.5px] font-medium text-ink-3">{g.title}</p>
              <div className="grid grid-cols-7 gap-1">{g.keys.map((k) => cell(k, 24))}</div>
            </div>
          ))}
          {catalog && !catalog.groups.length && !keys.length && <p className="py-4 text-center text-[12.5px] text-ink-3">{t("inbox-larkthread.reactions_empty")}</p>}
          {failed && <p className="text-[12px] text-red">{t("inbox-larkthread.reactions_failed")}</p>}
        </div>
      )}
    </Dialog>
  );
}

// ── profile ──────────────────────────────────────────────

const FIELD_LABELS = () => ({
  "B-DEPARTMENT": t("inbox-larkthread.field_department"),
  "B-LEADER": t("inbox-larkthread.field_leader"),
  "B-TITLE": t("inbox-larkthread.field_title"),
  email: t("inbox-larkthread.field_email"),
  mobile: t("inbox-larkthread.field_mobile"),
});

/** Someone's card (iOS LarkProfileSheet): picture, names, status, signature, the fields Feishu shows; "Message" opens the
 * one-to-one chat. */
export function ProfileDialog({
  who,
  feed,
  account,
  onClose,
  onChat,
}: {
  who: LarkProfileRef;
  feed?: string;
  account?: number;
  onClose: () => void;
  onChat?: (feed: string, name: string) => void;
}) {
  const manager = useItemManager();
  const [p, setP] = useState<LarkProfile | null>(null);
  const [error, setError] = useState("");
  const [round, setRound] = useState(0);
  const [opening, setOpening] = useState(false);
  useEffect(() => {
    let live = true;
    setError("");
    profile(who.id, feed, account, manager)
      .then((r) => live && setP(r))
      .catch((e) => live && setError(errorText(e)));
    return () => {
      live = false;
    };
  }, [who.id, feed, account, manager, round]);
  const name = p?.displayName || who.name;
  const subtitle = [p?.groupNickname && p.groupNickname !== name ? p.groupNickname : "", p?.enName && p.enName !== name ? p.enName : "", p?.alias || ""].filter(Boolean).join(" · ");
  const rows = p ? profileRows(p, FIELD_LABELS()) : [];
  const bot = who.bot || p?.userType === "bot";
  const chat = async () => {
    if (!onChat) return;
    setOpening(true);
    try {
      onChat(await directChat(who.id, account, manager), name);
    } catch (e) {
      toast(t("inbox-larkthread.chat_failed", { error: errorText(e) }), "error");
    } finally {
      setOpening(false);
    }
  };
  return (
    <Dialog
      open
      onClose={onClose}
      width={400}
      footer={onChat && !bot ? (
        <Button tone="accent" icon="chat" busy={opening} onClick={() => void chat()}>{t("inbox-larkthread.send_message")}</Button>
      ) : undefined}
    >
      <div className="flex items-center gap-3 pt-2">
        <LarkAvatar name={name} size={56} avatarURL={p?.avatar_url || who.avatar_url} avatar={p?.avatarUrl || who.avatar} account={account} />
        <div className="min-w-0 flex-1">
          <p className="flex items-center gap-1.5">
            <span className="truncate text-[16px] font-semibold text-ink">{name}</span>
            {bot && <span className="shrink-0 rounded-chip bg-field px-1 text-[10px] text-ink-3">{t("inbox-larkthread.bot")}</span>}
            {p?.relation?.external && <span className="shrink-0 rounded-chip bg-orange-tint px-1 text-[10px] text-orange">{t("inbox-larkthread.external")}</span>}
          </p>
          {subtitle && <p className="truncate text-[12px] text-ink-3">{subtitle}</p>}
          {p?.customStatus?.title && <p className="mt-0.5 truncate text-[12px] text-ink-2">{p.customStatus.title}</p>}
        </div>
      </div>
      {!p && !error && <div className="pt-3"><Loading /></div>}
      {error && (
        <div className="flex items-center gap-2 pt-3">
          <p className="min-w-0 flex-1 text-[12.5px] text-red">{t("inbox-larkthread.profile_failed", { error })}</p>
          <Button tone="quiet" icon="retry" onClick={() => setRound((n) => n + 1)}>{t("inbox-larkthread.retry")}</Button>
        </div>
      )}
      {p?.description && <p className="mt-3 text-[12.5px] leading-relaxed whitespace-pre-wrap text-ink-2">{p.description}</p>}
      {(rows.length > 0 || p?.relation?.tenantName) && (
        <dl className="mt-3 divide-y divide-line rounded-card bg-inset px-3 shadow-hairline">
          {p?.relation?.tenantName && (
            <div className="flex gap-3 py-2 text-[12.5px]">
              <dt className="w-16 shrink-0 text-ink-3">{t("inbox-larkthread.field_tenant")}</dt>
              <dd className="min-w-0 flex-1 break-words text-ink">{p.relation.tenantName}</dd>
            </div>
          )}
          {rows.map((r, i) => (
            <div key={i} className="flex gap-3 py-2 text-[12.5px]">
              <dt className="w-16 shrink-0 text-ink-3">{r.label}</dt>
              <dd className="min-w-0 flex-1 break-words text-ink">
                {r.url ? <a href={r.url} target="_blank" rel="noopener noreferrer" className="text-accent-ink hover:underline">{r.value}</a> : r.value}
              </dd>
            </div>
          ))}
        </dl>
      )}
    </Dialog>
  );
}
