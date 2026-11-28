// The account menu's rows for the other hubs (#1014, lib/hubs.ts). Kept apart from Hubs.tsx so the first screen, which
// draws that menu in the assistant switcher, does not load Settings' hub list and dialogs (and SettingsParts with them,
// #13319).
import { useCallback, useRef, useSyncExternalStore } from "react";
import { here, hubTitle, loadHubs, onHubsChange, switchURL, type SavedHub } from "../lib/hubs";
import { sameValue } from "../lib/stable-lines";
import type { MenuItem } from "../ui/atoms";
import { t } from "../i18n";

export interface UseHubsDeps {
  /** Controlled hubs list or getter (bypasses `loadHubs`, `onHubsChange`, and `storage` events when supplied). */
  hubs?: readonly SavedHub[] | (() => readonly SavedHub[]);
  /** Subscribe to controlled hubs updates (bypasses default hub subscriptions when supplied). */
  subscribeHubs?: (listener: () => void) => () => void;
}

export interface GoToHubDeps {
  /** Controlled hubs list or getter carried in `#hubs=` (defaults to `loadHubs`). */
  hubs?: readonly SavedHub[] | (() => readonly SavedHub[]);
  /** Custom switch URL builder (defaults to `lib/hubs.switchURL`). */
  buildSwitchURL?: (target: string, list: SavedHub[]) => string;
  /** Custom navigation callback (defaults to assigning `location.href`). */
  navigateTo?: (url: string, hub: SavedHub) => void | Promise<unknown>;
}

export interface HubMenuItemsDeps extends GoToHubDeps {
  /** Controlled current hub URL (defaults to `here()`). */
  current?: string | (() => string);
  /** Custom switch callback (defaults to calling `goToHub(hub, deps)`). */
  onSwitch?: (hub: SavedHub) => void | Promise<unknown>;
}

export const noopSubscribe = () => () => {};

function subscribeDefaultHubs(listener: () => void): () => void {
  const off = onHubsChange(listener);
  if (typeof addEventListener === "undefined") return off;
  // another tab of this hub's page changed it
  const storage = (e: StorageEvent) => {
    if (e.key === "persona.hubs" || e.key === null) listener();
  };
  addEventListener("storage", storage);
  return () => {
    off();
    removeEventListener("storage", storage);
  };
}

export function useHubs(deps?: UseHubsDeps): SavedHub[] {
  const controlled = deps?.hubs !== undefined || deps?.subscribeHubs !== undefined;
  const hubsDep = deps?.hubs;
  const subscribe = controlled
    ? (hubsDep !== undefined && typeof hubsDep !== "function" ? noopSubscribe : (deps?.subscribeHubs ?? noopSubscribe))
    : subscribeDefaultHubs;
  const cache = useRef<{ has: boolean; value: SavedHub[] }>({ has: false, value: [] });
  const getSnapshot = useCallback((): SavedHub[] => {
    const raw = hubsDep !== undefined ? (typeof hubsDep === "function" ? hubsDep() : hubsDep) : loadHubs();
    const next = raw as SavedHub[];
    if (cache.current.has && sameValue(cache.current.value, next)) {
      return cache.current.value;
    }
    cache.current = { has: true, value: next };
    return next;
  }, [hubsDep]);
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}

/** Open another hub's page, handing it the list. */
export function goToHub(h: SavedHub, deps?: GoToHubDeps) {
  const list = deps?.hubs !== undefined
    ? ([...(typeof deps.hubs === "function" ? deps.hubs() : deps.hubs)] as SavedHub[])
    : loadHubs();
  const targetUrl = (deps?.buildSwitchURL ?? switchURL)(h.url, list);
  if (deps?.navigateTo) return deps.navigateTo(targetUrl, h);
  location.href = targetUrl;
}

/** The account menu's rows for the other hubs (none when there is no other one). */
export function hubMenuItems(list: readonly SavedHub[], deps?: HubMenuItemsDeps): MenuItem[] {
  const cur = deps?.current !== undefined ? (typeof deps.current === "function" ? deps.current() : deps.current) : here();
  return list
    .filter((h) => h.url !== cur)
    .map((h, i) => ({
      key: `hub:${h.url}`,
      label: hubTitle(h),
      hint: t("hubs.switch"),
      icon: "hub" as const,
      divider: i === 0,
      onSelect: () => {
        if (deps?.onSwitch) return deps.onSwitch(h);
        return goToHub(h, deps);
      },
    }));
}
