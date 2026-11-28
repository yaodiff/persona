// The full surface of a terminal panel (docs/integrations/terminal.md "Full herdr UI"): herdr's own client, in a PTY on the
// computer, drawn by xterm — its sidebar, tabs and splits, driven from here. The feed is full.ts; what the page shares
// with the single-pane surface (the key bar, the find bar, the right-click menu, the saved commands) is the same
// components. The header's controls for this surface are drawn into the panel's header through `slot`; its gear has
// only Appearance (the terminal's light / dark, which herdr is told), the one setting of Single pane's that is this surface's too.
import { useCallback, useEffect, useLayoutEffect, useReducer, useRef, useState, useSyncExternalStore } from "react";
import { createPortal } from "react-dom";
import { t } from "../../../i18n";
import { shouldRefocus, terminalMenu, type TerminalMenuKey } from "../../../lib/terminal-details";
import { FIND_CLOSED, findReducer, isFindShortcut, queryFromSelection } from "../../../lib/terminal-search";
import { STATUS_LABEL, canReconnect, type FullStatus } from "../../../lib/terminal-mode";
import { schemeScope, useTerminalAppearance } from "../../../lib/terminal-appearance";
import { Button, cx, IconButton, Loading, Menu, type MenuItem } from "../../../ui/atoms";
import { Icon } from "../../../ui/icons";
import { FindBar } from "./FindBar";
import { focusFacts, selectionInside } from "./focus";
import { type FullFeed, type FullView } from "./full";
import { acquireFeed, releaseFeed } from "./full-registry";
import { KeyBar } from "./KeyBar";
import { saveKeyBar, savedKeyBar } from "./keybar-pref";
import { PaneActions } from "./PaneActions";
import { ShortcutsEditor } from "./Shortcuts";
import { AppearanceRow, OptionsPopover } from "./TerminalOptions";
import { UploadStatus } from "./UploadStatus";

const nothing = () => () => {};

function useFull(feed: FullFeed | null): FullView | null {
  return useSyncExternalStore(feed?.subscribe ?? nothing, () => feed?.snapshot() ?? null);
}

/** Whether the panel is under an `inert` ancestor: a terminal the page keeps mounted but not in front (views/terminal/TerminalPage.tsx). */
export function useUnseen(root: React.RefObject<HTMLElement | null>): boolean {
  const [unseen, setUnseen] = useState(false);
  useEffect(() => {
    const el = root.current;
    if (!el) return;
    const check = () => setUnseen(Boolean(el.closest("[inert]")));
    check();
    const watch = new MutationObserver(check);
    for (let a: HTMLElement | null = el; a; a = a.parentElement) watch.observe(a, { attributes: true, attributeFilter: ["inert"] });
    return () => watch.disconnect();
  }, [root]);
  return unseen;
}

const DOT: Record<FullStatus, string> = {
  connecting: "bg-orange animate-pulse busy-motion",
  live: "bg-green",
  closed: "bg-ink-3",
  error: "bg-red",
  unsupported: "bg-ink-3",
  forbidden: "bg-red",
};

export function FullTerminal({
  mid,
  sid,
  cwd,
  onFiles,
  slot,
  live,
  delay,
  onFallback,
  onFailed,
  compact,
}: {
  mid: string;
  sid: string;
  cwd: string;
  onFiles?: () => void;
  /** Where the header's controls for this surface go. */
  slot: HTMLElement | null;
  /** False while this surface is on its way out: the client on the computer is let go, what is drawn stays. */
  live: boolean;
  /** Wait this long (ms) before opening the client: the surface before it is still on screen, driving its own pane. */
  delay: number;
  /** Asks the panel to go back to "Single pane" (the hub or computer cannot do this one). */
  onFallback: () => void;
  /** The client did not start on this machine (why, in words): the panel falls back to "Single pane" by itself. */
  onFailed: (why: string) => void;
  /** Compact standalone header: fold secondary actions into a single overflow menu. */
  compact?: boolean;
}) {
  const root = useRef<HTMLDivElement>(null);
  const host = useRef<HTMLDivElement>(null);
  const viewport = useRef<HTMLDivElement>(null);
  const overflowAnchor = useRef<HTMLElement | null>(null);
  const [feed, setFeed] = useState<FullFeed | null>(null);
  const v = useFull(feed);
  const unseen = useUnseen(root);
  const [keyBar, setKeyBar] = useState(savedKeyBar);
  const [editing, setEditing] = useState(false);
  const [options, setOptions] = useState<HTMLElement | null>(null);
  const [find, dispatchFind] = useReducer(findReducer, FIND_CLOSED);
  const [menu, setMenu] = useState<{ x: number; y: number; selection: boolean } | null>(null);
  // the terminal's own light / dark (Terminal settings › Appearance): its screen takes that scheme's tokens
  const scheme = schemeScope(useTerminalAppearance());
  const delayAtMount = useRef(delay);
  const sidAtMount = useRef(sid);
  const token = useRef({}).current;
  const firstClaim = useRef(true);
  const leaving = useRef(false);
  leaving.current = !live;

  // The machine's one feed (full-registry.ts): the first surface of the machine builds xterm in its box, the others
  // take it over when they are in front, so a route change between panes of the machine keeps the client and its screen.
  useEffect(() => {
    const { feed: f, fresh } = acquireFeed(mid, sidAtMount.current);
    setFeed(f);
    if (fresh) void f.mount(host.current!, viewport.current!);
    return () => releaseFeed(mid, f);
  }, [mid]);

  // The client on the computer lives only while a surface of the machine is the one shown and in front. The DOM is
  // asked as well as `unseen` (an effect that has not run yet at the first render): a kept terminal behind the one
  // shown must not take the feed from it.
  const on = live && !unseen;
  // the gear goes with the header's controls
  useEffect(() => {
    if (!on) setOptions(null);
  }, [on]);
  useLayoutEffect(() => {
    if (!feed || !on || root.current?.closest("[inert]")) return;
    feed.claim(token, host.current!, viewport.current!, sid, firstClaim.current ? delayAtMount.current : 0);
    firstClaim.current = false;
    return () => feed.yield(token, leaving.current); // leaving for the single pane: let go at once, it drives the pane next
  }, [feed, on, sid, token]);

  const status = v?.status ?? "connecting";
  const driving = status === "live";
  const failed = v?.failedToStart ? v.note || STATUS_LABEL[status] : "";
  useEffect(() => {
    if (failed && live) onFailed(failed);
  }, [failed, live, onFailed]);

  const refocus = useCallback(() => {
    const el = root.current;
    if (el && feed && shouldRefocus(focusFacts(el, feed.driving, feed.hasSelection()))) feed.focus();
  }, [feed]);
  useEffect(() => {
    addEventListener("focus", refocus);
    return () => removeEventListener("focus", refocus);
  }, [refocus]);

  const chooseKeyBar = (next: boolean) => {
    saveKeyBar(next);
    setKeyBar(next);
  };

  // ── find (lib/terminal-search.ts), over xterm's screen and scrollback ──
  const openFind = () => {
    const picked = feed?.selectedText() ?? "";
    dispatchFind({ type: "open", query: queryFromSelection(picked) ?? undefined });
  };
  const closeFind = () => {
    dispatchFind({ type: "close" });
    feed?.clearFind();
    feed?.focus();
  };
  const onKeyDownCapture = (e: React.KeyboardEvent) => {
    if (!isFindShortcut(e.nativeEvent, { mac: /Mac|iPhone|iPad/.test(navigator.platform), driving })) return;
    e.preventDefault();
    e.stopPropagation(); // the client does not get the key
    openFind();
  };

  // ── right-click: herdr's own menus (pane, space) win; Persona's menu is Shift + right click (lib/terminal-conflicts.ts) ──
  const onContextMenu = (e: React.MouseEvent) => {
    const route = feed ? feed.rightClick(e, (e.nativeEvent as PointerEvent).pointerType) : "menu";
    if (route === "browser") return;
    e.preventDefault(); // herdr's: the press already went to it as a mouse report; the browser's menu must not cover its own
    if (route === "herdr") return;
    const own = Boolean(viewport.current && selectionInside(viewport.current));
    (document.activeElement as HTMLElement | null)?.blur();
    setMenu({ x: e.clientX, y: e.clientY, selection: Boolean(feed?.hasSelection()) || own });
  };
  const menuItems = (): MenuItem[] => {
    const icons: Record<TerminalMenuKey, MenuItem["icon"]> = { copy: "copy", paste: "paste", selectAll: "list", search: "search", clearSelection: "x" };
    const run: Record<TerminalMenuKey, () => void> = {
      copy: () => (feed?.hasSelection() ? feed.copySelection() : feed?.copyText(window.getSelection()?.toString() ?? "")),
      paste: () => void feed?.pasteClipboard(),
      selectAll: () => feed?.selectAll(),
      search: openFind,
      clearSelection: () => {
        feed?.clearSelection();
        window.getSelection()?.removeAllRanges();
      },
    };
    return terminalMenu({ selection: menu?.selection ?? false, driving }).map((m) => ({
      key: m.key,
      label: m.label,
      icon: icons[m.key],
      disabled: m.disabled || !feed,
      divider: m.divider,
      onSelect: run[m.key],
    }));
  };

  // The grid stays out of the line (#3667): it is in the hint, in plain columns and rows.
  const grid = v && v.cols && v.rows ? t("terminal-full.live_grid", { cols: v.cols, rows: v.rows }) : "";
  const compactMenuItems: MenuItem[] = compact ? [
    ...(onFiles && cwd ? [{ key: "files", label: t("terminal-full.files"), icon: "folder" as const, onSelect: onFiles }] : []),
    { key: "keybar", label: keyBar ? t("terminal-full.keybar_hide") : t("terminal-full.keybar_show"), icon: "keyboard", checked: keyBar, onSelect: () => chooseKeyBar(!keyBar) },
    { key: "settings", label: t("terminal-full.settings"), icon: "gear", onSelect: () => setOptions(overflowAnchor.current) },
    { key: "reconnect", label: t("terminal-full.reconnect"), icon: "retry", disabled: !feed, onSelect: () => feed?.reconnect() },
  ] : [];
  const actions = (
    <>
      <span
        title={
          status === "live"
            ? [t("terminal-full.live_hint"), grid].filter(Boolean).join("\n")
            : v?.note || STATUS_LABEL[status]
        }
        className="hidden shrink-0 items-center gap-1.5 whitespace-nowrap text-[11.5px] text-ink-3 @md:inline-flex"
      >
        <span aria-hidden className={cx("size-1.5 rounded-full", DOT[status])} />
        <span>{STATUS_LABEL[status]}</span>
      </span>
      {v?.elsewhere && (
        <button
          type="button"
          title={t("terminal-full.elsewhere_hint")}
          onClick={() => feed?.followTo(v.elsewhere!)}
          className="inline-flex h-6 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-chip px-2 text-[11.5px] text-ink-2 transition-colors duration-150 hover:bg-hover hover:text-ink"
        >
          <span aria-hidden className="size-1.5 rounded-full bg-orange" />
          {t("terminal-full.elsewhere")}
        </button>
      )}
      {compact ? (
        <PaneActions
          mid={mid}
          sid={sid}
          fullSupported
          onSplit={() => {}}
          compact
          extraItems={compactMenuItems}
          onButton={(el) => { overflowAnchor.current = el; }}
        />
      ) : (
        <>
          {onFiles && cwd && <IconButton icon="folder" label={t("terminal-full.files")} onClick={onFiles} className="max-lg:hidden" />}
          <IconButton icon="keyboard" label={keyBar ? t("terminal-full.keybar_hide") : t("terminal-full.keybar_show")} active={keyBar} onClick={() => chooseKeyBar(!keyBar)} />
          <IconButton
            icon="gear"
            label={t("terminal-full.settings")}
            active={Boolean(options)}
            onClick={(e) => {
              const target = e.currentTarget;
              setOptions((o) => (o ? null : target));
            }}
          />
          <IconButton icon="retry" label={t("terminal-full.reconnect")} onClick={() => feed?.reconnect()} disabled={!feed} />
        </>
      )}
    </>
  );

  return (
    <div
      ref={root}
      onKeyDownCapture={onKeyDownCapture}
      data-herdr-full=""
      onClick={(e) => !(e.target as Element).closest('[role="search"]') && setTimeout(refocus, 0)}
      tabIndex={-1}
      className="flex min-h-0 flex-1 flex-col outline-none"
    >
      {slot && on && createPortal(actions, slot)}
      <div className="relative flex min-h-0 flex-1 flex-col">
        <div
          ref={viewport}
          onContextMenu={onContextMenu}
          data-terminal-scheme={scheme}
          className={cx("relative min-h-0 flex-1 overflow-hidden p-2", scheme && "bg-page", v?.dropping && "ring-2 ring-inset ring-accent")}
        >
          <div ref={host} className="h-full w-full font-mono" />
        </div>
        {feed && v && find.open && (
          <FindBar
            feed={feed}
            v={v}
            state={find}
            onQuery={(query) => dispatchFind({ type: "query", query })}
            onToggleCase={() => dispatchFind({ type: "toggleCase" })}
            onClose={closeFind}
          />
        )}
        {v?.uploading && (
          <div className="pointer-events-none absolute inset-x-2 top-2 z-10 flex flex-col gap-1.5">
            <UploadStatus text={v.uploading} />
          </div>
        )}
        {(!v || !v.ready || status === "connecting") && (
          <div className="pointer-events-none absolute inset-0 flex items-center justify-center">
            <Loading label={t("terminal-full.opening")} />
          </div>
        )}
        {v && v.ready && (canReconnect(status) || status === "unsupported" || status === "forbidden") && (
          <div className="absolute inset-x-0 bottom-4 z-10 flex justify-center px-3" style={{ animation: "fade-up 180ms ease-out both" }}>
            <div role="status" className="flex max-w-full items-center gap-2 rounded-control bg-surface py-1.5 pr-1.5 pl-2.5 text-[12px] text-ink-2 shadow-raised">
              <Icon name="info" size={13} className="shrink-0 text-ink-3" />
              <span className="min-w-0 flex-1 truncate">{v.note || STATUS_LABEL[status]}</span>
              {canReconnect(status) ? (
                <Button tone="primary" onClick={() => feed?.reconnect()}>
                  {t("terminal-full.reconnect")}
                </Button>
              ) : (
                <Button tone="quiet" onClick={onFallback}>
                  {t("terminal-full.back_to_pane")}
                </Button>
              )}
            </div>
          </div>
        )}
      </div>
      {feed && v && v.ready && keyBar && <KeyBar feed={feed} v={v} onEditShortcuts={() => setEditing(true)} />}
      {options && (
        <OptionsPopover anchor={options} label={t("terminal-full.settings")} onClose={() => setOptions(null)}>
          <AppearanceRow />
        </OptionsPopover>
      )}
      {menu && (
        <Menu
          items={menuItems()}
          anchor={null}
          point={{ x: menu.x, y: menu.y }}
          onClose={() => {
            setMenu(null);
            setTimeout(refocus, 0);
          }}
          width={176}
        />
      )}
      <ShortcutsEditor
        open={editing}
        onClose={() => {
          setEditing(false);
          feed?.focus();
        }}
      />
    </div>
  );
}
