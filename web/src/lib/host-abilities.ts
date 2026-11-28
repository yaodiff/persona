// What each terminal host on a computer can do with its workspaces, tabs and panes (#17053). The node says it per host in
// `GET /machines/{mid}/panes` (`abilities`, server/internal/proto/panes.go), read from what the host implements, so a
// host that gains an ability (Orca splitting panes, say) gets its menu items without a client change. A node that
// predates it does not say, and the table here stands in for what such a node does. Menus (lib/node-actions.ts), the
// pane toolbar and the rename-space checks read this one rule, never an engine's name. No translated words here: the
// sidebar reads it on the first screen.
import type { PanesResponse } from "./terminals";
import type { Session } from "./types";

export interface HostAbilities {
  /** Rename a workspace or a tab (`spaces/{wid}/rename`, `tabs/rename`). */
  rename: boolean;
  /** Close one pane (`panes/close`). */
  closePane: boolean;
  /** Split a pane (`panes/split`). */
  split: boolean;
  /** Close a whole tab (`tabs/close`). */
  closeTab: boolean;
  /** Git ▸ Pull / Push / Fetch in a pane's or a workspace's folder (`panes/git`). */
  git: boolean;
  /** Run agent here… in a plain shell pane (`panes/agent`). */
  runHere: boolean;
}

/** `abilities` as the node sends it. */
export interface PaneAbilitiesDTO {
  rename?: boolean;
  close_pane?: boolean;
  split?: boolean;
  close_tab?: boolean;
  git?: boolean;
  run_here?: boolean;
}

const NONE: HostAbilities = { rename: false, closePane: false, split: false, closeTab: false, git: false, runHere: false };
const ALL: HostAbilities = { rename: true, closePane: true, split: true, closeTab: true, git: true, runHere: true };

/** What a node that does not report abilities does, by host: classic herdr all of it; Orca renames, closes a pane and runs
 *  Git; Tern and Rex rename; any other host is not asked. */
const FALLBACK: Record<string, HostAbilities> = {
  herdr: ALL,
  orca: { ...NONE, rename: true, closePane: true, git: true },
  tern: { ...NONE, rename: true },
  rex: { ...NONE, rename: true },
};

/** A host's abilities when its node does not say; "" and unset are herdr, the host most computers have. */
export function fallbackAbilities(host: string | undefined): HostAbilities {
  return FALLBACK[host || "herdr"] ?? NONE;
}

/** A listed host's abilities: the node's own when it reports them, else the fallback for its kind. */
export function abilitiesOf(h: { kind: string; abilities?: PaneAbilitiesDTO | null }): HostAbilities {
  const a = h.abilities;
  if (!a) return fallbackAbilities(h.kind);
  return {
    rename: a.rename === true,
    closePane: a.close_pane === true,
    split: a.split === true,
    closeTab: a.close_tab === true,
    git: a.git === true,
    runHere: a.run_here === true,
  };
}

// What each computer's node last reported, by `${mid}/${host}`: the terminal page and the sidebar's terminal tree read
// the panes list, and the menus of a session's row or chat (which never read it) follow what was seen there.
const reported = new Map<string, HostAbilities>();
const listeners = new Set<() => void>();
let version = 0;

const same = (a: HostAbilities, b: HostAbilities) =>
  a.rename === b.rename && a.closePane === b.closePane && a.split === b.split && a.closeTab === b.closeTab && a.git === b.git && a.runHere === b.runHere;

/** Keeps what a computer's panes list says each of its hosts can do; a host it no longer lists falls back to the table. */
export function rememberHostAbilities(mid: string, resp: PanesResponse | undefined): void {
  let changed = false;
  const seen = new Set<string>();
  for (const h of resp?.hosts ?? []) {
    const key = `${mid}/${h.kind}`;
    seen.add(key);
    const next = abilitiesOf(h);
    const prev = reported.get(key);
    if (!prev || !same(prev, next)) {
      reported.set(key, next);
      changed = true;
    }
  }
  for (const key of [...reported.keys()]) {
    if (key.startsWith(`${mid}/`) && !seen.has(key)) {
      reported.delete(key);
      changed = true;
    }
  }
  if (changed) {
    version++;
    for (const l of listeners) l();
  }
}

/** What a host on a computer can do: what its node last reported, else the fallback for its kind. */
export function hostAbilities(mid: string, host: string | undefined): HostAbilities {
  return reported.get(`${mid}/${host || "herdr"}`) ?? fallbackAbilities(host);
}

/** For useSyncExternalStore: a component that draws abilities draws again when a computer reports new ones. */
export function subscribeHostAbilities(l: () => void): () => void {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}
export const hostAbilitiesVersion = () => version;

/** An Orca native chat (a runtime session in an Orca worktree, server/internal/node/orcabridge.go) whose computer's Orca
 *  renames worktrees: its space is the worktree, renamed in Orca like a terminal's (#17053). */
export function orcaChatRenamesSpace(s: Pick<Session, "machine_id" | "space_id" | "space_kind" | "source_kind">): boolean {
  return Boolean(s.space_id) && s.source_kind === "runtime" && s.space_kind === "orca" && hostAbilities(s.machine_id, "orca").rename;
}

/** Tests: forget every report. */
export function resetHostAbilities(): void {
  reported.clear();
  version++;
}
