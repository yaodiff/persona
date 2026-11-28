package persona.feature.agents

import persona.core.designsystem.text
import android.content.ClipboardManager
import android.content.Context
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertTrue
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.AgentWorkspace
import persona.core.designsystem.*
import persona.core.domain.sort.*
import java.io.File

/** "By repository": computer → repository → working tree → sessions, drawn with the Agents components in every style and appearance. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h800dp-xhdpi")
class AgentRepoTreeShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/repo-tree-shots").also { it.mkdirs() }

    private fun at(id: String, title: String, cwd: String, state: String, machine: String = "mac") =
        AgentSession(Session(machineId = machine, sessionId = id, title = title, cwd = cwd, state = state, updatedAt = "2026-01-01T00:00:00Z"))

    private val list = buildAgentList(listOf(
        at("a", "修登录页", "/code/app", "working"), at("b", "看日志", "/code/app", "idle"),
        at("c", "改接口", "/code/app/.worktrees/fix-api", "needs_you"), at("d", "旧分支", "/code/app/.claude/worktrees/old", "idle"),
        at("e", "写周报", "/code/notes", "idle"), at("f", "跑测试", "/srv/api", "unread", machine = "box"),
    ), listOf(Machine("mac", "MacBook", online = true), Machine("box", "Server", online = true, icon = "server")),
        settings = AgentListSettings(grouping = AgentGrouping.Repo))

    @Test fun drawsTheRepositoryTreeInEveryStyleAndAppearance() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                    agentTreeEntries(list, "").forEach { entry ->
                        when (entry) {
                            is TreeMachineEntry -> AgentTreeHeader(entry.machine.machine.displayName, entry.machine.folded, {}, first = entry.first,
                                icon = entry.machine.machine.deviceIcon, online = entry.machine.machine.online,
                                detail = "${entry.machine.repoCount} 个仓库，${entry.machine.sessionCount} 个会话", counts = entry.machine.headingCounts)
                            is TreeGroupEntry -> AgentSpaceHeader(entry.group.title.text(), entry.group.folded, {}, depth = entry.depth, first = entry.first,
                                quiet = entry.quiet, sessions = entry.group.rows.size, counts = entry.group.headingCounts, icon = entry.group.repo?.icon,
                                kind = entry.group.repo?.kind ?: "空间")
                            is TreeRowEntry -> AgentRow(entry.row.session.displayTitle, entry.row.state.designStatus, {}, depth = entry.depth,
                                position = GroupPosition(entry.first, entry.last), time = "刚刚",
                                statusText = entry.row.state.takeIf { it != persona.core.contracts.AgentState.Idle }?.label)
                            is TreeChildrenEntry -> error("Fixture has no Codex subtasks")
                            is TreeIdleEntry -> AgentIdleRow(entry.hidden, {}, depth = entry.depth)
                            is TreeIdleReposEntry -> error("Only Glass's By Repository on the page heads idle repositories")
                            is TreeShowAllEntry -> AgentShowAllRow(entry.count, entry.expanded, {}, depth = entry.depth)
                            is TreeTabEntry -> error("按仓库 lists one agent a row")
                        }
                    }
                }
            }
        }
        for (nextStyle in AppStyle.entries) for (nextDark in listOf(false, true)) {
            rule.runOnIdle { style = nextStyle; dark = nextDark }
            rule.onNodeWithText("notes").assertIsDisplayed()
            rule.onNodeWithText("fix-api").assertIsDisplayed()
            rule.onNodeWithText("2 个仓库，5 个会话").assertIsDisplayed()
            rule.onNodeWithContentDescription("主工作树").assertIsDisplayed()
            rule.onNodeWithContentDescription("展开工作树").assertIsDisplayed()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            val background = bitmap.getPixel(1, 1)
            var drawn = 0
            for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) drawn++
            assertTrue("the tree must draw", drawn > 100)
            File(out(), "repo-tree-${nextStyle.name.lowercase()}-${if (nextDark) "dark" else "light"}.png").outputStream().use {
                assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
            }
        }
    }

    @Test fun drawsHerdrWorkspaceMarksOnTheRepositoryHeading() {
        val machine = Machine("mac", "MacBook", online = true, home = "/Users/me")
        val workspace = AgentWorkspace(
            machineId = machine.id, source = "herdr", sourceId = "notes", title = "notes-fixture",
            cwd = "/Users/me/code/notes-fixture", pinnedAt = "2026-11-16T10:00:00Z", available = false,
        )
        val notes = at("notes", "Notes agent", workspace.cwd, "offline").let {
            it.copy(session = it.session.copy(status = "offline", spaceKind = "herdr", workspaceId = workspace.sourceId))
        }
        val projected = buildAgentList(
            listOf(notes, at("main", "Main checkout agent", "/Users/me/code/repo-fixture", "working"),
                at("removed", "Removed tree agent", "/Users/me/.herdr/worktrees/repo-fixture/removed", "working")),
            listOf(machine), settings = AgentListSettings(grouping = AgentGrouping.Repo, workspaces = listOf(workspace)),
        )
        val heading = projected.groups.single { it.rows.any { row -> row.id == notes.id } }
        assertTrue(heading.repo == RepoLevel.Repository && heading.worktree?.pinned == true && heading.worktree?.offline == true)
        assertTrue(projected.machines.single().repoCount == 2)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = dark, reduceMotion = true) {
                Column(Modifier.testTag("herdr").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                    projected.groups.forEach { group ->
                        val title = group.title.text()
                        val line = group.worktree
                        if (group.repo == RepoLevel.Repository) {
                            val pathAction = workspaceCopyPathAction(group.workspace)
                            var actions by remember(group.id) { mutableStateOf(false) }
                            AgentRepoHeader(title, group.folded, {}, main = line?.main == true,
                                offline = line?.offline == true, pinned = line?.pinned == true,
                                onLongClick = pathAction?.let { { actions = true } })
                            if (actions && pathAction != null) JActionSheet(listOf(pathAction), { actions = false }, title = title)
                        } else {
                            AgentWorktreeLine(title, group.repo == RepoLevel.MainCheckout, group.folded, {}, offline = line?.offline == true)
                        }
                        group.visibleRows.forEach { row ->
                            AgentRow(row.session.displayTitle, row.state.designStatus, {}, depth = 1)
                        }
                    }
                }
            }
        }
        val context = ApplicationProvider.getApplicationContext<Context>()
        val separator = context.getString(persona.core.designsystem.R.string.designsystem_separator)
        val repoLabel = context.getString(persona.core.designsystem.R.string.designsystem_repository)
        val notesHeading = "notes-fixture$separator$repoLabel"
        for (appearance in listOf(false, true)) {
            rule.runOnIdle { dark = appearance }
            rule.onAllNodesWithContentDescription(notesHeading, substring = true).assertCountEquals(1)
            rule.onAllNodesWithContentDescription("repo-fixture$separator$repoLabel", substring = true).assertCountEquals(1)
            rule.onNodeWithText("Removed tree agent").assertIsDisplayed()
            val bitmap = rule.onNodeWithTag("herdr").captureToImage().asAndroidBitmap()
            val suffix = if (appearance) "dark" else "light"
            File(out(), "herdr-repo-tree-$suffix.png").outputStream().use {
                assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
            }
        }
        rule.onNodeWithContentDescription(notesHeading, substring = true).performTouchInput { longClick() }
        rule.onNodeWithText(context.getString(R.string.agents_copy_workspace_path)).performClick()
        val copied = rule.runOnIdle {
            context.getSystemService(ClipboardManager::class.java)?.primaryClip?.getItemAt(0)?.text?.toString()
        }
        assertEquals(workspace.cwd, copied)
    }

    @Test fun drawsOfflineHistoryUnderItsComputerAndHidesItWithComputerFold() {
        val computers = listOf(Machine("mac", "MacBook", online = true), Machine("box", "Server", online = true))
        val rows = listOf(
            at("running", "正在构建", "/tmp/example-project", "working"),
            at("other", "检查服务", "/srv/api", "working", machine = "box"),
            AgentSession(Session(machineId = "mac", sessionId = "old", title = "旧会话", cwd = "/tmp/example-project",
                state = "offline", status = "offline", updatedAt = "2026-01-02T00:00:00Z")),
            AgentSession(Session(machineId = "box", sessionId = "old", title = "另一台旧会话", cwd = "/tmp/example-project",
                state = "offline", status = "offline", updatedAt = "2026-01-01T00:00:00Z")),
        )
        var dark by mutableStateOf(false)
        var folded by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = dark, reduceMotion = true) {
                val settings = AgentListSettings(grouping = AgentGrouping.Repo,
                    folded = if (folded) setOf("machine/mac", "machine/box") else emptySet())
                val entries = agentTreeEntries(scopedAgentList(rows, computers,
                    AgentsPreferencesSnapshot(range = AgentRange.All), settings), "")
                Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                    entries.forEach { entry ->
                        when (entry) {
                            is TreeMachineEntry -> AgentTreeHeader(entry.machine.machine.displayName, entry.machine.folded, {}, first = entry.first,
                                icon = entry.machine.machine.deviceIcon, online = entry.machine.machine.online,
                                detail = "${entry.machine.repoCount} 个仓库，${entry.machine.sessionCount} 个会话", counts = entry.machine.headingCounts)
                            is TreeGroupEntry -> AgentSpaceHeader(entry.group.title.text(), entry.group.folded, {}, depth = entry.depth, first = entry.first,
                                quiet = entry.quiet, sessions = entry.group.rows.size, counts = entry.group.headingCounts, icon = entry.group.repo?.icon,
                                kind = entry.group.repo?.kind ?: "空间")
                            is TreeRowEntry -> AgentRow(entry.row.session.displayTitle, entry.row.state.designStatus, {}, depth = entry.depth,
                                position = GroupPosition(entry.first, entry.last), time = "刚刚",
                                statusText = entry.row.state.takeIf { it != persona.core.contracts.AgentState.Idle }?.label)
                            is TreeChildrenEntry -> error("Fixture has no Codex subtasks")
                            is TreeIdleEntry -> AgentIdleRow(entry.hidden, {}, depth = entry.depth)
                            is TreeIdleReposEntry -> error("Only Glass's By Repository on the page heads idle repositories")
                            is TreeShowAllEntry -> AgentShowAllRow(entry.count, entry.expanded, {}, depth = entry.depth)
                            is TreeTabEntry -> error("按仓库 lists one agent a row")
                        }
                    }
                }
            }
        }
        for (nextDark in listOf(false, true)) {
            rule.runOnIdle { dark = nextDark; folded = false }
            rule.onAllNodesWithText("example-project").assertCountEquals(1)
            rule.onAllNodesWithText("/tmp/example-project · 未运行").assertCountEquals(2)
            rule.onNodeWithText("MacBook").assertIsDisplayed()
            rule.onNodeWithText("Server").assertIsDisplayed()
            val expanded = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "repo-history-${if (nextDark) "dark" else "light"}.png").outputStream().use {
                assertTrue(expanded.compress(Bitmap.CompressFormat.PNG, 100, it))
            }
            rule.runOnIdle { folded = true }
            rule.onAllNodesWithText("example-project").assertCountEquals(0)
            rule.onAllNodesWithText("/tmp/example-project · 未运行").assertCountEquals(0)
            val collapsed = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "repo-history-folded-${if (nextDark) "dark" else "light"}.png").outputStream().use {
                assertTrue(collapsed.compress(Bitmap.CompressFormat.PNG, 100, it))
            }
        }
    }
}
