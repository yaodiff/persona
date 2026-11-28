// The right-hand panel next to the page (docs/web/web.md "Side panel"): one at a time, chosen in lib/panel.ts. (A session's
// terminal is not one: it is the agent page's own view, lib/session-view.ts.)
// Wide screens (lg+) get a resizable column that pushes the page; narrower ones an overlay over the page.
import { lazy, Suspense, useCallback, useEffect, useId, useLayoutEffect, useRef, useState, useSyncExternalStore, type KeyboardEventHandler, type ReactNode, type Ref } from "react";
import { closePanel, directoryPanelKey, panelOpener, popPanel, SidePanelBackContext, usePanel, usePanelCanGoBack, type Panel } from "../lib/panel";
import { activeComposer } from "../lib/links";
import { returnFocus } from "../ui/overlay";
import { CHAT_MIN, PANEL_MIN, useLayout } from "../lib/layout";
import { sameValue } from "../lib/stable-lines";
import { cx, EASE, Loading } from "../ui/atoms";
import { ResizeHandle } from "../ui/ResizeHandle";
import { useCloseOnBack } from "../lib/back-close";
import { mediaMatches, NARROW, PHONE, useMedia } from "../lib/viewport";
import { t } from "../i18n";
import { PageBoundary } from "./PageBoundary";

// Files, Git, the file viewer and a link load when one is first opened (#2673): with their viewers (sheets, Office,
// compare, documents) they are a large part of the main bundle no first screen needs. So do a task's and an Agent's
// panels (#5424), opened from a conversation while Agents open in the side panel (lib/agent-open.ts).
const FilesPanel = lazy(() => import("./session/FilesPanel").then((m) => ({ default: m.FilesPanel })));
const FileViewerPanel = lazy(() => import("./session/FileViewer").then((m) => ({ default: m.FileViewerPanel })));
const GitPanel = lazy(() => import("./session/GitPanel").then((m) => ({ default: m.GitPanel })));
const PreviewPanel = lazy(() => import("./session/PreviewPanel").then((m) => ({ default: m.PreviewPanel })));
const ReviewPanel = lazy(() => import("./session/ReviewPanel").then((m) => ({ default: m.ReviewPanel })));
const ChangesPanel = lazy(() => import("./session/ChangesPanel").then((m) => ({ default: m.ChangesPanel })));
const LinkPanel = lazy(() => import("./link/LinkPanel").then((m) => ({ default: m.LinkPanel })));
const ConversationTasksPanel = lazy(() => import("./chat/ConversationTasksPanel").then((m) => ({ default: m.ConversationTasksPanel })));
const TaskPanel = lazy(() => import("./assistant/TaskPanel").then((m) => ({ default: m.TaskPanel })));
const AgentPanel = lazy(() => import("./assistant/TaskPanel").then((m) => ({ default: m.AgentPanel })));
// a lead's Team (#5180): only on a lead's page, outside the first screen
const TeamPanel = lazy(() => import("./chat/TeamPanel").then((m) => ({ default: m.TeamPanel })));
// a session's Side sessions (#7944): the Team panel's layout, in the same chunk
const SidesPanel = lazy(() => import("./chat/TeamPanel").then((m) => ({ default: m.SidesPanel })));
// an Agent's Background tasks (#5421): opened by hand, outside the first screen
const BackgroundPanel = lazy(() => import("./chat/BackgroundPanel").then((m) => ({ default: m.BackgroundPanel })));
// a subagent's full conversation (#14323): shares SessionOverview's lazy chunk so overview -> detail needs no extra load
const SubagentPanel = lazy(() => import("./chat/SessionOverview").then((m) => ({ default: m.SubagentPanel })));

const WIDTH_KEY = "persona.panelWidth";
const DEFAULT_WIDTH = 600;
const noopSubscribe = () => () => {};
// the panel's least and the conversation's least beside it live with the shell's other widths (lib/layout.ts)
export { CHAT_MIN };

export interface SidePanelRenderActions {
  onClose: () => void;
  onBack?: () => void;
}

export interface SidePanelViewProps {
  panel: Panel | null;
  maximized?: boolean;
  canGoBack?: boolean;
  width: number;
  minWidth?: number;
  maxWidth: number;
  asideId?: string;
  asideRef?: Ref<HTMLElement>;
  pageKey?: string;
  contentKey?: string | number;
  onClose: () => void;
  onBack?: () => void;
  onResize: (width: number) => void;
  onCommitWidth: (width: number) => void;
  onResetWidth: () => void;
  onKeyDown?: KeyboardEventHandler<HTMLElement>;
  renderPanel?: (panel: Panel, actions: SidePanelRenderActions) => ReactNode;
  fallback?: ReactNode;
}

export interface SidePanelDeps {
  /** Optional scope partition (value or getter); switching it resets transient panel/width/back state and fences callbacks. */
  scopeKey?: string | (() => string);
  /** Subscribe to scope changes when `scopeKey` is a getter. */
  subscribeScopeKey?: (listener: () => void) => () => void;
  /** Controlled active panel or reader (`null` for closed; bypasses `usePanel` when supplied). */
  panel?: Panel | null | (() => Panel | null | undefined);
  /** Subscribe to controlled panel updates when `panel` is a getter. */
  subscribePanel?: (listener: () => void) => () => void;
  /** Controlled back-stack availability or reader (bypasses `usePanelCanGoBack` when supplied). */
  canGoBack?: boolean | (() => boolean);
  /** Subscribe to `canGoBack` updates when `canGoBack` is a getter. */
  subscribeCanGoBack?: (listener: () => void) => () => void;
  /** Controlled maximized layout flag or reader (bypasses `useLayout` when supplied). */
  maximized?: boolean | (() => boolean);
  /** Subscribe to `maximized` updates when `maximized` is a getter. */
  subscribeMaximized?: (listener: () => void) => () => void;
  /** Controlled narrow-viewport flag or reader (bypasses `useMedia(NARROW)` when supplied). */
  narrow?: boolean | (() => boolean);
  /** Subscribe to `narrow` updates when `narrow` is a getter. */
  subscribeNarrow?: (listener: () => void) => () => void;
  /** Controlled phone-viewport flag or reader used for close focus fallback (bypasses `mediaMatches(PHONE)`). */
  isPhone?: boolean | (() => boolean);
  /** Close the panel (defaults to `panel.closePanel`). */
  closePanel?: () => void;
  /** Pop one level of the side-panel stack (defaults to `panel.popPanel`). */
  popPanel?: () => boolean | void;
  /** Read the element that opened the panel for close focus restoration (defaults to `panel.panelOpener`). */
  getOpener?: () => Element | null;
  /** Fallback focus target on close (`null` disables fallback; defaults to `activeComposer()?.focus()`). */
  focusFallback?: (() => void) | null;
  /** Custom focus restoration handler on close (defaults to `overlay.returnFocus`). */
  restoreFocus?: (aside: HTMLElement, opener: Element | null, fallback?: () => void) => void;
  /** Whether narrow-screen Back closes/pops the panel (defaults to `true`). */
  closeOnBack?: boolean;
  /** Custom back-overlay registration (replaces `useCloseOnBack` / `history.pushState` when supplied). */
  registerBackOverlay?: (onBack: () => void) => () => void;
  /** Controlled panel width (bypasses `localStorage` width state when supplied). */
  width?: number;
  /** Default reset width (defaults to `600`). */
  defaultWidth?: number;
  /** Controlled combined room width (bypasses `innerWidth` and `ResizeObserver` when supplied). */
  room?: number;
  /** Custom width storage reader (replaces `localStorage.getItem`). */
  readStoredWidth?: () => number | null;
  /** Custom width storage writer (replaces `localStorage.setItem`). */
  writeStoredWidth?: (width: number) => void;
  /** Custom width storage clearer (replaces `localStorage.removeItem`). */
  clearStoredWidth?: () => void;
  /** Invoked on live resize, commit, and reset width updates. */
  onWidthChange?: (width: number) => void;
  /** Invoked when a resize gesture or keyboard adjustment commits. */
  onWidthCommit?: (width: number) => void;
  /** Invoked when width resets to default. */
  onWidthReset?: () => void;
  /** Custom panel content renderer (bypasses built-in lazy panels). */
  renderPanel?: (panel: Panel, actions: SidePanelRenderActions) => ReactNode;
  /** Custom Suspense fallback renderer. */
  renderFallback?: () => ReactNode;
  /** Custom outer view renderer (defaults to `<SidePanelView>`). */
  renderView?: (props: SidePanelViewProps) => ReactNode;
}

export interface SidePanelProps {
  deps?: SidePanelDeps;
}

/**
 * The width the panel is drawn at: the one asked for (dragged, or remembered), within what leaves the page beside it
 * CHAT_MIN. `room` is the page's width plus the panel's, the part of the shell the two share (the sidebar's is not in
 * it), so a folded or hidden sidebar gives the panel more. Never under PANEL_MIN.
 */
export function panelWidth(want: number, room: number) {
  return Math.round(Math.max(PANEL_MIN, Math.min(want, room - CHAT_MIN)));
}

function fallbackRoom(): number {
  return (typeof innerWidth === "number" ? innerWidth : 1280) * 0.72 + CHAT_MIN;
}

function readInitialWidth(deps: SidePanelDeps | undefined): number {
  const fallback = Math.max(PANEL_MIN, deps?.defaultWidth ?? DEFAULT_WIDTH);
  if (deps?.width !== undefined) return Math.max(PANEL_MIN, deps.width);
  try {
    const raw = deps?.readStoredWidth
      ? deps.readStoredWidth()
      : deps?.writeStoredWidth || deps?.clearStoredWidth
        ? null
        : Number(localStorage.getItem(WIDTH_KEY));
    return raw !== null && Number.isFinite(raw) && raw > 0 ? Math.max(PANEL_MIN, raw) : fallback;
  } catch {
    return fallback;
  }
}

function useTrackedScope(
  source: string | (() => string) | undefined,
  subscribe: ((listener: () => void) => () => void) | undefined,
) {
  const isGetter = typeof source === "function";
  const read = useCallback(() => (typeof source === "function" ? source() : (source ?? "")), [source]);
  const committedRef = useRef<{ value: string; version: number } | null>(null);
  const snapRef = useRef<{ value: string; version: number } | null>(null);
  const sub = useCallback((listener: () => void) => {
    if (!subscribe || !isGetter) return noopSubscribe();
    return subscribe(() => {
      const next = read();
      const cur = committedRef.current;
      if (!cur || next !== cur.value) {
        committedRef.current = { value: next, version: (cur?.version ?? 0) + 1 };
      }
      listener();
    });
  }, [subscribe, isGetter, read]);
  const getSnapshot = useCallback(() => {
    const next = read();
    const cur = committedRef.current;
    if (cur && next === cur.value) return cur;
    const nextVersion = cur ? cur.version + 1 : 0;
    if (snapRef.current && snapRef.current.value === next && snapRef.current.version === nextVersion) {
      return snapRef.current;
    }
    snapRef.current = { value: next, version: nextVersion };
    return snapRef.current;
  }, [read]);
  const snap = useSyncExternalStore(sub, getSnapshot, getSnapshot);
  useLayoutEffect(() => {
    committedRef.current = snap;
  }, [snap]);
  const readToken = useCallback(() => {
    const cur = read();
    const committed = committedRef.current;
    return committed && committed.value === cur ? `${committed.version}:${cur}` : `drift:${cur}`;
  }, [read]);
  return { value: snap.value, token: `${snap.version}:${snap.value}`, readToken };
}

function useCachedExternalValue<T>(
  source: T | (() => T) | undefined,
  subscribe: ((listener: () => void) => () => void) | undefined,
): T | undefined {
  const cache = useRef<{ has: boolean; value: T | undefined }>({ has: false, value: undefined });
  const getSnapshot = useCallback((): T | undefined => {
    const next = typeof source === "function" ? (source as () => T)() : source;
    if (cache.current.has && sameValue(cache.current.value, next)) {
      return cache.current.value;
    }
    cache.current = { has: true, value: next };
    return next;
  }, [source]);
  const sub = subscribe && typeof source === "function" ? subscribe : noopSubscribe;
  return useSyncExternalStore(sub, getSnapshot, getSnapshot);
}

function DefaultPanelBridge({ render }: { render: (panel: Panel | null) => ReactNode }) {
  return <>{render(usePanel())}</>;
}

function DefaultCanGoBackBridge({ render }: { render: (canGoBack: boolean) => ReactNode }) {
  return <>{render(usePanelCanGoBack())}</>;
}

function DefaultMaximizedBridge({ render }: { render: (maximized: boolean) => ReactNode }) {
  return <>{render(useLayout().maximized)}</>;
}

function DefaultNarrowBridge({ render }: { render: (narrow: boolean) => ReactNode }) {
  return <>{render(useMedia(NARROW))}</>;
}

/** Pure presentational view for the right-hand side panel. */
const NON_TEXT_INPUT = new Set(["checkbox", "radio", "button", "submit", "reset", "range", "file", "image", "color"]);

function isTextField(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false;
  if (target instanceof HTMLInputElement) return !NON_TEXT_INPUT.has(target.type);
  if (target.closest("textarea, select")) return true;
  const ce = target.closest("[contenteditable]");
  return ce ? ce.getAttribute("contenteditable") !== "false" : Boolean((target as HTMLElement).isContentEditable);
}

export function SidePanelView({
  panel,
  maximized = false,
  canGoBack = false,
  width,
  minWidth = PANEL_MIN,
  maxWidth,
  asideId,
  asideRef,
  pageKey,
  contentKey,
  onClose,
  onBack,
  onResize,
  onCommitWidth,
  onResetWidth,
  onKeyDown,
  renderPanel,
  fallback,
}: SidePanelViewProps) {
  if (!panel) return null;
  const back = canGoBack ? onBack : undefined;
  const handleKeyDown: KeyboardEventHandler<HTMLElement> = onKeyDown ?? ((e) => {
    // Esc closes the panel from inside it, unless something in it (a field, a menu) or a dialog over it took the key
    if (e.key !== "Escape" || e.defaultPrevented || e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return;
    if ([...document.querySelectorAll("[role=menu],[role=dialog],[role=alertdialog]")].some((el) => !el.closest("[data-closing],[inert]"))) return;
    if (isTextField(e.target)) {
      // Keep the panel open while editing a text field, and stop propagation so ChatView's window Esc never interrupts a working agent.
      e.stopPropagation();
      return;
    }
    const unsaved = e.currentTarget.querySelector("[data-unsaved-edit]");
    if (unsaved && !unsaved.dispatchEvent(new CustomEvent("persona:escape-unsaved", { cancelable: true }))) {
      e.preventDefault();
      e.stopPropagation();
      return;
    }
    e.preventDefault();
    // not also the conversation's Esc (it stops a running agent, ChatView)
    e.stopPropagation();
    // the focus goes back to the panel's button, or the message box (the close effect in SidePanel, #5155)
    onClose();
  });
  return (
    <aside
      ref={asideRef}
      id={asideId}
      data-side-panel=""
      aria-label={panelLabel(panel)}
      onKeyDown={handleKeyDown}
      // narrow: over the visible part of the page (--kb-top / --kb-h, lib/viewport.ts), so on iOS the terminal's key
      // bar sits on the keyboard instead of under it; fixed, it is outside #root's safe-area padding, so it keeps clear
      // of a landscape notch itself, as the drawer does
      className={cx(
        "fixed inset-x-0 top-(--kb-top) z-30 flex h-(--kb-h) flex-col bg-page shadow-overlay max-lg:pr-[env(safe-area-inset-right)] max-lg:pl-[env(safe-area-inset-left)] lg:relative lg:inset-auto lg:top-auto lg:z-auto lg:h-auto lg:w-(--panel-w) lg:shrink-0 lg:border-l lg:border-line lg:shadow-none",
        maximized && "lg:hidden",
      )}
      style={{ "--panel-w": `${width}px`, animation: `slide-in-right 260ms ${EASE} backwards` } as React.CSSProperties}
    >
      {/* the panel is on the right: dragging its left edge left (or ArrowLeft) widens it */}
      <ResizeHandle
        edge="left"
        value={width}
        min={minWidth}
        max={maxWidth}
        onResize={onResize}
        onCommit={onCommitWidth}
        onReset={onResetWidth}
        label={t("sidepanel.resize_label")}
        title={t("sidepanel.resize_title")}
        controls={asideId}
        className="hidden lg:flex"
      />
      <div className="flex min-h-0 flex-1 flex-col">
        {/* a chunk gone after a hub redeploy reloads once, else shows a retry; never a blank panel */}
        <SidePanelBackContext.Provider value={back}>
          <PageBoundary key={contentKey} page={pageKey ?? JSON.stringify(panel)}>
            <Suspense fallback={fallback ?? <div className="flex flex-1 items-center justify-center"><Loading /></div>}>
              {renderPanel ? renderPanel(panel, { onClose, onBack: back }) : body(panel, onClose)}
            </Suspense>
          </PageBoundary>
        </SidePanelBackContext.Provider>
      </div>
    </aside>
  );
}

interface SidePanelBodyProps {
  panel: Panel | null;
  canGoBack: boolean;
  maximized: boolean;
  narrow: boolean;
  deps?: SidePanelDeps;
  scopeEpoch: number;
  outerScopeToken: string;
  readOuterScopeToken: () => string;
  hasScope: boolean;
}

function SidePanelBody({
  panel,
  canGoBack,
  maximized,
  narrow,
  deps,
  scopeEpoch,
  outerScopeToken,
  readOuterScopeToken,
  hasScope,
}: SidePanelBodyProps) {
  const controlledWidth = deps?.width;
  const controlledRoom = deps?.room;
  const hasWidthReader = deps?.readStoredWidth !== undefined;
  const [widthSource, setWidthSource] = useState<{ epoch: number; controlled: boolean; hasReader: boolean; gen: number }>(() => ({
    epoch: scopeEpoch,
    controlled: controlledWidth !== undefined,
    hasReader: hasWidthReader,
    gen: 1,
  }));
  const widthSourceChanged = widthSource.epoch !== scopeEpoch
    || widthSource.controlled !== (controlledWidth !== undefined)
    || widthSource.hasReader !== hasWidthReader;
  const widthGen = widthSourceChanged ? widthSource.gen + 1 : widthSource.gen;
  if (widthSourceChanged) {
    setWidthSource({ epoch: scopeEpoch, controlled: controlledWidth !== undefined, hasReader: hasWidthReader, gen: widthGen });
  }

  const [localWidth, setLocalWidth] = useState<{ gen: number; value: number }>(() => ({
    gen: widthGen,
    value: readInitialWidth(deps),
  }));
  if (localWidth.gen !== widthGen) {
    setLocalWidth({ gen: widthGen, value: readInitialWidth(deps) });
  }

  const [roomSource, setRoomSource] = useState<{ epoch: number; controlled: boolean; gen: number }>(() => ({
    epoch: scopeEpoch,
    controlled: controlledRoom !== undefined,
    gen: 1,
  }));
  const roomSourceChanged = roomSource.epoch !== scopeEpoch || roomSource.controlled !== (controlledRoom !== undefined);
  const roomGen = roomSourceChanged ? roomSource.gen + 1 : roomSource.gen;
  if (roomSourceChanged) {
    setRoomSource({ epoch: scopeEpoch, controlled: controlledRoom !== undefined, gen: roomGen });
  }

  const [measuredRoom, setMeasuredRoom] = useState<{ gen: number; value: number }>(() => ({
    gen: roomGen,
    value: fallbackRoom(),
  }));

  const [backState, setBackState] = useState<{ epoch: number; step: number }>(() => ({ epoch: scopeEpoch, step: 0 }));
  const backStep = backState.epoch === scopeEpoch ? backState.step : 0;

  const effectiveLocalWidth = localWidth.gen === widthGen ? localWidth.value : readInitialWidth(deps);
  const width = controlledWidth !== undefined ? Math.max(PANEL_MIN, controlledWidth) : effectiveLocalWidth;
  const room = controlledRoom !== undefined
    ? controlledRoom
    : (measuredRoom.gen === roomGen ? measuredRoom.value : fallbackRoom());
  const shown = panelWidth(width, room);
  const id = useId();
  const aside = useRef<HTMLElement>(null);
  const open = Boolean(panel);

  const activeEpochRef = useRef(scopeEpoch);
  const minValidEpochRef = useRef(scopeEpoch);
  const activeWidthGenRef = useRef(widthGen);
  const depsRef = useRef(deps);
  const roomRef = useRef(room);
  const canGoBackRef = useRef(canGoBack);
  const readScopeTokenRef = useRef(readOuterScopeToken);

  const capturedEpoch = scopeEpoch;
  const capturedScopeToken = outerScopeToken;
  const isCurrent = () => activeEpochRef.current === capturedEpoch && readScopeTokenRef.current() === capturedScopeToken;

  const handleClose = () => {
    if (!isCurrent()) return;
    (depsRef.current?.closePanel ?? closePanel)();
  };

  const handleContextBack = () => {
    if (!isCurrent()) return;
    (depsRef.current?.popPanel ?? popPanel)();
  };

  const handleBack = () => {
    if (!isCurrent()) return;
    const overlays = [...document.querySelectorAll<HTMLElement>("[data-menu],[role=menu],[role=dialog]")].filter(
      (el) => !el.closest("[data-closing],[inert]"),
    );
    const topOverlay = overlays[overlays.length - 1];
    if (topOverlay) {
      topOverlay.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
      setBackState((prev) => ({ epoch: capturedEpoch, step: prev.epoch === capturedEpoch ? prev.step + 1 : 1 }));
      return;
    }
    const customPop = depsRef.current?.popPanel;
    const popped = customPop ? customPop() : popPanel();
    if (popped === true || (popped === undefined && canGoBackRef.current)) {
      setBackState((prev) => ({ epoch: capturedEpoch, step: prev.epoch === capturedEpoch ? prev.step + 1 : 1 }));
    }
  };

  const handleBackRef = useRef(handleBack);

  useLayoutEffect(() => {
    depsRef.current = deps;
    roomRef.current = room;
    canGoBackRef.current = canGoBack;
    activeWidthGenRef.current = widthGen;
    readScopeTokenRef.current = readOuterScopeToken;
    handleBackRef.current = handleBack;
  });

  useLayoutEffect(() => {
    activeEpochRef.current = scopeEpoch;
    if (scopeEpoch > minValidEpochRef.current) {
      minValidEpochRef.current = scopeEpoch;
    }
    return () => {
      if (activeEpochRef.current === scopeEpoch) activeEpochRef.current = 0;
    };
  }, [scopeEpoch]);

  const closeOnBack = deps?.closeOnBack !== false;
  const customRegisterBack = deps?.registerBackOverlay;
  const backOverlayStep = scopeEpoch * 1_000_000 + backStep;
  const useDefaultBack = open && narrow && closeOnBack && !customRegisterBack;
  // narrow, it covers the page: Back closes any open inner menu/dialog without its own Back entry first, then pops
  // one level in the side-panel stack, and finally closes the panel (staying on the conversation)
  useCloseOnBack(useDefaultBack, handleBack, useDefaultBack, backOverlayStep);

  useEffect(() => {
    if (!open || !narrow || !closeOnBack || !customRegisterBack) return;
    const runEpoch = scopeEpoch;
    const runScopeToken = outerScopeToken;
    let active = true;
    const cleanup = customRegisterBack(() => {
      if (!active || activeEpochRef.current !== runEpoch || readScopeTokenRef.current() !== runScopeToken) return;
      handleBackRef.current();
    });
    return () => {
      active = false;
      cleanup?.();
    };
  }, [open, narrow, closeOnBack, customRegisterBack, backOverlayStep, scopeEpoch, outerScopeToken]);

  // Closed (×, Esc, its button again): the focus goes back to the button that opened it, else to the message box, not
  // to the page (#5155). Not the message box on a phone: that would bring the keyboard up.
  useEffect(() => {
    const el = aside.current;
    if (!open || !el) return;
    const openedEpoch = scopeEpoch;
    return () => {
      if (
        openedEpoch < minValidEpochRef.current
        || (activeEpochRef.current !== 0 && activeEpochRef.current !== openedEpoch)
      ) return;
      const cur = depsRef.current;
      const opener = cur?.getOpener ? cur.getOpener() : panelOpener();
      const phone = cur?.isPhone !== undefined
        ? (typeof cur.isPhone === "function" ? cur.isPhone() : cur.isPhone)
        : mediaMatches(PHONE);
      const fallback = phone
        ? undefined
        : cur?.focusFallback !== undefined
          ? (cur.focusFallback ?? undefined)
          : () => activeComposer()?.focus();
      if (cur?.restoreFocus) cur.restoreFocus(el, opener, fallback);
      else returnFocus(el, opener, fallback);
    };
  }, [open, scopeEpoch]);

  // The page (<main>, the panel's sibling, App.tsx) and the panel share the room: measure it before paint, and again
  // whenever the page's width changes (window resized, sidebar folded / hidden / back). Their sum does not change with
  // the panel's own width, so this does not feed back on itself.
  useLayoutEffect(() => {
    if (controlledRoom !== undefined) return;
    const el = aside.current;
    const main = el?.parentElement?.querySelector<HTMLElement>(":scope > main");
    if (!el || !main) {
      setMeasuredRoom({ gen: roomGen, value: fallbackRoom() });
      return;
    }
    const measure = () => setMeasuredRoom({ gen: roomGen, value: main.getBoundingClientRect().width + el.getBoundingClientRect().width });
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(main);
    return () => ro.disconnect();
  }, [open, controlledRoom, roomGen]);

  const handleResize = (w: number) => {
    if (!isCurrent()) return;
    if (depsRef.current?.width === undefined) {
      setLocalWidth({ gen: activeWidthGenRef.current, value: w });
    }
    depsRef.current?.onWidthChange?.(w);
  };

  const handleCommitWidth = (w: number) => {
    if (!isCurrent()) return;
    const cur = depsRef.current;
    const next = panelWidth(w, roomRef.current);
    if (cur?.width === undefined) {
      setLocalWidth({ gen: activeWidthGenRef.current, value: next });
    }
    try {
      if (cur?.writeStoredWidth) {
        cur.writeStoredWidth(next);
      } else if (cur?.width === undefined && !cur?.readStoredWidth && !cur?.clearStoredWidth) {
        localStorage.setItem(WIDTH_KEY, String(next));
      }
    } catch {
      // storage refused (private mode / quota): width lasts this session
    } finally {
      cur?.onWidthChange?.(next);
      cur?.onWidthCommit?.(next);
    }
  };

  const handleResetWidth = () => {
    if (!isCurrent()) return;
    const cur = depsRef.current;
    const next = Math.max(PANEL_MIN, cur?.defaultWidth ?? DEFAULT_WIDTH);
    if (cur?.width === undefined) {
      setLocalWidth({ gen: activeWidthGenRef.current, value: next });
    }
    try {
      if (cur?.clearStoredWidth) {
        cur.clearStoredWidth();
      } else if (cur?.width === undefined && !cur?.readStoredWidth && !cur?.writeStoredWidth) {
        localStorage.removeItem(WIDTH_KEY);
      }
    } catch {
      // storage refused (private mode / quota): reset lasts this session
    } finally {
      cur?.onWidthChange?.(next);
      cur?.onWidthReset?.();
    }
  };

  const pageKey = panel ? (hasScope ? JSON.stringify([scopeEpoch, panel]) : JSON.stringify(panel)) : undefined;
  const viewProps: SidePanelViewProps = {
    panel,
    maximized,
    canGoBack,
    width: shown,
    minWidth: PANEL_MIN,
    maxWidth: panelWidth(Infinity, room),
    asideId: id,
    asideRef: aside,
    pageKey,
    contentKey: hasScope ? scopeEpoch : undefined,
    onClose: handleClose,
    onBack: handleContextBack,
    onResize: handleResize,
    onCommitWidth: handleCommitWidth,
    onResetWidth: handleResetWidth,
    renderPanel: deps?.renderPanel,
    fallback: deps?.renderFallback?.(),
  };
  if (deps?.renderView) return <>{deps.renderView(viewProps)}</>;
  return <SidePanelView {...viewProps} />;
}

export function SidePanel({ deps }: SidePanelProps = {}) {
  const trackedScope = useTrackedScope(deps?.scopeKey, deps?.subscribeScopeKey);
  const panelScope = trackedScope.token;
  const [scope, setScope] = useState<{ key: string; epoch: number }>(() => ({ key: panelScope, epoch: 1 }));
  const effectiveEpoch = scope.key === panelScope ? scope.epoch : scope.epoch + 1;
  if (scope.key !== panelScope) {
    setScope({ key: panelScope, epoch: effectiveEpoch });
  }

  const controlledPanel = useCachedExternalValue(deps?.panel, deps?.subscribePanel);
  const controlledCanGoBack = useCachedExternalValue(deps?.canGoBack, deps?.subscribeCanGoBack);
  const controlledMaximized = useCachedExternalValue(deps?.maximized, deps?.subscribeMaximized);
  const controlledNarrow = useCachedExternalValue(deps?.narrow, deps?.subscribeNarrow);

  const needDefaultPanel = deps?.panel === undefined;
  const needDefaultCanGoBack = deps?.canGoBack === undefined;
  const needDefaultMaximized = deps?.maximized === undefined;
  const needDefaultNarrow = deps?.narrow === undefined;
  const hasScope = deps?.scopeKey !== undefined;

  const renderBody = (panel: Panel | null, canGoBack: boolean, maximized: boolean, narrow: boolean) => (
    <SidePanelBody
      panel={panel}
      canGoBack={canGoBack}
      maximized={maximized}
      narrow={narrow}
      deps={deps}
      scopeEpoch={effectiveEpoch}
      outerScopeToken={trackedScope.token}
      readOuterScopeToken={trackedScope.readToken}
      hasScope={hasScope}
    />
  );

  const renderWithNarrow = (panel: Panel | null, canGoBack: boolean, maximized: boolean) => {
    if (needDefaultNarrow) {
      return <DefaultNarrowBridge render={(narrow) => renderBody(panel, canGoBack, maximized, narrow)} />;
    }
    return renderBody(panel, canGoBack, maximized, Boolean(controlledNarrow));
  };

  const renderWithMaximized = (panel: Panel | null, canGoBack: boolean) => {
    if (needDefaultMaximized) {
      return <DefaultMaximizedBridge render={(maximized) => renderWithNarrow(panel, canGoBack, maximized)} />;
    }
    return renderWithNarrow(panel, canGoBack, Boolean(controlledMaximized));
  };

  const renderWithCanGoBack = (panel: Panel | null) => {
    if (needDefaultCanGoBack) {
      return <DefaultCanGoBackBridge render={(canGoBack) => renderWithMaximized(panel, canGoBack)} />;
    }
    return renderWithMaximized(panel, Boolean(controlledCanGoBack));
  };

  if (needDefaultPanel) {
    return <DefaultPanelBridge render={renderWithCanGoBack} />;
  }
  return renderWithCanGoBack(controlledPanel ?? null);
}

function panelLabel(p: Panel) {
  if (p.kind === "conversation-tasks") return t("sidepanel.conversation_tasks");
  if (p.kind === "subagent") return t("sidepanel.subagent");
  return p.kind === "files" ? t("sidepanel.files") : p.kind === "git" ? "Git" : p.kind === "review" ? t("sidepanel.review") : p.kind === "changes" ? t("sidepanel.changes") : p.kind === "file" ? t("sidepanel.files") : p.kind === "preview" ? t("sidepanel.preview") : p.kind === "team" ? t("sidepanel.team") : p.kind === "sides" ? t("sidepanel.sides") : p.kind === "background" ? t("sidepanel.background") : p.kind === "task" ? t("sidepanel.task") : p.kind === "agent" ? t("sidepanel.agent") : t("sidepanel.link");
}

function body(p: Panel, onClose: () => void) {
  switch (p.kind) {
    case "files":
      return <FilesPanel key={directoryPanelKey(p)} mid={p.mid} sid={p.sid} cwd={p.cwd} path={p.path} file={p.file} highlight={p.highlight} onClose={onClose} />;
    case "git":
      return <GitPanel key={directoryPanelKey(p)} mid={p.mid} sid={p.sid} cwd={p.cwd} at={p.at} onClose={onClose} />;
    case "review":
      return <ReviewPanel key={directoryPanelKey(p)} mid={p.mid} sid={p.sid} cwd={p.cwd} scope={p.scope} base={p.base} target={p.target} live={p.live} at={p.at} opened={p} onClose={onClose} />;
    case "changes":
      return <ChangesPanel key={`${p.mid}/${p.sid}`} mid={p.mid} sid={p.sid} opened={p} onClose={onClose} />;
    case "file":
      return <FileViewerPanel key={`${p.mid}:${p.path}:${p.line ?? ""}-${p.to ?? ""}${p.readOnly ? ":ro" : ""}:${p.navigationId ?? ""}`} mid={p.mid} sid={p.sid} path={p.path} cwd={p.cwd} line={p.line} to={p.to} readOnly={p.readOnly} onClose={onClose} />;
    case "link":
      return <LinkPanel key={`${p.manager ?? ""} ${p.url}`} url={p.url} title={p.title} manager={p.manager} onClose={onClose} />;
    case "conversation-tasks":
      return <ConversationTasksPanel key={JSON.stringify([p.manager, p.mid, p.sid])} sid={p.sid} manager={p.manager} onClose={onClose} />;
    case "task":
      return <TaskPanel key={p.todo} todoId={p.todo} onClose={onClose} />;
    case "agent":
      // never a subagent's own conversation: openPanel opens its parent's instead (#5030, lib/subagent-link.ts)
      return <AgentPanel key={`${p.mid}/${p.sid}`} mid={p.mid} sid={p.sid} onClose={onClose} />;
    case "team":
      return <TeamPanel key={`${p.mid}/${p.sid}`} mid={p.mid} sid={p.sid} worker={p.worker} at={p.at} onClose={onClose} />;
    case "sides":
      // keyed by the parent, not the side: picking another pill keeps the strip where it is
      return <SidesPanel key={`${p.mid}/${p.sid}`} mid={p.mid} sid={p.sid} side={p.side} at={p.at} onClose={onClose} />;
    case "background":
      // keyed by the session, not the run: asking for another run (a new object) goes to its detail without a remount
      return <BackgroundPanel key={`${p.mid}/${p.sid}`} mid={p.mid} sid={p.sid} opened={p} onClose={onClose} />;
    case "subagent":
      return <SubagentPanel key={`${p.mid}/${p.sid}`} mid={p.mid} sid={p.sid} parentMid={p.parentMid} parentSid={p.parentSid} call={p.call} from={p.from} onClose={onClose} />;
    case "preview":
      return <PreviewPanel key={`${p.mid}/${p.sid}/${p.port ?? ""}${p.path ?? ""}`} mid={p.mid} sid={p.sid} cwd={p.cwd} port={p.port} path={p.path} onClose={onClose} />;
  }
}
