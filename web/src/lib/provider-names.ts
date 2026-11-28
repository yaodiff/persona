// What a model provider or launcher is called for people (#5029, docs/integrations/launchers.md "Display names"): its
// display name, in any language, with the startup ID (`name`, what a launch, a preset or the model settings refer to)
// as secondary text. A launcher without a display name (an rc alias, an older hub) keeps showing its ID. Every picker
// that names one goes through these, so a provider reads the same everywhere.
// A place that holds only a launcher's ID (a task, a schedule, a recent setup) looks the title up in useProviderNames:
// the model configuration's providers (GET /model-config, whose `name` the hub sets to the display name), read once a
// minute at most and shared by every place on the page.
import { useEffect, useState, useSyncExternalStore } from "react";
import { isTraeCli, normalizeTraeCliName } from "./agent-kind";
import { api } from "./api";
import { MANAGER_HEADER } from "./manager-choice";

/** Anything with a startup ID and maybe a display name: a `Launcher` (GET /machines/{mid}/launchers), a `ProviderView`
 *  (GET /launchers), a preset's add result. */
export interface Named {
  name: string;
  display_name?: string;
}

const BUILTIN_TITLES: Record<string, string> = {
  claude: "Claude Code",
  codex: "Codex",
  "claude-desktop": "Claude Desktop",
  "codex-app": "Codex App",
  agy: "Antigravity CLI",
  omp: "Oh My Pi",
  cursor: "Cursor",
  gemini: "Gemini CLI",
};

const shown = (l: Named) => l.display_name?.trim() ?? "";

/** The name to show first: the display name, else a built-in product name, else the startup ID. */
export function providerTitle(l: Named): string {
  const d = shown(l);
  if (isTraeCli(l.name)) return normalizeTraeCliName(d || l.name);
  const builtin = Object.hasOwn(BUILTIN_TITLES, l.name) ? BUILTIN_TITLES[l.name] : undefined;
  if (d) {
    const lower = d.toLowerCase();
    if (lower !== l.name.toLowerCase() && (!builtin || lower !== builtin.toLowerCase())) return d;
  }
  return builtin ?? (d || l.name);
}

/** The startup ID to show as secondary text (small monospace or a tooltip), or "" when the title already is the ID or a built-in title. */
export function providerIdNote(l: Named): string {
  if (l.name === "traex") return "";
  const d = shown(l);
  if (!d) return "";
  const lower = d.toLowerCase();
  if (lower === l.name.toLowerCase()) return "";
  if (Object.hasOwn(BUILTIN_TITLES, l.name) && lower === BUILTIN_TITLES[l.name].toLowerCase()) return "";
  return l.name;
}

/** One line for a place that holds only plain text (a native `<option>`, a title attribute): "Team relay (team-relay)". */
export function providerLabel(l: Named): string {
  const id = providerIdNote(l);
  return id ? `${providerTitle(l)} (${id})` : providerTitle(l);
}

/** The launcher with startup ID `name` in `list`, as a `Named`; one that is not in it stands for itself. */
export function namedLauncher(name: string, list: readonly Named[] | null | undefined): Named {
  return list?.find((l) => l.name === name) ?? { name };
}

/** The title of the launcher with startup ID `name`, looked up in `list` (the ID itself when it is not there). */
export function launcherTitle(name: string, list: readonly Named[] | null | undefined): string {
  return providerTitle(namedLauncher(name, list));
}

/** The launcher providers of GET /model-config as `Named`: `launcher.<id>` with the hub's name for it. The hub's own
 *  "Claude Code default" stand-in for plain `claude` is a label, not a display name, so it is left out. */
export function namesFromProviders(providers: readonly { id: string; name?: string }[] | null | undefined): Named[] {
  const out: Named[] = [];
  for (const p of providers ?? []) {
    if (!p.id.startsWith("launcher.")) continue;
    const name = p.id.slice("launcher.".length);
    if (!name || name === "claude") continue;
    const d = p.name?.trim();
    out.push(d && d !== name ? { name, display_name: d } : { name });
  }
  return out;
}

const NAMES_TTL_MS = 60_000;
const namesCache = new Map<string, { at: number; list: Promise<Named[]> }>();

/** The providers' names as `useProviderNames` reads them, shared by every caller for a minute; a failure (an older hub,
 *  no model configuration) is an empty list, so places show IDs as before. */
export function loadProviderNames(manager = ""): Promise<Named[]> {
  const hit = namesCache.get(manager);
  if (hit && Date.now() - hit.at < NAMES_TTL_MS) return hit.list;
  // the request itself, not model-config's helper: that module stays off the first screen, which plan cards are on
  const list = api<{ providers?: { id: string; name?: string }[] }>("/model-config", { headers: manager ? { [MANAGER_HEADER]: manager } : undefined })
    .then((r) => namesFromProviders(r.providers))
    .catch(() => {
      namesCache.delete(manager);
      return [];
    });
  namesCache.set(manager, { at: Date.now(), list });
  return list;
}

// Bumped by forgetProviderNames: every mounted reader reads the names again.
let namesVersion = 0;
const namesListeners = new Set<() => void>();
const onNamesForgotten = (l: () => void) => {
  namesListeners.add(l);
  return () => void namesListeners.delete(l);
};
const currentNamesVersion = () => namesVersion;

/** Forget the names read so far (a provider added, renamed or removed): the places showing them ask the hub again. */
export function forgetProviderNames(): void {
  namesCache.clear();
  namesVersion++;
  for (const l of namesListeners) l();
}

/** The providers' names for a place that holds only launcher IDs; [] until they are in. */
export function useProviderNames(manager = ""): Named[] {
  const [list, setList] = useState<Named[]>([]);
  const version = useSyncExternalStore(onNamesForgotten, currentNamesVersion, currentNamesVersion);
  useEffect(() => {
    let live = true;
    void loadProviderNames(manager).then((l) => live && setList(l));
    return () => {
      live = false;
    };
  }, [manager, version]);
  return list;
}
