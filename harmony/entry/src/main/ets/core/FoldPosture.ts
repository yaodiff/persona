/**
 * HarmonyOS foldable & tri-fold posture engine (Mate X7 dual-fold, Mate XT tri-fold,
 * MatePad / MateBook wide displays, and Mate 90 Pro straight phones).
 *
 * Pure TypeScript so layout decisions are deterministically verified on the host
 * without depending on ArkUI runtime mocks.
 */

export type FoldPostureKind =
  | 'straight'
  | 'cover'
  | 'dual_unfolded'
  | 'book_half_open'
  | 'trifold_angled'
  | 'trifold_unfolded'
  | 'hover_tabletop';

export type FoldWidthClass = 'xs' | 'sm' | 'md' | 'lg' | 'xl';

export interface FoldPostureInput {
  readonly widthClass: FoldWidthClass;
  readonly windowWidthVp: number;
  readonly windowHeightVp?: number;
  readonly isFoldable: boolean;
  /** HarmonyOS `display.FoldStatus` numeric value (0=unknown, 1=expanded, 2=folded, 3=half, 11=dual-axis expanded, 21/13/23=tri-fold half variants). */
  readonly foldStatus: number;
  /** HarmonyOS `display.FoldDisplayMode` numeric value (1=full, 2=main, 3=sub, 4=coordination, 5=global_full). */
  readonly foldDisplayMode: number;
  readonly nativeHoverAvailable: boolean;
}

export interface FoldAdaptiveLayout {
  readonly posture: FoldPostureKind;
  readonly navigationMode: 'stack' | 'split';
  readonly isFoldableSplit: boolean;
  readonly masterWidth: number;
  readonly masterMin: number;
  readonly masterMax: number;
  readonly detailMin: number;
  readonly supportsInspector: boolean;
  readonly autoShowInspector: boolean;
  readonly collapseRailForInspector: boolean;
  readonly inspectorWidth: number;
  readonly readingMaxWidth: number;
  readonly hoverMode: boolean;
  readonly cardColumns: number;
}

// HarmonyOS API 26 `display.FoldStatus` values
export const FOLD_STATUS_UNKNOWN = 0;
export const FOLD_STATUS_EXPANDED = 1;
export const FOLD_STATUS_FOLDED = 2;
export const FOLD_STATUS_HALF_FOLDED = 3;
export const FOLD_STATUS_EXPANDED_WITH_SECOND_EXPANDED = 11;
export const FOLD_STATUS_FOLDED_WITH_SECOND_EXPANDED = 12;
export const FOLD_STATUS_HALF_FOLDED_WITH_SECOND_EXPANDED = 13;
export const FOLD_STATUS_EXPANDED_WITH_SECOND_HALF_FOLDED = 21;
export const FOLD_STATUS_FOLDED_WITH_SECOND_HALF_FOLDED = 22;
export const FOLD_STATUS_HALF_FOLDED_WITH_SECOND_HALF_FOLDED = 23;

// HarmonyOS API 26 `display.FoldDisplayMode` values
export const FOLD_DISPLAY_MODE_UNKNOWN = 0;
export const FOLD_DISPLAY_MODE_FULL = 1;
export const FOLD_DISPLAY_MODE_MAIN = 2;
export const FOLD_DISPLAY_MODE_SUB = 3;
export const FOLD_DISPLAY_MODE_COORDINATION = 4;
export const FOLD_DISPLAY_MODE_GLOBAL_FULL = 5;

export function isHalfFoldedStatus(foldStatus: number): boolean {
  return (
    foldStatus === FOLD_STATUS_HALF_FOLDED ||
    foldStatus === FOLD_STATUS_HALF_FOLDED_WITH_SECOND_EXPANDED ||
    foldStatus === FOLD_STATUS_EXPANDED_WITH_SECOND_HALF_FOLDED ||
    foldStatus === FOLD_STATUS_FOLDED_WITH_SECOND_HALF_FOLDED ||
    foldStatus === FOLD_STATUS_HALF_FOLDED_WITH_SECOND_HALF_FOLDED
  );
}

export function isTriFoldExpandedStatus(foldStatus: number): boolean {
  return foldStatus === FOLD_STATUS_EXPANDED_WITH_SECOND_EXPANDED;
}

export function isTriFoldAngledStatus(foldStatus: number): boolean {
  return (
    foldStatus === FOLD_STATUS_EXPANDED_WITH_SECOND_HALF_FOLDED ||
    foldStatus === FOLD_STATUS_HALF_FOLDED_WITH_SECOND_EXPANDED ||
    foldStatus === FOLD_STATUS_HALF_FOLDED_WITH_SECOND_HALF_FOLDED
  );
}

export function widthClassFromBreakpoint(bp: number): FoldWidthClass {
  // ArkUI WidthBreakpoint: 0=XS, 1=SM, 2=MD, 3=LG, 4=XL
  if (bp <= 0) return 'xs';
  if (bp === 1) return 'sm';
  if (bp === 2) return 'md';
  if (bp === 3) return 'lg';
  return 'xl';
}

export function classifyFoldPosture(input: FoldPostureInput): FoldPostureKind {
  const width = Number.isFinite(input.windowWidthVp) ? Math.max(0, input.windowWidthVp) : 0;
  // Narrow split-screen or floating windows must stay compact stack even when hardware reports tri-fold.
  if (width > 0 && width < 560) {
    return input.isFoldable ? 'cover' : 'straight';
  }
  if (input.isFoldable && isTriFoldAngledStatus(input.foldStatus) && width >= 820) {
    return 'trifold_angled';
  }
  if (input.isFoldable && isHalfFoldedStatus(input.foldStatus)) {
    if (input.nativeHoverAvailable) {
      return 'hover_tabletop';
    }
    if (width >= 560) {
      return 'book_half_open';
    }
  }
  const allowsTriFold = width === 0 || width >= 820;
  if (
    allowsTriFold &&
    (input.foldDisplayMode === FOLD_DISPLAY_MODE_GLOBAL_FULL ||
      isTriFoldExpandedStatus(input.foldStatus) ||
      (input.isFoldable && width >= 920) ||
      input.widthClass === 'xl' ||
      (!input.isFoldable && input.widthClass === 'lg'))
  ) {
    return 'trifold_unfolded';
  }
  if (input.isFoldable) {
    if (
      width === 0 &&
      (input.foldStatus === FOLD_STATUS_FOLDED ||
        input.foldDisplayMode === FOLD_DISPLAY_MODE_MAIN ||
        input.foldDisplayMode === FOLD_DISPLAY_MODE_SUB)
    ) {
      return 'cover';
    }
    return 'dual_unfolded';
  }
  if (input.widthClass === 'md' && width >= 640) return 'dual_unfolded';
  return 'straight';
}

/**
 * Resolves adaptive multi-pane layout metrics for Mate 90 Pro (straight),
 * Mate X7 (cover / 707vp dual-unfolded / half-folded hover), and
 * Mate XT / Mate XT 2 (single / dual / angled stand / 1060vp+ tri-fold 3-pane workbench).
 */
export function resolveFoldLayout(input: FoldPostureInput): FoldAdaptiveLayout {
  const posture = classifyFoldPosture(input);
  const width = Number.isFinite(input.windowWidthVp) ? Math.max(0, input.windowWidthVp) : 0;
  const hoverMode = posture === 'hover_tabletop';

  if (posture === 'trifold_unfolded') {
    const compactTriFold = width > 0 && width < 960;
    const masterWidth = width >= 1160 ? 312 : compactTriFold ? 256 : 288;
    const inspectorWidth = width >= 1160 ? 356 : compactTriFold ? 288 : 316;
    return {
      posture: posture,
      navigationMode: 'split',
      isFoldableSplit: input.isFoldable,
      masterWidth: masterWidth,
      masterMin: compactTriFold ? 244 : 256,
      masterMax: 420,
      detailMin: 340,
      supportsInspector: true,
      autoShowInspector: true,
      collapseRailForInspector: compactTriFold,
      inspectorWidth: inspectorWidth,
      readingMaxWidth: 740,
      hoverMode: false,
      cardColumns: width === 0 || width >= 960 ? 3 : 2
    };
  }

  if (posture === 'trifold_angled') {
    const masterWidth = width >= 960 ? 276 : 256;
    return {
      posture: posture,
      navigationMode: 'split',
      isFoldableSplit: input.isFoldable,
      masterWidth: masterWidth,
      masterMin: 244,
      masterMax: 340,
      detailMin: 320,
      supportsInspector: true,
      autoShowInspector: true,
      collapseRailForInspector: width < 960,
      inspectorWidth: 304,
      readingMaxWidth: 720,
      hoverMode: false,
      cardColumns: 2
    };
  }

  if (posture === 'dual_unfolded' || posture === 'book_half_open' || (hoverMode && width >= 600)) {
    // Mate X7 unfolded (~707vp) and Mate XT 2-screen (~680vp):
    // True HarmonyOS dual-pane split (master 268vp + live detail 439vp) with toggleable inspector.
    const masterWidth = width >= 760 ? 296 : Math.max(256, Math.round(width * 0.39));
    return {
      posture: posture,
      navigationMode: 'split',
      isFoldableSplit: input.isFoldable,
      masterWidth: masterWidth,
      masterMin: 244,
      masterMax: Math.min(360, Math.round(width * 0.48)),
      detailMin: 320,
      supportsInspector: width >= 660,
      autoShowInspector: false,
      collapseRailForInspector: true,
      inspectorWidth: 268,
      readingMaxWidth: 720,
      hoverMode: hoverMode,
      cardColumns: 2
    };
  }

  // Straight phones (Mate 90 Pro ~366vp), Mate X7 cover (~346vp), Mate XT 1-screen (~360vp)
  return {
    posture: posture,
    navigationMode: 'stack',
    isFoldableSplit: false,
    masterWidth: 320,
    masterMin: 280,
    masterMax: 440,
    detailMin: 320,
    supportsInspector: false,
    autoShowInspector: false,
    collapseRailForInspector: false,
    inspectorWidth: 0,
    readingMaxWidth: 680,
    hoverMode: hoverMode,
    cardColumns: 1
  };
}

/**
 * Compact hierarchical left inset for repository/worktree agent trees.
 * On narrow straight phones / foldable cover screens (< 560vp), keeps indentation tight
 * so agent titles retain ~120px more readable width without premature ellipsis.
 */
export function compactTreeInset(level: number, windowWidthVp: number, controlScale: number): number {
  const clamped = Math.max(0, Math.min(3, Math.floor(level)));
  const scale = Number.isFinite(controlScale) && controlScale > 0 ? controlScale : 1;
  if (windowWidthVp > 0 && windowWidthVp < 560) {
    // 14, 18, 22, 28 vp (vs legacy 14, 36, 58, 80 vp — saves 36vp on session rows!)
    const compactSteps: number[] = [14, 18, 22, 28];
    return Math.round(compactSteps[clamped] * Math.min(scale, 1.15));
  }
  return Math.round(14 + 20 * scale * clamped);
}

export interface HoverSplitGeometry {
  readonly active: boolean;
  readonly topPaneHeightVp: number;
  readonly creaseBandVp: number;
  readonly bottomPaneHeightVp: number;
}

/**
 * Computes crease-aware top/bottom split heights for half-folded hover tabletop mode.
 * Returns `active: false` unless both the upper reading pane (>= 180vp) and lower control deck
 * (>= 140vp) fit cleanly on either side of the physical hinge crease.
 */
export function resolveHoverSplitGeometry(
  windowHeightVp: number,
  creaseTopVp: number,
  creaseHeightVp: number
): HoverSplitGeometry {
  if (
    !Number.isFinite(windowHeightVp) ||
    !Number.isFinite(creaseTopVp) ||
    !Number.isFinite(creaseHeightVp) ||
    windowHeightVp <= 0 ||
    creaseTopVp < 180 ||
    creaseHeightVp <= 0
  ) {
    return { active: false, topPaneHeightVp: 0, creaseBandVp: 0, bottomPaneHeightVp: 0 };
  }
  const topPaneHeightVp = Math.floor(creaseTopVp);
  const creaseBandVp = Math.max(12, Math.ceil(creaseHeightVp));
  const bottomPaneHeightVp = Math.floor(windowHeightVp - topPaneHeightVp - creaseBandVp);
  if (topPaneHeightVp < 180 || bottomPaneHeightVp < 140) {
    return { active: false, topPaneHeightVp: 0, creaseBandVp: 0, bottomPaneHeightVp: 0 };
  }
  return {
    active: true,
    topPaneHeightVp: topPaneHeightVp,
    creaseBandVp: creaseBandVp,
    bottomPaneHeightVp: bottomPaneHeightVp
  };
}

export type FoldShortcutAction =
  | 'search'
  | 'new_chat'
  | 'toggle_inspector'
  | 'workbench_pulse'
  | 'workbench_code'
  | 'workbench_machines'
  | 'open_xiaoyi'
  | 'next_unread'
  | 'prev_unread'
  | 'dismiss'
  | 'none';

export type FoldKeyboardShortcutAction = FoldShortcutAction | 'open_palette';

/**
 * Resolves hardware keyboard shortcuts for foldable, tri-fold, MatePad, and MateBook sessions.
 * - Ctrl/Meta + K -> open search
 * - Ctrl/Meta + N -> new chat or agent
 * - Ctrl/Meta + I -> toggle Live Inspector when supported by the current fold posture
 * - Ctrl/Meta + 1/2/3 -> switch Live Workbench tab (Pulse / Code / Machines) when supported
 * - Ctrl/Meta + 0 -> open XiaoYi diagnostics & settings
 * - Ctrl/Meta + ] / [ -> step to next / previous actionable session in the relay queue
 * - Escape -> dismiss active overlay, sheet, or inspector
 */
export function resolveFoldShortcut(
  key: string,
  ctrlOrMeta: boolean,
  supportsInspector: boolean
): FoldShortcutAction {
  const normalized = (key || '').trim().toLowerCase();
  if (!normalized) return 'none';
  if (normalized === 'escape' || normalized === 'esc') {
    return 'dismiss';
  }
  if (!ctrlOrMeta) return 'none';
  if (normalized === 'k') return 'search';
  if (normalized === 'n') return 'new_chat';
  if (normalized === 'i') return supportsInspector ? 'toggle_inspector' : 'none';
  if (normalized === '1') return supportsInspector ? 'workbench_pulse' : 'none';
  if (normalized === '2') return supportsInspector ? 'workbench_code' : 'none';
  if (normalized === '3') return supportsInspector ? 'workbench_machines' : 'none';
  if (normalized === '0') return 'open_xiaoyi';
  if (normalized === ']') return 'next_unread';
  if (normalized === '[') return 'prev_unread';
  return 'none';
}

/**
 * Resolves hardware keyboard shortcuts with Quick Command Palette (`Ctrl/Meta+K` -> `open_palette`).
 */
export function resolveFoldKeyboardShortcut(
  key: string,
  ctrlOrMeta: boolean,
  supportsInspector: boolean
): FoldKeyboardShortcutAction {
  const base = resolveFoldShortcut(key, ctrlOrMeta, supportsInspector);
  if (base === 'search') return 'open_palette';
  return base;
}

/**
 * Picks the next or previous actionable session index for Done-Unread / Needs-You relay sweeps.
 * Prioritizes `needs_you` and `unread` sessions, falling back to `working`, or `-1` if none.
 */
export function pickRelaySessionIndex(
  states: string[],
  currentIndex: number,
  direction: 1 | -1
): number {
  if (!Array.isArray(states) || states.length === 0) return -1;
  const primary: number[] = [];
  const fallback: number[] = [];
  for (let i = 0; i < states.length; i++) {
    const s = (states[i] || '').trim().toLowerCase();
    if (s === 'needs_you' || s === 'blocked' || s === 'unread' || s === 'done') {
      primary.push(i);
    } else if (s === 'working' || s === 'running') {
      fallback.push(i);
    }
  }
  const pool = primary.length > 0 ? primary : fallback;
  if (pool.length === 0) return -1;
  if (currentIndex < 0 || currentIndex >= states.length) {
    return direction === 1 ? pool[0] : pool[pool.length - 1];
  }
  if (direction === 1) {
    for (let i = 0; i < pool.length; i++) {
      if (pool[i] > currentIndex) return pool[i];
    }
    return pool[0];
  }
  for (let i = pool.length - 1; i >= 0; i--) {
    if (pool[i] < currentIndex) return pool[i];
  }
  return pool[pool.length - 1];
}

/**
 * Suppresses redundant subtitle place/pane labels that verbatim duplicate an agent row title
 * (case-insensitive), including matching segments inside compound ` · ` / ` › ` place strings.
 */
export function deduplicateAgentPlace(title: string, place: string): string {
  const cleanPlace = (place || '').trim();
  if (!cleanPlace) return '';
  const cleanTitle = (title || '').trim();
  if (!cleanTitle) return cleanPlace;
  const normalizedTitle = cleanTitle.toLocaleLowerCase();
  if (cleanPlace.toLocaleLowerCase() === normalizedTitle) return '';

  const dotParts = cleanPlace
    .split(' · ')
    .map((part: string) => {
      const subParts = part
        .split(' › ')
        .map((sub: string) => sub.trim())
        .filter((sub: string) => !!sub && sub.toLocaleLowerCase() !== normalizedTitle);
      return subParts.join(' › ');
    })
    .filter((part: string) => !!part);

  return dotParts.join(' · ');
}
