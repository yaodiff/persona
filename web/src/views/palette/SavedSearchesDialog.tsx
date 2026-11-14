import { forwardRef, useEffect, useId, useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";
import { t } from "../../i18n";
import { fromDefaultManager } from "../../lib/manager-choice";
import { onHubEvent } from "../../lib/store";
import { SavedAgentSearchesModel, savedSearchProblem, SAVED_SEARCH_LIMIT, SAVED_SEARCH_NAME_MAX, type SavedAgentSearch, type SavedAgentSearchesSnapshot } from "../../lib/saved-agent-searches";
import { Button, Dialog, IconButton, Kbd, Loading } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
export { SavedAgentSearchesModel };
export * from "./SearchSupport";
const word = (key: string, values?: Parameters<typeof t>[1]) => t(`saved-agent-searches.${key}`, values);

/** The open palette and its replacement dialog share one reader, without polling while either is closed. */
export function followSavedSearches(model: SavedAgentSearchesModel): () => void {
  void model.refresh();
  const changed = onHubEvent("saved_agent_searches", (event) => {
    if (!fromDefaultManager(event)) return;
    const revision = event.revision;
    if (typeof revision === "string" && /^(0|[1-9][0-9]*)$/.test(revision)) {
      if (BigInt(revision) <= BigInt(model.snapshot().document?.revision ?? "0")) return;
    }
    void model.refresh();
  });
  const hello = onHubEvent("hello", () => void model.refresh());
  return () => { changed(); hello(); model.dispose(); };
}

export const SavedSearchQuickRow = forwardRef<HTMLButtonElement, {
  item: SavedAgentSearch; option: { id: string; role: "option"; "aria-selected": boolean };
  className: string; onMouseMove: () => void; onClick: () => void;
}>(function SavedSearchQuickRow({ item, option, className, onMouseMove, onClick }, ref) {
  return <button ref={ref} type="button" {...option} className={className} onMouseMove={onMouseMove} onClick={onClick} data-saved-search-quick={item.id}>
    <span className="flex size-5 shrink-0 items-center justify-center"><Icon name="book" size={13} className="text-ink-3" /></span>
    <span className="min-w-0 flex-1"><span className="block truncate text-sm font-medium text-ink">{item.name}</span><span className="block truncate text-xs text-ink-3">{item.query}</span></span>
  </button>;
});

interface Editor { item: SavedAgentSearch | null; name: string; query: string; revision: string; returnToPalette: boolean }
export function SavedSearchesDialog({ model, initialQuery, onReturn, onApply }: {
  model: SavedAgentSearchesModel; initialQuery?: string; onReturn: () => void; onApply: (query: string) => void;
}) {
  const state = useSyncExternalStore(model.subscribe, model.snapshot, model.snapshot);
  const [editor, setEditor] = useState<Editor | null>(() => initialQuery === undefined || !model.snapshot().document ? null
    : { item: null, name: "", query: initialQuery, revision: model.snapshot().document!.revision, returnToPalette: true });
  const [attempted, setAttempted] = useState(false);
  const submitting = useRef(false);
  const alive = useRef(false);
  const currentEditor = useRef(editor);
  currentEditor.current = editor;
  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  const doc = state.document, busy = state.busy;
  const writable = !!doc?.can_write && !state.readonly && !state.unsupported;
  const invalid = editor ? savedSearchProblem(editor.name, editor.query) : "";
  const stale = !!(editor && doc && editor.revision !== doc.revision);
  const latest = editor?.item ? doc?.searches.find((item) => item.id === editor.item!.id) : undefined;
  const gone = !!(stale && editor?.item && !latest);
  const nameID = useId(), queryID = useId(), hintID = useId();
  const heading = useRef<HTMLHeadingElement>(null);
  const area = useRef<HTMLDivElement>(null);
  const focusAfterAction = useRef(false);
  const focusAfterEditor = useRef<string | null | undefined>(undefined);
  useLayoutEffect(() => {
    if (!editor && focusAfterEditor.current !== undefined) {
      const id = focusAfterEditor.current;
      focusAfterEditor.current = undefined;
      // The dialog stays mounted as its editor leaves. Its modal hook does not run again, so replace focus lost
      // with a removed form control; a close button or another control the user focused deliberately keeps it.
      if (document.activeElement === document.body) {
        const row = id ? area.current?.querySelector<HTMLElement>(`[data-saved-search="${id}"] > button`) : null;
        (row ?? heading.current)?.focus({ preventScroll: true });
      }
    }
    if (!busy && focusAfterAction.current) {
      focusAfterAction.current = false;
      if (document.activeElement === document.body) {
        (area.current?.querySelector<HTMLElement>('[data-testid="saved-search-undo"]') ?? heading.current)?.focus({ preventScroll: true });
      }
    }
  }, [editor, busy, doc, state.canUndo]);
  const edit = (item: SavedAgentSearch | null) => {
    if (!doc || !writable || busy) return;
    setAttempted(false);
    setEditor({ item, name: item?.name ?? "", query: item?.query ?? "", revision: doc.revision, returnToPalette: false });
  };
  const cancelEdit = () => { if (editor?.returnToPalette) onReturn(); else { focusAfterEditor.current = editor?.item?.id ?? null; setEditor(null); setAttempted(false); } };
  const save = async (reviewed = false) => {
    if (!editor || busy || submitting.current || !writable) return;
    setAttempted(true);
    if (invalid || (!reviewed && stale)) return;
    const revision = reviewed ? doc!.revision : editor.revision;
    const captured = reviewed ? { ...editor, revision } : editor;
    submitting.current = true;
    currentEditor.current = captured;
    if (reviewed) setEditor(captured);
    try {
      const action = captured.item && !gone ? { action: "update" as const, id: captured.item.id, name: captured.name, query: captured.query }
        : { action: "add" as const, name: captured.name, query: captured.query };
      if (await model.mutate(action, revision) && alive.current && currentEditor.current === captured) {
        if (captured.returnToPalette) onReturn(); else { focusAfterEditor.current = captured.item?.id ?? null; setEditor(null); setAttempted(false); }
      }
    } finally { submitting.current = false; }
  };
  const move = (item: SavedAgentSearch, direction: -1 | 1) => {
    if (!doc || busy || !writable) return;
    const at = doc.searches.findIndex((row) => row.id === item.id);
    void model.mutate({ action: "move", id: item.id, before_id: doc.searches[at + (direction < 0 ? -1 : 2)]?.id }, doc.revision);
  };
  const remove = (item: SavedAgentSearch) => {
    if (!doc || busy || !writable) return;
    focusAfterAction.current = true;
    void model.mutate({ action: "delete", id: item.id }, doc.revision);
  };
  return <Dialog open onClose={() => { if (editor) cancelEdit(); else onReturn(); }}
    title={editor ? word(editor.item ? "edit_title" : "save_title") : word("title")}
    footer={editor ? <>
      <Button onClick={cancelEdit}>{t("common.cancel")}</Button>
      <Button tone="primary" busy={busy} disabled={!writable || stale || !!invalid || (!editor.item && (doc?.searches.length ?? 0) >= SAVED_SEARCH_LIMIT)} onClick={() => void save()} testId="saved-search-save">{word("save")}</Button>
    </> : <Button onClick={onReturn}>{word("back_to_search")}</Button>}>
    <div ref={area} className="min-w-0 space-y-3" data-saved-searches>
      {state.unsupported ? <p role="status" className="text-sm text-ink-2">{word("unsupported")}</p> : <>
        {!doc && (state.loading || !state.error) && <Loading label={word("loading")} />}
        {state.error && <div role="alert" className="space-y-2 text-sm text-red"><p>{state.error}</p>{!editor && <Button busy={state.loading} disabled={busy} onClick={() => void model.refresh()}>{word("retry")}</Button>}</div>}
        {state.readonly && <p className="rounded-control bg-inset px-3 py-2 text-sm text-ink-2">{word("read_only")}</p>}
        {editor ? <form className="min-w-0 space-y-4" onSubmit={(event) => { event.preventDefault(); void save(); }} onKeyDown={(event) => {
          if (event.key === "Enter" && (event.metaKey || event.ctrlKey) && !event.nativeEvent.isComposing) { event.preventDefault(); void save(); }
        }}>
          <p className="text-sm text-ink-2">{word("editor_hint")}</p>
          <div><label htmlFor={nameID} className="mb-1 block text-sm font-medium text-ink">{word("name")}</label>
            <input id={nameID} autoFocus className="w-full rounded-control bg-field px-3 py-2 text-sm text-ink outline-none focus:ring-2 focus:ring-accent" maxLength={SAVED_SEARCH_NAME_MAX * 2} value={editor.name} disabled={busy || !writable} onChange={(event) => setEditor({ ...editor, name: event.target.value })} aria-describedby={hintID} />
          </div>
          <div><label htmlFor={queryID} className="mb-1 block text-sm font-medium text-ink">{word("query")}</label>
            <textarea id={queryID} rows={3} spellCheck={false} className="w-full resize-y rounded-control bg-field px-3 py-2 font-mono text-sm text-ink outline-none focus:ring-2 focus:ring-accent" value={editor.query} disabled={busy || !writable} onChange={(event) => setEditor({ ...editor, query: event.target.value })} aria-describedby={hintID} />
          </div>
          <p id={hintID} className="text-xs text-ink-3">{word("query_hint")}</p>
          {(attempted || Boolean(editor.name.trim() && editor.query.trim())) && invalid && <p role="alert" className="text-sm text-red">{invalid}</p>}
          {!editor.item && (doc?.searches.length ?? 0) >= SAVED_SEARCH_LIMIT && <p role="alert" className="text-sm text-ink-2">{word("limit", { count: SAVED_SEARCH_LIMIT })}</p>}
          {stale && <section className="min-w-0 space-y-2 rounded-control border border-line bg-inset p-3" aria-label={word("latest_title")}>
            <h3 className="text-sm font-medium text-ink">{word("latest_title")}</h3>
            <p className="text-sm text-ink-2">{word(gone ? "removed_elsewhere" : "draft_kept")}</p>
            {latest && <dl className="space-y-2 text-sm"><div><dt className="text-xs text-ink-3">{word("name")}</dt><dd className="break-words text-ink [overflow-wrap:anywhere]">{latest.name}</dd></div><div><dt className="text-xs text-ink-3">{word("query")}</dt><dd className="whitespace-pre-wrap break-words font-mono text-ink [overflow-wrap:anywhere]">{latest.query}</dd></div></dl>}
            <Button disabled={busy || !writable || !!invalid} onClick={() => void save(true)}>{word(gone ? "save_as_new" : "review_save")}</Button>
          </section>}
        </form> : doc && <>
          <div className="flex flex-wrap items-center gap-2"><h3 ref={heading} tabIndex={-1} className="min-w-0 flex-1 text-sm font-medium text-ink outline-none">{word("list_heading", { count: doc.searches.length })}</h3>
            {writable && <Button icon="plus" disabled={busy || doc.searches.length >= SAVED_SEARCH_LIMIT} onClick={() => edit(null)}>{word("add")}</Button>}
          </div>
          <p className="text-sm text-ink-2">{word("intro")}</p>
          {state.canUndo && <div role="status" className="flex flex-wrap items-center gap-2 rounded-control bg-inset px-3 py-2"><span className="min-w-0 flex-1 text-sm text-ink-2">{word("deleted")}</span><Button icon="undo" busy={busy} testId="saved-search-undo" onClick={() => { focusAfterAction.current = true; void model.undo(); }}>{word("undo")}</Button></div>}
          {!doc.searches.length && <p className="py-4 text-sm text-ink-3">{word("empty")}</p>}
          <ol className="min-w-0 divide-y divide-line">{doc.searches.map((item, index) => <li key={item.id} data-saved-search={item.id} className="min-w-0 space-y-2 py-3">
            <button type="button" className="block min-h-11 w-full min-w-0 rounded-control px-2 py-1 text-left hover:bg-hover" aria-label={word("apply_named", { name: item.name })} onClick={() => onApply(item.query)}>
              <span className="block break-words text-sm font-medium text-ink [overflow-wrap:anywhere]">{item.name}</span>
              <span className="mt-1 block whitespace-pre-wrap break-words font-mono text-xs text-ink-3 [overflow-wrap:anywhere]">{item.query}</span>
            </button>
            {writable && <div className="flex flex-wrap items-center gap-1 px-2">
              <IconButton icon="arrowUp" label={word("move_up", { name: item.name })} disabled={busy || index === 0} onClick={() => move(item, -1)} />
              <IconButton icon="arrowDown" label={word("move_down", { name: item.name })} disabled={busy || index === doc.searches.length - 1} onClick={() => move(item, 1)} />
              <Button tone="quiet" disabled={busy} onClick={() => edit(item)} aria-label={word("edit_named", { name: item.name })}>{word("edit")}</Button>
              <Button tone="quiet" disabled={busy} onClick={() => remove(item)} aria-label={word("delete_named", { name: item.name })}>{word("delete")}</Button>
            </div>}
          </li>)}</ol>
        </>}
      </>}
    </div>
  </Dialog>;
}

/** Search-only presentation stays with the intent-loaded tools rather than growing the idle palette bundle. */
export function SavedSearchFooter({ query, state, onSave }: { query: string; state: SavedAgentSearchesSnapshot; onSave: () => void }) {
  if (!query || !state.document?.can_write || state.readonly || state.unsupported) return null;
  return <div className="border-t border-line px-4 py-2"><button type="button" className="text-sm text-accent-ink hover:underline disabled:opacity-50" disabled={state.busy || state.loading} onClick={onSave}>{t("palette.save_search")}</button></div>;
}

export function EmptySearch({ query, onNewAgent }: { query: string; onNewAgent: () => void }) {
  return <div className="flex flex-col items-center gap-3 px-4 py-10 text-center">
    <span className="flex size-9 items-center justify-center rounded-full bg-field text-ink-3 shadow-hairline"><Icon name="search" size={15} /></span>
    <div><p className="text-meta font-medium text-ink">{t("palette.no_results", { query })}</p><p className="mt-0.5 text-label text-ink-3">{t("palette.no_results_hint")}</p></div>
    <button type="button" onClick={onNewAgent} className="rounded-control px-2.5 py-1 text-label font-medium text-accent-ink hover:bg-accent-tint">{t("palette.new_agent")}</button>
  </div>;
}

/** The list-filter action is only relevant after a search; keep its presentation with the query tools. */
export function FilterAgentsList({ query, onFilter }: { query: string; onFilter: () => void }) {
  return <button type="button" onClick={onFilter} data-testid="palette-filter-list" className="flex w-full items-center gap-2.5 border-t border-line px-4 py-2.5 text-left hover:bg-hover">
    <span className="flex size-5 shrink-0 items-center justify-center"><Icon name="filter" size={13} className="text-ink-3" /></span>
    <span className="min-w-0 flex-1 truncate text-meta text-ink">{t("palette.filter_agents_list", { query })}</span>
    <span className="flex shrink-0 items-center gap-1 [@media(pointer:coarse)]:hidden"><Kbd>⇧</Kbd><Kbd>↵</Kbd></span>
  </button>;
}
