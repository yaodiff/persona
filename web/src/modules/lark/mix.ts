// Settings › Connectors › Lark › "Personal Agents in Messages" (docs/extensions/lark-migration.md "Personal Agents in
// Messages"): whether the Messages page lists the personal Agents with the Feishu chats. On by default (it changes
// nothing while there are no personal Agents), kept by this browser like "Show messages on home" (home.ts).
import { useSyncExternalStore } from "react";

const MIX_KEY = "lark.messagesAgents";
const listeners = new Set<() => void>();

export function getMessagesMix(): boolean {
  try {
    return localStorage.getItem(MIX_KEY) !== "0";
  } catch {
    return true;
  }
}

export function setMessagesMix(on: boolean) {
  try {
    if (on) localStorage.removeItem(MIX_KEY);
    else localStorage.setItem(MIX_KEY, "0");
  } catch {
    // storage refused (private mode): nothing to keep it in
  }
  listeners.forEach((l) => l());
}

if (typeof window !== "undefined") {
  window.addEventListener("storage", (e) => {
    if (e.key === MIX_KEY || e.key === null) listeners.forEach((l) => l());
  });
}

/** Whether this browser's Messages page lists personal Agents too (on unless turned off). */
export function useMessagesMix(): boolean {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => {
        listeners.delete(l);
      };
    },
    getMessagesMix,
    () => true,
  );
}
