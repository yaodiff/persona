// "Health" (docs/assistant/health.md, #2691): the health space, apart from Persona's other conversations like ChatGPT's Health. `/health`
// opens its latest conversation (#4329), or its home when it has none; `/health/new` is its home, the new conversation page
// (what it is, where its data comes from, suggested questions, its past conversations, its memory); `/health/<id>` one of its conversations. Its conversations run in a directory of their own on the assistant's machine,
// so the assistant's other conversations don't see them and its memory is its own (`?space=health`).
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, enc, errorText } from "../../lib/api";
import { ago } from "../../lib/format";
import { memoryEntries, type MemoryEntry, type MemoryFile } from "../../lib/memory";
import { noteSent } from "../../lib/outbox";
import { plainMarkdown } from "../../lib/plain-markdown";
import { href, navigate, onLinkClick } from "../../lib/router";
import { onHubEvent, useStore } from "../../lib/store";
import { noteConversation } from "../../lib/manager-choice";
import type { Session } from "../../lib/types";
import { Button, Dialog, EASE, IconButton, Loading, Menu, toast, useMenu } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { ChatView } from "../chat/ChatView";
import { Composer, type Outgoing } from "../chat/Composer";
import { conversationTitle, newestFirst } from "../assistant/conversations";
import { HealthReports } from "./HealthReports";
import { REPORT_FILES, startHealthConversation, uploadReports } from "./reports";
import { healthEntry } from "./entry";
import { Empty, SectionTitle } from "../assistant/parts";
import { t } from "../../i18n";

export const HEALTH_SPACE = "health";

const SUGGESTIONS = [t("health.suggestion_sleep"), t("health.suggestion_steps"), t("health.suggestion_heart"), t("health.suggestion_log")];

const SOURCES: { icon: IconName; title: string; body: string }[] = [
  { icon: "heart", title: t("health.source_phone"), body: t("health.source_phone_body") },
  { icon: "pencil", title: t("health.source_write"), body: t("health.source_write_body") },
  { icon: "upload", title: t("health.source_upload"), body: t("health.source_upload_body") },
];

/** The space's conversations, newest first, and the machine they run on (GET /api/v1/assistant?space=health). The
 *  space is the default manager's whichever manager the Persona page shows (`?space=` names none, docs/assistant/managers.md):
 *  its machine is its own answer's, not the shown manager's. */
function useHealthConversations() {
  const [list, setList] = useState<Session[] | null>(null);
  const [mid, setMid] = useState<string | undefined>(undefined);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    try {
      const a = await api<{ machine_id?: string; conversations?: Session[] }>(`/assistant?space=${HEALTH_SPACE}`);
      for (const c of a.conversations ?? []) noteConversation(c.session_id, undefined);
      setList(a.conversations ?? []);
      setMid(a.machine_id || undefined);
      setError("");
    } catch (e) {
      setError(errorText(e));
    }
  }, []);
  useEffect(() => {
    void load();
    // read again on every hello: one that failed while the hub was away, or the space moved meanwhile
    return onHubEvent("hello", () => void load());
  }, [load]);
  // the live sessions of the space's machine bring their titles and last lines
  const live = useStore((s) => (mid ? s.sessions[mid] : undefined));
  const merged = useMemo(() => {
    if (!list) return null;
    const by = new Map(list.map((s) => [s.session_id, s]));
    for (const s of live ?? []) if (s.space === HEALTH_SPACE) by.set(s.session_id, { ...by.get(s.session_id), ...s });
    return newestFirst([...by.values()].filter((s) => !s.archived));
  }, [list, live]);
  return { list: merged, mid, error, reload: load };
}

export function Health({ sid, fresh, onToggleSidebar }: { sid?: string; fresh?: boolean; onToggleSidebar: () => void }) {
  if (sid) return <HealthConversation sid={sid} onToggleSidebar={onToggleSidebar} />;
  if (fresh) return <HealthHome onToggleSidebar={onToggleSidebar} />;
  return <HealthResume />;
}

/** `/health`, the sidebar's Health (#4329): back to the latest conversation, as Codex does, else the new conversation
 *  page. Either replaces this address, so Back skips it. */
function HealthResume() {
  const { list, error } = useHealthConversations();
  const entry = healthEntry(list, error);
  const to = entry.kind === "open" ? entry.sid : entry.kind;
  useEffect(() => {
    if (to === "wait") return;
    navigate(to === "new" ? { name: "health", fresh: true } : { name: "health", sid: to }, true);
  }, [to]);
  return <div className="flex h-full items-center justify-center"><Loading /></div>;
}

/** A health conversation's header (#4329): New opens the new conversation page, the clock the space's past conversations. */
function HealthChatActions({ sid, list }: { sid: string; list: Session[] | null }) {
  const menu = useMenu();
  const items = (list ?? []).map((s) => ({
    key: s.session_id,
    label: conversationTitle(s),
    hint: s.updated_at ? ago(s.updated_at) : undefined,
    checked: s.session_id === sid,
    onSelect: () => navigate({ name: "health", sid: s.session_id }),
  }));
  return (
    <>
      {items.length > 0 && <IconButton icon="clock" label={t("health.history")} onClick={menu.open} active={Boolean(menu.anchor)} />}
      <IconButton icon="compose" label={t("health.new_chat")} onClick={() => navigate({ name: "health", fresh: true })} />
      {menu.anchor && <Menu anchor={menu.anchor} onClose={menu.close} align="right" width={320} header={t("health.recent")} items={items} />}
    </>
  );
}

/** One of the space's conversations, on the space's machine (its list says which); a list that could not be read
 *  says why, with Retry. */
function HealthConversation({ sid, onToggleSidebar }: { sid: string; onToggleSidebar: () => void }) {
  const { list, mid, error, reload } = useHealthConversations();
  const [retrying, setRetrying] = useState(false);
  if (!mid && error) {
    const retry = () => {
      setRetrying(true);
      void reload().finally(() => setRetrying(false));
    };
    return (
      <div className="flex h-full flex-col">
        <header className="flex h-14 shrink-0 items-center gap-2 px-3 sm:px-4">
          <IconButton icon="sidebar" label={t("health.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
        </header>
        <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 px-5 text-center" role="alert">
          <p className="max-w-[420px] text-[13px] break-words text-ink-2">{t("health.conversation_load_failed", { error })}</p>
          <Button onClick={retry} busy={retrying} icon="retry">{t("health.retry")}</Button>
        </div>
      </div>
    );
  }
  if (!mid) return <div className="flex h-full items-center justify-center"><Loading /></div>;
  return <ChatView key={sid} mid={mid} sid={sid} assistant placeholder={t("health.ask_placeholder")} onToggleSidebar={onToggleSidebar} headerActions={<HealthChatActions sid={sid} list={list} />} />;
}

function HealthHome({ onToggleSidebar }: { onToggleSidebar: () => void }) {
  // a report picked in the box goes to the space's machine, into its reports/
  const { list, mid, error, reload } = useHealthConversations();
  const [seed, setSeed] = useState(0);

  // one conversation per message: the box empties at once (Composer), so a second send would start a second one
  const [starting, setStarting] = useState(false);
  const start = async (out: Outgoing): Promise<boolean> => {
    setStarting(true);
    try {
      const r = await startHealthConversation({ prompt: out.text, images: out.images.length ? out.images : undefined });
      noteConversation(r.session.session_id, undefined);
      noteSent(r.session.machine_id, r.session.session_id, out);
      navigate({ name: "health", sid: r.session.session_id });
      return true;
    } catch (e) {
      toast(t("health.start_failed", { error: errorText(e) }), "error");
      return false;
    } finally {
      setStarting(false);
    }
  };
  // "Upload a checkup report": picked files go to reports/ and their conversation starts by itself
  const picker = useRef<HTMLInputElement>(null);
  const [uploading, setUploading] = useState(false);
  const upload = async (files: File[]) => {
    if (!mid || !files.length) return;
    setUploading(true);
    try {
      await start({ text: await uploadReports(mid, files, HEALTH_SPACE), images: [] });
    } catch (e) {
      toast(t("health.report_failed", { error: errorText(e) }), "error");
    } finally {
      setUploading(false);
    }
  };
  const fill = (text: string) => {
    localStorage.setItem("draft:health-new", text);
    setSeed((x) => x + 1);
  };

  return (
    <div className="flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("health.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
        <h1 className="flex min-w-0 flex-1 items-center gap-2 truncate text-[14px] font-semibold text-ink">
          <Icon name="heart" size={14} className="text-accent-ink" />
          {t("health.title")}
        </h1>
        <IconButton icon="retry" label={t("health.refresh")} onClick={() => void reload()} />
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-[760px] px-4 py-6 sm:px-8" style={{ animation: `fade-up 320ms ${EASE} both` }}>
          <div className="mb-5 flex flex-col items-center text-center">
            <span className="mb-3 flex size-12 items-center justify-center rounded-full bg-inset text-accent-ink shadow-raised">
              <Icon name="heart" size={22} />
            </span>
            <h2 className="text-[22px] font-semibold tracking-tight text-ink">{t("health.hero_title")}</h2>
            <p className="mt-1.5 max-w-[520px] text-[13px] leading-relaxed text-ink-2">{t("health.hero_body")}</p>
          </div>

          <Composer key={seed} draftKey="health-new" placeholder={t("health.ask_placeholder")} machineId={mid} uploadDest={HEALTH_SPACE} onSend={start} disabled={starting} autoFocus commands={[]} />
          <input ref={picker} type="file" multiple hidden accept={REPORT_FILES} data-testid="health-report-input" onChange={(e) => void upload(Array.from(e.target.files ?? [])).finally(() => (e.target.value = ""))} />
          <button type="button" disabled={uploading || !mid} onClick={() => picker.current?.click()} className="mt-3 flex w-full items-center justify-center gap-2 rounded-control bg-surface px-3 py-2.5 text-[13px] font-medium whitespace-nowrap text-ink shadow-btn transition-colors hover:bg-hover disabled:opacity-60">
            <Icon name="upload" size={14} className="shrink-0" />
            {uploading ? t("health.report_uploading") : t("health.report_upload")}
          </button>
          <div className="mt-1.5 grid grid-cols-1 gap-1.5 sm:grid-cols-2">
            {SUGGESTIONS.map((s) => (
              <button key={s} type="button" onClick={() => fill(s)} className="flex items-center gap-2 rounded-control px-3 py-2 text-left text-[12.5px] text-ink-2 shadow-hairline transition-colors hover:bg-hover hover:text-ink">
                <Icon name="chevronRight" size={12} stroke={2.2} className="shrink-0 text-ink-3" />
                <span className="truncate">{s}</span>
              </button>
            ))}
          </div>

          <HealthReports version={seed} />

          <section className="mt-7">
            <SectionTitle>{t("health.recent")}</SectionTitle>
            {list === null && !error && <div className="flex justify-center py-8"><Loading /></div>}
            {error && list === null && <Empty icon="info" title={t("health.load_failed")}>{error}</Empty>}
            {list && list.length === 0 && <Empty icon="heart" title={t("health.empty_title")}>{t("health.empty_body")}</Empty>}
            {list && list.length > 0 && (
              <div className="divide-y divide-line overflow-hidden rounded-card bg-surface shadow-card">
                {list.map((s) => <HealthRow key={s.session_id} s={s} />)}
              </div>
            )}
          </section>

          <section className="mt-7">
            <SectionTitle>{t("health.sources")}</SectionTitle>
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
              {SOURCES.map((x) => (
                <div key={x.title} className="rounded-card bg-surface p-3.5 shadow-card">
                  <Icon name={x.icon} size={15} className="mb-2 text-ink-2" />
                  <p className="text-[13px] font-medium text-ink">{x.title}</p>
                  <p className="mt-1 text-[12px] leading-relaxed text-ink-3">{x.body}</p>
                </div>
              ))}
            </div>
          </section>

          <HealthMemory />

          <section className="mt-7 mb-4 rounded-card bg-surface p-4 shadow-card">
            <p className="flex items-center gap-2 text-[13px] font-medium text-ink"><Icon name="lock" size={14} className="text-ink-2" />{t("health.privacy_title")}</p>
            <ul className="mt-2 list-disc space-y-1 pl-5 text-[12.5px] leading-relaxed text-ink-2">
              <li>{t("health.privacy_separate")}</li>
              <li>{t("health.privacy_memory")}</li>
              <li>{t("health.privacy_where")}</li>
              <li>{t("health.privacy_doctor")}</li>
            </ul>
          </section>
        </div>
      </div>
    </div>
  );
}

function HealthRow({ s }: { s: Session }) {
  const route = { name: "health" as const, sid: s.session_id };
  const last = plainMarkdown(s.last_text);
  return (
    <a href={href(route)} onClick={(e) => onLinkClick(e, route)} className="flex min-w-0 items-center gap-3 px-4 py-2.5 transition-colors hover:bg-hover">
      <span className="flex size-8 shrink-0 items-center justify-center rounded-full bg-inset text-accent-ink"><Icon name="heart" size={13} /></span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[13.5px] font-medium text-ink">{conversationTitle(s)}</span>
        {last && <span className="block truncate text-[12px] text-ink-3">{last}</span>}
      </span>
      {s.updated_at && <span className="shrink-0 text-[11.5px] text-ink-3 tabular-nums">{ago(s.updated_at)}</span>}
    </a>
  );
}

/** The health space's own memory: what it remembers about you, apart from Persona's (view and delete). */
function HealthMemory() {
  const [files, setFiles] = useState<MemoryFile[] | null>(null);
  const [error, setError] = useState("");
  const [open, setOpen] = useState<MemoryEntry | null>(null);
  const [busy, setBusy] = useState(false);
  const load = useCallback(async () => {
    try {
      const r = await api<{ files: MemoryFile[] }>(`/assistant/memory?space=${HEALTH_SPACE}`);
      setFiles(r.files ?? []);
      setError("");
    } catch (e) {
      setError(errorText(e));
    }
  }, []);
  useEffect(() => {
    void load();
    // read again on every hello, as the space's conversations and reports are: one that failed while the hub was away
    return onHubEvent("hello", () => void load());
  }, [load]);
  const entries = useMemo(() => memoryEntries(files ?? []), [files]);

  const remove = async (e: MemoryEntry) => {
    setBusy(true);
    try {
      await api(`/assistant/memory/${enc(e.file.name)}?space=${HEALTH_SPACE}`, { method: "DELETE" });
      toast(t("health.memory_deleted", { title: e.title }), "ok");
      setOpen(null);
      await load();
    } catch (err) {
      toast(errorText(err), "error");
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="mt-7">
      <SectionTitle right={entries.length > 0 ? <span className="text-[11.5px] text-ink-3 tabular-nums">{t("health.memory_count", { count: entries.length })}</span> : undefined}>{t("health.memory")}</SectionTitle>
      <div className="overflow-hidden rounded-card bg-surface shadow-card">
        {files === null && !error ? (
          <div className="flex justify-center py-6"><Loading /></div>
        ) : entries.length === 0 ? (
          <p className="px-4 py-4 text-[12.5px] leading-relaxed text-ink-3">{error ? t("health.memory_load_failed", { error }) : t("health.memory_empty")}</p>
        ) : (
          <div className="divide-y divide-line">
            {entries.map((e) => (
              <button key={e.file.name} type="button" onClick={() => setOpen(e)} className="flex w-full min-w-0 flex-col items-start px-4 py-2.5 text-left transition-colors hover:bg-hover">
                <span className="w-full truncate text-[13px] font-medium text-ink">{e.title}</span>
                {e.detail && <span className="w-full truncate text-[12px] text-ink-3">{e.detail}</span>}
              </button>
            ))}
          </div>
        )}
      </div>
      <Dialog
        open={open !== null}
        onClose={() => setOpen(null)}
        title={open?.title}
        footer={open && (
          <>
            <Button tone="quiet" onClick={() => setOpen(null)}>{t("health.close")}</Button>
            <Button tone="danger" icon="trash" busy={busy} onClick={() => void remove(open)}>{t("health.memory_delete")}</Button>
          </>
        )}
      >
        <p className="whitespace-pre-wrap text-[13px] leading-relaxed text-ink-2">{open?.body}</p>
      </Dialog>
    </section>
  );
}
