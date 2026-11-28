// Inbox folders (docs/assistant/inbox.md "Folders", #2463): each item can be filed under a "/"-separated path
// ("work/alerts/engine"), every segment a folder level. The hub's triage model files them (when the user turned it on)
// and the user can move one by hand. The tree is built here from the items' paths; the settings (on / off, the user's
// folder rules) are the hub's (GET / PUT /api/v1/inbox/folders).

import { useEffect, useSyncExternalStore } from "react";
import { api } from "./api";
import { UNFILED, type Filed } from "./inbox-folder-filter";
import { itemHeaders } from "./manager-choice";

export { inFolderFilter, UNFILED } from "./inbox-folder-filter";

const MAX_DEPTH = 6;
const MAX_SEGMENT = 40;

/** A path as the hub keeps it (assistant.normalizeFolder): segments trimmed, inner spaces collapsed, empty / "." / ".."
 *  segments dropped, each at most 40 characters, at most 6 levels. "" is no folder. */
export function normalizeFolder(s: string): string {
  const out: string[] = [];
  for (const raw of s.replace(/\\/g, "/").split("/")) {
    // eslint-disable-next-line no-control-regex
    let seg = raw.replace(/[\u0000-\u001f\u007f]/g, " ").trim().replace(/\s+/g, " ");
    if (!seg || seg === "." || seg === "..") continue;
    if ([...seg].length > MAX_SEGMENT) seg = [...seg].slice(0, MAX_SEGMENT).join("").trim();
    if (out.length === MAX_DEPTH) break;
    out.push(seg);
  }
  return out.join("/");
}

/** A folder's name: its last segment. */
export function folderName(path: string): string {
  return path.slice(path.lastIndexOf("/") + 1);
}

/** The crumbs down to a folder: each level's name and path ("work", "work/alerts"…). */
export function folderCrumbs(path: string): { name: string; path: string }[] {
  if (!path || path === UNFILED) return [];
  const segs = path.split("/");
  return segs.map((name, i) => ({ name, path: segs.slice(0, i + 1).join("/") }));
}

/** A folder one level under another, with what is in it and under it. */
export interface FolderNode {
  path: string;
  name: string;
  /** Items in it and under it, and how many of them are open. */
  count: number;
  open: number;
}

/** The folders right under `parent` ("" the top), each with the items in and under it, by name. */
export function childFolders(items: Filed[], parent: string): FolderNode[] {
  const prefix = parent ? `${parent}/` : "";
  const by = new Map<string, FolderNode>();
  for (const it of items) {
    const path = it.folder ?? "";
    if (!path || !path.startsWith(prefix) || path === parent) continue;
    const name = path.slice(prefix.length).split("/")[0];
    const at = prefix + name;
    const n = by.get(at) ?? { path: at, name, count: 0, open: 0 };
    n.count++;
    if (!it.done_at) n.open++;
    by.set(at, n);
  }
  return [...by.values()].sort((a, b) => a.name.localeCompare(b.name));
}

/** How many of the items are in no folder (and how many of those are open). */
export function unfiledCount(items: Filed[]): { count: number; open: number } {
  let count = 0;
  let open = 0;
  for (const it of items) {
    if (it.folder) continue;
    count++;
    if (!it.done_at) open++;
  }
  return { count, open };
}

/** Every folder the items name, with the levels above each, in order: what "Move to folder…" suggests. */
export function knownFolders(items: Filed[]): string[] {
  const all = new Set<string>();
  for (const it of items) for (const c of folderCrumbs(it.folder ?? "")) all.add(c.path);
  return [...all].sort((a, b) => a.localeCompare(b));
}

// ── settings (the hub's) ─────────

/** The folder settings: whether the model files items, and the user's folder rules. */
export interface FolderSettings {
  enabled: boolean;
  rules: string;
}

let settings: FolderSettings | null = null;
let loading: Promise<void> | null = null;
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());

/** Reads the settings (the default manager's) once; an older hub without them leaves folders off. */
export function loadFolderSettings(force = false): Promise<void> {
  if (loading && !force) return loading;
  loading = api<Partial<FolderSettings>>("/inbox/folders", { headers: itemHeaders(undefined) }).then(
    (r) => {
      settings = { enabled: Boolean(r?.enabled), rules: r?.rules ?? "" };
      emit();
    },
    () => {
      settings = settings ?? { enabled: false, rules: "" };
      emit();
    },
  );
  return loading;
}

/** Saves what is given; resolves to the settings the hub answered (throws when it refused). */
export async function saveFolderSettings(patch: Partial<FolderSettings>): Promise<FolderSettings> {
  const r = await api<Partial<FolderSettings>>("/inbox/folders", { method: "PUT", json: patch, headers: itemHeaders(undefined) });
  settings = { enabled: Boolean(r?.enabled), rules: r?.rules ?? "" };
  emit();
  return settings;
}

/** The folder settings, read on first use; null until they are. */
export function useFolderSettings(): FolderSettings | null {
  useEffect(() => void loadFolderSettings(), []);
  return useSyncExternalStore((l) => (listeners.add(l), () => void listeners.delete(l)), () => settings, () => settings);
}

/** Tests: forget what was read. */
export function resetFolderSettings() {
  settings = null;
  loading = null;
}
