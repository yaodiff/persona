// The composer's one panel for + and @ (#5324, cindy's AtMentionPanel, at the owner's request). From the + it covers the
// box, a search box on top taking the keys; a typed @ opens it above the box, filtered by the text after the @, the keys
// staying in the box (ComposerPanelHandle). An empty query lists "Add" (files or images, Plan mode where the session has
// modes) and a few of each resource; a typed one every match in one list. The resources are Persona's (iOS
// PromptComposer.swift): files this chat's tools touched and the session's folder (a query with a slash browses that folder
// on the session's computer: `src/` lists src), the hub's workspaces in the Persona assistant, other sessions. Picking a
// folder keeps browsing, a file types `@path `, a workspace or a session becomes a context chip (Composer.tsx).
import { useEffect, useImperativeHandle, useMemo, useRef, useState, type Ref } from "react";
import { api, enc } from "../../lib/api";
import { stampedHeaders } from "../../lib/manager-choice";
import { PickerMenu, type PickerButton } from "./PickerMenu";
import { useSkillCatalog, type CatalogMemory, type SkillCatalogState } from "./useSkillCatalog";
import { SkillDetail, SkillRow } from "./SkillRow";
import { SkillCatalogStatus } from "./SkillCatalogStatus";
import type { FsListing } from "../../lib/files";
import { displayTitle } from "../../lib/mentions";
import { browseTarget, byPrefix, nameMatches, otherSessions, pageSessions, relPath, sessionDetail, sessionQuote } from "../../lib/mentions-panel";
import type { RepositoryMentionRow } from "../../lib/mentions-repositories";
import { modeTitle } from "../../lib/session-actions";
import type { Session } from "../../lib/types";
import type { Workspace } from "../../lib/workspace-board";
import { useStore } from "../../lib/store";
import { useCloseOnBack } from "../../lib/back-close";
import { hasFinePointer, NARROW, useMedia } from "../../lib/viewport";
import { useComposition } from "../../lib/ime";
import { Icon, type IconName } from "../../ui/icons";
import { t } from "../../i18n";
import { matchingSkills, type MentionSkill, type SkillSource } from "../../lib/skill-mentions";

export type { SkillCatalogState };

export interface MentionDirectoryRequest {
  machineId: string;
  dir: string;
  cwd?: string;
  taskFiles: boolean;
  fileManager?: string;
}

export type MentionDirectoryLoader = (
  request: MentionDirectoryRequest,
  signal: AbortSignal,
) => Promise<FsListing | null>;

export interface MentionRowsOptions {
  /** Explicit session candidates; skips reading `useStore` when supplied via `ComposerPanel`. */
  sessions?: readonly Session[];
  /** Static directory listings keyed by folder path; skips fetching for provided paths (and skips default `api` when `loadDirectory` is absent). */
  directoryListings?: Readonly<Record<string, FsListing | null>>;
  /** Custom directory listing loader replacing `GET /machines/:mid/fs`. */
  loadDirectory?: MentionDirectoryLoader;
}

export interface ComposerPanelProps {
  /** "plus": opened from the + with its own search box; "at": a typed @, filtered by `query`. */
  from: "plus" | "at";
  query?: string;
  source?: MentionSource;
  machineId?: string;
  /** The Add files row: files and images where the session has a computer, images only elsewhere; none when the box is off. */
  attach?: "files" | "images";
  /** The Plan mode check row, where the session has modes. */
  plan?: { on: boolean };
  onPick: (pick: PanelPick) => void;
  /** Esc, Backspace in an empty search, a click outside: `refocus` puts the caret back in the message box. */
  onClose: (refocus: boolean) => void;
  handle?: Ref<ComposerPanelHandle>;
  memory?: CatalogMemory;
  assistant?: AssistantMentions;
  /** Explicit mention rows; skips `useStore`, `/fs` reads, and default skill-catalog reads when supplied. */
  rows?: MentionRow[];
  /** Explicit loading state (used with controlled `rows`, or combined with internal loading). */
  loading?: boolean;
  /** Explicit session candidates; skips reading `useStore` when supplied. */
  sessions?: readonly Session[];
  /** Static directory listings keyed by folder path; skips `/fs` reads when supplied. */
  directoryListings?: Readonly<Record<string, FsListing | null>>;
  /** Custom directory listing loader replacing `GET /machines/:mid/fs` when `rows` is omitted. */
  loadDirectory?: MentionDirectoryLoader;
  /** Explicit skill catalog and status; skips `useSkillCatalog` when supplied. */
  skillCatalog?: SkillCatalogState;
  /** Controlled search value for `from="plus"`. */
  search?: string;
  /** Called when the `from="plus"` search value changes. */
  onSearchChange?: (search: string) => void;
  /** Whether a narrow-screen Back gesture closes the `from="plus"` panel; defaults to true. */
  closeOnBack?: boolean;
}

export interface MentionSource {
  /** The session's folder: relative paths and the first listing start here. */
  cwd?: string;
  /** This chat's draft key ("mid/sid"), left out of the sessions. */
  self?: string;
  /** Restrict directory browsing to inputs supported by a scoped file source. */
  allowsFileQuery?: (query: string) => boolean;
  /** Scheduled prompts have no live session to quote. */
  sessions?: boolean;
  /** Stable file references use the same execution context as the task. */
  referenceFiles?: { machine: string; cwd: string; launcher: string; runtime: string };
  /** The manager that resolved the task's file selection scope. */
  fileManager?: string;
  /** Absolute paths the chat's tools read or wrote, newest first (read when the panel opens). */
  files: () => string[];
  /** Offer the hub's workspaces (GET /workspaces): the Persona assistant, which works on any computer. */
  workspaces?: boolean;
  /** Offer the assistant's bound repositories (GET /assistant/repositories); string names the manager for X-Persona-Manager. */
  repositories?: string | boolean;
  /** Execution catalog. Ordinary Agents use session-owned facts; environment changes invalidate visible rows. */
  skills?: SkillSource;
}

export type MentionRow =
  | { kind: "skill"; id: string; skill: MentionSkill }
  | { kind: "file"; id: string; name: string; detail: string; insert: string; dir: boolean }
  | { kind: "workspace"; id: string; name: string; detail: string; workspace: Workspace; toContext: () => { title: string; url: string; text: string; attrs: Record<string, string> } }
  | RepositoryMentionRow
  | { kind: "session"; id: string; name: string; detail: string; session: Session; toContext: () => { title: string; url: string; text: string } };

/** The panel's own rows: the box's file picker and the session's Plan mode. */
export type PanelAction = "attach" | "plan";
export type PanelPick = { kind: "action"; action: PanelAction } | { kind: "row"; row: MentionRow } | { kind: "browse"; insert: string };

export interface ComposerPanelHandle {
  /** A key pressed in the message box while the panel is open from a typed @ (↑ ↓ move, Enter / Tab pick): true when the
   *  panel took it, so the box does nothing else with it. */
  key: (key: string) => boolean;
}

type Entry = { kind: "action"; action: PanelAction; label: string; icon: IconName; hint?: string; checked?: boolean } | { kind: "row"; row: MentionRow };

const NO_SESSIONS: Record<string, Session[]> = {};
const NO_ROWS: Session[] = [];
const EMPTY_CATALOG: SkillCatalogState = {};
const MAX_ROWS = 40;
/** An empty query shows this many of each resource (cindy's EMPTY_QUERY_CONTEXT_SECTION_LIMIT); typing finds the rest. */
const EMPTY_PER_GROUP = 3;

export interface AssistantMentions {
  rows: (query: string) => MentionRow[];
  browsingRows?: (query: string) => MentionRow[];
  loading: boolean;
}

function useMentionRowsCore(
  query: string,
  source: MentionSource | undefined,
  machineId: string | undefined,
  candidateSessions: readonly Session[],
  skills?: MentionSkill[],
  assistant?: AssistantMentions,
  options?: Pick<MentionRowsOptions, "directoryListings" | "loadDirectory">,
): { rows: MentionRow[]; loading: boolean } {
  // Composer remounts this panel when the execution environment changes.
  const cache = useRef(new Map<string, FsListing | null>());
  const [, setTick] = useState(0);
  const requestSeq = useRef(0);
  const [loadingReq, setLoadingReq] = useState<{ key: string; seq: number } | null>(null);
  const files = useMemo(() => source?.files() ?? [], [source]);

  const directoryListings = options?.directoryListings;
  const loadDirectory = options?.loadDirectory;
  const loadDirRef = useRef(loadDirectory);
  useEffect(() => {
    loadDirRef.current = loadDirectory;
  }, [loadDirectory]);

  const cwd = source?.cwd;
  const target = source ? browseTarget(query, cwd) : null;
  const taskFiles = Boolean(source?.referenceFiles);
  const fileManager = taskFiles ? source?.fileManager : undefined;
  const dir = !source || !machineId || source.allowsFileQuery?.(query) === false ? null : target ? target.dir : cwd ?? null;
  const hasStaticListings = directoryListings !== undefined;
  const hasStaticDir = Boolean(dir && directoryListings && Object.prototype.hasOwnProperty.call(directoryListings, dir));
  const staticListing = hasStaticDir && dir ? directoryListings![dir] : undefined;
  const hasLoadDirectory = Boolean(loadDirectory);
  const cacheKey = dir
    ? JSON.stringify([hasLoadDirectory ? "custom" : "default", taskFiles ? fileManager ?? "" : "", machineId ?? "", taskFiles ? cwd ?? "" : "", dir])
    : null;
  const skipFetch = !dir || !machineId || !cacheKey || hasStaticDir || (hasStaticListings && !hasLoadDirectory) || cache.current.has(cacheKey);

  useEffect(() => {
    if (skipFetch || !dir || !machineId || !cacheKey) {
      requestSeq.current += 1;
      setLoadingReq(null);
      return;
    }
    let live = true;
    const seq = ++requestSeq.current;
    const ac = new AbortController();
    const timer = setTimeout(() => {
      if (!live || ac.signal.aborted || requestSeq.current !== seq) return;
      setLoadingReq({ key: cacheKey, seq });
      let request: Promise<FsListing | null>;
      try {
        request = loadDirRef.current
          ? Promise.resolve(loadDirRef.current({ machineId, dir, cwd, taskFiles, fileManager }, ac.signal))
          : api<FsListing>(
              `/machines/${enc(machineId)}/fs?path=${enc(dir)}&cwd=${enc(cwd ?? "")}${taskFiles ? "&task_input=1" : ""}`,
              { signal: ac.signal, headers: stampedHeaders(fileManager) },
            );
      } catch {
        request = Promise.resolve(null);
      }
      request
        .then((l) => {
          if (!live || ac.signal.aborted || requestSeq.current !== seq) return;
          cache.current.set(cacheKey, l ?? null);
        })
        .catch(() => {
          if (!live || ac.signal.aborted || requestSeq.current !== seq) return;
          cache.current.set(cacheKey, null); // not a folder / unreadable: no rows from it
        })
        .finally(() => {
          if (!live || ac.signal.aborted || requestSeq.current !== seq) return;
          setLoadingReq((cur) => (cur?.seq === seq ? null : cur));
          setTick((n) => n + 1);
        });
    }, 120);
    return () => {
      live = false;
      clearTimeout(timer);
      ac.abort();
      setLoadingReq((cur) => (cur?.seq === seq ? null : cur));
    };
  }, [skipFetch, cacheKey, dir, machineId, cwd, taskFiles, fileManager, hasLoadDirectory]);

  const listing = hasStaticDir ? staticListing : cacheKey ? cache.current.get(cacheKey) : undefined;
  const rows = useMemo<MentionRow[]>(() => {
    if (!source) return [];
    if (target) {
      const entries = (listing?.entries ?? []).filter((e) => nameMatches(e.name, target.filter));
      const ordered = [...byPrefix(entries.filter((e) => e.dir), (e) => e.name, target.filter), ...byPrefix(entries.filter((e) => !e.dir), (e) => e.name, target.filter)];
      const fileRows: MentionRow[] = ordered.slice(0, MAX_ROWS).map((e) => ({
        kind: "file",
        id: `fs:${target.typed}${e.name}`,
        name: e.dir ? `${e.name}/` : e.name,
        detail: target.typed,
        insert: `@${target.typed}${e.name}${e.dir ? "/" : " "}`,
        dir: e.dir,
      }));
      const repoRows = assistant?.browsingRows ? assistant.browsingRows(query) : [];
      return [...fileRows, ...repoRows].slice(0, MAX_ROWS);
    }
    const q = query.toLowerCase();
    const out: MentionRow[] = [];
    const used = new Set<string>();
    for (const p of files) {
      const rel = relPath(p, cwd);
      if (q && !rel.toLowerCase().includes(q)) continue;
      used.add(rel);
      const slash = rel.lastIndexOf("/");
      out.push({ kind: "file", id: `used:${p}`, name: rel.slice(slash + 1), detail: slash > 0 ? rel.slice(0, slash) : t("chat-composerpanel.used_files"), insert: `@${rel} `, dir: false });
    }
    const top = (listing?.entries ?? []).filter((e) => nameMatches(e.name, q) && !used.has(e.name));
    for (const e of [...byPrefix(top.filter((x) => x.dir), (x) => x.name, q), ...byPrefix(top.filter((x) => !x.dir), (x) => x.name, q)].slice(0, 12)) {
      out.push({ kind: "file", id: `top:${e.name}`, name: e.dir ? `${e.name}/` : e.name, detail: e.dir ? t("chat-composerpanel.folder") : t("chat-composerpanel.session_dir"), insert: `@${e.name}${e.dir ? "/" : " "}`, dir: e.dir });
    }
    if (assistant) {
      out.push(...assistant.rows(query));
    }
    for (const s of source.sessions === false ? [] : otherSessions([...candidateSessions], source.self, q)) {
      out.push({ kind: "session", id: `session:${s.machine_id}/${s.session_id}`, name: displayTitle(s), detail: sessionDetail(s), session: s, toContext: () => sessionQuote(s) });
    }
    // A large skill inventory must not crowd the existing resource groups out of the empty panel.
    const matches: MentionRow[] = matchingSkills(skills ?? [], q).map(skill => ({ kind: "skill", id: `skill:${skill.id}`, skill }));
    return [...matches, ...(q.trim() ? out : firstOfEach(out)).slice(0, MAX_ROWS)];
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query, source, listing, candidateSessions, cwd, files, assistant, skills]);

  const loading = Boolean(cacheKey && loadingReq?.key === cacheKey && !listing);
  return { rows, loading: Boolean(loading || assistant?.loading) };
}

/** The rows for what is typed; the folder listings come from the session's computer (`/fs`), read once per folder while
 *  the panel is open. */
export function useMentionRows(
  query: string,
  source: MentionSource | undefined,
  machineId: string | undefined,
  skills?: MentionSkill[],
  assistant?: AssistantMentions,
  options?: MentionRowsOptions,
): { rows: MentionRow[]; loading: boolean } {
  const wantStore = options?.sessions === undefined && Boolean(source && source.sessions !== false);
  const sessions = useStore((s) => (wantStore ? s.sessions : NO_SESSIONS));
  // A paging hub keeps unopened Agents in the list's rows.
  const listed = useStore((s) => (wantStore ? s.sessionSummaries : NO_ROWS));
  const candidateSessions = useMemo(
    () => (options?.sessions !== undefined ? options.sessions : wantStore ? pageSessions(sessions, listed) : NO_ROWS),
    [options?.sessions, wantStore, sessions, listed],
  );
  return useMentionRowsCore(query, source, machineId, candidateSessions, skills, assistant, options);
}

/** With an empty query, a few of each resource: the files used here, the folder's first entries, workspaces, sessions. */
function firstOfEach(rows: MentionRow[]): MentionRow[] {
  const seen = new Map<string, number>();
  return rows.filter((r) => {
    const group = r.id.split(":", 1)[0];
    const n = seen.get(group) ?? 0;
    seen.set(group, n + 1);
    return n < EMPTY_PER_GROUP;
  });
}

export function ComposerPanel(props: ComposerPanelProps) {
  if (props.rows === undefined && props.skillCatalog === undefined && Boolean(props.source?.skills)) {
    return <DefaultSkillCatalogBridge {...props} />;
  }
  return (
    <ComposerPanelWithCatalog
      {...props}
      resolvedCatalog={props.skillCatalog ?? EMPTY_CATALOG}
      hasSkillStatus={props.skillCatalog !== undefined}
    />
  );
}

function DefaultSkillCatalogBridge(props: ComposerPanelProps) {
  const catalog = useSkillCatalog(props.source?.skills, props.machineId, props.source?.cwd, props.memory);
  return <ComposerPanelWithCatalog {...props} resolvedCatalog={catalog} hasSkillStatus />;
}

interface ComposerPanelWithCatalogProps extends ComposerPanelProps {
  resolvedCatalog: SkillCatalogState;
  hasSkillStatus: boolean;
}

function ComposerPanelWithCatalog(props: ComposerPanelWithCatalogProps) {
  const [innerSearch, setInnerSearch] = useState("");
  const currentSearch = props.search ?? innerSearch;
  const updateSearch = (next: string) => {
    if (props.search === undefined) setInnerSearch(next);
    props.onSearchChange?.(next);
  };
  const q = props.from === "plus" ? currentSearch : (props.query ?? "");
  const skillStatus = props.hasSkillStatus ? props.resolvedCatalog.status : undefined;

  if (props.rows !== undefined) {
    return (
      <ComposerPanelView
        {...props}
        q={q}
        currentSearch={currentSearch}
        updateSearch={updateSearch}
        resolvedRows={props.rows}
        resolvedLoading={Boolean(props.loading)}
        skillStatus={skillStatus}
      />
    );
  }
  if (props.sessions !== undefined || !props.source || props.source.sessions === false) {
    return (
      <ControlledMentionRowsBridge
        {...props}
        q={q}
        currentSearch={currentSearch}
        updateSearch={updateSearch}
        candidateSessions={props.sessions ?? NO_ROWS}
        skillStatus={skillStatus}
      />
    );
  }
  return (
    <StoreMentionRowsBridge
      {...props}
      q={q}
      currentSearch={currentSearch}
      updateSearch={updateSearch}
      skillStatus={skillStatus}
    />
  );
}

interface MentionRowsBridgeProps extends ComposerPanelWithCatalogProps {
  q: string;
  currentSearch: string;
  updateSearch: (next: string) => void;
  skillStatus: SkillCatalogState["status"];
}

function StoreMentionRowsBridge(props: MentionRowsBridgeProps) {
  const sessions = useStore((s) => s.sessions);
  const listed = useStore((s) => s.sessionSummaries);
  const candidateSessions = useMemo(() => pageSessions(sessions, listed), [sessions, listed]);
  return <ControlledMentionRowsBridge {...props} candidateSessions={candidateSessions} />;
}

function ControlledMentionRowsBridge({
  candidateSessions,
  ...props
}: MentionRowsBridgeProps & { candidateSessions: readonly Session[] }) {
  const { rows, loading } = useMentionRowsCore(
    props.q,
    props.source,
    props.machineId,
    candidateSessions,
    props.resolvedCatalog.catalog?.skills,
    props.assistant,
    { directoryListings: props.directoryListings, loadDirectory: props.loadDirectory },
  );
  return (
    <ComposerPanelView
      {...props}
      resolvedRows={rows}
      resolvedLoading={Boolean(loading || props.loading)}
    />
  );
}

interface ComposerPanelViewProps extends MentionRowsBridgeProps {
  resolvedRows: MentionRow[];
  resolvedLoading: boolean;
}

function ComposerPanelView({
  from,
  source,
  attach,
  plan,
  onPick,
  onClose,
  handle,
  closeOnBack = true,
  hasSkillStatus,
  q,
  currentSearch,
  updateSearch,
  resolvedRows: rows,
  resolvedLoading: loading,
  skillStatus,
}: ComposerPanelViewProps) {
  const searchLabel = t(source?.skills || hasSkillStatus ? "chat-composerpanel.search_skills" : "chat-composerpanel.search");
  const browsing = Boolean(source && browseTarget(q, source.cwd));
  const empty = !q.trim();

  const entries = useMemo<Entry[]>(() => {
    const actions: Extract<Entry, { kind: "action" }>[] = [];
    if (attach) {
      actions.push({ kind: "action", action: "attach", label: t(attach === "files" ? "chat-composerpanel.add_files" : "chat-composerpanel.add_images"), icon: "clip", hint: attach === "files" ? t("chat-composerpanel.add_files_hint") : undefined });
    }
    if (plan) actions.push({ kind: "action", action: "plan", label: modeTitle("plan"), icon: "list", checked: plan.on });
    const resources: Entry[] = rows.map((row) => ({ kind: "row", row }));
    if (browsing) return resources;
    if (empty) return [...actions, ...resources];
    // a typed query is a resource first: an Add row comes after them, and only when a word of it starts with the query,
    // so `@a` + Enter picks api.ts rather than opening the file picker
    const lq = q.trim().toLowerCase();
    const starts = (label: string) => {
      const l = label.toLowerCase();
      return l.startsWith(lq) || l.split(/[\s…]+/).some((w) => w.startsWith(lq));
    };
    return [...resources, ...actions.filter((a) => starts(a.label))];
  }, [attach, plan, rows, empty, browsing, q]);

  const itemKey = (e: Entry) => e.kind === "action" ? e.action : e.row.id;
  const [selection, setSelection] = useState<{ query: string; id: string }>();
  const current = Math.max(0, selection?.query === q ? entries.findIndex(e => itemKey(e) === selection.id) : 0);
  const setActive = (index: number) => setSelection({ query: q, id: itemKey(entries[index]) });
  const selected = entries[current];
  const skill = selected?.kind === "row" && selected.row.kind === "skill" ? selected.row.skill : undefined;

  const browse = (insert: string) => {
    if (from === "plus") updateSearch(insert.slice(1));
    else onPick({ kind: "browse", insert });
  };
  const choose = (e: Entry) => {
    if (e.kind === "action") return onPick({ kind: "action", action: e.action });
    // Scheduled file inputs accept regular files within the execution folder.
    if (source?.referenceFiles && e.row.kind === "file" && e.row.dir) return browse(e.row.insert);
    onPick({ kind: "row", row: e.row });
  };
  const key = (k: string): boolean => {
    if (!entries.length) return false;
    if (k === "ArrowDown" || k === "ArrowUp") {
      setActive((current + (k === "ArrowDown" ? 1 : entries.length - 1)) % entries.length);
      return true;
    }
    if (k === "Enter" || k === "Tab") {
      choose(entries[current]);
      return true;
    }
    const folder = selected?.kind === "row" && selected.row.kind === "file" && selected.row.dir ? selected.row : null;
    if (k === "ArrowRight" && folder) {
      browse(folder.insert);
      return true;
    }
    return false;
  };
  useImperativeHandle(handle, () => ({ key }));

  // from the +: a press anywhere else closes it (the + itself toggles it). pointerdown, so a touch closes it too: it covers
  // the composer, and a phone's tap sends no mousedown when the page handles the touch
  const root = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (from !== "plus") return;
    const down = (e: Event) => {
      const target = e.target as Element | null;
      if (root.current?.contains(target) || target?.closest?.("[data-composer-plus]")) return;
      onClose(false);
    };
    document.addEventListener("pointerdown", down);
    return () => document.removeEventListener("pointerdown", down);
  }, [from, onClose]);
  // on a narrow screen Back closes it too, staying in the chat
  const narrow = useMedia(NARROW);
  useCloseOnBack(from === "plus" && closeOnBack, () => onClose(false), narrow);

  // the search box takes the keys at once; not on a touch screen alone, where the focus would raise the keyboard over the rows
  const searchBox = useRef<HTMLInputElement>(null);
  const ime = useComposition();
  useEffect(() => {
    if (from === "plus" && hasFinePointer()) searchBox.current?.focus({ preventScroll: true });
  }, [from]);

  const group = (e: Entry) => {
    if (e.kind === "action") return t("chat-composerpanel.add");
    return t(({ skill: "chat-composerpanel.skills", file: "chat-composerpanel.files",
      session: "chat-composerpanel.sessions", workspace: "chat-composerpanel.workspaces",
      repository: "chat-composerpanel.repositories" } as const)[e.row.kind]);
  };
  const row = (e: Entry, common: PickerButton) => {
    if (e.kind === "action") {
      return (
        <button key={`action:${e.action}`} {...common} data-panel-action={e.action} aria-checked={e.checked === undefined ? undefined : e.checked} title={e.hint} className="relative flex h-9 w-full items-center gap-2 rounded-control px-2.5 text-left text-sm text-ink [@media(pointer:coarse)]:h-11">
          <Icon name={e.icon} size={14} stroke={2} className="shrink-0" />
          <span className="min-w-0 flex-1 truncate">{e.label}</span>
          {e.checked && <Icon name="check" size={13} stroke={2.5} className="shrink-0 text-ink" />}
        </button>
      );
    }
    const r = e.row;
    if (r.kind === "skill") return <SkillRow skill={r.skill} common={common} />;
    const folder = r.kind === "file" && r.dir;
    return (
      <button key={r.id} {...common} title={r.detail ? `${r.name} · ${r.detail}` : r.name} className="relative flex h-9 w-full items-center gap-2 rounded-control px-2.5 text-left text-sm text-ink [@media(pointer:coarse)]:h-11"
        aria-keyshortcuts={folder ? "ArrowRight" : undefined}
        onClick={(event) => {
          if (folder && (event.target as Element).closest("[data-browse-folder]")) browse(r.insert);
          else common.onClick?.(event);
        }}>
        <Icon name={r.kind === "session" ? "chat" : r.kind === "repository" ? "git" : r.kind === "workspace" || r.dir ? "folder" : "file"} size={16} className="shrink-0 text-ink-3" />
        <span className="min-w-0 flex-1 truncate">{r.name}</span>
        {r.detail && <span className="ml-auto max-w-[min(240px,50%)] shrink-0 truncate pl-2 text-[12px] text-ink-3">{r.detail}</span>}
        {folder && <span data-browse-folder title={t("chat-composerpanel.browse_folder")} aria-hidden className="-mr-2 flex h-full w-11 shrink-0 items-center justify-center rounded-control text-ink-3 hover:bg-hover-2"><Icon name="chevronRight" size={14} /></span>}
      </button>
    );
  };

  return (
    <PickerMenu root={root} mode={from} label={t("chat-composerpanel.label")}
      items={entries} current={current} itemKey={itemKey} group={group} onHover={setActive} onPick={choose}
      renderRow={row} title={skill?.name} detail={skill && <SkillDetail skill={skill} />}
      onBlur={(e) => {
        if (from === "plus" && !root.current?.contains(e.relatedTarget as Node | null)) onClose(false);
      }}
      header={from === "plus" && (
        <div className="relative shrink-0 px-1 pb-1.5">
          <Icon name="search" size={12} stroke={2} className="pointer-events-none absolute top-1/2 left-3.5 -translate-y-1/2 text-ink-3" />
          <input
            ref={searchBox}
            type="text"
            value={currentSearch}
            data-composer-panel-search
            aria-label={searchLabel}
            placeholder={searchLabel}
            onChange={(e) => updateSearch(e.target.value)}
            {...ime.handlers}
            onBlur={ime.onBlur}
            onKeyDown={(e) => {
              if (ime.busy(e.nativeEvent)) {
                // the IME's own Escape (cancelling a candidate) must not reach ChatView's interrupt on the window
                if (e.key === "Escape") e.stopPropagation();
                return;
              }
              if (e.key === "Escape" || (e.key === "Backspace" && !currentSearch)) {
                // closing the panel only: kept from ChatView's interrupt on the window
                e.preventDefault();
                e.stopPropagation();
                onClose(true);
                return;
              }
              if (e.key !== "Tab" && key(e.key)) e.preventDefault();
            }}
            className="h-8 w-full truncate rounded-full border border-line bg-transparent pr-2 pl-7 text-[13px] text-ink outline-none placeholder:text-ink-3 focus:border-line-strong [@media(pointer:coarse)]:text-[16px]"
          />
        </div>
      )}
      empty={!entries.length && skillStatus !== "loading" && (
        <div className="flex h-9 items-center justify-center text-meta text-ink-3">
          {loading ? t("chat-composerpanel.reading_folder") :
            browsing ? t("chat-composerpanel.no_match_in_folder") : t("chat-composerpanel.no_match")}
        </div>
      )}
      footer={<><SkillCatalogStatus status={skillStatus} />
        {from === "at" && empty && entries.length > 0 && <div className="mt-1 shrink-0 border-t border-line px-2.5 pt-1.5 pb-1 text-xs text-ink-3">{searchLabel}</div>}
      </>} />
  );
}
