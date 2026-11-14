import { storedValue, storeValue } from "../../lib/compatibility";
// A folder of a computer (docs/apple/ios.md "Information architecture", iOS FilesPane): it starts at the session's folder
// (or `path`; the computer page: the home) and goes anywhere (#5827): Back, Forward, Up and the home beside a path bar that
// names every folder from the home or "/", Find for names under the folder shown, Info for an item's details and a
// symlink's target. Folders first with size, time and Git marks; hidden files off unless asked for. A file opens in the
// viewer inside the panel (back returns to the list where it was). On a wide screen the folder opens in place as a tree
// instead (files/FilesTree.tsx, Moshi's form; the menu switches back); either view refreshes itself while shown
// (lib/fs-live.ts). Both views change files too (files/FileOps.tsx, #4883): files dropped or chosen upload into the
// folder, and a row's menu (right click, its ⋯) downloads, renames, moves or moves it to the trash; "Select" picks several
// for one of those. Folders outside the home are read-only.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { api, enc, errorText, isAbort } from "../../lib/api";
import { t } from "../../i18n";
import { entryPayload, ENTRY_DRAG_TYPE } from "../../lib/entry-drag";
import { aheadBehind, dirName, fileName, gitMark, joinPath, type FsEntry, type FsListing, type GitStatus } from "../../lib/files";
import { historyBack, historyForward, historyGo, parentPath, pathSegments, writableHere, type FilesHistory } from "../../lib/files-nav";
import { hitFolder, hitName, nameEntries, searchNames, type NameHit } from "../../lib/files-find-name";
import { ago, bytes, sessionCan, shortPath } from "../../lib/format";
import { openPanel } from "../../lib/panel";
import { navigate } from "../../lib/router";
import { notePlace } from "../../lib/panel-memory";
import { FILE_MKDIR_FEATURE, FILE_WRITE_FEATURE } from "../../lib/file-ops";

/** New terminals take a folder (#5832). */
const PANE_CWD_FEATURE = "pane_cwd";
import { hubHas, useStore } from "../../lib/store";
import { useFsLive } from "../../lib/use-fs-live";
import { NARROW, useMedia } from "../../lib/viewport";
import { Button, copyText, cx, IconButton, Loading, Menu, toast, useMenu } from "../../ui/atoms";
import type { MenuAt, MenuItem } from "../../ui/menu-model";
import { Icon } from "../../ui/icons";
import { SearchField } from "../../ui/SearchField";
import { scrollBehavior } from "../../ui/stream/motion";
import { PanelHeader } from "../../ui/PanelHeader";
import { FileInfoDialog } from "./files/FileInfo";
import { FILE_CHMOD_FEATURE, PermissionsDialog } from "./files/Permissions";
import { NewItemDialog, type NewKind } from "./files/NewItem";
import { FileView } from "./files/FileView";
import { DropHint, dragHasFiles, RowCheck, rowClick, rowFrameProps, RowMore, SelectionBar, useFileOps, type OpTarget, type RowOps } from "./files/FileOps";
import { FilesTree } from "./files/FilesTree";
import { Empty, FileGlyph, GitDot, GitBadge, useStoredFlag } from "./files/parts";

type Listed = { at: string; listing?: FsListing; error?: string };

export function FilesPanel({ mid, sid, cwd, path, file, highlight: marked, onClose }: { mid: string; sid: string; cwd: string; path?: string; file?: string; highlight?: string; onClose: () => void }) {
  // Where it starts: `path` (a folder to open at) or the session's folder; Back / Forward walk what was shown since.
  const startDir = path || cwd;
  const [history, setHistory] = useState<FilesHistory>({ current: startDir, back: [], forward: [] });
  useEffect(() => setHistory({ current: startDir, back: [], forward: [] }), [startDir]);
  const dir = history.current;
  const go = useCallback((p: string) => setHistory((h) => historyGo(h, p)), []);
  const machine = useStore((s) => s.machines.find((m) => m.id === mid));
  const home = machine?.home;
  const [highlight, setHighlight] = useState(marked);
  useEffect(() => setHighlight(marked), [marked]); // the same panel asked to mark another file (Show in Files again)
  useEffect(() => {
    if (!highlight) return;
    const tm = setTimeout(() => setHighlight(undefined), 2500);
    return () => clearTimeout(tm);
  }, [highlight]);

  const [showHidden, setShowHidden] = useStoredFlag("persona.files.showHidden", false);
  // The tree is the wide screens' view, the drill-in the narrow ones'; the menu's choice wins over both.
  const narrow = useMedia(NARROW);
  const [viewPref, setViewPref] = useState<"tree" | "drill" | null>(() => {
    const v = storedValue("persona.files.view");
    return v === "tree" || v === "drill" ? v : null;
  });
  const tree = (viewPref ?? (narrow ? "drill" : "tree")) === "tree";
  const chooseView = (v: "tree" | "drill") => {
    storeValue("persona.files.view", v);
    setViewPref(v);
  };
  const [listed, setListed] = useState<Record<string, Listed>>({});
  const [reloads, setReloads] = useState(0);
  const [dirTick, setDirTick] = useState(0); // the folder shown changed on the computer (drill-in view)
  const [gitTick, setGitTick] = useState(0); // the folder's Git status did
  const [git, setGit] = useState<GitStatus | null>(null);
  const [open, setOpen] = useState<string[]>(file ? [file] : []); // files opened from the list, newest last
  // where it is, kept when the page leaves the session so coming back opens it here again (#5150)
  useEffect(() => notePlace({ mid, sid, cwd }, { path: dir === cwd ? undefined : dir, file: open.at(-1) }), [mid, sid, cwd, dir, open]);
  const menu = useMenu();
  const newMenu = useMenu();
  const features = useStore((s) => s.features);
  const [newKind, setNewKind] = useState<NewKind | null>(null);
  const [info, setInfo] = useState<{ path: string; entry?: FsEntry } | null>(null);
  const [perm, setPerm] = useState<{ path: string; mode?: number } | null>(null);
  const [query, setQuery] = useState("");
  useEffect(() => setQuery(""), [dir]);

  const [selection, setSelection] = useState<{ scope: object; items: Map<string, OpTarget> } | null>(null);
  const [rowMenu, setRowMenu] = useState<{ scope: object; x: OpTarget; at: MenuAt; open: () => void } | null>(null);
  const [dropDir, setDropDir] = useState<string | null>(null);
  const endSelect = useCallback(() => setSelection(null), []);
  useEffect(() => endSelect(), [dir, tree, endSelect]); // a selection is of the folder shown
  // Changing files: what changed is listed again (the live watch would too, a moment later), and a selection is done.
  const ops = useFileOps({
    mid,
    cwd,
    path: dir,
    workspace: JSON.stringify([startDir, path]),
    onChanged: () => {
      setReloads((n) => n + 1);
      endSelect();
    },
  });
  const selecting = selection?.scope === ops.scope;
  const selected = selecting ? selection.items : new Map<string, OpTarget>();
  const rowOps: RowOps = {
    selecting,
    isSelected: (p) => selected.has(p),
    toggle: (x) => {
      setSelection((before) => {
        const n = new Map(before?.scope === ops.scope ? before.items : []);
        if (n.has(x.path)) n.delete(x.path);
        else n.set(x.path, x);
        return { scope: ops.scope, items: n };
      });
    },
    menu: (x, at, open) => setRowMenu({ scope: ops.scope, x, at, open }),
  };
  useEffect(() => {
    if (!selecting) return;
    const key = (e: KeyboardEvent) => e.key === "Escape" && !document.querySelector("[role=dialog],[role=menu]") && endSelect();
    addEventListener("keydown", key);
    return () => removeEventListener("keydown", key);
  }, [selecting, endSelect]);

  useEffect(() => {
    if (!dir || tree) return;
    const ac = new AbortController();
    api<FsListing>(`/machines/${enc(mid)}/fs?path=${enc(dir)}&cwd=${enc(cwd)}`, { signal: ac.signal })
      .then((l) => setListed((m) => ({ ...m, [dir]: { at: dir, listing: l } })))
      .catch((e) => {
        if (!isAbort(e)) setListed((m) => ({ ...m, [dir]: { at: dir, listing: m[dir]?.listing, error: errorText(e) } }));
      });
    return () => ac.abort();
  }, [mid, cwd, dir, reloads, dirTick, tree]);

  useEffect(() => {
    if (!cwd) return;
    const ac = new AbortController();
    api<GitStatus>(`/machines/${enc(mid)}/git/status?cwd=${enc(cwd)}`, { signal: ac.signal })
      .then(setGit)
      .catch(() => {}); // marks are extra: without Git the list is the same
    return () => ac.abort();
  }, [mid, cwd, reloads, gitTick]);

  // The drill-in view watches the folder it shows; the tree watches the ones it has open (FilesTree). Both want Git.
  useFsLive(mid, cwd, tree || !dir ? [] : [dir], true, (c) => {
    if (c.git) setGitTick((n) => n + 1);
    if (!tree && dir && c.dirs.includes(dir)) setDirTick((n) => n + 1);
  });

  const here = listed[dir];
  // The folder as the computer names it (absolute) once listed; the home for "~" before that.
  const shown = here?.listing?.path ?? (dir === "~" && home ? home : dir.startsWith("~/") && home ? home + dir.slice(1) : dir);
  const sessionFolder = cwd.startsWith("/") ? cwd.replace(/\/+$/, "") || "/" : null;
  const writable = writableHere(shown, home);
  // permissions (#5831): where the computer takes changes, not on Windows
  const canChmod = writable && hubHas(features, FILE_CHMOD_FEATURE) && machine?.os?.toLowerCase() !== "windows";
  // Open Terminal Here (#5832): a new tab on the computer in that folder, in the session's workspace when it has one
  const canTerminal = hubHas(features, PANE_CWD_FEATURE);
  // A runtime engine's session (Paseo's) names a workspace with no terminal: the folder decides where the tab opens.
  const workspace = useStore((s) => { const x = s.sessions[mid]?.find((y) => y.session_id === sid); return x && sessionCan(x, "terminal") ? x.workspace_id : undefined; });
  const openTerminal = async (folder: string) => {
    try {
      const r = await api<{ terminal: string }>(`/machines/${enc(mid)}/panes`, { method: "POST", json: { cwd: folder, ...(workspace ? { workspace_id: workspace } : {}) } });
      navigate({ name: "terminal", mid, id: r.terminal, view: "terminals" });
    } catch (e) {
      toast(t("files-nav.terminal_failed", { error: errorText(e) }), "error");
    }
  };
  // A broken link says what it says (Info); a folder opens; a file opens in the viewer.
  const enter = (e: FsEntry) => (e.link_broken ? setInfo({ path: joinPath(shown, e.name), entry: e }) : e.dir ? go(joinPath(shown, e.name)) : setOpen([joinPath(shown, e.name)]));

  // A path the viewer links to: a folder already listed opens in the list, anything else in the viewer.
  const openPath = (p: string) => {
    if (listed[p]?.listing) {
      setOpen([]);
      go(p);
    } else setOpen((o) => [...o, p]);
  };
  // The viewer's Show in Files: back to the list, at the file's folder, the file marked.
  const showInFolder = (p: string) => {
    setOpen([]);
    go(dirName(p) || "/");
    setHighlight(fileName(p));
  };
  const goTo = (p: string, isDir: boolean) => (isDir ? go(p) : showInFolder(p));

  const entries = (here?.listing?.entries ?? []).filter((e) => showHidden || !e.name.startsWith("."));
  const changes = git?.is_repo ? git.files.length : 0;
  const sync = aheadBehind(git);
  const viewing = open[open.length - 1];
  // where an upload goes when no folder row is meant: the folder shown (the tree's root)
  const here_dir = writable ? dir : "";
  const finding = query.trim() !== "";

  return (
    <>
      <div className={cx("flex min-h-0 flex-1 flex-col", viewing && "hidden")}>
        <PanelHeader
          icon="folder"
          title={t("session-filespanel.title")}
          subtitle={shortPath(shown)}
          onClose={onClose}
          actions={
            <>
              <button
                type="button"
                title="Git"
                aria-label={(changes ? t("session-filespanel.git_changes", { count: changes }) : "Git") + (sync ? ` ${sync}` : "")}
                onClick={() => openPanel({ kind: "git", mid, sid, cwd })}
                className="flex h-7 shrink-0 items-center gap-1.5 rounded-[8px] px-2 text-xs font-medium whitespace-nowrap text-ink-2 transition-colors hover:bg-hover hover:text-ink"
              >
                <Icon name="git" size={14} />
                Git
                {sync && <span className="text-xs text-ink-3 tabular-nums">{sync}</span>}
                {changes > 0 && <span className="rounded-full bg-field px-1.5 text-xs text-ink-2 tabular-nums">{changes}</span>}
              </button>
              {here_dir && (hubHas(features, FILE_MKDIR_FEATURE) || hubHas(features, FILE_WRITE_FEATURE)) && (
                <IconButton icon="plus" label={t("files-new.new")} onClick={newMenu.open} active={Boolean(newMenu.anchor)} />
              )}
              {ops.canUpload && here_dir && <IconButton icon="upload" label={t("files-ops.upload")} onClick={() => ops.choose(here_dir)} />}
              <IconButton icon="more" label={t("session-filespanel.more")} onClick={menu.open} active={Boolean(menu.anchor)} />
            </>
          }
        />
        {menu.anchor && (
          <Menu
            anchor={menu.anchor}
            onClose={menu.close}
            align="right"
            items={[
              { key: "tree", label: t("files-tree.treeView"), checked: tree, onSelect: () => chooseView(tree ? "drill" : "tree") },
              { key: "hidden", label: t("session-filespanel.show_hidden"), checked: showHidden, onSelect: () => setShowHidden(!showHidden) },
              ...(canTerminal ? [{ key: "terminal", label: t("files-nav.open_terminal"), icon: "terminal" as const, onSelect: () => void openTerminal(shown) }] : []),
              { key: "info", label: t("files-info.info"), icon: "info", onSelect: () => setInfo({ path: shown }) },
              { key: "copy", label: t("session-filespanel.copy_path"), icon: "copy", onSelect: () => copyText(shown) },
              ...(sessionFolder && shown !== sessionFolder ? [{ key: "session", label: t("files-nav.session_folder"), icon: "folder" as const, onSelect: () => go(sessionFolder) }] : []),
              { key: "reload", label: t("session-filespanel.reload"), icon: "retry", onSelect: () => setReloads((n) => n + 1) },
              ...(ops.canUpload && here_dir ? [{ key: "upload", label: t("files-ops.upload"), icon: "upload" as const, divider: true, onSelect: () => ops.choose(here_dir) }] : []),
              { key: "select", label: t("files-ops.select"), checked: selecting, divider: !(ops.canUpload && here_dir), onSelect: () => (selecting ? endSelect() : setSelection({ scope: ops.scope, items: new Map() })) },
            ]}
          />
        )}
        {newMenu.anchor && (
          <Menu
            anchor={newMenu.anchor}
            onClose={newMenu.close}
            align="right"
            items={[
              ...(hubHas(features, FILE_MKDIR_FEATURE) ? [{ key: "folder", label: t("files-new.new_folder"), icon: "folder" as const, onSelect: () => setNewKind("folder") }] : []),
              ...(hubHas(features, FILE_WRITE_FEATURE) ? [{ key: "text", label: t("files-new.new_text"), icon: "file" as const, onSelect: () => setNewKind("text") }] : []),
              ...(ops.canUpload ? [{ key: "upload", label: t("files-ops.upload"), icon: "upload" as const, divider: true, onSelect: () => ops.choose(here_dir) }] : []),
            ]}
          />
        )}
        {rowMenu?.scope === ops.scope && (
          <Menu
            items={beforeTrash(ops.rowItems(rowMenu.x, rowMenu.open), [
              { key: "info", label: t("files-info.info"), icon: "info", divider: true, onSelect: () => setInfo({ path: rowMenu.x.path }) },
              ...(canChmod ? [{ key: "perm", label: t("files-perm.menu"), icon: "shield" as const, onSelect: () => setPerm({ path: rowMenu.x.path }) }] : []),
              ...(canTerminal && rowMenu.x.dir ? [{ key: "terminal", label: t("files-nav.open_terminal"), icon: "terminal" as const, onSelect: () => void openTerminal(rowMenu.x.path) }] : []),
            ])}
            anchor={rowMenu.at.anchor ?? null}
            point={rowMenu.at.point}
            onClose={() => setRowMenu((current) => current === rowMenu ? null : current)}
            align="right"
          />
        )}
        {selecting && <SelectionBar ops={ops} selected={[...selected.values()]} onClear={endSelect} />}
        <PathBar
          shown={shown}
          home={home}
          session={sessionFolder}
          writable={writable || !here?.listing}
          history={history}
          onBack={() => setHistory(historyBack)}
          onForward={() => setHistory(historyForward)}
          go={go}
        />
        <FindField folder={fileName(shown) || shown} value={query} onChange={setQuery} />
        <div
          className="relative flex min-h-0 flex-1 flex-col"
          // files from the computer upload into the folder under the pointer (a folder row, else the one shown)
          onDragOver={(e) => {
            if (!ops.canUpload || !here_dir || !dragHasFiles(e)) return;
            e.preventDefault();
            e.dataTransfer.dropEffect = "copy";
            const over = (e.target as HTMLElement).closest<HTMLElement>("[data-drop-dir]")?.dataset.dropDir;
            setDropDir(over ?? here_dir);
          }}
          onDragLeave={(e) => !e.currentTarget.contains(e.relatedTarget as Node | null) && setDropDir(null)}
          onDrop={(e) => {
            if (!dropDir) return;
            e.preventDefault();
            ops.upload(Array.from(e.dataTransfer.files), dropDir);
            setDropDir(null);
          }}
        >
        {dropDir && <DropHint dir={dropDir} />}
        <div className="flex min-h-0 flex-1 flex-col overflow-y-auto" key={finding ? "find" : tree ? `tree:${dir}` : dir}>
          {finding ? (
            <FindResults mid={mid} dir={shown} query={query} onOpen={(hit) => (hit.dir ? go(joinPath(shown, hit.path)) : setOpen([joinPath(shown, hit.path)]))} />
          ) : tree ? (
            dir ? (
              <FilesTree
                mid={mid}
                sid={sid}
                cwd={cwd}
                root={dir}
                reveal={[]}
                showHidden={showHidden}
                git={git}
                reloads={reloads}
                onOpenFile={(p) => setOpen([p])}
                onShowHidden={() => setShowHidden(true)}
                rowOps={rowOps}
              />
            ) : (
              <Empty icon="folder" title={t("session-filespanel.no_dir")} />
            )
          ) : here?.listing ? (
            <div className="py-1" style={{ animation: "fade-in 160ms ease-out both" }}>
              {entries.length === 0 && (
                <Empty
                  icon="folder"
                  title={here.listing.entries.length ? t("session-filespanel.only_hidden") : t("session-filespanel.empty_folder")}
                  action={here.listing.entries.length ? <Button onClick={() => setShowHidden(true)}>{t("session-filespanel.show_hidden")}</Button> : undefined}
                />
              )}
              {entries.map((e) => (
                <EntryRow key={e.name} mid={mid} e={e} full={joinPath(shown, e.name)} mark={gitMark(git, joinPath(shown, e.name), e.dir)} marked={highlight === e.name} onOpen={() => enter(e)} ops={rowOps} />
              ))}
              {here.listing.truncated && <p className="px-4 py-2 text-[11.5px] text-ink-3">{t("session-filespanel.truncated")}</p>}
            </div>
          ) : here?.error ? (
            <Empty icon="folder" title={t("session-filespanel.open_failed")} detail={here.error} action={<Button icon="retry" onClick={() => setReloads((n) => n + 1)}>{t("session-filespanel.retry")}</Button>} />
          ) : !dir ? (
            <Empty icon="folder" title={t("session-filespanel.no_dir")} />
          ) : (
            <div className="flex h-40 items-center justify-center">
              <Loading label={t("session-filespanel.reading")} />
            </div>
          )}
        </div>
        </div>
        {ops.ui}
      </div>
      {newKind && (
        <NewItemDialog
          mid={mid}
          cwd={cwd}
          dir={shown}
          folderName={fileName(shown) || shown}
          kind={newKind}
          onClose={() => setNewKind(null)}
          onMade={(p, kind) => {
            setReloads((n) => n + 1);
            setHighlight(fileName(p));
            if (kind === "text") setOpen([p]);
          }}
        />
      )}
      {info && (
        <FileInfoDialog
          mid={mid}
          cwd={cwd}
          path={info.path}
          entry={info.entry}
          onClose={() => setInfo(null)}
          onGoTo={goTo}
          onChangeMode={canChmod && info.path !== shown ? (mode) => { setPerm({ path: info.path, mode }); setInfo(null); } : undefined}
        />
      )}
      {perm && <PermissionsDialog mid={mid} cwd={cwd} path={perm.path} mode={perm.mode} onClose={() => setPerm(null)} onChanged={() => setReloads((n) => n + 1)} />}
      {viewing && (
        <FileView
          key={viewing}
          mid={mid}
          sid={sid}
          path={viewing}
          cwd={cwd}
          onBack={() => setOpen((o) => o.slice(0, -1))}
          onClose={onClose}
          onOpenPath={openPath}
          onShowInFolder={showInFolder}
        />
      )}
    </>
  );
}

/** The row menu with the panel's own items (Info…) before Move to Trash, which stays last (destructive last). */
function beforeTrash(items: MenuItem[], extra: MenuItem[]): MenuItem[] {
  const i = items.findIndex((x) => x.key === "trash");
  return i < 0 ? [...items, ...extra] : [...items.slice(0, i), ...extra, ...items.slice(i)];
}

/** Back, Forward, Up and the path from the home (a house) or "/", every folder a button; the session's folder marked. */
function PathBar({ shown, home, session, writable, history, onBack, onForward, go }: {
  shown: string;
  home?: string;
  session: string | null;
  writable: boolean;
  history: FilesHistory;
  onBack: () => void;
  onForward: () => void;
  go: (p: string) => void;
}) {
  const bar = useRef<HTMLDivElement>(null);
  const segments = pathSegments(shown, home);
  const up = parentPath(shown);
  useLayoutEffect(() => {
    bar.current?.scrollTo({ left: bar.current.scrollWidth, behavior: scrollBehavior() });
  }, [shown]);
  return (
    <div className="flex h-10 shrink-0 items-center gap-0.5 border-b border-line px-2">
      <IconButton icon="chevronLeft" label={t("files-nav.back")} onClick={onBack} disabled={!history.back.length} iconSize={16} />
      <IconButton icon="chevronRight" label={t("files-nav.forward")} onClick={onForward} disabled={!history.forward.length} iconSize={16} />
      <IconButton icon="up" label={t("session-filespanel.up")} onClick={() => up && go(up)} disabled={!up} iconSize={16} />
      <div ref={bar} className="ml-1 flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto scrollbar-none" aria-label={t("session-filespanel.location")}>
        {segments.map((s, i) => {
          const current = i === segments.length - 1;
          return (
            <span key={s.path} className="flex shrink-0 items-center gap-0.5">
              {i > 0 && <Icon name="chevronRight" size={11} stroke={2.2} className="text-ink-3" />}
              <button
                type="button"
                onClick={() => !current && go(s.path)}
                aria-current={current ? "location" : undefined}
                aria-label={s.home ? t("files-nav.home") : undefined}
                title={s.path}
                className={cx(
                  "flex h-6 items-center gap-1 rounded-full px-2 text-xs whitespace-nowrap transition-colors",
                  current ? "bg-field font-semibold text-ink" : "text-ink-2 hover:bg-hover hover:text-ink",
                )}
              >
                {s.home ? <Icon name="home" size={13} /> : s.title}
                {session === s.path && <span className="rounded-full bg-accent/15 px-1.5 text-xs font-medium text-accent">{t("files-nav.session")}</span>}
              </button>
            </span>
          );
        })}
        {!writable && (
          <span className="ml-1 flex shrink-0 items-center gap-1 rounded-full bg-field px-2 py-0.5 text-xs text-ink-2">
            <Icon name="lock" size={11} />
            {t("files-nav.read_only")}
          </span>
        )}
      </div>
    </div>
  );
}

/** Finds names under the folder shown (⌘F / Ctrl+F while the panel has the focus). */
function FindField({ folder, value, onChange }: { folder: string; value: string; onChange: (v: string) => void }) {
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() !== "f" || !(e.metaKey || e.ctrlKey) || e.shiftKey || e.altKey) return;
      const panel = box.current?.closest("aside");
      if (!panel || !panel.contains(document.activeElement)) return;
      e.preventDefault();
      box.current?.querySelector("input")?.focus();
    };
    addEventListener("keydown", key);
    return () => removeEventListener("keydown", key);
  }, []);
  return (
    <div ref={box} className="shrink-0 border-b border-line px-2 py-1.5">
      <SearchField value={value} onChange={onChange} placeholder={t("files-nav.find_in", { name: folder })} />
    </div>
  );
}

/** The folder's files are read once a minute at most (fs/find), the folders they are in added, matched as typed. */
const findLists = new Map<string, { at: number; hits: NameHit[]; truncated: boolean }>();

function FindResults({ mid, dir, query, onOpen }: { mid: string; dir: string; query: string; onOpen: (hit: NameHit) => void }) {
  const key = `${mid}\u0000${dir}`;
  const [list, setList] = useState(() => findLists.get(key));
  const [error, setError] = useState("");
  useEffect(() => {
    const cached = findLists.get(key);
    if (cached && Date.now() - cached.at < 60_000) {
      setList(cached);
      return;
    }
    const ac = new AbortController();
    setError("");
    api<{ files?: string[]; truncated?: boolean }>(`/machines/${enc(mid)}/fs/find?cwd=${enc(dir)}`, { signal: ac.signal })
      .then((r) => {
        const next = { at: Date.now(), hits: nameEntries(r.files ?? []), truncated: Boolean(r.truncated) };
        findLists.set(key, next);
        setList(next);
      })
      .catch((e) => !isAbort(e) && setError(errorText(e)));
    return () => ac.abort();
  }, [mid, dir, key]);
  const hits = useMemo(() => (list ? searchNames(list.hits, query) : []), [list, query]);
  if (error) return <Empty icon="search" title={t("files-nav.find_failed")} detail={error} />;
  if (!list) return <div className="flex h-40 items-center justify-center"><Loading label={t("files-nav.finding")} /></div>;
  return (
    <div className="py-1" data-testid="files-find">
      <p className="px-4 py-1.5 text-[11.5px] text-ink-3">{t("files-nav.found", { name: fileName(dir) || dir, count: hits.length })}</p>
      {hits.map((h) => (
        <button key={h.path} type="button" onClick={() => onOpen(h)} title={h.path} className="flex w-full items-center gap-3 px-4 py-2 text-left transition-colors hover:bg-hover">
          <FileGlyph name={hitName(h)} dir={h.dir} />
          <span className="min-w-0 flex-1">
            <span className="block truncate text-xs text-ink">{hitName(h)}</span>
            <span className="block truncate font-mono text-xs text-ink-3">{(hitFolder(h) || ".") + "/"}</span>
          </span>
          {h.dir && <Icon name="chevronRight" size={13} stroke={2.2} className="shrink-0 text-ink-3" />}
        </button>
      ))}
      {list.truncated && <p className="px-4 py-2 text-xs text-ink-3">{t("files-nav.find_truncated")}</p>}
    </div>
  );
}

function EntryRow({ mid, e, full, mark, marked, onOpen, ops }: { mid: string; e: FsEntry; full: string; mark: string | null; marked?: boolean; onOpen: () => void; ops?: RowOps }) {
  const hidden = e.name.startsWith(".");
  const x: OpTarget = { path: full, name: e.name, dir: e.dir };
  const picked = ops?.selecting && ops.isSelected(full);
  const row = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (marked) row.current?.scrollIntoView?.({ block: "center", behavior: scrollBehavior() });
  }, [marked]);
  return (
    <div
      ref={row}
      {...rowFrameProps(ops, x, onOpen)}
      className={cx("group/row relative flex items-center transition-colors hover:bg-hover", picked && "bg-hover", marked && "bg-accent/12")}
      // dragged into a terminal or the chat box it becomes `@path ` (lib/entry-drag.ts)
      draggable
      onDragStart={(ev) => {
        ev.dataTransfer.setData(ENTRY_DRAG_TYPE, entryPayload({ mid, path: full, dir: e.dir }));
        ev.dataTransfer.effectAllowed = "copy";
      }}
    >
      <RowCheck ops={ops} x={x} />
      <button type="button" onClick={rowClick(ops, x, onOpen)} className={cx("flex min-w-0 flex-1 items-center gap-3 py-2 pr-2 text-left", ops?.selecting ? "pl-3" : "pl-4")} title={full}>
        <FileGlyph name={e.name} dir={e.dir} />
        <span className="min-w-0 flex-1">
          <span className="flex items-center gap-1">
            <span className={cx("truncate text-xs", e.ignored ? "text-ink-3" : hidden ? "text-ink-2" : "text-ink")}>{e.name}</span>
            {e.symlink && (
              <span role="img" aria-label={e.link_broken ? t("files-nav.broken_link") : t("files-nav.link")} className="flex shrink-0">
                <Icon name={e.link_broken ? "alertCircle" : "link"} size={11} className={e.link_broken ? "text-red" : "text-ink-3"} />
              </span>
            )}
          </span>
          {e.link_broken ? (
            <span className="block truncate text-xs text-red">{t("files-nav.link_broken", { target: e.link_target ?? "" })}</span>
          ) : e.symlink && e.link_target ? (
            <span className="block truncate text-xs text-ink-3">{t("files-nav.link_to", { target: e.link_target })}</span>
          ) : (
            <span className="flex gap-2 text-xs whitespace-nowrap text-ink-3 tabular-nums">
              {!e.dir && <span>{bytes(e.size)}</span>}
              {e.mtime && <span title={new Date(e.mtime).toLocaleString("zh-CN")}>{ago(e.mtime)}</span>}
            </span>
          )}
        </span>
        {mark && (e.dir ? <GitDot letter={mark} /> : <GitBadge letter={mark} />)}
        {e.dir && <Icon name="chevronRight" size={13} stroke={2.2} className="shrink-0 text-ink-3" />}
      </button>
      {/* hover devices only: on a touch screen it would be an invisible target at the row's end */}
      <span className="hidden items-center pr-2 opacity-0 transition-opacity group-hover/row:opacity-100 focus-within:opacity-100 [@media(hover:hover)]:flex">
        <IconButton icon="copy" label={t("session-filespanel.copy_path")} onClick={() => copyText(full)} size={26} iconSize={13} />
        <RowMore ops={ops} x={x} open={onOpen} />
      </span>
    </div>
  );
}
