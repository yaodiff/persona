// The assistant's tracks and domain spaces (#13500, docs/assistant/health.md "Tracks and kinds", docs/web/web.md
// "Domain spaces"): a typed client for the track API, SPACE.md, a conversation's information categories and what each
// extension may read, and the list the sidebar's Spaces section shows. Every route here is the default manager's
// (`?space=` and the track routes name no manager, docs/assistant/managers.md "Clients"), as the health space's are.
import { useSyncExternalStore } from "react";
import { api, ApiError, enc } from "./api";
import { onHubEvent } from "./store";
import type { Session } from "./types";
import type { IconName } from "../ui/icons";

/** The built-in health space's slug: it keeps its own page and sidebar entry (/health), never a row under Spaces. */
export const HEALTH_TRACK = "health";
/** The sidebar group a kind lists its tracks under to appear in the Spaces section (TrackSidebar.group). */
export const SPACES_GROUP = "spaces";
/** The kind a new space defaults to. */
export const DEFAULT_KIND = "space";

/** The information categories (server/internal/assistant/track_registry.go infoCategories), in the order shown. */
export const INFO_CATEGORIES = ["health", "finance", "relationships", "emotion", "legal_id"] as const;
export type InfoCategory = (typeof INFO_CATEGORIES)[number];

export interface TrackRhythm {
  title: string;
  cron: string;
  tz?: string;
  prompt: string;
  topic_policy: string;
  enabled: boolean;
}

export interface TrackUsageRule {
  scene: string;
  category: string;
  rule: string;
}

export interface TrackSidebar {
  group?: string;
  order?: number;
  icon?: string;
}

/** assistantwire.TrackKindConfig: what a track of a kind does. */
export interface TrackKindConfig {
  schema: number;
  template: string;
  user_instructions?: string;
  context: string;
  default_categories: string[];
  tag_focus: string[];
  rhythms: TrackRhythm[];
  attention: { push_cap: string; deep_default: string; enter_mode: string };
  supervision: string;
  usage: TrackUsageRule[];
  assembly: string;
  capture_target: boolean;
  channels: string[];
  sidebar: TrackSidebar;
}

/** assistantwire.Track: one track with its effective config. */
export interface Track {
  slug: string;
  kind: string;
  title: string;
  icon: string;
  description: string;
  default_categories: string[];
  template: string;
  config: TrackKindConfig;
  builtin: boolean;
  archived: boolean;
  revision: number;
}

/** assistantwire.TrackKind. `disabled`: why its tracks are left out ("schema", "invalid"). */
export interface TrackKind {
  slug: string;
  title: string;
  config: TrackKindConfig;
  builtin: boolean;
  revision: number;
  disabled?: string;
  warnings?: string[];
}

/** assistantwire.TrackCreate. */
export interface TrackCreate {
  slug: string;
  kind: string;
  title: string;
  icon: string;
  description: string;
  default_categories?: string[];
  template?: string;
}

/** assistantwire.TrackChange: the fields given change; `revision` is the one the change was made against. */
export interface TrackChange {
  revision: number;
  title?: string;
  icon?: string;
  description?: string;
  default_categories?: string[];
  archived?: boolean;
}

/** GET / PUT /assistant/tracks/{slug}/space-md: the whole file, its digest, and the managed state block's text. */
export interface SpaceMd {
  content: string;
  digest: string;
  state: string;
}

/** assistantwire.ConversationCategories. */
export interface ConversationCategories {
  categories: string[];
  floor: string[];
  removed: string[];
}

/** assistantwire.ExtensionTrust: "owner" reads everything; "third_party" only the categories it was granted. */
export interface ExtensionTrust {
  extension?: string;
  trust: "owner" | "third_party";
  categories: string[];
}

// ---- the track API ----

/** Every track, built-in and archived ones included. An answer without a list is an assistant without tracks. */
export async function listTracks(): Promise<Track[]> {
  const r = await api<{ tracks?: Track[] }>("/assistant/tracks");
  if (!Array.isArray(r?.tracks)) throw new TracksUnsupported();
  return r.tracks;
}

export function createTrack(body: TrackCreate): Promise<Track> {
  return api<Track>("/assistant/tracks", { method: "POST", json: body });
}

export function changeTrack(slug: string, change: TrackChange): Promise<Track> {
  return api<Track>(`/assistant/tracks/${enc(slug)}`, { method: "PUT", json: change });
}

/** The kinds a new track may be of: enabled ones (a disabled kind's tracks are left out). */
export async function listTrackKinds(): Promise<TrackKind[]> {
  const r = await api<{ kinds?: TrackKind[] }>("/assistant/track-kinds");
  return Array.isArray(r?.kinds) ? r.kinds : [];
}

export function readSpaceMd(slug: string): Promise<SpaceMd> {
  return api<SpaceMd>(`/assistant/tracks/${enc(slug)}/space-md`);
}

/** Writes SPACE.md's human area; `baseDigest` is the digest of the content the edit started from. */
export function writeSpaceMd(slug: string, content: string, baseDigest: string): Promise<SpaceMd> {
  return api<SpaceMd>(`/assistant/tracks/${enc(slug)}/space-md`, { method: "PUT", json: { content, base_digest: baseDigest } });
}

/** The space's conversations and the computer they run on (GET /assistant?space=<slug>). */
export function listSpaceConversations(slug: string): Promise<{ machine_id?: string; conversations?: Session[] }> {
  return api<{ machine_id?: string; conversations?: Session[] }>(`/assistant?space=${enc(slug)}`);
}

/** Starts a conversation in the space, as the health page starts one in its space. */
export function startSpaceConversation(slug: string, body: Record<string, unknown>) {
  return api<{ session: Session }>(`/assistant/conversations?space=${enc(slug)}`, { method: "POST", json: { ...body, space: slug } });
}

export function readConversationCategories(sid: string): Promise<ConversationCategories> {
  return api<ConversationCategories>(`/assistant/conversations/${enc(sid)}/categories`);
}

export function changeConversationCategories(sid: string, change: { add?: string[]; remove?: string[] }): Promise<ConversationCategories> {
  return api<ConversationCategories>(`/assistant/conversations/${enc(sid)}/categories`, { method: "PUT", json: change });
}

/** What every extension the user decided about may read (one with no row is a third party's with no grant); null from
 *  an assistant without grants. The scoped integration adapter keeps these operations for pre-roster hubs. */
export async function listExtensionTrust(): Promise<ExtensionTrust[] | null> {
  const r = await api<{ items?: ExtensionTrust[] }>("/assistant/extension-trust");
  return Array.isArray(r?.items) ? r.items : null;
}

export function setExtensionTrust(id: string, trust: ExtensionTrust): Promise<ExtensionTrust> {
  return api<ExtensionTrust>(`/assistant/extension-trust/${enc(id)}`, { method: "PUT", json: { trust: trust.trust, categories: trust.categories } });
}

/** An extension's trust as read from the list: no row is a third party's with no grant. */
export function trustOf(items: readonly ExtensionTrust[], id: string): ExtensionTrust {
  const row = items.find((x) => x.extension === id);
  return row ? { trust: row.trust === "owner" ? "owner" : "third_party", categories: [...(row.categories ?? [])] } : { trust: "third_party", categories: [] };
}

/** The list was answered without tracks: an assistant from before them. */
export class TracksUnsupported extends Error {
  constructor() {
    super("tracks unsupported");
  }
}

// ---- pure helpers ----

/** The tracks the sidebar's Spaces section lists: not archived, in the spaces group, the health space left out (it keeps
 *  its own entry). Ordered by the kind's sidebar order, then by title. */
export function sidebarSpaces(tracks: readonly Track[]): Track[] {
  return tracks
    .filter((x) => !x.archived && x.slug !== HEALTH_TRACK && x.config?.sidebar?.group === SPACES_GROUP)
    .sort((a, b) => (a.config.sidebar.order ?? 0) - (b.config.sidebar.order ?? 0) || a.title.localeCompare(b.title) || a.slug.localeCompare(b.slug));
}

/** The icons a space may take (design-system names, which the server checks as tokens: never a symbol's or a color). */
export const SPACE_ICONS: readonly IconName[] = ["layers", "book", "wallet", "home", "leaf", "car", "calendar", "star", "flag", "users", "music", "camera", "chart", "target", "smile", "sun"];

/** A track's icon as one the web draws: its own when it names one of the space icons, else its kind's, else layers. */
export function spaceIcon(track: Pick<Track, "icon" | "config"> | undefined): IconName {
  const own = track?.icon || track?.config?.sidebar?.icon || "";
  return (SPACE_ICONS as readonly string[]).includes(own) ? (own as IconName) : "layers";
}

const RESERVED = ["main", "health", "assistant", "guest", "spaces", "tracks"];

/** identity.ValidTrackSlug: a lowercase letter, then 1 to 31 lowercase letters, digits or dashes; no reserved name. */
export function validSlug(slug: string): boolean {
  return /^[a-z][a-z0-9-]{1,31}$/.test(slug) && !RESERVED.includes(slug);
}

/** Slugs the server takes but the web cannot open as a space's address (`/tracks/new` is the new space form). */
const PAGE_WORDS = ["new"];

/** Whether the new space form takes `slug`: valid for the server, and an address of its own here. */
export function formSlug(slug: string): boolean {
  return validSlug(slug) && !PAGE_WORDS.includes(slug);
}

/** A slug suggested from a title: its Latin letters and digits, dashes between words; a title with none of them (Chinese,
 *  Japanese) gets "space". A reserved or taken one gets a number. */
export function suggestSlug(title: string, taken: readonly string[] = []): string {
  let base = title
    .normalize("NFKD")
    .replace(/[\u0300-\u036f]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^[^a-z]+/, "")
    .slice(0, 28)
    .replace(/-+$/, "");
  if (base.length < 2) base = "space";
  if (formSlug(base) && !taken.includes(base)) return base;
  for (let n = 2; n < 1000; n++) {
    const s = `${base}-${n}`;
    if (formSlug(s) && !taken.includes(s)) return s;
  }
  return base;
}

/** How a SPACE.md read or write failed, for the words the editor shows. */
export type SpaceMdError = "stale" | "managed" | "missing" | "offline" | "other";

export function spaceMdError(e: unknown): SpaceMdError {
  if (!(e instanceof ApiError)) return "other";
  if (e.status === 409) return "stale";
  if (e.status === 400) return "managed";
  if (e.status === 404) return "missing";
  if (e.status === 503) return "offline";
  return "other";
}

// ---- the tracks this page knows ----

type TracksState = { tracks: Track[] | null; supported: boolean | null };
let state: TracksState = { tracks: null, supported: null };
const listeners = new Set<() => void>();
let loading: Promise<void> | null = null;
let helloOff: (() => void) | null = null;
let visOff: (() => void) | null = null;

function set(next: TracksState) {
  state = next;
  listeners.forEach((l) => l());
}

/** Reads the tracks again. A failed read keeps what was known; an assistant without tracks hides the section. */
export function loadTracks(): Promise<void> {
  loading ??= listTracks()
    .then((tracks) => set({ tracks, supported: true }))
    .catch((e) => {
      if (e instanceof TracksUnsupported || (e instanceof ApiError && (e.status === 404 || e.status === 405))) set({ tracks: null, supported: false });
    })
    .finally(() => {
      loading = null;
    });
  return loading;
}

/** A track the page just created or changed, shown at once (the hub's answer), before the next read. */
export function noteTrack(track: Track) {
  const list = state.tracks ?? [];
  const at = list.findIndex((x) => x.slug === track.slug);
  set({ tracks: at < 0 ? [...list, track] : list.map((x, i) => (i === at ? track : x)), supported: true });
}

function subscribe(l: () => void) {
  listeners.add(l);
  if (listeners.size === 1) {
    if (state.supported === null) void loadTracks();
    // read again on every hello or return to foreground: one that failed while the hub was away, or tracks another device changed
    helloOff ??= onHubEvent("hello", () => void loadTracks());
    if (!visOff && typeof document !== "undefined") {
      const onVis = () => { if (document.visibilityState === "visible") void loadTracks(); };
      document.addEventListener("visibilitychange", onVis);
      visOff = () => document.removeEventListener("visibilitychange", onVis);
    }
  }
  return () => {
    listeners.delete(l);
    if (!listeners.size) {
      helloOff?.();
      helloOff = null;
      visOff?.();
      visOff = null;
    }
  };
}

/** The tracks (null until read, or on an assistant without them) and whether the assistant has them. */
export function useTracks(): TracksState {
  return useSyncExternalStore(subscribe, () => state, () => state);
}

/** One track by its slug, once the tracks are read. */
export function useTrack(slug: string): Track | undefined {
  const { tracks } = useTracks();
  return slug ? tracks?.find((x) => x.slug === slug) : undefined;
}

const unsubscribed = () => () => {};

/** A track's title for the page's title; reads (and subscribes to) the tracks only for a slug. */
export function useTrackTitle(slug: string): string {
  const read = () => (slug ? state.tracks?.find((x) => x.slug === slug)?.title ?? "" : "");
  return useSyncExternalStore(slug ? subscribe : unsubscribed, read, read);
}

/** Forgets what was read (tests). */
export function resetTracksForTest() {
  helloOff?.();
  helloOff = null;
  visOff?.();
  visOff = null;
  loading = null;
  listeners.clear();
  state = { tracks: null, supported: null };
}
