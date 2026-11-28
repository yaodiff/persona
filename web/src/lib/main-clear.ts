// Clearing the main conversation's context in place (#5095), as Cindy's /clear: POST /assistant/main/clear stops a turn
// it is on, archives it as "Old main chat · to <date>" and carries nothing over, so the next message starts a new
// conversation with a fresh context. No confirmation: nothing is deleted (docs/design/DESIGN_SYSTEM.md 4.4, the
// reversible rung). The page stays where it is: the chat empties and the composer stays at the bottom
// (PersonaAssistant's Starter, `cleared`), where the first message starts the new one.
import { useSyncExternalStore } from "react";
import { t } from "../i18n";
import { toast } from "../ui/atoms";
import { api, ApiError, credentialOwner, errorText } from "./api";
import type { ComposerCommand } from "./extension-prompts";
import { chosenManager, onDefaultManager, onManagerChoice } from "./manager-choice";
import { navigate, subscribeRoute } from "./router";
import { getState, refreshAssistant, subscribe } from "./store";

export const CLEAR_MAIN_FEATURE = "assistant_main_clear";
export const CLEAR_MAIN_PATH = "/assistant/main/clear";

/** The main conversation's command list: /clear, worded as an Agent's /clear is. */
export function mainCommands(): ComposerCommand[] {
  return [{ word: "/clear", desc: t("chat-composer.cmd_clear_desc"), text: "/clear" }];
}

/** A message that is the /clear command: the word alone, no pictures (anything more is words for Persona). */
export function isClearCommand(text: string, images: number): boolean {
  return images === 0 && text.trim().toLowerCase() === "/clear";
}

// ── the conversation this tab cleared, while the main one is still to start ──
// The page keeps the conversation's layout for it (an empty chat, the composer at the bottom), for the manager it was
// cleared under only. Another main conversation of that manager in the store forgets it, whether the page is open or
// not, so a later "New main chat" or another device's change shows the ordinary start page.

const NONE = { manager: "", sid: "" };
let cleared = NONE;
const listeners = new Set<() => void>();
let unwatch: (() => void) | null = null;

const currentManager = () => chosenManager() ?? "";

function setCleared(next: { manager: string; sid: string }) {
  if (cleared.manager === next.manager && cleared.sid === next.sid) return;
  cleared = next;
  if (next.sid && !unwatch) {
    unwatch = subscribe(() => {
      const main = getState().assistant?.main;
      if (main && main !== cleared.sid && currentManager() === cleared.manager) forgetMainCleared();
    });
  } else if (!next.sid && unwatch) {
    unwatch();
    unwatch = null;
  }
  listeners.forEach((l) => l());
}

/** Keeps the docked conversation layout while this tab clears or replaces main conversation `sid`. */
export function markMainCleared(sid: string, manager = currentManager()) {
  setCleared({ manager, sid });
}

export function forgetMainCleared() {
  setCleared(NONE);
}

/** Forgets the docked cleared state only when it still matches `(sid, manager)` marked by the caller. */
export function forgetMainClearedIf(sid: string, manager = currentManager()) {
  if (cleared.manager === manager && cleared.sid === sid) forgetMainCleared();
}

/** Whether this tab cleared the main conversation of `manager` (the one chosen now by default) and none started since. */
export function mainCleared(manager = currentManager()): boolean {
  return cleared.sid !== "" && cleared.manager === manager;
}

export function useMainCleared(manager: string): boolean {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => mainCleared(manager),
    () => mainCleared(manager),
  );
}

// ── the action ──

export interface ClearDeps {
  post: (expect: string) => Promise<unknown>;
  refresh: () => Promise<unknown>;
  /** To the main conversation's page (a conversation's own address showed the old one). */
  home: (changedElsewhere?: boolean) => void;
  toast: (text: string, tone: "error" | "info") => void;
  /** Optional hook invoked right after `home()` (e.g. closing the mobile sidebar drawer). */
  afterHome?: () => void;
  /** Optional scope check for tests or controlled callers; defaults to tracking manager & credential scope. */
  isScopeCurrent?: () => boolean;
}

const defaultDeps: ClearDeps = {
  post: (expect) => api(CLEAR_MAIN_PATH, { method: "POST", json: { expect } }),
  refresh: () => refreshAssistant(),
  home: () => navigate({ name: "persona" }),
  toast: (text, tone) => toast(text, tone),
};

const inFlight = new Set<string>();
const busyListeners = new Set<() => void>();
// A read-only recovery action can outlive the row that started it. Track changes once per tab so
// dismissing a retry needs no per-request subscription cleanup, even after leaving and coming back.
let watchingScope = false;
let pageVersion = 0;
let managerVersion = 0;
export function mainActionScope() {
  const url = globalThis.location?.href;
  if (!watchingScope) {
    watchingScope = true;
    let lastUrl = url;
    subscribeRoute(() => {
      const next = globalThis.location?.href;
      if (next !== lastUrl) { lastUrl = next; pageVersion++; }
    });
    onManagerChoice(() => { managerVersion++; });
    onDefaultManager(() => { managerVersion++; });
  }
  const page = pageVersion;
  const manager = managerVersion;
  const owner = safeCredentialOwner();
  return {
    current: () => manager === managerVersion && safeCredentialOwner() === owner,
    samePage: () => page === pageVersion && globalThis.location?.href === url,
  };
}

function notifyBusy() {
  busyListeners.forEach((l) => l());
}

/** Clearing and replacing both archive the same main. Only one mutation may own its docked layout at a time. */
export function beginMainChange(sid: string): (() => void) | undefined {
  if (inFlight.has(sid)) return;
  inFlight.add(sid);
  notifyBusy();
  return () => { inFlight.delete(sid); notifyBusy(); };
}

/** Whether a clear-main request is currently in flight (for `sid` when provided, or any main chat). */
export function isMainClearing(sid?: string): boolean {
  return sid ? inFlight.has(sid) : inFlight.size > 0;
}

export function useMainClearing(sid?: string): boolean {
  return useSyncExternalStore(
    (l) => {
      busyListeners.add(l);
      return () => busyListeners.delete(l);
    },
    () => isMainClearing(sid),
    () => isMainClearing(sid),
  );
}

function safeCredentialOwner(): string {
  try {
    return typeof localStorage === "undefined" ? "" : credentialOwner();
  } catch {
    return "";
  }
}

/** Clears the main conversation sid (the one this device shows; another device's change since is refused, then shown).
 *  True when it is done or another one shows now, so the /clear typed is used up; false when it failed (a toast says
 *  why), so it goes back into the box. A second clear of the same one while the first is on its way does nothing. */
export async function clearMainContext(sid: string, overrides: Partial<ClearDeps> = {}): Promise<boolean> {
  const release = beginMainChange(sid);
  if (!release) return true;
  const deps = { ...defaultDeps, ...overrides };
  const scope = mainActionScope();
  try {
    const startedManager = currentManager();
    const stillCurrent = () => scope.current() && (deps.isScopeCurrent?.() ?? true);
    const clearOwnMarked = () => {
      forgetMainClearedIf(sid, startedManager);
    };
    // before the request: the hub's push of the change can come before its answer, and the page must not show the
    // centered start page in between
    setCleared({ manager: startedManager, sid });
    let changedElsewhere = false;
    try {
      await deps.post(sid);
    } catch (e) {
      clearOwnMarked();
      if (!stillCurrent()) return false;
      if (!(e instanceof ApiError && e.code === "main_changed")) {
        deps.toast(t("assistant-assistantchat.clear_failed", { error: errorText(e) }), "error");
        return false;
      }
      changedElsewhere = true;
    }
    if (!stillCurrent()) {
      clearOwnMarked();
      return true;
    }
    try {
      const updated = await deps.refresh();
      if (updated === false && getState().assistant?.main === sid) throw new Error(t("chat-chatview.refresh_failed"));
    } catch (e) {
      if (stillCurrent() && scope.samePage()) deps.toast(t("personaassistant.load_failed", { error: errorText(e) }), "error");
      // The mutation is settled. Do not put a destructive command back in the composer on a failed read.
      return true;
    }
    if (stillCurrent() && scope.samePage()) {
      deps.home(changedElsewhere);
      deps.afterHome?.();
      if (changedElsewhere) deps.toast(t("assistant-assistantchat.new_main_elsewhere"), "info");
    }
    return true;
  } finally {
    release();
  }
}
