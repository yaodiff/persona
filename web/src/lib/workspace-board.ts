import { t } from "../i18n";
import { api, ApiError, credentialOwner, errorText, getToken } from "./api";
import { defaultKnown, defaultManager, MANAGER_HEADER } from "./manager-choice";
import { sameValue } from "./stable-lines";
import type { Session } from "./types";

export interface BoardColumn { id: string; title: string }
export interface BoardCard {
  id: string; machine_id: string; workspace_id: string; title: string; cwd: string;
  source: string; source_id?: string; branch?: string; column_id: string; pinned: boolean; archived: boolean;
}
/** Compatible board name for the shared discovery wire type. */
export type { HostWorkspace as Workspace } from "./workspace-catalog";
import type { HostWorkspace as Workspace } from "./workspace-catalog";
export interface BoardState { revision: number; columns: BoardColumn[]; cards: BoardCard[]; can_write?: boolean; batch_cards?: boolean; activity?: boolean }
export interface WorkspaceList { workspaces: Workspace[]; errors?: { machine_id: string; error: string }[] }
export interface BoardCardChange {
  id: string; column_id?: string; before_id?: string; pinned?: boolean; archived?: boolean;
}
export interface BoardMutation {
  action: "add_card" | "update_card" | "move_card" | "add_column" | "update_column" | "move_column" | "delete_column" | "batch_cards";
  changes?: BoardCardChange[];
  id?: string; column_id?: string; before_id?: string; title?: string; pinned?: boolean; archived?: boolean;
  machine_id?: string; workspace_id?: string; cwd?: string; move_to?: string;
}

/** A removed selection falls back to the first persisted workflow column. */
export function selectedColumn(board: BoardState, id: string): string {
  return board.columns.some((c) => c.id === id) ? id : board.columns[0]?.id ?? "";
}

/** Workflow is metadata. Agent state, discovery order and availability never assign a column. */
export function columnCards(board: BoardState, column: string, opts: { search?: string; machine?: string; archived?: boolean } = {}): BoardCard[] {
  const q = opts.search?.trim().toLocaleLowerCase() ?? "";
  const cards = board.cards.filter((c) => c.column_id === column && c.archived === Boolean(opts.archived)
    && (!opts.machine || c.machine_id === opts.machine)
    && (!q || `${c.title} ${c.cwd} ${c.branch ?? ""} ${c.machine_id}`.toLocaleLowerCase().includes(q)));
  return [...cards.filter((c) => c.pinned), ...cards.filter((c) => !c.pinned)];
}
/** Match the hub's CanonicalCwd for remote POSIX, Windows drive and UNC syntax. */
export function directory(p: string): string | null {
  if (!p || p.includes("\0")) return null;
  const clean = (path: string) => {
    const parts: string[] = [];
    for (const part of path.split("/")) {
      if (!part || part === ".") continue;
      if (part === "..") parts.pop(); else parts.push(part);
    }
    return `/${parts.join("/")}`;
  };
  if (/^[a-z]:/i.test(p)) return /^[a-z]:[\\/]/i.test(p) ? p.slice(0, 2).toLowerCase() + clean(p.slice(2).replaceAll("\\", "/")) : null;
  if (p.startsWith("\\\\") || p.startsWith("//")) {
    const parts = p.slice(2).replaceAll("\\", "/").split("/");
    if (parts.length < 2 || parts.slice(0, 2).some((v) => !v || v === "." || v === "..")) return null;
    const root = `//${parts[0].toLowerCase()}/${parts[1].toLowerCase()}`;
    const rest = clean(parts.slice(2).join("/"));
    return root + (rest === "/" ? "" : rest);
  }
  return p.startsWith("/") ? clean(p) : null;
}
export function sameDirectory(a: string, b: string): boolean {
  const canonical = directory(a);
  return canonical !== null && canonical === directory(b);
}
export function cardSessions(card: BoardCard, sessions: readonly Session[]): Session[] {
  return sessions.filter((s) => s.machine_id === card.machine_id && sameDirectory(s.workspace_cwd || s.cwd, card.cwd));
}
export function boundCard(board: BoardState, workspace: Workspace): BoardCard | undefined {
  return board.cards.find((c) => c.machine_id === workspace.machine_id && sameDirectory(c.cwd, workspace.cwd));
}
function insertBefore<T extends { id: string }>(items: T[], item: T, before?: string): T[] {
  const rest = items.filter((v) => v.id !== item.id);
  const i = before ? rest.findIndex((v) => v.id === before) : -1;
  rest.splice(i < 0 ? rest.length : i, 0, item);
  return rest;
}
/** Only project reversible metadata edits; additions wait for trustworthy IDs and canonical paths from the hub. */
export function optimisticBoard(board: BoardState, m: BoardMutation): BoardState {
  if (m.action === "batch_cards") {
    return (m.changes ?? []).reduce((next, change) => {
      if (change.column_id !== undefined) next = optimisticBoard(next, { action: "move_card", ...change });
      return optimisticBoard(next, { action: "update_card", id: change.id, pinned: change.pinned, archived: change.archived });
    }, board);
  }
  const card = board.cards.find((c) => c.id === m.id);
  const column = board.columns.find((c) => c.id === m.id);
  if (m.action === "move_card" && card && m.id !== m.before_id) return { ...board, cards: insertBefore(board.cards, { ...card, column_id: m.column_id! }, m.before_id) };
  if (m.action === "update_card" && card) return { ...board, cards: board.cards.map((c) => c.id !== m.id ? c : { ...c,
    ...(m.title !== undefined ? { title: m.title } : {}), ...(m.pinned !== undefined ? { pinned: m.pinned } : {}), ...(m.archived !== undefined ? { archived: m.archived } : {}) }) };
  if (m.action === "move_column" && column && m.id !== m.before_id) return { ...board, columns: insertBefore(board.columns, column, m.before_id) };
  if (m.action === "update_column") return { ...board, columns: board.columns.map((c) => c.id === m.id ? { ...c, title: m.title! } : c) };
  return board;
}

/** Restore only the changed fields. Reverse traversal places each moved card before an already restored
 *  successor in its original column, including archived/pinned cards that the current filter hides. */
export function inverseBoardChanges(board: BoardState, changes: readonly BoardCardChange[]): BoardCardChange[] {
  const changed = new Map(changes.map((change) => [change.id, change]));
  const nextInColumn = new Map<string, string>();
  const inverse: BoardCardChange[] = [];
  for (let i = board.cards.length - 1; i >= 0; i--) {
    const card = board.cards[i], change = changed.get(card.id);
    if (change) inverse.push({
      id: card.id,
      ...(change.column_id !== undefined ? { column_id: card.column_id, before_id: nextInColumn.get(card.column_id) ?? "" } : {}),
      ...(change.pinned !== undefined ? { pinned: card.pinned } : {}),
      ...(change.archived !== undefined ? { archived: card.archived } : {}),
    });
    nextInColumn.set(card.column_id, card.id);
  }
  return inverse;
}

export interface BoardSnapshot {
  board: BoardState | null; discovery: WorkspaceList; loading: boolean; busy: boolean; readonly: boolean; error: string; discoveryError: string;
  /** Only the last successful batch can be undone, at exactly its acknowledged revision. */
  canUndo: boolean;
  /** The last read said this hub has no board (boardUnsupported): Retry can't help, updating the hub does. */
  unsupported: boolean;
}
type Transport = <T>(path: string, init?: RequestInit & { json?: unknown }) => Promise<T>;
export class BoardModel {
  private state: BoardSnapshot = { board: null, discovery: { workspaces: [] }, loading: true, busy: false, readonly: false, error: "", discoveryError: "", unsupported: false, canUndo: false };
  private listeners = new Set<() => void>();
  private reads = 0;
  private refreshAfterWrite = false;
  private undoEntry: { revision: number; changes: BoardCardChange[] } | null = null;
  // A mounted board is keyed by these identities. Retired requests may finish, but must never recover or write
  // through the next manager/credential. The transport captures its headers synchronously at invocation.
  private owner = credentialOwner();
  private manager = defaultManager();
  private production: boolean;
  private request: Transport;
  private current = () => (!this.production || defaultKnown()) && this.owner === credentialOwner() && this.manager === defaultManager();
  constructor(request?: Transport) {
    this.production = !request;
    const token = getToken();
    // These routes belong to the roster's default manager, not the sidebar's current Assistant. Name it explicitly:
    // a roster change on the hub between read and write must never apply this board's IDs to another manager.
    this.request = request ?? (<T>(path: string, init?: Parameters<Transport>[1]) => {
      const headers = new Headers(init?.headers);
      headers.set(MANAGER_HEADER, this.manager);
      return api<T>(path, { ...init, headers, token, tokenOwner: this.owner, isAuthContextCurrent: this.current });
    });
  }
  snapshot = () => this.state;
  subscribe = (fn: () => void) => { this.listeners.add(fn); return () => { this.listeners.delete(fn); }; };
  /** A patch that changes no field keeps the snapshot and notifies no one, so an unchanged poll does not re-render the board. */
  private set(patch: Partial<BoardSnapshot>) {
    if ((Object.keys(patch) as (keyof BoardSnapshot)[]).every((k) => Object.is(this.state[k], patch[k]))) return;
    this.state = { ...this.state, ...patch }; this.listeners.forEach((fn) => fn());
  }
  private canUndo(board = this.state.board): boolean {
    return Boolean(board?.batch_cards && board.can_write !== false && this.undoEntry?.revision === board.revision && !this.state.readonly);
  }
  async refresh() {
    if (!this.current()) return;
    // A pushed change during a write must be read after its acknowledgement, never silently discarded.
    if (this.state.busy) { this.refreshAfterWrite = true; return; }
    await this.load();
  }
  private async load() {
    const read = ++this.reads;
    await Promise.allSettled([
      this.request<BoardState>("/workspace-board").then((board) => {
        if (read !== this.reads || !this.current()) return;
        if (this.state.board && board.revision < this.state.board.revision) return;
        const readonly = board.can_write === undefined ? this.state.readonly : !board.can_write;
        if (!board.batch_cards || readonly || this.undoEntry?.revision !== board.revision) this.undoEntry = null;
        this.set({
          board: sameValue(board, this.state.board) ? this.state.board : board,
          readonly, error: "", unsupported: false, canUndo: Boolean(this.undoEntry),
        });
      }).catch((e) => {
        if (read !== this.reads || !this.current()) return;
        const denied = e instanceof ApiError && e.status === 403;
        if (denied || boardUnsupported(e)) this.undoEntry = null;
        this.set({ error: boardError(e), unsupported: boardUnsupported(e), canUndo: Boolean(this.undoEntry), ...(denied ? { readonly: true } : {}) });
      }),
      this.request<WorkspaceList>("/workspaces").then((discovery) => {
        if (read === this.reads && this.current()) this.set({ discovery: sameValue(discovery, this.state.discovery) ? this.state.discovery : discovery, discoveryError: "" });
      }).catch((e) => { if (read === this.reads && this.current()) this.set({ discoveryError: errorText(e) }); }),
    ]);
    if (read === this.reads && this.current()) this.set({ loading: false });
  }
  async mutate(mutation: BoardMutation): Promise<boolean> {
    return this.write(mutation, false);
  }
  async undo(): Promise<boolean> {
    if (!this.current() || !this.canUndo() || this.state.busy || !this.undoEntry) return false;
    return this.write({ action: "batch_cards", changes: this.undoEntry.changes }, true);
  }
  private async write(mutation: BoardMutation, undo: boolean): Promise<boolean> {
    const previous = this.state.board;
    if (!this.current() || !previous || this.state.busy || this.state.readonly) return false;
    const batch = mutation.action === "batch_cards";
    if (batch && (!previous.batch_cards || !mutation.changes?.length)) return false;
    // Snapshot caller-owned changes: a later selection edit must not change the request or its inverse.
    const submitted = batch ? { ...mutation, changes: mutation.changes!.map((c) => ({ ...c })) } : { ...mutation };
    const inverse = batch && !undo ? inverseBoardChanges(previous, submitted.changes!) : null;
    ++this.reads;
    this.set({ busy: true, error: "", canUndo: false, board: optimisticBoard(previous, submitted) });
    let conflict = false;
    let failure = "";
    try {
      const board = await this.request<BoardState>("/workspace-board", { method: "POST", json: { revision: previous.revision, ...submitted } });
      if (!this.current()) return false;
      this.undoEntry = inverse && board.batch_cards && board.can_write !== false ? { revision: board.revision, changes: inverse } : null;
      this.set({ board, readonly: board.can_write === undefined ? this.state.readonly : !board.can_write });
      return true;
    } catch (e) {
      if (!this.current()) return false;
      conflict = e instanceof ApiError && e.status === 409;
      failure = boardError(e);
      const denied = e instanceof ApiError && e.status === 403;
      if (conflict || denied) this.undoEntry = null;
      this.set({ board: previous, error: failure, ...(denied ? { readonly: true } : {}) });
      return false;
    } finally {
      if (this.current()) {
        if (conflict) {
          // Keep write admission closed until authoritative recovery finishes.
          this.refreshAfterWrite = false;
          await this.load();
        }
        // The caller clears its submitted selection when this promise resolves. Keep controls busy until queued
        // reads finish too, so a newly selected card cannot be cleared by the previous submission's continuation.
        while (this.refreshAfterWrite && this.current()) {
          this.refreshAfterWrite = false;
          await this.load();
        }
        if (this.current()) {
          if (failure && !this.state.error) this.set({ error: conflict ? t("lib-workspace-board.conflict_reloaded") : failure });
          this.set({ busy: false, canUndo: this.canUndo() });
        }
      }
    }
  }
}
/** The hub has no board: an older one answers 404, one without board storage 503 not_supported. A 503 for anything
 *  else (assistant_offline while the manager restarts, a proxy in front of the hub) passes: Retry or the next read helps. */
export function boardUnsupported(e: unknown): boolean {
  return e instanceof ApiError && (e.status === 404 || (e.status === 503 && e.code === "not_supported"));
}
export function boardError(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.status === 403) return t("lib-workspace-board.read_only");
    if (e.status === 409) return t("lib-workspace-board.conflict_reloading");
    if (boardUnsupported(e)) return t("lib-workspace-board.unsupported");
  }
  return errorText(e);
}
