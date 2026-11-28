import { useSyncExternalStore } from "react";

const key = "persona.confirmOutwardActions";

let tabOverride: boolean | undefined;

export function confirmationEnabled(): boolean {
  if (tabOverride !== undefined) return tabOverride;
  try { return localStorage.getItem(key) !== "0"; }
  catch { return true; }
}

export function setConfirmationEnabled(enabled: boolean) {
  try {
    if (enabled) localStorage.removeItem(key);
    else localStorage.setItem(key, "0");
    tabOverride = undefined;
  } catch {
    // Storage is blocked in some browsers. Keep the explicit choice for the current page.
    tabOverride = enabled;
  }
  window.dispatchEvent(new Event(key));
}

function subscribe(listener: () => void) {
  window.addEventListener(key, listener);
  window.addEventListener("storage", listener);
  return () => {
    window.removeEventListener(key, listener);
    window.removeEventListener("storage", listener);
  };
}

export const useApprovalConfirmation = () => useSyncExternalStore(subscribe, confirmationEnabled, () => true);
