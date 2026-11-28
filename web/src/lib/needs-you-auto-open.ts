// Whether entering Main chat from the expanded sidebar or collapsed rail automatically opens Needs you. This
// browser keeps the tray collapsed unless the owner opts in under Settings › General › Appearance (#17061).
import { useSyncExternalStore } from "react";

const KEY = "persona.needsYouAutoOpen";
const listeners = new Set<() => void>();

export function needsYouAutoOpen(): boolean {
  try {
    return localStorage.getItem(KEY) === "1";
  } catch {
    return false;
  }
}

export function setNeedsYouAutoOpen(on: boolean): void {
  try {
    if (on) localStorage.setItem(KEY, "1");
    else localStorage.removeItem(KEY);
  } catch {
    // Storage unavailable: nothing is kept and the default (off) stays in force.
  }
  listeners.forEach((listener) => listener());
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  window.addEventListener("storage", listener);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", listener);
  };
}

export const useNeedsYouAutoOpen = () => useSyncExternalStore(subscribe, needsYouAutoOpen, () => false);
