// An extension's View (docs/extensions/extensions.md "Declarative UI" "UI slots" "Navigation"), loaded and carried out: an extension page in
// Settings › Extensions or from the sidebar, a section on a launcher's or a machine's page, a card on "Overview", an inbox item's body.
// How the items look is views/extensions/ViewBody.tsx. Here: a button's confirm asks first, submit sends the fields'
// values, a segmented acts when chosen, and what the press answers (a new view, a word, a link, a page pushed or popped)
// is carried out. UI version 3: a page can push another one (list → detail → edit), kept on a stack here (8 deep, the
// browser's back pops it on a full page); the view's search box and filter and a section's "Load more" go to the
// extension; extension_ui {invalidate} draws the view on screen again (one off screen when it comes back). A view with
// refresh_after is drawn again while it is on screen. Logic in lib/extension-ui.ts.
import { Suspense, useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { ApiError, credentialOwner, errorText, getToken, onCredentialChange } from "../../lib/api";
import { extIdOf } from "../../lib/extension-api";
import { openLink } from "../../lib/links";
import { openTargetOf } from "../../ui/AssistantCard";
import {
  actUI, fieldValues, httpOnlyUI, isUnsupported, numberProblem, outcome, pushPage, refreshDelay, renderUI, submitValues, uiPlaces, UI_VERSION,
  type UIAction, type UIContext, type UIFilter, type UIItem, type UIMore, type UIPage, type UIPlace, type UIPush, type UISearch, type UITabs, type UIView,
} from "../../lib/extension-ui";
import { takeOverlayEntry, withoutOverlayState } from "../../lib/back-close";
import { navigate } from "../../lib/router";
import { isResumed } from "../../lib/reconnect";
import { appSocketReady, onAppSocket, onHubEvent } from "../../lib/store";
import { wideView } from "../../lib/code-view";
import { INERT, ViewSections } from "../extensions/ViewBody";
import { Button, cx, Dialog, Loading, Spinner, toast } from "../../ui/atoms";
import { NATIVE, NativeTabs } from "../../ui/ExtensionNative";
import { keepLoaded, patchView } from "../../lib/code-review";
import { KeysHelp, ReviewBar, ReviewKeys, ReviewProvider, ReviewToolbar, reviewing } from "../extensions/DiffReview";
import { Icon } from "../../ui/icons";
import { getState, useStore } from "../../lib/store";
import { canTryUIStream } from "../../lib/extension-ui-stream-gate";
import { lazyPage } from "../../lib/lazy-page";
import { PageBoundary } from "../PageBoundary";
import { sendDraft } from "../../lib/drafts";
import { conversationPage } from "../../lib/conversation-page";
import { mainConversationMachine } from "../../lib/feed-cache";
import { chosenManager, defaultKnown, defaultManager, otherManager, useKnownDefault, useLinkManager } from "../../lib/manager-choice";
import { keepView, NATIVE_UI_VERSION, readKeptView } from "../../lib/extension-view-cache";
import type { UIRowControl } from "../../lib/extension-ui-v6";
import { ExtensionSetupNotice } from "./ExtensionSetupNotice";
import { currentLanguage, t } from "../../i18n";
import { HEALTH_INTAKE_UI, STRENGTH_EVENT } from "../../lib/health-intake-ui";
import { SUBSCRIPTIONS_UI } from "../../lib/subscriptions-ui";
import { ExtensionOwnerScope } from "../../lib/extension-owner";

const inputClass =
  "h-8 w-full rounded-control bg-field px-2.5 text-meta text-ink shadow-inset-field outline-none placeholder:text-ink-3 focus:shadow-[inset_0_0_0_1px_var(--accent)] disabled:opacity-60";

/**
 * Draws again while the view is on screen (docs/extensions/extensions.md "Refresh"): `delay` ms after it was last drawn, never while the
 * page is hidden; coming back after the delay draws at once.
 */
function useRefresh(delay: number, drawnAt: number, draw: () => void) {
  const drawRef = useRef(draw);
  drawRef.current = draw;
  useEffect(() => {
    if (!delay || !drawnAt) return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const arm = () => {
      clearTimeout(timer);
      if (document.visibilityState !== "visible") return;
      const left = drawnAt + delay - Date.now();
      if (left <= 0) drawRef.current();
      else timer = setTimeout(() => drawRef.current(), left);
    };
    arm();
    document.addEventListener("visibilitychange", arm);
    return () => {
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", arm);
    };
  }, [delay, drawnAt]);
}


/**
 * extension_ui {invalidate} for this extension (and this ui, or all of its): `draw` now when the page is visible, else
 * once it is visible again. At most once a second.
 */
function useInvalidate(extension: string, ui: string, draw: () => void, skipHello?: () => boolean) {
  const drawRef = useRef(draw);
  drawRef.current = draw;
  const skipHelloRef = useRef(skipHello);
  skipHelloRef.current = skipHello;
  useEffect(() => {
    let dirty = false;
    let last = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const fire = () => {
      dirty = false;
      last = Date.now();
      drawRef.current();
    };
    const now = () => {
      if (document.visibilityState !== "visible") {
        dirty = true;
        return;
      }
      const wait = last + 1000 - Date.now();
      clearTimeout(timer);
      if (wait <= 0) fire();
      else timer = setTimeout(fire, wait);
    };
    const off = onHubEvent("extension_ui", (msg) => {
      if (msg.invalidate !== true || extIdOf(msg) !== extension) return;
      const id = typeof msg.id === "string" ? msg.id : "";
      if (id && id !== ui) return;
      now();
    });
    // the subscription pages (#2041): a subscription saved, run or deleted anywhere draws them again
    const offSubscription = extension === SUBSCRIPTIONS_UI ? onHubEvent("subscription", () => now()) : () => {};
    // the health pages (#13176): a record logged or undone anywhere, or a goal saved, draws them again; so does any
    // change to strength training (#14713: its message carries nothing, the pages and the home card read again)
    const offIntake = extension === HEALTH_INTAKE_UI ? onHubEvent("health_intake", () => now()) : () => {};
    const offStrength = extension === HEALTH_INTAKE_UI ? onHubEvent(STRENGTH_EVENT, () => now()) : () => {};
    // a reconnect the hub could not resume: an invalidate missed meanwhile is not replayed (#4954)
    const offHello = onHubEvent("hello", (msg) => !isResumed(msg) && !skipHelloRef.current?.() && now());
    const shown = () => {
      if (dirty && document.visibilityState === "visible") now();
    };
    document.addEventListener("visibilitychange", shown);
    return () => {
      off();
      offSubscription();
      offIntake();
      offStrength();
      offHello();
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", shown);
    };
  }, [extension, ui]);
}

/** An object without one key (the same object when it does not have it). */
function without<T>(o: Record<string, T>, k: string): Record<string, T> {
  if (!(k in o)) return o;
  const { [k]: _gone, ...rest } = o;
  return rest;
}

/** The controls' values to keep showing when the view is drawn: those whose request is still on its way. */
function keepFlying(c: Record<string, string>, flying: Map<string, unknown>): Record<string, string> {
  const keys = Object.keys(c);
  if (keys.every((k) => flying.has(k))) return c;
  return Object.fromEntries(keys.filter((k) => flying.has(k)).map((k) => [k, c[k]]));
}

/** Where a pushed page opens when the view cannot hold a stack (a card, a section on another page): the full page. */
export function pageHref(extension: string, ui: string, state?: string): string {
  return `/extension/${encodeURIComponent(extension)}/${encodeURIComponent(ui)}${state ? `?state=${encodeURIComponent(state)}` : ""}`;
}

/**
 * Loads and draws one of an extension's views. `embedded`: inside another page (a launcher's), lighter, and gone when the
 * extension says the launcher is not its (404 unsupported) instead of showing an error. `compact`: a card ("Overview"), no
 * chrome of its own, small charts. `bare`: no heading of its own (the page around it has one). `onFailed`: the first
 * draw failed (no view yet), and the page around it shows something else instead of the error. `revision`: what it is
 * drawn for changed (an inbox item took new messages): drawn again, the fields typed into kept. `onOpenSource`: a press
 * opened the inbox item's own source page (persona://inbox/<context.item>/source). `pages`: where a pushed page goes —
 * on this view's own stack (`stack`, the default; with `history`, the browser's back pops it), or the full page of it
 * (`open`, the default for a card and a section inside another page). `state`: the first page's state (a full page opened by a push).
 */
const HELLO_NEGOTIATION_MS = 5000;
const NOOP = () => {};
// Pages that fell back from the native UI stream to HTTP in this session: reopen over HTTP immediately instead of
// repeating the stream-open round-trip (cleared on credential changes or extension lifecycle updates).
const httpFallbacks = new Set<string>();
let fallbackResetWired = false;
function wireFallbackReset() {
  if (fallbackResetWired) return;
  fallbackResetWired = true;
  if (typeof onCredentialChange === "function") onCredentialChange(() => httpFallbacks.clear());
  if (typeof onHubEvent === "function") onHubEvent("extensions", () => httpFallbacks.clear());
}
// The native stream page and its schema validator are a chunk of their own: most pages are drawn over HTTP and never need it.
const NativeExtensionUI = lazyPage(() => import("./NativeExtensionUI").then((m) => m.NativeExtensionUI));
export function ExtensionUI({ remember, ...props }: Parameters<typeof LegacyExtensionUI>[0] & {
  /** A full page, or a card on Overview (`compact`, #11091): drawn at once from its last view while the extension draws
   *  it again (lib/extension-view-cache.ts). */
  remember?: boolean;
}) {
  const supported = useStore((s) => canTryUIStream(s.features));
  const known = useStore((s) => s.features.length > 0);
  const defaultId = useKnownDefault();
  const group = useStore((s) => s.mesh?.find((h) => h.self)?.hub_id) ?? (typeof location !== "undefined" ? location.origin : "offline");
  const owner = typeof localStorage !== "undefined" ? credentialOwner() : "offline";
  const ready = useSyncExternalStore(onAppSocket, appSocketReady, appSocketReady);
  const negotiated = useRef(false);
  const [legacyNegotiation, setLegacyNegotiation] = useState(false);
  if (ready) negotiated.current = true;
  // System/embedded slots remain full fitted snapshots. Old hubs keep the exact existing component.
  // Another manager's extension draws over HTTP, which names its manager on each request (docs/assistant/managers.md "Clients").
  // The manager's own pages (an id starting with "-": the subscription pages #2041, Dashboards #15017) are drawn over HTTP
  // only, so they have nothing to wait for the hello for either.
  const managerPage = httpOnlyUI(props.extension);
  const baseHttp = legacyNegotiation || !supported || props.embedded || props.compact || otherManager(props.manager) || managerPage;
  // The page's last view, until it is drawn again: one drawn with a UI version it may be drawn with now (before the hello
  // either; over the socket also HTTP's, which a page the socket cannot draw falls back to) by the default's extension
  // (before the roster, the last one drawn).
  const page = remember ? JSON.stringify([props.extension, props.ui, props.state ?? "", currentLanguage()]) : "";
  const httpOnly = known && baseHttp;
  const kept = useMemo(() => {
    if (!page) return null;
    const [extension, ui, state, language] = JSON.parse(page) as string[];
    return readKeptView({ extension, ui, state, language }, httpOnly ? [UI_VERSION] : [NATIVE_UI_VERSION, UI_VERSION], defaultId || undefined);
  }, [page, httpOnly, defaultId]);
  // Pages whose last render used HTTP (cached with UI_VERSION) or that already fell back to HTTP in this session start
  // their HTTP render immediately instead of waiting for WebSocket hello, GET /managers, and a failing stream probe.
  const fallbackKey = JSON.stringify([owner, group, props.extension, props.ui]);
  const knownHttp = kept?.key.version === UI_VERSION || httpFallbacks.has(fallbackKey);
  const mountKey = JSON.stringify([owner, group, props.extension, props.ui, props.state ?? "", props.manager ?? ""]);
  const httpLocked = useRef("");
  const lockedHttp = httpLocked.current === mountKey;
  const http = lockedHttp || baseHttp || knownHttp;
  const waitingHello = !lockedHttp && !knownHttp && !managerPage && !props.embedded && !props.compact && typeof localStorage !== "undefined" && !!getToken() && !ready && !negotiated.current;
  useEffect(() => {
    if (!waitingHello || legacyNegotiation) return;
    const timer = setTimeout(() => setLegacyNegotiation(true), HELLO_NEGOTIATION_MS);
    return () => clearTimeout(timer);
  }, [waitingHello, legacyNegotiation]);
  const placeholder = kept ? <KeptView view={kept.view} native={kept.key.version === NATIVE_UI_VERSION} compact={props.compact} bare={props.bare} title={props.title} onWide={props.onWide} onTitle={props.onTitle} /> : undefined;
  const onView = useCallback((view: UIView, version: number, manager = defaultId || (defaultKnown() ? defaultManager() : "")) => {
    if (!page || !manager) return;
    const [extension, ui, state, language] = JSON.parse(page) as string[];
    keepView({ extension, ui, state, language, version, manager }, view);
  }, [page, defaultId]);
  const onLegacyFallback = useCallback(() => {
    wireFallbackReset();
    httpFallbacks.add(fallbackKey);
  }, [fallbackKey]);
  const keeps = page ? { placeholder, onView } : {};
  // Bound initial negotiation when HTTP works but WebSocket upgrades do not. Once HTTP
  // mounts, retain it even if a late hello arrives: never allocate a second source page.
  if (waitingHello && !legacyNegotiation) return placeholder ?? <Loading />;
  if (http) {
    httpLocked.current = mountKey;
    return <LegacyExtensionUI {...props} {...keeps} />;
  }
  return (
    <PageBoundary page={`extension:${props.extension}/${props.ui}`}>
      <Suspense fallback={placeholder ?? <Loading />}>
        <NativeExtensionUI key={JSON.stringify([owner, group, props.extension, props.ui, props.state ?? ""])} {...props} {...keeps} onLegacyFallback={onLegacyFallback} legacy={(page) => <LegacyExtensionUI {...page} />} />
      </Suspense>
    </PageBoundary>
  );
}

/** The page's last view while its extension draws it again (docs/web/web.md "Extension pages on reload"): as it was, as
 *  wide, with its View-level tabs, search and filter intact, but nothing in it can be pressed or focused, and a quiet
 *  "Updating" in the page's top margin, so nothing moves when the drawing replaces it. `native`: drawn over the socket,
 *  which titles the page itself. `compact`: a card's (#11091), its sections laid out as the drawing's own in the card's
 *  column (`contents`), "Updating" for screen readers only: the card's heading marks it (OverviewCard, by `data-kept-view`). */
function KeptView({ view, native, compact, bare, title, onWide, onTitle }: { view: UIView; native: boolean; compact?: boolean; bare?: boolean; title?: string; onWide?: (wide: boolean) => void; onTitle?: (title: string | undefined) => void }) {
  const wide = wideView(view);
  useEffect(() => onWide?.(wide), [wide, onWide]);
  useEffect(() => onTitle?.(view.title), [view.title, onTitle]);
  const values = useMemo(() => fieldValues(view), [view]);
  const first = view.sections[0]?.items[0];
  const titled = view.title || title;
  const heading = first?.type === "header" && first.title === titled ? undefined : titled;
  const filterValue = view.filter ? view.filter.value ?? view.filter.choices[0]?.value ?? "" : "";
  if (compact)
    return (
      <div className="contents" data-kept-view>
        <p role="status" className="sr-only">{t("settings-extensionview.updating")}</p>
        <div inert aria-busy="true" className="contents">
          {view.tabs && <Tabs tabs={view.tabs} value={view.tabs.value} busy="" onChoose={NOOP} />}
          {(view.search || view.filter) && (
            <SearchFilterBar search={view.search} filter={view.filter} query={view.search?.value ?? ""} filterValue={filterValue} compact busy="" onQuery={NOOP} onFilter={NOOP} />
          )}
          <ViewSections view={view} compact {...INERT} values={values} />
        </div>
      </div>
    );
  return (
    <div className="relative" data-kept-view>
      <p role="status" className="absolute right-1 -top-5 flex items-center gap-1.5 text-label text-ink-3">
        <Spinner size={10} />
        {t("settings-extensionview.updating")}
      </p>
      <div inert aria-busy="true" className={native ? NATIVE.page : "flex flex-col gap-6"}>
        {native ? (
          <>
            {view.title !== undefined && <h2 className="text-xl font-semibold text-ink">{view.title}</h2>}
            {view.tabs && <NativeTabs name={view.tabs.name} items={view.tabs.items} value={view.tabs.value} disabled onChoose={NOOP} />}
            {(view.search || view.filter) && (
              <div className="flex flex-wrap items-center gap-2">
                {view.search && (
                  <input
                    type="search"
                    aria-label={view.search.placeholder ?? t("settings-extensionview.search")}
                    placeholder={view.search.placeholder}
                    className={NATIVE.field}
                    value={view.search.value ?? ""}
                    disabled
                    onChange={NOOP}
                  />
                )}
                {view.filter && (
                  <select aria-label={view.filter.title ?? view.filter.name} className={NATIVE.field} value={filterValue} disabled onChange={NOOP}>
                    {view.filter.choices.map((it) => (
                      <option key={it.value} value={it.value}>
                        {it.title ?? it.value}
                      </option>
                    ))}
                  </select>
                )}
              </div>
            )}
          </>
        ) : (
          <>
            {!bare && heading && (
              <div className="flex items-center gap-2 px-1">
                <h2 className="min-w-0 flex-1 truncate text-card-title font-semibold text-ink">{heading}</h2>
              </div>
            )}
            {view.tabs && <Tabs tabs={view.tabs} value={view.tabs.value} busy="" onChoose={NOOP} />}
            {(view.search || view.filter) && (
              <SearchFilterBar search={view.search} filter={view.filter} query={view.search?.value ?? ""} filterValue={filterValue} busy="" onQuery={NOOP} onFilter={NOOP} />
            )}
          </>
        )}
        <ViewSections view={view} {...INERT} values={values} />
      </div>
    </div>
  );
}
export function LegacyExtensionUI({
  extension, ui, context, embedded, compact, bare, title, onUnsupported, onMissing, onFailed, revision, onOpenSource, pages, history: withHistory, state: firstState, onWide, onDepth, navigation, active = true, manager,
  placeholder, onView, onTitle, openPage, onClose, setupOwner,
}: {
  extension: string; ui: string; context?: UIContext; embedded?: boolean; compact?: boolean; bare?: boolean; title?: string;
  /** Whose extension it is (another manager's inbox item or computer, docs/assistant/managers.md "Clients"); none: the default's. */
  manager?: string;
  /** A direct page can recover from its first 404 by opening the owning installation's settings. */
  setupOwner?: string; // empty until the direct page's default manager is known
  onUnsupported?: () => void; onMissing?: () => void; onFailed?: (e: unknown) => void; revision?: string; onOpenSource?: () => void;
  pages?: "stack" | "open"; history?: boolean; state?: string;
  /** A native/legacy mixed stack has one history owner; HTTP entries delegate navigation. */
  navigation?: { push: (page: UIPush) => void; pop: () => void };
  active?: boolean;
  /** The view drawn needs a wide page (split, columns, code, diffs, or canvas components such as graph, scene, map) or not. */
  onWide?: (wide: boolean) => void;
  /** How many pages deep its stack is, whenever that changes (Inbox Sources keeps its List / Relationships switch on the first). */
  onDepth?: (depth: number) => void;
  /** Drawn instead of the loading state until the first view is (the page's last view, ExtensionUI's `remember`). */
  placeholder?: ReactNode;
  /** The first page drew a view not typed into, asked with UI `version` (by `manager`'s extension; none: the default's). */
  onView?: (view: UIView, version: number, manager?: string) => void;
  /** The first page's title as its view (or the kept one) says it, each time it is drawn: the page around it titles its
   *  header with it (a Dashboards page, whose place is not listed unless pinned, #15017). */
  onTitle?: (title: string | undefined) => void;
  /** A push the page around opens itself, as a page of its own; true when it did (Dashboards' index opening a page, #15017). */
  openPage?: (page: UIPush) => boolean;
  /** The first page asked to be taken off (a pop with nothing under it): the page around it closes. Without it, ignored. */
  onClose?: () => void;
}) {
  type StackEntry = UIPage & { id: number; initialState?: string };
  const nextEntryId = useRef(1);
  const [stack, setStack] = useState<StackEntry[]>(() => [
    { id: 1, ui, ...(firstState ? { state: firstState, initialState: firstState } : {}) },
  ]);
  const stackRef = useRef(stack);
  stackRef.current = stack;
  const poppingRef = useRef<number | null>(null);
  const linkManager = useLinkManager();
  const top = stack[stack.length - 1];
  const depth = stack.length;
  const pageKey = `${top.id}\n${depth}\n${top.ui}`;
  const pageKeyRef = useRef(pageKey);
  pageKeyRef.current = pageKey;
  const ctxKey = `${context?.launcher ?? ""}\n${context?.machine ?? ""}\n${context?.item ?? ""}\n${context?.size ?? ""}`;
  const targetKey = `${extension}\0${pageKey}\0${ctxKey}\0${manager ?? ""}`;
  const targetKeyRef = useRef(targetKey);
  targetKeyRef.current = targetKey;
  const [shownView, setShownView] = useState<{ key: string; view: UIView } | null>(null);
  const view = shownView?.key === targetKey ? shownView.view : null;
  const setView = useCallback((next: UIView | null | ((before: UIView | null) => UIView | null)) => {
    const key = targetKeyRef.current;
    setShownView((cur) => {
      const prev = cur?.key === key ? cur.view : null;
      const resolved = typeof next === "function" ? next(prev) : next;
      return resolved ? { key, view: resolved } : null;
    });
  }, []);
  const [failure, setFailure] = useState<{ message: string; checkSetup: boolean; attempt: number }>();
  const error = failure?.message;
  const [unsupported, setUnsupported] = useState(false);
  const [loading, setLoading] = useState(false);
  const [values, setValues] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState("");
  const busyReqRef = useRef(0);
  const loadReqRef = useRef(0);
  // `done`: told whether the action asked about was done (a code component's act waits for it)
  const [confirming, setConfirming] = useState<{ action: UIAction; destructive: boolean; values?: Record<string, string>; control?: { c: UIRowControl; value: string }; done?: (ok: boolean) => void } | null>(null);
  const confirmingRef = useRef(confirming);
  confirmingRef.current = confirming;
  // UI version 6: a control's value the user chose, by its name, shown until the extension's answer is drawn; the ones on
  // their way more than a second (a small spinner); each control's request on its way and the latest change made meanwhile
  const [controls, setControls] = useState<Record<string, string>>({});
  const [slow, setSlow] = useState<Record<string, boolean>>({});
  const flight = useRef(new Map<string, { timer: ReturnType<typeof setTimeout>; queued?: { c: UIRowControl; value: string } }>());
  const [drawnAt, setDrawnAt] = useState(0);
  const [query, setQuery] = useState(""); // the search box as typed
  const stateRef = useRef<string | undefined>(top.state);
  const drawnRef = useRef(false); // a view has been drawn
  const asked = useRef(0);
  const visitRef = useRef(0);
  const inFlightRef = useRef(false);
  const mountedRef = useRef(true);
  const isCurrentPage = (
    expectedKey: string,
    expectedId: number | undefined,
    expectedVisit: number,
    expectedTarget: string,
  ) =>
    mountedRef.current
    && visitRef.current === expectedVisit
    && targetKeyRef.current === expectedTarget
    && pageKeyRef.current === expectedKey
    && stackRef.current.at(-1)?.id === expectedId;
  const mode = pages ?? (compact || embedded ? "open" : "stack");
  useEffect(() => {
    mountedRef.current = true;
    const live = flight.current;
    return () => {
      mountedRef.current = false;
      visitRef.current += 1;
      asked.current += 1;
      loadReqRef.current += 1;
      inFlightRef.current = false;
      live.forEach((f) => clearTimeout(f.timer));
      live.clear();
      if (confirmingRef.current) {
        const pending = confirmingRef.current;
        confirmingRef.current = null;
        pending.done?.(false);
      }
    };
  }, []);
  useEffect(() => onDepth?.(depth), [depth, onDepth]);

  // the first page follows the props (a different extension or page opened in the same place)
  const propKey = `${extension}\0${ui}\0${firstState ?? ""}`;
  const lastPropKey = useRef(propKey);
  useEffect(() => {
    if (lastPropKey.current === propKey) return;
    lastPropKey.current = propKey;
    visitRef.current += 1;
    asked.current += 1;
    poppingRef.current = null;
    const entry: StackEntry = { id: ++nextEntryId.current, ui, ...(firstState ? { state: firstState, initialState: firstState } : {}) };
    const next = [entry];
    stackRef.current = next;
    pageKeyRef.current = `${entry.id}\n1\n${entry.ui}`;
    setStack(next);
  }, [propKey, ui, firstState]);

  // keep: drawn again for the same thing, the fields typed into stay as typed
  const show = (
    v: UIView,
    keep = false,
    expectedKey = pageKeyRef.current,
    expectedId = stackRef.current.at(-1)?.id,
    expectedVisit = visitRef.current,
    expectedTarget = targetKeyRef.current,
  ) => {
    const current = stackRef.current;
    const lastIdx = current.length - 1;
    const curTop = current[lastIdx];
    if (!curTop || !isCurrentPage(expectedKey, expectedId, expectedVisit, expectedTarget)) return;
    // drawn again for the same page: the diffs loaded since stay (UI version 5)
    setView((before) => (keep ? keepLoaded(before, v) : v));
    const start = fieldValues(v);
    setValues((old) => (keep ? Object.fromEntries(Object.entries(start).map(([k, x]) => [k, old[k] ?? x])) : start));
    const nextState = v.state ?? curTop.initialState;
    stateRef.current = nextState;
    const nextTitle = v.title || curTop.title;
    if (curTop.state !== nextState || curTop.title !== nextTitle) {
      const updated: StackEntry = { id: curTop.id, ui: curTop.ui };
      if (curTop.initialState !== undefined) updated.initialState = curTop.initialState;
      if (nextState !== undefined) updated.state = nextState;
      if (nextTitle !== undefined) updated.title = nextTitle;
      const nextStack = [...current.slice(0, lastIdx), updated];
      stackRef.current = nextStack;
      setStack(nextStack);
    }
    drawnRef.current = true;
    // a control's choice stays only while its request is on its way: once answered, what the extension draws is shown
    setControls((c) => keepFlying(c, flight.current));
    if (!keep) setQuery(v.search?.value ?? "");
    setFailure(undefined);
    setUnsupported(false); // a view drawn is something here
    setDrawnAt(Date.now());
  };
  // Every request that draws the whole view (a draw, a press, a search) takes the next number when it leaves: an answer
  // whose number is not the latest any more is older than what was asked since (a press, another page, the next search)
  // and is not drawn, nor its error shown (#4931).
  const load = useCallback((keep?: boolean) => {
    const mine = ++asked.current;
    const loadMine = ++loadReqRef.current;
    const expectedKey = pageKey;
    const expectedId = top.id;
    const expectedVisit = visitRef.current;
    const expectedTarget = targetKey;
    inFlightRef.current = true;
    setLoading(true);
    renderUI(extension, top.ui, context, stateRef.current, manager)
      .then((v) => mine === asked.current && isCurrentPage(expectedKey, expectedId, expectedVisit, expectedTarget)
        && show(v, keep === true, expectedKey, expectedId, expectedVisit, expectedTarget))
      .catch((e) => {
        if (mine !== asked.current || !isCurrentPage(expectedKey, expectedId, expectedVisit, expectedTarget)) return;
        if (isUnsupported(e)) (setUnsupported(true), onUnsupported?.());
        else if (onMissing && e instanceof ApiError && e.status === 404 && depth === 1) onMissing();
        else if (onFailed && !drawnRef.current) onFailed(e);
        else {
          setFailure({ message: errorText(e), attempt: mine, checkSetup: setupOwner !== undefined && !drawnRef.current
            && depth === 1 && !httpOnlyUI(extension) && e instanceof ApiError && e.status === 404 });
          setDrawnAt(Date.now()); // a failed refresh waits a whole interval too
        }
      })
      .finally(() => {
        if (loadMine === loadReqRef.current) {
          inFlightRef.current = false;
          if (mountedRef.current) setLoading(false);
        }
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [extension, pageKey, ctxKey, manager]);
  // a new page (pushed, popped, another one): its own state, a fresh view
  useEffect(() => {
    visitRef.current += 1;
    stateRef.current = top.state;
    drawnRef.current = false;
    flight.current.forEach((f) => clearTimeout(f.timer));
    flight.current.clear();
    if (confirmingRef.current) {
      const pending = confirmingRef.current;
      confirmingRef.current = null;
      pending.done?.(false);
      setConfirming(null);
    }
    busyReqRef.current = 0;
    setBusy("");
    setControls({});
    setSlow({});
    setView(null);
    setFailure(undefined);
    setUnsupported(false); // "nothing here" was the page before's answer
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [load]);
  const wide = wideView(view);
  useEffect(() => {
    if (view) onWide?.(wide);
  }, [wide, view, onWide]);
  const viewTitle = view && depth === 1 ? view.title : null;
  useEffect(() => {
    if (viewTitle !== null) onTitle?.(viewTitle);
  }, [viewTitle, onTitle]);
  // the first page as the extension drew it, unless its fields hold what was typed and not sent (a redraw keeps them)
  const valuesNow = useRef(values);
  valuesNow.current = values;
  useEffect(() => {
    const start = fieldValues(view);
    if (view && depth === 1 && Object.entries(start).every(([k, v]) => (valuesNow.current[k] ?? v) === v)) onView?.(view, UI_VERSION, manager);
  }, [view, depth, onView, manager]);
  useRefresh(active && !busy ? refreshDelay(view) : 0, drawnAt, () => {
    if (!busy && !inFlightRef.current) load(true);
  });
  useInvalidate(
    extension,
    top.ui,
    () => {
      if (active && !busy) load(true);
    },
    // Skip the initial non-resumed hello while the first draw started before hello is already in flight.
    () => !drawnRef.current && inFlightRef.current,
  );
  const drawnFor = useRef(revision);
  useEffect(() => {
    if (drawnFor.current === revision) return;
    drawnFor.current = revision;
    if (drawnRef.current) load(true);
  }, [revision, load]);

  // the browser's back pops a page pushed here (a full page only: a section or a card never holds one)
  const historied = !navigation && (withHistory ?? (!embedded && !compact && mode === "stack"));
  useEffect(() => {
    if (!historied) return;
    const onPop = (e: PopStateEvent) => {
      poppingRef.current = null;
      const at = typeof (e.state as { extPage?: unknown } | null)?.extPage === "number" ? (e.state as { extPage: number }).extPage : 1;
      const current = stackRef.current;
      if (at >= current.length) return;
      const next = current.slice(0, Math.max(1, at));
      const nextTop = next[next.length - 1];
      visitRef.current += 1;
      asked.current += 1;
      stackRef.current = next;
      pageKeyRef.current = `${nextTop.id}\n${next.length}\n${nextTop.ui}`;
      setStack(next);
    };
    addEventListener("popstate", onPop);
    return () => {
      poppingRef.current = null;
      removeEventListener("popstate", onPop);
    };
  }, [historied]);

  const push = (p: UIPush) => {
    if (openPage?.(p)) return;
    if (navigation) { navigation.push(p); return; }
    const current = stackRef.current;
    const curTop = current[current.length - 1];
    if (mode === "open") {
      navigate(pageHref(extension, p.ui || curTop.ui, p.state));
      return;
    }
    const pushed = pushPage(current, p, curTop.ui);
    const next: StackEntry[] = pushed.map((entry, idx) =>
      idx === pushed.length - 1
        ? { ...entry, id: ++nextEntryId.current, ...(entry.state !== undefined ? { initialState: entry.state } : {}) }
        : current[idx] ?? { ...entry, id: ++nextEntryId.current, ...(entry.state !== undefined ? { initialState: entry.state } : {}) },
    );
    const nextTop = next[next.length - 1];
    visitRef.current += 1;
    asked.current += 1;
    poppingRef.current = null;
    if (historied && next.length > current.length) {
      // Pushing onto this stack from inside an open overlay (e.g. a Full screen map/graph stop) replaces the
      // overlay's entry without its markers so closing the overlay does not immediately history.back() off the new page.
      const nextState = { ...(withoutOverlayState(history.state) ?? {}), extPage: next.length };
      if (takeOverlayEntry()) history.replaceState(nextState, "", location.href);
      else history.pushState(nextState, "", location.href);
    }
    stackRef.current = next;
    pageKeyRef.current = `${nextTop.id}\n${next.length}\n${nextTop.ui}`;
    setStack(next);
  };
  const pop = () => {
    if (navigation) { navigation.pop(); return; }
    const current = stackRef.current;
    if (current.length <= 1) {
      onClose?.();
      return;
    }
    if (historied) {
      if (poppingRef.current === current.length) return;
      poppingRef.current = current.length;
      history.back(); // the popstate above takes the page off
    } else {
      const next = current.slice(0, -1);
      const nextTop = next[next.length - 1];
      visitRef.current += 1;
      asked.current += 1;
      stackRef.current = next;
      pageKeyRef.current = `${nextTop.id}\n${next.length}\n${nextTop.ui}`;
      setStack(next);
    }
  };

  // resolves to whether the action was done: false when it did not go (a field not a number) or failed
  const run = async (action: UIAction, extra?: Record<string, string>): Promise<boolean> => {
    // A full page may open an existing Persona session or to-do (#14617): route locally, like an assistant card.
    // The hub only keeps session and task targets on extension pages; other forms still go through ui.action.
    if (action.open) {
      openTargetOf(action.open, linkManager);
      return true;
    }
    if (action.push) {
      push(action.push);
      return true;
    }
    // the fields' values (when it submits), what the component sends, then what the action carries (UI version 4)
    const merged = { ...(action.submit ? submitValues(view, values) : {}), ...extra, ...action.values };
    const sent = action.submit || extra || action.values ? merged : undefined;
    if (sent && view) {
      const bad = view.sections.flatMap((s) => s.items).find((it) => it.type === "field" && it.kind === "number" && it.name && numberProblem(sent[it.name] ?? ""));
      if (bad) {
        toast(t("settings-extensionview.not_a_number", { field: bad.label || bad.name || "" }), "error");
        return false;
      }
    }
    const mine = ++asked.current;
    if (inFlightRef.current) {
      loadReqRef.current += 1;
      inFlightRef.current = false;
      setLoading(false);
    }
    busyReqRef.current = mine;
    setBusy(action.id);
    const expectedKey = pageKey;
    const expectedId = top.id;
    const expectedVisit = visitRef.current;
    const expectedTarget = targetKey;
    try {
      const r = outcome(await actUI(extension, top.ui, action.id, { context, state: stateRef.current, values: sent, manager }), context?.item, extension);
      // Do not apply view updates or trigger navigation/open/pop/reload side effects after unmount or page switch.
      const sameEntry = isCurrentPage(expectedKey, expectedId, expectedVisit, expectedTarget);
      if (!sameEntry) return false;
      // what it says to do is still done; the view it answers only while nothing was asked since
      const latest = mine === asked.current;
      if (r.view && latest) show(r.view, !!(extra && "q" in extra), expectedKey, expectedId, expectedVisit, expectedTarget); // a search keeps what is typed
      if (r.items && latest) setView((v) => (v ? patchView(v, r.items) : v));
      if (r.toast) toast(r.toast, "ok");
      if (r.url) openLink(r.url, undefined, linkManager);
      if (r.track) navigate(r.track);
      if (r.source) onOpenSource?.();
      // an extension's own settings (persona://settings/extensions/<id>) open in the app
      if (r.settings) navigate(r.settings);
      // the subscription pages' Set Up with … (persona://ask): the text to the assistant's main conversation, which opens
      if (r.ask) askAssistant(r.ask);
      // an extension's own quick prompt (persona://compose?prompt=<id>&text=<text>): into the main composer, not sent
      if (r.compose) composeAssistant(r.compose);
      if (r.push) push(r.push);
      if (r.pop) pop();
      if (r.reload && !r.push && !r.pop && isCurrentPage(expectedKey, expectedId, expectedVisit, expectedTarget)) load();
      return true;
    } catch (e) {
      if (!isCurrentPage(expectedKey, expectedId, expectedVisit, expectedTarget)) return false;
      // an older one's "unsupported" does not take away a view drawn since; a press that failed still says so
      if (isUnsupported(e)) {
        if (mine === asked.current) (setUnsupported(true), onUnsupported?.());
      } else toast(t("settings-extensionview.action_failed", { error: errorText(e) }), "error");
      return false;
    } finally {
      if (mountedRef.current && busyReqRef.current === mine) setBusy("");
    }
  };
  // an action that does not hold the page (UI version 5: a diff's lines, a viewed mark, a diff loaded as it comes near):
  // its components go in place, a new view is shown as a redraw; it says what was answered, null when it failed
  const fetchQuiet = useCallback(
    async (action: UIAction, extra?: Record<string, string>) => {
      // it does not take a number (it does not make a draw on its way older), but its view is not drawn over a newer one
      const at = asked.current;
      const expectedKey = pageKey;
      const expectedId = top.id;
      const expectedVisit = visitRef.current;
      const expectedTarget = targetKey;
      try {
        const r = await actUI(extension, top.ui, action.id, { context, state: stateRef.current, values: { ...extra, ...action.values }, manager });
        if (!isCurrentPage(expectedKey, expectedId, expectedVisit, expectedTarget)) return null;
        if (r.items?.length) setView((v) => (v ? patchView(v, r.items) : v));
        if (r.view && at === asked.current) show(r.view, true, expectedKey, expectedId, expectedVisit, expectedTarget);
        if (r.toast) toast(r.toast, "ok");
        return r;
      } catch (e) {
        if (isCurrentPage(expectedKey, expectedId, expectedVisit, expectedTarget)) toast(t("settings-extensionview.action_failed", { error: errorText(e) }), "error");
        return null;
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [extension, pageKey, ctxKey, manager],
  );
  // UI version 6: a control changed (docs/extensions/extensions.md "UI version 6" "Controls"). The new value shows at once; one
  // request per control is on its way at a time, a change made meanwhile goes after it (the latest only). The answer's
  // view or items say what the control is now; an empty answer draws the page again; a failure puts the extension's
  // last value back and says why. A redraw meanwhile (invalidate, refresh) keeps the user's value until its own answer.
  const sendControl = async (c: UIRowControl, value: string) => {
    const entry: { timer: ReturnType<typeof setTimeout>; queued?: { c: UIRowControl; value: string } } = {
      timer: setTimeout(() => {
        if (mountedRef.current && flight.current.get(c.name) === entry) setSlow((x) => ({ ...x, [c.name]: true }));
      }, 1000),
    };
    flight.current.set(c.name, entry);
    const at = asked.current;
    const expectedKey = pageKey;
    const expectedId = top.id;
    const expectedVisit = visitRef.current;
    const expectedTarget = targetKey;
    let r: ReturnType<typeof outcome> | null = null;
    let failed: unknown = null;
    try {
      r = outcome(await actUI(extension, top.ui, c.action.id, { context, state: stateRef.current, values: { ...c.action.values, [c.name]: value }, manager }), context?.item, extension);
    } catch (e) {
      failed = e;
    }
    clearTimeout(entry.timer);
    if (flight.current.get(c.name) === entry) flight.current.delete(c.name);
    if (!isCurrentPage(expectedKey, expectedId, expectedVisit, expectedTarget)) return;
    setSlow((x) => without(x, c.name));
    if (failed !== null) toast(t("settings-extensionview.action_failed", { error: errorText(failed) }), "error");
    // changed again while this one was on its way: the latest goes now, and stays shown (unless leaving this page)
    if (entry.queued && !r?.push && !r?.pop) void sendControl(entry.queued.c, entry.queued.value);
    else if (!r) setControls((x) => without(x, c.name)); // failed: the extension's last value again
    if (!r) return;
    const applied = (!!r.view && at === asked.current) || !!r.items;
    if (r.view && at === asked.current) show(r.view, true, expectedKey, expectedId, expectedVisit, expectedTarget);
    if (r.items) setView((v) => (v ? patchView(v, r.items) : v));
    if (applied && !entry.queued) setControls((x) => without(x, c.name));
    if (r.toast) toast(r.toast, "ok");
    if (r.url) openLink(r.url, undefined, linkManager);
    if (r.push) push(r.push);
    if (r.pop) pop();
    if (!r.push && !r.pop && isCurrentPage(expectedKey, expectedId, expectedVisit, expectedTarget) && (r.reload || (r.view && at !== asked.current && !inFlightRef.current))) load(true);
  };
  const applyControl = (c: UIRowControl, value: string) => {
    setControls((x) => ({ ...x, [c.name]: value }));
    const on = flight.current.get(c.name);
    if (on) on.queued = { c, value };
    else void sendControl(c, value);
  };
  const changeControl = (c: UIRowControl, value: string) => {
    if (c.disabled) return;
    // asked first when the action says so; cancelled, nothing changed
    if (c.action.confirm?.trim()) setConfirming({ action: c.action, destructive: false, control: { c, value } });
    else applyControl(c, value);
  };
  // a hub picture that does not load may have outlived its token (15 minutes): the page is drawn again, which signs
  // anew; at most once a minute, so a picture that is gone for good does not keep drawing the page
  const imageRedraw = useRef(0);
  const imageFailed = () => {
    if (Date.now() - imageRedraw.current < 60_000 || busy) return;
    imageRedraw.current = Date.now();
    load(true);
  };
  const [keysOpen, setKeysOpen] = useState(false);
  const isReview = !compact && !embedded && reviewing(view);
  const press = (action: UIAction, destructive = false) => {
    if (busy) return;
    if (action.confirm?.trim()) setConfirming({ action, destructive });
    else void run(action);
  };
  // a code hosting component's action (UI version 4): with its own values (a path, a line, a comment), confirmed first
  // when it says so. It resolves to whether it was done (false: busy, cancelled at the confirmation or failed), so a
  // box clears what was typed only once the extension has taken it.
  const act = (action: UIAction, extra?: Record<string, string>): Promise<boolean> => {
    if (busy) return Promise.resolve(false);
    if (action.confirm?.trim()) return new Promise((done) => setConfirming({ action, destructive: false, values: extra, done }));
    return run(action, extra);
  };
  const cancelConfirm = () => {
    const pending = confirmingRef.current;
    if (!pending) return;
    confirmingRef.current = null;
    pending.done?.(false);
    setConfirming(null);
  };
  // a segmented acts as soon as another choice is made: its own value goes along (the rest only when it submits)
  const choose = (item: UIItem, value: string) => {
    if (busy || !item.name || !item.action) return;
    setValues((x) => ({ ...x, [item.name!]: value }));
    void run(item.action, { [item.name]: value });
  };
  // the search box, the filter and "Load more" send what the list is narrowed to: the text typed and the filter's choice
  const listValues = (): Record<string, string> => ({
    ...(view?.search ? { q: query } : {}),
    ...(view?.tabs ? { [view.tabs.name]: values[view.tabs.name] ?? view.tabs.value } : {}),
    ...(view?.filter ? { [view.filter.name]: values[view.filter.name] ?? view.filter.value ?? view.filter.choices[0]?.value ?? "" } : {}),
  });
  const more = useCallback((m: UIMore) => {
    if (!busy) void run(m.action, listValues());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [busy, view, query, values]);
  // typed into the search box: asked 0.3 s after the last key
  const searched = useRef(query);
  useEffect(() => {
    const s = view?.search;
    if (!s || query === searched.current) return;
    const timer = setTimeout(() => {
      searched.current = query;
      void run(s.action, listValues());
    }, 300);
    return () => clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query]);
  useEffect(() => {
    searched.current = view?.search?.value ?? "";
  }, [pageKey, view?.search?.value]);

  const below = depth > 1 ? stack[depth - 2].title || (depth === 2 ? title : undefined) || t("settings-extensionview.back") : "";
  const back = depth > 1 && (
    <div className="-mb-3 flex items-center px-1">
      <Button tone="quiet" icon="chevronLeft" onClick={pop} className="-ml-2 max-w-full">
        <span className="truncate">{below}</span>
      </Button>
    </div>
  );
  if (unsupported) {
    if (embedded || compact) return null;
    const nothing = (
      <p className="rounded-card bg-surface px-4 py-6 text-center text-meta text-ink-3 shadow-card">{t("settings-extensionview.nothing_here")}</p>
    );
    // a pushed page that has nothing still goes back to the page below
    return back ? <div className="flex flex-col gap-6">{back}{nothing}</div> : nothing;
  }

  // a page that opens with a header (UI version 4) of the same title has its big title there: not said twice
  const first = view?.sections[0]?.items[0];
  const titled = view?.title || top.title || (depth === 1 ? title : undefined);
  const heading = first?.type === "header" && first.title === titled ? undefined : titled;
  const light = embedded || compact;
  return (
    <div className={cx("flex flex-col", compact ? "gap-1" : embedded ? "gap-3" : "gap-6")}>
      {back}
      {!light && (!bare || depth > 1) && heading && (
        <div className="flex items-center gap-2 px-1">
          <h2 className="min-w-0 flex-1 truncate text-card-title font-semibold text-ink">{heading}</h2>
          {loading && view && <Spinner />}
        </div>
      )}
      {view?.tabs && (
        <Tabs
          tabs={view.tabs}
          value={values[view.tabs.name] ?? view.tabs.value}
          busy={busy}
          onChoose={(v) => {
            const tb = view.tabs!;
            if (busy || v === (values[tb.name] ?? tb.value)) return;
            setValues((x) => ({ ...x, [tb.name]: v }));
            void run(tb.action, { ...listValues(), [tb.name]: v });
          }}
        />
      )}
      {view && (view.search || view.filter) && (
        <SearchFilterBar
          search={view.search}
          filter={view.filter}
          query={query}
          filterValue={view.filter ? values[view.filter.name] ?? view.filter.value ?? view.filter.choices[0]?.value ?? "" : ""}
          compact={compact}
          busy={busy}
          onQuery={setQuery}
          onFilter={(next) => {
            const f = view.filter!;
            setValues((x) => ({ ...x, [f.name]: next }));
            void run(f.action, { ...listValues(), [f.name]: next });
          }}
        />
      )}
      {!view && !error && (placeholder ?? (
        <div className={cx(compact ? "px-4 py-3" : embedded ? "py-2" : "rounded-card bg-surface px-4 py-3 shadow-card")}>
          <Loading />
        </div>
      ))}
      {error && compact && (
        <div className="flex items-center gap-2 px-4 py-2.5">
          <p className="min-w-0 flex-1 truncate text-label text-red" title={error}>
            {view ? t("settings-extensionview.update_failed_inline", { error }) : t("settings-extensionview.draw_failed_inline", { error })}
          </p>
          <Button tone="quiet" icon="retry" busy={loading} onClick={() => load()}>
            {t("settings-extensionview.retry")}
          </Button>
        </div>
      )}
      {error && !compact && (
        <ExtensionSetupNotice key={JSON.stringify([failure.attempt, setupOwner])} check={failure.checkSetup} extension={extension} manager={setupOwner || undefined}>
          <div className={cx("flex flex-col items-center gap-2 px-6 text-center", embedded ? "rounded-control bg-inset py-4 shadow-hairline" : "rounded-card bg-surface py-8 shadow-card")}>
            <span className="flex size-9 items-center justify-center rounded-[10px] bg-red-tint text-red">
              <Icon name="extension" size={16} />
            </span>
            <p className="text-meta font-medium text-ink">{view ? t("settings-extensionview.update_failed") : t("settings-extensionview.draw_failed")}</p>
            <p className="max-w-[420px] text-label leading-relaxed break-words text-ink-3 select-text">{error}</p>
            <Button tone="secondary" icon="retry" busy={loading} onClick={() => load()}>
              {t("settings-extensionview.retry")}
            </Button>
          </div>
        </ExtensionSetupNotice>
      )}
      {view && (
        <ReviewProvider view={isReview ? view : null} page={pageKey} fetch={fetchQuiet}>
          {isReview && <ReviewToolbar view={view} onHelp={() => setKeysOpen(true)} />}
          <ViewSections
            view={view}
            embedded={embedded}
            compact={compact}
            values={values}
            setValue={(k, v) => setValues((x) => ({ ...x, [k]: v }))}
            busy={busy}
            onPress={press}
            onChoose={choose}
            onMore={more}
            onAct={act}
            controls={controls}
            slow={slow}
            onControl={changeControl}
            onImageFailed={imageFailed}
          />
          {isReview && (
            <>
              <ReviewBar view={view} act={act} busy={busy} />
              <ReviewKeys onHelp={() => setKeysOpen(true)} />
              <KeysHelp open={keysOpen} onClose={() => setKeysOpen(false)} />
            </>
          )}
        </ReviewProvider>
      )}
      {confirming && (
        <Dialog
          open
          onClose={cancelConfirm}
          width={400}
          title={t("settings-extensionview.confirm_title")}
          footer={
            <>
              <Button tone="quiet" onClick={cancelConfirm}>
                {t("common.cancel")}
              </Button>
              <Button
                tone={confirming.destructive ? "danger" : "primary"}
                onClick={() => {
                  const pending = confirmingRef.current;
                  if (!pending) return;
                  confirmingRef.current = null;
                  setConfirming(null);
                  const { action: a, values: v, control, done } = pending;
                  if (control) applyControl(control.c, control.value);
                  else void run(a, v).then((ok) => done?.(ok));
                }}
              >
                {t("settings-extensionview.ok")}
              </Button>
            </>
          }
        >
          <p className="text-meta leading-relaxed whitespace-pre-line break-words text-ink-2">{confirming.action.confirm}</p>
        </Dialog>
      )}
    </div>
  );
}

/** A view's search box and filter bar (UI version 3): shared between live and cached views so refresh swaps only data. */
function SearchFilterBar({
  search, filter, query, filterValue, compact, busy, onQuery, onFilter,
}: {
  search?: UISearch; filter?: UIFilter; query: string; filterValue: string; compact?: boolean; busy: string;
  onQuery: (v: string) => void; onFilter: (v: string) => void;
}) {
  return (
    <div className={cx("flex items-center gap-2", compact ? "px-4" : "px-0")}>
      {search && (
        <label className="relative min-w-0 flex-1">
          <Icon name="search" size={13} className="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-ink-3" />
          <input
            type="search"
            value={query}
            onChange={(e) => onQuery(e.target.value)}
            placeholder={search.placeholder || t("settings-extensionview.search")}
            aria-label={search.placeholder || t("settings-extensionview.search")}
            spellCheck={false}
            autoComplete="off"
            className={cx(inputClass, "pl-8")}
          />
        </label>
      )}
      {filter && (
        <select
          aria-label={filter.title || filter.name}
          value={filterValue}
          disabled={!!busy}
          onChange={(e) => onFilter(e.target.value)}
          className={cx(inputClass, "w-auto max-w-[200px] shrink-0")}
        >
          {filter.choices.map((c) => (
            <option key={c.value} value={c.value}>
              {c.title || c.value}
            </option>
          ))}
        </select>
      )}
      {busy && <Spinner />}
    </div>
  );
}

/** A view's tabs (UI version 4): a row of them under the title, the chosen one underlined; scrolls sideways when narrow. */
function Tabs({ tabs, value, busy, onChoose }: { tabs: UITabs; value: string; busy: string; onChoose: (v: string) => void }) {
  return (
    <div role="tablist" aria-label={tabs.name} className="-mb-2 flex items-end gap-1 overflow-x-auto border-b border-line px-1">
      {tabs.items.map((it) => {
        const on = it.value === value;
        return (
          <button
            key={it.value}
            type="button"
            role="tab"
            aria-selected={on}
            disabled={!!busy && !on}
            onClick={() => onChoose(it.value)}
            className={cx(
              "relative flex h-9 shrink-0 items-center gap-1.5 px-3 text-meta whitespace-nowrap transition-colors",
              on ? "font-semibold text-ink" : "text-ink-3 enabled:hover:text-ink",
            )}
          >
            {it.title || it.value}
            {it.badge && <span className="rounded-full bg-field px-1.5 text-[length:var(--extension-meta-size)] leading-[18px] font-medium text-ink-2 tabular-nums">{it.badge}</span>}
            {busy === tabs.action.id && on && <Spinner size={10} />}
            {on && <span className="absolute inset-x-2 -bottom-px h-[2px] rounded-full bg-accent" />}
          </button>
        );
      })}
    </div>
  );
}

/**
 * The extensions' sections on a launcher's page (placement launcher_section): one per extension whose gateway matches. A
 * extension that says this launcher is not its gateway (404 unsupported) leaves no trace; nothing listed, nothing drawn.
 */
export function LauncherExtensionSections({ launcher, machine }: { launcher: string; machine?: string }) {
  const [places, setPlaces] = useState<UIPlace[]>([]);
  useEffect(() => {
    let live = true;
    setPlaces([]);
    uiPlaces("launcher_section", launcher, machine).then(
      (l) => live && setPlaces(l),
      () => {}, // an older hub, or no extensions: no section
    );
    return () => {
      live = false;
    };
  }, [launcher, machine]);
  return (
    <>
      {places.map((u) => (
        <LauncherSection key={`${u.extension}/${u.id}`} place={u} launcher={launcher} machine={machine} />
      ))}
    </>
  );
}

function LauncherSection({ place, launcher, machine }: { place: UIPlace; launcher: string; machine?: string }) {
  const [gone, setGone] = useState(false);
  const context = useMemo(() => ({ launcher, machine }), [launcher, machine]);
  if (gone) return null;
  return (
    <div data-extension-section={`${place.extension}/${place.id}`}>
      <div className="mb-1 flex items-center gap-1.5 text-submeta font-medium text-ink-3">
        <Icon name="extension" size={11} className="shrink-0" />
        {place.title}
      </div>
      {/* the extension's own id scopes what a graph's `key` keeps (lib/extension-owner.tsx) */}
      <ExtensionOwnerScope owner={place.extension}>
        <ExtensionUI extension={place.extension} ui={place.id} context={context} embedded onUnsupported={() => setGone(true)} />
      </ExtensionOwnerScope>
    </div>
  );
}

/** Sends the subscription pages' Set Up with … text to the assistant's main conversation (Ask Persona), loading the
 *  outbox only then; a conversation that can't start says so. Inbox Sources' relationship view sends the same. */
export function askAssistant(text: string) {
  void import("../../lib/outbox")
    .then((m) => m.askPersona(text))
    .catch((e: unknown) => toast(t("settings-extensionview.action_failed", { error: errorText(e) }), "error"));
}

/** Puts one of an extension's quick prompts in the assistant's main composer, without sending it: the main
 *  conversation opens on the computer it really runs on (main_machine_id, lib/feed-cache.ts), or the
 *  new-conversation page when there is none yet, and the owner sends when ready. */
export function composeAssistant(text: string) {
  const a = getState().assistant;
  const machine = mainConversationMachine(a);
  if (a?.main && machine) {
    sendDraft(`${machine}/${a.main}`, { type: "replace", text });
    navigate(conversationPage(a.main, machine, chosenManager() ?? undefined));
    return;
  }
  sendDraft("persona-new", { type: "replace", text });
  const onStarter = typeof location !== "undefined" && (location.pathname === "/persona/new" || (location.pathname === "/persona" && !a?.main));
  if (!onStarter) navigate("/persona/new");
}
