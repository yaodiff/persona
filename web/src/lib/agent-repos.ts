import { byAttention, stateOf } from "./format";
import { summaryState, type ChildStates } from "./herdr-tree";
import type { Machine, Session } from "./types";
import { workspaceKey, workspaceTarget, type HostWorkspace, type WorkspaceTarget } from "./workspace-catalog";
import { sessionHost } from "./agent-groups";

/** Where a session's cwd sits: the repository it belongs to and the working tree inside it. */
export type Repo = { repo: string; name: string; worktree: string; main: boolean };

export type RepoWorktree = {
  key: string;
  /** Its branch when the node says (#1805), else its folder's name (the main checkout: the repository's). A linked tree
   *  goes by the name its host gave the place instead, when its sessions agree on one (#4497, `hostPlaceName`). */
  name: string;
  /** The branch checked out there, when the node says. */
  branch?: string;
  path: string;
  main: boolean;
  sessions: Session[];
  /** Direct navigation candidates, including pinned Agents rendered elsewhere. */
  directSessions?: Session[];
  /** Direct navigation candidates throughout the subtree, including pinned Agents. */
  navigationTree?: Session[];
  workspace?: HostWorkspace;
  target?: WorkspaceTarget;
  /** Orca child worktrees, sorted by name. */
  children: RepoWorktree[];
  /** This worktree's own sessions, followed by every descendant's sessions. */
  tree: Session[];
  /** Direct child worktrees needing you, working, or done, by each child's whole-subtree summary. */
  childStates: ChildStates;
};

/** One repository on one machine, with its top-level working trees (the main checkout first, then by name). */
export type RepoTree = {
  key: string;
  machineId: string;
  name: string;
  repo: string;
  count: number;
  worktrees: RepoWorktree[];
};

/** One computer's section in the repository tree: its live repositories ordered by name. */
export interface RepoMachine {
  key: string;
  machine: Machine;
  foldKey: string;
  repos: RepoTree[];
  sessions: Session[];
  needs: number;
}

// The layouts a working tree commonly lives in (herdr, Claude Code, a home-level collection): the parts before the
// last one name the repository, everything from there on is the tree.
const WORKTREE_SEGMENTS = [["/.worktrees/"], ["/.claude/worktrees/"], ["/.herdr/worktrees/"]];

const segments = (path: string) => path.split("/").filter(Boolean);
const baseName = (path: string) => segments(path).pop() ?? "";

/**
 * The repository and working tree a cwd belongs to. The host may name the repository (the session's repo_root, #2840:
 * Paseo keeps its worktrees under its own folder): a cwd inside it is its main checkout, any other is one of its
 * working trees. Otherwise from the path alone (docs/web/web.md): a tree under one of the known layouts belongs to the
 * directory above that marker; any other path is its own repository's main checkout.
 */
export function repoOf(cwd: string, repoRoot?: string): Repo {
  if (!cwd) return { repo: "", name: "", worktree: "", main: true };
  const path = cwd.replace(/\/+$/, "");
  const root = (repoRoot ?? "").replace(/\/+$/, "");
  if (root) {
    const inside = path === root || path.startsWith(`${root}/`);
    return { repo: root, name: baseName(root), worktree: inside ? root : path, main: inside };
  }
  for (const [marker] of WORKTREE_SEGMENTS) {
    const at = path.indexOf(marker);
    if (at <= 0) continue;
    const rest = path.slice(at + marker.length).split("/").filter(Boolean);
    const repo = path.slice(0, at);
    // ~/.worktrees/<repo>/<tree>: the marker sits at the home level, so the repository is the first part after it and
    // the tree is what follows.
    if (rest.length >= 2) return { repo: `${repo}${marker}${rest[0]}`, name: rest[0]!, worktree: path, main: false };
    return { repo, name: baseName(repo), worktree: path, main: false };
  }
  return { repo: path, name: baseName(path), worktree: path, main: true };
}

const worktreeName = (path: string, repo: string) => (path === repo ? baseName(repo) : baseName(path));

/** The name the host gave the place a working tree's sessions run in (#4497: Paseo sends its workspace's title as
 *  slot_name, with no slot_id), when every session that gives one gives the same; two different names name nothing.
 *  A slot_name beside a slot_id is a tab's label (herdr), which names no working tree. */
function hostPlaceName(sessions: readonly Session[]): string | undefined {
  let name: string | undefined;
  for (const s of sessions) {
    const own = s.slot_id?.trim() ? undefined : s.slot_name?.trim();
    if (!own) continue;
    if (name !== undefined && own !== name) return undefined;
    name = own;
  }
  return name;
}

const trimmed = (path: string | undefined) => (path ?? "").replace(/\/+$/, "");

/** Where the node placed a session in git (#1805): its project (repo_root, else the working tree itself) and the
 *  working tree; undefined from an older node, which leaves the path inference above. */
export function gitPlaceOf(s: Pick<Session, "repo_root" | "worktree">): Repo | undefined {
  const worktree = trimmed(s.worktree);
  if (!worktree) return undefined;
  const repo = trimmed(s.repo_root) || worktree;
  return { repo, name: baseName(repo), worktree, main: worktree === repo };
}

type Placement = Repo & { path: string; spaceId?: string; git?: boolean };
type OrcaWorktree = { repoId: string; spaceId: string; path: string };

/** An Orca git worktree id is `<project id>::<absolute path>`; folder workspaces have no `::` and keep path inference. */
function orcaSpace(s: Session): OrcaWorktree | undefined {
  if (s.space_kind !== "orca" || !s.space_id) return undefined;
  const at = s.space_id.indexOf("::");
  if (at <= 0) return undefined;
  const repoId = s.space_id.slice(0, at);
  const path = s.space_id.slice(at + 2).replace(/\/+$/, "");
  return repoId && path ? { repoId, spaceId: s.space_id, path } : undefined;
}

/** The deepest known Orca worktree containing this session's cwd. A separate git worktree below a marker is not
 *  part of the containing Orca worktree. workspace_cwd is the node's verified fallback when cwd does not match. */
function containingOrcaWorktree(s: Session, known: ReadonlyMap<string, OrcaWorktree>): OrcaWorktree | undefined {
  const cwds = [s.cwd ?? ""];
  if (s.workspace_cwd && s.workspace_cwd !== s.cwd) cwds.push(s.workspace_cwd);
  for (const value of cwds) {
    const cwd = value.replace(/\/+$/, "");
    if (!cwd) continue;
    let deepest: OrcaWorktree | undefined;
    for (const [path, worktree] of known) {
      if (deepest && path.length <= deepest.path.length) continue;
      if (cwd !== path && !cwd.startsWith(`${path}/`)) continue;
      const below = cwd === path ? "" : cwd.slice(path.length + 1);
      if (WORKTREE_SEGMENTS.some(([marker]) => below.startsWith(marker.slice(1)))) continue;
      deepest = worktree;
    }
    if (deepest) return deepest;
  }
  return undefined;
}

/** Orca's exact project id wins for its own sessions and for any source running inside a known Orca worktree.
 *  Other sessions keep the host-supplied root or path inference. */
function placementOf(s: Session, known: ReadonlyMap<string, OrcaWorktree>): Placement {
  const git = gitPlaceOf(s);
  const orca = orcaSpace(s) ?? containingOrcaWorktree(s, known);
  if (orca) {
    // The node's git project joins an Orca worktree with every other source in the same repository; Orca's project id
    // is the fallback for older nodes.
    if (git) return { repo: git.repo, name: git.name, worktree: orca.spaceId, path: orca.path, main: git.main, spaceId: orca.spaceId, git: true };
    return { repo: `orca:${orca.repoId}`, name: baseName(orca.path), worktree: orca.spaceId, path: orca.path, main: false, spaceId: orca.spaceId };
  }
  if (git) return { ...git, path: git.worktree, git: true };
  if (s.repo_root) {
    const where = repoOf(s.cwd ?? "", s.repo_root);
    return { ...where, path: where.worktree };
  }
  const where = repoOf(s.cwd ?? "", undefined);
  return { ...where, path: where.worktree };
}

/** Prefer the repository name a member session's repo_root supplies. Without one Orca does not send the project name:
 *  guess it from distinct worktree folder names, preferring the most frequent, then the shortest, then alphabetical. */
function orcaProjectName(worktrees: readonly RepoWorktree[]): string {
  const roots = worktrees.flatMap((w) => w.sessions.map((s) => (s.repo_root ?? "").replace(/\/+$/, "")).filter(Boolean));
  if (roots.length) {
    const counts = new Map<string, number>();
    for (const root of roots) counts.set(root, (counts.get(root) ?? 0) + 1);
    const root = [...counts].sort((a, b) => b[1] - a[1] || a[0].length - b[0].length || a[0].localeCompare(b[0]))[0]?.[0] ?? "";
    return baseName(root);
  }
  const counts = new Map<string, number>();
  for (const path of new Set(worktrees.map((w) => w.path))) {
    const name = baseName(path);
    counts.set(name, (counts.get(name) ?? 0) + 1);
  }
  return [...counts].sort((a, b) => b[1] - a[1] || a[0].length - b[0].length || a[0].localeCompare(b[0]))[0]?.[0] ?? "";
}

/** Move Orca worktrees under their listed parent in the same project. A missing/self parent or any parent chain that
 *  loops leaves the worktree at the top level, as in By machine; then fill descendant sessions and state counts. */
function nestWorktrees(worktrees: RepoWorktree[], bySpace: ReadonlyMap<string, RepoWorktree>): RepoWorktree[] {
  const orca = new Set(bySpace.values());
  const parentOf = (w: RepoWorktree): RepoWorktree | undefined => {
    if (!orca.has(w)) return undefined;
    // Sessions attributed by cwd may carry their own unrelated parent id; only Orca-hosted sessions define this edge.
    const hosted = w.sessions.find((s) => s.parent_space_id && orcaSpace(s));
    const parent = hosted?.parent_space_id ? bySpace.get(hosted.parent_space_id) : undefined;
    return parent === w ? undefined : parent;
  };
  const loops = (w: RepoWorktree) => {
    const seen = new Set<RepoWorktree>([w]);
    for (let p = parentOf(w); p; p = parentOf(p)) {
      if (seen.has(p)) return true;
      seen.add(p);
    }
    return false;
  };
  const top: RepoWorktree[] = [];
  for (const w of worktrees) {
    const parent = loops(w) ? undefined : parentOf(w);
    if (parent) parent.children.push(w);
    else top.push(w);
  }
  for (const w of worktrees) w.children.sort((a, b) => a.name.localeCompare(b.name));
  const fill = (w: RepoWorktree): Session[] => {
    const below = w.children.flatMap(fill);
    const counts: ChildStates = { needs_you: 0, working: 0, done: 0 };
    for (const c of w.children) {
      const st = summaryState(c.tree);
      if (st === "needs_you" || st === "working") counts[st]++;
      else if (st === "unread" || st === "idle") counts.done++;
    }
    w.childStates = counts;
    w.tree = [...w.sessions, ...below];
    return w.tree;
  };
  top.forEach(fill);
  return top;
}

/** Whether By repo shows a worktree row, rather than putting a lone main or equivalently named worktree's agents
 *  directly under the project. A worktree with children always keeps its level, and so does a branch the node named
 *  (#1805: machine → project → branch → sessions, as Orca shows it). */
export function showsWorktreeLevel(tree: RepoTree): boolean {
  if (tree.worktrees.length !== 1) return true;
  const w = tree.worktrees[0]!;
  return Boolean(w.workspace) || w.children.length > 0 || Boolean(w.branch) || (!w.main && w.name !== tree.name);
}

type RepoBucket = { tree: RepoTree; orca: boolean; git: boolean; bySpace: Map<string, RepoWorktree> };

/** Build one computer's repositories. Learn Orca worktrees from live Orca-hosted sessions first, so every agent source
 *  running in one can share its exact identity; without a live Orca-hosted session its path is not known to be Orca's. */
function buildRepos(sessions: Session[], machineId: string): RepoTree[] {
  const known = new Map<string, OrcaWorktree>();
  for (const s of sessions) {
    const worktree = orcaSpace(s);
    if (worktree && !known.has(worktree.path)) known.set(worktree.path, worktree);
  }
  const repos = new Map<string, RepoBucket>();
  for (const s of sessions) {
    const where = placementOf(s, known);
    const key = `${machineId}\u0000${where.repo}`;
    let bucket = repos.get(key);
    if (!bucket) {
      bucket = {
        tree: { key, machineId, name: where.name, repo: where.repo, count: 0, worktrees: [] },
        orca: Boolean(where.spaceId),
        git: Boolean(where.git),
        bySpace: new Map(),
      };
      repos.set(key, bucket);
    }
    const { tree } = bucket;
    const identity = where.spaceId ?? where.worktree;
    const worktreeKey = `${key}\u0000${identity}`;
    let worktree = tree.worktrees.find((w) => w.key === worktreeKey);
    if (!worktree) {
      worktree = {
        key: worktreeKey,
        name: where.spaceId ? baseName(where.path) : worktreeName(where.worktree, where.repo),
        path: where.path,
        main: where.main,
        sessions: [],
        children: [],
        tree: [],
        childStates: { needs_you: 0, working: 0, done: 0 },
      };
      tree.worktrees.push(worktree);
    }
    if (where.spaceId && !bucket.bySpace.has(where.spaceId)) bucket.bySpace.set(where.spaceId, worktree);
    if (where.git) bucket.git = true;
    // Orca's sidebar and git name a working tree by its branch.
    if (!worktree.branch && s.branch) {
      worktree.branch = s.branch;
      worktree.name = s.branch;
    }
    worktree.sessions.push(s);
    tree.count++;
  }
  return [...repos.values()].map((bucket) => {
    const { tree } = bucket;
    const orcaTrees = new Set(bucket.bySpace.values());
    for (const w of tree.worktrees) {
      w.sessions.sort(byAttention);
      if (bucket.orca && !bucket.git) w.main = w.sessions.some((s) => (s.repo_root ?? "").replace(/\/+$/, "") === w.path);
      // A linked tree takes the name its host gave it (#4497), its branch staying beside it; its key stays its path.
      // The main checkout keeps its name (several workspaces can share it), and Orca's trees keep Orca's naming.
      if (!w.main && !orcaTrees.has(w)) w.name = hostPlaceName(w.sessions) ?? w.name;
    }
    // By the name shown, then by the key: two workspaces can have one title, and trees must not swap as sessions move.
    const worktrees = nestWorktrees(tree.worktrees, bucket.bySpace)
      .sort((a, b) => Number(b.main) - Number(a.main) || a.name.localeCompare(b.name) || a.key.localeCompare(b.key));
    return { ...tree, name: bucket.orca && !bucket.git ? orcaProjectName(tree.worktrees) : tree.name, worktrees };
  }).sort((a, b) => a.name.localeCompare(b.name));
}

/**
 * The sidebar's repository tree partitioned by computer (#2108 / #2126): each computer with live agents gets its own
 * section, kept in the hub's machine order; repositories within each computer sort alphabetically by name. Every live
 * agent is under the repository its cwd names, that repository's working trees under it: a tree's main checkout comes
 * first, the rest by name, and agents inside a tree keep the sidebar's own order (needs-you first).
 */
export interface RepoCatalogOptions {
  workspaces: readonly HostWorkspace[];
  owner: string;
  manager: string;
  /** Same filtered membership as all, plus pinned Agents (which are not drawn twice). */
  navigationSessions?: readonly Session[];
  hideEmpty?: boolean;
  search?: string;
  compareSessions?: (a: Session, b: Session) => number;
  hiddenSources?: readonly string[];
}

/** Exact runtime placement always wins. Directory inference is allowed only for an unbound Agent and a unique
 *  deepest containing directory. Host-owned bindings never fall through to a different runtime at the same path. */
export function sessionWorkspace(s: Session, catalog: readonly HostWorkspace[], manager?: string): HostWorkspace | undefined {
  if (manager && s.manager && s.manager !== manager) return undefined;
  const local = catalog.filter((w) => w.machine_id === s.machine_id);
  const host = s.space_kind || s.host || sessionHost(s);
  const bound = s.workspace_id || s.space_id;
  if (bound) {
    const exact = local.filter((w) => w.source_id === bound && (!host || w.source === host));
    if (exact.length === 1) return exact[0];
    // Desktop project ids describe an editor project, not a terminal workspace binding.
    if (exact.length > 1 || s.workspace_id || host !== "codex_app") return undefined;
  }
  if (["herdr", "herdr-core", "botmux", "orca", "paseo"].includes(host)) return undefined;
  for (const raw of [s.cwd, s.workspace_cwd]) {
    const cwd = catalogPath(raw);
    if (!cwd) continue;
    const candidates = local.filter((w) => {
      const path = catalogPath(w.cwd);
      if (!path || (cwd !== path && !(path === "/" ? cwd.startsWith("/") : cwd.startsWith(`${path}/`)))) return false;
      const below = cwd === path ? "" : cwd.slice(path === "/" ? 1 : path.length + 1);
      return !WORKTREE_SEGMENTS.some(([marker]) => below.startsWith(marker.slice(1)));
    });
    const length = Math.max(0, ...candidates.map((w) => catalogPath(w.cwd).length));
    const deepest = candidates.filter((w) => catalogPath(w.cwd).length === length);
    if (deepest.length === 1) return deepest[0];
    if (deepest.length > 1) return undefined;
  }
  return undefined;
}
const catalogPath = (value?: string) => {
  const path = (value ?? "").replaceAll("\\", "/");
  const clean = path.replace(/\/+$/, "") || (path.startsWith("/") ? "/" : "");
  return /^[a-z]:/i.test(clean) ? clean[0].toLowerCase() + clean.slice(1) : clean;
};

/** Runtime logical spaces share a repository display row at a verified checkout. Action attribution continues
 *  to use sessionWorkspace and its native binding rules. */
function repositoryWorkspace(s: Session, catalog: readonly HostWorkspace[], manager: string): HostWorkspace | undefined {
  const bound = sessionWorkspace(s, catalog, manager);
  if (bound || s.workspace_id || s.source_kind !== "runtime") return bound;
  const checkout = catalogPath(s.worktree);
  if (!checkout) return undefined;
  const candidate = sessionWorkspace({ ...s, space_id: undefined, cwd: checkout }, catalog, manager);
  return candidate && catalogPath(candidate.cwd) === checkout ? candidate : undefined;
}

/** Older hubs still get title navigation for pinned Agents, without rendering or counting them twice. */
function legacyNavigationRepos(sessions: Session[], machineId: string, options: RepoCatalogOptions): RepoTree[] {
  if (!options.navigationSessions) return buildRepos(sessions, machineId);
  const navigation = options.navigationSessions.filter((s) => !s.assistant && s.machine_id === machineId);
  const drawn = new Set(sessions);
  const fill = (w: RepoWorktree): Session[] => {
    w.directSessions = [...w.sessions].sort(options.compareSessions ?? byAttention);
    w.navigationTree = w.tree;
    w.sessions = w.sessions.filter((s) => drawn.has(s)).sort(options.compareSessions ?? byAttention);
    w.tree = [...w.sessions, ...w.children.flatMap(fill)];
    w.childStates = { needs_you: 0, working: 0, done: 0 };
    for (const child of w.children) {
      if (!child.tree.length) continue;
      const state = summaryState(child.tree);
      if (state === "needs_you" || state === "working") w.childStates[state]++;
      else if (state === "unread" || state === "idle") w.childStates.done++;
    }
    return w.tree;
  };
  return buildRepos(navigation, machineId).map((repo) => ({ ...repo, count: repo.worktrees.flatMap(fill).length }));
}

function catalogRepos(sessions: Session[], machineId: string, options: RepoCatalogOptions): RepoTree[] {
  // session-directory rows are the server's compatibility fallback, not host-owned workspaces or action targets.
  const inventory = options.workspaces.filter((w) => w.machine_id === machineId && w.source !== "session-directory");
  const catalog = inventory.filter((w) => !options.hiddenSources?.includes(w.source));
  if (!catalog.length) return legacyNavigationRepos(sessions, machineId, options);
  const keyOf = (w: HostWorkspace) => workspaceKey(w, options.owner, options.manager);
  const nodes = new Map<string, RepoWorktree>();
  for (const w of catalog) {
    const key = keyOf(w);
    nodes.set(key, {
      key, name: w.title || w.branch || baseName(w.cwd) || w.source_id, branch: w.branch,
      path: w.cwd, main: Boolean(w.is_main), workspace: w,
      target: workspaceTarget(w, options.owner, options.manager),
      sessions: [], directSessions: [], children: [], tree: [], childStates: { needs_you: 0, working: 0, done: 0 },
    });
  }
  const loose: Session[] = [];
  for (const s of sessions) {
    const w = repositoryWorkspace(s, inventory, options.manager);
    const node = w && nodes.get(keyOf(w));
    if (node) node.sessions.push(s); else loose.push(s);
  }
  for (const s of options.navigationSessions ?? sessions) {
    if (s.assistant || s.machine_id !== machineId) continue;
    const w = repositoryWorkspace(s, inventory, options.manager);
    if (w) nodes.get(keyOf(w))?.directSessions!.push(s);
  }
  const parentOf = (node: RepoWorktree) => {
    const w = node.workspace!;
    return w.parent_source_id ? nodes.get(keyOf({ ...w, source_id: w.parent_source_id })) : undefined;
  };
  const top: RepoWorktree[] = [];
  for (const node of nodes.values()) {
    const seen = new Set<RepoWorktree>([node]);
    let cycle = false;
    for (let parent = parentOf(node); parent; parent = parentOf(parent)) {
      if (seen.has(parent)) { cycle = true; break; }
      seen.add(parent);
    }
    const parent = !cycle && parentOf(node);
    if (parent) parent.children.push(node); else top.push(node);
  }
  const order = (a: RepoWorktree, b: RepoWorktree) => Number(b.main) - Number(a.main) || a.name.localeCompare(b.name) || a.key.localeCompare(b.key);
  const query = options.search?.trim().toLocaleLowerCase();
  const fill = (node: RepoWorktree): boolean => {
    node.sessions.sort(options.compareSessions ?? byAttention);
    node.directSessions!.sort(options.compareSessions ?? byAttention);
    const w = node.workspace!;
    const matches = !query || [node.name, node.branch, node.path, w.project_name, w.project_path, w.source].join(" ").toLocaleLowerCase().includes(query);
    if (!matches) {
      node.sessions = node.sessions.filter((s) => `${s.title} ${s.cwd} ${s.branch ?? ""}`.toLocaleLowerCase().includes(query!));
      node.directSessions = node.directSessions!.filter((s) => `${s.title} ${s.cwd} ${s.branch ?? ""}`.toLocaleLowerCase().includes(query!));
    }
    node.children = node.children.filter(fill).sort(order);
    node.tree = [...node.sessions, ...node.children.flatMap((c) => c.tree)];
    node.navigationTree = [...node.directSessions!, ...node.children.flatMap((c) => c.navigationTree ?? [])];
    for (const c of node.children) {
      if (!c.tree.length) continue;
      const state = summaryState(c.tree);
      if (state === "needs_you" || state === "working") node.childStates[state]++;
      else if (state === "unread" || state === "idle") node.childStates.done++;
    }
    return node.children.length > 0 || node.directSessions!.length > 0 || (!options.hideEmpty && matches);
  };
  const repos = new Map<string, RepoTree>();
  for (const root of top.filter(fill).sort(order)) {
    const w = root.workspace!;
    const project = w.project_id || w.project_path || w.source_id;
    const key = JSON.stringify([options.owner, options.manager, machineId, w.source, project]);
    let repo = repos.get(key);
    if (!repo) {
      repo = { key, machineId, name: w.project_name || baseName(w.project_path || "") || root.name, repo: w.project_path || `${w.source}:${project}`, count: 0, worktrees: [] };
      repos.set(key, repo);
    }
    repo.worktrees.push(root);
    repo.count += root.tree.length;
  }
  const unmatched = query ? loose.filter((s) => `${s.title} ${s.cwd}`.toLocaleLowerCase().includes(query)) : loose;
  const groupedNavigation = new Set([...nodes.values()].flatMap((node) => node.directSessions!));
  const navigation = options.navigationSessions?.filter((s) => !groupedNavigation.has(s)
    && (!query || `${s.title} ${s.cwd}`.toLocaleLowerCase().includes(query)));
  return [...repos.values(), ...legacyNavigationRepos(unmatched, machineId, { ...options, navigationSessions: navigation })].sort((a, b) => a.name.localeCompare(b.name) || a.key.localeCompare(b.key));
}

export function buildRepoMachineTree(all: Session[], machines: Machine[], options?: RepoCatalogOptions): RepoMachine[] {
  const online = new Set(machines.filter((m) => m.online).map((m) => m.id));
  const byMachine = new Map<string, Session[]>();
  for (const s of all) {
    if (s.assistant || !online.has(s.machine_id)) continue;
    let list = byMachine.get(s.machine_id);
    if (!list) {
      list = [];
      byMachine.set(s.machine_id, list);
    }
    list.push(s);
  }

  const out: RepoMachine[] = [];
  for (const m of machines) {
    const sessions = byMachine.get(m.id);
    const workspaces = options?.workspaces.some((w) => w.machine_id === m.id && w.source !== "session-directory");
    const navigation = m.online && options?.navigationSessions?.some((s) => !s.assistant && s.machine_id === m.id);
    if ((!sessions || sessions.length === 0) && !workspaces && !navigation) continue;
    const repos = options ? catalogRepos(sessions ?? [], m.id, options) : buildRepos(sessions!, m.id);
    if (!repos.length) continue;
    out.push({
      key: `repo-machine:${m.id}`,
      machine: m,
      foldKey: `repo-machine:${m.id}`,
      repos,
      sessions: sessions ?? [],
      needs: (sessions ?? []).filter((s) => stateOf(s) === "needs_you").length,
    });
  }
  return out;
}
