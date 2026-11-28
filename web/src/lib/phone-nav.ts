// Settings › Advanced › Appearance › "Phone navigation" (#16761): how a narrow screen moves between its main places. Top switch,
// the default, is the "Assistant | Agents" switch in the middle of the top bar (views/ShellSwitch.tsx, #1996), as it
// always was. Tab bar puts four places along the bottom instead (views/PhoneTabBar.tsx): the main chat, Agents, the
// chats external sources opened (Lark /agent and other extensions, `/history/external`) and Settings, for people who
// use Persona mainly as a console for their Agents. Wide screens keep the sidebar either way. Kept in this browser only,
// like the sidebar icons (lib/sidebar-icons.ts); Top switch stores nothing, so the page is what it was before this
// existed, and other tabs follow at once.
import { useSyncExternalStore } from "react";

export const PHONE_NAV_MODES = ["switch", "tabs"] as const;
export type PhoneNavMode = (typeof PHONE_NAV_MODES)[number];
export const PHONE_NAV_KEY = "persona.phone-nav";

const listeners = new Set<() => void>();
const notify = () => listeners.forEach((l) => l());
const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => void listeners.delete(l);
};

export function phoneNavMode(): PhoneNavMode {
  try {
    return localStorage.getItem(PHONE_NAV_KEY) === "tabs" ? "tabs" : "switch";
  } catch {
    return "switch";
  }
}

export function setPhoneNavMode(mode: PhoneNavMode) {
  try {
    if (mode === "tabs") localStorage.setItem(PHONE_NAV_KEY, mode);
    else localStorage.removeItem(PHONE_NAV_KEY);
  } catch {
    // not kept: the next page starts with the default
  }
  notify();
}

// Another tab of this browser changed it (or its storage was cleared): this one follows.
if (typeof window !== "undefined")
  addEventListener("storage", (e) => {
    if (e.key === null || e.key === PHONE_NAV_KEY) notify();
  });

// the same on a server render: Top switch without a browser's storage
export const usePhoneNavMode = () => useSyncExternalStore(subscribe, phoneNavMode, phoneNavMode);
