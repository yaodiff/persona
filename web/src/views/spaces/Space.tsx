// A domain space (#13500, docs/web/web.md "Domain spaces", docs/assistant/health.md "Tracks and kinds"): a place of its
// own for one part of the user's life, like the health space. `/tracks/<slug>` opens its latest conversation, or its
// home when it has none; `/tracks/<slug>/new` is its home (what it is, a box to start a conversation, its past
// conversations, its SPACE.md, its default categories); `/tracks/<slug>/<id>` one of its conversations. The health space
// keeps its own page (modules/health/Health.tsx).
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { errorText } from "../../lib/api";
import { NEW_CONVERSATION, pickedEffort, pickedLauncher, pickedModel, pickedServiceTier, pickEffort, pickLauncher, pickServiceTier } from "../../lib/assistant-chat";
import { ASSISTANT_START_SERVICE_TIER_FEATURE } from "../../lib/assistant-models";
import { ago } from "../../lib/format";
import { noteConversation } from "../../lib/manager-choice";
import { noteSent } from "../../lib/outbox";
import { plainMarkdown } from "../../lib/plain-markdown";
import { spaceMdBody } from "../../lib/space-md";
import { href, navigate, onLinkClick } from "../../lib/router";
import { hubHas, useStore } from "../../lib/store";
import { changeTrack, noteTrack, readSpaceMd, spaceIcon, startSpaceConversation, useTracks, type SpaceMd, type Track } from "../../lib/tracks";
import type { Session } from "../../lib/types";
import { Button, Dialog, EASE, IconButton, Loading, Menu, toast, useMenu } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { Markdown } from "../../ui/Markdown";
import { ChatView } from "../chat/ChatView";
import { ReadOnlyChatView } from "../chat/ReadOnlyChatView";
import { Composer, type Outgoing } from "../chat/Composer";
import { AssistantModelPill } from "../chat/ModelPill";
import { conversationTitle } from "../assistant/conversations";
import { Empty, SectionTitle, Tag } from "../assistant/parts";
import { spaceEntry } from "./entry";
import { useSpaceConversations } from "./conversations";
import { HEALTH_TRACK, healthArchiveTrack, healthReadOnly } from "../../lib/health-archive";
import { HealthReadOnlyData } from "./HealthReadOnlyData";
import { ConversationCategories } from "./ConversationCategories";
import { categoryLabel } from "./labels";
import { SpaceMdEditor } from "./SpaceMdEditor";
import { t } from "../../i18n";

const trackIcon = (track?: Track): IconName => track?.slug === HEALTH_TRACK ? "heart" : spaceIcon(track);

export function Space({ slug, sid, fresh, onToggleSidebar }: { slug: string; sid?: string; fresh?: boolean; onToggleSidebar: () => void }) {
  const readOnly = healthReadOnly(slug);
  const { tracks, supported } = useTracks();
  const track = readOnly ? tracks?.find((x) => x.slug === slug) ?? healthArchiveTrack() : tracks?.find((x) => x.slug === slug);
  // known once the tracks are read: an unknown or archived space says so, with the way back. Health predates that
  // optional API and keeps its built-in archive metadata instead.
  if (!readOnly && tracks && (!track || track.archived) && !sid) return <SpaceGone slug={slug} archived={track?.archived === true} onToggleSidebar={onToggleSidebar} />;
  if (!readOnly && supported === false) return <SpaceGone slug={slug} archived={false} onToggleSidebar={onToggleSidebar} />;
  if (sid) return <SpaceConversation slug={slug} sid={sid} track={track} readOnly={readOnly} onToggleSidebar={onToggleSidebar} />;
  if (fresh) return track ? <SpaceHome track={track} readOnly={readOnly} onToggleSidebar={onToggleSidebar} /> : <Centered />;
  return <SpaceResume slug={slug} />;
}

function Centered() {
  return <div className="flex h-full items-center justify-center"><Loading /></div>;
}

/** `/tracks/<slug>`, the sidebar's row: back to the latest conversation, else the home. Replaces this address, so Back
 *  skips it. */
function SpaceResume({ slug }: { slug: string }) {
  const { list, error } = useSpaceConversations(slug);
  const entry = spaceEntry(list, error);
  const to = entry.kind === "open" ? entry.sid : entry.kind;
  useEffect(() => {
    if (to === "wait") return;
    navigate(to === "new" ? { name: "track", slug, fresh: true } : { name: "track", slug, sid: to }, true);
  }, [to, slug]);
  return <Centered />;
}

function Header({ title, icon, onToggleSidebar, children }: { title: string; icon: IconName; onToggleSidebar: () => void; children?: React.ReactNode }) {
  return (
    <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
      <IconButton icon="sidebar" label={t("spaces.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
      <h1 className="flex min-w-0 flex-1 items-center gap-2 text-card-title font-semibold text-ink">
        <Icon name={icon} size={14} className="shrink-0 text-accent-ink" />
        <span className="truncate">{title}</span>
      </h1>
      {children}
    </header>
  );
}

function SpaceGone({ slug, archived, onToggleSidebar }: { slug: string; archived: boolean; onToggleSidebar: () => void }) {
  return (
    <div className="flex h-full flex-col">
      <Header title={slug} icon="layers" onToggleSidebar={onToggleSidebar} />
      <div className="flex min-h-0 flex-1 flex-col justify-center pb-[10vh]">
        <Empty icon={archived ? "archive" : "layers"} title={archived ? t("spaces.archived_title") : t("spaces.gone_title")} action={<Button icon="home" onClick={() => navigate({ name: "persona" })}>{t("spaces.back_to_persona")}</Button>}>
          {archived ? t("spaces.archived_body") : t("spaces.gone_body")}
        </Empty>
      </div>
    </div>
  );
}

/** A conversation's header: its categories, the space's past conversations, and New. */
function SpaceChatActions({ slug, sid, list, readOnly }: { slug: string; sid: string; list: Session[] | null; readOnly: boolean }) {
  const menu = useMenu();
  const items = (list ?? []).map((s) => ({
    key: s.session_id,
    label: conversationTitle(s),
    hint: s.updated_at ? ago(s.updated_at) : undefined,
    checked: s.session_id === sid,
    onSelect: () => navigate({ name: "track", slug, sid: s.session_id }),
  }));
  return (
    <>
      {!readOnly && <span className="hidden sm:flex"><ConversationCategories sid={sid} /></span>}
      {items.length > 0 && <IconButton icon="clock" label={t("spaces.history")} onClick={menu.open} active={Boolean(menu.anchor)} />}
      {!readOnly && <IconButton icon="compose" label={t("spaces.new_chat")} onClick={() => navigate({ name: "track", slug, fresh: true })} />}
      {menu.anchor && <Menu anchor={menu.anchor} onClose={menu.close} align="right" width={320} header={t("spaces.recent")} items={items} />}
    </>
  );
}

/** One of the space's conversations, on the space's computer (its list says which). */
function SpaceConversation({ slug, sid, track, readOnly, onToggleSidebar }: { slug: string; sid: string; track?: Track; readOnly: boolean; onToggleSidebar: () => void }) {
  const { list, mid, error, reload } = useSpaceConversations(slug);
  const [retrying, setRetrying] = useState(false);
  if (!mid && error) {
    const retry = () => {
      setRetrying(true);
      void reload().finally(() => setRetrying(false));
    };
    return (
      <div className="flex h-full flex-col">
        <Header title={track?.title || slug} icon={trackIcon(track)} onToggleSidebar={onToggleSidebar} />
        <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 px-5 text-center" role="alert">
          <p className="max-w-[420px] break-words text-meta text-ink-2">{t("spaces.conversation_load_failed", { error })}</p>
          <Button onClick={retry} busy={retrying} icon="retry">{t("spaces.retry")}</Button>
        </div>
      </div>
    );
  }
  if (!mid) return <Centered />;
  const history = <SpaceChatActions slug={slug} sid={sid} list={list} readOnly={readOnly} />;
  if (readOnly) return <ReadOnlyChatView key={sid} mid={mid} sid={sid} onToggleSidebar={onToggleSidebar} headerActions={history} />;
  return <ChatView key={sid} mid={mid} sid={sid} assistant placeholder={t("spaces.ask_placeholder", { name: track?.title || slug })} onToggleSidebar={onToggleSidebar} headerActions={history} />;
}

function SpaceHome({ track, readOnly, onToggleSidebar }: { track: Track; readOnly: boolean; onToggleSidebar: () => void }) {
  const slug = track.slug;
  const assistant = useStore((s) => s.assistant);
  const startServiceTier = useStore((s) => hubHas(s.features, ASSISTANT_START_SERVICE_TIER_FEATURE));
  const { list, mid, error, reload } = useSpaceConversations(slug);
  const icon = trackIcon(track);
  const menu = useMenu();
  const [editing, setEditing] = useState(false);
  const [archiving, setArchiving] = useState(false);
  const [saved, setSaved] = useState<SpaceMd | null>(null);

  // one conversation per message: the box empties at once (Composer), so a second send would start a second one
  const [starting, setStarting] = useState(false);
  const start = async (out: Outgoing): Promise<boolean> => {
    setStarting(true);
    const launcher = pickedLauncher(NEW_CONVERSATION);
    try {
      const r = await startSpaceConversation(slug, {
        prompt: out.text,
        images: out.images.length ? out.images : undefined,
        launcher,
        model: launcher ? pickedModel(NEW_CONVERSATION) : undefined,
        effort: pickedEffort(NEW_CONVERSATION),
        service_tier: startServiceTier ? pickedServiceTier() : undefined,
      });
      noteConversation(r.session.session_id, undefined);
      noteSent(r.session.machine_id, r.session.session_id, out);
      if (launcher) pickLauncher(NEW_CONVERSATION, undefined);
      pickEffort(NEW_CONVERSATION, undefined);
      pickServiceTier(undefined);
      navigate({ name: "track", slug, sid: r.session.session_id });
      return true;
    } catch (e) {
      toast(t("spaces.start_failed", { error: errorText(e) }), "error");
      return false;
    } finally {
      setStarting(false);
    }
  };

  return (
    <div className="flex h-full flex-col">
      <Header title={track.title || slug} icon={icon} onToggleSidebar={onToggleSidebar}>
        <IconButton icon="retry" label={t("spaces.refresh")} onClick={() => void reload()} />
        {!track.builtin && !readOnly && <IconButton icon="more" label={t("spaces.actions")} onClick={menu.open} active={Boolean(menu.anchor)} />}
      </Header>
      {menu.anchor && (
        <Menu
          anchor={menu.anchor}
          onClose={menu.close}
          align="right"
          items={[
            { key: "md", label: t("spaces.md_edit"), icon: "pencil", onSelect: () => setEditing(true) },
            { key: "archive", label: t("spaces.archive"), icon: "archive", onSelect: () => setArchiving(true) },
          ]}
        />
      )}
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-[760px] px-4 py-6 sm:px-8" style={{ animation: `fade-up 320ms ${EASE} backwards` }}>
          <div className="mb-5 flex flex-col items-center text-center">
            <span className="mb-3 flex size-12 items-center justify-center rounded-full bg-inset text-accent-ink shadow-raised">
              <Icon name={icon} size={22} />
            </span>
            <h2 className="text-2xl font-semibold tracking-tight text-ink">{track.title || slug}</h2>
            {track.description && <p className="mt-1.5 max-w-[520px] text-meta leading-relaxed text-ink-2">{track.description}</p>}
          </div>

          {readOnly ? (
            <p role="status" data-testid="health-readonly-note" className="mb-5 flex items-start gap-2 rounded-card bg-inset px-4 py-3 text-meta leading-relaxed text-ink-2">
              <Icon name="lock" size={14} className="mt-0.5 shrink-0" />
              <span>{t("spaces.health_read_only")}</span>
            </p>
          ) : (
            <>
              <Composer
                draftKey={`track-new:${slug}`}
                placeholder={t("spaces.ask_placeholder", { name: track.title || slug })}
                machineId={mid}
                onSend={start}
                disabled={starting}
                autoFocus
                commands={[]}
                trailing={assistant && !starting ? <AssistantModelPill /> : undefined}
              />
              <p className="mt-2 flex items-start gap-1.5 px-1 text-label leading-relaxed text-ink-3" data-testid="space-privacy">
                <Icon name="lock" size={12} className="mt-0.5 shrink-0" />
                <span>{t("spaces.privacy_line")}</span>
              </p>
            </>
          )}

          <section className="mt-7">
            <SectionTitle>{t("spaces.recent")}</SectionTitle>
            {list === null && !error && <div className="flex justify-center py-8"><Loading /></div>}
            {error && list === null && <Empty icon="info" title={t("spaces.load_failed")}>{error}</Empty>}
            {list && list.length === 0 && <Empty icon={icon} title={t("spaces.empty_title")}>{t(readOnly ? "spaces.empty_readonly_body" : "spaces.empty_body")}</Empty>}
            {list && list.length > 0 && (
              <div className="divide-y divide-line overflow-hidden rounded-card bg-surface shadow-card">
                {list.map((s) => <SpaceRow key={s.session_id} slug={slug} s={s} icon={icon} />)}
              </div>
            )}
          </section>

          {readOnly && <HealthReadOnlyData mid={mid} unavailable={error} />}

          {/* A built-in space (the Health archive) has no SPACE.md of its own: spaceTrack excludes it, so asking for one
              is always a 404 (server/internal/assistant/track_space.go). Draw the card only for a track that can have one. */}
          {!track.builtin && <SpaceMdCard slug={slug} title={track.title || slug} latest={saved} editable={!track.builtin && !readOnly} onEdit={() => setEditing(true)} />}

          <section className="mt-7 mb-4">
            <SectionTitle>{t("spaces.categories")}</SectionTitle>
            <div className="rounded-card bg-surface p-4 shadow-card">
              {track.default_categories.length > 0 ? (
                <div className="flex flex-wrap gap-1.5">{track.default_categories.map((c) => <Tag key={c}>{categoryLabel(c)}</Tag>)}</div>
              ) : (
                <p className="text-meta text-ink-3">{t("spaces.categories_none")}</p>
              )}
              <p className="mt-2 text-label leading-relaxed text-ink-3">{t("spaces.categories_body")}</p>
            </div>
          </section>
        </div>
      </div>
      {!readOnly && <SpaceMdEditor open={editing} slug={slug} onClose={() => setEditing(false)} onSaved={setSaved} />}
      {!readOnly && <ArchiveDialog open={archiving} track={track} onClose={() => setArchiving(false)} />}
    </div>
  );
}

function SpaceRow({ slug, s, icon }: { slug: string; s: Session; icon: IconName }) {
  const route = { name: "track" as const, slug, sid: s.session_id };
  const last = plainMarkdown(s.last_text);
  return (
    <a href={href(route)} onClick={(e) => onLinkClick(e, route)} className="flex min-w-0 items-center gap-3 px-4 py-2.5 transition-colors hover:bg-hover">
      <span className="flex size-8 shrink-0 items-center justify-center rounded-full bg-inset text-accent-ink"><Icon name={icon} size={13} /></span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-meta font-medium text-ink">{conversationTitle(s)}</span>
        {last && <span className="block truncate text-label text-ink-3">{last}</span>}
      </span>
      {s.updated_at && <span className="shrink-0 text-submeta text-ink-3 tabular-nums">{ago(s.updated_at)}</span>}
    </a>
  );
}

/** What SPACE.md says: the parts the user wrote, drawn as formatted text and folded to a few lines with Show more,
 *  then where things stand (Persona's state block); Persona's other kept blocks stay in the editor. */
function SpaceMdCard({ slug, title, latest, editable, onEdit }: { slug: string; title: string; latest: SpaceMd | null; editable: boolean; onEdit: () => void }) {
  const [read, setMd] = useState<SpaceMd | null>(null);
  const md = latest ?? read;
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let live = true;
    readSpaceMd(slug).then((r) => live && setMd(r), () => live && setFailed(true));
    return () => {
      live = false;
    };
  }, [slug]);
  const written = useMemo(() => (md ? spaceMdBody(md.content, title) : ""), [md, title]);
  return (
    <section className="mt-7">
      <SectionTitle center right={editable && md ? <Button tone="quiet" size="sm" icon="pencil" onClick={onEdit}>{t("spaces.md_edit")}</Button> : undefined}>{t("spaces.md_section")}</SectionTitle>
      <div className="rounded-card bg-surface p-4 shadow-card">
        {md ? (
          <>
            {written ? <FoldedMarkdown text={written} /> : <p className="text-meta text-ink-3">{t("spaces.md_empty")}</p>}
            {md.state.trim() && (
              <p className="mt-3 border-t border-line pt-3 text-label leading-relaxed text-ink-3">
                <span className="font-medium">{t("spaces.md_state")}</span> {md.state.trim()}
              </p>
            )}
          </>
        ) : (
          <p className="text-meta text-ink-3">{failed ? t("spaces.md_unavailable") : t("spaces.md_loading")}</p>
        )}
      </div>
    </section>
  );
}

/** Formatted text cut at a few lines with a fade, and Show more / Show less once it runs longer than that. */
function FoldedMarkdown({ text }: { text: string }) {
  const box = useRef<HTMLDivElement>(null);
  const [long, setLong] = useState(false);
  const [open, setOpen] = useState(false);
  useLayoutEffect(() => {
    const el = box.current;
    if (!el || open) return;
    const measure = () => setLong(el.scrollHeight > el.clientHeight + 1);
    measure();
    const ro = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    ro?.observe(el);
    return () => ro?.disconnect();
  }, [text, open]);
  const folded = !open;
  return (
    <div data-testid="space-md">
      <div
        ref={box}
        className={folded ? `max-h-40 overflow-hidden ${long ? "[mask-image:linear-gradient(to_bottom,black_55%,transparent)]" : ""}` : undefined}
      >
        <Markdown text={text} small />
      </div>
      {(long || open) && (
        <button type="button" onClick={() => setOpen((x) => !x)} aria-expanded={open} className="mt-1.5 text-label font-medium text-accent-ink hover:underline">
          {open ? t("spaces.md_show_less") : t("spaces.md_show_more")}
        </button>
      )}
    </div>
  );
}

function ArchiveDialog({ open, track, onClose }: { open: boolean; track: Track; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  const archive = async () => {
    setBusy(true);
    try {
      noteTrack(await changeTrack(track.slug, { revision: track.revision, archived: true }));
      toast(t("spaces.archived", { name: track.title || track.slug }), "ok");
      onClose();
      navigate({ name: "persona" });
    } catch (e) {
      toast(t("spaces.archive_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={t("spaces.archive_title", { name: track.title || track.slug })}
      footer={
        <>
          <Button tone="quiet" onClick={onClose}>{t("spaces.cancel")}</Button>
          <Button tone="primary" icon="archive" busy={busy} onClick={() => void archive()}>{t("spaces.archive")}</Button>
        </>
      }
    >
      <p className="text-meta leading-relaxed text-ink-2">{t("spaces.archive_body")}</p>
    </Dialog>
  );
}
