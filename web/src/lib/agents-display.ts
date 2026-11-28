// One acknowledged display choice for the paired hub's default manager. Inventory
// and to-do attribution stay unchanged. The old local key is migration input only.
import { useSyncExternalStore } from "react";
import { api, credentialOwner, onCredentialChange } from "./api";
import { BOTMUX_MODE_KEY, LEGACY_BOTMUX_HIDDEN_KEY } from "./botmux-mode";

const PATH = "/agents/display-settings";
/** `sort_by_activity` and `show_filter` are the effective values (saved choice, else the hub's initial default, else off);
 *  absent from a hub that has not got them, where the Settings switches stay hidden and the list keeps its stable order. */
/** `botmux_owner_union_id` is the BotMux Owner Union ID ("" for none), absent from a hub without it. */
export interface AgentsDisplay { show_botmux: boolean; configured: boolean; use_herdr_tab_name?: boolean; sort_by_activity?: boolean; show_filter?: boolean; botmux_owner_union_id?: string }
export type DisplayChange = { show_botmux: boolean } | { use_herdr_tab_name: boolean } | { sort_by_activity: boolean } | { show_filter: boolean } | { botmux_owner_union_id: string };
interface State { value: AgentsDisplay | null; saving: boolean; error?: unknown; change?: DisplayChange }
let state: State = { value: null, saving: false };
let revision = 0;
let reading: Promise<void> | undefined;
const listeners = new Set<() => void>();
const snapshot = () => state;
const subscribe = (f: () => void) => { listeners.add(f); return () => { listeners.delete(f); }; };
const set = (next: State) => { state = next; listeners.forEach((f) => f()); };
function checked(value: AgentsDisplay): AgentsDisplay {
  if (typeof value.show_botmux !== "boolean" || typeof value.configured !== "boolean" ||
    (value.use_herdr_tab_name !== undefined && typeof value.use_herdr_tab_name !== "boolean") ||
    (value.sort_by_activity !== undefined && typeof value.sort_by_activity !== "boolean") ||
    (value.show_filter !== undefined && typeof value.show_filter !== "boolean") ||
    (value.botmux_owner_union_id !== undefined && typeof value.botmux_owner_union_id !== "string")) throw new Error("Invalid Agents display settings");
  return value;
}
function legacyHidden() {
  try {
    return localStorage.getItem(LEGACY_BOTMUX_HIDDEN_KEY) === "1" || localStorage.getItem(BOTMUX_MODE_KEY) === "hide";
  } catch {
    return false;
  }
}
function forgetLegacy() {
  try {
    localStorage.removeItem(LEGACY_BOTMUX_HIDDEN_KEY);
    if (localStorage.getItem(BOTMUX_MODE_KEY) === "hide") localStorage.removeItem(BOTMUX_MODE_KEY);
  } catch { /* a later read may retry */ }
}

/** Called on hello and each Agents list refresh. Stale reads cannot undo a save or a new credential. */
export function refreshAgentsDisplay(): Promise<void> {
  credentialOwner();
  if (state.saving) return Promise.resolve();
  if (reading) return reading;
  const mine = revision;
  const work = (async () => {
    try {
      let value = checked(await api<AgentsDisplay>(PATH));
      if (mine !== revision) return;
      if (!value.configured && legacyHidden()) {
        value = checked(await api<AgentsDisplay>(PATH, { method: "PUT", json: { show_botmux: false, only_if_unset: true } }));
      }
      if (mine !== revision) return;
      if (value.configured) forgetLegacy();
      set(state.change === undefined ? { value, saving: false } : { ...state, value });
    } catch (error) {
      if (mine === revision && state.change === undefined) set({ ...state, error });
    }
  })();
  reading = work;
  void work.finally(() => { if (reading === work) reading = undefined; });
  return work;
}

export async function saveAgentsDisplay(input: boolean | DisplayChange): Promise<void> {
  credentialOwner();
  if (state.saving) return;
  const mine = ++revision;
  const change = typeof input === "boolean" ? { show_botmux: input } : input;
  set({ value: state.value, saving: true, change });
  try {
    const value = checked(await api<AgentsDisplay>(PATH, { method: "PUT", json: change }));
    if (mine !== revision) return;
    if (value.configured) forgetLegacy();
    set({ value, saving: false });
  } catch (error) {
    if (mine === revision) set({ ...state, saving: false, error });
  }
}

export function resetAgentsDisplay() {
  revision++;
  reading = undefined;
  set({ value: null, saving: false });
}
onCredentialChange(resetAgentsDisplay);
export function useAgentsDisplay() {
  return useSyncExternalStore(subscribe, snapshot, snapshot);
}

export function retryAgentsDisplay(): Promise<void> {
  return state.change === undefined ? refreshAgentsDisplay() : saveAgentsDisplay(state.change);
}

// A session-list refresh also refreshes display settings. Rows subscribe to
// the choice they use so a fresh answer or error with the same choice does not
// redraw them.
const botmuxHidden = () => state.value?.show_botmux !== true;
const herdrTabName = () => state.value?.use_herdr_tab_name === true;
const botmuxShown = () => state.value?.show_botmux;
const displayEditable = () => state.value !== null && !state.saving;

export function useBotmuxHidden(): boolean {
  // An unavailable preference might hide these sessions. Keep the last acknowledged choice.
  return useSyncExternalStore(subscribe, botmuxHidden, botmuxHidden);
}

/** The acknowledged BotMux choice, undefined until the hub answers. */
export function useBotmuxShown(): boolean | undefined {
  return useSyncExternalStore(subscribe, botmuxShown, botmuxShown);
}

/** Whether the display choice is loaded and not being saved. */
export function useAgentsDisplayEditable(): boolean {
  return useSyncExternalStore(subscribe, displayEditable, displayEditable);
}

export function useHerdrTabName(): boolean {
  return useSyncExternalStore(subscribe, herdrTabName, herdrTabName);
}

const sortByActivity = () => state.value?.sort_by_activity === true;
const showFilter = () => state.value?.show_filter === true;
export const showFilterNow = showFilter;

/** Settings › Agents › "Sort Agents by recent activity": off until the hub says on (older hubs never do). */
export function useSortByActivity(): boolean {
  return useSyncExternalStore(subscribe, sortByActivity, sortByActivity);
}

/** Settings › Agents › "Show the Filter on the Agents list": off until the hub says on. */
export function useShowFilter(): boolean {
  return useSyncExternalStore(subscribe, showFilter, showFilter);
}

/** Tests: the hub's answer as already read (no request). */
export function seedAgentsDisplay(value: AgentsDisplay) {
  revision++;
  set({ value, saving: false });
}
