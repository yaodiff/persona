package persona.core.domain.sort

import persona.core.contracts.api.AgentWorkspace
import persona.core.contracts.Session
import java.time.Instant

/** Where a session's cwd sits: the repository it belongs to and the working tree inside it. */
data class RepoPlace(val repo: String, val name: String, val worktree: String, val main: Boolean)

// The layouts a working tree commonly lives in (herdr, Claude Code, a home-level collection), as on web and iOS.
private val WorktreeMarkers = listOf("/.worktrees/", "/.claude/worktrees/", "/.herdr/worktrees/")
private val HomeWorktreeMarkers = listOf("/.worktrees/", "/.herdr/worktrees/")

private fun baseName(path: String) = path.split('/').lastOrNull { it.isNotEmpty() }.orEmpty()

/**
 * The repository and working tree a cwd belongs to (web's `repoOf`, iOS's `RepoPlace`). The host may name the
 * repository ([repoRoot], the session's `repo_root`, #2840: Paseo keeps its worktrees under its own folder): a cwd
 * inside it is its main checkout, any other one of its trees. Otherwise from the path alone: a tree under one of the
 * known layouts belongs to the directory above the marker, `~/.worktrees/<repo>/<tree>` to `<repo>` under it; any other
 * path is its own repository's main checkout.
 */
fun repoOf(cwd: String, repoRoot: String? = null): RepoPlace {
    val path = cwd.trimEnd('/')
    if (path.isEmpty()) return RepoPlace("", "", "", main = true)
    val root = repoRoot.orEmpty().trimEnd('/')
    if (root.isNotEmpty()) {
        val nestedTree = WorktreeMarkers.firstOrNull { path.startsWith(root + it) }?.let { marker ->
            root + marker + path.removePrefix(root + marker).substringBefore('/')
        }
        if (nestedTree != null) return RepoPlace(root, baseName(root), nestedTree, main = false)
        val inside = path == root || path.startsWith("$root/")
        return RepoPlace(root, baseName(root), if (inside) root else path, main = inside)
    }
    for (marker in WorktreeMarkers) {
        val at = path.indexOf(marker)
        if (at <= 0) continue
        val repo = path.substring(0, at)
        val rest = path.substring(at + marker.length).split('/').filter { it.isNotEmpty() }
        if (rest.size >= 2) return RepoPlace(repo + marker + rest[0], rest[0], path, main = false)
        return RepoPlace(repo, baseName(repo), path, main = false)
    }
    return RepoPlace(path, baseName(path), path, main = true)
}

/**
 * A working tree of a repository on one computer and the sessions in it (in the order they came). [name] is its [branch]
 * when the node says (#1805), else its folder's name (the main checkout: the repository's). A linked tree goes by the
 * name its host gave the place instead, when its sessions agree on one (#4497, [hostPlaceName]) and no Orca session is in
 * it; its [id] does not change with the name.
 */
data class AgentWorktree(
    val id: String, val name: String, val path: String, val main: Boolean, val members: List<AgentSession>, val branch: String? = null,
    /** The node placed its sessions in git (#1805) and named no branch: HEAD is detached there ([worktreeLine]). */
    val detached: Boolean = false,
    /** Exact source workspace, when known; its pin is independent of Persona's session/project pins. */
    val workspace: AgentWorkspace? = null,
    val pinnedAt: Instant? = null,
    val hasChildren: Boolean = false,
)

/** One repository on one computer: native pinned workspaces first, then the main checkout and names. */
data class AgentRepo(
    val id: String, val machineId: String, val name: String, val path: String, val worktrees: List<AgentWorktree>,
    internal val apart: Boolean = false,
    internal val catalogProject: Boolean = false,
) {
    val members: List<AgentSession> get() = worktrees.flatMap { it.members }
    val pinnedAt: Instant? get() = worktrees.mapNotNull { it.pinnedAt }.maxOrNull()
    /**
     * A lone unnamed checkout, or a herdr workspace that repeats the repository's name, shares its heading.
     */
    val flat: Boolean get() {
        val tree = worktrees.singleOrNull() ?: return false
        if (tree.branch != null || tree.hasChildren) return false
        if (tree.workspace?.source == "herdr") return tree.name == name && tree.members.isNotEmpty()
        return tree.main && tree.workspace == null
    }
}

/**
 * What a repository is called when its sessions have no cwd (iOS `RepoTree.unnamed`): sorts after every name; the
 * heading says "Other" (repoTitle).
 */
const val UnnamedRepo = "\uFFFF"

private val Chunks = Regex("\\d+|\\D+")

/**
 * A name's chunks, split once: NaturalOrder is a sort comparator, and splitting both names on every comparison of
 * hundreds of sessions was most of what froze the Agents list. Names are few, so the cache stays small.
 */
private val chunkCache = java.util.concurrent.ConcurrentHashMap<String, List<String>>()
private fun chunks(name: String): List<String> = chunkCache[name] ?: Chunks.findAll(name.lowercase()).map { it.value }.toList().also {
    if (chunkCache.size > 4_096) chunkCache.clear()
    chunkCache[name] = it
}

/** Natural order, like iOS `localizedStandardCompare`: case-insensitive, digit runs by value (repo2 before repo10). */
internal val NaturalOrder = Comparator<String> { a, b ->
    val xs = chunks(a)
    val ys = chunks(b)
    for (i in 0 until minOf(xs.size, ys.size)) {
        val x = xs[i]
        val y = ys[i]
        val c = if (x[0].isDigit() && y[0].isDigit()) {
            val (u, v) = x.trimStart('0') to y.trimStart('0')
            compareValuesBy(u to x, v to y, { it.first.length }, { it.first }, { it.second.length })
        } else x.compareTo(y)
        if (c != 0) return@Comparator c
    }
    xs.size.compareTo(ys.size)
}

private val byName = compareBy<Pair<String, String>, String>(NaturalOrder) { it.first }.thenBy { it.second }
private val worktreeOrder = compareByDescending<AgentWorktree> { it.pinnedAt }.thenBy { !it.main }
    .thenComparing({ it.name to it.path }, byName).thenBy { it.id }
private val repositoryOrder = compareByDescending<AgentRepo> { it.pinnedAt }
    .thenComparing({ it.name to it.path }, byName).thenBy { it.id }

/**
 * Where the node placed a session in git (#1805, web's `gitPlaceOf`, iOS's `RepoPlace.git`): its project (`repo_root`,
 * else the working tree itself) and working tree; null from an older node, which leaves [repoOf]'s inference.
 */
fun gitPlaceOf(repoRoot: String?, worktree: String?): RepoPlace? {
    val tree = worktree.orEmpty().trimEnd('/')
    if (tree.isEmpty()) return null
    val repo = repoRoot.orEmpty().trimEnd('/').ifEmpty { tree }
    return RepoPlace(repo, baseName(repo), tree, main = tree == repo)
}

private val AgentSession.place: RepoPlace
    get() = gitPlaceOf(session.repoRoot, session.worktree) ?: repoOf(session.cwd, session.repoRoot)

/**
 * The name the host gave the place [members] run in (#4497, web's `hostPlaceName`: Paseo sends its workspace's title as
 * `slot_name`, with no `slot_id`), when every session that gives one gives the same; two different names name nothing.
 * A `slot_name` beside a `slot_id` is a tab's label (herdr), which names no working tree.
 */
private fun hostPlaceName(members: List<AgentSession>): String? =
    members.mapNotNullTo(mutableSetOf()) { m -> m.session.slotName?.trim()?.takeIf { it.isNotEmpty() && m.session.slotId.isNullOrBlank() } }
        .singleOrNull()

val AgentWorkspace.inRepositoryCatalog: Boolean get() = source == "paseo" || source == "herdr"

internal val AgentWorkspace.repoWorkspaceKey: Triple<String, String, String>
    get() = Triple(machineId, source, sourceId)

internal fun Session.repoWorkspaceKey(): Triple<String, String, String>? =
    if (spaceKind == "herdr") {
        (workspaceId?.takeIf(String::isNotBlank) ?: spaceId?.takeIf(String::isNotBlank))?.let { Triple(machineId, "herdr", it) }
    } else {
        paseoWorkspaceKey()?.let { (machine, id) -> Triple(machine, "paseo", id) }
    }

/** Each computer's repositories, native pins first, then names in natural order. Source IDs override path inference. */
fun agentRepos(
    rows: List<AgentSession>,
    workspaces: List<AgentWorkspace> = emptyList(),
    homes: Map<String, String?> = emptyMap(),
    knownRows: List<AgentSession> = rows,
): List<AgentRepo> {
    val catalog = workspaces.filter { it.inRepositoryCatalog && it.machineId.isNotEmpty() && it.sourceId.isNotEmpty() }
        .distinctBy { it.repoWorkspaceKey }
    val byID = catalog.associateBy { it.repoWorkspaceKey }
    val assigned = rows.groupBy { it.session.repoWorkspaceKey()?.let(byID::get) }
    val visible = catalog.filter { assigned[it].orEmpty().isNotEmpty() || it.nativePinTime != null }
    // Paseo project keys own their workspaces even if several projects use the same folder.
    val paseo = visible.filter { it.source == "paseo" }
        .groupBy { it.machineId to (it.projectId?.takeIf(String::isNotBlank) ?: it.sourceId) }.map { (key, entries) ->
        val first = entries.first()
        val path = first.projectPath?.takeIf(String::isNotBlank) ?: first.cwd
        val name = first.projectName?.takeIf(String::isNotBlank) ?: baseName(path).ifEmpty { UnnamedRepo }
        val trees = entries.map { w ->
            AgentWorktree("tree/catalog/${w.machineId}/${w.source}/${w.sourceId}", w.title.ifBlank { w.branch ?: baseName(w.cwd) },
                w.cwd, w.isMain == true, assigned[w].orEmpty(), w.branch?.takeIf(String::isNotBlank),
                detached = w.workspaceKind == "git" && w.branch.isNullOrBlank(), workspace = w,
                pinnedAt = w.nativePinTime)
        }.sortedWith(compareByDescending<AgentWorktree> { it.pinnedAt }.thenBy { !it.main }
            .thenComparing({ it.name to it.id }, byName))
        AgentRepo("repo/catalog/${key.first}/paseo/${key.second}", key.first, name, path, trees, catalogProject = true)
    }
    val parents = catalog.mapNotNull { w -> w.parentSourceId?.let { Triple(w.machineId, w.source, it) } }.toSet()
    val herdr = visible.filter { it.source == "herdr" }.map { w ->
        val members = assigned[w].orEmpty()
        val roots = members.mapNotNullTo(mutableSetOf()) { knownRepo(it.session) }
        val project = w.projectId?.takeIf(String::isNotBlank)
        val explicitPath = w.projectPath?.trimEnd('/')?.takeIf(String::isNotBlank)
        val inferredPath = repoOf(w.cwd).repo
        val path = explicitPath ?: roots.singleOrNull() ?: inferredPath
        val ownProject = project != null || (explicitPath == null && roots.size > 1) || path.isEmpty()
        val name = w.projectName?.takeIf(String::isNotBlank) ?: baseName(path).ifEmpty { w.title }
        val branch = w.branch?.takeIf(String::isNotBlank)
        val checkout = explicitPath ?: path.takeUnless { ownProject }
        val tree = AgentWorktree(
            "tree/catalog/${w.machineId}/herdr/${w.sourceId}", w.title.ifBlank { branch ?: baseName(w.cwd) }, w.cwd,
            w.isMain ?: (w.cwd.trimEnd('/') == checkout), members, branch,
            detached = w.workspaceKind == "git" && branch == null, workspace = w, pinnedAt = w.nativePinTime,
            hasChildren = w.repoWorkspaceKey in parents,
        )
        val id = if (ownProject) "repo/catalog/${w.machineId}/herdr/${project ?: w.sourceId}" else "repo/${w.machineId}/$path"
        val apart = !ownProject && explicitPath == null && roots.isEmpty() && apartRepository(w.cwd, homes[w.machineId]) == path
        AgentRepo(id, w.machineId, name, path, listOf(tree), apart, catalogProject = ownProject)
    }
    val projected = (inferredAgentRepos(assigned[null].orEmpty(), homes) + paseo + herdr)
        .groupBy { it.id }.values.map { same ->
            val heading = same.firstOrNull { it.worktrees.any { tree -> tree.workspace != null } } ?: same.first()
            if (same.size == 1) heading else heading.copy(
                worktrees = same.flatMap { it.worktrees }.sortedWith(worktreeOrder), apart = same.all { it.apart },
            )
        }
    return joinApartRepositories(projected, homes, knownRows).sortedWith(repositoryOrder)
}

private fun knownRepo(session: Session): String? =
    gitPlaceOf(session.repoRoot, session.worktree)?.repo ?: session.repoRoot?.trimEnd('/')?.takeIf(String::isNotBlank)

/** A separate worktree collection is anchored to a known home, including when a tree has disappeared from Git. */
private fun apartRepository(path: String, home: String?): String? {
    val root = home?.trimEnd('/')?.takeIf(String::isNotEmpty) ?: return null
    val prefix = HomeWorktreeMarkers.firstNotNullOfOrNull { marker -> (root + marker).takeIf(path::startsWith) } ?: return null
    val parts = path.removePrefix(prefix).split('/', limit = 3)
    return if (parts.size > 1 && parts[0].isNotEmpty() && parts[1].isNotEmpty()) prefix + parts[0] else null
}

private fun joinApartRepositories(repos: List<AgentRepo>, homes: Map<String, String?>, knownRows: List<AgentSession>): List<AgentRepo> {
    val apart = repos.filter { it.apart }
    if (apart.isEmpty()) return repos
    val learned = knownRows.mapNotNull { row ->
        val session = row.session
        val git = gitPlaceOf(session.repoRoot, session.worktree)?.takeUnless { it.main } ?: return@mapNotNull null
        val folder = apartRepository(git.worktree, homes[session.machineId]) ?: return@mapNotNull null
        (session.machineId to folder) to git.repo
    }.groupBy({ it.first }, { it.second })
    val placed = repos.filterNot { it.apart }
    val byPath = placed.groupBy { it.machineId to it.path }
    val byName = placed.groupBy { it.machineId to it.name }
    val result = placed.associateByTo(linkedMapOf()) { it.id }
    for (repo in apart) {
        val roots = learned[repo.machineId to repo.path]?.toSet()
        val candidates = if (roots == null) byName[repo.machineId to repo.name] else roots.singleOrNull()?.let { byPath[repo.machineId to it] }
        val into = candidates?.singleOrNull()?.takeUnless { it.catalogProject }
        if (into == null) {
            result[repo.id] = repo
        } else {
            val current = result.getValue(into.id)
            result[into.id] = current.copy(worktrees = (current.worktrees + repo.worktrees).sortedWith(worktreeOrder))
        }
    }
    return result.values.toList()
}

/** Invalid or cleared source timestamps are not pins. */
internal val AgentWorkspace.nativePinTime: Instant? get() =
    pinnedAt?.let { runCatching { Instant.parse(it) }.getOrNull() }

/** Exact source attribution; another engine at the same folder or with the same ID is not this workspace. */
internal fun Session.paseoWorkspaceKey(): Pair<String, String>? = workspaceId?.takeIf(String::isNotBlank)?.let {
    if (sourceKind == "runtime" && (sourceInstanceId?.takeIf(String::isNotBlank) ?: spaceKind) == "paseo") machineId to it else null
}

private fun inferredAgentRepos(rows: List<AgentSession>, homes: Map<String, String?>): List<AgentRepo> =
    rows.groupBy { it.session.machineId to it.place.repo }.map { (key, members) ->
        val (machine, repo) = key
        val place = members.first().place
        val name = place.name.ifEmpty { UnnamedRepo }
        val trees = members.groupBy { it.place.worktree }.map { (path, inTree) ->
            val main = inTree.first().place.main
            // Orca's sidebar and git name a working tree by its branch.
            val branch = inTree.firstNotNullOfOrNull { it.session.branch?.takeIf(String::isNotEmpty) }
            // Keep the saved fold key for the repository inferred from the path. If the host puts the same
            // path under a different repository, scope its key to that repository even when filtered on its own.
            val id = "tree/$machine/$path" + if (repo != repoOf(path).repo) "\u0000$repo" else ""
            // A linked tree takes the name its host gave it (#4497), its branch staying beside it. The main checkout keeps
            // its name: several workspaces can share it. So does a tree with an Orca session in it, which Orca names (web `orcaTrees`).
            val hostName = if (main || inTree.any { it.session.spaceKind == "orca" }) null else hostPlaceName(inTree)
            AgentWorktree(id, hostName ?: branch ?: if (path == repo) name else baseName(path), path, main, inTree, branch,
                detached = branch == null && inTree.any { !it.session.worktree.isNullOrBlank() })
        }.sortedWith(compareBy<AgentWorktree> { !it.main }.thenComparing({ it.name to it.path }, byName))
        val apart = members.all { knownRepo(it.session) == null && apartRepository(it.session.cwd, homes[machine]) == repo }
        AgentRepo("repo/$machine/$repo", machine, name, repo, trees, apart = apart)
    }.sortedWith(compareBy<AgentRepo, Pair<String, String>>(byName) { it.name to it.path }.thenBy { it.machineId })
