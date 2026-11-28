package persona.core.domain.sort

import persona.core.domain.zhAny

import persona.core.contracts.AgentState
import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.AgentWorkspace
import org.junit.Assert.*
import org.junit.Test

/** Mirrors web/src/lib/agent-repos.test.ts, plus how "By repository" folds on Android. */
class AgentReposTest {
    private fun session(id: String, cwd: String, state: String = "idle", machine: String = "machine-a", hour: Int = 1, repoRoot: String? = null) =
        AgentSession(Session(machineId = machine, sessionId = id, title = id, cwd = cwd, state = state,
            updatedAt = "2026-01-01T0$hour:00:00Z", repoRoot = repoRoot))

    private val machines = listOf(Machine("machine-b", "电脑 B", online = true), Machine("machine-a", "电脑 A", online = true))
    private val byRepo = AgentListSettings(grouping = AgentGrouping.Repo)

    @Test fun theMainCheckoutIsTheRepositoryItself() {
        assertEquals(RepoPlace("/code/app", "app", "/code/app", main = true), repoOf("/code/app"))
        assertEquals(RepoPlace("/code/app", "app", "/code/app", main = true), repoOf("/code/app/"))
    }

    @Test fun commonWorktreeLayoutsBelongToTheRepositoryRoot() {
        listOf("/code/app/.worktrees/fix-login", "/code/app/.claude/worktrees/fix-login", "/code/app/.herdr/worktrees/fix-login").forEach {
            assertEquals(RepoPlace("/code/app", "app", it, main = false), repoOf(it))
        }
    }

    @Test fun aHomeLevelWorktreeCollectionNamesTheRepositoryAfterTheMarker() {
        assertEquals(RepoPlace("/home/me/.worktrees/persona", "persona", "/home/me/.worktrees/persona/fix-login", main = false),
            repoOf("/home/me/.worktrees/persona/fix-login"))
    }

    @Test fun withoutAMarkerTheDirectoryIsItsOwnRepository() {
        assertEquals(RepoPlace("/code/app/server/internal", "internal", "/code/app/server/internal", main = true), repoOf("/code/app/server/internal"))
        assertEquals(RepoPlace("", "", "", main = true), repoOf(""))
    }

    /** Paseo keeps its worktrees under its own folder, whose paths name no repository: its host names it (#2840). */
    @Test fun aRepositoryTheHostNamesComesBeforeThePath() {
        assertEquals(RepoPlace("/code/app", "app", "/home/me/.paseo/worktrees/1a2b/fix-login", main = false),
            repoOf("/home/me/.paseo/worktrees/1a2b/fix-login", "/code/app"))
        assertEquals("a directory inside it is its main checkout", RepoPlace("/code/app", "app", "/code/app", main = true),
            repoOf("/code/app/server/", "/code/app/"))
        assertEquals("a shared prefix is not inside it", RepoPlace("/code/app", "app", "/code/app-old", main = false),
            repoOf("/code/app-old", "/code/app"))
        assertEquals("an empty one names none", repoOf("/code/app/.worktrees/fix"), repoOf("/code/app/.worktrees/fix", ""))
        assertEquals("/code/app", JsonCodec.decodeFromString<Session>("""{"session_id":"s","repo_root":"/code/app"}""").repoRoot)
    }

    @Test fun worktreesElsewhereJoinTheRepositoryTheirHostNames() {
        val repos = agentRepos(listOf(session("main", "/code/app", repoRoot = "/code/app"),
            session("wt-b", "/home/me/.paseo/worktrees/3c4d/fix-b", repoRoot = "/code/app"),
            session("wt-a", "/home/me/.paseo/worktrees/1a2b/fix-a", repoRoot = "/code/app"),
            session("herdr", "/code/app/.worktrees/fix-c")))
        val repo = repos.single()
        assertEquals("repo/machine-a//code/app", repo.id)
        assertEquals(listOf("app*", "fix-a", "fix-b", "fix-c"), (repo.worktrees.map { it.name + if (it.main) "*" else "" }).zhAny())
        assertEquals("tree/machine-a//home/me/.paseo/worktrees/1a2b/fix-a\u0000/code/app", repo.worktrees[1].id)
    }

    @Test fun aRemovedHerdrTreeStaysInTheMainCheckoutsSection() {
        val machine = Machine("machine-a", "Computer", online = true, home = "/home/me")
        val rows = listOf(
            session("main", "/code/app", repoRoot = "/code/app"),
            session("removed", "/home/me/.herdr/worktrees/app/fix-login"),
        )
        val projection = buildAgentList(rows, listOf(machine), settings = byRepo)
        assertEquals(1, projection.machines.single().repoCount)
        assertEquals(setOf("main", "removed"), projection.groups.single().rows.map { it.session.sessionId }.toSet())
    }

    @Test fun knownGitPlacementWinsButAmbiguousHomeFoldersStaySeparate() {
        val rows = listOf(
            session("local", "/code/app"),
            session("other", "/other/app"),
            session("removed", "/home/me/.herdr/worktrees/app/removed"),
        )
        val homes = mapOf("machine-a" to "/home/me")
        assertEquals(3, agentRepos(rows, homes = homes).size)
        val known = placed("known", "/home/me/.herdr/worktrees/app/known", "/code/app",
            "/home/me/.herdr/worktrees/app/known", "known")
        val repositories = agentRepos(rows, homes = homes, knownRows = rows + known)
        assertEquals(setOf("local", "removed"), repositories.single { it.path == "/code/app" }.members.map { it.session.sessionId }.toSet())
        assertEquals(listOf("other"), repositories.single { it.path == "/other/app" }.members.map { it.session.sessionId })
        val conflicting = known.copy(session = known.session.copy(sessionId = "conflict", repoRoot = "/other/app"))
        assertEquals(3, agentRepos(rows, homes = homes, knownRows = rows + known + conflicting).size)
        val searchable = rows.map { it.copy(session = it.session.copy(title = "pick ${it.session.title}")) }
        val machine = Machine("machine-a", "Computer", online = true, home = "/home/me")
        val projection = buildAgentList(searchable + known, listOf(machine), settings = byRepo.copy(query = "pick"))
        assertEquals(2, projection.machines.single().repoCount)
        assertTrue(projection.groups.any { it.rows.map { row -> row.session.sessionId }.toSet() == setOf("local", "removed") })
    }

    @Test fun aLoneHerdrWorkspaceKeepsItsMarksOnTheRepositoryHeading() {
        val workspace = AgentWorkspace(
            machineId = "machine-a", source = "herdr", sourceId = "workspace-notes", title = "notes",
            cwd = "/code/notes", pinnedAt = "2026-11-16T10:00:00Z", available = false,
        )
        val agent = session("agent", workspace.cwd, state = "working").let {
            it.copy(session = it.session.copy(spaceKind = "herdr", spaceId = workspace.sourceId))
        }
        val list = buildAgentList(listOf(agent), machines, settings = byRepo.copy(workspaces = listOf(workspace)))
        val heading = list.groups.single()
        assertEquals(RepoLevel.Repository, heading.repo)
        assertEquals(workspace.sourceId, heading.workspace?.sourceId)
        assertEquals(listOf("agent"), heading.rows.map { it.session.sessionId })
        assertTrue(heading.worktree?.pinned == true)
        assertTrue(heading.worktree?.offline == true)
    }

    @Test fun aCatalogMainCheckoutComesFirstOnlyWhenTheFolderEstablishesIt() {
        val main = AgentWorkspace(machineId = "machine-a", source = "herdr", sourceId = "main", title = "z-checkout", cwd = "/code/app")
        val linked = main.copy(sourceId = "linked", title = "a-tree", cwd = "/home/me/.herdr/worktrees/app/a-tree")
        val rows = listOf(main, linked).map { workspace ->
            session(workspace.sourceId, workspace.cwd, repoRoot = "/code/app").let {
                it.copy(session = it.session.copy(spaceKind = "herdr", spaceId = workspace.sourceId))
            }
        }
        val trees = agentRepos(rows, listOf(linked, main)).single().worktrees
        assertEquals(listOf("main", "linked"), trees.map { it.workspace?.sourceId })
        assertTrue(trees.first().main)
        val ownProject = main.copy(projectId = "project")
        assertFalse(agentRepos(listOf(rows.first()), listOf(ownProject)).single().worktrees.single().main)
        assertTrue(agentRepos(listOf(rows.first()), listOf(ownProject.copy(projectPath = main.cwd))).single().worktrees.single().main)
        assertFalse(agentRepos(listOf(rows.first()), listOf(main.copy(isMain = false))).single().worktrees.single().main)
    }

    @Test fun theMainCheckoutComesFirstThenTreesByName() {
        val repos = agentRepos(listOf(session("main", "/code/app"), session("wt-b", "/code/app/.worktrees/fix-b"),
            session("wt-a", "/code/app/.worktrees/fix-a"), session("wt-a2", "/code/app/.worktrees/fix-a")))
        assertEquals(listOf("app"), (repos.map { it.name }).zhAny())
        assertEquals(listOf("app*", "fix-a", "fix-b"), (repos.single().worktrees.map { it.name + if (it.main) "*" else "" }).zhAny())
        assertEquals(listOf("wt-a", "wt-a2"), (repos.single().worktrees[1].members.map { it.session.sessionId }).zhAny())
        assertEquals(4, repos.single().members.size)
        assertFalse(repos.single().flat)
    }

    @Test fun namesSortNaturallyAsOnIos() {
        assertEquals(listOf("app", "App2", "repo2", "repo10"), (agentRepos(listOf(session("a", "/c/repo10"), session("b", "/c/repo2"),
            session("c", "/c/App2"), session("d", "/c/app"))).map { it.name }).zhAny())
    }

    @Test fun aQuietRepositoryWithTreesFoldsIntoOneGroupUnderItsRepositoryId() {
        val list = buildAgentList(listOf(session("m", "/code/app"), session("w", "/code/app/.worktrees/wt")), machines, settings = byRepo)
        assertEquals(listOf("repo/machine-a//code/app"), (list.machines.single().groups).zhAny())
        assertEquals(2, list.groups.single().rows.size)
    }

    @Test fun repositoriesSortByNameAndSameNamesOnTwoComputersStayApart() {
        assertEquals(listOf("alpha", "zeta"), (agentRepos(listOf(session("z", "/code/zeta"), session("a", "/code/alpha"))).map { it.name }).zhAny())
        val two = agentRepos(listOf(session("a", "/code/app"), session("b", "/code/app", machine = "machine-b")))
        assertEquals(listOf("repo/machine-a//code/app", "repo/machine-b//code/app"), (two.map { it.id }).zhAny())
        assertEquals(UnnamedRepo, agentRepos(listOf(session("x", ""))).single().name)
    }

    @Test fun computersKeepHubOrderAndTheirRepositoriesSortByNameWithoutAssistants() {
        val list = buildAgentList(listOf(session("a-zeta", "/code/zeta", "working"), session("a-alpha", "/code/alpha", "working"),
            session("b-beta", "/code/beta", "working", machine = "machine-b"),
            AgentSession(Session(machineId = "machine-a", sessionId = "assistant", cwd = "/code/app", assistant = true))), machines, settings = byRepo)
        assertEquals(listOf("machine-b", "machine-a"), (list.machines.map { it.machine.id }).zhAny())
        assertEquals("one-checkout repositories keep the checkout's id", listOf("tree/machine-a//code/alpha", "tree/machine-a//code/zeta"), (list.machines[1].groups).zhAny())
        assertEquals(listOf(1, 2), list.machines.map { it.repoCount })
        assertTrue(list.groups.flatMap { it.rows }.none { it.session.assistant })
    }

    @Test fun agentsInATreeNeedYouFirstAndBusyTreesHideTheirIdleOnes() {
        val list = buildAgentList(listOf(session("idle", "/code/app/.worktrees/wt"), session("needs", "/code/app/.worktrees/wt", "needs_you"),
            session("main", "/code/app", "working")), machines, settings = byRepo)
        val tree = list.groups.single { it.id == "tree/machine-a//code/app/.worktrees/wt" }
        assertEquals(listOf("needs", "idle"), (tree.rows.map { it.session.sessionId }).zhAny())
        assertEquals(listOf("needs"), (tree.visibleRows.map { it.session.sessionId }).zhAny())
        assertEquals(RepoLevel.Worktree, tree.repo)
        val repo = list.groups.single { it.id == "repo/machine-a//code/app" }
        assertTrue("the trees hold the repository's rows", repo.rows.isEmpty())
        assertEquals(1, repo.stateCounts?.get(AgentState.NeedsYou))
        assertEquals(listOf(repo.id, repo.id), list.groups.filter { it.under != null }.map { it.under })
        assertEquals(3, list.groups.sumOf { it.rows.size })
    }

    @Test fun quietRepositoriesAndTreesFoldAfterTheBusyOnesAndSearchOpensThem() {
        val rows = listOf(session("a", "/code/alpha"), session("b", "/code/beta", "working"),
            session("c", "/code/beta/.worktrees/old"), session("d", "/code/beta/.worktrees/old"))
        val list = buildAgentList(rows, machines, settings = byRepo)
        assertEquals(listOf("repo/machine-a//code/beta", "tree/machine-a//code/beta", "tree/machine-a//code/beta/.worktrees/old",
            "tree/machine-a//code/alpha"), (list.machines.single().groups).zhAny())
        val quiet = list.groups.filter { it.parent == "quiet" }.map { it.id }
        assertEquals(listOf("tree/machine-a//code/beta/.worktrees/old", "tree/machine-a//code/alpha"), (quiet).zhAny())
        assertTrue(list.groups.filter { it.parent == "quiet" }.all { it.folded && it.visibleRows.isEmpty() })
        val searched = buildAgentList(rows, machines, settings = byRepo.copy(query = "code"))
        assertTrue(searched.groups.filter { it.parent == "quiet" }.none { it.folded })
        val opened = buildAgentList(rows, machines, settings = byRepo.copy(expandedQuiet = setOf("tree/machine-a//code/alpha")))
        assertEquals(listOf("a"), (opened.groups.single { it.id == "tree/machine-a//code/alpha" }.visibleRows.map { it.session.sessionId }).zhAny())
    }

    @Test fun pinnedSessionsLeadAndLeaveTheTree() {
        val list = buildAgentList(listOf(session("a", "/code/app", "working"), session("b", "/code/app", "working")), machines,
            pins = AgentPreferences(sessions = listOf("machine-a/b")), settings = byRepo)
        assertEquals("pinned", (list.groups.first().id).zhAny())
        assertEquals(listOf("a"), (list.groups.single { it.id == "tree/machine-a//code/app" }.rows.map { it.session.sessionId }).zhAny())
    }

    // The node's git place (#1805): machine → project → branch → sessions, as on web and iOS.
    private fun placed(id: String, cwd: String, root: String, tree: String, branch: String, state: String = "working", machine: String = "machine-a") =
        AgentSession(Session(machineId = machine, sessionId = id, title = id, cwd = cwd, state = state,
            updatedAt = "2026-01-01T01:00:00Z", repoRoot = root, worktree = tree, branch = branch))

    @Test fun linkedWorktreesGoUnderTheirMainRepositoryByBranch() {
        val root = "/home/u/Code/app"
        val fix = "/home/u/orca/workspaces/app/fix-login"
        val repos = agentRepos(listOf(placed("fix", fix, root, fix, "fix/login"), placed("main", root, root, root, "main"),
            placed("sub", "$fix/web", root, fix, "fix/login"), placed("notes", "/home/u/Code/notes", "/home/u/Code/notes", "/home/u/Code/notes", "trunk")))
        assertEquals(listOf("app", "notes"), repos.map { it.name })
        assertEquals(listOf("main*", "fix/login"), repos[0].worktrees.map { it.name + if (it.main) "*" else "" })
        assertEquals(setOf("fix", "sub"), repos[0].worktrees[1].members.map { it.session.sessionId }.toSet())
    }

    @Test fun aNamedBranchKeepsItsHeadingUnderALoneMainCheckout() {
        val named = agentRepos(listOf(placed("main", "/code/app", "/code/app", "/code/app", "main"))).single()
        assertFalse(named.flat)
        assertEquals(listOf("main"), named.worktrees.map { it.name })
        assertTrue("an older node's lone main checkout stays flat", agentRepos(listOf(session("main", "/code/app"))).single().flat)
        val list = buildAgentList(listOf(placed("main", "/code/app", "/code/app", "/code/app", "main")), machines, settings = byRepo)
        assertEquals(listOf("repo/machine-a//code/app", "tree/machine-a//code/app"), list.machines.single().groups)
        assertEquals(RepoLevel.MainCheckout, list.groups.single { it.id == "tree/machine-a//code/app" }.repo)
    }

    @Test fun projectsSortByNameInsideEachComputerInTheHubsOrder() {
        val list = buildAgentList(listOf(placed("a", "/code/zeta", "/code/zeta", "/code/zeta", "main"),
            placed("b", "/code/alpha", "/code/alpha", "/code/alpha", "main", machine = "machine-b"),
            placed("c", "/code/beta", "/code/beta", "/code/beta", "main")), machines, settings = byRepo)
        assertEquals(listOf("machine-b", "machine-a"), list.machines.map { it.machine.id })
        assertEquals(listOf("repo/machine-a//code/beta", "tree/machine-a//code/beta", "repo/machine-a//code/zeta", "tree/machine-a//code/zeta"),
            list.machines[1].groups)
    }

    @Test fun theNodesPlaceWinsOverPathInference() {
        assertEquals(RepoPlace("/home/u/Code/app", "app", "/home/u/orca/workspaces/app/fix", main = false),
            gitPlaceOf("/home/u/Code/app", "/home/u/orca/workspaces/app/fix/"))
        assertNull(gitPlaceOf("/code/app", null))
    }

    // A linked worktree named by its host workspace's title (#4497), as on the web.
    private val appRoot = "/home/me/code/app"
    private val fox = "/home/me/.paseo/worktrees/abc123/brave-fox"
    /** Its group id: the path, under the repository its host files it in, which the path does not name (#4505). */
    private val foxId = "tree/machine-a/$fox\u0000$appRoot"

    /** An agent Paseo placed: the node's git place and branch ([gitTree] off: an older node's), its workspace's title in slot_name, no slot_id. */
    private fun paseo(id: String, tree: String = fox, branch: String? = "fix/login", workspace: String? = "Fix the login flow",
        slotId: String? = null, gitTree: Boolean = true) =
        AgentSession(Session(machineId = "machine-a", sessionId = id, title = id, cwd = tree, state = "working", updatedAt = "2026-01-01T01:00:00Z",
            repoRoot = appRoot, worktree = tree.takeIf { gitTree }, branch = branch, slotName = workspace, slotId = slotId))

    private fun mainCheckout(id: String, workspace: String? = null) = paseo(id, appRoot, "main", workspace)
    private fun linked(rows: List<AgentSession>) = agentRepos(rows).single().worktrees.single { !it.main }

    @Test fun aLinkedWorktreeIsNamedByTheTitleItsSessionsShareAndKeepsItsBranch() {
        val trees = agentRepos(listOf(mainCheckout("main"), paseo("a"), paseo("b"))).single().worktrees
        assertEquals(listOf(Triple("main", "main", true), Triple("Fix the login flow", "fix/login", false)),
            trees.map { Triple(it.name, it.branch, it.main) })
        assertEquals(setOf("a", "b"), trees[1].members.map { it.session.sessionId }.toSet())
    }

    @Test fun aSessionOfAnotherSourceWithoutASlotNameNeitherVotesNorBlocks() {
        val cli = AgentSession(Session(machineId = "machine-a", sessionId = "cli", title = "cli", cwd = fox, state = "working",
            repoRoot = appRoot, worktree = fox, branch = "fix/login"))
        val tree = linked(listOf(paseo("a"), paseo("b"), cli))
        assertEquals(listOf("Fix the login flow", "fix/login", 3), listOf(tree.name, tree.branch, tree.members.size))
    }

    @Test fun twoDifferentTitlesInOneTreeKeepTheBranch() {
        val tree = linked(listOf(paseo("a"), paseo("b", workspace = "Speed up the importer")))
        assertEquals(listOf("fix/login", "fix/login"), listOf(tree.name, tree.branch))
    }

    @Test fun aTabLabelBesideASlotIdKeepsTheBranch() {
        val tree = linked(listOf(paseo("tab", slotId = "w1:t2")))
        assertEquals(listOf("fix/login", "fix/login"), listOf(tree.name, tree.branch))
    }

    @Test fun theMainCheckoutKeepsItsName() {
        assertEquals(listOf("main"), agentRepos(listOf(mainCheckout("main", "Triage"))).single().worktrees.map { it.name })
        val older = agentRepos(listOf(AgentSession(Session(machineId = "machine-a", sessionId = "old", title = "old", cwd = appRoot,
            repoRoot = appRoot, slotName = "Triage")))).single()
        assertEquals("an older node's main checkout is named after its repository", listOf("app"), older.worktrees.map { it.name })
        assertTrue(older.flat)
    }

    @Test fun aTreeWithoutABranchIsNamedByItsTitleElseByItsFolder() {
        assertEquals("Fix the login flow", linked(listOf(paseo("a", branch = null))).name)
        assertEquals("brave-fox", linked(listOf(paseo("a", branch = null, workspace = null))).name)
        assertEquals("brave-fox", linked(listOf(paseo("a", branch = null, workspace = "  "))).name)
        assertEquals("an older node's tree under the repository its host names", "Fix the login flow",
            linked(listOf(paseo("a", branch = null, gitTree = false))).name)
    }

    @Test fun aTreeKeepsItsIdWithAndWithoutTheTitle() {
        val titled = linked(listOf(paseo("a")))
        val plain = linked(listOf(paseo("a", workspace = null)))
        assertEquals(listOf("Fix the login flow", "fix/login"), listOf(titled.name, plain.name))
        assertEquals(foxId, titled.id)
        assertEquals(plain.id, titled.id)
    }

    @Test fun linkedTreesSortByTheNamesTheyShowAfterTheMainCheckout() {
        val trees = agentRepos(listOf(
            paseo("readme", "/home/me/.paseo/worktrees/abc123/wise-owl", "docs/readme", "Update the README"),
            paseo("import", "/home/me/.paseo/worktrees/abc123/calm-otter", "perf/importer", "Speed up the importer"),
            paseo("login"), mainCheckout("main"))).single().worktrees
        assertEquals(listOf("main*", "Fix the login flow", "Speed up the importer", "Update the README"),
            trees.map { it.name + if (it.main) "*" else "" })
    }

    @Test fun theListHeadsANamedTreeWithItsTitleUnderTheSameId() {
        val list = buildAgentList(listOf(mainCheckout("main"), paseo("a")), machines, settings = byRepo)
        assertEquals(listOf("repo/machine-a/$appRoot", "tree/machine-a/$appRoot", foxId), list.machines.single().groups)
        val tree = list.groups.single { it.id == foxId }
        assertEquals("Fix the login flow", tree.title.zhAny())
        assertEquals(RepoLevel.Worktree, tree.repo)
    }

    @Test fun anOrcaWorktreeKeepsOrcasNaming() {
        val orcaTree = "/home/me/orca/workspaces/app/fix-login"
        val orca = AgentSession(Session(machineId = "machine-a", sessionId = "orca", title = "orca", cwd = orcaTree, state = "working",
            repoRoot = appRoot, worktree = orcaTree, branch = "fix/login", spaceKind = "orca", spaceId = "repo-a::$orcaTree",
            slotId = "tab1", slotName = "tab tab1"))
        val tree = linked(listOf(orca, paseo("paseo", orcaTree)))
        assertEquals(listOf("fix/login", "fix/login"), listOf(tree.name, tree.branch))
        assertEquals(setOf("orca", "paseo"), tree.members.map { it.session.sessionId }.toSet())
    }

    @Test fun searchingABranchFindsATreeHeadedByItsTitle() {
        val rows = listOf(mainCheckout("main"), paseo("a"), paseo("b"),
            paseo("import", "/home/me/.paseo/worktrees/abc123/calm-otter", "perf/importer", "Speed up the importer"))
        // The branch is shown nowhere: the tree is headed by its workspace's title.
        assertEquals("Fix the login flow", buildAgentList(rows, machines, settings = byRepo).groups
            .single { it.id == foxId }.title.zhAny())
        val found = buildAgentList(rows, machines, settings = byRepo.copy(query = "FIX/LOGIN"))
        assertEquals(setOf("a", "b"), found.groups.flatMap { it.rows }.map { it.session.sessionId }.toSet())
        assertEquals("Fix the login flow", found.groups.single { it.rows.isNotEmpty() }.title.zhAny())
        assertEquals(listOf("import"), buildAgentList(rows, machines, settings = byRepo.copy(query = "perf/"))
            .groups.flatMap { it.rows }.map { it.session.sessionId })
    }
}
