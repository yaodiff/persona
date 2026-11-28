// Shared launcher-first model column (#10249). Selection and setup controls stay in Row and Flyout.
import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useReducer, useRef, useState, type CSSProperties, type KeyboardEvent, type ReactNode } from "react";
import { createPortal } from "react-dom";
import "./ModelPanel.css";
import { t } from "../../i18n";
import { afterBack, useCloseOnBack } from "../../lib/back-close";
import {
  cleanSetup,
  engineChoices,
  engineSetup,
  levelSetup,
  panelSections,
  rowBlock,
  rowCustomizable,
  type PanelConfig,
  type PanelPreset,
  type PanelOverride,
  type PanelProvider,
  type PanelRow,
  type PanelScope,
  type PanelSection,
  type PanelState,
} from "../../lib/model-panel";
import { launcherSections, launcherSearch, quickPicks } from "../../lib/launcher-column";
import { navigate } from "../../lib/router";
import type { LauncherQuotaAnswer } from "../../lib/quota";
import { hasFinePointer, keyboardFrame } from "../../lib/viewport";
import { settingsHref } from "../../views/settings/nav";
import { Button, cx, Spinner } from "../atoms";
import { Icon } from "../icons";
import { NARROW_QUERY, returnFocus, SHEET_QUERY, useMedia } from "../overlay";
import { LauncherColumn, LevelColumn, SAVED_SIDE, SpeedSwitch } from "./Columns";
import { Flyout, type FlyoutMode } from "./Flyout";
import { LauncherRow } from "./LauncherRow";
import { Row } from "./Row";
import { usePanelStore } from "./store";

export interface ModelPanelProps {
  /** The element it opens from (the pill); the panel opens above it, right edges aligned. */
  anchor: HTMLElement;
  onClose: () => void;
  providers: PanelProvider[];
  state: PanelState;
  scope: PanelScope;
  /** Applies a whole setup (a row, a favorite, a recent entry); true when it was applied: the panel then records it in
   *  Recent and closes. false: the caller has said why; the panel stays open with the old setup. */
  onPick: (config: PanelConfig) => Promise<boolean>;
  /** A live effort or speed edit on the current model keeps the panel open. */
  onApplyCurrent?: (patch: PanelOverride) => Promise<boolean>;
  /** Refreshes a provider's catalog (also used for Retry). */
  onReload?: (provider: string) => void;
  /** Forgets a typed model ID (#1644). */
  onForget?: (provider: string, model: string) => void;
  /** Drawn inside the flyout of the model that runs now, under its controls (Claude Code's ultracode switch, #3428). */
  currentExtra?: ReactNode;
  /** Resets an explicit choice in currentExtra when the current setup is reset. */
  currentExtraReset?: () => void;
  /** The launcher's existing quota answer, read by the chat for this computer; unknown providers have no entry. */
  quotas?: Readonly<Record<string, LauncherQuotaAnswer>>;
  /** A strip above the search (a Persona chat's "Following the default" / "Picked for this chat", #5033). */
  header?: ReactNode;
  /** A row on top of the list, in no group (Cindy's "follow" row): following a default instead of a model of one's own
   *  (New Agent: Model settings › New agent). Checked while `active`; a click applies it, and the panel closes once it did. */
  follow?: {
    active: boolean;
    label: string;
    detail?: string;
    warning?: string;
    onFollow: () => boolean | Promise<boolean>;
  };
  /** Ready-made setups listed as a group after the favorites (New Agent's presets); a pick of one goes to `onPreset`. */
  presets?: PanelPreset[];
  onPreset?: (key: string) => boolean | Promise<boolean>;
  /** The flyout lists the other engines its model runs with here (other providers listing it) and picks one on a click. */
  engines?: boolean;
  /** "How to fix" after the reason under a provider that can't take a pick (New Agent's launcher that failed its check);
   *  the panel closes first. */
  onHelp?: (provider: string) => void;
  /** "Configure models" instead of opening Settings › Model providers from here (a dialog that closes first). */
  onConfigure?: () => void;
  /** A dialog the caller opened about the pick on its way is above the panel: the panel sits under its backdrop and
   *  doesn't close on its clicks, focus or Escape (they are the dialog's). */
  held?: boolean;
}

/** The body's height (rail and list) when the window has room; the panel's own cap (Cindy's max-h). */
const BODY = 428;
const BODY_MIN = 160;
const PANEL_MAX = 560;
const GAP = 6;
const EDGE = 8;
/** Wide enough for three columns (launchers, models, thinking levels, #12315); narrower keeps one launcher-first column. */
const COLUMNS_QUERY = "(min-width: 768px)";
/** The columns' body height: their rows are one line each, so the same list needs less room. */
const COLUMNS_BODY = 352;
/** A star just saved stays filled this long on the row it was saved from. */
const STAR_FLASH_MS = 700;

interface Geometry {
  left: number;
  top: number;
  body: number;
  /** The first placement's clip from the pill's shape (the opening animation); set once. */
  from?: string;
  shift?: number;
}

const rowsOf = (list: HTMLElement | null) => (list ? [...list.querySelectorAll<HTMLElement>("[data-panel-row], [data-panel-launcher]")] : []);

export function ModelPanel({ anchor, onClose, providers, state, scope, onPick, onApplyCurrent, onReload, onForget, quotas, currentExtra, currentExtraReset, header, follow, presets, onPreset, engines, onHelp, onConfigure, held }: ModelPanelProps): ReactNode {
  const store = usePanelStore(scope);
  const listId = useId();
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set(state.current ? [state.current.provider] : []));
  const [query, setQuery] = useState("");
  // families unfolded during this opening ("Earlier versions"); a new opening starts folded again
  const [openFamilies, setOpenFamilies] = useState<ReadonlySet<string>>(() => new Set());
  const searching = query.trim() !== "";
  // a wide screen draws three columns: the launcher chosen on the left lists its models in the middle
  const columns = useMedia(COLUMNS_QUERY);
  const [side, setSide] = useState<string>(() => state.current?.provider ?? providers[0]?.id ?? SAVED_SIDE);
  // the model whose levels and extras the right column and the footer show: the one picked here, else the one that runs
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const input = useMemo(
    () => ({
      providers,
      state,
      query,
      favorites: store.favorites,
      recent: store.recent,
      overrides: store.overrides,
      presets,
      openFamilies,
    }),
    [providers, state, query, store.favorites, store.recent, store.overrides, presets, openFamilies],
  );
  const sections = useMemo(() => launcherSections(input), [input]);
  const quick = useMemo(() => quickPicks(input), [input]);
  const found = useMemo(() => (searching ? launcherSearch(input) : []), [input, searching]);
  const presetSections = useMemo(() => panelSections({ ...input, view: { kind: "all" } }).filter((s) => s.kind === "presets"), [input]);
  const matches = providers.filter((p) =>
    query
      .trim()
      .toLowerCase()
      .split(/\s+/)
      .every((word) => [p.name, p.id, p.engineLabel].some((value) => value?.toLowerCase().includes(word))),
  );
  const toggleLauncher = (id: string) => {
    setFly(null);
    setExpanded((now) => {
      const next = new Set(now);
      if (!next.delete(id)) next.add(id);
      return next;
    });
  };
  const toggleFamily = (key: string) =>
    setOpenFamilies((now) => {
      const next = new Set(now);
      if (!next.delete(key)) next.add(key);
      return next;
    });
  const sideSection = columns && side !== SAVED_SIDE ? sections.find((s) => s.provider?.id === side) : undefined;
  const rows = useMemo(
    () =>
      searching
        ? found
        : columns
          ? side === SAVED_SIDE
            ? [...quick, ...presetSections.flatMap((s) => s.rows)]
            : (sideSection?.rows ?? [])
          : [...quick, ...presetSections.flatMap((s) => s.rows), ...sections.filter((s) => expanded.has(s.provider!.id)).flatMap((s) => s.rows)],
    [searching, found, quick, presetSections, sections, expanded, columns, side, sideSection],
  );
  const levelRow = columns ? (rows.find((r) => r.key === selectedKey) ?? rows.find((r) => r.current)) : undefined;

  const panel = useRef<HTMLDivElement>(null);
  const body = useRef<HTMLDivElement>(null);
  const list = useRef<HTMLDivElement>(null);
  const search = useRef<HTMLInputElement>(null);
  const glide = useRef<HTMLDivElement>(null);
  const live = Boolean(onApplyCurrent);
  const phone = useMedia(SHEET_QUERY);
  const narrow = useMedia(NARROW_QUERY);

  // a pick on its way (everything waits); a flyout edit of the model that runs now on its way (its new value shows)
  const [picking, setPicking] = useState(false);
  const [applying, setApplying] = useState<{
    key: string;
    patch: PanelOverride;
  } | null>(null);
  const [fly, setFly] = useState<{
    key: string;
    el: HTMLElement;
    keyboard: boolean;
  } | null>(null);
  const [flash, setFlash] = useState("");
  const closeFly = useCallback(() => setFly(null), []);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const flyRow = fly ? rows.find((r) => r.key === fly.key) : undefined;
  // a row that went (a search, another view, a favorite removed) takes its flyout with it; so does a dialog over the panel
  useEffect(() => {
    if (fly && (held || !flyRow || !fly.el.isConnected)) setFly(null);
  }, [fly, flyRow, held]);

  useEffect(() => {
    if (!flash) return;
    const timer = setTimeout(() => setFlash(""), STAR_FLASH_MS);
    return () => clearTimeout(timer);
  }, [flash]);

  // ── where it sits ──
  const [geo, setGeo] = useState<Geometry | null>(null);
  const [width, setWidth] = useState(0);
  // placed again after every render; this asks for one when only the window moved
  const [, replace] = useReducer((n: number) => n + 1, 0);
  useLayoutEffect(() => {
    const el = panel.current;
    const b = body.current;
    if (!el || !b) return;
    // as wide as it has been: a search or another view never makes it narrower while it is open
    if (el.offsetWidth > width) setWidth(el.offsetWidth);
    const a = anchor.getBoundingClientRect();
    const frame = narrow ? keyboardFrame(innerHeight, window.visualViewport) : undefined;
    const lo = (frame?.top ?? 0) + EDGE;
    const hi = frame ? frame.top + frame.height - EDGE : innerHeight - EDGE;
    const chrome = el.offsetHeight - b.offsetHeight;
    const natural = chrome + (columns ? COLUMNS_BODY : BODY);
    const cap = Math.min(PANEL_MAX, innerHeight - 120);
    const above = a.top - GAP - lo;
    const below = hi - (a.bottom + GAP);
    const up = above >= Math.min(natural, cap) || above >= below;
    const room = up ? above : below;
    const bodyH = Math.max(BODY_MIN, Math.min(natural, Math.max(cap, BODY_MIN + chrome), room) - chrome);
    const h = chrome + bodyH;
    const w = Math.max(el.offsetWidth, width);
    const left = Math.max(EDGE, Math.min(a.right - w, innerWidth - w - EDGE));
    let top = up ? a.top - GAP - h : a.bottom + GAP;
    top = Math.max(lo, Math.min(top, hi - h));
    setGeo((was) => {
      if (was && was.left === left && was.top === top && was.body === bodyH) return was;
      if (was) return { ...was, left, top, body: bodyH };
      // the first placement: the opening grows out of the pill (its shape at the panel's edge nearest to it)
      const shift = up ? a.bottom - (top + h) : a.top - top;
      const inset = (v: number, max: number) => Math.max(0, Math.min(max, Math.round(v)));
      const it = inset(a.top - (top + shift), h);
      const ib = inset(top + shift + h - a.bottom, h - it);
      const il = inset(a.left - left, w);
      const ir = inset(left + w - a.right, w - il);
      return {
        left,
        top,
        body: bodyH,
        shift,
        from: `inset(${it}px ${ir}px ${ib}px ${il}px round ${Math.round(a.height / 2)}px)`,
      };
    });
  });

  // the keyboard sliding in on a phone, a window that changes height: placed again; one that changes width: closed
  useEffect(() => {
    let w = innerWidth;
    const resize = () => {
      if (innerWidth !== w) {
        w = innerWidth;
        onClose();
      } else replace();
    };
    const vv = window.visualViewport;
    const again = () => replace();
    addEventListener("resize", resize);
    vv?.addEventListener("resize", again);
    vv?.addEventListener("scroll", again);
    return () => {
      removeEventListener("resize", resize);
      vv?.removeEventListener("resize", again);
      vv?.removeEventListener("scroll", again);
    };
  }, [onClose]);

  // ── closing from outside ──
  const latest = useRef({ fly, onClose, held });
  latest.current = { fly, onClose, held };
  // On a narrow screen Back closes it, its flyout with it, and stays where it opened
  useCloseOnBack(true, onClose, narrow);
  // Going elsewhere from it (Configure models, a provider's How to fix) waits for its entry to come off first: the caller
  // may also close a sheet under it and navigate, which would otherwise leave the sheet's entry under the new page.
  const leave = (go: () => void) => {
    onClose();
    if (narrow) afterBack(go);
    else go();
  };
  const help = onHelp && ((provider: string) => leave(() => onHelp(provider)));
  useEffect(() => {
    const inside = (n: Node | null) => Boolean(n && (panel.current?.contains(n) || anchor.contains(n) || (n instanceof Element && n.closest("[data-model-flyout]"))));
    const down = (e: PointerEvent) => {
      if (!latest.current.held && !inside(e.target as Node | null)) latest.current.onClose();
    };
    const focus = (e: FocusEvent) => {
      if (!latest.current.held && !inside(e.target as Node | null)) latest.current.onClose();
    };
    const key = (e: globalThis.KeyboardEvent) => {
      if (latest.current.held || e.key !== "Escape" || e.isComposing || e.keyCode === 229) return;
      e.preventDefault();
      e.stopPropagation();
      const { fly: open, onClose: close } = latest.current;
      if (open) {
        setFly(null);
        if (open.el.isConnected) open.el.focus({ preventScroll: true });
        return;
      }
      close();
      if (panel.current) returnFocus(panel.current, anchor);
    };
    addEventListener("pointerdown", down, true);
    document.addEventListener("focusin", focus);
    addEventListener("keydown", key, true);
    return () => {
      removeEventListener("pointerdown", down, true);
      document.removeEventListener("focusin", focus);
      removeEventListener("keydown", key, true);
    };
  }, [anchor]);

  // the search has the focus once it shows (a touch screen keeps its keyboard down until the search is tapped)
  const placed = Boolean(geo);
  useEffect(() => {
    if (placed && hasFinePointer()) search.current?.focus({ preventScroll: true });
  }, [placed]);

  // Keep the follow row and launcher summaries visible on opening; a new search starts at the top.
  const viewKey = query.trim().toLowerCase();
  useLayoutEffect(() => {
    if (list.current) list.current.scrollTop = 0;
  }, [viewKey]);

  // ── the hover that glides from row to row ──
  const hot = useRef<HTMLElement | null>(null);
  const glideTo = (row: HTMLElement | null) => {
    const g = glide.current;
    if (!g) return;
    if (hot.current && hot.current !== row) delete hot.current.dataset.glide;
    hot.current = row;
    if (!row) {
      g.style.opacity = "0";
      return;
    }
    row.dataset.glide = "";
    const appearing = g.style.opacity !== "1";
    // it appears where it is wanted and glides only between rows
    if (appearing) g.style.transition = "none";
    g.style.transform = `translate(${row.offsetLeft}px, ${row.offsetTop}px)`;
    g.style.width = `${row.offsetWidth}px`;
    g.style.height = `${row.offsetHeight}px`;
    g.style.opacity = "1";
    if (appearing) {
      void g.offsetHeight;
      g.style.transition = "";
    }
  };
  const restingRow = (): HTMLElement | null => {
    if (fly?.el.isConnected) return fly.el;
    const a = document.activeElement;
    return a instanceof HTMLElement && a.matches("[data-panel-row]:focus-visible") ? a : null;
  };
  // rows drawn again (a list read, a star) keep their glide where it was
  useLayoutEffect(() => {
    const h = hot.current;
    if (h && !h.isConnected) glideTo(null);
    else if (h) glideTo(h);
    else if (fly?.el.isConnected) glideTo(fly.el);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- follows the rows and the flyout
  }, [sections, fly]);

  // ── what a row does ──
  // a pick of a row, a preset, an engine in a flyout or the follow row: everything waits for it; once applied, the setup
  // (not the follow row's: it is no model of its own) goes into Recent and the panel closes. The columns stay open for
  // the effort and the speed after a model (#12315): they close from outside, and Recent takes the last setup applied.
  const lastApplied = useRef<PanelConfig | null>(null);
  const pushRecent = useRef(store.pushRecent);
  pushRecent.current = store.pushRecent;
  useEffect(
    () => () => {
      if (lastApplied.current) pushRecent.current(lastApplied.current);
    },
    [],
  );
  const apply = async (run: () => boolean | Promise<boolean>, setup: PanelConfig | null): Promise<boolean> => {
    if (picking) return false;
    setPicking(true);
    let ok = false;
    try {
      ok = await run();
    } catch {
      ok = false;
    }
    if (ok && setup) {
      if (columns) lastApplied.current = setup;
      else store.pushRecent(setup);
    }
    if (!mounted.current) return ok;
    setPicking(false);
    if (ok && !columns) onClose();
    return ok;
  };
  const pick = (row: PanelRow) => {
    if (columns) setSelectedKey(row.key);
    return apply(() => (row.kind === "preset" && row.preset && onPreset ? onPreset(row.preset.key) : onPick(row.setup)), row.setup);
  };

  const star = (row: PanelRow) => {
    if (row.kind === "favorite" && row.favorite) store.removeFavorite(row.favorite.uid);
    else if (row.kind === "recent" && row.starredUid) store.removeFavorite(row.starredUid);
    else {
      store.addFavorite(row.setup);
      setFlash(row.key);
    }
  };

  const applyLive = async (row: PanelRow, patch: PanelOverride): Promise<boolean> => {
    if (!onApplyCurrent) return false;
    setApplying({ key: row.key, patch });
    let ok = false;
    try {
      ok = await onApplyCurrent(patch);
    } catch {
      ok = false;
    }
    if (mounted.current) setApplying(null);
    return ok;
  };

  const edit = async (row: PanelRow, patch: PanelOverride): Promise<boolean> => {
    if (row.kind === "favorite" && row.favorite) {
      store.updateFavorite(row.favorite.uid, patch);
      return true;
    }
    if (row.kind !== "model") return false;
    const variants = row.model.variants;
    if (variants && patch.effort !== undefined) {
      // a model listed per level: its effort is another model ID. The one that runs now switches to it (or picks itself
      // back to drop a pending switch), as a pick does; any other row stands for the level chosen, and the list's first
      // needs no override
      if (row.current) {
        const setup = levelSetup(row, patch.effort);
        if (!setup || (setup.model === row.model.id && !state.pending)) return false;
        const ok = await apply(() => onPick(setup), setup);
        if (ok) store.setOverride(row.provider.id, variants.base, { effort: patch.effort === variants.first ? undefined : patch.effort });
        return ok;
      }
      store.setOverride(row.provider.id, variants.base, { effort: patch.effort === variants.first ? undefined : patch.effort });
      return true;
    }
    if (row.current && live) {
      if (!(await applyLive(row, patch))) return false;
      if (columns) lastApplied.current = cleanSetup({ ...row.setup, ...patch });
    }
    store.setOverride(row.provider.id, variants?.base ?? row.model.id, patch);
    return !row.current || live;
  };

  const reset = async (row: PanelRow) => {
    if (row.kind !== "model") return;
    // a folded row's level comes back to the list's first; the one that runs keeps running until another is picked
    if (row.model.variants) return store.resetOverride(row.provider.id, row.model.variants.base);
    if (row.current && live) {
      const patch: PanelOverride = {};
      // An agy or codex start uses its configured default when the request omits effort.
      const d = state.starting && (row.provider.engine === "agy" || row.provider.engine === "codex") ? undefined : row.model.defaultEffort;
      if (d && row.setup.effort && row.setup.effort !== d) patch.effort = d;
      // no default known: back to none, the model's own (an Agent not started yet; a running one sends nothing for it)
      else if (!d && row.setup.effort) patch.effort = "";
      if (row.setup.speed && row.setup.speed !== "standard" && row.model.speeds?.some((choice) => choice.id === "standard")) patch.speed = "standard";
      if (Object.keys(patch).length && !(await applyLive(row, patch))) return;
      currentExtraReset?.();
    }
    store.resetOverride(row.provider.id, row.model.id);
  };

  // the right column: a level of the model that runs applies at once as its flyout's slider does; any other model is
  // picked with that level (a model listed per level: that level's own model). Either way the level is kept as the
  // one this model opens at next time.
  const chooseLevel = async (row: PanelRow, effort: string) => {
    if (row.current && row.kind === "model") {
      await edit(row, { effort });
      return;
    }
    const setup = row.model.variants ? levelSetup(row, effort) : cleanSetup({ ...row.setup, effort });
    if (!setup) return;
    setSelectedKey(row.key);
    const ok = await apply(() => onPick(setup), setup);
    if (ok && row.kind === "model") {
      const variants = row.model.variants;
      if (variants) store.setOverride(row.provider.id, variants.base, { effort: effort === variants.first ? undefined : effort });
      else store.setOverride(row.provider.id, row.model.id, { effort });
    }
  };
  const levelOff = (row: PanelRow | undefined) =>
    picking || Boolean(applying) || !row || row.kind === "preset" || row.kind === "typed" || (Boolean(rowBlock(row.provider, state, row.current)) && !(row.current && rowCustomizable(row, state, live)));
  const chooseSide = (id: string) => {
    setFly(null);
    setSelectedKey(null);
    setQuery("");
    setSide(id);
  };

  const openFlyout = (row: PanelRow, el: HTMLElement, toggle: boolean, keyboard: boolean) => {
    if (toggle && fly?.key === row.key) return setFly(null);
    setFly({ key: row.key, el, keyboard });
  };

  // ── keys ──
  const focusRow = (to: HTMLElement | undefined) => {
    if (!to) return;
    to.focus({ preventScroll: true });
    to.scrollIntoView?.({ block: "nearest" });
    glideTo(to.getAttribute("aria-disabled") ? null : to);
  };
  const searchKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.nativeEvent.isComposing || e.keyCode === 229) return;
    const all = rowsOf(list.current);
    if (e.key === "ArrowDown" || (e.key === "Tab" && !e.shiftKey)) {
      if (!all.length) return;
      e.preventDefault();
      focusRow(all.find((r) => r.hasAttribute("data-current")) ?? all[0]);
    } else if (e.key === "Enter" && searching) {
      // a search and Enter: the first row that can be picked (a typed ID's "Use …" too)
      e.preventDefault();
      const first = rows.find((r) => !rowBlock(r.provider, state, r.current));
      if (first) void pick(first);
    }
  };
  const listKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    const all = rowsOf(list.current);
    const at = all.indexOf(e.target as HTMLElement);
    if (at < 0) return;
    e.preventDefault();
    if (e.key === "ArrowUp" && at === 0) return search.current?.focus();
    focusRow(all[Math.max(0, Math.min(all.length - 1, at + (e.key === "ArrowDown" ? 1 : -1)))]);
  };

  const second = (row: PanelRow): { text: string; describes?: boolean } => {
    const name = row.provider.name;
    if (row.kind === "typed") return { text: `${name} · ${t("ui-modelpanel.use_typed_note")}` };
    if (row.kind === "preset") return { text: `${name} · ${row.preset?.description || row.model.name}` };
    if (row.model.custom) return { text: `${name} · ${t("ui-modelpanel.typed_before")}` };
    if (!searching && !columns && row.kind === "model" && providers.some((p) => p.id !== row.provider.id && p.models?.some((m) => m.name === row.model.name))) return { text: name };
    if (!searching && row.kind === "model") return { text: row.model.description ?? "", describes: true };
    return { text: name };
  };

  const renderRow = (row: PanelRow) => {
    const customizable = rowCustomizable(row, state, live);
    const removes = row.kind === "favorite" || (row.kind === "recent" && Boolean(row.starredUid));
    const two = second(row);
    return (
      <Row
        key={row.key}
        row={row}
        asButton
        dense={columns}
        second={two.text}
        keepSecond={searching || row.kind === "recent" || row.kind === "favorite"}
        describes={two.describes || row.kind === "preset"}
        blocked={rowBlock(row.provider, state, row.current)}
        customizable={customizable}
        flyoutOpen={fly?.key === row.key}
        starred={removes || flash === row.key}
        starRemoves={removes}
        frozen={picking}
        onPick={() => void pick(row)}
        onStar={() => star(row)}
        onCustomize={(el, toggle) => openFlyout(row, el, toggle, false)}
        onCustomizeKey={(el) => openFlyout(row, el, false, true)}
        onForget={onForget && (() => onForget(row.provider.id, row.model.id))}
      />
    );
  };
  // why the launcher chosen in the left column can't take a pick, its check's status or its list's error: said once,
  // above its models (the left column says it only in a tooltip)
  const sideNote = (p: PanelProvider) => {
    const why = p.unavailable || p.locked;
    const status = p.status && p.status !== why ? p.status : undefined;
    if (!why && !status && !p.error) return null;
    return (
      <div role="note" data-panel-side-note className="flex w-0 min-w-full items-start gap-1.5 px-2.5 pt-1.5 pb-2 text-submeta leading-[1.5] text-ink-3">
        <Icon name="info" size={12} className="mt-[2.5px] shrink-0" />
        <div className="min-w-0 break-words">
          {why && (
            <div>
              {why}{" "}
              {p.unavailable && help && (
                <button type="button" disabled={picking} onClick={() => help(p.id)} className="text-accent-ink hover:underline">
                  {t("ui-modelpanel.how_to_fix")}
                </button>
              )}
            </div>
          )}
          {status && <div>{status}</div>}
          {p.error && (
            <div role="alert">
              {p.error}{" "}
              {onReload && (
                <button type="button" disabled={picking} onClick={() => onReload(p.id)} className="text-accent-ink hover:underline">
                  {t("ui-modelpanel.retry")}
                </button>
              )}
            </div>
          )}
        </div>
      </div>
    );
  };

  const catalogActions = (s: PanelSection) => {
    const p = s.provider!;
    const loading = s.loading || p.loading;
    const refresh = onReload && p.models !== null && !p.error && !p.unavailable && !p.locked;
    if (!loading && !refresh) return null;
    return (
      <div className="flex items-center justify-end px-2">
        {loading && (
          <div role="status" className="mr-auto flex items-center gap-2 px-1 py-2 text-label text-ink-3">
            <Spinner size={12} />
            {t("ui-modelpanel.loading")}
          </div>
        )}
        {refresh && (
          <Button
            tone="quiet"
            size="sm"
            icon="retry"
            className="touch-hit"
            aria-label={t("ui-modelpanel.refresh_provider_models", { provider: p.name })}
            disabled={picking || loading}
            onClick={() => onReload(p.id)}
          >
            {t("ui-modelpanel.refresh_models")}
          </Button>
        )}
      </div>
    );
  };

  // a provider's rows by family (lib/model-families.ts): a family's header and its fold, then its rows;
  // trailing unclassified/custom rows sit under a localized "Other" header so they never appear under the last family
  const renderBlocks = (s: PanelSection) => {
    if (!s.blocks) return s.rows.map(renderRow);
    const hasFamilies = s.blocks.some((b) => Boolean(b.family));
    return s.blocks.map((b) => {
      const heading = b.family?.label ?? (b.other && hasFamilies ? t("ui-modelpanel.other") : undefined);
      return heading ? (
        <div key={b.key} role="group" aria-label={heading} data-panel-family={b.family?.key ?? "other"}>
          <div className="flex min-w-0 items-center gap-2 px-3 pt-1.5 pb-0.5">
            <span aria-hidden className="min-w-0 truncate text-submeta leading-[1.4] text-ink-3">
              {heading}
            </span>
            {b.family && b.older > 0 && (
              <button
                type="button"
                aria-expanded={Boolean(b.open)}
                aria-label={
                  b.open
                    ? t("ui-modelpanel.hide_older_of", {
                        family: b.family.label,
                      })
                    : t("ui-modelpanel.older_of", {
                        family: b.family.label,
                        count: b.older,
                      })
                }
                data-panel-fold={b.key}
                disabled={picking}
                onClick={() => toggleFamily(b.key)}
                className="ml-auto flex shrink-0 items-center gap-0.5 rounded-control px-1.5 py-0.5 text-submeta leading-[1.4] whitespace-nowrap text-ink-3 transition-colors duration-150 outline-none enabled:hover:bg-hover-2 enabled:hover:text-ink focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed"
              >
                {b.open
                  ? t("ui-modelpanel.hide_older")
                  : t("ui-modelpanel.older", {
                      count: b.older,
                    })}
                <Icon name="chevron" size={11} className={cx("shrink-0 transition-transform duration-150", b.open && "rotate-180")} />
              </button>
            )}
          </div>
          {b.rows.map(renderRow)}
        </div>
      ) : (
        <div key={b.key}>{b.rows.map(renderRow)}</div>
      );
    });
  };
  const flyMode = (row: PanelRow): FlyoutMode => {
    if (row.kind === "favorite") return "favorite";
    return row.customized || (row.kind === "model" && row.current && currentExtraReset) ? "customized" : "recommended";
  };
  const loading = sections.some((s) => s.loading);
  const empty = searching ? !found.length && !matches.length && !loading : !sections.length;
  const notes = [
    state.busy && {
      icon: "info" as const,
      text: state.busy,
      tone: "text-ink-3",
    },
    state.note && {
      icon: "warning" as const,
      text: state.note,
      tone: "text-orange",
    },
  ].filter(Boolean) as {
    icon: "info" | "warning";
    text: string;
    tone: string;
  }[];

  const room = phone ? "calc(100vw - 16px)" : "calc(100vw - 48px)";
  const style: CSSProperties & Record<string, string | number | undefined> = {
    left: geo?.left ?? 0,
    top: geo?.top ?? 0,
    // three columns: as wide as they need, whatever the list holds; one column: as wide as it has been
    ...(columns ? { width: `min(640px, ${room})` } : { minWidth: width ? `max(300px, min(${width}px, ${room}))` : undefined, maxWidth: `min(460px, ${room})` }),
    visibility: geo ? undefined : "hidden",
    "--model-panel-from": geo?.from,
    "--model-panel-shift": geo?.shift === undefined ? undefined : `${geo.shift}px`,
    animation: geo ? "model-panel-morph var(--model-panel-morph) var(--model-panel-ease) backwards" : undefined,
  };

  return createPortal(
    <>
      {/* under a dialog's backdrop (z-40) while one asks about the pick, above the page otherwise */}
      <div
        ref={panel}
        role="dialog"
        aria-label={t("ui-modelpanel.list")}
        data-model-panel
        data-held={held || undefined}
        className={cx("fixed flex w-max min-w-[300px] flex-col overflow-hidden rounded-[12px] bg-surface text-ink shadow-overlay", held ? "z-30" : "z-50")}
        style={style}
      >
        <div
          className="flex min-h-0 flex-col"
          style={{
            animation: geo ? "model-panel-content var(--model-panel-fade) var(--model-panel-ease) var(--model-panel-fade-delay) backwards" : undefined,
          }}
        >
          {header && <div className="shrink-0">{header}</div>}
          <div className="flex shrink-0 items-center gap-2 border-b border-line px-3.5 py-3 text-ink-3">
            <Icon name="search" size={14} className="shrink-0" />
            <input
              ref={search}
              type="text"
              value={query}
              onChange={(e) => {
                setFly(null);
                setQuery(e.target.value);
              }}
              onKeyDown={searchKey}
              placeholder={t("ui-modelpanel.search")}
              aria-label={t("ui-modelpanel.search")}
              aria-controls={listId}
              spellCheck={false}
              autoComplete="off"
              className="min-w-0 flex-1 bg-transparent text-[13px] text-ink outline-none placeholder:text-ink-3"
            />
          </div>
          <div ref={body} className="flex min-h-0 min-w-0 shrink" style={{ height: geo?.body ?? BODY }}>
            {columns && <LauncherColumn providers={providers} state={state} quotas={quotas} side={side} saved={quick.length > 0 || presetSections.length > 0} frozen={picking} onSide={chooseSide} />}
            <div className="relative flex min-h-0 min-w-0 flex-1 flex-col">
              <div
                ref={list}
                id={listId}
                role="group"
                aria-label={t("ui-modelpanel.list")}
                aria-busy={picking || undefined}
                onKeyDown={listKey}
                onPointerMove={(e) => {
                  const row = (e.target as Element).closest?.<HTMLElement>("[data-panel-row]");
                  if (row && row === hot.current) return;
                  if (row && !row.getAttribute("aria-disabled")) glideTo(row);
                  else if (!row) glideTo(restingRow());
                }}
                onPointerLeave={() => glideTo(restingRow())}
                onFocus={(e) => {
                  const row = e.target as HTMLElement;
                  if (row.matches?.("[data-panel-row]:focus-visible") && !row.getAttribute("aria-disabled")) glideTo(row);
                }}
                className={cx("relative flex min-h-0 min-w-0 flex-1 flex-col overflow-y-auto overscroll-contain [scrollbar-gutter:stable]", columns ? "p-1.5" : "p-2 pb-3")}
              >
                <div ref={glide} aria-hidden className="pointer-events-none absolute top-0 left-0 z-0 rounded-control bg-hover-2 opacity-0 transition-[transform,height,opacity] duration-(--model-panel-glide) ease-out" />
                {follow && <FollowRow follow={follow} frozen={picking} onPick={() => void apply(follow.onFollow, null)} />}
                {notes.map((n) => (
                  <div key={n.icon} role="note" className={cx("flex w-0 min-w-full items-start gap-1.5 px-2.5 pt-1.5 pb-1 text-[11px] leading-[1.5]", n.tone)}>
                    <Icon name={n.icon} size={12} className="mt-[2.5px] shrink-0" />
                    <span className="min-w-0 break-words">{n.text}</span>
                  </div>
                ))}
                {columns ? (
                  searching ? (
                    <>
                      {found.map(renderRow)}
                      {loading && <div role="status" className="flex items-center gap-2 px-3 py-2 text-label text-ink-3"><Spinner size={12} />{t("ui-modelpanel.loading")}</div>}
                      {!found.length && !loading && <div className="w-0 min-w-full px-3 py-6 text-center text-meta text-ink-3">{t("ui-modelpanel.no_match")}</div>}
                    </>
                  ) : side === SAVED_SIDE ? (
                    <>
                      {quick.length > 0 && (
                        <div role="group" aria-label={t("ui-modelpanel.quick_picks")} data-panel-quick className="relative">
                          {quick.map(renderRow)}
                        </div>
                      )}
                      {presetSections.map((s) => (
                        <div key={s.key} role="group" aria-label={t("ui-modelpanel.presets")} data-panel-section="presets">
                          {s.rows.map(renderRow)}
                        </div>
                      ))}
                    </>
                  ) : sideSection ? (
                    <div role="group" aria-label={sideSection.provider!.name} data-panel-section="provider">
                      {sideNote(sideSection.provider!)}
                      {catalogActions(sideSection)}
                      {renderBlocks(sideSection)}
                    </div>
                  ) : null
                ) : empty ? (
                  <div className="w-0 min-w-full px-3 py-6 text-center text-[13px] text-ink-3">{t("ui-modelpanel.no_match")}</div>
                ) : (
                  <>
                    {!searching && quick.length > 0 && (
                      <div role="group" aria-label={t("ui-modelpanel.quick_picks")} data-panel-quick className="relative mb-1 border-b border-line pb-1">
                        {quick.map(renderRow)}
                      </div>
                    )}
                    {!searching &&
                      presetSections.map((s) => (
                        <div key={s.key} role="group" aria-label={t("ui-modelpanel.presets")} data-panel-section="presets">
                          {s.rows.map(renderRow)}
                        </div>
                      ))}
                    {searching ? (
                      <>
                        {matches.map((p) => (
                          <LauncherRow
                            key={p.id}
                            provider={p}
                            state={state}
                            quota={quotas?.[p.id]}
                            expanded={false}
                            frozen={picking}
                            onToggle={() => {
                              setQuery("");
                              setExpanded((now) => new Set([...now, p.id]));
                            }}
                            onReload={onReload}
                            onHelp={help}
                          />
                        ))}
                        {found.map(renderRow)}
                        {loading && <div role="status" className="flex items-center gap-2 px-3 py-2 text-label text-ink-3"><Spinner size={12} />{t("ui-modelpanel.loading")}</div>}
                      </>
                    ) : (
                      sections.map((s) => (
                        <div key={s.key} role="group" aria-label={s.provider!.name} data-panel-section="provider">
                          <LauncherRow
                            provider={s.provider!}
                            state={state}
                            quota={quotas?.[s.provider!.id]}
                            expanded={expanded.has(s.provider!.id)}
                            frozen={picking}
                            onToggle={() => toggleLauncher(s.provider!.id)}
                            onReload={onReload}
                            onHelp={help}
                          />
                          {expanded.has(s.provider!.id) && (
                            <div className="ml-2 border-l border-line pl-1">
                              {catalogActions(s)}
                              {renderBlocks(s)}
                            </div>
                          )}
                        </div>
                      ))
                    )}
                  </>
                )}
              </div>
            </div>
            {columns && <LevelColumn row={levelRow} disabled={levelOff(levelRow)} onLevel={(effort) => levelRow && void chooseLevel(levelRow, effort)} />}
          </div>
          <div className="flex shrink-0 items-center gap-2 border-t border-line px-3.5 py-[9px]">
            {columns && levelRow?.model.speeds && levelRow.kind !== "preset" && (
              <SpeedSwitch options={levelRow.model.speeds} speed={levelRow.setup.speed} disabled={picking || Boolean(applying) || !rowCustomizable(levelRow, state, live)} onSpeed={(speed) => void edit(levelRow, { speed })} />
            )}
            {columns && levelRow?.kind === "model" && levelRow.current && currentExtra && <div data-panel-extra className="min-w-0">{currentExtra}</div>}
            <button
              type="button"
              onClick={() => leave(() => {
                if (onConfigure) onConfigure();
                else navigate(settingsHref("models", null, phone, null, "providers"));
              })}
              className={cx("flex min-w-0 items-center gap-1.5 text-[13px] whitespace-nowrap text-ink-2 transition-colors duration-150 hover:text-ink", columns && "ml-auto")}
            >
              <Icon name="plus" size={14} className="shrink-0" />
              <span className="truncate">{t("ui-modelpanel.configure")}</span>
            </button>
          </div>
        </div>
      </div>
      {fly && flyRow && panel.current && (
        <Flyout
          rowEl={fly.el}
          panelEl={panel.current}
          row={flyRow}
          setup={applying?.key === flyRow.key && applying.patch.effort !== undefined ? { ...flyRow.setup, effort: applying.patch.effort } : flyRow.setup}
          mode={flyMode(flyRow)}
          starred={flyRow.kind === "favorite" || flash === flyRow.key}
          disabled={picking || Boolean(applying)}
          focusOnOpen={fly.keyboard}
          reposition={sections}
          extra={!columns && flyRow.kind === "model" && flyRow.current ? currentExtra : undefined}
          setupElsewhere={columns}
          engines={engines ? engineChoices(providers, flyRow, state) : undefined}
          onEngine={(choice) => {
            const setup = engineSetup(choice, flyRow.setup);
            void apply(() => onPick(setup), setup);
          }}
          onEffort={(effort) => void edit(flyRow, { effort })}
          onSpeed={(speed) => void edit(flyRow, { speed })}
          onReset={() => void reset(flyRow)}
          onStar={() => star(flyRow)}
          onClose={closeFly}
        />
      )}
    </>,
    document.body,
  );
}

/** The follow row (Cindy's "follow session" row): on top of the list in no group, its label and, under it, what the
 *  default runs; checked while it is followed. A row like the model rows for the keys (↑↓, Enter). */
function FollowRow({ follow, frozen, onPick }: { follow: NonNullable<ModelPanelProps["follow"]>; frozen: boolean; onPick: () => void }) {
  return (
    <>
      <div
        role="button"
        aria-pressed={follow.active}
        aria-disabled={frozen || undefined}
        tabIndex={0}
        data-panel-row="follow"
        data-follow-row
        // followed: the list opens at the top, where this row is (the panel scrolls to the first row marked current)
        data-current={follow.active || undefined}
        onClick={() => !frozen && onPick()}
        onKeyDown={(e) => {
          if (e.target !== e.currentTarget || e.nativeEvent.isComposing || (e.key !== "Enter" && e.key !== " ")) return;
          e.preventDefault();
          if (!frozen) onPick();
        }}
        className={cx(
          "group/row relative z-[1] flex w-full items-center gap-2 rounded-control px-3 py-2 text-[14px] leading-[1.43] text-ink outline-none select-none",
          "focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent",
          follow.active && "bg-hover-2 data-[glide]:bg-transparent",
          frozen ? "cursor-not-allowed" : "cursor-pointer",
        )}
      >
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="truncate leading-5 font-medium">{follow.label}</span>
          {(follow.detail || follow.warning) && (
            <span title={[follow.detail, follow.warning].filter(Boolean).join(" · ")} className="flex min-w-0 items-center gap-1 pt-px text-[12px] leading-[1.4] text-ink-2">
              {follow.warning && <Icon name="warning" size={11} className="shrink-0 text-orange" />}
              <span className="truncate">{follow.detail || follow.warning}</span>
            </span>
          )}
        </span>
        {follow.active && <Icon name="check" size={14} stroke={2.5} className="shrink-0 text-ink" />}
      </div>
      <div aria-hidden className="mx-1 my-1 h-px bg-line" />
    </>
  );
}
