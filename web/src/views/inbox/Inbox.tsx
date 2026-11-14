// Inbox (docs/assistant/inbox.md): Feishu messages, mail, Feishu documents and Moments in one list, sorted by the AI's triage,
// with the agents waiting on you at the head of "Needs you". Wide: the list (380px) and the opened item side by side;
// narrow: the opened item takes the list's place, with a way back. "Select" (top right) picks several items to mark them
// done at once (#2059).
import { useEffect, useLayoutEffect, useRef, useState, type RefObject } from "react";
import { TodoDialogHost } from "./InboxTodoDialog";
import { useInbox, refreshInbox, loadRoutedItem, type InboxItem } from "../../lib/inbox";
import { useScope } from "../../lib/current-assistant";
import { ItemManager, otherManager, sameManager } from "../../lib/manager-choice";
import { newDraft, useMailAccounts } from "../../lib/mail";
import { backToInboxList } from "../../lib/inbox-nav";
import { InboxViewMemoryContext, useInboxViewMemory } from "../../lib/inbox-view-memory";
import { t } from "../../i18n";
import { Button, cx, IconButton, Spinner } from "../../ui/atoms";
import { ComposeDialog } from "./ComposeDialog";
import { DetailPlaceholder, InboxDetail } from "./InboxDetail";
import { InboxList } from "./InboxList";
import { PolicyDialogHost } from "./PolicyDialog";
import { FolderDialogHost } from "./InboxFolders";

/** The item a route opens: its id of its manager (none: the default's). A link that names no manager (from before
 *  several managers, or written by hand) opens the default's copy, else the one manager that has it. */
export function routedItem(items: InboxItem[], id: string | undefined, manager: string | undefined): InboxItem | undefined {
  if (!id) return undefined;
  const same = items.filter((x) => x.id === id);
  return same.find((x) => sameManager(x.manager, manager)) ?? (manager ? undefined : same[0]);
}

export function Inbox({ id, manager, onToggleSidebar }: { id?: string; manager?: string; onToggleSidebar: () => void }) {
  const view = useInboxViewMemory(id, manager);
  // the list's copy, else Done's history's: the list carries only the newest done items (#5549)
  const item = useInbox((s) => routedItem(s.items, id, manager) ?? routedItem(s.history, id, manager));
  const loaded = useInbox((s) => s.loaded);
  const loading = useInbox((s) => s.loading);
  // neither has it (a link to an item done past what the list carries): the hub's copy, once
  const routing = useInbox((s) => s.routing.length > 0);
  useEffect(() => {
    if (id && loaded && !item) loadRoutedItem(id, manager);
  }, [id, manager, loaded, item]);
  // a new mail is the current assistant's (docs/assistant/managers.md "Clients"): its own mail accounts, sent through it
  const scope = useScope();
  const composer = scope.several ? otherManager(scope.id) : undefined;
  const accounts = useMailAccounts({ manager: composer });
  const [composing, setComposing] = useState(false);
  // Selecting (#2059): the picked items' keys (itemKey); null: not selecting. Esc leaves it.
  const [picked, setPicked] = useState<Set<string> | null>(null);
  const picking = picked !== null;
  useEffect(() => {
    if (!picking) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !e.defaultPrevented) setPicked(null);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [picking]);
  // Select and Done are one button, so the keyboard stays on it as it turns (audit item 71); when selecting ends from
  // elsewhere (Esc on a row's tick, Mark as done in the bar), what had the focus is gone, and it comes back here.
  const selectButton = useRef<HTMLButtonElement>(null);
  useFocusWhenLost(picking, () => selectButton.current);
  // On a narrow page the opened item has the list's place (audit item 74): the focus that went with the row opening it
  // moves to the item's title, and back to the list when ‹, Esc or the browser's Back returns there
  const listBox = useRef<HTMLElement>(null);
  const detailBox = useRef<HTMLElement>(null);
  useNarrowItemFocus(id, manager, listBox, detailBox);
  // Esc there goes back to the list as ‹ does: not over a dialog or a menu, nor in a field (Esc is its own there), and not
  // on a wide page, where the list is beside the item
  useEffect(() => {
    if (!id) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented || e.isComposing || shown(listBox.current)) return;
      if (document.querySelector("[role=dialog], [role=menu]") || (e.target instanceof Element && e.target.closest("input, textarea, select, [contenteditable]"))) return;
      backToInboxList();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [id]);
  return (
    <InboxViewMemoryContext.Provider value={view}>
    <div className="@container flex h-full flex-col">
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-line px-3 sm:px-4">
        <IconButton icon="sidebar" label={t("inbox-inbox.sidebar")} onClick={onToggleSidebar} className="lg:hidden" />
        {id && <IconButton icon="chevronLeft" label={t("inbox-inbox.back_to_inbox")} onClick={backToInboxList} className="@3xl:hidden" />}
        <h1 className="min-w-0 flex-1 truncate text-[14px] font-semibold text-ink">{t("inbox-inbox.title")}</h1>
        {loading && loaded && <Spinner />}
        {/* the list's: not over an opened item that has the list's place */}
        <Button ref={selectButton} tone="quiet" onClick={() => setPicked(picking ? null : new Set())} className={cx(!picking && id && "@max-3xl:hidden")}>
          {picking ? t("inbox-inbox.select_done") : t("inbox-inbox.select")}
        </Button>
        {!picking && (
          <>
            <IconButton icon="compose" label={accounts.length ? t("inbox-inbox.compose") : t("inbox-inbox.compose_no_account")} onClick={() => setComposing(true)} disabled={!accounts.length} />
            <IconButton icon="retry" label={t("inbox-inbox.refresh")} onClick={() => void refreshInbox()} disabled={loading} />
          </>
        )}
      </header>
      <div className="flex min-h-0 flex-1">
        <section ref={listBox} aria-label={t("inbox-inbox.list")} className={cx("min-h-0 w-full flex-col @3xl:flex @3xl:w-[380px] @3xl:shrink-0 @3xl:border-r @3xl:border-line", id ? "hidden" : "flex")}>
          <InboxList key={view.revision} selected={item ?? (id ? { id, manager } : undefined)} picked={picked} onPicked={setPicked} />
        </section>
        <section ref={detailBox} tabIndex={-1} aria-label={t("inbox-inbox.detail")} className={cx("min-h-0 min-w-0 flex-1 flex-col outline-none focus-visible:outline-solid focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent @3xl:flex", id ? "flex" : "hidden")}>
          {id ? <InboxDetail item={item} loaded={loaded && !routing} /> : <DetailPlaceholder />}
        </section>
      </div>
      <PolicyDialogHost />
      <FolderDialogHost />
      <TodoDialogHost />
      {composing && (
        <ItemManager.Provider value={composer}>
          <ComposeDialog draftKey="new" initial={newDraft(accounts[0]?.id ?? "")} accounts={accounts} onClose={() => setComposing(false)} />
        </ItemManager.Provider>
      )}
    </div>
    </InboxViewMemoryContext.Provider>
  );
}

/** When `key` changes and the focus was lost with what changed, it goes to `target()`. Lost: on the page's body (what
 *  had it was removed), or on something no longer drawn (hidden with its list; the browser moves such a focus to the
 *  body only at its next frame). A focus still in sight stays. */
function useFocusWhenLost(key: unknown, target: () => HTMLElement | null | undefined): void {
  const last = useRef(key);
  useLayoutEffect(() => {
    if (last.current === key) return;
    last.current = key;
    if (focusLost()) target()?.focus({ preventScroll: true });
  });
}

/** An opened item's address. */
type Opened = { id: string; manager?: string };
/** The row an item was opened from on a narrow page, and the rows after and before it then (each a `[data-item]`). */
type From = { it: Opened; row: HTMLElement | null; after: HTMLElement[]; before: HTMLElement[] };

/**
 * The keyboard where the opened item takes the list's place (a narrow page, audit item 74). Opening one moves the focus,
 * lost with the hidden list, to the item's title (the item's pane while it loads). Back on the list it returns to the
 * row the item was opened from, if that row is still where it was: a row is a new element once it is filed elsewhere
 * (Done from the item, another section or group), so the one noted is no longer in the page. Then the focus goes to the
 * row now in its place (the next one, else the one before), and the list stays where it was instead of following the
 * item into Done. A wide page, with the list beside the item, is left alone both ways.
 */
function useNarrowItemFocus(id: string | undefined, manager: string | undefined, listBox: RefObject<HTMLElement | null>, detailBox: RefObject<HTMLElement | null>): void {
  const last = useRef<Opened | undefined>(id ? { id, manager } : undefined);
  const from = useRef<From | undefined>(undefined);
  useLayoutEffect(() => {
    const was = last.current;
    if (was?.id === id && (!id || was?.manager === manager)) return;
    last.current = id ? { id, manager } : undefined;
    const list = listBox.current;
    if (id) {
      // wide: the list is beside the item, and what had the focus is still in sight
      if (shown(list)) {
        from.current = undefined;
        return;
      }
      // from the list (not from another item, which keeps the row the first was opened from)
      if (!was) from.current = noteRow(list, { id, manager });
      if (focusLost()) (detailBox.current?.querySelector<HTMLElement>("[data-item-title]") ?? detailBox.current)?.focus({ preventScroll: true });
      return;
    }
    const noted = from.current;
    from.current = undefined;
    // wide: the item's pane stays beside the list, holding its placeholder
    if (!was || shown(detailBox.current) || !focusLost()) return;
    // the rows noted that are still in the page, then the item's row found again, unless it went to Done (an item that
    // had no row under it, opened from elsewhere; a list drawn anew, as an in-Inbox Back to another item does)
    const rows = [...(noted?.row ? [noted.row, ...noted.after, ...noted.before].filter((r) => r.isConnected) : []), unmovedRow(list, noted?.it ?? was)];
    for (const row of rows) {
      const el = row?.querySelector<HTMLElement>(":scope > a, :scope > [role=checkbox]");
      el?.focus({ preventScroll: true });
      // only one that took it: a row in a folded section can't (and not the scroll's own answer, which a browser may
      // give as a promise: an effect returns nothing but its cleanup)
      if (!el || document.activeElement !== el) continue;
      el.scrollIntoView?.({ block: "nearest" });
      return;
    }
  });
}

/** The row of the item `it` in the list (`[data-item]`). */
function itemRow(list: HTMLElement | null, it: Opened): HTMLElement | null {
  const rows = [...(list?.querySelectorAll<HTMLElement>(`[data-item="${CSS.escape(it.id)}"]`) ?? [])];
  return rows.find((el) => sameManager(el.dataset.manager, it.manager)) ?? rows[0] ?? null;
}

/** Where the item `it` is opened from: its row, and the three rows after and before it in the list. */
function noteRow(list: HTMLElement | null, it: Opened): From {
  const row = itemRow(list, it);
  const rows = [...(list?.querySelectorAll<HTMLElement>("[data-item]") ?? [])];
  const at = row ? rows.indexOf(row) : -1;
  return { it, row, after: at < 0 ? [] : rows.slice(at + 1, at + 4), before: at < 0 ? [] : rows.slice(Math.max(0, at - 3), at).reverse() };
}

/** An item opened some other way (a link, a notification): its row, unless it has gone to Done meanwhile. */
function unmovedRow(list: HTMLElement | null, it: Opened): HTMLElement | null {
  const row = itemRow(list, it);
  return row?.closest("[data-section]")?.getAttribute("data-section") === "done" ? null : row;
}

/** Whether the focus was lost: on the page's body, or on something no longer drawn. */
function focusLost(): boolean {
  const at = document.activeElement;
  return !(at instanceof HTMLElement) || at === document.body || !shown(at);
}

/** Whether `el` is drawn (the list on a wide page, hidden while an opened item has its place on a narrow one; what is
 *  in either). */
function shown(el: HTMLElement | null): boolean {
  return Boolean(el && (el.checkVisibility ? el.checkVisibility() : el.getClientRects().length > 0));
}
