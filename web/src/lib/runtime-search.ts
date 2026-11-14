import { api, ApiError, credentialOwner, errorText, getToken } from "./api";
import { foldedRow } from "./folded-rows";
import { runtimeIdentity, runtimeOrder } from "./runtime-list";
import type { EvidenceSnippet } from "./agent-chatlist-filter";
import type { Session } from "./types";
import { t } from "../i18n";

export const RUNTIME_SEARCH_FEATURE = "sessions_search";
export const RUNTIME_SEARCH_PAGE_SIZE = 40;
export interface RuntimeSearchHit { session: Session; score: number; evidence?: EvidenceSnippet }
export interface RuntimeSearchSnapshot {
  query: string; hits: RuntimeSearchHit[]; loaded: boolean; loading: boolean; loadingMore: boolean; error: string; moreError: string;
  hasMore: boolean; total: number; expired: boolean; supported: boolean; retryable: boolean;
}
interface WireHit { score: number; evidence?: Omit<EvidenceSnippet, "roleLabel"> }
interface SearchPage {
  sessions: Session[]; search_hits: Record<string, WireHit>; has_more: boolean; next_cursor?: string; revision: string; total: number;
}
type Transport = <T>(path: string, init?: RequestInit) => Promise<T>;
const empty = (query = ""): RuntimeSearchSnapshot => ({ query, hits: [], loaded: false, loading: false, loadingMore: false, error: "", moreError: "", hasMore: false, total: 0, expired: false, supported: true, retryable: true });
class UnsupportedSearch extends Error {}
const invalid = () => new Error(t("runtime-search.invalid_response"));
const roles = new Set(["user", "agent", "error", "title"]);
function hitEvidence(value: WireHit["evidence"]): EvidenceSnippet | undefined {
  if (value === undefined) return;
  if (!value || !roles.has(value.role) || typeof value.before !== "string" || typeof value.match !== "string" || typeof value.after !== "string"
    || value.before.length > 512 || value.match.length > 2048 || value.after.length > 512) throw invalid();
  return { ...value, roleLabel: t(`lib-agent-filter.role_${value.role}`) };
}
function readPage(page: SearchPage): RuntimeSearchHit[] {
  if (!page || page.search_hits === undefined) throw new UnsupportedSearch();
  if (!Array.isArray(page.sessions) || page.sessions.length > RUNTIME_SEARCH_PAGE_SIZE || !page.search_hits || typeof page.search_hits !== "object" || Array.isArray(page.search_hits)
    || typeof page.has_more !== "boolean" || typeof page.revision !== "string" || !page.revision
    || !Number.isSafeInteger(page.total) || page.total < page.sessions.length
    || (page.has_more && (typeof page.next_cursor !== "string" || !page.next_cursor || !page.sessions.length))
    || (page.next_cursor !== undefined && typeof page.next_cursor !== "string")) throw invalid();
  const ids = new Set<string>();
  const hits = page.sessions.map((session): RuntimeSearchHit => {
    if (!session || typeof session.machine_id !== "string" || !session.machine_id || typeof session.session_id !== "string" || !session.session_id || session.assistant) throw invalid();
    const id = runtimeIdentity(session);
    const match = Object.hasOwn(page.search_hits, id) ? page.search_hits[id] : undefined;
    if (ids.has(id) || !match || !Number.isFinite(match.score) || match.score <= 0 || match.score > 200) throw invalid();
    ids.add(id);
    return { session: foldedRow(session), score: match.score, evidence: hitEvidence(match.evidence) };
  });
  // Evidence for a row not in the response is never allowed to leak through a side map.
  if (Object.keys(page.search_hits).some((id) => !ids.has(id))) throw invalid();
  for (let i = 1; i < hits.length; i++) if (compareSearchHits(hits[i - 1], hits[i]) > 0) throw invalid();
  return hits;
}
export function compareSearchHits(a: RuntimeSearchHit, b: RuntimeSearchHit): number {
  return b.score - a.score || runtimeOrder(a.session, b.session);
}

/** A query owns a frozen result window. Typing/closing/sign-in replaces it; it never fills the global session cache. */
export class RuntimeSearchModel {
  private state = empty();
  private listeners = new Set<() => void>();
  private owner = credentialOwner();
  private request: Transport;
  private timer?: ReturnType<typeof setTimeout>;
  private controller?: AbortController;
  private generation = 0;
  private retired = false;
  private manager?: string;
  private cursor?: string;
  private revision?: string;
  private cursors = new Set<string>();
  private failure: "first" | "more" = "first";
  constructor(request?: Transport, private debounceMS = 200) {
    const token = getToken();
    this.request = request ?? (<T>(path: string, init?: RequestInit) => api<T>(path, {
      ...init, token, tokenOwner: this.owner, isAuthContextCurrent: () => this.current(),
    }));
  }
  private current = () => !this.retired && this.owner === credentialOwner();
  snapshot = () => this.state;
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener); }; };
  private set(patch: Partial<RuntimeSearchSnapshot>) {
    this.state = { ...this.state, ...patch }; this.listeners.forEach((listener) => listener());
  }
  private cancel() {
    this.generation++;
    clearTimeout(this.timer); this.timer = undefined;
    this.controller?.abort(); this.controller = undefined;
  }
  dispose() { this.retired = true; this.cancel(); }
  search(query: string, options: { manager?: string } = {}): void {
    if (this.owner !== credentialOwner()) return;
    const text = query.trim();
    if (!this.retired && text === this.state.query && options.manager === this.manager) return;
    this.retired = false; this.cancel(); this.manager = options.manager;
    this.cursor = this.revision = undefined; this.cursors.clear(); this.failure = "first";
    this.state = empty(text);
    if (!text) { this.set({}); return; }
    if (new TextEncoder().encode(text).length > 1024) { this.set({ error: t("runtime-search.query_too_long"), retryable: false }); return; }
    this.set({ loading: true });
    this.timer = setTimeout(() => { this.timer = undefined; void this.load(false); }, this.debounceMS);
  }
  async more(): Promise<void> {
    if (!this.current() || this.state.loading || this.state.loadingMore || !this.state.hasMore || this.state.expired || !this.cursor) return;
    await this.load(true);
  }
  async retry(): Promise<void> {
    if (!this.current() || !this.state.query || this.state.loading || this.state.loadingMore || !this.state.supported) return;
    if (new TextEncoder().encode(this.state.query).length > 1024) return;
    if (this.failure === "more" && !this.state.expired && this.cursor) return this.load(true);
    this.cancel(); this.cursor = this.revision = undefined; this.cursors.clear();
    this.set({ hits: [], loaded: false, total: 0, hasMore: false, expired: false });
    await this.load(false);
  }
  private async load(more: boolean): Promise<void> {
    if (!this.current()) return;
    const generation = this.generation, cursor = more ? this.cursor : undefined;
    const controller = new AbortController(); this.controller = controller;
    const query = new URLSearchParams({ search: this.state.query, summary: "1", roots: "1", limit: String(RUNTIME_SEARCH_PAGE_SIZE) });
    if (cursor) query.set("cursor", cursor);
    if (this.manager) query.set("manager", this.manager);
    this.set(more ? { loadingMore: true, moreError: "" } : { loading: true, error: "", moreError: "" });
    try {
      const page = await this.request<SearchPage>(`/sessions?${query}`, { signal: controller.signal });
      if (!this.current() || generation !== this.generation || controller.signal.aborted) return;
      const hits = readPage(page);
      if (more && (page.revision !== this.revision || page.total !== this.state.total)) throw invalid();
      if (page.has_more && (page.next_cursor === cursor || this.cursors.has(page.next_cursor!))) throw invalid();
      const previous = more ? this.state.hits : [];
      const ids = new Set(previous.map((hit) => runtimeIdentity(hit.session)));
      if (hits.some((hit) => ids.has(runtimeIdentity(hit.session)))) throw invalid();
      if (previous.length && hits.length && compareSearchHits(previous[previous.length - 1], hits[0]) > 0) throw invalid();
      const all = [...previous, ...hits];
      if (all.length > page.total || (!page.has_more && all.length !== page.total)) throw invalid();
      this.revision = page.revision; this.cursor = page.has_more ? page.next_cursor : undefined;
      if (cursor) this.cursors.add(cursor);
      this.set({ hits: all, loaded: true, total: page.total, hasMore: page.has_more, expired: false, supported: true, retryable: true });
    } catch (error) {
      if (!this.current() || generation !== this.generation || controller.signal.aborted) return;
      this.failure = more ? "more" : "first";
      const unsupported = error instanceof UnsupportedSearch || error instanceof ApiError && (error.status === 404 || error.code === "not_supported");
      const expired = error instanceof ApiError && (error.status === 409 || (more && error.status === 400 && error.code === "invalid_query"));
      const text = unsupported ? t("runtime-search.unsupported") : expired ? t("runtime-search.expired") : errorText(error);
      this.set({ ...(more ? { moreError: text } : { error: text }), expired, supported: !unsupported, retryable: !unsupported, ...(unsupported ? { hasMore: false } : {}) });
    } finally {
      if (this.current() && generation === this.generation) {
        if (this.controller === controller) this.controller = undefined;
        this.set({ loading: false, loadingMore: false });
      }
    }
  }
}
