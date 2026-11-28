import { useSyncExternalStore } from "react";
import { api, ApiError, credentialOwner, getToken, onCredentialChange } from "../lib/api";
import { cacheSharedFonts, fontSettings, normalizeFonts, setFontChoice, type FontChoice, type FontKind, type FontPreference } from "./font-preference";

const path = "/settings/web-fonts";
type SyncStatus = "ready" | "saving" | "local" | "error";
let status: SyncStatus = "ready";
const listeners = new Set<() => void>();
const subscribe = (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener); }; };
export const useFontSync = () => useSyncExternalStore(subscribe, () => status);
function setStatus(next: SyncStatus) {
  status = next;
  listeners.forEach((listener) => listener());
}

let revision = 0;
let reading: Promise<void> | undefined;
onCredentialChange(() => {
  revision++;
  reading = undefined;
  setStatus("ready");
});

export function refreshSharedFonts(): Promise<void> {
  const owner = credentialOwner();
  if (!getToken() || status === "saving") return Promise.resolve();
  if (reading) return reading;
  const mine = revision;
  const work = (async () => {
    try {
      const value = await api<FontPreference>(path);
      if (owner !== credentialOwner() || mine !== revision) return;
      cacheSharedFonts(normalizeFonts(value));
      if (status !== "error") setStatus("ready");
    } catch (error) {
      if (owner === credentialOwner() && mine === revision && error instanceof ApiError && error.status === 404) setStatus("local");
    }
  })();
  reading = work;
  void work.finally(() => { if (reading === work) reading = undefined; });
  return work;
}

/** Save just the changed family so two browsers can edit different families independently. */
export async function saveFontChoice(kind: FontKind, choice: FontChoice): Promise<boolean> {
  const owner = credentialOwner();
  if (status === "saving") return false;
  if (fontSettings().browser || status === "local" || !getToken()) return setFontChoice(kind, choice);
  const mine = ++revision;
  setStatus("saving");
  try {
    const saved = await api<FontPreference>(path, { method: "PUT", json: { [kind]: choice } });
    if (owner !== credentialOwner() || mine !== revision) return false;
    cacheSharedFonts(normalizeFonts(saved));
    setStatus("ready");
    return true;
  } catch (error) {
    if (owner !== credentialOwner() || mine !== revision) return false;
    if (error instanceof ApiError && error.status === 404) {
      setStatus("local");
      return setFontChoice(kind, choice);
    }
    setStatus("error");
    return false;
  }
}

export function startFontSync() {
  const refresh = () => { void refreshSharedFonts(); };
  onCredentialChange(() => { queueMicrotask(refresh); });
  addEventListener("focus", refresh);
  refresh();
}
