import { storedValue, storeValue } from "../lib/compatibility";
import { useSyncExternalStore } from "react";

export type Theme = "system" | "light" | "dark";

const listeners = new Set<() => void>();
const media = matchMedia("(prefers-color-scheme: dark)");

function current(): Theme {
  return (storedValue("persona.theme") as Theme) || "system";
}

function apply() {
  const t = current();
  document.documentElement.classList.toggle("dark", t === "dark" || (t === "system" && media.matches));
  // The browser's / installed app's bar (index.html's theme-color metas): the system's scheme, or the one picked here.
  document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]').forEach((m) => {
    m.media = t === "system" ? `(prefers-color-scheme: ${m.dataset.scheme})` : m.dataset.scheme === t ? "all" : "not all";
  });
}

media.addEventListener("change", apply);

// Another tab of this browser picked a theme (or its storage was cleared): this one follows at once, and its Settings
// show the theme it shows (they read the stored choice).
if (typeof window !== "undefined")
  window.addEventListener("storage", (e) => {
    if (e.key !== null && e.key !== "persona.theme") return;
    apply();
    listeners.forEach((l) => l());
  });

export function setTheme(t: Theme) {
  storeValue("persona.theme", t);
  apply();
  listeners.forEach((l) => l());
}

export function useTheme(): Theme {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    current,
  );
}
