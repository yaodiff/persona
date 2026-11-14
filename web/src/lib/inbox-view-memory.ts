// Transient Inbox view state belongs to a browser history entry, not to the item's URL. The entry carries only an
// identifier; its snapshot lives in this page's memory. Thus a fresh navigation (and a reload) starts clean, while Back
// after another route remounts the Inbox can put back what that entry showed. Updates never write browser history.
import { createContext, useContext, useEffect, useLayoutEffect, useRef, type RefObject } from "react";
import { uuid } from "./format";
import { takeReturn } from "./router";

const ENTRY = "personaInboxView";
const SETTLE_MS = 1500;
const READER = ["wheel", "touchstart", "pointerdown", "keydown"] as const;

type ChainPlace = { item: string; open: boolean };
type ViewState = {
  query: string;
  toggled: Set<string>;
  scrollTop: number;
  chain: ChainPlace | null;
  original: ChainPlace | null;
};

export interface InboxViewMemory {
  /** Whether the current view brought back a snapshot (as opposed to starting a fresh visit). */
  readonly restored: boolean;
  /** Changes when an in-Inbox Back / Forward brings back another item entry's snapshot. */
  readonly revision: number;
  /** The current history entry's identifier; attached on the page's first layout effect. */
  readonly entryId: string | null;
  listQuery(): string;
  setListQuery(query: string): void;
  listToggled(): Set<string>;
  setListToggled(toggled: ReadonlySet<string>): void;
  listScroll(): number;
  setListScroll(top: number): void;
  chainOpen(itemKey: string): boolean;
  setChainOpen(itemKey: string, open: boolean): void;
  originalOpen(itemKey: string): boolean;
  setOriginalOpen(itemKey: string, open: boolean): void;
  /** Attach the view to the entry now current, carrying the view forward on an in-Inbox navigation. */
  attach(): void;
  /** Save outside browser history: navigate has already changed the address when an unmount cleanup runs. */
  detach(): void;
}

const entries = new Map<string, ViewState>();
// A reload leaves old identifiers in history. Its new entries must not reuse one and claim that old entry's new snapshot.
let load = uuid();
let nextEntry = 0;

function emptyState(): ViewState {
  return { query: "", toggled: new Set(), scrollTop: 0, chain: null, original: null };
}

function copyState(state: ViewState): ViewState {
  return {
    query: state.query,
    toggled: new Set(state.toggled),
    scrollTop: state.scrollTop,
    chain: state.chain ? { ...state.chain } : null,
    original: state.original ? { ...state.original } : null,
  };
}

function objectState(state: unknown): Record<string, unknown> {
  return state && typeof state === "object" ? state as Record<string, unknown> : {};
}

function currentEntryId(): string | null {
  if (typeof history === "undefined") return null;
  const id = objectState(history.state)[ENTRY];
  return typeof id === "string" && id ? id : null;
}

class ViewMemory implements InboxViewMemory {
  private id: string | null;
  private state: ViewState;
  private returned = false;
  private version = 0;

  constructor(returned: boolean) {
    this.id = currentEntryId();
    // Merely mounting again on the same entry is a fresh visit. Only the router's Back / Forward marker brings a
    // snapshot back; this also keeps a test, a reload and a plain remount from picking up the previous mount's state.
    const saved = returned && this.id ? entries.get(this.id) : undefined;
    this.returned = saved !== undefined;
    this.state = saved ? copyState(saved) : emptyState();
  }

  get restored(): boolean {
    return this.returned;
  }

  get revision(): number {
    return this.version;
  }

  get entryId(): string | null {
    return this.id;
  }

  listQuery(): string {
    return this.state.query;
  }

  setListQuery(query: string): void {
    this.state.query = query;
  }

  listToggled(): Set<string> {
    return new Set(this.state.toggled);
  }

  setListToggled(toggled: ReadonlySet<string>): void {
    this.state.toggled = new Set(toggled);
  }

  listScroll(): number {
    return this.state.scrollTop;
  }

  setListScroll(top: number): void {
    this.state.scrollTop = Number.isFinite(top) ? Math.max(0, top) : 0;
  }

  chainOpen(itemKey: string): boolean {
    return this.state.chain?.item === itemKey && this.state.chain.open;
  }

  setChainOpen(itemKey: string, open: boolean): void {
    this.state.chain = { item: itemKey, open };
  }

  originalOpen(itemKey: string): boolean {
    return this.state.original?.item === itemKey && this.state.original.open;
  }

  setOriginalOpen(itemKey: string, open: boolean): void {
    this.state.original = { item: itemKey, open };
  }

  /** Change entries before children initialize from the view. Only Back / Forward to an item reads its saved snapshot;
   *  a push carries the view on screen, and Back to the always-mounted list keeps its current place. */
  enter(returned: boolean, item: boolean): void {
    const actual = currentEntryId();
    if (this.id === actual) return;
    if (this.id) entries.set(this.id, this.state);
    this.id = actual ?? `${load}:i${++nextEntry}`;
    const saved = returned && item && actual ? entries.get(actual) : undefined;
    this.state = copyState(saved ?? this.state);
    if (saved) {
      this.returned = true;
      this.version++;
    }
    entries.set(this.id, this.state);
  }

  attach(): void {
    if (typeof history === "undefined") return;
    if (!this.id) this.id = `${load}:i${++nextEntry}`;
    // `enter` has already chosen the snapshot during render. The effect only stamps a newly pushed entry; in particular
    // an unmount cleanup after navigate must never write browser history (it now belongs to another route).
    if (!currentEntryId()) history.replaceState({ ...objectState(history.state), [ENTRY]: this.id }, "");
    entries.set(this.id, this.state);
  }

  detach(): void {
    if (this.id) entries.set(this.id, this.state);
  }
}

/** Tests: forget every entry and start another page load. Identifiers already in history are deliberately left alone. */
export function resetInboxViewMemoryForTests(): void {
  entries.clear();
  load = uuid();
  nextEntry = 0;
}

/** The page owns entry attachment. `id` and `manager` make an in-Inbox row navigation attach the pushed entry too. */
export function useInboxViewMemory(id?: string, manager?: string): InboxViewMemory {
  const ref = useRef<ViewMemory | null>(null);
  if (!ref.current) ref.current = new ViewMemory(takeReturn());
  const memory = ref.current;
  // navigate and popstate update the address before React draws. Choose the entry now, before InboxList's and Chain's
  // useState initializers read it; the layout effect below only stamps a new entry after the commit.
  if (memory.entryId !== currentEntryId()) memory.enter(takeReturn(), id !== undefined);
  useLayoutEffect(() => {
    memory.attach();
  }, [memory, id, manager]);
  useLayoutEffect(() => () => memory.detach(), [memory]);
  return memory;
}

export const InboxViewMemoryContext = createContext<InboxViewMemory | null>(null);

/** A list or chain under the page shares its memory; standalone component tests get a detached, clean one. */
export function useInboxView(): InboxViewMemory {
  const shared = useContext(InboxViewMemoryContext);
  const own = useRef<ViewMemory | null>(null);
  if (!own.current) own.current = new ViewMemory(false);
  return shared ?? own.current;
}

/** Put a list back where its entry was, retrying while late items grow its content. The reader can take over at once. */
function restoreList(el: HTMLElement, target: number): (() => void) | undefined {
  el.scrollTop = target;
  if (target === 0 || el.scrollTop >= target - 1) return undefined;
  let ro: ResizeObserver | undefined;
  const stop = () => {
    ro?.disconnect();
    ro = undefined;
    clearTimeout(timer);
    for (const event of READER) el.removeEventListener(event, stop);
  };
  const timer = setTimeout(stop, SETTLE_MS);
  for (const event of READER) el.addEventListener(event, stop, { passive: true });
  if (typeof ResizeObserver !== "undefined") {
    ro = new ResizeObserver(() => {
      el.scrollTop = target;
      if (el.scrollTop >= target - 1) stop();
    });
    ro.observe(el.firstElementChild ?? el);
  }
  return stop;
}

/**
 * Keeps the list's own scroller on its history entry. On a returned item, restoring wins over the usual selected-row
 * scrollIntoView. A single-column list may still be hidden then, so revealing the list tries again. Selecting another
 * item resumes the ordinary behavior, without resetting the list that stayed mounted.
 */
export function useInboxListScroll(
  box: RefObject<HTMLElement | null>,
  view: InboxViewMemory,
  selectedKey: string,
  loaded: boolean,
  storeLoaded: boolean,
  showSelected: () => void,
): void {
  const firstSelected = useRef(selectedKey);
  const restoring = useRef(view.restored);
  const show = useRef(showSelected);
  show.current = showSelected;

  useEffect(() => {
    if (selectedKey && selectedKey !== firstSelected.current) restoring.current = false;
    if (selectedKey && !restoring.current) show.current();
  }, [selectedKey, loaded, storeLoaded]);

  useLayoutEffect(() => {
    const el = box.current;
    if (!el || !restoring.current) return;
    return restoreList(el, view.listScroll());
  }, [box, view, selectedKey]);

  useEffect(() => {
    const el = box.current;
    if (!el) return;
    const save = () => view.setListScroll(el.scrollTop);
    el.addEventListener("scroll", save, { passive: true });
    return () => el.removeEventListener("scroll", save);
  }, [box, view]);
}
