import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { CHANGES_TREE_INDENT, CHANGES_TREE_INDENT_MAX_SHARE, CHANGES_DIFF_MIN, CHANGES_TREE_DEFAULT, CHANGES_TREE_MIN, CHANGES_TREE_MAX, CHANGES_TREE_WIDTH_KEY } from "../../lib/layout";
import { TreePaneSplitter, useResizableTreeWidth } from "../../ui/TreePaneSplitter";
import { errorText } from "../../lib/api";
import { relativeTo } from "../../lib/comments";
import { below, toolFile } from "../../lib/files";
import { openPanel, type Panel } from "../../lib/panel";
import { REVIEW_FEATURE } from "../../lib/session-review";
import { hubHas, useStore } from "../../lib/store";
import { changePath, EDIT_TOOLS, turnChanges, type ChangedFile } from "../../lib/turn-changes";
import { exchangeSteps, segments, type Segment } from "../../lib/turns";
import { cx, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { PanelHeader } from "../../ui/PanelHeader";
import { t } from "../../i18n";
import { useDiffMode } from "../../lib/code-view";
import { useMedia } from "../../ui/media-query";
import { DiffBlocks, diffStats, editDiffs } from "../chat/ActionRow";
import { useSharedTranscript } from "../chat/transcript-share";
import type { Transcript } from "../chat/useTranscript";

type Opened = Extract<Panel, { kind: "changes" }>;
type Scope = "exchange" | "session";
type ChangeEdit = {
  id: string;
  call?: string;
  turn: string;
  seq: number;
  path: string;
  diffs: NonNullable<ReturnType<typeof editDiffs>>;
};

type TreeNode = { dirs: Map<string, TreeNode>; files: ChangedFile[] };

function treeOf(files: ChangedFile[]): TreeNode {
  const root: TreeNode = { dirs: new Map(), files: [] };
  for (const file of files) {
    const parts = file.shown.split(/[\\/]/).filter(Boolean);
    let node = root;
    for (const part of parts.slice(0, -1)) {
      if (!node.dirs.has(part)) node.dirs.set(part, { dirs: new Map(), files: [] });
      node = node.dirs.get(part)!;
    }
    node.files.push(file);
  }
  return root;
}

function FileTree({ root, collapsed, toggle, select, selected }: {
  root: TreeNode;
  collapsed: ReadonlySet<string>;
  toggle: (path: string) => void;
  select: (path: string) => void;
  selected?: string;
}) {
  function draw(node: TreeNode, prefix: string, depth: number): React.ReactNode {
    return (
      <>
        {[...node.dirs.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([name, child]) => {
          const path = `${prefix}${name}`;
          const closed = collapsed.has(path);
          return (
            <div key={path}>
              <button type="button" aria-expanded={!closed} onClick={() => toggle(path)} className="flex h-7 w-full items-center gap-1.5 rounded-md px-2 text-left text-tree-row font-medium text-ink-2 transition-colors hover:bg-hover [@media(pointer:coarse)]:min-h-11" style={{ paddingLeft: `${8 + depth * CHANGES_TREE_INDENT}px` }}>
                <Icon name="chevronRight" size={12} className="shrink-0 text-ink-3" style={{ transform: closed ? undefined : "rotate(90deg)" }} />
                <Icon name="folder" size={14} className="shrink-0 text-ink-3" />
                <span className="truncate">{name}</span>
              </button>
              {!closed && draw(child, `${path}/`, depth + 1)}
            </div>
          );
        })}
        {[...node.files].sort((a, b) => a.shown.localeCompare(b.shown)).map((file) => (
          <button key={file.path} type="button" title={file.path} data-change-file={file.path} aria-current={selected === file.path ? "true" : undefined} onClick={() => select(file.path)} className={cx("flex h-7 w-full items-center gap-1.5 rounded-md px-2 text-left text-tree-row text-ink-2 transition-colors hover:bg-hover [@media(pointer:coarse)]:min-h-11", selected === file.path && "bg-hover-2 font-medium text-ink")}>
            <span className="min-w-0 flex-1 overflow-hidden">
              <span className="flex items-center gap-1.5" style={{ paddingLeft: `min(${(depth + 1) * CHANGES_TREE_INDENT}px, ${CHANGES_TREE_INDENT_MAX_SHARE * 100}%)` }}>
                <Icon name="file" size={14} className="shrink-0 text-ink-3" />
                <span className="min-w-0 flex-1 truncate">{file.shown.split(/[\\/]/).pop()}</span>
              </span>
            </span>
            {file.exact && (
              <span className="shrink-0 whitespace-nowrap font-mono text-submeta tabular-nums">
                <span className="text-diff-add-fg">+{file.add}</span> <span className="text-diff-del-fg">-{file.del}</span>
              </span>
            )}
          </button>
        ))}
      </>
    );
  }
  return draw(root, "", 0);
}

function callIn(segment: Segment, call: string): boolean {
  return segment.steps.some((step) => step.kind === "tool" && (step.use.tool_use_id === call || step.use.id === call));
}

type ChangesPanelProps = { mid: string; sid: string; opened: Opened; onClose: () => void };

export function ChangesPanel(props: ChangesPanelProps) {
  const tr = useSharedTranscript(props.mid, props.sid);
  if (!tr) {
    return <div className="flex min-h-0 flex-1 items-center justify-center" data-changes-panel data-transcript-loading><Spinner size={18} /></div>;
  }
  return <ChangesPanelView {...props} tr={tr} />;
}

function ChangesPanelView({ mid, sid, opened, onClose, tr }: ChangesPanelProps & { tr: Transcript }) {
  const session = useStore((state) => (state.sessions[mid] ?? []).find((row) => row.session_id === sid));
  const cwd = session?.cwd;
  const reviews = useStore((state) => hubHas(state.features, REVIEW_FEATURE));
  const all = useMemo(() => segments(tr.items), [tr.items]);
  const call = opened.at?.call;
  const called = useMemo(() => (call ? all.find((segment) => callIn(segment, call)) : undefined), [all, call]);
  const resolvingCall = Boolean(call && !called);
  const candidates = resolvingCall
    ? all.filter((segment) => segment.folds.some((fold) => fold.fold?.changes?.some((file) => file.path && changePath(file.path, cwd) === opened.at?.path)))
    : [];
  const turnId = called?.id ?? (call ? undefined : opened.turn ?? all.findLast((segment) => turnChanges(exchangeSteps(segment), cwd))?.id ?? all.at(-1)?.id);
  const exchange = turnId ? all.find((segment) => segment.id === turnId) : undefined;
  const [scope, setScope] = useState<Scope>("exchange");
  const [diffMode, setDiffMode] = useDiffMode();
  const roomy = useMedia("(min-width: 1024px)");
  const split = diffMode === "split" && roomy;
  const shown = scope === "session" ? all : resolvingCall ? candidates : exchange ? [exchange] : [];
  const edits = useMemo(() => {
    const out: ChangeEdit[] = [];
    for (const segment of shown) {
      for (const step of exchangeSteps(segment)) {
        if (step.kind !== "tool" || !step.result || step.result.is_error || !EDIT_TOOLS.has(step.use.tool_name ?? "")) continue;
        const diffs = editDiffs(step.use);
        const named = toolFile(step.use.tool_name ?? "", step.use.tool_input)?.path;
        if (!diffs || !named) continue;
        out.push({ id: step.use.id, call: step.use.tool_use_id, turn: segment.id, seq: step.use.seq, path: changePath(named, cwd), diffs });
      }
    }
    return out;
  }, [shown, cwd]);
  const files = useMemo(() => {
    const map = new Map<string, ChangedFile>();
    const add = (file: ChangedFile) => {
      const had = map.get(file.path);
      if (had) {
        had.add += file.add;
        had.del += file.del;
        had.exact &&= file.exact;
      } else map.set(file.path, { ...file });
    };
    for (const segment of shown) {
      const counted = turnChanges(exchangeSteps(segment), cwd);
      if (counted) {
        for (const file of counted.files) add(file);
        continue;
      }
      // An old fold can withhold counts. Direct calls remain useful, but their totals are not claimed as complete.
      for (const edit of edits.filter((candidate) => candidate.turn === segment.id)) {
        const stats = diffStats(edit.diffs);
        add({ path: edit.path, shown: relativeTo(cwd, edit.path), ...stats, exact: !segment.folds.length });
      }
    }
    return [...map.values()];
  }, [shown, edits, cwd]);
  const selectedPath = files.some((file) => file.path === opened.at?.path) ? opened.at!.path : files[0]?.path ?? opened.at?.path;
  const selectedFile = files.find((file) => file.path === selectedPath);
  const selectedEdits = useMemo(() => edits.filter((edit) => edit.path === selectedPath), [edits, selectedPath]);
  const editSignature = edits.map((edit) => edit.id).join("|");
  const scopeKey = scope === "session" ? "session" : turnId ?? "exchange";
  const historyReady = !resolvingCall && (scope !== "session" || (!tr.hasMore && !tr.loadingEarlier));
  const [pending, setPending] = useState({ count: 0, path: "" });
  const previous = useRef<{ scope: string; ids: Set<string>; newest: number } | null>(null);
  const pendingLive = useRef(new Map<string, { path: string; seq: number }>());
  const previousCall = useRef(call);
  const diffBody = useRef<HTMLDivElement>(null);
  const scrolledCall = useRef<{ call: string; path?: string } | undefined>(undefined);
  const anchor = useRef<{ path?: string; first: string; top: number; scroll: number }>({ first: "", top: 0, scroll: 0 });
  const [foldRetry, setFoldRetry] = useState(0);
  const [foldLoading, setFoldLoading] = useState(false);
  const foldRequests = useRef(new Set<string>());
  const foldFailures = useRef(new Map<string, string>());
  const foldRetrying = useRef(new Set<string>());
  const [collapsed, setCollapsed] = useState<ReadonlySet<string>>(new Set());
  const treeId = useId();
  const columns = useRef<HTMLDivElement>(null);
  const treePane = useResizableTreeWidth({
    storageKey: CHANGES_TREE_WIDTH_KEY,
    defaultWidth: CHANGES_TREE_DEFAULT,
    minWidth: CHANGES_TREE_MIN,
    maxWidth: CHANGES_TREE_MAX,
    minViewerWidth: CHANGES_DIFF_MIN,
    containerRef: columns,
    observeKey: resolvingCall,
  });


  useEffect(() => {
    if (!opened.turn && turnId) openPanel({ ...opened, turn: turnId }, { replace: true });
  }, [opened.turn, turnId]);

  useEffect(() => {
    if (call && call !== previousCall.current) setScope("exchange");
    previousCall.current = call;
  }, [call]);

  useEffect(() => {
    if ((scope === "session" || (resolvingCall && !candidates.length)) && tr.hasMore && !tr.loadingEarlier && !tr.error) void tr.loadEarlier();
  }, [scope, resolvingCall, candidates.length, tr.hasMore, tr.loadingEarlier, tr.error, tr.loadEarlier]);

  const selectedFolds = shown.flatMap((segment) => segment.folds.filter((fold) => {
    if (!fold.fold?.changes?.some((file) => file.path && changePath(file.path, cwd) === selectedPath)) return false;
    // A subscription replay can bring the same fold back after its expanded items landed. Only a direct edit from
    // inside this fold proves it is still expanded; an edit elsewhere in the exchange does not.
    return foldFailures.current.has(fold.id) || !edits.some((edit) => edit.path === selectedPath && edit.seq >= fold.seq && edit.seq <= fold.fold!.through_seq);
  }));
  const foldKey = selectedFolds.map((fold) => fold.id).join("|");
  const foldError = selectedFolds.map((fold) => foldFailures.current.get(fold.id)).find(Boolean) ?? "";
  useEffect(() => {
    let active = true;
    const fresh = selectedFolds.filter((fold) => !foldRequests.current.has(fold.id) && (!foldFailures.current.has(fold.id) || foldRetrying.current.has(fold.id)));
    if (!fresh.length) {
      if (!selectedFolds.length) setFoldLoading(false);
      return () => { active = false; };
    }
    for (const fold of fresh) foldRequests.current.add(fold.id);
    setFoldLoading(true);
    const finish = () => {
      // Requested is not the same as expanded: a later replay may need the same fold again.
      for (const fold of fresh) foldRequests.current.delete(fold.id);
    };
    Promise.all(fresh.map((fold) => tr.expandFold(fold)))
      .then((results) => {
        finish();
        for (const fold of fresh) foldRetrying.current.delete(fold.id);
        let incomplete = false;
        fresh.forEach((fold, index) => {
          if (results[index] === false) {
            incomplete = true;
            foldFailures.current.set(fold.id, t("session-changes.fold_failed"));
          } else foldFailures.current.delete(fold.id);
        });
        // Partial items may have restarted this effect. The failure belongs to the fold, not that obsolete effect,
        // so still publish it and let the current render decide whether this fold is selected.
        if (incomplete) setFoldRetry((n) => n + 1);
        if (active) setFoldLoading(false);
      })
      .catch((error) => {
        finish();
        for (const fold of fresh) foldRetrying.current.delete(fold.id);
        for (const fold of fresh) foldFailures.current.set(fold.id, errorText(error));
        setFoldRetry((n) => n + 1);
        if (active) setFoldLoading(false);
      });
    return () => {
      active = false;
      // StrictMode immediately mounts this effect again. Release only this attempt's reservation so that run can
      // restart the read; leaving the fold marked requested would strand it after the first attempt is disowned.
      finish();
    };
  }, [foldKey, foldRetry, tr.expandFold]);

  useEffect(() => {
    const ids = new Set(edits.map((edit) => edit.id));
    // A fold covers everything through its marker even before expansion. That covered tail, rather than the newest
    // visible successful edit, separates history revealed by a read from items appended after what we already hold.
    const newest = tr.items.reduce((latest, item) => Math.max(latest, item.seq, item.fold?.through_seq ?? 0), 0);
    const prior = previous.current;
    if (!prior || prior.scope !== scopeKey || !tr.firstRead) {
      pendingLive.current.clear();
      setPending({ count: 0, path: "" });
    } else {
      // Earlier pages and expanded folds cannot cross the covered tail. Deferring the pill while history loads must not
      // consume a genuinely appended edit into the ordinary seen-ID baseline.
      for (const edit of edits) {
        if (edit.seq > prior.newest && !prior.ids.has(edit.id) && edit.path !== selectedPath) pendingLive.current.set(edit.id, { path: edit.path, seq: edit.seq });
      }
      for (const [id, edit] of pendingLive.current) if (edit.path === selectedPath) pendingLive.current.delete(id);
      const fresh = [...pendingLive.current.values()].sort((a, b) => a.seq - b.seq);
      setPending(historyReady && fresh.length ? { count: fresh.length, path: fresh.at(-1)!.path } : { count: 0, path: "" });
    }
    previous.current = { scope: scopeKey, ids, newest: Math.max(prior?.scope === scopeKey ? prior.newest : 0, newest) };
  }, [editSignature, scopeKey, selectedPath, tr.items, tr.firstRead, historyReady]);

  useLayoutEffect(() => {
    const body = diffBody.current;
    if (!body) return;
    const old = anchor.current;
    if (old.path === selectedPath && old.first && selectedEdits[0]?.id && old.first !== selectedEdits[0].id) {
      const target = [...body.querySelectorAll<HTMLElement>("[data-change-id]")].find((el) => el.dataset.changeId === old.first);
      if (target) body.scrollTop = old.scroll + target.offsetTop - old.top;
    } else if (old.path !== selectedPath) body.scrollTop = 0;
    const first = [...body.querySelectorAll<HTMLElement>("[data-change-id]")].find((el) => el.dataset.changeId === selectedEdits[0]?.id);
    anchor.current = { path: selectedPath, first: selectedEdits[0]?.id ?? "", top: first?.offsetTop ?? 0, scroll: body.scrollTop };
  }, [editSignature, selectedPath]);

  useEffect(() => {
    if (!call) {
      scrolledCall.current = undefined;
      return;
    }
    if (scrolledCall.current?.call === call && scrolledCall.current.path === selectedPath) return;
    const body = diffBody.current;
    const target = body && [...body.querySelectorAll<HTMLElement>("[data-change-call]")].find((el) => el.dataset.changeCall === call || el.dataset.changeId === call);
    if (!body || !target) return;
    body.scrollTop = target.offsetTop;
    scrolledCall.current = { call, path: selectedPath };
  }, [call, editSignature, selectedPath]);

  function selectFile(path: string) {
    pendingLive.current.clear();
    setPending({ count: 0, path: "" });
    openPanel({ ...opened, turn: turnId, at: { path } }, { replace: true });
  }

  function retryFolds() {
    foldRequests.current.clear();
    for (const id of foldFailures.current.keys()) foldRetrying.current.add(id);
    setFoldRetry((n) => n + 1);
  }

  if (resolvingCall) {
    const waiting = tr.loadingEarlier || foldLoading || (!tr.error && !foldError && (tr.hasMore || candidates.length > 0));
    return (
      <div className="flex min-h-0 flex-1 flex-col" data-changes-panel data-resolving-call>
        <PanelHeader icon="fileDiff" title={t("session-changes.title")} onClose={onClose} />
        {tr.error && (
          <div className="flex shrink-0 items-center gap-2 border-b border-line px-3 py-1.5 text-label text-red" role="alert">
            <span className="min-w-0 flex-1 truncate">{tr.error}</span>
            <button type="button" onClick={tr.reload} className="shrink-0 rounded-control px-2 py-1 hover:bg-hover [@media(pointer:coarse)]:min-h-11">{t("session-changes.retry")}</button>
          </div>
        )}
        {foldError && (
          <div className="flex shrink-0 items-center gap-2 border-b border-line px-3 py-1.5 text-label text-red" role="alert">
            <span className="min-w-0 flex-1">{foldError}</span>
            <button type="button" onClick={retryFolds} className="shrink-0 rounded-control px-2 py-1 hover:bg-hover [@media(pointer:coarse)]:min-h-11">{t("session-changes.retry")}</button>
          </div>
        )}
        <div className="flex min-h-0 flex-1 items-center justify-center gap-2 p-5 text-center text-label text-ink-3" data-change-diff>
          {waiting ? <><Spinner size={16} /> {t("session-changes.loading_history")}</> : !tr.error && !foldError ? t("session-changes.empty") : null}
        </div>
      </div>
    );
  }

  const tree = treeOf(files);
  const action = "touch-hit flex h-7 shrink-0 items-center gap-1 rounded-control px-2 text-label font-medium text-ink-2 transition-colors hover:bg-hover hover:text-ink";
  return (
    <div className="flex min-h-0 flex-1 flex-col" data-changes-panel>
      <PanelHeader
        icon="fileDiff"
        title={t("session-changes.title")}
        subtitle={selectedFile?.shown}
        onClose={onClose}
        actions={
          <>
            {selectedPath && (
              <button type="button" aria-label={t("session-changes.open_file")} title={t("session-changes.open_file")} data-open-file onClick={() => openPanel({ kind: "file", mid, sid, path: selectedPath, cwd })} className={action}>
                <Icon name="read" size={13} />
                <span className="max-sm:hidden">{t("session-changes.open_file")}</span>
              </button>
            )}
            {reviews && cwd && selectedPath && below(cwd, selectedPath) && (
              <button type="button" aria-label={t("session-changes.review_git")} title={t("session-changes.review_git")} data-review-with-git onClick={() => openPanel({ kind: "review", mid, sid, cwd, scope: "branch", at: { path: selectedPath, file: true } })} className={action}>
                <Icon name="pullRequest" size={13} />
                <span className="max-sm:hidden">{t("session-changes.review_git")}</span>
              </button>
            )}
          </>
        }
      />
      <div className="flex min-h-10 shrink-0 items-center gap-2 border-b border-line px-3 py-1">
        <div role="group" aria-label={t("session-changes.scope_label")} className="flex rounded-control bg-field p-0.5">
          {(["exchange", "session"] as const).map((value) => (
            <button key={value} type="button" data-change-scope={value} aria-pressed={scope === value} onClick={() => { pendingLive.current.clear(); setPending({ count: 0, path: "" }); setScope(value); }} className={cx("min-h-7 rounded-[6px] px-2 text-label whitespace-nowrap [@media(pointer:coarse)]:min-h-11", scope === value ? "bg-surface text-ink shadow-btn" : "text-ink-3 hover:text-ink")}>
              {t(`session-changes.${value}`)}
            </button>
          ))}
        </div>
        {pending.count > 0 && (
          <button type="button" data-new-changes onClick={() => selectFile(pending.path)} className="ml-auto flex min-h-7 shrink-0 items-center rounded-full bg-accent-tint px-2 text-submeta font-medium whitespace-nowrap text-accent-ink [@media(pointer:coarse)]:min-h-11">
            {t("session-changes.new_changes", { count: pending.count })}
          </button>
        )}
        {roomy && (
          <div role="group" aria-label={t("extensions-codeviews.unified")} className={cx("flex rounded-control bg-field p-0.5", !pending.count && "ml-auto")}>
            {(["unified", "split"] as const).map((m) => (
              <button
                key={m}
                type="button"
                data-change-diff-mode={m}
                aria-pressed={diffMode === m}
                onClick={() => setDiffMode(m)}
                className={cx("min-h-7 rounded-[6px] px-2 text-label whitespace-nowrap", diffMode === m ? "bg-surface text-ink shadow-btn" : "text-ink-3 hover:text-ink")}
              >
                {m === "unified" ? t("extensions-codeviews.unified") : t("extensions-codeviews.split")}
              </button>
            ))}
          </div>
        )}
      </div>
      {scope === "session" && (tr.hasMore || tr.loadingEarlier) && (
        <div className="flex shrink-0 items-center gap-2 border-b border-line px-3 py-1.5 text-label text-ink-3" data-history-loading>
          <Spinner size={12} /> {t("session-changes.loading_history")}
        </div>
      )}
      {tr.error && (
        <div className="flex shrink-0 items-center gap-2 border-b border-line px-3 py-1.5 text-label text-red" role="alert">
          <span className="min-w-0 flex-1 truncate">{tr.error}</span>
          <button type="button" onClick={tr.reload} className="shrink-0 rounded-control px-2 py-1 hover:bg-hover [@media(pointer:coarse)]:min-h-11">{t("session-changes.retry")}</button>
        </div>
      )}
      <div ref={columns} className="flex min-h-0 flex-1 flex-col lg:flex-row">
        <nav
          id={treeId}
          aria-label={t("session-changes.files_label")}
          className="scrollbar-thin max-h-40 min-h-0 shrink-0 overflow-auto border-b border-line px-1.5 py-1 lg:h-full lg:max-h-none lg:w-(--changes-tree-w) lg:border-b-0"
          style={{ "--changes-tree-w": `${treePane.width}px` } as React.CSSProperties}
        >
          <FileTree root={tree} collapsed={collapsed} selected={selectedPath} select={selectFile} toggle={(path) => setCollapsed((old) => { const next = new Set(old); if (next.has(path)) next.delete(path); else next.add(path); return next; })} />
        </nav>
        <TreePaneSplitter
          value={treePane.width}
          min={treePane.min}
          max={treePane.max}
          onResize={treePane.setWidth}
          onCommit={treePane.commitWidth}
          onReset={treePane.resetWidth}
          label={t("session-changes.resize_label")}
          title={t("session-changes.resize_title")}
          controls={treeId}
          className="hidden lg:flex"
        />
        <div ref={diffBody} className="relative min-h-0 min-w-0 flex-1 overflow-auto p-2" data-change-diff>
          {foldLoading && <div className="flex items-center gap-2 p-2 text-label text-ink-3"><Spinner size={12} />{t("session-changes.loading_fold")}</div>}
          {foldError && (
            <div className="flex items-center gap-2 p-2 text-label text-red" role="alert">
              <span className="min-w-0 flex-1">{foldError}</span>
              <button type="button" onClick={retryFolds} className="shrink-0 rounded-control px-2 py-1 hover:bg-hover [@media(pointer:coarse)]:min-h-11">{t("session-changes.retry")}</button>
            </div>
          )}
          {selectedEdits.map((edit) => (
            <section key={edit.id} data-change-id={edit.id} data-change-call={edit.call} className="mb-2">
              <DiffBlocks diffs={edit.diffs} mode={split ? "split" : "unified"} />
            </section>
          ))}
          {!foldLoading && !selectedEdits.length && (
            <div className="flex h-full min-h-32 flex-col items-center justify-center gap-2 p-5 text-center text-label text-ink-3">
              <Icon name="fileDiff" size={22} />
              {t("session-changes.empty")}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
