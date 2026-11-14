import { api, credentialOwner, enc, errorText, getToken } from "./api";
import { defaultKnown, defaultManager, MANAGER_HEADER } from "./manager-choice";
import { t } from "../i18n";

export interface ActivityValue {
  title: string; column_id?: string; column_title?: string; pinned?: boolean; archived?: boolean;
}
export interface ActivityChange {
  kind: string; id?: string; title?: string; fields?: string[];
  before?: ActivityValue; after?: ActivityValue; card_ids?: string[];
}
export interface ActivitySummary {
  revision: string; at: string; change_count: number; card_count: number; kinds: string[];
}
export interface ActivityPage {
  entries: ActivitySummary[]; next_cursor?: string; latest_revision: string; oldest_revision?: string; retention_limit: number;
}
export interface ActivityDetail {
  revision: string; at: string; changes: ActivityChange[]; total: number; next_offset?: number;
}
export interface DetailState {
  changes: ActivityChange[]; total: number; loading: boolean; error: string; nextOffset?: number; loaded?: boolean;
}
export interface ActivitySnapshot {
  entries: ActivitySummary[]; loading: boolean; loadingMore: boolean; error: string; moreError: string;
  nextCursor?: string; newer: boolean; oldestRevision?: string; retentionLimit: number;
  details: ReadonlyMap<string, DetailState>;
}
type Transport = <T>(path: string, init?: RequestInit) => Promise<T>;
const ROOT = "/workspace-board/activity";
const MAX_REVISION = 9223372036854775807n;
function revision(value: unknown, zero = false): value is string {
  return typeof value === "string" && /^(0|[1-9][0-9]{0,18})$/.test(value)
    && BigInt(value) <= MAX_REVISION && (zero || value !== "0");
}
function invalid(): never { throw new Error(t("workspaceboard-activity.invalid_response")); }
function readPage(page: ActivityPage, before?: string): ActivityPage {
  if (!page || !Array.isArray(page.entries) || page.entries.length > 50 || !revision(page.latest_revision, true)
    || (page.oldest_revision !== undefined && !revision(page.oldest_revision))
    || !Number.isSafeInteger(page.retention_limit) || page.retention_limit < 1) invalid();
  let upper = before ? BigInt(before) - 1n : BigInt(page.latest_revision);
  for (const entry of page.entries) {
    if (!revision(entry.revision) || BigInt(entry.revision) > upper || (typeof entry.at !== "string" || !Number.isFinite(Date.parse(entry.at)))
      || !Number.isSafeInteger(entry.change_count) || entry.change_count < 0
      || !Number.isSafeInteger(entry.card_count) || entry.card_count < 0 || !Array.isArray(entry.kinds)
      || !entry.kinds.every((kind) => typeof kind === "string")) invalid();
    upper = BigInt(entry.revision) - 1n;
  }
  if (page.next_cursor !== undefined && (!revision(page.next_cursor) || page.next_cursor !== page.entries.at(-1)?.revision)) invalid();
  return page;
}
function validValue(value: ActivityValue | undefined): boolean {
  return value === undefined || (value !== null && typeof value === "object" && typeof value.title === "string"
    && [value.column_id, value.column_title].every((item) => item === undefined || typeof item === "string")
    && [value.pinned, value.archived].every((item) => item === undefined || typeof item === "boolean"));
}
function validChange(change: ActivityChange): boolean {
  return Boolean(change && typeof change.kind === "string"
    && [change.id, change.title].every((item) => item === undefined || typeof item === "string")
    && [change.fields, change.card_ids].every((items) => items === undefined || (Array.isArray(items) && items.every((item) => typeof item === "string")))
    && validValue(change.before) && validValue(change.after));
}
function readDetail(page: ActivityDetail, id: string, offset: number): ActivityDetail {
  if (!page || page.revision !== id || !Array.isArray(page.changes) || page.changes.length > 100
    || !Number.isSafeInteger(page.total) || page.total < offset + page.changes.length
    || !page.changes.every(validChange)) invalid();
  if (page.next_offset !== undefined && (!Number.isSafeInteger(page.next_offset) || page.changes.length === 0
    || page.next_offset !== offset + page.changes.length || page.next_offset >= page.total)) invalid();
  if (page.next_offset === undefined && offset + page.changes.length < page.total) invalid();
  return page;
}

/** Read-only, bounded history. The view owns lifetime; closed dialogs do not poll or keep requests alive. */
export class BoardActivityModel {
  private state: ActivitySnapshot = { entries: [], loading: false, loadingMore: false, error: "", moreError: "", newer: false, retentionLimit: 500, details: new Map() };
  private listeners = new Set<() => void>();
  private controllers = new Set<AbortController>();
  private epoch = 0;
  private retired = false;
  private latest = "0";
  private announced = "0";
  private owner = credentialOwner();
  private manager = defaultManager();
  private production: boolean;
  private request: Transport;
  constructor(private cardID?: string, request?: Transport) {
    this.production = !request;
    const token = getToken();
    this.request = request ?? (<T>(path: string, init?: RequestInit) => api<T>(path, {
      ...init, headers: { [MANAGER_HEADER]: this.manager }, token, tokenOwner: this.owner, isAuthContextCurrent: () => this.current(),
    }));
  }
  private scopeCurrent = () => (!this.production || defaultKnown()) && this.owner === credentialOwner() && this.manager === defaultManager();
  private current = () => !this.retired && this.scopeCurrent();
  snapshot = () => this.state;
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; };
  private set(patch: Partial<ActivitySnapshot>) {
    this.state = { ...this.state, ...patch };
    this.listeners.forEach((listener) => listener());
  }
  private setDetail(id: string, state: DetailState) {
    const details = new Map(this.state.details); details.set(id, state); this.set({ details });
  }
  private cancel() {
    this.epoch++;
    this.controllers.forEach((controller) => controller.abort());
    this.controllers.clear();
  }
  dispose() { this.retired = true; this.cancel(); }
  private async read<T>(path: string): Promise<T> {
    const controller = new AbortController(); this.controllers.add(controller);
    try { return await this.request<T>(path, { signal: controller.signal }); }
    finally { this.controllers.delete(controller); }
  }
  private path(before?: string): string {
    const query = new URLSearchParams({ limit: "20" });
    if (this.cardID) query.set("card_id", this.cardID);
    if (before) query.set("before", before);
    return `${ROOT}?${query}`;
  }
  noteRevision(value: string | number) {
    // Live board revisions are numbers on existing wire events. Never invent precision when that wire overflows.
    if (typeof value === "number" && !Number.isSafeInteger(value)) return;
    const id = String(value);
    if (!this.current() || !revision(id, true)) return;
    if (BigInt(id) > BigInt(this.announced)) this.announced = id;
    if (BigInt(id) > BigInt(this.latest) && !this.state.newer) this.set({ newer: true });
  }
  async refresh(): Promise<void> {
    if (!this.scopeCurrent() || (this.state.loading && !this.retired)) return;
    // Explicit reopen is allowed for React StrictMode's setup/cleanup replay; the epoch fences abandoned work.
    this.retired = false; this.cancel(); const epoch = this.epoch;
    const details = new Map([...this.state.details].map(([id, item]) => [id, { ...item, loading: false }]));
    this.set({ loading: true, loadingMore: false, error: "", moreError: "", details });
    try {
      const page = readPage(await this.read<ActivityPage>(this.path()));
      if (!this.current() || epoch !== this.epoch) return;
      this.latest = page.latest_revision;
      const kept = new Set(page.entries.map((entry) => entry.revision));
      this.set({ entries: page.entries, nextCursor: page.next_cursor, oldestRevision: page.oldest_revision,
        retentionLimit: page.retention_limit, newer: BigInt(this.announced) > BigInt(this.latest),
        details: new Map([...this.state.details].filter(([id]) => kept.has(id))),
      });
    } catch (error) { if (this.current() && epoch === this.epoch) this.set({ error: errorText(error) }); }
    finally { if (this.current() && epoch === this.epoch) this.set({ loading: false }); }
  }
  async more(): Promise<void> {
    const before = this.state.nextCursor;
    if (!this.current() || this.state.loading || this.state.loadingMore || !before) return;
    const epoch = this.epoch; this.set({ loadingMore: true, moreError: "" });
    try {
      const page = readPage(await this.read<ActivityPage>(this.path(before)), before);
      if (!this.current() || epoch !== this.epoch) return;
      const ids = new Set(this.state.entries.map((entry) => entry.revision));
      const additions = page.entries.filter((entry) => !ids.has(entry.revision));
      if (page.next_cursor && additions.length === 0) invalid();
      this.set({ entries: [...this.state.entries, ...additions], nextCursor: page.next_cursor,
        oldestRevision: page.oldest_revision, newer: this.state.newer || BigInt(page.latest_revision) > BigInt(this.latest) });
    } catch (error) { if (this.current() && epoch === this.epoch) this.set({ moreError: errorText(error) }); }
    finally { if (this.current() && epoch === this.epoch) this.set({ loadingMore: false }); }
  }
  async detail(id: string): Promise<void> {
    if (!this.current() || this.state.loading || !revision(id) || !this.state.entries.some((entry) => entry.revision === id)) return;
    const previous = this.state.details.get(id);
    if (previous?.loading || previous?.loaded) return;
    await this.loadDetail(id, 0);
  }
  async moreDetails(id: string): Promise<void> {
    const previous = this.state.details.get(id);
    if (!this.current() || this.state.loading || !previous || previous.loading || previous.nextOffset === undefined) return;
    await this.loadDetail(id, previous.nextOffset);
  }
  private async loadDetail(id: string, offset: number) {
    const previous = this.state.details.get(id) ?? { changes: [], total: 0, loading: false, error: "" };
    const epoch = this.epoch;
    this.setDetail(id, { ...previous, loading: true, error: "" });
    try {
      const page = readDetail(await this.read<ActivityDetail>(`${ROOT}/${enc(id)}?offset=${offset}&limit=30`), id, offset);
      if (!this.current() || epoch !== this.epoch) return;
      if (offset > 0 && page.total !== previous.total) invalid();
      this.setDetail(id, { changes: offset ? [...previous.changes, ...page.changes] : page.changes, total: page.total,
        nextOffset: page.next_offset, loading: false, error: "", loaded: true });
    } catch (error) {
      if (this.current() && epoch === this.epoch) this.setDetail(id, { ...previous, loading: false, error: errorText(error) });
    }
  }
}
