import { api, ApiError, credentialOwner, errorText, getToken } from "./api";
import { defaultKnown, defaultManager, MANAGER_HEADER } from "./manager-choice";
import { t } from "../i18n";
import { sameValue } from "./stable-lines";

export const SAVED_SEARCH_LIMIT = 50;
export const SAVED_SEARCH_NAME_MAX = 64;
export const SAVED_SEARCH_QUERY_MAX = 1024;
export interface SavedAgentSearch { id: string; name: string; query: string; created_at: string; updated_at: string }
export interface SavedAgentSearches { revision: string; searches: SavedAgentSearch[]; can_write: boolean }
export type SavedSearchMutation =
  | { action: "add"; name: string; query: string }
  | { action: "update"; id: string; name: string; query: string }
  | { action: "delete"; id: string }
  | { action: "move"; id: string; before_id?: string }
  | { action: "restore"; item: SavedAgentSearch; before_id?: string };
export interface SavedAgentSearchesSnapshot {
  document: SavedAgentSearches | null; loading: boolean; busy: boolean; error: string;
  readonly: boolean; unsupported: boolean; canUndo: boolean; conflict: boolean;
}
type Transport = <T>(path: string, init?: RequestInit & { json?: unknown }) => Promise<T>;
const PATH = "/saved-agent-searches";
const REVISION_MAX = 9223372036854775807n;
const revisionValid = (value: unknown): value is string => typeof value === "string" && /^(0|[1-9][0-9]{0,18})$/.test(value) && BigInt(value) <= REVISION_MAX;
const text = (key: string) => t(`saved-agent-searches.${key}`);

/** Save query definitions only. A leading > is the palette's command mode, not an Agent search. */
export function savedSearchProblem(name: string, query: string): string {
  const label = name.trim(), value = query.trim();
  if (!label || [...label].length > SAVED_SEARCH_NAME_MAX || /\p{Cc}/u.test(label)) return text("name_invalid");
  if (!value || value.startsWith(">") || new TextEncoder().encode(value).length > SAVED_SEARCH_QUERY_MAX || /\p{Cc}/u.test(value)) return text("query_invalid");
  return "";
}
function validItem(item: SavedAgentSearch): boolean {
  return Boolean(item && typeof item.id === "string" && /^search_[a-f0-9]{24}$/.test(item.id)
    && typeof item.name === "string" && typeof item.query === "string" && !savedSearchProblem(item.name, item.query)
    && item.name === item.name.trim() && item.query === item.query.trim()
    && typeof item.created_at === "string" && Number.isFinite(Date.parse(item.created_at))
    && typeof item.updated_at === "string" && Number.isFinite(Date.parse(item.updated_at))
    && Date.parse(item.updated_at) >= Date.parse(item.created_at));
}
function readDocument(document: SavedAgentSearches): SavedAgentSearches {
  if (!document || !revisionValid(document.revision) || !Array.isArray(document.searches) || document.searches.length > SAVED_SEARCH_LIMIT
    || typeof document.can_write !== "boolean" || !document.searches.every(validItem)
    || new Set(document.searches.map((item) => item.id)).size !== document.searches.length) throw new Error(text("invalid_response"));
  return document;
}
function unsupported(error: unknown): boolean {
  return error instanceof ApiError && ((error.status === 404 && error.code !== "search_not_found" && error.code !== "no_manager") || error.status === 503 && error.code === "not_supported");
}
function isConflict(error: unknown): boolean {
  return error instanceof ApiError && error.status === 409 && (error.code === "conflict" || error.code === undefined);
}
function message(error: unknown): string {
  if (unsupported(error)) return text("unsupported");
  if (error instanceof ApiError && error.status === 403) return text("read_only");
  if (isConflict(error)) return text("conflict");
  return errorText(error);
}
function project(document: SavedAgentSearches, mutation: SavedSearchMutation): SavedAgentSearches {
  if (mutation.action === "add" || mutation.action === "restore") return document; // server owns identities/times
  const searches = document.searches.map((item) => ({ ...item }));
  const index = searches.findIndex((item) => item.id === mutation.id);
  if (index < 0) return document;
  if (mutation.action === "delete") searches.splice(index, 1);
  else if (mutation.action === "update") searches[index] = { ...searches[index], name: mutation.name.trim(), query: mutation.query.trim() };
  else if (mutation.id !== mutation.before_id) {
    const [item] = searches.splice(index, 1);
    const before = mutation.before_id ? searches.findIndex((value) => value.id === mutation.before_id) : searches.length;
    if (before < 0) return document;
    searches.splice(before, 0, item);
  }
  return { ...document, searches };
}

/** One default-manager/credential owner. The palette and its management dialog share this lifetime. */
export class SavedAgentSearchesModel {
  private state: SavedAgentSearchesSnapshot = { document: null, loading: false, busy: false, error: "", readonly: false, unsupported: false, canUndo: false, conflict: false };
  private listeners = new Set<() => void>();
  private confirmed: SavedAgentSearches | null = null;
  private owner = credentialOwner();
  private manager = defaultManager();
  private production: boolean;
  private request: Transport;
  private retired = false;
  private reads = 0;
  private epoch = 0;
  private controllers = new Set<AbortController>();
  private refreshAfterWrite = false;
  private undoEntry: { revision: string; item: SavedAgentSearch; before_id?: string } | null = null;
  constructor(request?: Transport) {
    this.production = !request;
    const token = getToken();
    this.request = request ?? (<T>(path: string, init?: Parameters<Transport>[1]) => api<T>(path, {
      ...init, headers: { [MANAGER_HEADER]: this.manager }, token, tokenOwner: this.owner, isAuthContextCurrent: this.current,
    }));
  }
  private scopeCurrent = () => (!this.production || defaultKnown()) && this.owner === credentialOwner() && this.manager === defaultManager();
  private current = () => !this.retired && this.scopeCurrent();
  snapshot = () => this.state;
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; };
  private set(patch: Partial<SavedAgentSearchesSnapshot>) {
    if ((Object.keys(patch) as (keyof SavedAgentSearchesSnapshot)[]).every((key) => Object.is(this.state[key], patch[key]))) return;
    this.state = { ...this.state, ...patch }; this.listeners.forEach((listener) => listener());
  }
  private async send<T>(init?: Parameters<Transport>[1]): Promise<T> {
    const controller = new AbortController(); this.controllers.add(controller);
    try { return await this.request<T>(PATH, { ...init, signal: controller.signal }); }
    finally { this.controllers.delete(controller); }
  }
  dispose() {
    this.retired = true; this.epoch++; this.reads++;
    this.controllers.forEach((controller) => controller.abort()); this.controllers.clear();
    this.refreshAfterWrite = false; this.undoEntry = null;
    this.set({ document: this.confirmed, loading: false, busy: false, canUndo: false });
  }
  private accept(document: SavedAgentSearches) {
    this.confirmed = sameValue(document, this.confirmed) ? this.confirmed : document;
    if (!document.can_write || this.undoEntry?.revision !== document.revision) this.undoEntry = null;
    this.set({ document: this.confirmed,
      readonly: !document.can_write, unsupported: false, error: "", conflict: false, canUndo: Boolean(this.undoEntry) });
  }
  async refresh(): Promise<void> {
    if (!this.scopeCurrent()) return;
    this.retired = false;
    if (this.state.busy) { this.refreshAfterWrite = true; return; }
    await this.load();
  }
  private async load(): Promise<void> {
    const read = ++this.reads, epoch = this.epoch;
    this.set({ loading: true });
    try {
      const document = readDocument(await this.send<SavedAgentSearches>());
      if (!this.current() || read !== this.reads || epoch !== this.epoch) return;
      if (this.state.document && BigInt(document.revision) < BigInt(this.state.document.revision)) return;
      this.accept(document);
    } catch (error) {
      if (!this.current() || read !== this.reads || epoch !== this.epoch) return;
      const noWrite = unsupported(error) || error instanceof ApiError && error.status === 403;
      if (noWrite) this.undoEntry = null;
      this.set({ error: message(error), unsupported: unsupported(error), ...(noWrite ? { readonly: true, canUndo: false } : {}) });
    } finally { if (this.current() && read === this.reads && epoch === this.epoch) this.set({ loading: false }); }
  }
  async mutate(mutation: SavedSearchMutation, expectedRevision?: string): Promise<boolean> {
    return this.write(mutation, expectedRevision, false);
  }
  async undo(): Promise<boolean> {
    if (!this.current() || !this.state.canUndo || !this.undoEntry || this.state.busy) return false;
    const { item, before_id, revision } = this.undoEntry;
    return this.write({ action: "restore", item, before_id }, revision, true);
  }
  private async write(mutation: SavedSearchMutation, expectedRevision: string | undefined, undo: boolean): Promise<boolean> {
    const previous = this.state.document;
    if (!this.current() || !previous || this.state.busy || this.state.readonly || this.state.unsupported) return false;
    const expected = expectedRevision ?? previous.revision;
    if (!revisionValid(expected) || expected !== previous.revision) {
      this.set({ error: text("conflict"), conflict: true }); return false;
    }
    if (mutation.action === "add" || mutation.action === "update") {
      const problem = savedSearchProblem(mutation.name, mutation.query);
      if (problem) { this.set({ error: problem }); return false; }
    }
    const submitted: SavedSearchMutation = mutation.action === "restore" ? { ...mutation, item: { ...mutation.item } } : { ...mutation };
    const deletedAt = submitted.action === "delete" ? previous.searches.findIndex((item) => item.id === submitted.id) : -1;
    const deleted = deletedAt >= 0 ? { item: { ...previous.searches[deletedAt] }, before_id: previous.searches[deletedAt + 1]?.id } : null;
    const epoch = this.epoch; ++this.reads;
    this.set({ busy: true, loading: false, error: "", conflict: false, canUndo: false, document: project(previous, submitted) });
    let failure = "", conflict = false;
    try {
      const document = readDocument(await this.send<SavedAgentSearches>({ method: "POST", json: { revision: expected, ...submitted } }));
      if (!this.current() || epoch !== this.epoch) return false;
      if (BigInt(document.revision) !== BigInt(expected) + 1n) throw new Error(text("invalid_response"));
      this.undoEntry = !undo && deleted && document.can_write ? { ...deleted, revision: document.revision } : null;
      this.accept(document);
      return true;
    } catch (error) {
      if (!this.current() || epoch !== this.epoch) return false;
      failure = message(error); conflict = isConflict(error);
      const noWrite = unsupported(error) || error instanceof ApiError && error.status === 403;
      if (conflict || noWrite) this.undoEntry = null;
      this.set({ document: previous, error: failure, conflict, ...(noWrite ? { readonly: true, unsupported: unsupported(error) } : {}) });
      return false;
    } finally {
      if (this.current() && epoch === this.epoch) {
        if (conflict) { this.refreshAfterWrite = false; await this.load(); }
        while (this.refreshAfterWrite && this.current() && epoch === this.epoch) {
          this.refreshAfterWrite = false; await this.load();
        }
        if (this.current() && epoch === this.epoch) {
          if (failure && !this.state.error) this.set({ error: failure });
          this.set({ busy: false, conflict, canUndo: Boolean(this.undoEntry && !this.state.readonly && this.undoEntry.revision === this.state.document?.revision) });
        }
      }
    }
  }
}
