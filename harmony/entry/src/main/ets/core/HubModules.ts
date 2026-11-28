/** Pure headless Hub module flags, hidden page visibility, and LiveView push-route rules aligned with iOS. */

export const HUB_MODULES_MARKER: string = 'modules';
export const HUB_MODULE_PREFIX: string = 'module.';
export const MAIL_MODULE_FEATURE: string = 'module.mail';
export const LARK_MODULE_FEATURE: string = 'module.lark';
export const RECORDING_MODULE_FEATURE: string = 'module.recording';

/**
 * Extract the Hub's published module flags from a `hello` / `health` `features` list, matching iOS `HubModules`.
 * Returns `null` when the Hub does not announce the `modules` marker (older hub => every module allowed).
 * Returns a sorted array (possibly empty) of `module.*` flags when the `modules` marker is present.
 */
export function extractPublishedModules(features: readonly string[]): string[] | null {
  if (!features || features.length === 0) return null;
  if (!features.includes(HUB_MODULES_MARKER)) return null;
  const set = new Set<string>();
  for (let i = 0; i < features.length; i++) {
    const f = features[i];
    if (f && f.startsWith(HUB_MODULE_PREFIX)) set.add(f);
  }
  return Array.from(set).sort();
}

/**
 * Fail-closed Hub module gate matching iOS `HubModules.allows` (#15985 / #16073 / #16113):
 * returns `false` ONLY when `'modules'` is announced in the active feature list (or remembered modules when
 * `liveFeatures` is empty) and `featureKey` is absent. Live `features` takes precedence over remembered modules.
 */
export function isHubModuleAllowed(
  featureKey: string,
  liveFeatures: readonly string[],
  rememberedModules?: readonly string[] | null
): boolean {
  if (liveFeatures && liveFeatures.length > 0) {
    const published = extractPublishedModules(liveFeatures);
    if (published === null) return true;
    return published.includes(featureKey);
  }
  if (rememberedModules !== undefined && rememberedModules !== null) {
    return rememberedModules.includes(featureKey);
  }
  return true;
}

function normalizeHiddenPageId(raw: string): string {
  const normalized = raw.trim().toLowerCase();
  if (!normalized) return '';
  if (normalized === 'todo') return 'todos';
  return normalized;
}

function collectHiddenPageIds(list: Object | undefined, out: Set<string>): void {
  if (!Array.isArray(list)) return;
  for (let i = 0; i < list.length; i++) {
    const item = list[i];
    if (typeof item !== 'string') continue;
    const normalized = normalizeHiddenPageId(item);
    if (normalized) out.add(normalized);
  }
}

/**
 * Parse Hub `sidebarConfig` JSON matching iOS `HubPageVisibility.swift` (#16339).
 * Checks `hidden`, `hidden_pages`, and `pages` (`pages.hidden`, `pages.hidden_pages`, or `pages` array).
 * Fail-open empty set on empty or malformed JSON.
 */
export function parseHiddenHubPages(sidebarConfig: string): Set<string> {
  const out = new Set<string>();
  if (!sidebarConfig || !sidebarConfig.trim()) return out;
  try {
    const parsed = JSON.parse(sidebarConfig) as Record<string, Object>;
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return out;
    collectHiddenPageIds(parsed['hidden'], out);
    collectHiddenPageIds(parsed['hidden_pages'], out);
    const pages = parsed['pages'];
    if (Array.isArray(pages)) {
      collectHiddenPageIds(pages, out);
    } else if (pages && typeof pages === 'object') {
      const pagesObj = pages as Record<string, Object>;
      collectHiddenPageIds(pagesObj['hidden'], out);
      collectHiddenPageIds(pagesObj['hidden_pages'], out);
    }
    return out;
  } catch (_) {
    return new Set<string>();
  }
}

export function isHubPageHidden(page: string, sidebarConfig: string, isDemo: boolean = false): boolean {
  if (isDemo) return false;
  const normalized = normalizeHiddenPageId(page);
  if (!normalized) return false;
  return parseHiddenHubPages(sidebarConfig).has(normalized);
}

/**
 * Whether this client has a confirmed registered Hub background push route (matching iOS #16783 `PushHealth`).
 * HarmonyOS currently uses foreground `ws_notify` without Push Kit registration, so `hasPushRegistration` is false.
 */
export function hasRegisteredPushRoute(hasPushRegistration: boolean): boolean {
  return hasPushRegistration;
}

/**
 * Whether an active LiveView must end immediately when transitioning to background (#16783):
 * ends only for an active non-demo session when the Hub lacks a background push route.
 */
export function shouldEndLiveViewOnBackground(hasPushRoute: boolean, isDemo: boolean, active: boolean): boolean {
  return active && !isDemo && !hasPushRoute;
}
