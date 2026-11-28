import { baseName, byAttention, stateOf } from "./format";
import { summaryState, type ChildStates } from "./herdr-tree";
import type { Machine, Session } from "./types";
import { byWorkspacePin, workspaceKey, workspacePinTime, workspaceTarget, type HostWorkspace } from "./workspace-catalog";
import { sessionHost } from "./agent-groups";
import { directoryKey } from "./workspace-directory";
import {
  APART_SEGMENTS, pathKey, repoOf, showsWorktreeLevel, trimmed, withinPath, WORKTREE_SEGMENTS,
  type Repo, type RepoCatalogOptions, type RepoMachine, type RepoTree, type RepoWorktree,
} from "./agent-repos-core";

export { repoOf, showsWorktreeLevel, type Repo, type RepoCatalogOptions, type RepoMachine, type RepoTree, type RepoWorktree };

/** A working tree's name when neither a branch nor its host names it. The main checkout goes by the repository's folder,
 *  and a linked tree by its own folder. When that folder carries the repository's name too, the linked tree goes by the
 *  folder holding it instead, so it never reads like the repository: the Codex app keeps
 *  `~/.codex/worktrees/<name>/<repository>` (#12800). */
function worktreeName(path: string, repo: string): string {
  if (pathKey(path) === pathKey(repo)) return baseName(repo);
  const parts = trimmed(path).split("/").filter(Boolean);
  const own = parts.pop() ?? "";
  return baseName(pathKey(path)) === baseName(pathKey(repo)) ? (parts.pop() ?? own) : own;
}

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

/** Where the node placed a session in git (#1805): its project (repo_root, else the working tree itself) and the
 *  working tree; undefined from an older node, which leaves the path inference above. */
export function gitPlaceOf(s: Pick<Session, "repo_root" | "worktree">): Repo | undefined {
  const worktree = trimmed(s.worktree);
  if (!worktree) return undefined;
  const repo = trimmed(s.repo_root) || worktree;
  return { repo, name: baseName(repo), worktree, main: pathKey(worktree) === pathKey(repo) };
}

/** How many folders below the top of its Git working tree a session runs, when the node says. The top is its reported
 *  tree, else the main checkout for a cwd inside repo_root, else the cwd itself (0: a linked tree kept outside the
 *  repository, such as a desktop app's worktree folder). Git reports physical paths, so this counts from the
 *  node-verified physical cwd when there is one; a count, unlike a path, also holds for the cwd as given when a
 *  symlinked folder leads to it. Undefined without Git metadata. */
function gitTreeDepth(s: Pick<Session, "repo_root" | "worktree" | "cwd" | "workspace_cwd">): number | undefined {
  const tree = catalogPath(s.worktree) || catalogPath(s.repo_root);
  if (!tree) return undefined;
  const cwd = catalogPath(s.workspace_cwd) || catalogPath(s.cwd);
  return cwd && within(cwd, tree) ? depthOf(cwd) - depthOf(tree) : 0;
}

type Placement = Repo & { path: string; spaceId?: string; git?: boolean };
type OrcaWorktree = { repoId: string; spaceId: string; path: string };

/** An Orca git worktree id is `<project id>::<absolute path>`; folder workspaces have no `::` and keep path inference. */
function orcaSpace(s: Session): OrcaWorktree | undefined {
  if (s.space_kind !== "orca" || !s.space_id) return undefined;
  const at = s.space_id.indexOf("::");
  if (at <= 0) return undefined;
  const repoId = s.space_id.slice(0, at);
  const path = trimmed(s.space_id.slice(at + 2));
  return repoId && path ? { repoId, spaceId: s.space_id, path } : undefined;
}

/** The deepest known Orca worktree containing this session's cwd. A separate git worktree below a marker is not
 *  part of the containing Orca worktree. workspace_cwd is the node's verified fallback when cwd does not match. */
function containingOrcaWorktree(s: Session, known: ReadonlyMap<string, OrcaWorktree>): OrcaWorktree | undefined {
  const cwds = [s.cwd ?? ""];
  if (s.workspace_cwd && s.workspace_cwd !== s.cwd) cwds.push(s.workspace_cwd);
  for (const value of cwds) {
    const cwd = pathKey(value);
    if (!cwd) continue;
    let deepest: OrcaWorktree | undefined;
    for (const [path, worktree] of known) {
      if (deepest && path.length <= pathKey(deepest.path).length) continue;
      if (!withinPath(cwd, path)) continue;
      const below = cwd === path ? "" : cwd.slice(path.length + (path.endsWith("/") ? 0 : 1));
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
  const roots = worktrees.flatMap((w) => w.sessions.map((s) => trimmed(s.repo_root)).filter(Boolean));
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

type RepoBucket = { tree: RepoTree; orca: boolean; git: boolean; bySpace: Map<string, RepoWorktree> };

/** Build one computer's repositories. Learn Orca worktrees from live Orca-hosted sessions first, so every agent source
 *  running in one can share its exact identity; without a live Orca-hosted session its path is not known to be Orca's. */
function buildRepos(sessions: Session[], machineId: string): RepoTree[] {
  const known = new Map<string, OrcaWorktree>();
  for (const s of sessions) {
    const worktree = orcaSpace(s);
    if (worktree && !known.has(pathKey(worktree.path))) known.set(pathKey(worktree.path), worktree);
  }
  const repos = new Map<string, RepoBucket>();
  for (const s of sessions) {
    const where = placementOf(s, known);
    const key = `${machineId}\u0000${pathKey(where.repo)}`;
    let bucket = repos.get(key);
    if (!bucket) {
      bucket = {
        tree: { key, machineId, name: where.name, repo: where.repo, count: 0, worktrees: [], ...(where.apart && { apart: true }) },
        orca: Boolean(where.spaceId),
        git: Boolean(where.git),
        bySpace: new Map(),
      };
      repos.set(key, bucket);
    }
    const { tree } = bucket;
    const identity = where.spaceId ?? pathKey(where.worktree);
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
      if (bucket.orca && !bucket.git) w.main = w.sessions.some((s) => pathKey(s.repo_root) === pathKey(w.path));
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

/** Exact runtime placement always wins. Directory inference is allowed only for an unbound Agent and a unique
 *  deepest containing directory, inside its Git working tree when the node reports one: a folder above that tree (the
 *  home folder, a folder of repositories) holds it on disk but is not where it works. Host-owned bindings never fall
 *  through to a different runtime at the same path. */
export function sessionWorkspace(s: Session, catalog: readonly HostWorkspace[], manager?: string): HostWorkspace | undefined {
  if (manager && s.manager && s.manager !== manager) return undefined;
  let local = catalog.filter((w) => w.machine_id === s.machine_id);
  const host = s.space_kind || s.host || sessionHost(s);
  const bound = s.workspace_id || s.space_id;
  let inferredProjectRoot: string | undefined;
  if (bound) {
    const exact = local.filter((w) => w.source_id === bound && (!host || w.source === host));
    if (exact.length === 1) return exact[0];
    if (exact.length > 1) {
      // An explicit desktop project may own several roots. Only those roots can resolve its current directory or Git root.
      return ["codex_app", "claude_desktop"].includes(host) ? directoryWorkspace([s.cwd, s.workspace_cwd, s.repo_root], exact, true) : undefined;
    }
    // Desktop project ids describe an editor project, not a terminal workspace binding.
    if (s.workspace_id || !["codex_app", "claude_desktop"].includes(host)) return undefined;
    const directoryPrefixes = host === "codex_app" ? ["codex-project:"] : ["codex-project:", "claude-project:"];
    const directoryPrefix = directoryPrefixes.find((prefix) => bound.startsWith(prefix));
    const directoryRoot = directoryPrefix ? bound.slice(directoryPrefix.length) : "";
    if (directoryKey(directoryRoot) !== null) inferredProjectRoot = directoryRoot;
    // An unavailable saved project may use a generic directory, never another host's workspace at the same path.
    // Older, directory-derived desktop placement has no saved identity and can still resolve a unique matching project.
    if (!inferredProjectRoot) local = local.filter((w) => w.source === "directory");
  }
  if (["herdr", "herdr-core", "botmux", "orca", "paseo"].includes(host)) return undefined;
  const depth = gitTreeDepth(s);
  return directoryWorkspace([s.cwd, s.workspace_cwd, inferredProjectRoot], local, false, [depth, depth, depth === undefined ? undefined : 0]);
}
function directoryWorkspace(paths: readonly (string | undefined)[], workspaces: readonly HostWorkspace[], boundProject = false, depths?: readonly (number | undefined)[]): HostWorkspace | undefined {
  for (const [index, raw] of paths.entries()) {
    const cwd = catalogPath(raw);
    if (!cwd) continue;
    const depth = depths?.[index];
    const candidates = workspaces.filter((w) => {
      const path = catalogPath(w.cwd);
      if (!path || !within(cwd, path)) return false;
      if (boundProject) return true;
      if (depth !== undefined && depthOf(cwd) - depthOf(path) > depth) return false;
      const below = cwd === path ? "" : cwd.slice(path.length + (path.endsWith("/") ? 0 : 1));
      return !WORKTREE_SEGMENTS.some(([marker]) => below.startsWith(marker.slice(1)));
    });
    const length = Math.max(0, ...candidates.map((w) => catalogPath(w.cwd).length));
    const deepest = candidates.filter((w) => catalogPath(w.cwd).length === length);
    if (deepest.length === 1) return deepest[0];
    if (deepest.length > 1) return undefined;
  }
  return undefined;
}

/** A saved Agent remains in its host-pinned workspace even after its process stops. Never infer that pin from cwd. */
export function hasWorkspacePin(s: Session, catalog: readonly HostWorkspace[], manager?: string): boolean {
  if (s.archived || !s.workspace_id || !(s.space_kind || s.host || s.pane_id)) return false;
  return workspacePinTime(sessionWorkspace(s, catalog, manager)) !== undefined;
}
const catalogPath = (value?: string) => directoryKey(value ?? "") ?? pathKey(value);
/** Whether `path` is `dir` or below it, both already normalized by catalogPath. */
const within = withinPath;
/** How many folders deep a path normalized by catalogPath is: 0 at the root. */
const depthOf = (path: string) => (path === "/" ? 0 : path.split("/").length - 1);

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

/** Multi-root desktop projects give every root the project's name as its title: name colliding roots by their folder
 *  (adding parent directories while basenames still collide) so a project never lists duplicate identical rows. */
function projectRootNames(catalog: readonly HostWorkspace[], keyOf: (w: HostWorkspace) => string): Map<string, string> {
  const byProject = new Map<string, { key: string; cwd: string; segs: string[]; depth: number }[]>();
  for (const w of catalog) {
    if (!["codex_app", "claude_desktop"].includes(w.source) || !w.project_id || !w.project_name || w.title !== w.project_name || !w.cwd) continue;
    const cwd = trimmed(w.cwd);
    const id = `${w.source}\u0000${w.project_id}`;
    const item = { key: keyOf(w), cwd, segs: cwd.split("/").filter(Boolean), depth: 1 };
    byProject.get(id)?.push(item) ?? byProject.set(id, [item]);
  }
  const names = new Map<string, string>();
  const label = (p: { cwd: string; segs: string[]; depth: number }) => p.segs.slice(-p.depth).join("/") || p.cwd;
  for (const group of byProject.values()) {
    if (group.length < 2) continue;
    for (let bumped = true; bumped;) {
      const counts = new Map<string, number>();
      for (const p of group) counts.set(label(p), (counts.get(label(p)) ?? 0) + 1);
      bumped = false;
      for (const p of group) if (counts.get(label(p))! > 1 && p.depth < p.segs.length) { p.depth++; bumped = true; }
    }
    for (const p of group) names.set(p.key, label(p));
  }
  return names;
}

function catalogRepos(sessions: Session[], machineId: string, options: RepoCatalogOptions, machineOnline: boolean): RepoTree[] {
  // session-directory rows are the server's compatibility fallback, not host-owned workspaces or action targets.
  const inventory = options.workspaces.filter((w) => w.machine_id === machineId && w.source !== "session-directory");
  const catalog = inventory.filter((w) => !options.hiddenSources?.includes(w.source));
  if (!catalog.length) return legacyNavigationRepos(sessions, machineId, options);
  const keyOf = (w: HostWorkspace) => workspaceKey(w, options.owner, options.manager);
  const disambiguated = projectRootNames(catalog, keyOf);
  const nodes = new Map<string, RepoWorktree>();
  for (const w of catalog) {
    const key = keyOf(w);
    nodes.set(key, {
      key, name: disambiguated.get(key) || w.title || w.branch || baseName(w.cwd) || w.source_id, branch: w.branch,
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
  // Keep a pin inside its real ancestry, but lift that branch and repository ahead of ordinary ones. Only visible
  // descendants contribute: a pin hidden by the current search or filter cannot reorder the remaining tree.
  const order = (a: RepoWorktree, b: RepoWorktree) => byWorkspacePin(a, b)
    || Number(b.main) - Number(a.main) || a.name.localeCompare(b.name) || a.key.localeCompare(b.key);
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
    const pinned = Math.max(workspacePinTime(w) ?? -Infinity, node.children[0]?.latestPinTime ?? -Infinity);
    if (Number.isFinite(pinned)) node.latestPinTime = pinned;
    node.tree = [...node.sessions, ...node.children.flatMap((c) => c.tree)];
    node.navigationTree = [...node.directSessions!, ...node.children.flatMap((c) => c.navigationTree ?? [])];
    for (const c of node.children) {
      if (!c.tree.length) continue;
      const state = summaryState(c.tree);
      if (state === "needs_you" || state === "working") node.childStates[state]++;
      else if (state === "unread" || state === "idle") node.childStates.done++;
    }
    const pinnedEmpty = workspacePinTime(w) !== undefined;
    const liveEmpty = Boolean(options.showEmptyWorkspaces) && machineOnline && w.available !== false && w.online !== false;
    const keepEmpty = !options.hideEmpty && !w.hidden_by_directory && matches && (pinnedEmpty || liveEmpty);
    return node.children.length > 0 || node.directSessions!.length > 0 || keepEmpty;
  };
  // A host that names no project for a workspace (herdr) leaves it to Git: the repository its Agents agree on, else the
  // one its own folder is. Such a workspace shares that repository's section with the other hosts' places in it and with
  // the session-only rows below, so one repository is one section, while every place keeps its own row.
  const repositoryPath = (root: RepoWorktree): { path: string; apart?: boolean } => {
    const w = root.workspace!;
    if (w.project_id) return { path: "" };
    if (w.project_path) return { path: catalogPath(w.project_path) };
    const roots = new Set(root.tree.map((s) => catalogPath(gitPlaceOf(s)?.repo || s.repo_root)).filter(Boolean));
    if (roots.size > 1) return { path: "" };
    if (roots.size) return { path: [...roots][0]! };
    const own = repoOf(catalogPath(w.cwd));
    return { path: catalogPath(own.repo), apart: own.apart };
  };
  const repos = new Map<string, RepoTree>();
  const byPath = new Map<string, RepoTree>();
  for (const root of top.filter(fill).sort(order)) {
    const w = root.workspace!;
    const { path, apart } = repositoryPath(root);
    const project = w.project_id || w.project_path || w.source_id;
    const key = JSON.stringify(path ? [options.owner, options.manager, machineId, "path", path] : [options.owner, options.manager, machineId, w.source, project]);
    let repo = repos.get(key);
    if (!repo) {
      repo = { key, machineId, name: w.project_name || baseName(path || w.project_path || "") || root.name, repo: path || w.project_path || `${w.source}:${project}`, count: 0, worktrees: [], ...(apart && { apart: true }) };
      // Roots are already ordered by pin, so the first member holds this repository's newest one.
      if (root.latestPinTime !== undefined) repo.latestPinTime = root.latestPinTime;
      repos.set(key, repo);
      if (path) byPath.set(path, repo);
    }
    repo.worktrees.push(root);
    repo.count += root.tree.length;
  }
  // A host that names no project and does not say which of its workspaces is a repository's main checkout (herdr) leaves
  // it to the folder: the workspace at the repository's own folder, as Git or the path named it (repositoryPath), is. It
  // wears the home mark and comes first, as Git's main checkouts do (#13454), instead of sorting by name among the linked
  // trees. A project's folder is not that: a desktop app's project lists each of its folders as its own project_path
  // (#14497).
  const byFolder = (w: HostWorkspace) => w.is_main === undefined && !w.project_id && !w.project_path;
  for (const repo of repos.values()) {
    const unmarked = repo.worktrees.filter((w) => byFolder(w.workspace!) && catalogPath(w.path) === catalogPath(repo.repo));
    for (const w of unmarked) w.main = true;
    if (unmarked.length) repo.worktrees.sort(order);
  }
  const unmatched = query ? loose.filter((s) => `${s.title} ${s.cwd}`.toLocaleLowerCase().includes(query)) : loose;
  const groupedNavigation = new Set([...nodes.values()].flatMap((node) => node.directSessions!));
  const navigation = options.navigationSessions?.filter((s) => !groupedNavigation.has(s)
    && (!query || `${s.title} ${s.cwd}`.toLocaleLowerCase().includes(query)));
  const sessionOnly: RepoTree[] = [];
  for (const repo of legacyNavigationRepos(unmatched, machineId, { ...options, navigationSessions: navigation })) {
    const shared = byPath.get(catalogPath(repo.repo));
    if (!shared) { sessionOnly.push(repo); continue; }
    shared.worktrees.push(...repo.worktrees);
    shared.worktrees.sort(order);
    shared.count += repo.count;
  }
  return [...repos.values(), ...sessionOnly]
    .sort((a, b) => byWorkspacePin(a, b) || a.name.localeCompare(b.name) || a.key.localeCompare(b.key));
}

/**
 * One section per repository when a folder of trees kept apart from it (`~/.herdr/worktrees/<repository>/<tree>`) is all
 * that places some of its rows. A tree there that Git no longer places (removed once its work merged, or not read yet)
 * names its repository only by that folder, which is never the checkout, so its rows formed a second section with the
 * repository's name (#13453). They join the repository Git placed another tree of that folder in, else the one other
 * section of that name on the computer; when Git placed that folder's trees in two repositories, or two sections share
 * the name, they keep their own section rather than guess. Only a folder of trees at the computer's home keeps them
 * apart: in a repository's own tree a folder such as `/code/app/.worktrees/feature/src` has the same shape, but
 * `feature` is that tree, not a repository to join. Without the home, nothing joins.
 */
function joinApart(repos: RepoTree[], sessions: readonly Session[], home?: string): RepoTree[] {
  const folders = home ? APART_SEGMENTS.map((marker) => catalogPath(`${home}${marker}`)) : [];
  for (const r of repos) {
    const path = catalogPath(r.repo);
    if (r.apart && !folders.includes(path.slice(0, path.lastIndexOf("/")))) delete r.apart;
  }
  if (!repos.some((r) => r.apart)) return repos;
  const taught = new Map<string, string | null>();
  for (const s of sessions) {
    const git = gitPlaceOf(s);
    if (!git || git.main) continue;
    const folder = repoOf(git.worktree);
    if (!folder.apart) continue;
    const key = catalogPath(folder.repo);
    const known = taught.get(key);
    taught.set(key, known === undefined || known === git.repo ? git.repo : null);
  }
  const placed = repos.filter((r) => !r.apart);
  const out = [...placed];
  for (const r of repos) {
    if (!r.apart) continue;
    const root = taught.get(catalogPath(r.repo));
    const named = placed.filter((p) => p.name === r.name);
    const into = root ? placed.find((p) => catalogPath(p.repo) === catalogPath(root)) : root === undefined && named.length === 1 ? named[0] : undefined;
    if (!into) {
      out.push(r);
      continue;
    }
    into.worktrees.push(...r.worktrees);
    into.worktrees.sort((a, b) => byWorkspacePin(a, b) || Number(b.main) - Number(a.main) || a.name.localeCompare(b.name) || a.key.localeCompare(b.key));
    into.count += r.count;
    if (r.latestPinTime !== undefined && r.latestPinTime > (into.latestPinTime ?? -Infinity)) into.latestPinTime = r.latestPinTime;
  }
  return out.sort((a, b) => byWorkspacePin(a, b) || a.name.localeCompare(b.name) || a.key.localeCompare(b.key));
}

export function buildRepoMachineTree(all: Session[], machines: Machine[], options?: RepoCatalogOptions): RepoMachine[] {
  const online = new Set(machines.filter((m) => m.online).map((m) => m.id));
  const byMachine = new Map<string, Session[]>();
  for (const s of all) {
    if (s.assistant || (!online.has(s.machine_id) && !(options && hasWorkspacePin(s, options.workspaces, options.manager)))) continue;
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
    const workspaces = options?.workspaces.some((w) => w.machine_id === m.id && w.source !== "session-directory" && (m.online || workspacePinTime(w) !== undefined));
    const navigation = m.online && options?.navigationSessions?.some((s) => !s.assistant && s.machine_id === m.id);
    if ((!sessions || sessions.length === 0) && !workspaces && !navigation) continue;
    const known = [...(sessions ?? []), ...(options?.navigationSessions ?? []).filter((s) => s.machine_id === m.id)];
    const repos = joinApart(options ? catalogRepos(sessions ?? [], m.id, options, m.online) : buildRepos(sessions!, m.id), known, m.home);
    if (!repos.length) continue;
    out.push({
      key: `repo-machine:${m.id}`,
      machine: m,
      foldKey: `repo-machine:${m.id}`,
      repos,
      sessions: sessions ?? [],
      needs: (sessions ?? []).filter((s) => s.view ? s.view.counted : stateOf(s) === "needs_you").length,
    });
  }
  return out;
}
